package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestConfigDisabledWithoutTopic(t *testing.T) {
	c, err := FromEnv(env(map[string]string{"NTFY_SLOW_RATIO": "not even checked"}))
	if err != nil || c.Enabled {
		t.Fatalf("no topic must mean off, quietly: %+v %v", c, err)
	}
}

func TestConfigDefaultsAndOverrides(t *testing.T) {
	c, err := FromEnv(env(map[string]string{"NTFY_TOPIC": "home-net_1"}))
	if err != nil || !c.Enabled {
		t.Fatalf("%+v %v", c, err)
	}
	d := Defaults()
	if c.ServerURL != "https://ntfy.sh" || c.SlowRatio != d.SlowRatio || c.SlowConsecutive != 1 || c.LossConsecutive != 1 || c.SilenceMin != 3*time.Hour || c.RemindEvery != 12*time.Hour {
		t.Errorf("defaults not applied: %+v", c)
	}
	c, err = FromEnv(env(map[string]string{
		"NTFY_TOPIC": "t", "NTFY_URL": "https://ntfy.example.org/", "NTFY_TOKEN": "tk_abc", "APP_URL": "https://speed.example.org",
		"NTFY_SLOW_RATIO": "0.5", "NTFY_RECOVER_RATIO": "0.8", "NTFY_SLOW_CONSECUTIVE": "3", "NTFY_LOSS_PCT": "2.5",
		"NTFY_SILENCE_MIN_HOURS": "1.5", "NTFY_REMIND_HOURS": "0", "NTFY_CHECK_SECONDS": "30",
	}))
	if err != nil {
		t.Fatal(err)
	}
	if c.ServerURL != "https://ntfy.example.org" || c.Host() != "ntfy.example.org" || c.SlowRatio != 0.5 || c.SlowConsecutive != 3 ||
		c.LossPct != 2.5 || c.SilenceMin != 90*time.Minute || c.RemindEvery != 0 || c.CheckEvery != 30*time.Second || c.AppURL == "" {
		t.Errorf("overrides not applied: %+v", c)
	}
}

func TestConfigOutliersSetsBothStreaksUnlessOverridden(t *testing.T) {
	c, err := FromEnv(env(map[string]string{"NTFY_TOPIC": "t", "NTFY_OUTLIERS": "3"}))
	if err != nil || c.SlowConsecutive != 3 || c.LossConsecutive != 3 {
		t.Errorf("NTFY_OUTLIERS applies to slow and loss: %+v %v", c, err)
	}
	c, err = FromEnv(env(map[string]string{"NTFY_TOPIC": "t", "NTFY_OUTLIERS": "3", "NTFY_LOSS_CONSECUTIVE": "2"}))
	if err != nil || c.SlowConsecutive != 3 || c.LossConsecutive != 2 {
		t.Errorf("a per-kind setting wins over NTFY_OUTLIERS: %+v %v", c, err)
	}
}

func TestConfigRejectsBadValuesWithAllProblemsListed(t *testing.T) {
	cases := map[string]map[string]string{
		"NTFY_TOPIC":            {"NTFY_TOPIC": "has space"},
		"NTFY_URL":              {"NTFY_URL": "ntfy.sh"},
		"NTFY_SLOW_RATIO":       {"NTFY_SLOW_RATIO": "1"},
		"NTFY_RECOVER_RATIO":    {"NTFY_RECOVER_RATIO": "0.6"}, // not above the slow ratio
		"NTFY_OUTLIERS":         {"NTFY_OUTLIERS": "0"},
		"NTFY_SLOW_CONSECUTIVE": {"NTFY_SLOW_CONSECUTIVE": "0"},
		"NTFY_LOSS_PCT":         {"NTFY_LOSS_PCT": "150"},
		"NTFY_MIN_SAMPLES":      {"NTFY_MIN_SAMPLES": "1"},
		"NTFY_CHECK_SECONDS":    {"NTFY_CHECK_SECONDS": "2"},
		"NTFY_REMIND_HOURS":     {"NTFY_REMIND_HOURS": "-1"},
		"NTFY_TOKEN":            {"NTFY_TOKEN": "tk", "NTFY_USER": "me", "NTFY_PASSWORD": "pw"},
		"NTFY_USER":             {"NTFY_USER": "me"},
		"APP_URL":               {"APP_URL": "javascript:alert(1)"},
	}
	for name, vars := range cases {
		vars["NTFY_TOPIC"] = firstNonEmpty(vars["NTFY_TOPIC"], "ok")
		if _, err := FromEnv(env(vars)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: want an error naming it, got %v", name, err)
		}
	}
	_, err := FromEnv(env(map[string]string{"NTFY_TOPIC": "x y", "NTFY_SLOW_RATIO": "9", "NTFY_URL": "ftp://z"}))
	for _, want := range []string{"NTFY_TOPIC", "NTFY_SLOW_RATIO", "NTFY_URL"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("all problems should be reported at once; missing %s in %v", want, err)
		}
	}
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func newTestNtfy(t *testing.T, handler http.HandlerFunc, mutate func(*Config)) (*Ntfy, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	cfg := Defaults()
	cfg.Enabled, cfg.ServerURL, cfg.Topic = true, srv.URL, "secret-topic"
	if mutate != nil {
		mutate(&cfg)
	}
	n := NewNtfy(cfg)
	n.sleep = func(context.Context, time.Duration) {}
	return n, srv
}

