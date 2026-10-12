//! The notification monitor, with a fake clock and a fake sender against a real database. This is a port of the Go backend's
//! test suite (backend-go/internal/notify/integration_test.go): the same scenarios, so both implementations are held to the same
//! behaviour.

#![allow(clippy::type_complexity, clippy::too_many_arguments)]

mod common;

use std::sync::{Arc, Mutex};

use async_trait::async_trait;
use chrono::{DateTime, Duration, TimeZone, Utc};
use common::Harness;
use speedtest_backend::{
    notify::{
        config::Config,
        monitor::Monitor,
        sender::{Message, SendError, Sender},
    },
    store::Store,
    store_notify::AlertState,
};

#[derive(Default)]
struct FakeSender {
    sent: Mutex<Vec<Message>>,
    attempts: Mutex<usize>,
    fail: Mutex<Option<Box<dyn Fn(usize) -> Option<String> + Send + Sync>>>,
}

#[async_trait]
impl Sender for FakeSender {
    async fn send(&self, m: &Message) -> Result<(), SendError> {
        let n = {
            let mut a = self.attempts.lock().unwrap();
            *a += 1;
            *a
        };
        if let Some(f) = self.fail.lock().unwrap().as_ref() {
            if let Some(e) = f(n) {
                return Err(SendError::Failed(e));
            }
        }
        self.sent.lock().unwrap().push(m.clone());
        Ok(())
    }
}

fn t0() -> DateTime<Utc> {
    Utc.with_ymd_and_hms(2026, 10, 11, 12, 0, 0).unwrap()
}

fn h(n: i64) -> Duration {
    Duration::hours(n)
}

struct Rig {
    _h: Harness,
    db: Store,
    pool: sqlx::PgPool,
    clock: Arc<Mutex<DateTime<Utc>>>,
    send: Arc<FakeSender>,
    m: Arc<Monitor>,
    cfg: Config,
    seq: i64,
}

fn base_config() -> Config {
    Config { enabled: true, topic: "secret-topic".into(), server_url: "https://ntfy.example.org".into(), ..Config::default() }
}

async fn insert(pool: &sqlx::PgPool, device: &str, ct: &str, provider: &str, trigger: &str, at: DateTime<Utc>, dl: f64, loss: Option<f64>) {
    sqlx::query(
        r#"INSERT INTO speedtests (provider, "trigger", device, connection_type, tested_at, created, download_mbps, upload_mbps, latency_ms, packet_loss_pct, raw_data)
           VALUES ($1,$2,$3,$4,$5,$5,$6,10,10,$7,'{}')"#,
    )
    .bind(provider)
    .bind(trigger)
    .bind(device)
    .bind(ct)
    .bind(at)
    .bind(dl)
    .bind(loss)
    .execute(pool)
    .await
    .unwrap();
}

fn monitor(cfg: &Config, db: &Store, send: &Arc<FakeSender>, clock: &Arc<Mutex<DateTime<Utc>>>) -> Arc<Monitor> {
    let c = clock.clone();
    Monitor::with_clock(cfg.clone(), db.clone(), send.clone(), Arc::new(move || *c.lock().unwrap()))
}

/// Twelve results of history (a typical download of 900 Mbps) for server/wired/ookla, then the first check, so the cursor sits at
/// "now". Results added afterwards are the ones that can alert.
async fn rig(mutate: impl FnOnce(&mut Config)) -> Option<Rig> {
    let harness = Harness::new("UTC", None).await?;
    let mut cfg = base_config();
    mutate(&mut cfg);
    let (pool, db) = (harness.pool.clone(), Store::new(harness.pool.clone()));
    let (clock, send) = (Arc::new(Mutex::new(t0())), Arc::new(FakeSender::default()));
    let m = monitor(&cfg, &db, &send, &clock);
    for i in 0..12 {
        let dl = if i % 2 == 0 { 920.0 } else { 880.0 };
        insert(&pool, "server", "wired", "ookla", "on_demand", t0() - h(24 - i), dl, None).await;
    }
    let r = Rig { _h: harness, db, pool, clock, send, m, cfg, seq: 0 };
    r.tick().await;
    Some(r)
}

macro_rules! rig {
    () => {
        rig!(|_c: &mut Config| {})
    };
    ($mutate:expr) => {
        match rig($mutate).await {
            Some(r) => r,
            None => return,
        }
    };
}

