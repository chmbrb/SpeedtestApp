// Package login is the optional dashboard login: one shared password (DASHBOARD_PASSWORD) and signed session cookies. With no
// password set nothing here is used and the dashboard is open, as it always was.
//
// A session is a stateless cookie, "s1.<expiry as unix seconds>.<signature>". The signature is HMAC-SHA256 over "s1.<expiry>" with
// a signing key made from a random key kept in the database (sql/postgres/009_session_key.sql) and the password: so nobody can make
// a cookie without the database key, a stolen cookie cannot be used to guess the password, and changing the password signs everybody
// out. The Go and the Rust backend produce and accept exactly the same cookies (the same test vector is in both test suites).
package login

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// CookieName is the session cookie.
const CookieName = "speedtest_session"

const (
	minPasswordChars = 8
	maxPasswordBytes = 200
	defaultDays      = 30
	maxDays          = 365
	// skew is how far a cookie's expiry may lie beyond "now + lifetime" before it is refused (clock steps, a restart).
	skew = time.Minute
	// maxCookie bounds what Valid will even look at.
	maxCookie = 128
)

// Config is read from DASHBOARD_* environment variables.
type Config struct {
	Password string
	Lifetime time.Duration
}

// FromEnv reads DASHBOARD_PASSWORD (empty: the login is off) and DASHBOARD_SESSION_DAYS (default 30, 1 to 365).
func FromEnv(getenv func(string) string) (cfg Config, enabled bool, err error) {
	cfg.Password = getenv("DASHBOARD_PASSWORD")
	if cfg.Password == "" {
		return cfg, false, nil
	}
	if utf8.RuneCountInString(cfg.Password) < minPasswordChars || len(cfg.Password) > maxPasswordBytes {
		return cfg, false, fmt.Errorf("DASHBOARD_PASSWORD must be %d to %d characters", minPasswordChars, maxPasswordBytes)
	}
	days := defaultDays
	if v := strings.TrimSpace(getenv("DASHBOARD_SESSION_DAYS")); v != "" {
		n, convErr := strconv.Atoi(v)
		if convErr != nil || n < 1 || n > maxDays {
			return cfg, false, fmt.Errorf("DASHBOARD_SESSION_DAYS must be a whole number from 1 to %d", maxDays)
		}
		days = n
	}
	cfg.Lifetime = time.Duration(days) * 24 * time.Hour
	return cfg, true, nil
}

// Repo is where the random signing key lives.
type Repo interface {
	SessionKey(ctx context.Context) (key []byte, ok bool, err error)
	SaveSessionKey(ctx context.Context, key []byte) error
}

// Service checks the password and makes and checks session cookies.
type Service struct {
	lifetime time.Duration
	signing  []byte
	pwDigest [sha256.Size]byte
	now      func() time.Time
}

// New loads the signing key from the database, creating it on first use.
func New(ctx context.Context, cfg Config, repo Repo) (*Service, error) {
	key, ok, err := repo.SessionKey(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the session key: %w (has sql/postgres/009_session_key.sql been applied?)", err)
	}
	if !ok {
		fresh := make([]byte, 32)
		if _, err := rand.Read(fresh); err != nil {
			return nil, err
		}
		if err := repo.SaveSessionKey(ctx, fresh); err != nil {
			return nil, fmt.Errorf("saving the session key: %w", err)
		}
		// Read it back: if another server stored one first, that is the one to use.
		if key, ok, err = repo.SessionKey(ctx); err != nil || !ok {
			return nil, fmt.Errorf("reading back the session key: %v", err)
		}
	}
	return NewWithKey(cfg, key)
}

// NewWithKey builds a Service from a known database key (tests, and New).
func NewWithKey(cfg Config, dbKey []byte) (*Service, error) {
	if len(dbKey) < 32 {
		return nil, errors.New("the session key must be at least 32 bytes")
	}
	if cfg.Lifetime <= 0 {
		cfg.Lifetime = defaultDays * 24 * time.Hour
	}
	m := hmac.New(sha256.New, dbKey)
	m.Write([]byte("speedtest login v1|" + cfg.Password))
	return &Service{lifetime: cfg.Lifetime, signing: m.Sum(nil), pwDigest: sha256.Sum256([]byte(cfg.Password)), now: time.Now}, nil
}

// Lifetime is how long a new session lasts.
func (s *Service) Lifetime() time.Duration { return s.lifetime }

// CheckPassword compares in time that does not depend on how much of the password is right.
func (s *Service) CheckPassword(given string) bool {
	d := sha256.Sum256([]byte(given))
	return subtle.ConstantTimeCompare(d[:], s.pwDigest[:]) == 1
}

func (s *Service) sign(payload string) string {
	m := hmac.New(sha256.New, s.signing)
	m.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

// Issue makes a session cookie value that is valid for the configured lifetime.
func (s *Service) Issue() (value string, expires time.Time) {
	expires = s.now().Add(s.lifetime).Truncate(time.Second)
	return s.cookieFor(expires.Unix()), expires
}

func (s *Service) cookieFor(exp int64) string {
	payload := "s1." + strconv.FormatInt(exp, 10)
	return payload + "." + s.sign(payload)
}

// Valid reports whether value is an unexpired session cookie signed by this server for the current password.
func (s *Service) Valid(value string) bool {
	if value == "" || len(value) > maxCookie {
		return false
	}
	parts := strings.Split(value, ".")
	if len(parts) != 3 || parts[0] != "s1" {
		return false
	}
	exp, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || strconv.FormatInt(exp, 10) != parts[1] {
		return false
	}
	want := s.sign(parts[0] + "." + parts[1])
	if !hmac.Equal([]byte(want), []byte(parts[2])) {
		return false
	}
	now := s.now()
	// Expired, or beyond what a session may last now (DASHBOARD_SESSION_DAYS was made shorter since it was issued).
	return exp > now.Unix() && time.Unix(exp, 0).Before(now.Add(s.lifetime+skew))
}
