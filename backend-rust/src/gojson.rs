//! Reads JSON the way Go's `encoding/json` does, for the places where the two backends must agree on what is acceptable.
//! The quirks that matter to ingest validation:
//!  - object keys match struct fields ignoring ASCII case, and when a key repeats, the last one wins;
//!  - `null` stands for "nothing" in a string, number or struct field (no error, the zero value);
//!  - any value of the wrong JSON type is an error, and one such error rejects the whole struct;
//!  - a `map[string]RawMessage` (exact keys) is a different view of the same object than the struct (folded keys).

use serde::Deserialize;
use serde::de::{Deserializer, MapAccess, Visitor};
use serde_json::value::RawValue;

/// A member had the wrong JSON type for the field it fills (Go's `UnmarshalTypeError`).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub struct TypeMismatch;

/// What kind of JSON value a raw member holds, from its first byte (the way the Go code looks at `v[0]`).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Kind {
    Object,
    Array,
    String,
    Number,
    Bool,
    Null,
}

pub fn kind(raw: &RawValue) -> Kind {
    match raw.get().as_bytes().first() {
        Some(b'{') => Kind::Object,
        Some(b'[') => Kind::Array,
        Some(b'"') => Kind::String,
        Some(b'n') => Kind::Null,
        Some(b't' | b'f') => Kind::Bool,
        _ => Kind::Number,
    }
}

pub fn is_null(raw: Option<&RawValue>) -> bool {
    raw.is_some_and(|r| kind(r) == Kind::Null)
}

/// The members of one JSON object, in document order.
#[derive(Debug, Default)]
pub struct Members(Vec<(String, Box<RawValue>)>);

impl<'de> Deserialize<'de> for Members {
    fn deserialize<D: Deserializer<'de>>(d: D) -> Result<Self, D::Error> {
        struct V;
        impl<'de> Visitor<'de> for V {
            type Value = Members;
            fn expecting(&self, f: &mut std::fmt::Formatter) -> std::fmt::Result {
                f.write_str("a JSON object")
            }
            fn visit_map<A: MapAccess<'de>>(self, mut m: A) -> Result<Members, A::Error> {
                let mut out = Vec::new();
                while let Some((k, v)) = m.next_entry::<String, Box<RawValue>>()? {
                    out.push((k, v));
                }
                Ok(Members(out))
            }
        }
        d.deserialize_map(V)
    }
}

impl Members {
    /// Go's `json.Unmarshal(raw, &struct)`: an object gives its members, `null` gives none, anything else is a type error.
    pub fn decode(raw: Option<&RawValue>) -> Result<Members, TypeMismatch> {
        let Some(raw) = raw else { return Ok(Members::default()) };
        match kind(raw) {
            Kind::Null => Ok(Members::default()),
            Kind::Object => serde_json::from_str(raw.get()).map_err(|_| TypeMismatch),
            _ => Err(TypeMismatch),
        }
    }

    /// The value a struct field of this name receives: the last member whose key matches ignoring ASCII case.
    pub fn field(&self, name: &str) -> Option<&RawValue> {
        self.0.iter().rev().find(|(k, _)| k.eq_ignore_ascii_case(name)).map(|(_, v)| v.as_ref())
    }

    /// Like `field`, for a struct-typed field: every occurrence is decoded into the same struct, so their members add up.
    pub fn nested(&self, name: &str) -> Result<Members, TypeMismatch> {
        let mut all = Vec::new();
        for (k, v) in &self.0 {
            if k.eq_ignore_ascii_case(name) {
                all.extend(Members::decode(Some(v))?.0);
            }
        }
        Ok(Members(all))
    }

    /// The value in a `map[string]json.RawMessage`: exact key, last one wins.
    pub fn exact(&self, name: &str) -> Option<&RawValue> {
        self.0.iter().rev().find(|(k, _)| k == name).map(|(_, v)| v.as_ref())
    }
}

/// A `string` struct field: absent and `null` are "", any other type is an error.
pub fn string(raw: Option<&RawValue>) -> Result<String, TypeMismatch> {
    match raw {
        None => Ok(String::new()),
        Some(r) => match kind(r) {
            Kind::Null => Ok(String::new()),
            Kind::String => serde_json::from_str(r.get()).map_err(|_| TypeMismatch),
            _ => Err(TypeMismatch),
        },
    }
}

/// A `*float64` struct field: absent and `null` are None, a number is its value (out of range is an error), anything else is an error.
pub fn float(raw: Option<&RawValue>) -> Result<Option<f64>, TypeMismatch> {
    match raw {
        None => Ok(None),
        Some(r) => match kind(r) {
            Kind::Null => Ok(None),
            Kind::Number => r.get().parse::<f64>().ok().filter(|v| v.is_finite()).map(Some).ok_or(TypeMismatch),
            _ => Err(TypeMismatch),
        },
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn raw(s: &str) -> Box<RawValue> {
        RawValue::from_string(s.to_string()).unwrap()
    }

    #[test]
    fn keys_match_ignoring_case_and_the_last_one_wins() {
        let m = Members::decode(Some(&raw(r#"{"Type":"a","type":"b","TYPE":"c","other":1}"#))).unwrap();
        assert_eq!(m.field("type").unwrap().get(), r#""c""#);
        assert_eq!(m.exact("type").unwrap().get(), r#""b""#);
        assert!(m.exact("Missing").is_none() && m.field("missing").is_none());
    }

    #[test]
    fn null_is_nothing_and_other_types_are_errors() {
        assert!(Members::decode(Some(&raw("null"))).is_ok());
        assert!(Members::decode(None).is_ok());
        for bad in ["5", "\"x\"", "[]", "true"] {
            assert!(Members::decode(Some(&raw(bad))).is_err(), "{bad}");
        }
        assert_eq!(string(Some(&raw("null"))), Ok(String::new()));
        assert_eq!(string(Some(&raw(r#""aé""#))), Ok("aé".to_string()));
        assert!(string(Some(&raw("5"))).is_err());
        assert_eq!(float(Some(&raw("null"))), Ok(None));
        assert_eq!(float(None), Ok(None));
        assert_eq!(float(Some(&raw("-1.5e2"))), Ok(Some(-150.0)));
        assert!(float(Some(&raw("1e999"))).is_err()); // Go: cannot unmarshal number 1e999 into float64
        assert!(float(Some(&raw("\"5\""))).is_err());
        assert!(float(Some(&raw("true"))).is_err());
    }

    #[test]
    fn a_struct_field_seen_twice_adds_up() {
        let m = Members::decode(Some(&raw(r#"{"ping":{"latency":1},"Ping":{"jitter":2},"ping":null}"#))).unwrap();
        let ping = m.nested("ping").unwrap();
        assert_eq!(ping.field("latency").unwrap().get(), "1");
        assert_eq!(ping.field("jitter").unwrap().get(), "2");
        let bad = Members::decode(Some(&raw(r#"{"ping":5}"#))).unwrap();
        assert!(bad.nested("ping").is_err());
    }

    #[test]
    fn kinds() {
        for (s, k) in [
            ("{}", Kind::Object),
            ("[1]", Kind::Array),
            ("\"x\"", Kind::String),
            ("null", Kind::Null),
            ("true", Kind::Bool),
            ("false", Kind::Bool),
            ("-3", Kind::Number),
            ("0.5", Kind::Number),
        ] {
            assert_eq!(kind(&raw(s)), k, "{s}");
        }
    }
}
