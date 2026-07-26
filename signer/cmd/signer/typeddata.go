package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/big"
	"regexp"

	"github.com/ethereum/go-ethereum/common"
	"github.com/rwa-platform/signer/internal/attestation"
)

// typedData is the parsed shape of typed-data.json inside a .rwa package.
//
// NOTE: there is no frozen schema for typed-data.json yet; this shape was
// inferred from shared/vectors/mint-eip712.json and shared/eip712/domain.json,
// the only sources that pin it down today. Treat that as a known
// specification gap to close later rather than something to paper over here.
type typedData struct {
	PrimaryType string `json:"primaryType"`
	Domain      struct {
		Name              string      `json:"name"`
		Version           string      `json:"version"`
		ChainID           json.Number `json:"chainId"`
		VerifyingContract string      `json:"verifyingContract"`
	} `json:"domain"`
	Message struct {
		Auditor        string `json:"auditor"`
		ProfileDigest  string `json:"profileDigest"`
		RecordID       string `json:"recordId,omitempty"`
		RecordKey      string `json:"recordKey,omitempty"`
		OperationID    string `json:"operationId,omitempty"`
		MetadataDigest string `json:"metadataDigest"`
		Amount         string `json:"amount"`
		Nonce          string `json:"nonce"`
		ValidUntil     uint64 `json:"validUntil"`
		Vault          string `json:"vault"`
	} `json:"message"`
}

var (
	addrPattern = regexp.MustCompile(`^0x[0-9a-fA-F]{40}$`)
	bytes32Ptn  = regexp.MustCompile(`^0x[0-9a-fA-F]{64}$`)
	decimalPtn  = regexp.MustCompile(`^[0-9]+$`)
)

func parseTypedData(raw []byte) (*typedData, error) {
	var td typedData
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&td); err != nil {
		return nil, fmt.Errorf("typed-data.json: %w", err)
	}
	if dec.More() {
		return nil, fmt.Errorf("typed-data.json: trailing data")
	}

	if td.PrimaryType != "MintAttestation" && td.PrimaryType != "BurnAttestation" {
		return nil, fmt.Errorf("typed-data.json: primaryType must be MintAttestation or BurnAttestation, got %q", td.PrimaryType)
	}
	if td.Domain.Name != attestation.DomainName {
		return nil, fmt.Errorf("typed-data.json: domain.name %q does not match frozen domain name %q", td.Domain.Name, attestation.DomainName)
	}
	if td.Domain.Version != attestation.DomainVersion {
		return nil, fmt.Errorf("typed-data.json: domain.version %q does not match frozen domain version %q", td.Domain.Version, attestation.DomainVersion)
	}
	if _, ok := new(big.Int).SetString(td.Domain.ChainID.String(), 10); !ok {
		return nil, fmt.Errorf("typed-data.json: domain.chainId is not a valid integer")
	}
	if !addrPattern.MatchString(td.Domain.VerifyingContract) {
		return nil, fmt.Errorf("typed-data.json: domain.verifyingContract must be a 20-byte address")
	}

	m := td.Message
	if !addrPattern.MatchString(m.Auditor) {
		return nil, fmt.Errorf("typed-data.json: message.auditor must be a 20-byte address")
	}
	if !bytes32Ptn.MatchString(m.ProfileDigest) {
		return nil, fmt.Errorf("typed-data.json: message.profileDigest must be a 32-byte hex value")
	}
	if !bytes32Ptn.MatchString(m.MetadataDigest) {
		return nil, fmt.Errorf("typed-data.json: message.metadataDigest must be a 32-byte hex value")
	}
	if !decimalPtn.MatchString(m.Amount) {
		return nil, fmt.Errorf("typed-data.json: message.amount must be a decimal string")
	}
	if !decimalPtn.MatchString(m.Nonce) {
		return nil, fmt.Errorf("typed-data.json: message.nonce must be a decimal string")
	}
	if !addrPattern.MatchString(m.Vault) {
		return nil, fmt.Errorf("typed-data.json: message.vault must be a 20-byte address")
	}

	switch td.PrimaryType {
	case "MintAttestation":
		if m.RecordID == "" {
			return nil, fmt.Errorf("typed-data.json: message.recordId is required for MintAttestation")
		}
		if !bytes32Ptn.MatchString(m.RecordKey) {
			return nil, fmt.Errorf("typed-data.json: message.recordKey must be a 32-byte hex value")
		}
		if m.OperationID != "" {
			return nil, fmt.Errorf("typed-data.json: message.operationId must not be set for MintAttestation")
		}
	case "BurnAttestation":
		if !bytes32Ptn.MatchString(m.OperationID) {
			return nil, fmt.Errorf("typed-data.json: message.operationId must be a 32-byte hex value")
		}
		if m.RecordID != "" || m.RecordKey != "" {
			return nil, fmt.Errorf("typed-data.json: message.recordId/recordKey must not be set for BurnAttestation")
		}
	}

	return &td, nil
}

func hexToHash32(s string) [32]byte {
	var out [32]byte
	b := common.FromHex(s)
	copy(out[32-len(b):], b)
	return out
}

func (td *typedData) domain() attestation.Domain {
	chainID, _ := new(big.Int).SetString(td.Domain.ChainID.String(), 10)
	return attestation.Domain{
		ChainID:           chainID,
		VerifyingContract: common.HexToAddress(td.Domain.VerifyingContract),
	}
}

func (td *typedData) mintAttestation() attestation.MintAttestation {
	m := td.Message
	amount, _ := new(big.Int).SetString(m.Amount, 10)
	nonce, _ := new(big.Int).SetString(m.Nonce, 10)
	return attestation.MintAttestation{
		Auditor:        common.HexToAddress(m.Auditor),
		ProfileDigest:  hexToHash32(m.ProfileDigest),
		RecordKey:      hexToHash32(m.RecordKey),
		MetadataDigest: hexToHash32(m.MetadataDigest),
		Amount:         amount,
		Nonce:          nonce,
		ValidUntil:     m.ValidUntil,
		Vault:          common.HexToAddress(m.Vault),
	}
}

func (td *typedData) burnAttestation() attestation.BurnAttestation {
	m := td.Message
	amount, _ := new(big.Int).SetString(m.Amount, 10)
	nonce, _ := new(big.Int).SetString(m.Nonce, 10)
	return attestation.BurnAttestation{
		Auditor:        common.HexToAddress(m.Auditor),
		ProfileDigest:  hexToHash32(m.ProfileDigest),
		OperationID:    hexToHash32(m.OperationID),
		MetadataDigest: hexToHash32(m.MetadataDigest),
		Amount:         amount,
		Nonce:          nonce,
		ValidUntil:     m.ValidUntil,
		Vault:          common.HexToAddress(m.Vault),
	}
}
