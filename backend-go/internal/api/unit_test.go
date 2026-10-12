package api

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/config"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/model"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/store"
	"github.com/jackc/pgx/v5/pgconn"
)

// fakeStore records inserts and returns canned errors; query methods are not used by these tests.
type fakeStore struct {
	inserts   []string
	insertErr error
	pingErr   error
}

func (f *fakeStore) Ping(context.Context) error { return f.pingErr }
func (f *fakeStore) ListResults(context.Context, store.Filter, int, *store.Cursor) ([]model.Result, *store.Cursor, error) {
	return []model.Result{}, nil, nil
}
func (f *fakeStore) GetResult(_ context.Context, id string) (model.ResultDetail, error) {
	return model.ResultDetail{Result: model.Result{ID: id, Provider: "ookla"}}, nil
}
func (f *fakeStore) Summary(context.Context, store.Filter) (model.Summary, error) {
	return model.Summary{}, nil
}
func (f *fakeStore) Timeseries(context.Context, store.Filter, string, []string, string) (model.Timeseries, error) {
	return model.Timeseries{}, nil
}
func (f *fakeStore) Devices(context.Context) ([]model.Device, error) { return nil, nil }
func (f *fakeStore) InsertOokla(_ context.Context, _ []byte, device, ct, trigger string) (string, error) {
	f.inserts = append(f.inserts, "ookla/"+device+"/"+ct+"/"+trigger)
	return "11111111-1111-1111-1111-111111111111", f.insertErr
}
func (f *fakeStore) InsertNDT7(_ context.Context, _ []byte, device, ct, trigger, _ string) (string, error) {
	f.inserts = append(f.inserts, "ndt7/"+device+"/"+ct+"/"+trigger)
	return "22222222-2222-2222-2222-222222222222", f.insertErr
}

