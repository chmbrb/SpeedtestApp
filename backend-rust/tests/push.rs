//! Web Push: the subscription API and delivery, against a real database and a fake push service (a plain local HTTP server that
//! records what it is sent). Port of backend-go/internal/push and api/push tests.

mod common;

use std::sync::{Arc, Mutex};

use axum::{
    Router,
    body::Bytes,
    extract::State,
    http::{HeaderMap, StatusCode},
    routing::post,
};
use common::Harness;
use serde_json::{Value, json};
use speedtest_backend::{
    app::{AppState, router},
    auth::parse_tokens,
    notify::sender::{Message, SendError, Sender},
    push::{
        Config, MAX_SUBSCRIPTIONS, PushError, Service,
        webpush::{
            testing::{Browser, verify_vapid},
            unb64,
        },
    },
    store::Store,
};

#[derive(Clone, Debug)]
struct Got {
    path: String,
    headers: HeaderMap,
    body: Vec<u8>,
}

/// A push service: answers every POST with `status` (per path override via `/gone/..` -> 410, `/missing/..` -> 404, `/fail/..` -> 500,
/// `/redirect/..` -> 302 to `/elsewhere`).
struct Fake {
    base: String,
    got: Arc<Mutex<Vec<Got>>>,
}

async fn take(
    State(got): State<Arc<Mutex<Vec<Got>>>>,
    uri: axum::http::Uri,
    headers: HeaderMap,
    body: Bytes,
) -> (StatusCode, HeaderMap, String) {
    let path = uri.path().to_string();
    let host = headers.get("host").and_then(|h| h.to_str().ok()).unwrap_or_default().to_string();
    got.lock().unwrap().push(Got { path: path.clone(), headers, body: body.to_vec() });
    let mut h = HeaderMap::new();
    if path.starts_with("/redirect") {
        h.insert("location", "/elsewhere".parse().unwrap());
        return (StatusCode::FOUND, h, String::new());
    }
    let code = if path.starts_with("/gone") {
        StatusCode::GONE
    } else if path.starts_with("/missing") {
        StatusCode::NOT_FOUND
    } else if path.starts_with("/fail") {
        StatusCode::INTERNAL_SERVER_ERROR
    } else {
        StatusCode::CREATED
    };
    // like a real push service, say why; this one also repeats the address it was sent to (which must never reach the log)
    let body = if path.starts_with("/gone") { format!("NotRegistered http://{host}{path}") } else { String::new() };
    (code, h, body)
}

impl Fake {
    async fn start() -> Fake {
        let got = Arc::new(Mutex::new(Vec::new()));
        let app = Router::new().fallback(post(take)).with_state(got.clone());
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let base = format!("http://{}", listener.local_addr().unwrap());
        tokio::spawn(async move { axum::serve(listener, app).await.ok() });
        Fake { base, got }
    }
    fn got(&self) -> Vec<Got> {
        self.got.lock().unwrap().clone()
    }
    fn url(&self, path: &str) -> String {
        format!("{}{}", self.base, path)
    }
}

fn config() -> Config {
    Config {
        subject: "https://speedtest.example".into(),
        allowed_hosts: vec!["127.0.0.1".into()],
        require_https: false,
        ..Config::default()
    }
}

struct Rig {
    h: Harness,
    store: Store,
    svc: Arc<Service>,
    fake: Fake,
}

async fn rig() -> Option<Rig> {
    let h = Harness::new("UTC", None).await?;
    let store = Store::new(h.pool.clone());
    let svc = Service::new(config(), store.clone()).await.unwrap();
    Some(Rig { h, store, svc, fake: Fake::start().await })
}

macro_rules! rig {
    () => {
        match rig().await {
            Some(r) => r,
            None => return,
        }
    };
}

impl Rig {
    async fn subscribe(&self, path: &str) -> Browser {
        let b = Browser::new();
        self.svc.subscribe(&self.fake.url(path), &b.p256dh_b64(), &b.auth_b64(), "test agent").await.unwrap();
        b
    }
    fn msg(title: &str, body: &str, priority: u8) -> Message {
        Message { title: title.into(), body: body.into(), priority, tags: vec![] }
    }
}

