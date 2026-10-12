package ndt7

import (
	"context"
	"encoding/json"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	ndt7 "github.com/m-lab/ndt7-client-go"
	"github.com/m-lab/ndt7-client-go/spec"
	"github.com/m-lab/tcp-info/inetdiag"
	"github.com/m-lab/tcp-info/tcp"
)

func ip(n int64) *spec.TCPInfo { return &spec.TCPInfo{ElapsedTime: n} }

func results(dl, ul *ndt7.LatestMeasurements) map[spec.TestKind]*ndt7.LatestMeasurements {
	m := map[spec.TestKind]*ndt7.LatestMeasurements{}
	if dl != nil {
		m[spec.TestDownload] = dl
	}
	if ul != nil {
		m[spec.TestUpload] = ul
	}
	return m
}

var t0 = time.Date(2026, 10, 11, 15, 0, 0, 0, time.UTC)

func goodDownload() *ndt7.LatestMeasurements {
	return &ndt7.LatestMeasurements{
		Client: spec.Measurement{AppInfo: &spec.AppInfo{NumBytes: 109_375_000, ElapsedTime: 10_000_000}}, // 87.5 Mbit/s
		Server: spec.Measurement{TCPInfo: &spec.TCPInfo{LinuxTCPInfo: tcp.LinuxTCPInfo{BytesSent: 110_000_000, BytesRetrans: 341_000, MinRTT: 18_400}, ElapsedTime: 10_000_000}},
	}
}

func goodUpload() *ndt7.LatestMeasurements {
	return &ndt7.LatestMeasurements{
		Client: spec.Measurement{AppInfo: &spec.AppInfo{NumBytes: 40_000_000, ElapsedTime: 10_000_000}},                                      // 32: inflated by the send buffer
		Server: spec.Measurement{TCPInfo: &spec.TCPInfo{LinuxTCPInfo: tcp.LinuxTCPInfo{BytesReceived: 29_062_500}, ElapsedTime: 10_000_000}}, // 23.25
	}
}

func TestSummaryUsesClientDownloadAndServerUpload(t *testing.T) {
	s, err := BuildSummary(t0, t0.Add(22*time.Second), results(goodDownload(), goodUpload()), Server{Machine: "mlab1-lax05", Location: "Los Angeles", Country: "US"})
	if err != nil {
		t.Fatal(err)
	}
	if s.DownloadMbps != 87.5 || s.UploadMbps != 23.25 {
		t.Errorf("speeds: %v %v (upload must come from the server's received bytes, not the client's 32)", s.DownloadMbps, s.UploadMbps)
	}
	if s.MinRttMs == nil || *s.MinRttMs != 18.4 || s.RetransmissionPct == nil || *s.RetransmissionPct != 0.31 {
		t.Errorf("latency %v retransmission %v", s.MinRttMs, s.RetransmissionPct)
	}
	if s.Server.Machine != "mlab1-lax05" || !s.StartedAt.Equal(t0) || !s.FinishedAt.Equal(t0.Add(22*time.Second)) {
		t.Errorf("%+v", s)
	}
}

func TestSummaryFallsBackAndNeverGuesses(t *testing.T) {
	ul := goodUpload()
	ul.Server.TCPInfo = nil
	dl := goodDownload()
	dl.Server.TCPInfo = nil
	s, err := BuildSummary(t0, t0, results(dl, ul), Server{})
	if err != nil {
		t.Fatal(err)
	}
	if s.UploadMbps != 32 || s.MinRttMs != nil || s.RetransmissionPct != nil {
		t.Errorf("upload falls back to the client's figure; unknowns stay nil: %+v", s)
	}
	// MinRTT from BBR when TCP does not have it
	dl = goodDownload()
	dl.Server.TCPInfo.MinRTT = 0
	dl.Server.BBRInfo = &spec.BBRInfo{BBRInfo: inetdiag.BBRInfo{MinRTT: 12_000}}
	if s, _ := BuildSummary(t0, t0, results(dl, goodUpload()), Server{}); s.MinRttMs == nil || *s.MinRttMs != 12 {
		t.Errorf("BBR fallback: %v", s.MinRttMs)
	}
	// nothing sent: no retransmission rate; more retransmitted than sent: clamped
	dl = goodDownload()
	dl.Server.TCPInfo.BytesSent = 0
	if s, _ := BuildSummary(t0, t0, results(dl, goodUpload()), Server{}); s.RetransmissionPct != nil {
		t.Errorf("%v", *s.RetransmissionPct)
	}
	dl = goodDownload()
	dl.Server.TCPInfo.BytesSent, dl.Server.TCPInfo.BytesRetrans = 100, 500
	if s, _ := BuildSummary(t0, t0, results(dl, goodUpload()), Server{}); *s.RetransmissionPct != 100 {
		t.Errorf("%v", *s.RetransmissionPct)
	}
}

