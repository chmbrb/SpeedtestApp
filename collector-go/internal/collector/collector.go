// Package collector runs the tests, sends the results, and queues what cannot be sent.
package collector

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/chmbrb/SpeedtestApp/collector-go/internal/apiclient"
	"github.com/chmbrb/SpeedtestApp/collector-go/internal/lock"
	"github.com/chmbrb/SpeedtestApp/collector-go/internal/spool"
)

// Provider is one kind of speed test.
type Provider interface {
	Name() string
	Run(ctx context.Context) (json.RawMessage, error)
}

// Sender delivers a result to the API; *apiclient.Client is the real one.
type Sender interface {
	Ingest(ctx context.Context, env apiclient.Envelope) (string, error)
}

type Collector struct {
	Connection string
	Trigger    string
	Providers  []Provider
	API        Sender
	Spool      *spool.Spool
	Log        *slog.Logger
	Timeout    time.Duration // longest one test may take; also bounds how long a lock is trusted
}

// Outcome is what happened to one provider's test.
type Outcome struct {
	Provider string
	ResultID string // set when the API accepted it
	Sent     bool
	Spooled  bool  // the test worked but the result is waiting in the queue
	Err      error // the test failed, or the result was refused as invalid
	// SendErr is why the API did not take the result (set whenever a send failed, even when the result was kept in the queue).
	// A refused token shows up here: the data is safe, but a person needs to know and nothing more should be sent now.
	SendErr error
}

// OK reports whether the result is safe: delivered, or queued for delivery.
func (o Outcome) OK() bool { return o.Err == nil && (o.Sent || o.Spooled) }

func (c *Collector) lockPath() string { return filepath.Join(c.Spool.Dir(), "running.lock") }

// RunOnce runs every provider's test in turn, sends each result (or queues it), then tries to send anything already waiting.
// Only one test runs on the device at a time: if another collector is mid-test, lock.ErrBusy is returned.
func (c *Collector) RunOnce(ctx context.Context) ([]Outcome, error) {
	release, err := lock.Acquire(c.lockPath(), c.Timeout*time.Duration(len(c.Providers))+5*time.Minute)
	if err != nil {
		return nil, err
	}
	defer release()

	var out []Outcome
	authFailed := false
	for _, p := range c.Providers {
		if ctx.Err() != nil {
			break
		}
		o := c.runOne(ctx, p)
		out = append(out, o)
		if apiclient.IsKind(o.SendErr, apiclient.KindAuth) {
			authFailed = true
		}
	}
	if !authFailed && ctx.Err() == nil {
		c.Flush(ctx)
	}
	return out, nil
}

func (c *Collector) runOne(ctx context.Context, p Provider) Outcome {
	o := Outcome{Provider: p.Name()}
	tctx, cancel := context.WithTimeout(ctx, c.Timeout)
	defer cancel()
	c.Log.Info("running test", "provider", p.Name())
	started := time.Now()
	raw, err := p.Run(tctx)
	if err != nil {
		o.Err = fmt.Errorf("%s: %w", p.Name(), err)
		c.Log.Error("the test failed", "provider", p.Name(), "err", err)
		return o
	}
	c.Log.Info("test finished", "provider", p.Name(), "seconds", int(time.Since(started).Seconds()))

	env := apiclient.Envelope{Provider: p.Name(), ConnectionType: c.Connection, Trigger: c.Trigger, Result: raw}
	id, err := c.API.Ingest(ctx, env)
	if err == nil {
		o.Sent, o.ResultID = true, id
		c.Log.Info("result sent", "provider", p.Name(), "id", id)
		return o
	}
	o.SendErr = err
	var ae *apiclient.Error
	if errors.As(err, &ae) && !ae.Keep() {
		o.Err = fmt.Errorf("%s: the server refused the result: %w", p.Name(), err)
		c.Log.Error("the server refused the result; it is not queued because it would be refused again", "provider", p.Name(), "err", err)
		return o
	}
	if serr := c.queue(env); serr != nil {
		o.Err = fmt.Errorf("%s: the result could not be sent (%v) and could not be queued either: %w", p.Name(), err, serr)
		c.Log.Error("result lost", "provider", p.Name(), "send_err", err, "queue_err", serr)
		return o
	}
	o.Spooled = true
	level := slog.LevelWarn
	c.Log.Log(ctx, level, "the result could not be sent yet, so it is queued and will be retried", "provider", p.Name(), "why", err, "queued", c.Spool.Len())
	return o
}

func (c *Collector) queue(env apiclient.Envelope) error {
	b, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, dropped, err := c.Spool.Add(b)
	if dropped > 0 {
		c.Log.Warn("the queue was full, so the oldest results were dropped", "dropped", dropped)
	}
	return err
}

// FlushReport says what a flush did.
type FlushReport struct {
	Sent      int
	Rejected  int   // refused as invalid; moved aside, not retried
	Expired   int   // older than the queue keeps results
	Remaining int   // still waiting
	Stopped   error // why it stopped early (the API cannot be reached, the token is refused, ...); nil if it finished
}

// Flush sends the queued results, oldest first. It stops at the first failure that means "not now" (offline, rate limited,
// wrong token, wrong address) so a server that is down is not hammered, and keeps everything it did not send.
func (c *Collector) Flush(ctx context.Context) FlushReport {
	var r FlushReport
	items, expired, err := c.Spool.List()
	r.Expired = expired
	if err != nil {
		r.Stopped = err
		c.Log.Error("cannot read the queue", "err", err)
		return r
	}
	for i, it := range items {
		if ctx.Err() != nil {
			r.Stopped = ctx.Err()
			break
		}
		var env apiclient.Envelope
		if err := json.Unmarshal(it.Payload, &env); err != nil || env.Provider == "" {
			_ = c.Spool.Reject(it.Name, "unreadable queue file")
			r.Rejected++
			continue
		}
		id, err := c.API.Ingest(ctx, env)
		if err == nil {
			_ = c.Spool.Remove(it.Name)
			r.Sent++
			c.Log.Info("queued result sent", "provider", env.Provider, "id", id, "waited", time.Since(it.QueuedAt).Round(time.Second).String())
			continue
		}
		var ae *apiclient.Error
		if errors.As(err, &ae) && !ae.Keep() {
			_ = c.Spool.Reject(it.Name, ae.Error())
			r.Rejected++
			c.Log.Error("the server refused a queued result; it was moved aside", "name", it.Name, "err", err)
			continue
		}
		r.Stopped = err
		r.Remaining = len(items) - i
		c.Log.Warn("stopped sending queued results", "why", err, "waiting", r.Remaining)
		return r
	}
	r.Remaining = c.Spool.Len()
	if r.Sent+r.Rejected+r.Expired > 0 {
		c.Log.Info("queue handled", "sent", r.Sent, "rejected", r.Rejected, "expired", r.Expired, "waiting", r.Remaining)
	}
	return r
}
