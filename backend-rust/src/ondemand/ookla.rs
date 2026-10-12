//! Running the Ookla CLI and turning its output into live progress. Ports backend-go/internal/ondemand/ookla.go.

use std::{
    process::Stdio,
    time::{Duration, Instant},
};

use async_trait::async_trait;
use serde::{Deserialize, Serialize};
use tokio::{
    io::{AsyncBufReadExt, AsyncReadExt, BufReader},
    process::Command,
};
use tokio_util::sync::CancellationToken;

/// A live reading during a test (docs/openapi.yaml: ProgressEvent).
#[derive(Debug, Clone, PartialEq, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Progress {
    pub r#type: &'static str,
    /// ping | download | upload
    pub phase: &'static str,
    pub elapsed_ms: i64,
    pub fraction: Option<f64>,
    pub current_mbps: Option<f64>,
    pub latency_ms: Option<f64>,
    pub jitter_ms: Option<f64>,
}

#[derive(Debug, PartialEq)]
pub enum RunError {
    /// The caller stopped it (a cancel request, the time limit, or the server shutting down).
    Cancelled,
    Failed(String),
}

impl std::fmt::Display for RunError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        match self {
            RunError::Cancelled => f.write_str("cancelled"),
            RunError::Failed(m) => f.write_str(m),
        }
    }
}

/// Runs one speed test and reports progress as it goes. It returns the provider's final result.
#[async_trait]
pub trait Runner: Send + Sync {
    async fn run(&self, cancel: CancellationToken, emit: &(dyn Fn(Progress) + Send + Sync)) -> Result<Vec<u8>, RunError>;
}

/// The part of the CLI's `--format=jsonl` output that matters here.
#[derive(Deserialize)]
struct Line {
    r#type: Option<String>,
    ping: Option<Ping>,
    download: Option<Transfer>,
    upload: Option<Transfer>,
}

#[derive(Deserialize)]
struct Ping {
    jitter: Option<f64>,
    latency: Option<f64>,
    progress: Option<f64>,
}

#[derive(Deserialize)]
struct Transfer {
    bandwidth: Option<f64>, // bytes per second
    progress: Option<f64>,
    latency: Option<UnderLoad>,
}

#[derive(Deserialize)]
struct UnderLoad {
    iqm: Option<f64>,
    jitter: Option<f64>,
}

/// One line of CLI output as a progress reading, or recognised as the final result (`Ok(None, true)`: the caller keeps the
/// whole line). Lines of other types (testStart) give neither.
pub fn parse_line(line: &[u8], elapsed: Duration) -> Result<(Option<Progress>, bool), String> {
    let l: Line = serde_json::from_slice(line).map_err(|e| format!("unreadable output from the speed test: {e}"))?;
    let ms = elapsed.as_millis() as i64;
    Ok(match l.r#type.as_deref().unwrap_or("") {
        "result" => (None, true),
        "ping" => (
            l.ping.map(|p| Progress {
                r#type: "progress",
                phase: "ping",
                elapsed_ms: ms,
                fraction: p.progress,
                current_mbps: None,
                latency_ms: p.latency,
                jitter_ms: p.jitter,
            }),
            false,
        ),
        kind @ ("download" | "upload") => {
            let (phase, t) = if kind == "download" { ("download", l.download) } else { ("upload", l.upload) };
            (
                t.map(|t| Progress {
                    r#type: "progress",
                    phase,
                    elapsed_ms: ms,
                    fraction: t.progress,
                    current_mbps: t.bandwidth.map(|b| b / 125_000.0), // bytes/second -> megabits/second
                    // latency under load, when the CLI reports it
                    latency_ms: t.latency.as_ref().and_then(|u| u.iqm),
                    jitter_ms: t.latency.as_ref().and_then(|u| u.jitter),
                }),
                false,
            )
        }
        _ => (None, false),
    })
}

/// Runs the Ookla CLI. It is only constructed once the licence has been accepted, and passes the acceptance flags for exactly
/// that reason.
pub struct Ookla {
    pub path: String,
}

