//! Query-string parsing and validation. Mirrors backend-go/internal/api/params.go so both backends accept and refuse the same
//! requests, and a cursor from one is valid on the other.

use base64::{
    Engine, alphabet,
    engine::{DecodePaddingMode, GeneralPurpose, GeneralPurposeConfig},
};
use chrono::{DateTime, Utc};
use serde::{Deserialize, Serialize};
use std::str::FromStr;
use uuid::Uuid;

use crate::problem::FieldError;

pub const PROVIDERS: &[&str] = &["ookla", "ndt7"];
pub const CONNECTION_TYPES: &[&str] = &["wired", "wireless"];
pub const TRIGGERS: &[&str] = &["scheduled", "on_demand"];
pub const INTERVALS: &[&str] = &["raw", "hour", "day", "week", "month", "quarter", "year"];
pub const GROUP_FIELDS: &[&str] = &["provider", "device", "connectionType"];

const MAX_DEVICE_FILTERS: usize = 50;
const MAX_DEVICE_LEN: usize = 64;

/// The query string, decoded the way Go's `url.ParseQuery` does it: pairs with a bad percent escape or a semicolon are dropped,
/// `+` is a space, and bytes that are not UTF-8 survive (so they can be refused with a clear message instead of silently changed).
#[derive(Debug, Default)]
pub struct Query {
    pairs: Vec<(String, String, bool)>, // key, value, value was not valid UTF-8
}

impl Query {
    pub fn parse(raw: Option<&str>) -> Self {
        let mut pairs = Vec::new();
        for part in raw.unwrap_or("").split('&') {
            if part.is_empty() || part.contains(';') {
                continue;
            }
            let (k, v) = part.split_once('=').unwrap_or((part, ""));
            let (Some((k, _)), Some((v, bad))) = (unescape(k), unescape(v)) else { continue };
            pairs.push((k, v, bad));
        }
        Self { pairs }
    }

    pub fn has(&self, name: &str) -> bool {
        self.pairs.iter().any(|(k, _, _)| k == name)
    }

    /// The first value, or "" (like Go's `Values.Get`).
    pub fn get(&self, name: &str) -> &str {
        self.pairs.iter().find(|(k, _, _)| k == name).map_or("", |(_, v, _)| v.as_str())
    }

    fn values(&self, name: &str) -> impl Iterator<Item = (&str, bool)> {
        self.pairs.iter().filter(move |(k, _, _)| k == name).map(|(_, v, bad)| (v.as_str(), *bad))
    }
}

/// Percent-decodes one component. None for an invalid escape; the flag says the bytes were not UTF-8.
fn unescape(s: &str) -> Option<(String, bool)> {
    let b = s.as_bytes();
    let mut out = Vec::with_capacity(b.len());
    let mut i = 0;
    while i < b.len() {
        match b[i] {
            b'%' => {
                let hex = |c: u8| (c as char).to_digit(16);
                let (h, l) = (b.get(i + 1).and_then(|c| hex(*c)), b.get(i + 2).and_then(|c| hex(*c)));
                out.push((h? * 16 + l?) as u8);
                i += 3;
            }
            b'+' => {
                out.push(b' ');
                i += 1
            }
            c => {
                out.push(c);
                i += 1
            }
        }
    }
    Some(match String::from_utf8(out) {
        Ok(s) => (s, false),
        Err(e) => (String::from_utf8_lossy(e.as_bytes()).into_owned(), true),
    })
}

/// Collects validation problems so one response can report all of them.
#[derive(Debug, Default)]
pub struct Errs(pub Vec<FieldError>);

impl Errs {
    pub fn add(&mut self, field: &str, message: impl Into<String>) {
        self.0.push(FieldError { field: field.into(), message: message.into() });
    }
    pub fn is_empty(&self) -> bool {
        self.0.is_empty()
    }
}

