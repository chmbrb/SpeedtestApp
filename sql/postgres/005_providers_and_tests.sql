-- Multiple providers (Ookla, NDT7), provider-neutral metrics, and on-demand test tracking.
-- Run after 004: psql "$PG_URI" -f sql/postgres/005_providers_and_tests.sql
-- Existing rows become provider 'ookla', trigger 'scheduled'. Safe to re-run. Contract: docs/openapi.yaml

\set ON_ERROR_STOP on
BEGIN;

-- 1. Provider, trigger, and provider-neutral metrics -------------------------------------------
ALTER TABLE speedtests
    ADD COLUMN IF NOT EXISTS provider        text NOT NULL DEFAULT 'ookla',
    ADD COLUMN IF NOT EXISTS "trigger"       text NOT NULL DEFAULT 'scheduled',
    ADD COLUMN IF NOT EXISTS download_mbps   double precision,
    ADD COLUMN IF NOT EXISTS upload_mbps     double precision,
    ADD COLUMN IF NOT EXISTS latency_ms      double precision,
    ADD COLUMN IF NOT EXISTS jitter_ms       double precision,
    ADD COLUMN IF NOT EXISTS packet_loss_pct double precision;

-- Ookla bandwidth is bytes/second; Mbit/s = bandwidth / 125000. Only fills rows not yet filled.
UPDATE speedtests
SET download_mbps   = download_bandwidth / 125000.0,
    upload_mbps     = upload_bandwidth   / 125000.0,
    latency_ms      = ping_latency,
    jitter_ms       = ping_jitter,
    packet_loss_pct = packet_loss
WHERE download_mbps IS NULL OR upload_mbps IS NULL;

ALTER TABLE speedtests
    ALTER COLUMN download_mbps SET NOT NULL,
    ALTER COLUMN upload_mbps   SET NOT NULL;

DO $$
BEGIN
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'ck_speedtests_provider') THEN
        ALTER TABLE speedtests ADD CONSTRAINT ck_speedtests_provider
            CHECK (provider IN ('ookla', 'ndt7'));
    END IF;
    IF NOT EXISTS (SELECT 1 FROM pg_constraint WHERE conname = 'ck_speedtests_trigger') THEN
        ALTER TABLE speedtests ADD CONSTRAINT ck_speedtests_trigger
            CHECK ("trigger" IN ('scheduled', 'on_demand'));
    END IF;
END $$;

-- Sane bounds keep one absurd row from overflowing the aggregates (avg/percentile) behind the dashboard.
-- Dropped and re-added so re-running upgrades an older version of this constraint.
ALTER TABLE speedtests DROP CONSTRAINT IF EXISTS ck_speedtests_metrics;
ALTER TABLE speedtests ADD CONSTRAINT ck_speedtests_metrics CHECK (
    download_mbps BETWEEN 0 AND 1000000 AND upload_mbps BETWEEN 0 AND 1000000
    AND (latency_ms IS NULL OR latency_ms BETWEEN 0 AND 1000000)
    AND (jitter_ms IS NULL OR jitter_ms BETWEEN 0 AND 1000000)
    AND (packet_loss_pct IS NULL OR packet_loss_pct BETWEEN 0 AND 100));

CREATE INDEX IF NOT EXISTS ix_speedtests_provider_tested_at ON speedtests (provider, tested_at);

-- 2. On-demand tests ---------------------------------------------------------------------------
-- mode 'server': the backend runs the provider. mode 'browser': the caller's browser runs NDT7 and
-- submits the result with a single-use token (only its SHA-256 is stored, and cleared once used).
CREATE TABLE IF NOT EXISTS tests (
    id                uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    provider          text        NOT NULL CHECK (provider IN ('ookla', 'ndt7')),
    mode              text        NOT NULL CHECK (mode IN ('server', 'browser')),
    device            text        NOT NULL CHECK (btrim(device) <> ''),
    connection_type   text        NOT NULL CHECK (connection_type IN ('wired', 'wireless')),
    state             text        NOT NULL DEFAULT 'queued'
                                  CHECK (state IN ('queued', 'running', 'completed', 'failed', 'cancelled')),
    created_at        timestamptz NOT NULL DEFAULT now(),
    started_at        timestamptz,
    finished_at       timestamptz,
    result_id         uuid        REFERENCES speedtests (id) ON DELETE SET NULL,
    error             text,
    submit_token_hash bytea,
    client_ip         inet,
    -- finished_at is set exactly when the test is over
    CONSTRAINT ck_tests_finished CHECK ((state IN ('completed', 'failed', 'cancelled')) = (finished_at IS NOT NULL)),
    -- the server can only run Ookla here; NDT7 runs in a browser
    CONSTRAINT ck_tests_mode_provider CHECK (mode = 'browser' OR provider = 'ookla')
);

-- At most one active server test: concurrent requests race on this index and the loser gets 409.
CREATE UNIQUE INDEX IF NOT EXISTS ux_tests_one_active_server
    ON tests ((true)) WHERE mode = 'server' AND state IN ('queued', 'running');
CREATE INDEX IF NOT EXISTS ix_tests_created_at ON tests (created_at DESC);
CREATE INDEX IF NOT EXISTS ix_tests_state ON tests (state) WHERE state IN ('queued', 'running');

-- 3. Functions ---------------------------------------------------------------------------------
-- Signatures change, so drop the previous ones (leaving both would make calls ambiguous).
DROP FUNCTION IF EXISTS speedtests_insert(jsonb, text, text);
DROP FUNCTION IF EXISTS speedtest_results(text, text, text);

-- Ookla CLI JSON -> row. Old 1-3 argument calls still work through the defaults.
CREATE OR REPLACE FUNCTION speedtests_insert(
    p_raw             jsonb,
    p_device          text DEFAULT 'server',
    p_connection_type text DEFAULT 'wired',
    p_trigger         text DEFAULT 'scheduled')
