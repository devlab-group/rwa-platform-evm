package ipfs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// LocalDestinationName is the fixed name ReplicationManager uses for its
// primary local node's DestinationStatus entry.
const LocalDestinationName = "local"

// ErrPublicationCIDConflict is returned by Publish/AddRaw when an existing
// PublicationRecord for the caller-chosen id is already bound to a DIFFERENT
// CID than the one being published. A publication id (e.g. an
// AssetRecord's RecordID) is immutably bound to the content whose durable
// availability the mint gate later proves; rebinding it to different bytes —
// a duplicate asset create with changed metadata, or a retried create with
// changed input — would let relay prove durability for content the record was
// never signed against. The binding is a conflict, never an overwrite.
var ErrPublicationCIDConflict = errors.New("ipfs: publication id already bound to a different CID")

// Destination is one independent backup pinning target for replicated
// content; the platform requires at least one. It reuses Client's shape
// (AddRaw/Pin/Get) — a second live
// Kubo node, an IPFS Cluster HTTP shim, and a FileArchiveClient are all
// equally valid backing implementations.
type Destination struct {
	Name   string
	Client Client
}

// ReplicationManager coordinates publishing content to a local node plus a
// configured set of independent backup Destinations, and durably tracks
// per-item replication status through models.PublicationState. It implements
// the same AddRaw(ctx, data) (string, error) shape
// internal/assets' ipfsPinner interface expects, so it is a drop-in
// replacement for a bare Client wherever that interface is used — Publish
// is the richer entry point for callers that have their own meaningful
// record ID to key the PublicationRecord by.
type ReplicationManager struct {
	local     Client
	backups   []Destination
	threshold int
	records   repository.PublicationRepository
	now       func() time.Time
}

// NewReplicationManager constructs a ReplicationManager. threshold is how
// many of the configured backups (NOT counting local) must successfully
// pin+verify before a PublicationRecord is considered PublicationReplicated.
// An asset package must not be marked durably published until the configured
// replication threshold has been met. threshold<=0 means
// "local pin alone is sufficient" — an explicit, documented single-copy
// opt-out, not the default a caller falls into by omission.
func NewReplicationManager(local Client, backups []Destination, threshold int, records repository.PublicationRepository) *ReplicationManager {
	return &ReplicationManager{local: local, backups: backups, threshold: threshold, records: records, now: func() time.Time { return time.Now().UTC() }}
}

// AddRaw implements the ipfsPinner shape (see internal/assets), keying the
// PublicationRecord by the content's own CID. Prefer Publish when the
// caller has a more meaningful id (e.g. an AssetRecord's RecordID) to look
// up publication status by later.
func (rm *ReplicationManager) AddRaw(ctx context.Context, data []byte) (string, error) {
	cid, err := rm.local.AddRaw(ctx, data)
	if err != nil {
		return "", fmt.Errorf("ipfs: add to local node: %w", err)
	}
	return cid, rm.replicate(ctx, cid, cid, data)
}

// Publish is AddRaw plus a caller-chosen id to key the PublicationRecord by.
func (rm *ReplicationManager) Publish(ctx context.Context, id string, data []byte) (cid string, err error) {
	cid, err = rm.local.AddRaw(ctx, data)
	if err != nil {
		return "", fmt.Errorf("ipfs: add to local node: %w", err)
	}
	return cid, rm.replicate(ctx, id, cid, data)
}

