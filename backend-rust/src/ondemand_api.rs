//! HTTP handlers for on-demand tests: capabilities, start, list, read, cancel, the live event stream, and the browser's
//! result submission. Ports backend-go/internal/api/tests.go.

use std::{convert::Infallible, net::SocketAddr, sync::Arc, time::Duration};

use axum::{
    extract::{ConnectInfo, Path, RawQuery, Request, State},
    http::{HeaderMap, HeaderName, HeaderValue, StatusCode, header},
    response::{
        IntoResponse, Response,
        sse::{Event, KeepAlive, Sse},
    },
};
use chrono::Utc;
use serde::Serialize;
use serde_json::value::RawValue;

use crate::{
    app::AppState,
    auth::bearer_token,
    gojson::{self, Members, kind},
    ingest::{MAX_BODY_BYTES, NOT_NDT7, validate_ndt7},
    model::Test,
    ondemand::manager::{CreateRequest, CreateTestError, ErrorEvent, Manager, ResultEvent, StateEvent},
    params::{Errs, Query, enum_list, parse_limit, parse_rfc3339},
    problem::{FieldError, Problem},
    store_tests::SubmitError,
};

const TEST_STATES: &[&str] = &["queued", "running", "completed", "failed", "cancelled"];
const MAX_CREATE_BODY: usize = 4096;

fn json<T: Serialize>(status: StatusCode, v: &T) -> Response {
    match serde_json::to_vec(v) {
        Ok(b) => (status, [(header::CONTENT_TYPE, "application/json")], b).into_response(),
        Err(_) => Problem::internal().into_response(),
    }
}

/// The manager, or the 503 every test endpoint gives when on-demand tests are not enabled.
fn manager(s: &AppState) -> Result<&Arc<Manager>, Problem> {
    s.ondemand.as_ref().ok_or_else(|| Problem::new(StatusCode::SERVICE_UNAVAILABLE, "on-demand tests are not enabled on this server"))
}

/// The address the request came from, or "" if it cannot be determined. X-Forwarded-For is believed only behind your own proxy.
pub fn client_ip(trust_proxy: bool, headers: &HeaderMap, peer: Option<SocketAddr>) -> String {
    let mut candidate = String::new();
    if trust_proxy {
        if let Some(xff) = headers.get("x-forwarded-for").and_then(|v| v.to_str().ok()).filter(|v| !v.is_empty()) {
            candidate = xff.split(',').next().unwrap_or("").trim().to_string();
        }
    }
    if candidate.is_empty() {
        candidate = peer.map(|p| p.ip().to_string()).unwrap_or_default();
    }
    candidate.parse::<std::net::IpAddr>().map(|ip| ip.to_canonical().to_string()).unwrap_or_default()
}

pub async fn capabilities(State(s): State<AppState>) -> Response {
    match &s.ondemand {
        Some(m) => json(StatusCode::OK, &m.capabilities()),
        None => {
            let why = Some("On-demand tests are not enabled on this server.".to_string());
            json(
                StatusCode::OK,
                &crate::model::Capabilities {
                    device_name: "server".into(),
                    server_connection_type: "wired".into(),
                    ookla: crate::model::Capability { available: false, reason: why.clone() },
                    ndt7: crate::model::Capability { available: false, reason: why },
                },
            )
        }
    }
}

pub async fn create_test(State(s): State<AppState>, req: Request) -> Result<Response, Problem> {
    let m = manager(&s)?;
    let (parts, body) = req.into_parts();
    // Anonymous callers are allowed (the dashboard has no login). A token that is present must be valid, though: quietly
    // ignoring a wrong credential would hide mistakes.
    let mut token_device = String::new();
    if parts.headers.get(header::AUTHORIZATION).is_some_and(|v| !v.is_empty()) {
        match s.auth.device(&parts.headers) {
            Some(d) => token_device = d.to_string(),
            None => {
                return Err(Problem::new(StatusCode::UNAUTHORIZED, "missing or invalid bearer token")
                    .with_header(header::WWW_AUTHENTICATE, "Bearer"));
            }
        }
    }
    let bad_body = || Problem::new(StatusCode::BAD_REQUEST, "body must be JSON with provider, mode and connectionType");
    let raw = crate::body::first_json_value(body, MAX_CREATE_BODY).await.ok_or_else(bad_body)?;
    let top = Members::decode(Some(&raw)).map_err(|_| bad_body())?;
    let provider = gojson::string(top.field("provider")).map_err(|_| bad_body())?;
    let mode = gojson::string(top.field("mode")).map_err(|_| bad_body())?;
    let connection_type = gojson::string(top.field("connectionType")).map_err(|_| bad_body())?;
    let mut device = String::new();
    if let Some(d) = top.field("device") {
        // omitted is fine; null, a non-string or a blank string is not
        let name = (kind(d) == gojson::Kind::String).then(|| serde_json::from_str::<String>(d.get()).ok()).flatten();
        match name.as_deref().map(str::trim).filter(|n| !n.is_empty()) {
            Some(n) => device = n.to_string(),
            None => {
                return Err(Problem::invalid(
                    "request validation failed",
                    vec![FieldError {
                        field: "device".into(),
                        message: "must be a non-empty string; leave it out if it does not apply".into(),
                    }],
                ));
            }
        }
    }
    let client_ip = client_ip(m.config().trust_proxy, &parts.headers, parts.extensions.get::<ConnectInfo<SocketAddr>>().map(|c| c.0));
    match m.create(CreateRequest { provider, mode, connection_type, device, token_device, client_ip }).await {
        Ok(test) => Ok(json(StatusCode::CREATED, &test)),
        Err(CreateTestError::Validation { field, message }) => {
            Err(Problem::invalid("request validation failed", vec![FieldError { field: field.into(), message }]))
        }
        Err(CreateTestError::Unavailable(why)) => Err(Problem::new(StatusCode::SERVICE_UNAVAILABLE, why)),
        Err(CreateTestError::RateLimited(retry)) => {
            Err(Problem::new(StatusCode::TOO_MANY_REQUESTS, "too many tests started from this address; wait a moment")
                .with_header(header::RETRY_AFTER, &retry.to_string()))
        }
        Err(CreateTestError::ServerBusy) => {
            Err(Problem::new(StatusCode::CONFLICT, "A speed test is already running on the server. Wait for it to finish."))
        }
        Err(CreateTestError::Db(e)) => Err(e.into()),
    }
}

