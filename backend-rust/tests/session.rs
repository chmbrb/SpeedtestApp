//! The optional dashboard login: the gate in front of the API, the login endpoints, the cookie and the attempt limits. Ports
//! backend-go/internal/api/session_test.go. None of it needs the database.

mod common;

use std::sync::Arc;

use axum::Router;
use common::{Resp, TOKENS, send_from};
use speedtest_backend::{
    app::{AppState, router},
    auth::parse_tokens,
    login::{self, COOKIE_NAME},
    ondemand::{config::Config as OdConfig, manager::Manager},
    session_api::{RouteClass, classify},
    store::Store,
};
use sqlx::postgres::PgPoolOptions;

const PASSWORD: &str = "open sesame 123";

fn key() -> Vec<u8> {
    (1..=32u8).collect()
}

fn service(password: &str) -> Arc<login::Service> {
    let cfg = login::Config { password: password.into(), lifetime: std::time::Duration::from_secs(48 * 3600) };
    Arc::new(login::Service::with_key(cfg, &key()).unwrap())
}

fn store() -> Store {
    // no database: anything that needs one fails fast instead of waiting for a connection
    Store::new(
        PgPoolOptions::new()
            .acquire_timeout(std::time::Duration::from_millis(200))
            .connect_lazy("postgres://nobody@127.0.0.1:1/none")
            .unwrap(),
    )
}

struct Opts {
    login: bool,
    trust_proxy: bool,
    /// The ingest rate; 0 lifts every limit, including the login's.
    per_minute: u32,
    static_dir: Option<std::path::PathBuf>,
}

impl Default for Opts {
    fn default() -> Self {
        Self { login: true, trust_proxy: false, per_minute: 30, static_dir: None }
    }
}

fn server(o: Opts) -> Router {
    let mut state = AppState::new(store(), "UTC", o.static_dir).with_auth(parse_tokens(TOKENS), o.per_minute);
    if o.trust_proxy {
        let manager = Manager::new(OdConfig { trust_proxy: true, ..Default::default() }, store(), None);
        state = state.with_ondemand(manager);
    }
    if o.login {
        state = state.with_login(service(PASSWORD));
    }
    router(state)
}

async fn call(app: &Router, method: &str, uri: &str, headers: &[(&str, &str)], body: &str) -> Resp {
    send_from(app, method, uri, "10.0.0.9", headers, body).await
}

async fn get(app: &Router, uri: &str, cookie: Option<&str>) -> Resp {
    let c = cookie.map(|v| format!("{COOKIE_NAME}={v}"));
    let headers: Vec<(&str, &str)> = c.iter().map(|c| ("cookie", c.as_str())).collect();
    call(app, "GET", uri, &headers, "").await
}

fn login_body(password: &str) -> String {
    serde_json::json!({ "password": password }).to_string()
}

/// The session cookie a response sets: its value and the whole Set-Cookie line.
fn set_cookie(r: &Resp) -> Option<(String, String)> {
    let line =
        r.headers.get_all("set-cookie").iter().find_map(|v| v.to_str().ok().filter(|v| v.starts_with(&format!("{COOKIE_NAME}="))))?;
    let value = line.split(';').next()?.trim_start_matches(&format!("{COOKIE_NAME}=")).to_string();
    Some((value, line.to_string()))
}

async fn sign_in(app: &Router) -> String {
    let r = call(app, "POST", "/api/v1/auth/login", &[], &login_body(PASSWORD)).await;
    assert_eq!(r.status, 204, "{}", r.text());
    set_cookie(&r).expect("login set no session cookie").0
}

fn problem_detail(r: &Resp) -> String {
    assert_eq!(r.header("content-type"), "application/problem+json", "{}", r.text());
    let v = r.json();
    assert_eq!(v["status"], r.status.as_u16());
    v["detail"].as_str().unwrap_or("").to_string()
}

async fn session_state(app: &Router, cookie: Option<&str>) -> (bool, bool) {
    let r = get(app, "/api/v1/auth/session", cookie).await;
    assert_eq!(r.status, 200, "{}", r.text());
    assert_eq!(r.header("cache-control"), "no-store");
    let v = r.json();
    (v["required"].as_bool().unwrap(), v["authenticated"].as_bool().unwrap())
}

