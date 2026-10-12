# frontend-shared

The code the React and Svelte frontends have in common, in one place. Neither app imports from here directly: each builds from its own
folder (a Docker build sees nothing outside it), so `scripts/sync-shared.sh` copies this into the apps.

| Here | Copied to | What |
|---|---|---|
| `src/` | `<app>/src/shared/` | API client and generated types (`src/api`), formatting, statistics, colours, alert wording, NDT7 summary, platform and push helpers, chart series (`src/lib`), filters, live-test and install state (`src/state`), the service worker (`src/sw.ts`), the stylesheet, and their unit tests (`src/test`) |
| `e2e/` | `<app>/e2e/` | the Playwright browser tests, and the data and fake tools they use |
| `public/` | `<app>/public/` | icons and the theme script |

```
scripts/sync-shared.sh           # after editing anything here
scripts/sync-shared.sh --check   # fails if a copy has drifted (run it before committing)
```

Never edit the copies: they are overwritten. Regenerate the API types after changing `docs/openapi.yaml` with
`cd frontend-react && npm run gen:api` (it writes here and syncs).

The rule for what belongs here: no UI framework in it. A thing that needs React hooks or Svelte runes is written once per app.
