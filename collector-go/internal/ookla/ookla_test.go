package ookla

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

const result = `{"type":"result","timestamp":"2026-10-11T12:00:00Z","ping":{"jitter":1.2,"latency":12.5},"download":{"bandwidth":116000000},"upload":{"bandwidth":14000000},"packetLoss":0,"isp":"TestNet","server":{"id":1,"name":"Srv"},"result":{"id":"1ee33b76-fb40-407c-babc-fbb6fcf7758d","url":"https://www.speedtest.net/result/c/1"}}`

func fake(t *testing.T, script string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("these tests use shell scripts as stand-ins for the tool")
	}
	p := filepath.Join(t.TempDir(), "speedtest")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestReturnsTheResultLine(t *testing.T) {
	raw, err := Runner{Path: fake(t, "echo '"+result+"'")}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil || m["type"] != "result" || m["isp"] != "TestNet" {
		t.Errorf("%s", raw)
	}
}

func TestFindsTheResultAmongOtherOutput(t *testing.T) {
	raw, err := Runner{Path: fake(t, `echo "Speedtest by Ookla"; echo '{"type":"log","message":"hi"}'; echo '`+result+`'; echo "done"`)}.Run(context.Background())
	if err != nil || !strings.Contains(string(raw), `"type":"result"`) {
		t.Errorf("%s %v", raw, err)
	}
}

func TestTheAcceptanceFlagsAreOnlyPassedWhenThePersonAccepted(t *testing.T) {
	argsFile := filepath.Join(t.TempDir(), "args")
	script := `echo "$@" > ` + argsFile + "\necho '" + result + "'"
	for accepted, want := range map[bool]string{false: "--format=json", true: "--format=json --accept-license --accept-gdpr"} {
		if _, err := (Runner{Path: fake(t, script), AcceptLicense: accepted}).Run(context.Background()); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(argsFile)
		if strings.TrimSpace(string(b)) != want {
			t.Errorf("accepted=%v: args %q, want %q", accepted, b, want)
		}
	}
}

func TestFailuresExplainThemselves(t *testing.T) {
	cases := map[string]struct {
		script   string
		accepted bool
		want     string
	}{
		"network down":           {`echo "[error] Cannot read: Network is unreachable" >&2; exit 1`, true, "Network is unreachable"},
		"no message":             {`exit 3`, true, "exit status 3"},
		"terms not accepted":     {`echo "You must accept the license agreement to use Speedtest CLI" >&2; exit 1`, false, "run with --accept-ookla-license"},
		"licence text on stdout": {`echo "License acceptance required"; exit 1`, false, "terms have not been accepted"},
		"no result printed":      {`echo '{"type":"testStart"}'`, true, "without printing a result"},
		"garbage":                {`echo 'not json'`, true, "without printing a result"},
		"empty output":           {`true`, true, "without printing a result"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Runner{Path: fake(t, c.script), AcceptLicense: c.accepted}.Run(context.Background())
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("want %q, got %v", c.want, err)
			}
		})
	}
	// a licence complaint when the person HAS accepted is not blamed on them
	_, err := Runner{Path: fake(t, `echo "license error from server" >&2; exit 1`), AcceptLicense: true}.Run(context.Background())
	if err == nil || strings.Contains(err.Error(), "have not been accepted") {
		t.Errorf("%v", err)
	}
}

func TestAMissingToolSaysSo(t *testing.T) {
	_, err := Runner{Path: "/no/such/speedtest"}.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "was not found") || !strings.Contains(err.Error(), "--ookla-path") {
		t.Errorf("%v", err)
	}
	if _, err := (Runner{Path: "/no/such/speedtest"}).Version(context.Background()); err == nil || !strings.Contains(err.Error(), "not found") {
		t.Errorf("%v", err)
	}
}

func TestTheToolNeverWaitsForInput(t *testing.T) {
	// a tool that prompts and waits would hang an unattended run forever; with stdin closed it reads EOF and carries on
	p := fake(t, `read answer; echo '`+result+`'`)
	done := make(chan error, 1)
	go func() { _, err := Runner{Path: p}.Run(context.Background()); done <- err }()
	select {
	case err := <-done:
		if err != nil {
			t.Error(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("blocked waiting for input")
	}
}

func TestATestThatRunsTooLongIsStopped(t *testing.T) {
	p := fake(t, "sleep 30")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := Runner{Path: p}.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "did not finish in time") {
		t.Errorf("%v", err)
	}
	if time.Since(started) > 8*time.Second {
		t.Errorf("took %v to stop a 30 s process", time.Since(started))
	}
}

func TestVersion(t *testing.T) {
	v, err := Runner{Path: fake(t, `echo "Speedtest by Ookla 1.2.0.84"; echo more`)}.Version(context.Background())
	if err != nil || v != "Speedtest by Ookla 1.2.0.84" {
		t.Errorf("%q %v", v, err)
	}
}

func TestLongErrorOutputIsKeptShort(t *testing.T) {
	_, err := Runner{Path: fake(t, `printf '%.0sx' $(seq 1 6000) >&2; exit 1`), AcceptLicense: true}.Run(context.Background())
	if err == nil || len(err.Error()) > 400 {
		t.Errorf("len=%d", len(err.Error()))
	}
}
