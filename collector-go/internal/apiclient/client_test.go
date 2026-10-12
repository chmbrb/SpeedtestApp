package apiclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

var env = Envelope{Provider: "ookla", ConnectionType: "wired", Trigger: "scheduled", Result: json.RawMessage(`{"type":"result"}`)}

func serve(t *testing.T, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return New(srv.URL, "secret-token")
}

func TestIngestSendsTheEnvelopeWithTheToken(t *testing.T) {
	var got struct {
		Method, Path, Auth, CType, UA string
		Body                          map[string]any
	}
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		got.Method, got.Path, got.Auth, got.CType, got.UA = r.Method, r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.Header.Get("User-Agent")
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got.Body)
		w.WriteHeader(201)
		_, _ = w.Write([]byte(`{"id":"abc-123","provider":"ookla"}`))
	})
	id, err := c.Ingest(context.Background(), env)
	if err != nil || id != "abc-123" {
		t.Fatalf("%q %v", id, err)
	}
	if got.Method != "POST" || got.Path != "/api/v1/results" || got.Auth != "Bearer secret-token" || got.CType != "application/json" || !strings.HasPrefix(got.UA, "speedtest-collector/") {
		t.Errorf("%+v", got)
	}
	if got.Body["provider"] != "ookla" || got.Body["connectionType"] != "wired" || got.Body["trigger"] != "scheduled" || got.Body["result"].(map[string]any)["type"] != "result" {
		t.Errorf("body: %v", got.Body)
	}
}

func TestEveryFailureIsClassifiedByWhatItMeansForTheResult(t *testing.T) {
	problem := func(status int, detail string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"title":"x","status":` + itoa(status) + `,"detail":"` + detail + `","errors":[{"field":"result.download.bandwidth","message":"is required"}]}`))
		}
	}
	cases := []struct {
		name   string
		h      http.HandlerFunc
		kind   Kind
		keep   bool
		detail string
	}{
		{"invalid result", problem(400, "request validation failed"), KindRejected, false, "result.download.bandwidth: is required"},
		{"too large", problem(413, "body is larger than 64 KB"), KindRejected, false, ""},
		{"unprocessable", problem(422, "nope"), KindRejected, false, ""},
		{"bad token", problem(401, "missing or invalid bearer token"), KindAuth, true, "refused the token"},
		{"forbidden", problem(403, "no"), KindAuth, true, ""},
		{"wrong address", problem(404, "no such endpoint"), KindConfig, true, "does not look like a SpeedtestApp"},
		{"method not allowed", problem(405, "x"), KindConfig, true, ""},
		{"server error", problem(500, "internal error"), KindTransient, true, "internal error"},
		{"bad gateway", problem(502, "x"), KindTransient, true, ""},
		{"unavailable", problem(503, "database is unreachable"), KindTransient, true, ""},
		{"request timeout", problem(408, "x"), KindTransient, true, ""},
		{"rate limited", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "42")
			problem(429, "slow down")(w, r)
		}, KindRateLimited, true, ""},
		{"not json at all", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(400)
			_, _ = w.Write([]byte("<html>bad</html>"))
		}, KindRejected, false, "<html>"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := serve(t, c.h).Ingest(context.Background(), env)
			e, ok := err.(*Error)
			if !ok || e.Kind != c.kind || e.Keep() != c.keep || !strings.Contains(e.Detail, c.detail) {
				t.Fatalf("%#v", err)
			}
			if c.kind == KindRateLimited && e.RetryAfter != 42*time.Second {
				t.Errorf("Retry-After: %v", e.RetryAfter)
			}
		})
	}
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

func TestRateLimitWithoutRetryAfterDefaultsToAMinute(t *testing.T) {
	_, err := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(429) }).Ingest(context.Background(), env)
	if e := err.(*Error); e.Kind != KindRateLimited || e.RetryAfter != time.Minute {
		t.Errorf("%#v", e)
	}
}

func TestARedirectIsReportedNotFollowed(t *testing.T) {
	var hits int
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Location", "https://speed.example.org/api/v1/results")
		w.WriteHeader(301)
	})
	_, err := c.Ingest(context.Background(), env)
	e := err.(*Error)
	if e.Kind != KindConfig || !e.Keep() || !strings.Contains(e.Detail, "https://speed.example.org") || hits != 1 {
		t.Errorf("a POST must not be re-sent as a GET after a redirect: %#v hits=%d", e, hits)
	}
}

func TestUnreachableAndSlowServersAreTransient(t *testing.T) {
	c := New("http://127.0.0.1:1", "t") // nothing listens on port 1
	_, err := c.Ingest(context.Background(), env)
	if e, ok := err.(*Error); !ok || e.Kind != KindTransient || e.Status != 0 || !e.Keep() {
		t.Errorf("%#v", err)
	}
	slow := serve(t, func(w http.ResponseWriter, r *http.Request) { time.Sleep(2 * time.Second) })
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	if _, err := slow.Ingest(ctx, env); !IsKind(err, KindTransient) {
		t.Errorf("a timeout is transient: %v", err)
	}
}

func TestTheTokenNeverAppearsInAnErrorOrALog(t *testing.T) {
	c := serve(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"detail":"upstream said: Bearer secret-token was bad for secret-token"}`))
	})
	_, err := c.Ingest(context.Background(), env)
	if strings.Contains(err.Error(), "secret-token") {
		t.Errorf("token leaked: %v", err)
	}
	down := New("http://127.0.0.1:1/secret-token/path", "secret-token")
	_, err = down.Ingest(context.Background(), env)
	if strings.Contains(err.Error(), "secret-token") {
		t.Errorf("token leaked through a network error: %v", err)
	}
}

func TestCheck(t *testing.T) {
	healthy := func(token string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/api/v1/health":
				if r.Header.Get("Authorization") != "" {
					t.Error("the health check must not be sent the token")
				}
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			case "/api/v1/results":
				b, _ := io.ReadAll(r.Body)
				if string(b) != "{}" {
					t.Errorf("the token check must store nothing: %s", b)
				}
				if r.Header.Get("Authorization") != "Bearer "+token {
					w.WriteHeader(401)
					return
				}
				w.WriteHeader(400) // accepted the token, then refused the empty body
			}
		}
	}
	if r := serve(t, healthy("secret-token")).Check(context.Background()); !r.Reachable || !r.TokenOK || r.Problem != "" {
		t.Errorf("%+v", r)
	}
	if r := serve(t, healthy("other")).Check(context.Background()); !r.Reachable || r.TokenOK || !strings.Contains(r.Problem, "refused the token") {
		t.Errorf("bad token: %+v", r)
	}
	if r := New("http://127.0.0.1:1", "t").Check(context.Background()); r.Reachable || !strings.Contains(r.Problem, "cannot reach") {
		t.Errorf("down: %+v", r)
	}
	notOurs := serve(t, func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("<html>router login</html>")) })
	if r := notOurs.Check(context.Background()); r.Reachable || !strings.Contains(r.Problem, "not a healthy SpeedtestApp") {
		t.Errorf("another web server: %+v", r)
	}
	notFound := serve(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(404) })
	if r := notFound.Check(context.Background()); r.Reachable {
		t.Errorf("404: %+v", r)
	}
	limited := serve(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v1/health" {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.WriteHeader(429)
	})
	if r := limited.Check(context.Background()); !r.Reachable || r.TokenOK || !strings.Contains(r.Problem, "rate limiting") {
		t.Errorf("429: %+v", r)
	}
}
