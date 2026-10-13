//! Shared test setup: a guarded database, and a way to call the router like a client would.
#![allow(dead_code)]

use async_trait::async_trait;
use axum::{
    Router,
    body::Body,
    http::{HeaderMap, Request, StatusCode},
};
use http_body_util::BodyExt;
use serde_json::Value;
use speedtest_backend::{
    app::{AppState, router},
    auth::parse_tokens,
    ondemand::{
        config::Config as OdConfig,
        manager::Manager,
        ookla::{Progress, RunError, Runner},
    },
    store::Store,
};
use sqlx::{PgPool, postgres::PgPoolOptions};
use std::{
    path::PathBuf,
    sync::{
        Arc, OnceLock,
        atomic::{AtomicUsize, Ordering},
    },
};
use tokio_util::sync::CancellationToken;
use tower::ServiceExt;

/// The marker a database must carry before tests may empty its tables. (A name pattern is not a safe guard: "speedtestdb"
/// contains "test".) The same marker backend-go's tests use.
pub const MARKER: &str = "speedtest-integration-test";
/// Who may send results in the tests: the token decides the device.
pub const TOKENS: &str = "laptop=tok-laptop,phone=tok-phone";
const LOCK_KEY: i64 = 7_447_001; // the Go tests' key: both suites take turns on one database

static SERIAL: tokio::sync::Mutex<()> = tokio::sync::Mutex::const_new(());
static PROCESS_LOCK: OnceLock<()> = OnceLock::new();

pub struct Resp {
    pub status: StatusCode,
    pub headers: HeaderMap,
    pub body: Vec<u8>,
}

impl Resp {
    pub fn json(&self) -> Value {
        serde_json::from_slice(&self.body).unwrap_or_else(|e| panic!("not JSON ({e}): {}", String::from_utf8_lossy(&self.body)))
    }
    pub fn header(&self, name: &str) -> &str {
        self.headers.get(name).and_then(|v| v.to_str().ok()).unwrap_or("")
    }
    pub fn text(&self) -> String {
        String::from_utf8_lossy(&self.body).into_owned()
    }
}

pub struct Harness {
    pub app: Router,
    pub pool: PgPool,
    _turn: tokio::sync::MutexGuard<'static, ()>,
}

impl Harness {
    /// A router on the disposable database with every table emptied. None (and a note) when TEST_DATABASE_URL is not set.
    pub async fn new(timezone: &str, static_dir: Option<PathBuf>) -> Option<Self> {
        let Ok(url) = std::env::var("TEST_DATABASE_URL") else {
            eprintln!("TEST_DATABASE_URL not set: skipping (scripts/scratch-db.sh up)");
            return None;
        };
        let turn = SERIAL.lock().await; // tests in this file share one database
        take_process_lock(&url);
        let pool = PgPoolOptions::new().max_connections(5).connect(&url).await.expect("connect to TEST_DATABASE_URL");
        let marker: Option<String> =
            sqlx::query_scalar("SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = current_database()")
                .fetch_one(&pool)
                .await
                .unwrap();
        assert_eq!(
            marker.as_deref(),
            Some(MARKER),
            "refusing to run: this database is not marked as disposable.\nTo use a scratch database: COMMENT ON DATABASE <name> IS '{MARKER}';"
        );
        sqlx::query(
            "TRUNCATE speedtests, alert_state, notify_cursor, notification_events, push_subscriptions, push_vapid, session_key CASCADE",
        )
        .execute(&pool)
        .await
        .unwrap();
        let state = AppState::new(Store::new(pool.clone()), timezone, static_dir).with_auth(parse_tokens(TOKENS), 0);
        Some(Self { app: router(state), pool, _turn: turn })
    }

    /// The same database behind a router whose default time zone is `timezone`.
    pub fn with_timezone(&self, timezone: &str) -> Router {
        router(AppState::new(Store::new(self.pool.clone()), timezone, None).with_auth(parse_tokens(TOKENS), 0))
    }

    pub async fn request(&self, method: &str, uri: &str) -> Resp {
        let req = Request::builder().method(method).uri(uri).body(Body::empty()).unwrap();
        let res = self.app.clone().oneshot(req).await.unwrap();
        let (parts, body) = res.into_parts();
        Resp { status: parts.status, headers: parts.headers, body: body.collect().await.unwrap().to_bytes().to_vec() }
    }

    /// POST (or another method) with a body and, optionally, a bearer token.
    pub async fn send(&self, method: &str, uri: &str, token: Option<&str>, body: &str) -> Resp {
        send(&self.app, method, uri, token, body).await
    }

