package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

var (
	// ErrServerBusy: a server-run test is already queued or running (the unique index allows only one).
	ErrServerBusy = errors.New("a test is already running on the server")
	// ErrTokenInvalid: the submit token does not match the test, or the test has no token (already used).
	ErrTokenInvalid = errors.New("invalid submit token")
	// ErrTestExpired: the browser test was not finished in time.
	ErrTestExpired = errors.New("test expired")
)

// ClosedError means the test is no longer running, so it cannot accept a result.
type ClosedError struct{ State string }

func (e ClosedError) Error() string { return "test is already " + e.State }

// TestRow is one row of the tests table.
type TestRow struct {
	ID             string
	Provider       string
	Mode           string
	Device         string
	ConnectionType string
	State          string
	CreatedAt      time.Time
	StartedAt      *time.Time
	FinishedAt     *time.Time
	ResultID       *string
	Error          *string
}

func (t TestRow) Terminal() bool {
	return t.State == "completed" || t.State == "failed" || t.State == "cancelled"
}

// NewTest describes a test to create. A browser test starts out running (the browser begins at once) and carries the
// SHA-256 of its single-use token; a server test starts queued.
type NewTest struct {
	Provider, Mode, Device, ConnectionType, State string
	TokenHash                                     []byte
	ClientIP                                      string
}

const testCols = `id::text, provider, mode, device, connection_type, state, created_at, started_at, finished_at, result_id::text, error`

func scanTest(row pgx.Row) (TestRow, error) {
	var t TestRow
	err := row.Scan(&t.ID, &t.Provider, &t.Mode, &t.Device, &t.ConnectionType, &t.State, &t.CreatedAt, &t.StartedAt,
		&t.FinishedAt, &t.ResultID, &t.Error)
	t.CreatedAt = t.CreatedAt.UTC()
	for _, p := range []**time.Time{&t.StartedAt, &t.FinishedAt} {
		if *p != nil {
			u := (**p).UTC()
			*p = &u
		}
	}
	return t, err
}

func (s *Store) CreateTest(ctx context.Context, n NewTest) (TestRow, error) {
	var ip *string
	if n.ClientIP != "" {
		ip = &n.ClientIP
	}
	var hash any
	if len(n.TokenHash) > 0 {
		hash = n.TokenHash
	}
	t, err := scanTest(s.pool.QueryRow(ctx, `
		INSERT INTO tests (provider, mode, device, connection_type, state, started_at, submit_token_hash, client_ip)
		VALUES ($1, $2, $3, $4, $5, CASE WHEN $5 = 'running' THEN now() END, $6, $7::inet)
		RETURNING `+testCols, n.Provider, n.Mode, n.Device, n.ConnectionType, n.State, hash, ip))
	var pg *pgconn.PgError
	if errors.As(err, &pg) && pg.Code == "23505" && pg.ConstraintName == "ux_tests_one_active_server" {
		return TestRow{}, ErrServerBusy
	}
	return t, err
}

