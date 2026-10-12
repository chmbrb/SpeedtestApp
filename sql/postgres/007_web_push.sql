-- Web Push (browser and installed-app notifications). Run after 006.
--   psql "$PG_URI" -f sql/postgres/007_web_push.sql
-- Safe to re-run.

\set ON_ERROR_STOP on
BEGIN;

-- The server's VAPID key pair: it proves to the browsers' push services that a message comes from this server. One row,
-- created by the backend on first start. The private key never leaves the database.
CREATE TABLE IF NOT EXISTS push_vapid (
    id          boolean     PRIMARY KEY DEFAULT true CHECK (id),
    public_key  text        NOT NULL,
    private_key text        NOT NULL,
    created_at  timestamptz NOT NULL DEFAULT now()
);

-- One row per browser or installed app that turned notifications on. The endpoint is a secret address at the browser vendor's
-- push service (anyone who knows it can send that browser a message the server's key signs for), so it is never returned by the API.
CREATE TABLE IF NOT EXISTS push_subscriptions (
    id           uuid        PRIMARY KEY DEFAULT gen_random_uuid(),
    endpoint     text        NOT NULL UNIQUE CHECK (length(endpoint) <= 2048),
    p256dh       text        NOT NULL,
    auth         text        NOT NULL,
    user_agent   text,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_success timestamptz,
    failures     integer     NOT NULL DEFAULT 0   -- consecutive failed deliveries; the row is removed when this gets high
);

DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'speedtest') THEN
        GRANT SELECT, INSERT ON push_vapid TO speedtest;
        GRANT SELECT, INSERT, UPDATE, DELETE ON push_subscriptions TO speedtest;
    END IF;
END $$;

COMMIT;
