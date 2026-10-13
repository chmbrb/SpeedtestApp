package api

import (
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/login"
)

const testPassword = "open sesame 123"

func testKey() []byte {
	k := make([]byte, 32)
	for i := range k {
		k[i] = byte(i + 1)
	}
	return k
}

func loginService(t *testing.T, password string) *login.Service {
	t.Helper()
	svc, err := login.NewWithKey(login.Config{Password: password, Lifetime: 48 * time.Hour}, testKey())
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// loginServer is the usual test server with the login turned on.
func loginServer(t *testing.T) *Server {
	t.Helper()
	s := newTestServer(&fakeStore{})
	s.SetLogin(loginService(t, testPassword))
	return s
}

type opts struct {
	cookie string
	header map[string]string
	body   string
	ip     string
}

func call(t *testing.T, s *Server, method, target string, o opts) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(o.body))
	if o.cookie != "" {
		req.AddCookie(&http.Cookie{Name: login.CookieName, Value: o.cookie})
	}
	for k, v := range o.header {
		req.Header.Set(k, v)
	}
	if o.ip != "" {
		req.RemoteAddr = o.ip + ":4000"
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec
}

func signIn(t *testing.T, s *Server) string {
	t.Helper()
	rec := call(t, s, "POST", "/api/v1/auth/login", opts{body: `{"password":"` + testPassword + `"}`})
	if rec.Code != 204 {
		t.Fatalf("login: %d %s", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == login.CookieName {
			return c.Value
		}
	}
	t.Fatal("login set no session cookie")
	return ""
}

func sessionState(t *testing.T, s *Server, cookie string) (required, authenticated bool) {
	t.Helper()
	rec := call(t, s, "GET", "/api/v1/auth/session", opts{cookie: cookie})
	var out struct{ Required, Authenticated bool }
	if rec.Code != 200 || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
		t.Fatalf("session: %d %s", rec.Code, rec.Body)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("session Cache-Control = %q", cc)
	}
	return out.Required, out.Authenticated
}

func TestWithoutALoginNothingChanges(t *testing.T) {
	s := newTestServer(&fakeStore{})
	if req, authed := sessionState(t, s, ""); req || !authed {
		t.Errorf("session = required %v authenticated %v", req, authed)
	}
	if rec := call(t, s, "GET", "/api/v1/devices", opts{}); rec.Code != 200 {
		t.Errorf("devices without a login: %d", rec.Code)
	}
	if rec := call(t, s, "POST", "/api/v1/auth/login", opts{body: `{"password":"x"}`}); rec.Code != 404 {
		t.Errorf("login with the login off: %d", rec.Code)
	} else {
		problem(t, rec)
	}
	if rec := call(t, s, "POST", "/api/v1/auth/logout", opts{}); rec.Code != 204 {
		t.Errorf("logout: %d", rec.Code)
	}
}

func TestEverythingNeedsASessionExceptWhatMustBePublic(t *testing.T) {
	s := loginServer(t)
	if req, authed := sessionState(t, s, ""); !req || authed {
		t.Errorf("session = required %v authenticated %v", req, authed)
	}
	for _, rt := range []struct{ method, path string }{
		{"GET", "/api/v1/devices"}, {"GET", "/api/v1/results"}, {"GET", "/api/v1/results/11111111-1111-1111-1111-111111111111"},
		{"GET", "/api/v1/stats/summary"}, {"GET", "/api/v1/stats/timeseries"}, {"GET", "/api/v1/notifications"},
		{"GET", "/api/v1/capabilities"}, {"GET", "/api/v1/push/config"}, {"POST", "/api/v1/push/subscriptions"},
		{"DELETE", "/api/v1/push/subscriptions"}, {"POST", "/api/v1/push/test"}, {"GET", "/api/v1/tests"},
		{"POST", "/api/v1/tests"}, {"GET", "/api/v1/tests/abc"}, {"DELETE", "/api/v1/tests/abc"},
		{"GET", "/api/v1/tests/abc/events"}, {"HEAD", "/api/v1/devices"},
		// paths that match nothing are refused too, so the answer does not say what exists
		{"GET", "/api/v1/nothing"}, {"POST", "/api/v1/devices"}, {"GET", "/api/v1/health/"}, {"GET", "/api/v1/auth/login"},
		{"GET", "/api/v1/tests/a/b/result"}, {"POST", "/api/v1/tests//result"},
	} {
		rec := call(t, s, rt.method, rt.path, opts{})
		if rec.Code != 401 {
			t.Errorf("%s %s without a session: %d, want 401", rt.method, rt.path, rec.Code)
			continue
		}
		if rt.method != "HEAD" {
			if p := problem(t, rec); p.Detail != "login required" {
				t.Errorf("%s %s: detail %q", rt.method, rt.path, p.Detail)
			}
		}
		if rec.Header().Get("Cache-Control") != "no-store" {
			t.Errorf("%s %s: a refusal must not be cached", rt.method, rt.path)
		}
	}
	if rec := call(t, s, "GET", "/api/v1/health", opts{}); rec.Code != 200 {
		t.Errorf("health: %d (container health checks need it open)", rec.Code)
	}
	// the built frontend must load, or nobody could see the login page
	s.ServeStatic(fstest.MapFS{"index.html": {Data: []byte("<html>app</html>")}})
	if rec := call(t, s, "GET", "/", opts{}); rec.Code != 200 || !strings.Contains(rec.Body.String(), "app") {
		t.Errorf("the page itself: %d", rec.Code)
	}
}

