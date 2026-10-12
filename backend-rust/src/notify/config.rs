//! Notification settings from NTFY_* environment variables. Ports backend-go/internal/notify/config.go: the same names, defaults
//! and rules, and every problem reported at once.

use std::time::Duration;

/// Notifications are off unless NTFY_TOPIC is set.
#[derive(Debug, Clone)]
pub struct Config {
    pub enabled: bool,
    /// Base URL of the ntfy server, no trailing slash.
    pub server_url: String,
    pub topic: String,
    /// Access token (Bearer); optional.
    pub token: String,
    /// Or basic auth; optional.
    pub user: String,
    pub password: String,
    /// Opened when the notification is tapped; optional.
    pub app_url: String,

    /// A download below this fraction of the device's typical speed is "slow".
    pub slow_ratio: f64,
    /// Back to at least this fraction ends the alert (hysteresis).
    pub recover_ratio: f64,
    /// Slow tests in a row before alerting.
    pub slow_consecutive: i64,
    /// Packet loss at or above this percent is "lossy".
    pub loss_pct: f64,
    pub loss_consecutive: i64,
    /// Never call a device silent sooner than this.
    pub silence_min: Duration,
    /// Zero means no reminders.
    pub remind_every: Duration,
    /// History used for "typical".
    pub baseline_days: i64,
    /// Tests needed before a device's speed is judged.
    pub min_samples: i64,
    pub check_every: Duration,
}

/// Defaults, chosen against real data: isolated slow tests are common, so one is never enough to alert.
impl Default for Config {
    fn default() -> Self {
        Self {
            enabled: false,
            server_url: "https://ntfy.sh".into(),
            topic: String::new(),
            token: String::new(),
            user: String::new(),
            password: String::new(),
            app_url: String::new(),
            slow_ratio: 0.7,
            recover_ratio: 0.9,
            slow_consecutive: 2,
            loss_pct: 5.0,
            loss_consecutive: 2,
            silence_min: Duration::from_secs(3 * 3600),
            remind_every: Duration::from_secs(12 * 3600),
            baseline_days: 30,
            min_samples: 10,
            check_every: Duration::from_secs(60),
        }
    }
}

fn valid_topic(t: &str) -> bool {
    (1..=64).contains(&t.len()) && t.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'_' || b == b'-')
}

/// An absolute http(s) URL with a host.
fn http_url(s: &str) -> bool {
    url::Url::parse(s).is_ok_and(|u| matches!(u.scheme(), "http" | "https") && u.host_str().is_some_and(|h| !h.is_empty()))
}

