-- Limited login for the collector and the Flask app (replaces sql/create_spLogin.sql).
-- Run as the admin user, connected to the speedtest database:
--   psql "$PG_URI" -f sql/postgres/003_create_app_role.sql
-- Then set the password interactively so it never lands in a file or shell history:
--   psql "$PG_URI" -c '\password speedtest'

DO $$
BEGIN
    IF NOT EXISTS (SELECT FROM pg_roles WHERE rolname = 'speedtest') THEN
        CREATE ROLE speedtest LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE;
    END IF;
END $$;

-- Only this database, and no creating objects in the public schema
-- (Postgres 13 lets every role create there by default; 15+ does not).
REVOKE CREATE ON SCHEMA public FROM PUBLIC;
GRANT CONNECT ON DATABASE speedtestdb TO speedtest;
GRANT USAGE ON SCHEMA public TO speedtest;

-- The app reads through speedtest_results() and the collector writes through
-- speedtests_insert(); both are SQL functions running as the caller, so the
-- role needs SELECT and INSERT on the table. No UPDATE or DELETE.
GRANT SELECT, INSERT ON speedtests TO speedtest;
GRANT EXECUTE ON FUNCTION speedtest_results(text), speedtests_insert(jsonb) TO speedtest;