#[async_trait]
impl Runner for Ookla {
    async fn run(&self, cancel: CancellationToken, emit: &(dyn Fn(Progress) + Send + Sync)) -> Result<Vec<u8>, RunError> {
        let mut child = Command::new(&self.path)
            .args(["--format=jsonl", "--accept-license", "--accept-gdpr"])
            .stdin(Stdio::null())
            .stdout(Stdio::piped())
            .stderr(Stdio::piped())
            .kill_on_drop(true)
            .spawn()
            .map_err(|e| RunError::Failed(format!("could not start the Ookla speed test: {e}")))?;
        let start = Instant::now();
        let stdout = child.stdout.take().ok_or_else(|| RunError::Failed("could not read the speed test's output".into()))?;
        let mut stderr = child.stderr.take();
        // Keep the first 4 KiB of what it says on stderr, and drain the rest so it can never block on a full pipe.
        let stderr_task = tokio::spawn(async move {
            let mut kept = Vec::new();
            if let Some(s) = stderr.as_mut() {
                let mut buf = [0u8; 1024];
                while let Ok(n) = s.read(&mut buf).await {
                    if n == 0 {
                        break;
                    }
                    let room = 4096usize.saturating_sub(kept.len());
                    kept.extend_from_slice(&buf[..n.min(room)]);
                }
            }
            String::from_utf8_lossy(&kept).into_owned()
        });

        let mut lines = BufReader::new(stdout).lines();
        let mut result: Option<Vec<u8>> = None;
        let mut parse_err: Option<String> = None;
        loop {
            tokio::select! {
                biased;
                _ = cancel.cancelled() => {
                    stop(&mut child).await;
                    return Err(RunError::Cancelled);
                }
                line = lines.next_line() => match line {
                    Ok(Some(line)) => {
                        let line = line.trim();
                        if line.is_empty() { continue }
                        match parse_line(line.as_bytes(), start.elapsed()) {
                            Err(e) => parse_err = Some(e),
                            Ok((_, true)) => result = Some(line.as_bytes().to_vec()),
                            Ok((Some(p), false)) => emit(p),
                            Ok((None, false)) => {}
                        }
                    }
                    Ok(None) | Err(_) => break,
                },
            }
        }
        let status = child.wait().await;
        let stderr_text = stderr_task.await.unwrap_or_default();
        if cancel.is_cancelled() {
            return Err(RunError::Cancelled);
        }
        match status {
            Ok(s) if s.success() => {}
            Ok(s) => return Err(RunError::Failed(format!("the Ookla speed test failed: {}", explain(&stderr_text, &exit_text(&s))))),
            Err(e) => return Err(RunError::Failed(format!("the Ookla speed test failed: {}", explain(&stderr_text, &e.to_string())))),
        }
        result.ok_or_else(|| RunError::Failed(parse_err.unwrap_or_else(|| "the Ookla speed test finished without a result".into())))
    }
}

fn exit_text(s: &std::process::ExitStatus) -> String {
    s.code().map_or_else(|| s.to_string(), |c| format!("exit status {c}"))
}

/// Asks the process to stop (interrupt, so it can clean up), and kills it if it has not by then.
async fn stop(child: &mut tokio::process::Child) {
    if let Some(pid) = child.id() {
        // SAFETY: kill(2) with a pid we own and a valid signal number; it has no memory-safety preconditions.
        unsafe { libc::kill(pid as libc::pid_t, libc::SIGINT) };
    }
    if tokio::time::timeout(Duration::from_secs(5), child.wait()).await.is_err() {
        let _ = child.kill().await;
    }
}

