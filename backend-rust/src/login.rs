//! The optional dashboard login: one shared password (DASHBOARD_PASSWORD) and signed session cookies. Ports
//! backend-go/internal/login, and produces and accepts exactly the same cookies (the same test vector is in both).
//!
//! A session is a stateless cookie, `s1.<expiry as unix seconds>.<signature>`. The signature is HMAC-SHA256 over `s1.<expiry>` with a
//! signing key made from a random key kept in the database (sql/postgres/009_session_key.sql) and the password: nobody can make a
//! cookie without the database key, a stolen cookie cannot be used to guess the password, and changing the password signs everybody
//! out. With no password set none of this is used and the dashboard is open.

use std::{
    sync::Arc,
    time::{Duration, SystemTime, UNIX_EPOCH},
};

use base64::{Engine, engine::general_purpose::URL_SAFE_NO_PAD};
use hmac::{Hmac, Mac};
use sha2::{Digest, Sha256};

use crate::{auth::constant_time_eq, store::Store};

/// The session cookie.
pub const COOKIE_NAME: &str = "speedtest_session";

const MIN_PASSWORD_CHARS: usize = 8;
const MAX_PASSWORD_BYTES: usize = 200;
const DEFAULT_DAYS: u64 = 30;
const MAX_DAYS: u64 = 365;
/// How far a cookie's expiry may lie beyond "now + lifetime" before it is refused (clock steps, a restart).
const SKEW_SECS: i64 = 60;
/// What `valid` will even look at.
const MAX_COOKIE: usize = 128;

#[derive(Debug, Clone)]
pub struct Config {
    pub password: String,
    pub lifetime: Duration,
}

/// DASHBOARD_PASSWORD (empty or unset: the login is off) and DASHBOARD_SESSION_DAYS (default 30, 1 to 365).
pub fn config_from_env(get: impl Fn(&str) -> Option<String>) -> Result<Option<Config>, String> {
    let password = get("DASHBOARD_PASSWORD").unwrap_or_default();
    if password.is_empty() {
        return Ok(None);
    }
    if password.chars().count() < MIN_PASSWORD_CHARS || password.len() > MAX_PASSWORD_BYTES {
        return Err(format!("DASHBOARD_PASSWORD must be {MIN_PASSWORD_CHARS} to {MAX_PASSWORD_BYTES} characters"));
    }
    let mut days = DEFAULT_DAYS;
    if let Some(v) = get("DASHBOARD_SESSION_DAYS").map(|v| v.trim().to_string()).filter(|v| !v.is_empty()) {
        days = v
            .parse::<u64>()
            .ok()
            .filter(|n| (1..=MAX_DAYS).contains(n))
            .ok_or_else(|| format!("DASHBOARD_SESSION_DAYS must be a whole number from 1 to {MAX_DAYS}"))?;
    }
    Ok(Some(Config { password, lifetime: Duration::from_secs(days * 24 * 3600) }))
}

/// Checks the password and makes and checks session cookies.
pub struct Service {
    lifetime: Duration,
    signing: Vec<u8>,
    pw_digest: [u8; 32],
    clock: Arc<dyn Fn() -> i64 + Send + Sync>,
}

fn unix_now() -> i64 {
    SystemTime::now().duration_since(UNIX_EPOCH).map(|d| d.as_secs() as i64).unwrap_or(0)
}

fn hmac(key: &[u8], msg: &[u8]) -> Vec<u8> {
    // HMAC accepts keys of any length, so this cannot fail.
    let mut m = Hmac::<Sha256>::new_from_slice(key).expect("HMAC takes any key length");
    m.update(msg);
    m.finalize().into_bytes().to_vec()
}

impl Service {
    /// Loads the signing key from the database, creating it on first use.
    pub async fn new(cfg: Config, store: &Store) -> Result<Arc<Self>, String> {
        let read = || async {
            store
                .session_key()
                .await
                .map_err(|e| format!("reading the session key: {e} (has sql/postgres/009_session_key.sql been applied?)"))
        };
        let key = match read().await? {
            Some(k) => k,
            None => {
                let mut fresh = [0u8; 32];
                getrandom::fill(&mut fresh).map_err(|e| e.to_string())?;
                store.save_session_key(&fresh).await.map_err(|e| format!("saving the session key: {e}"))?;
                // Read it back: if another server stored one first, that is the one to use.
                read().await?.ok_or("reading back the session key: nothing stored")?
            }
        };
        Ok(Arc::new(Self::with_key(cfg, &key)?))
    }

