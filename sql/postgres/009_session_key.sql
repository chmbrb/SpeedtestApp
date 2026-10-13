-- The key that signs login sessions (the optional dashboard login, DASHBOARD_PASSWORD). Run after 008.
--   psql "$PG_URI" -f sql/postgres/009_session_key.sql
-- Safe to re-run.

\set ON_ERROR_STOP on
BEGIN;

-- One row, created by the backend the first time it starts with the login turned on. It is random and never leaves the database:
-- a session cookie is only valid if it was signed with it, so nobody can make one without it, and (unlike a key derived from the
-- password alone) a stolen cookie cannot be used to guess the password offline. Both backends read the same row, so switching
-- between the Go and the Rust version keeps people signed in.
CREATE TABLE IF NOT EXISTS session_key (
    id         boolean     PRIMARY KEY DEFAULT true CHECK (id),
    key        bytea       NOT NULL CHECK (length(key) >= 32),
    created_at timestamptz NOT NULL DEFAULT now()
);

DO $$
BEGIN
    IF EXISTS (SELECT FROM pg_roles WHERE rolname = 'speedtest') THEN
        GRANT SELECT, INSERT ON session_key TO speedtest;
    END IF;
END $$;

COMMIT;
