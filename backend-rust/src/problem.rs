//! RFC 9457 problem details, as docs/openapi.yaml defines them.

use axum::{
    http::{HeaderName, HeaderValue, StatusCode, header},
    response::{IntoResponse, Response},
};
use serde::Serialize;

#[derive(Debug, Clone, Serialize, PartialEq)]
pub struct FieldError {
    pub field: String,
    pub message: String,
}

#[derive(Debug, Serialize)]
struct Body<'a> {
    r#type: &'a str,
    title: &'a str,
    status: u16,
    #[serde(skip_serializing_if = "str::is_empty")]
    detail: &'a str,
    #[serde(skip_serializing_if = "<[FieldError]>::is_empty")]
    errors: &'a [FieldError],
}

/// An error answered as `application/problem+json`.
#[derive(Debug)]
pub struct Problem {
    pub status: StatusCode,
    pub detail: String,
    pub errors: Vec<FieldError>,
    pub headers: Vec<(HeaderName, HeaderValue)>,
}

impl Problem {
    pub fn new(status: StatusCode, detail: impl Into<String>) -> Self {
        Self { status, detail: detail.into(), errors: Vec::new(), headers: Vec::new() }
    }

    pub fn invalid(detail: impl Into<String>, errors: Vec<FieldError>) -> Self {
        Self { status: StatusCode::BAD_REQUEST, detail: detail.into(), errors, headers: Vec::new() }
    }

    pub fn with_header(mut self, name: HeaderName, value: &str) -> Self {
        if let Ok(v) = HeaderValue::from_str(value) {
            self.headers.push((name, v));
        }
        self
    }

    pub fn internal() -> Self {
        Self::new(StatusCode::INTERNAL_SERVER_ERROR, "internal error")
    }
}

impl IntoResponse for Problem {
    fn into_response(self) -> Response {
        let body = Body {
            r#type: "about:blank",
            title: match self.status {
                // the same words the Go backend uses (Go's http.StatusText), where the HTTP library's differ
                StatusCode::PAYLOAD_TOO_LARGE => "Request Entity Too Large",
                other => other.canonical_reason().unwrap_or(""),
            },
            status: self.status.as_u16(),
            detail: &self.detail,
            errors: &self.errors,
        };
        let json = serde_json::to_vec(&body).unwrap_or_default();
        let mut res = (self.status, [(header::CONTENT_TYPE, "application/problem+json")], json).into_response();
        for (name, value) in self.headers {
            res.headers_mut().insert(name, value);
        }
        res
    }
}

/// Database and other unexpected failures are logged, and the client only ever sees "internal error".
impl From<sqlx::Error> for Problem {
    fn from(e: sqlx::Error) -> Self {
        tracing::error!(err = %e, "request failed");
        Problem::internal()
    }
}
