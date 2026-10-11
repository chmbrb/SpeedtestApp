-- Target schema for the SQL Server -> PostgreSQL migration.
-- Run: psql "$PG_URI" -f sql/postgres/001_create_speedtests.sql

CREATE TABLE IF NOT EXISTS speedtests (
    id                      uuid             PRIMARY KEY DEFAULT gen_random_uuid(),
    legacy_id               integer          UNIQUE,          -- old SQL Server Speedtests.Id
    created                 timestamptz      NOT NULL DEFAULT now(),  -- old Created (UTC)
    tested_at               timestamptz      NOT NULL,        -- $.timestamp
    type                    text,

    ping_jitter             double precision,
    ping_latency            double precision,
    ping_low                double precision,
    ping_high               double precision,

    download_bandwidth      bigint,                           -- bytes/sec (Mbps = value / 125000)
    download_bytes          bigint,
    download_elapsed        integer,
    download_latency_iqm    double precision,
    download_latency_low    double precision,
    download_latency_high   double precision,
    download_latency_jitter double precision,

    upload_bandwidth        bigint,
    upload_bytes            bigint,
    upload_elapsed          integer,
    upload_latency_iqm      double precision,
    upload_latency_low      double precision,
    upload_latency_high     double precision,
    upload_latency_jitter   double precision,

    packet_loss             double precision,                 -- NULL when the CLI did not report it
    isp                     text,

    interface_internal_ip   inet,
    interface_name          text,
    interface_mac_addr      text,
    interface_is_vpn        boolean,
    interface_external_ip   inet,

    server_id               integer,
    server_host             text,
    server_port             integer,
    server_name             text,
    server_location         text,
    server_country          text,
    server_ip               inet,

    result_id               uuid,
    result_url              text,
    result_persisted        boolean,

    raw_data                jsonb            NOT NULL         -- original payload, kept as a safety net
);

CREATE INDEX IF NOT EXISTS ix_speedtests_tested_at ON speedtests (tested_at);

-- Replacement for dbo.spSpeedtestResults_GetAll. 'US Mountain Standard Time'
-- (Windows name) is America/Phoenix in IANA terms. Returns float8 so pandas
-- gets numeric dtypes (numeric would arrive as Decimal objects).
CREATE OR REPLACE FUNCTION speedtest_results(tz text DEFAULT 'America/Phoenix')
RETURNS TABLE ("localtime" text, "packetLoss" float8, download float8, upload float8)
LANGUAGE sql STABLE AS $$
    SELECT to_char(tested_at AT TIME ZONE tz, 'YYYY-MM-DD HH24:MI:SS'),
           COALESCE(round(packet_loss::numeric, 4), 0)::float8,
           round(download_bandwidth / 125000.0, 2)::float8,
           round(upload_bandwidth   / 125000.0, 2)::float8
    FROM speedtests
    ORDER BY tested_at;
$$;

-- Replacement for dbo.spSpeedtests_Insert: takes the raw Ookla CLI JSON and
-- stores it normalized. Returns the new row's UUID.
CREATE OR REPLACE FUNCTION speedtests_insert(p_raw jsonb)
RETURNS uuid
LANGUAGE sql AS $$
    INSERT INTO speedtests (
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
