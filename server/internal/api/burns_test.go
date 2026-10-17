package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/rwa-platform/server/internal/api/dto"
	"github.com/rwa-platform/server/internal/auditpkg"
	"github.com/rwa-platform/server/internal/dal/models"
)

// mintedRecord creates a record through the API, then marks it Minted
func mintedRecord(t *testing.T, env *testEnv, id, amount string) {
	t.Helper()
	body := CreateRecordRequestBody{RecordID: id, Asset: json.RawMessage(`{"serialNumber":"1","weightGrams":"250","purity":"999.9"}`), Amount: amount}
	if w := doJSON(t, env.router, http.MethodPost, "/api/v1/assets/records", body, map[string]string{"Authorization": env.bearer, "Idempotency-Key": "rec-" + id}); w.Code != http.StatusCreated {
		t.Fatalf("create record: %d %s", w.Code, w.Body.String())
	}
	rec, err := env.app.Repos.AssetRecords.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	rec.Status = models.RecordStatusMinted
	if err := env.app.Repos.AssetRecords.Update(context.Background(), rec); err != nil {
		t.Fatal(err)
	}
}

func burnReq(t *testing.T, env *testEnv, method, path string, body any, key string) (int, []byte) {
	h := map[string]string{"Authorization": env.bearer}
	if key != "" {
		h["Idempotency-Key"] = key
	}
	w := doJSON(t, env.router, method, path, body, h)
	return w.Code, w.Body.Bytes()
}

func TestBurnEndpointsFlow(t *testing.T) {
	env := setupTestApp(t)
	mintedRecord(t, env, "BURN-API-1", "1000")

	code, raw := burnReq(t, env, http.MethodPost, "/api/v1/assets/burns", CreateBurnRequestBody{RecordID: "BURN-API-1", Amount: "400"}, "burn-1")
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, raw)
	}
	var created dto.BurnResponse
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if created.Status != "Pending" || created.Amount != "400" || created.Remaining != "600" || created.OperationID == "" {
		t.Fatalf("unexpected burn %+v", created)
	}

	// Replay must not reserve the amount twice
	code, raw = burnReq(t, env, http.MethodPost, "/api/v1/assets/burns", CreateBurnRequestBody{RecordID: "BURN-API-1", Amount: "400"}, "burn-1")
	var replay dto.BurnResponse
	if err := json.Unmarshal(raw, &replay); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if code != http.StatusCreated || replay.BurnID != created.BurnID {
		t.Fatalf("replay = %d %s, want the same burn %s", code, raw, created.BurnID)
	}
	if got, _ := env.app.Repos.Burns.ListByRecord(context.Background(), "BURN-API-1"); len(got) != 1 {
		t.Fatalf("%d burns after replay, want 1", len(got))
	}

	// Reject amounts above the remaining amount
	if code, raw = burnReq(t, env, http.MethodPost, "/api/v1/assets/burns", CreateBurnRequestBody{RecordID: "BURN-API-1", Amount: "601"}, "burn-2"); code != http.StatusConflict {
		t.Errorf("over remaining = %d %s, want 409", code, raw)
	}
	if code, raw = burnReq(t, env, http.MethodPost, "/api/v1/assets/burns", CreateBurnRequestBody{RecordID: "BURN-API-1", Amount: "0"}, "burn-3"); code != http.StatusBadRequest {
		t.Errorf("zero amount = %d %s, want 400", code, raw)
	}
	if code, raw = burnReq(t, env, http.MethodPost, "/api/v1/assets/burns", CreateBurnRequestBody{RecordID: "NO-SUCH", Amount: "1"}, "burn-4"); code != http.StatusNotFound {
		t.Errorf("unknown record = %d %s, want 404", code, raw)
	}

	code, raw = burnReq(t, env, http.MethodGet, "/api/v1/assets/burns", nil, "")
	var listed []dto.BurnResponse
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if code != http.StatusOK || len(listed) != 1 || listed[0].BurnID != created.BurnID {
		t.Fatalf("list = %d %s", code, raw)
	}

	code, raw = burnReq(t, env, http.MethodGet, "/api/v1/assets/burns/"+created.BurnID+"/package", nil, "")
	if code != http.StatusOK {
		t.Fatalf("package = %d %s", code, raw)
	}
	opened, err := auditpkg.OpenPackage(raw)
	if err != nil || opened.Manifest.PrimaryType != "BurnAttestation" {
		t.Fatalf("package: %v %+v", err, opened)
	}

	code, raw = burnReq(t, env, http.MethodPost, "/api/v1/assets/burns/"+created.BurnID+"/reissue", nil, "reissue-1")
	var re dto.BurnResponse
	if err := json.Unmarshal(raw, &re); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if code != http.StatusOK || re.Nonce == created.Nonce || re.BurnID != created.BurnID {
		t.Fatalf("reissue = %d %s", code, raw)
	}

	if code, raw = burnReq(t, env, http.MethodGet, "/api/v1/assets/burns/nope/package", nil, ""); code != http.StatusNotFound {
		t.Errorf("unknown package = %d %s, want 404", code, raw)
	}
	if code, raw = burnReq(t, env, http.MethodPost, "/api/v1/assets/burns/nope/reissue", nil, "reissue-2"); code != http.StatusNotFound {
		t.Errorf("unknown reissue = %d %s, want 404", code, raw)
	}

	b, _ := env.app.Repos.Burns.Get(context.Background(), created.BurnID)
	b.Status = models.BurnStatusBurned
	if ok, _ := env.app.Repos.Burns.UpdateConditional(context.Background(), b, b.Version); !ok {
		t.Fatal("update failed")
	}
	if code, raw = burnReq(t, env, http.MethodPost, "/api/v1/assets/burns/"+created.BurnID+"/reissue", nil, "reissue-3"); code != http.StatusConflict {
		t.Errorf("reissue of Burned = %d %s, want 409", code, raw)
	}
}

func TestBurnEndpointsRequireAdmin(t *testing.T) {
	env := setupTestApp(t)
	mintedRecord(t, env, "BURN-API-2", "10")
	for _, rt := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/assets/burns"},
		{http.MethodGet, "/api/v1/assets/burns"},
		{http.MethodGet, "/api/v1/assets/burns/x/package"},
		{http.MethodPost, "/api/v1/assets/burns/x/reissue"},
	} {
		w := doJSON(t, env.router, rt.method, rt.path, CreateBurnRequestBody{RecordID: "BURN-API-2", Amount: "1"}, map[string]string{"Idempotency-Key": "k"})
		if w.Code != http.StatusForbidden {
			t.Errorf("%s %s without token = %d, want 403", rt.method, rt.path, w.Code)
		}
	}
	if got, _ := env.app.Repos.Burns.ListByRecord(context.Background(), "BURN-API-2"); len(got) != 0 {
		t.Error("a burn was created without admin authorization")
	}
}
