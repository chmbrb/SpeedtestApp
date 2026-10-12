//! What the notification monitor remembers: where it has read up to, the state of each alert, and what it has sent; see
//! sql/postgres/006_notifications.sql. Ports backend-go/internal/store/notify.go.

use chrono::{DateTime, Utc};
use sqlx::{Row, postgres::PgRow};

use crate::store::Store;

/// Where the monitor has read up to: the `created` time and id of the last result it has seen.
#[derive(Debug, Clone, PartialEq)]
pub struct Cursor {
    pub t: DateTime<Utc>,
    pub id: String,
}

/// The slice of a result the monitor needs.
#[derive(Debug, Clone)]
pub struct ResultPoint {
    pub id: String,
    pub created: DateTime<Utc>,
    pub tested_at: DateTime<Utc>,
    pub device: String,
    pub connection_type: String,
    pub provider: String,
    pub download_mbps: f64,
    pub packet_loss_pct: Option<f64>,
}

/// The persisted state of one alert condition.
#[derive(Debug, Clone, Default)]
pub struct AlertState {
    pub kind: String,
    pub subject: String,
    pub device: String,
    pub connection_type: Option<String>,
    pub provider: Option<String>,
    pub firing: bool,
    pub streak: i32,
    pub streak_since: Option<DateTime<Utc>>,
    pub since: Option<DateTime<Utc>>,
    pub notified_at: Option<DateTime<Utc>>,
    pub severity: String,
    pub title: Option<String>,
    pub detail: Option<String>,
}

/// A device's most recent scheduled tests, newest first.
#[derive(Debug, Clone)]
pub struct DeviceActivity {
    pub device: String,
    pub times: Vec<DateTime<Utc>>,
}

#[derive(Debug, Clone)]
pub struct NotificationEvent {
    pub id: i64,
    pub at: DateTime<Utc>,
    pub event: String,
    pub kind: Option<String>,
    pub subject: Option<String>,
    pub title: String,
    pub message: String,
    pub delivered: bool,
    pub error: Option<String>,
}

const ALERT_COLS: &str =
    "kind, subject, device, connection_type, provider, firing, streak, streak_since, since, notified_at, severity, title, detail";

fn alert(r: &PgRow) -> Result<AlertState, sqlx::Error> {
    Ok(AlertState {
        kind: r.try_get(0)?,
        subject: r.try_get(1)?,
        device: r.try_get(2)?,
        connection_type: r.try_get(3)?,
        provider: r.try_get(4)?,
        firing: r.try_get(5)?,
        streak: r.try_get(6)?,
        streak_since: r.try_get(7)?,
        since: r.try_get(8)?,
        notified_at: r.try_get(9)?,
        severity: r.try_get(10)?,
        title: r.try_get(11)?,
        detail: r.try_get(12)?,
    })
}

impl Store {
    /// Whether migration 006 has been applied.
    pub async fn notify_ready(&self) -> Result<(), String> {
        let ok: bool = sqlx::query_scalar(
            "SELECT to_regclass('notify_cursor') IS NOT NULL AND to_regclass('alert_state') IS NOT NULL AND to_regclass('notification_events') IS NOT NULL",
        )
        .fetch_one(&self.pool)
        .await
        .map_err(|e| e.to_string())?;
        if ok { Ok(()) } else { Err("notification tables are missing: apply sql/postgres/006_notifications.sql".into()) }
    }

    /// Where the monitor has read up to, creating the cursor at `now` the first time so that turning notifications on never
    /// alerts about old results.
    pub async fn cursor_or_init(&self, now: DateTime<Utc>) -> Result<Cursor, sqlx::Error> {
        sqlx::query("INSERT INTO notify_cursor (id, last_created, last_id) VALUES (true, $1, NULL) ON CONFLICT (id) DO NOTHING")
            .bind(now)
            .execute(&self.pool)
            .await?;
        let r = sqlx::query("SELECT last_created, last_id::text FROM notify_cursor WHERE id").fetch_one(&self.pool).await?;
        let id: Option<String> = r.try_get(1)?;
        Ok(Cursor { t: r.try_get(0)?, id: id.unwrap_or_default() })
    }