impl Rig {
    async fn tick(&self) {
        self.m.tick().await.unwrap();
    }
    fn step(&self, d: Duration) {
        let mut c = self.clock.lock().unwrap();
        *c += d;
    }
    fn now(&self) -> DateTime<Utc> {
        *self.clock.lock().unwrap()
    }
    /// A result as if it had just arrived (on demand, so the "gone quiet" rule stays out of the way).
    async fn add_for(&mut self, device: &str, ct: &str, provider: &str, dl: f64, loss: Option<f64>) {
        self.seq += 1;
        let at = self.now() - Duration::minutes(1) + Duration::seconds(self.seq);
        insert(&self.pool, device, ct, provider, "on_demand", at, dl, loss).await;
    }
    async fn add(&mut self, dl: f64) {
        self.add_for("server", "wired", "ookla", dl, None).await;
    }
    fn sent(&self) -> Vec<Message> {
        self.send.sent.lock().unwrap().clone()
    }
    fn titles(&self) -> Vec<String> {
        self.sent().into_iter().map(|m| m.title).collect()
    }
    fn expect_sent(&self, n: usize) {
        assert_eq!(self.sent().len(), n, "sent: {:?}", self.titles());
    }
    async fn alert(&self, kind: &str, subject: &str) -> Option<AlertState> {
        self.db.load_alert(kind, subject).await.unwrap()
    }
}

const SERVER_SLOW: &str = "server|wired|ookla";

#[tokio::test]
async fn first_run_ignores_history() {
    let Some(harness) = Harness::new("UTC", None).await else { return };
    // a long slow history already in the database when notifications are first switched on
    for i in 0..20 {
        insert(&harness.pool, "server", "wired", "ookla", "on_demand", t0() - h(30 - i), 100.0 + i as f64, None).await;
    }
    let db = Store::new(harness.pool.clone());
    let (clock, send) = (Arc::new(Mutex::new(t0())), Arc::new(FakeSender::default()));
    monitor(&base_config(), &db, &send, &clock).tick().await.unwrap();
    assert!(send.sent.lock().unwrap().is_empty(), "history must never trigger alerts");
    assert!(db.firing_alerts().await.unwrap().is_empty());
}

#[tokio::test]
async fn needs_two_slow_tests_in_a_row_then_alerts_once() {
    let mut r = rig!();
    r.step(h(1));
    r.add(390.0).await; // 43% of typical: slow, but a single slow test is only noise
    r.tick().await;
    r.expect_sent(0);
    let a = r.alert("slow", SERVER_SLOW).await.expect("a state");
    assert!(!a.firing && a.streak == 1, "one slow test = streak 1, not firing: {a:?}");

    r.step(h(1));
    r.add(410.0).await;
    r.tick().await;
    r.expect_sent(1);
    let m = r.sent().remove(0);
    assert_eq!((m.title.as_str(), m.priority, m.tags.len(), m.tags[1].as_str()), ("Slow download: server (wired)", 4, 2, "slow"));
    for want in ["410 Mbps", "(2 tests in a row)", "typical 880"] {
        assert!(m.body.contains(want), "{want} not in {}", m.body);
    }
    let a = r.alert("slow", SERVER_SLOW).await.unwrap();
    assert!(a.firing && a.notified_at.is_some() && a.severity == "critical" && a.since.is_some(), "{a:?}");
    // the alert dates from the first slow test
    assert_eq!(a.since.unwrap(), t0() + h(1) - Duration::minutes(1) + Duration::seconds(1));

    // still slow, and ticking again and again: no duplicates
    r.step(h(1));
    r.add(400.0).await;
    r.tick().await;
    r.tick().await;
    r.expect_sent(1);
    let a = r.alert("slow", SERVER_SLOW).await.unwrap();
    assert!(a.streak == 3 && a.detail.unwrap().contains("(3 tests in a row)"), "the description stays current");

    let st = r.m.status().await.unwrap();
    assert!(
        st.active.len() == 1
            && st.active[0].kind == "slow"
            && st.active[0].notified_at.is_some()
            && st.recent.len() == 1
            && st.recent[0].delivered,
        "{st:?}"
    );
}

#[tokio::test]
async fn recovery_is_sent_once() {
    let mut r = rig!();
    r.step(h(1));
    r.add(400.0).await;
    r.add(405.0).await;
    r.tick().await;
    r.expect_sent(1);

    r.step(h(3));
    r.add(910.0).await;
    r.tick().await;
    r.expect_sent(2);
    let m = r.sent().remove(1);
    assert_eq!((m.title.as_str(), m.priority), ("Recovered: server (wired)", 2));
    assert!(m.body.contains("back to normal") && m.body.contains("The problem lasted 3 hours."), "{}", m.body);
    r.step(h(1));
    r.add(920.0).await;
    r.tick().await;
    r.tick().await;
    r.expect_sent(2);
    let a = r.alert("slow", SERVER_SLOW).await.unwrap();
    assert!(!a.firing && a.notified_at.is_none() && a.streak == 0, "closed out: {a:?}");
    assert!(r.m.status().await.unwrap().active.is_empty());
}