func newTestServer(fs *fakeStore) *Server {
	auth := NewAuthenticator(config.ParseTokens("laptop=tok-laptop, phone = tok-phone ,bad,=x,y="))
	return NewServer(fs, auth, "UTC", slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func do(t *testing.T, s *Server, method, target, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

const goodOokla = `{"type":"result","timestamp":"2026-06-01T06:35:20Z","ping":{"jitter":0.5,"latency":12.4},
	"download":{"bandwidth":116027626},"upload":{"bandwidth":14126828},"packetLoss":0}`
const goodNDT7 = `{"startedAt":"2026-10-11T15:00:00Z","finishedAt":"2026-10-11T15:00:22Z","downloadMbps":87.5,
	"uploadMbps":23.25,"minRttMs":18.4,"retransmissionPct":0.31}`

func envelope(provider, ct, result string) string {
	return `{"provider":"` + provider + `","connectionType":"` + ct + `","result":` + result + `}`
}

func problem(t *testing.T, rec *httptest.ResponseRecorder) Problem {
	t.Helper()
	if ct := rec.Header().Get("Content-Type"); ct != "application/problem+json" {
		t.Fatalf("Content-Type = %q, want application/problem+json (body %s)", ct, rec.Body)
	}
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatal(err)
	}
	if p.Status != rec.Code {
		t.Fatalf("problem.status %d != HTTP %d", p.Status, rec.Code)
	}
	return p
}

func fieldsOf(p Problem) []string {
	var out []string
	for _, e := range p.Errors {
		out = append(out, e.Field)
	}
	return out
}

func TestParseTokens(t *testing.T) {
	got := config.ParseTokens("laptop=tok-laptop, phone = tok-phone ,bad,=x,y=,a=b=c")
	want := []config.TokenEntry{{Device: "laptop", Token: "tok-laptop"}, {Device: "phone", Token: "tok-phone"}, {Device: "a", Token: "b=c"}}
	if len(got) != len(want) {
		t.Fatalf("got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d: got %v want %v", i, got[i], want[i])
		}
	}
	if len(config.ParseTokens("")) != 0 {
		t.Error("empty string should yield no tokens")
	}
}

func TestIngestAuth(t *testing.T) {
	fs := &fakeStore{}
	s := newTestServer(fs)
	body := envelope("ookla", "wireless", goodOokla)

	for name, token := range map[string]string{"missing": "", "wrong": "nope", "prefix of real": "tok-lap"} {
		rec := do(t, s, "POST", "/api/v1/results", token, body)
		if rec.Code != 401 || rec.Header().Get("WWW-Authenticate") != "Bearer" {
			t.Errorf("%s: status %d, WWW-Authenticate %q", name, rec.Code, rec.Header().Get("WWW-Authenticate"))
		}
		problem(t, rec)
	}
	if len(fs.inserts) != 0 {
		t.Fatalf("unauthenticated requests reached the store: %v", fs.inserts)
	}

	rec := do(t, s, "POST", "/api/v1/results", "tok-phone", body)
	if rec.Code != 201 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if got := fs.inserts[0]; got != "ookla/phone/wireless/scheduled" {
		t.Errorf("device must come from the token, got %q", got)
	}

	// no tokens configured -> everything is rejected
	empty := NewServer(fs, NewAuthenticator(nil), "UTC", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if rec := do(t, empty, "POST", "/api/v1/results", "anything", body); rec.Code != 401 {
		t.Errorf("no tokens configured: status %d, want 401", rec.Code)
	}
}

func TestIngestValidation(t *testing.T) {
	s := newTestServer(&fakeStore{})
	s.SetIngestRate(0) // many cases from one token; limiting has its own test
	cases := []struct {
		name, body string
		fields     []string
	}{
		{"not json", `hi`, nil},
		{"bad provider", envelope("speedof", "wired", goodOokla), []string{"provider"}},
		{"bad connection type", envelope("ookla", "bluetooth", goodOokla), []string{"connectionType"}},
		{"bad trigger", `{"provider":"ookla","connectionType":"wired","trigger":"weekly","result":` + goodOokla + `}`, []string{"trigger"}},
		{"missing result", `{"provider":"ookla","connectionType":"wired"}`, []string{"result."}},
		{"ookla wrong type", envelope("ookla", "wired", `{"type":"log","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1}}`), []string{"result.type"}},
		{"ookla no bandwidth", envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z"}`), []string{"result.download.bandwidth", "result.upload.bandwidth"}},
		{"ookla negative", envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":-1},"upload":{"bandwidth":1}}`), []string{"result.download.bandwidth"}},
		{"ookla loss >100", envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1},"packetLoss":101}`), []string{"result.packetLoss"}},
		{"ookla bad time", envelope("ookla", "wired", `{"type":"result","timestamp":"yesterday","download":{"bandwidth":1},"upload":{"bandwidth":1}}`), []string{"result.timestamp"}},
		{"ookla absurd bandwidth", envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1e300},"upload":{"bandwidth":1}}`), []string{"result.download.bandwidth"}},
		{"ookla absurd latency", envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z","ping":{"latency":1e12},"download":{"bandwidth":1},"upload":{"bandwidth":1}}`), []string{"result.ping.latency"}},
		{"ookla year 0336", envelope("ookla", "wired", `{"type":"result","timestamp":"0336-08-12T20:55:46Z","download":{"bandwidth":1},"upload":{"bandwidth":1}}`), []string{"result.timestamp"}},
		{"ookla far future", envelope("ookla", "wired", `{"type":"result","timestamp":"2999-01-01T00:00:00Z","download":{"bandwidth":1},"upload":{"bandwidth":1}}`), []string{"result.timestamp"}},
		{"ndt7 absurd speed", envelope("ndt7", "wired", `{"startedAt":"2026-10-11T15:00:00Z","finishedAt":"2026-10-11T15:00:22Z","downloadMbps":1e300,"uploadMbps":1}`), []string{"result.downloadMbps"}},
		{"ndt7 server.location is an object", envelope("ndt7", "wired", `{"startedAt":"2026-10-11T15:00:00Z","finishedAt":"2026-10-11T15:00:22Z","downloadMbps":1,"uploadMbps":1,"server":{"location":{}}}`), []string{"result.server.location"}},
		{"ndt7 raw is not an object", envelope("ndt7", "wired", `{"startedAt":"2026-10-11T15:00:00Z","finishedAt":"2026-10-11T15:00:22Z","downloadMbps":1,"uploadMbps":1,"raw":[1]}`), []string{"result.raw"}},
		{"ookla result.id null", envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1},"result":{"id":null,"url":""}}`), []string{"result.result.id"}},
		{"ookla result.url number", envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1},"result":{"url":5}}`), []string{"result.result.url"}},
		{"ookla result is null", envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1},"result":null}`), []string{"result.result"}},
		{"ookla server is null", envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1},"server":null}`), []string{"result.server"}},
		{"ookla isp is a number", envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1},"isp":5}`), []string{"result.isp"}},
		{"ndt7 raw is null", envelope("ndt7", "wired", `{"startedAt":"2026-10-11T15:00:00Z","finishedAt":"2026-10-11T15:00:22Z","downloadMbps":1,"uploadMbps":1,"raw":null}`), []string{"result.raw"}},
		{"ndt7 server is null", envelope("ndt7", "wired", `{"startedAt":"2026-10-11T15:00:00Z","finishedAt":"2026-10-11T15:00:22Z","downloadMbps":1,"uploadMbps":1,"server":null}`), []string{"result.server"}},
		{"ookla ping is null", envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1},"ping":null}`), []string{"result.ping"}},
		{"trigger is null", `{"provider":"ookla","connectionType":"wired","trigger":null,"result":` + goodOokla + `}`, []string{"trigger"}},
		{"ookla packetLoss null", envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1},"packetLoss":null}`), []string{"result.packetLoss"}},
		{"ookla ping.latency null", envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1},"ping":{"latency":null}}`), []string{"result.ping.latency"}},
		{"ookla url is junk", envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1},"result":{"url":"not a url"}}`), []string{"result.result.url"}},
		{"ookla url javascript scheme", envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1},"result":{"url":"javascript:alert(1)"}}`), []string{"result.result.url"}},
		{"ndt7 server.country null", envelope("ndt7", "wired", `{"startedAt":"2026-10-11T15:00:00Z","finishedAt":"2026-10-11T15:00:22Z","downloadMbps":1,"uploadMbps":1,"server":{"country":null}}`), []string{"result.server.country"}},
		{"ndt7 missing speeds", envelope("ndt7", "wired", `{"startedAt":"2026-10-11T15:00:00Z","finishedAt":"2026-10-11T15:00:22Z"}`), []string{"result.downloadMbps", "result.uploadMbps"}},
		{"ndt7 finished before started", envelope("ndt7", "wired", `{"startedAt":"2026-10-11T15:00:22Z","finishedAt":"2026-10-11T15:00:00Z","downloadMbps":1,"uploadMbps":1}`), []string{"result.finishedAt"}},
		{"ndt7 retrans >100", envelope("ndt7", "wired", `{"startedAt":"2026-10-11T15:00:00Z","finishedAt":"2026-10-11T15:00:22Z","downloadMbps":1,"uploadMbps":1,"retransmissionPct":150}`), []string{"result.retransmissionPct"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(t, s, "POST", "/api/v1/results", "tok-laptop", c.body)
			if rec.Code != 400 {
				t.Fatalf("status %d, want 400: %s", rec.Code, rec.Body)
			}
			p := problem(t, rec)
			if c.fields != nil {
				got := map[string]bool{}
				for _, f := range fieldsOf(p) {
					got[f] = true
				}
				for _, f := range c.fields {
					if !got[f] {
						t.Errorf("missing error for field %q; got %v", f, fieldsOf(p))
					}
				}
			}
		})
	}
}

