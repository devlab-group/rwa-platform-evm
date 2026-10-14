// Package webui embeds the built admin SPA and serves it same-origin.
// Client-side routes fall back to index.html for direct navigation and reloads.
//
// The tracked dist/.gitkeep satisfies go:embed's all:dist directive without a
// web build. Release builds copy web/dist into this module before compilation
// through make platform or server/e2e/run_e2e.sh. Build output is gitignored;
// the binary embeds whatever dist/ contains at build time.
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

// FS returns the embedded assets rooted at dist/, so index.html sits at the root
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		// dist/ is tracked, so the embed always contains it
		panic("webui: dist embed is malformed: " + err.Error())
	}
	return sub
}

// Attach mounts a Gin NoRoute handler that serves the embedded SPA for requests
// that no registered route matched, so the API routes (/api/v1/**, /healthz and
// /readyz, registered by internal/api.NewRouter) always take precedence.
// Unmatched paths under /api/ and unmatched requests other than GET or HEAD get
// a JSON 404. A path that names a file in the embedded tree is served by
// http.FileServer, and any other GET or HEAD path serves index.html so
// react-router resolves client-side routes.
func Attach(r *gin.Engine) {
	attach(r, FS())
}

// attach implements Attach over any fs.FS, so tests can check the routing (SPA
// fallback, real file, /api/ 404) against a fixture, because the contents of the
// embedded dist/ depend on the build
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

		// No file at this path, so it is a client-side route or "/"; serve
		// index.html for react-router
		c.Request.URL.Path = "/"
		fileServer.ServeHTTP(c.Writer, c.Request)
	})
}
