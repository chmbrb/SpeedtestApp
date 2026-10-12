package ondemand

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/model"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/store"
)

// Repo is the persistence the manager needs; *store.Store implements it.
type Repo interface {
	CreateTest(ctx context.Context, n store.NewTest) (store.TestRow, error)
	GetTest(ctx context.Context, id string) (store.TestRow, error)
	ListTests(ctx context.Context, states []string, limit int) ([]store.TestRow, error)
	MarkTestRunning(ctx context.Context, id string) error
	FinishTest(ctx context.Context, id, state string, resultID, errMsg *string) (store.TestRow, bool, error)
	InterruptServerTests(ctx context.Context) (int64, error)
	SweepTests(ctx context.Context, browserTTL, serverMax time.Duration) (int64, error)
	SubmitBrowserResult(ctx context.Context, id string, tokenHash []byte, ttl time.Duration, summary []byte, clientIP string) (string, error)
	InsertOokla(ctx context.Context, raw []byte, device, connectionType, trigger string) (string, error)
	GetResult(ctx context.Context, id string) (model.ResultDetail, error)
}

var _ Repo = (*store.Store)(nil)

// Errors the API turns into responses.
type ValidationError struct{ Field, Message string }

func (e ValidationError) Error() string { return e.Field + ": " + e.Message }

type UnavailableError struct{ Reason string }

func (e UnavailableError) Error() string { return e.Reason }

type RateLimitedError struct{ RetryAfter int }

func (e RateLimitedError) Error() string { return "too many tests started; slow down" }

// Events as sent over SSE (docs/openapi.yaml: TestEvent).
type StateEvent struct {
	Type string     `json:"type"`
	Test model.Test `json:"test"`
}
type ResultEvent struct {
	Type   string       `json:"type"`
	Result model.Result `json:"result"`
}
type ErrorEvent struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

var errCancelled = errors.New("cancelled by request")

// CreateRequest is what a caller asks for. TokenDevice is set when the caller authenticated with an agent token (the
// token decides the device); ClientIP is the address the request came from.
type CreateRequest struct {
	Provider, Mode, ConnectionType, Device string
	TokenDevice                            string
	ClientIP                               string
}

type Manager struct {
	cfg      Config
	repo     Repo
	runner   Runner
	hub      *Hub
	log      *slog.Logger
	lookPath func(string) (string, error)
	limiter  *ipLimiter

	mu      sync.Mutex
	root    context.Context
	cancels map[string]context.CancelCauseFunc
	done    map[string]chan struct{}
}

// NewManager builds a manager. runner may be nil, in which case the real Ookla CLI is used when Ookla is enabled.
func NewManager(cfg Config, repo Repo, runner Runner, log *slog.Logger) *Manager {
	if runner == nil {
		runner = Ookla{Path: cfg.OoklaPath}
	}
	return &Manager{
		cfg: cfg, repo: repo, runner: runner, hub: NewHub(), log: log, lookPath: exec.LookPath,
		limiter: newIPLimiter(max(4, cfg.CreatePerMinute), cfg.CreatePerMinute), root: context.Background(),
		cancels: map[string]context.CancelCauseFunc{}, done: map[string]chan struct{}{},
	}
}

// Start ties the manager to ctx (tests stop when it ends) and fails tests a previous process left running, so they
// cannot block new ones. Call it before serving requests.
func (m *Manager) Start(ctx context.Context) {
	m.mu.Lock()
	m.root = ctx
	m.mu.Unlock()
	if n, err := m.repo.InterruptServerTests(ctx); err != nil {
		m.log.Error("could not clear tests left over from a restart", "err", err)
	} else if n > 0 {
		m.log.Warn("marked tests left over from a restart as failed", "count", n)
	}
}

// Run tidies up once a minute until ctx ends: stale tests, old event streams, idle rate-limit entries.
func (m *Manager) Run(ctx context.Context) {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if _, err := m.repo.SweepTests(ctx, m.cfg.BrowserTTL, m.cfg.TestTimeout+2*time.Minute); err != nil && ctx.Err() == nil {
				m.log.Error("could not sweep stale tests", "err", err)
			}
			m.hub.Forget()
			m.limiter.forget()
		}
	}
}

func (m *Manager) Capabilities() model.Capabilities {
	ok, why := m.cfg.OoklaStatus(m.lookPath)
	c := model.Capabilities{
		DeviceName: m.cfg.DeviceName, ServerConnectionType: m.cfg.ServerConnection,
		Ookla: model.Capability{Available: ok}, NDT7: model.Capability{Available: true},
	}
	if !ok {
		c.Ookla.Reason = &why
	}
	return c
}

// TestModel converts a stored test to its API shape.
func TestModel(r store.TestRow) model.Test {
	return model.Test{
		ID: r.ID, Provider: r.Provider, Mode: r.Mode, Device: r.Device, ConnectionType: r.ConnectionType, State: r.State,
		CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt, ResultID: r.ResultID, Error: r.Error,
	}
}

