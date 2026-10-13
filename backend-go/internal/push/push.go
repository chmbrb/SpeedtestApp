// Package push delivers notifications to browsers and installed apps with Web Push (RFC 8030, 8291, 8292).
package push

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	webpush "github.com/SherClockHolmes/webpush-go"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/notify"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/store"
)

// Repo is the persistence the service needs; *store.Store implements it.
type Repo interface {
	VAPID(ctx context.Context) (public, private string, ok bool, err error)
	SaveVAPID(ctx context.Context, public, private string) error
	SaveSubscription(ctx context.Context, sub store.PushSubscription, max int) error
	DeleteSubscription(ctx context.Context, endpoint string) (bool, error)
	Subscription(ctx context.Context, endpoint string) (store.PushSubscription, error)
	ListSubscriptions(ctx context.Context) ([]store.PushSubscription, error)
	MarkPushDelivery(ctx context.Context, id string, ok bool, maxFailures int) (bool, error)
	DeleteSubscriptionByID(ctx context.Context, id string) error
}

// DefaultAllowedHosts are the push services of the major browsers. A subscription's endpoint is an address the server will
// POST to, so only these (and PUSH_ALLOWED_HOSTS additions) are accepted: otherwise anyone who can reach the dashboard could
// make the server send requests to any address on the network. An entry starting with "." matches any subdomain.
var DefaultAllowedHosts = []string{
	"fcm.googleapis.com",         // Chrome, Edge, Brave, Samsung Internet, Opera on Android and desktop
	".push.services.mozilla.com", // Firefox
	".push.apple.com",            // Safari and installed web apps on iOS and macOS
	".notify.windows.com",        // Edge on Windows (WNS)
}

const (
	MaxSubscriptions = 50
	MaxEndpointLen   = 2048 // the longest endpoint accepted anywhere
	maxFailures      = 5
	sendTimeout      = 10 * time.Second
	workers          = 8
)

// Config holds the settings. Zero values get sensible defaults.
type Config struct {
	Subject      string   // who is sending: an https URL or an email address; push services may contact it
	AllowedHosts []string // replaces DefaultAllowedHosts when set
	TTL          time.Duration
	HTTPClient   *http.Client // for tests
}

// ValidationError says which field of a subscription was not acceptable.
type ValidationError struct{ Field, Message string }

func (e *ValidationError) Error() string { return e.Field + " " + e.Message }

var (
	ErrTooMany    = errors.New("too many subscriptions")
	ErrUnknown    = errors.New("this device is not subscribed")
	ErrNotAllowed = errors.New("the push service refused the message")
)

// Service owns the VAPID keys and the list of subscribers, and sends to them.
type Service struct {
	cfg    Config
	repo   Repo
	log    *slog.Logger
	client *http.Client
	pub    string
	priv   string
}

// New loads the VAPID key pair, creating and storing one on first use.
func New(ctx context.Context, cfg Config, repo Repo, log *slog.Logger) (*Service, error) {
	if cfg.Subject == "" {
		cfg.Subject = "speedtest@example.com"
	}
	if len(cfg.AllowedHosts) == 0 {
		cfg.AllowedHosts = DefaultAllowedHosts
	}
	if cfg.TTL <= 0 {
		cfg.TTL = 6 * time.Hour
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: sendTimeout}
	}
	c := *client
	c.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }

	pub, priv, ok, err := repo.VAPID(ctx)
	if err != nil {
		return nil, fmt.Errorf("reading the push keys: %w (has sql/postgres/007_web_push.sql been applied?)", err)
	}
	if !ok {
		priv, pub, err = webpush.GenerateVAPIDKeys()
		if err != nil {
			return nil, err
		}
		if err := repo.SaveVAPID(ctx, pub, priv); err != nil {
			return nil, err
		}
		// If another server saved a pair first, use that one.
		if pub, priv, ok, err = repo.VAPID(ctx); err != nil || !ok {
			return nil, fmt.Errorf("storing the push keys failed: %v", err)
		}
	}
	return &Service{cfg: cfg, repo: repo, log: log, client: &c, pub: pub, priv: priv}, nil
}

// PublicKey is what the browser needs to subscribe (the "application server key").
func (s *Service) PublicKey() string { return s.pub }