#[tokio::test]
async fn without_a_login_nothing_changes() {
    let app = server(Opts { login: false, ..Default::default() });
    assert_eq!(session_state(&app, None).await, (false, true));
    // reaches the handler (which needs the database, so it fails with 500 here), but is not stopped by a gate
    assert_ne!(get(&app, "/api/v1/devices", None).await.status, 401);
    let r = call(&app, "POST", "/api/v1/auth/login", &[], r#"{"password":"x"}"#).await;
    assert_eq!(r.status, 404);
    problem_detail(&r);
    assert_eq!(call(&app, "POST", "/api/v1/auth/logout", &[], "").await.status, 204);
}

#[tokio::test]
async fn everything_needs_a_session_except_what_must_be_public() {
    let dir = std::env::temp_dir().join(format!("speedtest-session-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    std::fs::write(dir.join("index.html"), "<html>app</html>").unwrap();
    let app = server(Opts { static_dir: Some(dir.clone()), ..Default::default() });
    assert_eq!(session_state(&app, None).await, (true, false));
    let routes = [
        ("GET", "/api/v1/devices"),
        ("GET", "/api/v1/results"),
        ("GET", "/api/v1/results/11111111-1111-1111-1111-111111111111"),
        ("GET", "/api/v1/stats/summary"),
        ("GET", "/api/v1/stats/timeseries"),
        ("GET", "/api/v1/notifications"),
        ("GET", "/api/v1/capabilities"),
        ("GET", "/api/v1/push/config"),
        ("POST", "/api/v1/push/subscriptions"),
        ("DELETE", "/api/v1/push/subscriptions"),
        ("POST", "/api/v1/push/test"),
        ("GET", "/api/v1/tests"),
        ("POST", "/api/v1/tests"),
        ("GET", "/api/v1/tests/abc"),
        ("DELETE", "/api/v1/tests/abc"),
        ("GET", "/api/v1/tests/abc/events"),
        ("HEAD", "/api/v1/devices"),
        // paths that match nothing are refused too, so the answer does not say what exists
        ("GET", "/api/v1/nothing"),
        ("POST", "/api/v1/devices"),
        ("GET", "/api/v1/health/"),
        ("GET", "/api/v1/auth/login"),
        ("GET", "/api/v1/tests/a/b/result"),
        ("POST", "/api/v1/tests//result"),
    ];
    for (method, path) in routes {
        let r = call(&app, method, path, &[], "").await;
        assert_eq!(r.status, 401, "{method} {path} without a session");
        if method != "HEAD" {
            assert_eq!(problem_detail(&r), "login required", "{method} {path}");
        }
        assert_eq!(r.header("cache-control"), "no-store", "{method} {path}: a refusal must not be cached");
    }
    // the health check has to stay open for container health checks (it needs the database, so it is 503 here, not 401)
    assert_ne!(get(&app, "/api/v1/health", None).await.status, 401);
    // the built frontend must load, or nobody could see the login page
    let r = get(&app, "/", None).await;
    assert_eq!(r.status, 200);
    assert!(r.text().contains("app"));
    std::fs::remove_dir_all(dir).ok();
}

#[tokio::test]
async fn signed_in_requests_pass() {
    let app = server(Opts::default());
    let cookie = sign_in(&app).await;
    // the answers are database errors here, which is fine: the point is that the gate let them through
    for path in ["/api/v1/devices", "/api/v1/results", "/api/v1/stats/summary", "/api/v1/notifications", "/api/v1/capabilities"] {
        assert_ne!(get(&app, path, Some(&cookie)).await.status, 401, "GET {path} signed in");
    }
    assert_eq!(get(&app, "/api/v1/capabilities", Some(&cookie)).await.status, 200);
    assert_eq!(session_state(&app, Some(&cookie)).await, (true, true));
}

#[tokio::test]
async fn own_token_routes_do_not_need_a_session() {
    let app = server(Opts::default());
    // a wrong or missing token is answered by the ingest endpoint itself, not by the login gate
    for headers in [&[("authorization", "Bearer nope")][..], &[][..]] {
        let r = call(&app, "POST", "/api/v1/results", headers, "{}").await;
        assert_eq!(r.status, 401);
        assert_ne!(problem_detail(&r), "login required");
        assert_eq!(r.header("www-authenticate"), "Bearer");
    }
    // a browser submitting the result of its own test authenticates with the test's single-use token
    let r = call(&app, "POST", "/api/v1/tests/abc/result", &[], "{}").await;
    assert_ne!(problem_detail(&r), "login required", "submit result went through the login gate");
    // starting a test: a session, or a bearer token for the handler to check, but not nothing
    let r = call(&app, "POST", "/api/v1/tests", &[], "{}").await;
    assert_eq!((r.status.as_u16(), problem_detail(&r)), (401, "login required".to_string()));
    let r = call(&app, "POST", "/api/v1/tests", &[("authorization", "Bearer tok-laptop")], "{}").await;
    assert_ne!(problem_detail(&r), "login required", "a collector starting a test was stopped by the login gate");
}

#[tokio::test]
async fn login_refusals() {
    let app = server(Opts { per_minute: 0, ..Default::default() });
    let long = format!(r#"{{"password":"{}"}}"#, "x".repeat(5000));
    for body in [r#"{"password":"wrong"}"#.to_string(), r#"{"password":""}"#.into(), login_body(&format!("{PASSWORD} "))] {
        let r = call(&app, "POST", "/api/v1/auth/login", &[], &body).await;
        assert_eq!((r.status.as_u16(), problem_detail(&r).as_str()), (401, "incorrect password"), "{body}");
        assert!(set_cookie(&r).is_none());
    }
    for body in ["", "not json", "{}", r#"{"password":null}"#, r#"{"password":5}"#, "[]", long.as_str()] {
        let r = call(&app, "POST", "/api/v1/auth/login", &[], body).await;
        assert_eq!(r.status, 400, "body {:?}", &body[..body.len().min(30)]);
        problem_detail(&r);
    }
}

#[tokio::test]
async fn session_cookie_attributes() {
    let app = server(Opts::default());
    let r = call(&app, "POST", "/api/v1/auth/login", &[], &login_body(PASSWORD)).await;
    let (_, line) = set_cookie(&r).unwrap();
    for want in ["Path=/", "Max-Age=172800", "HttpOnly", "SameSite=Strict", "Expires="] {
        assert!(line.contains(want), "{line} lacks {want}");
    }
    assert!(!line.contains("Secure"), "over plain http the cookie must not be Secure, or the browser would drop it: {line}");
    assert_eq!(r.header("cache-control"), "no-store");

    // X-Forwarded-Proto counts only behind a proxy we are told to trust
    let h = [("x-forwarded-proto", "https")];
    let r = call(&app, "POST", "/api/v1/auth/login", &h, &login_body(PASSWORD)).await;
    assert!(!set_cookie(&r).unwrap().1.contains("Secure"), "an untrusted X-Forwarded-Proto made the cookie Secure");
    let app = server(Opts { trust_proxy: true, ..Default::default() });
    let r = call(&app, "POST", "/api/v1/auth/login", &h, &login_body(PASSWORD)).await;
    assert!(set_cookie(&r).unwrap().1.contains("; Secure"), "behind a trusted proxy on https the cookie must be Secure");
}

#[tokio::test]
async fn logout_clears_the_cookie() {
    let app = server(Opts::default());
    let c = format!("{COOKIE_NAME}={}", sign_in(&app).await);
    let r = call(&app, "POST", "/api/v1/auth/logout", &[("cookie", &c)], "").await;
    let (value, line) = set_cookie(&r).unwrap();
    assert_eq!(r.status, 204);
    assert!(value.is_empty() && line.contains("Max-Age=0") && line.contains("HttpOnly"), "{line}");
}

#[tokio::test]
async fn bad_expired_and_stale_sessions_are_refused() {
    let app = server(Opts::default());
    let good = sign_in(&app).await;
    let mut tampered = good.clone();
    let last = tampered.pop().unwrap();
    tampered.push(if last == 'A' { 'B' } else { 'A' });
    let other_key = service("another password").issue().0;
    let expired = service(PASSWORD).cookie_for(1_000_000_000);
    for (name, c) in [("tampered", tampered), ("garbage", "abc".into()), ("another password", other_key), ("expired", expired)] {
        assert_eq!(get(&app, "/api/v1/devices", Some(&c)).await.status, 401, "{name}");
        assert!(!session_state(&app, Some(&c)).await.1, "{name} counts as signed in");
    }
    // several cookies, ours not first
    let mixed = format!("theme=dark; {COOKIE_NAME}={good}; other=1");
    assert_ne!(call(&app, "GET", "/api/v1/capabilities", &[("cookie", &mixed)], "").await.status, 401);
}

#[tokio::test]
async fn changing_the_password_ends_every_session() {
    let old = server(Opts::default());
    let cookie = sign_in(&old).await;
    let state = AppState::new(store(), "UTC", None).with_auth(parse_tokens(TOKENS), 30).with_login(service("a brand new password"));
    let new = router(state);
    assert_eq!(get(&new, "/api/v1/devices", Some(&cookie)).await.status, 401);
}

#[tokio::test]
async fn login_attempts_are_limited() {
    let app = server(Opts { trust_proxy: true, ..Default::default() });
    let attempt = |ip: &'static str, pw: &'static str| {
        let app = app.clone();
        async move { call(&app, "POST", "/api/v1/auth/login", &[("x-forwarded-for", ip)], &login_body(pw)).await }
    };
    for i in 1..=5 {
        assert_eq!(attempt("10.0.0.1", "wrong").await.status, 401, "attempt {i}");
    }
    let r = attempt("10.0.0.1", "wrong").await;
    assert_eq!(r.status, 429);
    assert!(!r.header("retry-after").is_empty());
    problem_detail(&r);
    // even the right password is not tried while the limit holds (otherwise the limit would not slow a guesser down)
    assert_eq!(attempt("10.0.0.1", PASSWORD).await.status, 429);
    // another address is not affected
    assert_eq!(attempt("10.0.0.2", PASSWORD).await.status, 204);
}

#[tokio::test]
async fn all_addresses_together_are_limited_too() {
    let app = server(Opts { trust_proxy: true, ..Default::default() });
    let mut limited = false;
    for i in 0..40 {
        let ip = format!("10.1.{}.{}", i / 10, i % 10); // a new address every time
        let r = call(&app, "POST", "/api/v1/auth/login", &[("x-forwarded-for", &ip)], &login_body("wrong")).await;
        if r.status == 429 {
            limited = true;
            break;
        }
    }
    assert!(limited, "40 guesses from 40 addresses were all answered");
}

#[tokio::test]
async fn ingest_rate_zero_lifts_the_login_limits() {
    let app = server(Opts { per_minute: 0, ..Default::default() });
    for i in 1..=50 {
        assert_eq!(call(&app, "POST", "/api/v1/auth/login", &[], &login_body("wrong")).await.status, 401, "attempt {i}");
    }
}

#[test]
fn classify_table() {
    use RouteClass::*;
    use axum::http::Method as M;
    let table = [
        (M::GET, "/", Public),
        (M::GET, "/assets/app.js", Public),
        (M::GET, "/ca.crt", Public),
        (M::GET, "/api/v1/health", Public),
        (M::HEAD, "/api/v1/health", Public),
        (M::GET, "/api/v1/auth/session", Public),
        (M::POST, "/api/v1/auth/login", Public),
        (M::POST, "/api/v1/auth/logout", Public),
        (M::POST, "/api/v1/results", OwnToken),
        (M::POST, "/api/v1/tests/abc/result", OwnToken),
        (M::POST, "/api/v1/tests", TokenOrSession),
        (M::GET, "/api/v1/results", Protected),
        (M::GET, "/api/v1/tests", Protected),
        (M::DELETE, "/api/v1/tests/abc", Protected),
        (M::GET, "/api/v1/tests/abc/result", Protected),
        (M::POST, "/api/v1/tests/abc/result/", Protected),
        (M::POST, "/api/v1/tests//result", Protected),
        (M::POST, "/api/v1/tests/a/b/result", Protected),
        (M::POST, "/api/v1/health", Protected),
        (M::GET, "/api/v1/auth/login", Protected),
        (M::POST, "/api/v1/auth/session", Protected),
        (M::GET, "/api/v1/auth/session/", Protected),
        (M::GET, "/api/", Protected),
        (M::GET, "/api/v2/anything", Protected),
    ];
    for (method, path, want) in table {
        assert_eq!(classify(&method, path), want, "{method} {path}");
    }
}

// The gate looks at the path before the router does, so a path written another way must not get past it: encoded characters,
// dot segments, doubled slashes, a trailing slash, a path hidden in the query. Whatever the router makes of these, nobody without
// a session may get an answer with data.
#[tokio::test]
async fn odd_paths_do_not_get_past_the_gate() {
    let app = server(Opts::default());
    let targets = [
        "/%61pi/v1/devices",
        "/api/v1/%64evices",
        "/api%2Fv1/devices",
        "/api/v1%2Fdevices",
        "/api/v1/devices%2F",
        "/api/v1/devices/",
        "/api/v1/./devices",
        "/api/v1/../v1/devices",
        "/api/v1/health/../devices",
        "//api/v1/devices",
        "/api//v1/devices",
        "/api/v1/devices?x=/api/v1/health",
        "/API/v1/devices",
        "/api/v1/results/%2e%2e/devices",
        "/api/v1/auth/session/../../devices",
        "/api/v1/auth/login/../../devices",
        "/api/v1/tests/x/result/../../../devices",
    ];
    for target in targets {
        for method in ["GET", "POST", "DELETE"] {
            let r = call(&app, method, target, &[], "").await;
            let code = r.status.as_u16();
            // refused (401), not found or not allowed; never an answer from a handler (which, with no database here, would be a 5xx)
            assert!([401, 404, 405].contains(&code), "{method} {target} without a session: {code} {}", r.text());
        }
    }
}
