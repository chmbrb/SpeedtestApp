// Package config reads the collector's settings from flags and environment variables.
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const (
	MinInterval = 5 * time.Minute // never hammer Ookla's or M-Lab's servers
	MaxInterval = 7 * 24 * time.Hour
	MLabPolicy  = "https://www.measurementlab.net/privacy/"
	OoklaTerms  = "https://www.speedtest.net/about/eula"
)

type Config struct {
	APIURL     string
	Token      string
	Connection string   // wired | wireless: stated by the person, never guessed
	Providers  []string // ookla and/or ndt7, in the order they run
	Trigger    string   // scheduled | on_demand

	OoklaPath     string
	OoklaAccepted bool // the person accepted Ookla's terms (passes --accept-license to the tool)
	MLabAccepted  bool // the person accepted M-Lab's data policy (required for ndt7)

	Interval   time.Duration // daemon: time between rounds
	Jitter     time.Duration // daemon: random spread, so many devices do not test at the same moment
	Timeout    time.Duration // longest one test may take
	RunAtStart bool          // daemon: test as soon as it starts, not after the first interval

	SpoolDir string
	JSONLogs bool
	Verbose  bool
}

// Defaults are applied before flags and environment variables.
func Defaults() Config {
	return Config{
		Providers: []string{"ookla"}, Trigger: "scheduled", OoklaPath: "speedtest",
		Interval: 2 * time.Hour, Timeout: 3 * time.Minute, RunAtStart: true,
	}
}

