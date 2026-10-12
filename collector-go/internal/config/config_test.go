package config

import (
	"errors"
	"flag"
	"os"
	"strings"
	"testing"
	"time"
)

func env(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

func noFile(string) ([]byte, error) { return nil, os.ErrNotExist }

var valid = map[string]string{"SPEEDTEST_API_URL": "http://192.168.0.7:8080", "SPEEDTEST_API_TOKEN": "tok123", "SPEEDTEST_CONNECTION": "wired"}

func TestDefaultsFromTheEnvironment(t *testing.T) {
	c, err := Parse("run", nil, env(valid), noFile)
	if err != nil {
		t.Fatal(err)
	}
	if c.APIURL != "http://192.168.0.7:8080" || c.Token != "tok123" || c.Connection != "wired" || len(c.Providers) != 1 || c.Providers[0] != "ookla" ||
		c.Trigger != "scheduled" || c.OoklaPath != "speedtest" || c.OoklaAccepted || c.MLabAccepted || c.Interval != 2*time.Hour ||
		c.Jitter != 10*time.Minute || c.Timeout != 3*time.Minute || !c.RunAtStart || c.SpoolDir == "" {
		t.Errorf("%+v", c)
	}
}

func TestFlagsWinOverTheEnvironment(t *testing.T) {
	e := map[string]string{}
	for k, v := range valid {
		e[k] = v
	}
	e["SPEEDTEST_PROVIDER"] = "ndt7"
	e["MLAB_ACCEPT_POLICY"] = "yes"
	c, err := Parse("daemon", []string{"--api-url", "https://speed.example.org/", "--connection", "wireless", "--provider", "ookla, ndt7,ookla",
		"--accept-ookla-license", "--ookla-path", "/opt/speedtest", "--interval", "30m", "--jitter", "2m", "--timeout", "90s",
		"--trigger", "on_demand", "--spool-dir", "/tmp/q", "--run-at-start=false", "--log-json", "-v"}, env(e), noFile)
	if err != nil {
		t.Fatal(err)
	}
	if c.APIURL != "https://speed.example.org" || c.Connection != "wireless" || strings.Join(c.Providers, ",") != "ookla,ndt7" || !c.OoklaAccepted ||
		!c.MLabAccepted || c.OoklaPath != "/opt/speedtest" || c.Interval != 30*time.Minute || c.Jitter != 2*time.Minute || c.Timeout != 90*time.Second ||
		c.Trigger != "on_demand" || c.SpoolDir != "/tmp/q" || c.RunAtStart || !c.JSONLogs || !c.Verbose {
		t.Errorf("%+v", c)
	}
}

func TestTheTokenComesOnlyFromTheEnvironmentOrAFile(t *testing.T) {
	e := map[string]string{"SPEEDTEST_API_URL": "http://h:1", "SPEEDTEST_CONNECTION": "wired"}
	if _, err := Parse("run", []string{"--token", "abc"}, env(e), noFile); err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Errorf("there is no --token flag, so it cannot leak into process lists: %v", err)
	}
	read := func(p string) ([]byte, error) {
		if p == "/etc/tok" {
			return []byte("  filetoken\n"), nil
		}
		return nil, os.ErrNotExist
	}
	c, err := Parse("run", []string{"--token-file", "/etc/tok"}, env(e), read)
	if err != nil || c.Token != "filetoken" {
		t.Errorf("token file is trimmed: %q %v", c.Token, err)
	}
	e["SPEEDTEST_API_TOKEN"] = "envtoken"
	if c, _ := Parse("run", []string{"--token-file", "/etc/tok"}, env(e), read); c.Token != "filetoken" {
		t.Errorf("an explicit file beats the environment: %q", c.Token)
	}
	if _, err := Parse("run", []string{"--token-file", "/missing"}, env(e), read); err == nil || !strings.Contains(err.Error(), "cannot read --token-file") {
		t.Errorf("%v", err)
	}
	e["SPEEDTEST_API_TOKEN"] = "has space"
	if _, err := Parse("run", nil, env(e), noFile); err == nil || !strings.Contains(err.Error(), "spaces") {
		t.Errorf("%v", err)
	}
}

