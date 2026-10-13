package push

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/notify"
	"github.com/chmbrb/SpeedtestApp/backend-go/internal/store"
	"github.com/golang-jwt/jwt/v5"
)

// memRepo is an in-memory Repo.
type memRepo struct {
	mu       sync.Mutex
	pub, prv string
	subs     map[string]*store.PushSubscription // by endpoint
	n        int
}

func newMemRepo() *memRepo { return &memRepo{subs: map[string]*store.PushSubscription{}} }

func (m *memRepo) VAPID(context.Context) (string, string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pub, m.prv, m.pub != "", nil
}
func (m *memRepo) SaveVAPID(_ context.Context, pub, prv string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pub == "" {
		m.pub, m.prv = pub, prv
	}
	return nil
}
func (m *memRepo) SaveSubscription(_ context.Context, s store.PushSubscription, max int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.subs[s.Endpoint]; ok {
		old.P256dh, old.Auth, old.Failures = s.P256dh, s.Auth, 0
		return nil
	}
	if len(m.subs) >= max {
		return store.ErrTooManySubscriptions
	}
	m.n++
	s.ID = "id-" + string(rune('a'+m.n))
	m.subs[s.Endpoint] = &s
	return nil
}
func (m *memRepo) DeleteSubscription(_ context.Context, e string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.subs[e]
	delete(m.subs, e)
	return ok, nil
}
func (m *memRepo) Subscription(_ context.Context, e string) (store.PushSubscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.subs[e]; ok {
		return *s, nil
	}
	return store.PushSubscription{}, store.ErrNotFound
}
func (m *memRepo) ListSubscriptions(context.Context) ([]store.PushSubscription, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []store.PushSubscription
	for _, s := range m.subs {
		out = append(out, *s)
	}
	return out, nil
}
func (m *memRepo) MarkPushDelivery(_ context.Context, id string, ok bool, max int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for e, s := range m.subs {
		if s.ID != id {
			continue
		}
		if ok {
			s.Failures = 0
			return false, nil
		}
		if s.Failures+1 >= max {
			delete(m.subs, e)
			return true, nil
		}
		s.Failures++
	}
	return false, nil
}
func (m *memRepo) DeleteSubscriptionByID(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for e, s := range m.subs {
		if s.ID == id {
			delete(m.subs, e)
		}
	}
	return nil
}

// browser plays the part of a browser: it owns the subscription keys and can read what the push service relays.
type browser struct {
	priv *ecdh.PrivateKey
	auth []byte
}

func newBrowser(t *testing.T) *browser {
	t.Helper()
	k, err := ecdh.P256().GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	a := make([]byte, 16)
	_, _ = rand.Read(a)
	return &browser{priv: k, auth: a}
}

func b64(b []byte) string          { return base64.RawURLEncoding.EncodeToString(b) }
func (b *browser) p256dh() string  { return b64(b.priv.PublicKey().Bytes()) }
func (b *browser) authKey() string { return b64(b.auth) }

// decrypt reads an aes128gcm message (RFC 8188) encrypted for this browser as RFC 8291 describes.
func (b *browser) decrypt(t *testing.T, body []byte) []byte {
	t.Helper()
	if len(body) < 21 {
		t.Fatalf("body too short: %d", len(body))
	}
	salt, rs, idlen := body[:16], binary.BigEndian.Uint32(body[16:20]), int(body[20])
	keyID := body[21 : 21+idlen]
	ct := body[21+idlen:]
	if uint32(len(ct)) > rs {
		t.Fatalf("more than one record (%d > %d)", len(ct), rs)
	}
	asPub, err := ecdh.P256().NewPublicKey(keyID)
	if err != nil {
		t.Fatal(err)
	}
	secret, err := b.priv.ECDH(asPub)
	if err != nil {
		t.Fatal(err)
	}
	prkKey, _ := hkdf.Extract(sha256.New, secret, b.auth)
	keyInfo := append(append([]byte("WebPush: info\x00"), b.priv.PublicKey().Bytes()...), keyID...)
	ikm, _ := hkdf.Expand(sha256.New, prkKey, string(keyInfo), 32)
	prk, _ := hkdf.Extract(sha256.New, ikm, salt)
	cek, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: aes128gcm\x00", 16)
	nonce, _ := hkdf.Expand(sha256.New, prk, "Content-Encoding: nonce\x00", 12)
	blk, _ := aes.NewCipher(cek)
	gcm, _ := cipher.NewGCM(blk)
	pt, err := gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		t.Fatalf("could not decrypt: %v", err)
	}
	pt = []byte(strings.TrimRight(string(pt), "\x00"))
	if len(pt) == 0 || pt[len(pt)-1] != 2 {
		t.Fatalf("missing padding delimiter")
	}
	return pt[:len(pt)-1]
}

