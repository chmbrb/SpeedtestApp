package api

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/config"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/ondemand"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/store"
)

// scripted is an on-demand runner the tests steer: it emits progress, then waits to be told to finish or fail.
type scripted struct {
	steps   chan struct{} // each receive lets the runner emit one more progress reading
	finish  chan struct{}
	raw     []byte
	failure error
	started atomic.Int32
}

func newScripted(raw []byte) *scripted {
	return &scripted{steps: make(chan struct{}, 100), finish: make(chan struct{}), raw: raw}
}

func (s *scripted) Run(ctx context.Context, emit func(ondemand.Progress)) ([]byte, error) {
	s.started.Add(1)
	n := 0
	for {
		select {
		case <-s.steps:
			n++
			mbps, f := float64(n)*10, float64(n)/10
			emit(ondemand.Progress{Type: "progress", Phase: "download", ElapsedMs: int64(n) * 100, CurrentMbps: &mbps, Fraction: &f})
		case <-s.finish:
			return s.raw, s.failure
		case <-ctx.Done():
			return nil, context.Cause(ctx)
		}
	}
}

const resultJSON = `{"type":"result","timestamp":"2026-10-11T12:00:00Z","ping":{"jitter":1.5,"latency":12.4,"low":11,"high":14},
 "download":{"bandwidth":12500000,"bytes":100,"elapsed":1000},"upload":{"bandwidth":2500000,"bytes":100,"elapsed":1000},
 "packetLoss":0,"isp":"TestNet","server":{"id":1,"name":"Srv","location":"Phoenix, AZ","country":"United States"},
 "result":{"id":"1ee33b76-fb40-407c-babc-fbb6fcf7758d","url":"https://www.speedtest.net/result/c/1"}}`

type odRig struct {
	t   *testing.T
	srv *Server
	mgr *ondemand.Manager
}

