package notify

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/model"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/store"
)

// Repo is the persistence the monitor needs; *store.Store implements it.
type Repo interface {
	NotifyReady(ctx context.Context) error
	CursorOrInit(ctx context.Context, now time.Time) (store.Cursor, error)
	SaveCursor(ctx context.Context, c store.Cursor) error
	NewResults(ctx context.Context, after store.Cursor, settledBefore time.Time, limit int) ([]store.ResultPoint, error)
	Baseline(ctx context.Context, device, connectionType, provider string, before time.Time, days int) (float64, int, error)
	ScheduledActivity(ctx context.Context, since time.Time) ([]store.DeviceActivity, error)
	LoadAlert(ctx context.Context, kind, subject string) (*store.AlertState, error)
	SaveAlert(ctx context.Context, a *store.AlertState) error
	AlertsToHandle(ctx context.Context) ([]store.AlertState, error)
	FiringAlerts(ctx context.Context) ([]store.AlertState, error)
	AddEvent(ctx context.Context, e store.NotificationEvent) error
	RecentEvents(ctx context.Context, n int) ([]store.NotificationEvent, error)
	PruneEvents(ctx context.Context, keep int) error
}

const (
	batchSize        = 500
	keepEvents       = 500
	maxReminderAge   = 7 * 24 * time.Hour // a device that has been dead for a week stops being nagged about
	failLogEvery     = 10 * time.Minute   // while ntfy is unreachable, record the failure at most this often
	settleDelay      = 5 * time.Second
	activityLookback = 14 * 24 * time.Hour
	minScheduled     = 4 // scheduled tests needed to know a device's rhythm
)

// Monitor reads new results, keeps the alert state up to date, and sends what needs sending.
// It is the only writer of alert state, and Tick is not safe for concurrent use.
type Monitor struct {
	cfg      Config
	repo     Repo
	send     Sender
	log      *slog.Logger
	now      func() time.Time
	lastFail map[string]time.Time
}

func NewMonitor(cfg Config, repo Repo, send Sender, log *slog.Logger) *Monitor {
	return &Monitor{cfg: cfg, repo: repo, send: send, log: log, now: func() time.Time { return time.Now().UTC() }, lastFail: map[string]time.Time{}}
}

// Prepare fails fast, with a clear message, when the database is not ready.
func (m *Monitor) Prepare(ctx context.Context) error { return m.repo.NotifyReady(ctx) }

