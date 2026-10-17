package ipfs

import (
	"context"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
)

// failingClient returns an error from every call, simulating an unavailable backup
type failingClient struct{ err error }

func (f *failingClient) AddRaw(ctx context.Context, data []byte) (string, error) { return "", f.err }
func (f *failingClient) Pin(ctx context.Context, cid string) error               { return f.err }
func (f *failingClient) Get(ctx context.Context, cid string) ([]byte, error)     { return nil, f.err }

var _ Client = (*failingClient)(nil)

// wrongCIDClient stores bytes but reports another CID, testing mismatch handling.
// Embedding *FakeClient avoids copying its mutex.
type wrongCIDClient struct{ *FakeClient }

func (w *wrongCIDClient) AddRaw(ctx context.Context, data []byte) (string, error) {
	if _, err := w.FakeClient.AddRaw(ctx, data); err != nil {
		return "", err
	}
	return "bafkreiWRONGCIDbafkreiWRONGCIDbafkreiWRONGCID", nil
}

// pinOKRetrieveFailsClient accepts AddRaw/Pin but fails Get, simulating pinned content
// that cannot be served
type pinOKRetrieveFailsClient struct{ *FakeClient }

func (p *pinOKRetrieveFailsClient) Get(ctx context.Context, cid string) ([]byte, error) {
	return nil, errors.New("simulated: content not actually retrievable")
}

// TestReplicationManagerPublishRejectsWrongReturnedCID requires Publish to report a
// backup's mismatched AddRaw CID immediately and exclude it from the replication threshold
func TestReplicationManagerPublishRejectsWrongReturnedCID(t *testing.T) {
	local := NewFakeClient()
	backup := &wrongCIDClient{FakeClient: NewFakeClient()}
	records := memory.NewPublicationRepository()
	rm := NewReplicationManager(local, []Destination{{Name: "backup-node", Client: backup}}, 1, records)

	data := []byte(`{"asset":"gold-bar-wrong-cid"}`)
	if _, err := rm.Publish(context.Background(), "record-wrong-cid", data); err == nil {
		t.Fatal("expected Publish to surface the wrong-CID backup failure")
	}
	rec, err := rm.Status(context.Background(), "record-wrong-cid")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != models.PublicationReplicationFailed {
		t.Fatalf("State = %s, want %s: a wrong-CID backup must not count toward the threshold", rec.State, models.PublicationReplicationFailed)
	}
	var found bool
	for _, d := range rec.Destinations {
		if d.Name == "backup-node" {
			found = true
			if d.Pinned || !strings.Contains(d.LastError, "destination returned CID") {
				t.Fatalf("expected the wrong-CID destination to be recorded as failed with the mismatch, got %+v", d)
			}
		}
	}
	if !found {
		t.Fatalf("the backup destination is missing from the record: %+v", rec.Destinations)
	}
}

// TestReplicationManagerPublishDetectsPinOKRetrieveFailSynchronously checks that
// publication catches unreadable pinned backups immediately, before periodic Verify
func TestReplicationManagerPublishDetectsPinOKRetrieveFailSynchronously(t *testing.T) {
	local := NewFakeClient()
	backup := &pinOKRetrieveFailsClient{FakeClient: NewFakeClient()}
	records := memory.NewPublicationRepository()
	rm := NewReplicationManager(local, []Destination{{Name: "backup-node", Client: backup}}, 1, records)

	data := []byte(`{"asset":"gold-bar-pin-ok-retrieve-fail"}`)
	if _, err := rm.Publish(context.Background(), "record-pin-ok", data); err == nil {
		t.Fatal("expected Publish to surface the retrieve failure synchronously")
	}
	rec, err := rm.Status(context.Background(), "record-pin-ok")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != models.PublicationReplicationFailed {
		t.Fatalf("State = %s, want %s: a pinned but unretrievable backup must not count toward the threshold, without waiting for a later Verify call", rec.State, models.PublicationReplicationFailed)
	}
}

