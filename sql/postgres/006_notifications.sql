-- Notification state for the ntfy alerts (backend-go/internal/notify). Run after 005.
--   psql "$PG_URI" -f sql/postgres/006_notifications.sql
-- Safe to re-run.

\set ON_ERROR_STOP on
BEGIN;

-- Where the monitor has read up to. One row. Created by the backend on first start, positioned at "now" so that
-- turning notifications on does not alert about history.
CREATE TABLE IF NOT EXISTS notify_cursor (
    id           boolean     PRIMARY KEY DEFAULT true CHECK (id),
    last_created timestamptz NOT NULL,
    last_id      uuid
);

-- One row per (kind, subject) that has ever been evaluated. 'firing' is the current condition; 'notified_at' is when
-- the person was last told about it (NULL = not told yet, so a failed delivery is retried on the next check).
CREATE TABLE IF NOT EXISTS alert_state (
    kind            text             NOT NULL CHECK (kind IN ('slow', 'loss', 'silence')),
    subject         text             NOT NULL,           -- slow/loss: 'device|connection|provider'; silence: 'device'
    device          text             NOT NULL,
    connection_type text,
    provider        text,
    firing          boolean          NOT NULL DEFAULT false,
    streak          integer          NOT NULL DEFAULT 0,  -- consecutive bad results so far
    streak_since    timestamptz,                          -- tested_at of the first bad result in the streak
    since           timestamptz,                          -- when the alert started firing
    notified_at     timestamptz,
    severity        text             NOT NULL DEFAULT 'warning' CHECK (severity IN ('warning', 'critical')),
    title           text,
    detail          text,
    updated_at      timestamptz      NOT NULL DEFAULT now(),
    PRIMARY KEY (kind, subject)
);
CREATE INDEX IF NOT EXISTS ix_alert_state_firing ON alert_state (firing) WHERE firing;

-- What was sent (or failed to send). The dashboard shows the recent ones; the backend prunes old rows.
CREATE TABLE IF NOT EXISTS notification_events (
    id         bigserial   PRIMARY KEY,
    created_at timestamptz NOT NULL DEFAULT now(),
    event      text        NOT NULL CHECK (event IN ('alert', 'recovery', 'reminder', 'test')),
    kind       text,
    subject    text,
    title      text        NOT NULL,
    message    text        NOT NULL,
    delivered  boolean     NOT NULL,
    error      text
);
CREATE INDEX IF NOT EXISTS ix_notification_events_created ON notification_events (created_at DESC);

-- Reading new results is "created > cursor", so index it.
CREATE INDEX IF NOT EXISTS ix_speedtests_created_id ON speedtests (created, id);

DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'speedtest') THEN
        GRANT SELECT, INSERT, UPDATE ON notify_cursor, alert_state TO speedtest;
        GRANT SELECT, INSERT, DELETE ON notification_events TO speedtest;
        GRANT USAGE ON SEQUENCE notification_events_id_seq TO speedtest;
    END IF;
END $$;

COMMIT;
