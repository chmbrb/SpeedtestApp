//! Tests run when a person asks for one: Ookla on the server with live progress, and NDT7 in the visitor's browser with a
//! single-use token to submit the result. A port of backend-go/internal/ondemand.

pub mod config;
pub mod hub;
pub mod manager;
pub mod ookla;
