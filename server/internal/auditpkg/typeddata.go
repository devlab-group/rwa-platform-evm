package auditpkg

import (
	"encoding/hex"

	"github.com/rwa-platform/server/internal/eip712"
)

// NewMintTypedDataDoc builds the typed-data.json document for a mint
// attestation package, matching shared/schemas/typed-data.schema.json.
func NewMintTypedDataDoc(domain eip712.Domain, recordID string, a eip712.MintAttestation) TypedDataDoc {
	return TypedDataDoc{
		PrimaryType: "MintAttestation",
		Domain: TypedDataDomain{
			Name:              domain.Name,
			Version:           domain.Version,
			ChainID:           domain.ChainID.Int64(),
			VerifyingContract: domain.VerifyingContract.Hex(),
		},
		Message: TypedDataMessage{
			Auditor:        a.Auditor.Hex(),
			ProfileDigest:  "0x" + hex.EncodeToString(a.ProfileDigest[:]),
			RecordID:       recordID,
			RecordKey:      "0x" + hex.EncodeToString(a.RecordKey[:]),
			MetadataDigest: "0x" + hex.EncodeToString(a.MetadataDigest[:]),
			Amount:         a.Amount.String(),
			Nonce:          a.Nonce.String(),
			ValidUntil:     int64(a.ValidUntil),
			Vault:          a.Vault.Hex(),
		},
	}
}

// NewBurnTypedDataDoc builds the typed-data.json document for a burn
// attestation package, matching shared/schemas/typed-data.schema.json.
func NewBurnTypedDataDoc(domain eip712.Domain, a eip712.BurnAttestation) TypedDataDoc {
	return TypedDataDoc{
		PrimaryType: "BurnAttestation",
		Domain: TypedDataDomain{
			Name:              domain.Name,
			Version:           domain.Version,
			ChainID:           domain.ChainID.Int64(),
			VerifyingContract: domain.VerifyingContract.Hex(),
		},
		Message: TypedDataMessage{
			Auditor:        a.Auditor.Hex(),
			ProfileDigest:  "0x" + hex.EncodeToString(a.ProfileDigest[:]),
			OperationID:    "0x" + hex.EncodeToString(a.OperationID[:]),
			MetadataDigest: "0x" + hex.EncodeToString(a.MetadataDigest[:]),
			Amount:         a.Amount.String(),
			Nonce:          a.Nonce.String(),
			ValidUntil:     int64(a.ValidUntil),
			Vault:          a.Vault.Hex(),
		},
	}
}
