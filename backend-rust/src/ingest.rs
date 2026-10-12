//! Validation of what `POST /api/v1/results` receives. A port of backend-go/internal/api/ingest.go: the same rules, the same
//! field names in the same order, so a client gets the same answer from either backend.

use chrono::{DateTime, Duration, Utc};
use serde_json::value::RawValue;

use crate::{
    gojson::{self, Kind, Members, TypeMismatch, is_null, kind},
    params::{CONNECTION_TYPES, Errs, PROVIDERS, TRIGGERS, clean_text, parse_rfc3339},
    problem::FieldError,
};

pub const MAX_BODY_BYTES: usize = 64 * 1024;

// Upper bounds mirror the database check constraint; see sql/postgres/005_providers_and_tests.sql.
const MAX_MBPS: f64 = 1_000_000.0; // 1 Tbit/s
const MAX_BANDWIDTH: f64 = MAX_MBPS * 125_000.0; // bytes per second
const MAX_MS: f64 = 1_000_000.0;

/// Go's `%g` for the few bounds that appear in messages: 100, 1e+06, 1.25e+11.
fn go_g(v: f64) -> String {
    let exp = if v == 0.0 { 0 } else { v.abs().log10().floor() as i32 };
    if !(-4..6).contains(&exp) {
        let s = format!("{v:e}"); // 1.25e11
        let (m, e) = s.split_once('e').unwrap_or((&s, "0"));
        let e: i32 = e.parse().unwrap_or(0);
        return format!("{m}e{}{:02}", if e < 0 { '-' } else { '+' }, e.abs());
    }
    format!("{v}")
}

/// An RFC 3339 time between 2000-01-01 and one day from now.
fn check_time(e: &mut Errs, field: &str, v: &str) -> Option<DateTime<Utc>> {
    let Some(t) = parse_rfc3339(v) else {
        e.add(field, "must be an RFC 3339 date-time");
        return None;
    };
    let earliest = DateTime::parse_from_rfc3339("2000-01-01T00:00:00Z").map(|t| t.with_timezone(&Utc)).unwrap_or_default();
    if t < earliest || t > Utc::now() + Duration::hours(24) {
        e.add(field, "must be between 2000-01-01 and tomorrow");
        return None;
    }
    Some(t)
}

/// A member that is present must have the expected JSON type. Missing is fine; an explicit null is not.
fn optional_kind(e: &mut Errs, field: &str, v: Option<&RawValue>, want: Kind) {
    let Some(v) = v else { return };
    if kind(v) == want {
        return;
    }
    e.add(field, if want == Kind::Object { "must be an object" } else { "must be a string" });
}

fn reject_null(e: &mut Errs, field: &str, v: Option<&RawValue>) {
    if is_null(v) {
        e.add(field, "must be a number, not null");
    }
}

/// "" (stored as null) or an absolute http(s) URL, matching the contract's `format: uri`.
fn valid_result_url(s: &str) -> bool {
    if s.is_empty() {
        return true;
    }
    if s.len() > 2048 || !clean_text(s) || s.contains('\\') {
        return false;
    }
    // Go reads "http:example.com" as an opaque URL with no host; the WHATWG parser would turn it into one. Require the slashes.
    let Some((scheme, rest)) = s.split_once(':') else { return false };
    if !(scheme.eq_ignore_ascii_case("http") || scheme.eq_ignore_ascii_case("https")) || !rest.starts_with("//") {
        return false;
    }
    // "https:///path": Go sees no host, the WHATWG parser reads "path" as the host. The authority must be there.
    if rest[2..].is_empty() || rest[2..].starts_with(['/', '?', '#']) {
        return false;
    }
    // Go refuses a bad percent escape anywhere; the WHATWG parser lets it through.
    let b = s.as_bytes();
    for (i, c) in b.iter().enumerate() {
        if *c == b'%' && !(b.get(i + 1).is_some_and(u8::is_ascii_hexdigit) && b.get(i + 2).is_some_and(u8::is_ascii_hexdigit)) {
            return false;
        }
    }
    url::Url::parse(s).is_ok_and(|u| matches!(u.scheme(), "http" | "https") && u.host_str().is_some_and(|h| !h.is_empty()))
}

fn check_range(e: &mut Errs, field: &str, v: Option<f64>, max: f64, required: bool) {
    match v {
        None if required => e.add(field, "is required"),
        Some(v) if v < 0.0 || v > max || v.is_nan() => e.add(field, format!("must be between 0 and {}", go_g(max))),
        _ => {}
    }
}

const NOT_OOKLA: &str = "must be the JSON object printed by `speedtest --format=json`";
pub const NOT_NDT7: &str = "must be an NDT7 summary object (check field types: server.* are strings, raw is an object)";

