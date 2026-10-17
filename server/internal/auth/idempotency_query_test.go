package auth

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/rwa-platform/server/internal/dal/memory"
)

// Equivalent query encodings must replay one response; changed parameters get a new scope
func TestIdempotency_QueryStringScoping(t *testing.T) {
	tests := []struct {
		name       string
		first      string
		second     string
		wantReplay bool
	}{
		{"parameter order is irrelevant", "/x?a=1&b=2", "/x?b=2&a=1", true},
		{"repeated values are order independent", "/x?a=1&a=2", "/x?a=2&a=1", true},
		{"plus and percent-20 are the same space", "/x?a=%20", "/x?a=+", true},
		{"empty value spellings agree", "/x?a", "/x?a=", true},
		{"different value", "/x?a=1", "/x?a=2", false},
		{"different key", "/x?a=1", "/x?b=1", false},
		{"missing parameter", "/x?a=1&b=2", "/x?a=1", false},
		{"extra repeated value", "/x?a=1", "/x?a=1&a=1", false},
		{"encoded ampersand is one value, not two parameters", "/x?a=1&b=2", "/x?a=1%26b%3D2", false},
		{"no query against a query", "/x", "/x?a=1", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls int32
			r := gin.New()
			r.Use(Idempotency(memory.NewIdempotencyRepository(), time.Hour))
			r.POST("/x", func(c *gin.Context) {
				atomic.AddInt32(&calls, 1)
				c.JSON(http.StatusAccepted, gin.H{"ok": true})
			})
			do := func(target string) {
				req := httptest.NewRequest(http.MethodPost, target, bytes.NewReader([]byte(`{}`)))
				req.Header.Set("Idempotency-Key", "k")
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				if w.Code != http.StatusAccepted {
					t.Fatalf("%s: status = %d, want 202", target, w.Code)
				}
			}
			do(tt.first)
			do(tt.second)

			want := int32(2)
			if tt.wantReplay {
				want = 1
			}
			if got := atomic.LoadInt32(&calls); got != want {
				t.Errorf("handler ran %d times for %s then %s, want %d", got, tt.first, tt.second, want)
			}
		})
	}
}
