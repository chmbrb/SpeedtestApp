//! Settings from environment variables; the same names as the Go backend.

use std::{net::SocketAddr, str::FromStr};

use crate::auth::{DEFAULT_PER_MINUTE, TokenEntry, parse_tokens};

#[derive(Debug, Clone)]
pub struct Config {
    pub listen: SocketAddr,
    pub database_url: String,
    pub timezone: String,
    pub static_dir: Option<String>,
    /// Who may send results: the token decides the device. Empty means every ingest request is refused.
    pub tokens: Vec<TokenEntry>,
    /// Results one device may send per minute (burst 20). 0 means unlimited.
    pub ingest_per_minute: u32,
}

impl Config {
    /// `getenv` is injected so tests do not touch the process environment.
    pub fn load(getenv: impl Fn(&str) -> Option<String>) -> Result<Self, String> {
        let get = |k: &str| getenv(k).filter(|v| !v.is_empty());
        let listen = parse_listen(&get("LISTEN_ADDR").unwrap_or_else(|| ":8080".into()))?;
        let database_url =
            get("DATABASE_URL").ok_or("DATABASE_URL is required (e.g. postgres://user:pass@host:5432/speedtestdb?sslmode=disable)")?;
        let timezone = get("TIMEZONE").unwrap_or_else(|| "UTC".into());
        if !crate::params::valid_timezone(&timezone) {
            return Err(format!("TIMEZONE {timezone:?} is not a valid IANA zone"));
        }
        let ingest_per_minute = match get("INGEST_RATE_PER_MIN") {
            None => DEFAULT_PER_MINUTE,
            Some(v) => v.parse().map_err(|_| format!("INGEST_RATE_PER_MIN must be a non-negative integer, got {v:?}"))?,
        };
        let tokens = parse_tokens(&get("API_TOKENS").unwrap_or_default());
        Ok(Self { listen, database_url, timezone, static_dir: get("STATIC_DIR"), tokens, ingest_per_minute })
    }
}

/// ":8080" (all interfaces, like Go) or a full "host:port".
fn parse_listen(s: &str) -> Result<SocketAddr, String> {
    let full = if s.starts_with(':') { format!("0.0.0.0{s}") } else { s.to_string() };
    SocketAddr::from_str(&full).map_err(|_| format!("LISTEN_ADDR {s:?} must look like :8080 or 127.0.0.1:8080"))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn env(pairs: &'static [(&'static str, &'static str)]) -> impl Fn(&str) -> Option<String> {
        move |k| pairs.iter().find(|(n, _)| *n == k).map(|(_, v)| v.to_string())
    }

    #[test]
    fn defaults() {
        let c = Config::load(env(&[("DATABASE_URL", "postgres://x")])).unwrap();
        assert_eq!(c.listen.to_string(), "0.0.0.0:8080");
        assert_eq!(c.timezone, "UTC");
        assert!(c.static_dir.is_none());
        assert!(c.tokens.is_empty());
        assert_eq!(c.ingest_per_minute, 30);
    }

    #[test]
    fn tokens_and_the_ingest_rate() {
        let c = Config::load(env(&[("DATABASE_URL", "x"), ("API_TOKENS", "laptop=a, phone=b"), ("INGEST_RATE_PER_MIN", "0")])).unwrap();
        assert_eq!(c.tokens.len(), 2);
        assert_eq!(c.ingest_per_minute, 0);
        for bad in ["-1", "many", "1.5", "99999999999"] {
            let get = |k: &str| match k {
                "DATABASE_URL" => Some("x".to_string()),
                "INGEST_RATE_PER_MIN" => Some(bad.to_string()),
                _ => None,
            };
            assert!(Config::load(get).is_err(), "{bad}");
        }
    }

    #[test]
    fn database_url_is_required() {
        assert!(Config::load(env(&[])).unwrap_err().contains("DATABASE_URL is required"));
        assert!(Config::load(env(&[("DATABASE_URL", "")])).is_err());
    }

    #[test]
    fn validates_timezone_and_listen() {
        assert!(Config::load(env(&[("DATABASE_URL", "x"), ("TIMEZONE", "Mars/Olympus")])).is_err());
        assert!(Config::load(env(&[("DATABASE_URL", "x"), ("TIMEZONE", "America/Phoenix")])).is_ok());
        assert!(Config::load(env(&[("DATABASE_URL", "x"), ("LISTEN_ADDR", "nonsense")])).is_err());
        let c = Config::load(env(&[("DATABASE_URL", "x"), ("LISTEN_ADDR", "127.0.0.1:9000")])).unwrap();
        assert_eq!(c.listen.port(), 9000);
    }
}
