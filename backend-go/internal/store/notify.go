package store

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ResultPoint is the slice of a result the notification monitor needs.
type ResultPoint struct {
	ID             string
	Created        time.Time
	TestedAt       time.Time
	Device         string
	ConnectionType string
	Provider       string
	DownloadMbps   float64
	PacketLossPct  *float64
}

// AlertState is the persisted state of one alert condition; see sql/postgres/006_notifications.sql.
type AlertState struct {
	Kind           string
	Subject        string
	Device         string
	ConnectionType *string
	Provider       *string
	Firing         bool
	Streak         int
	StreakSince    *time.Time
	Since          *time.Time
	NotifiedAt     *time.Time
	Severity       string
	Title          *string
	Detail         *string
	UpdatedAt      time.Time
}

// DeviceActivity is a device's most recent scheduled tests, newest first.
type DeviceActivity struct {
	Device string
	Times  []time.Time
}

type NotificationEvent struct {
	ID        int64
	At        time.Time
	Event     string
	Kind      *string
	Subject   *string
	Title     string
	Message   string
	Delivered bool
	Error     *string
}

const alertCols = `kind, subject, device, connection_type, provider, firing, streak, streak_since, since, notified_at,
	severity, title, detail, updated_at`

func scanAlert(row pgx.Row) (AlertState, error) {
	var a AlertState
	err := row.Scan(&a.Kind, &a.Subject, &a.Device, &a.ConnectionType, &a.Provider, &a.Firing, &a.Streak, &a.StreakSince,
		&a.Since, &a.NotifiedAt, &a.Severity, &a.Title, &a.Detail, &a.UpdatedAt)
	return a, err
}

// NotifyReady reports whether migration 006 has been applied.
func (s *Store) NotifyReady(ctx context.Context) error {
	var ok bool
	err := s.pool.QueryRow(ctx, `SELECT to_regclass('notify_cursor') IS NOT NULL AND to_regclass('alert_state') IS NOT NULL
		AND to_regclass('notification_events') IS NOT NULL`).Scan(&ok)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("notification tables are missing: apply sql/postgres/006_notifications.sql")
	}
	return nil
}

// CursorOrInit returns where the monitor has read up to, creating the cursor at `now` the first time so that
// turning notifications on never alerts about old results.
func (s *Store) CursorOrInit(ctx context.Context, now time.Time) (Cursor, error) {
	if _, err := s.pool.Exec(ctx, `INSERT INTO notify_cursor (id, last_created, last_id) VALUES (true, $1, NULL)
		ON CONFLICT (id) DO NOTHING`, now); err != nil {
		return Cursor{}, err
	}
	var c Cursor
	var id *string
	if err := s.pool.QueryRow(ctx, `SELECT last_created, last_id::text FROM notify_cursor WHERE id`).Scan(&c.T, &id); err != nil {
		return Cursor{}, err
	}
	c.T = c.T.UTC()
	if id != nil {
		c.ID = *id
	}
	return c, nil
}

func (s *Store) SaveCursor(ctx context.Context, c Cursor) error {
	var id *string
	if c.ID != "" {
		id = &c.ID
	}
	_, err := s.pool.Exec(ctx, `UPDATE notify_cursor SET last_created = $1, last_id = $2::uuid WHERE id`, c.T, id)
	return err
}

