package assets

import (
	"bytes"
	"fmt"
	"math/big"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/rwa-platform/server/internal/dal/models"
)

// Share the model types with API response views
type (
	LegalDescriptor   = models.LegalDescriptor
	DocumentReference = models.DocumentReference
	Rating            = models.Rating
)

var (
	leiPattern    = regexp.MustCompile(`^[A-Z0-9]{18}[0-9]{2}$`)
	sha256Pattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
)

// leiChecksumOK checks ISO 7064 MOD 97-10: expand A-Z to 10-35, then require remainder 1 mod 97
func leiChecksumOK(lei string) bool {
	var digits strings.Builder
	for _, r := range lei {
		if r >= 'A' && r <= 'Z' {
			fmt.Fprintf(&digits, "%d", r-'A'+10)
		} else {
			digits.WriteRune(r)
		}
	}
	n, ok := new(big.Int).SetString(digits.String(), 10)
	return ok && new(big.Int).Mod(n, big.NewInt(97)).Int64() == 1
}

// printableText matches signer textsafe: valid UTF-8, unicode.IsPrint, no C0/DEL/C1 controls
func printableText(s string) bool {
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r <= 0x9f) || !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

func printableLen(s string, min, max int) bool { return lengthInRange(s, min, max) && printableText(s) }

func checkDocRef(name string, d DocumentReference) []string {
	var errs []string
	if !printableLen(d.URI, 1, 2048) || strings.ContainsFunc(d.URI, unicode.IsSpace) {
		errs = append(errs, name+".uri must be printable text of 1..2048 characters with no whitespace")
	}
	if !sha256Pattern.MatchString(d.SHA256) {
		errs = append(errs, name+".sha256 must be 64 lowercase hex characters")
	}
	return errs
}

// parseLegal strictly decodes and validates the legal member of a profile
func parseLegal(raw []byte) (*LegalDescriptor, []string) {
	var l LegalDescriptor
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, []string{"legal must be an object"}
	}
	if err := unmarshalStrict(raw, &l); err != nil {
		return nil, []string{"legal must be an object with the documented members only: " + err.Error()}
	}
	var errs []string
	switch l.Wrapper {
	case "share", "entitlement", "certificate", "claim":
	default:
		errs = append(errs, "legal.wrapper must be one of share, entitlement, certificate, claim")
	}
	if l.IssuerLEI != nil && (!leiPattern.MatchString(*l.IssuerLEI) || !leiChecksumOK(*l.IssuerLEI)) {
		errs = append(errs, "legal.issuerLei must be a valid ISO 17442 LEI with correct check digits")
	}
	if l.HolderRights != nil && !printableLen(*l.HolderRights, 1, 2048) {
		errs = append(errs, "legal.holderRights must be printable text of 1..2048 characters")
	}
	if l.RedemptionTerms != nil {
		errs = append(errs, checkDocRef("legal.redemptionTerms", *l.RedemptionTerms)...)
	}
	if l.MaturityDate != nil {
		// time.Parse rejects impossible dates such as 2030-02-30
		if _, err := time.Parse("2006-01-02", *l.MaturityDate); err != nil {
			errs = append(errs, "legal.maturityDate must be a real calendar date, YYYY-MM-DD")
		}
	}
	if r := l.Rating; r != nil {
		if !printableLen(r.Provider, 1, 128) {
			errs = append(errs, "legal.rating.provider must be printable text of 1..128 characters")
		}
		if r.Grade != nil && !printableLen(*r.Grade, 1, 32) {
			errs = append(errs, "legal.rating.grade must be printable text of 1..32 characters")
		}
		errs = append(errs, checkDocRef("legal.rating", DocumentReference{URI: r.URI, SHA256: r.SHA256})...)
	}
	return &l, errs
}
