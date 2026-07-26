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
	// This is a guard against a future call site accidentally passing
	// secret material through AppendAuditEvent's free-form fields map --
	// it does not (and cannot) inspect field *values*, only catch the
	// obvious case of a caller mislabeling a field.
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

// --- verify-before-append, locking, fsync, anchor ---

// TestAuditLog_AppendRefusesToExtendBrokenChain checks that AppendAuditEvent
// fully re-verifies the existing chain before adding to it, not just trusting
// the final line, so corruption earlier in the log
// stops further signing activity from being recorded as if the log were
// still intact.
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

// TestAuditLogAnchored_DetectsTruncation: deleting the newest entries from
// the main log leaves its own hash chain internally
// valid (there is nothing after the cut to contradict), so only a
// cross-check against the independently-recorded anchor can catch it.
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

	// Truncate the main log back to its first entry only. Its remaining
	// content is still a perfectly valid (shorter) chain on its own.
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

// TestAuditLogAnchored_DetectsRewrittenHistory covers the companion case:
// the main log still has as many (or more) entries as the anchor recorded,
// but the entry at the anchored sequence number no longer has the hash the
// anchor remembers -- i.e. history was rewritten and re-extended rather
// than merely shortened.
func TestAuditLogAnchored_DetectsRewrittenHistory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "auditor.json")
	anchorPath := DefaultAnchorPath(path)
	if err := AppendAuditEvent(path, "event-a", nil); err != nil {
		t.Fatalf("AppendAuditEvent: %v", err)
	}
	if err := AppendAuditEvent(path, "event-b", nil); err != nil {
		t.Fatalf("AppendAuditEvent: %v", err)
	}

	// Rewrite from entry 1 with different content, re-deriving a
	// self-consistent chain of the same length -- passes VerifyAuditLog on
	// its own, since it's a perfectly valid chain, just not the same one
	// the anchor witnessed.
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

// TestAuditLog_ConcurrentAppendsDoNotForkTheChain: concurrent signer
// processes must not be able to fork the sequence/hash chain. This test
// drives that race in-process (goroutines calling
// AppendAuditEvent against the same keystore path concurrently) rather
// than spawning real subprocesses, which exercises the same
// acquireAuditLock/verify-then-append critical section a second real
// `signer sign` process running at the same time would hit.
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

// TestAuditLog_AppendFailsOnUnwritableDirectory is this package's stand-in
// for "disk full": a directory the process cannot write into is the most
// portably-testable way to force the same class of failure (the append
// syscall itself failing) without actually filling a filesystem. It
// verifies AppendAuditEvent surfaces the failure as an ordinary error
// rather than succeeding silently or panicking -- what cmd/signer's
// fail-closed handling (main.go) depends on to abort a signing attempt
// when the audit trail cannot be durably recorded.
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