impl Config {
    /// Invalid values are errors, not silently ignored: a notification setup that quietly does nothing is worse than one that
    /// refuses to start. With no topic the rest is not even looked at.
    pub fn from_env(getenv: impl Fn(&str) -> Option<String>) -> Result<Self, String> {
        let get = |k: &str| getenv(k).map(|v| v.trim().to_string()).unwrap_or_default();
        let mut c = Self { topic: get("NTFY_TOPIC"), ..Self::default() };
        if c.topic.is_empty() {
            return Ok(c); // disabled
        }
        c.enabled = true;
        let mut errs: Vec<String> = Vec::new();
        let mut bad = |name: &str, why: &str| errs.push(format!("{name} {why}"));

        if !valid_topic(&c.topic) {
            bad("NTFY_TOPIC", "must be 1-64 letters, digits, - or _");
        }
        let url = get("NTFY_URL");
        if !url.is_empty() {
            c.server_url = url.trim_end_matches('/').to_string();
        }
        if !http_url(&c.server_url) {
            bad("NTFY_URL", "must be an http(s) URL such as https://ntfy.sh");
        }
        // credentials are not trimmed beyond whitespace at the ends, like the Go backend reads them
        c.token = getenv("NTFY_TOKEN").unwrap_or_default();
        c.user = getenv("NTFY_USER").unwrap_or_default();
        c.password = getenv("NTFY_PASSWORD").unwrap_or_default();
        if !c.token.is_empty() && (!c.user.is_empty() || !c.password.is_empty()) {
            bad("NTFY_TOKEN", "cannot be combined with NTFY_USER/NTFY_PASSWORD; use one or the other");
        }
        if c.user.is_empty() != c.password.is_empty() {
            bad("NTFY_USER", "and NTFY_PASSWORD must be set together");
        }
        let app = get("APP_URL");
        if !app.is_empty() {
            if !http_url(&app) {
                bad("APP_URL", "must be an http(s) URL");
            }
            c.app_url = app;
        }

        let num = |name: &str, ok: &dyn Fn(f64) -> bool| -> Result<Option<f64>, ()> {
            match get(name).as_str() {
                "" => Ok(None),
                v => v.parse::<f64>().ok().filter(|f| f.is_finite() && ok(*f)).map(Some).ok_or(()),
            }
        };
        let int = |name: &str, min: i64| -> Result<Option<i64>, ()> {
            match get(name).as_str() {
                "" => Ok(None),
                v => v.parse::<i64>().ok().filter(|n| *n >= min).map(Some).ok_or(()),
            }
        };
        macro_rules! set {
            ($r:expr, $name:expr, $why:expr, $dst:expr) => {
                match $r {
                    Ok(Some(v)) => $dst = v,
                    Ok(None) => {}
                    Err(()) => bad($name, $why),
                }
            };
        }
        set!(
            num("NTFY_SLOW_RATIO", &|f| f > 0.0 && f < 1.0),
            "NTFY_SLOW_RATIO",
            "must be between 0 and 1 (exclusive), e.g. 0.7",
            c.slow_ratio
        );
        set!(
            num("NTFY_RECOVER_RATIO", &|f| f > 0.0 && f <= 1.0),
            "NTFY_RECOVER_RATIO",
            "must be above 0 and at most 1, e.g. 0.9",
            c.recover_ratio
        );
        set!(num("NTFY_LOSS_PCT", &|f| f > 0.0 && f <= 100.0), "NTFY_LOSS_PCT", "must be a percentage above 0 and at most 100", c.loss_pct);
        set!(int("NTFY_SLOW_CONSECUTIVE", 1), "NTFY_SLOW_CONSECUTIVE", "must be a whole number, 1 or more", c.slow_consecutive);
        set!(int("NTFY_LOSS_CONSECUTIVE", 1), "NTFY_LOSS_CONSECUTIVE", "must be a whole number, 1 or more", c.loss_consecutive);
        set!(int("NTFY_BASELINE_DAYS", 1), "NTFY_BASELINE_DAYS", "must be a whole number of days, 1 or more", c.baseline_days);
        set!(int("NTFY_MIN_SAMPLES", 3), "NTFY_MIN_SAMPLES", "must be a whole number, 3 or more", c.min_samples);
        let hours = |name: &str, allow_zero: bool| -> Result<Option<Duration>, ()> {
            match get(name).as_str() {
                "" => Ok(None),
                v => v
                    .parse::<f64>()
                    .ok()
                    .filter(|f| f.is_finite() && *f >= 0.0 && (*f > 0.0 || allow_zero))
                    .map(|h| Some(Duration::from_secs_f64(h * 3600.0)))
                    .ok_or(()),
            }
        };
        set!(hours("NTFY_SILENCE_MIN_HOURS", false), "NTFY_SILENCE_MIN_HOURS", "must be a number of hours above 0", c.silence_min);
        set!(hours("NTFY_REMIND_HOURS", true), "NTFY_REMIND_HOURS", "must be a number of hours (0 turns reminders off)", c.remind_every);
        match get("NTFY_CHECK_SECONDS").as_str() {
            "" => {}
            v => match v.parse::<u64>() {
                Ok(n) if n >= 10 => c.check_every = Duration::from_secs(n),
                _ => bad("NTFY_CHECK_SECONDS", "must be a whole number of seconds, 10 or more"),
            },
        }
        if c.recover_ratio <= c.slow_ratio {
            bad("NTFY_RECOVER_RATIO", "must be higher than NTFY_SLOW_RATIO, or alerts would flap");
        }
        if errs.is_empty() { Ok(c) } else { Err(errs.join("; ")) }
    }

