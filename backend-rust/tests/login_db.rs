//! The signing key of the dashboard login lives in the database, so a restart (or the other backend) keeps everybody signed in.
//! Ports TestIntegrationSessionKeyIsKeptInTheDatabaseAndSurvivesARestart. Needs TEST_DATABASE_URL (scripts/scratch-db.sh up).

mod common;

use std::time::Duration;

use common::Harness;
use speedtest_backend::{
    login::{Config, Service},
    store::Store,
};

#[tokio::test]
async fn the_session_key_is_kept_in_the_database_and_survives_a_restart() {
    let Some(h) = Harness::new("UTC", None).await else { return };
    let store = Store::new(h.pool.clone());
    let cfg = || Config { password: "a long enough password".into(), lifetime: Duration::from_secs(3600) };
    let count = || async { sqlx::query_scalar::<_, i64>("SELECT count(*) FROM session_key").fetch_one(&h.pool).await.unwrap() };

    let first = Service::new(cfg(), &store).await.unwrap();
    assert_eq!(count().await, 1);
    // a restarted server (or the other backend) reads the same key, so nobody is signed out
    let second = Service::new(cfg(), &store).await.unwrap();
    assert!(second.valid(&first.issue().0), "a restart signed everybody out");
    assert_eq!(count().await, 1, "a second start left another key");

    // the backend's limited database login must be able to read and create the key
    let granted: Option<bool> = sqlx::query_scalar(
        "SELECT CASE WHEN EXISTS (SELECT FROM pg_roles WHERE rolname = 'speedtest')
         THEN has_table_privilege('speedtest', 'session_key', 'SELECT') AND has_table_privilege('speedtest', 'session_key', 'INSERT') END",
    )
    .fetch_one(&h.pool)
    .await
    .unwrap();
    assert_ne!(granted, Some(false), "the speedtest role cannot use session_key (migration 009 grants)");
}

#[tokio::test]
async fn a_database_without_the_table_says_which_migration_is_missing() {
    let Some(h) = Harness::new("UTC", None).await else { return };
    let store = Store::new(h.pool.clone());
    sqlx::query("ALTER TABLE session_key RENAME TO session_key_away").execute(&h.pool).await.unwrap();
    let err = Service::new(Config { password: "long enough".into(), lifetime: Duration::from_secs(60) }, &store).await.err();
    sqlx::query("ALTER TABLE session_key_away RENAME TO session_key").execute(&h.pool).await.unwrap();
    assert!(err.is_some_and(|e| e.contains("009_session_key.sql")));
}
