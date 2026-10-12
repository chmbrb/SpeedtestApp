package api

import (
	"net/http"
	"strings"
	"testing"
	"testing/fstest"
)

func TestServeStatic(t *testing.T) {
	s := newTestServer(&fakeStore{})
	s.ServeStatic(fstest.MapFS{
		"index.html":           {Data: []byte("<html>app</html>")},
		"sw.js":                {Data: []byte("// sw")},
		"manifest.webmanifest": {Data: []byte("{}")},
		"assets/app-1.js":      {Data: []byte("console.log(1)")},
	})
	get := func(path string) (int, string, string) {
		rec := do(t, s, "GET", path, "", "")
		return rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control")
	}

	if code, body, cc := get("/"); code != 200 || body != "<html>app</html>" || cc != "no-cache" {
		t.Errorf("/: %d %q %q", code, body, cc)
	}
	if code, body, _ := get("/some/client/route"); code != 200 || body != "<html>app</html>" {
		t.Errorf("SPA fallback: %d %q", code, body)
	}
	if code, body, cc := get("/assets/app-1.js"); code != 200 || body != "console.log(1)" || cc != "public, max-age=31536000, immutable" {
		t.Errorf("hashed asset: %d %q %q", code, body, cc)
	}
	if _, _, cc := get("/sw.js"); cc != "no-cache" {
		t.Errorf("service worker must not be cached hard, got %q", cc)
	}
	// a missing file is a 404, never the HTML shell
	for _, p := range []string{"/assets/gone-9.js", "/favicon.ico", "/robots.txt", "/assets/"} {
		code, body, _ := get(p)
		if p == "/assets/" { // a directory is a client route, not a missing file
			if code != 200 || body != "<html>app</html>" {
				t.Errorf("%s: %d %q", p, code, body)
			}
			continue
		}
		if code != 404 || body == "<html>app</html>" {
			t.Errorf("%s: want 404 not the shell, got %d %q", p, code, body)
		}
	}
	// the manifest has the right type
	if ct := do(t, s, "GET", "/manifest.webmanifest", "", "").Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/manifest+json") {
		t.Errorf("manifest content type = %q", ct)
	}
	// API paths are never swallowed by the SPA fallback
	rec := do(t, s, "GET", "/api/v1/nope", "", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown API path: %d", rec.Code)
	}
	problem(t, rec)
	// real API routes still win
	if rec := do(t, s, "GET", "/api/v1/health", "", ""); rec.Code != 200 {
		t.Errorf("health: %d", rec.Code)
	}
	// non-GET to an unknown path is still a problem+json 404, not index.html
	if rec := do(t, s, "POST", "/whatever", "", ""); rec.Code != http.StatusNotFound && rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST /whatever: %d", rec.Code)
	}
	// a directory request does not list files
	if code, body, _ := get("/assets/"); code != 200 || body != "<html>app</html>" {
		t.Errorf("directory: %d %q", code, body)
	}
}