    /// Builds a service from a known database key (tests, and `new`).
    pub fn with_key(cfg: Config, db_key: &[u8]) -> Result<Self, String> {
        if db_key.len() < 32 {
            return Err("the session key must be at least 32 bytes".into());
        }
        let lifetime = if cfg.lifetime.is_zero() { Duration::from_secs(DEFAULT_DAYS * 24 * 3600) } else { cfg.lifetime };
        let signing = hmac(db_key, format!("speedtest login v1|{}", cfg.password).as_bytes());
        let pw_digest: [u8; 32] = Sha256::digest(cfg.password.as_bytes()).into();
        Ok(Self { lifetime, signing, pw_digest, clock: Arc::new(unix_now) })
    }

    /// Replaces the clock (unix seconds). For tests.
    pub fn with_clock(mut self, clock: impl Fn() -> i64 + Send + Sync + 'static) -> Self {
        self.clock = Arc::new(clock);
        self
    }

    /// How long a new session lasts.
    pub fn lifetime(&self) -> Duration {
        self.lifetime
    }

    /// Compares in time that does not depend on how much of the password is right.
    pub fn check_password(&self, given: &str) -> bool {
        let d: [u8; 32] = Sha256::digest(given.as_bytes()).into();
        constant_time_eq(&d, &self.pw_digest)
    }

    fn sign(&self, payload: &str) -> String {
        URL_SAFE_NO_PAD.encode(hmac(&self.signing, payload.as_bytes()))
    }

    /// Makes a session cookie value that is valid for the configured lifetime; also returns when it expires (unix seconds).
    pub fn issue(&self) -> (String, i64) {
        let exp = (self.clock)() + self.lifetime.as_secs() as i64;
        (self.cookie_for(exp), exp)
    }

    /// The cookie for an expiry. Public so tests can make expired and far-future ones.
    pub fn cookie_for(&self, exp: i64) -> String {
        let payload = format!("s1.{exp}");
        format!("{payload}.{}", self.sign(&payload))
    }

