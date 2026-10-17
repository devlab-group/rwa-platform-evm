package ipfs

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestKuboClientTimesOutOnSlowServer checks that an unresponsive endpoint
// fails the call at the client timeout instead of hanging it
func TestKuboClientTimesOutOnSlowServer(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release // never answers until the test is over
	}))
	defer srv.Close()
	defer close(release)

	client := NewKuboClientWithLimits(srv.URL, 50*time.Millisecond, DefaultMaxResponseBytes)
	_, err := client.AddRaw(context.Background(), []byte("data"))
	var netErr net.Error
	if !errors.As(err, &netErr) || !netErr.Timeout() {
		t.Fatalf("AddRaw error = %v, want a client timeout", err)
	}
}

// TestKuboClientRejectsOversizedResponse checks the response-size limit
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

// TestFileArchiveClientAddRawLeavesNoTempFiles checks that a successful AddRaw
// leaves no `.tmp-*` file behind
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

// TestKuboClientAddRawUsesSingleBlockChunker checks that the add request asks for
// a 1 MiB chunker and that larger data is refused before any request is made
func TestKuboClientAddRawUsesSingleBlockChunker(t *testing.T) {
	var query string
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		query = r.URL.RawQuery
		_, _ = w.Write([]byte(`{"Hash":"bafkstub"}`))
	}))
	defer srv.Close()

	client := NewKuboClient(srv.URL)
	if _, err := client.AddRaw(context.Background(), make([]byte, 1<<20)); err != nil {
		t.Fatalf("AddRaw at the limit: %v", err)
	}
	for _, want := range []string{"cid-version=1", "raw-leaves=true", "pin=true", "chunker=size-1048576"} {
		if !strings.Contains(query, want) {
			t.Errorf("query %q lacks %q", query, want)
		}
	}
	calls = 0
	if _, err := client.AddRaw(context.Background(), make([]byte, 1<<20+1)); err == nil || calls != 0 {
		t.Fatalf("expected an over-limit error without a request, err=%v calls=%d", err, calls)
	}
}
