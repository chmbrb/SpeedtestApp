mod common;

use axum::http::StatusCode;
use common::{Harness, call, offline_router};
use percent_encoding::{NON_ALPHANUMERIC, utf8_percent_encode};
use serde_json::{Value, json};

/// Known data, chosen so that the answers can be worked out by hand.
///   server (wired, ookla, scheduled): ten results, one per day at 12:00 in Phoenix (19:00Z) from 2026-06-01;
///       download 100..1000, upload 10..100, latency 10..19; packet loss null for the first two, 2.5 for the fourth, 0 otherwise.
///   laptop (wireless, ookla, scheduled): four results at 05:30Z, which is the evening before in Phoenix (UTC-7) - a day boundary.
///   phone (wireless, ndt7, on_demand): three results.
async fn seed(h: &Harness) {
    for sql in [
        r#"INSERT INTO speedtests (provider, "trigger", device, connection_type, tested_at, download_mbps, upload_mbps, latency_ms, jitter_ms, packet_loss_pct, isp, server_name, server_location, server_country, result_url, raw_data)
           SELECT 'ookla', 'scheduled', 'server', 'wired', timestamptz '2026-06-01 19:00:00+00' + (i * interval '1 day'),
                  100 * (i + 1), 10 * (i + 1), 10 + i, 1,
                  CASE WHEN i < 2 THEN NULL WHEN i = 3 THEN 2.5 ELSE 0 END,
                  'Cox', 'CenturyLink', 'Phoenix, AZ', 'United States', 'https://www.speedtest.net/result/c/' || i, jsonb_build_object('n', i)
           FROM generate_series(0, 9) i"#,
        r#"INSERT INTO speedtests (provider, "trigger", device, connection_type, tested_at, download_mbps, upload_mbps, latency_ms, raw_data)
           SELECT 'ookla', 'scheduled', 'laptop', 'wireless', timestamptz '2026-06-02 05:30:00+00' + (i * interval '1 day'), 300 + i, 30 + i, 20, '{}'::jsonb
           FROM generate_series(0, 3) i"#,
        r#"INSERT INTO speedtests (provider, "trigger", device, connection_type, tested_at, download_mbps, upload_mbps, latency_ms, raw_data)
           SELECT 'ndt7', 'on_demand', 'phone', 'wireless', timestamptz '2026-06-03 12:00:00.123456+00' + (i * interval '1 hour'), 50 + i, 5 + i, 30, '{"hello": "world", "n": 1.0}'::jsonb
           FROM generate_series(0, 2) i"#,
    ] {
        sqlx::query(sql).execute(&h.pool).await.unwrap();
    }
}

fn f(v: &Value) -> f64 {
    v.as_f64().unwrap_or_else(|| panic!("not a number: {v}"))
}

fn assert_problem(r: &common::Resp, status: u16) -> Value {
    assert_eq!(r.status.as_u16(), status, "{}", r.text());
    assert_eq!(r.header("content-type"), "application/problem+json");
    let p = r.json();
    assert_eq!(p["status"], status);
    assert_eq!(p["type"], "about:blank");
    p
}

macro_rules! harness {
    ($tz:expr) => {
        match Harness::new($tz, None).await {
            Some(h) => h,
            None => return,
        }
    };
}

// ---- routes that need no database -------------------------------------------------------------------------------------

#[tokio::test]
async fn unknown_paths_and_wrong_methods_answer_in_problem_json() {
    let app = offline_router(None);
    assert_problem(&call(&app, "GET", "/api/v1/nope").await, 404);
    assert_problem(&call(&app, "GET", "/nothing").await, 404); // no frontend configured
    let r = call(&app, "POST", "/api/v1/devices").await;
    assert_problem(&r, 405);
    assert!(r.header("allow").contains("GET"), "Allow: {}", r.header("allow"));
    assert_problem(&call(&app, "DELETE", "/api/v1/stats/summary").await, 405);
}