fn field_of(e: PushError) -> String {
    match e {
        PushError::Validation(v) => v.field.to_string(),
        other => panic!("not a validation error: {other:?}"),
    }
}

#[tokio::test]
async fn keys_are_created_once_and_reused() {
    let Some(h) = Harness::new("UTC", None).await else { return };
    let store = Store::new(h.pool.clone());
    let a = Service::new(config(), store.clone()).await.unwrap();
    let b = Service::new(config(), store.clone()).await.unwrap();
    assert_eq!(a.public_key(), b.public_key());
    let k = unb64(a.public_key()).unwrap();
    assert!(k.len() == 65 && k[0] == 4);
}

#[tokio::test]
async fn subscribe_validation() {
    let r = rig!();
    let good = Browser::new();
    let (p, a) = (good.p256dh_b64(), good.auth_b64());
    let ok = r.fake.url("/ok");
    for (endpoint, field) in [
        ("", "endpoint"),
        ("not a url", "endpoint"),
        ("https://evil.example/push", "endpoint"), // https but not a known push service
        ("http://127.0.0.1:1/x#frag", "endpoint"),
        ("ftp://127.0.0.1/x", "endpoint"),
        ("http://user:pw@127.0.0.1:1/x", "endpoint"),
        ("http://127.0.0.1:1/a\u{0}b", "endpoint"), // would be a 500 from the database
        ("http://127.0.0.1:1/a\nb", "endpoint"),
        ("http://127.0.0.1\\@evil.example/x", "endpoint"),
    ] {
        let e = r.svc.subscribe(endpoint, &p, &a, "").await.unwrap_err();
        assert_eq!(field_of(e), field, "{endpoint}");
    }
    let long = format!("{ok}/{}", "a".repeat(2100));
    assert_eq!(field_of(r.svc.subscribe(&long, &p, &a, "").await.unwrap_err()), "endpoint");
    for (p256dh, field) in [("", "keys.p256dh"), ("AAAA", "keys.p256dh"), ("***", "keys.p256dh")] {
        assert_eq!(field_of(r.svc.subscribe(&ok, p256dh, &a, "").await.unwrap_err()), field);
    }
    for auth in ["", "AAAA", &"A".repeat(40)] {
        assert_eq!(field_of(r.svc.subscribe(&ok, &p, auth, "").await.unwrap_err()), "keys.auth");
    }
    r.svc.subscribe(&ok, &p, &a, &"x".repeat(1000)).await.unwrap();
    assert_eq!(r.store.subscription(&ok).await.unwrap().unwrap().user_agent.chars().count(), 300);
}

#[tokio::test]
async fn https_is_required_in_production_configuration() {
    let Some(h) = Harness::new("UTC", None).await else { return };
    let svc =
        Service::new(Config { allowed_hosts: vec!["127.0.0.1".into()], ..Config::default() }, Store::new(h.pool.clone())).await.unwrap();
    let b = Browser::new();
    let e = svc.subscribe("http://127.0.0.1:1/x", &b.p256dh_b64(), &b.auth_b64(), "").await.unwrap_err();
    assert_eq!(field_of(e), "endpoint");
    // the built-in allow-list
    for (url, ok) in [
        ("https://fcm.googleapis.com/fcm/send/abc", true),
        ("https://updates.push.services.mozilla.com/wpush/v2/abc", true),
        ("https://web.push.apple.com/abc", true),
        ("https://abc.notify.windows.com/?token=x", true),
        ("https://evil.example/fcm.googleapis.com", false),
        ("https://fcm.googleapis.com.evil.example/x", false),
        ("https://notify.windows.com.evil.example/x", false),
    ] {
        let svc = Service::new(Config::default(), Store::new(h.pool.clone())).await.unwrap();
        let r = svc.subscribe(url, &b.p256dh_b64(), &b.auth_b64(), "").await;
        assert_eq!(r.is_ok(), ok, "{url}: {r:?}");
    }
}

