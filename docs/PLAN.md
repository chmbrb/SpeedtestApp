# SpeedtestApp: Plan

Source of truth for where this project is going. Written 2026-10-11 from the owner's
description of earlier planning; edit it as decisions change.

## Goals
1. A feature-rich dashboard: graphs, filtering (date range, provider, device, connection type,
   granularity), summary statistics, results table.
2. Two test providers: **Ookla** (CLI) and **M-Lab NDT7**.
3. **On-demand tests with real-time feedback** (live gauge and graph while a test runs).
4. Test **wireless devices**, not just the wired server.
5. Rewrite in two rounds on one shared API contract and one Postgres schema:
   - Round 1: **Go** backend + **React** frontend.
   - Round 2: **Rust** backend + **Svelte** frontend.

## Current state (done)
- SQL Server -> PostgreSQL migration: `sql/postgres/001`-`004` (normalized columns, UUID ids,
  `raw_data jsonb`, `device`, `connection_type`, `speedtests_insert()`, `speedtest_results()`).
- Python/Flask app (`flaskapp/`): tables only, pandas resampling. Reference behavior to port,
  deleted once the Go version has parity.
- Collector (`ops/`): Ookla CLI -> DB, or -> `POST /api/speedtests` with a per-device bearer token.
- Real data: ~1,500 wired Ookla results since 2026-06-01.

## Architecture
```
 browser (React, later Svelte) --HTTP/JSON, SSE--> backend (Go, later Rust) --> PostgreSQL
 wireless device / collector ----POST /api/v1/results (bearer)---^
 browser NDT7 test ------------ POST /api/v1/tests/{id}/result (single-use token)
```
- `docs/openapi.yaml` is the contract. Both backends implement it, both frontends consume it.
  Swapping Go for Rust (or React for Svelte) must not change the contract.
- Aggregation happens in SQL (`date_trunc`, `percentile_cont`), not in application code. At this
  data size that matters more for speed than the backend language.
- The backend serves the built frontend files, so one container runs the whole app.

## Deployment (target)
- `docker-compose` with separate containers: **backend** (Go, later Rust) and **frontend** (static build behind a web
  server that also proxies `/api` to the backend), plus PostgreSQL. One `docker compose up` for the whole stack.
- The Go backend can also serve the built frontend itself (`STATIC_DIR`), which stays useful for local use and tests;
  the compose setup uses the separate frontend container.
- **Ookla licence gate.** The Ookla CLI is licensed for personal, non-commercial use and the end user must accept
  Ookla's terms (EULA, terms of use, privacy policy) before using it. Consequences for the design:
  - Never bake the CLI into a published image or accept the licence on the user's behalf. The container fetches the
    CLI itself and runs `speedtest --accept-license --accept-gdpr` only after an explicit opt-in
    (e.g. `OOKLA_ACCEPT_LICENSE=yes` in the compose `.env`, or an acceptance step in the UI that records who accepted
    and when). Without acceptance the Ookla provider stays disabled and the app works with NDT7 only.
  - Show the terms link next to the opt-in, and say plainly that accepting is the user's decision.
  - NDT7 has no such gate (but its results are published by M-Lab; see Providers).

## Providers
| | Ookla | M-Lab NDT7 |
|---|---|---|
| Where it runs | CLI on the server or a device | In the browser (also usable as a Go/CLI agent) |
| Real-time progress | `speedtest --format=jsonl` emits progress events; backend relays over SSE | Browser client reports live measurements directly |
| Connections | Multiple parallel | Single TCP stream (reads lower than Ookla) |
| Trust | Backend ran it | Browser-reported: label it, rate-limit it |

Caveats to keep visible in the UI and docs:
- **Do not mix providers in one series.** NDT7 and Ookla are not comparable; charts split by provider.
- **M-Lab publishes results**, including the client IP, in its open dataset. Say so next to the NDT7 button.
- **Ookla CLI licence** is personal/non-commercial use.
- Rust has no official NDT7 client; in round 2 NDT7 stays browser-side (no server work needed).

## Schema changes (migration `005`, next)
- `provider text not null default 'ookla'` (`ookla`|`ndt7`), `trigger text not null default 'scheduled'`
  (`scheduled`|`on_demand`).