// Subscribe stores a subscription after checking it. Subscribing the same endpoint again just refreshes its keys.
func (s *Service) Subscribe(ctx context.Context, endpoint, p256dh, auth, userAgent string) error {
	if err := s.checkEndpoint(endpoint); err != nil {
		return err
	}
	if b, err := decode(p256dh); err != nil || len(b) != 65 || b[0] != 4 {
		return &ValidationError{"keys.p256dh", "must be the browser's 65-byte public key, base64url encoded"}
	}
	if b, err := decode(auth); err != nil || len(b) != 16 {
		return &ValidationError{"keys.auth", "must be the browser's 16-byte secret, base64url encoded"}
	}
	if len(userAgent) > 300 {
		userAgent = userAgent[:300]
	}
	err := s.repo.SaveSubscription(ctx, store.PushSubscription{Endpoint: endpoint, P256dh: p256dh, Auth: auth, UserAgent: userAgent}, MaxSubscriptions)
	if errors.Is(err, store.ErrTooManySubscriptions) {
		return ErrTooMany
	}
	return err
}

// Unsubscribe removes a subscription. Removing one that is not there is fine.
func (s *Service) Unsubscribe(ctx context.Context, endpoint string) error {
	_, err := s.repo.DeleteSubscription(ctx, endpoint)
	return err
}

func (s *Service) checkEndpoint(endpoint string) error {
	bad := func(why string) error { return &ValidationError{"endpoint", why} }
	if endpoint == "" || len(endpoint) > MaxEndpointLen {
		return bad("must be the subscription's URL, at most 2048 characters")
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.Fragment != "" {
		return bad("must be an https URL")
	}
	if !s.hostAllowed(u.Hostname()) {
		return bad("is not a known browser push service")
	}
	return nil
}

func (s *Service) hostAllowed(host string) bool {
	host = strings.ToLower(host)
	for _, a := range s.cfg.AllowedHosts {
		a = strings.ToLower(strings.TrimSpace(a))
		switch {
		case a == "":
		case strings.HasPrefix(a, "."):
			if strings.HasSuffix(host, a) {
				return true
			}
		case host == a:
			return true
		}
	}
	return false
}

func decode(s string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(strings.TrimRight(s, "="))
}

// payload is what the service worker receives.
type payload struct {
	Title string `json:"title"`
	Body  string `json:"body"`
	URL   string `json:"url,omitempty"`
	Test  bool   `json:"test,omitempty"`
}

// Send implements notify.Sender: it delivers the message to every subscriber. It returns notify.ErrNoRecipients when there
// are none, and an error only when every attempt failed.
func (s *Service) Send(ctx context.Context, m notify.Message) error {
	subs, err := s.repo.ListSubscriptions(ctx)
	if err != nil {
		return err
	}
	if len(subs) == 0 {
		return notify.ErrNoRecipients
	}
	urgency := webpush.UrgencyNormal
	if m.Priority >= 4 {
		urgency = webpush.UrgencyHigh
	}
	body, _ := json.Marshal(payload{Title: clip(m.Title, 100), Body: clip(m.Body, 300), URL: "/"})

	var (
		mu        sync.Mutex
		delivered int
		errs      []error
		wg        sync.WaitGroup
		sem       = make(chan struct{}, workers)
	)
	for _, sub := range subs {
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer func() { <-sem; wg.Done() }()
			err := s.deliver(ctx, sub, body, urgency)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				delivered++
			} else {
				errs = append(errs, err)
			}
		}()
	}
	wg.Wait()
	if delivered > 0 {
		if len(errs) > 0 {
			s.log.Warn("some push deliveries failed", "delivered", delivered, "failed", len(errs), "first", errs[0])
		}
		return nil
	}
	return fmt.Errorf("push: %w", errors.Join(errs...))
}

// SendTest sends a short test message to one subscription, so a person can check it works.
func (s *Service) SendTest(ctx context.Context, endpoint string) error {
	sub, err := s.repo.Subscription(ctx, endpoint)
	if errors.Is(err, store.ErrNotFound) {
		return ErrUnknown
	}
	if err != nil {
		return err
	}
	body, _ := json.Marshal(payload{Title: "Notifications are on", Body: "You'll hear about slow or missing speed tests here.", URL: "/", Test: true})
	return s.deliver(ctx, sub, body, webpush.UrgencyHigh)
}

