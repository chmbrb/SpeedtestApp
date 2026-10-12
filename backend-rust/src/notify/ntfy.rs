//! Publishing to ntfy (https://ntfy.sh) with its JSON API (a POST of `{"topic": ...}` to the server root), which, unlike the
//! header API, has no trouble with non-ASCII titles. Ports backend-go/internal/notify/ntfy.go.

use std::time::Duration;

use async_trait::async_trait;
use serde::Serialize;

use super::{
    config::Config,
    sender::{Message, SendError, Sender},
};

pub struct Ntfy {
    cfg: Config,
    client: reqwest::Client,
    /// Waits before the second and third attempts.
    retry_delays: Vec<Duration>,
}

#[derive(Serialize, Debug)]
struct Publish<'a> {
    topic: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    title: &'a str,
    message: &'a str,
    #[serde(skip_serializing_if = "is_zero")]
    priority: u8,
    #[serde(skip_serializing_if = "<[String]>::is_empty")]
    tags: &'a [String],
    #[serde(skip_serializing_if = "str::is_empty")]
    click: &'a str,
}

fn is_zero(n: &u8) -> bool {
    *n == 0
}

impl Ntfy {
    pub fn new(cfg: Config) -> Self {
        let client = reqwest::Client::builder().timeout(Duration::from_secs(10)).build().unwrap_or_default();
        Self { cfg, client, retry_delays: vec![Duration::from_secs(1), Duration::from_secs(3)] }
    }

    #[cfg(test)]
    pub fn without_waiting(mut self) -> Self {
        self.retry_delays = vec![Duration::ZERO, Duration::ZERO];
        self
    }

    /// Keeps the topic (which works like a password on public ntfy servers) and credentials out of logs and the UI.
    fn redact(&self, text: &str) -> String {
        let mut s = text.to_string();
        for secret in [&self.cfg.topic, &self.cfg.token, &self.cfg.password] {
            if !secret.is_empty() {
                s = s.replace(secret.as_str(), "***");
            }
        }
        s
    }

    /// One attempt. `Err((retry, message))`: only network errors, 429 and 5xx are worth trying again; a 4xx (bad topic, bad
    /// token) will not get better by waiting.
    async fn once(&self, body: &[u8]) -> Result<(), (bool, String)> {
        let mut req = self.client.post(&self.cfg.server_url).header("Content-Type", "application/json").body(body.to_vec());
        if !self.cfg.token.is_empty() {
            req = req.bearer_auth(&self.cfg.token);
        } else if !self.cfg.user.is_empty() {
            req = req.basic_auth(&self.cfg.user, Some(&self.cfg.password));
        }
        let resp = req.send().await.map_err(|e| (true, self.redact(&e.to_string())))?;
        let status = resp.status();
        if status.is_success() {
            let _ = resp.bytes().await;
            return Ok(());
        }
        let detail = resp.bytes().await.map(|b| String::from_utf8_lossy(&b[..b.len().min(512)]).trim().to_string()).unwrap_or_default();
        let retry = status.as_u16() == 429 || status.is_server_error();
        Err((retry, self.redact(&format!("ntfy answered {}: {detail}", status.as_u16()))))
    }
}

#[async_trait]
impl Sender for Ntfy {
    async fn send(&self, m: &Message) -> Result<(), SendError> {
        let body = serde_json::to_vec(&Publish {
            topic: &self.cfg.topic,
            title: &m.title,
            message: &m.body,
            priority: m.priority,
            tags: &m.tags,
            click: &self.cfg.app_url,
        })
        .map_err(|e| SendError::Failed(e.to_string()))?;
        let mut attempt = 0;
        loop {
            match self.once(&body).await {
                Ok(()) => return Ok(()),
                Err((retry, msg)) => {
                    if !retry || attempt >= self.retry_delays.len() {
                        return Err(SendError::Failed(msg));
                    }
                    tokio::time::sleep(self.retry_delays[attempt]).await;
                    attempt += 1;
                }
            }
        }
    }
}

/// A message that proves the setup works (`notify-test`).
pub fn test_message() -> Message {
    Message {
        title: "Speedtest test notification".into(),
        body: "If you can read this, notifications are working.".into(),
        priority: 3,
        tags: vec!["white_check_mark".into()],
    }
}

