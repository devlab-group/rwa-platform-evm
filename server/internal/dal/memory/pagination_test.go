package memory

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/rwa-platform/server/internal/dal/models"
)

// TestPurchaseRepositoryListPageWalksAllRecordsOver200 checks repository sorting and
// cursor encoding across pages. keyset_test.go covers the helper separately.
func TestPurchaseRepositoryListPageWalksAllRecordsOver200(t *testing.T) {
	repo := NewPurchaseRepository()
	ctx := context.Background()
	const total = 220
	for i := 0; i < total; i++ {
		if err := repo.Create(ctx, &models.Purchase{
			ID: fmt.Sprintf("tx-%04d:0", i), BlockNumber: uint64(i), Buyer: "0xBUYER",
		}); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]bool{}
	cursor := ""
	var lastBlock uint64 = total // sanity bound; first page's block must be < this
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("walk did not terminate")
		}
		page, next, err := repo.ListPage(ctx, cursor, 17) // an odd page size on purpose
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page {
			if seen[p.ID] {
				t.Fatalf("purchase %s visited twice", p.ID)
			}
			seen[p.ID] = true
			if p.BlockNumber >= lastBlock {
				t.Fatalf("expected strictly descending BlockNumber, got %d after %d", p.BlockNumber, lastBlock)
			}
			lastBlock = p.BlockNumber
		}
		if next == "" {
			break
		}
		cursor = next
	}

	if len(seen) != total {
		t.Fatalf("visited %d of %d purchases", len(seen), total)
	}
}

// TestPurchaseRepositoryListPageStableUnderConcurrentInsert checks that newer purchases
// inserted ahead of the cursor do not shift the next page, unlike offsets
func TestPurchaseRepositoryListPageStableUnderConcurrentInsert(t *testing.T) {
	repo := NewPurchaseRepository()
	ctx := context.Background()
	for i := 0; i < 10; i++ {
		if err := repo.Create(ctx, &models.Purchase{ID: fmt.Sprintf("tx-%04d:0", i), BlockNumber: uint64(i)}); err != nil {
			t.Fatal(err)
		}
	}

	firstPage, cursor, err := repo.ListPage(ctx, "", 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstPage) != 4 || firstPage[0].BlockNumber != 9 {
		t.Fatalf("first page = %+v", firstPage)
	}

	// new purchases get higher block numbers, so they land ahead of the walk
	for i := 10; i < 15; i++ {
		if err := repo.Create(ctx, &models.Purchase{ID: fmt.Sprintf("tx-%04d:0", i), BlockNumber: uint64(i)}); err != nil {
			t.Fatal(err)
		}
	}

	secondPage, _, err := repo.ListPage(ctx, cursor, 4)
	if err != nil {
		t.Fatal(err)
	}
	// After blocks 9, 8, 7 and 6, resume at 5 despite newer inserts
	if len(secondPage) != 4 || secondPage[0].BlockNumber != 5 {
		t.Fatalf("second page = %+v, want to resume at BlockNumber 5", secondPage)
	}
}

// TestAssetRecordRepositoryListPageWalksAllRecordsOver200 is the same check for
// the ascending direction, since asset records are listed oldest first
func TestAssetRecordRepositoryListPageWalksAllRecordsOver200(t *testing.T) {
	repo := NewAssetRecordRepository()
	ctx := context.Background()
	const total = 213
	base := time.Now().UTC()
	for i := 0; i < total; i++ {
		if err := repo.Create(ctx, &models.AssetRecord{
			RecordID: fmt.Sprintf("REC-%04d", i), CreatedAt: base.Add(time.Duration(i) * time.Millisecond),
		}); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]bool{}
	cursor := ""
	lastCreatedAt := time.Time{} // sanity bound; ascending, so each page must be >= the prior
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("walk did not terminate")
		}
		page, next, err := repo.ListPage(ctx, cursor, 19)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range page {
			if seen[r.RecordID] {
				t.Fatalf("record %s visited twice", r.RecordID)
			}
			seen[r.RecordID] = true
			if r.CreatedAt.Before(lastCreatedAt) {
				t.Fatalf("expected ascending CreatedAt, got %s after %s", r.CreatedAt, lastCreatedAt)
			}
			lastCreatedAt = r.CreatedAt
		}
		if next == "" {
			break
		}
		cursor = next
	}

	if len(seen) != total {
		t.Fatalf("visited %d of %d asset records", len(seen), total)
	}
}