RETURNS uuid
LANGUAGE sql AS $$
    INSERT INTO speedtests (
        provider, "trigger", device, connection_type,
        download_mbps, upload_mbps, latency_ms, jitter_ms, packet_loss_pct,
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
        'ookla', COALESCE(p_trigger, 'scheduled'), COALESCE(p_device, 'server'), COALESCE(p_connection_type, 'wired'),
        (p_raw#>>'{download,bandwidth}')::float8 / 125000.0, (p_raw#>>'{upload,bandwidth}')::float8 / 125000.0,
        (p_raw#>>'{ping,latency}')::float8, (p_raw#>>'{ping,jitter}')::float8, (p_raw->>'packetLoss')::float8,
        (p_raw->>'timestamp')::timestamptz, p_raw->>'type',
        (p_raw#>>'{ping,jitter}')::float8, (p_raw#>>'{ping,latency}')::float8,
        (p_raw#>>'{ping,low}')::float8, (p_raw#>>'{ping,high}')::float8,
        (p_raw#>>'{download,bandwidth}')::numeric::bigint, (p_raw#>>'{download,bytes}')::numeric::bigint, (p_raw#>>'{download,elapsed}')::numeric::int,
        (p_raw#>>'{download,latency,iqm}')::float8, (p_raw#>>'{download,latency,low}')::float8,
        (p_raw#>>'{download,latency,high}')::float8, (p_raw#>>'{download,latency,jitter}')::float8,
        (p_raw#>>'{upload,bandwidth}')::numeric::bigint, (p_raw#>>'{upload,bytes}')::numeric::bigint, (p_raw#>>'{upload,elapsed}')::numeric::int,
        (p_raw#>>'{upload,latency,iqm}')::float8, (p_raw#>>'{upload,latency,low}')::float8,
        (p_raw#>>'{upload,latency,high}')::float8, (p_raw#>>'{upload,latency,jitter}')::float8,
        (p_raw->>'packetLoss')::float8, p_raw->>'isp',
        nullif(p_raw#>>'{interface,internalIp}','')::inet, p_raw#>>'{interface,name}', p_raw#>>'{interface,macAddr}',
        (p_raw#>>'{interface,isVpn}')::boolean, nullif(p_raw#>>'{interface,externalIp}','')::inet,
        (p_raw#>>'{server,id}')::numeric::int, p_raw#>>'{server,host}', (p_raw#>>'{server,port}')::numeric::int, p_raw#>>'{server,name}',
        p_raw#>>'{server,location}', p_raw#>>'{server,country}', nullif(p_raw#>>'{server,ip}','')::inet,
        (p_raw#>>'{result,id}')::uuid, p_raw#>>'{result,url}', (p_raw#>>'{result,persisted}')::boolean, p_raw)
    RETURNING id;
$$;

-- NDT7 summary (docs/openapi.yaml: Ndt7Summary) -> row. p_client_ip is the address the server observed.
-- The packet-loss figure is the TCP retransmission rate, not true packet loss.
CREATE OR REPLACE FUNCTION speedtests_insert_ndt7(
    p_summary         jsonb,
    p_device          text,
    p_connection_type text,
    p_trigger         text DEFAULT 'on_demand',
    p_client_ip       inet DEFAULT NULL)
RETURNS uuid
LANGUAGE sql AS $$
    INSERT INTO speedtests (
        provider, "trigger", device, connection_type,
        download_mbps, upload_mbps, latency_ms, jitter_ms, packet_loss_pct,
        tested_at, type, interface_external_ip,
        server_name, server_location, server_country, raw_data)
    VALUES (
        'ndt7', p_trigger, p_device, p_connection_type,
        (p_summary->>'downloadMbps')::float8, (p_summary->>'uploadMbps')::float8,
        (p_summary->>'minRttMs')::float8, NULL, (p_summary->>'retransmissionPct')::float8,
        (p_summary->>'startedAt')::timestamptz, 'result', p_client_ip,
        p_summary#>>'{server,machine}', p_summary#>>'{server,location}', p_summary#>>'{server,country}',
        p_summary)
    RETURNING id;
$$;

-- Legacy report function (used by the Flask page). Now reads the neutral columns and can filter by provider.
CREATE OR REPLACE FUNCTION speedtest_results(
    tz                text DEFAULT 'America/Phoenix',
    p_device          text DEFAULT NULL,
    p_connection_type text DEFAULT NULL,
    p_provider        text DEFAULT NULL)
RETURNS TABLE ("localtime" text, "packetLoss" float8, download float8, upload float8)
LANGUAGE sql STABLE AS $$
    SELECT to_char(tested_at AT TIME ZONE tz, 'YYYY-MM-DD HH24:MI:SS'),
           COALESCE(round(packet_loss_pct::numeric, 4), 0)::float8,
           round(download_mbps::numeric, 2)::float8,
           round(upload_mbps::numeric, 2)::float8
    FROM speedtests
    WHERE (p_device IS NULL OR device = p_device)
      AND (p_connection_type IS NULL OR connection_type = p_connection_type)
      AND (p_provider IS NULL OR provider = p_provider)
    ORDER BY tested_at;
$$;

-- 4. Limited app role (if 003 created it): the backend manages tests, so it may update them. ---
DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'speedtest') THEN
        GRANT SELECT, INSERT, UPDATE ON tests TO speedtest;
        GRANT EXECUTE ON FUNCTION speedtest_results(text, text, text, text),
                                  speedtests_insert(jsonb, text, text, text),
                                  speedtests_insert_ndt7(jsonb, text, text, text, inet) TO speedtest;
    END IF;
END $$;

COMMIT;
