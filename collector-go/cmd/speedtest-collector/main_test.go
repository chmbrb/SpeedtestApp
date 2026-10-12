package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const okResult = `{"type":"result","timestamp":"2026-10-11T12:00:00Z","download":{"bandwidth":116000000},"upload":{"bandwidth":14000000}}`

// fakeAPI is a stand-in for the SpeedtestApp.
type fakeAPI struct {
	mu      sync.Mutex
	srv     *httptest.Server
	status  int // answer for POST /api/v1/results; 201 by default
	posts   []map[string]any
	auths   []string
	healthy bool
}

func newAPI(t *testing.T) *fakeAPI {
	t.Helper()
	f := &fakeAPI{status: 201, healthy: true}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		switch {
		case r.URL.Path == "/api/v1/health":
			if f.healthy {
				_, _ = w.Write([]byte(`{"status":"ok"}`))
			} else {
				w.WriteHeader(503)
			}
		case r.URL.Path == "/api/v1/results" && r.Method == "POST":
			b, _ := io.ReadAll(r.Body)
			var m map[string]any
			_ = json.Unmarshal(b, &m)
			if string(b) == "{}" { // the token check
				if r.Header.Get("Authorization") != "Bearer good-token" {
					w.WriteHeader(401)
				} else {
					w.WriteHeader(400)
				}
				return
			}
			f.auths = append(f.auths, r.Header.Get("Authorization"))
			if f.status == 201 {
				f.posts = append(f.posts, m)
				w.WriteHeader(201)
				_, _ = w.Write([]byte(`{"id":"new-id"}`))
				return
			}
			w.WriteHeader(f.status)
			_, _ = w.Write([]byte(`{"detail":"nope"}`))
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAPI) count() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.posts) }

func fakeOokla(t *testing.T, body string) (path, argsFile string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script stand-ins")
	}
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	path = filepath.Join(dir, "speedtest")
	script := "#!/bin/sh\necho \"$@\" > " + argsFile + "\n" + body + "\n"
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return path, argsFile
}

func base(t *testing.T, api *fakeAPI, ookla string) []string {
	t.Helper()
	t.Setenv("SPEEDTEST_API_TOKEN", "good-token")
	for _, k := range []string{"SPEEDTEST_API_URL", "SPEEDTEST_CONNECTION", "SPEEDTEST_PROVIDER", "OOKLA_ACCEPT_LICENSE", "MLAB_ACCEPT_POLICY", "OOKLA_PATH", "SPEEDTEST_SPOOL_DIR", "SPEEDTEST_INTERVAL", "SPEEDTEST_JITTER"} {
		t.Setenv(k, "")
	}
	return []string{"--api-url", api.srv.URL, "--connection", "wireless", "--ookla-path", ookla, "--spool-dir", filepath.Join(t.TempDir(), "q"), "--accept-ookla-license"}
}

func exec(t *testing.T, args ...string) (code int, stdout, stderr string) {
	t.Helper()
	var o, e bytes.Buffer
	code = run(args, &o, &e)
	return code, o.String(), e.String()
}

func TestHelpVersionAndUsageErrors(t *testing.T) {
	if code, out, _ := exec(t, "help"); code != 0 || !strings.Contains(out, "run, daemon, flush, check") && !strings.Contains(out, "daemon") {
		t.Errorf("%d", code)
	}
	if code, out, _ := exec(t); code != exitUsage || !strings.Contains(out, "Usage:") {
		t.Errorf("no arguments prints the usage and fails: %d", code)
	}
	if code, out, _ := exec(t, "version"); code != 0 || strings.TrimSpace(out) != "dev" {
		t.Errorf("%d %q", code, out)
	}
	if code, _, errOut := exec(t, "dance"); code != exitUsage || !strings.Contains(errOut, "unknown command") {
		t.Errorf("%d %s", code, errOut)
	}
	t.Setenv("SPEEDTEST_API_TOKEN", "")
	t.Setenv("SPEEDTEST_API_URL", "")
	if code, _, errOut := exec(t, "run"); code != exitUsage || !strings.Contains(errOut, "--api-url") || !strings.Contains(errOut, "token") {
		t.Errorf("settings problems exit 2 and name every problem: %d %s", code, errOut)
	}
	if code, out, _ := exec(t, "run", "-h"); code != 0 || !strings.Contains(out, "Usage:") {
		t.Errorf("-h: %d", code)
	}
}