#[tokio::test]
async fn limit_and_refresh_at_the_limit() {
    let r = rig!();
    let b = Browser::new();
    for i in 0..MAX_SUBSCRIPTIONS {
        r.svc.subscribe(&r.fake.url(&format!("/d{i}")), &b.p256dh_b64(), &b.auth_b64(), "").await.unwrap();
    }
    let e = r.svc.subscribe(&r.fake.url("/one-too-many"), &b.p256dh_b64(), &b.auth_b64(), "").await.unwrap_err();
    assert!(matches!(e, PushError::TooMany), "{e:?}");
    // refreshing a known endpoint still works when full
    let fresh = Browser::new();
    r.svc.subscribe(&r.fake.url("/d3"), &fresh.p256dh_b64(), &fresh.auth_b64(), "").await.unwrap();
    assert_eq!(r.store.subscription(&r.fake.url("/d3")).await.unwrap().unwrap().p256dh, fresh.p256dh_b64());
    assert_eq!(r.store.list_subscriptions().await.unwrap().len() as i64, MAX_SUBSCRIPTIONS);
}

#[tokio::test]
async fn concurrent_subscribers_cannot_exceed_the_limit() {
    let r = rig!();
    let b = Browser::new();
    let mut tasks = Vec::new();
    for i in 0..(MAX_SUBSCRIPTIONS + 20) {
        let (svc, url, p, a) = (r.svc.clone(), r.fake.url(&format!("/c{i}")), b.p256dh_b64(), b.auth_b64());
        tasks.push(tokio::spawn(async move { svc.subscribe(&url, &p, &a, "").await }));
    }
    let mut ok = 0;
    for t in tasks {
        if t.await.unwrap().is_ok() {
            ok += 1;
        }
    }
    assert_eq!(ok, MAX_SUBSCRIPTIONS);
    assert_eq!(r.store.list_subscriptions().await.unwrap().len() as i64, MAX_SUBSCRIPTIONS);
}

#[tokio::test]
async fn a_message_arrives_encrypted_and_signed() {
    let r = rig!();
    let browser = r.subscribe("/ok/1").await;
    r.svc.send(&Rig::msg("Slow download: server (wired)", "410 Mbps", 4)).await.unwrap();
    let got = r.fake.got();
    assert_eq!(got.len(), 1);
    let g = &got[0];
    let h = |n: &str| g.headers.get(n).and_then(|v| v.to_str().ok()).unwrap_or("").to_string();
    assert_eq!(h("content-encoding"), "aes128gcm");
    assert_eq!(h("content-type"), "application/octet-stream");
    assert_eq!(h("urgency"), "high");
    assert_eq!(h("ttl"), (6 * 3600).to_string());
    let claims = verify_vapid(&h("authorization"), r.svc.public_key());
    assert_eq!(claims["aud"], r.fake.base);
    assert_eq!(claims["sub"], "https://speedtest.example");
    let exp = claims["exp"].as_u64().unwrap();
    let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).unwrap().as_secs();
    assert!(exp > now && exp <= now + 24 * 3600, "exp {exp} vs {now}");
    let payload: Value = serde_json::from_slice(&browser.decrypt(&g.body)).unwrap();
    assert_eq!(payload, json!({"title": "Slow download: server (wired)", "body": "410 Mbps", "url": "/"}));

    // a low-priority message is not urgent
    r.svc.send(&Rig::msg("Recovered", "ok", 2)).await.unwrap();
    assert_eq!(r.fake.got()[1].headers["urgency"], "normal");
}

#[tokio::test]
async fn long_text_is_clipped() {
    let r = rig!();
    let browser = r.subscribe("/ok/1").await;
    r.svc.send(&Rig::msg(&"t".repeat(500), &"é".repeat(2000), 3)).await.unwrap();
    let payload: Value = serde_json::from_slice(&browser.decrypt(&r.fake.got()[0].body)).unwrap();
    assert_eq!(payload["title"].as_str().unwrap().chars().count(), 100);
    assert_eq!(payload["body"].as_str().unwrap().chars().count(), 300);
}