func TestSummaryRefusesToInventASpeed(t *testing.T) {
	if _, err := BuildSummary(t0, t0, results(nil, goodUpload()), Server{}); err == nil || !strings.Contains(err.Error(), "download") {
		t.Errorf("%v", err)
	}
	dl := goodDownload()
	dl.Client.AppInfo.ElapsedTime = 0
	if _, err := BuildSummary(t0, t0, results(dl, goodUpload()), Server{}); err == nil {
		t.Error("zero elapsed time")
	}
	if _, err := BuildSummary(t0, t0, results(goodDownload(), nil), Server{}); err == nil || !strings.Contains(err.Error(), "upload") {
		t.Errorf("%v", err)
	}
	if _, err := BuildSummary(t0, t0, results(goodDownload(), &ndt7.LatestMeasurements{}), Server{}); err == nil {
		t.Error("an upload with no data at all")
	}
}

func TestSummaryJSONMatchesTheContract(t *testing.T) {
	s, _ := BuildSummary(t0, t0.Add(time.Second), results(goodDownload(), goodUpload()), Server{Machine: "m"})
	b, _ := json.Marshal(s)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	for _, k := range []string{"startedAt", "finishedAt", "downloadMbps", "uploadMbps", "minRttMs", "retransmissionPct", "server", "raw"} {
		if _, ok := m[k]; !ok {
			t.Errorf("missing %q: %s", k, b)
		}
	}
	if m["startedAt"] != "2026-10-11T15:00:00Z" || len(b) > 2000 {
		t.Errorf("RFC 3339 times and a small body: %s", b)
	}
	s.MinRttMs, s.RetransmissionPct = nil, nil
	b, _ = json.Marshal(s)
	if !strings.Contains(string(b), `"minRttMs":null`) || !strings.Contains(string(b), `"retransmissionPct":null`) {
		t.Errorf("unknown values are explicit nulls, which the API accepts: %s", b)
	}
}

/* ---------- the whole client against a local stand-in for M-Lab ---------- */

type mock struct {
	mu                           sync.Mutex
	lastRecvBytes, lastRecvUs    int64 // what the upload server last reported
	lastSent, lastRetrans        int64 // what the download server last reported
	closeAfter                   time.Duration
	downloadSilent, uploadSilent bool
}

func (m *mock) handler() http.Handler {
	up := websocket.Upgrader{Subprotocols: []string{"net.measurementlab.ndt.v7"}, CheckOrigin: func(*http.Request) bool { return true }}
	mux := http.NewServeMux()
	mux.HandleFunc("/ndt/v7/download", func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		chunk := make([]byte, 64*1024)
		start, last := time.Now(), time.Now()
		var sent int64
		for time.Since(start) < m.closeAfter {
			if c.WriteMessage(websocket.BinaryMessage, chunk) != nil {
				return
			}
			sent += int64(len(chunk))
			time.Sleep(5 * time.Millisecond)
			if !m.downloadSilent && time.Since(last) > 100*time.Millisecond {
				last = time.Now()
				retrans := sent / 300
				m.mu.Lock()
				m.lastSent, m.lastRetrans = sent, retrans
				m.mu.Unlock()
				b, _ := json.Marshal(spec.Measurement{TCPInfo: &spec.TCPInfo{LinuxTCPInfo: tcp.LinuxTCPInfo{BytesSent: sent, BytesRetrans: retrans, MinRTT: 15_000}, ElapsedTime: time.Since(start).Microseconds()}})
				_ = c.WriteMessage(websocket.TextMessage, b)
			}
		}
		_ = c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	})
	mux.HandleFunc("/ndt/v7/upload", func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		start := time.Now()
		var total int64
		var wmu sync.Mutex
		go func() { // report what has been received, as the real server does
			for range time.Tick(100 * time.Millisecond) {
				wmu.Lock()
				done := time.Since(start) > m.closeAfter
				recv := total
				wmu.Unlock()
				if done {
					return
				}
				us := time.Since(start).Microseconds()
				m.mu.Lock()
				m.lastRecvBytes, m.lastRecvUs = recv, us
				m.mu.Unlock()
				if !m.uploadSilent {
					b, _ := json.Marshal(spec.Measurement{TCPInfo: &spec.TCPInfo{LinuxTCPInfo: tcp.LinuxTCPInfo{BytesReceived: recv}, ElapsedTime: us}})
					_ = c.WriteMessage(websocket.TextMessage, b)
				}
			}
		}()
		_ = c.SetReadDeadline(start.Add(m.closeAfter))
		for {
			mt, b, err := c.ReadMessage()
			if err != nil {
				break
			}
			if mt == websocket.BinaryMessage {
				wmu.Lock()
				total += int64(len(b))
				wmu.Unlock()
			}
		}
		_ = c.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(time.Second))
	})
	return mux
}

