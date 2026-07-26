// Package webui embeds the built web/ SPA and serves it same-origin from
// the platform binary — production assets embed and serve straight from
// the Go binary — with client-side-route SPA fallback so a
// direct navigation or hard refresh on e.g. /admin/setup still loads.
//
// go:embed can only reach files under this module tree, and web/dist does
// not exist at a bare `go build` (it is a separate npm package, built
// separately). So dist/ here is a real, checked-in directory, but its build
// output is gitignored — only an empty placeholder dist/.gitkeep is tracked
// (the `all:` embed prefix includes dotfiles, so .gitkeep alone satisfies the
// directive). That keeps `go build`/`go test` from ever depending on a web
// build existing, while ensuring a release build never dirties a tracked
// file: a real deployment (or server/e2e/run_e2e.sh, for the live-smoke
// suite) copies the actual web/dist build — index.html plus hashed assets —
// over it before building the platform binary. Either way this package embeds
// whatever is on disk at `go build` time; the two "real embedded build" tests
// skip when only the placeholder is present.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/auth"
)

//go:embed all:dist
var embedded embed.FS

// FS returns the embedded assets rooted at dist/ (so index.html etc. sit at
// the filesystem root instead of under "dist/").
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		// Can't happen: dist/ always ships in the repo (see package doc).
		panic("webui: dist embed is malformed: " + err.Error())
	}
	return sub
}

// Attach mounts a Gin NoRoute handler that serves the embedded SPA for any
// request the router's registered routes didn't claim. Every real API
// route (/api/v1/**, /healthz, /readyz) is registered explicitly by
// internal/api.NewRouter and so always wins; NoRoute only ever fires for
// paths nothing else matched. An unmatched /api/* path still 404s as JSON
// rather than falling through to the SPA — only unmatched GET/HEAD requests
// outside /api/ get the SPA treatment.
//
// Static assets (JS/CSS/images — anything that exists as a real file in
// the embedded tree) are served as-is with their normal content type and
// caching headers via http.FileServer; everything else falls back to
// index.html so react-router's client-side routes resolve.
func Attach(r *gin.Engine) {
	attach(r, FS())
}

// attach holds Attach's actual logic over an arbitrary fs.FS, so tests can
// exercise the routing behavior (SPA fallback vs. real-file vs. /api/ 404)
// against a small fixture instead of the real embedded dist/ — which
// server/e2e/run_e2e.sh overwrites with a real `npm run build` output that
// won't have the same files as whatever placeholder happens to be checked
// in, so tests must not depend on its exact contents.
func attach(r *gin.Engine, assets fs.FS) {
	fileServer := http.FileServer(http.FS(assets))

	r.NoRoute(func(c *gin.Context) {
		auth.SetSecurityHeaders(c.Writer.Header())
		if strings.HasPrefix(c.Request.URL.Path, "/api/") {
			c.JSON(http.StatusNotFound, gin.H{"code": "not_found", "message": "not found"})
			return
		}
		if c.Request.Method != http.MethodGet && c.Request.Method != http.MethodHead {
			c.JSON(http.StatusNotFound, gin.H{"code": "not_found", "message": "not found"})
			return
		}

		if clean := path.Clean(c.Request.URL.Path); clean != "/" && clean != "." {
			if f, err := assets.Open(strings.TrimPrefix(clean, "/")); err == nil {
				_ = f.Close()
				fileServer.ServeHTTP(c.Writer, c.Request)
				return
			}
		}

		// SPA fallback: no real file at this path, so it's a client-side
		// route (or "/") — serve index.html and let react-router take over.
		c.Request.URL.Path = "/"
		fileServer.ServeHTTP(c.Writer, c.Request)
	})
}