// NewResults returns results stored after the cursor, oldest first. `settledBefore` leaves out the last few
// seconds so a transaction that committed late is not skipped by a cursor that already moved past it.
func (s *Store) NewResults(ctx context.Context, after Cursor, settledBefore time.Time, limit int) ([]ResultPoint, error) {
	var id *string
	if after.ID != "" {
		id = &after.ID
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id::text, created, tested_at, device, connection_type, provider, download_mbps, packet_loss_pct
		FROM speedtests
		WHERE (created, id) > ($1, COALESCE($2::uuid, '00000000-0000-0000-0000-000000000000'::uuid))
		  AND created <= $3
		ORDER BY created, id LIMIT $4`, after.T, id, settledBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ResultPoint
	for rows.Next() {
		var r ResultPoint
		if err := rows.Scan(&r.ID, &r.Created, &r.TestedAt, &r.Device, &r.ConnectionType, &r.Provider, &r.DownloadMbps, &r.PacketLossPct); err != nil {
			return nil, err
		}
		r.Created, r.TestedAt = r.Created.UTC(), r.TestedAt.UTC()
		out = append(out, r)
	}
	return out, rows.Err()
}

// Baseline is the median download of an entity over the `days` before `before`, and how many tests that is.
func (s *Store) Baseline(ctx context.Context, device, connectionType, provider string, before time.Time, days int) (median float64, n int, err error) {
	var m *float64
	err = s.pool.QueryRow(ctx, `
		SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY download_mbps), count(*)
		FROM speedtests
		WHERE device = $1 AND connection_type = $2 AND provider = $3
		  AND tested_at < $4 AND tested_at >= $4 - make_interval(days => $5)`,
		device, connectionType, provider, before, days).Scan(&m, &n)
	if m != nil {
		median = *m
	}
	return median, n, err
}

// ScheduledActivity returns each device's latest scheduled tests (up to 21, newest first) since `since`.
func (s *Store) ScheduledActivity(ctx context.Context, since time.Time) ([]DeviceActivity, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT device, array_agg(tested_at ORDER BY tested_at DESC)
		FROM (SELECT device, tested_at, row_number() OVER (PARTITION BY device ORDER BY tested_at DESC) AS rn
		      FROM speedtests WHERE "trigger" = 'scheduled' AND tested_at >= $1) t
		WHERE rn <= 21 GROUP BY device ORDER BY device`, since)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DeviceActivity
	for rows.Next() {
		var a DeviceActivity
		if err := rows.Scan(&a.Device, &a.Times); err != nil {
			return nil, err
		}
		for i := range a.Times {
			a.Times[i] = a.Times[i].UTC()
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// LoadAlert returns the state for (kind, subject), or nil when it has never been evaluated.
func (s *Store) LoadAlert(ctx context.Context, kind, subject string) (*AlertState, error) {
	a, err := scanAlert(s.pool.QueryRow(ctx, `SELECT `+alertCols+` FROM alert_state WHERE kind = $1 AND subject = $2`, kind, subject))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &a, nil
}

func (s *Store) SaveAlert(ctx context.Context, a *AlertState) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO alert_state (kind, subject, device, connection_type, provider, firing, streak, streak_since, since,
		                         notified_at, severity, title, detail, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13, now())
		ON CONFLICT (kind, subject) DO UPDATE SET
		  firing = EXCLUDED.firing, streak = EXCLUDED.streak, streak_since = EXCLUDED.streak_since, since = EXCLUDED.since,
		  notified_at = EXCLUDED.notified_at, severity = EXCLUDED.severity, title = EXCLUDED.title,
		  detail = EXCLUDED.detail, updated_at = now()`,
		a.Kind, a.Subject, a.Device, a.ConnectionType, a.Provider, a.Firing, a.Streak, a.StreakSince, a.Since,
		a.NotifiedAt, a.Severity, a.Title, a.Detail)
	return err
}

// AlertsToHandle returns every alert that is firing or that the person was told about and is not yet closed out.
func (s *Store) AlertsToHandle(ctx context.Context) ([]AlertState, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+alertCols+` FROM alert_state WHERE firing OR notified_at IS NOT NULL ORDER BY kind, subject`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertState
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// FiringAlerts returns the alerts currently active, oldest first.
func (s *Store) FiringAlerts(ctx context.Context) ([]AlertState, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+alertCols+` FROM alert_state WHERE firing ORDER BY since, kind, subject`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlertState
	for rows.Next() {
		a, err := scanAlert(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func (s *Store) AddEvent(ctx context.Context, e NotificationEvent) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO notification_events (event, kind, subject, title, message, delivered, error)
		VALUES ($1,$2,$3,$4,$5,$6,$7)`, e.Event, e.Kind, e.Subject, e.Title, e.Message, e.Delivered, e.Error)
	return err
}

func (s *Store) RecentEvents(ctx context.Context, n int) ([]NotificationEvent, error) {
	rows, err := s.pool.Query(ctx, `SELECT id, created_at, event, kind, subject, title, message, delivered, error
		FROM notification_events ORDER BY id DESC LIMIT $1`, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []NotificationEvent{}
	for rows.Next() {
		var e NotificationEvent
		if err := rows.Scan(&e.ID, &e.At, &e.Event, &e.Kind, &e.Subject, &e.Title, &e.Message, &e.Delivered, &e.Error); err != nil {
			return nil, err
		}
		e.At = e.At.UTC()
		out = append(out, e)
	}
	return out, rows.Err()
}

// PruneEvents keeps only the newest `keep` events.
func (s *Store) PruneEvents(ctx context.Context, keep int) error {
	if keep < 1 {
		return fmt.Errorf("keep must be positive")
	}
	_, err := s.pool.Exec(ctx, `DELETE FROM notification_events WHERE id <= (SELECT id FROM notification_events ORDER BY id DESC OFFSET $1 LIMIT 1)`, keep)
	return err
}
