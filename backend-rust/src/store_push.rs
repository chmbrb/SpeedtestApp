//! Push subscriptions and the server's VAPID key pair; see sql/postgres/007_web_push.sql. Ports backend-go/internal/store/push.go.

use sqlx::Row;

use crate::store::Store;

/// One browser or installed app that asked for notifications.
#[derive(Debug, Clone)]
pub struct PushSubscription {
    pub id: String,
    pub endpoint: String,
    pub p256dh: String,
    pub auth: String,
    pub user_agent: String,
    pub failures: i32,
}

#[derive(Debug)]
pub enum SaveError {
    /// A new endpoint when the limit is already reached.
    TooMany,
    Db(sqlx::Error),
}

impl From<sqlx::Error> for SaveError {
    fn from(e: sqlx::Error) -> Self {
        SaveError::Db(e)
    }
}

impl Store {
    /// The stored key pair (public, private), or None when none has been created yet.
    pub async fn vapid(&self) -> Result<Option<(String, String)>, sqlx::Error> {
        sqlx::query("SELECT public_key, private_key FROM push_vapid")
            .fetch_optional(&self.pool)
            .await?
            .map(|r| Ok((r.try_get(0)?, r.try_get(1)?)))
            .transpose()
    }

    /// Stores the key pair unless one exists already (two servers starting together must end up with the same one).
    pub async fn save_vapid(&self, public: &str, private: &str) -> Result<(), sqlx::Error> {
        sqlx::query("INSERT INTO push_vapid (public_key, private_key) VALUES ($1, $2) ON CONFLICT DO NOTHING")
            .bind(public)
            .bind(private)
            .execute(&self.pool)
            .await
            .map(|_| ())
    }

    /// Adds the subscription, or refreshes the keys of an endpoint that is already stored. A new endpoint is refused when `max`
    /// subscriptions exist.
    pub async fn save_subscription(&self, sub: &PushSubscription, max: i64) -> Result<(), SaveError> {
        let ua = (!sub.user_agent.is_empty()).then_some(sub.user_agent.as_str());
        let mut tx = self.pool.begin().await?;
        // serialise writers so the count check below cannot be raced past
        sqlx::query("SELECT pg_advisory_xact_lock(7447002)").execute(&mut *tx).await?;
        let updated =
            sqlx::query("UPDATE push_subscriptions SET p256dh = $2, auth = $3, user_agent = $4, failures = 0 WHERE endpoint = $1")
                .bind(&sub.endpoint)
                .bind(&sub.p256dh)
                .bind(&sub.auth)
                .bind(ua)
                .execute(&mut *tx)
                .await?
                .rows_affected();
        if updated == 0 {
            let n: i64 = sqlx::query_scalar("SELECT count(*) FROM push_subscriptions").fetch_one(&mut *tx).await?;
            if n >= max {
                return Err(SaveError::TooMany);
            }
            sqlx::query("INSERT INTO push_subscriptions (endpoint, p256dh, auth, user_agent) VALUES ($1, $2, $3, $4)")
                .bind(&sub.endpoint)
                .bind(&sub.p256dh)
                .bind(&sub.auth)
                .bind(ua)
                .execute(&mut *tx)
                .await?;
        }
        tx.commit().await?;
        Ok(())
    }

    /// Removes an endpoint and reports whether it was there.
    pub async fn delete_subscription(&self, endpoint: &str) -> Result<bool, sqlx::Error> {
        Ok(sqlx::query("DELETE FROM push_subscriptions WHERE endpoint = $1").bind(endpoint).execute(&self.pool).await?.rows_affected() > 0)
    }

    pub async fn subscription(&self, endpoint: &str) -> Result<Option<PushSubscription>, sqlx::Error> {
        sqlx::query("SELECT id::text, endpoint, p256dh, auth, user_agent, failures FROM push_subscriptions WHERE endpoint = $1")
            .bind(endpoint)
            .fetch_optional(&self.pool)
            .await?
            .map(|r| row(&r))
            .transpose()
    }

    pub async fn list_subscriptions(&self) -> Result<Vec<PushSubscription>, sqlx::Error> {
        sqlx::query("SELECT id::text, endpoint, p256dh, auth, user_agent, failures FROM push_subscriptions ORDER BY created_at")
            .fetch_all(&self.pool)
            .await?
            .iter()
            .map(row)
            .collect()
    }

    /// Records the outcome of one delivery. A success clears the failure count. A failure adds one, and the subscription is
    /// removed once it has failed `max_failures` times in a row (reports whether it was removed).
    pub async fn mark_push_delivery(&self, id: &str, ok: bool, max_failures: i32) -> Result<bool, sqlx::Error> {
        if ok {
            sqlx::query("UPDATE push_subscriptions SET failures = 0, last_success = now() WHERE id = $1::uuid")
                .bind(id)
                .execute(&self.pool)
                .await?;
            return Ok(false);
        }
        let removed = sqlx::query("DELETE FROM push_subscriptions WHERE id = $1::uuid AND failures + 1 >= $2")
            .bind(id)
            .bind(max_failures)
            .execute(&self.pool)
            .await?
            .rows_affected()
            > 0;
        if !removed {
            sqlx::query("UPDATE push_subscriptions SET failures = failures + 1 WHERE id = $1::uuid").bind(id).execute(&self.pool).await?;
        }
        Ok(removed)
    }

    /// Removes a subscription the push service reported as gone.
    pub async fn delete_subscription_by_id(&self, id: &str) -> Result<(), sqlx::Error> {
        sqlx::query("DELETE FROM push_subscriptions WHERE id = $1::uuid").bind(id).execute(&self.pool).await.map(|_| ())
    }
}

fn row(r: &sqlx::postgres::PgRow) -> Result<PushSubscription, sqlx::Error> {
    let ua: Option<String> = r.try_get(4)?;
    Ok(PushSubscription {
        id: r.try_get(0)?,
        endpoint: r.try_get(1)?,
        p256dh: r.try_get(2)?,
        auth: r.try_get(3)?,
        user_agent: ua.unwrap_or_default(),
        failures: r.try_get(5)?,
    })
}
