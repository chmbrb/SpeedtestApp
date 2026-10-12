package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// PushSubscription is one browser or installed app that asked for notifications; see sql/postgres/007_web_push.sql.
type PushSubscription struct {
	ID        string
	Endpoint  string
	P256dh    string
	Auth      string
	UserAgent string
	Failures  int
}

// VAPID returns the stored key pair, or ok=false when none has been created yet.
func (s *Store) VAPID(ctx context.Context) (public, private string, ok bool, err error) {
	err = s.pool.QueryRow(ctx, `SELECT public_key, private_key FROM push_vapid`).Scan(&public, &private)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", false, nil
	}
	return public, private, err == nil, err
}

// SaveVAPID stores the key pair unless one exists already (two servers starting together must end up with the same one).
func (s *Store) SaveVAPID(ctx context.Context, public, private string) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO push_vapid (public_key, private_key) VALUES ($1, $2) ON CONFLICT DO NOTHING`, public, private)
	return err
}

// ErrTooManySubscriptions is returned by SaveSubscription when the limit is reached by a new endpoint.
var ErrTooManySubscriptions = errors.New("too many push subscriptions")

// SaveSubscription adds the subscription, or refreshes the keys of an endpoint that is already stored. A new endpoint is
// refused when max subscriptions exist.
func (s *Store) SaveSubscription(ctx context.Context, sub PushSubscription, max int) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	// Serialise writers so the count check below cannot be raced past.
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(7447002)`); err != nil {
		return err
	}
	tag, err := tx.Exec(ctx, `UPDATE push_subscriptions SET p256dh = $2, auth = $3, user_agent = $4, failures = 0 WHERE endpoint = $1`,
		sub.Endpoint, sub.P256dh, sub.Auth, nilIfBlank(sub.UserAgent))
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		var n int
		if err := tx.QueryRow(ctx, `SELECT count(*) FROM push_subscriptions`).Scan(&n); err != nil {
			return err
		}
		if n >= max {
			return ErrTooManySubscriptions
		}
		if _, err := tx.Exec(ctx, `INSERT INTO push_subscriptions (endpoint, p256dh, auth, user_agent) VALUES ($1, $2, $3, $4)`,
			sub.Endpoint, sub.P256dh, sub.Auth, nilIfBlank(sub.UserAgent)); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func nilIfBlank(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// DeleteSubscription removes an endpoint and reports whether it was there.
func (s *Store) DeleteSubscription(ctx context.Context, endpoint string) (bool, error) {
	tag, err := s.pool.Exec(ctx, `DELETE FROM push_subscriptions WHERE endpoint = $1`, endpoint)
	return tag.RowsAffected() > 0, err
}

// Subscription looks one up by endpoint; ErrNotFound if it is not stored.
func (s *Store) Subscription(ctx context.Context, endpoint string) (PushSubscription, error) {
	var p PushSubscription
	var ua *string
	err := s.pool.QueryRow(ctx, `SELECT id::text, endpoint, p256dh, auth, user_agent, failures FROM push_subscriptions WHERE endpoint = $1`, endpoint).
		Scan(&p.ID, &p.Endpoint, &p.P256dh, &p.Auth, &ua, &p.Failures)
	if errors.Is(err, pgx.ErrNoRows) {
		return p, ErrNotFound
	}
	if ua != nil {
		p.UserAgent = *ua
	}
	return p, err
}

func (s *Store) ListSubscriptions(ctx context.Context) ([]PushSubscription, error) {
	rows, err := s.pool.Query(ctx, `SELECT id::text, endpoint, p256dh, auth, failures FROM push_subscriptions ORDER BY created_at`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PushSubscription
	for rows.Next() {
		var p PushSubscription
		if err := rows.Scan(&p.ID, &p.Endpoint, &p.P256dh, &p.Auth, &p.Failures); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// MarkPushDelivery records the outcome of one delivery. A success clears the failure count. A failure adds one, and the
// subscription is removed once it has failed maxFailures times in a row (it reports whether it was removed).
func (s *Store) MarkPushDelivery(ctx context.Context, id string, ok bool, maxFailures int) (removed bool, err error) {
	if ok {
		_, err = s.pool.Exec(ctx, `UPDATE push_subscriptions SET failures = 0, last_success = now() WHERE id = $1`, id)
		return false, err
	}
	tag, err := s.pool.Exec(ctx, `DELETE FROM push_subscriptions WHERE id = $1 AND failures + 1 >= $2`, id, maxFailures)
	if err != nil || tag.RowsAffected() > 0 {
		return tag.RowsAffected() > 0, err
	}
	_, err = s.pool.Exec(ctx, `UPDATE push_subscriptions SET failures = failures + 1 WHERE id = $1`, id)
	return false, err
}

// DeleteSubscriptionByID removes a subscription the push service reported as gone.
func (s *Store) DeleteSubscriptionByID(ctx context.Context, id string) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM push_subscriptions WHERE id = $1`, id)
	return err
}
