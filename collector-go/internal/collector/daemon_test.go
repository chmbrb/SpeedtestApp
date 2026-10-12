package collector

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/chmbrb/SpeedtestApp/collector-go/internal/apiclient"
	"github.com/chmbrb/SpeedtestApp/collector-go/internal/lock"
)

func TestScheduleAfterSuccessIsTheIntervalWithJitter(t *testing.T) {
	s := Schedule{Interval: 2 * time.Hour, Jitter: 10 * time.Minute}
	for r, want := range map[float64]time.Duration{0: 110 * time.Minute, 0.5: 120 * time.Minute, 1: 130 * time.Minute, 0.25: 115 * time.Minute} {
		s.Rand = func() float64 { return r }
		if got := s.After(0); got != want {
			t.Errorf("rand %v: %v, want %v", r, got, want)
		}
	}
	// real randomness stays inside the bounds
	s.Rand = nil
	for i := 0; i < 1000; i++ {
		if d := s.After(0); d < 110*time.Minute || d > 130*time.Minute {
			t.Fatalf("%v is outside the jitter", d)
		}
	}
	// and it is never absurdly short
	tiny := Schedule{Interval: 90 * time.Second, Jitter: 80 * time.Second, Rand: func() float64 { return 0 }}
	if tiny.After(0) != time.Minute {
		t.Errorf("%v", tiny.After(0))
	}
	// no jitter means exactly the interval
	if (Schedule{Interval: time.Hour, Rand: func() float64 { return 0.9 }}).After(0) != time.Hour {
		t.Error("zero jitter")
	}
}

func TestScheduleBacksOffAfterFailures(t *testing.T) {
	s := Schedule{Interval: 2 * time.Hour}
	var got []time.Duration
	for f := 1; f <= 8; f++ {
		got = append(got, s.After(f))
	}
	want := []time.Duration{1, 2, 4, 8, 16, 30, 30, 30}
	for i, w := range want {
		if got[i] != w*time.Minute {
			t.Errorf("failure %d: %v, want %v", i+1, got[i], w*time.Minute)
		}
	}
	short := Schedule{Interval: 10 * time.Minute}
	if short.After(5) != 10*time.Minute || short.After(3) != 4*time.Minute {
		t.Errorf("backoff never exceeds the interval: %v %v", short.After(5), short.After(3))
	}
}

type clockless struct {
	waits  []time.Duration
	limit  int
	cancel context.CancelFunc
}

func (c *clockless) sleep(ctx context.Context, d time.Duration) error {
	c.waits = append(c.waits, d)
	if len(c.waits) >= c.limit {
		c.cancel()
		return ctx.Err()
	}
	return nil
}

func discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// runDaemon runs the loop until `rounds` waits have happened and returns the waits.
func runDaemon(t *testing.T, r *rig, s Schedule, runAtStart bool, waits int) []time.Duration {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cl := &clockless{limit: waits, cancel: cancel}
	Daemon(ctx, r.c, s, runAtStart, cl.sleep, discard())
	return cl.waits
}

func TestDaemonTestsAtStartThenOnTheInterval(t *testing.T) {
	r := newRig(t)
	s := Schedule{Interval: time.Hour, Jitter: 0}
	waits := runDaemon(t, r, s, true, 3)
	if r.ookla.runs.Load() != 3 || len(r.api.sent) != 3 {
		t.Errorf("a test before the first wait, then one after each: runs=%d", r.ookla.runs.Load())
	}
	for _, w := range waits {
		if w != time.Hour {
			t.Errorf("%v", waits)
		}
	}
}

func TestDaemonCanWaitBeforeTheFirstTest(t *testing.T) {
	r := newRig(t)
	waits := runDaemon(t, r, Schedule{Interval: time.Hour, Rand: func() float64 { return 0.5 }}, false, 1)
	if r.ookla.runs.Load() != 0 || len(waits) != 1 || waits[0] != time.Hour {
		t.Errorf("runs=%d waits=%v", r.ookla.runs.Load(), waits)
	}
}