/// A comma-separated query parameter (it may also be repeated). An empty item is an error.
fn list(q: &Query, name: &str, e: &mut Errs) -> Vec<String> {
    let mut out = Vec::new();
    for (v, _) in q.values(name) {
        for part in v.split(',') {
            let part = part.trim();
            if part.is_empty() {
                e.add(name, "must not contain empty values");
                return out;
            }
            out.push(part.to_string());
        }
    }
    out
}

pub fn enum_list(q: &Query, name: &str, allowed: &[&str], e: &mut Errs) -> Vec<String> {
    let vals = list(q, name, e);
    for v in &vals {
        if !allowed.contains(&v.as_str()) {
            e.add(name, format!("{v:?} is not one of {}", allowed.join(", ")));
        }
    }
    vals
}

/// RFC 3339 the way Go's `time.RFC3339` layout reads it: a capital T and Z, no spaces.
pub fn parse_rfc3339(s: &str) -> Option<DateTime<Utc>> {
    if s.contains(' ') || s.contains('t') || s.contains('z') {
        return None;
    }
    let t = DateTime::parse_from_rfc3339(s).ok()?.with_timezone(&Utc);
    // chrono reads a leap second (":60") as a time with 1,000,000,000+ nanoseconds; Go refuses it
    (t.timestamp_subsec_nanos() < 1_000_000_000).then_some(t)
}

fn parse_time(q: &Query, name: &str, e: &mut Errs) -> Option<DateTime<Utc>> {
    if !q.has(name) {
        return None;
    }
    let t = parse_rfc3339(q.get(name));
    if t.is_none() {
        e.add(name, "must be an RFC 3339 date-time, e.g. 2026-06-01T00:00:00Z");
    }
    t
}

/// Which results a request is about. Empty lists and None mean "no restriction".
#[derive(Debug, Default, Clone)]
pub struct Filter {
    pub from: Option<DateTime<Utc>>,
    pub to: Option<DateTime<Utc>>,
    pub providers: Vec<String>,
    pub devices: Vec<String>,
    pub connection_types: Vec<String>,
    pub triggers: Vec<String>,
}

pub fn parse_filter(q: &Query, e: &mut Errs) -> Filter {
    let f = Filter {
        from: parse_time(q, "from", e),
        to: parse_time(q, "to", e),
        providers: enum_list(q, "provider", PROVIDERS, e),
        connection_types: enum_list(q, "connectionType", CONNECTION_TYPES, e),
        triggers: enum_list(q, "trigger", TRIGGERS, e),
        devices: list(q, "device", e),
    };
    if let (Some(a), Some(b)) = (f.from, f.to) {
        if a > b {
            e.add("from", "must not be after `to`");
        }
    }
    if f.devices.len() > MAX_DEVICE_FILTERS {
        e.add("device", format!("at most {MAX_DEVICE_FILTERS} devices"));
    }
    let any_bad_bytes = q.values("device").any(|(_, bad)| bad);
    for d in &f.devices {
        if d.len() > MAX_DEVICE_LEN {
            e.add("device", format!("names are at most {MAX_DEVICE_LEN} characters"));
            break;
        }
        if any_bad_bytes || !clean_text(d) {
            e.add("device", "names must be valid UTF-8 without control characters");
            break;
        }
    }
    f
}

pub fn parse_limit(q: &Query, default: i64, max: i64, e: &mut Errs) -> i64 {
    if !q.has("limit") {
        return default;
    }
    match i64::from_str(q.get("limit")) {
        Ok(n) if (1..=max).contains(&n) => n,
        _ => {
            e.add("limit", format!("must be an integer from 1 to {max}"));
            default
        }
    }
}

/// No control characters (PostgreSQL cannot store NUL in text at all).
pub fn clean_text(s: &str) -> bool {
    !s.chars().any(char::is_control)
}

