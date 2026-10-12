// speedtest-collector runs speed tests on this device and sends the results to a SpeedtestApp.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/chmbrb/SpeedtestApp/collector-go/internal/apiclient"
	"github.com/chmbrb/SpeedtestApp/collector-go/internal/collector"
	"github.com/chmbrb/SpeedtestApp/collector-go/internal/config"
	"github.com/chmbrb/SpeedtestApp/collector-go/internal/lock"
	"github.com/chmbrb/SpeedtestApp/collector-go/internal/ndt7"
	"github.com/chmbrb/SpeedtestApp/collector-go/internal/ookla"
	"github.com/chmbrb/SpeedtestApp/collector-go/internal/spool"
	"github.com/chmbrb/SpeedtestApp/collector-go/internal/version"
)

// Exit codes, chosen so cron and scripts can tell what happened.
const (
	exitOK      = 0
	exitFailed  = 1 // a test failed, or a result was refused
	exitUsage   = 2 // wrong settings
	exitBusy    = 3 // another test is already running on this device
	exitAuth    = 4 // the API refused the token
	exitSetup   = 1
	locateCheck = "https://locate.measurementlab.net/v2/nearest/ndt/ndt7"
)

const usage = `speedtest-collector %s: run speed tests on this device and send the results to a SpeedtestApp.

Usage:
  speedtest-collector <command> [flags]

Commands:
  run      run the tests once, send the results (cron-friendly)
  daemon   keep running: test every --interval (plus a little random spread), no cron needed
  flush    send results that are waiting because the API could not be reached
  check    check the settings: API address, token, the test tools
  version  print the version

Settings (flags, or the environment variables in brackets):
  --api-url URL           the SpeedtestApp, e.g. http://192.168.0.7:8080      [SPEEDTEST_API_URL]
  --connection TYPE       wired or wireless: how THIS device is connected     [SPEEDTEST_CONNECTION]
  --token-file FILE       file holding this device's API token                [SPEEDTEST_API_TOKEN holds the token itself]
  --provider LIST         ookla, ndt7, or ookla,ndt7 (default ookla)           [SPEEDTEST_PROVIDER]
  --accept-ookla-license  you have read and accept Ookla's terms (%s)         [OOKLA_ACCEPT_LICENSE=yes]
  --accept-mlab-policy    you have read and accept M-Lab's data policy (%s)   [MLAB_ACCEPT_POLICY=yes]
  --ookla-path PATH       the Ookla command-line tool (default: speedtest)     [OOKLA_PATH]
  --interval D            daemon: time between rounds, 5m to 7d (default 2h)  [SPEEDTEST_INTERVAL]
  --jitter D              daemon: random spread (default a tenth of the interval)
  --timeout D             longest one test may take (default 3m)
  --trigger WHY           scheduled (default) or on_demand
  --spool-dir DIR         where waiting results are kept
  --log-json, -v          JSON log lines, more detail

The token is never a flag (it would show in process lists): use SPEEDTEST_API_TOKEN or --token-file.
Exit codes: 0 ok, 1 a test failed or a result was refused, 2 wrong settings, 3 another test is running, 4 token refused.
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprintf(stdout, usage, version.Version, config.OoklaTerms, config.MLabPolicy)
		if len(args) == 0 {
			return exitUsage
		}
		return exitOK
	}
	cmd := args[0]
	if cmd == "version" {
		fmt.Fprintln(stdout, version.Version)
		return exitOK
	}
	if cmd != "run" && cmd != "daemon" && cmd != "flush" && cmd != "check" {
		fmt.Fprintf(stderr, "unknown command %q; run `speedtest-collector help`\n", cmd)
		return exitUsage
	}

	cfg, err := config.Parse(cmd, args[1:], os.Getenv, os.ReadFile)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(stdout, usage, version.Version, config.OoklaTerms, config.MLabPolicy)
			return exitOK
		}
		fmt.Fprintf(stderr, "speedtest-collector %s: %v\n\nRun `speedtest-collector help` for the settings.\n", cmd, err)
		return exitUsage
	}

	level := slog.LevelInfo
	if cfg.Verbose {
		level = slog.LevelDebug
	}
	opts := &slog.HandlerOptions{Level: level}
	var h slog.Handler = slog.NewTextHandler(stderr, opts)
	if cfg.JSONLogs {
		h = slog.NewJSONHandler(stderr, opts)
	}
	log := slog.New(h)
	if config.InsecureTransport(cfg.APIURL) {
		log.Warn("the API token is sent over plain HTTP to an address outside your private network; consider HTTPS", "api", cfg.APIURL)
	}

	sp, err := spool.Open(cfg.SpoolDir, 0, 0)
	if err != nil {
		fmt.Fprintf(stderr, "speedtest-collector: %v\n", err)
		return exitSetup
	}
	api := apiclient.New(cfg.APIURL, cfg.Token)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cmd == "check" {
		return check(ctx, cfg, api, sp, stdout)
	}

	col := &collector.Collector{
		Connection: cfg.Connection, Trigger: cfg.Trigger, API: api, Spool: sp, Log: log, Timeout: cfg.Timeout,
		Providers: providers(cfg),
	}
	switch cmd {
	case "flush":
		r := col.Flush(ctx)
		fmt.Fprintf(stdout, "sent %d, rejected %d, expired %d, still waiting %d\n", r.Sent, r.Rejected, r.Expired, r.Remaining)
		switch {
		case apiclient.IsKind(r.Stopped, apiclient.KindAuth):
			return exitAuth
		case r.Stopped != nil || r.Rejected > 0:
			return exitFailed
		}
		return exitOK
	case "daemon":
		log.Info("daemon started", "version", version.Version, "providers", cfg.Providers, "every", cfg.Interval.String(), "api", cfg.APIURL)
		collector.Daemon(ctx, col, collector.Schedule{Interval: cfg.Interval, Jitter: cfg.Jitter}, cfg.RunAtStart, collector.RealSleep, log)
		log.Info("daemon stopped")
		return exitOK
	}

	// run
	outcomes, err := col.RunOnce(ctx)
	if errors.Is(err, lock.ErrBusy) {
		fmt.Fprintln(stderr, "speedtest-collector: "+err.Error())
		return exitBusy
	}
	if err != nil {
		fmt.Fprintf(stderr, "speedtest-collector: %v\n", err)
		return exitFailed
	}
	code := exitOK
	for _, o := range outcomes {
		switch {
		case apiclient.IsKind(o.SendErr, apiclient.KindAuth):
			code = exitAuth // the result is queued and safe, but cron should be told the token needs fixing
		case o.Err != nil && code == exitOK:
			code = exitFailed
		}
	}
	return code
}

// providers builds the tests named in the settings, in order.
func providers(cfg config.Config) []collector.Provider {
	var out []collector.Provider
	for _, name := range cfg.Providers {
		switch name {
		case "ookla":
			out = append(out, runner{"ookla", ookla.Runner{Path: cfg.OoklaPath, AcceptLicense: cfg.OoklaAccepted}.Run})
		case "ndt7":
			out = append(out, runner{"ndt7", ndt7.Runner{}.Run})
		}
	}
	return out
}

type runner struct {
	name string
	run  func(context.Context) (json.RawMessage, error)
}

func (r runner) Name() string                                     { return r.name }
func (r runner) Run(ctx context.Context) (json.RawMessage, error) { return r.run(ctx) }

// check says, in plain words, what works and what does not.
func check(ctx context.Context, cfg config.Config, api *apiclient.Client, sp *spool.Spool, out io.Writer) int {
	problems := 0
	say := func(ok bool, format string, a ...any) {
		mark := "ok   "
		if !ok {
			mark, problems = "FAIL ", problems+1
		}
		fmt.Fprintf(out, "%s %s\n", mark, fmt.Sprintf(format, a...))
	}
	fmt.Fprintf(out, "speedtest-collector %s, device is %s\n", version.Version, cfg.Connection)

	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	rep := api.Check(cctx)
	if rep.Reachable {
		say(true, "the API answers at %s", cfg.APIURL)
	} else {
		say(false, "%s", rep.Problem)
	}
	if rep.Reachable {
		if rep.TokenOK {
			say(true, "the token is accepted")
		} else {
			say(false, "%s", rep.Problem)
		}
	}

	for _, p := range cfg.Providers {
		switch p {
		case "ookla":
			v, err := ookla.Runner{Path: cfg.OoklaPath}.Version(cctx)
			switch {
			case err != nil:
				say(false, "Ookla tool: %v", err)
			case !cfg.OoklaAccepted:
				// Not a failure: the tool remembers an acceptance given when it was run by hand, and the collector cannot tell
				// without running a test.
				fmt.Fprintf(out, "note Ookla tool found (%s). Its terms are not confirmed by this collector: if you have not accepted them by running the tool yourself,\n"+
					"     read %s and add --accept-ookla-license.\n", v, config.OoklaTerms)
			default:
				say(true, "Ookla tool found (%s); terms accepted by you", v)
			}
		case "ndt7":
			req, _ := http.NewRequestWithContext(cctx, http.MethodGet, locateCheck, nil)
			req.Header.Set("User-Agent", version.UserAgent())
			resp, err := http.DefaultClient.Do(req)
			if err == nil {
				resp.Body.Close()
			}
			if err != nil || resp.StatusCode != http.StatusOK {
				say(false, "M-Lab's locate service is not reachable (is this device online?)")
			} else {
				say(true, "M-Lab's locate service is reachable; its data policy accepted by you")
			}
		}
	}
	n := sp.Len()
	fmt.Fprintf(out, "     %d result(s) waiting in %s\n", n, sp.Dir())
	if problems > 0 {
		fmt.Fprintf(out, "%d problem(s) found.\n", problems)
		return exitFailed
	}
	fmt.Fprintln(out, "Everything looks fine.")
	return exitOK
}