/// The Ookla CLI's `--format=json` result. `prefix` is "result." (so a bad payload as a whole is reported under "result.").
pub fn validate_ookla(raw: Option<&RawValue>, prefix: &str) -> Vec<FieldError> {
    let mut e = Errs::default();
    let f = |name: &str| format!("{prefix}{name}");
    // the struct view (keys ignoring case); a wrong type anywhere rejects the whole payload
    let decoded = (|| -> Result<_, TypeMismatch> {
        let top = Members::decode(raw)?;
        let ping = top.nested("ping")?;
        let download = top.nested("download")?;
        let upload = top.nested("upload")?;
        Ok((
            gojson::string(top.field("type"))?,
            gojson::string(top.field("timestamp"))?,
            gojson::float(ping.field("latency"))?,
            gojson::float(ping.field("jitter"))?,
            gojson::float(download.field("bandwidth"))?,
            gojson::float(upload.field("bandwidth"))?,
            gojson::float(top.field("packetLoss"))?,
        ))
    })();
    let (Some(raw), Ok((typ, ts, latency, jitter, down, up, loss))) = (raw, decoded) else {
        e.add(prefix, NOT_OOKLA);
        return e.0;
    };
    let top = Members::decode(Some(raw)).unwrap_or_default();
    if typ != "result" {
        e.add(&f("type"), r#"must be "result""#);
    }
    check_time(&mut e, &f("timestamp"), &ts);
    check_range(&mut e, &f("download.bandwidth"), down, MAX_BANDWIDTH, true);
    check_range(&mut e, &f("upload.bandwidth"), up, MAX_BANDWIDTH, true);
    check_range(&mut e, &f("ping.latency"), latency, MAX_MS, false);
    check_range(&mut e, &f("ping.jitter"), jitter, MAX_MS, false);
    check_range(&mut e, &f("packetLoss"), loss, 100.0, false);
    // optional members: when present they must have the documented JSON type (null is not "absent")
    let result = top.field("result");
    optional_kind(&mut e, &f("result"), result, Kind::Object);
    optional_kind(&mut e, &f("server"), top.field("server"), Kind::Object);
    optional_kind(&mut e, &f("interface"), top.field("interface"), Kind::Object);
    optional_kind(&mut e, &f("isp"), top.field("isp"), Kind::String);
    // numbers cannot tell null from absent once decoded, so look at the members themselves (exact keys)
    reject_null(&mut e, &f("packetLoss"), top.exact("packetLoss"));
    optional_kind(&mut e, &f("ping"), top.exact("ping"), Kind::Object);
    if let Some(ping) = top.exact("ping").filter(|p| kind(p) == Kind::Object) {
        let ping = Members::decode(Some(ping)).unwrap_or_default();
        reject_null(&mut e, &f("ping.latency"), ping.exact("latency"));
        reject_null(&mut e, &f("ping.jitter"), ping.exact("jitter"));
    }
    if let Some(res) = result.and_then(|r| Members::decode(Some(r)).ok().filter(|_| matches!(kind(r), Kind::Object))) {
        optional_kind(&mut e, &f("result.id"), res.exact("id"), Kind::String);
        optional_kind(&mut e, &f("result.url"), res.exact("url"), Kind::String);
        if let Some(url) = res.exact("url").filter(|u| kind(u) == Kind::String) {
            if let Ok(u) = serde_json::from_str::<String>(url.get()) {
                if !valid_result_url(&u) {
                    e.add(&f("result.url"), "must be empty or an absolute http(s) URL");
                }
            }
        }
    }
    e.0
}

/// The NDT7 summary (docs/openapi.yaml: Ndt7Summary).
pub fn validate_ndt7(raw: Option<&RawValue>, prefix: &str) -> Vec<FieldError> {
    let mut e = Errs::default();
    let f = |name: &str| format!("{prefix}{name}");
    let decoded = (|| -> Result<_, TypeMismatch> {
        let top = Members::decode(raw)?;
        let server = top.nested("server")?;
        Ok((
            gojson::string(top.field("startedAt"))?,
            gojson::string(top.field("finishedAt"))?,
            gojson::float(top.field("downloadMbps"))?,
            gojson::float(top.field("uploadMbps"))?,
            gojson::float(top.field("minRttMs"))?,
            gojson::float(top.field("retransmissionPct"))?,
            // the three server members are RawMessages in Go: any JSON is accepted here, the kind is checked below
            (top.field("raw").map(|r| r.to_owned()), server),
        ))
    })();
    let (Some(raw), Ok((started, finished, down, up, rtt, retrans, (raw_obj, server)))) = (raw, decoded) else {
        e.add(prefix, NOT_NDT7);
        return e.0;
    };
    let top = Members::decode(Some(raw)).unwrap_or_default();
    let t1 = check_time(&mut e, &f("startedAt"), &started);
    let t2 = check_time(&mut e, &f("finishedAt"), &finished);
    if let (Some(a), Some(b)) = (t1, t2) {
        if b < a {
            e.add(&f("finishedAt"), "must not be before startedAt");
        }
    }
    check_range(&mut e, &f("downloadMbps"), down, MAX_MBPS, true);
    check_range(&mut e, &f("uploadMbps"), up, MAX_MBPS, true);
    check_range(&mut e, &f("minRttMs"), rtt, MAX_MS, false);
    check_range(&mut e, &f("retransmissionPct"), retrans, 100.0, false);
    optional_kind(&mut e, &f("raw"), raw_obj.as_deref(), Kind::Object);
    optional_kind(&mut e, &f("server.machine"), server.field("machine"), Kind::String);
    optional_kind(&mut e, &f("server.location"), server.field("location"), Kind::String);
    optional_kind(&mut e, &f("server.country"), server.field("country"), Kind::String);
    optional_kind(&mut e, &f("server"), top.exact("server"), Kind::Object);
    e.0
}

/// A request that passed validation.
#[derive(Debug)]
pub struct Accepted {
    pub provider: String,
    pub connection_type: String,
    pub trigger: String,
    pub result: Box<RawValue>,
}

pub enum Rejected {
    /// The body is not even a JSON object of the right shape.
    NotJson,
    Fields(Vec<FieldError>),
}

/// Checks the whole envelope `{provider, connectionType, trigger?, result}`.
pub fn validate_envelope(body: &[u8]) -> Result<Accepted, Rejected> {
    let raw: Box<RawValue> = serde_json::from_slice(body).map_err(|_| Rejected::NotJson)?;
    let top = Members::decode(Some(&raw)).map_err(|_| Rejected::NotJson)?;
    let provider = gojson::string(top.field("provider")).map_err(|_| Rejected::NotJson)?;
    let connection_type = gojson::string(top.field("connectionType")).map_err(|_| Rejected::NotJson)?;
    let mut trigger = gojson::string(top.field("trigger")).map_err(|_| Rejected::NotJson)?;
    let result = top.field("result");

    let mut e = Errs::default();
    if !PROVIDERS.contains(&provider.as_str()) {
        e.add("provider", "must be one of ookla, ndt7");
    }
    if !CONNECTION_TYPES.contains(&connection_type.as_str()) {
        e.add("connectionType", "must be one of wired, wireless");
    }
    if is_null(top.exact("trigger")) {
        e.add("trigger", "must be a string, not null");
    }
    if trigger.is_empty() {
        trigger = "scheduled".to_string();
    } else if !TRIGGERS.contains(&trigger.as_str()) {
        e.add("trigger", "must be one of scheduled, on_demand");
    }
    let mut fields = e.0;
    match provider.as_str() {
        "ookla" => fields.extend(validate_ookla(result, "result.")),
        "ndt7" => fields.extend(validate_ndt7(result, "result.")),
        _ => {}
    }
    match (fields.is_empty(), result) {
        (true, Some(r)) => Ok(Accepted { provider, connection_type, trigger, result: r.to_owned() }),
        // not reachable (a missing result is reported by the payload validators), but never a panic on a request
        (true, None) => Err(Rejected::Fields(vec![FieldError { field: "result".into(), message: "is required".into() }])),
        (false, _) => Err(Rejected::Fields(fields)),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn go_style_numbers_in_messages() {
        assert_eq!(go_g(100.0), "100");
        assert_eq!(go_g(1_000_000.0), "1e+06");
        assert_eq!(go_g(MAX_BANDWIDTH), "1.25e+11");
    }

    #[test]
    fn result_urls() {
        for ok in
            ["", "https://www.speedtest.net/result/c/1", "http://a", "HTTP://EXAMPLE.com/x?y=1#z", "https://a/b c", "https://a.b:8443/p"]
        {
            assert!(valid_result_url(ok), "{ok:?} should be accepted");
        }
        for bad in [
            "not a url",
            "javascript:alert(1)",
            "ftp://x/y",
            "http:example.com",
            "http://",
            "https:///path",
            "//example.com",
            "https://exa mple.com",
            "http://a\\b",
            "https://a/%zz",
            "https://a/%4",
            "data:text/html,x",
            "https://a/\u{0}",
            &format!("https://a/{}", "x".repeat(2049)),
        ] {
            assert!(!valid_result_url(bad), "{bad:?} should be refused");
        }
    }
}
