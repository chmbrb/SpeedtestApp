package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Message is what gets sent. Priority is ntfy's 1 (min) to 5 (max); 3 is the default.
type Message struct {
	Title    string
	Body     string
	Priority int
	Tags     []string
}

// Sender delivers a message. The real one talks to ntfy; tests use a fake.
type Sender interface {
	Send(ctx context.Context, m Message) error
}

// Ntfy publishes with ntfy's JSON API (a POST of {"topic": ...} to the server root), which, unlike the header API,
// has no trouble with non-ASCII titles.
type Ntfy struct {
	cfg    Config
	client *http.Client
	sleep  func(context.Context, time.Duration)
}

func NewNtfy(cfg Config) *Ntfy {
	return &Ntfy{
		cfg:    cfg,
		client: &http.Client{Timeout: 10 * time.Second},
		sleep: func(ctx context.Context, d time.Duration) {
			select {
			case <-ctx.Done():
			case <-time.After(d):
			}
		},
	}
}

type publishBody struct {
	Topic    string   `json:"topic"`
	Title    string   `json:"title,omitempty"`
	Message  string   `json:"message"`
	Priority int      `json:"priority,omitempty"`
	Tags     []string `json:"tags,omitempty"`
	Click    string   `json:"click,omitempty"`
}

// retryDelays: up to three attempts. Only network errors, 429 and 5xx are retried; a 4xx (bad topic, bad token)
// will not get better by trying again.
var retryDelays = []time.Duration{time.Second, 3 * time.Second}

func (n *Ntfy) Send(ctx context.Context, m Message) error {
	body, _ := json.Marshal(publishBody{
		Topic: n.cfg.Topic, Title: m.Title, Message: m.Body, Priority: m.Priority, Tags: m.Tags, Click: n.cfg.AppURL,
	})
	var err error
	for attempt := 0; ; attempt++ {
		var retry bool
		if retry, err = n.once(ctx, body); err == nil || !retry || attempt >= len(retryDelays) {
			return err
		}
		n.sleep(ctx, retryDelays[attempt])
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
}

func (n *Ntfy) once(ctx context.Context, body []byte) (retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.cfg.ServerURL, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	switch {
	case n.cfg.Token != "":
		req.Header.Set("Authorization", "Bearer "+n.cfg.Token)
	case n.cfg.User != "":
		req.SetBasicAuth(n.cfg.User, n.cfg.Password)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return true, redact(err, n.cfg)
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 == 2 {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		return false, nil
	}
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
	err = fmt.Errorf("ntfy answered %d: %s", resp.StatusCode, strings.TrimSpace(string(detail)))
	return resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500, redact(err, n.cfg)
}

// redact keeps the topic (which works like a password on public ntfy servers) and credentials out of logs and the UI.
func redact(err error, c Config) error {
	s := err.Error()
	for _, secret := range []string{c.Topic, c.Token, c.Password} {
		if secret != "" {
			s = strings.ReplaceAll(s, secret, "***")
		}
	}
	return fmt.Errorf("%s", s)
}
