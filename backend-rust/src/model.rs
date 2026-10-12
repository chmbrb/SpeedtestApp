//! The JSON shapes defined by docs/openapi.yaml (the Go twin is backend-go/internal/model).

use chrono::{DateTime, Timelike, Utc};
use serde::{Serialize, Serializer, ser::SerializeMap};
use serde_json::value::RawValue;

/// Go's RFC 3339 form: `Z`, and only as many fractional digits as are needed. Keeping the two backends byte-identical here
/// means a comparison does not have to know about time formats.
pub fn format_time(t: &DateTime<Utc>) -> String {
    let base = t.format("%Y-%m-%dT%H:%M:%S").to_string();
    let nanos = t.nanosecond() % 1_000_000_000;
    if nanos == 0 {
        return format!("{base}Z");
    }
    let frac = format!("{nanos:09}");
    format!("{base}.{}Z", frac.trim_end_matches('0'))
}

mod ts {
    use super::*;
    pub fn ser<S: Serializer>(t: &DateTime<Utc>, s: S) -> Result<S::Ok, S::Error> {
        s.serialize_str(&format_time(t))
    }
    pub mod opt {
        use super::*;
        pub fn ser<S: Serializer>(t: &Option<DateTime<Utc>>, s: S) -> Result<S::Ok, S::Error> {
            match t {
                Some(t) => s.serialize_str(&format_time(t)),
                None => s.serialize_none(),
            }
        }
    }
}

#[derive(Debug, Serialize, Clone, Default)]
pub struct ServerInfo {
    pub name: Option<String>,
    pub location: Option<String>,
    pub country: Option<String>,
}

#[derive(Debug, Serialize, Clone)]
#[serde(rename_all = "camelCase")]
pub struct TestResult {
    pub id: String,
    pub provider: String,
    pub trigger: String,
    pub device: String,
    pub connection_type: String,
    #[serde(serialize_with = "ts::ser")]
    pub tested_at: DateTime<Utc>,
    pub download_mbps: f64,
    pub upload_mbps: f64,
    pub latency_ms: Option<f64>,
    pub jitter_ms: Option<f64>,
    pub packet_loss_pct: Option<f64>,
    pub isp: Option<String>,
    pub server: ServerInfo,
    pub result_url: Option<String>,
}

/// A result plus the provider's original payload, passed through exactly as stored. (`#[serde(flatten)]` cannot carry a raw
/// value, so the fields are written out by hand.)
pub struct ResultDetail {
    pub result: TestResult,
    pub raw: Box<RawValue>,
}

impl Serialize for ResultDetail {
    fn serialize<S: Serializer>(&self, s: S) -> Result<S::Ok, S::Error> {
        let v = serde_json::to_value(&self.result).map_err(serde::ser::Error::custom)?;
        let obj = v.as_object().ok_or_else(|| serde::ser::Error::custom("result is not an object"))?;
        let mut m = s.serialize_map(Some(obj.len() + 1))?;
        for (k, v) in obj {
            m.serialize_entry(k, v)?;
        }
        m.serialize_entry("raw", &self.raw)?;
        m.end()
    }
}

#[derive(Debug, Serialize)]
#[serde(rename_all = "camelCase")]
pub struct Device {
    pub name: String,
    pub connection_types: Vec<String>,
    pub providers: Vec<String>,
    pub test_count: i64,
    #[serde(serialize_with = "ts::ser")]
    pub last_test_at: DateTime<Utc>,
}

/// The distribution of one metric; every field is null when there is no data.
#[derive(Debug, Serialize, Default, Clone)]
pub struct Stat {
    pub avg: Option<f64>,
    pub min: Option<f64>,
    pub max: Option<f64>,
    pub p50: Option<f64>,
    pub p95: Option<f64>,
}

#[derive(Debug, Serialize, Default, Clone)]
#[serde(rename_all = "camelCase")]
pub struct Metrics {
    pub download_mbps: Stat,
    pub upload_mbps: Stat,
    pub latency_ms: Stat,
    pub packet_loss_pct: Stat,
}

#[derive(Debug, Serialize)]
pub struct Summary {
    #[serde(flatten)]
    pub metrics: Metrics,
    pub count: i64,
    #[serde(serialize_with = "ts::opt::ser")]
    pub first: Option<DateTime<Utc>>,
    #[serde(serialize_with = "ts::opt::ser")]
    pub last: Option<DateTime<Utc>>,
}

/// Only the `groupBy` fields are present.
#[derive(Debug, Serialize, Clone, PartialEq, Default)]
#[serde(rename_all = "camelCase")]
pub struct SeriesKey {
    #[serde(skip_serializing_if = "Option::is_none")]
    pub provider: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub device: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub connection_type: Option<String>,
}

#[derive(Debug, Serialize)]
pub struct TimeseriesPoint {
    #[serde(flatten)]
    pub metrics: Metrics,
    #[serde(serialize_with = "ts::ser")]
    pub bucket: DateTime<Utc>,
    pub count: i64,
}

#[derive(Debug, Serialize)]
pub struct Series {
    pub key: SeriesKey,
    pub points: Vec<TimeseriesPoint>,
}

