package api

import (
	"crypto/subtle"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/config"
)

// Authenticator maps a bearer token to a device name. The token decides the device; callers cannot choose.
type Authenticator struct{ entries []config.TokenEntry }

func NewAuthenticator(entries []config.TokenEntry) *Authenticator { return &Authenticator{entries} }

// Device returns the device for the request's bearer token. With no tokens configured it always fails.
func (a *Authenticator) Device(r *http.Request) (string, bool) {
	scheme, token, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	if !ok || !strings.EqualFold(scheme, "bearer") || token == "" {
		return "", false
	}
	device, found := "", false
	for _, e := range a.entries { // check every entry so timing does not reveal which one matched
		if subtle.ConstantTimeCompare([]byte(token), []byte(e.Token)) == 1 {
			device, found = e.Device, true
		}
	}
	return device, found
}

// limiter is a small per-key token bucket guarding the ingest endpoints.
type limiter struct {
	mu      sync.Mutex
	buckets map[string]*bucket
	burst   float64
	perSec  float64
	now     func() time.Time
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newLimiter(burst int, perMinute int) *limiter {
	return &limiter{buckets: map[string]*bucket{}, burst: float64(burst), perSec: float64(perMinute) / 60, now: time.Now}
}

// allow reports whether key may proceed; if not, retryAfter is the seconds until a token is available.
func (l *limiter) allow(key string) (ok bool, retryAfter int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b, found := l.buckets[key]
	if !found {
		b = &bucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens += now.Sub(b.last).Seconds() * l.perSec
	if b.tokens > l.burst {
		b.tokens = l.burst
	}
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, int((1-b.tokens)/l.perSec) + 1
}
