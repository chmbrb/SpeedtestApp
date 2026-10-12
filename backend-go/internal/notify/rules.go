package notify

import (
	"fmt"
	"math"
	"sort"
	"time"
)

// Entity is one thing whose speed is judged: a device on one kind of connection, tested with one provider.
type Entity struct {
	Device         string
	ConnectionType string
	Provider       string
}

func (e Entity) Subject() string { return e.Device + "|" + e.ConnectionType + "|" + e.Provider }

// Label is how a person would say it: "server (wired)", or "phone (wireless, NDT7)".
func (e Entity) Label() string {
	if e.Provider == "ookla" || e.Provider == "" {
		return fmt.Sprintf("%s (%s)", e.Device, e.ConnectionType)
	}
	return fmt.Sprintf("%s (%s, %s)", e.Device, e.ConnectionType, providerName(e.Provider))
}

func providerName(p string) string {
	switch p {
	case "ookla":
		return "Ookla"
	case "ndt7":
		return "NDT7"
	}
	return p
}

// Median of a slice (not modified). Zero for an empty slice.
func Median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if n := len(s); n%2 == 1 {
		return s[n/2]
	} else {
		return (s[n/2-1] + s[n/2]) / 2
	}
}

// SilenceThreshold is how long a device may stay quiet before that is worth a notification: three times its usual
// gap between tests, but never less than min. A server that tests every 2 hours is silent after 6; a laptop that
// tests every 8 hours after 24, with no per-device setting.
func SilenceThreshold(gaps []time.Duration, min time.Duration) time.Duration {
	if len(gaps) == 0 {
		return min
	}
	f := make([]float64, len(gaps))
	for i, g := range gaps {
		f[i] = float64(g)
	}
	t := time.Duration(3 * Median(f))
	if t < min {
		return min
	}
	return t
}

// Text is a notification as the dashboard and ntfy both show it.
type Text struct {
	Title    string
	Detail   string
	Severity string // warning | critical
}

func mbps(v float64) string {
	switch {
	case v >= 100:
		return fmt.Sprintf("%.0f", v)
	case v >= 10:
		return fmt.Sprintf("%.1f", v)
	}
	return fmt.Sprintf("%.2f", v)
}

func plural(n int, unit string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", unit)
	}
	return fmt.Sprintf("%d %ss", n, unit)
}

// SlowText describes a download that is below the device's typical speed.
func SlowText(e Entity, value, typical float64, streak int) Text {
	below := int((1-value/typical)*100 + 0.5)
	sev := "warning"
	if value/typical < 0.5 {
		sev = "critical"
	}
	return Text{
		Title: "Slow download: " + e.Label(),
		Detail: fmt.Sprintf("%s Mbps, %d%% below your typical %s Mbps (%s in a row).",
			mbps(value), below, mbps(typical), plural(streak, "test")),
		Severity: sev,
	}
}

// LossText describes packet loss above the threshold.
func LossText(e Entity, value float64, streak int) Text {
	sev := "warning"
	if value >= 20 {
		sev = "critical"
	}
	return Text{
		Title:    "Packet loss: " + e.Label(),
		Detail:   fmt.Sprintf("%.1f%% of packets lost (%s in a row).", value, plural(streak, "test")),
		Severity: sev,
	}
}

// SilenceText describes a device that has stopped reporting.
func SilenceText(device string, last, now time.Time, usualGap time.Duration) Text {
	return Text{
		Title: "No tests from " + device,
		Detail: fmt.Sprintf("The last test was %s ago; one is usually due every %s.",
			Humanize(now.Sub(last)), Humanize(usualGap)),
		Severity: "warning",
	}
}

// Humanize turns a duration into "3 hours" / "25 minutes" / "2 days" (coarse on purpose).
func Humanize(d time.Duration) string {
	switch {
	case d < time.Minute:
		return "under a minute"
	case d < time.Hour:
		return plural(int(d.Minutes()+0.5), "minute")
	case d < 48*time.Hour:
		h := math.Round(d.Hours()*10) / 10 // one decimal; a whole number reads better without ".0"
		if h == math.Trunc(h) || h >= 10 {
			return plural(int(h+0.5), "hour")
		}
		return fmt.Sprintf("%.1f hours", h)
	}
	return plural(int(d.Hours()/24+0.5), "day")
}

// Priority maps severity to ntfy priority: warnings use the default, critical ones are high, recoveries are quiet.
func Priority(severity string) int {
	if severity == "critical" {
		return 4
	}
	return 3
}

const PriorityRecovery = 2

// Tags are ntfy tags (emoji short codes show as icons on the notification).
func Tags(kind, severity string) []string {
	icon := "warning"
	if severity == "critical" {
		icon = "rotating_light"
	}
	return []string{icon, kind}
}
