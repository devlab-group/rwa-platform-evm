package main

import (
	"os"
	"path/filepath"
	"testing"
)

// Reject group/other password-file access; it exposes the password like a CLI argument
func TestCheckPasswordFilePermissions_RejectsGroupReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(path, []byte("secret\n"), 0o640); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := checkPasswordFilePermissions(path); err == nil {
		t.Fatal("expected rejection of a group-readable password file")
	}
}

func TestCheckPasswordFilePermissions_RejectsWorldReadable(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(path, []byte("secret\n"), 0o644); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := checkPasswordFilePermissions(path); err == nil {
		t.Fatal("expected rejection of a world-readable password file")
	}
}

func TestCheckPasswordFilePermissions_AcceptsOwnerOnly(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pw")
	if err := os.WriteFile(path, []byte("secret\n"), 0o600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := checkPasswordFilePermissions(path); err != nil {
		t.Errorf("expected an owner-only-readable password file to be accepted: %v", err)
	}
}

func TestCheckPasswordFilePermissions_MissingFile(t *testing.T) {
	if err := checkPasswordFilePermissions(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("expected an error for a missing password file")
	}
}