func TestRunSendsTheResult(t *testing.T) {
	api := newAPI(t)
	ookla, args := fakeOokla(t, "echo '"+okResult+"'")
	code, out, errOut := exec(t, append([]string{"run"}, base(t, api, ookla)...)...)
	if code != 0 || api.count() != 1 {
		t.Fatalf("%d posts=%d\n%s", code, api.count(), errOut)
	}
	p := api.posts[0]
	if p["provider"] != "ookla" || p["connectionType"] != "wireless" || p["trigger"] != "scheduled" || p["result"].(map[string]any)["type"] != "result" {
		t.Errorf("%v", p)
	}
	if api.auths[0] != "Bearer good-token" {
		t.Errorf("%q", api.auths[0])
	}
	if strings.Contains(out+errOut, "good-token") {
		t.Error("the token must never be printed")
	}
	b, _ := os.ReadFile(args)
	if strings.TrimSpace(string(b)) != "--format=json --accept-license --accept-gdpr" {
		t.Errorf("%q", b)
	}
}

func TestOoklaFlagsAreOnlyPassedWithTheAcceptanceFlag(t *testing.T) {
	api := newAPI(t)
	ookla, args := fakeOokla(t, "echo '"+okResult+"'")
	a := base(t, api, ookla)
	var noAccept []string
	for _, x := range a {
		if x != "--accept-ookla-license" {
			noAccept = append(noAccept, x)
		}
	}
	if code, _, _ := exec(t, append([]string{"run"}, noAccept...)...); code != 0 {
		t.Fatal(code)
	}
	if b, _ := os.ReadFile(args); strings.TrimSpace(string(b)) != "--format=json" {
		t.Errorf("without acceptance the tool is run plainly, as the person would: %q", b)
	}
}

func TestAnOfflineRunQueuesThenFlushSendsIt(t *testing.T) {
	api := newAPI(t)
	ookla, _ := fakeOokla(t, "echo '"+okResult+"'")
	args := base(t, api, ookla)
	api.mu.Lock()
	api.status = 503
	api.mu.Unlock()
	code, _, errOut := exec(t, append([]string{"run"}, args...)...)
	if code != 0 || api.count() != 0 || !strings.Contains(errOut, "queued") {
		t.Fatalf("a test that ran and is waiting is not a failure: %d\n%s", code, errOut)
	}
	if code, out, _ := exec(t, append([]string{"flush"}, args...)...); code != exitFailed || !strings.Contains(out, "still waiting 1") {
		t.Errorf("flush while still offline: %d %s", code, out)
	}
	api.mu.Lock()
	api.status = 201
	api.mu.Unlock()
	code, out, _ := exec(t, append([]string{"flush"}, args...)...)
	if code != 0 || api.count() != 1 || !strings.Contains(out, "sent 1") || !strings.Contains(out, "still waiting 0") {
		t.Errorf("%d %s posts=%d", code, out, api.count())
	}
}

func TestExitCodesTellCronWhatHappened(t *testing.T) {
	api := newAPI(t)
	ookla, _ := fakeOokla(t, "echo '"+okResult+"'")
	args := base(t, api, ookla)

	// a test that fails
	broken, _ := fakeOokla(t, "echo '[error] Network is unreachable' >&2; exit 1")
	failArgs := append([]string(nil), args...)
	failArgs[5] = broken // the value after --ookla-path
	if code, _, errOut := exec(t, append([]string{"run"}, failArgs...)...); code != exitFailed || !strings.Contains(errOut, "Network is unreachable") {
		t.Errorf("failed test: %d %s", code, errOut)
	}
	// a wrong token: the result is kept, and the exit code says the token needs fixing
	api.mu.Lock()
	api.status = 401
	api.mu.Unlock()
	if code, _, _ := exec(t, append([]string{"run"}, args...)...); code != exitAuth {
		t.Errorf("token refused: %d", code)
	}
	if code, _, _ := exec(t, append([]string{"flush"}, args...)...); code != exitAuth {
		t.Errorf("flush with a refused token: %d", code)
	}
	// the server refuses the result as invalid
	api.mu.Lock()
	api.status = 400
	api.mu.Unlock()
	if code, _, errOut := exec(t, append([]string{"run"}, args...)...); code != exitFailed || !strings.Contains(errOut, "refused") {
		t.Errorf("rejected: %d %s", code, errOut)
	}
	// another test is already running
	lockDir := filepath.Join(t.TempDir(), "q")
	_ = os.MkdirAll(lockDir, 0o700)
	_ = os.WriteFile(filepath.Join(lockDir, "running.lock"), []byte("1 now\n"), 0o600)
	busy := append([]string(nil), args...)
	busy[7] = lockDir // the value after --spool-dir
	if code, _, errOut := exec(t, append([]string{"run"}, busy...)...); code != exitBusy || !strings.Contains(errOut, "already running") {
		t.Errorf("busy: %d %s", code, errOut)
	}
}

