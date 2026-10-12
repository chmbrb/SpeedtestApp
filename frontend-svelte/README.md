# frontend-svelte

The second implementation of the dashboard: Svelte 5 + Vite, no SvelteKit, instead of React. It looks and behaves the same, and
**passes the very same browser tests** (`frontend-shared/e2e`, 49 of them: filters, charts, keyboard use, WCAG checks in both themes,
the installable PWA, offline, on-demand tests, push notifications).

```
npm install
npm run dev                  # http://localhost:5173, proxies /api to BACKEND_URL (default http://127.0.0.1:8080)
npm test                     # unit tests (the shared ones, run against this app's copy)
npm run build                # type-checks, then builds dist/
E2E_BASE_URL=http://127.0.0.1:8083 npm run e2e     # see ../frontend-react/README.md for the stack the browser tests need
```

## What is shared with the React app

Everything that is not a view: the API client and generated types, formatting, statistics, filters and their URL form, colours, alert
wording, the NDT7 summary, install and push logic, the service worker, the stylesheet and icons, and the browser tests. They live in
`../frontend-shared` and are copied into `src/shared/`, `e2e/` and `public/` by `scripts/sync-shared.sh` (each app builds from its own
folder, so Docker needs nothing outside it). **Edit `frontend-shared/`, never the copies**; `scripts/sync-shared.sh --check` fails if
they differ.

What is Svelte-specific:

| File | What it does |
|---|---|
| `src/lib/query.svelte.ts` | polling queries: keep the old data while new data loads, refresh on a timer and when the tab returns, one retry, `invalidate()`, paging |
| `src/lib/data.svelte.ts` | the questions the dashboard asks (same ones as the React hooks) |
| `src/lib/{filters,theme,clock,install}.svelte.ts` | state: filters in the URL, theme, the minute clock, the install prompt |
| `src/lib/push.svelte.ts`, `runTest.svelte.ts` | notifications for this device; running a test with live progress |
| `src/components/*.svelte` | the views |

Svelte quirk worth knowing: a variable named `state` in a component turns `$state` into a store subscription and breaks the build;
the components call theirs `st`/`ps`.