#[tokio::test]
async fn features_that_arrive_later_answer_as_their_off_states() {
    let app = offline_router(None); // no on-demand manager configured
    let n = call(&app, "GET", "/api/v1/notifications").await.json();
    assert_eq!(n, json!({"enabled": false, "server": null, "rules": null, "active": [], "recent": []}));
    let c = call(&app, "GET", "/api/v1/capabilities").await.json();
    assert_eq!(c["ookla"]["available"], false);
    assert!(c["ndt7"]["reason"].is_string());
    assert_eq!(call(&app, "GET", "/api/v1/push/config").await.json(), json!({"enabled": false}));
    assert_problem(&call(&app, "POST", "/api/v1/push/subscriptions").await, 503);
    assert_problem(&call(&app, "POST", "/api/v1/results").await, 401); // sending results needs a token
}

#[tokio::test]
async fn serves_the_built_frontend_with_the_same_cache_rules_as_go() {
    let dir = std::env::temp_dir().join(format!("speedtest-static-{}", std::process::id()));
    std::fs::create_dir_all(dir.join("assets")).unwrap();
    std::fs::write(dir.join("index.html"), "<!doctype html><title>app</title>").unwrap();
    std::fs::write(dir.join("sw.js"), "// worker").unwrap();
    std::fs::write(dir.join("manifest.webmanifest"), "{}").unwrap();
    std::fs::write(dir.join("assets/app-abc123.js"), "console.log(1)").unwrap();
    let app = offline_router(Some(dir.clone()));

    let index = call(&app, "GET", "/").await;
    assert_eq!((index.status.as_u16(), index.header("cache-control")), (200, "no-cache"));
    assert!(index.header("content-type").starts_with("text/html"));
    // a client-side route falls back to the shell, so a reload keeps working
    assert_eq!(call(&app, "GET", "/settings/anything").await.text(), "<!doctype html><title>app</title>");
    let asset = call(&app, "GET", "/assets/app-abc123.js").await;
    assert_eq!(asset.header("cache-control"), "public, max-age=31536000, immutable");
    assert!(asset.header("content-type").starts_with("text/javascript"));
    assert_eq!(call(&app, "GET", "/sw.js").await.header("cache-control"), "no-cache");
    assert_eq!(call(&app, "GET", "/manifest.webmanifest").await.header("content-type"), "application/manifest+json");
    // a missing file is a real 404, not the HTML shell (an old tab must not run HTML as JavaScript)
    for missing in ["/assets/old-deadbeef.js", "/favicon.ico", "/missing.png"] {
        assert_eq!(call(&app, "GET", missing).await.status, StatusCode::NOT_FOUND, "{missing}");
    }
    // the API keeps its JSON errors, and the frontend cannot be reached with a path that climbs out of the folder
    assert_problem(&call(&app, "GET", "/api/v1/missing").await, 404);
    let esc = call(&app, "GET", "/..%2F..%2Fetc%2Fpasswd").await;
    assert!(!esc.text().contains("root:"));
    let head = call(&app, "HEAD", "/").await;
    assert_eq!((head.status.as_u16(), head.body.len()), (200, 0));
    assert_eq!(head.header("content-length"), "33");
    std::fs::remove_dir_all(dir).ok();
}

// ---- reads against the database ---------------------------------------------------------------------------------------

#[tokio::test]
async fn health_and_devices() {
    let h = harness!("UTC");
    assert_eq!(h.get("/api/v1/health").await.json(), json!({"status": "ok"}));
    assert_eq!(h.get("/api/v1/devices").await.json(), json!({"items": []}));
    seed(&h).await;
    let d = h.get("/api/v1/devices").await.json();
    let items = d["items"].as_array().unwrap();
    // most recently active first
    let names: Vec<_> = items.iter().map(|i| i["name"].as_str().unwrap()).collect();
    assert_eq!(names, ["server", "laptop", "phone"]);
    assert_eq!(
        items[0],
        json!({"name": "server", "connectionTypes": ["wired"], "providers": ["ookla"], "testCount": 10, "lastTestAt": "2026-06-10T19:00:00Z"})
    );
    assert_eq!(items[2]["lastTestAt"], "2026-06-03T14:00:00.123456Z"); // Go's time format: only the digits that are needed
    assert_eq!(items[2]["providers"], json!(["ndt7"]));
}