#[cfg(test)]
mod tests {
    type Reply = Arc<dyn Fn(usize) -> (u16, &'static str) + Send + Sync>;
    use super::*;
    use axum::{Router, extract::State, http::HeaderMap, response::IntoResponse, routing::post};
    use std::sync::{
        Arc, Mutex,
        atomic::{AtomicUsize, Ordering},
    };

    #[derive(Default)]
    struct Seen {
        calls: AtomicUsize,
        last: Mutex<Option<(HeaderMap, String)>>,
    }

    /// A stand-in ntfy server: answers with `status(call number)` and a body, and records what it received.
    async fn server(status: impl Fn(usize) -> (u16, &'static str) + Send + Sync + 'static) -> (String, Arc<Seen>) {
        let seen = Arc::new(Seen::default());
        let status = Arc::new(status);
        let app = Router::new()
            .route(
                "/",
                post(|State((seen, status)): State<(Arc<Seen>, Reply)>, h: HeaderMap, body: String| async move {
                    let n = seen.calls.fetch_add(1, Ordering::SeqCst) + 1;
                    *seen.last.lock().unwrap() = Some((h, body));
                    let (code, text) = status(n);
                    (axum::http::StatusCode::from_u16(code).unwrap(), text).into_response()
                }),
            )
            .with_state((seen.clone(), status as Arc<dyn Fn(usize) -> (u16, &'static str) + Send + Sync>));
        let listener = tokio::net::TcpListener::bind("127.0.0.1:0").await.unwrap();
        let addr = listener.local_addr().unwrap();
        tokio::spawn(async move { axum::serve(listener, app).await.unwrap() });
        (format!("http://{addr}"), seen)
    }

    fn ntfy(url: &str, mutate: impl FnOnce(&mut Config)) -> Ntfy {
        let mut cfg = Config { enabled: true, server_url: url.into(), topic: "secret-topic".into(), ..Config::default() };
        mutate(&mut cfg);
        Ntfy::new(cfg).without_waiting()
    }

    fn msg() -> Message {
        Message {
            title: "Slow download: server (wired)".into(),
            body: "Zażółć gęślą jaźń — 390 Mbps".into(),
            priority: 4,
            tags: vec!["warning".into(), "slow".into()],
        }
    }

    #[tokio::test]
    async fn publishes_json_with_a_bearer_token() {
        let (url, seen) = server(|_| (200, "")).await;
        let n = ntfy(&url, |c| {
            c.token = "tk_123".into();
            c.app_url = "https://speed.example.org".into();
        });
        n.send(&msg()).await.unwrap();
        let (headers, body) = seen.last.lock().unwrap().clone().unwrap();
        assert_eq!(headers["content-type"], "application/json");
        assert_eq!(headers["authorization"], "Bearer tk_123");
        let got: serde_json::Value = serde_json::from_str(&body).unwrap();
        assert_eq!(
            got,
            serde_json::json!({"topic":"secret-topic","title":"Slow download: server (wired)","message":"Zażółć gęślą jaźń — 390 Mbps","priority":4,"tags":["warning","slow"],"click":"https://speed.example.org"})
        );
    }

    #[tokio::test]
    async fn basic_auth_and_no_auth() {
        let (url, seen) = server(|_| (200, "")).await;
        ntfy(&url, |c| {
            c.user = "me".into();
            c.password = "pw".into();
        })
        .send(&Message { body: "x".into(), ..Default::default() })
        .await
        .unwrap();
        let auth = seen.last.lock().unwrap().clone().unwrap().0["authorization"].to_str().unwrap().to_string();
        assert_eq!(auth, "Basic bWU6cHc="); // me:pw
        ntfy(&url, |_| {}).send(&Message { body: "x".into(), ..Default::default() }).await.unwrap();
        assert!(!seen.last.lock().unwrap().clone().unwrap().0.contains_key("authorization"), "a public topic sends no credentials");
        // an empty title, priority, tags and click are left out
        let body = seen.last.lock().unwrap().clone().unwrap().1;
        assert_eq!(serde_json::from_str::<serde_json::Value>(&body).unwrap(), serde_json::json!({"topic":"secret-topic","message":"x"}));
    }

    #[tokio::test]
    async fn retries_server_errors_but_not_client_errors() {
        let (url, seen) = server(|n| if n < 3 { (503, "") } else { (200, "") }).await;
        assert!(ntfy(&url, |_| {}).send(&msg()).await.is_ok());
        assert_eq!(seen.calls.load(Ordering::SeqCst), 3, "transient failures are retried");

        let (url, seen) = server(|_| (503, "")).await;
        assert!(ntfy(&url, |_| {}).send(&msg()).await.is_err());
        assert_eq!(seen.calls.load(Ordering::SeqCst), 3, "gives up after three attempts");

        let (url, seen) = server(|_| (429, "slow down")).await;
        assert!(ntfy(&url, |_| {}).send(&msg()).await.is_err());
        assert_eq!(seen.calls.load(Ordering::SeqCst), 3, "429 is worth waiting for");

        let (url, seen) = server(|_| (403, r#"{"error":"forbidden"}"#)).await;
        let err = ntfy(&url, |_| {}).send(&msg()).await.unwrap_err();
        assert_eq!(seen.calls.load(Ordering::SeqCst), 1, "a 4xx will not get better by retrying");
        assert!(err.to_string().contains("403"), "{err}");
    }

    #[tokio::test]
    async fn errors_never_contain_the_topic_or_credentials() {
        let (url, _) = server(|_| (400, "bad request for topic secret-topic using tk_supersecret and pw0rd")).await;
        let n = ntfy(&url, |c| {
            c.token = "tk_supersecret".into();
            c.password = "pw0rd".into();
        });
        let err = n.send(&msg()).await.unwrap_err().to_string();
        for secret in ["secret-topic", "tk_supersecret", "pw0rd"] {
            assert!(!err.contains(secret), "error leaks {secret:?}: {err}");
        }
        assert!(err.contains("***"));
    }

    #[tokio::test]
    async fn an_unreachable_server_is_an_error() {
        let n = ntfy("http://127.0.0.1:1", |_| {}); // nothing listens on port 1
        assert!(matches!(n.send(&msg()).await, Err(SendError::Failed(_))));
    }
}