func TestNtfyPublishesJSONWithBearerToken(t *testing.T) {
	var got publishBody
	var auth, ctype, method string
	n, _ := newTestNtfy(t, func(w http.ResponseWriter, r *http.Request) {
		auth, ctype, method = r.Header.Get("Authorization"), r.Header.Get("Content-Type"), r.Method
		b, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(b, &got)
		w.WriteHeader(200)
	}, func(c *Config) { c.Token, c.AppURL = "tk_123", "https://speed.example.org" })

	err := n.Send(context.Background(), Message{Title: "Slow download: server (wired)", Body: "Zażółć gęślą jaźń — 390 Mbps", Priority: 4, Tags: []string{"warning", "slow"}})
	if err != nil {
		t.Fatal(err)
	}
	if method != "POST" || ctype != "application/json" || auth != "Bearer tk_123" {
		t.Errorf("method=%s content-type=%s auth=%s", method, ctype, auth)
	}
	want := publishBody{Topic: "secret-topic", Title: "Slow download: server (wired)", Message: "Zażółć gęślą jaźń — 390 Mbps", Priority: 4, Tags: []string{"warning", "slow"}, Click: "https://speed.example.org"}
	if got.Topic != want.Topic || got.Title != want.Title || got.Message != want.Message || got.Priority != 4 || got.Click != want.Click || len(got.Tags) != 2 {
		t.Errorf("payload = %+v", got)
	}
}

func TestNtfyBasicAuthAndNoAuth(t *testing.T) {
	var user, pass string
	var ok bool
	var hasAuth bool
	n, _ := newTestNtfy(t, func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok = r.BasicAuth()
		hasAuth = r.Header.Get("Authorization") != ""
	}, func(c *Config) { c.User, c.Password = "me", "pw" })
	if err := n.Send(context.Background(), Message{Body: "x"}); err != nil || !ok || user != "me" || pass != "pw" {
		t.Errorf("basic auth: %v %v %s %s", err, ok, user, pass)
	}
	n, _ = newTestNtfy(t, func(w http.ResponseWriter, r *http.Request) { hasAuth = r.Header.Get("Authorization") != "" }, nil)
	if err := n.Send(context.Background(), Message{Body: "x"}); err != nil || hasAuth {
		t.Errorf("public topic must send no credentials: %v %v", err, hasAuth)
	}
}

func TestNtfyRetriesServerErrorsButNotClientErrors(t *testing.T) {
	var calls atomic.Int32
	n, _ := newTestNtfy(t, func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(200)
	}, nil)
	if err := n.Send(context.Background(), Message{Body: "x"}); err != nil || calls.Load() != 3 {
		t.Fatalf("transient failures should be retried: err=%v calls=%d", err, calls.Load())
	}

	calls.Store(0)
	n, _ = newTestNtfy(t, func(w http.ResponseWriter, r *http.Request) { calls.Add(1); w.WriteHeader(503) }, nil)
	if err := n.Send(context.Background(), Message{Body: "x"}); err == nil || calls.Load() != 3 {
		t.Errorf("gives up after three attempts: err=%v calls=%d", err, calls.Load())
	}

	calls.Store(0)
	n, _ = newTestNtfy(t, func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(403)
		_, _ = w.Write([]byte(`{"error":"forbidden"}`))
	}, nil)
	err := n.Send(context.Background(), Message{Body: "x"})
	if err == nil || calls.Load() != 1 || !strings.Contains(err.Error(), "403") {
		t.Errorf("a 4xx will not get better by retrying: err=%v calls=%d", err, calls.Load())
	}
}

func TestNtfyErrorsNeverContainTheTopicOrCredentials(t *testing.T) {
	n, _ := newTestNtfy(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		_, _ = w.Write([]byte(`bad request for topic secret-topic using tk_supersecret and pw0rd`))
	}, func(c *Config) { c.Token = "tk_supersecret"; c.Password = "pw0rd" })
	err := n.Send(context.Background(), Message{Body: "x"})
	if err == nil {
		t.Fatal("expected an error")
	}
	for _, secret := range []string{"secret-topic", "tk_supersecret", "pw0rd"} {
		if strings.Contains(err.Error(), secret) {
			t.Errorf("error leaks %q: %v", secret, err)
		}
	}
}

