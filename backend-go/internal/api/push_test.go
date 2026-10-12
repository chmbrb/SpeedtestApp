package api

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/push"
)

type fakePusher struct {
	subscribed []string
	removed    []string
	tested     []string
	err        error
}

func (f *fakePusher) PublicKey() string { return "BPublicKey" }
func (f *fakePusher) Subscribe(_ context.Context, endpoint, p, a, ua string) error {
	if f.err != nil {
		return f.err
	}
	f.subscribed = append(f.subscribed, endpoint+"|"+p+"|"+a+"|"+ua)
	return nil
}
func (f *fakePusher) Unsubscribe(_ context.Context, e string) error {
	f.removed = append(f.removed, e)
	return f.err
}
func (f *fakePusher) SendTest(_ context.Context, e string) error {
	f.tested = append(f.tested, e)
	return f.err
}

const sub = `{"endpoint":"https://fcm.googleapis.com/fcm/send/abc","expirationTime":null,"keys":{"p256dh":"PK","auth":"AK"}}`

func TestPushIsOffByDefault(t *testing.T) {
	s := newTestServer(&fakeStore{})
	rec := do(t, s, "GET", "/api/v1/push/config", "", "")
	var cfg pushConfig
	_ = json.Unmarshal(rec.Body.Bytes(), &cfg)
	if rec.Code != 200 || cfg.Enabled || cfg.PublicKey != "" {
		t.Errorf("config: %d %+v", rec.Code, cfg)
	}
	for _, c := range [][3]string{{"POST", "/api/v1/push/subscriptions", sub}, {"DELETE", "/api/v1/push/subscriptions", sub}, {"POST", "/api/v1/push/test", sub}} {
		if rec := do(t, s, c[0], c[1], "", c[2]); rec.Code != 503 {
			t.Errorf("%s %s with push off: %d", c[0], c[1], rec.Code)
		}
	}
}

func TestPushConfigGivesThePublicKey(t *testing.T) {
	s := newTestServer(&fakeStore{})
	s.SetPush(&fakePusher{})
	var cfg pushConfig
	_ = json.Unmarshal(do(t, s, "GET", "/api/v1/push/config", "", "").Body.Bytes(), &cfg)
	if !cfg.Enabled || cfg.PublicKey != "BPublicKey" {
		t.Errorf("%+v", cfg)
	}
}

func TestPushSubscribeUnsubscribeAndTest(t *testing.T) {
	fp := &fakePusher{}
	s := newTestServer(&fakeStore{})
	s.SetPush(fp)
	if rec := do(t, s, "POST", "/api/v1/push/subscriptions", "", sub); rec.Code != 204 || rec.Body.Len() != 0 {
		t.Fatalf("subscribe: %d %s", rec.Code, rec.Body)
	}
	if len(fp.subscribed) != 1 || fp.subscribed[0] != "https://fcm.googleapis.com/fcm/send/abc|PK|AK|" {
		t.Errorf("subscribed: %v", fp.subscribed)
	}
	if rec := do(t, s, "POST", "/api/v1/push/test", "", `{"endpoint":"https://x/y"}`); rec.Code != 204 || len(fp.tested) != 1 {
		t.Errorf("test: %d %v", rec.Code, fp.tested)
	}
	if rec := do(t, s, "DELETE", "/api/v1/push/subscriptions", "", `{"endpoint":"https://x/y"}`); rec.Code != 204 || len(fp.removed) != 1 {
		t.Errorf("unsubscribe: %d %v", rec.Code, fp.removed)
	}
}