// TestReplicationManagerLocalPinOnly checks that with no backups and a positive
// threshold, a publish pins locally but the record never reaches
// PublicationReplicated
func TestReplicationManagerLocalPinOnly(t *testing.T) {
	local := NewFakeClient()
	records := memory.NewPublicationRepository()
	rm := NewReplicationManager(local, nil, 1, records)

	cid, err := rm.Publish(context.Background(), "record-1", []byte(`{"a":1}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.Get(context.Background(), cid); err != nil {
		t.Fatalf("expected content pinned locally: %v", err)
	}

	published, err := rm.IsDurablyPublished(context.Background(), "record-1", cid)
	if err != nil {
		t.Fatal(err)
	}
	if published {
		t.Fatal("expected NOT durably published with zero backups configured and threshold 1")
	}
	rec, err := rm.Status(context.Background(), "record-1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != models.PublicationReplicationPending {
		t.Fatalf("State = %s, want %s", rec.State, models.PublicationReplicationPending)
	}
}

// TestReplicationManagerReplicatesToSecondDestination checks that the same CID
// is replicated to a second destination and that the record becomes
// PublicationReplicated once the threshold is met
func TestReplicationManagerReplicatesToSecondDestination(t *testing.T) {
	local := NewFakeClient()
	backup := NewFakeClient()
	records := memory.NewPublicationRepository()
	rm := NewReplicationManager(local, []Destination{{Name: "backup-node", Client: backup}}, 1, records)

	data := []byte(`{"asset":"gold-bar-1"}`)
	cid, err := rm.Publish(context.Background(), "record-1", data)
	if err != nil {
		t.Fatal(err)
	}
	backupData, err := backup.Get(context.Background(), cid)
	if err != nil {
		t.Fatalf("expected content replicated to backup: %v", err)
	}
	if string(backupData) != string(data) {
		t.Fatalf("backup content = %q, want %q", backupData, data)
	}

	published, err := rm.IsDurablyPublished(context.Background(), "record-1", cid)
	if err != nil {
		t.Fatal(err)
	}
	if !published {
		t.Fatal("expected durably published once the single configured backup succeeds")
	}
}

// TestReplicationManagerFailureVisibleAndRetryable checks Publish's error and
// Failed/Pending LastError, retained local pin/verification, and successful Retry after
// backup recovery
func TestReplicationManagerFailureVisibleAndRetryable(t *testing.T) {
	local := NewFakeClient()
	flaky := &failingClient{err: errors.New("simulated backup outage")}
	records := memory.NewPublicationRepository()
	rm := NewReplicationManager(local, []Destination{{Name: "flaky-backup", Client: flaky}}, 1, records)

	data := []byte(`{"asset":"gold-bar-2"}`)
	cid, err := rm.Publish(context.Background(), "record-2", data)
	if err == nil {
		t.Fatal("expected the backup failure to be returned to the caller")
	}
	if cid == "" {
		t.Fatal("expected a CID even though the backup failed (local pin still succeeded)")
	}
	rec, err := rm.Status(context.Background(), "record-2")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != models.PublicationReplicationFailed {
		t.Fatalf("State = %s, want %s", rec.State, models.PublicationReplicationFailed)
	}
	var flakyStatus *models.DestinationStatus
	for i := range rec.Destinations {
		if rec.Destinations[i].Name == "flaky-backup" {
			flakyStatus = &rec.Destinations[i]
		}
	}
	if flakyStatus == nil || flakyStatus.Pinned || flakyStatus.LastError == "" {
		t.Fatalf("expected a visible, unpinned failure status, got %+v", flakyStatus)
	}

	// The backup recovers; retry with the same content
	working := NewFakeClient()
	rm2 := NewReplicationManager(local, []Destination{{Name: "flaky-backup", Client: working}}, 1, records)
	if err := rm2.Retry(context.Background(), "record-2", data); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	published, err := rm2.IsDurablyPublished(context.Background(), "record-2", cid)
	if err != nil {
		t.Fatal(err)
	}
	if !published {
		t.Fatal("expected durably published after a successful retry")
	}
}

// TestReplicationManagerPublishRejectsCIDRebind checks that an existing publication ID
// rejects changed content rather than overwriting its CID
func TestReplicationManagerPublishRejectsCIDRebind(t *testing.T) {
	local := NewFakeClient()
	backup := NewFakeClient()
	records := memory.NewPublicationRepository()
	rm := NewReplicationManager(local, []Destination{{Name: "b", Client: backup}}, 1, records)

	original := []byte(`{"asset":"gold-bar-A"}`)
	cidA, err := rm.Publish(context.Background(), "record-1", original)
	if err != nil {
		t.Fatal(err)
	}

	// a second publish for the same id with different bytes must be rejected
	if _, err := rm.Publish(context.Background(), "record-1", []byte(`{"asset":"gold-bar-B"}`)); !errors.Is(err, ErrPublicationCIDConflict) {
		t.Fatalf("expected ErrPublicationCIDConflict, got %v", err)
	}
	rec, err := rm.Status(context.Background(), "record-1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.CID != cidA {
		t.Fatalf("publication CID was rebound: %s -> %s", cidA, rec.CID)
	}
}

// TestIsDurablyPublishedRejectsWrongExpectedCID checks that replicated status applies
// only to the record's bound CID
func TestIsDurablyPublishedRejectsWrongExpectedCID(t *testing.T) {
	local := NewFakeClient()
	backup := NewFakeClient()
	records := memory.NewPublicationRepository()
	rm := NewReplicationManager(local, []Destination{{Name: "b", Client: backup}}, 1, records)

	cid, err := rm.Publish(context.Background(), "record-1", []byte(`{"asset":"gold-bar"}`))
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := rm.IsDurablyPublished(context.Background(), "record-1", cid); err != nil || !ok {
		t.Fatalf("expected durable for the recorded CID: ok=%v err=%v", ok, err)
	}
	if ok, err := rm.IsDurablyPublished(context.Background(), "record-1", "bafkreiSOMEOTHERCIDbafkreiSOMEOTHERCID"); err != nil || ok {
		t.Fatalf("expected NOT durable for a mismatched CID: ok=%v err=%v", ok, err)
	}
}

// TestReplicationManagerRetryDoesNotPromoteUnverified checks that Pinned-but-unverified
// backups are retried and excluded from the threshold until verification succeeds
func TestReplicationManagerRetryDoesNotPromoteUnverified(t *testing.T) {
	local := NewFakeClient()
	backup := &pinOKRetrieveFailsClient{FakeClient: NewFakeClient()}
	records := memory.NewPublicationRepository()
	rm := NewReplicationManager(local, []Destination{{Name: "b", Client: backup}}, 1, records)

	data := []byte(`{"asset":"gold-bar-retry"}`)
	cid, _ := rm.Publish(context.Background(), "record-1", data) // fails: backup pins but can't be retrieved
	rec, err := rm.Status(context.Background(), "record-1")
	if err != nil {
		t.Fatal(err)
	}
	// the backup is Pinned but not Verified
	for _, d := range rec.Destinations {
		if d.Name == "b" && (!d.Pinned || d.Verified) {
			t.Fatalf("test precondition not met: want Pinned && !Verified, got %+v", d)
		}
	}
	if err := rm.Retry(context.Background(), "record-1", data); err != nil {
		t.Fatalf("Retry: %v", err)
	}
	if ok, _ := rm.IsDurablyPublished(context.Background(), "record-1", cid); ok {
		t.Fatal("Retry promoted an unretrievable (Pinned-but-unverified) destination to Replicated")
	}
}

// TestRestoreLocalRejectsWrongReturnedCID checks that RestoreLocal fails when
// the local node returns a CID other than the recorded one
func TestRestoreLocalRejectsWrongReturnedCID(t *testing.T) {
	local := NewFakeClient()
	backup := NewFakeClient()
	records := memory.NewPublicationRepository()
	rm := NewReplicationManager(local, []Destination{{Name: "b", Client: backup}}, 1, records)

	data := []byte(`{"asset":"gold-bar-restore"}`)
	if _, err := rm.Publish(context.Background(), "record-1", data); err != nil {
		t.Fatal(err)
	}

	// a local node that returns a wrong CID from AddRaw during restore
	badLocal := &wrongCIDClient{FakeClient: NewFakeClient()}
	rm2 := NewReplicationManager(badLocal, []Destination{{Name: "b", Client: backup}}, 1, records)
	err := rm2.RestoreLocal(context.Background(), "record-1")
	if err == nil || !strings.Contains(err.Error(), "restored local CID") {
		t.Fatalf("RestoreLocal err = %v, want a restored-local-CID mismatch", err)
	}
}

// TestReplicationManagerRestoresAfterLocalRepoLoss checks that content is
// restored from the archive backup after the local repository is lost
func TestReplicationManagerRestoresAfterLocalRepoLoss(t *testing.T) {
	local := NewFakeClient()
	archive, err := NewFileArchiveClient(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	records := memory.NewPublicationRepository()
	rm := NewReplicationManager(local, []Destination{{Name: "car-archive", Client: archive}}, 1, records)

	data := []byte(`{"asset":"gold-bar-3"}`)
	cid, err := rm.Publish(context.Background(), "record-3", data)
	if err != nil {
		t.Fatal(err)
	}

	// the local node loses its repository
	local.Reset()
	if _, err := local.Get(context.Background(), cid); err == nil {
		t.Fatal("expected local content to be gone after Reset")
	}

	if err := rm.RestoreLocal(context.Background(), "record-3"); err != nil {
		t.Fatalf("RestoreLocal: %v", err)
	}
	restored, err := local.Get(context.Background(), cid)
	if err != nil {
		t.Fatalf("expected content restored locally: %v", err)
	}
	if string(restored) != string(data) {
		t.Fatalf("restored content = %q, want %q", restored, data)
	}
}

// TestReplicationManagerVerifyDetectsUnretrievableContent checks that Verify
// catches a destination that accepted the content but can no longer serve it
func TestReplicationManagerVerifyDetectsUnretrievableContent(t *testing.T) {
	local := NewFakeClient()
	backup := NewFakeClient()
	records := memory.NewPublicationRepository()
	rm := NewReplicationManager(local, []Destination{{Name: "backup-node", Client: backup}}, 1, records)

	data := []byte(`{"asset":"gold-bar-4"}`)
	cid, err := rm.Publish(context.Background(), "record-4", data)
	if err != nil {
		t.Fatal(err)
	}
	if err := rm.Verify(context.Background(), "record-4"); err != nil {
		t.Fatal(err)
	}
	rec, err := rm.Status(context.Background(), "record-4")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State != models.PublicationReplicated {
		t.Fatalf("State = %s, want %s after a successful verify", rec.State, models.PublicationReplicated)
	}

	// the backup loses the block after the publish succeeded
	backup.Reset()

	if err := rm.Verify(context.Background(), "record-4"); err != nil {
		t.Fatal(err)
	}
	rec, err = rm.Status(context.Background(), "record-4")
	if err != nil {
		t.Fatal(err)
	}
	if rec.State == models.PublicationReplicated {
		t.Fatal("expected verification to demote the record once the backup can no longer serve the CID")
	}
	for _, d := range rec.Destinations {
		if d.Name == "backup-node" && (d.Verified || d.LastError == "") {
			t.Fatalf("backup destination = %+v, want unverified with the retrieval error recorded", d)
		}
	}
	if rec.CID != cid {
		t.Fatalf("CID changed unexpectedly: %s -> %s", cid, rec.CID)
	}
}

// TestFileArchiveClientRoundTripsContentUnderItsCID rehashes retrieved bytes against their CID.
// Archive reads do not verify integrity; TestReplicationManagerVerifyDemotesACorruptedArchiveBlock
// checks corruption detection by ReplicationManager.Verify.
func TestFileArchiveClientRoundTripsContentUnderItsCID(t *testing.T) {
	archive, err := NewFileArchiveClient(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	data := []byte(`{"asset":"gold-bar-5"}`)
	cid, err := archive.AddRaw(context.Background(), data)
	if err != nil {
		t.Fatal(err)
	}
	if err := archive.Pin(context.Background(), cid); err != nil {
		t.Fatal(err)
	}
	got, err := archive.Get(context.Background(), cid)
	if err != nil {
		t.Fatal(err)
	}
	gotCID, err := ComputeCIDv1Raw(got)
	if err != nil {
		t.Fatal(err)
	}
	if gotCID != cid {
		t.Fatalf("retrieved content hashes to %s, want %s", gotCID, cid)
	}
}

// countingAddClient counts AddRaw calls on the node it wraps
type countingAddClient struct {
	*FakeClient
	adds int
}

func (c *countingAddClient) AddRaw(ctx context.Context, data []byte) (string, error) {
	c.adds++
	return c.FakeClient.AddRaw(ctx, data)
}

// A rejected rebind must not store the new bytes on the local node
func TestReplicationManagerPublishRejectedRebindStoresNothingLocally(t *testing.T) {
	local := &countingAddClient{FakeClient: NewFakeClient()}
	rm := NewReplicationManager(local, nil, 0, memory.NewPublicationRepository())

	if _, err := rm.Publish(context.Background(), "record-1", []byte(`{"asset":"gold-bar-A"}`)); err != nil {
		t.Fatal(err)
	}
	if local.adds != 1 {
		t.Fatalf("AddRaw calls after the first publish = %d, want 1", local.adds)
	}

	rebound := []byte(`{"asset":"gold-bar-B"}`)
	if _, err := rm.Publish(context.Background(), "record-1", rebound); !errors.Is(err, ErrPublicationCIDConflict) {
		t.Fatalf("err = %v, want ErrPublicationCIDConflict", err)
	}
	if local.adds != 1 {
		t.Errorf("AddRaw calls after the rejected rebind = %d, want still 1 (no orphan block)", local.adds)
	}
	orphan, err := ComputeCIDv1Raw(rebound)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := local.Get(context.Background(), orphan); err == nil {
		t.Error("the rejected content is retrievable from the local node")
	}
}

// mislabelingClient is a node that can be switched to answer AddRaw with the CID of
// other bytes, as a node with a different chunker or hash setting would
type mislabelingClient struct {
	*FakeClient
	lie bool
}

func (c *mislabelingClient) AddRaw(ctx context.Context, data []byte) (string, error) {
	if c.lie {
		return c.FakeClient.AddRaw(ctx, append([]byte("different:"), data...))
	}
	return c.FakeClient.AddRaw(ctx, data)
}

// replicate must check the node's returned CID, even after Publish checks the locally computed one
func TestReplicationManagerReplicateRejectsNodeReturningADifferentCID(t *testing.T) {
	ctx := context.Background()
	local := &mislabelingClient{FakeClient: NewFakeClient()}
	records := memory.NewPublicationRepository()
	rm := NewReplicationManager(local, nil, 0, records)

	content := []byte(`{"asset":"gold-bar-A"}`)
	cidA, err := rm.Publish(ctx, "record-1", content)
	if err != nil {
		t.Fatal(err)
	}

	local.lie = true
	if _, err := rm.Publish(ctx, "record-1", content); !errors.Is(err, ErrPublicationCIDConflict) {
		t.Fatalf("Publish with a node returning another CID: err = %v, want ErrPublicationCIDConflict", err)
	}
	rec, err := rm.Status(ctx, "record-1")
	if err != nil {
		t.Fatal(err)
	}
	if rec.CID != cidA {
		t.Errorf("publication CID was rebound: %s -> %s", cidA, rec.CID)
	}
}

// A block that was altered on disk after publication must not keep the record Replicated
func TestReplicationManagerVerifyDemotesACorruptedArchiveBlock(t *testing.T) {
	dir := t.TempDir()
	archive, err := NewFileArchiveClient(dir)
	if err != nil {
		t.Fatal(err)
	}
	records := memory.NewPublicationRepository()
	rm := NewReplicationManager(NewFakeClient(), []Destination{{Name: "car-archive", Client: archive}}, 1, records)

	cid, err := rm.Publish(context.Background(), "record-1", []byte(`{"asset":"gold-bar-6"}`))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, url.PathEscape(cid)), []byte(`{"asset":"tampered"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := rm.Verify(context.Background(), "record-1"); err != nil {
		t.Fatal(err)
	}
	if ok, _ := rm.IsDurablyPublished(context.Background(), "record-1", cid); ok {
		t.Fatal("a record whose archive block no longer hashes to its CID is still reported durably published")
	}
}
