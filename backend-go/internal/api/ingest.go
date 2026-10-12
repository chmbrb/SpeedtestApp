package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const maxBodyBytes = 64 * 1024

// Upper bounds mirror the database check constraint; see sql/postgres/005_providers_and_tests.sql.
const (
	maxMbps      = 1_000_000.0      // 1 Tbit/s
	maxBandwidth = maxMbps * 125000 // bytes per second
	maxMs        = 1_000_000.0
)

var earliestResult = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)

// checkTime requires an RFC 3339 time between 2000-01-01 and one day from now.
func checkTime(e *errs, field, v string) (time.Time, bool) {
	t, err := time.Parse(time.RFC3339, v)
	if err != nil {
		e.add(field, "must be an RFC 3339 date-time")
		return t, false
	}
	if t.Before(earliestResult) || t.After(time.Now().Add(24*time.Hour)) {
		e.add(field, "must be between 2000-01-01 and tomorrow")
		return t, false
	}
	return t, true
}

// optionalKind reports an error when a member is present but not of the expected JSON kind
// ('{' object, '"' string). A missing member (len 0) is fine; an explicit null is not.
func optionalKind(e *errs, field string, v json.RawMessage, kind byte) {
	if len(v) == 0 || v[0] == kind {
		return
	}
	if kind == '{' {
		e.add(field, "must be an object")
	} else {
		e.add(field, "must be a string")
	}
}

func rejectNull(e *errs, field string, v json.RawMessage) {
	if string(v) == "null" {
		e.add(field, "must be a number, not null")
	}
}

// validResultURL accepts "" (stored as null) or an absolute http(s) URL, matching the contract's format: uri.
func validResultURL(s string) bool {
	if s == "" {
		return true
	}
	if len(s) > 2048 || !cleanText(s) {
		return false
	}
	u, err := url.Parse(s)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

func checkRange(e *errs, field string, v *float64, max float64, required bool) {
	switch {
	case v == nil && required:
		e.add(field, "is required")
	case v != nil && (*v < 0 || *v > max || *v != *v):
		e.add(field, "must be between 0 and %g", max)
	}
}

// ooklaPayload is the part of the Ookla CLI output we require.
type ooklaPayload struct {
	Type      string `json:"type"`
	Timestamp string `json:"timestamp"`
	Ping      struct {
		Latency *float64 `json:"latency"`
		Jitter  *float64 `json:"jitter"`
	} `json:"ping"`
	Download struct {
		Bandwidth *float64 `json:"bandwidth"`
	} `json:"download"`
	Upload struct {
		Bandwidth *float64 `json:"bandwidth"`
	} `json:"upload"`
	PacketLoss *float64        `json:"packetLoss"`
	Result     json.RawMessage `json:"result"`
	Server     json.RawMessage `json:"server"`
	Interface  json.RawMessage `json:"interface"`
	ISP        json.RawMessage `json:"isp"`
}

func validateOokla(raw json.RawMessage, prefix string) errs {
	var e errs
	var p ooklaPayload
	if len(raw) == 0 || json.Unmarshal(raw, &p) != nil {
		e.add(prefix, "must be the JSON object printed by `speedtest --format=json`")
		return e
	}
	if p.Type != "result" {
		e.add(prefix+"type", `must be "result"`)
	}
	checkTime(&e, prefix+"timestamp", p.Timestamp)
	checkRange(&e, prefix+"download.bandwidth", p.Download.Bandwidth, maxBandwidth, true)
	checkRange(&e, prefix+"upload.bandwidth", p.Upload.Bandwidth, maxBandwidth, true)
	checkRange(&e, prefix+"ping.latency", p.Ping.Latency, maxMs, false)
	checkRange(&e, prefix+"ping.jitter", p.Ping.Jitter, maxMs, false)
	checkRange(&e, prefix+"packetLoss", p.PacketLoss, 100, false)
	// optional members: when present they must have the documented JSON type (null is not "absent")
	optionalKind(&e, prefix+"result", p.Result, '{')
	optionalKind(&e, prefix+"server", p.Server, '{')
	optionalKind(&e, prefix+"interface", p.Interface, '{')
	optionalKind(&e, prefix+"isp", p.ISP, '"')
	// numbers decoded into *float64 cannot tell null from absent, so look at the raw members
	var top map[string]json.RawMessage
	_ = json.Unmarshal(raw, &top)
	rejectNull(&e, prefix+"packetLoss", top["packetLoss"])
	optionalKind(&e, prefix+"ping", top["ping"], '{')
	var ping map[string]json.RawMessage
	if json.Unmarshal(top["ping"], &ping) == nil {
		rejectNull(&e, prefix+"ping.latency", ping["latency"])
		rejectNull(&e, prefix+"ping.jitter", ping["jitter"])
	}
	var res map[string]json.RawMessage
	if json.Unmarshal(p.Result, &res) == nil {
		optionalKind(&e, prefix+"result.id", res["id"], '"')
		optionalKind(&e, prefix+"result.url", res["url"], '"')
		var u string
		if json.Unmarshal(res["url"], &u) == nil && !validResultURL(u) {
			e.add(prefix+"result.url", "must be empty or an absolute http(s) URL")
		}
	}
	return e
}

type ndt7Payload struct {
	StartedAt         string   `json:"startedAt"`
	FinishedAt        string   `json:"finishedAt"`
	DownloadMbps      *float64 `json:"downloadMbps"`
	UploadMbps        *float64 `json:"uploadMbps"`
	MinRttMs          *float64 `json:"minRttMs"`
	RetransmissionPct *float64 `json:"retransmissionPct"`
	Server            struct {
		Machine  json.RawMessage `json:"machine"`
		Location json.RawMessage `json:"location"`
		Country  json.RawMessage `json:"country"`
	} `json:"server"`
	RawObj json.RawMessage `json:"raw"`
}

func validateNDT7(raw json.RawMessage, prefix string) errs {
	var e errs
	var p ndt7Payload
	if len(raw) == 0 || json.Unmarshal(raw, &p) != nil {
		e.add(prefix, "must be an NDT7 summary object (check field types: server.* are strings, raw is an object)")
		return e
	}
	started, ok1 := checkTime(&e, prefix+"startedAt", p.StartedAt)
	finished, ok2 := checkTime(&e, prefix+"finishedAt", p.FinishedAt)
	if ok1 && ok2 && finished.Before(started) {
		e.add(prefix+"finishedAt", "must not be before startedAt")
	}
	checkRange(&e, prefix+"downloadMbps", p.DownloadMbps, maxMbps, true)
	checkRange(&e, prefix+"uploadMbps", p.UploadMbps, maxMbps, true)
	checkRange(&e, prefix+"minRttMs", p.MinRttMs, maxMs, false)
	checkRange(&e, prefix+"retransmissionPct", p.RetransmissionPct, 100, false)
	optionalKind(&e, prefix+"raw", p.RawObj, '{')
	optionalKind(&e, prefix+"server.machine", p.Server.Machine, '"')
	optionalKind(&e, prefix+"server.location", p.Server.Location, '"')
	optionalKind(&e, prefix+"server.country", p.Server.Country, '"')
	var top map[string]json.RawMessage
	if json.Unmarshal(raw, &top) == nil {
		optionalKind(&e, prefix+"server", top["server"], '{')
	}
	return e
}

// authenticateIngest checks the bearer token and the per-device rate limit. ok=false means a response was written.
func (s *Server) authenticateIngest(w http.ResponseWriter, r *http.Request) (device string, ok bool) {
	device, found := s.auth.Device(r)
	if !found {
		w.Header().Set("WWW-Authenticate", "Bearer")
		writeProblem(w, http.StatusUnauthorized, "missing or invalid bearer token")
		return "", false
	}
	if allowed, retry := s.limiter.allow(device); !allowed && s.limitIngest {
		w.Header().Set("Retry-After", strconv.Itoa(retry))
		writeProblem(w, http.StatusTooManyRequests, "too many results submitted; slow down")
		return "", false
	}
	return device, true
}

func readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	var tooBig *http.MaxBytesError
	if errors.As(err, &tooBig) {
		writeProblem(w, http.StatusRequestEntityTooLarge, "body is larger than 64 KB")
		return nil, false
	}
	if err != nil {
		writeProblem(w, http.StatusBadRequest, "could not read body")
		return nil, false
	}
	return body, true
}