#[derive(Debug, Serialize)]
pub struct Timeseries {
    pub interval: String,
    pub timezone: String,
    pub series: Vec<Series>,
}

/// An on-demand test run. `submit_token` is present only in the response that creates a browser test.
#[derive(Debug, Serialize, Clone)]
#[serde(rename_all = "camelCase")]
pub struct Test {
    pub id: String,
    pub provider: String,
    pub mode: String,
    pub device: String,
    pub connection_type: String,
    pub state: String,
    #[serde(serialize_with = "ts::ser")]
    pub created_at: DateTime<Utc>,
    #[serde(serialize_with = "ts::opt::ser")]
    pub started_at: Option<DateTime<Utc>>,
    #[serde(serialize_with = "ts::opt::ser")]
    pub finished_at: Option<DateTime<Utc>>,
    pub result_id: Option<String>,
    pub error: Option<String>,
    pub queue_position: Option<i32>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub submit_token: String,
}

#[derive(Debug, Serialize, Clone)]
pub struct Capability {
    pub available: bool,
    pub reason: Option<String>,
}

#[derive(Debug, Serialize, Clone)]
#[serde(rename_all = "camelCase")]
pub struct Capabilities {
    pub device_name: String,
    pub server_connection_type: String,
    pub ookla: Capability,
    pub ndt7: Capability,
}

/// The thresholds the notification monitor applies, in the units a person thinks in.
#[derive(Debug, Serialize, Clone, PartialEq)]
#[serde(rename_all = "camelCase")]
pub struct NotificationRules {
    pub slow_ratio: f64,
    pub slow_consecutive: i64,
    pub recover_ratio: f64,
    pub loss_pct: f64,
    pub loss_consecutive: i64,
    pub silence_min_hours: f64,
    pub remind_hours: f64,
    pub baseline_days: i64,
}

#[derive(Debug, Serialize, Clone)]
#[serde(rename_all = "camelCase")]
pub struct ActiveAlert {
    pub kind: String,
    pub subject: String,
    pub title: String,
    pub detail: String,
    pub severity: String,
    #[serde(serialize_with = "ts::ser")]
    pub since: DateTime<Utc>,
    #[serde(serialize_with = "ts::opt::ser")]
    pub notified_at: Option<DateTime<Utc>>,
}

#[derive(Debug, Serialize, Clone)]
pub struct NotificationEventView {
    pub id: i64,
    #[serde(serialize_with = "ts::ser")]
    pub at: DateTime<Utc>,
    pub event: String,
    pub kind: Option<String>,
    pub title: String,
    pub message: String,
    pub delivered: bool,
    pub error: Option<String>,
}

/// Never includes the ntfy topic or credentials.
#[derive(Debug, Serialize, Clone)]
pub struct NotificationStatus {
    pub enabled: bool,
    pub server: Option<String>,
    pub rules: Option<NotificationRules>,
    pub active: Vec<ActiveAlert>,
    pub recent: Vec<NotificationEventView>,
}

impl NotificationStatus {
    pub fn disabled() -> Self {
        Self { enabled: false, server: None, rules: None, active: Vec::new(), recent: Vec::new() }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use chrono::TimeZone;

    #[test]
    fn times_are_written_like_go() {
        let t = |ns| Utc.with_ymd_and_hms(2026, 10, 12, 6, 38, 15).unwrap().with_nanosecond(ns).unwrap();
        assert_eq!(format_time(&t(0)), "2026-10-12T06:38:15Z");
        assert_eq!(format_time(&t(500_000_000)), "2026-10-12T06:38:15.5Z");
        assert_eq!(format_time(&t(313_602_000)), "2026-10-12T06:38:15.313602Z");
        assert_eq!(format_time(&t(1)), "2026-10-12T06:38:15.000000001Z");
    }

    #[test]
    fn null_stats_serialize_as_null_and_series_keys_omit_unused_fields() {
        let v = serde_json::to_value(Stat::default()).unwrap();
        assert_eq!(v["avg"], serde_json::Value::Null);
        let k = serde_json::to_value(SeriesKey { device: Some("a".into()), ..Default::default() }).unwrap();
        assert_eq!(k, serde_json::json!({"device": "a"}));
    }

    #[test]
    fn result_detail_keeps_the_raw_payload_byte_for_byte() {
        let raw = RawValue::from_string(r#"{"b": 1.0, "a":[1,  2]}"#.to_string()).unwrap();
        let r = TestResult {
            id: "i".into(),
            provider: "ookla".into(),
            trigger: "scheduled".into(),
            device: "d".into(),
            connection_type: "wired".into(),
            tested_at: Utc.with_ymd_and_hms(2026, 1, 1, 0, 0, 0).unwrap(),
            download_mbps: 1.0,
            upload_mbps: 2.0,
            latency_ms: None,
            jitter_ms: None,
            packet_loss_pct: None,
            isp: None,
            server: ServerInfo::default(),
            result_url: None,
        };
        let s = serde_json::to_string(&ResultDetail { result: r, raw }).unwrap();
        assert!(s.ends_with(r#","raw":{"b": 1.0, "a":[1,  2]}}"#), "{s}");
        assert!(s.contains(r#""connectionType":"wired""#) && s.contains(r#""testedAt":"2026-01-01T00:00:00Z""#));
    }
}