func (s *Service) deliver(ctx context.Context, sub store.PushSubscription, body []byte, urgency webpush.Urgency) error {
	ctx, cancel := context.WithTimeout(ctx, sendTimeout)
	defer cancel()
	// The library pads the message in place, using the spare capacity of the slice it is given, so every delivery gets a copy.
	resp, err := webpush.SendNotificationWithContext(ctx, append([]byte(nil), body...),
		&webpush.Subscription{Endpoint: sub.Endpoint, Keys: webpush.Keys{P256dh: sub.P256dh, Auth: sub.Auth}},
		&webpush.Options{
			HTTPClient: s.client, Subscriber: s.cfg.Subject, VAPIDPublicKey: s.pub, VAPIDPrivateKey: s.priv,
			TTL: int(s.cfg.TTL.Seconds()), Urgency: urgency,
		})
	if err != nil {
		s.mark(ctx, sub, false)
		return redact(err, sub.Endpoint)
	}
	defer resp.Body.Close()
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 300))
	switch {
	case resp.StatusCode/100 == 2:
		s.mark(ctx, sub, true)
		return nil
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone:
		// The browser or the user cancelled it: nothing will ever be delivered there again. The push service's own words
		// ("NotRegistered", "InvalidRegistration", ...) are the only way to tell why, so they go to the log.
		s.log.Warn("push service says the subscription is gone", "status", resp.StatusCode,
			"detail", clip(strings.ReplaceAll(strings.TrimSpace(string(detail)), sub.Endpoint, "<subscription>"), 300))
		if err := s.repo.DeleteSubscriptionByID(context.WithoutCancel(ctx), sub.ID); err != nil {
			s.log.Error("could not remove an expired push subscription", "err", err)
		}
		return fmt.Errorf("%w: the subscription has expired and was removed", ErrNotAllowed)
	default:
		s.mark(ctx, sub, false)
		return fmt.Errorf("%w: the push service answered %d %s", ErrNotAllowed, resp.StatusCode, strings.TrimSpace(string(detail)))
	}
}

func (s *Service) mark(ctx context.Context, sub store.PushSubscription, ok bool) {
	removed, err := s.repo.MarkPushDelivery(context.WithoutCancel(ctx), sub.ID, ok, maxFailures)
	if err != nil {
		s.log.Error("could not record a push delivery", "err", err)
	}
	if removed {
		s.log.Warn("removed a push subscription after repeated failures")
	}
}

// redact keeps the subscription's endpoint (a secret address) out of errors that reach logs and the UI.
func redact(err error, endpoint string) error {
	return errors.New(strings.ReplaceAll(err.Error(), endpoint, "<subscription>"))
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// FromEnv reads the settings. PUSH_ENABLED=no turns browser notifications off. PUSH_SUBJECT is who the server tells the push
// services it is (an https URL or an email address); it defaults to APP_URL when that is https. PUSH_ALLOWED_HOSTS adds
// push-service hosts to the built-in list (comma separated; a leading dot means "and subdomains").
func FromEnv(getenv func(string) string) (cfg Config, enabled bool, err error) {
	switch strings.ToLower(strings.TrimSpace(getenv("PUSH_ENABLED"))) {
	case "", "yes", "true", "1":
		enabled = true
	case "no", "false", "0":
		return cfg, false, nil
	default:
		return cfg, false, errors.New("PUSH_ENABLED must be yes or no")
	}
	cfg.Subject = strings.TrimSpace(getenv("PUSH_SUBJECT"))
	if cfg.Subject == "" {
		if u, perr := url.Parse(strings.TrimSpace(getenv("APP_URL"))); perr == nil && u.Scheme == "https" && u.Host != "" {
			cfg.Subject = u.Scheme + "://" + u.Host
		}
	}
	if extra := strings.TrimSpace(getenv("PUSH_ALLOWED_HOSTS")); extra != "" {
		cfg.AllowedHosts = append([]string{}, DefaultAllowedHosts...)
		for _, h := range strings.Split(extra, ",") {
			if h = strings.TrimSpace(h); h != "" {
				cfg.AllowedHosts = append(cfg.AllowedHosts, h)
			}
		}
	}
	return cfg, true, nil
}