#[tokio::test]
async fn hysteresis() {
    // firing, then a middling result (80% of typical): neither slow enough to count nor good enough to clear
    let mut r = rig!();
    r.step(h(1));
    r.add(400.0).await;
    r.add(405.0).await;
    r.tick().await;
    r.expect_sent(1);
    r.step(h(1));
    r.add(720.0).await;
    r.tick().await;
    assert!(r.alert("slow", SERVER_SLOW).await.unwrap().firing, "an 80% result must not clear the alert");
    r.expect_sent(1);
    r.step(h(1));
    r.add(860.0).await; // 95%: recovered
    r.tick().await;
    r.expect_sent(2);
}

#[tokio::test]
async fn a_middling_result_breaks_a_streak_that_has_not_become_an_alert() {
    let mut r = rig!();
    for dl in [400.0, 720.0, 400.0] {
        r.step(h(1));
        r.add(dl).await;
        r.tick().await;
    }
    r.expect_sent(0);
    let a = r.alert("slow", SERVER_SLOW).await.unwrap();
    assert!(!a.firing && a.streak == 1, "the middling result broke the streak: {a:?}");
}

#[tokio::test]
async fn waits_for_enough_history() {
    let mut r = rig!(|c: &mut Config| c.min_samples = 20); // the rig only has 12 tests of history
    r.step(h(1));
    for _ in 0..3 {
        r.add(100.0).await;
    }
    r.tick().await;
    r.expect_sent(0);
    assert!(r.alert("slow", SERVER_SLOW).await.is_none(), "no judgement without history");
}

#[tokio::test]
async fn a_flap_that_ends_before_any_message_is_silent() {
    let mut r = rig!();
    r.step(h(1));
    // the monitor was down; by the time it looks, a slow stretch has come and gone
    r.add(400.0).await;
    r.add(410.0).await;
    r.add(900.0).await;
    r.tick().await;
    r.expect_sent(0);
    let a = r.alert("slow", SERVER_SLOW).await.unwrap();
    assert!(!a.firing && a.notified_at.is_none(), "{a:?}");
}

#[tokio::test]
async fn reminders_and_their_limit() {
    let mut r = rig!();
    r.step(h(1));
    r.add(400.0).await;
    r.add(405.0).await;
    r.tick().await;
    r.expect_sent(1);

    r.step(h(11));
    r.tick().await;
    r.expect_sent(1); // 11h later: not yet
    r.step(h(1));
    r.tick().await;
    r.expect_sent(2);
    let m = r.sent().remove(1);
    assert_eq!((m.title.as_str(), m.priority), ("Still: Slow download: server (wired)", 4));
    r.step(h(6));
    r.tick().await;
    r.expect_sent(2); // the clock restarts after each reminder

    // a problem older than a week stops being repeated
    r.step(Duration::days(8));
    r.tick().await;
    r.step(h(13));
    r.tick().await;
    r.expect_sent(2);

    // and reminders can be switched off
    drop(r);
    let mut r = rig!(|c: &mut Config| c.remind_every = std::time::Duration::ZERO);
    r.step(h(1));
    r.add(400.0).await;
    r.add(405.0).await;
    r.tick().await;
    r.step(h(72));
    r.tick().await;
    r.expect_sent(1);
}

#[tokio::test]
async fn a_failed_delivery_is_retried_and_recorded_once() {
    let mut r = rig!();
    *r.send.fail.lock().unwrap() = Some(Box::new(|n| (n <= 2).then(|| "ntfy answered 503: down".to_string())));
    r.step(h(1));
    r.add(400.0).await;
    r.add(405.0).await;
    r.tick().await; // attempt 1 fails
    r.expect_sent(0);
    let a = r.alert("slow", SERVER_SLOW).await.unwrap();
    assert!(a.firing && a.notified_at.is_none(), "a failed delivery must leave the alert un-notified so it is retried: {a:?}");
    r.step(Duration::minutes(1));
    r.tick().await; // attempt 2 fails again, a minute later
    r.expect_sent(0);
    r.step(Duration::minutes(1));
    r.tick().await; // attempt 3 succeeds
    r.expect_sent(1);
    assert!(r.alert("slow", SERVER_SLOW).await.unwrap().notified_at.is_some(), "notified after the retry succeeded");
    let ev = r.db.recent_events(10).await.unwrap();
    assert_eq!(ev.len(), 2, "one failure recorded (not one per retry) and one success: {ev:?}");
    assert!(
        ev[0].delivered && !ev[1].delivered && ev[1].error.as_deref().is_some_and(|e| e.contains("503")),
        "events (newest first): {ev:?}"
    );
}

