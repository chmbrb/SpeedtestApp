# SpeedtestApp Go backend

Implements `docs/openapi.yaml` (phase 2: results, statistics, devices, ingest, health).
On-demand tests (`/api/v1/tests*`) come in phase 4 and are not served yet (they answer 404).

## Run
```
export DATABASE_URL='postgres://USER:PASS@HOST:5432/speedtestdb?sslmode=disable'
export API_TOKENS='laptop=<token>,phone=<token>'   # empty = ingest endpoints reject everything
export TIMEZONE=America/Phoenix                    # default zone for time-series buckets (default UTC)
go run ./cmd/server                                # listens on :8080 (LISTEN_ADDR to change)
```
Other settings: `INGEST_RATE_PER_MIN` (per-device POST rate, default 30, burst 20, `0` = unlimited, for tests; it also lifts the limits on the browser-notification endpoints).
Optional dashboard login: `DASHBOARD_PASSWORD` (8 to 200 characters; empty or unset means no login) and `DASHBOARD_SESSION_DAYS` (default 30, 1 to 365).
With a password every endpoint needs a session cookie except `/api/v1/health`, `/api/v1/auth/*`, the built frontend, and the endpoints that
have their own bearer token (`POST /api/v1/results`, `POST /api/v1/tests/{id}/result`, `POST /api/v1/tests` with a token); see `internal/login`
and `internal/api/session.go`, and `docs/DEPLOY.md`. It needs migration `009` (the signing key's table).
Needs migrations `001`-`005` from `sql/postgres/` applied. Quote the URL in single quotes in zsh if the password has `!`.

## On-demand tests
The dashboard's **Run a test** button starts a test now, with live progress. There are two kinds.

**Test the server (Ookla).** The server runs Ookla's command-line tool and streams its progress to the browser
(Server-Sent Events, `GET /api/v1/tests/{id}/events`). Only one server test runs at a time; a second request is told so, and the
dashboard follows the one in progress instead. A test can be stopped, and one a crash left behind is cleared on restart.

> **Ookla's terms.** The Ookla CLI is licensed for personal, non-commercial use, and whoever runs this server must accept Ookla's
> terms ([EULA](https://www.speedtest.net/about/eula)) themselves. Nothing here accepts them for you: Ookla tests stay **off**
> until you read the terms and set `OOKLA_ACCEPT_LICENSE=yes`. Only then does the server pass `--accept-license --accept-gdpr`
> to the tool. Without it the dashboard says why and offers the browser test only.

**Test this device (M-Lab NDT7).** The visitor's own browser runs the test (so it measures *that* device's connection, which is how
you test a phone on Wi-Fi) and submits the result with a single-use token the server issued when the test started. Before it runs,
the dashboard shows M-Lab's data policy and requires an explicit acceptance: **M-Lab publishes results, including the IP address, as
open data.** The server checks the token, the plausibility of the numbers and a time limit, and records the address it saw.

| Variable | Default | Meaning |
|---|---|---|
| `OOKLA_ACCEPT_LICENSE` | *(off)* | exactly `yes` turns on Ookla tests (see above) |
| `OOKLA_PATH` | `speedtest` | the Ookla CLI |
| `DEVICE_NAME` / `SERVER_CONNECTION_TYPE` | `server` / `wired` | what the server's own tests are stored as |
| `ONDEMAND_TIMEOUT_SECONDS` | `180` | longest a server test may run |
| `BROWSER_TEST_TTL_MINUTES` | `15` | how long a browser test may stay open before it expires |
| `ONDEMAND_PER_MINUTE` | `6` | tests one address may start per minute |
| `TRUST_PROXY` | `0` | `1` reads the client address from `X-Forwarded-For`; only behind your own reverse proxy |

Starting a test needs no login (the dashboard has none): it is limited per address and to one server test at a time. Put the app
behind your own access control if it is reachable by people you do not trust. A reverse proxy must not buffer the event stream
(`X-Accel-Buffering: no` is already sent for nginx).

## Notifications (ntfy)
The server can tell you on your phone when something is wrong, through [ntfy](https://ntfy.sh). It is off until you set
`NTFY_TOPIC`.

**Set up**
1. Install the ntfy app on your phone and subscribe to a topic. **On the public ntfy.sh server a topic name works like a
   password: anyone who knows it can read and send to it.** Pick a long random one (`openssl rand -hex 12`), or use your own
   ntfy server or an access token for stronger protection.
2. Apply `sql/postgres/006_notifications.sql` (the server refuses to start with notifications on until you do).
3. Set the environment, then restart the server:
   ```
   NTFY_TOPIC=your-long-random-topic
   NTFY_URL=https://ntfy.sh            # your own server if you run one
   NTFY_TOKEN=tk_...                   # or NTFY_USER + NTFY_PASSWORD, for a protected topic (optional)
   APP_URL=http://your-server:8080     # tapping a notification opens the dashboard (optional)
   ```
4. Check it works without waiting for a problem: `go run ./cmd/server notify-test` (in Docker: `docker compose run backend notify-test`).
   It sends one message and says whether it was delivered. Quiet hours and sounds are set in the ntfy app.

**When you are notified** (the dashboard's Notifications section shows the same sentences with your numbers)
Only scheduled tests are judged, and each one only against the history of the same device, connection type and provider: the
server against the server, `laptop_1` against `laptop_1`. On-demand tests (the dashboard's "Run a test") never notify and do not
count towards anyone's "typical".
- **Slow downloads:** below 70% of a device's own typical speed (the median of its scheduled tests over the last 30 days) for
  `NTFY_OUTLIERS` tests in a row (1 unless set). It ends when the speed is back above 90%.
- **Packet loss:** 5% or more for `NTFY_OUTLIERS` tests in a row.
- **Gone quiet:** a device stops running its scheduled tests: nothing for three times its usual interval, and never sooner
  than 3 hours. A server that tests every 2 hours is silent after 6; a laptop that tests every 8 hours after 24. Devices that are
  only tested by hand are never "silent", and a device with no scheduled test for two weeks is treated as retired.
- **Recovered:** a quiet message when it clears. **Reminders:** every 12 hours while it lasts, for up to a week.

The check runs every minute and reads new results from the database, so it also sees results written directly by the
collector, not only ones sent through the API. Alert state is stored in the database, so a restart never repeats a message.
Turning notifications on never alerts about old results. If delivery fails the alert is retried on the next check.

| Variable | Default | Meaning |
|---|---|---|
| `NTFY_SLOW_RATIO` | `0.7` | slow below this fraction of typical |
| `NTFY_RECOVER_RATIO` | `0.9` | back above this ends the alert (must be higher than the slow ratio) |
| `NTFY_OUTLIERS` | `1` | outlier tests in a row (slow or lossy) before alerting |
| `NTFY_SLOW_CONSECUTIVE` | `NTFY_OUTLIERS` | slow tests in a row before alerting, if it should differ from `NTFY_OUTLIERS` |
| `NTFY_LOSS_PCT` / `NTFY_LOSS_CONSECUTIVE` | `5` / `NTFY_OUTLIERS` | packet loss threshold (percent) and tests in a row |
| `NTFY_SILENCE_MIN_HOURS` | `3` | never call a device silent sooner than this |
| `NTFY_REMIND_HOURS` | `12` | `0` turns reminders off |
| `NTFY_BASELINE_DAYS` / `NTFY_MIN_SAMPLES` | `30` / `10` | history that defines "typical", and tests needed before judging |
| `NTFY_CHECK_SECONDS` | `60` | how often to check (10 or more) |

Invalid values stop the server with a message naming every problem. The topic and credentials never appear in the API, the
dashboard or the logs.

## Browser notifications (Web Push)

The same alerts that go to ntfy can also reach browsers and installed apps. A browser subscribes itself from the dashboard; the server
then sends each alert through the browser maker's push service (RFC 8030, encrypted per RFC 8291, signed with the server's VAPID key).
It runs on the alert monitor, so `NTFY_TOPIC` must be set; with it off, a device can only receive the test message.

| Variable | Default | Meaning |
|---|---|---|
| `PUSH_ENABLED` | on | `no` turns it off (the endpoints then answer 503) |
| `PUSH_SUBJECT` | `APP_URL` if https | who the server says it is to push services: an https URL or an email address |
| `PUSH_ALLOWED_HOSTS` | *(built-in)* | extra push-service hosts to accept; a leading dot also matches subdomains |

Needs `sql/postgres/007_web_push.sql`. The server creates its VAPID key pair on first start and keeps it in the database (`push_vapid`);
subscriptions are in `push_subscriptions`. Endpoints: `GET /api/v1/push/config`, `POST` and `DELETE /api/v1/push/subscriptions`,
`POST /api/v1/push/test` (see `docs/openapi.yaml`). Things worth knowing:

- A subscription's endpoint is an address the **server will POST to**, so it must be https and on the built-in list of browser push
  services (Chrome/Edge, Firefox, Safari, Windows); anything else is a 400. Redirects are never followed.
- Endpoints are secrets and are never returned by the API or written to logs.
- Subscriptions are removed when the push service answers 404/410, or after five failed deliveries in a row. At most 50 exist.
- An alert counts as delivered if any channel (ntfy or push) delivered it, so a working channel isn't repeated because another is down.

## Test
```
go test ./...                                      # unit tests, no database needed
```
Integration tests need a **disposable** database. They `TRUNCATE` tables, so they refuse to run unless the
database carries an explicit marker (a name pattern is not a safe guard). They take an advisory lock, so `go test ./...` runs the
database packages one after another instead of wiping each other's data:
```
createdb speedtest_scratch
psql speedtest_scratch -f ../sql/postgres/001_create_speedtests.sql -f ../sql/postgres/004_add_device_connection_type.sql -f ../sql/postgres/005_providers_and_tests.sql -f ../sql/postgres/006_notifications.sql
psql -c "COMMENT ON DATABASE speedtest_scratch IS 'speedtest-integration-test'"
TEST_DATABASE_URL='postgres://USER:PASS@HOST/speedtest_scratch?sslmode=disable' go test -race ./...
```

## Check the server against the contract
Schemathesis generates requests from the OpenAPI file and checks every response against it. Run it against a
**copy** of the data (it posts junk) with the rate limit off:
```
INGEST_RATE_PER_MIN=0 DATABASE_URL=<copy> API_TOKENS=t=tok go run ./cmd/server &
pip install schemathesis
st run ../docs/openapi.yaml --url http://127.0.0.1:8080 -H 'Authorization: Bearer tok' \
  --include-path-regex '^/api/(v1/(health|devices|results|stats)|speedtests)' --exclude-checks positive_data_acceptance
```
`use_after_free` is excluded too: `DELETE /tests/{id}` cancels a test but keeps it in the history (state `cancelled`), so a later `GET` is
correctly 200, not 404. `positive_data_acceptance` is excluded because some valid-looking values are rejected on purpose (a `from` after `to`,
an unknown time zone, a cursor this API did not issue); those are documented 400s that a schema cannot express.

## Layout
`cmd/server` entry point · `internal/config` env settings · `internal/store` SQL (aggregation happens here) ·
`internal/api` handlers, validation, auth, rate limit · `internal/model` JSON shapes.
