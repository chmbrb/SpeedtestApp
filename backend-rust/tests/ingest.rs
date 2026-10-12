mod common;

use axum::http::StatusCode;
use common::{Harness, Resp, send};
use serde_json::{Value, json};

const GOOD_OOKLA: &str = r#"{"type":"result","timestamp":"2026-06-01T06:35:20Z","ping":{"jitter":0.5,"latency":12.4},
	"download":{"bandwidth":116027626},"upload":{"bandwidth":14126828},"packetLoss":0}"#;
const GOOD_NDT7: &str = r#"{"startedAt":"2026-10-11T15:00:00Z","finishedAt":"2026-10-11T15:00:22Z","downloadMbps":87.5,
	"uploadMbps":23.25,"minRttMs":18.4,"retransmissionPct":0.31}"#;

fn envelope(provider: &str, connection: &str, result: &str) -> String {
    format!(r#"{{"provider":"{provider}","connectionType":"{connection}","result":{result}}}"#)
}

fn problem(r: &Resp) -> Value {
    assert_eq!(r.header("content-type"), "application/problem+json", "{}", r.text());
    r.json()
}

fn fields(p: &Value) -> Vec<String> {
    p["errors"].as_array().map(|a| a.iter().map(|e| e["field"].as_str().unwrap().to_string()).collect()).unwrap_or_default()
}

macro_rules! harness {
    () => {
        match Harness::new("UTC", None).await {
            Some(h) => h,
            None => return,
        }
    };
}

#[tokio::test]
async fn only_a_valid_token_gets_in_and_the_token_names_the_device() {
    let h = harness!();
    let body = envelope("ookla", "wireless", GOOD_OOKLA);
    for (name, token) in [
        ("missing", None),
        ("wrong", Some("nope")),
        ("a prefix of a real one", Some("tok-lap")),
        ("a real one with more", Some("tok-laptop-x")),
    ] {
        let r = h.send("POST", "/api/v1/results", token, &body).await;
        assert_eq!(r.status, StatusCode::UNAUTHORIZED, "{name}");
        assert_eq!(r.header("www-authenticate"), "Bearer", "{name}");
        problem(&r);
    }
    for scheme in ["Basic tok-laptop", "tok-laptop", "Bearer"] {
        let req = axum::http::Request::builder()
            .method("POST")
            .uri("/api/v1/results")
            .header("authorization", scheme)
            .body(axum::body::Body::from(body.clone()))
            .unwrap();
        let res = tower::ServiceExt::oneshot(h.app.clone(), req).await.unwrap();
        assert_eq!(res.status(), StatusCode::UNAUTHORIZED, "{scheme}");
    }
    let n: i64 = sqlx::query_scalar("SELECT count(*) FROM speedtests").fetch_one(&h.pool).await.unwrap();
    assert_eq!(n, 0, "an unauthenticated request reached the database");

    let r = h.send("POST", "/api/v1/results", Some("tok-phone"), &body).await;
    assert_eq!(r.status, StatusCode::CREATED, "{}", r.text());
    let got = r.json();
    assert_eq!(
        (got["device"].clone(), got["connectionType"].clone(), got["provider"].clone(), got["trigger"].clone()),
        (json!("phone"), json!("wireless"), json!("ookla"), json!("scheduled"))
    );

    // no tokens configured: everything is refused
    let none = h.with_tokens("", 0);
    assert_eq!(send(&none, "POST", "/api/v1/results", Some("anything"), &body).await.status, StatusCode::UNAUTHORIZED);
}

#[tokio::test]
async fn what_is_sent_is_what_is_stored() {
    let h = harness!();
    let r = h.send("POST", "/api/v1/results", Some("tok-laptop"), &envelope("ookla", "wired",
        r#"{"type":"result","timestamp":"2026-06-01T06:35:20Z","ping":{"jitter":0.5,"latency":12.4,"low":11,"high":13},
        "download":{"bandwidth":116027626,"bytes":1000,"elapsed":9000},"upload":{"bandwidth":14126828},"packetLoss":0.25,
        "isp":"Cox Communications","server":{"id":17360,"name":"CenturyLink","location":"Phoenix, AZ","country":"United States"},
        "result":{"id":"44d2fffb-8951-46aa-8d99-20e93b53ca7b","url":"https://www.speedtest.net/result/c/44d2fffb-8951-46aa-8d99-20e93b53ca7b"}}"#)).await;
    assert_eq!(r.status, StatusCode::CREATED, "{}", r.text());
    let created = r.json();
    // 116027626 bytes/s is 928.221008 Mbps
    assert!((created["downloadMbps"].as_f64().unwrap() - 928.221008).abs() < 1e-6);
    assert_eq!(created["server"], json!({"name": "CenturyLink", "location": "Phoenix, AZ", "country": "United States"}));
    assert_eq!(created["isp"], "Cox Communications");
    assert_eq!(created["packetLossPct"], 0.25);
    assert_eq!(created["testedAt"], "2026-06-01T06:35:20Z");
    // it is the same row the read side finds, with the payload kept exactly
    let id = created["id"].as_str().unwrap();
    let detail = h.get(&format!("/api/v1/results/{id}")).await.json();
    assert_eq!(detail["raw"]["server"]["name"], "CenturyLink");
    assert_eq!(detail["raw"]["download"]["bytes"], 1000);
    assert_eq!(h.get("/api/v1/devices").await.json()["items"][0]["name"], "laptop");

    // NDT7, and an explicit trigger
    let r = h
        .send(
            "POST",
            "/api/v1/results",
            Some("tok-laptop"),
            &format!(r#"{{"provider":"ndt7","connectionType":"wireless","trigger":"on_demand","result":{GOOD_NDT7}}}"#),
        )
        .await;
    assert_eq!(r.status, StatusCode::CREATED, "{}", r.text());
    let ndt = r.json();
    assert_eq!(
        (ndt["provider"].clone(), ndt["trigger"].clone(), ndt["downloadMbps"].clone()),
        (json!("ndt7"), json!("on_demand"), json!(87.5))
    );
}

/// Keys in other cases ("Timestamp"): the validators read them as Go does (ignoring case), the database function does not, so the
/// request is refused by the database as a 400. Same in both backends; the validator is more lenient than it needs to be.
#[tokio::test]
async fn keys_in_other_cases_are_refused_by_the_database() {
    let h = harness!();
    let body = r#"{"Provider":"ookla","CONNECTIONTYPE":"wired","Result":{"TYPE":"result","Timestamp":"2026-06-01T06:35:20Z","Download":{"Bandwidth":1},"UPLOAD":{"bandwidth":1}}}"#;
    let r = h.send("POST", "/api/v1/results", Some("tok-laptop"), body).await;
    assert_eq!(r.status, StatusCode::BAD_REQUEST, "{}", r.text());
    assert!(r.json()["detail"].as_str().unwrap().starts_with("the database rejected the result"));
}

#[tokio::test]
async fn accepted_variations() {
    let h = harness!();
    for (name, body) in [
        (
            "https url",
            envelope(
                "ookla",
                "wired",
                r#"{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1},"result":{"id":"44d2fffb-8951-46aa-8d99-20e93b53ca7b","url":"https://www.speedtest.net/result/c/1"}}"#,
            ),
        ),
        (
            "empty url",
            envelope(
                "ookla",
                "wired",
                r#"{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1},"result":{"url":""}}"#,
            ),
        ),
        (
            "ndt7 null floats and server strings",
            envelope(
                "ndt7",
                "wired",
                r#"{"startedAt":"2026-10-11T15:00:00Z","finishedAt":"2026-10-11T15:00:22Z","downloadMbps":1,"uploadMbps":1,"minRttMs":null,"retransmissionPct":null,"server":{"machine":"m","location":"l","country":"US"}}"#,
            ),
        ),
        (
            "an explicit empty trigger means scheduled",
            format!(r#"{{"provider":"ookla","connectionType":"wired","trigger":"","result":{GOOD_OOKLA}}}"#),
        ),
        ("whitespace around everything", format!("  \n {} \t", envelope("ookla", "wired", GOOD_OOKLA))),
    ] {
        let r = h.send("POST", "/api/v1/results", Some("tok-laptop"), &body).await;
        assert_eq!(r.status, StatusCode::CREATED, "{name}: {}", r.text());
    }
}

/// Each case must be refused with 400, and the response must name every listed field (it may name more).
#[tokio::test]
async fn validation() {
    let h = harness!();
    let ookla = |inner: &str| envelope("ookla", "wired", inner);
    let ndt7 = |inner: &str| envelope("ndt7", "wired", inner);
    let ok_head = r#""type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1}"#;
    let nd = r#""startedAt":"2026-10-11T15:00:00Z","finishedAt":"2026-10-11T15:00:22Z","downloadMbps":1,"uploadMbps":1"#;
    let cases: Vec<(&str, String, Vec<&str>)> = vec![
        ("not json", "hi".into(), vec![]),
        ("empty body", "".into(), vec![]),
        ("a JSON array", "[]".into(), vec![]),
        ("provider is a number", r#"{"provider":5,"connectionType":"wired","result":{}}"#.into(), vec![]),
        ("two documents", format!("{} {}", ookla(GOOD_OOKLA), ookla(GOOD_OOKLA)), vec![]),
        ("null body", "null".into(), vec!["provider", "connectionType"]),
        ("bad provider", envelope("speedof", "wired", GOOD_OOKLA), vec!["provider"]),
        ("bad connection type", envelope("ookla", "bluetooth", GOOD_OOKLA), vec!["connectionType"]),
        (
            "bad trigger",
            format!(r#"{{"provider":"ookla","connectionType":"wired","trigger":"weekly","result":{GOOD_OOKLA}}}"#),
            vec!["trigger"],
        ),
        (
            "trigger is null",
            format!(r#"{{"provider":"ookla","connectionType":"wired","trigger":null,"result":{GOOD_OOKLA}}}"#),
            vec!["trigger"],
        ),
        ("missing result", r#"{"provider":"ookla","connectionType":"wired"}"#.into(), vec!["result."]),
        ("result is a number", ookla("5"), vec!["result."]),
        ("result is an array", ookla("[]"), vec!["result."]),
        ("result is a string", ookla("\"x\""), vec!["result."]),
        (
            "ookla wrong type",
            ookla(r#"{"type":"log","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1}}"#),
            vec!["result.type"],
        ),
        (
            "ookla type is a number",
            ookla(r#"{"type":5,"timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1}}"#),
            vec!["result."],
        ),
        (
            "ookla download is a number",
            ookla(r#"{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":5,"upload":{"bandwidth":1}}"#),
            vec!["result."],
        ),
        (
            "ookla bandwidth is a string",
            ookla(r#"{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":"5"},"upload":{"bandwidth":1}}"#),
            vec!["result."],
        ),
        (
            "ookla no bandwidth",
            ookla(r#"{"type":"result","timestamp":"2026-06-01T06:35:20Z"}"#),
            vec!["result.download.bandwidth", "result.upload.bandwidth"],
        ),
        (
            "ookla negative",
            ookla(r#"{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":-1},"upload":{"bandwidth":1}}"#),
            vec!["result.download.bandwidth"],
        ),
        ("ookla loss > 100", ookla(&format!(r#"{{{ok_head},"packetLoss":101}}"#)), vec!["result.packetLoss"]),
        (
            "ookla bad time",
            ookla(r#"{"type":"result","timestamp":"yesterday","download":{"bandwidth":1},"upload":{"bandwidth":1}}"#),
            vec!["result.timestamp"],
        ),
        (
            "ookla timestamp null",
            ookla(r#"{"type":"result","timestamp":null,"download":{"bandwidth":1},"upload":{"bandwidth":1}}"#),
            vec!["result.timestamp"],
        ),
        (
            "ookla absurd bandwidth",
            ookla(r#"{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1e300},"upload":{"bandwidth":1}}"#),
            vec!["result.download.bandwidth"],
        ),
        (
            "ookla number out of range",
            ookla(r#"{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1e999},"upload":{"bandwidth":1}}"#),
            vec!["result."],
        ),
        (
            "ookla absurd latency",
            ookla(
                r#"{"type":"result","timestamp":"2026-06-01T06:35:20Z","ping":{"latency":1e12},"download":{"bandwidth":1},"upload":{"bandwidth":1}}"#,
            ),
            vec!["result.ping.latency"],
        ),
        (
            "ookla year 0336",
            ookla(r#"{"type":"result","timestamp":"0336-08-12T20:55:46Z","download":{"bandwidth":1},"upload":{"bandwidth":1}}"#),
            vec!["result.timestamp"],
        ),
        (
            "ookla far future",
            ookla(r#"{"type":"result","timestamp":"2999-01-01T00:00:00Z","download":{"bandwidth":1},"upload":{"bandwidth":1}}"#),
            vec!["result.timestamp"],
        ),
        (
            "ookla a leap second",
            ookla(r#"{"type":"result","timestamp":"2026-06-30T23:59:60Z","download":{"bandwidth":1},"upload":{"bandwidth":1}}"#),
            vec!["result.timestamp"],
        ),
        ("ookla result.id null", ookla(&format!(r#"{{{ok_head},"result":{{"id":null,"url":""}}}}"#)), vec!["result.result.id"]),
        ("ookla result.url number", ookla(&format!(r#"{{{ok_head},"result":{{"url":5}}}}"#)), vec!["result.result.url"]),
        ("ookla result is null", ookla(&format!(r#"{{{ok_head},"result":null}}"#)), vec!["result.result"]),
        ("ookla server is null", ookla(&format!(r#"{{{ok_head},"server":null}}"#)), vec!["result.server"]),
        ("ookla interface is a string", ookla(&format!(r#"{{{ok_head},"interface":"eth0"}}"#)), vec!["result.interface"]),
        ("ookla isp is a number", ookla(&format!(r#"{{{ok_head},"isp":5}}"#)), vec!["result.isp"]),
        ("ookla ping is null", ookla(&format!(r#"{{{ok_head},"ping":null}}"#)), vec!["result.ping"]),
        ("ookla packetLoss null", ookla(&format!(r#"{{{ok_head},"packetLoss":null}}"#)), vec!["result.packetLoss"]),
        ("ookla ping.latency null", ookla(&format!(r#"{{{ok_head},"ping":{{"latency":null}}}}"#)), vec!["result.ping.latency"]),
        ("ookla ping.jitter null", ookla(&format!(r#"{{{ok_head},"ping":{{"jitter":null}}}}"#)), vec!["result.ping.jitter"]),
        ("ookla url is junk", ookla(&format!(r#"{{{ok_head},"result":{{"url":"not a url"}}}}"#)), vec!["result.result.url"]),
        (
            "ookla url javascript scheme",
            ookla(&format!(r#"{{{ok_head},"result":{{"url":"javascript:alert(1)"}}}}"#)),
            vec!["result.result.url"],
        ),
        ("ookla url without slashes", ookla(&format!(r#"{{{ok_head},"result":{{"url":"http:example.com"}}}}"#)), vec!["result.result.url"]),
        (
            "ndt7 absurd speed",
            ndt7(r#"{"startedAt":"2026-10-11T15:00:00Z","finishedAt":"2026-10-11T15:00:22Z","downloadMbps":1e300,"uploadMbps":1}"#),
            vec!["result.downloadMbps"],
        ),
        ("ndt7 server.location is an object", ndt7(&format!(r#"{{{nd},"server":{{"location":{{}}}}}}"#)), vec!["result.server.location"]),
        ("ndt7 server is a number", ndt7(&format!(r#"{{{nd},"server":5}}"#)), vec!["result."]),
        ("ndt7 raw is not an object", ndt7(&format!(r#"{{{nd},"raw":[1]}}"#)), vec!["result.raw"]),
        ("ndt7 raw is null", ndt7(&format!(r#"{{{nd},"raw":null}}"#)), vec!["result.raw"]),
        ("ndt7 server is null", ndt7(&format!(r#"{{{nd},"server":null}}"#)), vec!["result.server"]),
        ("ndt7 server.country null", ndt7(&format!(r#"{{{nd},"server":{{"country":null}}}}"#)), vec!["result.server.country"]),
        (
            "ndt7 missing speeds",
            ndt7(r#"{"startedAt":"2026-10-11T15:00:00Z","finishedAt":"2026-10-11T15:00:22Z"}"#),
            vec!["result.downloadMbps", "result.uploadMbps"],
        ),
        (
            "ndt7 finished before started",
            ndt7(r#"{"startedAt":"2026-10-11T15:00:22Z","finishedAt":"2026-10-11T15:00:00Z","downloadMbps":1,"uploadMbps":1}"#),
            vec!["result.finishedAt"],
        ),
        ("ndt7 retransmission > 100", ndt7(&format!(r#"{{{nd},"retransmissionPct":150}}"#)), vec!["result.retransmissionPct"]),
        ("ndt7 an Ookla result", ndt7(GOOD_OOKLA), vec!["result.startedAt", "result.finishedAt"]),
        ("ookla an NDT7 result", ookla(GOOD_NDT7), vec!["result.type"]),
    ];
    for (name, body, want) in cases {
        let r = h.send("POST", "/api/v1/results", Some("tok-laptop"), &body).await;
        assert_eq!(r.status, StatusCode::BAD_REQUEST, "{name}: {}", r.text());
        let got = fields(&problem(&r));
        for f in &want {
            assert!(got.iter().any(|g| g == f), "{name}: no error for {f:?}; got {got:?}");
        }
    }
    let n: i64 = sqlx::query_scalar("SELECT count(*) FROM speedtests").fetch_one(&h.pool).await.unwrap();
    assert_eq!(n, 0, "a refused request was stored");
}

#[tokio::test]
async fn size_limit_and_the_database_having_the_last_word() {
    let h = harness!();
    let big = h.send("POST", "/api/v1/results", Some("tok-laptop"), &"x".repeat(64 * 1024 + 1)).await;
    assert_eq!(big.status, StatusCode::PAYLOAD_TOO_LARGE);
    problem(&big);
    // exactly 64 KiB is within the limit (it is then refused as not JSON, not as too large)
    let edge = h.send("POST", "/api/v1/results", Some("tok-laptop"), &"x".repeat(64 * 1024)).await;
    assert_eq!(edge.status, StatusCode::BAD_REQUEST);
    // the size check comes after the token check
    assert_eq!(h.send("POST", "/api/v1/results", None, &"x".repeat(64 * 1024 + 1)).await.status, StatusCode::UNAUTHORIZED);

    // an Ookla result id that is not a uuid passes the API's checks but not the database's cast: the caller's fault, so 400 not 500
    let bad_id = h.send("POST", "/api/v1/results", Some("tok-laptop"), &envelope("ookla", "wired",
        r#"{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1},"result":{"id":"x"}}"#)).await;
    assert_eq!(bad_id.status, StatusCode::BAD_REQUEST, "{}", bad_id.text());
    // likewise a NUL, which cannot be stored in text at all
    let r = h.send("POST", "/api/v1/results", Some("tok-laptop"), &envelope("ookla", "wired",
        r#"{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1},"isp":"a\u0000b"}"#)).await;
    assert_eq!(r.status, StatusCode::BAD_REQUEST, "{}", r.text());
    assert!(problem(&r)["detail"].as_str().unwrap().starts_with("the database rejected the result"));
}

#[tokio::test]
async fn rate_limit_per_device() {
    let h = harness!();
    let limited = h.with_tokens("laptop=tok-laptop,phone=tok-phone", 30);
    let body = envelope("ookla", "wired", GOOD_OOKLA);
    for i in 0..20 {
        assert_eq!(send(&limited, "POST", "/api/v1/results", Some("tok-laptop"), &body).await.status, StatusCode::CREATED, "burst {i}");
    }
    let r = send(&limited, "POST", "/api/v1/results", Some("tok-laptop"), &body).await;
    assert_eq!(r.status, StatusCode::TOO_MANY_REQUESTS);
    assert!(r.header("retry-after").parse::<u64>().unwrap() >= 1);
    problem(&r);
    // another device is not affected, and a refused request stored nothing
    assert_eq!(send(&limited, "POST", "/api/v1/results", Some("tok-phone"), &body).await.status, StatusCode::CREATED);
    let n: i64 = sqlx::query_scalar("SELECT count(*) FROM speedtests").fetch_one(&h.pool).await.unwrap();
    assert_eq!(n, 21);
    // an unauthenticated request is refused before it can use up anyone's allowance
    assert_eq!(send(&limited, "POST", "/api/v1/results", Some("wrong"), &body).await.status, StatusCode::UNAUTHORIZED);
    // 0 means unlimited
    let free = h.with_tokens("laptop=tok-laptop", 0);
    for _ in 0..25 {
        assert_eq!(send(&free, "POST", "/api/v1/results", Some("tok-laptop"), &body).await.status, StatusCode::CREATED);
    }
}

#[tokio::test]
async fn other_methods_on_the_results_path() {
    let h = harness!();
    for m in ["PUT", "DELETE", "PATCH"] {
        let r = h.send(m, "/api/v1/results", Some("tok-laptop"), "{}").await;
        assert_eq!(r.status, StatusCode::METHOD_NOT_ALLOWED, "{m}");
        assert!(r.header("allow").contains("POST") && r.header("allow").contains("GET"), "Allow: {}", r.header("allow"));
        problem(&r);
    }
}
