package ipfs

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestKuboClientTimesOutOnSlowServer checks that a wedged endpoint does not
// hang a call indefinitely, which it would with an http.Client{} that has no
// timeout at all.
func TestKuboClientTimesOutOnSlowServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"Hash":"bafkstub"}`))
	}))
	defer srv.Close()

	client := NewKuboClientWithLimits(srv.URL, 50*time.Millisecond, DefaultMaxResponseBytes)
	start := time.Now()
	_, err := client.AddRaw(context.Background(), []byte("data"))
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected a timeout error from a server slower than the configured overall timeout")
	}
	if elapsed > 400*time.Millisecond {
		t.Fatalf("expected the call to time out well before the server's 500ms response, took %s", elapsed)
	}
}

// TestKuboClientRejectsOversizedResponse checks the bounded-read limit.
func TestKuboClientRejectsOversizedResponse(t *testing.T) {
	const limit = 16
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(strings.Repeat("x", limit*4))) // well over the configured limit
	}))
	defer srv.Close()

	client := NewKuboClientWithLimits(srv.URL, 5*time.Second, limit)
	if _, err := client.Get(context.Background(), "bafkstub"); err == nil {
		t.Fatal("expected an error for a response exceeding the configured byte limit")
	}
}

// TestFileArchiveClientAddRawLeavesNoTempFiles proves the crash-safe write
// path (temp file -> fsync -> atomic rename) never leaves a stray
// `.tmp-*` file behind on the successful path.
func TestFileArchiveClientAddRawLeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	archive, err := NewFileArchiveClient(dir)
	if err != nil {
		t.Fatal(err)
	}
	cid, err := archive.AddRaw(context.Background(), []byte(`{"asset":"durable-write-test"}`))
	if err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	sawFinal := false
	for _, e := range entries {
		names = append(names, e.Name())
		if strings.HasPrefix(e.Name(), ".tmp-") {
			t.Fatalf("expected no stray temp file after a successful AddRaw, found %s", e.Name())
		}
		if e.Name() == cid {
			sawFinal = true
		}
	}
	if !sawFinal {
		t.Fatalf("expected the final block file for CID %s, got directory entries %v", cid, names)
	}
}
