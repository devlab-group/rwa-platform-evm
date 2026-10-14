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
// unexpired challenge exists for the address, either because none was issued
// or because it expired. The two cases are not distinguished, so a login probe
// learns nothing about which applies.
var ErrAdminChallengeExpired = errors.New("auth: no active admin challenge for this address")

// ErrAdminChallengeUsed is returned by Verify for a challenge that was already
// consumed
var ErrAdminChallengeUsed = errors.New("auth: admin challenge already used")

// ErrAdminAddressMismatch is returned when the recovered signer does not
// control the address the challenge was issued for
var ErrAdminAddressMismatch = errors.New("auth: recovered signer does not match challenge address")

// AdminChallengeService issues and verifies single-use admin wallet-login
// challenges, the first half of the wallet-signature to JWT flow. A challenge
// is a random nonce in a personal_sign (EIP-191) message. The service only
// proves control of a wallet and has no link to investor records; the caller
// (api/auth.go) compares the recovered signer to the configured admin address
// before issuing a JWT.
//
// Each address has one active challenge (Create upserts). POST /auth/session
// carries only {address, signature}, so Verify looks the challenge up by
// address and recovers the signer from the stored message.
type AdminChallengeService struct {
	repo   repository.AdminChallengeRepository
	ttl    time.Duration
	domain string
}

// NewAdminChallengeService constructs an AdminChallengeService. domainName
// opens the human-readable challenge message, as in the investor challenge,
// and is typically the issuer's site name.
func NewAdminChallengeService(repo repository.AdminChallengeRepository, ttl time.Duration, domainName string) *AdminChallengeService {
	return &AdminChallengeService{repo: repo, ttl: ttl, domain: domainName}
}

// Create stores a new challenge for address, replacing any earlier one, and
// returns the exact message the wallet must personal_sign with its expiry. It
// does not check whether address is the admin, so anyone may request a
// challenge; authorization rests on Verify and the caller's comparison with the
// configured admin address.
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
// address, enforcing expiry and single use, and returns the recovered signer.
// The caller decides authorization by comparing the returned address to the
// configured admin address. The challenge is consumed only when the signature
// recovers to the address it was issued for, so a wrong signature cannot burn
// the admin's pending challenge.
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

	// MarkUsed is a compare-and-set, so of two concurrent replays only one wins
	if err := s.repo.MarkUsed(ctx, normalized); err != nil {
		if errors.Is(err, repository.ErrAlreadyExists) {
			return common.Address{}, ErrAdminChallengeUsed
		}
		return common.Address{}, err
	}
	return recovered, nil
}
