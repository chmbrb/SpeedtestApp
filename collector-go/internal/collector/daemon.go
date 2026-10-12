package collector

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/chmbrb/SpeedtestApp/collector-go/internal/apiclient"
	"github.com/chmbrb/SpeedtestApp/collector-go/internal/lock"
)

const (
	backoffStart = time.Minute
	backoffMax   = 30 * time.Minute
	busyRetry    = time.Minute
)

// Schedule decides how long to wait between rounds.
type Schedule struct {
	Interval time.Duration
	Jitter   time.Duration
	Rand     func() float64 // in [0,1); injected so tests are deterministic
}

// After returns the wait before the next round. After a success it is the interval, spread by up to ±Jitter so that many devices
// do not all test at the same moment (and hammer the same test servers). After failures it backs off 1m, 2m, 4m ... up to
// 30m, but never longer than the normal interval.
func (s Schedule) After(failures int) time.Duration {
	if failures > 0 {
		d := backoffStart
		for i := 1; i < failures && d < backoffMax; i++ {
			d *= 2
		}
		return min(d, backoffMax, s.Interval)
	}
	r := rand.Float64
	if s.Rand != nil {
		r = s.Rand
	}
	d := s.Interval + time.Duration((r()*2-1)*float64(s.Jitter))
	return max(d, time.Minute)
}

// Sleeper waits for d or until ctx ends; it returns ctx.Err() in the second case.
type Sleeper func(ctx context.Context, d time.Duration) error

func RealSleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Daemon runs rounds until ctx ends. A round counts as failed only if no provider's result was delivered or queued.
func Daemon(ctx context.Context, c *Collector, s Schedule, runAtStart bool, sleep Sleeper, log *slog.Logger) {
	wait := s.After(0)
	if runAtStart {
		wait = 0
	}
	failures := 0
	for {
		if wait > 0 {
			log.Info("next test", "in", wait.Round(time.Second).String(), "at", time.Now().Add(wait).Format(time.RFC3339))
			if sleep(ctx, wait) != nil {
				return
			}
		}
		outcomes, err := c.RunOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		switch {
		case errors.Is(err, lock.ErrBusy):
			log.Warn("another test is running on this device; waiting", "retry_in", busyRetry.String())
			wait = busyRetry
			continue
		case err != nil:
			log.Error("round failed", "err", err)
			failures++
		default:
			ok := false
			for _, o := range outcomes {
				ok = ok || o.OK()
			}
			if ok {
				failures = 0
			} else {
				failures++
			}
			// A refused token will not fix itself with retries: wait the normal interval and say so every round.
			for _, o := range outcomes {
				if apiclient.IsKind(o.SendErr, apiclient.KindAuth) {
					log.Error("the API refused this device's token; fix SPEEDTEST_API_TOKEN (results are being kept)")
				}
			}
		}
		wait = s.After(failures)
	}
}
