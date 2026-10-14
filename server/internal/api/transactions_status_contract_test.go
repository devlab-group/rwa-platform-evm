package api

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/rwa-platform/server/internal/dal/models"
)

// TestAllTxStatusesUniqueAndComplete guards models.AllTxStatuses, the single
// list of statuses, against duplicates and empties. The two tests below pin it to
// the TxStatus constants and to the OpenAPI enum, so a status missing from the API
// contract fails CI instead of rendering as an undefined badge in the SPA.
func TestAllTxStatusesUniqueAndComplete(t *testing.T) {
	seen := map[models.TxStatus]bool{}
	for _, s := range models.AllTxStatuses {
		if s == "" {
			t.Fatalf("models.AllTxStatuses contains an empty status")
		}
		if seen[s] {
			t.Fatalf("models.AllTxStatuses contains duplicate %q", s)
		}
		seen[s] = true
	}
	// Every status the handler emits is string(tx.Status). This explicit list of the
	// TxStatus constants, instead of reflection, makes a status missing from
	// AllTxStatuses fail this test
	wantConsts := []models.TxStatus{
		models.TxPending, models.TxMined, models.TxConfirmed, models.TxReplaced,
		models.TxReverted, models.TxReorged, models.TxFailed, models.TxBroadcastUnknown,
		models.TxNonceConsumedExternally, models.TxNeedsIntervention,
	}
	for _, s := range wantConsts {
		if !seen[s] {
			t.Errorf("TxStatus const %q is not present in models.AllTxStatuses", s)
		}
	}
	if len(seen) != len(wantConsts) {
		t.Errorf("models.AllTxStatuses has %d statuses, const mirror has %d", len(seen), len(wantConsts))
	}
}

// TestTransactionStatusEnumMatchesDomain checks that the statuses the handler
// can emit (models.AllTxStatuses, since the handler returns string(tx.Status))
// equal the api/openapi.yaml Transaction.status enum exactly. A status the server
// can persist but the schema omits is out of contract, and the generated client
// and UI cannot render it.
func TestTransactionStatusEnumMatchesDomain(t *testing.T) {
	enum := transactionStatusEnumFromOpenAPI(t)

	domainSet := map[string]bool{}
	for _, s := range models.AllTxStatuses {
		domainSet[string(s)] = true
	}
	enumSet := map[string]bool{}
	for _, s := range enum {
		enumSet[s] = true
	}

	var missingFromEnum, extraInEnum []string
	for s := range domainSet {
		if !enumSet[s] {
			missingFromEnum = append(missingFromEnum, s)
		}
	}
	for s := range enumSet {
		if !domainSet[s] {
			extraInEnum = append(extraInEnum, s)
		}
	}
	sort.Strings(missingFromEnum)
	sort.Strings(extraInEnum)
	if len(missingFromEnum) > 0 {
		t.Errorf("TxStatus values missing from openapi Transaction.status enum: %v", missingFromEnum)
	}
	if len(extraInEnum) > 0 {
		t.Errorf("openapi Transaction.status enum has values not in models.AllTxStatuses: %v", extraInEnum)
	}
}

var txStatusEnumRE = regexp.MustCompile(`status:\s*\{\s*type:\s*string,\s*enum:\s*\[([^\]]*)\]`)

func transactionStatusEnumFromOpenAPI(t *testing.T) []string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot resolve test file path")
	}
	// internal/api -> internal -> server -> repo root, where api/openapi.yaml lives
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", ".."))
	path := filepath.Join(repoRoot, "api", "openapi.yaml")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	// Scope to the Transaction schema block, because other `status:` enums in the
	// file (such as compliance Allowed, Blocked and Unknown) are not transaction
	// statuses
	body := string(data)
	idx := strings.Index(body, "\n    Transaction:")
	if idx < 0 {
		t.Fatalf("could not find Transaction schema in %s", path)
	}
	m := txStatusEnumRE.FindStringSubmatch(body[idx:])
	if m == nil {
		t.Fatalf("could not find Transaction.status enum in %s", path)
	}
	parts := strings.Split(m[1], ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if v := strings.TrimSpace(p); v != "" {
			out = append(out, v)
		}
	}
	return out
}
