//! PostgreSQL access. The SQL is the Go backend's, statement for statement: aggregation happens in the database.

use chrono::{DateTime, Utc};
use serde_json::value::RawValue;
use sqlx::{PgPool, Row, postgres::PgRow, types::Json};
use uuid::Uuid;

use crate::{model::*, params::Filter};

/// A timeseries response is capped so a wide range with a fine interval cannot exhaust memory.
pub const MAX_POINTS: i64 = 20_000;

#[derive(Debug)]
pub enum StoreError {
    TooManyPoints,
    Db(sqlx::Error),
}

impl From<sqlx::Error> for StoreError {
    fn from(e: sqlx::Error) -> Self {
        StoreError::Db(e)
    }
}

#[derive(Clone)]
pub struct Store {
    pub(crate) pool: PgPool,
}

const WHERE_FILTER: &str = r#"
	($1::timestamptz IS NULL OR tested_at >= $1)
	AND ($2::timestamptz IS NULL OR tested_at < $2)
	AND ($3::text[] IS NULL OR provider = ANY($3))
	AND ($4::text[] IS NULL OR device = ANY($4))
	AND ($5::text[] IS NULL OR connection_type = ANY($5))
	AND ($6::text[] IS NULL OR "trigger" = ANY($6))"#;

const RESULT_COLS: &str = r#"id::text, provider, "trigger", device, connection_type, tested_at,
	download_mbps, upload_mbps, latency_ms, jitter_ms, packet_loss_pct,
	isp, server_name, server_location, server_country, NULLIF(result_url, '')"#;

/// The query arguments for `WHERE_FILTER`, in order. An empty list means "no restriction" (NULL).
macro_rules! bind_filter {
    ($q:expr, $f:expr) => {{
        let none_if_empty = |v: &Vec<String>| if v.is_empty() { None } else { Some(v.clone()) };
        $q.bind($f.from)
            .bind($f.to)
            .bind(none_if_empty(&$f.providers))
            .bind(none_if_empty(&$f.devices))
            .bind(none_if_empty(&$f.connection_types))
            .bind(none_if_empty(&$f.triggers))
    }};
}

fn stat_cols(col: &str) -> String {
    format!(
        "avg({col}), min({col}), max({col}), percentile_cont(0.5) WITHIN GROUP (ORDER BY {col}), percentile_cont(0.95) WITHIN GROUP (ORDER BY {col})"
    )
}

fn metrics_sql() -> String {
    ["download_mbps", "upload_mbps", "latency_ms", "packet_loss_pct"].map(stat_cols).join(",\n")
}

/// Reads one `Stat` (five columns) starting at `at`.
fn stat(row: &PgRow, at: usize) -> Result<Stat, sqlx::Error> {
    Ok(Stat {
        avg: row.try_get(at)?,
        min: row.try_get(at + 1)?,
        max: row.try_get(at + 2)?,
        p50: row.try_get(at + 3)?,
        p95: row.try_get(at + 4)?,
    })
}

fn metrics(row: &PgRow, at: usize) -> Result<Metrics, sqlx::Error> {
    Ok(Metrics {
        download_mbps: stat(row, at)?,
        upload_mbps: stat(row, at + 5)?,
        latency_ms: stat(row, at + 10)?,
        packet_loss_pct: stat(row, at + 15)?,
    })
}

fn result(row: &PgRow) -> Result<TestResult, sqlx::Error> {
    Ok(TestResult {
        id: row.try_get(0)?,
        provider: row.try_get(1)?,
        trigger: row.try_get(2)?,
        device: row.try_get(3)?,
        connection_type: row.try_get(4)?,
        tested_at: row.try_get(5)?,
        download_mbps: row.try_get(6)?,
        upload_mbps: row.try_get(7)?,
        latency_ms: row.try_get(8)?,
        jitter_ms: row.try_get(9)?,
        packet_loss_pct: row.try_get(10)?,
        isp: row.try_get(11)?,
        server: ServerInfo { name: row.try_get(12)?, location: row.try_get(13)?, country: row.try_get(14)? },
        result_url: row.try_get(15)?,
    })
}

