package login

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// This vector is also in backend-rust/src/login.rs: the two backends must make and accept the very same cookies.
const (
	vectorPassword = "correct horse battery"
	vectorExpiry   = 2000000000
	vectorCookie   = "s1.2000000000.qC_Eurww35muXn2znWnJ2QbyZMG-OvSOfLDd4S7Lkqs"
)

func vectorKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i)
	}
	return k
}

// newAt builds a service whose clock reads at (unix seconds).
func newAt(t *testing.T, password string, lifetime time.Duration, key []byte, at int64) *Service {
	t.Helper()
	s, err := NewWithKey(Config{Password: password, Lifetime: lifetime}, key)
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return time.Unix(at, 0) }
	return s
}

func TestCookieVector(t *testing.T) {
	s := newAt(t, vectorPassword, 24*time.Hour, vectorKey(), vectorExpiry-3600)
	if got := s.cookieFor(vectorExpiry); got != vectorCookie {
		t.Fatalf("cookie = %q, want %q", got, vectorCookie)
	}
	if !s.Valid(vectorCookie) {
		t.Error("the vector cookie is refused")
	}
}

func TestIssueAndValid(t *testing.T) {
	s := newAt(t, "a good password", 7*24*time.Hour, vectorKey(), 1_800_000_000)
	v, exp := s.Issue()
	if want := time.Unix(1_800_000_000, 0).Add(7 * 24 * time.Hour); !exp.Equal(want) {
		t.Errorf("expires %v, want %v", exp, want)
	}
	if !s.Valid(v) {
		t.Fatalf("a fresh cookie %q is refused", v)
	}
	// still valid just before it expires, refused from the second it does
	s.now = func() time.Time { return exp.Add(-time.Second) }
	if !s.Valid(v) {
		t.Error("refused one second before it expires")
	}
	s.now = func() time.Time { return exp }
	if s.Valid(v) {
		t.Error("accepted at its expiry")
	}
}

func TestInvalidCookies(t *testing.T) {
	s := newAt(t, vectorPassword, 24*time.Hour, vectorKey(), vectorExpiry-3600)
	mac := strings.TrimPrefix(vectorCookie, "s1.2000000000.")
	flipped := []byte(mac)
	flipped[0] ^= 1 // still base64url, one bit different
	for name, c := range map[string]string{
		"empty":             "",
		"no dots":           "s1",
		"two parts":         "s1.2000000000",
		"four parts":        vectorCookie + ".x",
		"another version":   "s2.2000000000." + mac,
		"later expiry":      "s1.2000003600." + mac,
		"flipped signature": "s1.2000000000." + string(flipped),
		"empty signature":   "s1.2000000000.",
		"plus sign":         "s1.+2000000000." + mac,
		"leading zero":      "s1.02000000000." + mac,
		"not a number":      "s1.soon." + mac,
		"too long":          "s1.2000000000." + strings.Repeat("A", 200),
		"padded signature":  vectorCookie + "=",
	} {
		if s.Valid(c) {
			t.Errorf("%s: %q was accepted", name, c)
		}
	}
}

func TestACookieBeyondTheCurrentLifetimeIsRefused(t *testing.T) {
	// made when sessions lasted 30 days, checked after DASHBOARD_SESSION_DAYS was set to 1
	long := newAt(t, vectorPassword, 30*24*time.Hour, vectorKey(), 1_800_000_000)
	v, _ := long.Issue()
	short := newAt(t, vectorPassword, 24*time.Hour, vectorKey(), 1_800_000_000)
	if short.Valid(v) {
		t.Error("a 30-day cookie was accepted by a server that now allows 1 day")
	}
}

func TestChangingThePasswordOrTheKeyEndsEverySession(t *testing.T) {
	a := newAt(t, "first password", time.Hour, vectorKey(), 1_800_000_000)
	v, _ := a.Issue()
	if !a.Valid(v) {
		t.Fatal("setup")
	}
	if b := newAt(t, "second password", time.Hour, vectorKey(), 1_800_000_000); b.Valid(v) {
		t.Error("a session survived a password change")
	}
	other := vectorKey()
	other[31] ^= 0xff
	if c := newAt(t, "first password", time.Hour, other, 1_800_000_000); c.Valid(v) {
		t.Error("a cookie signed with another database key was accepted")
	}
}