func TestDaemonBacksOffWhenEveryTestFailsAndRecovers(t *testing.T) {
	r := newRig(t)
	failing := true
	r.ookla.err = errors.New("network down")
	r.api.respond = func(call int, _ apiclient.Envelope) error { return nil }
	go func() {}() // (the provider's behaviour is switched from the sleeper, between rounds)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var waits []time.Duration
	sleep := func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		if len(waits) == 4 && failing { // after four failed rounds the network comes back
			failing = false
			r.ookla.err = nil
		}
		if len(waits) >= 6 {
			cancel()
			return ctx.Err()
		}
		return nil
	}
	Daemon(ctx, r.c, Schedule{Interval: 2 * time.Hour}, true, sleep, discard())
	want := []time.Duration{time.Minute, 2 * time.Minute, 4 * time.Minute, 8 * time.Minute, 2 * time.Hour, 2 * time.Hour}
	for i, w := range want {
		if i >= len(waits) || waits[i] != w {
			t.Fatalf("waits %v, want %v", waits, want)
		}
	}
}

func TestDaemonWaitsAMinuteWhenAnotherTestIsRunningAndDoesNotCountItAsAFailure(t *testing.T) {
	r := newRig(t)
	release, _ := lock.Acquire(filepath.Join(r.sp.Dir(), "running.lock"), time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var waits []time.Duration
	sleep := func(ctx context.Context, d time.Duration) error {
		waits = append(waits, d)
		if len(waits) == 3 {
			release() // the other test finishes
		}
		if len(waits) >= 5 {
			cancel()
			return ctx.Err()
		}
		return nil
	}
	Daemon(ctx, r.c, Schedule{Interval: 3 * time.Hour}, true, sleep, discard())
	want := []time.Duration{time.Minute, time.Minute, time.Minute, 3 * time.Hour, 3 * time.Hour}
	for i, w := range want {
		if i >= len(waits) || waits[i] != w {
			t.Fatalf("waits %v, want %v", waits, want)
		}
	}
	if r.ookla.runs.Load() < 1 {
		t.Error("it tested as soon as the device was free")
	}
}

func TestDaemonKeepsTheNormalIntervalWhenTheTokenIsRefusedAndSaysSo(t *testing.T) {
	r := newRig(t)
	r.api.respond = func(int, apiclient.Envelope) error { return authErr() }
	var logs strings.Builder
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cl := &clockless{limit: 2, cancel: cancel}
	Daemon(ctx, r.c, Schedule{Interval: time.Hour}, true, cl.sleep, slog.New(slog.NewTextHandler(&logs, nil)))
	for _, w := range cl.waits {
		if w != time.Hour {
			t.Errorf("a wrong token is not fixed by retrying faster: %v", cl.waits)
		}
	}
	if !strings.Contains(logs.String(), "refused this device's token") || r.sp.Len() != 2 {
		t.Errorf("queue=%d\n%s", r.sp.Len(), logs.String())
	}
}

func TestDaemonStopsImmediatelyWhenCancelled(t *testing.T) {
	r := newRig(t)
	r.ookla.block = true // a test is in progress
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { Daemon(ctx, r.c, Schedule{Interval: time.Hour}, true, RealSleep, discard()); close(done) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("did not stop")
	}
	if r.sp.Len() != 0 || len(r.api.sent) != 0 {
		t.Error("an interrupted test sends and queues nothing")
	}
}

func TestRealSleepWakesEarlyOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	started := time.Now()
	if err := RealSleep(ctx, time.Hour); err == nil || time.Since(started) > 2*time.Second {
		t.Errorf("%v after %v", err, time.Since(started))
	}
	if err := RealSleep(context.Background(), time.Millisecond); err != nil {
		t.Error(err)
	}
}
