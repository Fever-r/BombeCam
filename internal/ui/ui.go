package ui

import (
	"io/fs"
	"net/http"

	"github.com/Fever-r/BombeCam/web"
)

// Handler returns an http.Handler that serves the embedded web UI static assets.
// It maps /index.html directly to / so http.FileServer does not issue a 301 redirect.
// Assets are sent with no-cache so a browser tab never keeps running the page
// of an older build after BombeCam is updated.
func Handler() http.Handler {
	fileServer := http.FileServer(http.FS(web.Assets))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/index.html" {
			r.URL.Path = "/"
		}
		w.Header().Set("Cache-Control", "no-cache")
		fileServer.ServeHTTP(w, r)
	})
}

// FS returns the embedded web UI filesystem.
func FS() fs.FS {
	return web.Assets
}
