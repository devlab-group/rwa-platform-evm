// Command signer is the air-gapped CLI auditor tool: it validates a .rwa
// audit package end-to-end and produces a signed-result.json. It never makes
// a network request while signing.
package main

import (
	"bufio"
	"bytes"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/rwa-platform/signer/internal/attestation"
	"github.com/rwa-platform/signer/internal/hardware"
	"github.com/rwa-platform/signer/internal/keystore"
	"github.com/rwa-platform/signer/internal/metadata"
	"github.com/rwa-platform/signer/internal/output"
	rwapkg "github.com/rwa-platform/signer/internal/package"
	"github.com/rwa-platform/signer/internal/policy"
	"github.com/rwa-platform/signer/internal/profile"
	"github.com/rwa-platform/signer/internal/ui"
	"golang.org/x/term"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "sign":
		if err := runSign(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "signer: error:", err)
			os.Exit(1)
		}
	case "keystore":
		if err := runKeystoreCmd(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "signer: error:", err)
			os.Exit(1)
		}
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "signer: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `Usage: signer sign <package.rwa> --keystore <path> [flags]
       signer keystore <create|import> --out <path> [flags]

Air-gapped .rwa audit package review and EIP-712 signing tool.
Makes zero network requests.

Flags:
  --keystore <path>          password-encrypted keystore file (required unless --hardware); accepts
                              either a standard Ethereum V3 JSON keystore or this signer's own
                              Argon2id-based format (internal/keystore), auto-detected
  --password <password>      DEPRECATED, INSECURE: keystore password on the command line, visible in
                              process listings and shell history. Prefer --password-file or the
                              interactive prompt. May be removed in a future version.
  --password-file <path>     file containing the keystore password (first line, trailing newline
                              trimmed). The file must not be group- or world-readable.
  --hardware <device>        sign with a hardware-wallet/HSM adapter instead of a keystore file;
                              <device> is one of: ledger, trezor, yubikey, hsm. No device has a
                              working integration in this V1 build (see internal/hardware) -- this
                              always errors today, but is the recommended production signing path
                              once wired up.
  --out <path>                output path for signed-result.json (default: <package-dir>/signed-result.json)
  --policy <path>            REQUIRED. Independently-provisioned deployment trust root (JSON):
                              chainId, controller, vault, auditor, projectId, profileDigest, and
                              optionally maxAttestationLifetimeHours. Every value must match the
                              package or the signer refuses to sign -- see internal/policy and
                              docs/auditor/auditor-guide.md.
  --yes                      skip the interactive confirmation prompt. Requires --unsafe-test-mode.
  --unsafe-test-mode         required alongside --yes; noninteractive signing removes the human
                              confirmation step and is restricted to scripted test/CI use. --policy
                              is still fully enforced even with this flag -- it never permits signing
                              from package-supplied values alone. It also relaxes the keystore
                              audit-log write failures below from fail-closed to warn-and-continue.
  --audit-anchor <path>      path to the keystore's independent audit-log anchor (default:
                              <keystore>.auditlog.anchor.jsonl, next to the keystore file). Every
                              unlock/sign verifies the keystore's audit log against this anchor
                              before proceeding and refuses to continue if it detects truncation or
                              rewritten history. Point this at separate media (a different mounted
                              drive) for real protection -- an anchor on the same disk as the
                              keystore cannot resist an attacker who can write to that disk. See
                              docs/auditor/auditor-guide.md §5.