func TestSignedInRequestsPass(t *testing.T) {
	s := loginServer(t)
	cookie := signIn(t, s)
	for _, path := range []string{"/api/v1/devices", "/api/v1/results", "/api/v1/stats/summary", "/api/v1/notifications", "/api/v1/capabilities"} {
		if rec := call(t, s, "GET", path, opts{cookie: cookie}); rec.Code != 200 {
			t.Errorf("GET %s signed in: %d %s", path, rec.Code, rec.Body)
		}
	}
	if req, authed := sessionState(t, s, cookie); !req || !authed {
		t.Errorf("session = required %v authenticated %v", req, authed)
	}
}

func TestOwnTokenRoutesDoNotNeedASession(t *testing.T) {
	fs := &fakeStore{}
	s := newTestServer(fs)
	s.SetLogin(loginService(t, testPassword))
	body := envelope("ookla", "wired", goodOokla)
	// a collector with its device token
	if rec := call(t, s, "POST", "/api/v1/results", opts{header: map[string]string{"Authorization": "Bearer tok-laptop"}, body: body}); rec.Code != 201 {
		t.Errorf("ingest with a token: %d %s", rec.Code, rec.Body)
	}
	// a wrong or missing token is answered by the ingest endpoint itself, not by the login gate
	rec := call(t, s, "POST", "/api/v1/results", opts{header: map[string]string{"Authorization": "Bearer nope"}, body: body})
	if p := problem(t, rec); rec.Code != 401 || p.Detail == "login required" || rec.Header().Get("WWW-Authenticate") != "Bearer" {
		t.Errorf("wrong token: %d %+v", rec.Code, p)
	}
	rec = call(t, s, "POST", "/api/v1/results", opts{body: body})
	if p := problem(t, rec); rec.Code != 401 || p.Detail == "login required" {
		t.Errorf("no token: %d %+v", rec.Code, p)
	}
	// a browser submitting the result of its own test authenticates with the test's single-use token
	rec = call(t, s, "POST", "/api/v1/tests/abc/result", opts{})
	if p := problem(t, rec); p.Detail == "login required" {
		t.Errorf("submit result went through the login gate: %d %+v", rec.Code, p)
	}
	// starting a test: a session, or a bearer token for the handler to check, but not nothing
	rec = call(t, s, "POST", "/api/v1/tests", opts{})
	if p := problem(t, rec); rec.Code != 401 || p.Detail != "login required" {
		t.Errorf("anonymous test start: %d %+v", rec.Code, p)
	}
	rec = call(t, s, "POST", "/api/v1/tests", opts{header: map[string]string{"Authorization": "Bearer tok-laptop"}})
	if p := problem(t, rec); p.Detail == "login required" {
		t.Errorf("a collector starting a test was stopped by the login gate: %d %+v", rec.Code, p)
	}
}

func TestLoginRefusals(t *testing.T) {
	s := loginServer(t)
	s.SetIngestRate(0) // this test makes more attempts than the limit allows; the limit has its own tests
	for _, body := range []string{`{"password":"wrong"}`, `{"password":""}`, `{"password":"` + testPassword + ` "}`} {
		rec := call(t, s, "POST", "/api/v1/auth/login", opts{body: body})
		if p := problem(t, rec); rec.Code != 401 || p.Detail != "incorrect password" || len(rec.Result().Cookies()) != 0 {
			t.Errorf("%s: %d %+v cookies=%v", body, rec.Code, p, rec.Result().Cookies())
		}
	}
	for _, body := range []string{``, `not json`, `{}`, `{"password":null}`, `{"password":5}`, `[]`, `{"password":"` + strings.Repeat("x", 5000) + `"}`} {
		rec := call(t, s, "POST", "/api/v1/auth/login", opts{body: body})
		if problem(t, rec); rec.Code != 400 {
			t.Errorf("body %.30q: %d, want 400", body, rec.Code)
		}
	}
}

