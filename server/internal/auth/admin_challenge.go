package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/eip712"
)

// ErrAdminChallengeExpired is returned by AdminChallengeService.Verify when no
// active (unexpired) challenge exists for the address — either it was never
// issued, or it has expired. The two are deliberately not distinguished (a
// login probe learns nothing about which).
var ErrAdminChallengeExpired = errors.New("auth: no active admin challenge for this address")

// ErrAdminChallengeUsed is returned by Verify for a replayed challenge.
var ErrAdminChallengeUsed = errors.New("auth: admin challenge already used")

// ErrAdminAddressMismatch is returned when the recovered signer does not
// control the address the challenge was issued for.
var ErrAdminAddressMismatch = errors.New("auth: recovered signer does not match challenge address")

// AdminChallengeService issues and verifies single-use admin wallet-login
// challenges (the admin half of the wallet-signature -> JWT flow). It reuses
// the same primitives that power investor auth — a random nonce, a
// personal_sign message, EIP-191 hashing via accounts.TextHash, and
// eip712.RecoverSigner — but is deliberately NOT entangled with the
// compliance/investor Investor record: it only proves control of a wallet so
// the caller (api/auth.go) can compare the recovered signer to the configured
// admin address before issuing a JWT.
//
// There is one active challenge per address (Create upserts): the verify step
// carries no client-echoed nonce (POST /auth/session is {address, signature}),
// so the service looks the challenge back up by address and recomputes its
// message to recover the signer.
type AdminChallengeService struct {
	repo   repository.AdminChallengeRepository
	ttl    time.Duration
	domain string
}

// NewAdminChallengeService constructs an AdminChallengeService. domainName
// appears in the human-readable challenge message (e.g. the issuer's site
// name), matching the investor challenge's presentation.
func NewAdminChallengeService(repo repository.AdminChallengeRepository, ttl time.Duration, domainName string) *AdminChallengeService {
	return &AdminChallengeService{repo: repo, ttl: ttl, domain: domainName}
}

// Create issues (and stores, replacing any prior) the single active challenge
// for address, returning the exact message the wallet must personal_sign and
// its expiry. It does not itself check whether address is the admin — anyone
// may request a challenge; only Verify + the caller's admin-address comparison
// decides authorization.
func (s *AdminChallengeService) Create(ctx context.Context, address string) (message string, expiresAt time.Time, err error) {
	if !common.IsHexAddress(address) {
		return "", time.Time{}, fmt.Errorf("auth: invalid address %q", address)
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return "", time.Time{}, fmt.Errorf("auth: generate nonce: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes)
	now := time.Now().UTC()
	expiresAt = now.Add(s.ttl)
	addr := common.HexToAddress(address)

	message = fmt.Sprintf(
		"%s admin login.\nSign this message to authenticate as the platform admin.\nAddress: %s\nNonce: %s\nIssued At: %s\nExpires At: %s",
		s.domain, addr.Hex(), nonce, now.Format(time.RFC3339), expiresAt.Format(time.RFC3339),
	)

	c := &models.AdminChallenge{
		Address: addr.Hex(), Message: message, Nonce: nonce,
		ExpiresAt: expiresAt, Used: false, CreatedAt: now,
	}
	if err := s.repo.Upsert(ctx, c); err != nil {
		return "", time.Time{}, err
	}
	return message, expiresAt, nil
}

// Verify checks a personal_sign signature over the active challenge for
// address, enforcing single-use and expiry, and returns the recovered signer.
// The caller is responsible for the final authorization decision (comparing
// the returned address to the configured admin address). The challenge is
// marked used only on a signature that recovers to the address it was issued
// for, so a mismatched signature cannot burn a pending admin's challenge.
func (s *AdminChallengeService) Verify(ctx context.Context, address string, signature []byte) (common.Address, error) {
	if !common.IsHexAddress(address) {
		return common.Address{}, fmt.Errorf("auth: invalid address %q", address)
	}
	normalized := common.HexToAddress(address).Hex()
	c, err := s.repo.Get(ctx, normalized)
	if err != nil {
		if errors.Is(err, repository.ErrNotFound) {
			return common.Address{}, ErrAdminChallengeExpired
		}
		return common.Address{}, err
	}
	if c.Used {
		return common.Address{}, ErrAdminChallengeUsed
	}

	hash := accounts.TextHash([]byte(c.Message))
	var digest [32]byte
	copy(digest[:], hash)
	recovered, err := eip712.RecoverSigner(digest, signature)
	if err != nil {
		return common.Address{}, fmt.Errorf("auth: signature recovery: %w", err)
	}
	if !strings.EqualFold(recovered.Hex(), c.Address) {
		return common.Address{}, ErrAdminAddressMismatch
	}

	// Mark used only after the address matches, so a concurrent replay loses
	// (CAS) and a wrong-address signature cannot consume the challenge.
	if err := s.repo.MarkUsed(ctx, normalized); err != nil {
		if errors.Is(err, repository.ErrAlreadyExists) {
			return common.Address{}, ErrAdminChallengeUsed
		}
		return common.Address{}, err
	}
	return recovered, nil
}
