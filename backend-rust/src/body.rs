//! Reading a request body the way Go's `json.NewDecoder(r.Body).Decode(&v)` does behind `http.MaxBytesReader`: only the first
//! JSON value counts (anything after it is ignored), and at most `limit` bytes are read.

use axum::body::Body;
use futures_util::StreamExt;
use serde_json::value::RawValue;

pub async fn first_json_value(body: Body, limit: usize) -> Option<Box<RawValue>> {
    let mut stream = body.into_data_stream();
    let mut buf: Vec<u8> = Vec::new();
    while let Some(Ok(chunk)) = stream.next().await {
        buf.extend_from_slice(&chunk);
        if buf.len() >= limit {
            buf.truncate(limit);
            break;
        }
    }
    serde_json::Deserializer::from_slice(&buf).into_iter::<Box<RawValue>>().next()?.ok()
}