fn interval_unit(interval: &str) -> Option<&'static str> {
    Some(match interval {
        "hour" => "hour",
        "day" => "day",
        "week" => "week",
        "month" => "month",
        "quarter" => "quarter",
        "year" => "year",
        _ => return None,
    })
}

fn group_col(field: &str) -> Option<&'static str> {
    Some(match field {
        "provider" => "provider",
        "device" => "device",
        "connectionType" => "connection_type",
        _ => return None,
    })
}

impl Store {
    pub fn new(pool: PgPool) -> Self {
        Self { pool }
    }

    pub async fn ping(&self) -> Result<(), sqlx::Error> {
        sqlx::query("SELECT 1").execute(&self.pool).await.map(|_| ())
    }

    /// Up to `limit` results, newest first, and the position to continue from (None at the end).
    pub async fn list_results(
        &self,
        f: &Filter,
        limit: i64,
        after: Option<(DateTime<Utc>, Uuid)>,
    ) -> Result<(Vec<TestResult>, Option<(DateTime<Utc>, String)>), sqlx::Error> {
        let sql = format!(
            "SELECT {RESULT_COLS} FROM speedtests WHERE{WHERE_FILTER}
		AND ($7::timestamptz IS NULL OR (tested_at, id) < ($7, $8::uuid))
		ORDER BY tested_at DESC, id DESC LIMIT $9"
        );
        let (t, id) = match after {
            Some((t, id)) => (Some(t), Some(id.to_string())),
            None => (None, None),
        };
        let rows = bind_filter!(sqlx::query(&sql), f).bind(t).bind(id).bind(limit + 1).fetch_all(&self.pool).await?;
        let mut items = rows.iter().map(result).collect::<Result<Vec<_>, _>>()?;
        if items.len() as i64 <= limit {
            return Ok((items, None));
        }
        items.truncate(limit as usize);
        let last = items.last().map(|r| (r.tested_at, r.id.clone()));
        Ok((items, last))
    }

    pub async fn get_result(&self, id: Uuid) -> Result<Option<ResultDetail>, sqlx::Error> {
        let sql = format!("SELECT {RESULT_COLS}, raw_data FROM speedtests WHERE id = $1::uuid");
        let Some(row) = sqlx::query(&sql).bind(id.to_string()).fetch_optional(&self.pool).await? else { return Ok(None) };
        let raw: Json<Box<RawValue>> = row.try_get(16)?;
        Ok(Some(ResultDetail { result: result(&row)?, raw: raw.0 }))
    }

    pub async fn summary(&self, f: &Filter) -> Result<Summary, sqlx::Error> {
        let sql = format!("SELECT count(*), min(tested_at), max(tested_at), {} FROM speedtests WHERE{WHERE_FILTER}", metrics_sql());
        let row = bind_filter!(sqlx::query(&sql), f).fetch_one(&self.pool).await?;
        Ok(Summary { count: row.try_get(0)?, first: row.try_get(1)?, last: row.try_get(2)?, metrics: metrics(&row, 3)? })
    }