// replicate pins data locally, attempts every configured backup, and
// persists the resulting PublicationRecord.
//
// Every destination — local included — is verified
// SYNCHRONOUSLY before counting toward Replicated, not just on the later
// periodic Verify ticker: a destination's own returned CID from AddRaw
// MUST equal the locally computed cid (a destination that silently
// computed/stored something else is a failure, not a success with a
// surprising CID), and the content is immediately retrieved back and
// digest-checked (a bare AddRaw+Pin API success is not proof the content
// is actually retrievable — see Verify's doc comment for the same
// reasoning applied on the recurring path).
//
// It does not itself return an error for a BACKUP failure (that is
// exactly what PublicationReplicationFailed/ReplicationPending are for —
// visible via Status/DestinationStatus.LastError and retryable via Retry,
// not raised as an exception on the publish path, so one flaky backup
// does not block ordinary content creation); it only errors if the LOCAL
// pin+verify fails or the record can't be persisted at all — local is
// this content's only source of truth until at least one backup is
// verified, so a local failure IS fatal to publishing at all.
func (rm *ReplicationManager) replicate(ctx context.Context, id, cid string, data []byte) error {
	now := rm.now()
	rec, err := rm.records.Get(ctx, id)
	switch {
	case err == nil:
		// An existing publication row for id is immutably bound
		// to its recorded CID. A publish carrying a DIFFERENT CID is a
		// conflict, never an overwrite — otherwise a duplicate/retried asset
		// create with changed metadata could move the durable-publication gate
		// onto content the signed record never covered (see
		// ErrPublicationCIDConflict).
		if rec.CID != "" && rec.CID != cid {
			return fmt.Errorf("%w: id %s is bound to %s, refusing to rebind to %s", ErrPublicationCIDConflict, id, rec.CID, cid)
		}
	case errors.Is(err, repository.ErrNotFound):
		rec = &models.PublicationRecord{ID: id, CreatedAt: now}
	default:
		// A transient read failure is NOT "no such row" — do
		// not fabricate a fresh replacement row that would silently drop an
		// existing CID binding; surface the error and let the caller retry.
		return fmt.Errorf("ipfs: load publication record %s: %w", id, err)
	}
	rec.CID = cid
	rec.ReplicationThreshold = rm.threshold
	rec.UpdatedAt = now

	local := findOrAppendDestination(&rec.Destinations, LocalDestinationName)
	if err := rm.pinAndVerifySynchronously(ctx, rm.local, cid, local, now); err != nil {
		rec.State = models.PublicationCreated
		_ = rm.records.Upsert(ctx, rec)
		return fmt.Errorf("ipfs: pin locally: %w", err)
	}
	rec.State = models.PublicationPinnedLocally

	succeeded := 0
	var backupErrs []error
	for _, b := range rm.backups {
		status := findOrAppendDestination(&rec.Destinations, b.Name)
		if err := rm.addAndVerifySynchronously(ctx, b.Client, cid, data, status, now); err != nil {
			backupErrs = append(backupErrs, fmt.Errorf("%s: %w", b.Name, err))
			continue
		}
		succeeded++
	}

	rec.State = replicationState(succeeded, len(rm.backups), rm.threshold)
	if err := rm.records.Upsert(ctx, rec); err != nil {
		return err
	}
	// Backup failures ARE surfaced to the publishing caller (joined,
	// non-fatal to the publish having happened at all: the content IS
	// locally pinned and verified, and DestinationStatus/Status remain
	// the durable record) so a caller that wants to log/alert immediately
	// can, without forcing every publish to hard-fail on one flaky
	// backup.
	return errors.Join(backupErrs...)
}

// pinAndVerifySynchronously pins cid on client (already holding the
// content, from a prior AddRaw) and immediately retrieves+digest-verifies
// it, updating status in place.
func (rm *ReplicationManager) pinAndVerifySynchronously(ctx context.Context, client Client, cid string, status *models.DestinationStatus, now time.Time) error {
	if err := client.Pin(ctx, cid); err != nil {
		status.Pinned = false
		status.Verified = false
		status.LastError = err.Error()
		return fmt.Errorf("pin: %w", err)
	}
	status.Pinned = true
	if err := rm.retrieveAndVerify(ctx, client, cid); err != nil {
		status.Verified = false
		status.LastError = err.Error()
		return err
	}
	status.Verified = true
	status.LastVerifiedAt = now
	status.LastError = ""
	return nil
}

// addAndVerifySynchronously is pinAndVerifySynchronously plus the AddRaw
// step and the returned-CID check: a destination's returned CID must equal
// the locally computed CID (discarding AddRaw's returned value would let a
// destination silently store something else).
func (rm *ReplicationManager) addAndVerifySynchronously(ctx context.Context, client Client, cid string, data []byte, status *models.DestinationStatus, now time.Time) error {
	gotCID, err := client.AddRaw(ctx, data)
	if err != nil {
		status.Pinned = false
		status.Verified = false
		status.LastError = fmt.Sprintf("add: %v", err)
		return fmt.Errorf("add: %w", err)
	}
	if gotCID != cid {
		status.Pinned = false
		status.Verified = false
		status.LastError = fmt.Sprintf("destination returned CID %s, expected %s", gotCID, cid)
		return fmt.Errorf("returned CID %s does not match expected %s", gotCID, cid)
	}
	return rm.pinAndVerifySynchronously(ctx, client, cid, status, now)
}

