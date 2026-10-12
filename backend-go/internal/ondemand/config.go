// Package ondemand runs tests when a person asks for one: Ookla on the server with live progress, and NDT7 in the
// visitor's browser with a single-use token to submit the result.
package ondemand

import (
	"errors"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Config comes from environment variables.
type Config struct {
	DeviceName       string        // name the server's own tests are stored under (DEVICE_NAME, default "server")
	ServerConnection string        // how the server is connected (SERVER_CONNECTION_TYPE, default "wired")
	OoklaPath        string        // the Ookla CLI (OOKLA_PATH, default "speedtest")
	OoklaAccepted    bool          // the person running the server has accepted Ookla's terms (OOKLA_ACCEPT_LICENSE=yes)
	TestTimeout      time.Duration // longest a server test may run (ONDEMAND_TIMEOUT_SECONDS, default 180)
	BrowserTTL       time.Duration // how long a browser test may stay open (BROWSER_TEST_TTL_MINUTES, default 15)
	TrustProxy       bool          // read the client address from X-Forwarded-For (TRUST_PROXY=1), only behind your own proxy
	CreatePerMinute  int           // tests one address may start per minute (ONDEMAND_PER_MINUTE, default 6)
}

func Defaults() Config {
	return Config{
		DeviceName: "server", ServerConnection: "wired", OoklaPath: "speedtest",
		TestTimeout: 3 * time.Minute, BrowserTTL: 15 * time.Minute, CreatePerMinute: 6,
	}
}

// OoklaTermsURL is where the person running the server reads what they are accepting.
const OoklaTermsURL = "https://www.speedtest.net/about/eula"

func FromEnv(getenv func(string) string) (Config, error) {
	c := Defaults()
	var errs []error
	bad := func(name, why string) { errs = append(errs, fmt.Errorf("%s %s", name, why)) }

	if v := strings.TrimSpace(getenv("DEVICE_NAME")); v != "" {
		if len(v) > 64 || !utf8.ValidString(v) || strings.ContainsFunc(v, unicode.IsControl) {
			bad("DEVICE_NAME", "must be at most 64 characters of plain text")
		}
		c.DeviceName = v
	}
	if v := strings.TrimSpace(getenv("SERVER_CONNECTION_TYPE")); v != "" {
		if v != "wired" && v != "wireless" {
			bad("SERVER_CONNECTION_TYPE", "must be wired or wireless")
		}
		c.ServerConnection = v
	}
	if v := strings.TrimSpace(getenv("OOKLA_PATH")); v != "" {
		c.OoklaPath = v
	}
	switch v := strings.TrimSpace(getenv("OOKLA_ACCEPT_LICENSE")); v {
	case "":
	case "yes":
		c.OoklaAccepted = true
	default:
		bad("OOKLA_ACCEPT_LICENSE", "must be exactly yes (accepting is your decision, so nothing else counts)")
	}
	if v := strings.TrimSpace(getenv("ONDEMAND_TIMEOUT_SECONDS")); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 30 || n > 1800 {
			bad("ONDEMAND_TIMEOUT_SECONDS", "must be a whole number from 30 to 1800")
		} else {
			c.TestTimeout = time.Duration(n) * time.Second
		}
	}
	if v := strings.TrimSpace(getenv("BROWSER_TEST_TTL_MINUTES")); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 || n > 120 {
			bad("BROWSER_TEST_TTL_MINUTES", "must be a whole number from 1 to 120")
		} else {
			c.BrowserTTL = time.Duration(n) * time.Minute
		}
	}
	if v := strings.TrimSpace(getenv("ONDEMAND_PER_MINUTE")); v != "" {
		if n, err := strconv.Atoi(v); err != nil || n < 1 {
			bad("ONDEMAND_PER_MINUTE", "must be a whole number, 1 or more")
		} else {
			c.CreatePerMinute = n
		}
	}
	switch strings.ToLower(strings.TrimSpace(getenv("TRUST_PROXY"))) {
	case "", "0", "false", "no":
	case "1", "true", "yes":
		c.TrustProxy = true
	default:
		bad("TRUST_PROXY", "must be 1 or 0")
	}
	return c, errors.Join(errs...)
}

// OoklaStatus says whether Ookla tests can run, and if not, why in words a person can act on.
func (c Config) OoklaStatus(lookPath func(string) (string, error)) (bool, string) {
	if !c.OoklaAccepted {
		return false, "Ookla’s terms have not been accepted on this server. Read them at " + OoklaTermsURL +
			" and, if you agree, set OOKLA_ACCEPT_LICENSE=yes and restart."
	}
	if lookPath == nil {
		lookPath = exec.LookPath
	}
	if _, err := lookPath(c.OoklaPath); err != nil {
		return false, "The Ookla command-line tool was not found (looked for “" + c.OoklaPath + "”). Install it, or set OOKLA_PATH."
	}
	return true, ""
}
