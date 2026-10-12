use speedtest_backend::{
    app,
    config::Config,
    notify::{
        config::Config as NtfyConfig,
        monitor::Monitor,
        ntfy::{Ntfy, test_message},
        sender::{Fanout, Sender},
    },
    ondemand::{config as od_config, manager::Manager},
    push,
    store::Store,
};
use sqlx::postgres::PgPoolOptions;
use std::{net::SocketAddr, sync::Arc, time::Duration};
use tokio_util::sync::CancellationToken;

#[tokio::main]
async fn main() {
    tracing_subscriber::fmt()
        .with_env_filter(tracing_subscriber::EnvFilter::try_from_default_env().unwrap_or_else(|_| "info".into()))
        .init();
    if let Err(e) = run().await {
        tracing::error!(err = %e, "fatal");
        std::process::exit(1);
    }
}

async fn run() -> Result<(), String> {
    if let Some(cmd) = std::env::args().nth(1) {
        return match cmd.as_str() {
            "notify-test" => notify_test().await,
            other => Err(format!("unknown command {other:?} (the only command is notify-test; run with no arguments to start the server)")),
        };
    }
    let cfg = Config::load(|k| std::env::var(k).ok())?;
    let pool = PgPoolOptions::new()
        .max_connections(std::env::var("DB_MAX_CONNS").ok().and_then(|v| v.parse().ok()).unwrap_or(10))
        .acquire_timeout(Duration::from_secs(5))
        .connect(&cfg.database_url)
        .await
        .map_err(|e| format!("could not connect to the database: {e}"))?;

    let static_dir = cfg.static_dir.map(std::path::PathBuf::from);
    if let Some(d) = &static_dir {
        std::fs::metadata(d).map_err(|e| format!("STATIC_DIR {}: {e}", d.display()))?;
        tracing::info!(dir = %d.display(), "serving frontend");
    }
    if cfg.tokens.is_empty() {
        tracing::warn!("API_TOKENS is empty: ingest endpoints will reject every request");
    }
    let od_cfg = od_config::Config::from_env(|k| std::env::var(k).ok()).map_err(|e| format!("invalid on-demand test settings: {e}"))?;
    let store = Store::new(pool);
    let manager = Manager::new(od_cfg, store.clone(), None);
    match manager.capabilities().ookla.reason {
        None => tracing::info!(device = %manager.config().device_name, "on-demand tests ready (ookla: on)"),
        Some(why) => tracing::info!(%why, "on-demand tests: Ookla is off; NDT7 runs in the visitor's browser"),
    }
    let root = CancellationToken::new();
    manager.start(root.clone()).await;
    tokio::spawn(Arc::clone(&manager).run(root.clone()));

    let mut state = app::AppState::new(store.clone(), &cfg.timezone, static_dir)
        .with_auth(cfg.tokens.clone(), cfg.ingest_per_minute)
        .with_ondemand(Arc::clone(&manager));

    // Browser notifications (Web Push) ride on the same alerts as ntfy.
    let ntfy_cfg = NtfyConfig::from_env(|k| std::env::var(k).ok()).map_err(|e| format!("invalid ntfy settings: {e}"))?;
    let push_cfg = push::config_from_env(|k| std::env::var(k).ok()).map_err(|e| format!("invalid push settings: {e}"))?;
    let pusher = match push_cfg {
        Some(pcfg) => {
            let svc = push::Service::new(pcfg, store.clone()).await?;
            state = state.with_pusher(Arc::clone(&svc));
            tracing::info!("browser notifications (web push) on");
            Some(svc)
        }
        None => {
            tracing::info!("browser notifications (web push) off (PUSH_ENABLED=no)");
            None
        }
    };
    if ntfy_cfg.enabled {
        let ntfy: Arc<dyn Sender> = Arc::new(Ntfy::new(ntfy_cfg.clone()));
        let sender: Arc<dyn Sender> = match pusher {
            Some(p) => Arc::new(Fanout(vec![ntfy, p])),
            None => ntfy,
        };
        let monitor = Monitor::new(ntfy_cfg.clone(), store.clone(), sender);
        monitor.prepare().await?;
        state = state.with_notifier(Arc::clone(&monitor));
        tokio::spawn(monitor.run(root.clone()));
        tracing::info!(ntfy = %ntfy_cfg.host(), check_every = ?ntfy_cfg.check_every, "notifications on");
    } else {
        tracing::info!("alerts are off (set NTFY_TOPIC to turn them on); browser notifications can only send tests");
    }

    let listener = tokio::net::TcpListener::bind(cfg.listen).await.map_err(|e| format!("could not listen on {}: {e}", cfg.listen))?;
    tracing::info!(addr = %cfg.listen, timezone = %cfg.timezone, "listening");
    let signalled = root.clone();
    tokio::spawn(async move {
        shutdown().await;
        tracing::info!("shutting down");
        signalled.cancel(); // running tests stop and record that the server was shutting down
    });
    let stop = root.clone();
    let serve = axum::serve(listener, app::router(state).into_make_service_with_connect_info::<SocketAddr>())
        .with_graceful_shutdown(async move { stop.cancelled().await });
    // Open event streams would hold a graceful shutdown for ever, so it gets ten seconds.
    let result = tokio::select! {
        r = serve => r.map_err(|e| e.to_string()),
        _ = async { root.cancelled().await; tokio::time::sleep(Duration::from_secs(10)).await } => {
            tracing::warn!("some connections were still open after 10 seconds");
            Ok(())
        }
    };
    manager.wait_idle(Duration::from_secs(5)).await;
    tracing::info!("shut down");
    result
}

/// Sends one message with the configured ntfy settings, so a setup can be checked without waiting for a real problem:
/// `speedtest-backend notify-test`. It needs no database.
async fn notify_test() -> Result<(), String> {
    let cfg = NtfyConfig::from_env(|k| std::env::var(k).ok()).map_err(|e| format!("invalid ntfy settings: {e}"))?;
    if !cfg.enabled {
        return Err("NTFY_TOPIC is not set".into());
    }
    tokio::time::timeout(Duration::from_secs(30), Ntfy::new(cfg.clone()).send(&test_message()))
        .await
        .map_err(|_| "the test notification timed out".to_string())?
        .map_err(|e| format!("the test notification could not be delivered: {e}"))?;
    println!("Sent a test notification through {}. Check your phone.", cfg.host());
    Ok(())
}

/// Waits for Ctrl-C or SIGTERM (what `docker stop` sends).
async fn shutdown() {
    let ctrl_c = async {
        let _ = tokio::signal::ctrl_c().await;
    };
    #[cfg(unix)]
    let term = async {
        if let Ok(mut s) = tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate()) {
            s.recv().await;
        }
    };
    #[cfg(not(unix))]
    let term = std::future::pending::<()>();
    tokio::select! { _ = ctrl_c => {}, _ = term => {} }
}