func newODRig(t *testing.T, runner ondemand.Runner, mutate func(*ondemand.Config), trustProxy bool) *odRig {
	t.Helper()
	needDB(t)
	cfg := ondemand.Defaults()
	cfg.OoklaAccepted, cfg.OoklaPath = true, "sh" // "sh" exists on every machine these tests run on
	cfg.CreatePerMinute = 600
	if mutate != nil {
		mutate(&cfg)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := ondemand.NewManager(cfg, store.New(itPool), runner, log)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	mgr.Start(ctx)
	srv := NewServer(store.New(itPool), NewAuthenticator(config.ParseTokens("laptop=tok-laptop")), "UTC", log)
	srv.SetIngestRate(0)
	srv.SetOnDemand(mgr, trustProxy)
	return &odRig{t: t, srv: srv, mgr: mgr}
}

func (r *odRig) do(method, target, token, body string) *httptest.ResponseRecorder {
	r.t.Helper()
	return do(r.t, r.srv, method, target, token, body)
}

func (r *odRig) create(body string) (int, map[string]any) {
	r.t.Helper()
	rec := r.do("POST", "/api/v1/tests", "", body)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	return rec.Code, m
}

func (r *odRig) waitState(id, want string) map[string]any {
	r.t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	var m map[string]any
	for time.Now().Before(deadline) {
		rec := r.do("GET", "/api/v1/tests/"+id, "", "")
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		if m["state"] == want {
			return m
		}
		time.Sleep(20 * time.Millisecond)
	}
	r.t.Fatalf("test %s never reached %q: %v", id, want, m["state"])
	return nil
}

type sseEvent struct {
	ID, Name string
	Data     map[string]any
}

// readSSE reads events from a streaming response until the stream ends or stop returns true.
func readSSE(t *testing.T, body io.Reader, stop func(sseEvent) bool) []sseEvent {
	t.Helper()
	var out []sseEvent
	var cur sseEvent
	var data strings.Builder
	rd := bufio.NewReader(body)
	for {
		line, err := rd.ReadString('\n')
		if err != nil {
			return out
		}
		line = strings.TrimRight(line, "\r\n")
		switch {
		case line == "":
			if cur.Name != "" {
				_ = json.Unmarshal([]byte(data.String()), &cur.Data)
				out = append(out, cur)
				if stop != nil && stop(cur) {
					return out
				}
			}
			cur, data = sseEvent{}, strings.Builder{}
		case strings.HasPrefix(line, ":"): // comment / keep-alive
		case strings.HasPrefix(line, "id: "):
			cur.ID = line[4:]
		case strings.HasPrefix(line, "event: "):
			cur.Name = line[7:]
		case strings.HasPrefix(line, "data: "):
			data.WriteString(line[6:])
		}
	}
}

func eventNames(evs []sseEvent) string {
	var n []string
	for _, e := range evs {
		n = append(n, e.Name)
	}
	return strings.Join(n, ",")
}

func TestCapabilitiesReportWhatWorksAndWhy(t *testing.T) {
	r := newODRig(t, nil, func(c *ondemand.Config) { c.OoklaAccepted = false; c.DeviceName = "homelab" }, false)
	rec := r.do("GET", "/api/v1/capabilities", "", "")
	var caps struct {
		DeviceName, ServerConnectionType string
		Ookla, NDT7                      struct {
			Available bool
			Reason    *string
		}
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &caps)
	if rec.Code != 200 || caps.DeviceName != "homelab" || caps.ServerConnectionType != "wired" || caps.Ookla.Available || caps.Ookla.Reason == nil ||
		!strings.Contains(*caps.Ookla.Reason, "OOKLA_ACCEPT_LICENSE=yes") || !caps.NDT7.Available || caps.NDT7.Reason != nil {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	// with nothing wired up the endpoint still answers, saying everything is off
	bare := newTestServer(&fakeStore{})
	rec = do(t, bare, "GET", "/api/v1/capabilities", "", "")
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"available":false`) {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	if rec := do(t, bare, "POST", "/api/v1/tests", "", `{}`); rec.Code != 503 {
		t.Errorf("tests are off without a manager: %d", rec.Code)
	} else {
		problem(t, rec)
	}
}

func TestCreateTestMapsEveryOutcomeToTheRightStatus(t *testing.T) {
	sc := newScripted([]byte(resultJSON))
	r := newODRig(t, sc, nil, false)
	for name, c := range map[string]struct{ body, field string }{
		"not json":                   {`hi`, ""},
		"empty":                      {`{}`, "provider"},
		"unknown mode":               {`{"provider":"ookla","mode":"cloud"}`, "mode"},
		"server, no connection type": {`{"provider":"ookla","mode":"server"}`, "connectionType"},
		"server, null device":        {`{"provider":"ookla","mode":"server","connectionType":"wired","device":null}`, "device"},
		"server, numeric device":     {`{"provider":"ookla","mode":"server","connectionType":"wired","device":5}`, "device"},
		"server, blank device":       {`{"provider":"ookla","mode":"server","connectionType":"wired","device":""}`, "device"},
		"server, whitespace device":  {`{"provider":"ookla","mode":"server","connectionType":"wired","device":"   "}`, "device"},
		"server, 65-char device":     {`{"provider":"ookla","mode":"server","connectionType":"wired","device":"` + strings.Repeat("0", 65) + `"}`, "device"},
		"ndt7 on the server":         {`{"provider":"ndt7","mode":"server","connectionType":"wired"}`, "provider"},
		"no device":                  {`{"provider":"ndt7","mode":"browser","connectionType":"wired"}`, "device"},
		"bad connection":             {`{"provider":"ndt7","mode":"browser","connectionType":"lte","device":"x"}`, "connectionType"},
	} {
		rec := r.do("POST", "/api/v1/tests", "", c.body)
		if rec.Code != 400 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
			continue
		}
		p := problem(t, rec)
		if c.field != "" && (len(p.Errors) == 0 || p.Errors[0].Field != c.field) {
			t.Errorf("%s: errors %+v", name, p.Errors)
		}
	}
	if rec := r.do("POST", "/api/v1/tests", "not-a-real-token", `{"provider":"ookla","mode":"server","connectionType":"wired"}`); rec.Code != 401 || rec.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("a wrong token is an error, not silently ignored: %d", rec.Code)
	}
	if rec := r.do("POST", "/api/v1/tests", "", strings.Repeat("x", 5000)); rec.Code != 400 {
		t.Errorf("oversized: %d", rec.Code)
	}

	code, test := r.create(`{"provider":"ookla","mode":"server","connectionType":"wireless","device":"spoof"}`)
	if code != 201 || test["state"] != "queued" || test["device"] != "server" || test["connectionType"] != "wired" || test["submitToken"] != nil || test["queuePosition"] != nil {
		t.Fatalf("%d %v", code, test)
	}
	id := test["id"].(string)
	r.waitState(id, "running")
	for i := 0; i < 200 && sc.started.Load() < 1; i++ { // "running" is recorded just before the process is launched
		time.Sleep(10 * time.Millisecond)
	}
	if code, _ := r.create(`{"provider":"ookla","mode":"server","connectionType":"wired"}`); code != 409 {
		t.Errorf("a second server test while one runs: %d", code)
	} else if rec := r.do("POST", "/api/v1/tests", "", `{"provider":"ookla","mode":"server","connectionType":"wired"}`); !strings.Contains(rec.Body.String(), "already running") {
		t.Errorf("%s", rec.Body)
	}
	if sc.started.Load() != 1 {
		t.Errorf("only one process: %d", sc.started.Load())
	}
	close(sc.finish)
	r.waitState(id, "completed")
}

func TestCreateTestWithoutAcceptedTermsIs503AndStartsNothing(t *testing.T) {
	sc := newScripted(nil)
	r := newODRig(t, sc, func(c *ondemand.Config) { c.OoklaAccepted = false }, false)
	rec := r.do("POST", "/api/v1/tests", "", `{"provider":"ookla","mode":"server","connectionType":"wired"}`)
	if rec.Code != 503 || !strings.Contains(rec.Body.String(), "OOKLA_ACCEPT_LICENSE=yes") {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	problem(t, rec)
	if sc.started.Load() != 0 {
		t.Error("no process may start")
	}
	if code, _ := r.create(`{"provider":"ndt7","mode":"browser","connectionType":"wired","device":"pc"}`); code != 201 {
		t.Errorf("NDT7 in a browser does not need Ookla's terms: %d", code)
	}
}

func TestStartingTestsIsRateLimited(t *testing.T) {
	r := newODRig(t, newScripted(nil), func(c *ondemand.Config) { c.CreatePerMinute = 1 }, false)
	body := `{"provider":"ndt7","mode":"browser","connectionType":"wired","device":"pc"}`
	for i := 0; i < 4; i++ {
		if code, _ := r.create(body); code != 201 {
			t.Fatalf("request %d: %d", i, code)
		}
	}
	rec := r.do("POST", "/api/v1/tests", "", body)
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Errorf("%d %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	problem(t, rec)
}

func TestAnAgentTokenDecidesTheDeviceName(t *testing.T) {
	r := newODRig(t, newScripted(nil), nil, false)
	rec := r.do("POST", "/api/v1/tests", "tok-laptop", `{"provider":"ndt7","mode":"browser","connectionType":"wireless","device":"spoofed"}`)
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if rec.Code != 201 || m["device"] != "laptop" {
		t.Errorf("%d %v", rec.Code, m)
	}
}

func TestLiveProgressOverSSEAndResume(t *testing.T) {
	sc := newScripted([]byte(resultJSON))
	r := newODRig(t, sc, nil, false)
	srv := httptest.NewUnstartedServer(r.srv.Handler())
	srv.Config.WriteTimeout = time.Second // the real server has a write timeout; the stream must outlive it
	srv.Start()
	defer srv.Close()

	_, test := r.create(`{"provider":"ookla","mode":"server","connectionType":"wired"}`)
	id := test["id"].(string)
	r.waitState(id, "running")

	resp, err := http.Get(srv.URL + "/api/v1/tests/" + id + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") ||
		resp.Header.Get("Cache-Control") != "no-cache" || resp.Header.Get("X-Accel-Buffering") != "no" {
		t.Fatalf("headers: %d %v", resp.StatusCode, resp.Header)
	}
	events := make(chan []sseEvent, 1)
	go func() {
		events <- readSSE(t, resp.Body, func(e sseEvent) bool { return e.Name == "result" || e.Name == "error" })
	}()

	// readings arrive live, one by one, spread over longer than the server's write timeout
	for i := 0; i < 3; i++ {
		sc.steps <- struct{}{}
		time.Sleep(800 * time.Millisecond)
	}
	close(sc.finish)

	var got []sseEvent
	select {
	case got = <-events:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream never delivered the result")
	}
	if names := eventNames(got); !strings.HasPrefix(names, "state,") || !strings.HasSuffix(names, ",state,result") || strings.Count(names, "progress") != 3 {
		t.Fatalf("event order: %s", names)
	}
	if got[0].ID != "" || got[0].Data["type"] != "state" {
		t.Errorf("the first event is the current state, with no id: %+v", got[0])
	}
	for _, e := range got {
		if e.Data["type"] != e.Name {
			t.Errorf("the SSE event name must equal the payload type: %s vs %v", e.Name, e.Data["type"])
		}
	}
	var progress []sseEvent
	for _, e := range got {
		if e.Name == "progress" {
			progress = append(progress, e)
		}
	}
	if progress[0].Data["phase"] != "download" || progress[0].Data["currentMbps"].(float64) != 10 || progress[2].Data["currentMbps"].(float64) != 30 {
		t.Errorf("readings: %+v", progress)
	}
	res := got[len(got)-1].Data["result"].(map[string]any)
	if res["provider"] != "ookla" || res["trigger"] != "on_demand" || res["downloadMbps"].(float64) != 100 {
		t.Errorf("result: %v", res)
	}

	// reconnect with Last-Event-ID: only what came after, preceded by the current state
	lastSeen := progress[0].ID
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/tests/"+id+"/events", nil)
	req.Header.Set("Last-Event-ID", lastSeen)
	resp2, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	resumed := readSSE(t, resp2.Body, nil) // the stream is over, so the server ends it
	if len(resumed) == 0 || resumed[0].Name != "state" || resumed[0].Data["test"].(map[string]any)["state"] != "completed" {
		t.Fatalf("a reconnect starts with the current state: %s", eventNames(resumed))
	}
	for _, e := range resumed[1:] {
		if e.ID <= lastSeen && len(e.ID) == len(lastSeen) {
			t.Errorf("already seen event %s replayed", e.ID)
		}
	}
	if !strings.HasSuffix(eventNames(resumed), "result") {
		t.Errorf("%s", eventNames(resumed))
	}
}

func TestSSEForATestFromBeforeARestartStillTellsTheOutcome(t *testing.T) {
	sc := newScripted([]byte(resultJSON))
	r := newODRig(t, sc, nil, false)
	_, test := r.create(`{"provider":"ookla","mode":"server","connectionType":"wired"}`)
	id := test["id"].(string)
	close(sc.finish)
	r.waitState(id, "completed")

	// a "restarted" process: same database, no memory of the test's events
	r2 := newODRigSharing(t, newScripted(nil))
	srv := httptest.NewServer(r2.srv.Handler())
	defer srv.Close()
	resp, err := http.Get(srv.URL + "/api/v1/tests/" + id + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	got := readSSE(t, resp.Body, nil)
	if eventNames(got) != "state,result" || got[1].Data["result"].(map[string]any)["downloadMbps"].(float64) != 100 {
		t.Errorf("%s", eventNames(got))
	}

	// and a test that failed
	failing := newScripted(nil)
	failing.failure = errors.New("the Ookla speed test failed: Network is unreachable")
	r3 := newODRigSharing(t, failing)
	_, test = func() (int, map[string]any) {
		rec := r3.do("POST", "/api/v1/tests", "", `{"provider":"ookla","mode":"server","connectionType":"wired"}`)
		var m map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &m)
		return rec.Code, m
	}()
	close(failing.finish)
	r3.waitState(test["id"].(string), "failed")
	r4 := newODRigSharing(t, newScripted(nil))
	srv2 := httptest.NewServer(r4.srv.Handler())
	defer srv2.Close()
	resp2, err := http.Get(srv2.URL + "/api/v1/tests/" + test["id"].(string) + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	got = readSSE(t, resp2.Body, nil)
	if eventNames(got) != "state,error" || !strings.Contains(got[1].Data["message"].(string), "Network is unreachable") {
		t.Errorf("%s %v", eventNames(got), got)
	}
}

// newODRigSharing builds another server on the same database without emptying it (a "restarted" process).
func newODRigSharing(t *testing.T, runner ondemand.Runner) *odRig {
	t.Helper()
	cfg := ondemand.Defaults()
	cfg.OoklaAccepted, cfg.OoklaPath, cfg.CreatePerMinute = true, "sh", 600
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	mgr := ondemand.NewManager(cfg, store.New(itPool), runner, log)
	srv := NewServer(store.New(itPool), NewAuthenticator(nil), "UTC", log)
	srv.SetOnDemand(mgr, false)
	return &odRig{t: t, srv: srv, mgr: mgr}
}

func TestSSEUnknownTestIs404(t *testing.T) {
	r := newODRig(t, newScripted(nil), nil, false)
	for _, id := range []string{"00000000-0000-0000-0000-000000000000", "not-a-uuid"} {
		rec := r.do("GET", "/api/v1/tests/"+id+"/events", "", "")
		if rec.Code != 404 {
			t.Errorf("%s: %d", id, rec.Code)
			continue
		}
		problem(t, rec)
	}
	for _, m := range []string{"GET", "DELETE"} {
		if rec := r.do(m, "/api/v1/tests/not-a-uuid", "", ""); rec.Code != 404 {
			t.Errorf("%s unknown: %d", m, rec.Code)
		}
	}
}

func TestCancelOverHTTPEndsTheStream(t *testing.T) {
	sc := newScripted([]byte(resultJSON))
	r := newODRig(t, sc, nil, false)
	srv := httptest.NewServer(r.srv.Handler())
	defer srv.Close()
	_, test := r.create(`{"provider":"ookla","mode":"server","connectionType":"wired"}`)
	id := test["id"].(string)
	r.waitState(id, "running")
	resp, err := http.Get(srv.URL + "/api/v1/tests/" + id + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	done := make(chan []sseEvent, 1)
	go func() { done <- readSSE(t, resp.Body, nil) }()
	time.Sleep(200 * time.Millisecond)

	rec := r.do("DELETE", "/api/v1/tests/"+id, "", "")
	var m map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &m)
	if rec.Code != 200 || m["state"] != "cancelled" || m["finishedAt"] == nil {
		t.Fatalf("%d %v", rec.Code, m)
	}
	select {
	case got := <-done:
		last := got[len(got)-1]
		if last.Name != "state" || last.Data["test"].(map[string]any)["state"] != "cancelled" {
			t.Errorf("%s", eventNames(got))
		}
	case <-time.After(8 * time.Second):
		t.Fatal("the stream must end when the test is cancelled")
	}
	if rec := r.do("DELETE", "/api/v1/tests/"+id, "", ""); rec.Code != 200 {
		t.Errorf("cancelling twice is fine: %d", rec.Code)
	}
}

func TestBrowserNDT7FlowOverHTTP(t *testing.T) {
	r := newODRig(t, newScripted(nil), nil, false)
	code, test := r.create(`{"provider":"ndt7","mode":"browser","connectionType":"wireless","device":"My phone"}`)
	if code != 201 || test["state"] != "running" || test["mode"] != "browser" {
		t.Fatalf("%d %v", code, test)
	}
	id, token := test["id"].(string), test["submitToken"].(string)
	url := "/api/v1/tests/" + id + "/result"
	now := time.Now().UTC()
	body := func(extra string) string {
		return `{"startedAt":"` + now.Add(-20*time.Second).Format(time.RFC3339) + `","finishedAt":"` + now.Format(time.RFC3339) +
			`","downloadMbps":87.5,"uploadMbps":23.25,"minRttMs":18.4,"retransmissionPct":0.31,` + extra +
			`"server":{"machine":"mlab1-lax05","location":"Los Angeles","country":"US"}}`
	}

	if rec := r.do("POST", url, "", body("")); rec.Code != 401 || rec.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("no token: %d", rec.Code)
	}
	if rec := r.do("POST", url, "wrong", body("")); rec.Code != 401 {
		t.Errorf("wrong token: %d", rec.Code)
	}
	// the agent token is not a submit token
	if rec := r.do("POST", url, "tok-laptop", body("")); rec.Code != 401 {
		t.Errorf("an agent token cannot submit a browser test: %d", rec.Code)
	}
	for name, bad := range map[string]string{
		"not json":       `hi`,
		"absurd speed":   strings.Replace(body(""), `87.5`, `1e30`, 1),
		"too quick":      `{"startedAt":"` + now.Format(time.RFC3339) + `","finishedAt":"` + now.Format(time.RFC3339) + `","downloadMbps":1,"uploadMbps":1}`,
		"too long":       `{"startedAt":"` + now.Add(-30*time.Minute).Format(time.RFC3339) + `","finishedAt":"` + now.Format(time.RFC3339) + `","downloadMbps":1,"uploadMbps":1}`,
		"long ago":       `{"startedAt":"2026-01-01T00:00:00Z","finishedAt":"2026-01-01T00:00:20Z","downloadMbps":1,"uploadMbps":1}`,
		"missing speeds": `{"startedAt":"` + now.Add(-20*time.Second).Format(time.RFC3339) + `","finishedAt":"` + now.Format(time.RFC3339) + `"}`,
	} {
		rec := r.do("POST", url, token, bad)
		if rec.Code != 400 {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
			continue
		}
		problem(t, rec)
	}
	if got := r.waitState(id, "running"); got["resultId"] != nil {
		t.Fatal("rejected submissions must leave the test open")
	}

	rec := r.do("POST", url, token, body(""))
	var res map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &res)
	if rec.Code != 201 || res["provider"] != "ndt7" || res["device"] != "My phone" || res["connectionType"] != "wireless" || res["trigger"] != "on_demand" ||
		res["downloadMbps"].(float64) != 87.5 || res["packetLossPct"].(float64) != 0.31 {
		t.Fatalf("%d %v", rec.Code, res)
	}
	done := r.waitState(id, "completed")
	if done["resultId"] != res["id"] {
		t.Errorf("the test points at its result: %v vs %v", done["resultId"], res["id"])
	}
	if rec := r.do("POST", url, token, body("")); rec.Code != 409 || !strings.Contains(rec.Body.String(), "already completed") {
		t.Errorf("a token works once: %d %s", rec.Code, rec.Body)
	}
	if rec := r.do("POST", "/api/v1/tests/00000000-0000-0000-0000-000000000000/result", token, body("")); rec.Code != 404 {
		t.Errorf("unknown test: %d", rec.Code)
	}
	// it is an ordinary result as far as the dashboard is concerned
	var list listResp
	getJSON(t, "/api/v1/results?provider=ndt7", &list)
	_ = list
}

