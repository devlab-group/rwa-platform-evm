package auditlog

import (
	"context"
	"testing"

	"github.com/rwa-platform/server/internal/dal/memory"
)

func TestRecordAndRecent(t *testing.T) {
	repo := memory.NewAuditLogRepository()
	logger := New(repo)
	ctx := context.Background()

	if err := logger.Record(ctx, "compliance", "admin", "compliance.setStatus", "0xabc", map[string]any{"status": "Allowed"}); err != nil {
		t.Fatal(err)
	}
	if err := logger.Record(ctx, "assets", "relayer", "mint.relay", "record-1", nil); err != nil {
		t.Fatal(err)
	}

	entries, err := logger.Recent(ctx, "", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	// Recent() returns newest first.
	if entries[0].Action != "mint.relay" {
		t.Errorf("entries[0].Action = %q, want mint.relay", entries[0].Action)
	}
	if entries[1].Actor != "admin" {
		t.Errorf("entries[1].Actor = %q, want admin", entries[1].Actor)
	}
}

func TestRecentRespectsLimit(t *testing.T) {
	repo := memory.NewAuditLogRepository()
	logger := New(repo)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		_ = logger.Record(ctx, "compliance", "a", "action", "t", nil)
	}
	entries, err := logger.Recent(ctx, "", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
}

func TestRecentFiltersByCategory(t *testing.T) {
	repo := memory.NewAuditLogRepository()
	logger := New(repo)
	ctx := context.Background()
	_ = logger.Record(ctx, "compliance", "a", "compliance.setStatus", "t1", nil)
	_ = logger.Record(ctx, "assets", "b", "assets.createRecord", "t2", nil)
	_ = logger.Record(ctx, "compliance", "c", "compliance.webhook", "t3", nil)

	entries, err := logger.Recent(ctx, "compliance", 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(entries))
	}
	for _, e := range entries {
		if e.Category != "compliance" {
			t.Errorf("got category %q, want compliance", e.Category)
		}
	}
}
