package ondemand

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Progress is a live reading during a test (docs/openapi.yaml: ProgressEvent).
type Progress struct {
	Type        string   `json:"type"`
	Phase       string   `json:"phase"` // ping | download | upload
	ElapsedMs   int64    `json:"elapsedMs"`
	Fraction    *float64 `json:"fraction"`
	CurrentMbps *float64 `json:"currentMbps"`
	LatencyMs   *float64 `json:"latencyMs"`
	JitterMs    *float64 `json:"jitterMs"`
}

// Runner runs one speed test and reports progress as it goes. It returns the provider's final result.
type Runner interface {
	Run(ctx context.Context, emit func(Progress)) (raw []byte, err error)
}

// ooklaLine is the part of the CLI's `--format=jsonl` output that matters here.
type ooklaLine struct {
	Type string `json:"type"`
	Ping *struct {
		Jitter   *float64 `json:"jitter"`
		Latency  *float64 `json:"latency"`
		Progress *float64 `json:"progress"`
	} `json:"ping"`
	Download *ooklaTransfer `json:"download"`
	Upload   *ooklaTransfer `json:"upload"`
}

type ooklaTransfer struct {
	Bandwidth *float64 `json:"bandwidth"` // bytes per second
	Progress  *float64 `json:"progress"`
	Latency   *struct {
		Iqm    *float64 `json:"iqm"`
		Jitter *float64 `json:"jitter"`
	} `json:"latency"`
}

// ParseOoklaLine turns one line of CLI output into a progress reading, or recognises the final result.
// isResult is true for the last line, which the caller keeps whole. Lines of other types (testStart) give neither.
func ParseOoklaLine(line []byte, elapsed time.Duration) (p *Progress, isResult bool, err error) {
	var l ooklaLine
	if err := json.Unmarshal(line, &l); err != nil {
		return nil, false, fmt.Errorf("unreadable output from the speed test: %w", err)
	}
	ms := elapsed.Milliseconds()
	switch l.Type {
	case "result":
		return nil, true, nil
	case "ping":
		if l.Ping == nil {
			return nil, false, nil
		}
		return &Progress{Type: "progress", Phase: "ping", ElapsedMs: ms, Fraction: l.Ping.Progress,
			LatencyMs: l.Ping.Latency, JitterMs: l.Ping.Jitter}, false, nil
	case "download", "upload":
		t := l.Download
		if l.Type == "upload" {
			t = l.Upload
		}
		if t == nil {
			return nil, false, nil
		}
		p := &Progress{Type: "progress", Phase: l.Type, ElapsedMs: ms, Fraction: t.Progress}
		if t.Bandwidth != nil {
			mbps := *t.Bandwidth / 125000 // bytes/second -> megabits/second
			p.CurrentMbps = &mbps
		}
		if t.Latency != nil { // latency under load, when the CLI reports it
			p.LatencyMs, p.JitterMs = t.Latency.Iqm, t.Latency.Jitter
		}
		return p, false, nil
	}
	return nil, false, nil
}

// Ookla runs the Ookla CLI. It is only constructed once the licence has been accepted (Config.OoklaAccepted), and passes
// the acceptance flags for exactly that reason.
type Ookla struct {
	Path string
}

func (o Ookla) Run(ctx context.Context, emit func(Progress)) ([]byte, error) {
	cmd := exec.CommandContext(ctx, o.Path, "--format=jsonl", "--accept-license", "--accept-gdpr")
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) } // let it clean up; killed after WaitDelay
	cmd.WaitDelay = 5 * time.Second
	var stderr bytes.Buffer
	cmd.Stderr = &limitedWriter{w: &stderr, max: 4096}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("could not start the Ookla speed test: %w", err)
	}

	var result []byte
	var parseErr error
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		p, isResult, err := ParseOoklaLine(line, time.Since(start))
		if err != nil {
			parseErr = err
			continue
		}
		if isResult {
			result = append([]byte(nil), line...)
		} else if p != nil {
			emit(*p)
		}
	}
	waitErr := cmd.Wait()

	if ctx.Err() != nil {
		return nil, context.Cause(ctx)
	}
	if waitErr != nil {
		return nil, fmt.Errorf("the Ookla speed test failed: %s", explain(stderr.String(), waitErr))
	}
	if result == nil {
		if parseErr != nil {
			return nil, parseErr
		}
		return nil, errors.New("the Ookla speed test finished without a result")
	}
	return result, nil
}

// explain keeps the useful last line of the CLI's error output, without paths or noise.
func explain(stderr string, err error) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		if len(last) > 200 {
			last = last[:200]
		}
		return last
	}
	return err.Error()
}

type limitedWriter struct {
	w   *bytes.Buffer
	max int
}

func (l *limitedWriter) Write(p []byte) (int, error) {
	if room := l.max - l.w.Len(); room > 0 {
		if len(p) > room {
			l.w.Write(p[:room])
		} else {
			l.w.Write(p)
		}
	}
	return len(p), nil
}
