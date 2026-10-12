//! Delivers notifications to browsers and installed apps with Web Push (RFC 8030, 8291, 8292). Ports backend-go/internal/push.

pub mod webpush;

use std::{sync::Arc, time::Duration};

use async_trait::async_trait;
use serde::Serialize;

use crate::{
    notify::sender::{Message, SendError, Sender},
    store::Store,
    store_push::{PushSubscription, SaveError},
};
use webpush::{Vapid, generate_keys, unb64};

/// The push services of the major browsers. A subscription's endpoint is an address the server will POST to, so only these (and
/// PUSH_ALLOWED_HOSTS additions) are accepted: otherwise anyone who can reach the dashboard could make the server send requests to
/// any address on the network. An entry starting with "." matches any subdomain.
pub const DEFAULT_ALLOWED_HOSTS: &[&str] = &[
    "fcm.googleapis.com",         // Chrome, Edge, Brave, Samsung Internet, Opera on Android and desktop
    ".push.services.mozilla.com", // Firefox
    ".push.apple.com",            // Safari and installed web apps on iOS and macOS
    ".notify.windows.com",        // Edge on Windows (WNS)
];

pub const MAX_SUBSCRIPTIONS: i64 = 50;
/// The longest endpoint accepted anywhere.
pub const MAX_ENDPOINT_LEN: usize = 2048;
const MAX_FAILURES: i32 = 5;
const SEND_TIMEOUT: Duration = Duration::from_secs(10);
const WORKERS: usize = 8;

/// Settings. Zero values get sensible defaults.
#[derive(Debug, Clone)]
pub struct Config {
    /// Who is sending: an https URL or an email address; push services may contact it.
    pub subject: String,
    /// Replaces the default list when set.
    pub allowed_hosts: Vec<String>,
    pub ttl: Duration,
    /// Endpoints must be https. Only tests (which talk to a plain local server) turn this off.
    pub require_https: bool,
}

impl Default for Config {
    fn default() -> Self {
        Self { subject: String::new(), allowed_hosts: Vec::new(), ttl: Duration::ZERO, require_https: true }
    }
}

/// PUSH_ENABLED=no turns browser notifications off. PUSH_SUBJECT is who the server tells the push services it is (an https URL or
/// an email address); it defaults to APP_URL when that is https. PUSH_ALLOWED_HOSTS adds push-service hosts to the built-in list
/// (comma separated; a leading dot means "and subdomains").
pub fn config_from_env(getenv: impl Fn(&str) -> Option<String>) -> Result<Option<Config>, String> {
    let get = |k: &str| getenv(k).map(|v| v.trim().to_string()).unwrap_or_default();
    match get("PUSH_ENABLED").to_lowercase().as_str() {
        "" | "yes" | "true" | "1" => {}
        "no" | "false" | "0" => return Ok(None),
        _ => return Err("PUSH_ENABLED must be yes or no".into()),
    }
    let mut cfg = Config { subject: get("PUSH_SUBJECT"), ..Config::default() };
    if cfg.subject.is_empty() {
        if let Ok(u) = url::Url::parse(&get("APP_URL")) {
            if u.scheme() == "https" && u.host_str().is_some() {
                cfg.subject =
                    format!("https://{}", u.host_str().map(|h| u.port().map_or(h.to_string(), |p| format!("{h}:{p}"))).unwrap_or_default());
            }
        }
    }
    let extra = get("PUSH_ALLOWED_HOSTS");
    if !extra.is_empty() {
        cfg.allowed_hosts = DEFAULT_ALLOWED_HOSTS
            .iter()
            .map(|s| s.to_string())
            .chain(extra.split(',').map(str::trim).filter(|h| !h.is_empty()).map(String::from))
            .collect();
    }
    Ok(Some(cfg))
}

/// Which field of a subscription was not acceptable.
#[derive(Debug, PartialEq)]
pub struct ValidationError {
    pub field: &'static str,
    pub message: &'static str,
}

#[derive(Debug)]
pub enum PushError {
    Validation(ValidationError),
    TooMany,
    /// This device is not subscribed.
    Unknown,
    /// The push service refused the message.
    Refused(String),
    Db(sqlx::Error),
}

impl From<sqlx::Error> for PushError {
    fn from(e: sqlx::Error) -> Self {
        PushError::Db(e)
    }
}

/// What the service worker receives.
#[derive(Serialize)]
struct Payload<'a> {
    title: &'a str,
    body: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    url: &'a str,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    test: bool,
}

/// Owns the VAPID keys and the list of subscribers, and sends to them.
pub struct Service {
    cfg: Config,
    store: Store,
    client: reqwest::Client,
    vapid: Vapid,
}

fn clip(s: &str, n: usize) -> String {
    if s.chars().count() <= n { s.to_string() } else { s.chars().take(n - 1).collect::<String>() + "…" }
}

