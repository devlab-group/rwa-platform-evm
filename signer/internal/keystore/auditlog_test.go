package keystore

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestAuditLog_AppendAndVerify(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auditor.json")
	if err := AppendAuditEvent(path, "unlock_success", map[string]string{"address": "0xabc"}); err != nil {
		t.Fatalf("AppendAuditEvent: %v", err)
	}
	if err := AppendAuditEvent(path, "signed", map[string]string{"digest": "0xdead"}); err != nil {
		t.Fatalf("AppendAuditEvent: %v", err)
	}
	if err := VerifyAuditLog(path); err != nil {
		t.Errorf("expected a freshly written audit log to verify, got: %v", err)
	}
}

func TestAuditLog_VerifyDetectsModifiedEntry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auditor.json")
	AppendAuditEvent(path, "unlock_success", map[string]string{"address": "0xabc"})
	AppendAuditEvent(path, "signed", map[string]string{"digest": "0xdead"})

	data, err := os.ReadFile(auditLogPath(path))
	if err != nil {
		t.Fatalf("reading audit log: %v", err)
	}
	tampered := bytes.ReplaceAll(data, []byte("unlock_success"), []byte("unlock_failure"))
	if bytes.Equal(tampered, data) {
		t.Fatal("test setup: tampering did not change the file")
	}
	if err := os.WriteFile(auditLogPath(path), tampered, 0o600); err != nil {
		t.Fatalf("writing tampered log: %v", err)
	}

	if err := VerifyAuditLog(path); err == nil {
		t.Fatal("expected VerifyAuditLog to detect a modified entry")
	} else if !strings.Contains(err.Error(), "hash mismatch") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAuditLog_VerifyDetectsReorderedEntries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auditor.json")
	AppendAuditEvent(path, "event-a", nil)
	AppendAuditEvent(path, "event-b", nil)

	data, err := os.ReadFile(auditLogPath(path))
	if err != nil {
		t.Fatalf("reading audit log: %v", err)
	}
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte("\n"))
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	reordered := append(append([]byte{}, lines[1]...), '\n')
	reordered = append(reordered, lines[0]...)
	reordered = append(reordered, '\n')
	if err := os.WriteFile(auditLogPath(path), reordered, 0o600); err != nil {
		t.Fatalf("writing reordered log: %v", err)
	}

	if err := VerifyAuditLog(path); err == nil {
		t.Fatal("expected VerifyAuditLog to detect reordered entries")
	}
}

func TestAuditLog_MissingLogVerifiesClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "never-appended.json")
	if err := VerifyAuditLog(path); err != nil {
		t.Errorf("expected a nonexistent audit log to verify with no error, got: %v", err)
	}
}

func TestAuditLog_NeverContainsPasswordOrKeyFieldNames(t *testing.T) {
	// Catch secret-like field names passed to AppendAuditEvent; this test cannot inspect
	// values for secret material
	path := filepath.Join(t.TempDir(), "auditor.json")
	AppendAuditEvent(path, "unlock_success", map[string]string{"address": "0xabc", "digest": "0xdead"})
	data, err := os.ReadFile(auditLogPath(path))
	if err != nil {
		t.Fatalf("reading audit log: %v", err)
	}
	for _, forbidden := range []string{"password", "privateKey", "\"D\":"} {
		if strings.Contains(strings.ToLower(string(data)), strings.ToLower(forbidden)) {
			t.Errorf("audit log unexpectedly contains %q", forbidden)
		}
	}
}

// Audit verification, locking, sync and anchors

// Recheck the whole chain before appending; earlier corruption must block later entries
// too
func TestAuditLog_AppendRefusesToExtendBrokenChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auditor.json")
	if err := AppendAuditEvent(path, "unlock_success", nil); err != nil {
		t.Fatalf("AppendAuditEvent: %v", err)
	}
	data, err := os.ReadFile(auditLogPath(path))
	if err != nil {
		t.Fatalf("reading audit log: %v", err)
	}
	corrupted := bytes.ReplaceAll(data, []byte("unlock_success"), []byte("unlock_failure"))
	if err := os.WriteFile(auditLogPath(path), corrupted, 0o600); err != nil {
		t.Fatalf("writing corrupted log: %v", err)
	}

	if err := AppendAuditEvent(path, "signed", nil); err == nil {
		t.Fatal("expected AppendAuditEvent to refuse to extend a broken chain")
	} else if !strings.Contains(err.Error(), "chain is broken") {
		t.Errorf("unexpected error: %v", err)
	}
}

