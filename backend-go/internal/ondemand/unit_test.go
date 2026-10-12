package ondemand

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func TestConfigDefaultsAndValidation(t *testing.T) {
	c, err := FromEnv(env(nil))
	if err != nil || c.DeviceName != "server" || c.ServerConnection != "wired" || c.OoklaAccepted || c.TestTimeout != 3*time.Minute ||
		c.BrowserTTL != 15*time.Minute || c.TrustProxy || c.CreatePerMinute != 6 || c.OoklaPath != "speedtest" {
		t.Fatalf("defaults: %+v %v", c, err)
	}
	c, err = FromEnv(env(map[string]string{
		"DEVICE_NAME": "homelab", "SERVER_CONNECTION_TYPE": "wireless", "OOKLA_PATH": "/opt/speedtest", "OOKLA_ACCEPT_LICENSE": "yes",
		"ONDEMAND_TIMEOUT_SECONDS": "60", "BROWSER_TEST_TTL_MINUTES": "5", "TRUST_PROXY": "1", "ONDEMAND_PER_MINUTE": "2",
	}))
	if err != nil || c.DeviceName != "homelab" || c.ServerConnection != "wireless" || !c.OoklaAccepted || c.TestTimeout != time.Minute ||
		c.BrowserTTL != 5*time.Minute || !c.TrustProxy || c.CreatePerMinute != 2 {
		t.Fatalf("overrides: %+v %v", c, err)
	}
	for name, vars := range map[string]map[string]string{
		"OOKLA_ACCEPT_LICENSE":     {"OOKLA_ACCEPT_LICENSE": "true"}, // only an exact "yes" counts as accepting
		"SERVER_CONNECTION_TYPE":   {"SERVER_CONNECTION_TYPE": "lte"},
		"DEVICE_NAME":              {"DEVICE_NAME": strings.Repeat("x", 65)},
		"ONDEMAND_TIMEOUT_SECONDS": {"ONDEMAND_TIMEOUT_SECONDS": "5"},
		"BROWSER_TEST_TTL_MINUTES": {"BROWSER_TEST_TTL_MINUTES": "0"},
		"ONDEMAND_PER_MINUTE":      {"ONDEMAND_PER_MINUTE": "0"},
		"TRUST_PROXY":              {"TRUST_PROXY": "maybe"},
	} {
		if _, err := FromEnv(env(vars)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: want an error naming it, got %v", name, err)
		}
	}
}

func TestOoklaStatusExplainsWhatToDo(t *testing.T) {
	found := func(string) (string, error) { return "/usr/bin/speedtest", nil }
	missing := func(string) (string, error) { return "", exec.ErrNotFound }
	c := Defaults()
	if ok, why := c.OoklaStatus(found); ok || !strings.Contains(why, "OOKLA_ACCEPT_LICENSE=yes") || !strings.Contains(why, OoklaTermsURL) {
		t.Errorf("not accepted: %v %q", ok, why)
	}
	c.OoklaAccepted = true
	if ok, why := c.OoklaStatus(missing); ok || !strings.Contains(why, "not found") || !strings.Contains(why, "OOKLA_PATH") {
		t.Errorf("missing tool: %v %q", ok, why)
	}
	if ok, why := c.OoklaStatus(found); !ok || why != "" {
		t.Errorf("ready: %v %q", ok, why)
	}
}

func readFixture(t *testing.T) [][]byte {
	t.Helper()
	f, err := os.Open("testdata/ookla_real.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var lines [][]byte
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		lines = append(lines, append([]byte(nil), sc.Bytes()...))
	}
	return lines
}