#[tokio::test]
async fn results_paginate_newest_first_without_gaps_or_repeats() {
    let h = harness!("UTC");
    seed(&h).await;
    let mut ids = Vec::new();
    let mut times: Vec<String> = Vec::new();
    let mut cursor = String::new();
    for page in 0.. {
        let uri = format!("/api/v1/results?limit=5{cursor}");
        let body = h.get(&uri).await.json();
        let items = body["items"].as_array().unwrap();
        assert!(items.len() <= 5);
        for i in items {
            ids.push(i["id"].as_str().unwrap().to_string());
            times.push(i["testedAt"].as_str().unwrap().to_string());
        }
        match body["nextCursor"].as_str() {
            Some(c) => cursor = format!("&cursor={c}"),
            None => {
                assert_eq!(page, 3, "17 results in pages of 5");
                break;
            }
        }
    }
    assert_eq!(ids.len(), 17);
    let mut unique = ids.clone();
    unique.sort();
    unique.dedup();
    assert_eq!(unique.len(), 17, "a result appeared twice");
    let parsed: Vec<_> = times.iter().map(|t| chrono::DateTime::parse_from_rfc3339(t).unwrap()).collect();
    assert!(parsed.windows(2).all(|w| w[0] >= w[1]), "not newest first: {times:?}");
    // a result carries everything the dashboard shows
    let phone = h.get("/api/v1/results?limit=1&device=phone").await.json()["items"][0].clone();
    assert_eq!(phone["server"], json!({"name": null, "location": null, "country": null}));
    assert_eq!(
        (
            phone["latencyMs"].clone(),
            phone["jitterMs"].clone(),
            phone["packetLossPct"].clone(),
            phone["isp"].clone(),
            phone["resultUrl"].clone()
        ),
        (json!(30.0), Value::Null, Value::Null, Value::Null, Value::Null)
    );
    let server = h.get("/api/v1/results?limit=1").await.json()["items"][0].clone();
    assert_eq!(server["device"], "server");
    assert_eq!(server["server"], json!({"name": "CenturyLink", "location": "Phoenix, AZ", "country": "United States"}));
    assert_eq!(
        (server["isp"].clone(), server["resultUrl"].clone(), server["connectionType"].clone(), server["trigger"].clone()),
        (json!("Cox"), json!("https://www.speedtest.net/result/c/9"), json!("wired"), json!("scheduled"))
    );
}

#[tokio::test]
async fn ties_in_time_are_paged_by_id_so_nothing_is_skipped() {
    let h = harness!("UTC");
    sqlx::query(r#"INSERT INTO speedtests (provider, "trigger", device, connection_type, tested_at, download_mbps, upload_mbps, raw_data)
                   SELECT 'ookla', 'scheduled', 'twins', 'wired', timestamptz '2026-06-01 00:00:00+00', 1, 1, '{}' FROM generate_series(1, 7)"#)
        .execute(&h.pool).await.unwrap();
    let mut seen = std::collections::BTreeSet::new();
    let mut cursor = String::new();
    loop {
        let body = h.get(&format!("/api/v1/results?limit=2{cursor}")).await.json();
        for i in body["items"].as_array().unwrap() {
            assert!(seen.insert(i["id"].as_str().unwrap().to_string()), "repeat");
        }
        match body["nextCursor"].as_str() {
            Some(c) => cursor = format!("&cursor={c}"),
            None => break,
        }
    }
    assert_eq!(seen.len(), 7);
}

#[tokio::test]
async fn filters() {
    let h = harness!("UTC");
    seed(&h).await;
    let count = |q: &'static str| {
        let h = &h;
        async move { h.get(&format!("/api/v1/results?limit=500&{q}")).await.json()["items"].as_array().unwrap().len() }
    };
    assert_eq!(count("provider=ndt7").await, 3);
    assert_eq!(count("provider=ookla,ndt7").await, 17);
    assert_eq!(count("provider=ookla&provider=ndt7").await, 17);
    assert_eq!(count("device=server").await, 10);
    assert_eq!(count("device=server,phone").await, 13);
    assert_eq!(count("connectionType=wireless").await, 7);
    assert_eq!(count("trigger=on_demand").await, 3);
    assert_eq!(count("device=laptop&connectionType=wired").await, 0);
    assert_eq!(count("device=nobody").await, 0);
    assert_eq!(count("device=%25%5F").await, 0); // "%_": a name with LIKE wildcards matches nothing, and nothing else
    // from is inclusive and to is exclusive
    assert_eq!(count("from=2026-06-01T19:00:00Z&to=2026-06-03T19:00:00Z&device=server").await, 2);
    assert_eq!(count("from=2026-06-10T19:00:00Z&device=server").await, 1);
    assert_eq!(count("to=2026-06-01T19:00:00Z&device=server").await, 0);
    assert_eq!(count("from=2026-06-01T12:00:00-07:00&to=2026-06-02T12:00:00-07:00&device=server").await, 1); // offsets are honoured
}