    /// The same database behind a router with these tokens and this ingest rate (0 = unlimited).
    pub fn with_tokens(&self, tokens: &str, per_minute: u32) -> Router {
        router(AppState::new(Store::new(self.pool.clone()), "UTC", None).with_auth(parse_tokens(tokens), per_minute))
    }

    pub async fn get(&self, uri: &str) -> Resp {
        self.request("GET", uri).await
    }
}

/// A router that never needs the database (routes, stubs, static files).
pub fn offline_router(static_dir: Option<PathBuf>) -> Router {
    let pool = PgPoolOptions::new().connect_lazy("postgres://nobody@127.0.0.1:1/none").unwrap();
    router(AppState::new(Store::new(pool), "UTC", static_dir).with_auth(parse_tokens(TOKENS), 0))
}

pub async fn send(app: &Router, method: &str, uri: &str, token: Option<&str>, body: &str) -> Resp {
    let mut b = Request::builder().method(method).uri(uri).header("content-type", "application/json");
    if let Some(t) = token {
        b = b.header("authorization", format!("Bearer {t}"));
    }
    let req = b.body(Body::from(body.to_string())).unwrap();
    let (parts, body) = app.clone().oneshot(req).await.unwrap().into_parts();
    Resp { status: parts.status, headers: parts.headers, body: body.collect().await.unwrap().to_bytes().to_vec() }
}

pub async fn call(app: &Router, method: &str, uri: &str) -> Resp {
    let req = Request::builder().method(method).uri(uri).body(Body::empty()).unwrap();
    let (parts, body) = app.clone().oneshot(req).await.unwrap().into_parts();
    Resp { status: parts.status, headers: parts.headers, body: body.collect().await.unwrap().to_bytes().to_vec() }
}

/// Holds an advisory lock for the whole test process on a connection of its own, in a thread of its own: a connection made
/// inside one test's runtime would die with it and drop the lock, and `go test ./...` runs other packages against the same
/// database in parallel.
fn take_process_lock(url: &str) {
    PROCESS_LOCK.get_or_init(|| {
        let (tx, rx) = std::sync::mpsc::channel();
        let url = url.to_string();
        std::thread::spawn(move || {
            tokio::runtime::Builder::new_current_thread().enable_all().build().unwrap().block_on(async {
                use sqlx::Connection;
                let mut conn = sqlx::PgConnection::connect(&url).await.expect("lock connection");
                sqlx::query("SELECT pg_advisory_lock($1)").bind(LOCK_KEY).execute(&mut conn).await.expect("advisory lock");
                tx.send(()).ok();
                std::future::pending::<()>().await; // keep the connection (and the lock) until the process ends
            });
        });
        rx.recv().expect("advisory lock");
    });
}

// ---- on-demand tests ----------------------------------------------------------------------------------------------------

type Steps = Box<
    dyn Fn(CancellationToken, &(dyn Fn(Progress) + Send + Sync)) -> futures_util::future::BoxFuture<'static, Result<Vec<u8>, RunError>>
        + Send
        + Sync,
>;

/// Stands in for the Ookla CLI: `steps` decides what a test does.
pub struct FakeRunner {
    steps: Steps,
    pub calls: AtomicUsize,
}

#[async_trait]
impl Runner for FakeRunner {
    async fn run(&self, cancel: CancellationToken, emit: &(dyn Fn(Progress) + Send + Sync)) -> Result<Vec<u8>, RunError> {
        self.calls.fetch_add(1, Ordering::SeqCst);
        (self.steps)(cancel, emit).await
    }
}

