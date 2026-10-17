# backend-rust

The second implementation of the backend: Rust (axum + sqlx) instead of Go. Same contract (`docs/openapi.yaml`), same
PostgreSQL schema, same SQL. Either backend can sit behind either frontend.

## Status

| Part | State |
|---|---|
| Health, devices, results (paging by cursor), one result with its raw payload, summary, timeseries | **done**, same answers as Go |
| Serving the built frontend (`STATIC_DIR`) with the same cache rules | **done** |
| Ingest (`POST /api/v1/results`): per-device bearer tokens, rate limit, validation of Ookla and NDT7 results | **done**, same answers as Go |
| On-demand tests: Ookla on the server with a live event stream (replay and resume), cancel, time limit, one at a time; browser NDT7 tests with a single-use submit token | **done**, same answers as Go |
| ntfy alerts (slow, packet loss, gone quiet; reminders; recovery) and browser push (Web Push, VAPID) | **done**, same behaviour as Go; the monitor was written from the Go tests and rules, so its time-based flows are checked by `tests/notify.rs` rather than by a live side-by-side run |

## Run

```
DATABASE_URL='postgres://user:pass@host:5432/speedtestdb?sslmode=disable' \
TIMEZONE=America/Phoenix STATIC_DIR=../frontend-svelte/dist cargo run --release
```

Settings are the Go backend's, with the same names: `DATABASE_URL` (required), `LISTEN_ADDR` (default `:8080`), `TIMEZONE`,
`STATIC_DIR`, `API_TOKENS` (`device=token,device2=token2`; the token decides the device; empty refuses every ingest request) and
`INGEST_RATE_PER_MIN` (per device, default 30, burst 20, `0` = unlimited), and `DB_MAX_CONNS` (database connections, default 10; the Go backend uses 4). On-demand tests use the Go backend's
variables too: `OOKLA_ACCEPT_LICENSE` (exactly `yes`: your decision, see `docs/DEPLOY.md`), `OOKLA_PATH`, `DEVICE_NAME`, `SERVER_CONNECTION_TYPE`,
`ONDEMAND_TIMEOUT_SECONDS`, `BROWSER_TEST_TTL_MINUTES`, `ONDEMAND_PER_MINUTE`, `TRUST_PROXY`. Alerts are off until `NTFY_TOPIC` is set; the rest
(`NTFY_URL`, `NTFY_USER`/`NTFY_PASSWORD` or `NTFY_TOKEN`, `NTFY_SLOW_RATIO`, `NTFY_OUTLIERS`, `NTFY_SLOW_CONSECUTIVE`, `NTFY_RECOVER_RATIO`, `NTFY_LOSS_PCT`,
`NTFY_LOSS_CONSECUTIVE`, `NTFY_SILENCE_MIN_HOURS`, `NTFY_REMIND_HOURS`, `NTFY_BASELINE_DAYS`, `NTFY_MIN_SAMPLES`, `NTFY_CHECK_SECONDS`) are the Go
backend's. Browser push is on by default (`PUSH_ENABLED=no`, `PUSH_SUBJECT`, `PUSH_ALLOWED_HOSTS`). The optional dashboard login (`DASHBOARD_PASSWORD`, `DASHBOARD_SESSION_DAYS`; `src/login.rs`, `src/session_api.rs`) is the Go backend's, cookie for cookie: a session made by one is accepted by the other. `speedtest-backend notify-test` sends one
message to ntfy without a database. The schema is `sql/postgres/*.sql`, applied exactly as for the Go backend.

## Container

`Dockerfile` builds a small Alpine image with the same layout as the Go one (same `/app/server`, same Ookla download gate in
`docker/entrypoint.sh`). In `deploy/`: `docker compose -f docker-compose.yml -f docker-compose.rust-svelte.yml up -d --build`; see
`docs/DEPLOY.md` and, for how it compares, `docs/COMPARISON.md`.

## Test

```
scripts/scratch-db.sh up                      # a throwaway PostgreSQL with the real migrations; prints the URL
export TEST_DATABASE_URL='postgres://postgres:scratch@127.0.0.1:55432/speedtestdb?sslmode=disable'
cargo test                                    # unit tests, plus HTTP-level tests against that database
scripts/compare-backends.sh                   # Go and Rust side by side on one database, every answer compared
scripts/scratch-db.sh down
```

The database tests **empty the tables**, so, like the Go ones, they refuse any database that is not marked disposable
(`COMMENT ON DATABASE x IS 'speedtest-integration-test'`; `scratch-db.sh` does it) and take the same advisory lock, so the Go and Rust
suites can share one database without trampling each other. Without `TEST_DATABASE_URL` they skip themselves.

`scripts/compare-backends.sh` is the real check of "same contract" (it also runs the on-demand flows on each backend in turn with a
fake Ookla tool: validation, the browser token, a whole server run with its events and resume, cancel, failure; ids and times are left out): it starts both backends on one database and sends hundreds of
generated requests (valid filters, hostile and malformed ones, every interval and time zone, cursors from one backend used on the
other), then compares status, JSON (numbers to 1e-9) and the list of problem fields.

## What is the same, and what is not

- The SQL is the Go backend's, statement for statement, including `percentile_cont` in the database.
- Query parsing mirrors Go's `url.ParseQuery` (a pair with a bad `%` escape or a `;` is dropped, `+` is a space) and its validation
  rules, so both accept and refuse the same requests. Times are written the way Go writes them, so responses can be compared as text.
- A cursor from one backend is valid on the other.
- Ingest reads JSON the way Go's `encoding/json` does (`src/gojson.rs`): keys match ignoring case, the last duplicate wins, `null` is a
  zero value, one wrongly typed member rejects the whole payload. Without that the two would disagree about what is acceptable.
  Known small differences: a URL whose port is above 65535 is refused here (Go accepts it; the same goes for a push endpoint), and JSON nested deeper than 128 levels is
  refused here (Go allows far more).
- Not the same on purpose: error messages are worded independently (the contract fixes the status and the field names, not the text).

## Layout

`src/main.rs` startup and shutdown, `config.rs`, `app.rs` routes and handlers, `params.rs` query parsing and validation, `store.rs` SQL,
`model.rs` JSON shapes, `problem.rs` error responses, `static_files.rs`, `auth.rs` tokens and the rate limiter, `ingest.rs` validation of what
is sent, `gojson.rs` Go-compatible JSON reading, `store_tests.rs` the `tests` table, `ondemand/` the test manager (`config`, `hub` for the live
events, `ookla` for running the tool, `manager`), `ondemand_api.rs` the HTTP side of it (including the event stream), `notify/` the alert monitor (`rules`, `monitor`, `ntfy`, `sender`, `config`),
`push/` Web Push (`webpush` is the encryption and VAPID signing), `push_api.rs` its HTTP side, `store_notify.rs` and `store_push.rs` their SQL; `tests/` has the HTTP-level tests.
