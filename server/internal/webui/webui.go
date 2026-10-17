// Package webui embeds the admin SPA for same-origin serving, falling back to
// index.html for client routes and reloads.
//
// Tracked dist/.gitkeep satisfies go:embed all:dist without a web build. make platform
// and server/e2e/run_e2e.sh copy web/dist here before compilation; output is
// gitignored, and the binary embeds dist/'s build-time contents.
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

// FS returns embedded assets rooted at dist/, with index.html at the root
func FS() fs.FS {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		// dist/ is tracked, so the embed always contains it
		panic("webui: dist embed is malformed: " + err.Error())
	}
	return sub
}

// Attach serves the embedded SPA with Gin NoRoute; registered API routes (/api/v1/**,
// /healthz, /readyz from internal/api.NewRouter) take precedence. Unmatched /api/ paths
// and methods other than GET/HEAD return JSON 404.
//
// Existing assets use http.FileServer. Missing paths with extensions return JSON 404;
// other GET/HEAD paths serve index.html for react-router navigation.
func Attach(r *gin.Engine) {
	attach(r, FS())
}

// attach accepts an fs.FS so fixture tests cover routing, static files and /api/ 404s
// independently of embedded build contents
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
			// Missing assets must return 404 instead of HTML interpreted as JavaScript or CSS
			if path.Ext(clean) != "" {
				c.JSON(http.StatusNotFound, gin.H{"code": "not_found", "message": "not found"})
				return
			}
		}

		// Serve index.html for "/" or a client route without an embedded path
		c.Request.URL.Path = "/"
		fileServer.ServeHTTP(c.Writer, c.Request)
	})
}
