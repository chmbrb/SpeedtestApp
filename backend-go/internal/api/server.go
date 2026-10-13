// Package api implements the HTTP contract in docs/openapi.yaml.
package api

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/login"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/model"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/notify"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/store"
	"github.com/jackc/pgx/v5/pgconn"
)

// Notifications reports the state of the ntfy alerts. It is nil when notifications are off.
type Notifications interface {
	Status(ctx context.Context) (model.NotificationStatus, error)
}

// Store is what the handlers need from the database layer.
type Store interface {
	Ping(ctx context.Context) error
	ListResults(ctx context.Context, f store.Filter, limit int, after *store.Cursor) ([]model.Result, *store.Cursor, error)
	GetResult(ctx context.Context, id string) (model.ResultDetail, error)
	Summary(ctx context.Context, f store.Filter) (model.Summary, error)
	Timeseries(ctx context.Context, f store.Filter, interval string, groupBy []string, tz string) (model.Timeseries, error)
	Devices(ctx context.Context) ([]model.Device, error)
	InsertOokla(ctx context.Context, raw []byte, device, connectionType, trigger string) (string, error)
	InsertNDT7(ctx context.Context, raw []byte, device, connectionType, trigger, clientIP string) (string, error)
}

type Server struct {
	store           Store
	auth            *Authenticator
	limiter         *limiter
	limitIngest     bool
	tz              string // default time zone for timeseries
	log             *slog.Logger
	mux             *http.ServeMux
	static          http.Handler // optional built frontend; see ServeStatic
	notifier        Notifications
	pusher          Pusher
	pushLimiter     *limiter
	pushTestLimiter *limiter
	onDemand        OnDemand
	trustProxy      bool
	login           *login.Service // nil: no login, the dashboard is open
	loginLimiter    *limiter       // per address
	loginGlobal     *limiter       // all addresses together
}

func NewServer(st Store, auth *Authenticator, defaultTZ string, log *slog.Logger) *Server {
	s := &Server{store: st, auth: auth, limiter: newLimiter(20, 30), limitIngest: true, pushLimiter: newLimiter(10, 20), pushTestLimiter: newLimiter(3, 6), tz: defaultTZ, log: log, mux: http.NewServeMux()}
	s.mux.HandleFunc("GET /api/v1/health", s.health)
	s.mux.HandleFunc("GET /api/v1/devices", s.devices)
	s.mux.HandleFunc("GET /api/v1/results", s.listResults)
	s.mux.HandleFunc("POST /api/v1/results", s.postResult)
	s.mux.HandleFunc("GET /api/v1/results/{id}", s.getResult)
	s.mux.HandleFunc("GET /api/v1/stats/summary", s.summary)
	s.mux.HandleFunc("GET /api/v1/stats/timeseries", s.timeseries)
	s.mux.HandleFunc("GET /api/v1/notifications", s.notifications)
	s.mux.HandleFunc("GET /api/v1/push/config", s.pushConfig)
	s.mux.HandleFunc("POST /api/v1/push/subscriptions", s.pushSubscribe)
	s.mux.HandleFunc("DELETE /api/v1/push/subscriptions", s.pushUnsubscribe)
	s.mux.HandleFunc("POST /api/v1/push/test", s.pushTest)
	s.mux.HandleFunc("GET /api/v1/auth/session", s.authSession)
	s.mux.HandleFunc("POST /api/v1/auth/login", s.authLogin)
	s.mux.HandleFunc("POST /api/v1/auth/logout", s.authLogout)
	s.mux.HandleFunc("GET /api/v1/capabilities", s.capabilities)
	s.mux.HandleFunc("GET /api/v1/tests", s.listTests)
	s.mux.HandleFunc("POST /api/v1/tests", s.createTest)
	s.mux.HandleFunc("GET /api/v1/tests/{id}", s.getTest)
	s.mux.HandleFunc("DELETE /api/v1/tests/{id}", s.cancelTest)
	s.mux.HandleFunc("GET /api/v1/tests/{id}/events", s.testEvents)
	s.mux.HandleFunc("POST /api/v1/tests/{id}/result", s.submitResult)
	return s
}

// SetIngestRate sets the sustained POST rate allowed per device, per minute (burst 20). 0 disables limiting, including the
// limits on the browser-notification endpoints (and on login attempts, if SetLogin came first): it exists so load and fuzz tests can run, not for normal use.
func (s *Server) SetIngestRate(perMinute int) {
	s.limitIngest = perMinute > 0
	if perMinute > 0 {
		s.limiter = newLimiter(20, perMinute)
		s.resetLoginLimits()
		return
	}
	s.pushLimiter, s.pushTestLimiter = newLimiter(1_000_000, 1_000_000), newLimiter(1_000_000, 1_000_000)
	s.resetLoginLimits()
}

// SetNotifications connects the alert monitor so its status can be shown. Without it the endpoint reports "off".
func (s *Server) SetNotifications(n Notifications) { s.notifier = n }

// Handler wraps the routes with panic recovery, access logging, and problem+json 404/405 responses.
func (s *Server) Handler() http.Handler {
	return s.recoverer(s.logRequests(s.gate(http.HandlerFunc(s.route))))
}