    /// The ntfy server's host, safe to show in the UI. The topic and credentials are never exposed.
    pub fn host(&self) -> String {
        url::Url::parse(&self.server_url)
            .ok()
            .and_then(|u| {
                u.host_str().map(|h| match u.port() {
                    Some(p) => format!("{h}:{p}"),
                    None => h.to_string(),
                })
            })
            .unwrap_or_default()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;

    fn env(pairs: &[(&str, &str)]) -> impl Fn(&str) -> Option<String> {
        let m: HashMap<String, String> = pairs.iter().map(|(k, v)| (k.to_string(), v.to_string())).collect();
        move |k| m.get(k).cloned()
    }

    #[test]
    fn off_without_a_topic_and_without_complaints() {
        let c = Config::from_env(env(&[("NTFY_SLOW_RATIO", "not even checked")])).unwrap();
        assert!(!c.enabled);
    }

    #[test]
    fn defaults_and_overrides() {
        let c = Config::from_env(env(&[("NTFY_TOPIC", "home-net_1")])).unwrap();
        assert!(c.enabled);
        let d = Config::default();
        assert_eq!((c.server_url.as_str(), c.slow_ratio, c.slow_consecutive), ("https://ntfy.sh", d.slow_ratio, 2));
        assert_eq!(
            (c.silence_min, c.remind_every, c.check_every.as_secs()),
            (Duration::from_secs(3 * 3600), Duration::from_secs(12 * 3600), 60)
        );
        let c = Config::from_env(env(&[
            ("NTFY_TOPIC", "t"),
            ("NTFY_URL", "https://ntfy.example.org/"),
            ("NTFY_TOKEN", "tk_abc"),
            ("APP_URL", "https://speed.example.org"),
            ("NTFY_SLOW_RATIO", "0.5"),
            ("NTFY_RECOVER_RATIO", "0.8"),
            ("NTFY_SLOW_CONSECUTIVE", "3"),
            ("NTFY_LOSS_PCT", "2.5"),
            ("NTFY_SILENCE_MIN_HOURS", "1.5"),
            ("NTFY_REMIND_HOURS", "0"),
            ("NTFY_CHECK_SECONDS", "30"),
        ]))
        .unwrap();
        assert_eq!(
            (c.server_url.as_str(), c.host().as_str(), c.slow_ratio, c.slow_consecutive, c.loss_pct),
            ("https://ntfy.example.org", "ntfy.example.org", 0.5, 3, 2.5)
        );
        assert_eq!(
            (c.silence_min, c.remind_every, c.check_every.as_secs(), c.app_url.is_empty()),
            (Duration::from_secs(5400), Duration::ZERO, 30, false)
        );
        let local = Config::from_env(env(&[("NTFY_TOPIC", "t"), ("NTFY_URL", "http://ntfy:8081")])).unwrap();
        assert_eq!(local.host(), "ntfy:8081");
    }

    #[test]
    fn bad_values_are_refused_and_every_problem_is_listed() {
        let cases: Vec<(&str, Vec<(&str, &str)>)> = vec![
            ("NTFY_TOPIC", vec![("NTFY_TOPIC", "has space")]),
            ("NTFY_URL", vec![("NTFY_URL", "ntfy.sh")]),
            ("NTFY_SLOW_RATIO", vec![("NTFY_SLOW_RATIO", "1")]),
            ("NTFY_RECOVER_RATIO", vec![("NTFY_RECOVER_RATIO", "0.6")]), // not above the slow ratio
            ("NTFY_SLOW_CONSECUTIVE", vec![("NTFY_SLOW_CONSECUTIVE", "0")]),
            ("NTFY_LOSS_PCT", vec![("NTFY_LOSS_PCT", "150")]),
            ("NTFY_MIN_SAMPLES", vec![("NTFY_MIN_SAMPLES", "1")]),
            ("NTFY_CHECK_SECONDS", vec![("NTFY_CHECK_SECONDS", "2")]),
            ("NTFY_REMIND_HOURS", vec![("NTFY_REMIND_HOURS", "-1")]),
            ("NTFY_TOKEN", vec![("NTFY_TOKEN", "tk"), ("NTFY_USER", "me"), ("NTFY_PASSWORD", "pw")]),
            ("NTFY_USER", vec![("NTFY_USER", "me")]),
            ("APP_URL", vec![("APP_URL", "javascript:alert(1)")]),
        ];
        for (name, mut vars) in cases {
            if !vars.iter().any(|(k, _)| *k == "NTFY_TOPIC") {
                vars.push(("NTFY_TOPIC", "ok"));
            }
            let err = Config::from_env(env(&vars)).unwrap_err();
            assert!(err.contains(name), "{name}: {err}");
        }
        let err = Config::from_env(env(&[("NTFY_TOPIC", "x y"), ("NTFY_SLOW_RATIO", "9"), ("NTFY_URL", "ftp://z")])).unwrap_err();
        for want in ["NTFY_TOPIC", "NTFY_SLOW_RATIO", "NTFY_URL"] {
            assert!(err.contains(want), "all problems at once; missing {want} in {err}");
        }
    }
}