// Tail deletion leaves a valid shorter chain; the retained anchor must detect it
func TestAuditLogAnchored_DetectsTruncation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auditor.json")
	anchorPath := DefaultAnchorPath(path)
	for i := 0; i < 3; i++ {
		if err := AppendAuditEvent(path, fmt.Sprintf("event-%d", i), nil); err != nil {
			t.Fatalf("AppendAuditEvent: %v", err)
		}
	}
	if err := VerifyAuditLogAnchored(path, anchorPath); err != nil {
		t.Fatalf("expected a fresh log+anchor to verify, got: %v", err)
	}

	// Retain only the first entry: a valid shorter chain
	lines, err := readAuditLines(path)
	if err != nil {
		t.Fatalf("readAuditLines: %v", err)
	}
	if len(lines) != 3 {
		t.Fatalf("expected 3 lines before truncation, got %d", len(lines))
	}
	if err := os.WriteFile(auditLogPath(path), append(lines[0], '\n'), 0o600); err != nil {
		t.Fatalf("writing truncated log: %v", err)
	}
	if err := VerifyAuditLog(path); err != nil {
		t.Fatalf("truncated-but-internally-consistent log unexpectedly fails VerifyAuditLog: %v", err)
	}

	if err := VerifyAuditLogAnchored(path, anchorPath); err == nil {
		t.Fatal("expected VerifyAuditLogAnchored to detect truncation that VerifyAuditLog alone cannot")
	} else if !strings.Contains(err.Error(), "truncated") {
		t.Errorf("unexpected error: %v", err)
	}
}

// Detect recomputed history whose hash at the anchored sequence differs, even without
// shortening the log
func TestAuditLogAnchored_DetectsRewrittenHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auditor.json")
	anchorPath := DefaultAnchorPath(path)
	if err := AppendAuditEvent(path, "event-a", nil); err != nil {
		t.Fatalf("AppendAuditEvent: %v", err)
	}
	if err := AppendAuditEvent(path, "event-b", nil); err != nil {
		t.Fatalf("AppendAuditEvent: %v", err)
	}

	// Recompute a same-length chain from altered entry 1; it passes VerifyAuditLog but
	// disagrees with the anchor
	e1 := AuditEvent{Seq: 1, Time: time.Now().UTC().Format(time.RFC3339), Event: "forged", PrevHash: ""}
	h1, err := hashEvent(e1)
	if err != nil {
		t.Fatalf("hashEvent: %v", err)
	}
	e1.Hash = h1
	e2 := AuditEvent{Seq: 2, Time: time.Now().UTC().Format(time.RFC3339), Event: "forged-2", PrevHash: h1}
	h2, err := hashEvent(e2)
	if err != nil {
		t.Fatalf("hashEvent: %v", err)
	}
	e2.Hash = h2
	l1, _ := json.Marshal(e1)
	l2, _ := json.Marshal(e2)
	forged := append(append(l1, '\n'), append(l2, '\n')...)
	if err := os.WriteFile(auditLogPath(path), forged, 0o600); err != nil {
		t.Fatalf("writing forged log: %v", err)
	}
	if err := VerifyAuditLog(path); err != nil {
		t.Fatalf("forged-but-internally-consistent log unexpectedly fails VerifyAuditLog: %v", err)
	}

	if err := VerifyAuditLogAnchored(path, anchorPath); err == nil {
		t.Fatal("expected VerifyAuditLogAnchored to detect rewritten history")
	} else if !strings.Contains(err.Error(), "rewritten") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestAuditLogAnchored_EmptyAnchorVerifiesClean(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auditor.json")
	if err := VerifyAuditLogAnchored(path, DefaultAnchorPath(path)); err != nil {
		t.Errorf("expected a brand-new keystore with no history to verify clean, got: %v", err)
	}
}

