//! Reads new results, keeps the alert state up to date, and sends what needs sending. Alert state lives in the database, so a
//! restart never repeats a message and turning notifications on never alerts about old results. It is the only writer of alert
//! state. The behaviour is the Go backend's (backend-go/internal/notify); its test suite is ported in `tests/notify.rs`.

use std::{
    collections::{HashMap, HashSet},
    sync::{Arc, Mutex},
};

use chrono::{DateTime, Duration as Span, Utc};
use tokio_util::sync::CancellationToken;

use super::{
    config::Config,
    rules::{self, Entity, Text},
    sender::{Message, SendError, Sender},
};
use crate::{
    model::{ActiveAlert, NotificationEventView, NotificationRules, NotificationStatus},
    store::Store,
    store_notify::{AlertState, Cursor, NotificationEvent, ResultPoint},
};

const BATCH: i64 = 500;
const KEEP_EVENTS: i64 = 500;
const RECENT_EVENTS: i64 = 20;
const MAX_REMINDER_AGE: Span = Span::days(7); // a device that has been dead for a week stops being nagged about
const FAIL_LOG_EVERY: Span = Span::minutes(10); // while ntfy is unreachable, record the failure at most this often
const SETTLE_DELAY: Span = Span::seconds(5);
const ACTIVITY_LOOKBACK: Span = Span::days(14);
const MIN_SCHEDULED: usize = 4; // scheduled tests needed to know a device's rhythm

type Clock = Arc<dyn Fn() -> DateTime<Utc> + Send + Sync>;

pub struct Monitor {
    cfg: Config,
    store: Store,
    send: Arc<dyn Sender>,
    now: Clock,
    tick_lock: tokio::sync::Mutex<()>,
    last_fail: Mutex<HashMap<String, DateTime<Utc>>>,
}

fn text(st: &mut AlertState, t: Text) {
    st.title = Some(t.title);
    st.detail = Some(t.detail);
    st.severity = t.severity.to_string();
}

impl Monitor {
    pub fn new(cfg: Config, store: Store, send: Arc<dyn Sender>) -> Arc<Self> {
        Self::with_clock(cfg, store, send, Arc::new(Utc::now))
    }

    /// For tests: the time the monitor believes it is.
    pub fn with_clock(cfg: Config, store: Store, send: Arc<dyn Sender>, now: Clock) -> Arc<Self> {
        Arc::new(Self { cfg, store, send, now, tick_lock: tokio::sync::Mutex::new(()), last_fail: Mutex::new(HashMap::new()) })
    }

    /// Fails fast, with a clear message, when the database is not ready.
    pub async fn prepare(&self) -> Result<(), String> {
        self.store.notify_ready().await
    }

    /// Checks immediately and then every `check_every` until `root` ends.
    pub async fn run(self: Arc<Self>, root: CancellationToken) {
        let mut every = tokio::time::interval(self.cfg.check_every);
        every.set_missed_tick_behavior(tokio::time::MissedTickBehavior::Delay);
        loop {
            tokio::select! {
                _ = root.cancelled() => return,
                _ = every.tick() => {}
            }
            let me = Arc::clone(&self);
            // a panic in one check must not end the monitor
            match tokio::spawn(async move { me.tick().await }).await {
                Ok(Ok(())) => {}
                Ok(Err(e)) => {
                    if !root.is_cancelled() {
                        tracing::error!(err = %e, "notification check failed");
                    }
                }
                Err(e) => tracing::error!(err = %e, "notification check panicked"),
            }
        }
    }

    /// One check. Not safe to run twice at once, so a second call waits for the first.
    pub async fn tick(&self) -> Result<(), sqlx::Error> {
        let _one_at_a_time = self.tick_lock.lock().await;
        let now = (self.now)();
        let mut cursor = self.store.cursor_or_init(now).await?;
        loop {
            let batch = self.store.new_results(&cursor, now - SETTLE_DELAY, BATCH).await?;
            for r in &batch {
                self.judge(r).await?;
                cursor = Cursor { t: r.created, id: r.id.clone() };
            }
            if !batch.is_empty() {
                self.store.save_cursor(&cursor).await?;
            }
            if (batch.len() as i64) < BATCH {
                break;
            }
        }
        self.check_silence(now).await?;
        self.deliver(now).await
    }

    async fn state(&self, kind: &str, e: &Entity) -> Result<AlertState, sqlx::Error> {
        Ok(self.store.load_alert(kind, &e.subject()).await?.unwrap_or_else(|| AlertState {
            kind: kind.into(),
            subject: e.subject(),
            device: e.device.clone(),
            connection_type: Some(e.connection_type.clone()),
            provider: Some(e.provider.clone()),
            severity: "warning".into(),
            ..Default::default()
        }))
    }

