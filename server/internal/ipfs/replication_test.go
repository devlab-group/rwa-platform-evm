package ipfs

import (
	"context"
	"errors"
	"testing"

	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
)

// failingClient always errors, for tests simulating a backup destination
// that is unavailable.
type failingClient struct{ err error }

func (f *failingClient) AddRaw(ctx context.Context, data []byte) (string, error) { return "", f.err }
func (f *failingClient) Pin(ctx context.Context, cid string) error               { return f.err }
func (f *failingClient) Get(ctx context.Context, cid string) ([]byte, error)     { return nil, f.err }

var _ Client = (*failingClient)(nil)

// wrongCIDClient always succeeds AddRaw/Pin, but AddRaw returns a
// DIFFERENT CID than whatever the caller expects — simulating a
// destination that silently stores/computes something else. Embeds
// *FakeClient (a pointer) rather than a value so nothing ever copies its
// internal mutex.
type wrongCIDClient struct{ *FakeClient }

func (w *wrongCIDClient) AddRaw(ctx context.Context, data []byte) (string, error) {
	if _, err := w.FakeClient.AddRaw(ctx, data); err != nil {
		return "", err
	}
	return "bafkreiWRONGCIDbafkreiWRONGCIDbafkreiWRONGCID", nil
}

// pinOKRetrieveFailsClient accepts AddRaw/Pin normally but Get always
// fails — simulating a destination that reports success without the
// content actually being retrievable.
type pinOKRetrieveFailsClient struct{ *FakeClient }

func (p *pinOKRetrieveFailsClient) Get(ctx context.Context, cid string) ([]byte, error) {
	return nil, errors.New("simulated: content not actually retrievable")
}

// TestReplicationManagerPublishRejectsWrongReturnedCID checks the
// synchronous requirement that a destination's returned CID equal the
// locally computed CID. A backup whose AddRaw returns a
// mismatched CID must never be counted toward the replication threshold,
// checked at PUBLISH time, not only discovered later by the periodic
// Verify ticker.
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
	if rec.State == models.PublicationReplicated {
		t.Fatal("expected a wrong-CID backup to NOT count toward the replication threshold")
	}
	for _, d := range rec.Destinations {
		if d.Name == "backup-node" && (d.Pinned || d.LastError == "") {
			t.Fatalf("expected the wrong-CID destination to be recorded as failed, got %+v", d)
		}
	}
}

// TestReplicationManagerPublishDetectsPinOKRetrieveFailSynchronously is the
// other half of the synchronous-verification requirement: a destination
// that accepts AddRaw/Pin but cannot actually be retrieved from must be
// caught immediately at publish time (not just eventually, by the 30-
// minute Verify ticker) — "successful AddRaw plus Pin calls count as
// Replicated before any retrieval/digest verification" was the exact
// finding.
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
	if rec.State == models.PublicationReplicated {
		t.Fatal("expected a pin-ok-but-unretrievable backup to NOT count toward the replication threshold, without waiting for a later Verify call")
	}
}

// TestReplicationManagerLocalPinOnly: with no backups configured and a
// positive threshold, a publish pins locally but the record must NEVER
// reach PublicationReplicated: an asset package must not be marked durably
// published until the configured replication threshold has been met.
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

// TestReplicationManagerReplicatesToSecondDestination is the core
// acceptance criterion: "the same CID is replicated to a second
// destination," and once the threshold is met the record becomes
// PublicationReplicated.
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

// TestReplicationManagerFailureVisibleAndRetryable: a backup failure must
// be visible (ReplicationFailed/Pending, LastError populated, AND returned
// to the publishing caller — a backup failure must not be swallowed) and a
// later Retry with the same content must recover it. The
// content is still considered created (local pinned+verified) despite the
// backup failure — Publish's error is informational/non-fatal for that
// reason, not a rejection of the whole call.
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

	// The backup recovers; retry with the same content.
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

// TestReplicationManagerPublishRejectsCIDRebind checks that an existing
// publication id is immutably bound to its recorded CID; publishing different
// bytes under the same id must be a conflict, never an overwrite.
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

	// A second publish for the SAME id with DIFFERENT bytes (a duplicate
	// asset create with changed metadata) must be rejected.
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

// TestIsDurablyPublishedRejectsWrongExpectedCID covers the mint gate: even a
// fully replicated record must not report durable for a CID other than the
// one it is actually bound to.
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

// TestReplicationManagerRetryDoesNotPromoteUnverified checks that Retry
// re-runs the full add+verify path, counting only VERIFIED backups. A
// destination that is Pinned=true but Verified=false (its content is not
// actually retrievable) must not be counted toward the replication threshold
// just because a prior Pin succeeded.
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
	// Precondition: the backup is Pinned but not Verified — exactly the state
	// the old Retry counted as success.
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

// TestRestoreLocalRejectsWrongReturnedCID checks that on restore the
// local node's own returned CID must equal the recorded CID before the local
// destination is marked verified.
func TestRestoreLocalRejectsWrongReturnedCID(t *testing.T) {
	local := NewFakeClient()
	backup := NewFakeClient()
	records := memory.NewPublicationRepository()
	rm := NewReplicationManager(local, []Destination{{Name: "b", Client: backup}}, 1, records)

	data := []byte(`{"asset":"gold-bar-restore"}`)
	if _, err := rm.Publish(context.Background(), "record-1", data); err != nil {
		t.Fatal(err)
	}

	// A local node that returns a wrong CID from AddRaw during restore.
	badLocal := &wrongCIDClient{FakeClient: NewFakeClient()}
	rm2 := NewReplicationManager(badLocal, []Destination{{Name: "b", Client: backup}}, 1, records)
	if err := rm2.RestoreLocal(context.Background(), "record-1"); err == nil {
		t.Fatal("expected RestoreLocal to reject a local node returning a CID != recorded CID")
	}
}

// TestReplicationManagerRestoresAfterLocalRepoLoss covers the required
// integration scenario: simulated loss of the local IPFS repository,
// restored from the retained archive backup.
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

	// Simulate the local node losing its repository entirely.
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

// TestReplicationManagerVerifyDetectsUnretrievableContent: a destination
// that accepted AddRaw/Pin (an API-level success) but can no longer
// actually serve the content must be caught by Verify, not just trusted
// because the original publish succeeded: a successful HTTP or API response
// from a pinning provider is insufficient by itself.
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

	// The backup silently loses the block (e.g. its own storage failed
	// after reporting success) without the publish path ever knowing.
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
	if rec.CID != cid {
		t.Fatalf("CID changed unexpectedly: %s -> %s", cid, rec.CID)
	}
}

// TestFileArchiveClientDownloadVerifiesCID exercises the archive
// destination directly: content retrieved from it must hash to the CID it
// was stored under.
func TestFileArchiveClientDownloadVerifiesCID(t *testing.T) {
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
