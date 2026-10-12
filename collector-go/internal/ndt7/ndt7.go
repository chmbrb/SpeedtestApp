// Package ndt7 runs M-Lab's NDT7 speed test using M-Lab's own Go client, and turns its measurements into the summary the API
// stores. The summary maths mirrors the dashboard's browser test, so both report the same quantities the same way.
package ndt7

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	v2 "github.com/m-lab/locate/api/v2"
	ndt7 "github.com/m-lab/ndt7-client-go"
	"github.com/m-lab/ndt7-client-go/spec"

	"github.com/chmbrb/SpeedtestApp/collector-go/internal/version"
)

// Summary is docs/openapi.yaml's Ndt7Summary.
type Summary struct {
	StartedAt         time.Time `json:"startedAt"`
	FinishedAt        time.Time `json:"finishedAt"`
	DownloadMbps      float64   `json:"downloadMbps"`
	UploadMbps        float64   `json:"uploadMbps"`
	MinRttMs          *float64  `json:"minRttMs"`
	RetransmissionPct *float64  `json:"retransmissionPct"`
	Server            Server    `json:"server"`
	Raw               any       `json:"raw,omitempty"`
}

type Server struct {
	Machine  string `json:"machine"`
	Location string `json:"location"`
	Country  string `json:"country"`
}

func round(v float64, digits int) float64 {
	p := math.Pow10(digits)
	return math.Round(v*p) / p
}

func ptr(v float64) *float64 { return &v }

// BuildSummary computes the summary from what the client saw.
//   - Download speed is the client's own mean over the test (bytes received / elapsed).
//   - Upload speed prefers what the *server* received: the client's byte count includes data still queued in its send buffer
//     and overstates the speed. It falls back to the client's figure.
//   - "Packet loss" is the TCP retransmission rate the server saw while sending (retransmitted / sent bytes).
//   - Latency is the lowest round-trip time the server saw.
//
// Unknown values are nil, never guessed.
func BuildSummary(started, finished time.Time, res map[spec.TestKind]*ndt7.LatestMeasurements, server Server) (Summary, error) {
	dl, ul := res[spec.TestDownload], res[spec.TestUpload]
	s := Summary{StartedAt: started.UTC(), FinishedAt: finished.UTC(), Server: server}

	if dl == nil || dl.Client.AppInfo == nil || dl.Client.AppInfo.ElapsedTime <= 0 {
		return s, errors.New("the download speed could not be measured")
	}
	// bytes * 8 bits / microseconds = megabits per second
	s.DownloadMbps = round(float64(dl.Client.AppInfo.NumBytes)*8/float64(dl.Client.AppInfo.ElapsedTime), 2)

	var up float64
	switch {
	case ul != nil && ul.Server.TCPInfo != nil && ul.Server.TCPInfo.ElapsedTime > 0 && ul.Server.TCPInfo.BytesReceived > 0:
		up = float64(ul.Server.TCPInfo.BytesReceived) * 8 / float64(ul.Server.TCPInfo.ElapsedTime)
	case ul != nil && ul.Client.AppInfo != nil && ul.Client.AppInfo.ElapsedTime > 0:
		up = float64(ul.Client.AppInfo.NumBytes) * 8 / float64(ul.Client.AppInfo.ElapsedTime)
	default:
		return s, errors.New("the upload speed could not be measured")
	}
	s.UploadMbps = round(up, 2)

	if t := dl.Server.TCPInfo; t != nil {
		if t.BytesSent > 0 && t.BytesRetrans >= 0 {
			s.RetransmissionPct = ptr(round(math.Min(100, float64(t.BytesRetrans)/float64(t.BytesSent)*100), 3))
		}
		rtt := float64(t.MinRTT)
		if rtt == 0 && dl.Server.BBRInfo != nil {
			rtt = float64(dl.Server.BBRInfo.MinRTT)
		}
		if rtt > 0 {
			s.MinRttMs = ptr(round(rtt/1000, 2)) // microseconds -> milliseconds
		}
	}
	s.Raw = map[string]any{"download": rawOf(dl), "upload": rawOf(ul)}
	return s, nil
}

func rawOf(m *ndt7.LatestMeasurements) any {
	if m == nil {
		return nil
	}
	return map[string]any{"client": m.Client.AppInfo, "serverTCPInfo": m.Server.TCPInfo}
}

// captureLocator remembers which M-Lab machine the client was pointed at, so the result can say where it was measured.
type captureLocator struct {
	inner  ndt7.Locator
	target *v2.Target
}

func (c *captureLocator) Nearest(ctx context.Context, service string) ([]v2.Target, error) {
	targets, err := c.inner.Nearest(ctx, service)
	if err == nil && len(targets) > 0 {
		c.target = &targets[0]
	}
	return targets, err
}

// Runner runs one NDT7 test. Server and Scheme exist for tests (a local stand-in for M-Lab); leave them empty to use M-Lab's
// locate service.
type Runner struct {
	Server string // host:port, bypasses the locate service
	Scheme string // "ws" for a local test server; default "wss"
}

// Run measures download then upload. The caller must already have had the person accept M-Lab's data policy.
func (r Runner) Run(ctx context.Context) (json.RawMessage, error) {
	client := ndt7.NewClient("speedtest-collector", version.Version)
	capture := &captureLocator{inner: client.Locate}
	client.Locate = capture
	if r.Server != "" {
		client.Server = r.Server
	}
	if r.Scheme != "" {
		client.Scheme = r.Scheme
	}

	started := time.Now()
	for _, start := range []func(context.Context) (<-chan spec.Measurement, error){client.StartDownload, client.StartUpload} {
		ch, err := start(ctx)
		if err != nil {
			return nil, friendly(ctx, err)
		}
		for range ch { // the client keeps its latest measurements; we only need the stream drained
		}
		if ctx.Err() != nil {
			return nil, friendly(ctx, ctx.Err())
		}
	}

	server := Server{Machine: client.FQDN}
	if capture.target != nil {
		server.Machine = capture.target.Machine
		if capture.target.Location != nil {
			server.Location, server.Country = capture.target.Location.City, capture.target.Location.Country
		}
	}
	sum, err := BuildSummary(started, time.Now(), client.Results(), server)
	if err != nil {
		return nil, err
	}
	return json.Marshal(sum)
}

func friendly(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("the NDT7 test did not finish in time: %w", context.Cause(ctx))
	}
	if errors.Is(err, ndt7.ErrNoTargets) {
		return errors.New("M-Lab has no test server available right now; try again later")
	}
	return fmt.Errorf("the NDT7 test could not run (is this device online?): %w", err)
}
