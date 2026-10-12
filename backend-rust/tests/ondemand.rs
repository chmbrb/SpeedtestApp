mod common;

use std::time::Duration;

use axum::{body::Body, http::Request};
use common::*;
use serde_json::{Value, json};
use tower::ServiceExt;

macro_rules! rig {
    ($runner:expr) => {
        rig!($runner, |_c: &mut speedtest_backend::ondemand::config::Config| {})
    };
    ($runner:expr, $mutate:expr) => {{
        let Some(h) = Harness::new("UTC", None).await else { return };
        let mut cfg = od_config();
        ($mutate)(&mut cfg);
        let runner: Option<std::sync::Arc<dyn speedtest_backend::ondemand::ookla::Runner>> = $runner;
        h.with_ondemand(cfg, runner).await
    }};
}

fn finishes() -> Option<std::sync::Arc<dyn speedtest_backend::ondemand::ookla::Runner>> {
    Some(finishes_with(real_result()))
}

fn summary(finished_ago: i64) -> String {
    let end = chrono::Utc::now() - chrono::Duration::seconds(finished_ago);
    let start = end - chrono::Duration::seconds(20);
    format!(
        r#"{{"startedAt":"{}","finishedAt":"{}","downloadMbps":87.5,"uploadMbps":23.25,"minRttMs":18.4,"retransmissionPct":0.31,
        "server":{{"machine":"mlab1-lax05","location":"Los Angeles","country":"US"}},"raw":{{"note":"x"}}}}"#,
        start.format("%Y-%m-%dT%H:%M:%SZ"),
        end.format("%Y-%m-%dT%H:%M:%SZ")
    )
}

fn problem_detail(r: &Resp) -> String {
    assert_eq!(r.header("content-type"), "application/problem+json", "{}", r.text());
    r.json()["detail"].as_str().unwrap_or_default().to_string()
}

fn field_errors(r: &Resp) -> Vec<String> {
    r.json()["errors"].as_array().map(|a| a.iter().map(|e| e["field"].as_str().unwrap().to_string()).collect()).unwrap_or_default()
}

const SERVER: &str = r#"{"provider":"ookla","mode":"server","connectionType":"wired"}"#;

// ---- capabilities -------------------------------------------------------------------------------------------------------

#[tokio::test]
async fn capabilities_report_what_works_and_why() {
    let rig = rig!(finishes(), |c: &mut speedtest_backend::ondemand::config::Config| {
        c.ookla_accepted = false;
        c.device_name = "homelab".into();
    });
    let caps = rig.get("/api/v1/capabilities").await.json();
    assert_eq!((caps["deviceName"].clone(), caps["serverConnectionType"].clone()), (json!("homelab"), json!("wired")));
    assert_eq!(caps["ookla"]["available"], false);
    assert!(caps["ookla"]["reason"].as_str().unwrap().contains("OOKLA_ACCEPT_LICENSE=yes"));
    assert_eq!(caps["ndt7"], json!({"available": true, "reason": null}));
    // with the terms accepted and the tool found, Ookla is available
    let ok = rig.also(od_config(), finishes()).await;
    assert_eq!(call(&ok, "GET", "/api/v1/capabilities").await.json()["ookla"], json!({"available": true, "reason": null}));
    // ...but not when the tool is missing, and then it says what to do
    let missing = rig
        .also(speedtest_backend::ondemand::config::Config { ookla_path: "no-such-speedtest-tool".into(), ..od_config() }, finishes())
        .await;
    assert!(call(&missing, "GET", "/api/v1/capabilities").await.json()["ookla"]["reason"].as_str().unwrap().contains("OOKLA_PATH"));
}

#[tokio::test]
async fn without_a_manager_every_test_endpoint_says_so() {
    let app = offline_router(None);
    for (m, p) in [
        ("GET", "/api/v1/tests"),
        ("POST", "/api/v1/tests"),
        ("GET", "/api/v1/tests/3f2a9c1e-0000-4000-8000-000000000000"),
        ("DELETE", "/api/v1/tests/3f2a9c1e-0000-4000-8000-000000000000"),
        ("GET", "/api/v1/tests/x/events"),
        ("POST", "/api/v1/tests/x/result"),
    ] {
        let r = call(&app, m, p).await;
        assert_eq!(r.status, 503, "{m} {p}");
        assert_eq!(r.header("content-type"), "application/problem+json");
    }
    let caps = call(&app, "GET", "/api/v1/capabilities").await.json();
    assert_eq!(caps["ookla"]["available"], false);
}

// ---- a server test, start to finish -------------------------------------------------------------------------------------

