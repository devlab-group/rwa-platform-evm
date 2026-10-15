package auditpkg

import (
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/eip712"
)

// SignedResult mirrors shared/schemas/signed-result.schema.json / api Schemas.SignedResult
type SignedResult struct {
	FormatVersion   string `json:"formatVersion"`
	Auditor         string `json:"auditor"`
	PrimaryType     string `json:"primaryType"`
	TypedDataDigest string `json:"typedDataDigest"`
	Signature       string `json:"signature"`
	SignedAt        string `json:"signedAt"`
}

// VerifyMintSignedResult recomputes the MintAttestation EIP-712 digest, checks
// sr.TypedDataDigest, and recovers the signer. That signer must equal sr.Auditor, the
// attestation's auditor and configuredAuditor.
func VerifyMintSignedResult(sr SignedResult, domain eip712.Domain, attestation eip712.MintAttestation, configuredAuditor common.Address) error {
	if sr.FormatVersion != "1.0" {
		return fmt.Errorf("auditpkg: unsupported formatVersion %q", sr.FormatVersion)
	}
	if sr.PrimaryType != "MintAttestation" {
		return fmt.Errorf("auditpkg: expected primaryType MintAttestation, got %q", sr.PrimaryType)
	}
	digest := eip712.MintDigest(domain, attestation)
	return verifyCommon(sr, digest, attestation.Auditor, configuredAuditor)
}

// VerifyBurnSignedResult is VerifyMintSignedResult for a BurnAttestation
func VerifyBurnSignedResult(sr SignedResult, domain eip712.Domain, attestation eip712.BurnAttestation, configuredAuditor common.Address) error {
	if sr.FormatVersion != "1.0" {
		return fmt.Errorf("auditpkg: unsupported formatVersion %q", sr.FormatVersion)
	}
	if sr.PrimaryType != "BurnAttestation" {
		return fmt.Errorf("auditpkg: expected primaryType BurnAttestation, got %q", sr.PrimaryType)
	}
	digest := eip712.BurnDigest(domain, attestation)
	return verifyCommon(sr, digest, attestation.Auditor, configuredAuditor)
}

func verifyCommon(sr SignedResult, locallyComputedDigest [32]byte, attestationAuditor, configuredAuditor common.Address) error {
	declaredDigest := strings.TrimPrefix(sr.TypedDataDigest, "0x")
	declaredBytes, err := hex.DecodeString(declaredDigest)
	if err != nil || len(declaredBytes) != 32 {
		return fmt.Errorf("auditpkg: invalid typedDataDigest %q", sr.TypedDataDigest)
	}
	for i := range locallyComputedDigest {
		if locallyComputedDigest[i] != declaredBytes[i] {
			return errors.New("auditpkg: typedDataDigest does not match the locally recomputed EIP-712 digest")
		}
	}

	sigHex := strings.TrimPrefix(sr.Signature, "0x")
	sigBytes, err := hex.DecodeString(sigHex)
	if err != nil {
		return fmt.Errorf("auditpkg: invalid signature hex: %w", err)
	}
	recovered, err := eip712.RecoverSigner(locallyComputedDigest, sigBytes)
	if err != nil {
		return fmt.Errorf("auditpkg: signature recovery failed: %w", err)
	}

	declaredAuditor := common.HexToAddress(sr.Auditor)
	if recovered != declaredAuditor {
		return fmt.Errorf("auditpkg: recovered signer %s does not match declared auditor %s", recovered.Hex(), declaredAuditor.Hex())
	}
	if recovered != attestationAuditor {
		return fmt.Errorf("auditpkg: recovered signer %s does not match attestation auditor %s", recovered.Hex(), attestationAuditor.Hex())
	}
	if recovered != configuredAuditor {
		return fmt.Errorf("auditpkg: recovered signer %s is not the configured project auditor %s", recovered.Hex(), configuredAuditor.Hex())
	}
	return nil
}