impl Service {
    /// Loads the VAPID key pair, creating and storing one on first use.
    pub async fn new(mut cfg: Config, store: Store) -> Result<Arc<Self>, String> {
        if cfg.subject.is_empty() {
            cfg.subject = "speedtest@example.com".into();
        }
        if cfg.allowed_hosts.is_empty() {
            cfg.allowed_hosts = DEFAULT_ALLOWED_HOSTS.iter().map(|s| s.to_string()).collect();
        }
        if cfg.ttl.is_zero() {
            cfg.ttl = Duration::from_secs(6 * 3600);
        }
        let client = reqwest::Client::builder()
            .timeout(SEND_TIMEOUT)
            .redirect(reqwest::redirect::Policy::none()) // an endpoint must never be able to send the server somewhere else
            .build()
            .map_err(|e| e.to_string())?;
        let keys =
            store.vapid().await.map_err(|e| format!("reading the push keys: {e} (has sql/postgres/007_web_push.sql been applied?)"))?;
        let (public, private) = match keys {
            Some(k) => k,
            None => {
                let (public, private) = generate_keys();
                store.save_vapid(&public, &private).await.map_err(|e| e.to_string())?;
                // if another server saved a pair first, use that one
                store.vapid().await.map_err(|e| e.to_string())?.ok_or("storing the push keys failed")?
            }
        };
        let vapid = Vapid::from_keys(&public, &private)?;
        Ok(Arc::new(Self { cfg, store, client, vapid }))
    }

    /// What the browser needs to subscribe (the "application server key").
    pub fn public_key(&self) -> &str {
        &self.vapid.public_b64
    }

    fn host_allowed(&self, host: &str) -> bool {
        let host = host.to_lowercase();
        self.cfg.allowed_hosts.iter().map(|a| a.trim().to_lowercase()).any(|a| match a.strip_prefix('.') {
            _ if a.is_empty() => false,
            Some(_) => host.ends_with(&a),
            None => host == a,
        })
    }

    fn check_endpoint(&self, endpoint: &str) -> Result<(), PushError> {
        let bad = |message| PushError::Validation(ValidationError { field: "endpoint", message });
        if endpoint.is_empty() || endpoint.len() > MAX_ENDPOINT_LEN {
            return Err(bad("must be the subscription's URL, at most 2048 characters"));
        }
        // control characters (a NUL would be refused by the database as a 500) and backslashes (read as a path separator by some URL
        // parsers and as part of the host by others) are never part of a real push endpoint
        if endpoint.chars().any(|c| c.is_control() || c == '\\') {
            return Err(bad("must be an https URL"));
        }
        let u = url::Url::parse(endpoint).ok().filter(|u| {
            (u.scheme() == "https" || (!self.cfg.require_https && u.scheme() == "http"))
                && u.host_str().is_some()
                && u.username().is_empty()
                && u.password().is_none()
                && u.fragment().is_none()
        });
        let Some(u) = u else { return Err(bad("must be an https URL")) };
        if !self.host_allowed(u.host_str().unwrap_or("")) {
            return Err(bad("is not a known browser push service"));
        }
        Ok(())
    }

    /// Stores a subscription after checking it. Subscribing the same endpoint again just refreshes its keys.
    pub async fn subscribe(&self, endpoint: &str, p256dh: &str, auth: &str, user_agent: &str) -> Result<(), PushError> {
        self.check_endpoint(endpoint)?;
        if !unb64(p256dh).is_some_and(|b| b.len() == 65 && b[0] == 4) {
            return Err(PushError::Validation(ValidationError {
                field: "keys.p256dh",
                message: "must be the browser's 65-byte public key, base64url encoded",
            }));
        }
        if unb64(auth).is_none_or(|b| b.len() != 16) {
            return Err(PushError::Validation(ValidationError {
                field: "keys.auth",
                message: "must be the browser's 16-byte secret, base64url encoded",
            }));
        }
        let sub = PushSubscription {
            id: String::new(),
            endpoint: endpoint.into(),
            p256dh: p256dh.into(),
            auth: auth.into(),
            user_agent: user_agent.chars().take(300).collect(),
            failures: 0,
        };
        self.store.save_subscription(&sub, MAX_SUBSCRIPTIONS).await.map_err(|e| match e {
            SaveError::TooMany => PushError::TooMany,
            SaveError::Db(e) => PushError::Db(e),
        })
    }

    /// Removes a subscription. Removing one that is not there is fine.
    pub async fn unsubscribe(&self, endpoint: &str) -> Result<(), PushError> {
        self.store.delete_subscription(endpoint).await?;
        Ok(())
    }

    /// Sends a short test message to one subscription, so a person can check it works.
    pub async fn send_test(&self, endpoint: &str) -> Result<(), PushError> {
        let sub = self.store.subscription(endpoint).await?.ok_or(PushError::Unknown)?;
        let body = serde_json::to_vec(&Payload {
            title: "Notifications are on",
            body: "You'll hear about slow or missing speed tests here.",
            url: "/",
            test: true,
        })
        .unwrap_or_default();
        self.deliver(&sub, &body, "high").await
    }

