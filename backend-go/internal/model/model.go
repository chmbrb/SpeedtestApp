// Package model holds the JSON shapes defined by docs/openapi.yaml.
package model

import (
	"encoding/json"
	"time"
)

type Server struct {
	Name     *string `json:"name"`
	Location *string `json:"location"`
	Country  *string `json:"country"`
}

type Result struct {
	ID             string    `json:"id"`
	Provider       string    `json:"provider"`
	Trigger        string    `json:"trigger"`
	Device         string    `json:"device"`
	ConnectionType string    `json:"connectionType"`
	TestedAt       time.Time `json:"testedAt"`
	DownloadMbps   float64   `json:"downloadMbps"`
	UploadMbps     float64   `json:"uploadMbps"`
	LatencyMs      *float64  `json:"latencyMs"`
	JitterMs       *float64  `json:"jitterMs"`
	PacketLossPct  *float64  `json:"packetLossPct"`
	ISP            *string   `json:"isp"`
	Server         Server    `json:"server"`
	ResultURL      *string   `json:"resultUrl"`
}

// ResultDetail is a Result plus the provider's original payload.
type ResultDetail struct {
	Result
	Raw json.RawMessage `json:"raw"`
}

type Device struct {
	Name            string    `json:"name"`
	ConnectionTypes []string  `json:"connectionTypes"`
	Providers       []string  `json:"providers"`
	TestCount       int       `json:"testCount"`
	LastTestAt      time.Time `json:"lastTestAt"`
}

// Stat is the distribution of one metric; fields are null when there is no data.
type Stat struct {
	Avg *float64 `json:"avg"`
	Min *float64 `json:"min"`
	Max *float64 `json:"max"`
	P50 *float64 `json:"p50"`
	P95 *float64 `json:"p95"`
}

type Metrics struct {
	DownloadMbps  Stat `json:"downloadMbps"`
	UploadMbps    Stat `json:"uploadMbps"`
	LatencyMs     Stat `json:"latencyMs"`
	PacketLossPct Stat `json:"packetLossPct"`
}

type Summary struct {
	Metrics
	Count int        `json:"count"`
	First *time.Time `json:"first"`
	Last  *time.Time `json:"last"`
}

// SeriesKey carries only the groupBy fields.
type SeriesKey struct {
	Provider       *string `json:"provider,omitempty"`
	Device         *string `json:"device,omitempty"`
	ConnectionType *string `json:"connectionType,omitempty"`
}

type TimeseriesPoint struct {
	Metrics
	Bucket time.Time `json:"bucket"`
	Count  int       `json:"count"`
}

type Series struct {
	Key    SeriesKey         `json:"key"`
	Points []TimeseriesPoint `json:"points"`
}

type Timeseries struct {
	Interval string   `json:"interval"`
	Timezone string   `json:"timezone"`
	Series   []Series `json:"series"`
}

// NotificationRules are the thresholds the notification monitor applies, in the units a person thinks in.
type NotificationRules struct {
	SlowRatio       float64 `json:"slowRatio"`
	SlowConsecutive int     `json:"slowConsecutive"`
	RecoverRatio    float64 `json:"recoverRatio"`
	LossPct         float64 `json:"lossPct"`
	LossConsecutive int     `json:"lossConsecutive"`
	SilenceMinHours float64 `json:"silenceMinHours"`
	RemindHours     float64 `json:"remindHours"`
	BaselineDays    int     `json:"baselineDays"`
}

type ActiveAlert struct {
	Kind       string     `json:"kind"`
	Subject    string     `json:"subject"`
	Title      string     `json:"title"`
	Detail     string     `json:"detail"`
	Severity   string     `json:"severity"`
	Since      time.Time  `json:"since"`
	NotifiedAt *time.Time `json:"notifiedAt"`
}

type NotificationEvent struct {
	ID        int64     `json:"id"`
	At        time.Time `json:"at"`
	Event     string    `json:"event"`
	Kind      *string   `json:"kind"`
	Title     string    `json:"title"`
	Message   string    `json:"message"`
	Delivered bool      `json:"delivered"`
	Error     *string   `json:"error"`
}

// NotificationStatus never includes the ntfy topic or credentials.
type NotificationStatus struct {
	Enabled bool                `json:"enabled"`
	Server  *string             `json:"server"`
	Rules   *NotificationRules  `json:"rules"`
	Active  []ActiveAlert       `json:"active"`
	Recent  []NotificationEvent `json:"recent"`
}

// Test is an on-demand test run. SubmitToken is present only in the response that creates a browser test.
type Test struct {
	ID             string     `json:"id"`
	Provider       string     `json:"provider"`
	Mode           string     `json:"mode"`
	Device         string     `json:"device"`
	ConnectionType string     `json:"connectionType"`
	State          string     `json:"state"`
	CreatedAt      time.Time  `json:"createdAt"`
	StartedAt      *time.Time `json:"startedAt"`
	FinishedAt     *time.Time `json:"finishedAt"`
	ResultID       *string    `json:"resultId"`
	Error          *string    `json:"error"`
	QueuePosition  *int       `json:"queuePosition"`
	SubmitToken    string     `json:"submitToken,omitempty"`
}

type Capability struct {
	Available bool    `json:"available"`
	Reason    *string `json:"reason"`
}

type Capabilities struct {
	DeviceName           string     `json:"deviceName"`
	ServerConnectionType string     `json:"serverConnectionType"`
	Ookla                Capability `json:"ookla"`
	NDT7                 Capability `json:"ndt7"`
}