func startMock(t *testing.T, m *mock) string {
	t.Helper()
	srv := httptest.NewServer(m.handler())
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

func TestTheWholeClientAgainstALocalMLab(t *testing.T) {
	m := &mock{closeAfter: 1500 * time.Millisecond}
	addr := startMock(t, m)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	raw, err := Runner{Server: addr, Scheme: "ws"}.Run(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var s struct {
		DownloadMbps, UploadMbps float64
		MinRttMs                 *float64
		RetransmissionPct        *float64
		StartedAt, FinishedAt    time.Time
		Server                   Server
	}
	if err := json.Unmarshal(raw, &s); err != nil {
		t.Fatal(err)
	}
	if s.DownloadMbps < 5 || s.DownloadMbps > 200 {
		t.Errorf("the mock streams roughly 100 Mbit/s: %v", s.DownloadMbps)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	wantUp := math.Round(float64(m.lastRecvBytes)*8/float64(m.lastRecvUs)*100) / 100
	if s.UploadMbps <= 0 || math.Abs(s.UploadMbps-wantUp) > 1.0 {
		t.Errorf("upload comes from the server's own counters: got %v, server's last report works out at %v", s.UploadMbps, wantUp)
	}
	if s.MinRttMs == nil || *s.MinRttMs != 15 {
		t.Errorf("MinRTT 15000 µs is 15 ms: %v", s.MinRttMs)
	}
	wantRetrans := float64(m.lastRetrans) / float64(m.lastSent) * 100
	if s.RetransmissionPct == nil || math.Abs(*s.RetransmissionPct-wantRetrans) > 0.05 {
		t.Errorf("retransmission %v, want about %v", s.RetransmissionPct, wantRetrans)
	}
	if s.Server.Machine != "127.0.0.1" || !s.FinishedAt.After(s.StartedAt) || s.FinishedAt.Sub(s.StartedAt) < 2*time.Second {
		t.Errorf("both phases ran: %+v", s)
	}
}

func TestAnUnreachableServerIsExplained(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	addr := l.Addr().String()
	l.Close() // nothing listens here now
	_, err := Runner{Server: addr, Scheme: "ws"}.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "could not run") || !strings.Contains(err.Error(), "online") {
		t.Errorf("%v", err)
	}
}

func TestATestThatNeverEndsIsStopped(t *testing.T) {
	m := &mock{closeAfter: time.Minute} // the "server" keeps going far past our limit
	addr := startMock(t, m)
	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	started := time.Now()
	_, err := Runner{Server: addr, Scheme: "ws"}.Run(ctx)
	if err == nil || !strings.Contains(err.Error(), "did not finish in time") {
		t.Errorf("%v", err)
	}
	if time.Since(started) > 8*time.Second {
		t.Errorf("took %v", time.Since(started))
	}
}

func TestAServerThatReportsNothingIsNotTrustedWithGuesses(t *testing.T) {
	// no server measurements at all: download and (client-side) upload still work, latency and retransmission stay unknown
	m := &mock{closeAfter: time.Second, downloadSilent: true, uploadSilent: true}
	addr := startMock(t, m)
	raw, err := Runner{Server: addr, Scheme: "ws"}.Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var s map[string]any
	_ = json.Unmarshal(raw, &s)
	if s["minRttMs"] != nil || s["retransmissionPct"] != nil || s["downloadMbps"].(float64) <= 0 || s["uploadMbps"].(float64) <= 0 {
		t.Errorf("%s", raw)
	}
}
