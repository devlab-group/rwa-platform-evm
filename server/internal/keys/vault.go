package keys

import (
	"bytes"
	"context"
	"encoding/asn1"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
)

// vaultProvider signs through the HashiCorp Vault Transit HTTP API, or any
// Transit-compatible mount, so this server never holds the private key.
//
//  1. Stock Vault Transit has no secp256k1 key type (its ECDSA types are
//     ecdsa-p256, ecdsa-p384 and ecdsa-p521), and Ethereum needs secp256k1.
//     VaultAddr must point at a backend that implements the Transit wire
//     protocol for secp256k1, such as a plugin. The provider also expects the
//     key read response to carry a base64 uncompressed public key, which a
//     stock Transit ECDSA key does not return.
//  2. Transit returns a bare (r,s) signature without a recovery id, and
//     Ethereum also needs v. The provider recovers the public key for both
//     candidate values of v and keeps the one that matches the key's public
//     key.
//
// Signing requests the latest key version, but the address and public key are
// fetched at construction and on Reload. After a Transit key rotation, which
// creates a new keypair, signatures stop matching the cached public key and
// SignTx fails until Reload or a restart. The on-chain role granted to the old
// address must also move to the new one.
type vaultProvider struct {
	http    *http.Client
	addr    string // Vault server base URL, e.g. https://vault.internal:8200
	token   string
	keyName string // VaultKeyPrefix + role

	mu          sync.RWMutex
	ethAddr     common.Address
	pubKeyBytes []byte // uncompressed secp256k1 public key, 65 bytes (0x04 || X || Y)
}

func newVaultProvider(role, addr, token, keyPrefix string) (*vaultProvider, error) {
	if addr == "" {
		return nil, fmt.Errorf("keys: vault mode: VAULT_ADDR is required")
	}
	if token == "" {
		return nil, fmt.Errorf("keys: vault mode: VAULT_TOKEN is required")
	}
	p := &vaultProvider{
		http: &http.Client{Timeout: 15 * time.Second}, addr: strings.TrimRight(addr, "/"),
		token: token, keyName: keyPrefix + role,
	}
	if err := p.fetchPublicKey(context.Background()); err != nil {
		return nil, err
	}
	return p, nil
}

// transitKeyResponse holds the fields used from GET /v1/transit/keys/:name.
// PublicKey must be a base64-encoded 65-byte uncompressed secp256k1 point, a
// backend-specific format described on vaultProvider.
type transitKeyResponse struct {
	Data struct {
		LatestVersion int `json:"latest_version"`
		Keys          map[string]struct {
			PublicKey string `json:"public_key"`
		} `json:"keys"`
	} `json:"data"`
}