#[tokio::test]
async fn packet_loss() {
    let mut r = rig!();
    r.step(h(1));
    r.add_for("server", "wired", "ookla", 900.0, Some(6.0)).await;
    r.tick().await;
    r.expect_sent(0);
    r.add_for("server", "wired", "ookla", 900.0, Some(7.5)).await;
    r.tick().await;
    r.expect_sent(1);
    let m = r.sent().remove(0);
    assert!(m.title == "Packet loss: server (wired)" && m.body.contains("7.5%") && m.priority == 3, "{m:?}");
    r.step(h(1));
    r.add_for("server", "wired", "ookla", 900.0, Some(0.0)).await;
    r.tick().await;
    r.expect_sent(2);
    let m = r.sent().remove(1);
    assert!(m.title.starts_with("Recovered: server") && m.body.contains("Packet loss has cleared"), "{m:?}");
    // a result that carries no loss figure (NDT7 often) neither raises nor clears anything
    r.step(h(1));
    r.add_for("server", "wired", "ookla", 900.0, None).await;
    r.tick().await;
    r.expect_sent(2);
}

#[tokio::test]
async fn entities_are_independent() {
    let mut r = rig!();
    for i in 0..12 {
        // a laptop with its own, much lower, normal
        insert(&r.pool, "laptop", "wireless", "ookla", "on_demand", t0() - h(40 - i), 350.0, None).await;
    }
    r.tick().await;
    r.step(h(1));
    r.add_for("server", "wired", "ookla", 400.0, None).await;
    r.add_for("laptop", "wireless", "ookla", 340.0, None).await; // normal for a laptop, would be "slow" against the server's 900
    r.add_for("server", "wired", "ookla", 405.0, None).await;
    r.add_for("laptop", "wireless", "ookla", 345.0, None).await;
    r.tick().await;
    r.expect_sent(1);
    assert_eq!(r.titles()[0], "Slow download: server (wired)");
    let a = r.alert("slow", "laptop|wireless|ookla").await;
    assert!(a.is_some_and(|a| !a.firing), "laptop is fine");
}

/// A scheduled-test history: the server every 2 hours (the last 30 minutes before t0), a laptop only ever tested by hand, and a
/// device with too little history to know its rhythm.
async fn silence_rig() -> Option<Rig> {
    let harness = Harness::new("UTC", None).await?;
    let (pool, db) = (harness.pool.clone(), Store::new(harness.pool.clone()));
    for i in 0..12 {
        insert(&pool, "server", "wired", "ookla", "scheduled", t0() - Duration::minutes(30) - h(2 * i), 900.0, None).await;
        insert(&pool, "laptop", "wired", "ookla", "on_demand", t0() - h(100) - h(i), 900.0, None).await;
    }
    for i in 0..3 {
        insert(&pool, "newbie", "wired", "ookla", "scheduled", t0() - h(100) - h(2 * i), 900.0, None).await;
    }
    let cfg = base_config();
    let (clock, send) = (Arc::new(Mutex::new(t0())), Arc::new(FakeSender::default()));
    let m = monitor(&cfg, &db, &send, &clock);
    Some(Rig { _h: harness, db, pool, clock, send, m, cfg, seq: 0 })
}

#[tokio::test]
async fn silence() {
    let Some(r) = silence_rig().await else { return };
    r.tick().await;
    r.expect_sent(0); // 30 minutes after a 2-hourly test is fine

    r.step(h(5)); // 5.5 hours since the last test; the threshold is 6 hours
    r.tick().await;
    r.expect_sent(0);
    r.step(h(2)); // 7.5 hours
    r.tick().await;
    r.tick().await;
    let sent = r.sent();
    assert!(
        sent.len() == 1
            && sent[0].title == "No tests from server"
            && sent[0].body.contains("one is usually due every 2 hours")
            && sent[0].priority == 3,
        "silence alert (once, and only for server): {sent:?}"
    );

    // a scheduled test arrives again
    insert(&r.pool, "server", "wired", "ookla", "scheduled", r.now() - Duration::minutes(1), 900.0, None).await;
    r.step(Duration::minutes(1));
    r.tick().await;
    let sent = r.sent();
    assert!(
        sent.len() == 2 && sent[1].title == "Recovered: server" && sent[1].body.contains("Tests are arriving again"),
        "recovery: {sent:?}"
    );
    assert!(r.db.firing_alerts().await.unwrap().is_empty(), "nothing should be firing");
}

