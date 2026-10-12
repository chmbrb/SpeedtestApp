package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/push"
)

// Pusher is what the push endpoints need; *push.Service implements it. It is nil when push is off.
type Pusher interface {
	PublicKey() string
	Subscribe(ctx context.Context, endpoint, p256dh, auth, userAgent string) error
	Unsubscribe(ctx context.Context, endpoint string) error
	SendTest(ctx context.Context, endpoint string) error
}

// SetPush turns on the browser-notification endpoints.
func (s *Server) SetPush(p Pusher) { s.pusher = p }

type pushConfig struct {
	Enabled   bool   `json:"enabled"`
	PublicKey string `json:"publicKey,omitempty"`
}

func (s *Server) pushConfig(w http.ResponseWriter, r *http.Request) {
	if s.pusher == nil {
		writeJSON(w, http.StatusOK, pushConfig{})
		return
	}
	writeJSON(w, http.StatusOK, pushConfig{Enabled: true, PublicKey: s.pusher.PublicKey()})
}

// pushGate answers 503 when push is off and 429 when the caller is asking too often. ok=false means a response was written.
func (s *Server) pushGate(w http.ResponseWriter, r *http.Request, l *limiter) bool {
	if s.pusher == nil {
		writeProblem(w, http.StatusServiceUnavailable, "browser notifications are not enabled on this server")
		return false
	}
	if allowed, retry := l.allow(s.clientIP(r)); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeProblem(w, http.StatusTooManyRequests, "too many requests; try again shortly")
		return false
	}
	return true
}

func decodeEndpoint(w http.ResponseWriter, r *http.Request) (string, bool) {
	r.Body = http.MaxBytesReader(w, r.Body, 4096+1024)
	var body struct {
		Endpoint string `json:"endpoint"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Endpoint == "" {
		writeProblem(w, http.StatusBadRequest, "body must be JSON with the subscription's endpoint",
			FieldError{Field: "endpoint", Message: "is required"})
		return "", false
	}
	// Control characters (a NUL byte would be refused by the database as a 500) can never be part of a real endpoint.
	if strings.ContainsFunc(body.Endpoint, unicode.IsControl) {
		writeProblem(w, http.StatusBadRequest, "request validation failed",
			FieldError{Field: "endpoint", Message: "must not contain control characters"})
		return "", false
	}
	if len(body.Endpoint) > push.MaxEndpointLen {
		writeProblem(w, http.StatusBadRequest, "request validation failed",
			FieldError{Field: "endpoint", Message: "must be at most 2048 characters"})
		return "", false
	}
	return body.Endpoint, true
}

// writePushError maps the push package's errors to responses.
func (s *Server) writePushError(w http.ResponseWriter, err error) {
	var ve *push.ValidationError
	switch {
	case errors.As(err, &ve):
		writeProblem(w, http.StatusBadRequest, "request validation failed", FieldError{Field: ve.Field, Message: ve.Message})
	case errors.Is(err, push.ErrTooMany):
		writeProblem(w, http.StatusConflict, "too many devices have notifications turned on")
	case errors.Is(err, push.ErrUnknown):
		writeProblem(w, http.StatusNotFound, "this device is not subscribed")
	case errors.Is(err, push.ErrNotAllowed):
		writeProblem(w, http.StatusBadGateway, err.Error())
	default:
		s.serverError(w, err)
	}
}

func (s *Server) pushSubscribe(w http.ResponseWriter, r *http.Request) {
	if !s.pushGate(w, r, s.pushLimiter) {
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096+1024)
	var body struct {
		Endpoint string `json:"endpoint"`
		Keys     struct {
			P256dh string `json:"p256dh"`
			Auth   string `json:"auth"`
		} `json:"keys"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeProblem(w, http.StatusBadRequest, "body must be a browser push subscription: endpoint and keys")
		return
	}
	if err := s.pusher.Subscribe(r.Context(), body.Endpoint, body.Keys.P256dh, body.Keys.Auth, r.UserAgent()); err != nil {
		s.writePushError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pushUnsubscribe(w http.ResponseWriter, r *http.Request) {
	if !s.pushGate(w, r, s.pushLimiter) {
		return
	}
	endpoint, ok := decodeEndpoint(w, r)
	if !ok {
		return
	}
	// Idempotent, and it never says whether the endpoint was known.
	if err := s.pusher.Unsubscribe(r.Context(), endpoint); err != nil {
		s.writePushError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) pushTest(w http.ResponseWriter, r *http.Request) {
	if !s.pushGate(w, r, s.pushTestLimiter) {
		return
	}
	endpoint, ok := decodeEndpoint(w, r)
	if !ok {
		return
	}
	if err := s.pusher.SendTest(r.Context(), endpoint); err != nil {
		s.writePushError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
