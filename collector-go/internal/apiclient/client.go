// Package apiclient talks to the SpeedtestApp API: it sends results and checks that the setup works.
package apiclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chmbrb/SpeedtestApp/collector-go/internal/version"
)

// Kind says what a failure means for the result being sent, which decides whether it is kept for later.
type Kind int

const (
	// KindTransient: the server or the network failed. The result is kept and retried.
	KindTransient Kind = iota
	// KindRateLimited: too many results too fast. Kept and retried after RetryAfter.
	KindRateLimited
	// KindAuth: the token is wrong or not allowed. Kept (the token can be fixed), but nothing more is sent now.
	KindAuth
	// KindConfig: the address is wrong (not found, redirected). Kept: the fix is in the settings, not the data.
	KindConfig
	// KindRejected: the server understood and refused this result as invalid. It will never succeed: not retried.
	KindRejected
)

func (k Kind) String() string {
	return [...]string{"transient", "rate limited", "unauthorized", "misconfigured", "rejected"}[k]
}

// Error is a failed request, classified.
type Error struct {
	Kind       Kind
	Status     int           // 0 for network errors
	Detail     string        // what the server said, or what went wrong
	RetryAfter time.Duration // for KindRateLimited
	Err        error
}

func (e *Error) Error() string {
	if e.Status != 0 {
		return fmt.Sprintf("%s (HTTP %d): %s", e.Kind, e.Status, e.Detail)
	}
	return fmt.Sprintf("%s: %s", e.Kind, e.Detail)
}
func (e *Error) Unwrap() error { return e.Err }

// Keep reports whether a result that failed this way must be kept for a later attempt.
func (e *Error) Keep() bool { return e.Kind != KindRejected }

// Envelope is the body of POST /api/v1/results.
type Envelope struct {
	Provider       string          `json:"provider"`
	ConnectionType string          `json:"connectionType"`
	Trigger        string          `json:"trigger,omitempty"`
	Result         json.RawMessage `json:"result"`
}

type Client struct {
	base  string
	token string
	http  *http.Client
}

func New(base, token string) *Client {
	return &Client{
		base: strings.TrimRight(base, "/"), token: token,
		http: &http.Client{
			Timeout: 30 * time.Second,
			// A redirect would turn the POST into a GET and lose the result. Report it instead, so the address gets fixed.
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}
}

type problem struct {
	Detail string `json:"detail"`
	Title  string `json:"title"`
	Errors []struct {
		Field   string `json:"field"`
		Message string `json:"message"`
	} `json:"errors"`
}

func (c *Client) do(ctx context.Context, method, path string, body []byte) (*http.Response, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(body))
	if err != nil {
		return nil, nil, &Error{Kind: KindConfig, Detail: err.Error(), Err: err}
	}
	req.Header.Set("User-Agent", version.UserAgent())
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" && path != "/api/v1/health" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, &Error{Kind: KindTransient, Detail: scrub(err.Error(), c.token), Err: err}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))
	return resp, b, nil
}

// scrub removes the token from anything that could be logged.
func scrub(s, token string) string {
	if token != "" {
		s = strings.ReplaceAll(s, token, "***")
	}
	return s
}

func describe(body []byte) string {
	var p problem
	if json.Unmarshal(body, &p) != nil || (p.Detail == "" && p.Title == "") {
		s := strings.TrimSpace(string(body))
		if len(s) > 200 {
			s = s[:200]
		}
		return s
	}
	msg := p.Detail
	if msg == "" {
		msg = p.Title
	}
	var parts []string
	for _, e := range p.Errors {
		parts = append(parts, e.Field+": "+e.Message)
	}
	if len(parts) > 0 {
		msg += " (" + strings.Join(parts, "; ") + ")"
	}
	return msg
}