// retrieveAndVerify fetches cid from client and confirms the retrieved
// bytes actually hash to it — shared by the synchronous publish-time check
// above and the periodic Verify below, so both apply the exact same
// standard.
func (rm *ReplicationManager) retrieveAndVerify(ctx context.Context, client Client, cid string) error {
	data, err := client.Get(ctx, cid)
	if err != nil {
		return fmt.Errorf("retrieve: %w", err)
	}
	gotCID, err := ComputeCIDv1Raw(data)
	if err != nil || gotCID != cid {
		return errors.New("retrieved content does not hash to the expected CID")
	}
	return nil
}

// replicationState decides the overall PublicationState from how many of
// the configured backups just succeeded against the replication threshold.
func replicationState(succeeded, configured, threshold int) models.PublicationState {
	switch {
	case threshold <= 0:
		return models.PublicationReplicated // explicit single-copy opt-out — see NewReplicationManager's doc comment
	case succeeded >= threshold:
		return models.PublicationReplicated
	case succeeded > 0 || configured == 0:
		// configured==0 with threshold>0 is a MISCONFIGURATION (a threshold
		// that can never be met) — ReplicationPending forever is the
		// correct, honest reflection of that, not silently Replicated.
		return models.PublicationReplicationPending
	default:
		return models.PublicationReplicationFailed
	}
}

// findOrAppendDestination returns the DestinationStatus for name within
// *destinations, appending a fresh zero-value one if absent.
func findOrAppendDestination(destinations *[]models.DestinationStatus, name string) *models.DestinationStatus {
	for i := range *destinations {
		if (*destinations)[i].Name == name {
			return &(*destinations)[i]
		}
	}
	*destinations = append(*destinations, models.DestinationStatus{Name: name})
	return &(*destinations)[len(*destinations)-1]
}

// IsDurablyPublished reports whether id has met its configured replication
// threshold AND is still bound to expectedCID: the durability proof must be
// for the EXACT content the caller is about to mint against, not merely "some
// content under this id is replicated". A missing publication row is reported as
// not-durable (false, nil), not an error, so the mint relay path surfaces its
// own friendly "retry once replicated" message rather than a raw lookup error.
func (rm *ReplicationManager) IsDurablyPublished(ctx context.Context, id, expectedCID string) (bool, error) {
	rec, err := rm.records.Get(ctx, id)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return false, nil
		}
		return false, err
	}
	return rec.State == models.PublicationReplicated && rec.CID == expectedCID, nil
}

// Status returns id's current PublicationRecord.
func (rm *ReplicationManager) Status(ctx context.Context, id string) (*models.PublicationRecord, error) {
	return rm.records.Get(ctx, id)
}

// clientFor resolves a DestinationStatus's Name back to the Client that
// owns it (local or a configured backup), used by Verify.
func (rm *ReplicationManager) clientFor(name string) (Client, bool) {
	if name == LocalDestinationName {
		return rm.local, true
	}
	for _, b := range rm.backups {
		if b.Name == name {
			return b.Client, true
		}
	}
	return nil, false
}

// Verify re-fetches id's content from EVERY configured destination (local
// and every backup) and confirms the retrieved bytes actually hash to the
// recorded CID. Verification retrieves the content and confirms its digest
// matches the expected CID — a successful HTTP or API response is
// insufficient by itself. Updates each
// DestinationStatus's Verified/LastVerifiedAt/LastError and recomputes the
// record's overall PublicationState from how many destinations verify
// successfully right now (a destination that reports success but can't
// actually be retrieved no longer counts toward the replication threshold).
func (rm *ReplicationManager) Verify(ctx context.Context, id string) error {
	rec, err := rm.records.Get(ctx, id)
	if err != nil {
		return err
	}
	now := rm.now()
	verifiedBackups := 0
	for i := range rec.Destinations {
		status := &rec.Destinations[i]
		client, ok := rm.clientFor(status.Name)
		if !ok {
			continue // a destination removed from config since this record was written; leave its last-known status as-is
		}
		if err := rm.retrieveAndVerify(ctx, client, rec.CID); err != nil {
			status.Verified = false
			status.LastError = err.Error()
			continue
		}
		status.Verified = true
		status.LastVerifiedAt = now
		status.LastError = ""
		if status.Name != LocalDestinationName {
			verifiedBackups++
		}
	}
	rec.UpdatedAt = now
	rec.State = replicationState(verifiedBackups, len(rm.backups), rm.threshold)
	return rm.records.Upsert(ctx, rec)
}

