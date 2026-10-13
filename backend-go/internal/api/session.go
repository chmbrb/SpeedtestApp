package api

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/chmbrb/SpeedtestApp/backend-go/internal/login"
)

// SetLogin turns the dashboard login on (see package login). With nil, or never calling this, the dashboard is open.
func (s *Server) SetLogin(l *login.Service) {
	s.login = l
	s.resetLoginLimits()
}

// resetLoginLimits sets the limits on login attempts: 5 in a burst and 6 a minute per address, and 30 and 30 for all addresses
// together. They are lifted when ingest limiting is off (SetIngestRate(0)), which exists for load and fuzz tests.
func (s *Server) resetLoginLimits() {
	if s.limitIngest {
		s.loginLimiter, s.loginGlobal = newLimiter(5, 6), newLimiter(30, 30)
		return
	}
	s.loginLimiter, s.loginGlobal = newLimiter(1_000_000, 1_000_000), newLimiter(1_000_000, 1_000_000)
}

type routeClass int

const (
	// classProtected needs a session.
	classProtected routeClass = iota
	// classPublic is reachable without one: the page itself, the health check and the login endpoints.
	classPublic
	// classOwnToken has its own bearer token, which the handler checks (a collector sending a result, a browser submitting the
	// result of its own test).
	classOwnToken
	// classTokenOrSession is a session, or a bearer token the handler checks (a collector starting a test on its device).
	classTokenOrSession
)

// classify decides what a request needs when the login is on. Anything it does not know needs a session, so a route added later
// is protected unless someone deliberately lists it here. backend-rust/src/session_api.rs has the same table.
func classify(method, urlPath string) routeClass {
	if !strings.HasPrefix(urlPath, "/api/") {
		return classPublic // the built frontend: the login page must load before anybody can log in
	}
	switch {
	case (method == http.MethodGet || method == http.MethodHead) && urlPath == "/api/v1/health":
		return classPublic
	case method == http.MethodGet && urlPath == "/api/v1/auth/session":
		return classPublic
	case method == http.MethodPost && (urlPath == "/api/v1/auth/login" || urlPath == "/api/v1/auth/logout"):
		return classPublic
	case method == http.MethodPost && urlPath == "/api/v1/results":
		return classOwnToken
	case method == http.MethodPost && urlPath == "/api/v1/tests":
		return classTokenOrSession
	case method == http.MethodPost && isTestResultPath(urlPath):
		return classOwnToken
	}
	return classProtected
}

// isTestResultPath matches /api/v1/tests/{id}/result with a real, single-segment id.
func isTestResultPath(urlPath string) bool {
	id, ok := strings.CutPrefix(urlPath, "/api/v1/tests/")
	if !ok {
		return false
	}
	id, ok = strings.CutSuffix(id, "/result")
	return ok && id != "" && !strings.Contains(id, "/")
}

func (s *Server) hasSession(r *http.Request) bool {
	c, err := r.Cookie(login.CookieName)
	return err == nil && s.login.Valid(c.Value)
}

// gate refuses what needs a session when there is none. It runs before routing, so it also covers paths that match no route.
func (s *Server) gate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.login == nil {
			next.ServeHTTP(w, r)
			return
		}
		switch classify(r.Method, r.URL.Path) {
		case classPublic, classOwnToken:
			next.ServeHTTP(w, r)
			return
		case classTokenOrSession:
			if r.Header.Get("Authorization") != "" || s.hasSession(r) {
				next.ServeHTTP(w, r)
				return
			}
		default:
			if s.hasSession(r) {
				next.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Cache-Control", "no-store")
		writeProblem(w, http.StatusUnauthorized, "login required")
	})
}

// secureRequest says whether the browser reached us over https, which decides the cookie's Secure flag: a Secure cookie set over
// plain http would simply be thrown away by the browser, and nobody could log in.
func (s *Server) secureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return s.trustProxy && strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
}

func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, value string, expires time.Time, maxAge int) {
	http.SetCookie(w, &http.Cookie{
		Name: login.CookieName, Value: value, Path: "/", Expires: expires, MaxAge: maxAge,
		HttpOnly: true, SameSite: http.SameSiteStrictMode, Secure: s.secureRequest(r),
	})
}

// authSession tells the page whether it must show a login, and whether this browser is signed in.
func (s *Server) authSession(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	required := s.login != nil
	writeJSON(w, http.StatusOK, map[string]bool{"required": required, "authenticated": !required || s.hasSession(r)})
}

func (s *Server) authLogin(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if s.login == nil {
		writeProblem(w, http.StatusNotFound, "the login is not turned on on this server")
		return
	}
	ip := s.clientIP(r)
	for _, l := range []struct {
		l   *limiter
		key string
	}{{s.loginLimiter, ip}, {s.loginGlobal, "all"}} {
		if ok, retry := l.l.allow(l.key); !ok {
			w.Header().Set("Retry-After", strconv.Itoa(retry))
			writeProblem(w, http.StatusTooManyRequests, "too many login attempts; wait a moment")
			return
		}
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	var body struct {
		Password *string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Password == nil {
		writeProblem(w, http.StatusBadRequest, "body must be JSON with a password")
		return
	}
	if !s.login.CheckPassword(*body.Password) {
		s.log.Warn("login refused: wrong password", "ip", ip)
		writeProblem(w, http.StatusUnauthorized, "incorrect password")
		return
	}
	value, expires := s.login.Issue()
	s.setSessionCookie(w, r, value, expires, int(s.login.Lifetime().Seconds()))
	w.WriteHeader(http.StatusNoContent)
}

// authLogout forgets the cookie in this browser. (A session is stateless: copies of the cookie elsewhere stay valid until they
// expire. Changing DASHBOARD_PASSWORD ends every session at once.)
func (s *Server) authLogout(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.setSessionCookie(w, r, "", time.Unix(0, 0), -1)
	w.WriteHeader(http.StatusNoContent)
}