Run "signer keystore --help" for the keystore-creation subcommand.`)
}

type signFlags struct {
	keystorePath   string
	password       string
	passwordFile   string
	hardware       string
	out            string
	policyPath     string
	yes            bool
	unsafeTestMode bool
	auditAnchor    string
}

func runSign(args []string) error {
	// The package path is a fixed leading positional argument, not parsed
	// by the flag package: Go's flag.Parse stops consuming flags at the
	// first non-flag token, so "signer sign <package.rwa> --keystore X"
	// (package path before flags, per usage()) would otherwise leave
	// --keystore unparsed.
	if len(args) == 0 {
		usage()
		return fmt.Errorf("expected a <package.rwa> argument")
	}
	pkgPath := args[0]

	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	var f signFlags
	fs.StringVar(&f.keystorePath, "keystore", "", "password-encrypted Ethereum keystore file")
	fs.StringVar(&f.password, "password", "", "DEPRECATED, INSECURE: keystore password on the command line")
	fs.StringVar(&f.passwordFile, "password-file", "", "file containing the keystore password")
	fs.StringVar(&f.hardware, "hardware", "", "sign with a hardware-wallet adapter (ledger, trezor, yubikey, hsm)")
	fs.StringVar(&f.out, "out", "", "output path for signed-result.json")
	fs.StringVar(&f.policyPath, "policy", "", "REQUIRED: path to the independently-provisioned deployment policy file")
	fs.BoolVar(&f.yes, "yes", false, "skip interactive confirmation (requires --unsafe-test-mode)")
	fs.BoolVar(&f.unsafeTestMode, "unsafe-test-mode", false, "required alongside --yes for noninteractive signing")
	fs.StringVar(&f.auditAnchor, "audit-anchor", "", "path to the keystore's independent audit-log anchor (default: <keystore>.auditlog.anchor.jsonl); point this at separate media for real truncation resistance -- see docs/auditor/auditor-guide.md §5")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		usage()
		return fmt.Errorf("unexpected extra arguments: %v", fs.Args())
	}

	if f.hardware == "" && f.keystorePath == "" {
		return fmt.Errorf("--keystore is required (or pass --hardware <device>)")
	}
	if f.policyPath == "" {
		return fmt.Errorf("--policy is required: the signer refuses to sign without an independently-provisioned deployment trust root; see internal/policy and docs/auditor/auditor-guide.md")
	}
	// --yes removes the human confirmation step entirely, so a scripted or
	// automated invocation has to opt in explicitly. Passing --yes by accident
	// (a copy-pasted command, a CI template) must never silently start signing
	// package-declared values with nobody looking at the review screen.
	// --policy is checked regardless of these two flags; this gate only
	// concerns interactive confirmation, not the trust-root check.
	if f.yes && !f.unsafeTestMode {
		return fmt.Errorf("--yes requires --unsafe-test-mode: noninteractive signing bypasses the human confirmation step and is restricted to scripted test/CI use")
	}

	pol, err := policy.Load(f.policyPath)
	if err != nil {
		return fmt.Errorf("loading --policy: %w", err)
	}

	zipBytes, err := rwapkg.ReadPackageFile(pkgPath, rwapkg.MaxCompressedSize)
	if err != nil {
		return fmt.Errorf("reading package: %w", err)
	}

	files, err := rwapkg.Extract(zipBytes, rwapkg.DefaultOptions())
	if err != nil {
		return fmt.Errorf("extracting package: %w", err)
	}

	manifestRaw, ok := files["manifest.json"]
	if !ok {
		return fmt.Errorf("package is missing manifest.json")
	}
	manifest, err := rwapkg.ParseManifest(manifestRaw)
	if err != nil {
		return fmt.Errorf("manifest.json: %w", err)
	}
	// manifest.json itself is not listed among its own "files".
	filesExceptManifest := make(map[string][]byte, len(files))
	for k, v := range files {
		if k != "manifest.json" {
			filesExceptManifest[k] = v
		}
	}
	if err := manifest.VerifyFiles(filesExceptManifest); err != nil {
		return fmt.Errorf("verifying package files against manifest: %w", err)
	}

	profileRaw, ok := files["profile.json"]
	if !ok {
		return fmt.Errorf("package is missing profile.json")
	}
	prof, err := profile.Validate(profileRaw)
	if err != nil {
		return fmt.Errorf("profile.json: %w", err)
	}

	metadataRaw, ok := files["metadata.json"]
	if !ok {
		return fmt.Errorf("package is missing metadata.json")
	}
	meta, err := metadata.Validate(metadataRaw)
	if err != nil {
		return fmt.Errorf("metadata.json: %w", err)
	}

	if err := prof.ValidateAsset(meta.Asset); err != nil {
		return fmt.Errorf("metadata.json: asset does not conform to the project's Asset Profile: %w", err)
	}

	// Cross-document binding: profile.json and metadata.json are otherwise
	// validated independently, so nothing above stops a package from pairing
	// an audited profile with metadata that actually describes a different
	// project or legal unit. The contract only checks hashes and the auditor
	// signature -- not JSON semantics -- so an unbound mismatch here could
	// authorize minting under the wrong project or unit.
	if meta.ProjectID != prof.ProjectID {
		return fmt.Errorf("metadata.json projectId %q does not match profile.json projectId %q", meta.ProjectID, prof.ProjectID)
	}
	if meta.IssuanceUnit != prof.TokenUnit {
		return fmt.Errorf("metadata.json issuance.unit %q does not match profile.json tokenUnit %q", meta.IssuanceUnit, prof.TokenUnit)
	}
	if pol.ProjectID != prof.ProjectID {
		return fmt.Errorf("profile.json projectId %s does not match policy projectId %s", prof.ProjectID, pol.ProjectID)
	}

	profileDigestHex := "0x" + hex.EncodeToString(prof.Digest[:])
	metadataDigestHex := "0x" + hex.EncodeToString(meta.Digest[:])
	if !strings.EqualFold(pol.ProfileDigest, profileDigestHex) {
		return fmt.Errorf("recomputed profileDigest %s does not match policy profileDigest %s", profileDigestHex, pol.ProfileDigest)
	}
	if !strings.EqualFold(profileDigestHex, manifest.ProfileDigest) {
		return fmt.Errorf("recomputed profileDigest %s does not match manifest.json profileDigest %s", profileDigestHex, manifest.ProfileDigest)
	}
	if !strings.EqualFold(metadataDigestHex, manifest.MetadataDigest) {
		return fmt.Errorf("recomputed metadataDigest %s does not match manifest.json metadataDigest %s", metadataDigestHex, manifest.MetadataDigest)
	}

	typedDataRaw, ok := files["typed-data.json"]
	if !ok {
		return fmt.Errorf("package is missing typed-data.json")
	}
	td, err := parseTypedData(typedDataRaw)
	if err != nil {
		return fmt.Errorf("typed-data.json: %w", err)
	}
	if td.PrimaryType != manifest.PrimaryType {
		return fmt.Errorf("typed-data.json primaryType %q does not match manifest.json primaryType %q", td.PrimaryType, manifest.PrimaryType)
	}

	if !strings.EqualFold(td.Message.ProfileDigest, profileDigestHex) {
		return fmt.Errorf("typed-data.json message.profileDigest %s does not match recomputed profileDigest %s", td.Message.ProfileDigest, profileDigestHex)
	}
	if !strings.EqualFold(td.Message.MetadataDigest, metadataDigestHex) {
		return fmt.Errorf("typed-data.json message.metadataDigest %s does not match recomputed metadataDigest %s", td.Message.MetadataDigest, metadataDigestHex)
	}

	amountFromMetadata, ok := new(big.Int).SetString(meta.IssuanceAmount, 10)
	if !ok {
		return fmt.Errorf("internal error: metadata issuance.amount %q failed to re-parse", meta.IssuanceAmount)
	}
	amountFromTypedData, ok := new(big.Int).SetString(td.Message.Amount, 10)
	if !ok {
		return fmt.Errorf("internal error: typed-data amount %q failed to re-parse", td.Message.Amount)
	}
	if amountFromMetadata.Cmp(amountFromTypedData) != 0 {
		return fmt.Errorf("metadata issuance.amount %s does not equal typed-data amount %s", meta.IssuanceAmount, td.Message.Amount)
	}

	var recordKey [32]byte
	if td.PrimaryType == "MintAttestation" {
		if td.Message.RecordID != meta.RecordID {
			return fmt.Errorf("typed-data.json message.recordId %q does not match metadata.json recordId %q", td.Message.RecordID, meta.RecordID)
		}
		recordKey = attestation.RecordKey(meta.RecordID)
		if "0x"+hex.EncodeToString(recordKey[:]) != strings.ToLower(td.Message.RecordKey) {
			return fmt.Errorf("recomputed recordKey does not match typed-data.json message.recordKey")
		}
	}

	// Each of these was once an optional --expect-* flag, so omitting them all
	// meant the signer would sign using only values the .rwa package itself
	// supplied. --policy is mandatory now (see above), so these checks always
	// run against an independently provisioned trust root, never against
	// anything read from the package.
	if pol.ChainID != td.Domain.ChainID.String() {
		return fmt.Errorf("typed-data chainId %s does not match policy chainId %s", td.Domain.ChainID.String(), pol.ChainID)
	}
	if !strings.EqualFold(pol.Controller, td.Domain.VerifyingContract) {
		return fmt.Errorf("typed-data verifyingContract %s does not match policy controller %s", td.Domain.VerifyingContract, pol.Controller)
	}
	if !strings.EqualFold(pol.Vault, td.Message.Vault) {
		return fmt.Errorf("typed-data vault %s does not match policy vault %s", td.Message.Vault, pol.Vault)
	}
	if !strings.EqualFold(pol.Auditor, td.Message.Auditor) {
		return fmt.Errorf("typed-data auditor %s does not match policy auditor %s", td.Message.Auditor, pol.Auditor)
	}

	// Enforce now < validUntil AND a documented maximum horizon, rather than
	// merely displaying validUntil for the auditor to eyeball. The ceiling is
	// the MORE RESTRICTIVE of "now + max lifetime" and "metadata.createdAt +
	// max lifetime": metadata.createdAt is itself package-supplied
	// (untrusted), so bounding only against it would let a forged far-future
	// createdAt excuse an equally far-future validUntil. Bounding against
	// wall-clock "now" as well closes that gap regardless of what createdAt
	// claims.
	createdAt, err := time.Parse(time.RFC3339, meta.CreatedAt)
	if err != nil {
		// metadata.Validate already enforces RFC3339, so this should be
		// unreachable, but never trust a re-derivation to silently
		// succeed against unvalidated input.
		return fmt.Errorf("internal error: metadata.json createdAt %q failed to re-parse: %w", meta.CreatedAt, err)
	}
	now := time.Now().UTC()
	validUntil := time.Unix(int64(td.Message.ValidUntil), 0).UTC()
	if !validUntil.After(now) {
		return fmt.Errorf("typed-data validUntil %s is not after the current time %s -- refusing to sign a zero, already-expired, or non-future validUntil", validUntil.Format(time.RFC3339), now.Format(time.RFC3339))
	}
	maxValidUntil := now.Add(pol.MaxAttestationLifetime)
	if fromCreated := createdAt.Add(pol.MaxAttestationLifetime); fromCreated.Before(maxValidUntil) {
		maxValidUntil = fromCreated
	}
	if validUntil.After(maxValidUntil) {
		return fmt.Errorf("typed-data validUntil %s exceeds the maximum attestation lifetime of %s (max allowed: %s, the earlier of now+lifetime and metadata.createdAt+lifetime)", validUntil.Format(time.RFC3339), pol.MaxAttestationLifetime, maxValidUntil.Format(time.RFC3339))
	}

	domain := td.domain()
	var digest common.Hash
	switch td.PrimaryType {
	case "MintAttestation":
		digest, err = attestation.MintDigest(domain, td.mintAttestation())
	case "BurnAttestation":
		digest, err = attestation.BurnDigest(domain, td.burnAttestation())
	}
	if err != nil {
		return fmt.Errorf("computing EIP-712 digest: %w", err)
	}

	critical := []ui.Field{
		{Label: "primaryType", Value: td.PrimaryType},
		// Both project IDs and the unit/decimals/normalized amount are shown
		// here even though they are already enforced equal above -- an auditor
		// who trusts only the on-screen confirmation (not the CLI's exit code)
		// must still be able to see and independently confirm the binding this
		// signature authorizes.
		{Label: "profile.json projectId", Value: prof.ProjectID},
		{Label: "metadata.json projectId", Value: meta.ProjectID},
		{Label: "tokenUnit (profile) / issuance.unit (metadata)", Value: prof.TokenUnit},
		{Label: "tokenDecimals", Value: fmt.Sprintf("%d", prof.TokenDecimals)},
		{Label: "chainId", Value: td.Domain.ChainID.String()},
		{Label: "verifyingContract (SupplyController)", Value: td.Domain.VerifyingContract},
		{Label: "auditor", Value: td.Message.Auditor},
		{Label: "vault", Value: td.Message.Vault},
		{Label: "profileDigest", Value: td.Message.ProfileDigest},
		{Label: "metadataDigest", Value: td.Message.MetadataDigest},
		{Label: "amount (raw, smallest unit)", Value: td.Message.Amount},
		{Label: "amount (normalized)", Value: normalizedAmount(amountFromTypedData, prof.TokenDecimals) + " " + prof.TokenUnit},
		{Label: "nonce", Value: td.Message.Nonce},
		{Label: "validUntil (unix seconds)", Value: fmt.Sprintf("%d (%s, checked against policy: now < validUntil <= %s)", td.Message.ValidUntil, validUntil.Format(time.RFC3339), maxValidUntil.Format(time.RFC3339))},
		{Label: "EIP-712 digest", Value: digest.Hex()},
		// chainId/controller/vault/auditor/projectId/profileDigest are not
		// optional to display -- they were all just checked, hard-fail-closed,
		// against --policy above. Shown here so the human reviewer sees the
		// same independently-provisioned trust root the tool already enforced,
		// not just its verdict.
		{Label: "policy file", Value: f.policyPath},
	}
	if td.PrimaryType == "MintAttestation" {
		critical = append(critical,
			ui.Field{Label: "recordId", Value: td.Message.RecordID},
			ui.Field{Label: "recordKey", Value: td.Message.RecordKey},
		)
	} else {
		critical = append(critical, ui.Field{Label: "operationId", Value: td.Message.OperationID})
	}

	critical = append(critical, proofFields(meta, filesExceptManifest)...)

	displayFields := buildDisplayFields(prof, meta.Asset)

	// Render an unmistakable mint/burn banner before the field-by-field
	// review screen, so the action type is established up front rather than
	// buried as one row among many.
	ui.RenderActionBanner(os.Stdout, td.PrimaryType)
	ui.Render(os.Stdout, fmt.Sprintf("Review %s — %s", td.PrimaryType, pkgPath), critical, displayFields)

	if !f.yes {
		confirmed, err := ui.Confirm(os.Stdout, os.Stdin, "Sign this attestation")
		if err != nil {
			return err
		}
		if !confirmed {
			return fmt.Errorf("aborted: not confirmed")
		}
	}

	sig, auditorAddr, err := signDigest(digest, f)
	if err != nil {
		return err
	}

	declaredAuditor := common.HexToAddress(td.Message.Auditor)
	if auditorAddr != declaredAuditor {
		return fmt.Errorf("signing key address %s does not match typed-data auditor %s; refusing to write a signature that would not recover to the declared auditor", auditorAddr.Hex(), declaredAuditor.Hex())
	}

	result, err := output.New(auditorAddr.Hex(), td.PrimaryType, digest.Hex(), "0x"+hex.EncodeToString(sig))
	if err != nil {
		return fmt.Errorf("building signed-result.json: %w", err)
	}

	outPath := f.out
	if outPath == "" {
		outPath = filepath.Join(filepath.Dir(pkgPath), "signed-result.json")
	}
	if err := output.Write(outPath, result); err != nil {
		return fmt.Errorf("writing signed-result.json: %w", err)
	}

	fmt.Fprintf(os.Stdout, "\nSigned. Wrote %s\n", outPath)
	return nil
}

// signDigest signs digest using either a keystore key or a hardware
// adapter, per f, and returns the signature and the recovered signer
// address.
func signDigest(digest common.Hash, f signFlags) ([]byte, common.Address, error) {
	if f.hardware != "" {
		hw, err := hardware.Open(hardware.DeviceKind(f.hardware))
		if err != nil {
			return nil, common.Address{}, err
		}
		defer hw.Close()
		addr, err := hw.Address()
		if err != nil {
			return nil, common.Address{}, fmt.Errorf("hardware-wallet signing (%s) is not implemented in this V1 build (see internal/hardware): %w", f.hardware, err)
		}
		sig, err := hw.SignDigest(digest)
		if err != nil {
			return nil, common.Address{}, fmt.Errorf("hardware-wallet signing (%s): %w", f.hardware, err)
		}
		return sig, addr, nil
	}

	anchorPath := f.auditAnchor
	if anchorPath == "" {
		anchorPath = keystore.DefaultAnchorPath(f.keystorePath)
	}
	// Verify the WHOLE audit chain -- not just that the last append succeeded
	// -- before this process unlocks or signs anything against this keystore.
	// A broken or (if --audit-anchor points at genuinely separate media)
	// truncated log is itself evidence of tampering; signing anyway would
	// extend an audit trail that can no longer be trusted to reflect a
	// complete history.
	if err := keystore.VerifyAuditLogAnchored(f.keystorePath, anchorPath); err != nil {
		return nil, common.Address{}, fmt.Errorf("audit log integrity check failed, refusing to unlock or sign against this keystore: %w", err)
	}

	// A locked-out keystore must be rejected before any password is read or a
	// decrypt is even attempted -- checking after prompting would let an
	// operator (or a script) burn a real attempt against the file during the
	// lockout window.
	if err := keystore.CheckThrottle(f.keystorePath); err != nil {
		return nil, common.Address{}, err
	}

	// --password puts the keystore password in process listings (ps, /proc)
	// and shell history for the life of the shell session. It cannot be
	// zeroized -- by the time this process sees it, it is already a copy owned
	// by the OS/shell -- so it stays deprecated rather than removed outright
	// (some scripted deployments still depend on it), but every use is loudly
	// flagged.
	password := f.password
	if password != "" {
		fmt.Fprintln(os.Stderr, "signer: WARNING: --password exposes the keystore password via process listings and shell history; prefer --password-file or the interactive prompt. This flag is deprecated and may be removed in a future version.")
	}

	if password == "" && f.passwordFile != "" {
		if err := checkPasswordFilePermissions(f.passwordFile); err != nil {
			return nil, common.Address{}, err
		}
		data, err := os.ReadFile(f.passwordFile)
		if err != nil {
			return nil, common.Address{}, fmt.Errorf("reading --password-file: %w", err)
		}
		trimmed := bytes.TrimRight(data, "\r\n")
		password = string(trimmed)
		keystore.ZeroBytes(data) // data and trimmed share a backing array
	}
	if password == "" {
		pw, err := readPassword(os.Stdin)
		if err != nil {
			return nil, common.Address{}, fmt.Errorf("reading password: %w", err)
		}
		password = string(pw)
		keystore.ZeroBytes(pw)
	}

	priv, addr, err := keystore.Load(f.keystorePath, password)
	if err != nil {
		// Every failed unlock attempt counts toward the lockout and is
		// recorded in the tamper-evident audit log, including a KDF-policy or
		// file-permission rejection -- those are just as much a signal of an
		// attacker probing the file as a wrong password would be. This
		// particular pair (throttle/audit for a failed attempt) stays
		// warn-and-continue even in production: the substantive failure below
		// is already returned to the caller, so there is no "operation
		// succeeded but wasn't recorded" gap here -- the fail-closed rule is
		// about the success-path writes below, not this one.
		if throttleErr := keystore.RecordUnlockFailure(f.keystorePath); throttleErr != nil {
			fmt.Fprintln(os.Stderr, "signer: WARNING: failed to record unlock failure for throttling:", throttleErr)
		}
		if auditErr := keystore.AppendAuditEventTo(f.keystorePath, anchorPath, "unlock_failure", map[string]string{"reason": err.Error()}); auditErr != nil {
			fmt.Fprintln(os.Stderr, "signer: WARNING: failed to append unlock-failure audit event:", auditErr)
		}
		return nil, common.Address{}, fmt.Errorf("loading keystore: %w", err)
	}
	defer keystore.Zero(priv)

	// Fail closed in production for the writes on this success path. Unlike
	// the failure path above, silently continuing here means the sensitive
	// operation (unlock, then a signature) proceeds with no durable record of
	// it -- exactly the audit-trail-fails-open trap. --unsafe-test-mode is this
	// signer's existing, already-required marker for "restricted to scripted
	// test/CI use" (main.go's --yes gate), so it doubles as the fail-open
	// escape hatch for exactly that use case; every other invocation fails
	// closed.
	auditFailure := func(step string, err error) error {
		return fmt.Errorf("recording %s to the audit trail failed, refusing to continue without a durable record: %w", step, err)
	}

	if err := keystore.RecordUnlockSuccess(f.keystorePath); err != nil {
		if !f.unsafeTestMode {
			return nil, common.Address{}, auditFailure("throttle-clear", err)
		}
		fmt.Fprintln(os.Stderr, "signer: WARNING (--unsafe-test-mode): failed to clear unlock-throttle state:", err)
	}
	if err := keystore.AppendAuditEventTo(f.keystorePath, anchorPath, "unlock_success", map[string]string{"address": addr.Hex()}); err != nil {
		if !f.unsafeTestMode {
			return nil, common.Address{}, auditFailure("unlock-success", err)
		}
		fmt.Fprintln(os.Stderr, "signer: WARNING (--unsafe-test-mode): failed to append unlock-success audit event:", err)
	}

	sig, err := attestation.Sign(digest, priv)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("signing: %w", err)
	}
	if err := keystore.AppendAuditEventTo(f.keystorePath, anchorPath, "signed", map[string]string{"address": addr.Hex(), "digest": digest.Hex()}); err != nil {
		if !f.unsafeTestMode {
			// The signature was computed but is deliberately discarded here:
			// per this signer's existing invariant ("any failure at any step
			// exits non-zero and writes nothing," README), a signature that
			// cannot be durably recorded as having been produced must never
			// reach output.Write.
			return nil, common.Address{}, auditFailure("signed", err)
		}
		fmt.Fprintln(os.Stderr, "signer: WARNING (--unsafe-test-mode): failed to append signed audit event:", err)
	}
	return sig, addr, nil
}

// checkPasswordFilePermissions rejects a --password-file that is group- or
// world-readable: such a file leaks the keystore password to any other local
// account, defeating the point of using a file instead of a command-line
// argument.
func checkPasswordFilePermissions(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("stat --password-file %s: %w", path, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("--password-file %s is group- or world-readable (mode %s); refusing to read it -- run: chmod 600 %s", path, info.Mode().Perm(), path)
	}
	return nil
}

// readPassword reads a keystore password from stdin, suppressing terminal
// echo via golang.org/x/term when stdin is an interactive terminal.
func readPassword(stdin *os.File) ([]byte, error) {
	return readPasswordPrompt(stdin, newStdinLineReader(stdin), "Keystore password: ")
}

// stdinLineReader wraps a single, shared bufio.Reader over stdin for the
// non-terminal fallback path. It must be created once and reused across
// every readPasswordPrompt call in a process: bufio.Reader reads ahead in
// chunks, so creating a fresh one per call (as an earlier version of this
// function did) can silently swallow a second piped line into a buffer
// that gets discarded when that call returns -- exactly the failure mode
// that would break "signer keystore create"'s two-prompt (password +
// confirmation) piped-stdin tests.
func newStdinLineReader(stdin *os.File) *bufio.Reader {
	return bufio.NewReader(stdin)
}

// readPasswordPrompt is readPassword generalized to a caller-supplied
// prompt and a shared line reader, so "signer keystore create"/"import" can
// ask for a new password twice (with distinct prompts) against the same
// underlying stdin stream. When stdin is not a terminal -- piped input
// from a script or test harness -- there is no echo to suppress, so it
// falls back to a plain line read via br; term.ReadPassword itself would
// simply fail on a non-tty file descriptor.
func readPasswordPrompt(stdin *os.File, br *bufio.Reader, prompt string) ([]byte, error) {
	fmt.Fprint(os.Stdout, prompt)
	if term.IsTerminal(int(stdin.Fd())) {
		pw, err := term.ReadPassword(int(stdin.Fd()))
		fmt.Fprintln(os.Stdout) // ReadPassword swallows the operator's Enter keystroke
		return pw, err
	}
	line, err := br.ReadString('\n')
	if err != nil && err != io.EOF {
		return nil, err
	}
	return []byte(strings.TrimRight(line, "\r\n")), nil
}