#[tokio::test]
async fn nobody_subscribed_is_not_a_failure() {
    let r = rig!();
    assert!(matches!(r.svc.send(&Rig::msg("t", "b", 3)).await, Err(SendError::NoRecipients)));
}

#[tokio::test]
async fn expired_subscriptions_are_removed() {
    let r = rig!();
    let live = r.subscribe("/ok/live").await;
    r.subscribe("/gone/1").await;
    r.subscribe("/missing/1").await;
    // others are delivered, so the message as a whole is fine
    r.svc.send(&Rig::msg("t", "b", 3)).await.unwrap();
    let left: Vec<String> = r.store.list_subscriptions().await.unwrap().into_iter().map(|s| s.endpoint).collect();
    assert_eq!(left, vec![r.fake.url("/ok/live")]);
    let _ = live;
}

/// Collects what `tracing` prints on this thread (tokio tests run on one thread), so a test can read the log.
#[derive(Clone, Default)]
struct LogBuf(Arc<Mutex<Vec<u8>>>);

impl std::io::Write for LogBuf {
    fn write(&mut self, b: &[u8]) -> std::io::Result<usize> {
        self.0.lock().unwrap().extend_from_slice(b);
        Ok(b.len())
    }
    fn flush(&mut self) -> std::io::Result<()> {
        Ok(())
    }
}

impl<'a> tracing_subscriber::fmt::MakeWriter<'a> for LogBuf {
    type Writer = LogBuf;
    fn make_writer(&'a self) -> LogBuf {
        self.clone()
    }
}

#[tokio::test]
async fn a_gone_subscription_logs_what_the_push_service_said() {
    let r = rig!();
    r.subscribe("/gone/1").await;
    let endpoint = r.fake.url("/gone/1");
    let logs = LogBuf::default();
    let _guard = tracing::subscriber::set_default(tracing_subscriber::fmt().with_writer(logs.clone()).with_ansi(false).finish());
    let err = r.svc.send_test(&endpoint).await.unwrap_err();
    assert!(matches!(err, PushError::Refused(_)), "{err:?}");
    let out = String::from_utf8(logs.0.lock().unwrap().clone()).unwrap();
    assert!(out.contains("status=410") && out.contains("NotRegistered"), "the log lacks the status or the answer: {out}");
    assert!(!out.contains(&endpoint), "the log leaks the endpoint: {out}");
    assert!(out.contains("<subscription>"), "{out}");
}

#[tokio::test]
async fn five_failures_in_a_row_remove_a_subscription_and_a_success_resets_the_count() {
    let r = rig!();
    r.subscribe("/fail/1").await;
    let url = r.fake.url("/fail/1");
    for i in 1..=4 {
        let e = r.svc.send(&Rig::msg("t", "b", 3)).await.unwrap_err();
        assert!(matches!(e, SendError::Failed(_)));
        assert_eq!(r.store.subscription(&url).await.unwrap().unwrap().failures, i);
    }
    // the fifth failure removes it
    assert!(r.svc.send(&Rig::msg("t", "b", 3)).await.is_err());
    assert!(r.store.subscription(&url).await.unwrap().is_none());

    r.subscribe("/ok/2").await;
    let url = r.fake.url("/ok/2");
    sqlx::query("UPDATE push_subscriptions SET failures = 4").execute(&r.h.pool).await.unwrap();
    r.svc.send(&Rig::msg("t", "b", 3)).await.unwrap();
    assert_eq!(r.store.subscription(&url).await.unwrap().unwrap().failures, 0);
}

#[tokio::test]
async fn redirects_are_not_followed() {
    let r = rig!();
    r.subscribe("/redirect/1").await;
    let e = r.svc.send(&Rig::msg("t", "b", 3)).await.unwrap_err();
    assert!(matches!(e, SendError::Failed(_)));
    assert!(r.fake.got().iter().all(|g| g.path != "/elsewhere"), "the redirect was followed");
}

