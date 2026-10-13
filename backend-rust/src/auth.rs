//! Bearer tokens (one per device) and the per-device rate limit. Ports of backend-go/internal/api/auth.go and
//! backend-go/internal/config (ParseTokens).

use std::{collections::HashMap, sync::Mutex, time::Instant};

use axum::http::{HeaderMap, header};

#[derive(Debug, Clone, PartialEq)]
pub struct TokenEntry {
    pub device: String,
    pub token: String,
}

/// "device=token,device2=token2". Malformed or empty entries are skipped.
pub fn parse_tokens(s: &str) -> Vec<TokenEntry> {
    s.split(',')
        .filter_map(|pair| {
            let (device, token) = pair.trim().split_once('=')?;
            let (device, token) = (device.trim(), token.trim());
            (!device.is_empty() && !token.is_empty()).then(|| TokenEntry { device: device.into(), token: token.into() })
        })
        .collect()
}

/// Equal contents, in time that does not depend on where the first difference is. (A different length is visible, as in Go.)
pub(crate) fn constant_time_eq(a: &[u8], b: &[u8]) -> bool {
    a.len() == b.len() && a.iter().zip(b).fold(0u8, |acc, (x, y)| acc | (x ^ y)) == 0
}

/// Maps a bearer token to a device name. The token decides the device; callers cannot choose.
#[derive(Debug, Clone, Default)]
pub struct Authenticator {
    entries: Vec<TokenEntry>,
}

impl Authenticator {
    pub fn new(entries: Vec<TokenEntry>) -> Self {
        Self { entries }
    }

    /// The device for the request's bearer token. With no tokens configured it always fails.
    pub fn device(&self, headers: &HeaderMap) -> Option<&str> {
        let token = bearer_token(headers)?;
        let mut found = None;
        for e in &self.entries {
            // every entry is checked, so timing does not reveal which one matched
            if constant_time_eq(token.as_bytes(), e.token.as_bytes()) {
                found = Some(e.device.as_str());
            }
        }
        found
    }
}

/// The token of an `Authorization: Bearer <token>` header (the scheme is case-insensitive; the token is trimmed and not empty).
pub fn bearer_token(headers: &HeaderMap) -> Option<&str> {
    let value = headers.get(header::AUTHORIZATION)?.to_str().ok()?;
    let (scheme, token) = value.split_once(' ')?;
    let token = token.trim();
    (scheme.eq_ignore_ascii_case("bearer") && !token.is_empty()).then_some(token)
}

struct Bucket {
    tokens: f64,
    last: Instant,
}

/// A small per-key token bucket.
pub struct Limiter {
    burst: f64,
    per_sec: f64,
    buckets: Mutex<HashMap<String, Bucket>>,
}

impl Limiter {
    pub fn new(burst: u32, per_minute: u32) -> Self {
        Self { burst: burst as f64, per_sec: per_minute as f64 / 60.0, buckets: Mutex::new(HashMap::new()) }
    }

    /// Whether `key` may proceed; if not, the seconds until a token is available.
    pub fn allow(&self, key: &str) -> Result<(), u64> {
        self.allow_at(key, Instant::now())
    }

    pub fn allow_at(&self, key: &str, now: Instant) -> Result<(), u64> {
        let mut buckets = self.buckets.lock().unwrap_or_else(|e| e.into_inner());
        let b = buckets.entry(key.to_string()).or_insert(Bucket { tokens: self.burst, last: now });
        b.tokens = (b.tokens + now.saturating_duration_since(b.last).as_secs_f64() * self.per_sec).min(self.burst);
        b.last = now;
        if b.tokens >= 1.0 {
            b.tokens -= 1.0;
            return Ok(());
        }
        Err(((1.0 - b.tokens) / self.per_sec) as u64 + 1)
    }
}

impl Limiter {
    /// Drops buckets nobody has used for `idle`, so the table cannot grow for ever.
    pub fn forget_idle(&self, idle: std::time::Duration) {
        let now = Instant::now();
        self.buckets.lock().unwrap_or_else(|e| e.into_inner()).retain(|_, b| now.saturating_duration_since(b.last) <= idle);
    }
}

pub const BURST: u32 = 20;
pub const DEFAULT_PER_MINUTE: u32 = 30;

#[cfg(test)]
mod tests {
    use super::*;
    use axum::http::HeaderValue;
    use std::time::Duration;

    fn headers(v: &str) -> HeaderMap {
        let mut h = HeaderMap::new();
        h.insert(header::AUTHORIZATION, HeaderValue::from_str(v).unwrap());
        h
    }

    #[test]
    fn tokens_parse_like_go() {
        let t = parse_tokens("laptop=tok-laptop, phone = tok-phone ,bad,=x,y=,a=b=c");
        let pairs: Vec<_> = t.iter().map(|e| (e.device.as_str(), e.token.as_str())).collect();
        assert_eq!(pairs, [("laptop", "tok-laptop"), ("phone", "tok-phone"), ("a", "b=c")]);
        assert!(parse_tokens("").is_empty());
    }

    #[test]
    fn the_token_decides_the_device() {
        let a = Authenticator::new(parse_tokens("laptop=tok-laptop,phone=tok-phone"));
        assert_eq!(a.device(&headers("Bearer tok-phone")), Some("phone"));
        assert_eq!(a.device(&headers("bearer   tok-laptop  ")), Some("laptop"));
        assert_eq!(a.device(&headers("BEARER tok-laptop")), Some("laptop"));
        for bad in ["Bearer nope", "Bearer tok-lap", "Bearer tok-laptop-x", "Basic tok-laptop", "tok-laptop", "Bearer", "Bearer ", ""] {
            assert_eq!(a.device(&headers(bad)), None, "{bad:?}");
        }
        assert_eq!(a.device(&HeaderMap::new()), None);
        assert_eq!(Authenticator::default().device(&headers("Bearer anything")), None);
    }

    #[test]
    fn the_bucket_allows_a_burst_then_refills() {
        let l = Limiter::new(3, 60); // one per second
        let t0 = Instant::now();
        for _ in 0..3 {
            assert!(l.allow_at("a", t0).is_ok());
        }
        let wait = l.allow_at("a", t0).unwrap_err();
        assert!((1..=2).contains(&wait), "{wait}");
        assert!(l.allow_at("b", t0).is_ok(), "another device has its own bucket");
        assert!(l.allow_at("a", t0 + Duration::from_millis(1100)).is_ok(), "a token came back");
        assert!(l.allow_at("a", t0 + Duration::from_millis(1100)).is_err());
        // a long pause never stores more than the burst
        let later = t0 + Duration::from_secs(3600);
        for _ in 0..3 {
            assert!(l.allow_at("a", later).is_ok());
        }
        assert!(l.allow_at("a", later).is_err());
    }
}
