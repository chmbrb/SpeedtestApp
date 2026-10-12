#!/usr/bin/env bash
# Runs the Go and Rust backends side by side on one SCRATCH database and compares their answers.
#   scripts/compare-backends.sh [requests] [seed]
# Needs scripts/scratch-db.sh up first (or TEST_DATABASE_URL pointing at a disposable database).
set -euo pipefail
cd "$(dirname "$0")/.."
URL="${TEST_DATABASE_URL:-$(scripts/scratch-db.sh url)}"
N="${1:-600}"; SEED="${2:-1}"
TMP="$(mktemp -d)"; PIDS=()
cleanup() { for p in "${PIDS[@]:-}"; do [ -n "$p" ] && kill "$p" 2>/dev/null || true; done; rm -rf "$TMP"; }
trap cleanup EXIT

marker="$(psql "$URL" -Atc "select shobj_description(oid,'pg_database') from pg_database where datname=current_database()")"
[ "$marker" = "speedtest-integration-test" ] || { echo "refusing: $URL is not marked as a disposable database" >&2; exit 1; }
if [ "$(psql "$URL" -Atc 'select count(*) from speedtests')" = 0 ]; then
  psql "$URL" -q -v ON_ERROR_STOP=1 -f scripts/seed-wired.sql -f frontend-shared/e2e/seed.sql >/dev/null
fi

(cd backend-go && go build -o "$TMP/go-server" ./cmd/server)
(cd backend-rust && cargo build --release -q) && cp backend-rust/target/release/speedtest-backend "$TMP/rust-server"
export DATABASE_URL="$URL" TIMEZONE=America/Phoenix API_TOKENS="laptop=tok-laptop,phone=tok-phone" INGEST_RATE_PER_MIN=0 OOKLA_ACCEPT_LICENSE=yes OOKLA_PATH="$PWD/frontend-shared/e2e/fake-speedtest.sh" ONDEMAND_PER_MINUTE=100000 COMPARE_ONDEMAND=1
LISTEN_ADDR=127.0.0.1:8091 "$TMP/go-server" >"$TMP/go.log" 2>&1 & PIDS+=($!)
LISTEN_ADDR=127.0.0.1:8092 "$TMP/rust-server" >"$TMP/rust.log" 2>&1 & PIDS+=($!)
for port in 8091 8092; do
  for _ in $(seq 1 50); do curl -sf "http://127.0.0.1:$port/api/v1/health" >/dev/null && break; sleep 0.2; done
done
node scripts/compare-backends.mjs http://127.0.0.1:8091 http://127.0.0.1:8092 "$N" "$SEED"