    /// Counts one new result towards the slow-download and packet-loss alerts of its device.
    async fn judge(&self, r: &ResultPoint) -> Result<(), sqlx::Error> {
        let e = Entity { device: r.device.clone(), connection_type: r.connection_type.clone(), provider: r.provider.clone() };

        let (typical, n) =
            self.store.baseline(&r.device, &r.connection_type, &r.provider, r.tested_at, self.cfg.baseline_days as i32).await?;
        // no judgement without enough history to know what is normal
        if n >= self.cfg.min_samples && typical > 0.0 {
            let mut st = self.state("slow", &e).await?;
            let ratio = r.download_mbps / typical;
            if ratio < self.cfg.slow_ratio {
                st.streak += 1;
                if st.streak == 1 {
                    st.streak_since = Some(r.tested_at);
                }
                if st.streak as i64 >= self.cfg.slow_consecutive {
                    if !st.firing {
                        (st.firing, st.since, st.notified_at) = (true, st.streak_since, None);
                    }
                    let t = rules::slow_text(&e, r.download_mbps, typical, st.streak as i64);
                    text(&mut st, t);
                }
            } else if ratio >= self.cfg.recover_ratio {
                (st.streak, st.streak_since, st.firing) = (0, None, false);
            } else if !st.firing {
                // neither slow enough to count nor good enough to clear: a firing alert stays, a streak is broken
                (st.streak, st.streak_since) = (0, None);
            }
            self.store.save_alert(&st).await?;
        }

        // a result with no loss figure (NDT7 often) neither raises nor clears anything
        if let Some(loss) = r.packet_loss_pct {
            let mut st = self.state("loss", &e).await?;
            if loss >= self.cfg.loss_pct {
                st.streak += 1;
                if st.streak == 1 {
                    st.streak_since = Some(r.tested_at);
                }
                if st.streak as i64 >= self.cfg.loss_consecutive {
                    if !st.firing {
                        (st.firing, st.since, st.notified_at) = (true, st.streak_since, None);
                    }
                    let t = rules::loss_text(&e, loss, st.streak as i64);
                    text(&mut st, t);
                }
            } else {
                (st.streak, st.streak_since, st.firing) = (0, None, false);
            }
            self.store.save_alert(&st).await?;
        }
        Ok(())
    }

    /// A device that stops running its scheduled tests: nothing for three times its usual gap, and never sooner than
    /// `silence_min`. Devices only ever tested by hand have no scheduled tests and are never "silent", and one that has none for
    /// two weeks (or too few to know its rhythm) is treated as retired: its alert closes without a word.
    async fn check_silence(&self, now: DateTime<Utc>) -> Result<(), sqlx::Error> {
        let mut known: HashSet<String> = HashSet::new();
        for a in self.store.scheduled_activity(now - ACTIVITY_LOOKBACK).await? {
            if a.times.len() < MIN_SCHEDULED {
                continue;
            }
            known.insert(a.device.clone());
            let gaps: Vec<Span> = a.times.windows(2).map(|w| w[0] - w[1]).collect();
            let usual = Span::milliseconds(rules::median(&gaps.iter().map(|g| g.num_milliseconds() as f64).collect::<Vec<_>>()) as i64);
            let threshold = rules::silence_threshold(&gaps, Span::from_std(self.cfg.silence_min).unwrap_or(Span::hours(3)));
            let last = a.times[0];
            let silent = now - last > threshold;

            let existing = self.store.load_alert("silence", &a.device).await?;
            if !silent && !existing.as_ref().is_some_and(|s| s.firing) {
                continue;
            }
            let mut st = existing.unwrap_or_else(|| AlertState {
                kind: "silence".into(),
                subject: a.device.clone(),
                device: a.device.clone(),
                severity: "warning".into(),
                ..Default::default()
            });
            if silent {
                if !st.firing {
                    (st.firing, st.since, st.notified_at) = (true, Some(now), None);
                }
                text(&mut st, rules::silence_text(&a.device, last, now, usual));
            } else {
                st.firing = false;
            }
            self.store.save_alert(&st).await?;
        }
        for mut st in self.store.alerts_to_handle().await? {
            if st.kind == "silence" && !known.contains(&st.subject) {
                (st.firing, st.notified_at) = (false, None);
                self.store.save_alert(&st).await?;
            }
        }
        Ok(())
    }

