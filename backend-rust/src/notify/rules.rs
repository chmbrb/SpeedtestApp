//! What counts as a problem, and how it is said. Ports backend-go/internal/notify/rules.go.

use chrono::{DateTime, Duration, Utc};

/// One thing whose speed is judged: a device on one kind of connection, tested with one provider.
#[derive(Debug, Clone, PartialEq)]
pub struct Entity {
    pub device: String,
    pub connection_type: String,
    pub provider: String,
}

impl Entity {
    pub fn subject(&self) -> String {
        format!("{}|{}|{}", self.device, self.connection_type, self.provider)
    }

    /// How a person would say it: "server (wired)", or "phone (wireless, NDT7)".
    pub fn label(&self) -> String {
        if self.provider == "ookla" || self.provider.is_empty() {
            format!("{} ({})", self.device, self.connection_type)
        } else {
            format!("{} ({}, {})", self.device, self.connection_type, provider_name(&self.provider))
        }
    }
}

fn provider_name(p: &str) -> &str {
    match p {
        "ookla" => "Ookla",
        "ndt7" => "NDT7",
        other => other,
    }
}

/// Median of a slice (not modified). Zero for an empty slice.
pub fn median(xs: &[f64]) -> f64 {
    if xs.is_empty() {
        return 0.0;
    }
    let mut s = xs.to_vec();
    s.sort_by(|a, b| a.total_cmp(b));
    let n = s.len();
    if n % 2 == 1 { s[n / 2] } else { (s[n / 2 - 1] + s[n / 2]) / 2.0 }
}

/// How long a device may stay quiet before that is worth a notification: three times its usual gap between tests, but never
/// less than `min`. A server that tests every 2 hours is silent after 6; a laptop that tests every 8 hours after 24.
pub fn silence_threshold(gaps: &[Duration], min: Duration) -> Duration {
    if gaps.is_empty() {
        return min;
    }
    let ms: Vec<f64> = gaps.iter().map(|g| g.num_milliseconds() as f64).collect();
    let t = Duration::milliseconds((3.0 * median(&ms)) as i64);
    t.max(min)
}

/// A notification as the dashboard and ntfy both show it.
#[derive(Debug, Clone, PartialEq)]
pub struct Text {
    pub title: String,
    pub detail: String,
    /// warning | critical
    pub severity: &'static str,
}

fn mbps(v: f64) -> String {
    if v >= 100.0 {
        format!("{v:.0}")
    } else if v >= 10.0 {
        format!("{v:.1}")
    } else {
        format!("{v:.2}")
    }
}

fn plural(n: i64, unit: &str) -> String {
    if n == 1 { format!("1 {unit}") } else { format!("{n} {unit}s") }
}

/// A download that is below the device's typical speed.
pub fn slow_text(e: &Entity, value: f64, typical: f64, streak: i64) -> Text {
    let below = ((1.0 - value / typical) * 100.0 + 0.5) as i64;
    Text {
        title: format!("Slow download: {}", e.label()),
        detail: format!("{} Mbps, {below}% below your typical {} Mbps ({} in a row).", mbps(value), mbps(typical), plural(streak, "test")),
        severity: if value / typical < 0.5 { "critical" } else { "warning" },
    }
}

/// Packet loss above the threshold.
pub fn loss_text(e: &Entity, value: f64, streak: i64) -> Text {
    Text {
        title: format!("Packet loss: {}", e.label()),
        detail: format!("{value:.1}% of packets lost ({} in a row).", plural(streak, "test")),
        severity: if value >= 20.0 { "critical" } else { "warning" },
    }
}

/// A device that has stopped reporting.
pub fn silence_text(device: &str, last: DateTime<Utc>, now: DateTime<Utc>, usual_gap: Duration) -> Text {
    Text {
        title: format!("No tests from {device}"),
        detail: format!("The last test was {} ago; one is usually due every {}.", humanize(now - last), humanize(usual_gap)),
        severity: "warning",
    }
}

/// A duration as "3 hours" / "25 minutes" / "2 days" (coarse on purpose).
pub fn humanize(d: Duration) -> String {
    let secs = d.num_milliseconds() as f64 / 1000.0;
    let (minutes, hours) = (secs / 60.0, secs / 3600.0);
    if secs < 60.0 {
        "under a minute".into()
    } else if secs < 3600.0 {
        plural((minutes + 0.5) as i64, "minute")
    } else if hours < 48.0 {
        let h = (hours * 10.0).round() / 10.0; // one decimal; a whole number reads better without ".0"
        if h == h.trunc() || h >= 10.0 { plural((h + 0.5) as i64, "hour") } else { format!("{h:.1} hours") }
    } else {
        plural((hours / 24.0 + 0.5) as i64, "day")
    }
}

