# Go + React or Rust + Svelte?

Both versions implement the same contract (`docs/openapi.yaml`) on the same database, pass the same browser tests (49 Playwright tests, run
against each frontend on each backend), and were checked against each other request by request (`scripts/compare-backends.sh`). This is
what the second round measured. Everything here can be reproduced; the commands are at the end.

**Short version:** for this app the choice does not change what you experience. Almost every request spends its time in PostgreSQL, and
there the two backends are level. Rust wins on memory at rest and image size, and the Svelte frontend ships about half the JavaScript.
Go wins on build time and on how much code you have to trust. Choose on what you want to maintain.

## Backend: Go vs Rust

Measured on one 4-core machine, each backend alone against the same freshly loaded and vacuumed table of 200,000 results, 16 requests in
flight (8 for sending), median of 3 rounds after a warm-up. The load generator (`ab`, and a small Node script for sending) shares the
machine with the server.

| Request | Go: req/s, p50, p99 (ms) | Rust: req/s, p50, p99 (ms) |
|---|---|---|
| health | 8076, 2, 4 | 6662, 2, 5 |
| devices | 11, 1400, 2164 | 11, 1394, 2444 |
| results page (50) | 2540, 6, 10 | 2692, 6, 9 |
| results filtered | 2206, 7, 11 | 2325, 7, 10 |
| summary (30 days) | 27, 570, 754 | 40, 408, 616 |
| timeseries (hourly, 30 days) | 8, 1983, 2393 | 9, 1545, 2347 |
| send a result (POST) | 1057, 6.9, 16.3 | 1131, 6.4, 14.6 |

| | Go | Rust |
|---|---|---|
| time to first healthy answer | 41 ms | 40 ms |
| memory at rest (RSS) | 14 MiB | 8 MiB |
| memory after the run (peak RSS) | 74 MiB | 75 MiB |
| binary (stripped) | 12.2 MB | 7.3 MB |
| container image | 24.8 MB | 19.9 MB |
| cold image build | 44 s | 237 s |
| rebuild after changing one file | 20 s | 85 s |
| source, without tests | 4,858 lines | 5,499 lines |
| tests | 4,402 lines, 119 test functions | 4,056 lines, 151 test functions |
| libraries | 3 direct (9 with their own dependencies) | 27 direct (293 crates in the lock file) |

How to read it:

- **The database is the bottleneck.** `devices`, `summary` and `timeseries` run the same SQL in both and are limited by PostgreSQL. The
  differences on the cheap endpoints are 5 to 20 percent, in both directions, which is about the noise of this setup. Go answers the
  bare `health` check faster; Rust is a little faster on the results pages, on sending, and on `summary` and `timeseries`, where I did
  not find out why.
- **Memory:** Rust idles at about half of Go's footprint. Under load both end up at about 75 MiB, because most of that is database
  connections and response buffers, not the language.
- **Size of the work:** Rust needed about 13 percent more non-test code, mostly because it has no standard library equivalents for
  Go's HTTP/JSON conventions: reading JSON "the way Go does" is its own module (`gojson.rs`), and Web Push encryption is assembled from
  small cryptography crates. Go also leans on its standard library, hence 3 libraries instead of 27 (293 with everything they bring).
  Fewer moving parts is a real maintenance advantage.
- **Builds:** Rust compiles from scratch in 4 minutes and takes about 1.5 minutes after a change, against well under a minute for Go.
  On a home server that rebuilds rarely this is a minor cost; if you iterate a lot, it adds up.

## Frontend: React vs Svelte

Both apps share one directory of logic, styles, service worker and tests (`frontend-shared/`, about 3,100 lines); what differs is the
component layer.

| | React | Svelte |
|---|---|---|
| component and state code | 1,890 lines | 1,974 lines |
| JavaScript sent to the browser (gzip) | 119 KB | 64 KB |
| CSS (gzip) | 4.4 KB | 4.4 KB |
| runtime libraries | 9 | 7 |
| container image | 64.1 MB | 63.9 MB |
| cold image build | 22 s | 21 s |
| browser tests passed | 49 of 49 | 49 of 49 |

The Svelte app sends about half the JavaScript for the same screens, which is the one clear, user-visible difference between the two
versions (faster first load on a phone). The image is the same size because it is mostly the nginx base image. Writing the second
frontend was easy precisely because the shared layer existed: the component code is the same size either way.

## What the comparison found besides numbers

Building the same thing twice and diffing the answers is a strong test. It found three bugs in the Go backend (a malformed cursor
answered 500; an empty `cursor=` or `interval=` was accepted although the contract forbids it), one in the Rust one (a control
character in a push endpoint caused a 500), and several places where `docs/openapi.yaml` said less than the code did. All are fixed.
Known remaining differences are listed in `backend-rust/README.md` (a push endpoint or URL with a port above 65535, and JSON nested
deeper than 128 levels).

Two findings are about the app, not the language, and apply to both versions at 200,000 results:

- **`/api/v1/devices` takes about 90 ms on its own** (it reads the whole table to find each device's latest test). The dashboard calls it
  on load. An index or a small summary table would fix it for both backends.
- **A 30-day hourly `timeseries` response is about 1 MB.** It is fast to produce but heavy to send; the dashboard would do as well with a
  coarser interval for long ranges.

## How reliable are these numbers?

One machine, one run of the whole suite, `ab` as the load tool, a database on the same host. Treat differences under about 15 percent as
noise. My first run showed Rust 2.5 times slower on one query; that turned out to be my method (Rust ran after Go's inserts had
dirtied the table), not the language, and the script now reloads and vacuums the table before each backend. Per-request logging in the
Rust backend and the size of its connection pool (10, against Go's 4) were also tested and made no measurable difference.
The pool size can be set with `DB_MAX_CONNS`.

## Which one to run

- Want the least to maintain, the shortest builds and the fewest dependencies: **Go + React** is already done and nothing about the
  numbers argues against it.
- Want the smallest memory footprint and image, and a lighter page for phones: **Rust + Svelte**. Neither backend is faster for you in
  practice, but the Svelte bundle is.
- A mixed stack is fine too: any backend works behind any frontend, because they share the contract. For most people the interesting
  combination is **Go + Svelte** (small page, small build, few dependencies).

## Reproduce it

```
scripts/scratch-db.sh up
scripts/bench-backends.sh 200000 3     # backends, markdown tables like the ones above
scripts/compare-backends.sh            # same answers? (also on-demand, push and notifications status)
docker compose -f docker-compose.yml -f docker-compose.rust-svelte.yml up -d --build   # in deploy/
scripts/scratch-db.sh down
```
