#!/usr/bin/env bash
# Measures the Go and Rust backends one at a time on the same scratch database (200,000 results), and prints a markdown table.
#   scripts/bench-backends.sh [rows=200000] [rounds=3]
# Needs `ab` (ApacheBench), node, psql, and scripts/scratch-db.sh. Run on an otherwise idle machine; the numbers are only comparable
# with each other, on the same hardware. The load generator shares the machine with the server, as it would on a home server.
set -euo pipefail
cd "$(dirname "$0")/.."
ROWS="${1:-200000}"; ROUNDS="${2:-3}"
export PGSSLMODE=disable
URL="$(scripts/scratch-db.sh url)"
TMP="$(mktemp -d)"; PID=""
cleanup() { [ -n "$PID" ] && kill "$PID" 2>/dev/null || true; rm -rf "$TMP"; }
trap cleanup EXIT
marker="$(psql "$URL" -Atc "select shobj_description(oid,'pg_database') from pg_database where datname=current_database()")"
[ "$marker" = "speedtest-integration-test" ] || { echo "refusing: $URL is not a disposable database" >&2; exit 1; }

load_data() {
echo "loading $ROWS results..." >&2
psql "$URL" -q -v ON_ERROR_STOP=1 <<SQL
TRUNCATE speedtests, alert_state, notify_cursor, notification_events, push_subscriptions, tests CASCADE;
INSERT INTO speedtests (provider, "trigger", device, connection_type, tested_at, created, download_mbps, upload_mbps, latency_ms, packet_loss_pct, raw_data)
SELECT CASE WHEN g % 5 = 0 THEN 'ndt7' ELSE 'ookla' END,
       CASE WHEN g % 7 = 0 THEN 'on_demand' ELSE 'scheduled' END,
       (ARRAY['server','laptop','phone','nas'])[1 + g % 4],
       CASE WHEN g % 4 = 0 THEN 'wired' ELSE (ARRAY['wired','wireless'])[1 + g % 2] END,
       now() - (g * interval '40 seconds'), now() - (g * interval '40 seconds'),
       400 + 400 * random(), 40 + 40 * random(), 5 + 20 * random(), CASE WHEN g % 9 = 0 THEN 2 * random() END,
       jsonb_build_object('type','result','filler', repeat('x', 1500))
FROM generate_series(1, $ROWS) g;
ANALYZE speedtests;
SQL
  psql "$URL" -q -c "VACUUM (ANALYZE) speedtests"
}

(cd backend-go && go build -o "$TMP/go-server" ./cmd/server)
(cd backend-rust && cargo build --release -q) && cp backend-rust/target/release/speedtest-backend "$TMP/rust-server"
export DATABASE_URL="$URL" TIMEZONE=America/Phoenix API_TOKENS="bench=tok-bench" INGEST_RATE_PER_MIN=0 LISTEN_ADDR=127.0.0.1:8095

ENDPOINTS=(
  "health|/api/v1/health|8000"
  "devices|/api/v1/devices|4000"
  "results page (50)|/api/v1/results?limit=50|4000"
  "results filtered|/api/v1/results?limit=50&provider=ookla&connectionType=wired&device=server|4000"
  "summary (30 days)|/api/v1/stats/summary?from=$(date -u -d '30 days ago' +%Y-%m-%dT%H:%M:%SZ)|1000"
  "timeseries (hourly, 30 days)|/api/v1/stats/timeseries?interval=hour&from=$(date -u -d '30 days ago' +%Y-%m-%dT%H:%M:%SZ)|400"
)
rss() { awk -v k="$2" '$1==k":" {print int($2/1024)}' /proc/$1/status; }
median() { sort -n | awk '{a[NR]=$1} END {print a[int((NR+1)/2)]}'; }
declare -A OUT

for who in go rust; do
  load_data   # every backend gets the same freshly loaded and vacuumed table: the other one's inserts must not slow it down
  "$TMP/$who-server" >"$TMP/$who.log" 2>&1 & PID=$!
  start=$(date +%s.%N)
  until curl -sf http://127.0.0.1:8095/api/v1/health >/dev/null 2>&1; do sleep 0.02; done
  OUT[$who,startup]=$(awk -v a="$start" -v b="$(date +%s.%N)" 'BEGIN{printf "%.0f", (b-a)*1000}')
  OUT[$who,idle]=$(rss $PID VmRSS)
  for e in "${ENDPOINTS[@]}"; do
    IFS='|' read -r name path n <<<"$e"
    ab -q -s 120 -n $((n / 8)) -c 16 "http://127.0.0.1:8095$path" >/dev/null 2>&1 || true   # warm up
    rps=(); p50=(); p99=()
    for _ in $(seq "$ROUNDS"); do
      r="$(ab -q -s 120 -n "$n" -c 16 "http://127.0.0.1:8095$path" 2>&1 || true)"
      [ "$(grep -oP 'Non-2xx responses:\s+\K\d+' <<<"$r" || echo 0)" = 0 ] || { echo "non-2xx on $path" >&2; exit 1; }
      grep -q 'Requests per second' <<<"$r" || { echo "ab failed on $path: $(tail -2 <<<"$r")" >&2; exit 1; }
      rps+=("$(grep -oP 'Requests per second:\s+\K[0-9.]+' <<<"$r")")
      p50+=("$(awk '$1=="50%" {print $2}' <<<"$r")"); p99+=("$(awk '$1=="99%" {print $2}' <<<"$r")")
    done
    OUT[$who,$name]="$(printf '%s\n' "${rps[@]}" | median | cut -d. -f1) / $(printf '%s\n' "${p50[@]}" | median) / $(printf '%s\n' "${p99[@]}" | median)"
  done
  # sending results: 2000 unique valid results, 8 at a time
  node scripts/bench-ingest.mjs http://127.0.0.1:8095 tok-bench 300 8 >/dev/null   # warm up
  read -r irps ip50 ip99 ifail < <(node scripts/bench-ingest.mjs http://127.0.0.1:8095 tok-bench 2000 8)
  OUT[$who,ingest]="$irps / $ip50 / $ip99"; [ "$ifail" = 0 ] || echo "ingest failures: $ifail ($who)" >&2
  OUT[$who,peak]=$(rss $PID VmHWM)
  kill $PID; wait $PID 2>/dev/null || true; PID=""
done

echo
echo "| Request (c=16) | Go: req/s / p50 ms / p99 ms | Rust: req/s / p50 ms / p99 ms |"
echo "|---|---|---|"
for e in "${ENDPOINTS[@]}"; do n="${e%%|*}"; echo "| $n | ${OUT[go,$n]} | ${OUT[rust,$n]} |"; done
echo "| send a result (POST, c=8) | ${OUT[go,ingest]} | ${OUT[rust,ingest]} |"
echo
echo "| | Go | Rust |"
echo "|---|---|---|"
echo "| time to first healthy answer (ms) | ${OUT[go,startup]} | ${OUT[rust,startup]} |"
echo "| memory at rest, RSS (MiB) | ${OUT[go,idle]} | ${OUT[rust,idle]} |"
echo "| memory after the run, peak RSS (MiB) | ${OUT[go,peak]} | ${OUT[rust,peak]} |"
