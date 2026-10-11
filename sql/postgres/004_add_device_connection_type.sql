-- Adds which device ran a test and how it was connected.
-- Run after 001 (and 003, if used): psql "$PG_URI" -f sql/postgres/004_add_device_connection_type.sql
-- Existing rows are backfilled as the wired server. Safe to re-run.

\set ON_ERROR_STOP on
BEGIN;

ALTER TABLE speedtests
    ADD COLUMN IF NOT EXISTS device          text NOT NULL DEFAULT 'server',
    ADD COLUMN IF NOT EXISTS connection_type text NOT NULL DEFAULT 'wired';

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'ck_speedtests_connection_type') THEN
        ALTER TABLE speedtests ADD CONSTRAINT ck_speedtests_connection_type
            CHECK (connection_type IN ('wired', 'wireless'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'ck_speedtests_device') THEN
        ALTER TABLE speedtests ADD CONSTRAINT ck_speedtests_device
            CHECK (btrim(device) <> '');
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS ix_speedtests_device_tested_at ON speedtests (device, tested_at);

-- The functions change signature, so drop the old ones first; leaving both would
-- make calls like speedtests_insert(jsonb) ambiguous.
DROP FUNCTION IF EXISTS speedtests_insert(jsonb);
DROP FUNCTION IF EXISTS speedtest_results(text);

-- Same as before, plus device / connection type (defaults keep old callers working).
CREATE OR REPLACE FUNCTION speedtests_insert(
    p_raw             jsonb,
    p_device          text DEFAULT 'server',
    p_connection_type text DEFAULT 'wired')
RETURNS uuid
LANGUAGE sql AS $$
    INSERT INTO speedtests (
        device, connection_type,
        tested_at, type,
        ping_jitter, ping_latency, ping_low, ping_high,
        download_bandwidth, download_bytes, download_elapsed,
        download_latency_iqm, download_latency_low, download_latency_high, download_latency_jitter,
        upload_bandwidth, upload_bytes, upload_elapsed,
        upload_latency_iqm, upload_latency_low, upload_latency_high, upload_latency_jitter,
        packet_loss, isp,
        interface_internal_ip, interface_name, interface_mac_addr, interface_is_vpn, interface_external_ip,
        server_id, server_host, server_port, server_name, server_location, server_country, server_ip,
        result_id, result_url, result_persisted, raw_data)
    VALUES (
        COALESCE(p_device, 'server'), COALESCE(p_connection_type, 'wired'),
        (p_raw->>'timestamp')::timestamptz, p_raw->>'type',
        (p_raw#>>'{ping,jitter}')::float8, (p_raw#>>'{ping,latency}')::float8,
        (p_raw#>>'{ping,low}')::float8, (p_raw#>>'{ping,high}')::float8,
        (p_raw#>>'{download,bandwidth}')::bigint, (p_raw#>>'{download,bytes}')::bigint, (p_raw#>>'{download,elapsed}')::int,
        (p_raw#>>'{download,latency,iqm}')::float8, (p_raw#>>'{download,latency,low}')::float8,
        (p_raw#>>'{download,latency,high}')::float8, (p_raw#>>'{download,latency,jitter}')::float8,
        (p_raw#>>'{upload,bandwidth}')::bigint, (p_raw#>>'{upload,bytes}')::bigint, (p_raw#>>'{upload,elapsed}')::int,
        (p_raw#>>'{upload,latency,iqm}')::float8, (p_raw#>>'{upload,latency,low}')::float8,
        (p_raw#>>'{upload,latency,high}')::float8, (p_raw#>>'{upload,latency,jitter}')::float8,
        (p_raw->>'packetLoss')::float8, p_raw->>'isp',
        nullif(p_raw#>>'{interface,internalIp}','')::inet, p_raw#>>'{interface,name}', p_raw#>>'{interface,macAddr}',
        (p_raw#>>'{interface,isVpn}')::boolean, nullif(p_raw#>>'{interface,externalIp}','')::inet,
        (p_raw#>>'{server,id}')::int, p_raw#>>'{server,host}', (p_raw#>>'{server,port}')::int, p_raw#>>'{server,name}',
        p_raw#>>'{server,location}', p_raw#>>'{server,country}', nullif(p_raw#>>'{server,ip}','')::inet,
        (p_raw#>>'{result,id}')::uuid, p_raw#>>'{result,url}', (p_raw#>>'{result,persisted}')::boolean, p_raw)
    RETURNING id;
$$;

-- Same columns as before; optional filters. NULL means "all".
CREATE OR REPLACE FUNCTION speedtest_results(
    tz                text DEFAULT 'America/Phoenix',
    p_device          text DEFAULT NULL,
    p_connection_type text DEFAULT NULL)
RETURNS TABLE ("localtime" text, "packetLoss" float8, download float8, upload float8)
LANGUAGE sql STABLE AS $$
    SELECT to_char(tested_at AT TIME ZONE tz, 'YYYY-MM-DD HH24:MI:SS'),
           COALESCE(round(packet_loss::numeric, 4), 0)::float8,
           round(download_bandwidth / 125000.0, 2)::float8,
           round(upload_bandwidth   / 125000.0, 2)::float8
    FROM speedtests
    WHERE (p_device IS NULL OR device = p_device)
      AND (p_connection_type IS NULL OR connection_type = p_connection_type)
    ORDER BY tested_at;
$$;

-- Re-grant for the limited app role (dropping the functions removed its grants).
DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'speedtest') THEN
        GRANT EXECUTE ON FUNCTION speedtest_results(text, text, text),
                                  speedtests_insert(jsonb, text, text) TO speedtest;
    END IF;
END $$;

COMMIT;