// Run checks immediately and then every cfg.CheckEvery until ctx ends.
func (m *Monitor) Run(ctx context.Context) {
	t := time.NewTicker(m.cfg.CheckEvery)
	defer t.Stop()
	for {
		m.safeTick(ctx)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (m *Monitor) safeTick(ctx context.Context) {
	defer func() {
		if v := recover(); v != nil {
			m.log.Error("notification check panicked", "value", v)
		}
	}()
	if err := m.Tick(ctx); err != nil && ctx.Err() == nil {
		m.log.Error("notification check failed", "err", err)
	}
}

// Tick does one pass: read new results, check for silent devices, send what is due.
func (m *Monitor) Tick(ctx context.Context) error {
	now := m.now()
	cur, err := m.repo.CursorOrInit(ctx, now)
	if err != nil {
		return err
	}
	for {
		rows, err := m.repo.NewResults(ctx, cur, now.Add(-settleDelay), batchSize)
		if err != nil {
			return err
		}
		for _, r := range rows {
			if err := m.evalResult(ctx, r); err != nil {
				return err
			}
			cur = store.Cursor{T: r.Created, ID: r.ID}
			if err := m.repo.SaveCursor(ctx, cur); err != nil { // per result, so a failure never re-counts a streak
				return err
			}
		}
		if len(rows) < batchSize {
			break
		}
	}
	if err := m.evalSilence(ctx, now); err != nil {
		return err
	}
	if err := m.dispatch(ctx, now); err != nil {
		return err
	}
	return m.repo.PruneEvents(ctx, keepEvents)
}

type outcome int

const (
	bad outcome = iota
	good
	between // neither bad enough to count nor good enough to clear (hysteresis)
)

func (m *Monitor) evalResult(ctx context.Context, r store.ResultPoint) error {
	e := Entity{Device: r.Device, ConnectionType: r.ConnectionType, Provider: r.Provider}

	typical, n, err := m.repo.Baseline(ctx, e.Device, e.ConnectionType, e.Provider, r.TestedAt, m.cfg.BaselineDays)
	if err != nil {
		return err
	}
	if n >= m.cfg.MinSamples && typical > 0 { // not enough history yet: no judgement either way
		ratio := r.DownloadMbps / typical
		o := between
		switch {
		case ratio < m.cfg.SlowRatio:
			o = bad
		case ratio >= m.cfg.RecoverRatio:
			o = good
		}
		err := m.advance(ctx, "slow", e, o, m.cfg.SlowConsecutive, r.TestedAt,
			func(streak int) Text { return SlowText(e, r.DownloadMbps, typical, streak) })
		if err != nil {
			return err
		}
	}

	if r.PacketLossPct != nil {
		o := good
		if *r.PacketLossPct >= m.cfg.LossPct {
			o = bad
		}
		loss := *r.PacketLossPct
		return m.advance(ctx, "loss", e, o, m.cfg.LossConsecutive, r.TestedAt,
			func(streak int) Text { return LossText(e, loss, streak) })
	}
	return nil
}

func newState(kind, subject string, e Entity) *store.AlertState {
	st := &store.AlertState{Kind: kind, Subject: subject, Device: e.Device, Severity: "warning"}
	if e.ConnectionType != "" {
		st.ConnectionType, st.Provider = &e.ConnectionType, &e.Provider
	}
	return st
}

func applyText(st *store.AlertState, t Text) {
	st.Severity, st.Title, st.Detail = t.Severity, &t.Title, &t.Detail
}

// advance moves one alert condition forward by one result. It only updates state; dispatch sends the messages.
func (m *Monitor) advance(ctx context.Context, kind string, e Entity, o outcome, need int, testedAt time.Time, text func(streak int) Text) error {
	subject := e.Subject()
	st, err := m.repo.LoadAlert(ctx, kind, subject)
	if err != nil {
		return err
	}
	if st == nil {
		st = newState(kind, subject, e)
	}
	switch o {
	case bad:
		if st.Streak == 0 {
			t := testedAt
			st.StreakSince = &t
		}
		st.Streak++
		switch {
		case st.Firing: // already firing: keep the description current
			applyText(st, text(st.Streak))
		case st.Streak >= need:
			since := *st.StreakSince
			st.Firing, st.Since, st.NotifiedAt = true, &since, nil
			applyText(st, text(st.Streak))
		}
	case good:
		st.Streak, st.StreakSince, st.Firing = 0, nil, false
	case between:
		if !st.Firing {
			st.Streak, st.StreakSince = 0, nil
		}
	}
	return m.repo.SaveAlert(ctx, st)
}

// evalSilence flags devices that have stopped reporting. A device's rhythm is learned from its own scheduled
// tests, so nothing needs configuring per device. On-demand tests are ignored; people do not run those on a clock.
func (m *Monitor) evalSilence(ctx context.Context, now time.Time) error {
	acts, err := m.repo.ScheduledActivity(ctx, now.Add(-activityLookback))
	if err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, a := range acts {
		seen[a.Device] = true
		if len(a.Times) < minScheduled {
			continue
		}
		gaps := make([]time.Duration, 0, len(a.Times)-1)
		for i := 0; i+1 < len(a.Times); i++ {
			gaps = append(gaps, a.Times[i].Sub(a.Times[i+1]))
		}
		usual := time.Duration(Median(durations(gaps)))
		thr := SilenceThreshold(gaps, m.cfg.SilenceMin)
		last := a.Times[0]

		st, err := m.repo.LoadAlert(ctx, "silence", a.Device)
		if err != nil {
			return err
		}
		if st == nil {
			st = newState("silence", a.Device, Entity{Device: a.Device})
		}
		if now.Sub(last) > thr {
			if !st.Firing {
				since := last.Add(thr)
				st.Firing, st.Since, st.NotifiedAt = true, &since, nil
			}
			applyText(st, SilenceText(a.Device, last, now, usual))
		} else {
			st.Firing = false
		}
		if err := m.repo.SaveAlert(ctx, st); err != nil {
			return err
		}
	}

	// A device with no scheduled tests at all for two weeks is retired, not "silent": stop alerting without a message.
	firing, err := m.repo.FiringAlerts(ctx)
	if err != nil {
		return err
	}
	for i := range firing {
		if firing[i].Kind == "silence" && !seen[firing[i].Device] {
			firing[i].Firing, firing[i].NotifiedAt = false, nil
			if err := m.repo.SaveAlert(ctx, &firing[i]); err != nil {
				return err
			}
		}
	}
	return nil
}

func durations(ds []time.Duration) []float64 {
	out := make([]float64, len(ds))
	for i, d := range ds {
		out[i] = float64(d)
	}
	return out
}

func labelOf(st store.AlertState) string {
	if st.ConnectionType == nil {
		return st.Device
	}
	e := Entity{Device: st.Device, ConnectionType: *st.ConnectionType}
	if st.Provider != nil {
		e.Provider = *st.Provider
	}
	return e.Label()
}

func recoveryText(st store.AlertState, now time.Time) Message {
	what := map[string]string{
		"slow":    "Downloads are back to normal.",
		"loss":    "Packet loss has cleared.",
		"silence": "Tests are arriving again.",
	}[st.Kind]
	if st.Since != nil && now.After(*st.Since) {
		what += fmt.Sprintf(" The problem lasted %s.", Humanize(now.Sub(*st.Since)))
	}
	return Message{Title: "Recovered: " + labelOf(st), Body: what, Priority: PriorityRecovery, Tags: []string{"white_check_mark", st.Kind}}
}

// dispatch sends what is due. A failed delivery leaves the state untouched, so the next check tries again.
func (m *Monitor) dispatch(ctx context.Context, now time.Time) error {
	states, err := m.repo.AlertsToHandle(ctx)
	if err != nil {
		return err
	}
	for i := range states {
		st := &states[i]
		var msg Message
		var event string
		switch {
		case st.Firing && st.NotifiedAt == nil:
			event, msg = "alert", alertMessage(st, "")
		case st.Firing && m.cfg.RemindEvery > 0 && st.NotifiedAt != nil && now.Sub(*st.NotifiedAt) >= m.cfg.RemindEvery &&
			st.Since != nil && now.Sub(*st.Since) < maxReminderAge:
			event, msg = "reminder", alertMessage(st, "Still: ")
		case !st.Firing && st.NotifiedAt != nil:
			event, msg = "recovery", recoveryText(*st, now)
		default:
			continue
		}
		if err := m.deliver(ctx, st, event, msg, now); err != nil {
			return err
		}
	}
	return nil
}

func alertMessage(st *store.AlertState, prefix string) Message {
	title, detail := "", ""
	if st.Title != nil {
		title = *st.Title
	}
	if st.Detail != nil {
		detail = *st.Detail
	}
	return Message{Title: prefix + title, Body: detail, Priority: Priority(st.Severity), Tags: Tags(st.Kind, st.Severity)}
}

func (m *Monitor) deliver(ctx context.Context, st *store.AlertState, event string, msg Message, now time.Time) error {
	sendCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	sendErr := m.send.Send(sendCtx, msg)

	key := st.Kind + "|" + st.Subject
	kind, subject := st.Kind, st.Subject
	ev := store.NotificationEvent{Event: event, Kind: &kind, Subject: &subject, Title: msg.Title, Message: msg.Body, Delivered: sendErr == nil}
	if sendErr != nil {
		es := sendErr.Error()
		ev.Error = &es
		m.log.Warn("could not deliver notification", "kind", st.Kind, "subject", st.Subject, "event", event, "err", es)
		if last, ok := m.lastFail[key]; ok && now.Sub(last) < failLogEvery {
			return nil // already recorded recently; keep retrying quietly
		}
		m.lastFail[key] = now
		return m.repo.AddEvent(ctx, ev)
	}
	delete(m.lastFail, key)
	if err := m.repo.AddEvent(ctx, ev); err != nil {
		return err
	}
	if event == "recovery" {
		st.NotifiedAt, st.Streak, st.StreakSince = nil, 0, nil
	} else {
		t := now
		st.NotifiedAt = &t
	}
	return m.repo.SaveAlert(ctx, st)
}

// Status is what the dashboard shows. It never includes the topic or credentials.
func (m *Monitor) Status(ctx context.Context) (model.NotificationStatus, error) {
	firing, err := m.repo.FiringAlerts(ctx)
	if err != nil {
		return model.NotificationStatus{}, err
	}
	recent, err := m.repo.RecentEvents(ctx, 20)
	if err != nil {
		return model.NotificationStatus{}, err
	}
	host := m.cfg.Host()
	st := model.NotificationStatus{
		Enabled: true,
		Server:  &host,
		Rules: &model.NotificationRules{
			SlowRatio: m.cfg.SlowRatio, SlowConsecutive: m.cfg.SlowConsecutive, RecoverRatio: m.cfg.RecoverRatio,
			LossPct: m.cfg.LossPct, LossConsecutive: m.cfg.LossConsecutive, SilenceMinHours: m.cfg.SilenceMin.Hours(),
			RemindHours: m.cfg.RemindEvery.Hours(), BaselineDays: m.cfg.BaselineDays,
		},
		Active: []model.ActiveAlert{},
		Recent: []model.NotificationEvent{},
	}
	for _, a := range firing {
		if a.Since == nil || a.Title == nil || a.Detail == nil {
			continue
		}
		st.Active = append(st.Active, model.ActiveAlert{
			Kind: a.Kind, Subject: a.Subject, Title: *a.Title, Detail: *a.Detail, Severity: a.Severity,
			Since: a.Since.UTC(), NotifiedAt: a.NotifiedAt,
		})
	}
	for _, e := range recent {
		st.Recent = append(st.Recent, model.NotificationEvent{
			ID: e.ID, At: e.At, Event: e.Event, Kind: e.Kind, Title: e.Title, Message: e.Message, Delivered: e.Delivered, Error: e.Error,
		})
	}
	return st, nil
}

// DisabledStatus is reported when NTFY_TOPIC is not set.
func DisabledStatus() model.NotificationStatus {
	return model.NotificationStatus{Active: []model.ActiveAlert{}, Recent: []model.NotificationEvent{}}
}

// SendTest sends a message that proves the setup works.
func SendTest(ctx context.Context, s Sender) error {
	return s.Send(ctx, Message{
		Title: "Speedtest test notification", Priority: 3, Tags: []string{"white_check_mark"},
		Body: "If you can read this, notifications are working.",
	})
}

var _ Repo = (*store.Store)(nil)