func TestEveryProblemIsReportedAtOnce(t *testing.T) {
	_, err := Parse("run", []string{"--provider", "ndt7,speedof", "--interval", "1s", "--jitter", "10h", "--timeout", "1s", "--trigger", "weekly"}, env(nil), noFile)
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"--api-url", "token", "--connection", `"speedof"`, "M-Lab", "--interval", "--timeout", "--trigger"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in:\n%v", want, err)
		}
	}
}

func TestAcceptanceIsExplicitForBothProviders(t *testing.T) {
	// ndt7 refuses to run without the M-Lab acceptance, and says what that means
	_, err := Parse("run", []string{"--provider", "ndt7"}, env(valid), noFile)
	if err == nil || !strings.Contains(err.Error(), "publishes results as open data") || !strings.Contains(err.Error(), MLabPolicy) {
		t.Errorf("%v", err)
	}
	if _, err := Parse("run", []string{"--provider", "ndt7", "--accept-mlab-policy"}, env(valid), noFile); err != nil {
		t.Errorf("%v", err)
	}
	// only an exact "yes" in the environment counts
	e := map[string]string{"MLAB_ACCEPT_POLICY": "true"}
	for k, v := range valid {
		e[k] = v
	}
	if _, err := Parse("run", []string{"--provider", "ndt7"}, env(e), noFile); err == nil {
		t.Error("MLAB_ACCEPT_POLICY=true must not count")
	}
	// ookla's acceptance is recorded, never assumed
	c, _ := Parse("run", nil, env(valid), noFile)
	if c.OoklaAccepted {
		t.Error("Ookla's terms are not accepted unless the person says so")
	}
	e["OOKLA_ACCEPT_LICENSE"] = "yes"
	if c, _ := Parse("run", nil, env(e), noFile); !c.OoklaAccepted {
		t.Error("OOKLA_ACCEPT_LICENSE=yes")
	}
}

func TestValidationBounds(t *testing.T) {
	for name, args := range map[string][]string{
		"interval too short":  {"--interval", "4m59s"},
		"interval too long":   {"--interval", "169h"},
		"interval not a time": {"--interval", "often"},
		"jitter too big":      {"--interval", "1h", "--jitter", "31m"},
		"timeout too short":   {"--timeout", "29s"},
		"timeout too long":    {"--timeout", "31m"},
		"bad connection":      {"--connection", "lte"},
		"bad url":             {"--api-url", "192.168.0.7:8080"},
		"url with query":      {"--api-url", "http://h:1/?x=1"},
		"stray argument":      {"extra"},
	} {
		if _, err := Parse("run", args, env(valid), noFile); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	for name, args := range map[string][]string{
		"interval at the minimum": {"--interval", "5m"},
		"zero jitter":             {"--interval", "1h", "--jitter", "0"},
		"jitter at the maximum":   {"--interval", "1h", "--jitter", "30m"},
		"wireless":                {"--connection", "wireless"},
	} {
		if _, err := Parse("run", args, env(valid), noFile); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	if c, _ := Parse("run", []string{"--interval", "30m"}, env(valid), noFile); c.Jitter != 3*time.Minute {
		t.Errorf("default jitter is a tenth of the interval: %v", c.Jitter)
	}
	if c, _ := Parse("run", []string{"--interval", "24h"}, env(valid), noFile); c.Jitter != 10*time.Minute {
		t.Errorf("but at most ten minutes: %v", c.Jitter)
	}
}

func TestHelpIsNotAnError(t *testing.T) {
	if _, err := Parse("run", []string{"-h"}, env(valid), noFile); !errors.Is(err, flag.ErrHelp) {
		t.Errorf("%v", err)
	}
}

func TestInsecureTransport(t *testing.T) {
	for url, want := range map[string]bool{
		"https://speed.example.org": false, "http://192.168.0.7:8080": false, "http://10.0.0.5": false, "http://localhost:8080": false,
		"http://127.0.0.1:8080": false, "http://mybox": false, "http://mybox.local:8080": false, "http://[::1]:8080": false,
		"http://203.0.113.9:8080": true, "http://speed.example.org": true, "http://8.8.8.8": true, "not a url": false,
	} {
		if got := InsecureTransport(url); got != want {
			t.Errorf("InsecureTransport(%q) = %v, want %v", url, got, want)
		}
	}
}
