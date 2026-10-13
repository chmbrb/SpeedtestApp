#!/usr/bin/env bash
# A throwaway PostgreSQL for tests, in a container, with the real migrations applied and the database marked as disposable
# (the integration tests of both backends refuse to touch any database that is not marked).
#
#   scripts/scratch-db.sh up      # start it (or reuse it) and print the TEST_DATABASE_URL to use
#   scripts/scratch-db.sh url     # print the URL
#   scripts/scratch-db.sh psql    # open psql in it
#   scripts/scratch-db.sh down    # remove it and its data
#
# Needs podman or docker, and psql on this machine. PORT (default 55432) and ENGINE can be overridden.
set -euo pipefail
cd "$(dirname "$0")/.."
ENGINE="${ENGINE:-$(command -v podman >/dev/null 2>&1 && echo podman || echo docker)}"
NAME=speedtest-scratch-pg
PORT="${PORT:-55432}"
URL="postgres://postgres:scratch@127.0.0.1:$PORT/speedtestdb?sslmode=disable"
export PGPASSWORD=scratch PGSSLMODE=disable

psql_() { psql -h 127.0.0.1 -p "$PORT" -U postgres -v ON_ERROR_STOP=1 -q -X "$@"; }

case "${1:-up}" in
  up)
    if ! "$ENGINE" ps --format '{{.Names}}' | grep -qx "$NAME"; then
      "$ENGINE" rm -f "$NAME" >/dev/null 2>&1 || true
      "$ENGINE" run -d --name "$NAME" --tmpfs /var/lib/postgresql/data -e POSTGRES_PASSWORD=scratch -e POSTGRES_DB=speedtestdb \
        -p "127.0.0.1:$PORT:5432" postgres:17-alpine >/dev/null
    fi
    for _ in $(seq 1 60); do psql_ -d speedtestdb -c 'select 1' >/dev/null 2>&1 && break; sleep 1; done
    psql_ -d speedtestdb -c 'select 1' >/dev/null 2>&1 || { echo "the scratch database did not start" >&2; exit 1; }
    # the same order deploy/migrate.sh uses (002 loads legacy data and is not needed)
    for f in 001_create_speedtests 003_create_app_role; do psql_ -d speedtestdb -f "sql/postgres/$f.sql" >/dev/null; done
    echo "ALTER ROLE speedtest WITH LOGIN PASSWORD 'scratch';" | psql_ -d speedtestdb >/dev/null
    for f in 004_add_device_connection_type 005_providers_and_tests 006_notifications 007_web_push 008_devices_summary_index 009_session_key; do
      psql_ -d speedtestdb -f "sql/postgres/$f.sql" >/dev/null
    done
    psql_ -d speedtestdb -c "COMMENT ON DATABASE speedtestdb IS 'speedtest-integration-test'" >/dev/null
    echo "export TEST_DATABASE_URL='$URL'"
    ;;
  url) echo "$URL" ;;
  psql) exec psql -h 127.0.0.1 -p "$PORT" -U postgres -d speedtestdb ;;
  down) "$ENGINE" rm -f "$NAME" >/dev/null 2>&1 || true; echo "removed $NAME" ;;
  *) echo "usage: $0 up|url|psql|down" >&2; exit 2 ;;
esac
