package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strconv"
	"strings"
	"time"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/model"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/ondemand"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/store"
)

// OnDemand is what the handlers need from the on-demand test manager. It is nil when on-demand tests are not enabled.
type OnDemand interface {
	Capabilities() model.Capabilities
	Create(ctx context.Context, req ondemand.CreateRequest) (model.Test, error)
	Get(ctx context.Context, id string) (model.Test, error)
	List(ctx context.Context, states []string, limit int) ([]model.Test, error)
	Cancel(ctx context.Context, id string) (model.Test, error)
	Result(ctx context.Context, id string) (model.Result, error)
	Subscribe(id string, lastID int64) ondemand.Subscription
	Submit(ctx context.Context, id, token string, summary []byte, clientIP string) (model.Result, error)
}

var testStates = []string{"queued", "running", "completed", "failed", "cancelled"}

// SetOnDemand connects the on-demand test manager. trustProxy says whether X-Forwarded-For can be believed (only when
// the server sits behind your own reverse proxy).
func (s *Server) SetOnDemand(od OnDemand, trustProxy bool) {
	s.onDemand, s.trustProxy = od, trustProxy
}

// clientIP is the address the request came from, or "" if it cannot be determined.
func (s *Server) clientIP(r *http.Request) string {
	candidate := ""
	if s.trustProxy {
		if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
			candidate = strings.TrimSpace(strings.Split(xff, ",")[0])
		}
	}
	if candidate == "" {
		host, _, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			host = r.RemoteAddr
		}
		candidate = host
	}
	if a, err := netip.ParseAddr(candidate); err == nil {
		return a.Unmap().String()
	}
	return ""
}

func (s *Server) noOnDemand(w http.ResponseWriter) bool {
	if s.onDemand != nil {
		return false
	}
	writeProblem(w, http.StatusServiceUnavailable, "on-demand tests are not enabled on this server")
	return true
}

func (s *Server) capabilities(w http.ResponseWriter, r *http.Request) {
	if s.onDemand == nil {
		why := "On-demand tests are not enabled on this server."
		writeJSON(w, http.StatusOK, model.Capabilities{
			DeviceName: "server", ServerConnectionType: "wired",
			Ookla: model.Capability{Reason: &why}, NDT7: model.Capability{Reason: &why},
		})
		return
	}
	writeJSON(w, http.StatusOK, s.onDemand.Capabilities())
}