/// The canonical 8-4-4-4-12 form (the uuid crate also accepts braces and no dashes, which no id here ever looks like).
pub fn is_uuid(s: &str) -> bool {
    s.len() == 36 && Uuid::parse_str(s).is_ok()
}

pub fn valid_timezone(name: &str) -> bool {
    !name.is_empty() && name != "Local" && chrono_tz::Tz::from_str(name).is_ok()
}

/// The position after the last row of a page (results are newest first). Opaque to clients: base64url of `{"t":..,"id":..}`,
/// byte for byte what the Go backend writes and reads.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
pub struct Cursor {
    pub t: String,
    pub id: String,
}

const B64: GeneralPurpose = GeneralPurpose::new(
    &alphabet::URL_SAFE,
    GeneralPurposeConfig::new()
        .with_encode_padding(false)
        .with_decode_padding_mode(DecodePaddingMode::RequireNone)
        .with_decode_allow_trailing_bits(true),
);

pub fn encode_cursor(t: DateTime<Utc>, id: &str) -> String {
    let c = Cursor { t: crate::model::format_time(&t), id: id.to_string() };
    B64.encode(serde_json::to_vec(&c).unwrap_or_default())
}

pub fn decode_cursor(s: &str) -> Option<(DateTime<Utc>, Uuid)> {
    let c: Cursor = serde_json::from_slice(&B64.decode(s).ok()?).ok()?;
    let t = parse_rfc3339(&c.t)?;
    if t == DateTime::<Utc>::MIN_UTC || c.t.starts_with("0001-01-01T00:00:00") {
        return None; // Go refuses the zero time
    }
    Some((t, Uuid::parse_str(&c.id).ok().filter(|_| c.id.len() == 36)?))
}

#[cfg(test)]
mod tests {
    use super::*;

    fn filter(raw: &str) -> (Filter, Vec<FieldError>) {
        let mut e = Errs::default();
        let f = parse_filter(&Query::parse(Some(raw)), &mut e);
        (f, e.0)
    }

    #[test]
    fn lists_accept_commas_and_repeats() {
        let (f, e) = filter("provider=ookla,ndt7&provider=ookla&device=a%2Cb&device=c");
        assert!(e.is_empty(), "{e:?}");
        assert_eq!(f.providers, ["ookla", "ndt7", "ookla"]);
        assert_eq!(f.devices, ["a", "b", "c"]);
    }

    #[test]
    fn empty_items_and_unknown_values_are_reported_together() {
        let (_, e) = filter("provider=ookla,,ndt7&connectionType=satellite&trigger=");
        let fields: Vec<_> = e.iter().map(|x| x.field.as_str()).collect();
        assert_eq!(fields, ["provider", "connectionType", "trigger"]);
    }

    #[test]
    fn times_follow_go_rfc3339() {
        for ok in ["2026-06-01T00:00:00Z", "2026-06-01T00:00:00.123456Z", "2026-06-01T00:00:00-07:00"] {
            assert!(filter(&format!("from={ok}")).1.is_empty(), "{ok}");
        }
        for bad in ["2026-06-01", "2026-06-01 00:00:00Z", "2026-06-01t00:00:00z", "yesterday", ""] {
            assert_eq!(filter(&format!("from={bad}")).1.len(), 1, "{bad}");
        }
        assert_eq!(filter("from=2026-06-02T00:00:00Z&to=2026-06-01T00:00:00Z").1.len(), 1);
        assert_eq!(parse_rfc3339("2026-06-01T00:00:00-07:00").unwrap().to_rfc3339(), "2026-06-01T07:00:00+00:00");
        assert!(parse_rfc3339("2026-06-30T23:59:60Z").is_none(), "leap seconds are refused, as Go does");
    }