#[tokio::test]
async fn errors_do_not_reveal_the_endpoint() {
    let r = rig!();
    // an address nothing listens on
    let b = Browser::new();
    let dead = "http://127.0.0.1:1/secret-endpoint-token";
    r.svc.subscribe(dead, &b.p256dh_b64(), &b.auth_b64(), "").await.unwrap();
    let e = r.svc.send(&Rig::msg("t", "b", 3)).await.unwrap_err();
    assert!(!format!("{e:?}").contains("secret-endpoint-token"), "{e:?}");
}

#[tokio::test]
async fn test_message_and_unsubscribe() {
    let r = rig!();
    let browser = r.subscribe("/ok/1").await;
    let url = r.fake.url("/ok/1");
    r.svc.send_test(&url).await.unwrap();
    let payload: Value = serde_json::from_slice(&browser.decrypt(&r.fake.got()[0].body)).unwrap();
    assert_eq!(payload["test"], true);
    assert_eq!(payload["title"], "Notifications are on");
    assert_eq!(r.fake.got()[0].headers["urgency"], "high");
    assert!(matches!(r.svc.send_test(&r.fake.url("/ok/unknown")).await, Err(PushError::Unknown)));
    r.svc.unsubscribe(&url).await.unwrap();
    r.svc.unsubscribe(&url).await.unwrap(); // idempotent
    assert!(r.store.subscription(&url).await.unwrap().is_none());
}

#[tokio::test]
async fn thirty_subscribers_all_receive_the_message() {
    let r = rig!();
    let mut browsers = Vec::new();
    for i in 0..30 {
        browsers.push((i, r.subscribe(&format!("/ok/{i}")).await));
    }
    r.svc.send(&Rig::msg("everyone", "hello", 3)).await.unwrap();
    let got = r.fake.got();
    assert_eq!(got.len(), 30);
    for (i, b) in browsers {
        let g = got.iter().find(|g| g.path == format!("/ok/{i}")).expect("delivered");
        let p: Value = serde_json::from_slice(&b.decrypt(&g.body)).unwrap();
        assert_eq!(p["title"], "everyone");
    }
}

// ---- the HTTP API ------------------------------------------------------------------------------------------------------------

fn app(h: &Harness, svc: Option<Arc<Service>>) -> Router {
    let mut state = AppState::new(Store::new(h.pool.clone()), "UTC", None).with_auth(parse_tokens(common::TOKENS), 0);
    if let Some(s) = svc {
        state = state.with_pusher(s);
    }
    router(state)
}

#[tokio::test]
async fn api_when_push_is_off() {
    let Some(h) = Harness::new("UTC", None).await else { return };
    let app = app(&h, None);
    let c = common::call(&app, "GET", "/api/v1/push/config").await;
    assert_eq!((c.status.as_u16(), c.json()), (200, json!({"enabled": false})));
    for path in ["subscriptions", "test"] {
        let r = common::send(&app, "POST", &format!("/api/v1/push/{path}"), None, "{}").await;
        assert_eq!(r.status.as_u16(), 503, "{path}");
        assert_eq!(r.header("content-type"), "application/problem+json");
    }
    assert_eq!(common::send(&app, "DELETE", "/api/v1/push/subscriptions", None, "{}").await.status.as_u16(), 503);
}

