package dto

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/rwa-platform/server/internal/dal/models"
)

func TestToProjectResponse_ReportsAdminDelayChanges(t *testing.T) {
	p := &models.Project{
		Security: &models.SecurityState{
			AdminDelayChanges: []models.AdminDelayChange{
				{Contract: "vault", NewDelay: 86400, EffectAt: 1800000000},
			},
		},
	}

	body, err := json.Marshal(ToProjectResponse(p, "", nil, 0, false))
	if err != nil {
		t.Fatal(err)
	}

	want := `"adminDelayChanges":[{"contract":"vault","newDelay":86400,"effectAt":1800000000}]`
	if !strings.Contains(string(body), want) {
		t.Fatalf("response %s does not contain %s", body, want)
	}
}

func TestToProjectResponse_NoAdminDelayChanges_OmitsTheField(t *testing.T) {
	body, err := json.Marshal(ToProjectResponse(&models.Project{Security: &models.SecurityState{}}, "", nil, 0, false))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), "adminDelayChanges") {
		t.Fatalf("response %s lists adminDelayChanges", body)
	}
}