func TestSessionCookieAttributes(t *testing.T) {
	s := loginServer(t)
	rec := call(t, s, "POST", "/api/v1/auth/login", opts{body: `{"password":"` + testPassword + `"}`})
	cs := rec.Result().Cookies()
	if len(cs) != 1 {
		t.Fatalf("cookies: %v", cs)
	}
	c := cs[0]
	if !c.HttpOnly || c.SameSite != http.SameSiteStrictMode || c.Path != "/" || c.MaxAge != 48*3600 || c.Secure {
		t.Errorf("cookie = %+v (over plain http it must not be Secure, or the browser would drop it)", c)
	}
	if d := time.Until(c.Expires); d < 47*time.Hour || d > 49*time.Hour {
		t.Errorf("expires in %v", d)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("the login answer must not be cached")
	}

	// X-Forwarded-Proto counts only behind a proxy we are told to trust
	h := map[string]string{"X-Forwarded-Proto": "https"}
	rec = call(t, s, "POST", "/api/v1/auth/login", opts{header: h, body: `{"password":"` + testPassword + `"}`})
	if c := rec.Result().Cookies()[0]; c.Secure {
		t.Error("an untrusted X-Forwarded-Proto made the cookie Secure")
	}
	s.SetOnDemand(nil, true)
	rec = call(t, s, "POST", "/api/v1/auth/login", opts{header: h, body: `{"password":"` + testPassword + `"}`})
	if c := rec.Result().Cookies()[0]; !c.Secure {
		t.Error("behind a trusted proxy on https the cookie must be Secure")
	}
	req := httptest.NewRequest("POST", "https://dash.example/api/v1/auth/login", strings.NewReader(`{"password":"`+testPassword+`"}`))
	req.TLS = &tls.ConnectionState{}
	rec = httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	if c := rec.Result().Cookies()[0]; !c.Secure {
		t.Error("a direct TLS connection must give a Secure cookie")
	}
}

func TestLogoutClearsTheCookie(t *testing.T) {
	s := loginServer(t)
	rec := call(t, s, "POST", "/api/v1/auth/logout", opts{cookie: signIn(t, s)})
	cs := rec.Result().Cookies()
	if rec.Code != 204 || len(cs) != 1 || cs[0].Value != "" || cs[0].MaxAge >= 0 || !cs[0].HttpOnly {
		t.Errorf("logout: %d %+v", rec.Code, cs)
	}
}

func TestBadExpiredAndStaleSessionsAreRefused(t *testing.T) {
	s := loginServer(t)
	good := signIn(t, s)
	tampered := good[:len(good)-1] + map[bool]string{true: "A", false: "B"}[good[len(good)-1] != 'A']
	for name, c := range map[string]string{"tampered": tampered, "garbage": "abc", "another key": otherServerCookie(t, "another password")} {
		if rec := call(t, s, "GET", "/api/v1/devices", opts{cookie: c}); rec.Code != 401 {
			t.Errorf("%s cookie: %d", name, rec.Code)
		}
		if _, authed := sessionState(t, s, c); authed {
			t.Errorf("%s cookie counts as signed in", name)
		}
	}
	// the same database key but another password: changing DASHBOARD_PASSWORD signs everybody out
	s.SetLogin(loginService(t, "a brand new password"))
	if rec := call(t, s, "GET", "/api/v1/devices", opts{cookie: good}); rec.Code != 401 {
		t.Errorf("a session survived a password change: %d", rec.Code)
	}
}

func otherServerCookie(t *testing.T, password string) string {
	t.Helper()
	o := newTestServer(&fakeStore{})
	svc := loginService(t, password)
	o.SetLogin(svc)
	v, _ := svc.Issue()
	return v
}

func TestLoginAttemptsAreLimited(t *testing.T) {
	s := loginServer(t)
	s.SetOnDemand(nil, true) // believe X-Forwarded-For, to act as different visitors
	attempt := func(ip, pw string) int {
		return call(t, s, "POST", "/api/v1/auth/login", opts{header: map[string]string{"X-Forwarded-For": ip}, body: `{"password":"` + pw + `"}`}).Code
	}
	for i := 0; i < 5; i++ {
		if c := attempt("10.0.0.1", "wrong"); c != 401 {
			t.Fatalf("attempt %d: %d", i+1, c)
		}
	}
	rec := call(t, s, "POST", "/api/v1/auth/login", opts{header: map[string]string{"X-Forwarded-For": "10.0.0.1"}, body: `{"password":"wrong"}`})
	if rec.Code != 429 || rec.Header().Get("Retry-After") == "" {
		t.Fatalf("sixth attempt: %d retry-after=%q", rec.Code, rec.Header().Get("Retry-After"))
	}
	problem(t, rec)
	// even the right password is not tried while the limit holds (otherwise the limit would not slow a guesser down)
	if c := attempt("10.0.0.1", testPassword); c != 429 {
		t.Errorf("right password while limited: %d", c)
	}
	// another address is not affected
	if c := attempt("10.0.0.2", testPassword); c != 204 {
		t.Errorf("another visitor: %d", c)
	}
}