#[tokio::test]
async fn one_result_with_its_original_payload() {
    let h = harness!("UTC");
    seed(&h).await;
    let list = h.get("/api/v1/results?provider=ndt7&limit=1").await.json();
    let id = list["items"][0]["id"].as_str().unwrap();
    let r = h.get(&format!("/api/v1/results/{id}")).await;
    assert_eq!(r.status, StatusCode::OK);
    let body = r.json();
    assert_eq!(body["raw"], json!({"hello": "world", "n": 1.0}));
    assert_eq!(body["id"], id);
    assert_eq!(body["downloadMbps"], 52.0);
    // the payload is passed through as stored: jsonb prints the shorter key first, and 1.0 stays 1.0
    assert!(r.text().contains(r#""raw":{"n": 1.0, "hello": "world"}"#), "{}", r.text());
    // anything that is not an id, or not a result, is a plain 404
    for bad in [
        "not-a-uuid",
        "3f2a9c1e-0000-4000-8000-000000000000",
        "3F2A9C1E00004000800000000000000",
        "%7B3f2a9c1e-0000-4000-8000-000000000000%7D",
    ] {
        assert_problem(&h.get(&format!("/api/v1/results/{bad}")).await, 404);
    }
}

#[tokio::test]
async fn summary_numbers() {
    let h = harness!("UTC");
    assert_eq!(h.get("/api/v1/stats/summary").await.json()["count"], 0);
    let empty = h.get("/api/v1/stats/summary").await.json();
    assert_eq!(
        (empty["first"].clone(), empty["last"].clone(), empty["downloadMbps"]["avg"].clone()),
        (Value::Null, Value::Null, Value::Null)
    );

    seed(&h).await;
    let s = h.get("/api/v1/stats/summary?device=server").await.json();
    assert_eq!(s["count"], 10);
    assert_eq!((s["first"].as_str(), s["last"].as_str()), (Some("2026-06-01T19:00:00Z"), Some("2026-06-10T19:00:00Z")));
    let d = &s["downloadMbps"];
    assert_eq!((f(&d["avg"]), f(&d["min"]), f(&d["max"]), f(&d["p50"])), (550.0, 100.0, 1000.0, 550.0));
    assert!((f(&d["p95"]) - 955.0).abs() < 1e-9, "p95 = {}", d["p95"]); // 900 + 0.55 * (1000 - 900)
    assert_eq!(f(&s["uploadMbps"]["p50"]), 55.0);
    assert_eq!(f(&s["latencyMs"]["min"]), 10.0);
    // nulls are left out of the aggregates: eight values, one of them 2.5
    let loss = &s["packetLossPct"];
    assert!((f(&loss["avg"]) - 0.3125).abs() < 1e-9 && f(&loss["max"]) == 2.5 && f(&loss["min"]) == 0.0, "{loss}");
    // a filter with no matches is a valid, empty summary
    let none = h.get("/api/v1/stats/summary?device=nobody").await.json();
    assert_eq!(none["count"], 0);
    assert_eq!(none["packetLossPct"], json!({"avg": null, "min": null, "max": null, "p50": null, "p95": null}));
}

fn series_of(ts: &Value, key: Value) -> &Vec<Value> {
    ts["series"].as_array().unwrap().iter().find(|s| s["key"] == key).unwrap_or_else(|| panic!("no series {key} in {ts}"))["points"]
        .as_array()
        .unwrap()
}

#[tokio::test]
async fn timeseries_buckets_follow_the_time_zone() {
    let h = harness!("UTC");
    seed(&h).await;
    // laptop results are at 05:30Z: the next day in UTC, the evening before in Phoenix
    let utc = h.get("/api/v1/stats/timeseries?device=laptop&interval=day&timezone=UTC&groupBy=device").await.json();
    let phx = h.get("/api/v1/stats/timeseries?device=laptop&interval=day&timezone=America/Phoenix&groupBy=device").await.json();
    let buckets = |ts: &Value| {
        series_of(ts, json!({"device": "laptop"})).iter().map(|p| p["bucket"].as_str().unwrap().to_string()).collect::<Vec<_>>()
    };
    assert_eq!(buckets(&utc), ["2026-06-02T00:00:00Z", "2026-06-03T00:00:00Z", "2026-06-04T00:00:00Z", "2026-06-05T00:00:00Z"]);
    assert_eq!(buckets(&phx), ["2026-06-01T07:00:00Z", "2026-06-02T07:00:00Z", "2026-06-03T07:00:00Z", "2026-06-04T07:00:00Z"]);
    assert_eq!(phx["timezone"], "America/Phoenix");
    assert_eq!(phx["interval"], "day");

    // the server's default time zone is used when none is asked for
    let phoenix = h.with_timezone("America/Phoenix");
    let dflt = call(&phoenix, "GET", "/api/v1/stats/timeseries?device=laptop&groupBy=device").await.json();
    assert_eq!(dflt["timezone"], "America/Phoenix");
    assert_eq!(buckets(&dflt), buckets(&phx));
}

#[tokio::test]
async fn timeseries_intervals_grouping_and_values() {
    let h = harness!("UTC");
    seed(&h).await;
    // weeks start on Monday; 2026-06-01 is a Monday: days 1-7 are one week, days 8-10 the next
    let w = h.get("/api/v1/stats/timeseries?device=server&interval=week&groupBy=device").await.json();
    let pts = series_of(&w, json!({"device": "server"}));
    assert_eq!(pts.len(), 2);
    assert_eq!((pts[0]["bucket"].as_str(), pts[0]["count"].as_i64()), (Some("2026-06-01T00:00:00Z"), Some(7)));
    assert_eq!((pts[1]["bucket"].as_str(), pts[1]["count"].as_i64()), (Some("2026-06-08T00:00:00Z"), Some(3)));
    assert_eq!(f(&pts[0]["downloadMbps"]["avg"]), 400.0); // (100 + ... + 700) / 7
    assert_eq!(f(&pts[1]["downloadMbps"]["max"]), 1000.0);
    // month and year
    let m = h.get("/api/v1/stats/timeseries?device=server&interval=month&groupBy=device").await.json();
    assert_eq!(series_of(&m, json!({"device": "server"}))[0]["bucket"], "2026-06-01T00:00:00Z");
    let y = h.get("/api/v1/stats/timeseries?device=server&interval=year&groupBy=device").await.json();
    assert_eq!(series_of(&y, json!({"device": "server"}))[0]["count"], 10);
    // raw: every result is its own point
    let raw = h.get("/api/v1/stats/timeseries?device=server&interval=raw&groupBy=device").await.json();
    let rp = series_of(&raw, json!({"device": "server"}));
    assert_eq!(rp.len(), 10);
    assert!(rp.iter().all(|p| p["count"] == 1));
    // grouping: the key holds only the groupBy fields, and series come out ordered by key
    let g = h.get("/api/v1/stats/timeseries?interval=year&groupBy=provider,connectionType").await.json();
    let keys: Vec<_> = g["series"].as_array().unwrap().iter().map(|s| s["key"].clone()).collect();
    assert_eq!(
        keys,
        [
            json!({"provider": "ndt7", "connectionType": "wireless"}),
            json!({"provider": "ookla", "connectionType": "wired"}),
            json!({"provider": "ookla", "connectionType": "wireless"}),
        ]
    );
    // no groupBy means all three fields
    let all = h.get("/api/v1/stats/timeseries?interval=year").await.json();
    assert_eq!(all["series"].as_array().unwrap().len(), 3);
    assert_eq!(all["series"][0]["key"], json!({"provider": "ndt7", "device": "phone", "connectionType": "wireless"}));
    // a range with no data has no series
    assert_eq!(h.get("/api/v1/stats/timeseries?device=nobody").await.json()["series"], json!([]));
}

#[tokio::test]
async fn too_many_points_is_refused_not_truncated() {
    let h = harness!("UTC");
    sqlx::query(r#"INSERT INTO speedtests (provider, "trigger", device, connection_type, tested_at, download_mbps, upload_mbps, raw_data)
                   SELECT 'ookla', 'scheduled', 'bulk', 'wired', timestamptz '2026-01-01 00:00:00+00' + (i * interval '1 second'), 1, 1, '{}' FROM generate_series(0, 20000) i"#)
        .execute(&h.pool).await.unwrap();
    let r = h.get("/api/v1/stats/timeseries?interval=raw&groupBy=device").await;
    assert!(assert_problem(&r, 400)["detail"].as_str().unwrap().contains("too many points"));
    // the same data in daily buckets is fine
    assert_eq!(h.get("/api/v1/stats/timeseries?interval=day&groupBy=device").await.status, StatusCode::OK);
}

#[tokio::test]
async fn bad_requests_are_400_with_every_problem_listed() {
    let h = harness!("UTC");
    seed(&h).await;
    for uri in [
        "/api/v1/results?limit=0",
        "/api/v1/results?limit=501",
        "/api/v1/results?limit=abc",
        "/api/v1/results?limit=",
        "/api/v1/results?cursor=garbage",
        "/api/v1/results?cursor=",
        "/api/v1/results?provider=carrier-pigeon",
        "/api/v1/results?connectionType=satellite",
        "/api/v1/results?trigger=sometimes",
        "/api/v1/results?from=yesterday",
        "/api/v1/results?device=a,,b",
        "/api/v1/results?from=2026-06-02T00:00:00Z&to=2026-06-01T00:00:00Z",
        "/api/v1/results?device=a%00b",
        "/api/v1/stats/summary?provider=x",
        "/api/v1/stats/summary?to=2026-13-45T00:00:00Z",
        "/api/v1/stats/timeseries?interval=fortnight",
        "/api/v1/stats/timeseries?interval=",
        "/api/v1/stats/timeseries?groupBy=colour",
        "/api/v1/stats/timeseries?timezone=Mars/Olympus",
        "/api/v1/stats/timeseries?timezone=Local",
        "/api/v1/stats/timeseries?timezone=",
    ] {
        let r = h.get(uri).await;
        // "timezone=" and "limit=" are present-but-empty: refused for limit, and an empty timezone means "use the default"
        if uri.ends_with("timezone=") {
            assert_eq!(r.status, StatusCode::OK, "{uri}");
            continue;
        }
        let p = assert_problem(&r, 400);
        assert!(!p["errors"].as_array().unwrap().is_empty(), "{uri}: {p}");
    }
    let p = assert_problem(&h.get("/api/v1/results?limit=0&provider=x&from=nope").await, 400);
    let fields: Vec<_> = p["errors"].as_array().unwrap().iter().map(|e| e["field"].as_str().unwrap()).collect();
    assert_eq!(fields, ["from", "provider", "limit"]);
}

#[tokio::test]
async fn hostile_input_is_data_not_sql() {
    let h = harness!("UTC");
    seed(&h).await;
    for evil in ["'; DROP TABLE speedtests; --", "\" OR 1=1 --", "'); DROP TABLE speedtests; --", "x' OR '1'='1"] {
        let r = h.get(&format!("/api/v1/results?device={}", utf8_percent_encode(evil, NON_ALPHANUMERIC))).await;
        assert!(r.status == StatusCode::OK || r.status == StatusCode::BAD_REQUEST, "{evil}: {}", r.status);
        if r.status == StatusCode::OK {
            assert_eq!(r.json()["items"], json!([]));
        }
    }
    assert_eq!(h.get("/api/v1/stats/summary").await.json()["count"], 17, "the table survived");
}
