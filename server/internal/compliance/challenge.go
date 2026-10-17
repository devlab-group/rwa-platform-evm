// Package compliance proves wallet ownership, processes KYC webhooks and relays
// provider-decided status to ComplianceRegistry with the compliance hot key. Webhooks
// require ownership proof unless allowManualOverride is set; the package does not
// decide KYC outcomes.
package compliance

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

// ErrChallengeUsed is returned by Verify for a replayed challenge
var ErrChallengeUsed = errors.New("compliance: challenge already used")

// ErrChallengeExpired is returned by Verify for an expired challenge
var ErrChallengeExpired = errors.New("compliance: challenge expired")

// ErrAddressMismatch is returned when the recovered signer does not match
// the address the challenge was issued for
var ErrAddressMismatch = errors.New("compliance: recovered signer does not match challenge address")

// ErrTooManyActiveChallenges means Create counted at least
// MaxActiveChallengesPerAddress unused, unexpired challenges. Wait for one to be used
// or expire.
var ErrTooManyActiveChallenges = errors.New("compliance: too many active challenges for this address")

// DefaultMaxActiveChallengesPerAddress sets Create's active-count threshold for public
// challenge requests. The check precedes insertion without an atomic reservation, so
// concurrent requests may exceed it. MongoDB's wallet_challenges TTL index later
// removes expired documents.
const DefaultMaxActiveChallengesPerAddress = 20

// ChallengeService issues and verifies single-use wallet-ownership challenges
type ChallengeService struct {
	repo      repository.WalletChallengeRepository
	investors repository.InvestorRepository
	ttl       time.Duration
	domain    string
	now       func() time.Time // clock; tests replace it to pin challenge expiry
	// MaxActiveChallengesPerAddress is Create's pre-insert count limit for unused,
	// unexpired challenges. At the limit it returns ErrTooManyActiveChallenges; <= 0
	// disables checking. The constructor uses DefaultMaxActiveChallengesPerAddress.
	MaxActiveChallengesPerAddress int
}

// NewChallengeService uses domainName, typically the issuer site, to open challenge
// messages. Only VerifyOwnership uses investors, so that repository may be nil when
// VerifyOwnership is unused.
func NewChallengeService(repo repository.WalletChallengeRepository, investors repository.InvestorRepository, ttl time.Duration, domainName string) *ChallengeService {
	return &ChallengeService{
		repo: repo, investors: investors, ttl: ttl, domain: domainName, now: time.Now,
		MaxActiveChallengesPerAddress: DefaultMaxActiveChallengesPerAddress,
	}
}

// Create stores a new wallet challenge under its nonce, which Verify uses to retrieve
// the message for signature verification
func (s *ChallengeService) Create(ctx context.Context, address string) (*models.WalletChallenge, error) {
	if !common.IsHexAddress(address) {
		return nil, fmt.Errorf("compliance: invalid address %q", address)
	}
	if s.MaxActiveChallengesPerAddress > 0 {
		normalized := common.HexToAddress(address).Hex()
		now := s.now().UTC()
		active, err := s.repo.CountActive(ctx, normalized, now)
		if err != nil {
			return nil, fmt.Errorf("compliance: count active challenges: %w", err)
		}
		if active >= s.MaxActiveChallengesPerAddress {
			return nil, ErrTooManyActiveChallenges
		}
	}
	nonceBytes := make([]byte, 16)
	if _, err := rand.Read(nonceBytes); err != nil {
		return nil, fmt.Errorf("compliance: generate nonce: %w", err)
	}
	nonce := hex.EncodeToString(nonceBytes)
	now := s.now().UTC()
	expiresAt := now.Add(s.ttl)
	addr := common.HexToAddress(address)

	message := fmt.Sprintf(
		"%s wants you to prove ownership of this wallet.\nAddress: %s\nNonce: %s\nIssued At: %s\nExpires At: %s",
		s.domain, addr.Hex(), nonce, now.Format(time.RFC3339), expiresAt.Format(time.RFC3339),
	)

	c := &models.WalletChallenge{
		ID: nonce, Address: addr.Hex(), Nonce: nonce, Message: message,
		ExpiresAt: expiresAt, Used: false, CreatedAt: now,
	}
	if err := s.repo.Create(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// Verify recovers a personal_sign signer after checking the nonce's challenge, expiry
// and single use. VerifyOwnership additionally records ownership in investors.
func (s *ChallengeService) Verify(ctx context.Context, nonce string, signature []byte) (common.Address, error) {
	c, err := s.repo.Get(ctx, nonce)
	if err != nil {
		return common.Address{}, err
	}
	if c.Used {
		return common.Address{}, ErrChallengeUsed
	}
	if s.now().UTC().After(c.ExpiresAt) {
		return common.Address{}, ErrChallengeExpired
	}

	hash := accounts.TextHash([]byte(c.Message))
	var digest [32]byte
	copy(digest[:], hash)
	recovered, err := eip712.RecoverSigner(digest, signature)
	if err != nil {
		return common.Address{}, fmt.Errorf("compliance: signature recovery: %w", err)
	}
	if !strings.EqualFold(recovered.Hex(), c.Address) {
		return common.Address{}, ErrAddressMismatch
	}

	// MarkUsed atomically consumes the challenge; concurrent replay losers map
	// ErrAlreadyExists to ErrChallengeUsed
	if err := s.repo.MarkUsed(ctx, c.ID); err != nil {
		if errors.Is(err, repository.ErrAlreadyExists) {
			return common.Address{}, ErrChallengeUsed
		}
		return common.Address{}, err
	}
	return recovered, nil
}

// VerifyOwnership backs POST /api/v1/compliance/challenge/verify: Verify recovers the
// signer, the claimed address must match, and OwnershipVerified is stored. Webhooks
// require this flag unless allowManualOverride is set.
func (s *ChallengeService) VerifyOwnership(ctx context.Context, address, nonce string, signature []byte) (*models.Investor, error) {
	recovered, err := s.Verify(ctx, nonce, signature)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(recovered.Hex(), address) {
		return nil, ErrAddressMismatch
	}

	now := s.now().UTC()
	inv, err := s.investors.Get(ctx, recovered.Hex())
	if err != nil {
		if !errors.Is(err, repository.ErrNotFound) {
			return nil, err
		}
		inv = &models.Investor{Address: recovered.Hex(), Status: models.ComplianceUnknown, CreatedAt: now}
	}
	inv.OwnershipVerified = true
	inv.UpdatedAt = now
	if err := s.investors.Upsert(ctx, inv); err != nil {
		return nil, err
	}
	return inv, nil
}