    async fn deliver(&self, sub: &PushSubscription, payload: &[u8], urgency: &str) -> Result<(), PushError> {
        let redact = |s: String| s.replace(&sub.endpoint, "<subscription>");
        let attempt = async {
            let (p256dh, auth) = (unb64(&sub.p256dh).unwrap_or_default(), unb64(&sub.auth).unwrap_or_default());
            let body = webpush::encrypt(payload, &p256dh, &auth)?;
            let now = std::time::SystemTime::now().duration_since(std::time::UNIX_EPOCH).map(|d| d.as_secs()).unwrap_or(0);
            let authorization = self.vapid.authorization(&sub.endpoint, &self.cfg.subject, now)?;
            self.client
                .post(&sub.endpoint)
                .header("Content-Encoding", "aes128gcm")
                .header("Content-Type", "application/octet-stream")
                .header("TTL", self.cfg.ttl.as_secs().to_string())
                .header("Urgency", urgency)
                .header("Authorization", authorization)
                .body(body)
                .send()
                .await
                .map_err(|e| e.to_string())
        };
        let resp = match attempt.await {
            Ok(r) => r,
            Err(e) => {
                self.mark(sub, false).await;
                return Err(PushError::Refused(redact(e)));
            }
        };
        let status = resp.status();
        let detail = resp.bytes().await.map(|b| String::from_utf8_lossy(&b[..b.len().min(300)]).trim().to_string()).unwrap_or_default();
        if status.is_success() {
            self.mark(sub, true).await;
            Ok(())
        } else if status.as_u16() == 404 || status.as_u16() == 410 {
            // the browser or the user cancelled it: nothing will ever be delivered there again
            if let Err(e) = self.store.delete_subscription_by_id(&sub.id).await {
                tracing::error!(err = %e, "could not remove an expired push subscription");
            }
            Err(PushError::Refused("the subscription has expired and was removed".into()))
        } else {
            self.mark(sub, false).await;
            Err(PushError::Refused(format!("the push service answered {} {detail}", status.as_u16())))
        }
    }

    async fn mark(&self, sub: &PushSubscription, ok: bool) {
        match self.store.mark_push_delivery(&sub.id, ok, MAX_FAILURES).await {
            Ok(true) => tracing::warn!("removed a push subscription after repeated failures"),
            Ok(false) => {}
            Err(e) => tracing::error!(err = %e, "could not record a push delivery"),
        }
    }
}

#[async_trait]
impl Sender for Service {
    /// Delivers the message to every subscriber. `NoRecipients` when there are none, and an error only when every attempt failed.
    async fn send(&self, m: &Message) -> Result<(), SendError> {
        let subs = self.store.list_subscriptions().await.map_err(|e| SendError::Failed(e.to_string()))?;
        if subs.is_empty() {
            return Err(SendError::NoRecipients);
        }
        let urgency = if m.priority >= 4 { "high" } else { "normal" };
        let body = serde_json::to_vec(&Payload { title: &clip(&m.title, 100), body: &clip(&m.body, 300), url: "/", test: false })
            .unwrap_or_default();
        // WORKERS at a time
        let mut results: Vec<Result<(), PushError>> = Vec::new();
        for chunk in subs.chunks(WORKERS) {
            let mut batch = Vec::new();
            for sub in chunk {
                batch.push(self.deliver(sub, &body, urgency));
            }
            results.extend(futures_util::future::join_all(batch).await);
        }
        let delivered = results.iter().filter(|r| r.is_ok()).count();
        let errors: Vec<String> = results
            .into_iter()
            .filter_map(|r| r.err())
            .map(|e| match e {
                PushError::Refused(m) => m,
                other => format!("{other:?}"),
            })
            .collect();
        if delivered > 0 {
            if !errors.is_empty() {
                tracing::warn!(delivered, failed = errors.len(), first = %errors[0], "some push deliveries failed");
            }
            return Ok(());
        }
        Err(SendError::Failed(format!("push: {}", errors.join("; "))))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn env(pairs: &'static [(&'static str, &'static str)]) -> impl Fn(&str) -> Option<String> {
        move |k| pairs.iter().find(|(n, _)| *n == k).map(|(_, v)| v.to_string())
    }

    #[test]
    fn settings_from_the_environment() {
        assert!(config_from_env(env(&[])).unwrap().is_some(), "on by default");
        assert!(config_from_env(env(&[("PUSH_ENABLED", "no")])).unwrap().is_none());
        assert!(config_from_env(env(&[("PUSH_ENABLED", "maybe")])).is_err());
        let c = config_from_env(env(&[("APP_URL", "https://192.168.0.7:8443/some/path")])).unwrap().unwrap();
        assert_eq!(c.subject, "https://192.168.0.7:8443");
        assert_eq!(config_from_env(env(&[("APP_URL", "http://192.168.0.7:8080")])).unwrap().unwrap().subject, "");
        let c = config_from_env(env(&[("PUSH_ALLOWED_HOSTS", " push.example.org , .corp.example ")])).unwrap().unwrap();
        assert_eq!(c.allowed_hosts.len(), DEFAULT_ALLOWED_HOSTS.len() + 2);
    }

    #[test]
    fn clipping_counts_characters_not_bytes() {
        assert_eq!(clip("short", 100), "short");
        let long = "é".repeat(1000);
        assert_eq!(clip(&long, 300).chars().count(), 300);
        assert!(clip(&long, 300).ends_with('…'));
    }
}