    #[test]
    fn device_names_are_checked() {
        assert!(filter("device=my%20laptop").1.is_empty());
        assert_eq!(filter(&format!("device={}", "x".repeat(65))).1.len(), 1);
        assert!(filter(&format!("device={}", "x".repeat(64))).1.is_empty());
        assert_eq!(filter("device=a%00b").1.len(), 1); // NUL
        assert_eq!(filter("device=a%0Ab").1.len(), 1); // newline
        assert_eq!(filter("device=%ff%fe").1.len(), 1); // not UTF-8
        let many = (0..51).map(|i| format!("device=d{i}")).collect::<Vec<_>>().join("&");
        assert_eq!(filter(&many).1.len(), 1);
    }

    #[test]
    fn broken_pairs_are_dropped_not_fatal() {
        let q = Query::parse(Some("device=ok&bad=%zz&semi=a;b&limit=5&&=x"));
        assert_eq!(q.get("device"), "ok");
        assert!(!q.has("bad") && !q.has("semi"));
        assert_eq!(q.get("limit"), "5");
        assert_eq!(Query::parse(Some("a=1+2%20%7e")).get("a"), "1 2 ~");
        assert_eq!(Query::parse(None).get("a"), "");
    }

    #[test]
    fn limit() {
        let lim = |s: &str| {
            let mut e = Errs::default();
            let n = parse_limit(&Query::parse(Some(s)), 100, 500, &mut e);
            (n, e.0.len())
        };
        assert_eq!(lim(""), (100, 0));
        assert_eq!(lim("limit=1"), (1, 0));
        assert_eq!(lim("limit=500"), (500, 0));
        assert_eq!(lim("limit=%2B7"), (7, 0)); // a literal plus sign
        assert_eq!(lim("limit=+7"), (100, 1)); // "+" in a query string is a space
        for bad in ["limit=0", "limit=501", "limit=-1", "limit=", "limit=abc", "limit=1.5", "limit=99999999999999999999"] {
            assert_eq!(lim(bad), (100, 1), "{bad}");
        }
    }

    #[test]
    fn timezones() {
        for ok in ["UTC", "America/Phoenix", "Europe/Berlin", "Asia/Kolkata", "US/Pacific"] {
            assert!(valid_timezone(ok), "{ok}");
        }
        for bad in ["", "Local", "Mars/Olympus", "america/phoenix", "../etc/passwd", "UTC; DROP TABLE"] {
            assert!(!valid_timezone(bad), "{bad}");
        }
    }

    #[test]
    fn cursors_round_trip_and_match_the_go_format() {
        let t = parse_rfc3339("2026-10-12T06:38:15.313602Z").unwrap();
        let id = "6dadce9a-a9ec-4709-93d9-2893744df037";
        let c = encode_cursor(t, id);
        assert_eq!(decode_cursor(&c), Some((t, Uuid::parse_str(id).unwrap())));
        // exactly what Go's json.Marshal(Cursor{T, ID}) + base64.RawURLEncoding produce
        assert_eq!(c, "eyJ0IjoiMjAyNi0xMC0xMlQwNjozODoxNS4zMTM2MDJaIiwiaWQiOiI2ZGFkY2U5YS1hOWVjLTQ3MDktOTNkOS0yODkzNzQ0ZGYwMzcifQ");
    }

    #[test]
    fn bad_cursors_are_refused() {
        for bad in [
            "",
            "!!!",
            "e30",
            "bm90IGpzb24",
            "eyJ0IjoiMDAwMS0wMS0wMVQwMDowMDowMFoiLCJpZCI6IjZkYWRjZTlhLWE5ZWMtNDcwOS05M2Q5LTI4OTM3NDRkZjAzNyJ9",
        ] {
            assert_eq!(decode_cursor(bad), None, "{bad}");
        }
        // 36 characters long, but not a UUID: refused here rather than passed to the database
        let junk = B64.encode(br#"{"t":"2026-10-12T06:38:15Z","id":"zzzzzzzz-zzzz-zzzz-zzzz-zzzzzzzzzzzz"}"#);
        assert_eq!(decode_cursor(&junk), None);
    }
}