func TestAllAddressesTogetherAreLimitedToo(t *testing.T) {
	s := loginServer(t)
	s.SetOnDemand(nil, true)
	got429 := false
	for i := 0; i < 40 && !got429; i++ {
		ip := "10.1." + string(rune('0'+i/10)) + "." + string(rune('0'+i%10)) // a new address every time
		rec := call(t, s, "POST", "/api/v1/auth/login", opts{header: map[string]string{"X-Forwarded-For": ip}, body: `{"password":"wrong"}`})
		got429 = rec.Code == 429
	}
	if !got429 {
		t.Error("40 guesses from 40 addresses were all answered")
	}
}

func TestIngestRateZeroLiftsTheLoginLimits(t *testing.T) {
	s := loginServer(t)
	s.SetIngestRate(0)
	for i := 0; i < 50; i++ {
		if c := call(t, s, "POST", "/api/v1/auth/login", opts{body: `{"password":"wrong"}`}).Code; c != 401 {
			t.Fatalf("attempt %d: %d", i+1, c)
		}
	}
}

func TestClassify(t *testing.T) {
	for _, c := range []struct {
		method, path string
		want         routeClass
	}{
		{"GET", "/", classPublic}, {"GET", "/assets/app.js", classPublic}, {"GET", "/ca.crt", classPublic},
		{"GET", "/api/v1/health", classPublic}, {"HEAD", "/api/v1/health", classPublic},
		{"GET", "/api/v1/auth/session", classPublic}, {"POST", "/api/v1/auth/login", classPublic}, {"POST", "/api/v1/auth/logout", classPublic},
		{"POST", "/api/v1/results", classOwnToken}, {"POST", "/api/v1/tests/abc/result", classOwnToken},
		{"POST", "/api/v1/tests", classTokenOrSession},
		{"GET", "/api/v1/results", classProtected}, {"GET", "/api/v1/tests", classProtected}, {"DELETE", "/api/v1/tests/abc", classProtected},
		{"GET", "/api/v1/tests/abc/result", classProtected}, {"POST", "/api/v1/tests/abc/result/", classProtected},
		{"POST", "/api/v1/tests//result", classProtected}, {"POST", "/api/v1/tests/a/b/result", classProtected},
		{"POST", "/api/v1/health", classProtected}, {"GET", "/api/v1/auth/login", classProtected}, {"POST", "/api/v1/auth/session", classProtected},
		{"GET", "/api/v1/auth/session/", classProtected}, {"GET", "/api/", classProtected}, {"GET", "/api/v2/anything", classProtected},
	} {
		if got := classify(c.method, c.path); got != c.want {
			t.Errorf("%s %s = %d, want %d", c.method, c.path, got, c.want)
		}
	}
}

// The gate looks at the path before the router does, so a path written another way must not get past it: encoded characters,
// dot segments, doubled slashes, a trailing slash, a path hidden in the query. Whatever the router makes of these, nobody
// without a session may get an answer with data.
func TestOddPathsDoNotGetPastTheGate(t *testing.T) {
	s := loginServer(t)
	for _, target := range []string{
		"/%61pi/v1/devices", "/api/v1/%64evices", "/api%2Fv1/devices", "/api/v1%2Fdevices", "/api/v1/devices%2F", "/api/v1/devices/",
		"/api/v1/./devices", "/api/v1/../v1/devices", "/api/v1/health/../devices", "//api/v1/devices", "/api//v1/devices",
		"/api/v1/devices?x=/api/v1/health", "/api/v1/devices#/api/v1/health", "/API/v1/devices", "/api/v1/results/%2e%2e/devices",
		"/api/v1/auth/session/../../devices", "/api/v1/auth/login/../../devices", "/api/v1/tests/x/result/../../../devices",
	} {
		for _, method := range []string{"GET", "POST", "DELETE"} {
			rec := call(t, s, method, target, opts{})
			// refused (401), not found or not allowed, or sent to the tidy form of the path (which is then refused in turn); never an
			// answer from a handler
			switch rec.Code {
			case 401, 404, 405, 301, 307, 308:
			default:
				t.Errorf("%s %s without a session: %d %s", method, target, rec.Code, rec.Body)
			}
			if strings.Contains(rec.Body.String(), `"items"`) {
				t.Errorf("%s %s without a session answered with data: %s", method, target, rec.Body)
			}
		}
	}
}
