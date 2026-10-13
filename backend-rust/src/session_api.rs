//! The login endpoints and the gate in front of the API. Ports backend-go/internal/api/session.go.

use std::net::SocketAddr;

use axum::{
    extract::{ConnectInfo, Request, State},
    http::{HeaderMap, HeaderValue, Method, StatusCode, header},
    middleware::Next,
    response::{IntoResponse, Response},
};
use serde::Serialize;

use crate::{
    app::AppState,
    body::first_json_value,
    gojson::{self, Members},
    login::{COOKIE_NAME, Service},
    ondemand_api::client_ip,
    problem::Problem,
};

/// What a request needs when the login is on.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum RouteClass {
    /// A session.
    Protected,
    /// Reachable without one: the page itself, the health check and the login endpoints.
    Public,
    /// Has its own bearer token, which the handler checks (a collector sending a result, a browser submitting the result of its
    /// own test).
    OwnToken,
    /// A session, or a bearer token the handler checks (a collector starting a test on its device).
    TokenOrSession,
}

/// Decides what a request needs. Anything not known needs a session, so a route added later is protected unless someone
/// deliberately lists it here. backend-go/internal/api/session.go has the same table.
pub fn classify(method: &Method, path: &str) -> RouteClass {
    if !path.starts_with("/api/") {
        return RouteClass::Public; // the built frontend: the login page must load before anybody can log in
    }
    let get_or_head = method == Method::GET || method == Method::HEAD;
    match path {
        "/api/v1/health" if get_or_head => RouteClass::Public,
        "/api/v1/auth/session" if method == Method::GET => RouteClass::Public,
        "/api/v1/auth/login" | "/api/v1/auth/logout" if method == Method::POST => RouteClass::Public,
        "/api/v1/results" if method == Method::POST => RouteClass::OwnToken,
        "/api/v1/tests" if method == Method::POST => RouteClass::TokenOrSession,
        _ if method == Method::POST && is_test_result_path(path) => RouteClass::OwnToken,
        _ => RouteClass::Protected,
    }
}

/// `/api/v1/tests/{id}/result` with a real, single-segment id.
fn is_test_result_path(path: &str) -> bool {
    path.strip_prefix("/api/v1/tests/").and_then(|rest| rest.strip_suffix("/result")).is_some_and(|id| !id.is_empty() && !id.contains('/'))
}

/// The value of the first cookie called `name`.
fn cookie_value<'a>(headers: &'a HeaderMap, name: &str) -> Option<&'a str> {
    headers
        .get_all(header::COOKIE)
        .iter()
        .filter_map(|v| v.to_str().ok())
        .flat_map(|line| line.split(';'))
        .filter_map(|pair| pair.trim().split_once('='))
        .find(|(n, _)| *n == name)
        .map(|(_, v)| v)
}

fn has_session(login: &Service, headers: &HeaderMap) -> bool {
    cookie_value(headers, COOKIE_NAME).is_some_and(|v| login.valid(v))
}

fn no_store(mut res: Response) -> Response {
    res.headers_mut().insert(header::CACHE_CONTROL, HeaderValue::from_static("no-store"));
    res
}

/// Refuses what needs a session when there is none. It runs before routing, so it also covers paths that match no route.
pub async fn require_login(State(s): State<AppState>, req: Request, next: Next) -> Response {
    let Some(login) = s.login.as_deref() else { return next.run(req).await };
    let has_bearer = req.headers().get(header::AUTHORIZATION).is_some_and(|v| !v.is_empty());
    let allowed = match classify(req.method(), req.uri().path()) {
        RouteClass::Public | RouteClass::OwnToken => true,
        RouteClass::TokenOrSession => has_bearer || has_session(login, req.headers()),
        RouteClass::Protected => has_session(login, req.headers()),
    };
    if allowed {
        return next.run(req).await;
    }
    no_store(Problem::new(StatusCode::UNAUTHORIZED, "login required").into_response())
}

