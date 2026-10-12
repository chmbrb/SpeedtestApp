-- A wired "server" with 60 days of two-hourly Ookla results, for a SCRATCH database (never your real one). Together with
-- frontend-shared/e2e/seed.sql (a wireless laptop and phone) it gives the dashboard and the comparison script realistic data:
--   psql "$SCRATCH_URL" -f scripts/seed-wired.sql -f frontend-shared/e2e/seed.sql
SELECT setseed(0.7);
INSERT INTO speedtests (provider, "trigger", device, connection_type, tested_at, download_mbps, upload_mbps, latency_ms,
                        jitter_ms, packet_loss_pct, server_name, server_location, raw_data)
SELECT 'ookla', 'scheduled', 'server', 'wired', ts, 880 + 60 * random(), 35 + 5 * random(), 11 + 3 * random(), 1 + random(), 0,
       'CenturyLink', 'Phoenix, AZ', '{}'::jsonb
FROM (SELECT now() - interval '60 days' + g * interval '2 hours' AS ts FROM generate_series(0, 700) g) s;
