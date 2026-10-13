//! Routes and handlers.

use axum::{
    Router,
    extract::{Path, RawQuery, Request, State},
    http::{Method, StatusCode, header},
    middleware::{self, Next},
    response::{IntoResponse, Response},
    routing::{get, post},
};
use serde::Serialize;
use std::{
    path::PathBuf,
    sync::Arc,
    time::{Duration, Instant},
};
use tower_http::catch_panic::CatchPanicLayer;
use uuid::Uuid;

use crate::{
    auth::{Authenticator, BURST, DEFAULT_PER_MINUTE, Limiter, TokenEntry},
    ingest::{self, MAX_BODY_BYTES, Rejected},
    model::NotificationStatus,
    notify::monitor::Monitor,
    ondemand::manager::Manager,
    ondemand_api as od,
    params::*,
    problem::{FieldError, Problem},
    push_api, session_api, static_files,
    store::{Store, StoreError},
};

#[derive(Clone)]
pub struct AppState {
    pub store: Store,
    /// Default time zone for timeseries buckets.
    pub timezone: Arc<str>,
    pub static_dir: Option<Arc<PathBuf>>,
    pub auth: Arc<Authenticator>,
    pub limiter: Arc<Limiter>,
    /// False when INGEST_RATE_PER_MIN=0: the limiter still counts, but never refuses.
    pub limit_ingest: bool,
    /// On-demand tests (None: those endpoints answer 503).
    pub ondemand: Option<Arc<Manager>>,
    /// The notification monitor (None: alerts are off).
    pub notifier: Option<Arc<Monitor>>,
    /// Browser notifications (None: those endpoints answer 503).
    pub pusher: Option<Arc<crate::push::Service>>,
    pub push_limiter: Arc<Limiter>,
    pub push_test_limiter: Arc<Limiter>,
    /// The dashboard login (None: the dashboard is open).
    pub login: Option<Arc<crate::login::Service>>,
    /// Login attempts per address, and from all addresses together.
    pub login_limiter: Arc<Limiter>,
    pub login_global: Arc<Limiter>,
}

impl AppState {
    /// A state with no tokens (every ingest request is refused) and the default rate.
    pub fn new(store: Store, timezone: &str, static_dir: Option<PathBuf>) -> Self {
        Self {
            store,
            timezone: timezone.into(),
            static_dir: static_dir.map(Arc::new),
            auth: Arc::new(Authenticator::default()),
            limiter: Arc::new(Limiter::new(BURST, DEFAULT_PER_MINUTE)),
            limit_ingest: true,
            ondemand: None,
            notifier: None,
            pusher: None,
            push_limiter: Arc::new(Limiter::new(10, 20)),
            push_test_limiter: Arc::new(Limiter::new(3, 6)),
            login: None,
            login_limiter: Arc::new(Limiter::new(5, 6)),
            login_global: Arc::new(Limiter::new(30, 30)),
        }
    }

    /// Turns the dashboard login on (see the `login` module).
    pub fn with_login(mut self, l: Arc<crate::login::Service>) -> Self {
        self.login = Some(l);
        self.reset_login_limits();
        self
    }

    /// 5 attempts in a burst and 6 a minute per address, and 30 and 30 for all addresses together. Lifted when ingest limiting is
    /// off (INGEST_RATE_PER_MIN=0), which exists for load and fuzz tests.
    fn reset_login_limits(&mut self) {
        let (per_ip, all) = if self.limit_ingest { ((5, 6), (30, 30)) } else { ((1_000_000, 1_000_000), (1_000_000, 1_000_000)) };
        self.login_limiter = Arc::new(Limiter::new(per_ip.0, per_ip.1));
        self.login_global = Arc::new(Limiter::new(all.0, all.1));
    }

    pub fn with_notifier(mut self, m: Arc<Monitor>) -> Self {
        self.notifier = Some(m);
        self
    }

    pub fn with_pusher(mut self, p: Arc<crate::push::Service>) -> Self {
        self.pusher = Some(p);
        self
    }

