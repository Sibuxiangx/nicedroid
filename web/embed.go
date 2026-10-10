// Package web embeds the built admin UI and serves it with SPA fallback.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"
)

//go:embed all:dist
var dist embed.FS

// Handler returns an HTTP handler serving the embedded admin UI.
//
// Unknown paths fall back to index.html so client-side routes work, but only
// when the request looks like a page. A missing file — anything with a file
// extension — is a real 404: answering it with index.html would hand the
// browser HTML under an image content type and hide broken references.
func Handler() http.Handler {
	sub, err := fs.Sub(dist, "dist")
	if err != nil {
		return http.NotFoundHandler()
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if p == "" || p == "." {
			p = "index.html"
		}
		if _, err := fs.Stat(sub, p); err != nil {
			if looksLikeFile(p) {
				http.NotFound(w, r)
				return
			}
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		fileServer.ServeHTTP(w, r)
	})
}

// looksLikeFile reports whether a path names an asset rather than a page.
func looksLikeFile(p string) bool {
	base := path.Base(p)
	i := strings.LastIndexByte(base, '.')
	return i > 0 && i < len(base)-1
}
