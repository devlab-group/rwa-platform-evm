package ipfs

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

// LocalDestinationName is the DestinationStatus name ReplicationManager uses
// for the local node
const LocalDestinationName = "local"

// ErrPublicationCIDConflict is returned by Publish when the PublicationRecord
// for id is already bound to a different CID. A publication id, such as an
// AssetRecord's RecordID, stays bound to one CID, so changed content under the
// same id is rejected instead of overwriting the record. Otherwise the record's
// durability status could describe content other than what was signed.
var ErrPublicationCIDConflict = errors.New("ipfs: publication id already bound to a different CID")

// Destination is a named backup target for replicated content. Any Client can
// back one, such as a second Kubo node or a FileArchiveClient.
type Destination struct {
	Name   string
	Client Client
}

// ReplicationManager publishes content to a local node and a set of backup
// Destinations and stores each item's replication status as a PublicationRecord
// (models.PublicationState). Its AddRaw has the signature internal/assets'
// ipfsPinner expects. Publish does the same but keys the record by a
// caller-chosen id.
type ReplicationManager struct {
	local     Client
	backups   []Destination
	threshold int
	records   repository.PublicationRepository
	now       func() time.Time
}

// NewReplicationManager returns a ReplicationManager. threshold is the number of
// backups, not counting the local node, that must pin and verify the content
// before the record reaches PublicationReplicated. A threshold of zero or less
// makes the local pin alone sufficient (a single copy).
func NewReplicationManager(local Client, backups []Destination, threshold int, records repository.PublicationRepository) *ReplicationManager {
	return &ReplicationManager{local: local, backups: backups, threshold: threshold, records: records, now: func() time.Time { return time.Now().UTC() }}
}

// AddRaw publishes data and keys the PublicationRecord by the content's CID.
// Use Publish to key it by an id such as an AssetRecord's RecordID.
func (rm *ReplicationManager) AddRaw(ctx context.Context, data []byte) (string, error) {
	cid, err := rm.local.AddRaw(ctx, data)
	if err != nil {
		return "", fmt.Errorf("ipfs: add to local node: %w", err)
	}
	return cid, rm.replicate(ctx, cid, cid, data)
}

// Publish is AddRaw with a caller-chosen id as the PublicationRecord key
func (rm *ReplicationManager) Publish(ctx context.Context, id string, data []byte) (cid string, err error) {
	cid, err = rm.local.AddRaw(ctx, data)
	if err != nil {
		return "", fmt.Errorf("ipfs: add to local node: %w", err)
	}
	return cid, rm.replicate(ctx, id, cid, data)
}

// replicate pins locally, adds each backup and stores the PublicationRecord.
// A destination counts toward the threshold only after fetching and rehashing
// the content; a backup's AddRaw CID must also match the local CID.
//
// Local pin or verification failures and record-storage failures return errors.
// Backup failures set DestinationStatus.LastError and leave replication Pending
// or Failed for Retry. Their joined errors return after the record is stored,
// so a backup failure does not discard the local publication.
func (rm *ReplicationManager) replicate(ctx context.Context, id, cid string, data []byte) error {
	now := rm.now()
	rec, err := rm.records.Get(ctx, id)
	switch {
	case err == nil:
		// the record for id stays bound to its CID; see ErrPublicationCIDConflict
		if rec.CID != "" && rec.CID != cid {
			return fmt.Errorf("%w: id %s is bound to %s, refusing to rebind to %s", ErrPublicationCIDConflict, id, rec.CID, cid)
		}
	case errors.Is(err, repository.ErrNotFound):
		rec = &models.PublicationRecord{ID: id, CreatedAt: now}
	default:
		// a read failure is not "not found"; a fresh row would drop an existing CID binding
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
	// the content is pinned and verified locally, so backup failures are
	// informational
	return errors.Join(backupErrs...)
}

// pinAndVerifySynchronously pins cid on client, which already holds the content,
// fetches it back and checks its digest, and records the outcome in status
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

// addAndVerifySynchronously is pinAndVerifySynchronously preceded by AddRaw. The
// CID the client returns must equal cid.
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

// retrieveAndVerify fetches cid from client and checks that the bytes hash to it
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

// replicationState returns the PublicationState for succeeded verified backups
// out of configured, given threshold
func replicationState(succeeded, configured, threshold int) models.PublicationState {
	switch {
	case threshold <= 0:
		return models.PublicationReplicated // single-copy mode, see NewReplicationManager
	case succeeded >= threshold:
		return models.PublicationReplicated
	case succeeded > 0 || configured == 0:
		// no backups with a positive threshold can never be met, so it stays pending
		return models.PublicationReplicationPending
	default:
		return models.PublicationReplicationFailed
	}
}

// findOrAppendDestination returns the DestinationStatus named name in
// *destinations, appending an empty one if absent
func findOrAppendDestination(destinations *[]models.DestinationStatus, name string) *models.DestinationStatus {
	for i := range *destinations {
		if (*destinations)[i].Name == name {
			return &(*destinations)[i]
		}
	}
	*destinations = append(*destinations, models.DestinationStatus{Name: name})
	return &(*destinations)[len(*destinations)-1]
}

// IsDurablyPublished reports whether id's record is PublicationReplicated and
// bound to expectedCID. A missing record returns false and a nil error.
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

// Status returns id's PublicationRecord
func (rm *ReplicationManager) Status(ctx context.Context, id string) (*models.PublicationRecord, error) {
	return rm.records.Get(ctx, id)
}

// clientFor returns the Client configured under name, the local node or a backup
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

// Verify fetches id's content from every destination recorded on its
// PublicationRecord, checks that it hashes to the recorded CID, and updates each
// DestinationStatus (Verified, LastVerifiedAt, LastError). It then recomputes
// the record's state from the backups that verified, so a backup that can no
// longer serve the content stops counting toward the threshold.
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
			continue // no longer configured; its last status is kept
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

// RestoreLocal repopulates the local node after it loses its repository. It takes
// id's content from the first backup that returns bytes hashing to the recorded
// CID, re-adds it locally, and requires the CID the local node returns to equal
// the recorded one. It then pins and verifies the content as publication does.
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

// Retry re-adds and re-verifies id's content on every backup that is not
// currently Verified, then recomputes the record's state. data must hash to the
// recorded CID; the manager does not keep the bytes, so the caller supplies them
func (rm *ReplicationManager) Retry(ctx context.Context, id string, data []byte) error {
	rec, err := rm.records.Get(ctx, id)
	if err != nil {
		return err
	}
	if gotCID, err := ComputeCIDv1Raw(data); err != nil || gotCID != rec.CID {
		return fmt.Errorf("ipfs: retry data does not hash to publication %s's recorded CID %s", id, rec.CID)
	}
	now := rm.now()
	// a backup with Pinned set but Verified cleared, as Verify leaves one it could
	// not read, is retried; only a Verified backup is skipped
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
