package dto

import (
	"encoding/json"

	"github.com/rwa-platform/server/internal/dal/models"
)

// StoredProfileResponse mirrors components.schemas.StoredProfile: stored profile JSON
// and derived identity fields used to restore the admin editor and deploy form after
// reload
type StoredProfileResponse struct {
	Profile       json.RawMessage `json:"profile"`
	ProjectID     string          `json:"projectId"`
	ProfileDigest string          `json:"profileDigest"`
	CID           string          `json:"cid,omitempty"`
	Decimals      uint8           `json:"decimals"`
	TokenUnit     string          `json:"tokenUnit"`
}

// ToStoredProfileResponse returns the stored profile JSON without re-marshalling. Its
// digest covers the JCS canonical form.
func ToStoredProfileResponse(p *models.AssetProfile) StoredProfileResponse {
	return StoredProfileResponse{
		Profile: json.RawMessage(p.ProfileRaw), ProjectID: p.ProjectID, ProfileDigest: p.Digest,
		CID: p.CID, Decimals: p.TokenDecimals, TokenUnit: p.TokenUnit,
	}
}

// AssetRecordResponse mirrors components.schemas.AssetRecord. The admin wallet uses
// recordKey (0x-hex), nonce (decimal string) and validUntil (Unix seconds) to rebuild
// MintAttestation for auditor signing and wallet submission; the server does not relay
// mints.
type AssetRecordResponse struct {
	RecordID       string `json:"recordId"`
	Status         string `json:"status"`
	MetadataDigest string `json:"metadataDigest"`
	CID            string `json:"cid"`
	Amount         string `json:"amount"`
	RecordKey      string `json:"recordKey"`
	Nonce          string `json:"nonce"`
	ValidUntil     int64  `json:"validUntil"`
	CreatedAt      string `json:"createdAt"`
}

// ToAssetRecordResponse maps one stored asset record onto its API view
func ToAssetRecordResponse(r *models.AssetRecord) AssetRecordResponse {
	return AssetRecordResponse{
		RecordID: r.RecordID, Status: string(r.Status), MetadataDigest: r.MetadataDigest,
		CID: r.CID, Amount: r.Amount, RecordKey: r.RecordKey, Nonce: r.Nonce, ValidUntil: r.ValidUntil,
		CreatedAt: r.CreatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}

// BurnResponse mirrors components.schemas.Burn; Remaining is computed for the
// response and omitted if unavailable (see assets.BurnService.Remaining)
type BurnResponse struct {
	BurnID         string `json:"burnId"`
	RecordID       string `json:"recordId"`
	OperationID    string `json:"operationId"`
	Amount         string `json:"amount"`
	Unit           string `json:"unit"`
	Status         string `json:"status"`
	MetadataDigest string `json:"metadataDigest"`
	CID            string `json:"cid,omitempty"`
	Nonce          string `json:"nonce"`
	ValidUntil     int64  `json:"validUntil"`
	BurnTxHash     string `json:"burnTxHash,omitempty"`
	Remaining      string `json:"remaining,omitempty"`
	CreatedAt      string `json:"createdAt"`
	UpdatedAt      string `json:"updatedAt"`
}

// ToBurnResponse maps one stored burn onto its API view; remaining may be empty
func ToBurnResponse(b *models.Burn, remaining string) BurnResponse {
	return BurnResponse{
		BurnID: b.BurnID, RecordID: b.RecordID, OperationID: b.OperationID, Amount: b.Amount, Unit: b.Unit,
		Status: string(b.Status), MetadataDigest: b.MetadataDigest, CID: b.CID, Nonce: b.Nonce, ValidUntil: b.ValidUntil,
		BurnTxHash: b.BurnTxHash, Remaining: remaining,
		CreatedAt: b.CreatedAt.Format("2006-01-02T15:04:05Z07:00"), UpdatedAt: b.UpdatedAt.Format("2006-01-02T15:04:05Z07:00"),
	}
}
