//! HTTP handlers for browser notifications. Ports backend-go/internal/api/push.go.

use std::net::SocketAddr;

use axum::{
    extract::{ConnectInfo, Request, State},
    http::{StatusCode, header},
    response::{IntoResponse, Response},
};
use serde::Serialize;

use crate::{
    app::AppState,
    auth::Limiter,
    body::first_json_value,
    gojson::{self, Members},
    ondemand_api::client_ip,
    problem::{FieldError, Problem},
    push::{MAX_ENDPOINT_LEN, PushError, Service},
};

const MAX_BODY: usize = 4096 + 1024;

#[derive(Serialize)]
struct PushConfig {
    enabled: bool,
    #[serde(rename = "publicKey", skip_serializing_if = "Option::is_none")]
    public_key: Option<String>,
}

pub async fn config(State(s): State<AppState>) -> Response {
    let body = match &s.pusher {
        Some(p) => PushConfig { enabled: true, public_key: Some(p.public_key().to_string()) },
        None => PushConfig { enabled: false, public_key: None },
    };
    (StatusCode::OK, [(header::CONTENT_TYPE, "application/json")], serde_json::to_vec(&body).unwrap_or_default()).into_response()
}

/// The push service, or 503 when push is off; 429 when the caller is asking too often.
fn gate<'a>(s: &'a AppState, headers: &axum::http::HeaderMap, peer: Option<SocketAddr>, limiter: &Limiter) -> Result<&'a Service, Problem> {
    let Some(p) = s.pusher.as_deref() else {
        return Err(Problem::new(StatusCode::SERVICE_UNAVAILABLE, "browser notifications are not enabled on this server"));
    };
    if let Err(retry) = limiter.allow(&client_ip(s.trust_proxy(), headers, peer)) {
        return Err(Problem::new(StatusCode::TOO_MANY_REQUESTS, "too many requests; try again shortly")
            .with_header(header::RETRY_AFTER, &retry.to_string()));
    }
    Ok(p)
}

fn problem(e: PushError) -> Problem {
    match e {
        PushError::Validation(v) => {
            Problem::invalid("request validation failed", vec![FieldError { field: v.field.into(), message: v.message.into() }])
        }
        PushError::TooMany => Problem::new(StatusCode::CONFLICT, "too many devices have notifications turned on"),
        PushError::Unknown => Problem::new(StatusCode::NOT_FOUND, "this device is not subscribed"),
        PushError::Refused(m) => Problem::new(StatusCode::BAD_GATEWAY, format!("the push service refused the message: {m}")),
        PushError::Db(e) => e.into(),
    }
}

/// `{"endpoint": "..."}`: present, no control characters (a NUL would be refused by the database as a 500), at most 2048 long.
async fn endpoint_of(body: axum::body::Body) -> Result<String, Problem> {
    let missing = || {
        Problem::invalid(
            "body must be JSON with the subscription's endpoint",
            vec![FieldError { field: "endpoint".into(), message: "is required".into() }],
        )
    };
    let raw = first_json_value(body, MAX_BODY).await.ok_or_else(missing)?;
    let top = Members::decode(Some(&raw)).map_err(|_| missing())?;
    let endpoint = gojson::string(top.field("endpoint")).map_err(|_| missing())?;
    if endpoint.is_empty() {
        return Err(missing());
    }
    let refuse = |message: &str| {
        Problem::invalid("request validation failed", vec![FieldError { field: "endpoint".into(), message: message.into() }])
    };
    if endpoint.chars().any(char::is_control) {
        return Err(refuse("must not contain control characters"));
    }
    if endpoint.len() > MAX_ENDPOINT_LEN {
        return Err(refuse("must be at most 2048 characters"));
    }
    Ok(endpoint)
}

fn no_content() -> Response {
    StatusCode::NO_CONTENT.into_response()
}

fn peer(parts: &axum::http::request::Parts) -> Option<SocketAddr> {
    parts.extensions.get::<ConnectInfo<SocketAddr>>().map(|c| c.0)
}

pub async fn subscribe(State(s): State<AppState>, req: Request) -> Result<Response, Problem> {
    let (parts, body) = req.into_parts();
    let pusher = gate(&s, &parts.headers, peer(&parts), &s.push_limiter)?;
    let bad = || Problem::new(StatusCode::BAD_REQUEST, "body must be a browser push subscription: endpoint and keys");
    let raw = first_json_value(body, MAX_BODY).await.ok_or_else(bad)?;
    let top = Members::decode(Some(&raw)).map_err(|_| bad())?;
    let endpoint = gojson::string(top.field("endpoint")).map_err(|_| bad())?;
    let keys = top.nested("keys").map_err(|_| bad())?;
    let (p256dh, auth) = (gojson::string(keys.field("p256dh")).map_err(|_| bad())?, gojson::string(keys.field("auth")).map_err(|_| bad())?);
    let ua = parts.headers.get(header::USER_AGENT).and_then(|v| v.to_str().ok()).unwrap_or("");
    pusher.subscribe(&endpoint, &p256dh, &auth, ua).await.map_err(problem)?;
    Ok(no_content())
}

pub async fn unsubscribe(State(s): State<AppState>, req: Request) -> Result<Response, Problem> {
    let (parts, body) = req.into_parts();
    let pusher = gate(&s, &parts.headers, peer(&parts), &s.push_limiter)?;
    let endpoint = endpoint_of(body).await?;
    // idempotent, and it never says whether the endpoint was known
    pusher.unsubscribe(&endpoint).await.map_err(problem)?;
    Ok(no_content())
}

pub async fn test(State(s): State<AppState>, req: Request) -> Result<Response, Problem> {
    let (parts, body) = req.into_parts();
    let pusher = gate(&s, &parts.headers, peer(&parts), &s.push_test_limiter)?;
    let endpoint = endpoint_of(body).await?;
    pusher.send_test(&endpoint).await.map_err(problem)?;
    Ok(no_content())
}