#[tokio::test]
async fn a_server_test_from_start_to_finish() {
    let rig = rig!(finishes());
    let r = rig.create(r#"{"provider":"ookla","mode":"server","connectionType":"wireless","device":"ignored"}"#).await;
    assert_eq!(r.status, 201, "{}", r.text());
    let test = r.json();
    // a server test uses the server's own name and connection, has no token and no queue position
    assert_eq!(
        (test["state"].clone(), test["device"].clone(), test["connectionType"].clone(), test["provider"].clone()),
        (json!("queued"), json!("server"), json!("wired"), json!("ookla"))
    );
    assert!(test.get("submitToken").is_none());
    assert_eq!(test["queuePosition"], Value::Null);
    let id = test["id"].as_str().unwrap();

    let evs = events(&rig, id).await;
    // the current state first (sent without an id), then the whole history numbered 1, 2, 3...
    assert_eq!(names(&evs), "state,state,state,progress,progress,progress,state,result");
    assert!(evs[0].id.is_none(), "the current state is not part of the replay");
    let ids: Vec<_> = evs[1..].iter().map(|e| e.id.clone().unwrap()).collect();
    assert_eq!(ids, ["1", "2", "3", "4", "5", "6", "7"]);
    assert_eq!(evs[1].data["test"]["state"], "queued");
    assert_eq!(evs[2].data["test"]["state"], "running");
    assert_eq!(evs[6].data["test"]["state"], "completed");
    assert_eq!(evs[3].data["phase"], "ping");
    assert_eq!(
        evs[4].data,
        json!({"type":"progress","phase":"download","elapsedMs":100,"fraction":0.5,"currentMbps":90.0,"latencyMs":null,"jitterMs":null})
    );
    let res = &evs[7].data["result"];
    assert!((res["downloadMbps"].as_f64().unwrap() - 99.2).abs() < 0.1, "{res}");
    assert_eq!(
        (res["trigger"].clone(), res["provider"].clone(), res["device"].clone()),
        (json!("on_demand"), json!("ookla"), json!("server"))
    );

    // the stored test and result
    let done = rig.get(&format!("/api/v1/tests/{id}")).await.json();
    assert_eq!(done["state"], "completed");
    assert!(done["resultId"].is_string() && done["finishedAt"].is_string() && done["startedAt"].is_string() && done["error"].is_null());
    let stored = rig.get(&format!("/api/v1/results/{}", done["resultId"].as_str().unwrap())).await.json();
    assert!(stored["packetLossPct"].as_f64().unwrap() >= 2.0);
    // a finished test can be listened to later, and cancelling it is a harmless no-op
    assert_eq!(events(&rig, id).await.len(), 8, "a late listener gets the whole history");
    let again = rig.send("DELETE", &format!("/api/v1/tests/{id}"), None, "").await;
    assert_eq!((again.status.as_u16(), again.json()["state"].clone()), (200, json!("completed")));
}

#[tokio::test]
async fn only_one_server_test_runs_at_a_time() {
    let (runner, release) = blocks(real_result());
    let rig = rig!(Some(runner.clone() as std::sync::Arc<dyn speedtest_backend::ondemand::ookla::Runner>));
    let first = rig.server().await;
    rig.wait_state(&first, "running").await;
    for _ in 0..200 {
        if runner.calls.load(std::sync::atomic::Ordering::SeqCst) >= 1 {
            break; // "running" is recorded just before the runner starts
        }
        tokio::time::sleep(Duration::from_millis(10)).await;
    }
    let second = rig.create(SERVER).await;
    assert_eq!(second.status, 409, "{}", second.text());
    assert!(problem_detail(&second).contains("already running"));
    assert_eq!(runner.calls.load(std::sync::atomic::Ordering::SeqCst), 1, "the refused test must never start a process");
    release.notify_one();
    rig.wait_state(&first, "completed").await;
    // the slot is free again
    let again = rig.server().await;
    rig.wait_state(&again, "running").await;
    release.notify_one();
    rig.wait_state(&again, "completed").await;
}

#[tokio::test]
async fn concurrent_starts_let_exactly_one_through() {
    let (runner, release) = blocks(real_result());
    let rig = rig!(Some(runner as std::sync::Arc<dyn speedtest_backend::ondemand::ookla::Runner>));
    let futures = (0..12).map(|_| rig.create(SERVER));
    let results = futures_util::future::join_all(futures).await;
    let statuses: Vec<u16> = results.iter().map(|r| r.status.as_u16()).collect();
    assert_eq!(statuses.iter().filter(|s| **s == 201).count(), 1, "the database decides the race: {statuses:?}");
    assert_eq!(statuses.iter().filter(|s| **s == 409).count(), 11, "{statuses:?}");
    assert_eq!(rig.count("SELECT count(*) FROM tests WHERE state IN ('queued','running')").await, 1);
    release.notify_one();
}

#[tokio::test]
async fn cancelling_stops_a_running_test() {
    let (runner, _release) = blocks(real_result());
    let rig = rig!(Some(runner as std::sync::Arc<dyn speedtest_backend::ondemand::ookla::Runner>));
    let id = rig.server().await;
    rig.wait_state(&id, "running").await;
    let listening = {
        let req = Request::builder().uri(format!("/api/v1/tests/{id}/events")).body(Body::empty()).unwrap();
        let res = rig.app.clone().oneshot(req).await.unwrap();
        tokio::spawn(read_sse(res, |_| false))
    };
    let r = rig.send("DELETE", &format!("/api/v1/tests/{id}"), None, "").await;
    assert_eq!(r.status, 200, "{}", r.text());
    let t = r.json();
    assert_eq!(t["state"], "cancelled", "the answer is the final state");
    assert!(t["finishedAt"].is_string() && t["resultId"].is_null());
    let evs = listening.await.unwrap();
    let last = evs.last().unwrap();
    assert_eq!((last.name.as_str(), last.data["test"]["state"].clone()), ("state", json!("cancelled")), "{}", names(&evs));
    assert!(evs.iter().all(|e| e.name != "result" && e.name != "error"), "a cancelled test has no result and no error: {}", names(&evs));
    assert_eq!(rig.count("SELECT count(*) FROM speedtests").await, 0, "nothing is stored for a cancelled test");
    // the slot is free straight away
    let r = rig.create(SERVER).await;
    assert_eq!(r.status, 201, "{}", r.text());
}

#[tokio::test]
async fn a_failure_is_reported_with_a_reason() {
    let rig = rig!(Some(fails_with("the Ookla speed test failed: Network is unreachable")));
    let id = rig.server().await;
    let evs = events(&rig, &id).await;
    let last = evs.last().unwrap();
    assert_eq!(last.name, "error", "{}", names(&evs));
    assert!(last.data["message"].as_str().unwrap().contains("Network is unreachable"));
    let t = rig.get(&format!("/api/v1/tests/{id}")).await.json();
    assert_eq!(t["state"], "failed");
    assert!(t["error"].as_str().unwrap().contains("Network is unreachable") && t["resultId"].is_null());
    assert_eq!(rig.count("SELECT count(*) FROM speedtests").await, 0);
}

#[tokio::test]
async fn tests_that_run_too_long_are_stopped() {
    let (runner, _r) = blocks(real_result());
    let rig = rig!(
        Some(runner as std::sync::Arc<dyn speedtest_backend::ondemand::ookla::Runner>),
        |c: &mut speedtest_backend::ondemand::config::Config| { c.test_timeout = Duration::from_millis(300) }
    );
    let id = rig.server().await;
    let t = rig.wait_state(&id, "failed").await;
    assert_eq!(t["error"], "The test took longer than 300ms and was stopped.");
}

#[tokio::test]
async fn a_result_the_database_rejects_fails_the_test_cleanly() {
    let absurd = br#"{"type":"result","timestamp":"2026-10-11T12:00:00Z","download":{"bandwidth":1e30},"upload":{"bandwidth":1}}"#.to_vec();
    let rig = rig!(Some(finishes_with(absurd)));
    let id = rig.server().await;
    let t = rig.wait_state(&id, "failed").await;
    assert!(t["error"].as_str().unwrap().contains("could not be saved"), "a reason a person can read, not a SQL error: {t}");
}

#[tokio::test]
async fn ookla_stays_off_until_the_terms_are_accepted() {
    let runner = finishes_with(real_result());
    let rig = rig!(
        Some(runner.clone() as std::sync::Arc<dyn speedtest_backend::ondemand::ookla::Runner>),
        |c: &mut speedtest_backend::ondemand::config::Config| { c.ookla_accepted = false }
    );
    let r = rig.create(SERVER).await;
    assert_eq!(r.status, 503);
    assert!(problem_detail(&r).contains("OOKLA_ACCEPT_LICENSE=yes"));
    assert_eq!(runner.calls.load(std::sync::atomic::Ordering::SeqCst), 0);
    assert_eq!(rig.count("SELECT count(*) FROM tests").await, 0, "nothing starts or is recorded without acceptance");
    // NDT7 in a browser has no such gate
    let b = rig.create(r#"{"provider":"ndt7","mode":"browser","connectionType":"wireless","device":"My phone"}"#).await;
    assert_eq!(b.status, 201, "{}", b.text());
}

// ---- asking for a test --------------------------------------------------------------------------------------------------

#[tokio::test]
async fn bad_requests_are_refused_with_the_field_named() {
    let rig = rig!(finishes());
    let long = "x".repeat(65);
    let cases: Vec<(&str, String, u16, &str)> = vec![
        ("unknown provider", r#"{"provider":"speedof","mode":"server","connectionType":"wired"}"#.into(), 400, "provider"),
        ("unknown mode", r#"{"provider":"ookla","mode":"cloud","connectionType":"wired"}"#.into(), 400, "mode"),
        ("ndt7 on the server", r#"{"provider":"ndt7","mode":"server","connectionType":"wired"}"#.into(), 400, "provider"),
        ("ookla in a browser", r#"{"provider":"ookla","mode":"browser","connectionType":"wired","device":"x"}"#.into(), 400, "provider"),
        ("no connection type", r#"{"provider":"ndt7","mode":"browser","device":"x"}"#.into(), 400, "connectionType"),
        ("a server test needs a valid connection type too", r#"{"provider":"ookla","mode":"server"}"#.into(), 400, "connectionType"),
        ("bad connection", r#"{"provider":"ndt7","mode":"browser","connectionType":"lte","device":"x"}"#.into(), 400, "connectionType"),
        ("no device", r#"{"provider":"ndt7","mode":"browser","connectionType":"wired"}"#.into(), 400, "device"),
        ("device is null", r#"{"provider":"ndt7","mode":"browser","connectionType":"wired","device":null}"#.into(), 400, "device"),
        ("device is a number", r#"{"provider":"ndt7","mode":"browser","connectionType":"wired","device":5}"#.into(), 400, "device"),
        ("device is blank", r#"{"provider":"ndt7","mode":"browser","connectionType":"wired","device":"   "}"#.into(), 400, "device"),
        (
            "control characters",
            r#"{"provider":"ndt7","mode":"browser","connectionType":"wired","device":"a\u0000b"}"#.into(),
            400,
            "device",
        ),
        ("too long", format!(r#"{{"provider":"ndt7","mode":"browser","connectionType":"wired","device":"{long}"}}"#), 400, "device"),
        (
            "a server test with a junk device name",
            format!(r#"{{"provider":"ookla","mode":"server","connectionType":"wired","device":"{long}"}}"#),
            400,
            "device",
        ),
    ];
    for (name, body, status, field) in cases {
        let r = rig.create(&body).await;
        assert_eq!(r.status.as_u16(), status, "{name}: {}", r.text());
        assert!(field_errors(&r).contains(&field.to_string()), "{name}: want an error on {field:?}, got {:?}", field_errors(&r));
    }
    for (name, body) in [
        ("not json", "hi"),
        ("empty", ""),
        ("an array", "[]"),
        ("a number", "5"),
        ("provider is a number", r#"{"provider":5,"mode":"server","connectionType":"wired"}"#),
    ] {
        let r = rig.create(body).await;
        assert_eq!(r.status, 400, "{name}");
        assert!(problem_detail(&r).contains("body must be JSON"), "{name}: {}", r.text());
    }
    assert_eq!(rig.count("SELECT count(*) FROM tests").await, 0, "invalid requests create nothing");
    // Go's decoder reads the first JSON value and ignores what follows; keys match ignoring case
    let r = rig.create(r#"  {"Provider":"ndt7","MODE":"browser","connectionType":"wired","device":"  My PC  "}  trailing junk"#).await;
    assert_eq!(r.status, 201, "{}", r.text());
    assert_eq!(r.json()["device"], "My PC", "the name is trimmed");
}

#[tokio::test]
async fn an_agent_token_decides_the_device_and_a_wrong_one_is_refused() {
    let rig = rig!(finishes());
    let body = r#"{"provider":"ndt7","mode":"browser","connectionType":"wired","device":"spoofed"}"#;
    let ok = rig.send("POST", "/api/v1/tests", Some("tok-laptop"), body).await;
    assert_eq!((ok.status.as_u16(), ok.json()["device"].clone()), (201, json!("laptop")));
    // a token that is present must be valid
    let bad = rig.send("POST", "/api/v1/tests", Some("nope"), body).await;
    assert_eq!(bad.status, 401);
    assert_eq!(bad.header("www-authenticate"), "Bearer");
}

#[tokio::test]
async fn starting_tests_is_rate_limited_per_address() {
    let rig = rig!(finishes(), |c: &mut speedtest_backend::ondemand::config::Config| {
        c.create_per_minute = 1;
        c.trust_proxy = true;
    });
    let body = r#"{"provider":"ndt7","mode":"browser","connectionType":"wired","device":"d"}"#;
    let from = |peer: &'static str, xff: &'static str| {
        let app = rig.app.clone();
        async move {
            let forwarded = [("x-forwarded-for", xff)];
            let headers: &[(&str, &str)] = if xff.is_empty() { &[] } else { &forwarded };
            send_from(&app, "POST", "/api/v1/tests", peer, headers, body).await
        }
    };
    for i in 0..4 {
        assert_eq!(from("203.0.113.5", "198.51.100.1").await.status, 201, "burst {i}"); // behind the proxy: the address is the one it forwards
    }
    let limited = from("203.0.113.5", "198.51.100.1").await;
    assert_eq!(limited.status, 429);
    assert!(limited.header("retry-after").parse::<u64>().unwrap() >= 1);
    assert_eq!(from("203.0.113.5", "198.51.100.2").await.status, 201, "another visitor behind the same proxy");
    assert_eq!(from("203.0.113.5", "").await.status, 201, "no forwarded address: the peer's own");
    assert_eq!(from("198.51.100.1", "").await.status, 429, "the peer address 198.51.100.1 is the same address as the forwarded one");
}

#[tokio::test]
async fn a_forwarded_address_is_only_believed_behind_a_proxy() {
    let rig = rig!(finishes(), |c: &mut speedtest_backend::ondemand::config::Config| c.create_per_minute = 1); // trust_proxy is off
    let body = r#"{"provider":"ndt7","mode":"browser","connectionType":"wired","device":"d"}"#;
    let mut statuses = Vec::new();
    for i in 0..6 {
        let xff = format!("198.51.100.{i}");
        statuses
            .push(send_from(&rig.app, "POST", "/api/v1/tests", "203.0.113.5", &[("x-forwarded-for", &xff)], body).await.status.as_u16());
    }
    assert_eq!(statuses, [201, 201, 201, 201, 429, 429], "forging the header must not buy more tests");
}

// ---- a browser test -----------------------------------------------------------------------------------------------------

#[tokio::test]
async fn a_browser_test_and_its_single_use_token() {
    let rig = rig!(finishes());
    let r =
        rig.send_from_ip("203.0.113.50", r#"{"provider":"ndt7","mode":"browser","connectionType":"wireless","device":"My phone"}"#).await;
    assert_eq!(r.status, 201, "{}", r.text());
    let test = r.json();
    let (id, token) = (test["id"].as_str().unwrap().to_string(), test["submitToken"].as_str().unwrap().to_string());
    assert_eq!(
        (test["state"].clone(), test["device"].clone(), test["provider"].clone()),
        (json!("running"), json!("My phone"), json!("ndt7"))
    );
    assert!(test["startedAt"].is_string() && token.len() >= 40);
    // only a hash of the token is stored
    let stored: Vec<u8> =
        sqlx::query_scalar("SELECT submit_token_hash FROM tests WHERE id = $1::uuid").bind(&id).fetch_one(&rig.pool).await.unwrap();
    assert_eq!(stored.len(), 32);
    assert!(!String::from_utf8_lossy(&stored).contains(&token));
    // reading the test again never shows the token
    assert!(rig.get(&format!("/api/v1/tests/{id}")).await.json().get("submitToken").is_none());

    let url = format!("/api/v1/tests/{id}/result");
    // no token / a wrong token: refused, and the right one is not burned
    for t in [None, Some("wrong-token")] {
        let r = rig.send("POST", &url, t, &summary(0)).await;
        assert_eq!(r.status, 401, "{t:?}");
        assert_eq!(r.header("www-authenticate"), "Bearer");
    }
    assert_eq!(rig.get(&format!("/api/v1/tests/{id}")).await.json()["state"], "running");

    let ok = rig.send_from("POST", &url, "203.0.113.50", Some(&token), &summary(0)).await;
    assert_eq!(ok.status, 201, "{}", ok.text());
    let res = ok.json();
    assert_eq!(
        (res["provider"].clone(), res["device"].clone(), res["connectionType"].clone(), res["trigger"].clone()),
        (json!("ndt7"), json!("My phone"), json!("wireless"), json!("on_demand"))
    );
    assert_eq!(
        (res["downloadMbps"].clone(), res["uploadMbps"].clone(), res["latencyMs"].clone()),
        (json!(87.5), json!(23.25), json!(18.4))
    );
    let ip: Option<String> = sqlx::query_scalar("SELECT host(interface_external_ip) FROM speedtests WHERE id = $1::uuid")
        .bind(res["id"].as_str().unwrap())
        .fetch_one(&rig.pool)
        .await
        .unwrap();
    assert_eq!(ip.as_deref(), Some("203.0.113.50"), "the address the server saw is recorded");
    let done = rig.get(&format!("/api/v1/tests/{id}")).await.json();
    assert_eq!((done["state"].clone(), done["resultId"].clone()), (json!("completed"), res["id"].clone()));
    let hash: Option<Vec<u8>> =
        sqlx::query_scalar("SELECT submit_token_hash FROM tests WHERE id = $1::uuid").bind(&id).fetch_one(&rig.pool).await.unwrap();
    assert!(hash.is_none(), "the token is spent");

    // the same token cannot be used twice
    let twice = rig.send("POST", &url, Some(&token), &summary(0)).await;
    assert_eq!(twice.status, 409);
    assert!(problem_detail(&twice).contains("already completed"));
    assert_eq!(rig.count("SELECT count(*) FROM speedtests").await, 1, "exactly one result");
    // unknown and malformed ids
    for bad in ["3f2a9c1e-0000-4000-8000-000000000000", "not-a-uuid"] {
        assert_eq!(rig.send("POST", &format!("/api/v1/tests/{bad}/result"), Some("x"), &summary(0)).await.status, 404, "{bad}");
    }
}

#[tokio::test]
async fn a_result_that_is_refused_does_not_spend_the_token() {
    let rig = rig!(finishes());
    let t = rig.create(r#"{"provider":"ndt7","mode":"browser","connectionType":"wired","device":"pc"}"#).await.json();
    let (id, token) = (t["id"].as_str().unwrap(), t["submitToken"].as_str().unwrap());
    let url = format!("/api/v1/tests/{id}/result");
    // the API's checks
    let absurd = summary(0).replace("87.5", "1e30");
    let r = rig.send("POST", &url, Some(token), &absurd).await;
    assert_eq!(r.status, 400);
    assert!(field_errors(&r).contains(&"downloadMbps".to_string()), "fields have no prefix here: {:?}", field_errors(&r));
    // the browser-only checks: a test lasts 1 second to 10 minutes, and must be recent
    let end = chrono::Utc::now();
    let short = format!(r#"{{"startedAt":"{0}","finishedAt":"{0}","downloadMbps":1,"uploadMbps":1}}"#, end.format("%Y-%m-%dT%H:%M:%SZ"));
    assert_eq!(rig.send("POST", &url, Some(token), &short).await.status, 400);
    let old = summary(2 * 3600);
    let r = rig.send("POST", &url, Some(token), &old).await;
    assert_eq!(r.status, 400);
    assert!(r.json()["errors"].to_string().contains("must be recent"));
    for bad in ["not json", "", "[]"] {
        assert_eq!(rig.send("POST", &url, Some(token), bad).await.status, 400, "{bad:?}");
    }
    let big = rig.send("POST", &url, Some(token), &"x".repeat(64 * 1024 + 1)).await;
    assert_eq!(big.status, 413);
    // the database as the backstop, one transaction: a refusal there changes nothing either
    let res = rig.manager.submit(id, token, &absurd, None).await;
    assert!(matches!(res, Err(speedtest_backend::store_tests::SubmitError::Db(_))), "{res:?}");
    assert_eq!(rig.get(&format!("/api/v1/tests/{id}")).await.json()["state"], "running");
    assert_eq!(rig.count("SELECT count(*) FROM speedtests").await, 0, "no partial result");
    // ...and the browser can try again with corrected data
    assert_eq!(rig.send("POST", &url, Some(token), &summary(0)).await.status, 201);
}

#[tokio::test]
async fn browser_tests_expire() {
    let rig = rig!(finishes(), |c: &mut speedtest_backend::ondemand::config::Config| c.browser_ttl = Duration::from_secs(1));
    let t = rig.create(r#"{"provider":"ndt7","mode":"browser","connectionType":"wired","device":"pc"}"#).await.json();
    let (id, token) = (t["id"].as_str().unwrap(), t["submitToken"].as_str().unwrap());
    tokio::time::sleep(Duration::from_millis(1300)).await;
    let late = rig.send("POST", &format!("/api/v1/tests/{id}/result"), Some(token), &summary(0)).await;
    assert_eq!(late.status, 409, "{}", late.text());
    assert!(problem_detail(&late).contains("not finished in time"));
    let swept = rig.manager_store().sweep_tests(Duration::from_secs(1), Duration::from_secs(3600)).await.unwrap();
    assert_eq!(swept, 1);
    let row = rig.get(&format!("/api/v1/tests/{id}")).await.json();
    assert_eq!(row["state"], "failed");
    assert!(row["error"].as_str().unwrap().contains("not finished in time"));
    assert_eq!(
        rig.send("POST", &format!("/api/v1/tests/{id}/result"), Some(token), &summary(0)).await.status,
        409,
        "an expired test stays closed"
    );
}

#[tokio::test]
async fn cancelling_a_browser_test() {
    let rig = rig!(finishes());
    let t = rig.create(r#"{"provider":"ndt7","mode":"browser","connectionType":"wired","device":"pc"}"#).await.json();
    let (id, token) = (t["id"].as_str().unwrap(), t["submitToken"].as_str().unwrap());
    let r = rig.send("DELETE", &format!("/api/v1/tests/{id}"), None, "").await;
    assert_eq!((r.status.as_u16(), r.json()["state"].clone()), (200, json!("cancelled")));
    let late = rig.send("POST", &format!("/api/v1/tests/{id}/result"), Some(token), &summary(0)).await;
    assert_eq!(late.status, 409);
    assert!(problem_detail(&late).contains("already cancelled"), "a cancelled test takes no result");
    assert_eq!(rig.send("DELETE", "/api/v1/tests/3f2a9c1e-0000-4000-8000-000000000000", None, "").await.status, 404);
    assert_eq!(rig.send("DELETE", "/api/v1/tests/not-a-uuid", None, "").await.status, 404);
}

// ---- restarts, shutdown, listing ----------------------------------------------------------------------------------------

#[tokio::test]
async fn starting_recovers_tests_a_crash_left_behind() {
    let rig = rig!(None);
    sqlx::query("INSERT INTO tests (provider, mode, device, connection_type, state, started_at) VALUES ('ookla','server','server','wired','running', now())").execute(&rig.pool).await.unwrap();
    sqlx::query("INSERT INTO tests (provider, mode, device, connection_type, state, finished_at) VALUES ('ookla','server','server','wired','completed', now())").execute(&rig.pool).await.unwrap();
    // the stuck row blocks new tests...
    assert_eq!(rig.create(SERVER).await.status, 409);
    // ...until the next start clears it
    rig.manager.start(tokio_util::sync::CancellationToken::new()).await;
    let msg: Option<String> = sqlx::query_scalar("SELECT error FROM tests WHERE state = 'failed'").fetch_one(&rig.pool).await.unwrap();
    assert!(msg.unwrap().contains("restarted"), "a reason a person can read");
    assert_eq!(rig.count("SELECT count(*) FROM tests WHERE state = 'completed'").await, 1, "finished tests are left alone");
}

#[tokio::test]
async fn shutting_down_fails_a_running_test_instead_of_leaving_it_stuck() {
    let (runner, _r) = blocks(real_result());
    let rig = rig!(Some(runner as std::sync::Arc<dyn speedtest_backend::ondemand::ookla::Runner>));
    let id = rig.server().await;
    rig.wait_state(&id, "running").await;
    rig.root.cancel(); // the server is shutting down
    let t = rig.wait_state(&id, "failed").await;
    assert!(t["error"].as_str().unwrap().contains("shutting down"));
    rig.manager.wait_idle(Duration::from_secs(2)).await;
}

#[tokio::test]
async fn listing_and_reading_tests() {
    let (runner, release) = blocks(real_result());
    let rig = rig!(Some(runner as std::sync::Arc<dyn speedtest_backend::ondemand::ookla::Runner>));
    let id = rig.server().await;
    rig.wait_state(&id, "running").await;
    rig.create(r#"{"provider":"ndt7","mode":"browser","connectionType":"wired","device":"pc"}"#).await;
    let count = |q: &'static str| {
        let rig = &rig;
        async move { rig.get(&format!("/api/v1/tests?{q}")).await.json()["items"].as_array().unwrap().len() }
    };
    assert_eq!((count("state=running").await, count("").await, count("state=queued").await), (2, 2, 0));
    release.notify_one();
    rig.wait_state(&id, "completed").await;
    let done = rig.get("/api/v1/tests?state=completed,failed").await.json();
    assert_eq!(done["items"].as_array().unwrap().len(), 1);
    assert_eq!(done["items"][0]["id"], id.as_str());
    assert!(done["items"][0]["resultId"].is_string());
    assert_eq!(count("limit=1").await, 1);
    // newest first
    assert_eq!(rig.get("/api/v1/tests").await.json()["items"][0]["mode"], "browser");
    for bad in ["state=bogus", "limit=0", "limit=101", "state=running,,queued"] {
        assert_eq!(rig.get(&format!("/api/v1/tests?{bad}")).await.status, 400, "{bad}");
    }
    assert_eq!(rig.get("/api/v1/tests/not-a-uuid").await.status, 404);
    assert_eq!(rig.get("/api/v1/tests/3f2a9c1e-0000-4000-8000-000000000000").await.status, 404);
}

// ---- the live stream ----------------------------------------------------------------------------------------------------

#[tokio::test]
async fn live_progress_arrives_while_the_test_runs_and_a_reader_can_resume() {
    let (runner, release) = blocks(real_result());
    let rig = rig!(Some(runner as std::sync::Arc<dyn speedtest_backend::ondemand::ookla::Runner>));
    let id = rig.server().await;
    rig.wait_state(&id, "running").await;
    // a first listener sees the state, replays what it missed, then waits for more
    let req = Request::builder().uri(format!("/api/v1/tests/{id}/events")).body(Body::empty()).unwrap();
    let res = rig.app.clone().oneshot(req).await.unwrap();
    assert_eq!(res.headers()["content-type"], "text/event-stream");
    assert_eq!(res.headers()["cache-control"], "no-cache");
    assert_eq!(res.headers()["x-accel-buffering"], "no");
    let first = tokio::spawn(read_sse(res, |e| e.name == "result"));
    tokio::time::sleep(Duration::from_millis(100)).await;
    release.notify_one();
    let evs = first.await.unwrap();
    assert_eq!(
        names(&evs),
        "state,state,state,state,result",
        "state now, replay of queued+running, then the live completed and the result"
    );
    // another listener resumes after what it has already seen: only the events after that id
    let last_seen = evs[2].id.clone().unwrap(); // the "running" event
    let req = Request::builder().uri(format!("/api/v1/tests/{id}/events")).header("last-event-id", &last_seen).body(Body::empty()).unwrap();
    let resumed = read_sse(rig.app.clone().oneshot(req).await.unwrap(), |_| false).await;
    assert_eq!(names(&resumed), "state,state,result", "the current state, then what it missed");
    // the id can also come in the query string (an EventSource cannot set headers by hand)
    let req = Request::builder().uri(format!("/api/v1/tests/{id}/events?lastEventId={last_seen}")).body(Body::empty()).unwrap();
    assert_eq!(names(&read_sse(rig.app.clone().oneshot(req).await.unwrap(), |_| false).await), "state,state,result");
    // junk is treated as "from the start"
    let req = Request::builder().uri(format!("/api/v1/tests/{id}/events?lastEventId=-5")).body(Body::empty()).unwrap();
    assert_eq!(read_sse(rig.app.clone().oneshot(req).await.unwrap(), |_| false).await.len(), 1 + 4);
}

#[tokio::test]
async fn an_unknown_test_has_no_stream() {
    let rig = rig!(finishes());
    for id in ["3f2a9c1e-0000-4000-8000-000000000000", "not-a-uuid"] {
        let r = rig.get(&format!("/api/v1/tests/{id}/events")).await;
        assert_eq!(r.status, 404, "{id}");
        assert_eq!(r.header("content-type"), "application/problem+json");
    }
}

#[tokio::test]
async fn a_test_from_before_a_restart_still_tells_how_it_ended() {
    let rig = rig!(None);
    // this process has no record of these (they ended before it started)
    let ok_result: String = sqlx::query_scalar("SELECT speedtests_insert_ndt7($1::jsonb, 'server', 'wired', 'on_demand', NULL)::text")
        .bind(summary(0))
        .fetch_one(&rig.pool)
        .await
        .unwrap();
    let done: String = sqlx::query_scalar("INSERT INTO tests (provider, mode, device, connection_type, state, finished_at, result_id) VALUES ('ndt7','browser','server','wired','completed', now(), $1::uuid) RETURNING id::text")
        .bind(&ok_result).fetch_one(&rig.pool).await.unwrap();
    let failed: String = sqlx::query_scalar("INSERT INTO tests (provider, mode, device, connection_type, state, finished_at, error) VALUES ('ookla','server','server','wired','failed', now(), 'Network is unreachable') RETURNING id::text")
        .fetch_one(&rig.pool).await.unwrap();
    let cancelled: String = sqlx::query_scalar("INSERT INTO tests (provider, mode, device, connection_type, state, finished_at) VALUES ('ookla','server','server','wired','cancelled', now()) RETURNING id::text")
        .fetch_one(&rig.pool).await.unwrap();

    let evs = events(&rig, &done).await;
    assert_eq!(names(&evs), "state,result");
    assert_eq!(evs[1].data["result"]["id"], ok_result.as_str());
    let evs = events(&rig, &failed).await;
    assert_eq!(names(&evs), "state,error");
    assert_eq!(evs[1].data["message"], "Network is unreachable");
    assert_eq!(names(&events(&rig, &cancelled).await), "state");
}

#[tokio::test]
async fn a_running_test_this_process_does_not_know_is_watched_in_the_database() {
    let rig = rig!(None);
    let id: String = sqlx::query_scalar("INSERT INTO tests (provider, mode, device, connection_type, state, started_at) VALUES ('ookla','server','server','wired','running', now()) RETURNING id::text")
        .fetch_one(&rig.pool).await.unwrap();
    let req = Request::builder().uri(format!("/api/v1/tests/{id}/events")).body(Body::empty()).unwrap();
    let res = rig.app.clone().oneshot(req).await.unwrap();
    let listening = tokio::spawn(read_sse(res, |_| false));
    tokio::time::sleep(Duration::from_millis(300)).await;
    sqlx::query("UPDATE tests SET state = 'failed', finished_at = now(), error = 'Gone.' WHERE id = $1::uuid")
        .bind(&id)
        .execute(&rig.pool)
        .await
        .unwrap();
    let evs = listening.await.unwrap(); // found within the 5-second poll, then the stream ends
    assert_eq!(names(&evs), "state,state,error");
    assert_eq!(evs[1].data["test"]["state"], "failed");
}

// ---- through the real tool-running code ---------------------------------------------------------------------------------

#[tokio::test]
async fn the_real_runner_reads_a_real_run() {
    use std::os::unix::fs::PermissionsExt;
    let dir = std::env::temp_dir().join(format!("speedtest-od-{}", std::process::id()));
    std::fs::create_dir_all(&dir).unwrap();
    let script = dir.join("speedtest");
    let fixture = concat!(env!("CARGO_MANIFEST_DIR"), "/tests/ookla_real.jsonl");
    std::fs::write(&script, format!("#!/bin/sh\ncat '{fixture}'\n")).unwrap();
    std::fs::set_permissions(&script, std::fs::Permissions::from_mode(0o755)).unwrap();
    let path = script.to_string_lossy().into_owned();
    let rig = rig!(None, |c: &mut speedtest_backend::ondemand::config::Config| c.ookla_path = path.clone());
    let id = rig.server().await;
    // the tool prints the whole run at once, so a listener falls behind and is dropped: it must be able to reconnect and resume
    let evs = events_following(&rig, &id).await;
    let n_progress = evs.iter().filter(|e| e.name == "progress").count();
    assert!(n_progress > 100, "a whole run is about 150 readings: {n_progress}");
    let ids: Vec<i64> = evs.iter().map(|e| e.id.as_ref().unwrap().parse().unwrap()).collect();
    assert_eq!(ids, (1..=ids.len() as i64).collect::<Vec<_>>(), "no event lost and none repeated across reconnections");
    assert_eq!(evs.last().unwrap().name, "result");
    assert!((evs.last().unwrap().data["result"]["downloadMbps"].as_f64().unwrap() - 99.2).abs() < 0.1);
    assert_eq!(rig.wait_state(&id, "completed").await["error"], Value::Null);
    std::fs::remove_dir_all(dir).ok();
}