// pushService is a stand-in for a browser vendor's push service.
type pushService struct {
	*httptest.Server
	mu     sync.Mutex
	hits   []hit
	status func(path string) int
	body   string // written after the status, when set
}

type hit struct {
	path string
	hdr  http.Header
	body []byte
}

func newPushService(t *testing.T) *pushService {
	t.Helper()
	p := &pushService{}
	p.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		p.mu.Lock()
		p.hits = append(p.hits, hit{r.URL.Path, r.Header.Clone(), b})
		st := 201
		if p.status != nil {
			st = p.status(r.URL.Path)
		}
		body := p.body
		p.mu.Unlock()
		w.WriteHeader(st)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(p.Close)
	return p
}

func (p *pushService) endpoint(path string) string { return p.URL + "/" + path }
func (p *pushService) hitsFor(path string) []hit {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out []hit
	for _, h := range p.hits {
		if h.path == "/"+path {
			out = append(out, h)
		}
	}
	return out
}

func newService(t *testing.T, ps *pushService) (*Service, *memRepo) {
	t.Helper()
	return newServiceLogging(t, ps, io.Discard)
}

func newServiceLogging(t *testing.T, ps *pushService, logTo io.Writer) (*Service, *memRepo) {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(ps.Certificate())
	repo := newMemRepo()
	u, _ := url.Parse(ps.URL)
	svc, err := New(context.Background(), Config{
		Subject:      "https://speedtest.example.org",
		AllowedHosts: []string{u.Hostname()},
		HTTPClient:   &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}, Timeout: 5 * time.Second},
	}, repo, slog.New(slog.NewTextHandler(logTo, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return svc, repo
}

func TestKeysAreCreatedOnceAndReused(t *testing.T) {
	repo := newMemRepo()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := New(context.Background(), Config{}, repo, log)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := New(context.Background(), Config{}, repo, log)
	if a.PublicKey() == "" || a.PublicKey() != b.PublicKey() {
		t.Fatalf("keys differ across starts: %q vs %q", a.PublicKey(), b.PublicKey())
	}
	if raw, err := base64.RawURLEncoding.DecodeString(a.PublicKey()); err != nil || len(raw) != 65 {
		t.Fatalf("public key is not an uncompressed P-256 point: %v", err)
	}
}

func TestSubscribeValidation(t *testing.T) {
	svc, _ := newService(t, newPushService(t))
	br := newBrowser(t)
	defaults, _ := New(context.Background(), Config{}, newMemRepo(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	long := "https://fcm.googleapis.com/fcm/send/" + strings.Repeat("a", 2100)
	cases := []struct {
		name, endpoint, p256dh, auth, field string
		svc                                 *Service
	}{
		{"http endpoint", "http://fcm.googleapis.com/x", br.p256dh(), br.authKey(), "endpoint", defaults},
		{"unknown host", "https://example.com/x", br.p256dh(), br.authKey(), "endpoint", defaults},
		{"internal host", "https://192.168.0.7/x", br.p256dh(), br.authKey(), "endpoint", defaults},
		{"look-alike host", "https://fcm.googleapis.com.evil.example/x", br.p256dh(), br.authKey(), "endpoint", defaults},
		{"credentials in url", "https://user:pw@fcm.googleapis.com/x", br.p256dh(), br.authKey(), "endpoint", defaults},
		{"empty", "", br.p256dh(), br.authKey(), "endpoint", defaults},
		{"too long", long, br.p256dh(), br.authKey(), "endpoint", defaults},
		{"short p256dh", "https://fcm.googleapis.com/x", "AAAA", br.authKey(), "keys.p256dh", defaults},
		{"p256dh not base64", "https://fcm.googleapis.com/x", "!!!!", br.authKey(), "keys.p256dh", defaults},
		{"short auth", "https://fcm.googleapis.com/x", br.p256dh(), "AAAA", "keys.auth", defaults},
		{"missing auth", "https://fcm.googleapis.com/x", br.p256dh(), "", "keys.auth", defaults},
	}
	for _, c := range cases {
		err := c.svc.Subscribe(context.Background(), c.endpoint, c.p256dh, c.auth, "ua")
		var ve *ValidationError
		if !errors.As(err, &ve) || ve.Field != c.field {
			t.Errorf("%s: err = %v, want a validation error on %s", c.name, err, c.field)
		}
	}
	// the real services' hosts are accepted
	for _, ep := range []string{
		"https://fcm.googleapis.com/fcm/send/abc",
		"https://updates.push.services.mozilla.com/wpush/v2/abc",
		"https://web.push.apple.com/abc",
		"https://wns2-par02p.notify.windows.com/w/?token=abc",
	} {
		if err := defaults.Subscribe(context.Background(), ep, br.p256dh(), br.authKey(), "ua"); err != nil {
			t.Errorf("%s rejected: %v", ep, err)
		}
	}
	_ = svc
}

func TestSubscriptionLimitAndRefresh(t *testing.T) {
	ps := newPushService(t)
	svc, repo := newService(t, ps)
	br := newBrowser(t)
	for i := 0; i < MaxSubscriptions; i++ {
		if err := svc.Subscribe(context.Background(), ps.endpoint("dev"+string(rune('A'+i%26))+string(rune('a'+i/26))), br.p256dh(), br.authKey(), ""); err != nil {
			t.Fatalf("subscription %d: %v", i, err)
		}
	}
	if err := svc.Subscribe(context.Background(), ps.endpoint("one-too-many"), br.p256dh(), br.authKey(), ""); !errors.Is(err, ErrTooMany) {
		t.Errorf("over the limit: %v", err)
	}
	// an existing endpoint can still be refreshed at the limit
	if err := svc.Subscribe(context.Background(), ps.endpoint("devAa"), br.p256dh(), br.authKey(), ""); err != nil {
		t.Errorf("refresh at the limit: %v", err)
	}
	if len(repo.subs) != MaxSubscriptions {
		t.Errorf("subscriptions = %d", len(repo.subs))
	}
}

func TestSendDeliversAnEncryptedMessageThePhoneCanRead(t *testing.T) {
	ps := newPushService(t)
	svc, _ := newService(t, ps)
	br := newBrowser(t)
	if err := svc.Subscribe(context.Background(), ps.endpoint("phone"), br.p256dh(), br.authKey(), "Mozilla/5.0 (test)"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Send(context.Background(), notify.Message{Title: "Downloads are slow", Body: "server (wired) is at 40% of normal", Priority: 4}); err != nil {
		t.Fatal(err)
	}
	hits := ps.hitsFor("phone")
	if len(hits) != 1 {
		t.Fatalf("hits = %d", len(hits))
	}
	h := hits[0]
	if h.hdr.Get("Content-Encoding") != "aes128gcm" || h.hdr.Get("TTL") != "21600" || h.hdr.Get("Urgency") != "high" {
		t.Errorf("headers: %v", h.hdr)
	}
	var got payload
	if err := json.Unmarshal(br.decrypt(t, h.body), &got); err != nil {
		t.Fatal(err)
	}
	if got.Title != "Downloads are slow" || got.Body != "server (wired) is at 40% of normal" || got.URL != "/" || got.Test {
		t.Errorf("payload: %+v", got)
	}

	// The VAPID header is a signed token for the push service's origin, made by the key the browser was told about.
	auth := h.hdr.Get("Authorization")
	parts := strings.Split(strings.TrimPrefix(auth, "vapid "), ", ")
	if len(parts) != 2 || !strings.HasPrefix(parts[0], "t=") || parts[1] != "k="+svc.PublicKey() {
		t.Fatalf("Authorization = %q", auth)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(svc.PublicKey())
	x, y := elliptic.Unmarshal(elliptic.P256(), raw) //nolint:staticcheck // only to verify the token in a test
	pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: x, Y: y}
	tok, err := jwt.Parse(strings.TrimPrefix(parts[0], "t="), func(*jwt.Token) (any, error) { return pub, nil }, jwt.WithValidMethods([]string{"ES256"}))
	if err != nil {
		t.Fatalf("VAPID token does not verify: %v", err)
	}
	claims := tok.Claims.(jwt.MapClaims)
	if claims["aud"] != ps.URL || claims["sub"] != "https://speedtest.example.org" {
		t.Errorf("claims: %v", claims)
	}
}

func TestLowPriorityIsNormalUrgencyAndLongTextIsClipped(t *testing.T) {
	ps := newPushService(t)
	svc, _ := newService(t, ps)
	br := newBrowser(t)
	_ = svc.Subscribe(context.Background(), ps.endpoint("p"), br.p256dh(), br.authKey(), "")
	if err := svc.Send(context.Background(), notify.Message{Title: "t", Body: strings.Repeat("é", 1000), Priority: 3}); err != nil {
		t.Fatal(err)
	}
	h := ps.hitsFor("p")[0]
	if h.hdr.Get("Urgency") != "normal" {
		t.Errorf("urgency %q", h.hdr.Get("Urgency"))
	}
	var got payload
	_ = json.Unmarshal(br.decrypt(t, h.body), &got)
	if n := len([]rune(got.Body)); n != 300 || !strings.HasSuffix(got.Body, "…") {
		t.Errorf("body not clipped: %d runes", n)
	}
}

func TestNoSubscribersIsNotADelivery(t *testing.T) {
	svc, _ := newService(t, newPushService(t))
	if err := svc.Send(context.Background(), notify.Message{Title: "x"}); !errors.Is(err, notify.ErrNoRecipients) {
		t.Errorf("err = %v", err)
	}
}

func TestExpiredSubscriptionsAreRemoved(t *testing.T) {
	for _, status := range []int{404, 410} {
		ps := newPushService(t)
		ps.status = func(path string) int {
			if path == "/gone" {
				return status
			}
			return 201
		}
		svc, repo := newService(t, ps)
		a, b := newBrowser(t), newBrowser(t)
		_ = svc.Subscribe(context.Background(), ps.endpoint("gone"), a.p256dh(), a.authKey(), "")
		_ = svc.Subscribe(context.Background(), ps.endpoint("alive"), b.p256dh(), b.authKey(), "")
		// One working subscriber is enough for the alert to count as delivered.
		if err := svc.Send(context.Background(), notify.Message{Title: "x"}); err != nil {
			t.Fatalf("status %d: %v", status, err)
		}
		if _, ok := repo.subs[ps.endpoint("gone")]; ok {
			t.Errorf("status %d: the expired subscription was kept", status)
		}
		if _, ok := repo.subs[ps.endpoint("alive")]; !ok {
			t.Errorf("status %d: the working subscription was removed", status)
		}
	}
}

func TestGoneSubscriptionLogsWhatThePushServiceSaid(t *testing.T) {
	ps := newPushService(t)
	ps.status = func(string) int { return 410 }
	br := newBrowser(t)
	ep := ps.endpoint("gone")
	ps.body = "NotRegistered " + ep
	var logs strings.Builder
	svc, _ := newServiceLogging(t, ps, &logs)
	_ = svc.Subscribe(context.Background(), ep, br.p256dh(), br.authKey(), "")
	if err := svc.SendTest(context.Background(), ep); !errors.Is(err, ErrNotAllowed) {
		t.Fatalf("err = %v", err)
	}
	out := logs.String()
	if !strings.Contains(out, "status=410") || !strings.Contains(out, "NotRegistered") {
		t.Errorf("the log lacks the status or the push service's answer: %s", out)
	}
	if strings.Contains(out, ep) {
		t.Errorf("the log leaks the endpoint: %s", out)
	}
}

func TestRepeatedFailuresRemoveASubscriptionAndAreReportedWithoutTheEndpoint(t *testing.T) {
	ps := newPushService(t)
	ps.status = func(string) int { return 503 }
	svc, repo := newService(t, ps)
	br := newBrowser(t)
	ep := ps.endpoint("flaky")
	_ = svc.Subscribe(context.Background(), ep, br.p256dh(), br.authKey(), "")
	for i := 1; i <= maxFailures; i++ {
		err := svc.Send(context.Background(), notify.Message{Title: "x"})
		if err == nil || !strings.Contains(err.Error(), "503") {
			t.Fatalf("attempt %d: %v", i, err)
		}
		if strings.Contains(err.Error(), ep) {
			t.Errorf("the error leaks the endpoint: %v", err)
		}
		_, still := repo.subs[ep]
		if still == (i == maxFailures) {
			t.Fatalf("after %d failures present=%v", i, still)
		}
	}
}

func TestOneSuccessResetsTheFailureCount(t *testing.T) {
	ps := newPushService(t)
	fail := true
	ps.status = func(string) int {
		if fail {
			return 500
		}
		return 201
	}
	svc, repo := newService(t, ps)
	br := newBrowser(t)
	ep := ps.endpoint("p")
	_ = svc.Subscribe(context.Background(), ep, br.p256dh(), br.authKey(), "")
	for i := 0; i < maxFailures-1; i++ {
		_ = svc.Send(context.Background(), notify.Message{Title: "x"})
	}
	fail = false
	if err := svc.Send(context.Background(), notify.Message{Title: "x"}); err != nil {
		t.Fatal(err)
	}
	if repo.subs[ep].Failures != 0 {
		t.Errorf("failures = %d", repo.subs[ep].Failures)
	}
}

func TestRedirectsAreNotFollowed(t *testing.T) {
	target := newPushService(t)
	redirector := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/stolen", http.StatusTemporaryRedirect)
	}))
	defer redirector.Close()
	pool := x509.NewCertPool()
	pool.AddCert(redirector.Certificate())
	pool.AddCert(target.Certificate())
	u, _ := url.Parse(redirector.URL)
	svc, err := New(context.Background(), Config{AllowedHosts: []string{u.Hostname()},
		HTTPClient: &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool}}}},
		newMemRepo(), slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	br := newBrowser(t)
	_ = svc.Subscribe(context.Background(), redirector.URL+"/x", br.p256dh(), br.authKey(), "")
	if err := svc.Send(context.Background(), notify.Message{Title: "x"}); err == nil {
		t.Error("a redirect was treated as success")
	}
	if len(target.hitsFor("stolen")) != 0 {
		t.Error("the redirect was followed")
	}
}

