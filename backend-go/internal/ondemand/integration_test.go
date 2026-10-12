package ondemand

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/store"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/testdb"
	"github.com/jackc/pgx/v5/pgxpool"
)

// fakeRunner stands in for the Ookla CLI. run decides what a test does.
type fakeRunner struct {
	run   func(ctx context.Context, emit func(Progress)) ([]byte, error)
	calls atomic.Int32
}

func (f *fakeRunner) Run(ctx context.Context, emit func(Progress)) ([]byte, error) {
	f.calls.Add(1)
	return f.run(ctx, emit)
}

// realResult is the final line of a real Ookla run (sanitized): 99.2 Mbps down, 49.1 Mbps up, 2% packet loss.
func realResult(t *testing.T) []byte {
	t.Helper()
	lines := readFixture(t)
	return lines[len(lines)-1]
}

func finishesWith(raw []byte) *fakeRunner {
	return &fakeRunner{run: func(ctx context.Context, emit func(Progress)) ([]byte, error) {
		f := 0.5
		emit(Progress{Type: "progress", Phase: "ping", Fraction: &f})
		mbps := 90.0
		emit(Progress{Type: "progress", Phase: "download", CurrentMbps: &mbps, Fraction: &f})
		emit(Progress{Type: "progress", Phase: "upload", CurrentMbps: &mbps, Fraction: &f})
		return raw, nil
	}}
}

