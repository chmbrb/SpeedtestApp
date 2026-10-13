# SpeedtestApp dashboard (React)

Dark-first dashboard and installable PWA for the speed-test history. It only talks to the HTTP API in
`../docs/openapi.yaml`; the API types in `src/api/schema.d.ts` are generated from that file, so a contract change
that breaks the UI fails the type check.

## Run
```
npm install
npm run dev              # http://localhost:5173, proxies /api to the Go backend on :8080 (BACKEND_URL to change)
npm run build            # type-checks, then builds to dist/
```
To serve the build from the Go backend: `STATIC_DIR=../frontend-react/dist go run ./cmd/server` (from `backend-go/`).

After a contract change: `npm run gen:api`. After editing `public/icon.svg`: `npm run gen:icons` (needs ImageMagick).

## Test
```
npm test                                  # unit tests (URL state, colour assignment, verdict rules, formatting)
E2E_BASE_URL=http://127.0.0.1:8083 npm run e2e   # real-browser tests against a running stack
```
The e2e tests need the backend serving `dist/` on a **disposable copy** of your data that also has wired and wireless devices:
run `e2e/seed.sql` once on that copy (it is idempotent, and the suite tells you if the data is missing). First run:
`npx playwright install chromium`.
For the on-demand tests, start the backend with the fake Ookla tool (it prints the real tool's `jsonl` shape in about 7 seconds and
never touches a network): `OOKLA_ACCEPT_LICENSE=yes OOKLA_PATH=$PWD/frontend-shared/e2e/fake-speedtest.sh`. Those tests skip
themselves with that instruction when Ookla is off. The NDT7 tests run the real M-Lab client library against a local stand-in for
M-Lab (`e2e/helpers/mlab-mock.ts`), so they need no internet.
To test behind the dashboard login, start the backend with `DASHBOARD_PASSWORD=<password>` (and `INGEST_RATE_PER_MIN=0`, or the limit on
sign-in attempts stops the tests from signing in so often) and run `E2E_PASSWORD=<password> E2E_API_TOKEN=<one of its API_TOKENS> npm run e2e`:
it signs in once, runs the whole suite signed in, and adds `e2e/login.spec.ts` (wrong and right passwords, sign out, a session that ends,
the offline copy being emptied, a phone screen). Without `E2E_PASSWORD` that spec is left out and the server is expected to be open.
They cover filters and URL state, chart hover and keyboard use, the table view, theme persistence, a phone-width layout,
WCAG 2.1 AA checks (axe) in both themes, the manifest and service worker, opening offline, and the notification banner and
status section (with the API mocked, so every state can be shown).

## Design notes
- **Dark by default** (stored choice: dark / light / auto). The interface chrome is monochrome so colour always means data.
- **Charts** are plain SVG (d3 scales/shapes): median line, shaded slowest-to-fastest band, crosshair that snaps to the nearest
  bucket, one tooltip for every series, keyboard (arrow keys / Home / End), a legend for two or more series, and a table view of
  the same numbers. Colours are the validated categorical set; a series keeps its colour no matter which filters are on.
- **The hero** judges the latest test against that device's own typical speed (median over the selected period), in words.
- **Ookla and NDT7 are not comparable**, so one provider is shown by default; "Both" adds a note.
- **Run a test**: a dialog with a live number, a latency/download/upload stepper and a speed chart, fed by the server's event stream
  (Ookla) or by M-Lab's client library running in the browser (NDT7, after the person accepts M-Lab's data policy). A test already
  running on the server is followed, not duplicated. The test endpoints are never cached by the service worker.
- **PWA**: the app shell is precached; `/api/v1/*` is network-first with a 5 s timeout and falls back to the last data seen on
  the device, with an "offline" banner.
- **Filters live in the URL** (`?range=7d&device=laptop&connection=wireless&provider=ookla&interval=hour`), so any view can be
  bookmarked and Back/Forward work.

## Shared code

Everything that is not React (API client and types, formatting, statistics, filters, colours, alert wording, install and push logic,
the service worker, the stylesheet, icons, and the browser tests) is in `../frontend-shared` and copied into `src/shared/`, `e2e/` and
`public/` by `../scripts/sync-shared.sh`, because the Svelte app (`../frontend-svelte`) uses the same code and passes the same tests.
**Edit `frontend-shared/`, never the copies.** `../scripts/sync-shared.sh --check` fails if they differ.