pub async fn list_tests(State(s): State<AppState>, RawQuery(raw): RawQuery) -> Result<Response, Problem> {
    let m = manager(&s)?;
    let q = Query::parse(raw.as_deref());
    let mut e = Errs::default();
    let states = enum_list(&q, "state", TEST_STATES, &mut e);
    let limit = parse_limit(&q, 20, 100, &mut e);
    if !e.is_empty() {
        return Err(Problem::invalid("invalid query parameters", e.0));
    }
    let items = m.list(&states, limit).await?;
    Ok(json(StatusCode::OK, &serde_json::json!({ "items": items })))
}

fn found(test: Option<Test>) -> Result<Response, Problem> {
    test.map(|t| json(StatusCode::OK, &t)).ok_or_else(|| Problem::new(StatusCode::NOT_FOUND, "no such test"))
}

pub async fn get_test(State(s): State<AppState>, Path(id): Path<String>) -> Result<Response, Problem> {
    found(manager(&s)?.get(&id).await?)
}

pub async fn cancel_test(State(s): State<AppState>, Path(id): Path<String>) -> Result<Response, Problem> {
    found(manager(&s)?.cancel(&id).await?)
}

fn last_event_id(headers: &HeaderMap, query: &Query) -> i64 {
    let v =
        headers.get("last-event-id").and_then(|v| v.to_str().ok()).filter(|v| !v.is_empty()).unwrap_or_else(|| query.get("lastEventId"));
    v.parse::<i64>().ok().filter(|n| *n >= 0).unwrap_or(0)
}

fn terminal(state: &str) -> bool {
    matches!(state, "completed" | "failed" | "cancelled")
}

fn sse(name: &'static str, id: Option<i64>, data: String) -> Event {
    let e = Event::default().event(name).data(data);
    match id {
        Some(id) if id > 0 => e.id(id.to_string()),
        _ => e,
    }
}

