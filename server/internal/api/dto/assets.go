package dto

import (
	"encoding/json"

	"github.com/rwa-platform/server/internal/dal/models"
)

// StoredProfileResponse mirrors components.schemas.StoredProfile: the raw
// stored Asset Profile document plus the server-derived identity fields, so the
// admin console can repopulate its editor and deploy form after a reload
type StoredProfileResponse struct {
	Profile       json.RawMessage `json:"profile"`
	ProjectID     string          `json:"projectId"`
	ProfileDigest string          `json:"profileDigest"`
	CID           string          `json:"cid,omitempty"`
	Decimals      uint8           `json:"decimals"`
	TokenUnit     string          `json:"tokenUnit"`
}

// ToStoredProfileResponse emits the profile as the stored JSON document, without
// re-marshalling it, so the bytes match the digest computed at creation
func ToStoredProfileResponse(p *models.AssetProfile) StoredProfileResponse {
	return StoredProfileResponse{
		Profile: json.RawMessage(p.ProfileRaw), ProjectID: p.ProjectID, ProfileDigest: p.Digest,
		CID: p.CID, Decimals: p.TokenDecimals, TokenUnit: p.TokenUnit,
	}
}

// AssetRecordResponse mirrors components.schemas.AssetRecord. recordKey, nonce
// and validUntil let the admin's web wallet rebuild the MintAttestation
// (recordKey as 0x-hex, nonce as a decimal string, validUntil as Unix seconds)
// for the auditor to sign and the wallet to submit; the server does not relay
// the mint
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