pub fn progress(phase: &'static str, mbps: Option<f64>, fraction: f64) -> Progress {
    Progress { r#type: "progress", phase, elapsed_ms: 100, fraction: Some(fraction), current_mbps: mbps, latency_ms: None, jitter_ms: None }
}

/// A runner that reports three readings and finishes with `raw`.
pub fn finishes_with(raw: Vec<u8>) -> Arc<FakeRunner> {
    Arc::new(FakeRunner {
        calls: AtomicUsize::new(0),
        steps: Box::new(move |_, emit| {
            emit(progress("ping", None, 0.5));
            emit(progress("download", Some(90.0), 0.5));
            emit(progress("upload", Some(90.0), 0.5));
            let raw = raw.clone();
            Box::pin(async move { Ok(raw) })
        }),
    })
}

/// A runner that waits until released (then returns `raw`) or cancelled.
pub fn blocks(raw: Vec<u8>) -> (Arc<FakeRunner>, Arc<tokio::sync::Notify>) {
    let release = Arc::new(tokio::sync::Notify::new());
    let r2 = release.clone();
    let runner = Arc::new(FakeRunner {
        calls: AtomicUsize::new(0),
        steps: Box::new(move |cancel, _| {
            let (release, raw) = (r2.clone(), raw.clone());
            Box::pin(async move {
                tokio::select! {
                    _ = release.notified() => Ok(raw),
                    _ = cancel.cancelled() => Err(RunError::Cancelled),
                }
            })
        }),
    });
    (runner, release)
}

pub fn fails_with(message: &'static str) -> Arc<FakeRunner> {
    Arc::new(FakeRunner {
        calls: AtomicUsize::new(0),
        steps: Box::new(move |_, _| Box::pin(async move { Err(RunError::Failed(message.into())) })),
    })
}

/// The last line of a real (sanitized) Ookla run: 99.2 Mbps down, 49.1 Mbps up, 2% packet loss.
pub fn real_result() -> Vec<u8> {
    let text = std::fs::read_to_string(concat!(env!("CARGO_MANIFEST_DIR"), "/tests/ookla_real.jsonl")).unwrap();
    text.lines().rfind(|l| !l.trim().is_empty()).unwrap().as_bytes().to_vec()
}

pub fn od_config() -> OdConfig {
    OdConfig { ookla_accepted: true, ookla_path: "sh".into(), create_per_minute: 600, ..Default::default() } // "sh" is on every machine these tests run on
}

pub struct OdRig {
    pub app: Router,
    pub manager: Arc<Manager>,
    pub pool: PgPool,
    pub root: CancellationToken,
    _turn: tokio::sync::MutexGuard<'static, ()>,
}

impl Harness {
    /// On-demand tests on top of this harness: a manager with this runner, started (so tests a previous run left behind are failed).
    pub async fn with_ondemand(self, cfg: OdConfig, runner: Option<Arc<dyn Runner>>) -> OdRig {
        let manager = Manager::new(cfg, Store::new(self.pool.clone()), runner);
        let root = CancellationToken::new();
        manager.start(root.clone()).await;
        let state =
            AppState::new(Store::new(self.pool.clone()), "UTC", None).with_auth(parse_tokens(TOKENS), 0).with_ondemand(manager.clone());
        let app = router(state);
        OdRig { app, manager, pool: self.pool, root, _turn: self._turn }
    }
}

impl OdRig {
    pub async fn send(&self, method: &str, uri: &str, token: Option<&str>, body: &str) -> Resp {
        send(&self.app, method, uri, token, body).await
    }
    pub async fn get(&self, uri: &str) -> Resp {
        self.send("GET", uri, None, "").await
    }

    /// A request from a given network address, optionally with a bearer token.
    pub async fn send_from(&self, method: &str, uri: &str, peer: &str, token: Option<&str>, body: &str) -> Resp {
        let auth = token.map(|t| format!("Bearer {t}"));
        let headers: Vec<(&str, &str)> = auth.iter().map(|a| ("authorization", a.as_str())).collect();
        send_from(&self.app, method, uri, peer, &headers, body).await
    }

    /// Starts a test over HTTP from a given address.
    pub async fn send_from_ip(&self, peer: &str, body: &str) -> Resp {
        self.send_from("POST", "/api/v1/tests", peer, None, body).await
    }

    /// Another router on the same database with a different configuration (a rig holds the turn on the database, so a second
    /// rig made the usual way would wait for it for ever).
    pub async fn also(&self, cfg: OdConfig, runner: Option<Arc<dyn Runner>>) -> Router {
        let manager = Manager::new(cfg, Store::new(self.pool.clone()), runner);
        manager.start(CancellationToken::new()).await;
        router(AppState::new(Store::new(self.pool.clone()), "UTC", None).with_auth(parse_tokens(TOKENS), 0).with_ondemand(manager))
    }

    pub fn manager_store(&self) -> Store {
        Store::new(self.pool.clone())
    }

    /// Starts a test over HTTP.
    pub async fn create(&self, body: &str) -> Resp {
        self.send("POST", "/api/v1/tests", None, body).await
    }

    /// Starts a server test and returns its id.
    pub async fn server(&self) -> String {
        let r = self.create(r#"{"provider":"ookla","mode":"server","connectionType":"wired"}"#).await;
        assert_eq!(r.status, StatusCode::CREATED, "{}", r.text());
        r.json()["id"].as_str().unwrap().to_string()
    }

    pub async fn wait_state(&self, id: &str, want: &str) -> Value {
        for _ in 0..400 {
            let t = self.get(&format!("/api/v1/tests/{id}")).await.json();
            if t["state"] == want {
                return t;
            }
            tokio::time::sleep(std::time::Duration::from_millis(20)).await;
        }
        panic!("test {id} never reached {want:?}: {}", self.get(&format!("/api/v1/tests/{id}")).await.text());
    }

    pub async fn count(&self, sql: &str) -> i64 {
        sqlx::query_scalar(sql).fetch_one(&self.pool).await.unwrap()
    }
}

/// One server-sent event.
#[derive(Debug, Clone)]
pub struct Sse {
    pub id: Option<String>,
    pub name: String,
    pub data: Value,
}

/// Reads events from a streaming response until it ends, or `stop` says so. Fails the test if nothing arrives for 10 seconds.
pub async fn read_sse(res: axum::response::Response, stop: impl Fn(&Sse) -> bool) -> Vec<Sse> {
    use futures_util::StreamExt;
    assert_eq!(res.status(), StatusCode::OK);
    let mut stream = res.into_body().into_data_stream();
    let (mut buf, mut out) = (String::new(), Vec::new());
    loop {
        let chunk = match tokio::time::timeout(std::time::Duration::from_secs(10), stream.next()).await {
            Err(_) => panic!("the stream went quiet; got so far: {out:?}"),
            Ok(None) | Ok(Some(Err(_))) => return out,
            Ok(Some(Ok(c))) => c,
        };
        buf.push_str(&String::from_utf8_lossy(&chunk));
        while let Some(end) = buf.find("\n\n") {
            let block: String = buf.drain(..end + 2).collect();
            let (mut id, mut name, mut data) = (None, String::new(), String::new());
            for line in block.lines() {
                if let Some(v) = line.strip_prefix("id: ") {
                    id = Some(v.to_string());
                } else if let Some(v) = line.strip_prefix("event: ") {
                    name = v.to_string();
                } else if let Some(v) = line.strip_prefix("data: ") {
                    data.push_str(v);
                }
            }
            if name.is_empty() {
                continue; // a keep-alive comment
            }
            let ev = Sse { id, name, data: serde_json::from_str(&data).unwrap_or(Value::Null) };
            let done = stop(&ev);
            out.push(ev);
            if done {
                return out;
            }
        }
    }
}

pub async fn events(rig: &OdRig, id: &str) -> Vec<Sse> {
    let req = Request::builder().uri(format!("/api/v1/tests/{id}/events")).body(Body::empty()).unwrap();
    read_sse(rig.app.clone().oneshot(req).await.unwrap(), |_| false).await
}

/// Listens the way a browser's EventSource does: when the connection ends before the test has, reconnect with the id of the
/// last event seen, until the test's last event (a result, an error, or the cancelled state) arrives.
pub async fn events_following(rig: &OdRig, id: &str) -> Vec<Sse> {
    let mut all: Vec<Sse> = Vec::new();
    for _ in 0..50 {
        let last = all.iter().rev().find_map(|e| e.id.clone()).unwrap_or_default();
        let mut req = Request::builder().uri(format!("/api/v1/tests/{id}/events"));
        if !last.is_empty() {
            req = req.header("last-event-id", last);
        }
        let got = read_sse(rig.app.clone().oneshot(req.body(Body::empty()).unwrap()).await.unwrap(), |_| false).await;
        // the first event of every connection is the current state (it has no id): the numbered ones are the history
        all.extend(got.into_iter().filter(|e| e.id.is_some()));
        let finished =
            all.iter().any(|e| e.name == "result" || e.name == "error" || (e.name == "state" && e.data["test"]["state"] == "cancelled"));
        if finished {
            return all;
        }
    }
    panic!("the test never finished; got {} events", all.len());
}

pub fn names(evs: &[Sse]) -> String {
    evs.iter().map(|e| e.name.as_str()).collect::<Vec<_>>().join(",")
}

/// Like `send`, from a given network address (what the server sees as the peer).
pub async fn send_from(app: &Router, method: &str, uri: &str, peer: &str, headers: &[(&str, &str)], body: &str) -> Resp {
    let mut b = Request::builder().method(method).uri(uri).header("content-type", "application/json");
    for (k, v) in headers {
        b = b.header(*k, *v);
    }
    let mut req = b.body(Body::from(body.to_string())).unwrap();
    req.extensions_mut().insert(axum::extract::ConnectInfo::<std::net::SocketAddr>(format!("{peer}:4000").parse().unwrap()));
    let (parts, body) = app.clone().oneshot(req).await.unwrap().into_parts();
    Resp { status: parts.status, headers: parts.headers, body: body.collect().await.unwrap().to_bytes().to_vec() }
}