    /// Sends what is due: new alerts, reminders for old ones, and a quiet message when one has cleared.
    async fn deliver(&self, now: DateTime<Utc>) -> Result<(), sqlx::Error> {
        let mut added = false;
        for mut st in self.store.alerts_to_handle().await? {
            let label = if st.kind == "silence" {
                st.device.clone()
            } else {
                Entity {
                    device: st.device.clone(),
                    connection_type: st.connection_type.clone().unwrap_or_default(),
                    provider: st.provider.clone().unwrap_or_default(),
                }
                .label()
            };
            let lasted = st.since.map(|s| rules::humanize(now - s)).unwrap_or_default();
            let (title, detail) = (st.title.clone().unwrap_or_default(), st.detail.clone().unwrap_or_default());

            let (event, msg) = if st.firing {
                match st.notified_at {
                    None => (
                        "alert",
                        Message { title, body: detail, priority: rules::priority(&st.severity), tags: rules::tags(&st.kind, &st.severity) },
                    ),
                    Some(at) => {
                        let due = !self.cfg.remind_every.is_zero()
                            && now - at >= Span::from_std(self.cfg.remind_every).unwrap_or(Span::hours(12))
                            && st.since.is_none_or(|s| now - s <= MAX_REMINDER_AGE);
                        if !due {
                            continue;
                        }
                        (
                            "reminder",
                            Message {
                                title: format!("Still: {title}"),
                                body: detail,
                                priority: rules::priority(&st.severity),
                                tags: rules::tags(&st.kind, &st.severity),
                            },
                        )
                    }
                }
            } else if st.notified_at.is_some() {
                let what = match st.kind.as_str() {
                    "slow" => "Download speeds are back to normal.",
                    "loss" => "Packet loss has cleared.",
                    _ => "Tests are arriving again.",
                };
                let body = if lasted.is_empty() {
                    what.to_string()
                } else if st.kind == "silence" {
                    format!("{what} The quiet spell lasted {lasted}.")
                } else {
                    format!("{what} The problem lasted {lasted}.")
                };
                (
                    "recovery",
                    Message {
                        title: format!("Recovered: {label}"),
                        body,
                        priority: rules::PRIORITY_RECOVERY,
                        tags: vec!["white_check_mark".into(), st.kind.clone()],
                    },
                )
            } else {
                continue;
            };

            let key = format!("{}|{}|{event}", st.kind, st.subject);
            let result = self.send.send(&msg).await;
            let error = match &result {
                Ok(()) => None,
                Err(SendError::NoRecipients) => Some("no recipients".to_string()),
                Err(SendError::Failed(e)) => Some(e.clone()),
            };
            match &error {
                None => {
                    lock(&self.last_fail).remove(&key);
                    st.notified_at = if event == "recovery" { None } else { Some(now) };
                    self.store.save_alert(&st).await?;
                }
                Some(_) => {
                    // the alert stays un-notified (or un-recovered), so it is retried at the next check, but the failure is
                    // written down only once in a while
                    let record = {
                        let mut fails = lock(&self.last_fail);
                        let due = fails.get(&key).is_none_or(|t| now - *t >= FAIL_LOG_EVERY);
                        if due {
                            fails.insert(key.clone(), now);
                        }
                        due
                    };
                    if !record {
                        continue;
                    }
                }
            }
            self.store
                .add_event(&NotificationEvent {
                    id: 0,
                    at: now,
                    event: event.into(),
                    kind: Some(st.kind.clone()),
                    subject: Some(st.subject.clone()),
                    title: msg.title.clone(),
                    message: msg.body.clone(),
                    delivered: error.is_none(),
                    error,
                })
                .await?;
            added = true;
        }
        if added {
            self.store.prune_events(KEEP_EVENTS).await?;
        }
        Ok(())
    }

    /// What the dashboard shows: the rules, what is active now, and what was sent recently. Never the topic or credentials.
    pub async fn status(&self) -> Result<NotificationStatus, sqlx::Error> {
        let c = &self.cfg;
        let active = self
            .store
            .firing_alerts()
            .await?
            .into_iter()
            .map(|a| ActiveAlert {
                kind: a.kind,
                subject: a.subject,
                title: a.title.unwrap_or_default(),
                detail: a.detail.unwrap_or_default(),
                severity: a.severity,
                since: a.since.unwrap_or_else(|| (self.now)()),
                notified_at: a.notified_at,
            })
            .collect();
        let recent = self
            .store
            .recent_events(RECENT_EVENTS)
            .await?
            .into_iter()
            .map(|e| NotificationEventView {
                id: e.id,
                at: e.at,
                event: e.event,
                kind: e.kind,
                title: e.title,
                message: e.message,
                delivered: e.delivered,
                error: e.error,
            })
            .collect();
        Ok(NotificationStatus {
            enabled: true,
            server: Some(c.host()),
            rules: Some(NotificationRules {
                slow_ratio: c.slow_ratio,
                slow_consecutive: c.slow_consecutive,
                recover_ratio: c.recover_ratio,
                loss_pct: c.loss_pct,
                loss_consecutive: c.loss_consecutive,
                silence_min_hours: c.silence_min.as_secs_f64() / 3600.0,
                remind_hours: c.remind_every.as_secs_f64() / 3600.0,
                baseline_days: c.baseline_days,
            }),
            active,
            recent,
        })
    }
}

fn lock<T>(m: &Mutex<T>) -> std::sync::MutexGuard<'_, T> {
    m.lock().unwrap_or_else(|e| e.into_inner())
}
