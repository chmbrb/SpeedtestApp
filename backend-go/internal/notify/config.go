// Package notify watches the speed-test results and tells a person about problems through ntfy (https://ntfy.sh).
package notify

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Config is read from NTFY_* environment variables. Notifications are off unless NTFY_TOPIC is set.
type Config struct {
	Enabled   bool
	ServerURL string // base URL of the ntfy server, no trailing slash
	Topic     string
	Token     string // access token (Bearer); optional
	User      string // or basic auth; optional
	Password  string
	AppURL    string // opened when the notification is tapped; optional

	SlowRatio       float64       // a download below this fraction of the device's typical speed is "slow"
	RecoverRatio    float64       // back to at least this fraction ends the alert (hysteresis)
	SlowConsecutive int           // slow tests in a row before alerting (NTFY_OUTLIERS unless NTFY_SLOW_CONSECUTIVE is set)
	LossPct         float64       // packet loss at or above this percent is "lossy"
	LossConsecutive int           // lossy tests in a row before alerting (NTFY_OUTLIERS unless NTFY_LOSS_CONSECUTIVE is set)
	SilenceMin      time.Duration // never call a device silent sooner than this
	RemindEvery     time.Duration // 0 = no reminders
	BaselineDays    int           // history used for "typical"
	MinSamples      int           // tests needed before a device's speed is judged
	CheckEvery      time.Duration
}

// DefaultOutliers is how many of a device's scheduled tests in a row must be outliers before anyone is told.
const DefaultOutliers = 1

// Defaults. Only scheduled tests are judged, each against its own device's history, so one outlier is enough by default;
// raise NTFY_OUTLIERS if isolated slow tests turn out to be noise.
func Defaults() Config {
	return Config{
		ServerURL:       "https://ntfy.sh",
		SlowRatio:       0.7,
		RecoverRatio:    0.9,
		SlowConsecutive: DefaultOutliers,
		LossPct:         5,
		LossConsecutive: DefaultOutliers,
		SilenceMin:      3 * time.Hour,
		RemindEvery:     12 * time.Hour,
		BaselineDays:    30,
		MinSamples:      10,
		CheckEvery:      time.Minute,
	}
}

var topicRe = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)

// FromEnv builds the configuration from environment variables via getenv. Invalid values are errors, not silently
// ignored: a notification setup that quietly does nothing is worse than one that refuses to start.
func FromEnv(getenv func(string) string) (Config, error) {
	c := Defaults()
	c.Topic = strings.TrimSpace(getenv("NTFY_TOPIC"))
	if c.Topic == "" {
		return c, nil // disabled
	}
	c.Enabled = true
	var errs []error
	bad := func(name, why string) { errs = append(errs, fmt.Errorf("%s %s", name, why)) }

	if !topicRe.MatchString(c.Topic) {
		bad("NTFY_TOPIC", "must be 1-64 letters, digits, - or _")
	}
	if v := strings.TrimSpace(getenv("NTFY_URL")); v != "" {
		c.ServerURL = strings.TrimRight(v, "/")
	}
	if u, err := url.Parse(c.ServerURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		bad("NTFY_URL", "must be an http(s) URL such as https://ntfy.sh")
	}
	c.Token, c.User, c.Password = getenv("NTFY_TOKEN"), getenv("NTFY_USER"), getenv("NTFY_PASSWORD")
	if c.Token != "" && (c.User != "" || c.Password != "") {
		bad("NTFY_TOKEN", "cannot be combined with NTFY_USER/NTFY_PASSWORD; use one or the other")
	}
	if (c.User == "") != (c.Password == "") {
		bad("NTFY_USER", "and NTFY_PASSWORD must be set together")
	}
	if v := strings.TrimSpace(getenv("APP_URL")); v != "" {
		if u, err := url.Parse(v); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			bad("APP_URL", "must be an http(s) URL")
		}
		c.AppURL = v
	}

	num := func(name string, dst *float64, ok func(float64) bool, why string) {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || !ok(f) {
				bad(name, why)
				return
			}
			*dst = f
		}
	}
	integer := func(name string, dst *int, min int, why string) {
		if v := strings.TrimSpace(getenv(name)); v != "" {
			n, err := strconv.Atoi(v)
			if err != nil || n < min {
				bad(name, why)
				return
			}
			*dst = n
		}
	}
	hours := func(name string, dst *time.Duration, allowZero bool, why string) {
		var h float64
		if v := strings.TrimSpace(getenv(name)); v != "" {
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f < 0 || (f == 0 && !allowZero) {
				bad(name, why)
				return
			}
			h = f
			*dst = time.Duration(h * float64(time.Hour))
		}
	}

	num("NTFY_SLOW_RATIO", &c.SlowRatio, func(f float64) bool { return f > 0 && f < 1 }, "must be between 0 and 1 (exclusive), e.g. 0.7")
	num("NTFY_RECOVER_RATIO", &c.RecoverRatio, func(f float64) bool { return f > 0 && f <= 1 }, "must be above 0 and at most 1, e.g. 0.9")
	num("NTFY_LOSS_PCT", &c.LossPct, func(f float64) bool { return f > 0 && f <= 100 }, "must be a percentage above 0 and at most 100")
	// NTFY_OUTLIERS sets both streaks; NTFY_SLOW_CONSECUTIVE and NTFY_LOSS_CONSECUTIVE override it for one kind
	integer("NTFY_OUTLIERS", &c.SlowConsecutive, 1, "must be a whole number, 1 or more")
	c.LossConsecutive = c.SlowConsecutive
	integer("NTFY_SLOW_CONSECUTIVE", &c.SlowConsecutive, 1, "must be a whole number, 1 or more")
	integer("NTFY_LOSS_CONSECUTIVE", &c.LossConsecutive, 1, "must be a whole number, 1 or more")
	integer("NTFY_BASELINE_DAYS", &c.BaselineDays, 1, "must be a whole number of days, 1 or more")
	integer("NTFY_MIN_SAMPLES", &c.MinSamples, 3, "must be a whole number, 3 or more")
	hours("NTFY_SILENCE_MIN_HOURS", &c.SilenceMin, false, "must be a number of hours above 0")
	hours("NTFY_REMIND_HOURS", &c.RemindEvery, true, "must be a number of hours (0 turns reminders off)")
	if v := strings.TrimSpace(getenv("NTFY_CHECK_SECONDS")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 10 {
			bad("NTFY_CHECK_SECONDS", "must be a whole number of seconds, 10 or more")
		} else {
			c.CheckEvery = time.Duration(n) * time.Second
		}
	}
	if c.RecoverRatio <= c.SlowRatio {
		bad("NTFY_RECOVER_RATIO", "must be higher than NTFY_SLOW_RATIO, or alerts would flap")
	}
	return c, errors.Join(errs...)
}

// Host is the ntfy server's host, safe to show in the UI. The topic and credentials are never exposed.
func (c Config) Host() string {
	u, err := url.Parse(c.ServerURL)
	if err != nil {
		return ""
	}
	return u.Host
}