func (s *Server) createTest(w http.ResponseWriter, r *http.Request) {
	if s.noOnDemand(w) {
		return
	}
	// Anonymous callers are allowed (the dashboard has no login). A token that is present must be valid, though:
	// quietly ignoring a wrong credential would hide mistakes.
	tokenDevice := ""
	if r.Header.Get("Authorization") != "" {
		device, ok := s.auth.Device(r)
		if !ok {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeProblem(w, http.StatusUnauthorized, "missing or invalid bearer token")
			return
		}
		tokenDevice = device
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var body struct {
		Provider       string          `json:"provider"`
		Mode           string          `json:"mode"`
		ConnectionType string          `json:"connectionType"`
		Device         json.RawMessage `json:"device"` // omitted is fine; null, a non-string or a blank string is not
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeProblem(w, http.StatusBadRequest, "body must be JSON with provider, mode and connectionType")
		return
	}
	device := ""
	if len(body.Device) > 0 {
		var name string
		if err := json.Unmarshal(body.Device, &name); err != nil || strings.TrimSpace(name) == "" {
			writeProblem(w, http.StatusBadRequest, "request validation failed",
				FieldError{Field: "device", Message: "must be a non-empty string; leave it out if it does not apply"})
			return
		}
		device = strings.TrimSpace(name)
	}
	test, err := s.onDemand.Create(r.Context(), ondemand.CreateRequest{
		Provider: body.Provider, Mode: body.Mode, ConnectionType: body.ConnectionType, Device: device,
		TokenDevice: tokenDevice, ClientIP: s.clientIP(r),
	})
	var (
		verr  ondemand.ValidationError
		unav  ondemand.UnavailableError
		limit ondemand.RateLimitedError
	)
	switch {
	case err == nil:
		writeJSON(w, http.StatusCreated, test)
	case errors.As(err, &verr):
		writeProblem(w, http.StatusBadRequest, "request validation failed", FieldError{Field: verr.Field, Message: verr.Message})
	case errors.As(err, &unav):
		writeProblem(w, http.StatusServiceUnavailable, unav.Reason)
	case errors.As(err, &limit):
		w.Header().Set("Retry-After", strconv.Itoa(limit.RetryAfter))
		writeProblem(w, http.StatusTooManyRequests, "too many tests started from this address; wait a moment")
	case errors.Is(err, store.ErrServerBusy):
		writeProblem(w, http.StatusConflict, "A speed test is already running on the server. Wait for it to finish.")
	default:
		s.serverError(w, err)
	}
}

func (s *Server) listTests(w http.ResponseWriter, r *http.Request) {
	if s.noOnDemand(w) {
		return
	}
	q := r.URL.Query()
	var e errs
	states := enumList(q, "state", testStates, &e)
	limit := parseLimit(q, 20, 100, &e)
	if len(e) > 0 {
		writeProblem(w, http.StatusBadRequest, "invalid query parameters", e...)
		return
	}
	items, err := s.onDemand.List(r.Context(), states, limit)
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (s *Server) getTest(w http.ResponseWriter, r *http.Request) {
	if s.noOnDemand(w) {
		return
	}
	t, err := s.onDemand.Get(r.Context(), r.PathValue("id"))
	s.respondTest(w, t, err)
}

func (s *Server) cancelTest(w http.ResponseWriter, r *http.Request) {
	if s.noOnDemand(w) {
		return
	}
	t, err := s.onDemand.Cancel(r.Context(), r.PathValue("id"))
	s.respondTest(w, t, err)
}

func (s *Server) respondTest(w http.ResponseWriter, t model.Test, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "no such test")
	case err != nil:
		s.serverError(w, err)
	default:
		writeJSON(w, http.StatusOK, t)
	}
}

