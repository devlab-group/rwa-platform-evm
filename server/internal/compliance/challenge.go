// Package compliance implements wallet-ownership challenges, the KYC webhook
// and compliance status transaction submission. It does not decide KYC
// outcomes. It proves that a wallet is controlled by whoever claims it before a
// webhook may update that wallet, and it relays an already-decided status to
// ComplianceRegistry with the compliance hot key.
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

// ErrChallengeUsed is returned by VerifyChallenge for a replayed challenge
var ErrChallengeUsed = errors.New("compliance: challenge already used")

// ErrChallengeExpired is returned by VerifyChallenge for an expired challenge
var ErrChallengeExpired = errors.New("compliance: challenge expired")

// ErrAddressMismatch is returned when the recovered signer does not match
// the address the challenge was issued for
var ErrAddressMismatch = errors.New("compliance: recovered signer does not match challenge address")

// ErrTooManyActiveChallenges is returned by Create when address already has
// MaxActiveChallengesPerAddress challenges that are neither used nor expired.
// The caller should wait for one to be used or to expire.
var ErrTooManyActiveChallenges = errors.New("compliance: too many active challenges for this address")

// DefaultMaxActiveChallengesPerAddress is the default for
// ChallengeService.MaxActiveChallengesPerAddress. Anyone can request a
// challenge for any valid address, and each request stores a document. The cap
// bounds that storage immediately, while the TTL index on wallet_challenges
// (internal/dal/mongodb) only reclaims it after expiry.
const DefaultMaxActiveChallengesPerAddress = 20

// ChallengeService issues and verifies single-use wallet-ownership challenges
type ChallengeService struct {
	repo      repository.WalletChallengeRepository
	investors repository.InvestorRepository
	ttl       time.Duration
	domain    string
	// MaxActiveChallengesPerAddress caps how many unused, unexpired challenges
	// an address may hold. Create returns ErrTooManyActiveChallenges beyond it,
	// and a value <= 0 removes the cap. NewChallengeService sets it to
	// DefaultMaxActiveChallengesPerAddress.
	MaxActiveChallengesPerAddress int
}

// NewChallengeService constructs a ChallengeService. domainName opens the
// human-readable challenge message and is typically the issuer's site name.
// investors is used only by VerifyOwnership to record the ownership flag, so it
// may be nil if VerifyOwnership is never called.
func NewChallengeService(repo repository.WalletChallengeRepository, investors repository.InvestorRepository, ttl time.Duration, domainName string) *ChallengeService {
	return &ChallengeService{
		repo: repo, investors: investors, ttl: ttl, domain: domainName,
		MaxActiveChallengesPerAddress: DefaultMaxActiveChallengesPerAddress,
	}
}

// Create issues a new challenge for address. The challenge is stored under its
// nonce, so a client only needs the nonce from the response to complete
// verification.
func (s *ChallengeService) Create(ctx context.Context, address string) (*models.WalletChallenge, error) {
	if !common.IsHexAddress(address) {
		return nil, fmt.Errorf("compliance: invalid address %q", address)
	}
	if s.MaxActiveChallengesPerAddress > 0 {
		normalized := common.HexToAddress(address).Hex()
		now := time.Now().UTC()
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
	now := time.Now().UTC()
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

// Verify checks a personal_sign signature over the challenge identified by
// nonce, enforcing expiry and single use, and returns the recovered signer. It
// does not touch the investors repository. VerifyOwnership also records the
// ownership flag.
func (s *ChallengeService) Verify(ctx context.Context, nonce string, signature []byte) (common.Address, error) {
	c, err := s.repo.Get(ctx, nonce)
	if err != nil {
		return common.Address{}, err
	}
	if c.Used {
		return common.Address{}, ErrChallengeUsed
	}
	if time.Now().UTC().After(c.ExpiresAt) {
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

	// MarkUsed is a compare-and-set, so when two requests replay the same
	// signature only one succeeds and the other gets ErrAlreadyExists
	if err := s.repo.MarkUsed(ctx, c.ID); err != nil {
		if errors.Is(err, repository.ErrAlreadyExists) {
			return common.Address{}, ErrChallengeUsed
		}
		return common.Address{}, err
	}
	return recovered, nil
}

// VerifyOwnership backs POST /api/v1/compliance/challenge/verify. It verifies
// the signature with Verify, checks that the claimed address equals the
// recovered signer, and sets the investor's OwnershipVerified flag, which the
// KYC webhook requires before accepting a status change for that wallet.
func (s *ChallengeService) VerifyOwnership(ctx context.Context, address, nonce string, signature []byte) (*models.Investor, error) {
	recovered, err := s.Verify(ctx, nonce, signature)
	if err != nil {
		return nil, err
	}
	if !strings.EqualFold(recovered.Hex(), address) {
		return nil, ErrAddressMismatch
	}

	now := time.Now().UTC()
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