func TestParseRealOoklaOutput(t *testing.T) {
	lines := readFixture(t)
	if len(lines) < 100 {
		t.Fatalf("fixture too small: %d", len(lines))
	}
	phases := map[string]int{}
	var results int
	var lastDownload *Progress
	for _, l := range lines {
		p, isResult, err := ParseOoklaLine(l, 1500*time.Millisecond)
		if err != nil {
			t.Fatalf("%s: %v", l, err)
		}
		if isResult {
			results++
			continue
		}
		if p == nil {
			continue // testStart
		}
		if p.Type != "progress" || p.ElapsedMs != 1500 {
			t.Errorf("progress shape: %+v", p)
		}
		phases[p.Phase]++
		if p.Phase == "download" {
			lastDownload = p
		}
		if p.Fraction != nil && (*p.Fraction < 0 || *p.Fraction > 1) {
			t.Errorf("fraction out of range: %v", *p.Fraction)
		}
	}
	if results != 1 || phases["ping"] != 5 || phases["download"] != 75 || phases["upload"] != 62 {
		t.Errorf("results=%d phases=%v", results, phases)
	}
	// the last download reading is the final speed: 12402857 bytes/s = 99.2 Mbps
	if lastDownload == nil || lastDownload.CurrentMbps == nil || *lastDownload.CurrentMbps < 99.2 || *lastDownload.CurrentMbps > 99.3 {
		t.Errorf("bytes/s must be converted to Mbps: %+v", lastDownload)
	}
	if lastDownload.Fraction == nil || *lastDownload.Fraction != 1 {
		t.Errorf("final reading is 100%%: %+v", lastDownload)
	}
}

func TestParseOoklaLineEdgeCases(t *testing.T) {
	for name, line := range map[string]string{
		"not json":      `speedtest: cannot open socket`,
		"truncated":     `{"type":"download","download":{"bandwidth":`,
		"empty object":  `{}`,
		"unknown type":  `{"type":"log","message":"hi"}`,
		"null download": `{"type":"download","download":null}`,
		"null ping":     `{"type":"ping","ping":null}`,
	} {
		p, isResult, err := ParseOoklaLine([]byte(line), time.Second)
		switch name {
		case "not json", "truncated":
			if err == nil {
				t.Errorf("%s: want an error", name)
			}
		default:
			if err != nil || p != nil || isResult {
				t.Errorf("%s: want nothing, got %+v %v %v", name, p, isResult, err)
			}
		}
	}
	// a reading without bandwidth is still a progress reading, just without a speed
	p, _, _ := ParseOoklaLine([]byte(`{"type":"upload","upload":{"progress":0.5}}`), 0)
	if p == nil || p.Phase != "upload" || p.CurrentMbps != nil || *p.Fraction != 0.5 {
		t.Errorf("%+v", p)
	}
}

func TestHubReplayResumeAndClose(t *testing.T) {
	h := NewHub()
	if s := h.Subscribe("nope", 0); s.Exists || s.Closed || s.Live != nil {
		t.Errorf("unknown test: %+v", s)
	}
	h.Publish("t1", "state", map[string]string{"a": "1"})
	h.Publish("t1", "progress", map[string]int{"n": 1})
	h.Publish("t1", "progress", map[string]int{"n": 2})

	late := h.Subscribe("t1", 0)
	if !late.Exists || len(late.Replay) != 3 || late.Replay[0].ID != 1 || late.Replay[2].ID != 3 || late.Replay[1].Name != "progress" {
		t.Fatalf("a late subscriber gets everything, in order: %+v", late.Replay)
	}
	resumed := h.Subscribe("t1", 2)
	if len(resumed.Replay) != 1 || resumed.Replay[0].ID != 3 {
		t.Errorf("Last-Event-ID resumes after the given id: %+v", resumed.Replay)
	}

	h.Publish("t1", "progress", map[string]int{"n": 3})
	if ev := <-late.Live; ev.ID != 4 || string(ev.Data) != `{"n":3}` {
		t.Errorf("live event: %+v", ev)
	}
	h.Close("t1")
	if _, ok := <-late.Live; ok { // drained: the channel is closed
		for range late.Live {
		}
	}
	h.Publish("t1", "progress", 1) // ignored after close
	after := h.Subscribe("t1", 0)
	if !after.Closed || after.Live != nil || len(after.Replay) != 4 {
		t.Errorf("after close: closed=%v live=%v replay=%d", after.Closed, after.Live != nil, len(after.Replay))
	}
	h.Close("t1") // idempotent
	late.Cancel() // cancelling after close is safe
}