- Provider-neutral columns, backfilled for existing rows: `download_mbps`, `upload_mbps`, `latency_ms`,
  `jitter_ms`, `packet_loss_pct` (Ookla's `bandwidth / 125000` conversion stays the definition of Mbps).
  Provider-specific detail stays in the existing Ookla columns and `raw_data`.
- `tests` table for on-demand runs: id, provider, mode (`server`|`browser`), device, connection_type,
  state (`queued`|`running`|`completed`|`failed`|`cancelled`), timestamps, `result_id`, `error`,
  hashed single-use submit token.
- Indexes for the dashboard filters (`provider`, `device`, `tested_at`).

## Phases
- [x] 0. Postgres migration, device/connection_type, ingest endpoint (Python)
- [x] 1a. **API contract** (`docs/openapi.yaml`, lints clean with `npx @redocly/cli lint docs/openapi.yaml`)
- [x] 1b. Migration `005` (provider, trigger, neutral columns, `tests` table, `speedtests_insert_ndt7()`)
- [x] 2. Go backend (`backend-go/`): read APIs (results, summary, timeseries, devices), ingest, health; SQL aggregation. Tested: unit + integration + Schemathesis against the contract
- [x] 3. React dashboard (`frontend-react/`): hero verdict, KPIs with period deltas, four charts, filters in the URL, results table, dark default. Tested: unit + 18 real-browser tests (axe WCAG 2.1 AA, keyboard, phone layout)
- [x] 3b-i. PWA: manifest, icons, service worker, opens offline with the last data seen (tested)
- [x] 3b-iii. PWA install prompt, **Web Push** notifications (VAPID keys in the database, migration 007, allow-listed push services, fan-out with ntfy), and HTTPS for a private network with no domain (Caddy with its own certificate authority). Tested: unit and DB integration, fuzzing, encrypted round trip to a fake push service, 13 browser tests (new) plus the whole suite on the real HTTPS container stack
- [x] 3b-ii. **ntfy** notifications (`backend-go/internal/notify`, migration 006): slow downloads, packet loss, devices gone quiet, recovery and reminders; dashboard banner and status section; `notify-test` command. Configured by environment variables on purpose (the topic works like a password, so no settings API). Tested: unit, DB integration with a fake clock and fake ntfy, live end to end, browser
- [x] 4. On-demand tests: server-run Ookla with live SSE feedback (gated on accepting Ookla's terms), browser NDT7 with a single-use submit token and an explicit M-Lab data-policy consent; "Run a test" dialog with live chart. Tested: unit, DB integration, SSE over HTTP, fake and real Ookla output, real M-Lab client against a local stand-in
- [x] 4b. Docker: `deploy/` compose stack (db, migrate, backend, frontend/nginx, private ntfy), `setup.sh`, Ookla download gated on `OOKLA_ACCEPT_LICENSE=yes` and checksum-pinned. Tested on real containers (rootless Podman via the Docker API)
- [x] 5. Go collector (`collector-go/`): single static binary, Ookla + NDT7, store-and-forward spool, daemon or cron, per-device token, exit codes. Tested: unit (race), real backend + Postgres, real Ookla run, offline queue and flush, wrong token. Guide: `docs/COLLECTOR.md`
- [x] 6. Retired the Python app (Flask app, Python collector, `run.sh`, Pipfile) and the legacy `POST /api/speedtests` endpoint; contract, generated types and tests updated
- [x] 7. Round 2: Rust backend and Svelte frontend against the same contract; compare. Staged:
  - [x] 7a. Rust backend (`backend-rust/`, axum + sqlx): read side (health, devices, results, summary, timeseries, static files) with the
        same SQL and validation as Go; endpoints for features that come later answer as the contract's "off" states. Tested: unit,
        HTTP-level against a real database, Schemathesis, and a differential run of Go and Rust on one database (`scripts/compare-backends.sh`),
        which also found and fixed three bugs in the Go backend: a cursor whose id was 36 characters of anything answered 500, and an empty `cursor=` or `interval=` was accepted although the contract forbids it
  - [x] 7b. Svelte frontend (`frontend-svelte/`, Svelte 5 + Vite) with parity to the React app. Both apps share `frontend-shared/` (logic, styles, service worker) and pass the same 49 browser tests
  - [x] 7c. Rust: ingest, auth, rate limits, validation. Tested: unit, HTTP-level against a real database (the Go case list ported), Schemathesis, and the Go-vs-Rust comparison extended with generated and mutated payloads
  - [x] 7d. Rust: on-demand tests and the live event stream. Tested: unit, HTTP-level (Go case list ported, including SSE replay/resume and a real runner), a 59-step Go-vs-Rust transcript of on-demand flows with a fake Ookla CLI, Schemathesis on both backends (found two contract wording issues, fixed in `openapi.yaml`)
  - [x] 7e. Rust: notifications (ntfy) and Web Push. The monitor (slow / packet loss / gone quiet, reminders, recovery) is ported from the Go behaviour spec (its tests, README, rules and store), then checked by the same scenarios (`tests/notify.rs`, fake clock and sender, real database); Web Push (RFC 8291 encryption, RFC 8292 VAPID, endpoint allow-list, limits, expiry) has a fake browser that decrypts and verifies what is sent (`tests/push.rs`). The comparison script also covers the push endpoints and the notifications status
  - [x] 7f. Compose option for Rust + Svelte (`deploy/docker-compose.rust-svelte.yml`, `backend-rust/Dockerfile`, `frontend-svelte/Dockerfile`; tested on real containers: healthy stack, notify-test through the private ntfy, 38 browser tests through nginx), benchmarks (`scripts/bench-backends.sh`) and the write-up `docs/COMPARISON.md`

## Assumptions (change if wrong)
- Everything lives in this repo: `backend-go/`, `frontend-react/`, later `backend-rust/`, `frontend-svelte/`.
- NDT7 is browser-only at first; no scheduled server-side NDT7.
- Dashboard reads are open on the LAN. Starting a server-side on-demand test is rate-limited and allows one
  test at a time (409 otherwise); optionally require a token. Revisit before exposing beyond the LAN.
- One on-demand test at a time, and none while a scheduled run is active, so tests don't skew each other.

## Open questions
- Should starting an on-demand test always require a token?
- Charts library for React (candidates: Recharts, uPlot for large series)?
