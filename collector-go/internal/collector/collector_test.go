package collector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chmbrb/SpeedtestApp/collector-go/internal/apiclient"
	"github.com/chmbrb/SpeedtestApp/collector-go/internal/lock"
	"github.com/chmbrb/SpeedtestApp/collector-go/internal/spool"
)

type fakeProvider struct {
	name  string
	raw   string
	err   error
	block bool // wait for the context (a test that never finishes)
	runs  atomic.Int32
}

func (f *fakeProvider) Name() string { return f.name }
func (f *fakeProvider) Run(ctx context.Context) (json.RawMessage, error) {
	f.runs.Add(1)
	if f.block {
		<-ctx.Done()
		return nil, context.Cause(ctx)
	}
	if f.err != nil {
		return nil, f.err
	}
	return json.RawMessage(f.raw), nil
}

// fakeAPI decides per call what happens; sent records what was accepted.
type fakeAPI struct {
	respond func(call int, env apiclient.Envelope) error
	calls   int
	sent    []apiclient.Envelope
}

func (f *fakeAPI) Ingest(ctx context.Context, env apiclient.Envelope) (string, error) {
	f.calls++
	if f.respond != nil {
		if err := f.respond(f.calls, env); err != nil {
			return "", err
		}
	}
	f.sent = append(f.sent, env)
	return fmt.Sprintf("id-%d", f.calls), nil
}

func transient() error {
	return &apiclient.Error{Kind: apiclient.KindTransient, Detail: "connection refused"}
}
func rejected() error {
	return &apiclient.Error{Kind: apiclient.KindRejected, Status: 400, Detail: "result.download.bandwidth is required"}
}
func authErr() error {
	return &apiclient.Error{Kind: apiclient.KindAuth, Status: 401, Detail: "refused the token"}
}

type rig struct {
	t     *testing.T
	c     *Collector
	api   *fakeAPI
	sp    *spool.Spool
	logs  *bytes.Buffer
	ookla *fakeProvider
}

func newRig(t *testing.T) *rig {
	t.Helper()
	sp, err := spool.Open(filepath.Join(t.TempDir(), "q"), 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	api := &fakeAPI{}
	ookla := &fakeProvider{name: "ookla", raw: `{"type":"result","n":1}`}
	return &rig{t: t, api: api, sp: sp, logs: logs, ookla: ookla, c: &Collector{
		Connection: "wired", Trigger: "scheduled", API: api, Spool: sp, Timeout: 5 * time.Second,
		Log: slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug})), Providers: []Provider{ookla},
	}}
}

func TestASuccessfulTestIsSentWithTheRightEnvelope(t *testing.T) {
	r := newRig(t)
	out, err := r.c.RunOnce(context.Background())
	if err != nil || len(out) != 1 || !out[0].Sent || out[0].ResultID != "id-1" || out[0].Spooled || out[0].Err != nil || !out[0].OK() {
		t.Fatalf("%+v %v", out, err)
	}
	env := r.api.sent[0]
	if env.Provider != "ookla" || env.ConnectionType != "wired" || env.Trigger != "scheduled" || string(env.Result) != `{"type":"result","n":1}` {
		t.Errorf("%+v", env)
	}
	if r.sp.Len() != 0 {
		t.Error("nothing is queued when the send worked")
	}
}

func TestATestThatFailsSendsNothing(t *testing.T) {
	r := newRig(t)
	r.ookla.err = errors.New("Network is unreachable")
	out, _ := r.c.RunOnce(context.Background())
	if len(out) != 1 || out[0].OK() || out[0].Err == nil || !strings.Contains(out[0].Err.Error(), "Network is unreachable") || r.api.calls != 0 {
		t.Errorf("%+v calls=%d", out, r.api.calls)
	}
}

func TestAResultThatCannotBeSentIsQueuedAndSentLater(t *testing.T) {
	r := newRig(t)
	offline := true
	r.api.respond = func(int, apiclient.Envelope) error {
		if offline {
			return transient()
		}
		return nil
	}
	out, _ := r.c.RunOnce(context.Background())
	if len(out) != 1 || out[0].Sent || !out[0].Spooled || !out[0].OK() || out[0].Err != nil {
		t.Fatalf("a result that is waiting is safe: %+v", out)
	}
	if r.sp.Len() != 1 || !strings.Contains(r.logs.String(), "queued and will be retried") {
		t.Fatalf("queue=%d\n%s", r.sp.Len(), r.logs.String())
	}
	offline = false // the laptop is back online
	r.ookla.err = errors.New("the next test fails, but the queued one still goes out")
	r.c.RunOnce(context.Background()) //nolint:errcheck
	if r.sp.Len() != 0 || len(r.api.sent) != 1 || string(r.api.sent[0].Result) != `{"type":"result","n":1}` {
		t.Errorf("queue=%d sent=%d", r.sp.Len(), len(r.api.sent))
	}
}