#[tokio::test]
async fn a_retired_device_stops_quietly() {
    let Some(harness) = Harness::new("UTC", None).await else { return };
    for i in 0..8 {
        insert(&harness.pool, "old-box", "wired", "ookla", "scheduled", t0() - h(2 * i), 900.0, None).await;
    }
    let db = Store::new(harness.pool.clone());
    let (clock, send) = (Arc::new(Mutex::new(t0())), Arc::new(FakeSender::default()));
    let m = monitor(&base_config(), &db, &send, &clock);
    m.tick().await.unwrap();
    *clock.lock().unwrap() = t0() + h(10);
    m.tick().await.unwrap();
    assert_eq!(send.sent.lock().unwrap().len(), 1, "silence alert expected");
    *clock.lock().unwrap() = t0() + Duration::days(20); // the device has been gone for three weeks: retired, not "silent"
    m.tick().await.unwrap();
    m.tick().await.unwrap();
    assert_eq!(send.sent.lock().unwrap().len(), 1, "no 'recovered' message for a device that never came back");
    let a = db.load_alert("silence", "old-box").await.unwrap().unwrap();
    assert!(!a.firing && a.notified_at.is_none(), "closed out quietly: {a:?}");
}

#[tokio::test]
async fn survives_a_restart() {
    let mut r = rig!();
    r.step(h(1));
    r.add(400.0).await;
    r.add(405.0).await;
    r.tick().await;
    r.expect_sent(1);
    // a brand-new Monitor (the process restarted) picks up from the database
    let m2 = monitor(&r.cfg, &r.db, &r.send, &r.clock);
    r.step(Duration::minutes(1));
    m2.tick().await.unwrap();
    r.expect_sent(1); // not re-sent, and the old results are not re-counted
    assert_eq!(r.alert("slow", SERVER_SLOW).await.unwrap().streak, 2, "streak re-counted after restart");
}

#[tokio::test]
async fn status_never_contains_the_topic_or_credentials() {
    let mut r = rig!(|c: &mut Config| c.token = "tk_supersecret".into());
    r.step(h(1));
    r.add(400.0).await;
    r.add(405.0).await;
    r.tick().await;
    let st = r.m.status().await.unwrap();
    let json = serde_json::to_string(&st).unwrap();
    for secret in ["secret-topic", "tk_supersecret"] {
        assert!(!json.contains(secret), "status leaks {secret:?}: {json}");
    }
    assert!(
        st.enabled && st.server.as_deref() == Some("ntfy.example.org") && st.rules.as_ref().is_some_and(|r| r.slow_consecutive == 2),
        "{json}"
    );
    let rules = st.rules.unwrap();
    assert_eq!((rules.silence_min_hours, rules.remind_hours, rules.baseline_days, rules.loss_pct), (3.0, 12.0, 30, 5.0));
}

#[tokio::test]
async fn events_are_pruned() {
    let Some(harness) = Harness::new("UTC", None).await else { return };
    sqlx::query(
        "INSERT INTO notification_events (event, title, message, delivered) SELECT 'alert', 't', 'm', true FROM generate_series(1, 520)",
    )
    .execute(&harness.pool)
    .await
    .unwrap();
    let db = Store::new(harness.pool.clone());
    db.prune_events(500).await.unwrap();
    let (n, min, max): (i64, i64, i64) =
        sqlx::query_as("SELECT count(*), min(id), max(id) FROM notification_events").fetch_one(&harness.pool).await.unwrap();
    assert!(n == 500 && max - min == 499, "kept {n} (ids {min}..{max}), want the newest 500");
    db.prune_events(1000).await.unwrap(); // fewer than the limit: nothing happens
    assert!(db.prune_events(0).await.is_err());
    // the status shows the newest 20, newest first
    let (clock, send) = (Arc::new(Mutex::new(t0())), Arc::new(FakeSender::default()));
    let st = monitor(&base_config(), &db, &send, &clock).status().await.unwrap();
    assert_eq!(st.recent.len(), 20);
    assert!(st.recent[0].id > st.recent[19].id);
}

#[tokio::test]
async fn prepare_requires_migration_006() {
    let Some(harness) = Harness::new("UTC", None).await else { return };
    let (clock, send) = (Arc::new(Mutex::new(t0())), Arc::new(FakeSender::default()));
    assert!(monitor(&base_config(), &Store::new(harness.pool.clone()), &send, &clock).prepare().await.is_ok());
}
