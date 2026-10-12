package api

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
	"unicode"
	"unicode/utf8"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/store"
)

var (
	providers       = []string{"ookla", "ndt7"}
	connectionTypes = []string{"wired", "wireless"}
	triggers        = []string{"scheduled", "on_demand"}
	intervals       = []string{"raw", "hour", "day", "week", "month", "quarter", "year"}
	groupFields     = []string{"provider", "device", "connectionType"}
)

const (
	maxDeviceFilters = 50
	maxDeviceLen     = 64
)

// errs collects validation problems so one response can report all of them.
type errs []FieldError

func (e *errs) add(field, format string, a ...any) {
	*e = append(*e, FieldError{Field: field, Message: fmt.Sprintf(format, a...)})
}

// list reads a comma-separated query parameter (also accepts it repeated). Empty items are an error.
func list(q url.Values, name string, e *errs) []string {
	var out []string
	for _, v := range q[name] {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				e.add(name, "must not contain empty values")
				return out
			}
			out = append(out, part)
		}
	}
	return out
}

func enumList(q url.Values, name string, allowed []string, e *errs) []string {
	vals := list(q, name, e)
	for _, v := range vals {
		if !slices.Contains(allowed, v) {
			e.add(name, "%q is not one of %s", v, strings.Join(allowed, ", "))
		}
	}
	return vals
}

func parseTime(q url.Values, name string, e *errs) *time.Time {
	if !q.Has(name) {
		return nil
	}
	t, err := time.Parse(time.RFC3339, q.Get(name))
	if err != nil {
		e.add(name, "must be an RFC 3339 date-time, e.g. 2026-06-01T00:00:00Z")
		return nil
	}
	t = t.UTC()
	return &t
}

func parseFilter(q url.Values, e *errs) store.Filter {
	f := store.Filter{
		From:            parseTime(q, "from", e),
		To:              parseTime(q, "to", e),
		Providers:       enumList(q, "provider", providers, e),
		ConnectionTypes: enumList(q, "connectionType", connectionTypes, e),
		Triggers:        enumList(q, "trigger", triggers, e),
		Devices:         list(q, "device", e),
	}
	if f.From != nil && f.To != nil && f.From.After(*f.To) {
		e.add("from", "must not be after `to`")
	}
	if len(f.Devices) > maxDeviceFilters {
		e.add("device", "at most %d devices", maxDeviceFilters)
	}
	for _, d := range f.Devices {
		if len(d) > maxDeviceLen {
			e.add("device", "names are at most %d characters", maxDeviceLen)
			break
		}
		if !cleanText(d) {
			e.add("device", "names must be valid UTF-8 without control characters")
			break
		}
	}
	return f
}

func parseLimit(q url.Values, def, max int, e *errs) int {
	if !q.Has("limit") {
		return def
	}
	n, err := strconv.Atoi(q.Get("limit"))
	if err != nil || n < 1 || n > max {
		e.add("limit", "must be an integer from 1 to %d", max)
		return def
	}
	return n
}

var cursorIDRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// Cursors are opaque to clients: base64url of {"t": time, "id": uuid}.
func encodeCursor(c *store.Cursor) *string {
	if c == nil {
		return nil
	}
	b, _ := json.Marshal(c)
	s := base64.RawURLEncoding.EncodeToString(b)
	return &s
}

func decodeCursor(s string) (*store.Cursor, bool) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return nil, false
	}
	var c store.Cursor
	// The id goes to the database as a uuid, so it must really be one (36 characters of anything else would be a 500).
	if json.Unmarshal(b, &c) != nil || c.T.IsZero() || !cursorIDRe.MatchString(c.ID) {
		return nil, false
	}
	return &c, true
}

func validTimezone(name string) bool {
	_, err := time.LoadLocation(name)
	return err == nil && name != "" && name != "Local"
}

// cleanText rejects invalid UTF-8 and control characters (PostgreSQL cannot store NUL in text at all).
func cleanText(s string) bool {
	return utf8.ValidString(s) && !strings.ContainsFunc(s, unicode.IsControl)
}