/// Whether the browser reached us over https, which decides the cookie's Secure flag: a Secure cookie set over plain http would
/// simply be thrown away by the browser, and nobody could log in. Only a proxy we are told to trust can say so.
fn secure_request(s: &AppState, headers: &HeaderMap) -> bool {
    s.trust_proxy()
        && headers.get("x-forwarded-proto").and_then(|v| v.to_str().ok()).is_some_and(|v| v.trim().eq_ignore_ascii_case("https"))
}

/// A `Set-Cookie` value for the session cookie.
fn set_cookie(value: &str, expires_unix: i64, max_age: i64, secure: bool) -> HeaderValue {
    let expires = chrono::DateTime::from_timestamp(expires_unix, 0).unwrap_or_default().format("%a, %d %b %Y %H:%M:%S GMT");
    let secure = if secure { "; Secure" } else { "" };
    HeaderValue::from_str(&format!(
        "{COOKIE_NAME}={value}; Path=/; Expires={expires}; Max-Age={max_age}; HttpOnly; SameSite=Strict{secure}"
    ))
    .unwrap_or_else(|_| HeaderValue::from_static(""))
}

#[derive(Serialize)]
struct SessionState {
    // alphabetical, as Go writes the keys of its map
    authenticated: bool,
    required: bool,
}

/// Tells the page whether it must show a login, and whether this browser is signed in.
pub async fn session(State(s): State<AppState>, req: Request) -> Response {
    let required = s.login.is_some();
    let authenticated = s.login.as_deref().is_none_or(|l| has_session(l, req.headers()));
    let body = serde_json::to_vec(&SessionState { authenticated, required }).unwrap_or_default();
    no_store(([(header::CONTENT_TYPE, "application/json")], body).into_response())
}

fn peer(parts: &axum::http::request::Parts) -> Option<SocketAddr> {
    parts.extensions.get::<ConnectInfo<SocketAddr>>().map(|c| c.0)
}

pub async fn login(State(s): State<AppState>, req: Request) -> Response {
    let (parts, body) = req.into_parts();
    no_store(try_login(&s, &parts, body).await.unwrap_or_else(IntoResponse::into_response))
}

async fn try_login(s: &AppState, parts: &axum::http::request::Parts, body: axum::body::Body) -> Result<Response, Problem> {
    let Some(login) = s.login.as_deref() else {
        return Err(Problem::new(StatusCode::NOT_FOUND, "the login is not turned on on this server"));
    };
    let ip = client_ip(s.trust_proxy(), &parts.headers, peer(parts));
    for (limiter, key) in [(&s.login_limiter, ip.as_str()), (&s.login_global, "all")] {
        if let Err(retry) = limiter.allow(key) {
            return Err(Problem::new(StatusCode::TOO_MANY_REQUESTS, "too many login attempts; wait a moment")
                .with_header(header::RETRY_AFTER, &retry.to_string()));
        }
    }
    let bad = || Problem::new(StatusCode::BAD_REQUEST, "body must be JSON with a password");
    let raw = first_json_value(body, 2048).await.ok_or_else(bad)?;
    let top = Members::decode(Some(&raw)).map_err(|_| bad())?;
    let field = top.field("password");
    if field.is_none() || gojson::is_null(field) {
        return Err(bad());
    }
    let password = gojson::string(field).map_err(|_| bad())?;
    if !login.check_password(&password) {
        tracing::warn!(ip = %ip, "login refused: wrong password");
        return Err(Problem::new(StatusCode::UNAUTHORIZED, "incorrect password"));
    }
    let (value, expires) = login.issue();
    let mut res = StatusCode::NO_CONTENT.into_response();
    res.headers_mut()
        .insert(header::SET_COOKIE, set_cookie(&value, expires, login.lifetime().as_secs() as i64, secure_request(s, &parts.headers)));
    Ok(res)
}

/// Forgets the cookie in this browser. (A session is stateless: copies of the cookie elsewhere stay valid until they expire.
/// Changing DASHBOARD_PASSWORD ends every session at once.)
pub async fn logout(State(s): State<AppState>, req: Request) -> Response {
    let mut res = StatusCode::NO_CONTENT.into_response();
    res.headers_mut().insert(header::SET_COOKIE, set_cookie("", 0, 0, secure_request(&s, req.headers())));
    no_store(res)
}