/// The useful last line of the CLI's error output, without paths or noise.
fn explain(stderr: &str, fallback: &str) -> String {
    let last = stderr.trim().lines().next_back().unwrap_or("").trim();
    if last.is_empty() {
        return fallback.to_string();
    }
    last.chars().take(200).collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::{os::unix::fs::PermissionsExt, sync::Mutex};

    const T: Duration = Duration::from_millis(1500);

    #[test]
    fn progress_from_each_kind_of_line() {
        let ping = parse_line(br#"{"type":"ping","timestamp":"x","ping":{"jitter":0.5,"latency":12.5,"progress":0.4}}"#, T).unwrap();
        assert_eq!(
            ping,
            (
                Some(Progress {
                    r#type: "progress",
                    phase: "ping",
                    elapsed_ms: 1500,
                    fraction: Some(0.4),
                    current_mbps: None,
                    latency_ms: Some(12.5),
                    jitter_ms: Some(0.5)
                }),
                false
            )
        );
        let down =
            parse_line(br#"{"type":"download","download":{"bandwidth":12500000,"progress":0.5,"latency":{"iqm":30.5,"jitter":2.5}}}"#, T)
                .unwrap();
        let d = down.0.unwrap();
        assert_eq!(
            (d.phase, d.current_mbps, d.fraction, d.latency_ms, d.jitter_ms),
            ("download", Some(100.0), Some(0.5), Some(30.5), Some(2.5))
        );
        let up = parse_line(br#"{"type":"upload","upload":{"bandwidth":625000}}"#, T).unwrap().0.unwrap();
        assert_eq!((up.phase, up.current_mbps, up.fraction, up.latency_ms), ("upload", Some(5.0), None, None));
    }

    #[test]
    fn the_result_line_and_the_lines_to_ignore() {
        assert_eq!(parse_line(br#"{"type":"result","timestamp":"x"}"#, T).unwrap(), (None, true));
        assert_eq!(parse_line(br#"{"type":"testStart","server":{}}"#, T).unwrap(), (None, false));
        assert_eq!(parse_line(br#"{"type":"download"}"#, T).unwrap(), (None, false), "no data in it, nothing to report");
        assert_eq!(parse_line(br#"{"type":"ping","ping":null}"#, T).unwrap(), (None, false));
        assert_eq!(parse_line(b"{}", T).unwrap(), (None, false));
        for bad in ["not json", "[1]", "", "{\"type\":5}"] {
            assert!(parse_line(bad.as_bytes(), T).unwrap_err().starts_with("unreadable output from the speed test"), "{bad}");
        }
    }

    #[test]
    fn progress_serializes_as_the_contract_says() {
        let p = Progress {
            r#type: "progress",
            phase: "download",
            elapsed_ms: 2000,
            fraction: None,
            current_mbps: Some(94.5),
            latency_ms: None,
            jitter_ms: None,
        };
        assert_eq!(
            serde_json::to_value(p).unwrap(),
            serde_json::json!({"type":"progress","phase":"download","elapsedMs":2000,"fraction":null,"currentMbps":94.5,"latencyMs":null,"jitterMs":null})
        );
    }

    #[test]
    fn error_text_is_the_last_line_and_short() {
        assert_eq!(explain("noise\n[error] Cannot open socket\n", "exit status 1"), "[error] Cannot open socket");
        assert_eq!(explain("  \n", "exit status 1"), "exit status 1");
        assert_eq!(explain(&"x".repeat(500), "e").len(), 200);
    }

    /// Writes an executable shell script and returns its path (kept in a directory that lives as long as the test process).
    fn script(name: &str, body: &str) -> String {
        let dir = std::env::temp_dir().join(format!("speedtest-ookla-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join(name);
        std::fs::write(&path, format!("#!/bin/sh\n{body}\n")).unwrap();
        std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o755)).unwrap();
        path.to_string_lossy().into_owned()
    }

    async fn run(path: &str, cancel: CancellationToken) -> (Result<Vec<u8>, RunError>, Vec<Progress>) {
        let seen = Mutex::new(Vec::new());
        let r = Ookla { path: path.into() }.run(cancel, &|p| seen.lock().unwrap().push(p)).await;
        (r, seen.into_inner().unwrap())
    }

    #[tokio::test]
    async fn a_run_reports_progress_and_returns_the_result_line() {
        let p = script(
            "ok.sh",
            r#"echo '{"type":"testStart"}'
echo '{"type":"ping","ping":{"latency":10,"jitter":1,"progress":1}}'
echo '{"type":"download","download":{"bandwidth":12500000,"progress":0.5}}'
echo '{"type":"upload","upload":{"bandwidth":1250000,"progress":1}}'
echo '{"type":"result","download":{"bandwidth":1}}'"#,
        );
        let (r, seen) = run(&p, CancellationToken::new()).await;
        assert_eq!(r.unwrap(), br#"{"type":"result","download":{"bandwidth":1}}"#);
        assert_eq!(seen.iter().map(|p| p.phase).collect::<Vec<_>>(), ["ping", "download", "upload"]);
        assert_eq!(seen[1].current_mbps, Some(100.0));
    }

    #[tokio::test]
    async fn the_tool_gets_the_flags_that_record_the_acceptance() {
        let p = script("args.sh", r#"echo "{\"type\":\"result\",\"args\":\"$*\"}""#);
        let (r, _) = run(&p, CancellationToken::new()).await;
        let out: serde_json::Value = serde_json::from_slice(&r.unwrap()).unwrap();
        assert_eq!(out["args"], "--format=jsonl --accept-license --accept-gdpr");
    }

    #[tokio::test]
    async fn failures_say_why() {
        let (r, _) =
            run(&script("fail.sh", "echo 'warming up' >&2\necho '[error] Cannot open socket' >&2\nexit 1"), CancellationToken::new()).await;
        assert_eq!(r.unwrap_err(), RunError::Failed("the Ookla speed test failed: [error] Cannot open socket".into()));
        let (r, _) = run(&script("silent.sh", "exit 3"), CancellationToken::new()).await;
        assert_eq!(r.unwrap_err(), RunError::Failed("the Ookla speed test failed: exit status 3".into()));
        let (r, _) = run(&script("none.sh", "echo '{\"type\":\"ping\",\"ping\":{\"latency\":1}}'"), CancellationToken::new()).await;
        assert_eq!(r.unwrap_err(), RunError::Failed("the Ookla speed test finished without a result".into()));
        let (r, _) = run(&script("junk.sh", "echo 'hello'"), CancellationToken::new()).await;
        assert!(matches!(r.unwrap_err(), RunError::Failed(m) if m.starts_with("unreadable output")));
        let (r, _) = run("/no/such/speedtest", CancellationToken::new()).await;
        assert!(matches!(r.unwrap_err(), RunError::Failed(m) if m.starts_with("could not start the Ookla speed test")));
    }

    #[tokio::test]
    async fn cancelling_interrupts_the_tool_and_returns_quickly() {
        // a tool that ignores nothing: sleeping in the foreground, it dies on SIGINT
        let p = script("slow.sh", "echo '{\"type\":\"ping\",\"ping\":{\"latency\":1}}'\nexec sleep 30");
        let cancel = CancellationToken::new();
        let c2 = cancel.clone();
        tokio::spawn(async move {
            tokio::time::sleep(Duration::from_millis(200)).await;
            c2.cancel();
        });
        let started = Instant::now();
        let (r, seen) = run(&p, cancel).await;
        assert_eq!(r.unwrap_err(), RunError::Cancelled);
        assert!(started.elapsed() < Duration::from_secs(3), "took {:?}", started.elapsed());
        assert_eq!(seen.len(), 1);
    }

    #[tokio::test]
    async fn a_tool_that_ignores_the_interrupt_is_killed_after_a_grace_period() {
        let p = script("stubborn.sh", "trap '' INT\necho '{\"type\":\"ping\",\"ping\":{\"latency\":1}}'\nwhile true; do sleep 0.1; done");
        let cancel = CancellationToken::new();
        let c2 = cancel.clone();
        tokio::spawn(async move {
            tokio::time::sleep(Duration::from_millis(200)).await;
            c2.cancel();
        });
        let started = Instant::now();
        let (r, _) = run(&p, cancel).await;
        assert_eq!(r.unwrap_err(), RunError::Cancelled);
        let took = started.elapsed();
        assert!(took >= Duration::from_secs(4) && took < Duration::from_secs(8), "took {took:?}");
    }
}