func TestCheckPassword(t *testing.T) {
	s := newAt(t, vectorPassword, time.Hour, vectorKey(), 0)
	if !s.CheckPassword(vectorPassword) {
		t.Error("the right password is refused")
	}
	for _, bad := range []string{"", "correct horse", vectorPassword + " ", strings.ToUpper(vectorPassword), strings.Repeat("x", 10_000)} {
		if s.CheckPassword(bad) {
			t.Errorf("%.20q was accepted", bad)
		}
	}
}

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestFromEnv(t *testing.T) {
	if _, on, err := FromEnv(env(nil)); on || err != nil {
		t.Errorf("no password: on=%v err=%v", on, err)
	}
	cfg, on, err := FromEnv(env(map[string]string{"DASHBOARD_PASSWORD": "long enough"}))
	if !on || err != nil || cfg.Lifetime != 30*24*time.Hour {
		t.Errorf("defaults: %+v on=%v err=%v", cfg, on, err)
	}
	cfg, on, err = FromEnv(env(map[string]string{"DASHBOARD_PASSWORD": "long enough", "DASHBOARD_SESSION_DAYS": " 7 "}))
	if !on || err != nil || cfg.Lifetime != 7*24*time.Hour {
		t.Errorf("7 days: %+v on=%v err=%v", cfg, on, err)
	}
	for name, m := range map[string]map[string]string{
		"short password":  {"DASHBOARD_PASSWORD": "short"},
		"huge password":   {"DASHBOARD_PASSWORD": strings.Repeat("x", 201)},
		"zero days":       {"DASHBOARD_PASSWORD": "long enough", "DASHBOARD_SESSION_DAYS": "0"},
		"too many days":   {"DASHBOARD_PASSWORD": "long enough", "DASHBOARD_SESSION_DAYS": "366"},
		"days not number": {"DASHBOARD_PASSWORD": "long enough", "DASHBOARD_SESSION_DAYS": "a week"},
	} {
		if _, _, err := FromEnv(env(m)); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	// characters, not bytes: eight multi-byte letters are fine
	if _, on, err := FromEnv(env(map[string]string{"DASHBOARD_PASSWORD": "ééééééééé"})); !on || err != nil {
		t.Errorf("multi-byte password: on=%v err=%v", on, err)
	}
}

type memRepo struct {
	key    []byte
	saves  int
	getErr error
	rival  []byte // a key another server stores between our read and our save
}

func (m *memRepo) SessionKey(context.Context) ([]byte, bool, error) {
	return m.key, m.key != nil, m.getErr
}
func (m *memRepo) SaveSessionKey(_ context.Context, k []byte) error {
	m.saves++
	if m.key == nil { // ON CONFLICT DO NOTHING
		m.key = k
		if m.rival != nil {
			m.key = m.rival
		}
	}
	return nil
}

func TestNewCreatesTheKeyOnceAndReusesIt(t *testing.T) {
	repo := &memRepo{}
	cfg := Config{Password: "long enough", Lifetime: time.Hour}
	a, err := New(context.Background(), cfg, repo)
	if err != nil || repo.saves != 1 || len(repo.key) != 32 {
		t.Fatalf("first start: err=%v saves=%d key=%d", err, repo.saves, len(repo.key))
	}
	v, _ := a.Issue()
	b, err := New(context.Background(), cfg, repo)
	if err != nil || repo.saves != 1 {
		t.Fatalf("second start: err=%v saves=%d", err, repo.saves)
	}
	if !b.Valid(v) {
		t.Error("a restart signed everybody out")
	}
}

func TestNewUsesTheKeyAnotherServerStoredFirst(t *testing.T) {
	rival := bytes.Repeat([]byte{7}, 32)
	repo := &memRepo{rival: rival}
	cfg := Config{Password: "long enough", Lifetime: time.Hour}
	a, err := New(context.Background(), cfg, repo)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := NewWithKey(cfg, rival)
	v, _ := a.Issue()
	if !b.Valid(v) {
		t.Error("the server did not end up with the stored key")
	}
}

func TestNewReportsADatabaseWithoutTheTable(t *testing.T) {
	_, err := New(context.Background(), Config{Password: "long enough"}, &memRepo{getErr: errors.New("relation does not exist")})
	if err == nil || !strings.Contains(err.Error(), "009_session_key.sql") {
		t.Errorf("err = %v", err)
	}
}

func TestShortKeysAreRefused(t *testing.T) {
	if _, err := NewWithKey(Config{Password: "long enough"}, make([]byte, 31)); err == nil {
		t.Error("a 31-byte key was accepted")
	}
}