func TestTokenFileAndTrigger(t *testing.T) {
	api := newAPI(t)
	ookla, _ := fakeOokla(t, "echo '"+okResult+"'")
	args := base(t, api, ookla)
	t.Setenv("SPEEDTEST_API_TOKEN", "")
	tf := filepath.Join(t.TempDir(), "token")
	_ = os.WriteFile(tf, []byte("good-token\n"), 0o600)
	code, _, errOut := exec(t, append([]string{"run", "--token-file", tf, "--trigger", "on_demand"}, args...)...)
	if code != 0 || api.count() != 1 || api.posts[0]["trigger"] != "on_demand" {
		t.Errorf("%d %s", code, errOut)
	}
}

func TestCheckExplainsWhatIsWrong(t *testing.T) {
	api := newAPI(t)
	ookla, _ := fakeOokla(t, "echo 'Speedtest by Ookla 1.2.0.84'")
	args := base(t, api, ookla)
	code, out, _ := exec(t, append([]string{"check"}, args...)...)
	if code != 0 || !strings.Contains(out, "the API answers") || !strings.Contains(out, "the token is accepted") ||
		!strings.Contains(out, "Ookla tool found (Speedtest by Ookla 1.2.0.84)") || !strings.Contains(out, "Everything looks fine") || strings.Contains(out, "good-token") {
		t.Errorf("%d\n%s", code, out)
	}
	// a wrong token, an unaccepted Ookla licence and a missing tool are each named
	t.Setenv("SPEEDTEST_API_TOKEN", "wrong-token")
	var noAccept []string
	for _, x := range args {
		if x != "--accept-ookla-license" {
			noAccept = append(noAccept, x)
		}
	}
	code, out, _ = exec(t, append([]string{"check"}, noAccept...)...)
	if code != exitFailed || !strings.Contains(out, "refused the token") || !strings.Contains(out, "1 problem(s)") ||
		!strings.Contains(out, "note Ookla tool found") || !strings.Contains(out, "--accept-ookla-license") {
		t.Errorf("an unconfirmed licence is a note, not a failure (the tool remembers an earlier acceptance): %d\n%s", code, out)
	}
	noToken := append([]string(nil), noAccept...)
	t.Setenv("SPEEDTEST_API_TOKEN", "good-token")
	if code, out, _ := exec(t, append([]string{"check"}, noToken...)...); code != 0 || !strings.Contains(out, "Everything looks fine") {
		t.Errorf("a working setup passes even without the flag: %d\n%s", code, out)
	}
	t.Setenv("SPEEDTEST_API_TOKEN", "wrong-token")
	missing := append([]string(nil), args...)
	missing[5] = "/no/such/speedtest" // the value after --ookla-path
	if code, out, _ := exec(t, append([]string{"check"}, missing...)...); code != exitFailed || !strings.Contains(out, "Ookla tool: not found") {
		t.Errorf("%d\n%s", code, out)
	}
	// the API down
	api.mu.Lock()
	api.healthy = false
	api.mu.Unlock()
	t.Setenv("SPEEDTEST_API_TOKEN", "good-token")
	if code, out, _ := exec(t, append([]string{"check"}, args...)...); code != exitFailed || !strings.Contains(out, "not a healthy SpeedtestApp") || strings.Contains(out, "token is accepted") {
		t.Errorf("%d\n%s", code, out)
	}
}

func TestDaemonRunsAndStopsCleanlyOnInterrupt(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("signals")
	}
	api := newAPI(t)
	ookla, _ := fakeOokla(t, "echo '"+okResult+"'")
	args := append([]string{"daemon", "--interval", "1h"}, base(t, api, ookla)...)
	done := make(chan int, 1)
	go func() { code, _, _ := exec(t, args...); done <- code }()
	deadline := time.Now().Add(10 * time.Second)
	for api.count() == 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if api.count() != 1 {
		t.Fatal("the daemon should test as soon as it starts")
	}
	_ = syscall.Kill(syscall.Getpid(), syscall.SIGINT) // the handler is installed by now: a result has already been sent
	select {
	case code := <-done:
		if code != 0 {
			t.Errorf("exit %d", code)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the daemon did not stop on SIGINT")
	}
}
