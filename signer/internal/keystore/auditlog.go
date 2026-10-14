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

// AuditEvent is one hash-chained line in the tamper-evident keystore audit log. Fields
// holds non-sensitive metadata only; callers must never pass a password or private key
// material.
type AuditEvent struct {
	Seq      int               `json:"seq"`
	Time     string            `json:"time"` // RFC3339, UTC
	Event    string            `json:"event"`
	Fields   map[string]string `json:"fields,omitempty"`
	PrevHash string            `json:"prevHash"`
	Hash     string            `json:"hash"`
}

// AnchorEntry is one line of a keystore's anchor log, a separate record of the sequence
// number and hash of each AuditEvent, written when the event is durably appended. See
// DefaultAnchorPath and VerifyAuditLogAnchored.
type AnchorEntry struct {
	Seq  int    `json:"seq"`
	Hash string `json:"hash"`
	Time string `json:"time"`
}

func auditLogPath(keystorePath string) string { return keystorePath + ".auditlog.jsonl" }

// DefaultAnchorPath places the anchor beside the keystore. It detects main-log
// edits or truncation only while the anchor remains intact. Use signer sign
// --audit-anchor on independently protected media against an attacker who can
// rewrite both files; see docs/auditor/auditor-guide.md §5.
func DefaultAnchorPath(keystorePath string) string { return keystorePath + ".auditlog.anchor.jsonl" }

func auditLockPath(keystorePath string) string { return keystorePath + ".auditlog.lock" }

const (
	auditLockTimeout      = 30 * time.Second
	auditLockPollInterval = 50 * time.Millisecond
	// auditLockStaleAfter is the age at which a lock file is assumed abandoned by a
	// crashed process and broken. The lock uses only the os package, with no flock or
	// cgo, which suits a single-operator CLI where concurrent means two signer runs
	// close together. The lock is held only while one entry is verified and appended.
	auditLockStaleAfter = 2 * time.Minute
)

// acquireAuditLock exclusively locks the keystore's audit and anchor logs across
// verification and append, preventing concurrent signer processes from forking
// the hash chain. The caller must release it. Locks older than auditLockStaleAfter
// are removed; waiting times out after auditLockTimeout.
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
			return nil, fmt.Errorf("keystore: timed out after %s waiting for the audit log lock %s; another signer process may be running against this keystore", auditLockTimeout, path)
		}
		time.Sleep(auditLockPollInterval)
	}
}

// hashEvent computes the chain hash of e over every field except Hash, including
// PrevHash, so altering any field of an entry, or splicing in a different PrevHash,
// changes its Hash and breaks the chain from that point on
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

// verifyChain recomputes every entry's hash and checks the hash chain and sequence
// numbers in lines. It returns the last entry's seq and hash, or (0, "") for an empty
// log, or an error describing the first broken link.
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

// appendDurable appends line (already newline-terminated) to path, creating it if
// needed, and fsyncs before returning, so the append survives a crash right after the
// call and does not live only in the OS page cache
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

// AppendAuditEvent is AppendAuditEventTo with the default anchor path,
// DefaultAnchorPath(keystorePath)
func AppendAuditEvent(keystorePath, event string, fields map[string]string) error {
	return AppendAuditEventTo(keystorePath, DefaultAnchorPath(keystorePath), event, fields)
}

// AppendAuditEventTo locks, verifies the full existing chain and appends one
// hash-chained entry, recording its sequence and hash in anchorPath. Both logs
// are fsynced before return. The lock covers read, verification and append;
// the anchor lets VerifyAuditLogAnchored detect deletion of recent main-log entries.
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

// VerifyAuditLog checks every entry's hash, sequence and chain link, returning
// the first failure. A valid chain detects alterations or reordering relative
// to neighboring entries but cannot detect removed final lines. Use
// VerifyAuditLogAnchored to compare the retained length with an anchor.
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

// VerifyAuditLogAnchored verifies the chain and compares it with the latest
// anchor entry to detect truncation before unlock or signing. An absent or
// empty anchor passes for a new keystore. Protection requires an independently
// protected anchor; two equally writable files do not establish a security boundary.
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
		return fmt.Errorf("keystore: audit log has only %d entries but the anchor log recorded seq=%d; the audit log appears to have been truncated", len(mainLines), lastAnchor.Seq)
	}
	// VerifyAuditLog checked that sequence numbers run contiguously from 1, so entry N
	// is at index N-1
	var atAnchorSeq AuditEvent
	if err := json.Unmarshal(mainLines[lastAnchor.Seq-1], &atAnchorSeq); err != nil {
		return fmt.Errorf("keystore: audit log line %d: %w", lastAnchor.Seq, err)
	}
	if atAnchorSeq.Hash != lastAnchor.Hash {
		return fmt.Errorf("keystore: audit log entry seq=%d has hash %s, but the anchor log recorded %s for that sequence; the audit log appears to have been rewritten", lastAnchor.Seq, atAnchorSeq.Hash, lastAnchor.Hash)
	}
	return nil
}