/// Maps severity to ntfy priority: warnings use the default, critical ones are high, recoveries are quiet.
pub fn priority(severity: &str) -> u8 {
    if severity == "critical" { 4 } else { 3 }
}

pub const PRIORITY_RECOVERY: u8 = 2;

/// ntfy tags (emoji short codes show as icons on the notification).
pub fn tags(kind: &str, severity: &str) -> Vec<String> {
    vec![if severity == "critical" { "rotating_light" } else { "warning" }.to_string(), kind.to_string()]
}

#[cfg(test)]
mod tests {
    use super::*;

    fn h(n: i64) -> Duration {
        Duration::hours(n)
    }

    #[test]
    fn median_and_silence_threshold() {
        assert_eq!((median(&[]), median(&[5.0]), median(&[3.0, 1.0, 2.0]), median(&[1.0, 2.0, 3.0, 10.0])), (0.0, 5.0, 2.0, 2.5));
        let input = [9.0, 1.0, 5.0];
        median(&input);
        assert_eq!(input[0], 9.0, "median must not reorder its input");
        assert_eq!(silence_threshold(&[h(2), h(2), h(2), h(2)], h(3)), h(6), "2-hourly device");
        assert_eq!(silence_threshold(&[h(8), h(8), h(9)], h(3)), h(24), "8-hourly device");
        assert_eq!(silence_threshold(&[Duration::minutes(10), Duration::minutes(10)], h(3)), h(3), "a busy device still gets the floor");
        assert_eq!(
            silence_threshold(&[h(2), h(2), h(2), h(40)], h(3)),
            h(6),
            "one long outage must not stretch the threshold (median, not mean)"
        );
        assert_eq!(silence_threshold(&[], h(3)), h(3), "no history -> the floor");
    }

    #[test]
    fn message_texts() {
        let e = Entity { device: "server".into(), connection_type: "wired".into(), provider: "ookla".into() };
        let phone = Entity { device: "phone".into(), connection_type: "wireless".into(), provider: "ndt7".into() };
        assert_eq!((e.label().as_str(), phone.label().as_str()), ("server (wired)", "phone (wireless, NDT7)"));
        assert_eq!(e.subject(), "server|wired|ookla");
        let slow = slow_text(&e, 390.4, 936.6, 2);
        assert_eq!(slow.title, "Slow download: server (wired)");
        assert_eq!(slow.detail, "390 Mbps, 58% below your typical 937 Mbps (2 tests in a row).");
        assert_eq!(slow.severity, "critical");
        assert_eq!(slow_text(&e, 700.0, 936.6, 2).severity, "warning", "25% below typical is a warning, not critical");
        assert_eq!(
            (slow_text(&e, 100.0, 900.0, 2).severity, slow_text(&e, 449.0, 900.0, 2).severity, slow_text(&e, 451.0, 900.0, 2).severity),
            ("critical", "critical", "warning")
        );
        let small = slow_text(&e, 9.5, 20.4, 1).detail;
        assert!(small.contains("9.50 Mbps") && small.contains("1 test in a row"), "{small}");
        let loss = loss_text(&e, 7.25, 3);
        assert_eq!(loss.title, "Packet loss: server (wired)");
        assert!(loss.detail.contains("7.2%") || loss.detail.contains("7.3%"), "{}", loss.detail);
        assert_eq!((loss_text(&e, 19.9, 2).severity, loss_text(&e, 20.0, 2).severity), ("warning", "critical"));
        let now = DateTime::parse_from_rfc3339("2026-10-11T12:00:00Z").unwrap().with_timezone(&Utc);
        let sil = silence_text("server", now - h(7), now, h(2));
        assert_eq!(sil.title, "No tests from server");
        assert_eq!(sil.detail, "The last test was 7 hours ago; one is usually due every 2 hours.");
    }

    #[test]
    fn durations_in_words() {
        for (d, want) in [
            (Duration::seconds(20), "under a minute"),
            (Duration::minutes(25), "25 minutes"),
            (Duration::minutes(1), "1 minute"),
            (h(3), "3 hours"),
            (h(3) + Duration::seconds(59), "3 hours"),
            (h(3) + Duration::minutes(24), "3.4 hours"),
            (h(30), "30 hours"),
            (h(5 * 24), "5 days"),
        ] {
            assert_eq!(humanize(d), want, "{d:?}");
        }
    }

    #[test]
    fn priorities_and_tags() {
        assert_eq!((priority("critical"), priority("warning")), (4, 3));
        const { assert!(PRIORITY_RECOVERY < 3, "recovery is quiet") };
        assert_eq!(tags("slow", "critical"), ["rotating_light", "slow"]);
        assert_eq!(tags("loss", "warning"), ["warning", "loss"]);
    }
}
