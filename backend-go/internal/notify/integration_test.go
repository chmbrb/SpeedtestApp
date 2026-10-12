package notify

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/store"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/testdb"
)

// fakeSender records what would have gone to ntfy.
type fakeSender struct {
	sent []Message
	fail func(n int) error // n = how many sends have been attempted so far (including this one)
	n    int
}

func (f *fakeSender) Send(_ context.Context, m Message) error {
	f.n++
	if f.fail != nil {
		if err := f.fail(f.n); err != nil {
			return err
		}
	}
	f.sent = append(f.sent, m)
	return nil
}

type harness struct {
	t     *testing.T
	ctx   context.Context
	db    *store.Store
	m     *Monitor
	send  *fakeSender
	clock time.Time
	seq   int
	exec  func(q string, args ...any)
}

var t0 = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)

// newHarness seeds 12 results of history (a typical download of 900 Mbps) for server/wired/ookla, then runs the first
// check so the cursor sits at "now". Results added afterwards are the ones that can alert.
func newHarness(t *testing.T, mutate func(*Config)) *harness {
	t.Helper()
	pool := testdb.Pool(t)
	cfg := Defaults()
	cfg.Enabled, cfg.Topic, cfg.ServerURL = true, "secret-topic", "https://ntfy.example.org"
	if mutate != nil {
		mutate(&cfg)
	}
	h := &harness{t: t, ctx: context.Background(), db: store.New(pool), clock: t0, send: &fakeSender{}}
	h.exec = func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(h.ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	h.m = NewMonitor(cfg, h.db, h.send, slog.New(slog.NewTextHandler(io.Discard, nil)))
	h.m.now = func() time.Time { return h.clock }
	for i := 0; i < 12; i++ {
		dl := 880.0
		if i%2 == 0 {
			dl = 920
		}
		h.insert("server", "wired", "ookla", "on_demand", t0.Add(-time.Duration(24-i)*time.Hour), dl, nil)
	}
	h.tick()
	return h
}

func (h *harness) insert(device, ct, provider, trigger string, at time.Time, dl float64, loss *float64) {
	h.t.Helper()
	h.exec(`INSERT INTO speedtests (provider, "trigger", device, connection_type, tested_at, created, download_mbps, upload_mbps,
		latency_ms, packet_loss_pct, raw_data) VALUES ($1,$2,$3,$4,$5,$5,$6,10,10,$7,'{}')`, provider, trigger, device, ct, at, dl, loss)
}

// add stores a result as if it had just arrived (on demand, so the "gone quiet" rule stays out of the way).
func (h *harness) add(dl float64) { h.addFor("server", "wired", "ookla", dl, nil) }

func (h *harness) addFor(device, ct, provider string, dl float64, loss *float64) {
	h.t.Helper()
	h.seq++
	h.insert(device, ct, provider, "on_demand", h.clock.Add(-time.Minute+time.Duration(h.seq)*time.Second), dl, loss)
}

func (h *harness) tick() {
	h.t.Helper()
	if err := h.m.Tick(h.ctx); err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) step(d time.Duration) { h.clock = h.clock.Add(d) }

func (h *harness) titles() []string {
	var out []string
	for _, m := range h.send.sent {
		out = append(out, m.Title)
	}
	return out
}

func (h *harness) expectSent(n int) {
	h.t.Helper()
	if len(h.send.sent) != n {
		h.t.Fatalf("sent %d messages, want %d: %v", len(h.send.sent), n, h.titles())
	}
}

func (h *harness) alert(kind, subject string) *store.AlertState {
	h.t.Helper()
	a, err := h.db.LoadAlert(h.ctx, kind, subject)
	if err != nil {
		h.t.Fatal(err)
	}
	return a
}

const serverSlow = "server|wired|ookla"

func TestMonitorFirstRunIgnoresHistory(t *testing.T) {
	pool := testdb.Pool(t)
	db := store.New(pool)
	ctx := context.Background()
	// a long slow history already in the database when notifications are first switched on
	for i := 0; i < 20; i++ {
		_, err := pool.Exec(ctx, `INSERT INTO speedtests (provider, "trigger", device, connection_type, tested_at, created, download_mbps, upload_mbps, raw_data)
			VALUES ('ookla','on_demand','server','wired',$1,$1,$2,10,'{}')`, t0.Add(-time.Duration(30-i)*time.Hour), 100+float64(i))
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg := Defaults()
	cfg.Enabled, cfg.Topic = true, "t"
	send := &fakeSender{}
	m := NewMonitor(cfg, db, send, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m.now = func() time.Time { return t0 }
	if err := m.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if len(send.sent) != 0 {
		t.Fatalf("history must never trigger alerts: %v", send.sent)
	}
	if firing, _ := db.FiringAlerts(ctx); len(firing) != 0 {
		t.Fatalf("firing: %v", firing)
	}
}

func TestMonitorNeedsTwoSlowTestsInARowThenAlertsOnce(t *testing.T) {
	h := newHarness(t, nil)
	h.step(time.Hour)
	h.add(390) // 43% of typical: slow, but a single slow test is only noise
	h.tick()
	h.expectSent(0)
	if a := h.alert("slow", serverSlow); a == nil || a.Firing || a.Streak != 1 {
		t.Fatalf("one slow test = streak 1, not firing: %+v", a)
	}

	h.step(time.Hour)
	h.add(410)
	h.tick()
	h.expectSent(1)
	m := h.send.sent[0]
	if m.Title != "Slow download: server (wired)" || m.Priority != 4 || len(m.Tags) != 2 || m.Tags[1] != "slow" ||
		!strings.Contains(m.Body, "410 Mbps") || !strings.Contains(m.Body, "(2 tests in a row)") || !strings.Contains(m.Body, "typical 880") {
		t.Errorf("message: %+v", m)
	}
	a := h.alert("slow", serverSlow)
	if !a.Firing || a.NotifiedAt == nil || a.Severity != "critical" || a.Since == nil {
		t.Errorf("state: %+v", a)
	}
	if want := t0.Add(time.Hour).Add(-time.Minute + time.Second); !a.Since.Equal(want) {
		t.Errorf("the alert dates from the first slow test (%v), got %v", want, a.Since)
	}

	// still slow, and ticking again and again: no duplicates
	h.step(time.Hour)
	h.add(400)
	h.tick()
	h.tick()
	h.expectSent(1)
	if a := h.alert("slow", serverSlow); a.Streak != 3 || !strings.Contains(*a.Detail, "(3 tests in a row)") {
		t.Errorf("the description stays current: %+v", a)
	}

	st, err := h.m.Status(h.ctx)
	if err != nil || len(st.Active) != 1 || st.Active[0].Kind != "slow" || st.Active[0].NotifiedAt == nil || len(st.Recent) != 1 || !st.Recent[0].Delivered {
		t.Errorf("status: %+v %v", st, err)
	}
}

func TestMonitorRecoveryIsSentOnce(t *testing.T) {
	h := newHarness(t, nil)
	h.step(time.Hour)
	h.add(400)
	h.add(405)
	h.tick()
	h.expectSent(1)

	h.step(3 * time.Hour)
	h.add(910)
	h.tick()
	h.expectSent(2)
	r := h.send.sent[1]
	if r.Title != "Recovered: server (wired)" || r.Priority != PriorityRecovery || !strings.Contains(r.Body, "back to normal") || !strings.Contains(r.Body, "The problem lasted 3 hours.") {
		t.Errorf("recovery: %+v", r)
	}
	h.step(time.Hour)
	h.add(920)
	h.tick()
	h.tick()
	h.expectSent(2)
	if a := h.alert("slow", serverSlow); a.Firing || a.NotifiedAt != nil || a.Streak != 0 {
		t.Errorf("closed out: %+v", a)
	}
	if st, _ := h.m.Status(h.ctx); len(st.Active) != 0 {
		t.Errorf("nothing active any more: %+v", st.Active)
	}
}

func TestMonitorHysteresis(t *testing.T) {
	// firing, then a middling result (80% of typical): neither slow enough to count nor good enough to clear
	h := newHarness(t, nil)
	h.step(time.Hour)
	h.add(400)
	h.add(405)
	h.tick()
	h.expectSent(1)
	h.step(time.Hour)
	h.add(720)
	h.tick()
	if a := h.alert("slow", serverSlow); !a.Firing {
		t.Fatal("an 80% result must not clear the alert")
	}
	h.expectSent(1)
	h.step(time.Hour)
	h.add(860) // 95%: recovered
	h.tick()
	h.expectSent(2)

	// not firing: slow, middling, slow is not "two in a row"
	h = newHarness(t, nil)
	h.step(time.Hour)
	h.add(400)
	h.tick()
	h.step(time.Hour)
	h.add(720)
	h.tick()
	h.step(time.Hour)
	h.add(400)
	h.tick()
	h.expectSent(0)
	if a := h.alert("slow", serverSlow); a.Firing || a.Streak != 1 {
		t.Errorf("the middling result broke the streak: %+v", a)
	}
}

func TestMonitorWaitsForEnoughHistory(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.MinSamples = 20 }) // the harness only has 12 tests of history
	h.step(time.Hour)
	h.add(100)
	h.add(100)
	h.add(100)
	h.tick()
	h.expectSent(0)
	if a := h.alert("slow", serverSlow); a != nil {
		t.Errorf("no judgement without history: %+v", a)
	}
}

func TestMonitorAFlapThatEndsBeforeAnyMessageIsSilent(t *testing.T) {
	h := newHarness(t, nil)
	h.step(time.Hour)
	// the monitor was down; by the time it looks, a slow stretch has come and gone
	h.add(400)
	h.add(410)
	h.add(900)
	h.tick()
	h.expectSent(0)
	if a := h.alert("slow", serverSlow); a.Firing || a.NotifiedAt != nil {
		t.Errorf("%+v", a)
	}
}

func TestMonitorRemindersAndTheirLimit(t *testing.T) {
	h := newHarness(t, nil)
	h.step(time.Hour)
	h.add(400)
	h.add(405)
	h.tick()
	h.expectSent(1)

	h.step(11 * time.Hour)
	h.tick()
	h.expectSent(1) // 11h later: not yet
	h.step(time.Hour)
	h.tick()
	h.expectSent(2)
	if r := h.send.sent[1]; r.Title != "Still: Slow download: server (wired)" || r.Priority != 4 {
		t.Errorf("reminder: %+v", r)
	}
	h.step(6 * time.Hour)
	h.tick()
	h.expectSent(2) // the clock restarts after each reminder

	// a problem older than a week stops being repeated
	h.step(8 * 24 * time.Hour)
	h.tick()
	h.step(13 * time.Hour)
	h.tick()
	h.expectSent(2)

	// and reminders can be switched off
	h = newHarness(t, func(c *Config) { c.RemindEvery = 0 })
	h.step(time.Hour)
	h.add(400)
	h.add(405)
	h.tick()
	h.step(72 * time.Hour)
	h.tick()
	h.expectSent(1)
}

func TestMonitorFailedDeliveryIsRetriedAndRecordedOnce(t *testing.T) {
	h := newHarness(t, nil)
	h.send.fail = func(n int) error {
		if n <= 2 {
			return errors.New("ntfy answered 503: down")
		}
		return nil
	}
	h.step(time.Hour)
	h.add(400)
	h.add(405)
	h.tick() // attempt 1 fails
	h.expectSent(0)
	if a := h.alert("slow", serverSlow); !a.Firing || a.NotifiedAt != nil {
		t.Fatalf("a failed delivery must leave the alert un-notified so it is retried: %+v", a)
	}
	h.step(time.Minute)
	h.tick() // attempt 2 fails again, a minute later
	h.expectSent(0)
	h.step(time.Minute)
	h.tick() // attempt 3 succeeds
	h.expectSent(1)
	if a := h.alert("slow", serverSlow); a.NotifiedAt == nil {
		t.Error("notified after the retry succeeded")
	}
	ev, err := h.db.RecentEvents(h.ctx, 10)
	if err != nil || len(ev) != 2 {
		t.Fatalf("one failure recorded (not one per retry) and one success: %+v %v", ev, err)
	}
	if ev[0].Delivered != true || ev[1].Delivered != false || ev[1].Error == nil || !strings.Contains(*ev[1].Error, "503") {
		t.Errorf("events (newest first): %+v", ev)
	}
}

func TestMonitorPacketLoss(t *testing.T) {
	h := newHarness(t, nil)
	loss := func(v float64) *float64 { return &v }
	h.step(time.Hour)
	h.addFor("server", "wired", "ookla", 900, loss(6))
	h.tick()
	h.expectSent(0)
	h.addFor("server", "wired", "ookla", 900, loss(7.5))
	h.tick()
	h.expectSent(1)
	if m := h.send.sent[0]; m.Title != "Packet loss: server (wired)" || !strings.Contains(m.Body, "7.5%") || m.Priority != 3 {
		t.Errorf("%+v", m)
	}
	h.step(time.Hour)
	h.addFor("server", "wired", "ookla", 900, loss(0))
	h.tick()
	h.expectSent(2)
	if !strings.HasPrefix(h.send.sent[1].Title, "Recovered: server") || !strings.Contains(h.send.sent[1].Body, "Packet loss has cleared") {
		t.Errorf("%+v", h.send.sent[1])
	}
	// a result that carries no loss figure (NDT7 often) neither raises nor clears anything
	h.step(time.Hour)
	h.addFor("server", "wired", "ookla", 900, nil)
	h.tick()
	h.expectSent(2)
}

func TestMonitorEntitiesAreIndependent(t *testing.T) {
	h := newHarness(t, nil)
	for i := 0; i < 12; i++ { // a laptop with its own, much lower, normal
		h.insert("laptop", "wireless", "ookla", "on_demand", t0.Add(-time.Duration(40-i)*time.Hour), 350, nil)
	}
	h.tick()
	h.step(time.Hour)
	h.addFor("server", "wired", "ookla", 400, nil)
	h.addFor("laptop", "wireless", "ookla", 340, nil) // normal for a laptop, would be "slow" against the server's 900
	h.addFor("server", "wired", "ookla", 405, nil)
	h.addFor("laptop", "wireless", "ookla", 345, nil)
	h.tick()
	h.expectSent(1)
	if h.send.sent[0].Title != "Slow download: server (wired)" {
		t.Errorf("%v", h.titles())
	}
	if a := h.alert("slow", "laptop|wireless|ookla"); a == nil || a.Firing {
		t.Errorf("laptop is fine: %+v", a)
	}
}

func TestMonitorSilence(t *testing.T) {
	pool := testdb.Pool(t)
	db := store.New(pool)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := pool.Exec(ctx, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	ins := func(device, trigger string, at time.Time) {
		exec(`INSERT INTO speedtests (provider, "trigger", device, connection_type, tested_at, created, download_mbps, upload_mbps, raw_data)
			VALUES ('ookla',$1,$2,'wired',$3,$3,900,100,'{}')`, trigger, device, at)
	}
	for i := 0; i < 12; i++ { // server: a scheduled test every 2 hours, the last one 30 minutes ago
		ins("server", "scheduled", t0.Add(-30*time.Minute-time.Duration(i)*2*time.Hour))
	}
	for i := 0; i < 12; i++ { // a laptop only ever tested by hand: never "silent"
		ins("laptop", "on_demand", t0.Add(-100*time.Hour-time.Duration(i)*time.Hour))
	}
	for i := 0; i < 3; i++ { // a device with too little history to know its rhythm
		ins("newbie", "scheduled", t0.Add(-100*time.Hour-time.Duration(i)*2*time.Hour))
	}
	cfg := Defaults()
	cfg.Enabled, cfg.Topic = true, "t"
	send := &fakeSender{}
	m := NewMonitor(cfg, db, send, slog.New(slog.NewTextHandler(io.Discard, nil)))
	clock := t0
	m.now = func() time.Time { return clock }
	tick := func() {
		t.Helper()
		if err := m.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	tick()
	if len(send.sent) != 0 {
		t.Fatalf("30 minutes after a 2-hourly test is fine: %v", send.sent)
	}

	clock = t0.Add(5 * time.Hour) // 5.5 hours since the last test; the threshold is 6 hours
	tick()
	if len(send.sent) != 0 {
		t.Fatalf("not yet 6 hours: %v", send.sent)
	}
	clock = t0.Add(7 * time.Hour) // 7.5 hours
	tick()
	tick()
	if len(send.sent) != 1 || send.sent[0].Title != "No tests from server" || !strings.Contains(send.sent[0].Body, "one is usually due every 2 hours") ||
		send.sent[0].Priority != 3 {
		t.Fatalf("silence alert (once, and only for server): %+v", send.sent)
	}

	// a scheduled test arrives again
	ins("server", "scheduled", clock.Add(-time.Minute))
	clock = clock.Add(time.Minute)
	tick()
	if len(send.sent) != 2 || send.sent[1].Title != "Recovered: server" || !strings.Contains(send.sent[1].Body, "Tests are arriving again") {
		t.Fatalf("recovery: %+v", send.sent)
	}
	if st, _ := db.FiringAlerts(ctx); len(st) != 0 {
		t.Errorf("nothing should be firing: %+v", st)
	}
}

func TestMonitorRetiredDeviceStopsQuietly(t *testing.T) {
	pool := testdb.Pool(t)
	db := store.New(pool)
	ctx := context.Background()
	for i := 0; i < 8; i++ {
		_, err := pool.Exec(ctx, `INSERT INTO speedtests (provider, "trigger", device, connection_type, tested_at, created, download_mbps, upload_mbps, raw_data)
			VALUES ('ookla','scheduled','old-box','wired',$1,$1,900,100,'{}')`, t0.Add(-time.Duration(i)*2*time.Hour))
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg := Defaults()
	cfg.Enabled, cfg.Topic = true, "t"
	send := &fakeSender{}
	m := NewMonitor(cfg, db, send, slog.New(slog.NewTextHandler(io.Discard, nil)))
	clock := t0
	m.now = func() time.Time { return clock }
	tick := func() {
		t.Helper()
		if err := m.Tick(ctx); err != nil {
			t.Fatal(err)
		}
	}
	tick()
	clock = t0.Add(10 * time.Hour)
	tick()
	if len(send.sent) != 1 {
		t.Fatalf("silence alert expected: %v", send.sent)
	}
	clock = t0.Add(20 * 24 * time.Hour) // the device has been gone for three weeks: retired, not "silent"
	tick()
	tick()
	if len(send.sent) != 1 {
		t.Fatalf("no 'recovered' message for a device that never came back: %v", send.sent)
	}
	if a, _ := db.LoadAlert(ctx, "silence", "old-box"); a == nil || a.Firing || a.NotifiedAt != nil {
		t.Errorf("closed out quietly: %+v", a)
	}
}

func TestMonitorSurvivesARestart(t *testing.T) {
	h := newHarness(t, nil)
	h.step(time.Hour)
	h.add(400)
	h.add(405)
	h.tick()
	h.expectSent(1)

	// a brand-new Monitor (the process restarted) picks up from the database
	m2 := NewMonitor(h.m.cfg, h.db, h.send, slog.New(slog.NewTextHandler(io.Discard, nil)))
	m2.now = func() time.Time { return h.clock }
	h.step(time.Minute)
	if err := m2.Tick(h.ctx); err != nil {
		t.Fatal(err)
	}
	h.expectSent(1) // not re-sent, and the old results are not re-counted
	if a := h.alert("slow", serverSlow); a.Streak != 2 {
		t.Errorf("streak re-counted after restart: %+v", a)
	}
}

func TestStatusNeverContainsTheTopicOrCredentials(t *testing.T) {
	h := newHarness(t, func(c *Config) { c.Token = "tk_supersecret" })
	h.step(time.Hour)
	h.add(400)
	h.add(405)
	h.tick()
	st, err := h.m.Status(h.ctx)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(st)
	for _, secret := range []string{"secret-topic", "tk_supersecret"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("status leaks %q: %s", secret, b)
		}
	}
	if st.Server == nil || *st.Server != "ntfy.example.org" || !st.Enabled || st.Rules == nil || st.Rules.SlowConsecutive != 2 {
		t.Errorf("status: %s", b)
	}
}

func TestEventsArePruned(t *testing.T) {
	pool := testdb.Pool(t)
	db := store.New(pool)
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO notification_events (event, title, message, delivered)
		SELECT 'alert', 't', 'm', true FROM generate_series(1, 520)`); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneEvents(ctx, 500); err != nil {
		t.Fatal(err)
	}
	var n int
	var minID, maxID int64
	_ = pool.QueryRow(ctx, `SELECT count(*), min(id), max(id) FROM notification_events`).Scan(&n, &minID, &maxID)
	if n != 500 || maxID-minID != 499 {
		t.Errorf("kept %d (ids %d..%d), want the newest 500", n, minID, maxID)
	}
	if err := db.PruneEvents(ctx, 1000); err != nil { // fewer than the limit: nothing happens
		t.Fatal(err)
	}
}

func TestPrepareRequiresMigration006(t *testing.T) {
	pool := testdb.Pool(t)
	m := NewMonitor(Defaults(), store.New(pool), &fakeSender{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err := m.Prepare(context.Background()); err != nil {
		t.Fatalf("with migration 006 applied: %v", err)
	}
}
