package notify

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stub struct {
	err   error
	calls int
}

func (s *stub) Send(context.Context, Message) error { s.calls++; return s.err }

func TestFanout(t *testing.T) {
	boom, down := errors.New("ntfy is down"), errors.New("push is down")
	cases := []struct {
		name     string
		errs     []error
		wantNil  bool
		wantNone bool
		contains []string
	}{
		{"all delivered", []error{nil, nil}, true, false, nil},
		{"one channel is enough", []error{boom, nil}, true, false, nil},
		{"nobody listening on the other channel", []error{nil, ErrNoRecipients}, true, false, nil},
		{"both fail", []error{boom, down}, false, false, []string{"ntfy is down", "push is down"}},
		{"one fails, the other has no recipients", []error{boom, ErrNoRecipients}, false, false, []string{"ntfy is down"}},
		{"nobody anywhere", []error{ErrNoRecipients, ErrNoRecipients}, false, true, nil},
	}
	for _, c := range cases {
		var senders []Sender
		var stubs []*stub
		for _, e := range c.errs {
			s := &stub{err: e}
			stubs = append(stubs, s)
			senders = append(senders, s)
		}
		err := Fanout(senders...).Send(context.Background(), Message{Title: "x"})
		switch {
		case c.wantNil && err != nil:
			t.Errorf("%s: err = %v", c.name, err)
		case c.wantNone && !errors.Is(err, ErrNoRecipients):
			t.Errorf("%s: err = %v", c.name, err)
		case !c.wantNil && !c.wantNone && err == nil:
			t.Errorf("%s: expected an error", c.name)
		}
		for _, want := range c.contains {
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Errorf("%s: %v does not mention %q", c.name, err, want)
			}
		}
		for i, s := range stubs {
			if s.calls != 1 {
				t.Errorf("%s: sender %d called %d times (every channel is tried once)", c.name, i, s.calls)
			}
		}
	}
}