func TestARefusedResultIsNotQueuedBecauseItWouldBeRefusedAgain(t *testing.T) {
	r := newRig(t)
	r.api.respond = func(int, apiclient.Envelope) error { return rejected() }
	out, _ := r.c.RunOnce(context.Background())
	if len(out) != 1 || out[0].OK() || out[0].Err == nil || !strings.Contains(out[0].Err.Error(), "refused the result") || r.sp.Len() != 0 {
		t.Errorf("%+v queue=%d", out, r.sp.Len())
	}
}

func TestARefusedTokenKeepsTheResultAndStopsSending(t *testing.T) {
	r := newRig(t)
	r.api.respond = func(int, apiclient.Envelope) error { return authErr() }
	out, _ := r.c.RunOnce(context.Background())
	if len(out) != 1 || !out[0].Spooled || r.sp.Len() != 1 {
		t.Fatalf("the data is kept so that fixing the token loses nothing: %+v", out)
	}
	if r.api.calls != 1 {
		t.Errorf("after a refused token nothing more is sent this round (no flush): %d calls", r.api.calls)
	}
}

func TestFlushSendsOldestFirstAndStopsAtTheFirstFailureKeepingTheRest(t *testing.T) {
	r := newRig(t)
	for i := 0; i < 5; i++ {
		b, _ := json.Marshal(apiclient.Envelope{Provider: "ookla", ConnectionType: "wired", Result: json.RawMessage(fmt.Sprintf(`{"n":%d}`, i))})
		if _, _, err := r.sp.Add(b); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	r.api.respond = func(call int, env apiclient.Envelope) error {
		if call == 3 {
			return transient() // the connection drops after two results
		}
		return nil
	}
	rep := r.c.Flush(context.Background())
	if rep.Sent != 2 || rep.Remaining != 3 || !apiclient.IsKind(rep.Stopped, apiclient.KindTransient) {
		t.Fatalf("%+v", rep)
	}
	if r.api.calls != 3 {
		t.Errorf("it stopped hammering after the first failure: %d calls", r.api.calls)
	}
	if string(r.api.sent[0].Result) != `{"n":0}` || string(r.api.sent[1].Result) != `{"n":1}` {
		t.Errorf("oldest first: %s %s", r.api.sent[0].Result, r.api.sent[1].Result)
	}
	r.api.respond = nil
	rep = r.c.Flush(context.Background())
	if rep.Sent != 3 || rep.Remaining != 0 || rep.Stopped != nil || string(r.api.sent[2].Result) != `{"n":2}` {
		t.Errorf("%+v", rep)
	}
}

func TestFlushStopsOnRateLimitsAuthAndMisconfiguration(t *testing.T) {
	for name, err := range map[string]error{
		"rate limited":  &apiclient.Error{Kind: apiclient.KindRateLimited, Status: 429, RetryAfter: time.Minute},
		"bad token":     authErr(),
		"wrong address": &apiclient.Error{Kind: apiclient.KindConfig, Status: 404},
	} {
		r := newRig(t)
		for i := 0; i < 3; i++ {
			b, _ := json.Marshal(apiclient.Envelope{Provider: "ookla", Result: json.RawMessage(`{}`)})
			r.sp.Add(b) //nolint:errcheck
		}
		e := err
		r.api.respond = func(int, apiclient.Envelope) error { return e }
		rep := r.c.Flush(context.Background())
		if rep.Sent != 0 || rep.Remaining != 3 || rep.Stopped == nil || r.api.calls != 1 || r.sp.Len() != 3 {
			t.Errorf("%s: %+v calls=%d queue=%d (nothing may be lost or retried in a loop)", name, rep, r.api.calls, r.sp.Len())
		}
	}
}

func TestFlushMovesAsideWhatTheServerRefusesAndWhatItCannotRead(t *testing.T) {
	r := newRig(t)
	good, _ := json.Marshal(apiclient.Envelope{Provider: "ookla", Result: json.RawMessage(`{"ok":true}`)})
	bad, _ := json.Marshal(apiclient.Envelope{Provider: "ookla", Result: json.RawMessage(`{"bad":true}`)})
	r.sp.Add([]byte(`this is not json`)) //nolint:errcheck
	time.Sleep(2 * time.Millisecond)
	r.sp.Add(bad) //nolint:errcheck
	time.Sleep(2 * time.Millisecond)
	r.sp.Add(good) //nolint:errcheck
	r.api.respond = func(_ int, env apiclient.Envelope) error {
		if strings.Contains(string(env.Result), "bad") {
			return rejected()
		}
		return nil
	}
	rep := r.c.Flush(context.Background())
	if rep.Sent != 1 || rep.Rejected != 2 || rep.Remaining != 0 || rep.Stopped != nil {
		t.Fatalf("one bad result must not block the good ones behind it: %+v", rep)
	}
	entries, _ := os.ReadDir(filepath.Join(r.sp.Dir(), "rejected"))
	if len(entries) != 4 { // two results and their reason files
		t.Errorf("kept for inspection: %d files", len(entries))
	}
}

func TestOneProviderFailingDoesNotStopTheOthers(t *testing.T) {
	r := newRig(t)
	r.ookla.err = errors.New("tool missing")
	ndt := &fakeProvider{name: "ndt7", raw: `{"startedAt":"x"}`}
	r.c.Providers = []Provider{r.ookla, ndt}
	out, _ := r.c.RunOnce(context.Background())
	if len(out) != 2 || out[0].OK() || !out[1].OK() || len(r.api.sent) != 1 || r.api.sent[0].Provider != "ndt7" {
		t.Errorf("%+v", out)
	}
}

func TestATestThatNeverFinishesIsCutOffAtTheTimeout(t *testing.T) {
	r := newRig(t)
	r.c.Timeout = 300 * time.Millisecond
	r.ookla.block = true
	started := time.Now()
	out, _ := r.c.RunOnce(context.Background())
	if len(out) != 1 || out[0].Err == nil || !strings.Contains(out[0].Err.Error(), "deadline") || time.Since(started) > 5*time.Second {
		t.Errorf("%+v after %v", out, time.Since(started))
	}
}

func TestOnlyOneRoundRunsAtATime(t *testing.T) {
	r := newRig(t)
	release, err := lock.Acquire(filepath.Join(r.sp.Dir(), "running.lock"), time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.c.RunOnce(context.Background()); !errors.Is(err, lock.ErrBusy) || r.ookla.runs.Load() != 0 {
		t.Errorf("a second test must not start while one is running: %v runs=%d", err, r.ookla.runs.Load())
	}
	release()
	if out, err := r.c.RunOnce(context.Background()); err != nil || len(out) != 1 || !out[0].Sent {
		t.Errorf("%+v %v", out, err)
	}
	if _, err := os.Stat(filepath.Join(r.sp.Dir(), "running.lock")); !os.IsNotExist(err) {
		t.Error("the lock is released after a round")
	}
}

func TestCancellingStopsBetweenProviders(t *testing.T) {
	r := newRig(t)
	ctx, cancel := context.WithCancel(context.Background())
	first := &fakeProvider{name: "ookla", raw: `{}`}
	second := &fakeProvider{name: "ndt7", raw: `{}`}
	r.c.Providers = []Provider{first, second}
	r.api.respond = func(int, apiclient.Envelope) error { cancel(); return nil } // the person presses Ctrl-C after the first result
	out, _ := r.c.RunOnce(ctx)
	if len(out) != 1 || second.runs.Load() != 0 {
		t.Errorf("%+v second ran %d times", out, second.runs.Load())
	}
}

func TestAFullQueueDropsTheOldestAndSaysSo(t *testing.T) {
	r := newRig(t)
	sp, _ := spool.Open(filepath.Join(t.TempDir(), "small"), 2, 0)
	r.c.Spool = sp
	r.api.respond = func(int, apiclient.Envelope) error { return transient() }
	for i := 0; i < 3; i++ {
		r.c.RunOnce(context.Background()) //nolint:errcheck
		time.Sleep(2 * time.Millisecond)
	}
	if sp.Len() != 2 || !strings.Contains(r.logs.String(), "the oldest results were dropped") {
		t.Errorf("len=%d\n%s", sp.Len(), r.logs.String())
	}
}
