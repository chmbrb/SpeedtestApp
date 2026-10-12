//! The `tests` table: on-demand test runs. Ports backend-go/internal/store/tests.go.

use chrono::{DateTime, Utc};
use sqlx::{Row, postgres::PgRow};
use std::time::Duration;

use crate::{params::is_uuid, store::Store};

/// One row of the tests table.
#[derive(Debug, Clone)]
pub struct TestRow {
    pub id: String,
    pub provider: String,
    pub mode: String,
    pub device: String,
    pub connection_type: String,
    pub state: String,
    pub created_at: DateTime<Utc>,
    pub started_at: Option<DateTime<Utc>>,
    pub finished_at: Option<DateTime<Utc>>,
    pub result_id: Option<String>,
    pub error: Option<String>,
}

impl TestRow {
    pub fn terminal(&self) -> bool {
        matches!(self.state.as_str(), "completed" | "failed" | "cancelled")
    }
}

/// A test to create. A browser test starts out running (the browser begins at once) and carries the SHA-256 of its
/// single-use token; a server test starts queued.
#[derive(Debug, Default)]
pub struct NewTest {
    pub provider: String,
    pub mode: String,
    pub device: String,
    pub connection_type: String,
    pub state: String,
    pub token_hash: Option<Vec<u8>>,
    pub client_ip: Option<String>,
}

#[derive(Debug)]
pub enum CreateError {
    /// A server-run test is already queued or running (the unique index allows only one).
    ServerBusy,
    Db(sqlx::Error),
}

#[derive(Debug)]
pub enum SubmitError {
    NotFound,
    /// The submit token does not match the test, or the test has no token (already used).
    TokenInvalid,
    /// The browser test was not finished in time.
    Expired,
    /// The test is no longer running, so it cannot take a result.
    Closed(String),
    Db(sqlx::Error),
}

impl From<sqlx::Error> for SubmitError {
    fn from(e: sqlx::Error) -> Self {
        SubmitError::Db(e)
    }
}

const TEST_COLS: &str =
    "id::text, provider, mode, device, connection_type, state, created_at, started_at, finished_at, result_id::text, error";

fn row(r: &PgRow) -> Result<TestRow, sqlx::Error> {
    Ok(TestRow {
        id: r.try_get(0)?,
        provider: r.try_get(1)?,
        mode: r.try_get(2)?,
        device: r.try_get(3)?,
        connection_type: r.try_get(4)?,
        state: r.try_get(5)?,
        created_at: r.try_get(6)?,
        started_at: r.try_get(7)?,
        finished_at: r.try_get(8)?,
        result_id: r.try_get(9)?,
        error: r.try_get(10)?,
    })
}

impl Store {
    pub async fn create_test(&self, n: NewTest) -> Result<TestRow, CreateError> {
        let sql = format!(
            "INSERT INTO tests (provider, mode, device, connection_type, state, started_at, submit_token_hash, client_ip)
		VALUES ($1, $2, $3, $4, $5, CASE WHEN $5 = 'running' THEN now() END, $6, $7::inet)
		RETURNING {TEST_COLS}"
        );
        let res = sqlx::query(&sql)
            .bind(&n.provider)
            .bind(&n.mode)
            .bind(&n.device)
            .bind(&n.connection_type)
            .bind(&n.state)
            .bind(&n.token_hash)
            .bind(&n.client_ip)
            .fetch_one(&self.pool)
            .await;
        match res {
            Ok(r) => row(&r).map_err(CreateError::Db),
            Err(sqlx::Error::Database(d))
                if d.code().as_deref() == Some("23505") && d.constraint() == Some("ux_tests_one_active_server") =>
            {
                Err(CreateError::ServerBusy)
            }
            Err(e) => Err(CreateError::Db(e)),
        }
    }

    /// None when there is no such test (or `id` is not even a uuid).
    pub async fn get_test(&self, id: &str) -> Result<Option<TestRow>, sqlx::Error> {
        if !is_uuid(id) {
            return Ok(None);
        }
        let sql = format!("SELECT {TEST_COLS} FROM tests WHERE id = $1::uuid");
        sqlx::query(&sql).bind(id).fetch_optional(&self.pool).await?.as_ref().map(row).transpose()
    }

    /// The newest tests, optionally limited to some states.
    pub async fn list_tests(&self, states: &[String], limit: i64) -> Result<Vec<TestRow>, sqlx::Error> {
        let sql = format!(
            "SELECT {TEST_COLS} FROM tests WHERE ($1::text[] IS NULL OR state = ANY($1))
		ORDER BY created_at DESC, id DESC LIMIT $2"
        );
        let states = if states.is_empty() { None } else { Some(states.to_vec()) };
        sqlx::query(&sql).bind(states).bind(limit).fetch_all(&self.pool).await?.iter().map(row).collect()
    }

    /// Moves a queued test to running.
    pub async fn mark_test_running(&self, id: &str) -> Result<(), sqlx::Error> {
        sqlx::query("UPDATE tests SET state = 'running', started_at = now() WHERE id = $1::uuid AND state = 'queued'")
            .bind(id)
            .execute(&self.pool)
            .await
            .map(|_| ())
    }