    /// Whether X-Forwarded-For can be believed (only behind your own proxy).
    pub fn trust_proxy(&self) -> bool {
        self.ondemand.as_ref().is_some_and(|m| m.config().trust_proxy)
    }

    pub fn with_ondemand(mut self, manager: Arc<Manager>) -> Self {
        self.ondemand = Some(manager);
        self
    }

    pub fn with_auth(mut self, tokens: Vec<TokenEntry>, per_minute: u32) -> Self {
        self.auth = Arc::new(Authenticator::new(tokens));
        self.limit_ingest = per_minute > 0;
        self.limiter = Arc::new(Limiter::new(BURST, per_minute.max(1)));
        if per_minute == 0 {
            // limits off (load and fuzz tests): this includes the browser-notification endpoints
            self.push_limiter = Arc::new(Limiter::new(1_000_000, 1_000_000));
            self.push_test_limiter = Arc::new(Limiter::new(1_000_000, 1_000_000));
        }
        self.reset_login_limits();
        self
    }
}

pub fn router(state: AppState) -> Router {
    Router::new()
        .route("/api/v1/health", get(health))
        .route("/api/v1/devices", get(devices))
        .route("/api/v1/results", get(list_results).post(post_result))
        .route("/api/v1/results/{id}", get(get_result))
        .route("/api/v1/stats/summary", get(summary))
        .route("/api/v1/stats/timeseries", get(timeseries))
        // Features that arrive in later stages answer as the contract describes their "off" states, so a dashboard can run
        // against this backend today. Ingest is the one exception: the contract has no "off" answer for it, so it says 501.
        .route("/api/v1/notifications", get(notifications))
        .route("/api/v1/auth/session", get(session_api::session))
        .route("/api/v1/auth/login", post(session_api::login))
        .route("/api/v1/auth/logout", post(session_api::logout))
        .route("/api/v1/capabilities", get(od::capabilities))
        .route("/api/v1/push/config", get(push_api::config))
        .route("/api/v1/push/subscriptions", post(push_api::subscribe).delete(push_api::unsubscribe))
        .route("/api/v1/push/test", post(push_api::test))
        .route("/api/v1/tests", get(od::list_tests).post(od::create_test))
        .route("/api/v1/tests/{id}", get(od::get_test).delete(od::cancel_test))
        .route("/api/v1/tests/{id}/events", get(od::test_events))
        .route("/api/v1/tests/{id}/result", post(od::submit_result))
        .fallback(fallback)
        .layer(middleware::from_fn_with_state(state.clone(), session_api::require_login))
        .layer(middleware::from_fn(problem_for_bare_errors))
        .layer(middleware::from_fn(log_requests))
        .layer(CatchPanicLayer::custom(|_| Problem::internal().into_response()))
        .with_state(state)
}

fn json<T: Serialize>(v: &T) -> Response {
    match serde_json::to_vec(v) {
        Ok(b) => ([(header::CONTENT_TYPE, "application/json")], b).into_response(),
        Err(e) => {
            tracing::error!(err = %e, "could not serialize a response");
            Problem::internal().into_response()
        }
    }
}

fn bad_request(e: Errs) -> Problem {
    Problem::invalid("invalid query parameters", e.0)
}

async fn health(State(s): State<AppState>) -> Response {
    match tokio::time::timeout(Duration::from_secs(2), s.store.ping()).await {
        Ok(Ok(())) => json(&serde_json::json!({"status": "ok"})),
        other => {
            tracing::error!(err = ?other, "health check failed");
            Problem::new(StatusCode::SERVICE_UNAVAILABLE, "database is unreachable").into_response()
        }
    }
}

async fn devices(State(s): State<AppState>) -> Result<Response, Problem> {
    Ok(json(&serde_json::json!({"items": s.store.devices().await?})))
}

