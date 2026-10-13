#!/bin/sh
# Applies the database schema. Every script is idempotent, so this is safe to run on every start, which is also how
# upgrades reach an existing database. Connection settings come from the PG* environment variables.
#
# The order matters: the limited login is created after the first schema (so its grants have something to attach to), and the
# later scripts re-grant to it whenever they replace a function or add a table.
set -eu
cd /migrations

run() {
  echo "migrate: $1"
  psql -v ON_ERROR_STOP=1 -q -X -f "$1"
}

run 001_create_speedtests.sql
run 003_create_app_role.sql
printf "ALTER ROLE speedtest WITH LOGIN PASSWORD :'pw';\n" | psql -v ON_ERROR_STOP=1 -q -X -v pw="$APP_DB_PASSWORD"
run 004_add_device_connection_type.sql
run 005_providers_and_tests.sql
run 006_notifications.sql
run 007_web_push.sql
run 008_devices_summary_index.sql
run 009_session_key.sql

echo "migrate: done"
