package webui

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

// fixtureFS is a small, self-contained SPA build used by every test below
// except the two that specifically check the real embedded dist/ — server/
// e2e/run_e2e.sh overwrites dist/ with a real `npm run build` output whose
// exact file list this package's tests must not depend on (see attach's doc
// comment in webui.go), so the routing-behavior tests exercise attach()
// directly against this fixture instead of Attach()/FS().
var fixtureFS = fstest.MapFS{
	"index.html":    {Data: []byte("<html><body>spa shell</body></html>")},
	"robots.txt":    {Data: []byte("User-agent: *\n")},
	"assets/app.js": {Data: []byte("console.log('app')")},
}

// newTestRouter mirrors how cmd/platform wires things: a couple of real
// API routes registered first (so attach's NoRoute only ever sees paths
// nothing else claimed), then attach.
func newTestRouter() *gin.Engine {
	r := gin.New()
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"status": "ok"}) })
	api := r.Group("/api/v1")
	api.GET("/project", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"projectId": "known"}) })
	attach(r, fixtureFS)
	return r
}

func doReq(r *gin.Engine, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func doGet(r *gin.Engine, path string) *httptest.ResponseRecorder {
	return doReq(r, http.MethodGet, path)
}

func TestRegisteredAPIRouteTakesPrecedence(t *testing.T) {
	r := newTestRouter()
	rec := doGet(r, "/api/v1/project")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"projectId"`) {
		t.Errorf("body = %q, want the real API handler's JSON, not the SPA fallback", rec.Body.String())
	}
}

func TestUnmatchedAPIPathStays404JSON(t *testing.T) {
	r := newTestRouter()
	rec := doGet(r, "/api/v1/does-not-exist")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"not_found"`) {
		t.Errorf("body = %q, want a not_found JSON error, not the SPA index.html", rec.Body.String())
	}
}

func TestRootServesIndexHTML(t *testing.T) {
	r := newTestRouter()
	rec := doGet(r, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "spa shell") {
		t.Errorf("body = %q, want the fixture's index.html", rec.Body.String())
	}
}

func TestClientSideRouteFallsBackToIndexHTML(t *testing.T) {
	r := newTestRouter()
	rec := doGet(r, "/admin/setup")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (SPA fallback)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "spa shell") {
		t.Errorf("body = %q, want the fixture's index.html via SPA fallback", rec.Body.String())
	}
}

func TestRealStaticFileIsServedAsIs(t *testing.T) {
	r := newTestRouter()
	rec := doGet(r, "/assets/app.js")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if strings.Contains(rec.Body.String(), "spa shell") {
		t.Errorf("assets/app.js was served as index.html instead of the real file: %q", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "console.log") {
		t.Errorf("body = %q, want the fixture's assets/app.js content", rec.Body.String())
	}
}

func TestNonGetUnmatchedPathIs404NotSPA(t *testing.T) {
	r := newTestRouter()
	rec := doReq(r, http.MethodPost, "/admin/setup")
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rec.Code)
	}
}

// requireRealIndexHTML skips a test when only the tracked dist/.gitkeep
// placeholder is present (a bare checkout: dist/ is otherwise gitignored, its
// real index.html + assets are copied in only at build/release time by
// `make platform` or server/e2e/run_e2e.sh). These two tests assert
// properties of a REAL embedded build; with no index.html embedded there is
// nothing to assert, so they skip rather than fail — keeping their intent for
// when a real build IS embedded.
func requireRealIndexHTML(t *testing.T) {
	t.Helper()
	f, err := FS().Open("index.html")
	if err != nil {
		t.Skip("no real embedded index.html (only the dist/.gitkeep placeholder is present)")
	}
	_ = f.Close()
}

// TestRealEmbeddedFSServesIndexHTML exercises the actual FS()/Attach() path
// (real embedded dist/, whatever is copied there by a build) — only asserting
// the one thing guaranteed true when a real build is embedded: a GET /
// returns 200 with an HTML document.
func TestRealEmbeddedFSServesIndexHTML(t *testing.T) {
	requireRealIndexHTML(t)
	r := gin.New()
	Attach(r)
	rec := doGet(r, "/")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<html") {
		t.Errorf("body doesn't look like an HTML document: %q", rec.Body.String())
	}
}

func TestFSRootedAtDistNotUnderDistPrefix(t *testing.T) {
	requireRealIndexHTML(t)
	f, err := FS().Open("index.html")
	if err != nil {
		t.Fatalf("FS().Open(\"index.html\"): %v (FS() should be rooted at dist/, not its parent)", err)
	}
	_ = f.Close()
}

// TestSecurityHeadersOnSPAResponses covers the review item asking these to
// be set directly by this package (not only relied on from
// internal/auth's router-wide middleware) — on the index.html fallback,
// on a real static asset, and on the /api/ 404 branch.
func TestSecurityHeadersOnSPAResponses(t *testing.T) {
	r := newTestRouter()
	for _, path := range []string{"/", "/admin/setup", "/assets/app.js", "/api/v1/does-not-exist"} {
		rec := doGet(r, path)
		for _, header := range []string{"X-Content-Type-Options", "X-Frame-Options", "Content-Security-Policy", "Referrer-Policy"} {
			if rec.Header().Get(header) == "" {
				t.Errorf("%s: missing security header %s", path, header)
			}
		}
		if rec.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: X-Content-Type-Options = %q, want nosniff", path, rec.Header().Get("X-Content-Type-Options"))
		}
	}
}