async fn list_results(State(s): State<AppState>, RawQuery(raw): RawQuery) -> Result<Response, Problem> {
    let q = Query::parse(raw.as_deref());
    let mut e = Errs::default();
    let f = parse_filter(&q, &mut e);
    let limit = parse_limit(&q, 100, 500, &mut e);
    let mut after = None;
    if q.has("cursor") {
        // present but empty is not "no cursor": the contract wants a real one
        after = decode_cursor(q.get("cursor"));
        if after.is_none() {
            e.add("cursor", "is not a cursor returned by this API");
        }
    }
    if !e.is_empty() {
        return Err(bad_request(e));
    }
    let (items, next) = s.store.list_results(&f, limit, after).await?;
    let next = next.map(|(t, id)| encode_cursor(t, &id));
    Ok(json(&serde_json::json!({"items": items, "nextCursor": next})))
}

async fn get_result(State(s): State<AppState>, Path(id): Path<String>) -> Result<Response, Problem> {
    // Only the canonical 8-4-4-4-12 form counts as an id (the uuid crate also accepts braces and no dashes).
    let uuid = Uuid::parse_str(&id).ok().filter(|_| id.len() == 36);
    let found = match uuid {
        Some(u) => s.store.get_result(u).await?,
        None => None,
    };
    found.map(|r| json(&r)).ok_or_else(|| Problem::new(StatusCode::NOT_FOUND, "no such result"))
}

async fn summary(State(s): State<AppState>, RawQuery(raw): RawQuery) -> Result<Response, Problem> {
    let q = Query::parse(raw.as_deref());
    let mut e = Errs::default();
    let f = parse_filter(&q, &mut e);
    if !e.is_empty() {
        return Err(bad_request(e));
    }
    Ok(json(&s.store.summary(&f).await?))
}

async fn timeseries(State(s): State<AppState>, RawQuery(raw): RawQuery) -> Result<Response, Problem> {
    let q = Query::parse(raw.as_deref());
    let mut e = Errs::default();
    let f = parse_filter(&q, &mut e);
    // present but empty is not "use the default": the contract's enum has no empty value
    let interval = match (q.has("interval"), q.get("interval")) {
        (false, _) => "day",
        (true, i) if INTERVALS.contains(&i) => i,
        _ => {
            e.add("interval", format!("must be one of {}", INTERVALS.join(", ")));
            "day"
        }
    };
    let mut group_by = enum_list(&q, "groupBy", GROUP_FIELDS, &mut e);
    if group_by.is_empty() {
        group_by = GROUP_FIELDS.iter().map(|g| g.to_string()).collect();
    }
    let tz = match q.get("timezone") {
        "" => s.timezone.to_string(),
        name if valid_timezone(name) => name.to_string(),
        _ => {
            e.add("timezone", "must be an IANA time zone name such as America/Phoenix");
            s.timezone.to_string()
        }
    };
    if !e.is_empty() {
        return Err(bad_request(e));
    }
    match s.store.timeseries(&f, interval, &group_by, &tz).await {
        Ok(ts) => Ok(json(&ts)),
        Err(StoreError::TooManyPoints) => Err(Problem::new(
            StatusCode::BAD_REQUEST,
            "the range holds too many points for this interval; narrow from/to or use a coarser interval",
        )),
        // A zone name PostgreSQL does not know (the checks above use a different list) is the caller's mistake.
        Err(StoreError::Db(sqlx::Error::Database(d))) if d.code().as_deref() == Some("22023") => Err(Problem::invalid(
            "invalid query parameters",
            vec![FieldError { field: "timezone".into(), message: "is not a time zone the database knows".into() }],
        )),
        Err(StoreError::Db(e)) => Err(e.into()),
    }
}

