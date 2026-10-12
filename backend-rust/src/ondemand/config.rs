//! On-demand test settings from environment variables; the same names and rules as backend-go/internal/ondemand/config.go.

use std::{path::Path, time::Duration};

#[derive(Debug, Clone)]
pub struct Config {
    /// Name the server's own tests are stored under (DEVICE_NAME, default "server").
    pub device_name: String,
    /// How the server is connected (SERVER_CONNECTION_TYPE, default "wired").
    pub server_connection: String,
    /// The Ookla CLI (OOKLA_PATH, default "speedtest").
    pub ookla_path: String,
    /// The person running the server has accepted Ookla's terms (OOKLA_ACCEPT_LICENSE=yes).
    pub ookla_accepted: bool,
    /// Longest a server test may run (ONDEMAND_TIMEOUT_SECONDS, default 180).
    pub test_timeout: Duration,
    /// How long a browser test may stay open (BROWSER_TEST_TTL_MINUTES, default 15).
    pub browser_ttl: Duration,
    /// Read the client address from X-Forwarded-For (TRUST_PROXY=1), only behind your own proxy.
    pub trust_proxy: bool,
    /// Tests one address may start per minute (ONDEMAND_PER_MINUTE, default 6).
    pub create_per_minute: u32,
}

impl Default for Config {
    fn default() -> Self {
        Self {
            device_name: "server".into(),
            server_connection: "wired".into(),
            ookla_path: "speedtest".into(),
            ookla_accepted: false,
            test_timeout: Duration::from_secs(180),
            browser_ttl: Duration::from_secs(15 * 60),
            trust_proxy: false,
            create_per_minute: 6,
        }
    }
}

/// Where the person running the server reads what they are accepting.
pub const OOKLA_TERMS_URL: &str = "https://www.speedtest.net/about/eula";

impl Config {
    /// Invalid values are errors, not silently ignored; every problem is reported at once.
    pub fn from_env(getenv: impl Fn(&str) -> Option<String>) -> Result<Self, String> {
        let get = |k: &str| getenv(k).map(|v| v.trim().to_string()).filter(|v| !v.is_empty());
        let mut c = Self::default();
        let mut errs: Vec<String> = Vec::new();
        let mut bad = |name: &str, why: &str| errs.push(format!("{name} {why}"));

        if let Some(v) = get("DEVICE_NAME") {
            if v.len() > 64 || v.chars().any(char::is_control) {
                bad("DEVICE_NAME", "must be at most 64 characters of plain text");
            }
            c.device_name = v;
        }
        if let Some(v) = get("SERVER_CONNECTION_TYPE") {
            if v != "wired" && v != "wireless" {
                bad("SERVER_CONNECTION_TYPE", "must be wired or wireless");
            }
            c.server_connection = v;
        }
        if let Some(v) = get("OOKLA_PATH") {
            c.ookla_path = v;
        }
        match get("OOKLA_ACCEPT_LICENSE").as_deref() {
            None => {}
            Some("yes") => c.ookla_accepted = true,
            Some(_) => bad("OOKLA_ACCEPT_LICENSE", "must be exactly yes (accepting is your decision, so nothing else counts)"),
        }
        if let Some(v) = get("ONDEMAND_TIMEOUT_SECONDS") {
            match v.parse::<u64>() {
                Ok(n) if (30..=1800).contains(&n) => c.test_timeout = Duration::from_secs(n),
                _ => bad("ONDEMAND_TIMEOUT_SECONDS", "must be a whole number from 30 to 1800"),
            }
        }
        if let Some(v) = get("BROWSER_TEST_TTL_MINUTES") {
            match v.parse::<u64>() {
                Ok(n) if (1..=120).contains(&n) => c.browser_ttl = Duration::from_secs(n * 60),
                _ => bad("BROWSER_TEST_TTL_MINUTES", "must be a whole number from 1 to 120"),
            }
        }
        if let Some(v) = get("ONDEMAND_PER_MINUTE") {
            match v.parse::<u32>() {
                Ok(n) if n >= 1 => c.create_per_minute = n,
                _ => bad("ONDEMAND_PER_MINUTE", "must be a whole number, 1 or more"),
            }
        }
        match get("TRUST_PROXY").map(|v| v.to_lowercase()).as_deref() {
            None | Some("0" | "false" | "no") => {}
            Some("1" | "true" | "yes") => c.trust_proxy = true,
            Some(_) => bad("TRUST_PROXY", "must be 1 or 0"),
        }
        if errs.is_empty() { Ok(c) } else { Err(errs.join("; ")) }
    }