    /// Whether `value` is an unexpired session cookie signed by this server for the current password.
    pub fn valid(&self, value: &str) -> bool {
        if value.is_empty() || value.len() > MAX_COOKIE {
            return false;
        }
        let parts: Vec<&str> = value.split('.').collect();
        if parts.len() != 3 || parts[0] != "s1" {
            return false;
        }
        let Ok(exp) = parts[1].parse::<i64>() else { return false };
        if exp.to_string() != parts[1] {
            return false;
        }
        if !constant_time_eq(self.sign(&format!("{}.{}", parts[0], parts[1])).as_bytes(), parts[2].as_bytes()) {
            return false;
        }
        let now = (self.clock)();
        // Expired, or beyond what a session may last now (DASHBOARD_SESSION_DAYS was made shorter since it was issued).
        exp > now && exp < now + self.lifetime.as_secs() as i64 + SKEW_SECS
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    // This vector is also in backend-go/internal/login/login_test.go: the two backends must make and accept the very same cookies.
    const VECTOR_PASSWORD: &str = "correct horse battery";
    const VECTOR_EXPIRY: i64 = 2_000_000_000;
    const VECTOR_COOKIE: &str = "s1.2000000000.qC_Eurww35muXn2znWnJ2QbyZMG-OvSOfLDd4S7Lkqs";

    fn vector_key() -> Vec<u8> {
        (0..32u8).collect()
    }

    fn at(password: &str, lifetime: Duration, key: &[u8], now: i64) -> Service {
        Service::with_key(Config { password: password.into(), lifetime }, key).unwrap().with_clock(move || now)
    }

    #[test]
    fn the_cookie_matches_the_go_backend() {
        let s = at(VECTOR_PASSWORD, Duration::from_secs(24 * 3600), &vector_key(), VECTOR_EXPIRY - 3600);
        assert_eq!(s.cookie_for(VECTOR_EXPIRY), VECTOR_COOKIE);
        assert!(s.valid(VECTOR_COOKIE));
    }

    #[test]
    fn issue_and_valid() {
        let s = at("a good password", Duration::from_secs(7 * 24 * 3600), &vector_key(), 1_800_000_000);
        let (v, exp) = s.issue();
        assert_eq!(exp, 1_800_000_000 + 7 * 24 * 3600);
        assert!(s.valid(&v));
        let just_before = at("a good password", Duration::from_secs(7 * 24 * 3600), &vector_key(), exp - 1);
        assert!(just_before.valid(&v), "refused one second before it expires");
        let at_expiry = at("a good password", Duration::from_secs(7 * 24 * 3600), &vector_key(), exp);
        assert!(!at_expiry.valid(&v), "accepted at its expiry");
    }

    #[test]
    fn invalid_cookies() {
        let s = at(VECTOR_PASSWORD, Duration::from_secs(24 * 3600), &vector_key(), VECTOR_EXPIRY - 3600);
        let mac = VECTOR_COOKIE.trim_start_matches("s1.2000000000.");
        let mut flipped = mac.as_bytes().to_vec();
        flipped[0] ^= 1;
        let flipped = String::from_utf8(flipped).unwrap();
        let long = "A".repeat(200);
        let cases = [
            ("empty", String::new()),
            ("no dots", "s1".into()),
            ("two parts", "s1.2000000000".into()),
            ("four parts", format!("{VECTOR_COOKIE}.x")),
            ("another version", format!("s2.2000000000.{mac}")),
            ("later expiry", format!("s1.2000003600.{mac}")),
            ("flipped signature", format!("s1.2000000000.{flipped}")),
            ("empty signature", "s1.2000000000.".into()),
            ("plus sign", format!("s1.+2000000000.{mac}")),
            ("leading zero", format!("s1.02000000000.{mac}")),
            ("not a number", format!("s1.soon.{mac}")),
            ("too long", format!("s1.2000000000.{long}")),
            ("padded signature", format!("{VECTOR_COOKIE}=")),
        ];
        for (name, c) in cases {
            assert!(!s.valid(&c), "{name}: {c:?} was accepted");
        }
    }

    #[test]
    fn a_cookie_beyond_the_current_lifetime_is_refused() {
        let long = at(VECTOR_PASSWORD, Duration::from_secs(30 * 24 * 3600), &vector_key(), 1_800_000_000);
        let (v, _) = long.issue();
        let short = at(VECTOR_PASSWORD, Duration::from_secs(24 * 3600), &vector_key(), 1_800_000_000);
        assert!(!short.valid(&v));
    }

    #[test]
    fn changing_the_password_or_the_key_ends_every_session() {
        let hour = Duration::from_secs(3600);
        let a = at("first password", hour, &vector_key(), 1_800_000_000);
        let (v, _) = a.issue();
        assert!(a.valid(&v));
        assert!(!at("second password", hour, &vector_key(), 1_800_000_000).valid(&v));
        let mut other = vector_key();
        other[31] ^= 0xff;
        assert!(!at("first password", hour, &other, 1_800_000_000).valid(&v));
    }

    #[test]
    fn check_password() {
        let s = at(VECTOR_PASSWORD, Duration::from_secs(3600), &vector_key(), 0);
        assert!(s.check_password(VECTOR_PASSWORD));
        for bad in ["", "correct horse", &format!("{VECTOR_PASSWORD} "), &VECTOR_PASSWORD.to_uppercase(), &"x".repeat(10_000)] {
            assert!(!s.check_password(bad), "{:?} was accepted", &bad[..bad.len().min(20)]);
        }
    }

    fn env(pairs: &[(&'static str, &'static str)]) -> impl Fn(&str) -> Option<String> {
        let m: Vec<_> = pairs.to_vec();
        move |k| m.iter().find(|(n, _)| *n == k).map(|(_, v)| v.to_string())
    }

    #[test]
    fn from_env() {
        assert!(config_from_env(env(&[])).unwrap().is_none());
        assert!(config_from_env(env(&[("DASHBOARD_PASSWORD", "")])).unwrap().is_none());
        let c = config_from_env(env(&[("DASHBOARD_PASSWORD", "long enough")])).unwrap().unwrap();
        assert_eq!(c.lifetime, Duration::from_secs(30 * 24 * 3600));
        let c = config_from_env(env(&[("DASHBOARD_PASSWORD", "long enough"), ("DASHBOARD_SESSION_DAYS", " 7 ")])).unwrap().unwrap();
        assert_eq!(c.lifetime, Duration::from_secs(7 * 24 * 3600));
        let long = Box::leak("x".repeat(201).into_boxed_str());
        for (name, pairs) in [
            ("short password", vec![("DASHBOARD_PASSWORD", "short")]),
            ("huge password", vec![("DASHBOARD_PASSWORD", &*long)]),
            ("zero days", vec![("DASHBOARD_PASSWORD", "long enough"), ("DASHBOARD_SESSION_DAYS", "0")]),
            ("too many days", vec![("DASHBOARD_PASSWORD", "long enough"), ("DASHBOARD_SESSION_DAYS", "366")]),
            ("days not a number", vec![("DASHBOARD_PASSWORD", "long enough"), ("DASHBOARD_SESSION_DAYS", "a week")]),
        ] {
            assert!(config_from_env(env(&pairs)).is_err(), "{name}");
        }
        // characters, not bytes: eight multi-byte letters are fine
        assert!(config_from_env(env(&[("DASHBOARD_PASSWORD", "ééééééééé")])).unwrap().is_some());
    }

    #[test]
    fn short_keys_are_refused() {
        assert!(Service::with_key(Config { password: "long enough".into(), lifetime: Duration::from_secs(60) }, &[0u8; 31]).is_err());
    }
}