// Parse reads a command's flags. Flags win over environment variables, which win over defaults. getenv and readFile are
// injected so tests need no real environment. The token can come only from the environment or a file, never a flag: a
// flag would show up in `ps` output and shell history.
func Parse(command string, args []string, getenv func(string) string, readFile func(string) ([]byte, error)) (Config, error) {
	c := Defaults()
	fs := flag.NewFlagSet(command, flag.ContinueOnError)
	fs.SetOutput(io.Discard)

	var providers, tokenFile, interval, jitter string
	apiURL := fs.String("api-url", getenv("SPEEDTEST_API_URL"), "base URL of the SpeedtestApp (env SPEEDTEST_API_URL)")
	fs.StringVar(&tokenFile, "token-file", "", "file holding the device's API token (or set SPEEDTEST_API_TOKEN)")
	connection := fs.String("connection", getenv("SPEEDTEST_CONNECTION"), "how this device is connected: wired or wireless")
	fs.StringVar(&providers, "provider", orDefault(getenv("SPEEDTEST_PROVIDER"), "ookla"), "tests to run, comma separated: ookla, ndt7")
	fs.StringVar(&c.Trigger, "trigger", c.Trigger, "what to record as the cause: scheduled or on_demand")
	fs.StringVar(&c.OoklaPath, "ookla-path", orDefault(getenv("OOKLA_PATH"), c.OoklaPath), "the Ookla speedtest command")
	acceptOokla := fs.Bool("accept-ookla-license", getenv("OOKLA_ACCEPT_LICENSE") == "yes", "I have read and accept Ookla's terms")
	acceptMLab := fs.Bool("accept-mlab-policy", getenv("MLAB_ACCEPT_POLICY") == "yes", "I have read and accept M-Lab's data policy (needed for ndt7)")
	fs.StringVar(&interval, "interval", orDefault(getenv("SPEEDTEST_INTERVAL"), c.Interval.String()), "daemon: time between rounds")
	fs.StringVar(&jitter, "jitter", getenv("SPEEDTEST_JITTER"), "daemon: random spread added to each interval (default a tenth of it, at most 10m)")
	fs.DurationVar(&c.Timeout, "timeout", c.Timeout, "longest one test may take")
	fs.BoolVar(&c.RunAtStart, "run-at-start", c.RunAtStart, "daemon: test as soon as it starts")
	fs.StringVar(&c.SpoolDir, "spool-dir", getenv("SPEEDTEST_SPOOL_DIR"), "where results wait when the API cannot be reached")
	fs.BoolVar(&c.JSONLogs, "log-json", false, "log as JSON lines")
	fs.BoolVar(&c.Verbose, "v", false, "more detail in the log")
	if err := fs.Parse(args); err != nil {
		return c, err
	}
	if fs.NArg() > 0 {
		return c, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	var errs []error
	bad := func(format string, a ...any) { errs = append(errs, fmt.Errorf(format, a...)) }

	c.APIURL = strings.TrimRight(strings.TrimSpace(*apiURL), "/")
	if c.APIURL == "" {
		bad("--api-url (or SPEEDTEST_API_URL) is required, for example http://192.168.0.7:8080")
	} else if u, err := url.Parse(c.APIURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.RawQuery != "" {
		bad("--api-url must be an http or https address such as http://192.168.0.7:8080")
	}

	switch {
	case tokenFile != "":
		b, err := readFile(tokenFile)
		if err != nil {
			bad("cannot read --token-file: %v", err)
		}
		c.Token = strings.TrimSpace(string(b))
	default:
		c.Token = strings.TrimSpace(getenv("SPEEDTEST_API_TOKEN"))
	}
	if c.Token == "" {
		bad("an API token is required: set SPEEDTEST_API_TOKEN or use --token-file")
	} else if strings.ContainsAny(c.Token, " \t\r\n") {
		bad("the API token must not contain spaces or line breaks")
	}

	c.Connection = strings.TrimSpace(*connection)
	if c.Connection != "wired" && c.Connection != "wireless" {
		bad("--connection must be wired or wireless (say how this device is connected; it is not guessed)")
	}

	seen := map[string]bool{}
	c.Providers = nil
	for _, p := range strings.Split(providers, ",") {
		if p = strings.TrimSpace(p); p == "" || seen[p] {
			continue
		}
		if p != "ookla" && p != "ndt7" {
			bad("--provider: %q is not one of ookla, ndt7", p)
			continue
		}
		seen[p] = true
		c.Providers = append(c.Providers, p)
	}
	if len(c.Providers) == 0 && len(errs) == 0 {
		bad("--provider needs at least one of ookla, ndt7")
	}
	if c.Trigger != "scheduled" && c.Trigger != "on_demand" {
		bad("--trigger must be scheduled or on_demand")
	}

	c.OoklaAccepted, c.MLabAccepted = *acceptOokla, *acceptMLab
	if seen["ndt7"] && !c.MLabAccepted {
		bad("the ndt7 test sends your IP address to M-Lab, which publishes results as open data. Read %s and, if you agree, add --accept-mlab-policy", MLabPolicy)
	}

	var err error
	if c.Interval, err = time.ParseDuration(interval); err != nil || c.Interval < MinInterval || c.Interval > MaxInterval {
		bad("--interval must be a duration from %s to %s, such as 2h", MinInterval, MaxInterval)
		c.Interval = Defaults().Interval
	}
	if jitter == "" {
		c.Jitter = min(c.Interval/10, 10*time.Minute)
	} else if c.Jitter, err = time.ParseDuration(jitter); err != nil || c.Jitter < 0 || c.Jitter > c.Interval/2 {
		bad("--jitter must be a duration from 0 up to half the interval")
	}
	if c.Timeout < 30*time.Second || c.Timeout > 30*time.Minute {
		bad("--timeout must be from 30s to 30m")
	}
	if c.SpoolDir == "" {
		dir, err := os.UserCacheDir()
		if err != nil {
			dir = os.TempDir()
		}
		c.SpoolDir = filepath.Join(dir, "speedtest-collector")
	}
	return c, errors.Join(errs...)
}

func orDefault(v, d string) string {
	if v == "" {
		return d
	}
	return v
}

// InsecureTransport reports whether the API address sends the token over plain HTTP to something that is not on a
// private network. That is allowed (home networks use plain HTTP) but worth a warning.
func InsecureTransport(apiURL string) bool {
	u, err := url.Parse(apiURL)
	if err != nil || u.Scheme != "http" {
		return false
	}
	host := u.Hostname()
	if host == "localhost" || strings.HasSuffix(host, ".local") || !strings.Contains(host, ".") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return !(ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
	}
	return true
}