// Race AppendAuditEvent goroutines through the same OS lock used by concurrent signer
// processes; sequence and hashes must remain linear
func TestAuditLog_ConcurrentAppendsDoNotForkTheChain(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auditor.json")
	const n = 25

	var wg sync.WaitGroup
	errs := make([]error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			errs[i] = AppendAuditEvent(path, fmt.Sprintf("event-%d", i), nil)
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Errorf("goroutine %d: AppendAuditEvent: %v", i, err)
		}
	}
	if err := VerifyAuditLog(path); err != nil {
		t.Fatalf("expected a valid, unforked chain after concurrent appends, got: %v", err)
	}
	lines, err := readAuditLines(path)
	if err != nil {
		t.Fatalf("readAuditLines: %v", err)
	}
	if len(lines) != n {
		t.Fatalf("expected exactly %d entries (no lost or duplicated appends), got %d", n, len(lines))
	}
}

func TestAcquireAuditLock_BreaksStaleLockFromCrashedProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auditor.json")
	lockFile := auditLockPath(path)
	if err := os.WriteFile(lockFile, []byte("99999999\n"), 0o600); err != nil {
		t.Fatalf("writing pre-existing lock file: %v", err)
	}
	stale := time.Now().Add(-auditLockStaleAfter - time.Second)
	if err := os.Chtimes(lockFile, stale, stale); err != nil {
		t.Fatalf("Chtimes: %v", err)
	}

	release, err := acquireAuditLock(path)
	if err != nil {
		t.Fatalf("expected acquireAuditLock to break a stale lock, got: %v", err)
	}
	release()
}

// An unwritable audit directory must return an append error so cmd/signer can abort
func TestAuditLog_AppendFailsOnUnwritableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory permission bits do not work the same way on windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root ignores permission bits, so this can't be exercised here")
	}
	dir := t.TempDir()
	roDir := filepath.Join(dir, "readonly")
	if err := os.Mkdir(roDir, 0o500); err != nil {
		t.Fatalf("Mkdir: %v", err)
	}
	t.Cleanup(func() { os.Chmod(roDir, 0o700) }) // let t.TempDir() clean up afterward
	path := filepath.Join(roDir, "auditor.json")

	if err := AppendAuditEvent(path, "unlock_success", nil); err == nil {
		t.Fatal("expected AppendAuditEvent to fail when it cannot write to the containing directory")
	}
}

// appendDurable must surface open, write and sync failures; a log line that was not
// durably stored must not be reported as recorded
func TestAppendDurable_ReportsEachFailure(t *testing.T) {
	t.Run("open fails", func(t *testing.T) {
		err := appendDurable(filepath.Join(t.TempDir(), "no-such-dir", "log.jsonl"), []byte("x\n"))
		if err == nil || !strings.Contains(err.Error(), "opening") {
			t.Errorf("error = %v, want an opening error", err)
		}
	})
	if runtime.GOOS != "linux" {
		return
	}
	t.Run("write fails on a full device", func(t *testing.T) {
		if _, err := os.Stat("/dev/full"); err != nil {
			t.Skip("/dev/full is not available, so a failing write cannot be provoked")
		}
		err := appendDurable("/dev/full", []byte("x\n"))
		if err == nil || !strings.Contains(err.Error(), "writing") {
			t.Errorf("error = %v, want a writing error", err)
		}
	})
	t.Run("sync fails on a device that cannot sync", func(t *testing.T) {
		f, err := os.OpenFile("/dev/null", os.O_WRONLY, 0)
		if err != nil {
			t.Skipf("/dev/null is not writable: %v", err)
		}
		syncErr := f.Sync()
		f.Close()
		if syncErr == nil {
			t.Skip("fsync on /dev/null succeeds on this system, so a failing sync cannot be provoked")
		}
		err = appendDurable("/dev/null", []byte("x\n"))
		if err == nil || !strings.Contains(err.Error(), "syncing") {
			t.Errorf("error = %v, want a syncing error", err)
		}
	})
	t.Run("success appends and keeps earlier lines", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "log.jsonl")
		for _, line := range []string{"a\n", "b\n"} {
			if err := appendDurable(path, []byte(line)); err != nil {
				t.Fatal(err)
			}
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "a\nb\n" {
			t.Errorf("log = %q, want %q", got, "a\nb\n")
		}
	})
}
