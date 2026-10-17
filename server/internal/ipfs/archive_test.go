package ipfs

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileArchiveAddRaw_StoresVerifiedBlockAndLeavesNoTempFile(t *testing.T) {
	dir := t.TempDir()
	c, err := NewFileArchiveClient(dir)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"asset":"gold"}`)

	cid, err := c.AddRaw(context.Background(), data)
	if err != nil {
		t.Fatalf("AddRaw: %v", err)
	}
	want, _ := ComputeCIDv1Raw(data)
	if cid != want {
		t.Errorf("cid = %s, want %s", cid, want)
	}
	got, err := c.Get(context.Background(), cid)
	if err != nil || string(got) != string(data) {
		t.Errorf("Get = %q, %v, want the stored bytes", got, err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 || entries[0].Name() != cid {
		t.Errorf("archive holds %v, want exactly the block %s", entries, cid)
	}
}

// A block whose final path cannot be replaced fails the write and removes its temp file
func TestFileArchiveAddRaw_RenameFailureIsReportedAndCleansUp(t *testing.T) {
	dir := t.TempDir()
	c, err := NewFileArchiveClient(dir)
	if err != nil {
		t.Fatal(err)
	}
	data := []byte("blocked")
	cid, _ := ComputeCIDv1Raw(data)
	// A non-empty directory squats on the block's name, so the rename cannot succeed
	squat := filepath.Join(dir, cid)
	if err := os.MkdirAll(filepath.Join(squat, "child"), 0o700); err != nil {
		t.Fatal(err)
	}

	if _, err := c.AddRaw(context.Background(), data); err == nil || !strings.Contains(err.Error(), "rename") {
		t.Fatalf("AddRaw error = %v, want a rename failure", err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Errorf("archive holds %v after the failure, want only the squatting directory", entries)
	}
}

func TestFileArchiveAddRaw_UnwritableDirectoryIsReported(t *testing.T) {
	dir := t.TempDir()
	c, err := NewFileArchiveClient(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := c.AddRaw(context.Background(), []byte("x")); err == nil || !strings.Contains(err.Error(), "create temp file") {
		t.Fatalf("AddRaw error = %v, want a temp-file creation failure", err)
	}
}
