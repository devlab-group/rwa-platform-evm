package keystore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

// AuditEvent is one hash-chained line in the tamper-evident keystore-unlock
// audit log. Only non-sensitive metadata belongs in Fields — AppendAuditEvent's
// caller must never pass a password or private key material.
type AuditEvent struct {
	Seq      int               `json:"seq"`
	Time     string            `json:"time"` // RFC3339, UTC
	Event    string            `json:"event"`
	Fields   map[string]string `json:"fields,omitempty"`
	PrevHash string            `json:"prevHash"`
	Hash     string            `json:"hash"`
}

// AnchorEntry is one line in a keystore's independent "high-water mark"
// log: the (seq, hash) of the newest AuditEvent as of the moment it was
// durably appended. See DefaultAnchorPath and VerifyAuditLogAnchored.
type AnchorEntry struct {
	Seq  int    `json:"seq"`
	Hash string `json:"hash"`
	Time string `json:"time"`
}

func auditLogPath(keystorePath string) string { return keystorePath + ".auditlog.jsonl" }

// DefaultAnchorPath returns the default independent-anchor log path for
// keystorePath. It lives beside the keystore by default, which detects an
// attacker who edits or rewinds only the main audit log; genuine resistance
// to an attacker with full filesystem access to that disk requires pointing
// "signer sign"'s --audit-anchor flag at physically separate media instead
// (see docs/auditor/auditor-guide.md §5) -- two files on the same disk
// cannot be made mutually tamper-proof against whoever can write to that disk.
func DefaultAnchorPath(keystorePath string) string { return keystorePath + ".auditlog.anchor.jsonl" }

func auditLockPath(keystorePath string) string { return keystorePath + ".auditlog.lock" }

const (
	auditLockTimeout      = 30 * time.Second
	auditLockPollInterval = 50 * time.Millisecond
	// auditLockStaleAfter is how old an uncontested lock file must be
	// before a new caller assumes its owner crashed and breaks it. This is
	// a portable (pure os package, no flock/cgo) substitute for OS-level
	// advisory locking, adequate for a single-operator air-gapped CLI where
	// "concurrent" means "two signer invocations close together," not a
	// high-throughput multi-writer system.
	auditLockStaleAfter = 2 * time.Minute
)

// acquireAuditLock creates an exclusive lock file guarding keystorePath's
// audit log and anchor log together: without it, two signer processes
// verifying-then-appending at the same time could fork the sequence/hash
// chain. The returned release func MUST be called (via defer) to drop the lock.
//
// A fresh, uncontested lock is always available immediately. A lock file
// left behind by a process that crashed while holding it is broken after
// auditLockStaleAfter, so a dead process can never permanently wedge a
// keystore's audit trail.
func acquireAuditLock(keystorePath string) (release func(), err error) {
	path := auditLockPath(keystorePath)
	deadline := time.Now().Add(auditLockTimeout)
	for {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err == nil {
			fmt.Fprintf(f, "%d\n", os.Getpid())
			f.Close()
			return func() { os.Remove(path) }, nil
		}
		if !os.IsExist(err) {
			return nil, fmt.Errorf("keystore: creating audit log lock %s: %w", path, err)
		}
		if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > auditLockStaleAfter {
			os.Remove(path) // break a lock abandoned by a crashed process
			continue
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("keystore: timed out after %s waiting for the audit log lock %s -- another signer process may be running against this keystore", auditLockTimeout, path)
		}
		time.Sleep(auditLockPollInterval)
	}
}

// hashEvent computes e's chain hash over every field except Hash itself
// (which is what's being computed), including PrevHash — so altering any
// field of any entry, or splicing in a different PrevHash, changes that
// entry's Hash and breaks the chain from that point forward.
func hashEvent(e AuditEvent) (string, error) {
	e.Hash = ""
	b, err := json.Marshal(e)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:]), nil
}

func readLines(path string) ([][]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("keystore: reading %s: %w", path, err)
	}
	data = bytes.TrimRight(data, "\n")
	if len(data) == 0 {
		return nil, nil
	}
	return bytes.Split(data, []byte("\n")), nil
}