    /// Buckets results in the given IANA time zone (weeks start on Monday). Interval "raw" returns every result as its own
    /// point. Series are ordered by key, points by time.
    pub async fn timeseries(&self, f: &Filter, interval: &str, group_by: &[String], tz: &str) -> Result<Timeseries, StoreError> {
        let (bucket, extra_group, bucketed) = match interval {
            "raw" => ("tested_at".to_string(), ", id", false),
            other => {
                let unit = interval_unit(other).ok_or_else(|| sqlx::Error::Protocol(format!("unknown interval {other:?}")))?;
                (format!("(date_trunc('{unit}', tested_at AT TIME ZONE $8) AT TIME ZONE $8)"), "", true)
            }
        };
        let select = |name: &str| match group_by.iter().find(|g| g.as_str() == name).and_then(|g| group_col(g)) {
            Some(col) => format!("{col}::text"),
            None => "NULL::text".to_string(),
        };
        if let Some(bad) = group_by.iter().find(|g| group_col(g).is_none()) {
            return Err(sqlx::Error::Protocol(format!("unknown groupBy {bad:?}")).into());
        }
        let sql = format!(
            "SELECT {}, {}, {}, {bucket} AS bucket, count(*), {}
		FROM speedtests WHERE{WHERE_FILTER}
		GROUP BY 1, 2, 3, 4{extra_group} ORDER BY 1, 2, 3, 4 LIMIT $7",
            select("provider"),
            select("device"),
            select("connectionType"),
            metrics_sql()
        );
        let q = bind_filter!(sqlx::query(&sql), f).bind(MAX_POINTS + 1);
        // $8 is only referenced (so only sent) when bucketing
        let rows = if bucketed { q.bind(tz.to_string()).fetch_all(&self.pool).await? } else { q.fetch_all(&self.pool).await? };

        let mut out = Timeseries { interval: interval.to_string(), timezone: tz.to_string(), series: Vec::new() };
        for (n, row) in rows.iter().enumerate() {
            if n as i64 >= MAX_POINTS {
                return Err(StoreError::TooManyPoints);
            }
            let key = SeriesKey { provider: row.try_get(0)?, device: row.try_get(1)?, connection_type: row.try_get(2)? };
            let point = TimeseriesPoint { bucket: row.try_get(3)?, count: row.try_get(4)?, metrics: metrics(row, 5)? };
            match out.series.last_mut() {
                Some(s) if s.key == key => s.points.push(point),
                _ => out.series.push(Series { key, points: vec![point] }),
            }
        }
        Ok(out)
    }

    /// Stores an Ookla CLI result (the database function reads the JSON) and returns the new id.
    pub async fn insert_ookla(&self, raw: &str, device: &str, connection_type: &str, trigger: &str) -> Result<String, sqlx::Error> {
        sqlx::query_scalar("SELECT speedtests_insert($1::jsonb, $2, $3, $4)::text")
            .bind(raw)
            .bind(device)
            .bind(connection_type)
            .bind(trigger)
            .fetch_one(&self.pool)
            .await
    }

    /// Stores an NDT7 summary and returns the new id. `client_ip` may be None.
    pub async fn insert_ndt7(
        &self,
        raw: &str,
        device: &str,
        connection_type: &str,
        trigger: &str,
        client_ip: Option<&str>,
    ) -> Result<String, sqlx::Error> {
        sqlx::query_scalar("SELECT speedtests_insert_ndt7($1::jsonb, $2, $3, $4, $5::inet)::text")
            .bind(raw)
            .bind(device)
            .bind(connection_type)
            .bind(trigger)
            .bind(client_ip)
            .fetch_one(&self.pool)
            .await
    }

    /// Counting per (device, connection type, provider) first and rolling that up lets Postgres answer from
    /// ix_speedtests_devices_summary (sql/postgres/008) without a sort of the whole table.
    pub async fn devices(&self) -> Result<Vec<Device>, sqlx::Error> {
        let rows = sqlx::query(
            "SELECT device,
		       array_agg(DISTINCT connection_type ORDER BY connection_type),
		       array_agg(DISTINCT provider ORDER BY provider),
		       sum(n)::bigint, max(last_at)
		FROM (SELECT device, connection_type, provider, count(*) AS n, max(tested_at) AS last_at
		      FROM speedtests GROUP BY device, connection_type, provider) g
		GROUP BY device ORDER BY max(last_at) DESC, device",
        )
        .fetch_all(&self.pool)
        .await?;
        rows.iter()
            .map(|r| {
                Ok(Device {
                    name: r.try_get(0)?,
                    connection_types: r.try_get(1)?,
                    providers: r.try_get(2)?,
                    test_count: r.try_get(3)?,
                    last_test_at: r.try_get(4)?,
                })
            })
            .collect()
    }
}