func TestSendTest(t *testing.T) {
	ps := newPushService(t)
	svc, _ := newService(t, ps)
	br := newBrowser(t)
	_ = svc.Subscribe(context.Background(), ps.endpoint("me"), br.p256dh(), br.authKey(), "")
	other := newBrowser(t)
	_ = svc.Subscribe(context.Background(), ps.endpoint("someone-else"), other.p256dh(), other.authKey(), "")

	if err := svc.SendTest(context.Background(), ps.endpoint("me")); err != nil {
		t.Fatal(err)
	}
	var got payload
	_ = json.Unmarshal(br.decrypt(t, ps.hitsFor("me")[0].body), &got)
	if !got.Test || got.Title == "" {
		t.Errorf("payload: %+v", got)
	}
	if len(ps.hitsFor("someone-else")) != 0 {
		t.Error("a test went to another device")
	}
	if err := svc.SendTest(context.Background(), ps.endpoint("never-subscribed")); !errors.Is(err, ErrUnknown) {
		t.Errorf("unknown endpoint: %v", err)
	}
}

func TestUnsubscribeIsIdempotent(t *testing.T) {
	ps := newPushService(t)
	svc, repo := newService(t, ps)
	br := newBrowser(t)
	_ = svc.Subscribe(context.Background(), ps.endpoint("p"), br.p256dh(), br.authKey(), "")
	for i := 0; i < 2; i++ {
		if err := svc.Unsubscribe(context.Background(), ps.endpoint("p")); err != nil {
			t.Fatal(err)
		}
	}
	if len(repo.subs) != 0 {
		t.Error("still subscribed")
	}
}

func TestFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	if _, on, err := FromEnv(env(nil)); err != nil || !on {
		t.Errorf("default: on=%v err=%v", on, err)
	}
	if _, on, _ := FromEnv(env(map[string]string{"PUSH_ENABLED": "no"})); on {
		t.Error("PUSH_ENABLED=no left push on")
	}
	if _, _, err := FromEnv(env(map[string]string{"PUSH_ENABLED": "maybe"})); err == nil {
		t.Error("PUSH_ENABLED=maybe accepted")
	}
	c, _, _ := FromEnv(env(map[string]string{"APP_URL": "https://192.168.0.7:8443/some/path"}))
	if c.Subject != "https://192.168.0.7:8443" {
		t.Errorf("subject from APP_URL: %q", c.Subject)
	}
	c, _, _ = FromEnv(env(map[string]string{"APP_URL": "http://192.168.0.7:8080"}))
	if c.Subject != "" {
		t.Errorf("subject from an http APP_URL: %q", c.Subject)
	}
	c, _, _ = FromEnv(env(map[string]string{"PUSH_ALLOWED_HOSTS": " push.example.org , .corp.example "}))
	if len(c.AllowedHosts) != len(DefaultAllowedHosts)+2 {
		t.Errorf("allowed hosts: %v", c.AllowedHosts)
	}
}

// Regression: the webpush library pads a message in place, so a body shared between concurrent deliveries was corrupted.
func TestManySubscribersAllReceiveTheSameReadableMessage(t *testing.T) {
	ps := newPushService(t)
	svc, _ := newService(t, ps)
	var browsers []*browser
	for i := 0; i < 30; i++ {
		b := newBrowser(t)
		browsers = append(browsers, b)
		if err := svc.Subscribe(context.Background(), ps.endpoint("b"+string(rune('a'+i%26))+string(rune('a'+i/26))), b.p256dh(), b.authKey(), ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := svc.Send(context.Background(), notify.Message{Title: "Packet loss", Body: "server (wired) is losing 12% of packets"}); err != nil {
		t.Fatal(err)
	}
	for i, b := range browsers {
		hits := ps.hitsFor("b" + string(rune('a'+i%26)) + string(rune('a'+i/26)))
		if len(hits) != 1 {
			t.Fatalf("browser %d: %d deliveries", i, len(hits))
		}
		var got payload
		if err := json.Unmarshal(b.decrypt(t, hits[0].body), &got); err != nil || got.Title != "Packet loss" {
			t.Fatalf("browser %d got %+v (%v)", i, got, err)
		}
	}
}