func (s *Server) route(w http.ResponseWriter, r *http.Request) {
	h, pattern := s.mux.Handler(r)
	if pattern != "" {
		s.mux.ServeHTTP(w, r)
		return
	}
	if s.static != nil && (r.Method == http.MethodGet || r.Method == http.MethodHead) && !strings.HasPrefix(r.URL.Path, "/api/") {
		s.static.ServeHTTP(w, r)
		return
	}
	// No route matched: let the mux decide between 404 and 405 (with Allow), but answer in problem+json.
	rec := &discard{header: http.Header{}}
	h.ServeHTTP(rec, r)
	if rec.status == http.StatusMethodNotAllowed {
		w.Header().Set("Allow", rec.header.Get("Allow"))
		writeProblem(w, http.StatusMethodNotAllowed, "method not allowed for this path")
		return
	}
	writeProblem(w, http.StatusNotFound, "no such endpoint")
}

type discard struct {
	header http.Header
	status int
}

func (d *discard) Header() http.Header         { return d.header }
func (d *discard) Write(b []byte) (int, error) { return len(b), nil }
func (d *discard) WriteHeader(code int)        { d.status = code }

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.store.Ping(ctx); err != nil {
		s.log.Error("health check failed", "err", err)
		writeProblem(w, http.StatusServiceUnavailable, "database is unreachable")
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) devices(w http.ResponseWriter, r *http.Request) {
	items, err := s.store.Devices(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) listResults(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var e errs
	f := parseFilter(q, &e)
	limit := parseLimit(q, 100, 500, &e)
	var after *store.Cursor
	if q.Has("cursor") { // present but empty is not "no cursor": the contract wants a real one
		var ok bool
		if after, ok = decodeCursor(q.Get("cursor")); !ok {
			e.add("cursor", "is not a cursor returned by this API")
		}
	}
	if len(e) > 0 {
		writeProblem(w, http.StatusBadRequest, "invalid query parameters", e...)
		return
	}
	items, next, err := s.store.ListResults(r.Context(), f, limit, after)
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "nextCursor": encodeCursor(next)})
}

func (s *Server) getResult(w http.ResponseWriter, r *http.Request) {
	res, err := s.store.GetResult(r.Context(), r.PathValue("id"))
	if errors.Is(err, store.ErrNotFound) {
		writeProblem(w, http.StatusNotFound, "no such result")
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, res)
}

func (s *Server) summary(w http.ResponseWriter, r *http.Request) {
	var e errs
	f := parseFilter(r.URL.Query(), &e)
	if len(e) > 0 {
		writeProblem(w, http.StatusBadRequest, "invalid query parameters", e...)
		return
	}
	sum, err := s.store.Summary(r.Context(), f)
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, sum)
}

func (s *Server) timeseries(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	var e errs
	f := parseFilter(q, &e)
	interval := "day"
	if q.Has("interval") { // present but empty is not "use the default": the contract's enum has no empty value
		if interval = q.Get("interval"); !slices.Contains(intervals, interval) {
			e.add("interval", "must be one of %s", strings.Join(intervals, ", "))
		}
	}
	groupBy := enumList(q, "groupBy", groupFields, &e)
	if len(groupBy) == 0 {
		groupBy = groupFields
	}
	tz := q.Get("timezone")
	if tz == "" {
		tz = s.tz
	} else if !validTimezone(tz) {
		e.add("timezone", "must be an IANA time zone name such as America/Phoenix")
	}
	if len(e) > 0 {
		writeProblem(w, http.StatusBadRequest, "invalid query parameters", e...)
		return
	}
	ts, err := s.store.Timeseries(r.Context(), f, interval, groupBy, tz)
	if errors.Is(err, store.ErrTooManyPoints) {
		writeProblem(w, http.StatusBadRequest,
			"the range holds too many points for this interval; narrow from/to or use a coarser interval")
		return
	}
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, ts)
}

// insertFailed maps database rejections of bad input (data exceptions, constraint violations) to 400.
func (s *Server) insertFailed(w http.ResponseWriter, err error) {
	var pg *pgconn.PgError
	if errors.As(err, &pg) && (strings.HasPrefix(pg.Code, "22") || strings.HasPrefix(pg.Code, "23")) {
		writeProblem(w, http.StatusBadRequest, "the database rejected the result: "+pg.Message)
		return
	}
	s.serverError(w, err)
}

func (s *Server) serverError(w http.ResponseWriter, err error) {
	if errors.Is(err, context.Canceled) {
		// The client went away (closed tab, dropped connection) while we were working. That is not a server fault and
		// nobody is left to read an error body; 499 is the conventional status for the access log.
		s.log.Debug("client disconnected", "err", err)
		w.WriteHeader(499)
		return
	}
	s.log.Error("request failed", "err", err)
	writeProblem(w, http.StatusInternalServerError, "internal error")
}

func (s *Server) notifications(w http.ResponseWriter, r *http.Request) {
	if s.notifier == nil {
		writeJSON(w, http.StatusOK, notify.DisabledStatus())
		return
	}
	st, err := s.notifier.Status(r.Context())
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, st)
}
