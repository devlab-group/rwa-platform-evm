package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/rwa-platform/server/internal/assets"
	"github.com/rwa-platform/server/internal/config"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
)

const (
	boundProjectID = "4fd4224f-6e65-4d6b-9fa9-c5c2b3514e61"
	otherProjectID = "9a1e6f0c-2b7d-4c55-8f3e-0d4b6a7c8e91"
)

// profileJSON returns a profile for projectID whose assetSchema property "kind" has the given
// schema, so a test can swap in a form this build rejects
func profileJSON(projectID, kindSchema string) []byte {
	return []byte(`{
  "profileVersion": "1.0",
  "projectId": "` + projectID + `",
  "assetType": "allocated-gold-bar",
  "tokenUnit": "gram",
  "tokenDecimals": 18,
  "recordIdLabel": "Bar serial number",
  "assetSchema": {
    "type": "object",
    "additionalProperties": false,
    "required": ["serialNumber"],
    "properties": {
      "serialNumber": { "type": "string", "minLength": 1 },
      "kind": ` + kindSchema + `
    }
  }
}`)
}

const (
	validKind = `{ "type": "string" }`
	// an empty enum passed both validators before this release and is now rejected
	emptyEnumKind = `{ "enum": [] }`
	staleDigest   = "0x1111111111111111111111111111111111111111111111111111111111111111"
)

// storeProfile saves raw the way the platform does after pinning it. digest "" stores the
// digest this build computes, as for a profile pinned by the current release.
func storeProfile(t *testing.T, repos *repository.Repositories, projectID string, raw []byte, digest string, createdAt time.Time) {
	t.Helper()
	if digest == "" {
		result, _ := assets.ValidateProfile(raw)
		if !result.Valid {
			t.Fatalf("fixture profile for %s is invalid: %v", projectID, result.Errors)
		}
		digest = result.ProfileDigest
	}
	if err := repos.AssetProfiles.Create(t.Context(), &models.AssetProfile{
		ProjectID:  projectID,
		ProfileRaw: raw,
		Digest:     digest,
		CreatedAt:  createdAt,
	}); err != nil {
		t.Fatalf("store profile %s: %v", projectID, err)
	}
}

func storeProject(t *testing.T, repos *repository.Repositories, projectID string) {
	t.Helper()
	if err := repos.Projects.Upsert(t.Context(), &models.Project{ProjectID: projectID}); err != nil {
		t.Fatalf("store project: %v", err)
	}
}

func TestRunProfileCheck(t *testing.T) {
	older := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	newer := older.Add(time.Hour)

	tests := []struct {
		name    string
		arrange func(t *testing.T, repos *repository.Repositories)
		wantErr bool
		// every string must appear in the command's output
		wantOut []string
	}{
		{
			name: "bound profile that validates with an unchanged digest passes",
			arrange: func(t *testing.T, repos *repository.Repositories) {
				storeProject(t, repos, boundProjectID)
				storeProfile(t, repos, boundProjectID, profileJSON(boundProjectID, validKind), "", older)
			},
			wantOut: []string{"OK: stored profile " + boundProjectID + " validates"},
		},
		{
			name: "bound profile using a newly rejected form fails and lists the error",
			arrange: func(t *testing.T, repos *repository.Repositories) {
				storeProject(t, repos, boundProjectID)
				storeProfile(t, repos, boundProjectID, profileJSON(boundProjectID, emptyEnumKind), staleDigest, older)
			},
			wantErr: true,
			wantOut: []string{"FAIL: stored profile " + boundProjectID + " does not validate", "enum"},
		},
		{
			name: "bound profile whose digest this build computes differently fails",
			arrange: func(t *testing.T, repos *repository.Repositories) {
				storeProject(t, repos, boundProjectID)
				storeProfile(t, repos, boundProjectID, profileJSON(boundProjectID, validKind), staleDigest, older)
			},
			wantErr: true,
			wantOut: []string{"FAIL: stored profile " + boundProjectID + " validates, but its digest is now", "instead of the stored " + staleDigest},
		},
		{
			name: "checks the profile bound to the project, not a newer stored one",
			arrange: func(t *testing.T, repos *repository.Repositories) {
				storeProject(t, repos, boundProjectID)
				storeProfile(t, repos, boundProjectID, profileJSON(boundProjectID, validKind), "", older)
				storeProfile(t, repos, otherProjectID, profileJSON(otherProjectID, emptyEnumKind), staleDigest, newer)
			},
			wantOut: []string{"OK: stored profile " + boundProjectID},
		},
		{
			name: "before deployment checks the latest stored profile",
			arrange: func(t *testing.T, repos *repository.Repositories) {
				storeProfile(t, repos, boundProjectID, profileJSON(boundProjectID, validKind), "", older)
				storeProfile(t, repos, otherProjectID, profileJSON(otherProjectID, emptyEnumKind), staleDigest, newer)
			},
			wantErr: true,
			wantOut: []string{"FAIL: stored profile " + otherProjectID},
		},
		{
			name:    "no stored profile fails",
			arrange: func(t *testing.T, repos *repository.Repositories) {},
			wantErr: true,
		},
		{
			name: "project whose profile is missing fails instead of checking another profile",
			arrange: func(t *testing.T, repos *repository.Repositories) {
				storeProject(t, repos, boundProjectID)
				storeProfile(t, repos, otherProjectID, profileJSON(otherProjectID, validKind), "", older)
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			repos := newTestRepos()
			tt.arrange(t, repos)
			o := testOpsCtx(repos)
			out := o.out.(*bytes.Buffer)

			err := runProfileCheck(o)

			if (err != nil) != tt.wantErr {
				t.Fatalf("runProfileCheck() error = %v, wantErr %v\noutput:\n%s", err, tt.wantErr, out)
			}
			for _, want := range tt.wantOut {
				if !strings.Contains(out.String(), want) {
					t.Errorf("output does not contain %q\noutput:\n%s", want, out)
				}
			}
			if tt.wantErr && strings.Contains(out.String(), "OK:") {
				t.Errorf("a failed check printed OK\noutput:\n%s", out)
			}
		})
	}
}

func TestDispatchRunsProfileCheck(t *testing.T) {
	repos := newTestRepos()
	storeProject(t, repos, boundProjectID)
	storeProfile(t, repos, boundProjectID, profileJSON(boundProjectID, emptyEnumKind), staleDigest, time.Now())
	o := testOpsCtx(repos)
	out := o.out.(*bytes.Buffer)

	err := dispatch(o, config.Config{}, "profile", "check", nil)

	// the unknown-command error also names "profile check", so require the check's own output
	if err == nil || !strings.Contains(out.String(), "FAIL: stored profile "+boundProjectID) {
		t.Fatalf("dispatch(profile check) error = %v, output %q; want the profile check to run and fail", err, out)
	}
}