func cleanName(s string) bool {
	return s != "" && len(s) <= 64 && utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}

// Create starts a test. For a server test it returns at once and the test runs in the background; for a browser test
// the response carries the single-use token (shown only here).
func (m *Manager) Create(ctx context.Context, req CreateRequest) (model.Test, error) {
	if req.Provider != "ookla" && req.Provider != "ndt7" {
		return model.Test{}, ValidationError{"provider", "must be ookla or ndt7"}
	}
	if req.Mode != "server" && req.Mode != "browser" {
		return model.Test{}, ValidationError{"mode", "must be server or browser"}
	}
	if req.Mode == "server" && req.Provider != "ookla" {
		return model.Test{}, ValidationError{"provider", "NDT7 runs in the browser; use mode browser"}
	}
	if req.Mode == "browser" && req.Provider != "ndt7" {
		return model.Test{}, ValidationError{"provider", "Ookla tests can only run on the server; use mode server"}
	}
	// The contract requires both fields in every case, even where a value is ignored (a server test always uses the
	// server's own device name and connection type): a request that breaks the contract is refused, not half-honoured.
	if req.ConnectionType != "wired" && req.ConnectionType != "wireless" {
		return model.Test{}, ValidationError{"connectionType", "must be wired or wireless"}
	}
	if req.Device != "" && !cleanName(req.Device) {
		return model.Test{}, ValidationError{"device", "must be a name of up to 64 characters, without control characters"}
	}
	device := req.Device
	if req.TokenDevice != "" {
		device = req.TokenDevice
	}
	if req.Mode == "browser" && !cleanName(device) {
		return model.Test{}, ValidationError{"device", "is required: a name of up to 64 characters, without control characters"}
	}
	if ok, retry := m.limiter.allow(req.ClientIP); !ok {
		return model.Test{}, RateLimitedError{RetryAfter: retry}
	}

	if req.Mode == "server" {
		if ok, why := m.cfg.OoklaStatus(m.lookPath); !ok {
			return model.Test{}, UnavailableError{why}
		}
		row, err := m.repo.CreateTest(ctx, store.NewTest{
			Provider: "ookla", Mode: "server", Device: m.cfg.DeviceName, ConnectionType: m.cfg.ServerConnection,
			State: "queued", ClientIP: req.ClientIP,
		})
		if err != nil {
			return model.Test{}, err
		}
		test := TestModel(row)
		m.hub.Publish(row.ID, "state", StateEvent{Type: "state", Test: test})
		go m.runServer(row)
		return test, nil
	}

	token, hash, err := newToken()
	if err != nil {
		return model.Test{}, err
	}
	row, err := m.repo.CreateTest(ctx, store.NewTest{
		Provider: "ndt7", Mode: "browser", Device: device, ConnectionType: req.ConnectionType,
		State: "running", TokenHash: hash, ClientIP: req.ClientIP,
	})
	if err != nil {
		return model.Test{}, err
	}
	test := TestModel(row)
	test.SubmitToken = token
	return test, nil
}

func newToken() (token string, hash []byte, err error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	token = base64.RawURLEncoding.EncodeToString(b)
	sum := sha256.Sum256([]byte(token))
	return token, sum[:], nil
}

func hashToken(token string) []byte { sum := sha256.Sum256([]byte(token)); return sum[:] }

func (m *Manager) runServer(row store.TestRow) {
	id := row.ID
	m.mu.Lock()
	root := m.root
	m.mu.Unlock()

	ctx, cancel := context.WithCancelCause(root)
	tctx, tcancel := context.WithTimeout(ctx, m.cfg.TestTimeout)
	finished := make(chan struct{})
	m.mu.Lock()
	m.cancels[id], m.done[id] = cancel, finished
	m.mu.Unlock()
	defer func() {
		tcancel()
		cancel(nil)
		m.mu.Lock()
		delete(m.cancels, id)
		delete(m.done, id)
		m.mu.Unlock()
		close(finished)
	}()

	bg, bgCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer bgCancel()
	if err := m.repo.MarkTestRunning(bg, id); err != nil {
		m.finish(id, "failed", nil, "The test could not be started: "+err.Error())
		return
	}
	if cur, err := m.repo.GetTest(bg, id); err == nil {
		m.hub.Publish(id, "state", StateEvent{Type: "state", Test: TestModel(cur)})
	}

	raw, err := m.runner.Run(tctx, func(p Progress) { m.hub.Publish(id, "progress", p) })

	if tctx.Err() != nil { // cancelled, timed out, or the server is shutting down
		switch cause := context.Cause(tctx); {
		case errors.Is(cause, errCancelled):
			m.finish(id, "cancelled", nil, "")
		case errors.Is(cause, context.DeadlineExceeded):
			m.finish(id, "failed", nil, fmt.Sprintf("The test took longer than %s and was stopped.", m.cfg.TestTimeout))
		default:
			m.finish(id, "failed", nil, "The server was shutting down.")
		}
		return
	}
	if err != nil {
		m.finish(id, "failed", nil, err.Error())
		return
	}
	// bg started its 15 seconds before the test did, so it has usually expired by now: saving needs its own.
	saveCtx, saveCancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer saveCancel()
	resultID, err := m.repo.InsertOokla(saveCtx, raw, m.cfg.DeviceName, m.cfg.ServerConnection, "on_demand")
	if err != nil {
		m.log.Error("could not store an on-demand result", "err", err)
		m.finish(id, "failed", nil, "The test finished but its result could not be saved.")
		return
	}
	m.finish(id, "completed", &resultID, "")
}