    pub async fn save_cursor(&self, c: &Cursor) -> Result<(), sqlx::Error> {
        let id = (!c.id.is_empty()).then_some(c.id.as_str());
        sqlx::query("UPDATE notify_cursor SET last_created = $1, last_id = $2::uuid WHERE id")
            .bind(c.t)
            .bind(id)
            .execute(&self.pool)
            .await
            .map(|_| ())
    }

    /// Results stored after the cursor, oldest first. `settled_before` leaves out the last few seconds so a transaction that
    /// committed late is not skipped by a cursor that already moved past it.
    pub async fn new_results(&self, after: &Cursor, settled_before: DateTime<Utc>, limit: i64) -> Result<Vec<ResultPoint>, sqlx::Error> {
        let id = (!after.id.is_empty()).then_some(after.id.as_str());
        sqlx::query(
            "SELECT id::text, created, tested_at, device, connection_type, provider, download_mbps, packet_loss_pct
		FROM speedtests
		WHERE (created, id) > ($1, COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid))
		  AND created <= $3
		ORDER BY created, id LIMIT $4",
        )
        .bind(after.t)
        .bind(id)
        .bind(settled_before)
        .bind(limit)
        .fetch_all(&self.pool)
        .await?
        .iter()
        .map(|r| {
            Ok(ResultPoint {
                id: r.try_get(0)?,
                created: r.try_get(1)?,
                tested_at: r.try_get(2)?,
                device: r.try_get(3)?,
                connection_type: r.try_get(4)?,
                provider: r.try_get(5)?,
                download_mbps: r.try_get(6)?,
                packet_loss_pct: r.try_get(7)?,
            })
        })
        .collect()
    }

    /// The median download of an entity over the `days` before `before`, and how many tests that is.
    pub async fn baseline(
        &self,
        device: &str,
        connection_type: &str,
        provider: &str,
        before: DateTime<Utc>,
        days: i32,
    ) -> Result<(f64, i64), sqlx::Error> {
        let r = sqlx::query(
            "SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY download_mbps), count(*)
		FROM speedtests
		WHERE device = $1 AND connection_type = $2 AND provider = $3
		  AND tested_at < $4 AND tested_at >= $4 - make_interval(days => $5)",
        )
        .bind(device)
        .bind(connection_type)
        .bind(provider)
        .bind(before)
        .bind(days)
        .fetch_one(&self.pool)
        .await?;
        let median: Option<f64> = r.try_get(0)?;
        Ok((median.unwrap_or(0.0), r.try_get(1)?))
    }

    /// Each device's latest scheduled tests (up to 21, newest first) since `since`.
    pub async fn scheduled_activity(&self, since: DateTime<Utc>) -> Result<Vec<DeviceActivity>, sqlx::Error> {
        sqlx::query(
            "SELECT device, array_agg(tested_at ORDER BY tested_at DESC)
		FROM (SELECT device, tested_at, row_number() OVER (PARTITION BY device ORDER BY tested_at DESC) AS rn
		      FROM speedtests WHERE \"trigger\" = 'scheduled' AND tested_at >= $1) t
		WHERE rn <= 21 GROUP BY device ORDER BY device",
        )
        .bind(since)
        .fetch_all(&self.pool)
        .await?
        .iter()
        .map(|r| Ok(DeviceActivity { device: r.try_get(0)?, times: r.try_get(1)? }))
        .collect()
    }

    /// The state for (kind, subject), or None when it has never been evaluated.
    pub async fn load_alert(&self, kind: &str, subject: &str) -> Result<Option<AlertState>, sqlx::Error> {
        let sql = format!("SELECT {ALERT_COLS} FROM alert_state WHERE kind = $1 AND subject = $2");
        sqlx::query(&sql).bind(kind).bind(subject).fetch_optional(&self.pool).await?.as_ref().map(alert).transpose()
    }

    pub async fn save_alert(&self, a: &AlertState) -> Result<(), sqlx::Error> {
        sqlx::query(
            "INSERT INTO alert_state (kind, subject, device, connection_type, provider, firing, streak, streak_since, since,
		                         notified_at, severity, title, detail, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13, now())
		ON CONFLICT (kind, subject) DO UPDATE SET
		  firing = EXCLUDED.firing, streak = EXCLUDED.streak, streak_since = EXCLUDED.streak_since, since = EXCLUDED.since,
		  notified_at = EXCLUDED.notified_at, severity = EXCLUDED.severity, title = EXCLUDED.title,
		  detail = EXCLUDED.detail, updated_at = now()",
        )
        .bind(&a.kind)
        .bind(&a.subject)
        .bind(&a.device)
        .bind(&a.connection_type)
        .bind(&a.provider)
        .bind(a.firing)
        .bind(a.streak)
        .bind(a.streak_since)
        .bind(a.since)
        .bind(a.notified_at)
        .bind(&a.severity)
        .bind(&a.title)
        .bind(&a.detail)
        .execute(&self.pool)
        .await
        .map(|_| ())
    }

    /// Every alert that is firing or that the person was told about and is not yet closed out.
    pub async fn alerts_to_handle(&self) -> Result<Vec<AlertState>, sqlx::Error> {
        let sql = format!("SELECT {ALERT_COLS} FROM alert_state WHERE firing OR notified_at IS NOT NULL ORDER BY kind, subject");
        sqlx::query(&sql).fetch_all(&self.pool).await?.iter().map(alert).collect()
    }

    /// The alerts currently active, oldest first.
    pub async fn firing_alerts(&self) -> Result<Vec<AlertState>, sqlx::Error> {
        let sql = format!("SELECT {ALERT_COLS} FROM alert_state WHERE firing ORDER BY since, kind, subject");
        sqlx::query(&sql).fetch_all(&self.pool).await?.iter().map(alert).collect()
    }

    pub async fn add_event(&self, e: &NotificationEvent) -> Result<(), sqlx::Error> {
        sqlx::query(
            "INSERT INTO notification_events (event, kind, subject, title, message, delivered, error) VALUES ($1,$2,$3,$4,$5,$6,$7)",
        )
        .bind(&e.event)
        .bind(&e.kind)
        .bind(&e.subject)
        .bind(&e.title)
        .bind(&e.message)
        .bind(e.delivered)
        .bind(&e.error)
        .execute(&self.pool)
        .await
        .map(|_| ())
    }

    /// The newest `n` events, newest first.
    pub async fn recent_events(&self, n: i64) -> Result<Vec<NotificationEvent>, sqlx::Error> {
        sqlx::query("SELECT id, created_at, event, kind, subject, title, message, delivered, error FROM notification_events ORDER BY id DESC LIMIT $1")
            .bind(n)
            .fetch_all(&self.pool)
            .await?
            .iter()
            .map(|r| {
                Ok(NotificationEvent {
                    id: r.try_get(0)?,
                    at: r.try_get(1)?,
                    event: r.try_get(2)?,
                    kind: r.try_get(3)?,
                    subject: r.try_get(4)?,
                    title: r.try_get(5)?,
                    message: r.try_get(6)?,
                    delivered: r.try_get(7)?,
                    error: r.try_get(8)?,
                })
            })
            .collect()
    }

    /// Keeps only the newest `keep` events.
    pub async fn prune_events(&self, keep: i64) -> Result<(), sqlx::Error> {
        if keep < 1 {
            return Err(sqlx::Error::Protocol("keep must be positive".into()));
        }
        sqlx::query("DELETE FROM notification_events WHERE id <= (SELECT id FROM notification_events ORDER BY id DESC OFFSET $1 LIMIT 1)")
            .bind(keep)
            .execute(&self.pool)
            .await
            .map(|_| ())
    }
}