// postResult implements POST /api/v1/results.
func (s *Server) postResult(w http.ResponseWriter, r *http.Request) {
	device, ok := s.authenticateIngest(w, r)
	if !ok {
		return
	}
	body, ok := readBody(w, r)
	if !ok {
		return
	}
	var env struct {
		Provider       string          `json:"provider"`
		ConnectionType string          `json:"connectionType"`
		Trigger        string          `json:"trigger"`
		Result         json.RawMessage `json:"result"`
	}
	if json.Unmarshal(body, &env) != nil {
		writeProblem(w, http.StatusBadRequest, "body is not valid JSON")
		return
	}
	var e errs
	if !contains(providers, env.Provider) {
		e.add("provider", "must be one of ookla, ndt7")
	}
	if !contains(connectionTypes, env.ConnectionType) {
		e.add("connectionType", "must be one of wired, wireless")
	}
	var top map[string]json.RawMessage
	_ = json.Unmarshal(body, &top)
	if string(top["trigger"]) == "null" {
		e.add("trigger", "must be a string, not null")
	}
	if env.Trigger == "" {
		env.Trigger = "scheduled"
	} else if !contains(triggers, env.Trigger) {
		e.add("trigger", "must be one of scheduled, on_demand")
	}
	switch env.Provider {
	case "ookla":
		e = append(e, validateOokla(env.Result, "result.")...)
	case "ndt7":
		e = append(e, validateNDT7(env.Result, "result.")...)
	}
	if len(e) > 0 {
		writeProblem(w, http.StatusBadRequest, "request validation failed", e...)
		return
	}

	var id string
	var err error
	if env.Provider == "ookla" {
		id, err = s.store.InsertOokla(r.Context(), env.Result, device, env.ConnectionType, env.Trigger)
	} else {
		id, err = s.store.InsertNDT7(r.Context(), env.Result, device, env.ConnectionType, env.Trigger, "")
	}
	if err != nil {
		s.insertFailed(w, err)
		return
	}
	detail, err := s.store.GetResult(r.Context(), id)
	if err != nil {
		s.serverError(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, detail.Result)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}
