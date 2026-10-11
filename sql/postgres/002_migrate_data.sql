-- Loads the CSV from scripts/export_speedtests.py and normalizes it.
-- Run from the directory containing speedtests_export.csv:
--   psql "$PG_URI" -f sql/postgres/002_migrate_data.sql
-- (\copy does not expand psql variables, so the file name is fixed below.)
-- Safe to re-run: rows already migrated (by legacy_id) are skipped.

\set ON_ERROR_STOP on

BEGIN;

CREATE TEMP TABLE stage (id integer, created timestamptz, raw_data text) ON COMMIT DROP;

\copy stage FROM 'speedtests_export.csv' WITH (FORMAT csv)

INSERT INTO speedtests (
    legacy_id, created, tested_at, type,
    ping_jitter, ping_latency, ping_low, ping_high,
    download_bandwidth, download_bytes, download_elapsed,
    download_latency_iqm, download_latency_low, download_latency_high, download_latency_jitter,
    upload_bandwidth, upload_bytes, upload_elapsed,
    upload_latency_iqm, upload_latency_low, upload_latency_high, upload_latency_jitter,
    packet_loss, isp,
    interface_internal_ip, interface_name, interface_mac_addr, interface_is_vpn, interface_external_ip,
    server_id, server_host, server_port, server_name, server_location, server_country, server_ip,
    result_id, result_url, result_persisted, raw_data)
SELECT
    s.id, s.created, (j->>'timestamp')::timestamptz, j->>'type',
    (j#>>'{ping,jitter}')::float8, (j#>>'{ping,latency}')::float8, (j#>>'{ping,low}')::float8, (j#>>'{ping,high}')::float8,
    (j#>>'{download,bandwidth}')::bigint, (j#>>'{download,bytes}')::bigint, (j#>>'{download,elapsed}')::int,
    (j#>>'{download,latency,iqm}')::float8, (j#>>'{download,latency,low}')::float8,
    (j#>>'{download,latency,high}')::float8, (j#>>'{download,latency,jitter}')::float8,
    (j#>>'{upload,bandwidth}')::bigint, (j#>>'{upload,bytes}')::bigint, (j#>>'{upload,elapsed}')::int,
    (j#>>'{upload,latency,iqm}')::float8, (j#>>'{upload,latency,low}')::float8,
    (j#>>'{upload,latency,high}')::float8, (j#>>'{upload,latency,jitter}')::float8,
    (j->>'packetLoss')::float8, j->>'isp',
    nullif(j#>>'{interface,internalIp}','')::inet, j#>>'{interface,name}', j#>>'{interface,macAddr}',
    (j#>>'{interface,isVpn}')::boolean, nullif(j#>>'{interface,externalIp}','')::inet,
    (j#>>'{server,id}')::int, j#>>'{server,host}', (j#>>'{server,port}')::int, j#>>'{server,name}',
    j#>>'{server,location}', j#>>'{server,country}', nullif(j#>>'{server,ip}','')::inet,
    (j#>>'{result,id}')::uuid, j#>>'{result,url}', (j#>>'{result,persisted}')::boolean, j
FROM stage s
CROSS JOIN LATERAL (SELECT s.raw_data::jsonb AS j) p(j)
WHERE NOT EXISTS (SELECT 1 FROM speedtests t WHERE t.legacy_id = s.id);

-- Verification: compare against the source row count printed by the export script.
SELECT count(*) AS migrated_rows, min(tested_at) AS first_test, max(tested_at) AS last_test FROM speedtests;

COMMIT;