func (s *Store) GetTest(ctx context.Context, id string) (TestRow, error) {
	if !uuidRe.MatchString(id) {
		return TestRow{}, ErrNotFound
	}
	t, err := scanTest(s.pool.QueryRow(ctx, `SELECT `+testCols+` FROM tests WHERE id = $1::uuid`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return TestRow{}, ErrNotFound
	}
	return t, err
}

// ListTests returns the newest tests, optionally limited to some states.
func (s *Store) ListTests(ctx context.Context, states []string, limit int) ([]TestRow, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+testCols+` FROM tests WHERE ($1::text[] IS NULL OR state = ANY($1))
		ORDER BY created_at DESC, id DESC LIMIT $2`, nilIfEmpty(states), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TestRow{}
	for rows.Next() {
		t, err := scanTest(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// MarkTestRunning moves a queued test to running.
func (s *Store) MarkTestRunning(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `UPDATE tests SET state = 'running', started_at = now() WHERE id = $1::uuid AND state = 'queued'`, id)
	return err
}

// FinishTest ends a queued or running test. changed is false when it had already ended (for example a cancel that
// raced with completion); the returned row is then the final state.
func (s *Store) FinishTest(ctx context.Context, id, state string, resultID, errMsg *string) (t TestRow, changed bool, err error) {
	t, err = scanTest(s.pool.QueryRow(ctx, `
		UPDATE tests SET state = $2, finished_at = now(), result_id = $3::uuid, error = $4, submit_token_hash = NULL
		WHERE id = $1::uuid AND state IN ('queued', 'running')
		RETURNING `+testCols, id, state, resultID, errMsg))
	if errors.Is(err, pgx.ErrNoRows) {
		t, err = s.GetTest(ctx, id)
		return t, false, err
	}
	return t, err == nil, err
}

// InterruptServerTests fails server tests left queued or running by a process that died, so they cannot block new ones.
func (s *Store) InterruptServerTests(ctx context.Context) (int64, error) {
	tag, err := s.pool.Exec(ctx, `UPDATE tests SET state = 'failed', finished_at = now(), error = 'The server restarted while this test was running.'
		WHERE mode = 'server' AND state IN ('queued', 'running')`)
	return tag.RowsAffected(), err
}

// SweepTests fails tests that have been open too long: browser tests the browser never finished, and server tests that
// somehow outlived their own timeout.
func (s *Store) SweepTests(ctx context.Context, browserTTL, serverMax time.Duration) (int64, error) {
	tag, err := s.pool.Exec(ctx, `
		UPDATE tests SET state = 'failed', finished_at = now(), submit_token_hash = NULL,
		       error = CASE mode WHEN 'browser' THEN 'The test was not finished in time.' ELSE 'The test took too long and was stopped.' END
		WHERE state IN ('queued', 'running')
		  AND ((mode = 'browser' AND created_at < now() - make_interval(secs => $1))
		    OR (mode = 'server'  AND created_at < now() - make_interval(secs => $2)))`,
		browserTTL.Seconds(), serverMax.Seconds())
	return tag.RowsAffected(), err
}

// SubmitBrowserResult stores an NDT7 summary against a browser test, in one transaction: the single-use token is checked
// and spent, the result is inserted, and the test is linked to it. If the insert is rejected nothing changes, so the
// browser can try again with corrected data. It returns the new result's id.
func (s *Store) SubmitBrowserResult(ctx context.Context, id string, tokenHash []byte, ttl time.Duration, summary []byte, clientIP string) (string, error) {
	if !uuidRe.MatchString(id) {
		return "", ErrNotFound
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer tx.Rollback(ctx) //nolint:errcheck // rolled back unless committed

	var device, connectionType string
	err = tx.QueryRow(ctx, `
		UPDATE tests SET state = 'completed', finished_at = now(), submit_token_hash = NULL
		WHERE id = $1::uuid AND mode = 'browser' AND state = 'running' AND submit_token_hash = $2
		  AND created_at > now() - make_interval(secs => $3)
		RETURNING device, connection_type`, id, tokenHash, ttl.Seconds()).Scan(&device, &connectionType)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", s.whyNotSubmittable(ctx, id, ttl)
	}
	if err != nil {
		return "", err
	}

	var ip *string
	if clientIP != "" {
		ip = &clientIP
	}
	var resultID string
	if err := tx.QueryRow(ctx, `SELECT speedtests_insert_ndt7($1::jsonb, $2, $3, 'on_demand', $4::inet)::text`,
		string(summary), device, connectionType, ip).Scan(&resultID); err != nil {
		return "", err
	}
	if _, err := tx.Exec(ctx, `UPDATE tests SET result_id = $2::uuid, client_ip = $3::inet WHERE id = $1::uuid`, id, resultID, ip); err != nil {
		return "", err
	}
	return resultID, tx.Commit(ctx)
}

// whyNotSubmittable explains a refused submission: unknown test, test already over, expired, or a wrong/spent token.
func (s *Store) whyNotSubmittable(ctx context.Context, id string, ttl time.Duration) error {
	var state string
	var expired bool
	err := s.pool.QueryRow(ctx, `SELECT state, created_at <= now() - make_interval(secs => $2) FROM tests WHERE id = $1::uuid AND mode = 'browser'`,
		id, ttl.Seconds()).Scan(&state, &expired)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return ErrNotFound
	case err != nil:
		return err
	case state != "running":
		return ClosedError{State: state}
	case expired:
		return ErrTestExpired
	}
	return ErrTokenInvalid
}