func (p *vaultProvider) fetchPublicKey(ctx context.Context) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.addr+"/v1/transit/keys/"+p.keyName, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Vault-Token", p.token)
	resp, err := p.http.Do(req)
	if err != nil {
		return fmt.Errorf("keys: vault: GET transit/keys/%s: %w", p.keyName, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("keys: vault: GET transit/keys/%s: status %d", p.keyName, resp.StatusCode)
	}
	var body transitKeyResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return fmt.Errorf("keys: vault: decoding transit/keys/%s response: %w", p.keyName, err)
	}
	latest, ok := body.Data.Keys[fmt.Sprintf("%d", body.Data.LatestVersion)]
	if !ok {
		return fmt.Errorf("keys: vault: transit/keys/%s response missing latest_version %d", p.keyName, body.Data.LatestVersion)
	}
	pubBytes, err := base64.StdEncoding.DecodeString(latest.PublicKey)
	if err != nil {
		return fmt.Errorf("keys: vault: transit/keys/%s public_key is not valid base64: %w", p.keyName, err)
	}
	if len(pubBytes) != 65 || pubBytes[0] != 0x04 {
		return fmt.Errorf("keys: vault: transit/keys/%s public_key is not an uncompressed secp256k1 point (got %d bytes)", p.keyName, len(pubBytes))
	}
	pubKey, err := crypto.UnmarshalPubkey(pubBytes)
	if err != nil {
		return fmt.Errorf("keys: vault: transit/keys/%s public_key does not parse as secp256k1: %w", p.keyName, err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.pubKeyBytes = pubBytes
	p.ethAddr = crypto.PubkeyToAddress(*pubKey)
	return nil
}

func (p *vaultProvider) Address(context.Context) (common.Address, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.ethAddr, nil
}

func (p *vaultProvider) Reload(ctx context.Context) error {
	return p.fetchPublicKey(ctx)
}

// Close does nothing because the provider never holds private key material,
// only the public key and signatures
func (p *vaultProvider) Close() error { return nil }

// transitSignRequest and transitSignResponse model POST /v1/transit/sign/:name
type transitSignRequest struct {
	Input         string `json:"input"`
	Prehashed     bool   `json:"prehashed"`
	HashAlgorithm string `json:"hash_algorithm"`
}

type transitSignResponse struct {
	Data struct {
		Signature string `json:"signature"` // "vault:v<n>:<base64 DER (r,s)>"
	} `json:"data"`
}

// derSignature is the ASN.1 SEQUENCE{r INTEGER, s INTEGER} that Transit returns
// for ECDSA keys by default (marshaling_algorithm asn1)
type derSignature struct {
	R, S *big.Int
}

func (p *vaultProvider) SignTx(ctx context.Context, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error) {
	ethSigner := types.LatestSignerForChainID(chainID)
	sighash := ethSigner.Hash(tx)

	reqBody, err := json.Marshal(transitSignRequest{
		Input: base64.StdEncoding.EncodeToString(sighash[:]), Prehashed: true, HashAlgorithm: "sha2-256",
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.addr+"/v1/transit/sign/"+p.keyName, bytes.NewReader(reqBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Vault-Token", p.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := p.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("keys: vault: POST transit/sign/%s: %w", p.keyName, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("keys: vault: POST transit/sign/%s: status %d", p.keyName, resp.StatusCode)
	}
	var body transitSignResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("keys: vault: decoding transit/sign/%s response: %w", p.keyName, err)
	}

	parts := strings.Split(body.Data.Signature, ":")
	if len(parts) != 3 {
		return nil, fmt.Errorf("keys: vault: unexpected signature format %q", body.Data.Signature)
	}
	derBytes, err := base64.StdEncoding.DecodeString(parts[2])
	if err != nil {
		return nil, fmt.Errorf("keys: vault: signature is not valid base64: %w", err)
	}
	var sig derSignature
	if _, err := asn1.Unmarshal(derBytes, &sig); err != nil {
		return nil, fmt.Errorf("keys: vault: signature is not valid ASN.1 DER: %w", err)
	}

	rsv, err := recoverableSignature(sighash[:], sig.R, sig.S, p.pubKeyBytesSnapshot())
	if err != nil {
		return nil, fmt.Errorf("keys: vault: %w", err)
	}
	return tx.WithSignature(ethSigner, rsv)
}

func (p *vaultProvider) pubKeyBytesSnapshot() []byte {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]byte, len(p.pubKeyBytes))
	copy(out, p.pubKeyBytes)
	return out
}

// secp256k1HalfN is half the secp256k1 group order, used to canonicalize s
var secp256k1HalfN = new(big.Int).Rsh(crypto.S256().Params().N, 1)

// recoverableSignature builds the 65-byte R||S||V Ethereum signature from a bare
// (r,s) pair. It tries both recovery ids and keeps the one that recovers to
// expectedPubKey.
//
// It also lowers s to the lower half of the curve order, as EIP-2 requires for
// transaction signatures: if s is in the upper half, (r, N-s) is an equally
// valid signature for the same message and key. A generic ECDSA signer may
// return a high s, and Ethereum nodes reject it.
func recoverableSignature(digest []byte, r, s *big.Int, expectedPubKey []byte) ([]byte, error) {
	if s.Cmp(secp256k1HalfN) > 0 {
		s = new(big.Int).Sub(crypto.S256().Params().N, s)
	}

	rBytes := make([]byte, 32)
	sBytes := make([]byte, 32)
	r.FillBytes(rBytes)
	s.FillBytes(sBytes)

	for v := byte(0); v < 2; v++ {
		candidate := append(append(append([]byte{}, rBytes...), sBytes...), v)
		recovered, err := crypto.Ecrecover(digest, candidate)
		if err != nil {
			continue
		}
		if bytes.Equal(recovered, expectedPubKey) {
			return candidate, nil
		}
	}
	return nil, fmt.Errorf("could not derive a recovery id matching the key's known public key (signature or public key mismatch)")
}