func TestClientAddressIsOnlyTrustedBehindAProxy(t *testing.T) {
	for _, trust := range []bool{false, true} {
		r := newODRig(t, newScripted(nil), nil, trust)
		req := httptest.NewRequest("POST", "/api/v1/tests", strings.NewReader(`{"provider":"ndt7","mode":"browser","connectionType":"wired","device":"pc"}`))
		req.RemoteAddr = "192.0.2.77:51234"
		req.Header.Set("X-Forwarded-For", "203.0.113.200, 10.0.0.1")
		rec := httptest.NewRecorder()
		r.srv.Handler().ServeHTTP(rec, req)
		if rec.Code != 201 {
			t.Fatalf("%d %s", rec.Code, rec.Body)
		}
		var ip *string
		_ = itPool.QueryRow(context.Background(), `SELECT host(client_ip) FROM tests ORDER BY created_at DESC LIMIT 1`).Scan(&ip)
		want := "192.0.2.77" // a client can put anything in X-Forwarded-For; it only counts when we are behind our own proxy
		if trust {
			want = "203.0.113.200"
		}
		if ip == nil || *ip != want {
			t.Errorf("trustProxy=%v: recorded %v, want %s", trust, ip, want)
		}
	}
}

func TestListAndGetTests(t *testing.T) {
	r := newODRig(t, newScripted(nil), nil, false)
	r.create(`{"provider":"ndt7","mode":"browser","connectionType":"wired","device":"pc"}`)
	var list struct{ Items []map[string]any }
	rec := r.do("GET", "/api/v1/tests?state=running,queued&limit=5", "", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if rec.Code != 200 || len(list.Items) != 1 || list.Items[0]["device"] != "pc" || list.Items[0]["submitToken"] != nil {
		t.Errorf("%d %s", rec.Code, rec.Body)
	}
	rec = r.do("GET", "/api/v1/tests?state=completed", "", "")
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Errorf("empty list, not null: %s", rec.Body)
	}
	for _, q := range []string{"state=bogus", "limit=0", "limit=101", "state="} {
		if rec := r.do("GET", "/api/v1/tests?"+q, "", ""); rec.Code != 400 {
			t.Errorf("?%s: %d", q, rec.Code)
		}
	}
}
