-- Extra history for the browser tests: a wireless laptop (Ookla) and a wireless phone (NDT7), on top of a wired server's
-- results. Run it ONCE against a disposable copy of the database, never your real one:
--   psql "$DISPOSABLE_DB_URL" -f frontend-react/e2e/seed.sql
-- It does nothing if the devices already exist.
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM speedtests WHERE device IN ('laptop', 'phone')) THEN
        RAISE NOTICE 'laptop/phone already seeded; nothing to do';
        RETURN;
    END IF;
    PERFORM setseed(0.42);
    -- laptop on wifi: Ookla, ~3 tests a day for 45 days, slower and noisier than a wired server; the last one is poor
    INSERT INTO speedtests (provider, "trigger", device, connection_type, tested_at, download_mbps, upload_mbps, latency_ms,
                            jitter_ms, packet_loss_pct, server_name, server_location, raw_data)
    SELECT 'ookla', 'scheduled', 'laptop', 'wireless', ts,
           CASE WHEN ts = max(ts) OVER () THEN 212 ELSE 330 + 90*random() - 120*(random()<0.05)::int*random() END,
           32 + 8*random(), 18 + 14*random() + 40*(random()<0.04)::int, 3 + 6*random(),
           CASE WHEN random() < 0.12 THEN round((random()*2)::numeric, 2) ELSE 0 END,
           'Optimum Online', 'Phoenix, AZ', '{}'::jsonb
    FROM (SELECT now() - interval '45 days' + (g * interval '8 hours') + (random() * interval '90 minutes') AS ts
          FROM generate_series(0, 134) g) t;
    -- phone on wifi: NDT7 from the browser, ~2 manual tests a day for 20 days
    INSERT INTO speedtests (provider, "trigger", device, connection_type, tested_at, download_mbps, upload_mbps, latency_ms,
                            jitter_ms, packet_loss_pct, server_name, server_location, raw_data)
    SELECT 'ndt7', 'on_demand', 'phone', 'wireless',
           now() - interval '20 days' + (g * interval '11 hours') + (random() * interval '2 hours'),
           170 + 70*random(), 24 + 9*random(), 14 + 8*random(), NULL, round((random()*0.6)::numeric, 2),
           'mlab1-lax05', 'Los Angeles', '{}'::jsonb
    FROM generate_series(0, 43) g;
END $$;