/// POST /api/v1/results: a device sends a speed test result. The bearer token decides which device it is.
async fn post_result(State(s): State<AppState>, req: Request) -> Result<Response, Problem> {
    let (parts, body) = req.into_parts();
    let Some(device) = s.auth.device(&parts.headers) else {
        return Err(
            Problem::new(StatusCode::UNAUTHORIZED, "missing or invalid bearer token").with_header(header::WWW_AUTHENTICATE, "Bearer")
        );
    };
    if let Err(retry) = s.limiter.allow(device) {
        if s.limit_ingest {
            return Err(Problem::new(StatusCode::TOO_MANY_REQUESTS, "too many results submitted; slow down")
                .with_header(header::RETRY_AFTER, &retry.to_string()));
        }
    }
    let body = match axum::body::to_bytes(body, MAX_BODY_BYTES).await {
        Ok(b) => b,
        Err(e) if is_length_limit(&e) => return Err(Problem::new(StatusCode::PAYLOAD_TOO_LARGE, "body is larger than 64 KB")),
        Err(_) => return Err(Problem::new(StatusCode::BAD_REQUEST, "could not read body")),
    };
    let ok = match ingest::validate_envelope(&body) {
        Ok(ok) => ok,
        Err(Rejected::NotJson) => return Err(Problem::new(StatusCode::BAD_REQUEST, "body is not valid JSON")),
        Err(Rejected::Fields(errors)) => return Err(Problem::invalid("request validation failed", errors)),
    };
    let stored = if ok.provider == "ookla" {
        s.store.insert_ookla(ok.result.get(), device, &ok.connection_type, &ok.trigger).await
    } else {
        s.store.insert_ndt7(ok.result.get(), device, &ok.connection_type, &ok.trigger, None).await
    };
    let id = stored.map_err(insert_failed)?;
    let detail = s.store.get_result(Uuid::parse_str(&id).map_err(|_| Problem::internal())?).await?.ok_or_else(Problem::internal)?;
    let mut res = json(&detail.result);
    *res.status_mut() = StatusCode::CREATED;
    Ok(res)
}

/// `to_bytes` reports a body over the limit as an error whose source is the length-limit marker.
fn is_length_limit(e: &axum::Error) -> bool {
    let mut src: Option<&(dyn std::error::Error + 'static)> = std::error::Error::source(e);
    while let Some(s) = src {
        if s.is::<http_body_util::LengthLimitError>() {
            return true;
        }
        src = s.source();
    }
    false
}

/// The database refusing the input itself (a data exception or constraint violation) is the caller's mistake: 400, not 500.
pub fn insert_failed(e: sqlx::Error) -> Problem {
    if let sqlx::Error::Database(d) = &e {
        if d.code().is_some_and(|c| c.starts_with("22") || c.starts_with("23")) {
            return Problem::new(StatusCode::BAD_REQUEST, format!("the database rejected the result: {}", d.message()));
        }
    }
    e.into()
}

async fn notifications(State(s): State<AppState>) -> Result<Response, Problem> {
    let status = match &s.notifier {
        Some(m) => m.status().await?,
        None => NotificationStatus::disabled(),
    };
    Ok(json(&status))
}

fn fallback_status(status: StatusCode, detail: &str) -> Response {
    Problem::new(status, detail).into_response()
}

/// Anything no route matched: the built frontend (when one is configured) for page loads, JSON 404 for the rest.
async fn fallback(State(s): State<AppState>, req: Request) -> Response {
    let path = req.uri().path();
    let page_load = matches!(*req.method(), Method::GET | Method::HEAD) && !path.starts_with("/api/");
    match (&s.static_dir, page_load) {
        (Some(dir), true) => static_files::serve(dir, req.method(), path).await,
        _ => fallback_status(StatusCode::NOT_FOUND, "no such endpoint"),
    }
}

/// axum answers a wrong method with a bare 405 (and an `Allow` header); say it in the API's own error format.
async fn problem_for_bare_errors(req: Request, next: Next) -> Response {
    let res = next.run(req).await;
    if res.status() == StatusCode::METHOD_NOT_ALLOWED && !res.headers().contains_key(header::CONTENT_TYPE) {
        let allow = res.headers().get(header::ALLOW).cloned();
        let mut out = Problem::new(StatusCode::METHOD_NOT_ALLOWED, "method not allowed for this path").into_response();
        if let Some(a) = allow {
            out.headers_mut().insert(header::ALLOW, a);
        }
        return out;
    }
    res
}

async fn log_requests(req: Request, next: Next) -> Response {
    let (method, path, start) = (req.method().clone(), req.uri().path().to_string(), Instant::now());
    let res = next.run(req).await;
    tracing::info!(method = %method, path = %path, status = res.status().as_u16(), ms = start.elapsed().as_millis() as u64, "request");
    res
}