// blocks runs until released (returning raw) or cancelled.
func blocks(raw []byte) (*fakeRunner, chan struct{}) {
	release := make(chan struct{})
	return &fakeRunner{run: func(ctx context.Context, emit func(Progress)) ([]byte, error) {
		select {
		case <-release:
			return raw, nil
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}}, release
}

type rig struct {
	t    *testing.T
	pool *pgxpool.Pool
	db   *store.Store
	m    *Manager
	ctx  context.Context
}

func newEnv(t *testing.T, runner Runner, mutate func(*Config)) *rig {
	t.Helper()
	pool := testdb.Pool(t)
	cfg := Defaults()
	cfg.OoklaAccepted = true
	cfg.CreatePerMinute = 600
	if mutate != nil {
		mutate(&cfg)
	}
	db := store.New(pool)
	m := NewManager(cfg, db, runner, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.lookPath = func(string) (string, error) { return "/usr/bin/speedtest", nil }
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	m.Start(ctx)
	return &rig{t: t, pool: pool, db: db, m: m, ctx: ctx}
}

func (e *rig) exec(q string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(e.ctx, q, args...); err != nil {
		e.t.Fatal(err)
	}
}

func (e *rig) count(q string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.pool.QueryRow(e.ctx, q, args...).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func collect(t *testing.T, sub Subscription) []Event {
	t.Helper()
	events := append([]Event(nil), sub.Replay...)
	if sub.Live == nil {
		return events
	}
	timeout := time.After(10 * time.Second)
	for {
		select {
		case ev, ok := <-sub.Live:
			if !ok {
				return events
			}
			events = append(events, ev)
		case <-timeout:
			t.Fatalf("stream did not end; got %d events", len(events))
		}
	}
}

func names(evs []Event) []string {
	var out []string
	for _, e := range evs {
		out = append(out, e.Name)
	}
	return out
}

func waitState(t *testing.T, e *rig, id, want string) {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if row, err := e.db.GetTest(e.ctx, id); err == nil && row.State == want {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	row, _ := e.db.GetTest(e.ctx, id)
	t.Fatalf("test %s never reached %q; it is %q", id, want, row.State)
}

func server(t *testing.T, e *rig) string {
	t.Helper()
	test, err := e.m.Create(e.ctx, CreateRequest{Provider: "ookla", Mode: "server", ConnectionType: "wired", ClientIP: "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	return test.ID
}

func TestServerTestHappyPath(t *testing.T) {
	e := newEnv(t, finishesWith(realResult(t)), nil)
	test, err := e.m.Create(e.ctx, CreateRequest{Provider: "ookla", Mode: "server", ConnectionType: "wireless", Device: "ignored", ClientIP: "192.0.2.1"})
	if err != nil {
		t.Fatal(err)
	}
	if test.State != "queued" || test.Device != "server" || test.ConnectionType != "wired" || test.SubmitToken != "" || test.Provider != "ookla" {
		t.Fatalf("a server test uses the server's own name and connection, and has no token: %+v", test)
	}
	events := collect(t, e.m.Subscribe(test.ID, 0))
	got := strings.Join(names(events), ",")
	if got != "state,state,progress,progress,progress,state,result" {
		t.Fatalf("event order: %s", got)
	}
	for i, ev := range events {
		if ev.ID != int64(i+1) {
			t.Errorf("ids are 1,2,3...: event %d has id %d", i, ev.ID)
		}
	}
	if !bytes.Contains(events[0].Data, []byte(`"state":"queued"`)) || !bytes.Contains(events[1].Data, []byte(`"state":"running"`)) ||
		!bytes.Contains(events[5].Data, []byte(`"state":"completed"`)) {
		t.Errorf("states: %s | %s | %s", events[0].Data, events[1].Data, events[5].Data)
	}
	if !bytes.Contains(events[6].Data, []byte(`"downloadMbps":99.2`)) || !bytes.Contains(events[6].Data, []byte(`"trigger":"on_demand"`)) {
		t.Errorf("result event: %s", events[6].Data)
	}

	row, _ := e.db.GetTest(e.ctx, test.ID)
	if row.State != "completed" || row.ResultID == nil || row.FinishedAt == nil || row.StartedAt == nil || row.Error != nil {
		t.Errorf("stored test: %+v", row)
	}
	res, err := e.db.GetResult(e.ctx, *row.ResultID)
	if err != nil || res.Provider != "ookla" || res.Trigger != "on_demand" || res.Device != "server" || res.ConnectionType != "wired" {
		t.Errorf("stored result: %+v %v", res, err)
	}
	if res.DownloadMbps < 99.2 || res.DownloadMbps > 99.3 || res.PacketLossPct == nil || *res.PacketLossPct < 2 {
		t.Errorf("numbers: %+v", res)
	}
	// a finished test can be listened to later, and cancelling it is a harmless no-op
	late := collect(t, e.m.Subscribe(test.ID, 0))
	if len(late) != 7 {
		t.Errorf("a late listener gets the whole history: %v", names(late))
	}
	if again, err := e.m.Cancel(e.ctx, test.ID); err != nil || again.State != "completed" {
		t.Errorf("cancel after completion: %+v %v", again, err)
	}
}

func TestOnlyOneServerTestAtATime(t *testing.T) {
	r, release := blocks(realResult(t))
	e := newEnv(t, r, nil)
	first := server(t, e)
	waitState(t, e, first, "running")
	for i := 0; i < 200 && r.calls.Load() < 1; i++ { // "running" is recorded just before the process is launched
		time.Sleep(10 * time.Millisecond)
	}
	_, err := e.m.Create(e.ctx, CreateRequest{Provider: "ookla", Mode: "server", ConnectionType: "wired"})
	if !errors.Is(err, store.ErrServerBusy) {
		t.Fatalf("second test while one runs: %v", err)
	}
	if r.calls.Load() != 1 {
		t.Errorf("the second test must never start a process: %d", r.calls.Load())
	}
	close(release)
	waitState(t, e, first, "completed")
	// the slot is free again
	r2 := finishesWith(realResult(t))
	e.m.runner = r2
	second := server(t, e)
	waitState(t, e, second, "completed")
}

func TestConcurrentStartsLetExactlyOneThrough(t *testing.T) {
	r, release := blocks(realResult(t))
	e := newEnv(t, r, nil)
	var wg sync.WaitGroup
	var ok, busy, other atomic.Int32
	for i := 0; i < 12; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := e.m.Create(e.ctx, CreateRequest{Provider: "ookla", Mode: "server", ConnectionType: "wired", ClientIP: "192.0.2.9"})
			switch {
			case err == nil:
				ok.Add(1)
			case errors.Is(err, store.ErrServerBusy):
				busy.Add(1)
			default:
				other.Add(1)
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if ok.Load() != 1 || busy.Load() != 11 || other.Load() != 0 {
		t.Fatalf("the database decides the race: ok=%d busy=%d other=%d", ok.Load(), busy.Load(), other.Load())
	}
	close(release)
	if e.count(`SELECT count(*) FROM tests WHERE state IN ('queued','running')`) > 1 {
		t.Error("never more than one active server test")
	}
}

func TestCancelStopsARunningTest(t *testing.T) {
	r, _ := blocks(realResult(t))
	e := newEnv(t, r, nil)
	id := server(t, e)
	waitState(t, e, id, "running")
	sub := e.m.Subscribe(id, 0)

	got, err := e.m.Cancel(e.ctx, id)
	if err != nil || got.State != "cancelled" || got.FinishedAt == nil || got.ResultID != nil {
		t.Fatalf("cancel returns the final state: %+v %v", got, err)
	}
	events := collect(t, sub)
	last := events[len(events)-1]
	if last.Name != "state" || !bytes.Contains(last.Data, []byte(`"state":"cancelled"`)) {
		t.Errorf("the stream ends with the cancelled state: %v", names(events))
	}
	for _, ev := range events {
		if ev.Name == "result" || ev.Name == "error" {
			t.Errorf("a cancelled test has no result and no error: %v", names(events))
		}
	}
	if n := e.count(`SELECT count(*) FROM speedtests`); n != 0 {
		t.Errorf("nothing is stored for a cancelled test: %d", n)
	}
	// the slot is free straight away
	e.m.runner = finishesWith(realResult(t))
	waitState(t, e, server(t, e), "completed")
}

func TestFailureIsReportedWithAReason(t *testing.T) {
	e := newEnv(t, &fakeRunner{run: func(ctx context.Context, emit func(Progress)) ([]byte, error) {
		return nil, errors.New("the Ookla speed test failed: Network is unreachable")
	}}, nil)
	id := server(t, e)
	events := collect(t, e.m.Subscribe(id, 0))
	last := events[len(events)-1]
	if last.Name != "error" || !bytes.Contains(last.Data, []byte("Network is unreachable")) {
		t.Fatalf("%v: %s", names(events), last.Data)
	}
	row, _ := e.db.GetTest(e.ctx, id)
	if row.State != "failed" || row.Error == nil || !strings.Contains(*row.Error, "Network is unreachable") || row.ResultID != nil {
		t.Errorf("%+v", row)
	}
	if n := e.count(`SELECT count(*) FROM speedtests`); n != 0 {
		t.Errorf("no result for a failed test: %d", n)
	}
}

func TestTestsThatRunTooLongAreStopped(t *testing.T) {
	r, _ := blocks(realResult(t))
	e := newEnv(t, r, func(c *Config) { c.TestTimeout = 300 * time.Millisecond })
	id := server(t, e)
	waitState(t, e, id, "failed")
	row, _ := e.db.GetTest(e.ctx, id)
	if row.Error == nil || !strings.Contains(*row.Error, "took longer than") {
		t.Errorf("%+v", row.Error)
	}
}

func TestAResultTheDatabaseRejectsFailsTheTestCleanly(t *testing.T) {
	e := newEnv(t, finishesWith([]byte(`{"type":"result","timestamp":"2026-10-11T12:00:00Z","download":{"bandwidth":1e30},"upload":{"bandwidth":1}}`)), nil)
	id := server(t, e)
	waitState(t, e, id, "failed")
	row, _ := e.db.GetTest(e.ctx, id)
	if row.Error == nil || !strings.Contains(*row.Error, "could not be saved") {
		t.Errorf("a person-readable reason, not a SQL error: %v", row.Error)
	}
}

func TestOoklaStaysOffUntilTheTermsAreAccepted(t *testing.T) {
	r := finishesWith(realResult(t))
	e := newEnv(t, r, func(c *Config) { c.OoklaAccepted = false })
	_, err := e.m.Create(e.ctx, CreateRequest{Provider: "ookla", Mode: "server", ConnectionType: "wired"})
	var unav UnavailableError
	if !errors.As(err, &unav) || !strings.Contains(unav.Reason, "OOKLA_ACCEPT_LICENSE=yes") {
		t.Fatalf("%v", err)
	}
	if r.calls.Load() != 0 || e.count(`SELECT count(*) FROM tests`) != 0 {
		t.Error("nothing must start or be recorded without acceptance")
	}
	caps := e.m.Capabilities()
	if caps.Ookla.Available || caps.Ookla.Reason == nil || !caps.NDT7.Available || caps.DeviceName != "server" {
		t.Errorf("%+v", caps)
	}
	// NDT7 in a browser has no such gate
	if _, err := e.m.Create(e.ctx, CreateRequest{Provider: "ndt7", Mode: "browser", ConnectionType: "wireless", Device: "My phone"}); err != nil {
		t.Errorf("browser tests are unaffected: %v", err)
	}
}

func TestValidation(t *testing.T) {
	e := newEnv(t, finishesWith(realResult(t)), nil)
	cases := map[string]struct {
		req   CreateRequest
		field string
	}{
		"unknown provider":   {CreateRequest{Provider: "speedof", Mode: "server", ConnectionType: "wired"}, "provider"},
		"unknown mode":       {CreateRequest{Provider: "ookla", Mode: "cloud"}, "mode"},
		"ndt7 on the server": {CreateRequest{Provider: "ndt7", Mode: "server", ConnectionType: "wired"}, "provider"},
		"ookla in a browser": {CreateRequest{Provider: "ookla", Mode: "browser", ConnectionType: "wired", Device: "x"}, "provider"},
		"no connection type": {CreateRequest{Provider: "ndt7", Mode: "browser", Device: "x"}, "connectionType"},
		"server test needs a valid connection type too": {CreateRequest{Provider: "ookla", Mode: "server"}, "connectionType"},
		"server test with a junk device name":           {CreateRequest{Provider: "ookla", Mode: "server", ConnectionType: "wired", Device: strings.Repeat("x", 65)}, "device"},
		"bad connection":                                {CreateRequest{Provider: "ndt7", Mode: "browser", ConnectionType: "lte", Device: "x"}, "connectionType"},
		"no device":                                     {CreateRequest{Provider: "ndt7", Mode: "browser", ConnectionType: "wired"}, "device"},
		"control characters":                            {CreateRequest{Provider: "ndt7", Mode: "browser", ConnectionType: "wired", Device: "a\x00b"}, "device"},
		"too long":                                      {CreateRequest{Provider: "ndt7", Mode: "browser", ConnectionType: "wired", Device: strings.Repeat("x", 65)}, "device"},
	}
	for name, c := range cases {
		_, err := e.m.Create(e.ctx, c.req)
		var v ValidationError
		if !errors.As(err, &v) || v.Field != c.field {
			t.Errorf("%s: want a validation error on %q, got %v", name, c.field, err)
		}
	}
	if e.count(`SELECT count(*) FROM tests`) != 0 {
		t.Error("invalid requests create nothing")
	}
	// an authenticated agent's token decides the device name
	test, err := e.m.Create(e.ctx, CreateRequest{Provider: "ndt7", Mode: "browser", ConnectionType: "wired", Device: "spoofed", TokenDevice: "laptop"})
	if err != nil || test.Device != "laptop" {
		t.Errorf("token decides the device: %+v %v", test, err)
	}
}

func TestStartingTestsIsRateLimitedPerAddress(t *testing.T) {
	e := newEnv(t, finishesWith(realResult(t)), func(c *Config) { c.CreatePerMinute = 1 })
	req := CreateRequest{Provider: "ndt7", Mode: "browser", ConnectionType: "wired", Device: "d", ClientIP: "198.51.100.1"}
	for i := 0; i < 4; i++ { // burst of 4
		if _, err := e.m.Create(e.ctx, req); err != nil {
			t.Fatalf("burst %d: %v", i, err)
		}
	}
	var limited RateLimitedError
	if _, err := e.m.Create(e.ctx, req); !errors.As(err, &limited) || limited.RetryAfter < 1 {
		t.Errorf("fifth: %v", err)
	}
	req.ClientIP = "198.51.100.2"
	if _, err := e.m.Create(e.ctx, req); err != nil {
		t.Errorf("another address: %v", err)
	}
}

const goodSummary = `{"startedAt":"%s","finishedAt":"%s","downloadMbps":87.5,"uploadMbps":23.25,"minRttMs":18.4,"retransmissionPct":0.31,
 "server":{"machine":"mlab1-lax05","location":"Los Angeles","country":"US"},"raw":{"note":"x"}}`

func summary(offset time.Duration) []byte {
	now := time.Now().UTC().Add(offset)
	s := strings.Replace(goodSummary, "%s", now.Add(-20*time.Second).Format(time.RFC3339), 1)
	return []byte(strings.Replace(s, "%s", now.Format(time.RFC3339), 1))
}

func TestBrowserTestFlowAndTheSingleUseToken(t *testing.T) {
	e := newEnv(t, finishesWith(realResult(t)), nil)
	test, err := e.m.Create(e.ctx, CreateRequest{Provider: "ndt7", Mode: "browser", ConnectionType: "wireless", Device: "My phone", ClientIP: "203.0.113.50"})
	if err != nil {
		t.Fatal(err)
	}
	if test.State != "running" || test.StartedAt == nil || len(test.SubmitToken) < 40 || test.Device != "My phone" || test.Provider != "ndt7" {
		t.Fatalf("%+v", test)
	}
	// only a hash of the token is stored
	var stored []byte
	_ = e.pool.QueryRow(e.ctx, `SELECT submit_token_hash FROM tests WHERE id = $1::uuid`, test.ID).Scan(&stored)
	if len(stored) != 32 || bytes.Contains(stored, []byte(test.SubmitToken)) || !bytes.Equal(stored, hashToken(test.SubmitToken)) {
		t.Errorf("stored token hash: %x", stored)
	}
	// a re-read of the test never reveals the token
	if again, _ := e.m.Get(e.ctx, test.ID); again.SubmitToken != "" {
		t.Error("the token is shown once, at creation")
	}

	// a wrong token is refused and does not burn the right one
	if _, err := e.m.Submit(e.ctx, test.ID, "wrong-token", summary(0), "203.0.113.50"); !errors.Is(err, store.ErrTokenInvalid) {
		t.Fatalf("wrong token: %v", err)
	}
	if row, _ := e.db.GetTest(e.ctx, test.ID); row.State != "running" {
		t.Fatalf("still running after a bad attempt: %s", row.State)
	}

	res, err := e.m.Submit(e.ctx, test.ID, test.SubmitToken, summary(0), "203.0.113.50")
	if err != nil {
		t.Fatal(err)
	}
	if res.Provider != "ndt7" || res.Device != "My phone" || res.ConnectionType != "wireless" || res.Trigger != "on_demand" ||
		res.DownloadMbps != 87.5 || res.UploadMbps != 23.25 || res.LatencyMs == nil || *res.LatencyMs != 18.4 {
		t.Errorf("%+v", res)
	}
	var ip string
	_ = e.pool.QueryRow(e.ctx, `SELECT host(interface_external_ip) FROM speedtests WHERE id = $1::uuid`, res.ID).Scan(&ip)
	if ip != "203.0.113.50" {
		t.Errorf("the address the server saw is recorded: %q", ip)
	}
	row, _ := e.db.GetTest(e.ctx, test.ID)
	if row.State != "completed" || row.ResultID == nil || *row.ResultID != res.ID {
		t.Errorf("%+v", row)
	}
	var hash []byte
	_ = e.pool.QueryRow(e.ctx, `SELECT submit_token_hash FROM tests WHERE id = $1::uuid`, test.ID).Scan(&hash)
	if hash != nil {
		t.Error("the token is spent")
	}

	// the same token cannot be used twice
	_, err = e.m.Submit(e.ctx, test.ID, test.SubmitToken, summary(0), "203.0.113.50")
	var closed store.ClosedError
	if !errors.As(err, &closed) || closed.State != "completed" {
		t.Errorf("second submit: %v", err)
	}
	if n := e.count(`SELECT count(*) FROM speedtests`); n != 1 {
		t.Errorf("exactly one result: %d", n)
	}
	if _, err := e.m.Submit(e.ctx, "00000000-0000-0000-0000-000000000000", "x", summary(0), ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown test: %v", err)
	}
	if _, err := e.m.Submit(e.ctx, "not-a-uuid", "x", summary(0), ""); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("malformed id: %v", err)
	}
}

func TestATokenIsNotSpentByARejectedResult(t *testing.T) {
	e := newEnv(t, nil, nil)
	test, _ := e.m.Create(e.ctx, CreateRequest{Provider: "ndt7", Mode: "browser", ConnectionType: "wired", Device: "pc"})
	bad := bytes.Replace(summary(0), []byte(`"downloadMbps":87.5`), []byte(`"downloadMbps":1e30`), 1) // the API validates this; the database is the backstop
	if _, err := e.m.Submit(e.ctx, test.ID, test.SubmitToken, bad, ""); err == nil {
		t.Fatal("the database must refuse an absurd speed")
	}
	if row, _ := e.db.GetTest(e.ctx, test.ID); row.State != "running" || row.ResultID != nil {
		t.Fatalf("the failed attempt changed nothing (one transaction): %+v", row)
	}
	if n := e.count(`SELECT count(*) FROM speedtests`); n != 0 {
		t.Fatalf("no partial result: %d", n)
	}
	if _, err := e.m.Submit(e.ctx, test.ID, test.SubmitToken, summary(0), ""); err != nil {
		t.Errorf("the browser can retry with corrected data: %v", err)
	}
}

func TestBrowserTestsExpire(t *testing.T) {
	e := newEnv(t, nil, func(c *Config) { c.BrowserTTL = time.Second })
	test, _ := e.m.Create(e.ctx, CreateRequest{Provider: "ndt7", Mode: "browser", ConnectionType: "wired", Device: "pc"})
	time.Sleep(1300 * time.Millisecond)
	if _, err := e.m.Submit(e.ctx, test.ID, test.SubmitToken, summary(0), ""); !errors.Is(err, store.ErrTestExpired) {
		t.Fatalf("a late result is refused: %v", err)
	}
	n, err := e.db.SweepTests(e.ctx, time.Second, time.Hour)
	if err != nil || n != 1 {
		t.Fatalf("sweep: %d %v", n, err)
	}
	row, _ := e.db.GetTest(e.ctx, test.ID)
	if row.State != "failed" || row.Error == nil || !strings.Contains(*row.Error, "not finished in time") {
		t.Errorf("%+v", row)
	}
	if _, err := e.m.Submit(e.ctx, test.ID, test.SubmitToken, summary(0), ""); err == nil {
		t.Error("an expired test stays closed")
	}
}

func TestCancelABrowserTest(t *testing.T) {
	e := newEnv(t, nil, nil)
	test, _ := e.m.Create(e.ctx, CreateRequest{Provider: "ndt7", Mode: "browser", ConnectionType: "wired", Device: "pc"})
	got, err := e.m.Cancel(e.ctx, test.ID)
	if err != nil || got.State != "cancelled" {
		t.Fatalf("%+v %v", got, err)
	}
	var closed store.ClosedError
	if _, err := e.m.Submit(e.ctx, test.ID, test.SubmitToken, summary(0), ""); !errors.As(err, &closed) || closed.State != "cancelled" {
		t.Errorf("a cancelled test takes no result: %v", err)
	}
	if _, err := e.m.Cancel(e.ctx, "00000000-0000-0000-0000-000000000000"); !errors.Is(err, store.ErrNotFound) {
		t.Errorf("unknown: %v", err)
	}
}

func TestStartRecoversTestsLeftByACrash(t *testing.T) {
	e := newEnv(t, nil, nil)
	e.exec(`INSERT INTO tests (provider, mode, device, connection_type, state, started_at) VALUES ('ookla','server','server','wired','running', now())`)
	e.exec(`INSERT INTO tests (provider, mode, device, connection_type, state, finished_at) VALUES ('ookla','server','server','wired','completed', now())`)
	// the stuck row blocks new tests...
	if _, err := e.m.Create(e.ctx, CreateRequest{Provider: "ookla", Mode: "server", ConnectionType: "wired"}); !errors.Is(err, store.ErrServerBusy) {
		t.Fatalf("before recovery: %v", err)
	}
	// ...until the next start clears it
	e.m.Start(e.ctx)
	var msg *string
	_ = e.pool.QueryRow(e.ctx, `SELECT error FROM tests WHERE state = 'failed'`).Scan(&msg)
	if msg == nil || !strings.Contains(*msg, "restarted") {
		t.Errorf("a reason a person can read: %v", msg)
	}
	e.m.runner = finishesWith(realResult(t))
	waitState(t, e, server(t, e), "completed")
	if e.count(`SELECT count(*) FROM tests WHERE state = 'completed'`) != 2 {
		t.Error("finished tests are left alone")
	}
}

func TestListTestsFiltersByState(t *testing.T) {
	r, release := blocks(realResult(t))
	e := newEnv(t, r, nil)
	id := server(t, e)
	waitState(t, e, id, "running")
	e.m.Create(e.ctx, CreateRequest{Provider: "ndt7", Mode: "browser", ConnectionType: "wired", Device: "pc"}) //nolint:errcheck
	running, _ := e.m.List(e.ctx, []string{"running"}, 10)
	all, _ := e.m.List(e.ctx, nil, 10)
	if len(running) != 2 || len(all) != 2 {
		t.Errorf("running=%d all=%d", len(running), len(all))
	}
	close(release)
	waitState(t, e, id, "completed")
	done, _ := e.m.List(e.ctx, []string{"completed", "failed"}, 10)
	if len(done) != 1 || done[0].ID != id || done[0].ResultID == nil {
		t.Errorf("%+v", done)
	}
	if one, _ := e.m.List(e.ctx, nil, 1); len(one) != 1 {
		t.Error("limit")
	}
}

func TestShutdownFailsARunningTestInsteadOfLeavingItStuck(t *testing.T) {
	r, _ := blocks(realResult(t))
	e := newEnv(t, r, nil)
	rootCtx, stop := context.WithCancel(context.Background())
	e.m.Start(rootCtx)
	id := server(t, e)
	waitState(t, e, id, "running")
	stop() // the server is shutting down
	waitState(t, e, id, "failed")
	row, _ := e.db.GetTest(context.Background(), id)
	if row.Error == nil || !strings.Contains(*row.Error, "shutting down") {
		t.Errorf("%v", row.Error)
	}
}