// TestTransactionRepositoryListPageWalksAllRecordsOver200 is the same check for
// transactions, in ascending SubmittedAt order, including the address filter
func TestTransactionRepositoryListPageWalksAllRecordsOver200(t *testing.T) {
	repo := NewTransactionRepository()
	ctx := context.Background()
	const total = 209
	base := time.Now().UTC()
	for i := 0; i < total; i++ {
		if err := repo.Create(ctx, &models.Transaction{
			ID: fmt.Sprintf("tx-%04d", i), From: "0xAAAA", To: "0xBBBB",
			SubmittedAt: base.Add(time.Duration(i) * time.Millisecond),
		}); err != nil {
			t.Fatal(err)
		}
	}

	seen := map[string]bool{}
	cursor := ""
	for pages := 0; ; pages++ {
		if pages > total {
			t.Fatal("walk did not terminate")
		}
		page, next, err := repo.ListPage(ctx, "", cursor, 23)
		if err != nil {
			t.Fatal(err)
		}
		for _, tx := range page {
			if seen[tx.ID] {
				t.Fatalf("transaction %s visited twice", tx.ID)
			}
			seen[tx.ID] = true
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if len(seen) != total {
		t.Fatalf("visited %d of %d transactions", len(seen), total)
	}

	// the case-insensitive address filter must combine with the keyset walk
	page, next, err := repo.ListPage(ctx, "0xaaaa", "", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 5 {
		t.Fatalf("filtered page len = %d, want 5", len(page))
	}
	if next == "" {
		t.Fatal("expected a next cursor for the filtered walk too")
	}
}

// TestRedemptionRequestRepositoryListPageFiltersByBeneficiary checks case-insensitive
// beneficiary filtering combined with status and keyset pagination, as for transactions
func TestRedemptionRequestRepositoryListPageFiltersByBeneficiary(t *testing.T) {
	repo := NewRedemptionRequestRepository()
	ctx := context.Background()

	alice := "0x00000000000000000000000000000000000000A1"
	bob := "0x00000000000000000000000000000000000000B2"
	for i := 0; i < 12; i++ { // alice: 12 Pending
		if err := repo.Upsert(ctx, &models.RedemptionRequest{
			ID: fmt.Sprintf("a-%02d", i), Beneficiary: alice,
			Status: models.RedemptionPending, CreatedAt: int64(i),
		}); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 5; i++ { // bob: 5 Funded
		if err := repo.Upsert(ctx, &models.RedemptionRequest{
			ID: fmt.Sprintf("b-%02d", i), Beneficiary: bob,
			Status: models.RedemptionFunded, CreatedAt: int64(100 + i),
		}); err != nil {
			t.Fatal(err)
		}
	}

	// Use small pages to check beneficiary filtering and absence of duplicate IDs across the walk
	count := func(status, address string) int {
		seen := map[string]bool{}
		cursor := ""
		for pages := 0; pages <= 20; pages++ {
			page, next, err := repo.ListPage(ctx, status, address, cursor, 4)
			if err != nil {
				t.Fatal(err)
			}
			for _, r := range page {
				if seen[r.ID] {
					t.Fatalf("request %s visited twice", r.ID)
				}
				seen[r.ID] = true
				if address != "" && !strings.EqualFold(r.Beneficiary, address) {
					t.Fatalf("beneficiary %s leaked into filter for %s", r.Beneficiary, address)
				}
			}
			if next == "" {
				return len(seen)
			}
			cursor = next
		}
		t.Fatal("pagination walk did not terminate")
		return 0
	}

	if n := count("", strings.ToLower(alice)); n != 12 { // case-insensitive match
		t.Errorf("alice (lowercased) address filter = %d, want 12", n)
	}
	if n := count("", bob); n != 5 {
		t.Errorf("bob address filter = %d, want 5", n)
	}
	// status and address combine
	if n := count(string(models.RedemptionFunded), alice); n != 0 {
		t.Errorf("alice+Funded = %d, want 0 (alice is Pending)", n)
	}
	if n := count(string(models.RedemptionFunded), bob); n != 5 {
		t.Errorf("bob+Funded = %d, want 5", n)
	}
	if n := count("", ""); n != 17 { // no filter = everything
		t.Errorf("unfiltered = %d, want 17", n)
	}
}

// TestPurchaseRepositoryListPageWalksTiedBlockNumbers is the memory twin of the live
// tie test: purchases sharing a block come out newest id first, each exactly once
func TestPurchaseRepositoryListPageWalksTiedBlockNumbers(t *testing.T) {
	repo := NewPurchaseRepository()
	ctx := context.Background()
	for _, id := range []string{"c:0", "a:0", "e:0", "b:0", "d:0"} {
		if err := repo.Create(ctx, &models.Purchase{ID: id, TxHash: id, BlockNumber: 7}); err != nil {
			t.Fatal(err)
		}
	}
	var ids []string
	cursor := ""
	for range 5 {
		page, next, err := repo.ListPage(ctx, cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page {
			ids = append(ids, p.ID)
		}
		if next == "" {
			break
		}
		cursor = next
	}
	if got, want := strings.Join(ids, ","), "e:0,d:0,c:0,b:0,a:0"; got != want {
		t.Fatalf("paged order = %s, want %s", got, want)
	}
}
