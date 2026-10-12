//! SpeedtestApp backend in Rust: the second implementation of docs/openapi.yaml, next to backend-go.

pub mod app;
pub mod auth;
pub mod body;
pub mod config;
pub mod gojson;
pub mod ingest;
pub mod model;
pub mod notify;
pub mod ondemand;
pub mod ondemand_api;
pub mod params;
pub mod problem;
pub mod push;
pub mod push_api;
pub mod static_files;
pub mod store;
pub mod store_notify;
pub mod store_push;
pub mod store_tests;
