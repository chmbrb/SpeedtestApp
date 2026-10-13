package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"
)

// SessionKey returns the key that signs login sessions, or ok=false when none has been created yet; see
// sql/postgres/009_session_key.sql.
func (s *Store) SessionKey(ctx context.Context) (key []byte, ok bool, err error) {
	err = s.pool.QueryRow(ctx, `SELECT key FROM session_key`).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	return key, err == nil, err
}

// SaveSessionKey stores the key unless one exists already (two servers starting together must end up with the same one).
func (s *Store) SaveSessionKey(ctx context.Context, key []byte) error {
	_, err := s.pool.Exec(ctx, `INSERT INTO session_key (key) VALUES ($1) ON CONFLICT DO NOTHING`, key)
	return err
}