// classify turns a non-success response into an Error.
func classify(resp *http.Response, body []byte, token string) *Error {
	detail := scrub(describe(body), token)
	e := &Error{Status: resp.StatusCode, Detail: detail}
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		e.Kind = KindAuth
		e.Detail = "the API refused the token; check SPEEDTEST_API_TOKEN against API_TOKENS on the server"
	case resp.StatusCode == http.StatusTooManyRequests:
		e.Kind = KindRateLimited
		if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
			e.RetryAfter = time.Duration(secs) * time.Second
		} else {
			e.RetryAfter = time.Minute
		}
	case resp.StatusCode >= 300 && resp.StatusCode < 400:
		e.Kind = KindConfig
		e.Detail = "the address redirects to " + resp.Header.Get("Location") + "; use that address as --api-url"
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusMethodNotAllowed:
		e.Kind = KindConfig
		e.Detail = "the address does not look like a SpeedtestApp (" + detail + "); check --api-url"
	case resp.StatusCode == http.StatusRequestTimeout:
		e.Kind = KindTransient
	case resp.StatusCode >= 500:
		e.Kind = KindTransient
	default: // 400, 413, 415, 422 ...: the server read it and said no
		e.Kind = KindRejected
	}
	return e
}

// Ingest sends one result and returns the id the server gave it.
func (c *Client) Ingest(ctx context.Context, env Envelope) (string, error) {
	body, err := json.Marshal(env)
	if err != nil {
		return "", &Error{Kind: KindRejected, Detail: err.Error(), Err: err}
	}
	resp, rb, err := c.do(ctx, http.MethodPost, "/api/v1/results", body)
	if err != nil {
		return "", err
	}
	if resp.StatusCode == http.StatusCreated || resp.StatusCode == http.StatusOK {
		var out struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(rb, &out)
		return out.ID, nil
	}
	return "", classify(resp, rb, c.token)
}

// Report is the outcome of Check.
type Report struct {
	Reachable bool   // the address answers like a SpeedtestApp
	TokenOK   bool   // the token is accepted
	Problem   string // what is wrong, in plain words, when either is false
}

// Check verifies the address and the token without storing anything: a deliberately empty body is rejected as invalid (400)
// only after the token has been accepted, whereas a bad token is rejected first (401).
func (c *Client) Check(ctx context.Context) Report {
	resp, body, err := c.do(ctx, http.MethodGet, "/api/v1/health", nil)
	if err != nil {
		return Report{Problem: "cannot reach the API: " + err.(*Error).Detail}
	}
	var h struct {
		Status string `json:"status"`
	}
	if resp.StatusCode != http.StatusOK || json.Unmarshal(body, &h) != nil || h.Status != "ok" {
		var ce *Error
		if resp.StatusCode >= 300 && resp.StatusCode < 400 {
			ce = classify(resp, body, c.token)
			return Report{Problem: ce.Detail}
		}
		return Report{Problem: fmt.Sprintf("the address answered (HTTP %d) but is not a healthy SpeedtestApp; check --api-url", resp.StatusCode)}
	}
	r := Report{Reachable: true}
	resp, body, err = c.do(ctx, http.MethodPost, "/api/v1/results", []byte(`{}`))
	if err != nil {
		r.Problem = "could not check the token: " + err.(*Error).Detail
		return r
	}
	switch resp.StatusCode {
	case http.StatusBadRequest:
		r.TokenOK = true
	case http.StatusUnauthorized, http.StatusForbidden:
		r.Problem = "the API refused the token; check SPEEDTEST_API_TOKEN against API_TOKENS on the server"
	case http.StatusTooManyRequests:
		r.Problem = "the API is rate limiting this device right now; try again in a minute"
	default:
		r.Problem = fmt.Sprintf("unexpected answer while checking the token (HTTP %d): %s", resp.StatusCode, describe(body))
	}
	return r
}

// IsKind reports whether err is an *Error of the given kind.
func IsKind(err error, k Kind) bool {
	var e *Error
	return errors.As(err, &e) && e.Kind == k
}
