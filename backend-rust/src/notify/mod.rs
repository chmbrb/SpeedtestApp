//! Watches the speed-test results and tells a person about problems through ntfy and browser push. A port of
//! backend-go/internal/notify.

pub mod config;
pub mod monitor;
pub mod ntfy;
pub mod rules;
pub mod sender;
