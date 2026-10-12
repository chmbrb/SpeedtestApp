package notify

import (
	"context"
	"errors"
)

// ErrNoRecipients is what a channel returns when nobody is listening on it (for example no browser has turned on push). It is
// neither a delivery nor a failure.
var ErrNoRecipients = errors.New("no recipients")

type fanout []Sender

// Fanout sends each message through every sender. The message counts as delivered when at least one channel delivered it, so
// an alert is not repeated on the channels that worked just because another one is down. It fails only when nothing was
// delivered; the error then names every channel that failed.
func Fanout(senders ...Sender) Sender { return fanout(senders) }

func (f fanout) Send(ctx context.Context, m Message) error {
	var delivered int
	var errs []error
	for _, s := range f {
		switch err := s.Send(ctx, m); {
		case err == nil:
			delivered++
		case errors.Is(err, ErrNoRecipients):
		default:
			errs = append(errs, err)
		}
	}
	switch {
	case delivered > 0:
		return nil
	case len(errs) > 0:
		return errors.Join(errs...)
	}
	return ErrNoRecipients
}
