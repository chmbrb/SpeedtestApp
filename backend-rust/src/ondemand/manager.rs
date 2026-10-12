//! Starts, follows, cancels and cleans up tests. Ports backend-go/internal/ondemand/manager.go.

use std::{
    collections::HashMap,
    sync::{Arc, Mutex},
    time::Duration,
};

use base64::{Engine, engine::general_purpose::URL_SAFE_NO_PAD};
use serde::Serialize;
use sha2::{Digest, Sha256};
use tokio::sync::watch;
use tokio_util::sync::CancellationToken;
use uuid::Uuid;

use super::{
    config::{Config, executable_exists},
    hub::{Hub, Subscription},
    ookla::{Ookla, RunError, Runner},
};
use crate::{
    auth::Limiter,
    model::{Capabilities, Capability, Test, TestResult},
    store::Store,
    store_tests::{CreateError, NewTest, SubmitError, TestRow},
};

/// Why `create` refused, for the API to turn into a response.
#[derive(Debug)]
pub enum CreateTestError {
    Validation {
        field: &'static str,
        message: String,
    },
    Unavailable(String),
    RateLimited(u64),
    /// A server test is already queued or running.
    ServerBusy,
    Db(sqlx::Error),
}

/// What a caller asks for. `token_device` is set when the caller authenticated with an agent token (the token decides the
/// device); `client_ip` is the address the request came from ("" if unknown).
#[derive(Debug, Default, Clone)]
pub struct CreateRequest {
    pub provider: String,
    pub mode: String,
    pub connection_type: String,
    pub device: String,
    pub token_device: String,
    pub client_ip: String,
}

// Events as sent over SSE (docs/openapi.yaml: TestEvent).
#[derive(Serialize)]
pub struct StateEvent<'a> {
    pub r#type: &'static str,
    pub test: &'a Test,
}
#[derive(Serialize)]
pub struct ResultEvent<'a> {
    pub r#type: &'static str,
    pub result: &'a TestResult,
}
#[derive(Serialize)]
pub struct ErrorEvent<'a> {
    pub r#type: &'static str,
    pub message: &'a str,
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum Cause {
    Requested,
    TimedOut,
}

struct Running {
    token: CancellationToken,
    cause: Arc<Mutex<Option<Cause>>>,
    done: watch::Receiver<bool>,
}

pub struct Manager {
    cfg: Config,
    store: Store,
    runner: Arc<dyn Runner>,
    hub: Hub,
    limiter: Limiter,
    look_path: Box<dyn Fn(&str) -> bool + Send + Sync>,
    root: Mutex<CancellationToken>,
    running: Mutex<HashMap<String, Running>>,
}

fn lock<T>(m: &Mutex<T>) -> std::sync::MutexGuard<'_, T> {
    m.lock().unwrap_or_else(|e| e.into_inner())
}

/// Go's `time.Duration` text ("3m0s", "300ms", "1.5s"), which the failure message has always used.
pub fn go_duration(d: Duration) -> String {
    if d < Duration::from_secs(1) {
        return if d.as_micros() % 1000 == 0 { format!("{}ms", d.as_millis()) } else { format!("{}µs", d.as_micros()) };
    }
    let secs = d.as_secs();
    let frac = d.subsec_nanos();
    let sec_text =
        if frac == 0 { format!("{}", secs % 60) } else { format!("{}.{}", secs % 60, format!("{frac:09}").trim_end_matches('0')) };
    match secs {
        0..=59 => format!("{sec_text}s"),
        60..=3599 => format!("{}m{sec_text}s", secs / 60),
        _ => format!("{}h{}m{sec_text}s", secs / 3600, (secs % 3600) / 60),
    }
}

fn clean_name(s: &str) -> bool {
    !s.is_empty() && s.len() <= 64 && !s.chars().any(char::is_control)
}

fn hash_token(token: &str) -> Vec<u8> {
    Sha256::digest(token.as_bytes()).to_vec()
}

pub fn test_model(r: TestRow) -> Test {
    Test {
        id: r.id,
        provider: r.provider,
        mode: r.mode,
        device: r.device,
        connection_type: r.connection_type,
        state: r.state,
        created_at: r.created_at,
        started_at: r.started_at,
        finished_at: r.finished_at,
        result_id: r.result_id,
        error: r.error,
        queue_position: None,
        submit_token: String::new(),
    }
}