// finish records the end of a test, tells subscribers, and closes the stream.
func (m *Manager) finish(id, state string, resultID *string, errMsg string) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var em *string
	if errMsg != "" {
		em = &errMsg
	}
	row, changed, err := m.repo.FinishTest(ctx, id, state, resultID, em)
	if err != nil {
		m.log.Error("could not record the end of a test", "id", id, "err", err)
		return
	}
	if changed {
		m.hub.Publish(id, "state", StateEvent{Type: "state", Test: TestModel(row)})
		switch state {
		case "completed":
			if resultID != nil {
				if res, err := m.repo.GetResult(ctx, *resultID); err == nil {
					m.hub.Publish(id, "result", ResultEvent{Type: "result", Result: res.Result})
				}
			}
		case "failed":
			m.hub.Publish(id, "error", ErrorEvent{Type: "error", Message: errMsg})
		}
	}
	m.hub.Close(id)
}

// Cancel stops a test and returns its final state. Cancelling a test that has already ended just reports it.
func (m *Manager) Cancel(ctx context.Context, id string) (model.Test, error) {
	row, err := m.repo.GetTest(ctx, id)
	if err != nil {
		return model.Test{}, err
	}
	if row.Terminal() {
		return TestModel(row), nil
	}
	m.mu.Lock()
	cancel, running := m.cancels[id]
	done := m.done[id]
	m.mu.Unlock()
	if running {
		cancel(errCancelled)
		select { // give the runner a moment to wind down so the answer is the final state
		case <-done:
		case <-time.After(6 * time.Second):
		case <-ctx.Done():
		}
	} else { // a browser test, or a server test this process is not running
		m.finish(id, "cancelled", nil, "")
	}
	row, err = m.repo.GetTest(ctx, id)
	return TestModel(row), err
}

func (m *Manager) Get(ctx context.Context, id string) (model.Test, error) {
	row, err := m.repo.GetTest(ctx, id)
	return TestModel(row), err
}

func (m *Manager) List(ctx context.Context, states []string, limit int) ([]model.Test, error) {
	rows, err := m.repo.ListTests(ctx, states, limit)
	out := make([]model.Test, 0, len(rows))
	for _, r := range rows {
		out = append(out, TestModel(r))
	}
	return out, err
}

// Result returns the stored result a completed test produced.
func (m *Manager) Result(ctx context.Context, id string) (model.Result, error) {
	d, err := m.repo.GetResult(ctx, id)
	return d.Result, err
}

func (m *Manager) Subscribe(id string, lastID int64) Subscription { return m.hub.Subscribe(id, lastID) }

// Submit stores a browser test's result. token is the single-use token from Create. summary is the validated NDT7
// summary. It returns the stored result.
func (m *Manager) Submit(ctx context.Context, id, token string, summary []byte, clientIP string) (model.Result, error) {
	resultID, err := m.repo.SubmitBrowserResult(ctx, id, hashToken(token), m.cfg.BrowserTTL, summary, clientIP)
	if err != nil {
		return model.Result{}, err
	}
	m.hub.Close(id)
	return m.Result(ctx, resultID)
}

/* ---------- per-address rate limit for starting tests ---------- */

type ipLimiter struct {
	mu      sync.Mutex
	buckets map[string]*ipBucket
	burst   float64
	perSec  float64
	now     func() time.Time
}

type ipBucket struct {
	tokens float64
	last   time.Time
}

func newIPLimiter(burst, perMinute int) *ipLimiter {
	return &ipLimiter{buckets: map[string]*ipBucket{}, burst: float64(burst), perSec: float64(perMinute) / 60, now: time.Now}
}

func (l *ipLimiter) allow(key string) (bool, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	b := l.buckets[key]
	if b == nil {
		b = &ipBucket{tokens: l.burst, last: now}
		l.buckets[key] = b
	}
	b.tokens = min(l.burst, b.tokens+now.Sub(b.last).Seconds()*l.perSec)
	b.last = now
	if b.tokens >= 1 {
		b.tokens--
		return true, 0
	}
	return false, int((1-b.tokens)/l.perSec) + 1
}

func (l *ipLimiter) forget() {
	l.mu.Lock()
	defer l.mu.Unlock()
	for k, b := range l.buckets {
		if l.now().Sub(b.last) > 10*time.Minute {
			delete(l.buckets, k)
		}
	}
}