    /// Ends a queued or running test. `changed` is false when it had already ended (for example a cancel that raced with
    /// completion); the returned row is then the final state.
    pub async fn finish_test(
        &self,
        id: &str,
        state: &str,
        result_id: Option<&str>,
        error: Option<&str>,
    ) -> Result<Option<(TestRow, bool)>, sqlx::Error> {
        let sql = format!(
            "UPDATE tests SET state = $2, finished_at = now(), result_id = $3::uuid, error = $4, submit_token_hash = NULL
		WHERE id = $1::uuid AND state IN ('queued', 'running')
		RETURNING {TEST_COLS}"
        );
        match sqlx::query(&sql).bind(id).bind(state).bind(result_id).bind(error).fetch_optional(&self.pool).await? {
            Some(r) => Ok(Some((row(&r)?, true))),
            None => Ok(self.get_test(id).await?.map(|t| (t, false))),
        }
    }

    /// Fails server tests left queued or running by a process that died, so they cannot block new ones.
    pub async fn interrupt_server_tests(&self) -> Result<u64, sqlx::Error> {
        sqlx::query(
            "UPDATE tests SET state = 'failed', finished_at = now(), error = 'The server restarted while this test was running.'
		WHERE mode = 'server' AND state IN ('queued', 'running')",
        )
        .execute(&self.pool)
        .await
        .map(|r| r.rows_affected())
    }

    /// Fails tests that have been open too long: browser tests the browser never finished, and server tests that somehow
    /// outlived their own timeout.
    pub async fn sweep_tests(&self, browser_ttl: Duration, server_max: Duration) -> Result<u64, sqlx::Error> {
        sqlx::query(
            "UPDATE tests SET state = 'failed', finished_at = now(), submit_token_hash = NULL,
		       error = CASE mode WHEN 'browser' THEN 'The test was not finished in time.' ELSE 'The test took too long and was stopped.' END
		WHERE state IN ('queued', 'running')
		  AND ((mode = 'browser' AND created_at < now() - make_interval(secs => $1))
		    OR (mode = 'server'  AND created_at < now() - make_interval(secs => $2)))",
        )
        .bind(browser_ttl.as_secs_f64())
        .bind(server_max.as_secs_f64())
        .execute(&self.pool)
        .await
        .map(|r| r.rows_affected())
    }

    /// Stores an NDT7 summary against a browser test, in one transaction: the single-use token is checked and spent, the
    /// result is inserted, and the test is linked to it. If the insert is rejected nothing changes, so the browser can try
    /// again with corrected data. Returns the new result's id.
    pub async fn submit_browser_result(
        &self,
        id: &str,
        token_hash: &[u8],
        ttl: Duration,
        summary: &str,
        client_ip: Option<&str>,
    ) -> Result<String, SubmitError> {
        if !is_uuid(id) {
            return Err(SubmitError::NotFound);
        }
        let mut tx = self.pool.begin().await?;
        let claimed = sqlx::query(
            "UPDATE tests SET state = 'completed', finished_at = now(), submit_token_hash = NULL
		WHERE id = $1::uuid AND mode = 'browser' AND state = 'running' AND submit_token_hash = $2
		  AND created_at > now() - make_interval(secs => $3)
		RETURNING device, connection_type",
        )
        .bind(id)
        .bind(token_hash)
        .bind(ttl.as_secs_f64())
        .fetch_optional(&mut *tx)
        .await?;
        let Some(claimed) = claimed else {
            drop(tx); // nothing changed
            return Err(self.why_not_submittable(id, ttl).await);
        };
        let (device, connection_type): (String, String) = (claimed.try_get(0)?, claimed.try_get(1)?);
        let result_id: String = sqlx::query_scalar("SELECT speedtests_insert_ndt7($1::jsonb, $2, $3, 'on_demand', $4::inet)::text")
            .bind(summary)
            .bind(&device)
            .bind(&connection_type)
            .bind(client_ip)
            .fetch_one(&mut *tx)
            .await?;
        sqlx::query("UPDATE tests SET result_id = $2::uuid, client_ip = $3::inet WHERE id = $1::uuid")
            .bind(id)
            .bind(&result_id)
            .bind(client_ip)
            .execute(&mut *tx)
            .await?;
        tx.commit().await?;
        Ok(result_id)
    }

    /// Explains a refused submission: unknown test, test already over, expired, or a wrong/spent token.
    async fn why_not_submittable(&self, id: &str, ttl: Duration) -> SubmitError {
        let found = sqlx::query(
            "SELECT state, created_at <= now() - make_interval(secs => $2) FROM tests WHERE id = $1::uuid AND mode = 'browser'",
        )
        .bind(id)
        .bind(ttl.as_secs_f64())
        .fetch_optional(&self.pool)
        .await;
        match found {
            Err(e) => SubmitError::Db(e),
            Ok(None) => SubmitError::NotFound,
            Ok(Some(r)) => {
                let (state, expired): (String, bool) = match (r.try_get(0), r.try_get(1)) {
                    (Ok(s), Ok(e)) => (s, e),
                    (Err(e), _) | (_, Err(e)) => return SubmitError::Db(e),
                };
                if state != "running" {
                    SubmitError::Closed(state)
                } else if expired {
                    SubmitError::Expired
                } else {
                    SubmitError::TokenInvalid
                }
            }
        }
    }
}
