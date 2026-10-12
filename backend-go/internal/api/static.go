package api

import (
	"io/fs"
	"mime"
	"net/http"
	"path"
	"strings"
)

func init() {
	// Browsers want the manifest served as JSON, and the platform mime table does not know the extension.
	_ = mime.AddExtensionType(".webmanifest", "application/manifest+json")
}

// ServeStatic serves a built single-page app (the frontend's dist folder) for every GET that is not an /api/ path.
// Unknown paths fall back to index.html so client-side routes survive a reload; /api/ paths keep their JSON 404s.
// Hashed build assets are cached for a year; everything else (index.html, the service worker, the manifest)
// must be revalidated so a new release is picked up.
func (s *Server) ServeStatic(root fs.FS) {
	files := http.FileServerFS(root)
	s.static = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		info, err := fs.Stat(root, name)
		switch {
		case name != "" && err == nil && !info.IsDir():
			if strings.HasPrefix(name, "assets/") {
				w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
			} else {
				w.Header().Set("Cache-Control", "no-cache")
			}
			files.ServeHTTP(w, r)
		case strings.HasPrefix(name, "assets/") || (path.Ext(name) != "" && path.Ext(name) != ".html"):
			// A missing file (stale hashed asset, favicon.ico, ...) must be a real 404, not the HTML shell,
			// or an old browser tab would try to run HTML as JavaScript after a release.
			http.NotFound(w, r)
		default: // "/", directories and client-side routes: the app shell. (FileServer would 301 a literal /index.html.)
			w.Header().Set("Cache-Control", "no-cache")
			http.ServeFileFS(w, r, root, "index.html")
		}
	})
}