func TestPushRequestsAreChecked(t *testing.T) {
	s := newTestServer(&fakeStore{})
	s.SetPush(&fakePusher{})
	s.SetIngestRate(0) // this test is about validation, and makes more calls than the rate limits allow
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/v1/push/subscriptions", ``},
		{"POST", "/api/v1/push/subscriptions", `not json`},
		{"POST", "/api/v1/push/subscriptions", `[]`},
		{"DELETE", "/api/v1/push/subscriptions", `{}`},
		{"DELETE", "/api/v1/push/subscriptions", `{"endpoint":""}`},
		{"POST", "/api/v1/push/test", `{"endpoint":5}`},
		{"POST", "/api/v1/push/test", `null`},
	} {
		if rec := do(t, s, c.method, c.path, "", c.body); rec.Code != 400 {
			t.Errorf("%s %s %q: %d", c.method, c.path, c.body, rec.Code)
		}
	}
	tooLong := `{"endpoint":"` + strings.Repeat("a", 2049) + `"}`
	for _, c := range [][2]string{{"DELETE", "/api/v1/push/subscriptions"}, {"POST", "/api/v1/push/test"}} {
		if rec := do(t, s, c[0], c[1], "", tooLong); rec.Code != 400 {
			t.Errorf("%s %s with a 2049-character endpoint: %d", c[0], c[1], rec.Code)
		}
	}
	for _, c := range [][2]string{{"DELETE", "/api/v1/push/subscriptions"}, {"POST", "/api/v1/push/test"}} {
		if rec := do(t, s, c[0], c[1], "", `{"endpoint":"https://x/\u0000y"}`); rec.Code != 400 {
			t.Errorf("%s %s with a NUL byte in the endpoint: %d", c[0], c[1], rec.Code)
		}
	}
	big := `{"endpoint":"https://fcm.googleapis.com/` + string(make([]byte, 6000)) + `"}`
	if rec := do(t, s, "POST", "/api/v1/push/subscriptions", "", big); rec.Code != 400 {
		t.Errorf("oversized body: %d", rec.Code)
	}
}

func TestPushErrorsBecomeTheRightResponses(t *testing.T) {
	cases := []struct {
		err    error
		status int
		field  string
	}{
		{&push.ValidationError{Field: "endpoint", Message: "must be an https URL"}, 400, "endpoint"},
		{push.ErrTooMany, 409, ""},
		{push.ErrUnknown, 404, ""},
		{push.ErrNotAllowed, 502, ""},
		{errors.New("database is down"), 500, ""},
	}
	for _, c := range cases {
		s := newTestServer(&fakeStore{})
		s.SetPush(&fakePusher{err: c.err})
		rec := do(t, s, "POST", "/api/v1/push/subscriptions", "", sub)
		if rec.Code != c.status {
			t.Errorf("%v: %d, want %d", c.err, rec.Code, c.status)
			continue
		}
		var p Problem
		_ = json.Unmarshal(rec.Body.Bytes(), &p)
		if c.field != "" && (len(p.Errors) != 1 || p.Errors[0].Field != c.field) {
			t.Errorf("%v: field errors %+v", c.err, p.Errors)
		}
		if c.status == 500 && p.Detail != "internal error" {
			t.Errorf("an internal error leaked detail: %q", p.Detail)
		}
	}
	// the test endpoint maps "not subscribed" to 404 as well
	s := newTestServer(&fakeStore{})
	s.SetPush(&fakePusher{err: push.ErrUnknown})
	if rec := do(t, s, "POST", "/api/v1/push/test", "", `{"endpoint":"https://x"}`); rec.Code != 404 {
		t.Errorf("test for an unknown device: %d", rec.Code)
	}
}

func TestPushRateLimits(t *testing.T) {
	s := newTestServer(&fakeStore{})
	s.SetPush(&fakePusher{})
	var last int
	for i := 0; i < 11; i++ {
		last = do(t, s, "POST", "/api/v1/push/subscriptions", "", sub).Code
	}
	if last != 429 {
		t.Errorf("11th subscription in a burst: %d", last)
	}
	rec := do(t, s, "POST", "/api/v1/push/subscriptions", "", sub)
	if rec.Header().Get("Retry-After") == "" {
		t.Error("no Retry-After on a 429")
	}
	s = newTestServer(&fakeStore{})
	s.SetPush(&fakePusher{})
	for i := 0; i < 3; i++ {
		if c := do(t, s, "POST", "/api/v1/push/test", "", `{"endpoint":"https://x"}`).Code; c != 204 {
			t.Fatalf("test %d: %d", i, c)
		}
	}
	if c := do(t, s, "POST", "/api/v1/push/test", "", `{"endpoint":"https://x"}`).Code; c != 429 {
		t.Errorf("4th test in a burst: %d", c)
	}
}