func TestIngestAccepted(t *testing.T) {
	fs := &fakeStore{}
	s := newTestServer(fs)
	for _, b := range []string{envelope("ookla", "wired", goodOokla), envelope("ndt7", "wireless", goodNDT7),
		`{"provider":"ndt7","connectionType":"wireless","trigger":"on_demand","result":` + goodNDT7 + `}`} {
		if rec := do(t, s, "POST", "/api/v1/results", "tok-laptop", b); rec.Code != 201 {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
	}
	want := []string{"ookla/laptop/wired/scheduled", "ndt7/laptop/wireless/scheduled", "ndt7/laptop/wireless/on_demand"}
	for i := range want {
		if fs.inserts[i] != want[i] {
			t.Errorf("insert %d = %q, want %q", i, fs.inserts[i], want[i])
		}
	}
}

func TestIngestAcceptsDocumentedOptionals(t *testing.T) {
	fs := &fakeStore{}
	s := newTestServer(fs)
	s.SetIngestRate(0)
	for name, b := range map[string]string{
		"ookla https url":  envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1},"result":{"id":"x","url":"https://www.speedtest.net/result/c/1"}}`),
		"ookla empty url":  envelope("ookla", "wired", `{"type":"result","timestamp":"2026-06-01T06:35:20Z","download":{"bandwidth":1},"upload":{"bandwidth":1},"result":{"url":""}}`),
		"ndt7 null floats": envelope("ndt7", "wired", `{"startedAt":"2026-10-11T15:00:00Z","finishedAt":"2026-10-11T15:00:22Z","downloadMbps":1,"uploadMbps":1,"minRttMs":null,"retransmissionPct":null,"server":{"machine":"m","location":"l","country":"US"}}`),
	} {
		if rec := do(t, s, "POST", "/api/v1/results", "tok-laptop", b); rec.Code != 201 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestIngestLimitsAndErrors(t *testing.T) {
	s := newTestServer(&fakeStore{})
	if rec := do(t, s, "POST", "/api/v1/results", "tok-laptop", strings.Repeat("x", maxBodyBytes+1)); rec.Code != 413 {
		t.Errorf("oversized body: status %d, want 413", rec.Code)
	} else {
		problem(t, rec)
	}

	// the database rejecting input (check constraint, bad cast) is the caller's fault: 400, not 500
	for _, code := range []string{"23514", "22P02"} {
		fs := &fakeStore{insertErr: &pgconn.PgError{Code: code, Message: "nope"}}
		rec := do(t, newTestServer(fs), "POST", "/api/v1/results", "tok-laptop", envelope("ookla", "wired", goodOokla))
		if rec.Code != 400 {
			t.Errorf("pg %s -> %d, want 400", code, rec.Code)
		}
	}
	fs := &fakeStore{insertErr: &pgconn.PgError{Code: "53300", Message: "too many connections"}}
	rec := do(t, newTestServer(fs), "POST", "/api/v1/results", "tok-laptop", envelope("ookla", "wired", goodOokla))
	if rec.Code != 500 || strings.Contains(rec.Body.String(), "too many connections") {
		t.Errorf("internal errors must be 500 without leaking details, got %d %s", rec.Code, rec.Body)
	}
}

func TestRateLimit(t *testing.T) {
	s := newTestServer(&fakeStore{})
	now := time.Unix(0, 0)
	s.limiter.now = func() time.Time { return now }
	body := envelope("ookla", "wired", goodOokla)
	for i := 0; i < 20; i++ {
		if rec := do(t, s, "POST", "/api/v1/results", "tok-laptop", body); rec.Code != 201 {
			t.Fatalf("request %d: status %d", i, rec.Code)
		}
	}
	rec := do(t, s, "POST", "/api/v1/results", "tok-laptop", body)
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("21st request: status %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	problem(t, rec)
	if rec := do(t, s, "POST", "/api/v1/results", "tok-phone", body); rec.Code != 201 {
		t.Errorf("another device must not be limited, got %d", rec.Code)
	}
	now = now.Add(3 * time.Second) // 30/min => one token per 2s
	if rec := do(t, s, "POST", "/api/v1/results", "tok-laptop", body); rec.Code != 201 {
		t.Errorf("after refill: status %d", rec.Code)
	}
}

func TestIngestRateSetting(t *testing.T) {
	s := newTestServer(&fakeStore{})
	s.SetIngestRate(0) // unlimited
	body := envelope("ookla", "wired", goodOokla)
	for i := 0; i < 100; i++ {
		if rec := do(t, s, "POST", "/api/v1/results", "tok-laptop", body); rec.Code != 201 {
			t.Fatalf("request %d with limiting off: %d", i, rec.Code)
		}
	}
}

func TestQueryParamValidation(t *testing.T) {
	s := newTestServer(&fakeStore{})
	bad := map[string]string{
		"/api/v1/results?provider=speedof":                                  "provider",
		"/api/v1/results?connectionType=lte":                                "connectionType",
		"/api/v1/results?trigger=weekly":                                    "trigger",
		"/api/v1/results?from=2026-06-01":                                   "from",
		"/api/v1/results?from=2026-07-01T00:00:00Z&to=2026-06-01T00:00:00Z": "from",
		"/api/v1/results?limit=":                                            "limit",
		"/api/v1/results?limit=0":                                           "limit",
		"/api/v1/results?limit=501":                                         "limit",
		"/api/v1/results?limit=abc":                                         "limit",
		"/api/v1/results?cursor=garbage":                                    "cursor",
		"/api/v1/stats/summary?provider=x":                                  "provider",
		"/api/v1/stats/timeseries?interval=minute":                          "interval",
		"/api/v1/stats/timeseries?groupBy=isp":                              "groupBy",
		"/api/v1/stats/timeseries?timezone=Mars/Base":                       "timezone",
		"/api/v1/stats/timeseries?timezone=Local":                           "timezone",
	}
	for url, field := range bad {
		rec := do(t, s, "GET", url, "", "")
		if rec.Code != 400 {
			t.Errorf("%s: status %d, want 400", url, rec.Code)
			continue
		}
		if got := fieldsOf(problem(t, rec)); len(got) == 0 || got[0] != field {
			t.Errorf("%s: error fields %v, want %q first", url, got, field)
		}
	}
	for _, url := range []string{"/api/v1/results?provider=ookla,ndt7&device=a,b&connectionType=wired",
		"/api/v1/results?provider=ookla&provider=ndt7", "/api/v1/stats/timeseries?interval=raw&groupBy=device&timezone=America/Phoenix",
		"/api/v1/results?from=2026-06-01T00:00:00-07:00&limit=500"} {
		if rec := do(t, s, "GET", url, "", ""); rec.Code != 200 {
			t.Errorf("%s: status %d: %s", url, rec.Code, rec.Body)
		}
	}
}

func TestCursorRoundTrip(t *testing.T) {
	c := &store.Cursor{T: time.Date(2026, 6, 1, 6, 35, 20, 123456000, time.UTC), ID: "11111111-1111-1111-1111-111111111111"}
	enc := encodeCursor(c)
	got, ok := decodeCursor(*enc)
	if !ok || !got.T.Equal(c.T) || got.ID != c.ID {
		t.Fatalf("round trip failed: %v %v", got, ok)
	}
	if encodeCursor(nil) != nil {
		t.Error("nil cursor must encode to nil (JSON null)")
	}
	for _, bad := range []string{"", "!!!", "e30", "bnVsbA"} {
		if _, ok := decodeCursor(bad); ok {
			t.Errorf("decodeCursor(%q) accepted", bad)
		}
	}
}

func TestRoutingErrorsAreProblems(t *testing.T) {
	s := newTestServer(&fakeStore{})
	rec := do(t, s, "GET", "/api/v1/nope", "", "")
	if rec.Code != 404 {
		t.Errorf("unknown path: %d", rec.Code)
	}
	problem(t, rec)
	rec = do(t, s, "DELETE", "/api/v1/results", "", "")
	if rec.Code != 405 || !strings.Contains(rec.Header().Get("Allow"), "GET") {
		t.Errorf("wrong method: %d Allow=%q", rec.Code, rec.Header().Get("Allow"))
	}
	problem(t, rec)
	if rec := do(t, s, "GET", "/api/v1/results/not-a-uuid", "", ""); rec.Code != 200 {
		// fake store returns a value for any id; real not-found handling is covered by the integration test
		t.Logf("note: fake store answered %d", rec.Code)
	}
}

func TestHealth(t *testing.T) {
	rec := do(t, newTestServer(&fakeStore{}), "GET", "/api/v1/health", "", "")
	if rec.Code != 200 || !bytes.Contains(rec.Body.Bytes(), []byte(`"ok"`)) {
		t.Errorf("healthy: %d %s", rec.Code, rec.Body)
	}
	rec = do(t, newTestServer(&fakeStore{pingErr: context.DeadlineExceeded}), "GET", "/api/v1/health", "", "")
	if rec.Code != 503 {
		t.Errorf("db down: %d", rec.Code)
	}
	problem(t, rec)
	if strings.Contains(rec.Body.String(), "deadline") {
		t.Error("health problem must not leak the underlying error")
	}
}

func TestPanicRecovery(t *testing.T) {
	s := newTestServer(&fakeStore{})
	s.mux.HandleFunc("GET /boom", func(http.ResponseWriter, *http.Request) { panic("x") })
	rec := do(t, s, "GET", "/boom", "", "")
	if rec.Code != 500 {
		t.Errorf("panic -> %d", rec.Code)
	}
	problem(t, rec)
}