#[tokio::test]
async fn api_subscribe_test_and_unsubscribe() {
    let r = rig!();
    let app = app(&r.h, Some(r.svc.clone()));
    let c = common::call(&app, "GET", "/api/v1/push/config").await;
    assert_eq!(c.json(), json!({"enabled": true, "publicKey": r.svc.public_key()}));

    let b = Browser::new();
    let url = r.fake.url("/ok/api");
    let sub = json!({"endpoint": url, "keys": {"p256dh": b.p256dh_b64(), "auth": b.auth_b64()}}).to_string();
    assert_eq!(common::send(&app, "POST", "/api/v1/push/subscriptions", None, &sub).await.status.as_u16(), 204);
    assert_eq!(common::send(&app, "POST", "/api/v1/push/subscriptions", None, &sub).await.status.as_u16(), 204, "again: refresh");
    assert_eq!(r.store.list_subscriptions().await.unwrap().len(), 1);

    let ep = json!({"endpoint": url}).to_string();
    assert_eq!(common::send(&app, "POST", "/api/v1/push/test", None, &ep).await.status.as_u16(), 204);
    assert_eq!(r.fake.got().len(), 1);
    let unknown = json!({"endpoint": r.fake.url("/ok/nobody")}).to_string();
    assert_eq!(common::send(&app, "POST", "/api/v1/push/test", None, &unknown).await.status.as_u16(), 404);
    // a push service that refuses -> 502
    let bad = Browser::new();
    let bad_url = r.fake.url("/fail/api");
    r.svc.subscribe(&bad_url, &bad.p256dh_b64(), &bad.auth_b64(), "").await.unwrap();
    let res = common::send(&app, "POST", "/api/v1/push/test", None, &json!({"endpoint": bad_url}).to_string()).await;
    assert_eq!(res.status.as_u16(), 502);
    assert!(!res.text().contains("/fail/api"), "{}", res.text());

    assert_eq!(common::send(&app, "DELETE", "/api/v1/push/subscriptions", None, &ep).await.status.as_u16(), 204);
    assert_eq!(common::send(&app, "DELETE", "/api/v1/push/subscriptions", None, &ep).await.status.as_u16(), 204);
    assert!(r.store.subscription(&url).await.unwrap().is_none());
}

#[tokio::test]
async fn api_rejects_bad_requests() {
    let r = rig!();
    let app = app(&r.h, Some(r.svc.clone()));
    let b = Browser::new();
    let keys = json!({"p256dh": b.p256dh_b64(), "auth": b.auth_b64()});
    let post = |body: String| {
        let app = app.clone();
        async move { common::send(&app, "POST", "/api/v1/push/subscriptions", None, &body).await }
    };
    for body in ["", "{", "[]", "null", "\"x\"", r#"{"endpoint": 5}"#, r#"{"endpoint": "x", "keys": 3}"#] {
        assert_eq!(post(body.to_string()).await.status.as_u16(), 400, "{body}");
    }
    let res = post(json!({"endpoint": "https://evil.example/x", "keys": keys}).to_string()).await;
    assert_eq!(res.status.as_u16(), 400);
    assert_eq!(res.json()["errors"][0]["field"], "endpoint");
    let res = post(json!({"endpoint": r.fake.url("/ok"), "keys": {"p256dh": "AAAA", "auth": b.auth_b64()}}).to_string()).await;
    assert_eq!(res.json()["errors"][0]["field"], "keys.p256dh");
    // too big a body
    assert_eq!(post(format!("{{\"endpoint\":\"{}\"}}", "a".repeat(9000))).await.status.as_u16(), 400);
    // endpoint-only calls
    for path in ["test"] {
        let url = format!("/api/v1/push/{path}");
        for body in ["", "{}", r#"{"endpoint": ""}"#] {
            assert_eq!(common::send(&app, "POST", &url, None, body).await.status.as_u16(), 400, "{body}");
        }
        let res = common::send(&app, "POST", &url, None, &json!({"endpoint": "a\u{0}b"}).to_string()).await;
        assert_eq!(res.status.as_u16(), 400);
        let res = common::send(&app, "POST", &url, None, &json!({"endpoint": "a".repeat(2049)}).to_string()).await;
        assert_eq!(res.status.as_u16(), 400);
    }
    // the database never saw any of it
    assert!(r.store.list_subscriptions().await.unwrap().is_empty());
}

#[tokio::test]
async fn api_is_rate_limited() {
    let r = rig!();
    // no with_auth(.., 0): that lifts the limits
    let app = router(AppState::new(Store::new(r.h.pool.clone()), "UTC", None).with_pusher(r.svc.clone()));
    let mut last = 0;
    let mut retry = String::new();
    for _ in 0..30 {
        let res = common::send(&app, "POST", "/api/v1/push/subscriptions", None, "{}").await;
        last = res.status.as_u16();
        if last == 429 {
            retry = res.header("retry-after").to_string();
            break;
        }
    }
    assert_eq!(last, 429);
    assert!(retry.parse::<u64>().is_ok_and(|n| n >= 1), "Retry-After: {retry:?}");
}