// RestoreLocal recovers from the local IPFS node losing its repository by
// fetching id's content from the first backup destination that still has
// it and re-adding/pinning it locally.
func (rm *ReplicationManager) RestoreLocal(ctx context.Context, id string) error {
	rec, err := rm.records.Get(ctx, id)
	if err != nil {
		return err
	}
	var lastErr error
	for _, b := range rm.backups {
		data, err := b.Client.Get(ctx, rec.CID)
		if err != nil {
			lastErr = err
			continue
		}
		if gotCID, err := ComputeCIDv1Raw(data); err != nil || gotCID != rec.CID {
			lastErr = fmt.Errorf("ipfs: backup %s returned content not matching CID %s", b.Name, rec.CID)
			continue
		}
		restoredCID, err := rm.local.AddRaw(ctx, data)
		if err != nil {
			return fmt.Errorf("ipfs: restore to local node: %w", err)
		}
		// Require the local node's own returned CID to equal the
		// recorded CID (a node that stored something else is a failed restore,
		// not a success with a surprising CID), then pin + retrieve + rehash
		// via the same synchronous verification the publish path uses before
		// marking the local destination Verified — a bare AddRaw+Pin success
		// is not proof the content is actually retrievable.
		if restoredCID != rec.CID {
			return fmt.Errorf("ipfs: restored local CID %s does not match recorded CID %s", restoredCID, rec.CID)
		}
		now := rm.now()
		local := findOrAppendDestination(&rec.Destinations, LocalDestinationName)
		if err := rm.pinAndVerifySynchronously(ctx, rm.local, rec.CID, local, now); err != nil {
			rec.UpdatedAt = now
			_ = rm.records.Upsert(ctx, rec)
			return fmt.Errorf("ipfs: verify restored content locally: %w", err)
		}
		rec.UpdatedAt = now
		return rm.records.Upsert(ctx, rec)
	}
	if lastErr == nil {
		lastErr = fmt.Errorf("ipfs: no configured backup destination could restore CID %s", rec.CID)
	}
	return fmt.Errorf("ipfs: restore local repository for %s: %w", id, lastErr)
}

// Retry re-attempts every backup destination NOT currently Pinned for id,
// given the caller-supplied data, so replication failures stay visible and
// retryable. data MUST hash to the
// record's already-recorded CID; ReplicationManager does not itself retain
// raw bytes (they live in Mongo via the caller's own record, or in a
// FileArchiveClient backup, not duplicated here) so a retry needs them
// re-supplied.
func (rm *ReplicationManager) Retry(ctx context.Context, id string, data []byte) error {
	rec, err := rm.records.Get(ctx, id)
	if err != nil {
		return err
	}
	if gotCID, err := ComputeCIDv1Raw(data); err != nil || gotCID != rec.CID {
		return fmt.Errorf("ipfs: retry data does not hash to publication %s's recorded CID %s", id, rec.CID)
	}
	now := rm.now()
	// Retry runs the SAME addAndVerifySynchronously path as
	// initial publication (returned-CID check + pin + retrieve + rehash) and
	// counts ONLY verified backups toward PublicationReplicated. Counting any
	// destination whose Pinned flag was set — including one the periodic
	// Verify had already found unretrievable (Pinned=true, Verified=false) —
	// could restore Replicated without re-proving durability at all. Only a
	// currently-Verified destination is skipped; every other is re-added and
	// re-verified.
	verified := 0
	for _, b := range rm.backups {
		status := findOrAppendDestination(&rec.Destinations, b.Name)
		if status.Verified {
			verified++
			continue
		}
		if err := rm.addAndVerifySynchronously(ctx, b.Client, rec.CID, data, status, now); err != nil {
			continue // status.LastError/Verified already updated in place
		}
		verified++
	}
	rec.UpdatedAt = now
	rec.State = replicationState(verified, len(rm.backups), rm.threshold)
	return rm.records.Upsert(ctx, rec)
}