func TestHubDropsASlowSubscriberWithoutBlocking(t *testing.T) {
	h := NewHub()
	h.Publish("t", "state", 0)
	slow := h.Subscribe("t", 0)
	fast := h.Subscribe("t", 0)
	done := make(chan struct{})
	go func() {
		for i := 0; i < subBuffer+20; i++ {
			h.Publish("t", "progress", i)
			for len(fast.Live) > 0 {
				<-fast.Live
			}
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("a stalled reader must never block publishing")
	}
	n := 0
	for range slow.Live { // closed by the hub once it fell behind
		n++
	}
	if n != subBuffer {
		t.Errorf("slow reader got %d buffered events before being dropped, want %d", n, subBuffer)
	}
	if resume := h.Subscribe("t", int64(n)); len(resume.Replay) == 0 {
		t.Error("a dropped reader can reconnect and catch up from the buffer")
	}
}

func TestHubBoundsMemoryAndForgetsFinishedStreams(t *testing.T) {
	h := NewHub()
	for i := 0; i < replayLimit+50; i++ {
		h.Publish("t", "progress", i)
	}
	if s := h.Subscribe("t", 0); len(s.Replay) != replayLimit || s.Replay[len(s.Replay)-1].ID != replayLimit+50 {
		t.Errorf("replay is capped at the newest %d: got %d", replayLimit, len(s.Replay))
	}
	clock := time.Now()
	h.now = func() time.Time { return clock }
	h.Close("t")
	h.Forget()
	if !h.Subscribe("t", 0).Exists {
		t.Error("a just-finished stream is kept for late listeners")
	}
	clock = clock.Add(keepFinished + time.Second)
	h.Forget()
	if h.Subscribe("t", 0).Exists {
		t.Error("an old finished stream is dropped")
	}
}

// fakeCLI writes an executable shell script and returns its path.
func fakeCLI(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "speedtest")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func fixturePath(t *testing.T) string {
	t.Helper()
	p, err := filepath.Abs("testdata/ookla_real.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestOoklaRunnerStreamsProgressAndReturnsTheResult(t *testing.T) {
	var args string
	cli := fakeCLI(t, `echo "$@" > `+filepath.Join(t.TempDir(), "args")+`
while IFS= read -r line; do echo "$line"; done < `+fixturePath(t))
	_ = args
	var got []Progress
	raw, err := Ookla{Path: cli}.Run(context.Background(), func(p Progress) { got = append(got, p) })
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5+75+62 {
		t.Errorf("progress events: %d", len(got))
	}
	if !bytes.Contains(raw, []byte(`"type":"result"`)) || !bytes.Contains(raw, []byte(`"bandwidth":12402857`)) {
		t.Errorf("result: %.200s", raw)
	}
	for i := 1; i < len(got); i++ {
		if got[i].ElapsedMs < got[i-1].ElapsedMs {
			t.Fatal("elapsed time must not go backwards")
		}
	}
}

func TestOoklaRunnerPassesTheAcceptanceFlagsOnlyBecauseTheGateIsOpen(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	cli := fakeCLI(t, `echo "$@" > `+argsFile+"\ncat "+fixturePath(t))
	if _, err := (Ookla{Path: cli}).Run(context.Background(), func(Progress) {}); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(argsFile)
	if strings.TrimSpace(string(b)) != "--format=jsonl --accept-license --accept-gdpr" {
		t.Errorf("args: %q", b)
	}
}

func TestOoklaRunnerFailures(t *testing.T) {
	cases := map[string]struct {
		script string
		want   string
	}{
		"non-zero exit with a reason": {`echo "[error] Cannot read: Network is unreachable" >&2; exit 1`, "Network is unreachable"},
		"exit without any message":    {`exit 3`, "exit status 3"},
		"no result line":              {`echo '{"type":"testStart"}'`, "finished without a result"},
		"garbage only":                {`echo 'not json at all'`, "unreadable output"},
		"empty output":                {`true`, "finished without a result"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Ookla{Path: fakeCLI(t, c.script)}.Run(context.Background(), func(Progress) {})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("want %q, got %v", c.want, err)
			}
		})
	}
	if _, err := (Ookla{Path: "/no/such/speedtest"}).Run(context.Background(), func(Progress) {}); err == nil || !strings.Contains(err.Error(), "could not start") {
		t.Errorf("missing executable: %v", err)
	}
}