func TestNtfyUnreachableServerAndCancellation(t *testing.T) {
	cfg := Defaults()
	cfg.Enabled, cfg.ServerURL, cfg.Topic = true, "http://127.0.0.1:1", "t" // nothing listens on port 1
	n := NewNtfy(cfg)
	n.sleep = func(context.Context, time.Duration) {}
	if err := n.Send(context.Background(), Message{Body: "x"}); err == nil {
		t.Error("an unreachable server must be an error")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := n.Send(ctx, Message{Body: "x"}); err == nil {
		t.Error("a cancelled context must stop the send")
	}
}

func TestMedianAndSilenceThreshold(t *testing.T) {
	if Median(nil) != 0 || Median([]float64{5}) != 5 || Median([]float64{3, 1, 2}) != 2 || Median([]float64{1, 2, 3, 10}) != 2.5 {
		t.Error("median")
	}
	in := []float64{9, 1, 5}
	Median(in)
	if in[0] != 9 {
		t.Error("Median must not reorder its input")
	}
	h := time.Hour
	if got := SilenceThreshold([]time.Duration{2 * h, 2 * h, 2 * h, 2 * h}, 3*h); got != 6*h {
		t.Errorf("2-hourly device: threshold %v, want 6h", got)
	}
	if got := SilenceThreshold([]time.Duration{8 * h, 8 * h, 9 * h}, 3*h); got != 24*h {
		t.Errorf("8-hourly device: %v, want 24h", got)
	}
	if got := SilenceThreshold([]time.Duration{10 * time.Minute, 10 * time.Minute}, 3*h); got != 3*h {
		t.Errorf("a busy device still gets the floor: %v", got)
	}
	if got := SilenceThreshold([]time.Duration{2 * h, 2 * h, 2 * h, 40 * h}, 3*h); got != 6*h {
		t.Errorf("one long outage must not stretch the threshold (median, not mean): %v", got)
	}
	if SilenceThreshold(nil, 3*h) != 3*h {
		t.Error("no history -> the floor")
	}
}

func TestMessageTexts(t *testing.T) {
	e := Entity{Device: "server", ConnectionType: "wired", Provider: "ookla"}
	if e.Label() != "server (wired)" || (Entity{Device: "phone", ConnectionType: "wireless", Provider: "ndt7"}).Label() != "phone (wireless, NDT7)" {
		t.Error("labels")
	}
	slow := SlowText(e, 390.4, 936.6, 2)
	if slow.Title != "Slow download: server (wired)" || slow.Detail != "390 Mbps, 58% below your typical 937 Mbps (2 tests in a row)." || slow.Severity != "critical" {
		t.Errorf("slow: %+v", slow)
	}
	if SlowText(e, 700, 936.6, 2).Severity != "warning" {
		t.Error("25% below typical is a warning, not critical")
	}
	if SlowText(e, 100, 900, 2).Severity != "critical" || SlowText(e, 449, 900, 2).Severity != "critical" || SlowText(e, 451, 900, 2).Severity != "warning" {
		t.Error("critical below half of typical")
	}
	if got := SlowText(e, 9.5, 20.4, 1).Detail; !strings.Contains(got, "9.50 Mbps") || !strings.Contains(got, "1 test in a row") {
		t.Errorf("small speeds and singular: %s", got)
	}
	loss := LossText(e, 7.25, 3)
	if loss.Title != "Packet loss: server (wired)" || !strings.Contains(loss.Detail, "7.2%") && !strings.Contains(loss.Detail, "7.3%") {
		t.Errorf("loss: %+v", loss)
	}
	now := time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	sil := SilenceText("server", now.Add(-7*time.Hour), now, 2*time.Hour)
	if sil.Title != "No tests from server" || sil.Detail != "The last test was 7 hours ago; one is usually due every 2 hours." {
		t.Errorf("silence: %+v", sil)
	}
	for d, want := range map[time.Duration]string{20 * time.Second: "under a minute", 25 * time.Minute: "25 minutes", time.Minute: "1 minute",
		3 * time.Hour: "3 hours", 3*time.Hour + 59*time.Second: "3 hours", 3*time.Hour + 24*time.Minute: "3.4 hours", 30 * time.Hour: "30 hours", 5 * 24 * time.Hour: "5 days"} {
		if got := Humanize(d); got != want {
			t.Errorf("Humanize(%v) = %q, want %q", d, got, want)
		}
	}
	if Priority("critical") != 4 || Priority("warning") != 3 || PriorityRecovery >= 3 {
		t.Error("priorities: critical high, warning default, recovery quiet")
	}
	if tags := Tags("slow", "critical"); tags[0] != "rotating_light" || tags[1] != "slow" {
		t.Errorf("tags %v", tags)
	}
}

func TestDisabledStatusHasEmptyListsNotNulls(t *testing.T) {
	b, _ := json.Marshal(DisabledStatus())
	if !strings.Contains(string(b), `"active":[]`) || !strings.Contains(string(b), `"recent":[]`) || !strings.Contains(string(b), `"enabled":false`) ||
		!strings.Contains(string(b), `"server":null`) || !strings.Contains(string(b), `"rules":null`) {
		t.Errorf("%s", b)
	}
}
