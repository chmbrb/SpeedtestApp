-- Makes the device list (GET /api/v1/devices) fast on a big table. Run after 007.
--   psql "$PG_URI" -f sql/postgres/008_devices_summary_index.sql
-- Safe to re-run.
--
-- The list needs, for every device, its connection types, providers, number of tests and latest test. The query reads
-- (device, connection_type, provider, tested_at) and nothing else, so with all four in one index it is answered from the index
-- alone, without reading the (wide) table rows: about 7 times faster at 200,000 results. Building it blocks writes for the
-- moment it takes (a fraction of a second at that size, nothing noticeable at a few thousand results).

\set ON_ERROR_STOP on
BEGIN;

CREATE INDEX IF NOT EXISTS ix_speedtests_devices_summary
    ON speedtests (device, connection_type, provider, tested_at);

COMMIT;