func readAuditLines(keystorePath string) ([][]byte, error) {
	return readLines(auditLogPath(keystorePath))
}

// verifyChain recomputes and checks every entry's hash chain and sequence
// number in lines, returning the last entry's (seq, hash) -- (0, "") for
// an empty log -- or an error describing the first broken link found.
func verifyChain(lines [][]byte) (lastSeq int, lastHash string, err error) {
	prevHash, prevSeq := "", 0
	for i, line := range lines {
		var e AuditEvent
		if err := json.Unmarshal(line, &e); err != nil {
			return 0, "", fmt.Errorf("audit log line %d: %w", i+1, err)
		}
		if e.PrevHash != prevHash {
			return 0, "", fmt.Errorf("audit log line %d: broken hash chain (prevHash mismatch)", i+1)
		}
		if e.Seq != prevSeq+1 {
			return 0, "", fmt.Errorf("audit log line %d: out-of-order seq %d (want %d)", i+1, e.Seq, prevSeq+1)
		}
		want, err := hashEvent(e)
		if err != nil {
			return 0, "", fmt.Errorf("audit log line %d: %w", i+1, err)
		}
		if e.Hash != want {
			return 0, "", fmt.Errorf("audit log line %d: hash mismatch (entry was modified)", i+1)
		}
		prevHash, prevSeq = e.Hash, e.Seq
	}
	return prevSeq, prevHash, nil
}

// appendDurable appends line (already newline-terminated by the caller) to
// path, creating it if necessary, and fsyncs it before returning -- the
// append must survive a crash immediately after this call returns, not just
// live in the OS page cache.
func appendDurable(path string, line []byte) error {
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening %s: %w", path, err)
	}
	defer f.Close()
	if _, err := f.Write(line); err != nil {
		return fmt.Errorf("writing %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", path, err)
	}
	return nil
}

// AppendAuditEvent appends one hash-chained entry to keystorePath's audit
// log and records its (seq, hash) in the default anchor log
// (DefaultAnchorPath). Equivalent to
// AppendAuditEventTo(keystorePath, DefaultAnchorPath(keystorePath), event, fields).
func AppendAuditEvent(keystorePath, event string, fields map[string]string) error {
	return AppendAuditEventTo(keystorePath, DefaultAnchorPath(keystorePath), event, fields)
}

// AppendAuditEventTo appends one hash-chained entry to keystorePath's
// audit log, recording its (seq, hash) in anchorPath, under an exclusive
// lock spanning the whole verify-then-append sequence. Specifically:
//
//   - The existing chain is fully re-verified (not just its last line
//     trusted) before the new entry is built, so a corrupted log is refused
//     rather than silently extended, and the read-verify-append sequence is
//     atomic with respect to any other process doing the same against this
//     keystore (acquireAuditLock).
//   - Both the main log and the anchor entry are fsynced before this
//     function returns.
//   - The anchor entry lets a later VerifyAuditLogAnchored detect wholesale
//     truncation of the newest main-log entries, which an internal hash
//     chain alone cannot (see VerifyAuditLog's doc).
func AppendAuditEventTo(keystorePath, anchorPath, event string, fields map[string]string) error {
	release, err := acquireAuditLock(keystorePath)
	if err != nil {
		return err
	}
	defer release()

	lines, err := readAuditLines(keystorePath)
	if err != nil {
		return err
	}
	lastSeq, lastHash, err := verifyChain(lines)
	if err != nil {
		return fmt.Errorf("keystore: refusing to append: audit log chain is broken: %w", err)
	}

	e := AuditEvent{Seq: lastSeq + 1, Time: time.Now().UTC().Format(time.RFC3339), Event: event, Fields: fields, PrevHash: lastHash}
	hash, err := hashEvent(e)
	if err != nil {
		return fmt.Errorf("keystore: hashing audit event: %w", err)
	}
	e.Hash = hash

	line, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("keystore: encoding audit event: %w", err)
	}
	if err := appendDurable(auditLogPath(keystorePath), append(line, '\n')); err != nil {
		return fmt.Errorf("keystore: appending audit log: %w", err)
	}

	anchor := AnchorEntry{Seq: e.Seq, Hash: e.Hash, Time: e.Time}
	anchorLine, err := json.Marshal(anchor)
	if err != nil {
		return fmt.Errorf("keystore: encoding anchor entry: %w", err)
	}
	if err := appendDurable(anchorPath, append(anchorLine, '\n')); err != nil {
		return fmt.Errorf("keystore: appending anchor log (main audit entry seq=%d was already durably recorded): %w", e.Seq, err)
	}
	return nil
}