impl Manager {
    /// `runner` may be None, in which case the real Ookla CLI is used.
    pub fn new(cfg: Config, store: Store, runner: Option<Arc<dyn Runner>>) -> Arc<Self> {
        let runner = runner.unwrap_or_else(|| Arc::new(Ookla { path: cfg.ookla_path.clone() }));
        let per_minute = cfg.create_per_minute;
        Arc::new(Self {
            limiter: Limiter::new(per_minute.max(4), per_minute),
            cfg,
            store,
            runner,
            hub: Hub::new(),
            look_path: Box::new(executable_exists),
            root: Mutex::new(CancellationToken::new()),
            running: Mutex::new(HashMap::new()),
        })
    }

    /// Ties the manager to `root` (tests stop when it is cancelled) and fails tests a previous process left running, so they
    /// cannot block new ones. Call it before serving requests.
    pub async fn start(&self, root: CancellationToken) {
        *lock(&self.root) = root;
        match self.store.interrupt_server_tests().await {
            Ok(0) => {}
            Ok(n) => tracing::warn!(count = n, "marked tests left over from a restart as failed"),
            Err(e) => tracing::error!(err = %e, "could not clear tests left over from a restart"),
        }
    }

    /// Tidies up once a minute until `root` ends: stale tests, old event streams, idle rate-limit entries.
    pub async fn run(self: Arc<Self>, root: CancellationToken) {
        let mut tick = tokio::time::interval(Duration::from_secs(60));
        tick.tick().await; // the first tick is immediate
        loop {
            tokio::select! {
                _ = root.cancelled() => return,
                _ = tick.tick() => {
                    if let Err(e) = self.store.sweep_tests(self.cfg.browser_ttl, self.cfg.test_timeout + Duration::from_secs(120)).await {
                        if !root.is_cancelled() {
                            tracing::error!(err = %e, "could not sweep stale tests");
                        }
                    }
                    self.hub.forget();
                    self.limiter.forget_idle(Duration::from_secs(600));
                }
            }
        }
    }

    /// Waits (up to `max`) until no server test is running, so a shutdown can record how each one ended.
    pub async fn wait_idle(&self, max: Duration) {
        let deadline = tokio::time::Instant::now() + max;
        while !lock(&self.running).is_empty() && tokio::time::Instant::now() < deadline {
            tokio::time::sleep(Duration::from_millis(50)).await;
        }
    }

    pub fn config(&self) -> &Config {
        &self.cfg
    }

    pub fn capabilities(&self) -> Capabilities {
        let status = self.cfg.ookla_status(&*self.look_path);
        Capabilities {
            device_name: self.cfg.device_name.clone(),
            server_connection_type: self.cfg.server_connection.clone(),
            ookla: Capability { available: status.is_ok(), reason: status.err() },
            ndt7: Capability { available: true, reason: None },
        }
    }

