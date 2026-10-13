// Package testdb gives integration tests a database they are allowed to wreck.
//
// These tests TRUNCATE tables, so they refuse to run against anything that has not been explicitly marked as disposable
// with a database comment. (A name pattern is not a safe guard: "speedtestdb" contains "test".)
//
//	COMMENT ON DATABASE <name> IS 'speedtest-integration-test';
package testdb

import (
	"context"
	"os"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const Marker = "speedtest-integration-test"

const lockKey = 7447001

var (
	lockOnce sync.Once
	lockErr  error
	// lockConn is kept in a package variable on purpose: an unreferenced connection is closed by the garbage collector,
	// which would silently drop the lock.
	lockConn *pgx.Conn
)

// ProcessLock serializes whole test processes on one database. `go test ./...` runs packages in parallel, and these
// tests empty tables, so two packages sharing a database would wipe each other's data mid-test. The advisory lock is
// held by a dedicated connection (not one from a pool: pool.Close waits for every borrowed connection, so a pooled one
// would hang the process at exit) that stays open until the test process ends.
func ProcessLock(ctx context.Context) error {
	lockOnce.Do(func() {
		conn, err := pgx.Connect(ctx, os.Getenv("TEST_DATABASE_URL"))
		if err != nil {
			lockErr = err
			return
		}
		lockConn = conn
		_, lockErr = lockConn.Exec(ctx, `SELECT pg_advisory_lock($1)`, lockKey) // blocks while another package's tests run
	})
	return lockErr
}

// Pool returns a pool on TEST_DATABASE_URL with the mutable tables emptied. It skips the test when the variable is not
// set and fails it when the database is not marked disposable.
func Pool(t testing.TB) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	var marker *string
	if err := pool.QueryRow(ctx, `SELECT shobj_description(oid, 'pg_database') FROM pg_database WHERE datname = current_database()`).Scan(&marker); err != nil {
		t.Fatal(err)
	}
	if marker == nil || *marker != Marker {
		t.Fatalf("refusing to run: this database is not marked as disposable.\nTo use a scratch database: COMMENT ON DATABASE <name> IS '%s';", Marker)
	}
	if err := ProcessLock(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `TRUNCATE speedtests, alert_state, notify_cursor, notification_events, push_subscriptions, push_vapid, session_key CASCADE`); err != nil {
		t.Fatal(err)
	}
	return pool
}