func TestOoklaRunnerStopsPromptlyWhenCancelled(t *testing.T) {
	cli := fakeCLI(t, `echo '{"type":"ping","ping":{"latency":1,"jitter":0,"progress":0.2}}'
sleep 30`)
	ctx, cancel := context.WithCancelCause(context.Background())
	started := time.Now()
	go func() { time.Sleep(300 * time.Millisecond); cancel(errCancelled) }()
	_, err := Ookla{Path: cli}.Run(ctx, func(Progress) {})
	if !errors.Is(err, errCancelled) {
		t.Errorf("the cancellation cause must come back: %v", err)
	}
	if time.Since(started) > 6*time.Second {
		t.Errorf("took %v to stop a 30 s process", time.Since(started))
	}

	ctx2, cancel2 := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel2()
	if _, err := (Ookla{Path: cli}).Run(ctx2, func(Progress) {}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("a timeout is reported as one: %v", err)
	}
}

func TestOoklaRunnerKeepsStderrShort(t *testing.T) {
	long := strings.Repeat("e", 5000)
	_, err := Ookla{Path: fakeCLI(t, `echo "`+long+`" >&2; exit 1`)}.Run(context.Background(), func(Progress) {})
	if err == nil || len(err.Error()) > 400 {
		t.Errorf("error message must stay short, got %d chars", len(err.Error()))
	}
}

func TestTokensAreRandomAndHashedConsistently(t *testing.T) {
	a, ha, err := newToken()
	if err != nil {
		t.Fatal(err)
	}
	b, _, _ := newToken()
	if a == b || len(a) < 40 {
		t.Errorf("tokens must be long and unique: %q %q", a, b)
	}
	if !bytes.Equal(hashToken(a), ha) || bytes.Equal(hashToken(b), ha) {
		t.Error("hash of the token must match what is stored, and only that token")
	}
}

func TestIPLimiter(t *testing.T) {
	l := newIPLimiter(2, 60) // burst 2, one more per second
	now := time.Unix(0, 0)
	l.now = func() time.Time { return now }
	for i := 0; i < 2; i++ {
		if ok, _ := l.allow("1.2.3.4"); !ok {
			t.Fatalf("burst %d refused", i)
		}
	}
	if ok, retry := l.allow("1.2.3.4"); ok || retry < 1 {
		t.Errorf("third in a row: ok=%v retry=%d", ok, retry)
	}
	if ok, _ := l.allow("5.6.7.8"); !ok {
		t.Error("another address has its own allowance")
	}
	now = now.Add(1100 * time.Millisecond)
	if ok, _ := l.allow("1.2.3.4"); !ok {
		t.Error("refills over time")
	}
	now = now.Add(time.Hour)
	l.forget()
	if len(l.buckets) != 0 {
		t.Error("idle entries are dropped")
	}
}

func TestCleanName(t *testing.T) {
	for s, want := range map[string]bool{"My phone": true, "": false, strings.Repeat("a", 64): true, strings.Repeat("a", 65): false,
		"bad\x00name": false, "tab\there": false, "\xff\xfe": false, "ünïcode ✓": true} {
		if got := cleanName(s); got != want {
			t.Errorf("cleanName(%q) = %v, want %v", s, got, want)
		}
	}
}