    /// Whether Ookla tests can run, and if not, why, in words a person can act on.
    pub fn ookla_status(&self, look_path: &dyn Fn(&str) -> bool) -> Result<(), String> {
        if !self.ookla_accepted {
            return Err(format!(
                "Ookla’s terms have not been accepted on this server. Read them at {OOKLA_TERMS_URL} and, if you agree, set OOKLA_ACCEPT_LICENSE=yes and restart."
            ));
        }
        if !look_path(&self.ookla_path) {
            return Err(format!(
                "The Ookla command-line tool was not found (looked for “{}”). Install it, or set OOKLA_PATH.",
                self.ookla_path
            ));
        }
        Ok(())
    }
}

/// Is there an executable by this name (a path, or something on PATH)?
pub fn executable_exists(name: &str) -> bool {
    fn is_exec(p: &Path) -> bool {
        use std::os::unix::fs::PermissionsExt;
        p.metadata().is_ok_and(|m| m.is_file() && m.permissions().mode() & 0o111 != 0)
    }
    if name.contains('/') {
        return is_exec(Path::new(name));
    }
    std::env::var_os("PATH").is_some_and(|paths| std::env::split_paths(&paths).any(|d| is_exec(&d.join(name))))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn env(pairs: &[(&str, &str)]) -> impl Fn(&str) -> Option<String> {
        let owned: Vec<(String, String)> = pairs.iter().map(|(k, v)| (k.to_string(), v.to_string())).collect();
        move |k| owned.iter().find(|(n, _)| n == k).map(|(_, v)| v.clone())
    }

    #[test]
    fn defaults() {
        let c = Config::from_env(env(&[])).unwrap();
        assert_eq!((c.device_name.as_str(), c.server_connection.as_str(), c.ookla_path.as_str()), ("server", "wired", "speedtest"));
        assert!(!c.ookla_accepted && !c.trust_proxy);
        assert_eq!((c.test_timeout.as_secs(), c.browser_ttl.as_secs(), c.create_per_minute), (180, 900, 6));
    }

    #[test]
    fn values_are_read_and_checked() {
        let c = Config::from_env(env(&[
            ("DEVICE_NAME", " nas "),
            ("SERVER_CONNECTION_TYPE", "wireless"),
            ("OOKLA_PATH", "/opt/speedtest"),
            ("OOKLA_ACCEPT_LICENSE", "yes"),
            ("ONDEMAND_TIMEOUT_SECONDS", "60"),
            ("BROWSER_TEST_TTL_MINUTES", "5"),
            ("ONDEMAND_PER_MINUTE", "2"),
            ("TRUST_PROXY", "yes"),
        ]))
        .unwrap();
        assert_eq!(
            (c.device_name.as_str(), c.server_connection.as_str(), c.test_timeout.as_secs(), c.browser_ttl.as_secs()),
            ("nas", "wireless", 60, 300)
        );
        assert!(c.ookla_accepted && c.trust_proxy);
        assert_eq!(c.create_per_minute, 2);
        let err = Config::from_env(env(&[
            ("OOKLA_ACCEPT_LICENSE", "true"),
            ("ONDEMAND_TIMEOUT_SECONDS", "5"),
            ("BROWSER_TEST_TTL_MINUTES", "0"),
            ("ONDEMAND_PER_MINUTE", "0"),
            ("TRUST_PROXY", "maybe"),
            ("SERVER_CONNECTION_TYPE", "satellite"),
            ("DEVICE_NAME", &"x".repeat(65)),
        ]))
        .unwrap_err();
        for name in [
            "OOKLA_ACCEPT_LICENSE",
            "ONDEMAND_TIMEOUT_SECONDS",
            "BROWSER_TEST_TTL_MINUTES",
            "ONDEMAND_PER_MINUTE",
            "TRUST_PROXY",
            "SERVER_CONNECTION_TYPE",
            "DEVICE_NAME",
        ] {
            assert!(err.contains(name), "{name} not reported in: {err}");
        }
    }

    #[test]
    fn ookla_status_says_what_to_do() {
        let mut c = Config::default();
        assert!(c.ookla_status(&|_| true).unwrap_err().contains("OOKLA_ACCEPT_LICENSE=yes"));
        c.ookla_accepted = true;
        assert!(c.ookla_status(&|_| false).unwrap_err().contains("OOKLA_PATH"));
        assert!(c.ookla_status(&|_| true).is_ok());
    }

    #[test]
    fn finds_executables() {
        assert!(executable_exists("sh"));
        assert!(executable_exists("/bin/sh"));
        assert!(!executable_exists("definitely-not-a-program-xyz"));
        assert!(!executable_exists("/etc/passwd")); // exists, but is not executable
    }
}