func writeSSE(w http.ResponseWriter, id int64, name string, data []byte) {
	if id > 0 {
		fmt.Fprintf(w, "id: %d\n", id)
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", name, data)
}

func isTerminal(state string) bool {
	return state == "completed" || state == "failed" || state == "cancelled"
}

func lastEventID(r *http.Request) int64 {
	v := r.Header.Get("Last-Event-ID")
	if v == "" {
		v = r.URL.Query().Get("lastEventId")
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}

// testEvents streams a test's live events (text/event-stream). A new listener first gets the current state, then
// anything it missed since Last-Event-ID, then live events until the test ends.
func (s *Server) testEvents(w http.ResponseWriter, r *http.Request) {
	if s.noOnDemand(w) {
		return
	}
	id := r.PathValue("id")
	sub := s.onDemand.Subscribe(id, lastEventID(r)) // subscribe before reading the state, so nothing falls in between
	defer sub.Cancel()
	test, err := s.onDemand.Get(r.Context(), id)
	if err != nil {
		s.respondTest(w, test, err)
		return
	}

	rc := http.NewResponseController(w)
	_ = rc.SetWriteDeadline(time.Time{}) // this response is meant to stay open longer than the server's write timeout
	h := w.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("X-Accel-Buffering", "no") // do not let a reverse proxy hold events back
	w.WriteHeader(http.StatusOK)
	flush := func() { _ = rc.Flush() }

	state, _ := json.Marshal(ondemand.StateEvent{Type: "state", Test: test})
	writeSSE(w, 0, "state", state)

	// This process has no record of the test (it ended before a restart): report how it ended and stop.
	finishFromStore := func(t model.Test) {
		switch {
		case t.State == "completed" && t.ResultID != nil:
			if res, err := s.onDemand.Result(r.Context(), *t.ResultID); err == nil {
				b, _ := json.Marshal(ondemand.ResultEvent{Type: "result", Result: res})
				writeSSE(w, 0, "result", b)
			}
		case t.State == "failed":
			msg := "The test failed."
			if t.Error != nil {
				msg = *t.Error
			}
			b, _ := json.Marshal(ondemand.ErrorEvent{Type: "error", Message: msg})
			writeSSE(w, 0, "error", b)
		}
		flush()
	}
	if !sub.Exists && isTerminal(test.State) {
		finishFromStore(test)
		return
	}
	for _, ev := range sub.Replay {
		writeSSE(w, ev.ID, ev.Name, ev.Data)
	}
	flush()
	if sub.Closed {
		return
	}

	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	var poll <-chan time.Time
	if !sub.Exists { // nobody in this process will publish for it; watch the database for its end
		t := time.NewTicker(5 * time.Second)
		defer t.Stop()
		poll = t.C
	}
	for {
		select {
		case ev, ok := <-sub.Live:
			if !ok {
				return
			}
			writeSSE(w, ev.ID, ev.Name, ev.Data)
			flush()
		case <-heartbeat.C:
			fmt.Fprint(w, ": keep-alive\n\n")
			flush()
		case <-poll:
			if cur, err := s.onDemand.Get(r.Context(), id); err == nil && isTerminal(cur.State) {
				st, _ := json.Marshal(ondemand.StateEvent{Type: "state", Test: cur})
				writeSSE(w, 0, "state", st)
				finishFromStore(cur)
				return
			}
		case <-r.Context().Done():
			return
		}
	}
}

// submitResult stores the NDT7 result a browser measured. The single-use token from POST /tests authenticates it.
func (s *Server) submitResult(w http.ResponseWriter, r *http.Request) {
	if s.noOnDemand(w) {
		return
	}
	scheme, token, _ := strings.Cut(r.Header.Get("Authorization"), " ")
	token = strings.TrimSpace(token)
	if !strings.EqualFold(scheme, "bearer") || token == "" {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeProblem(w, http.StatusUnauthorized, "missing or invalid submit token")
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	if e := validateBrowserNDT7(body); len(e) > 0 {
		writeProblem(w, http.StatusBadRequest, "request validation failed", e...)
		return
	}
	res, err := s.onDemand.Submit(r.Context(), r.PathValue("id"), token, body, s.clientIP(r))
	var closed store.ClosedError
	switch {
	case err == nil:
		writeJSON(w, http.StatusCreated, res)
	case errors.Is(err, store.ErrNotFound):
		writeProblem(w, http.StatusNotFound, "no such test")
	case errors.Is(err, store.ErrTokenInvalid):
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeProblem(w, http.StatusUnauthorized, "missing or invalid submit token")
	case errors.As(err, &closed):
		writeProblem(w, http.StatusConflict, "This test is already "+closed.State+" and cannot take another result.")
	case errors.Is(err, store.ErrTestExpired):
		writeProblem(w, http.StatusConflict, "This test was not finished in time. Start a new one.")
	default:
		s.insertFailed(w, err)
	}
}

// validateBrowserNDT7 applies the NDT7 summary rules plus checks that only make sense for a live browser test.
func validateBrowserNDT7(body []byte) errs {
	e := validateNDT7(body, "")
	if len(e) > 0 {
		return e
	}
	var p ndt7Payload
	_ = json.Unmarshal(body, &p)
	started, _ := time.Parse(time.RFC3339, p.StartedAt)
	finished, _ := time.Parse(time.RFC3339, p.FinishedAt)
	if d := finished.Sub(started); d < time.Second || d > 10*time.Minute {
		e.add("finishedAt", "a test lasts between 1 second and 10 minutes")
	}
	if time.Since(finished) > time.Hour {
		e.add("finishedAt", "must be recent: results of an old test cannot be submitted")
	}
	return e
}