// VerifyAuditLog recomputes and checks every entry's hash chain and
// sequence number in keystorePath's audit log, returning an error
// describing the first broken link found, if any.
//
// This proves that no entry present in the file has been individually
// altered or reordered relative to its neighbors. It does NOT by itself
// prove the file was never truncated: an attacker who can also delete the
// most recent N lines wholesale leaves a chain that still verifies
// internally, since a hash chain alone cannot attest to a length it
// doesn't record externally. Use VerifyAuditLogAnchored to also catch
// that.
func VerifyAuditLog(keystorePath string) error {
	lines, err := readAuditLines(keystorePath)
	if err != nil {
		return err
	}
	_, _, err = verifyChain(lines)
	if err != nil {
		return fmt.Errorf("keystore: %w", err)
	}
	return nil
}

// VerifyAuditLogAnchored is VerifyAuditLog plus a truncation check against
// anchorPath's independently-recorded high-water mark: call it before
// unlock/sign, not just VerifyAuditLog alone, so a main log
// that was silently rewound or truncated (as opposed to merely edited in
// place) is also refused. An empty or missing anchor log verifies clean --
// there is nothing yet to cross-check against for a brand-new keystore.
//
// As documented on DefaultAnchorPath, this only detects truncation if
// anchorPath is actually harder for an attacker to rewrite than the main
// log -- e.g. it lives on separate media via --audit-anchor. Two files on
// the same disk, writable by the same attacker, offer no truncation
// resistance to each other; VerifyAuditLogAnchored still runs the
// comparison in that configuration (so a *careless* or partial
// truncation/edit is still caught), but it is not a security boundary in
// that configuration.
func VerifyAuditLogAnchored(keystorePath, anchorPath string) error {
	if err := VerifyAuditLog(keystorePath); err != nil {
		return err
	}

	anchorLines, err := readLines(anchorPath)
	if err != nil {
		return fmt.Errorf("keystore: reading anchor log: %w", err)
	}
	if len(anchorLines) == 0 {
		return nil
	}
	var lastAnchor AnchorEntry
	if err := json.Unmarshal(anchorLines[len(anchorLines)-1], &lastAnchor); err != nil {
		return fmt.Errorf("keystore: anchor log is corrupted: %w", err)
	}

	mainLines, err := readAuditLines(keystorePath)
	if err != nil {
		return err
	}
	if len(mainLines) < lastAnchor.Seq {
		return fmt.Errorf("keystore: audit log has only %d entries but the anchor log recorded seq=%d -- the audit log appears to have been truncated", len(mainLines), lastAnchor.Seq)
	}
	// mainLines is 1-indexed by Seq (verified contiguous by VerifyAuditLog
	// above), so the entry at Seq lastAnchor.Seq is at this slice index.
	var atAnchorSeq AuditEvent
	if err := json.Unmarshal(mainLines[lastAnchor.Seq-1], &atAnchorSeq); err != nil {
		return fmt.Errorf("keystore: audit log line %d: %w", lastAnchor.Seq, err)
	}
	if atAnchorSeq.Hash != lastAnchor.Hash {
		return fmt.Errorf("keystore: audit log entry seq=%d has hash %s, but the anchor log recorded %s for that sequence -- the audit log appears to have been rewritten", lastAnchor.Seq, atAnchorSeq.Hash, lastAnchor.Hash)
	}
	return nil
}