    /// Starts a test. A server test returns at once and runs in the background; a browser test's response carries the
    /// single-use token (shown only here).
    pub async fn create(self: &Arc<Self>, req: CreateRequest) -> Result<Test, CreateTestError> {
        let invalid = |field, message: &str| CreateTestError::Validation { field, message: message.to_string() };
        if req.provider != "ookla" && req.provider != "ndt7" {
            return Err(invalid("provider", "must be ookla or ndt7"));
        }
        if req.mode != "server" && req.mode != "browser" {
            return Err(invalid("mode", "must be server or browser"));
        }
        if req.mode == "server" && req.provider != "ookla" {
            return Err(invalid("provider", "NDT7 runs in the browser; use mode browser"));
        }
        if req.mode == "browser" && req.provider != "ndt7" {
            return Err(invalid("provider", "Ookla tests can only run on the server; use mode server"));
        }
        // The contract requires both fields in every case, even where a value is ignored (a server test always uses the
        // server's own device name and connection type): a request that breaks the contract is refused, not half-honoured.
        if req.connection_type != "wired" && req.connection_type != "wireless" {
            return Err(invalid("connectionType", "must be wired or wireless"));
        }
        if !req.device.is_empty() && !clean_name(&req.device) {
            return Err(invalid("device", "must be a name of up to 64 characters, without control characters"));
        }
        let device = if req.token_device.is_empty() { req.device.clone() } else { req.token_device.clone() };
        if req.mode == "browser" && !clean_name(&device) {
            return Err(invalid("device", "is required: a name of up to 64 characters, without control characters"));
        }
        if let Err(retry) = self.limiter.allow(&req.client_ip) {
            return Err(CreateTestError::RateLimited(retry));
        }
        let client_ip = (!req.client_ip.is_empty()).then(|| req.client_ip.clone());

        if req.mode == "server" {
            if let Err(why) = self.cfg.ookla_status(&*self.look_path) {
                return Err(CreateTestError::Unavailable(why));
            }
            let row = self
                .store
                .create_test(NewTest {
                    provider: "ookla".into(),
                    mode: "server".into(),
                    device: self.cfg.device_name.clone(),
                    connection_type: self.cfg.server_connection.clone(),
                    state: "queued".into(),
                    client_ip,
                    ..Default::default()
                })
                .await
                .map_err(|e| match e {
                    CreateError::ServerBusy => CreateTestError::ServerBusy,
                    CreateError::Db(e) => CreateTestError::Db(e),
                })?;
            let test = test_model(row.clone());
            self.hub.publish(&row.id, "state", &StateEvent { r#type: "state", test: &test });
            tokio::spawn(Arc::clone(self).run_server(row));
            return Ok(test);
        }

        let (token, hash) = new_token().map_err(|e| CreateTestError::Db(sqlx::Error::Protocol(format!("no randomness: {e}"))))?;
        let row = self
            .store
            .create_test(NewTest {
                provider: "ndt7".into(),
                mode: "browser".into(),
                device,
                connection_type: req.connection_type.clone(),
                state: "running".into(),
                token_hash: Some(hash),
                client_ip,
            })
            .await
            .map_err(|e| match e {
                CreateError::ServerBusy => CreateTestError::ServerBusy,
                CreateError::Db(e) => CreateTestError::Db(e),
            })?;
        let mut test = test_model(row);
        test.submit_token = token;
        Ok(test)
    }

    async fn run_server(self: Arc<Self>, row: TestRow) {
        let id = row.id.clone();
        let token = lock(&self.root).child_token();
        let cause = Arc::new(Mutex::new(None));
        let (done_tx, done_rx) = watch::channel(false);
        lock(&self.running).insert(id.clone(), Running { token: token.clone(), cause: cause.clone(), done: done_rx });

        self.run_server_inner(&id, token, cause).await;

        lock(&self.running).remove(&id);
        let _ = done_tx.send(true);
    }

    async fn run_server_inner(&self, id: &str, token: CancellationToken, cause: Arc<Mutex<Option<Cause>>>) {
        if let Err(e) = tokio::time::timeout(Duration::from_secs(15), self.store.mark_test_running(id))
            .await
            .unwrap_or_else(|_| Err(sqlx::Error::PoolTimedOut))
        {
            self.finish(id, "failed", None, &format!("The test could not be started: {e}")).await;
            return;
        }
        if let Ok(Some(cur)) = self.store.get_test(id).await {
            self.hub.publish(id, "state", &StateEvent { r#type: "state", test: &test_model(cur) });
        }

        // The time limit is a cancel with its own cause.
        let limit = {
            let (token, cause, timeout) = (token.clone(), cause.clone(), self.cfg.test_timeout);
            tokio::spawn(async move {
                tokio::time::sleep(timeout).await;
                lock(&cause).get_or_insert(Cause::TimedOut);
                token.cancel();
            })
        };
        let hub = &self.hub;
        let emit = |p| hub.publish(id, "progress", &p);
        let outcome = self.runner.run(token.clone(), &emit).await;
        limit.abort();

        if token.is_cancelled() {
            // cancelled, timed out, or the server is shutting down
            let why = *lock(&cause);
            match why {
                Some(Cause::Requested) => self.finish(id, "cancelled", None, "").await,
                Some(Cause::TimedOut) => {
                    self.finish(
                        id,
                        "failed",
                        None,
                        &format!("The test took longer than {} and was stopped.", go_duration(self.cfg.test_timeout)),
                    )
                    .await
                }
                None => self.finish(id, "failed", None, "The server was shutting down.").await,
            }
            return;
        }
        let raw = match outcome {
            Ok(raw) => raw,
            Err(RunError::Failed(m)) => return self.finish(id, "failed", None, &m).await,
            Err(RunError::Cancelled) => return self.finish(id, "failed", None, "The test was stopped.").await,
        };
        // saving needs its own time limit: the test may have used up any earlier one
        let raw = String::from_utf8_lossy(&raw).into_owned();
        let saved = tokio::time::timeout(
            Duration::from_secs(15),
            self.store.insert_ookla(&raw, &self.cfg.device_name, &self.cfg.server_connection, "on_demand"),
        )
        .await;
        match saved {
            Ok(Ok(result_id)) => self.finish(id, "completed", Some(&result_id), "").await,
            other => {
                tracing::error!(err = ?other, "could not store an on-demand result");
                self.finish(id, "failed", None, "The test finished but its result could not be saved.").await;
            }
        }
    }

    /// Records the end of a test, tells listeners, and closes the stream.
    async fn finish(&self, id: &str, state: &str, result_id: Option<&str>, err_msg: &str) {
        let error = (!err_msg.is_empty()).then_some(err_msg);
        let finished = tokio::time::timeout(Duration::from_secs(15), self.store.finish_test(id, state, result_id, error)).await;
        let (row, changed) = match finished {
            Ok(Ok(Some(x))) => x,
            other => {
                tracing::error!(id, err = ?other, "could not record the end of a test");
                return;
            }
        };
        if changed {
            self.hub.publish(id, "state", &StateEvent { r#type: "state", test: &test_model(row) });
            match state {
                "completed" => {
                    if let Some(rid) = result_id.and_then(|r| Uuid::parse_str(r).ok()) {
                        if let Ok(Some(res)) = self.store.get_result(rid).await {
                            self.hub.publish(id, "result", &ResultEvent { r#type: "result", result: &res.result });
                        }
                    }
                }
                "failed" => self.hub.publish(id, "error", &ErrorEvent { r#type: "error", message: err_msg }),
                _ => {}
            }
        }
        self.hub.close(id);
    }

    /// Stops a test and returns its final state. Cancelling a test that has already ended just reports it.
    pub async fn cancel(&self, id: &str) -> Result<Option<Test>, sqlx::Error> {
        let Some(row) = self.store.get_test(id).await? else { return Ok(None) };
        if row.terminal() {
            return Ok(Some(test_model(row)));
        }
        let local = lock(&self.running).get(id).map(|r| (r.token.clone(), r.cause.clone(), r.done.clone()));
        match local {
            Some((token, cause, mut done)) => {
                lock(&cause).get_or_insert(Cause::Requested);
                token.cancel();
                // give the runner a moment to wind down, so the answer is the final state
                let _ = tokio::time::timeout(Duration::from_secs(6), done.wait_for(|d| *d)).await;
            }
            // a browser test, or a server test this process is not running
            None => self.finish(id, "cancelled", None, "").await,
        }
        Ok(self.store.get_test(id).await?.map(test_model))
    }

    pub async fn get(&self, id: &str) -> Result<Option<Test>, sqlx::Error> {
        Ok(self.store.get_test(id).await?.map(test_model))
    }

    pub async fn list(&self, states: &[String], limit: i64) -> Result<Vec<Test>, sqlx::Error> {
        Ok(self.store.list_tests(states, limit).await?.into_iter().map(test_model).collect())
    }

    /// The stored result a completed test produced.
    pub async fn result(&self, id: &str) -> Result<Option<TestResult>, sqlx::Error> {
        let Ok(uuid) = Uuid::parse_str(id) else { return Ok(None) };
        Ok(self.store.get_result(uuid).await?.map(|d| d.result))
    }

    pub fn subscribe(&self, id: &str, last_id: i64) -> Subscription {
        self.hub.subscribe(id, last_id)
    }

    /// Stores a browser test's result. `token` is the single-use token from `create`; `summary` is the validated NDT7 summary.
    pub async fn submit(&self, id: &str, token: &str, summary: &str, client_ip: Option<&str>) -> Result<TestResult, SubmitError> {
        let result_id = self.store.submit_browser_result(id, &hash_token(token), self.cfg.browser_ttl, summary, client_ip).await?;
        self.hub.close(id);
        self.result(&result_id).await?.ok_or(SubmitError::NotFound)
    }
}

fn new_token() -> Result<(String, Vec<u8>), getrandom::Error> {
    let mut b = [0u8; 32];
    getrandom::fill(&mut b)?;
    let token = URL_SAFE_NO_PAD.encode(b);
    let hash = hash_token(&token);
    Ok((token, hash))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn durations_read_like_gos() {
        for (secs, nanos, want) in [
            (180, 0, "3m0s"),
            (0, 300_000_000, "300ms"),
            (90, 0, "1m30s"),
            (3600, 0, "1h0m0s"),
            (45, 0, "45s"),
            (1, 500_000_000, "1.5s"),
            (3725, 0, "1h2m5s"),
        ] {
            assert_eq!(go_duration(Duration::new(secs, nanos)), want);
        }
    }

    #[test]
    fn names() {
        assert!(clean_name("Alex’s phone"));
        assert!(clean_name(&"x".repeat(64)));
        assert!(!clean_name(""));
        assert!(!clean_name(&"x".repeat(65)));
        assert!(!clean_name("a\nb"));
        assert!(!clean_name("a\u{0}b"));
    }

    #[test]
    fn tokens_are_long_random_and_stored_only_as_a_hash() {
        let (a, ha) = new_token().unwrap();
        let (b, _) = new_token().unwrap();
        assert_ne!(a, b);
        assert_eq!(a.len(), 43); // 32 bytes, base64url without padding
        assert!(a.chars().all(|c| c.is_ascii_alphanumeric() || c == '-' || c == '_'));
        assert_eq!(ha, hash_token(&a));
        assert_eq!(ha.len(), 32);
        assert_ne!(hash_token(&a), hash_token(&b));
    }
}