/// What a test that this process has no record of (it ended before a restart) tells a late listener: how it ended.
async fn finish_from_store(m: &Manager, t: &Test) -> Vec<Event> {
    match (t.state.as_str(), &t.result_id) {
        ("completed", Some(rid)) => match m.result(rid).await {
            Ok(Some(res)) => {
                vec![sse("result", None, serde_json::to_string(&ResultEvent { r#type: "result", result: &res }).unwrap_or_default())]
            }
            _ => Vec::new(),
        },
        ("failed", _) => {
            let msg = t.error.clone().unwrap_or_else(|| "The test failed.".into());
            vec![sse("error", None, serde_json::to_string(&ErrorEvent { r#type: "error", message: &msg }).unwrap_or_default())]
        }
        _ => Vec::new(),
    }
}

/// Streams a test's live events (text/event-stream). A new listener first gets the current state, then anything it missed
/// since Last-Event-ID, then live events until the test ends.
pub async fn test_events(
    State(s): State<AppState>,
    Path(id): Path<String>,
    RawQuery(raw): RawQuery,
    req: Request,
) -> Result<Response, Problem> {
    let m = Arc::clone(manager(&s)?);
    let last = last_event_id(req.headers(), &Query::parse(raw.as_deref()));
    let mut sub = m.subscribe(&id, last); // subscribe before reading the state, so nothing falls in between
    let Some(test) = m.get(&id).await? else {
        return Err(Problem::new(StatusCode::NOT_FOUND, "no such test"));
    };
    let stream = async_stream::stream! {
        let state = serde_json::to_string(&StateEvent { r#type: "state", test: &test }).unwrap_or_default();
        yield Ok::<Event, Infallible>(sse("state", None, state));

        if !sub.exists && terminal(&test.state) {
            for e in finish_from_store(&m, &test).await { yield Ok(e) }
            return;
        }
        for ev in std::mem::take(&mut sub.replay) {
            yield Ok(sse(ev.name, Some(ev.id), ev.data));
        }
        if sub.closed { return }

        // nobody in this process will publish for a test it has no record of: watch the database for its end
        let watch_db = !sub.exists;
        let mut poll = tokio::time::interval(Duration::from_secs(5));
        poll.tick().await;
        let mut live = sub.live.take();
        loop {
            tokio::select! {
                ev = async { match live.as_mut() { Some(rx) => rx.recv().await, None => std::future::pending().await } } => match ev {
                    Some(ev) => yield Ok(sse(ev.name, Some(ev.id), ev.data)),
                    None => return,
                },
                _ = poll.tick(), if watch_db => {
                    if let Ok(Some(cur)) = m.get(&id).await {
                        if terminal(&cur.state) {
                            let st = serde_json::to_string(&StateEvent { r#type: "state", test: &cur }).unwrap_or_default();
                            yield Ok(sse("state", None, st));
                            for e in finish_from_store(&m, &cur).await { yield Ok(e) }
                            return;
                        }
                    }
                }
            }
        }
    };
    let mut res = Sse::new(stream).keep_alive(KeepAlive::new().interval(Duration::from_secs(15)).text("keep-alive")).into_response();
    // do not let a reverse proxy hold events back
    res.headers_mut().insert(HeaderName::from_static("x-accel-buffering"), HeaderValue::from_static("no"));
    Ok(res)
}

/// The NDT7 summary rules plus checks that only make sense for a live browser test.
fn validate_browser_ndt7(body: &[u8]) -> Vec<FieldError> {
    let Ok(raw) = serde_json::from_slice::<Box<RawValue>>(body) else {
        return vec![FieldError { field: String::new(), message: NOT_NDT7.into() }];
    };
    let mut e = validate_ndt7(Some(&raw), "");
    if !e.is_empty() {
        return e;
    }
    let top = Members::decode(Some(&raw)).unwrap_or_default();
    let time = |name: &str| gojson::string(top.field(name)).ok().and_then(|v| parse_rfc3339(&v));
    if let (Some(started), Some(finished)) = (time("startedAt"), time("finishedAt")) {
        let d = finished - started;
        if d < chrono::Duration::seconds(1) || d > chrono::Duration::minutes(10) {
            e.push(FieldError { field: "finishedAt".into(), message: "a test lasts between 1 second and 10 minutes".into() });
        }
        if Utc::now() - finished > chrono::Duration::hours(1) {
            e.push(FieldError { field: "finishedAt".into(), message: "must be recent: results of an old test cannot be submitted".into() });
        }
    }
    e
}

/// Stores the NDT7 result a browser measured. The single-use token from POST /tests authenticates it.
pub async fn submit_result(State(s): State<AppState>, Path(id): Path<String>, req: Request) -> Result<Response, Problem> {
    let m = manager(&s)?;
    let (parts, body) = req.into_parts();
    let Some(token) = bearer_token(&parts.headers) else {
        return Err(
            Problem::new(StatusCode::UNAUTHORIZED, "missing or invalid submit token").with_header(header::WWW_AUTHENTICATE, "Bearer")
        );
    };
    let body = match axum::body::to_bytes(body, MAX_BODY_BYTES).await {
        Ok(b) => b,
        Err(_) => return Err(Problem::new(StatusCode::PAYLOAD_TOO_LARGE, "body is larger than 64 KB")),
    };
    let errors = validate_browser_ndt7(&body);
    if !errors.is_empty() {
        return Err(Problem::invalid("request validation failed", errors));
    }
    let ip = client_ip(m.config().trust_proxy, &parts.headers, parts.extensions.get::<ConnectInfo<SocketAddr>>().map(|c| c.0));
    let summary = String::from_utf8_lossy(&body).into_owned();
    match m.submit(&id, token, &summary, (!ip.is_empty()).then_some(ip.as_str())).await {
        Ok(res) => Ok(json(StatusCode::CREATED, &res)),
        Err(SubmitError::NotFound) => Err(Problem::new(StatusCode::NOT_FOUND, "no such test")),
        Err(SubmitError::TokenInvalid) => {
            Err(Problem::new(StatusCode::UNAUTHORIZED, "missing or invalid submit token").with_header(header::WWW_AUTHENTICATE, "Bearer"))
        }
        Err(SubmitError::Closed(state)) => {
            Err(Problem::new(StatusCode::CONFLICT, format!("This test is already {state} and cannot take another result.")))
        }
        Err(SubmitError::Expired) => Err(Problem::new(StatusCode::CONFLICT, "This test was not finished in time. Start a new one.")),
        Err(SubmitError::Db(e)) => Err(crate::app::insert_failed(e)),
    }
}
