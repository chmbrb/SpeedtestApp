// Package ookla runs Ookla's command-line speed test and returns its JSON result.
package ookla

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/chmbrb/SpeedtestApp/collector-go/internal/config"
)

type Runner struct {
	Path string
	// AcceptLicense is true only when the person running the collector said they accepted Ookla's terms
	// (--accept-ookla-license). Only then are the acceptance flags passed to the tool.
	AcceptLicense bool
}

// Run executes `speedtest --format=json` and returns the one-line JSON result. The tool never reads from the terminal (stdin is
// closed), so a licence prompt cannot hang an unattended run.
func (r Runner) Run(ctx context.Context) (json.RawMessage, error) {
	args := []string{"--format=json"}
	if r.AcceptLicense {
		args = append(args, "--accept-license", "--accept-gdpr")
	}
	cmd := exec.CommandContext(ctx, r.Path, args...)
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &limited{b: &stderr, max: 4096}
	if err := cmd.Start(); err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("the Ookla command-line tool was not found (looked for %q); install it or set --ookla-path", r.Path)
		}
		return nil, fmt.Errorf("could not start the Ookla tool: %w", err)
	}
	waitErr := cmd.Wait()
	if ctx.Err() != nil {
		return nil, fmt.Errorf("the Ookla test did not finish in time: %w", context.Cause(ctx))
	}
	if waitErr != nil {
		return nil, r.explain(stdout.String()+"\n"+stderr.String(), waitErr)
	}
	return parse(stdout.Bytes())
}

// explain turns the tool's failure into something a person can act on.
func (r Runner) explain(output string, err error) error {
	if !r.AcceptLicense && strings.Contains(strings.ToLower(output), "license") {
		return fmt.Errorf("Ookla's terms have not been accepted on this device. Read them at %s and, if you agree, run with --accept-ookla-license", config.OoklaTerms)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	// The tool reports some failures as a JSON object on stdout, e.g. {"error":"Cannot open socket"}: show just the words.
	var jerr struct {
		Error   string `json:"error"`
		Message string `json:"message"`
	}
	if strings.HasPrefix(last, "{") && json.Unmarshal([]byte(last), &jerr) == nil {
		if msg := strings.TrimSpace(firstNonEmpty(jerr.Error, jerr.Message)); msg != "" {
			return fmt.Errorf("the Ookla test failed: %s", msg)
		}
	}
	if len(last) > 200 {
		last = last[:200]
	}
	if last == "" {
		return fmt.Errorf("the Ookla test failed: %w", err)
	}
	return fmt.Errorf("the Ookla test failed: %s", last)
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

// parse finds the result in the tool's output: the last line that is a JSON object of type "result".
func parse(out []byte) (json.RawMessage, error) {
	lines := bytes.Split(bytes.TrimSpace(out), []byte("\n"))
	for i := len(lines) - 1; i >= 0; i-- {
		line := bytes.TrimSpace(lines[i])
		if len(line) == 0 || line[0] != '{' {
			continue
		}
		var head struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(line, &head) == nil && head.Type == "result" {
			return json.RawMessage(append([]byte(nil), line...)), nil
		}
	}
	return nil, errors.New("the Ookla tool finished without printing a result")
}

// Version runs `speedtest --version`, for the setup check. It does not accept anything.
func (r Runner) Version(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, r.Path, "--version").Output()
	if err != nil {
		if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("not found (looked for %q)", r.Path)
		}
		return "", err
	}
	return strings.TrimSpace(strings.SplitN(string(out), "\n", 2)[0]), nil
}

type limited struct {
	b   *bytes.Buffer
	max int
}

func (l *limited) Write(p []byte) (int, error) {
	if room := l.max - l.b.Len(); room > 0 {
		l.b.Write(p[:min(len(p), room)])
	}
	return len(p), nil
}
