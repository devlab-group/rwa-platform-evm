// Command signer reviews and signs .rwa audit packages offline, producing
// signed results or Safe owner shares without network requests
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
	case "combine":
		if err := runCombine(os.Args[2:]); err != nil {
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
       signer combine --policy <path> [--out <path>] <share.json>...
       signer keystore <create|import> --out <path> [flags]

Review and sign .rwa audit packages offline. Makes no network requests.

With a "safe" policy block, sign writes an owner's signature share, defaulting to
signature-share-<owner>.json. combine needs no key and assembles shares from at least
the policy's threshold of distinct owners into signed-result.json. It verifies the
shares, but does not repeat package or expiry checks.

Flags for sign:
  --keystore <path>          encrypted keystore (required unless --hardware); accepts
                              Ethereum V3 or the native Argon2id format, auto-detected
  --password <password>      DEPRECATED: visible in process listings and shell history.
                              Prefer the interactive prompt or --password-file.
  --password-file <path>     plaintext password file, with trailing newlines trimmed;
                              must allow no group or other access (chmod 600)
  --hardware <device>        ledger, trezor, yubikey or hsm; all integrations are stubs
                              and currently fail. Hardware signing is recommended
                              for production once an integration is available.
  --out <path>              result or Safe-share path; defaults to the package directory
  --policy <path>            REQUIRED: independent deployment policy (JSON): chainId,
                              controller, vault, auditor, projectId, profileDigest;
                              optional maxAttestationLifetimeHours and safe block.
                              Package values must match. See docs/auditor/auditor-guide.md.
  --yes                     skip confirmation (requires --unsafe-test-mode)
  --unsafe-test-mode         permit --yes and warn on successful-unlock/signature audit
                              or throttle-clear failures. For tests and CI only;
                              policy checks still run.
  --audit-anchor <path>      audit watermark, default: <keystore>.auditlog.anchor.jsonl.
                              Checked with the main log before unlocking. An intact
                              anchor detects deleted recent entries; protect it
                              independently from the main log. See the auditor guide §5.

Run "signer keystore --help" for key creation and import.`)
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
	// Read the leading package path before flags because flag.Parse stops at
	// the first positional argument
	if len(args) == 0 {
		usage()
		return fmt.Errorf("expected a <package.rwa> argument")
	}
	pkgPath := args[0]

	fs := flag.NewFlagSet("sign", flag.ContinueOnError)
	var f signFlags
	fs.StringVar(&f.keystorePath, "keystore", "", "encrypted Ethereum V3 or native Argon2id keystore file")
	fs.StringVar(&f.password, "password", "", "DEPRECATED, INSECURE: keystore password on the command line")
	fs.StringVar(&f.passwordFile, "password-file", "", "file containing the keystore password")
	fs.StringVar(&f.hardware, "hardware", "", "sign with a hardware-wallet adapter (ledger, trezor, yubikey, hsm)")
	fs.StringVar(&f.out, "out", "", "output path for signed-result.json or a Safe owner share")
	fs.StringVar(&f.policyPath, "policy", "", "REQUIRED: path to the independent deployment policy file")
	fs.BoolVar(&f.yes, "yes", false, "skip interactive confirmation (requires --unsafe-test-mode)")
	fs.BoolVar(&f.unsafeTestMode, "unsafe-test-mode", false, "permit --yes and warn on audit-write failures (tests and CI only)")
	fs.StringVar(&f.auditAnchor, "audit-anchor", "", "audit anchor path (default: <keystore>.auditlog.anchor.jsonl); protect independently from the main log; see docs/auditor/auditor-guide.md §5")
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
	// Skipping review requires an explicit test-mode flag; policy checks still apply
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
	// The manifest does not list itself in its file inventory
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

	// Bind the profile and metadata to the same project and unit;
	// the contract verifies hashes and signatures, not JSON semantics
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

	// Compare deployment values with the independent policy, never package-supplied defaults
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

	// Bound expiry relative to both now and metadata.createdAt so a forged
	// future creation time cannot extend the policy window
	createdAt, err := time.Parse(time.RFC3339, meta.CreatedAt)
	if err != nil {
		// Keep this conversion checked even though metadata.Validate requires RFC3339
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
	// Safe owners sign the Safe message hash wrapping the attestation digest
	signHash := digest
	if pol.Safe != nil {
		signHash, err = attestation.SafeMessageHash(domain.ChainID, common.HexToAddress(td.Message.Auditor), digest)
		if err != nil {
			return fmt.Errorf("computing the Safe message hash: %w", err)
		}
	}

	critical := []ui.Field{
		{Label: "primaryType", Value: td.PrimaryType},
		// Show the project and amount binding so the auditor can verify it independently
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
		// Display the policy path alongside the deployment values checked above
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
	if pol.Safe != nil {
		owners := make([]string, len(pol.Safe.Owners))
		for i, owner := range pol.Safe.Owners {
			owners[i] = owner.Hex()
		}
		critical = append(critical,
			ui.Field{Label: "auditor type", Value: fmt.Sprintf("Safe multisig, %d of %d owners (policy)", pol.Safe.Threshold, len(pol.Safe.Owners))},
			ui.Field{Label: "Safe owners (policy)", Value: strings.Join(owners, ", ")},
			ui.Field{Label: "Safe message hash (this key signs it)", Value: signHash.Hex()},
		)
	}

	critical = append(critical, proofFields(meta, filesExceptManifest)...)

	displayFields := buildDisplayFields(prof, meta.Asset)

	// Establish the mint or burn action before showing individual fields
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

	sig, signerAddr, err := signDigest(signHash, f)
	if err != nil {
		return err
	}

	declaredAuditor := common.HexToAddress(td.Message.Auditor)
	if pol.Safe != nil {
		if !pol.Safe.IsOwner(signerAddr) {
			return fmt.Errorf("signing key address %s is not an owner of the auditor Safe %s in the policy; refusing to write a share the Safe would reject", signerAddr.Hex(), declaredAuditor.Hex())
		}
		share, err := output.NewShare(declaredAuditor.Hex(), domain.ChainID.String(), signerAddr.Hex(), td.PrimaryType, digest.Hex(), signHash.Hex(), "0x"+hex.EncodeToString(sig))
		if err != nil {
			return fmt.Errorf("building the signature share: %w", err)
		}
		outPath := f.out
		if outPath == "" {
			outPath = filepath.Join(filepath.Dir(pkgPath), "signature-share-"+strings.ToLower(signerAddr.Hex())+".json")
		}
		if err := output.WriteShare(outPath, share); err != nil {
			return fmt.Errorf("writing the signature share: %w", err)
		}
		fmt.Fprintf(os.Stdout, "\nSigned as Safe owner %s. Wrote %s\nCollect shares from at least %d owners, then run: signer combine --policy <policy.json> <share.json>...\n", signerAddr.Hex(), outPath, pol.Safe.Threshold)
		return nil
	}
	if signerAddr != declaredAuditor {
		return fmt.Errorf("signing key address %s does not match typed-data auditor %s; refusing to write a signature that would not recover to the declared auditor", signerAddr.Hex(), declaredAuditor.Hex())
	}

	result, err := output.New(signerAddr.Hex(), td.PrimaryType, digest.Hex(), "0x"+hex.EncodeToString(sig))
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

// runCombine verifies Safe owner shares and combines them into signed-result.json.
// It checks policy membership, hashes and signatures without a key or the original package.
func runCombine(args []string) error {
	fs := flag.NewFlagSet("combine", flag.ContinueOnError)
	policyPath := fs.String("policy", "", "REQUIRED: policy file naming the auditor Safe, its owners and threshold")
	out := fs.String("out", "", "output path for signed-result.json (default: next to the first share)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	sharePaths := fs.Args()
	if *policyPath == "" {
		return fmt.Errorf("--policy is required")
	}
	if len(sharePaths) == 0 {
		return fmt.Errorf("expected one or more <share.json> arguments after the flags")
	}
	pol, err := policy.Load(*policyPath)
	if err != nil {
		return fmt.Errorf("loading --policy: %w", err)
	}
	if pol.Safe == nil {
		return fmt.Errorf("policy %s has no safe block; combine only applies to a Safe auditor", *policyPath)
	}
	safe := common.HexToAddress(pol.Auditor)
	chainID, _ := new(big.Int).SetString(pol.ChainID, 10) // policy.Load already checked it is decimal

	var first output.SignatureShare
	sigs := map[common.Address][]byte{}
	for i, path := range sharePaths {
		share, err := output.ReadShare(path)
		if err != nil {
			return err
		}
		if i == 0 {
			first = share
		}
		if common.HexToAddress(share.Safe) != safe || share.ChainID != pol.ChainID {
			return fmt.Errorf("%s: share is for Safe %s on chain %s, but the policy names Safe %s on chain %s", path, share.Safe, share.ChainID, safe.Hex(), pol.ChainID)
		}
		if share.PrimaryType != first.PrimaryType || !strings.EqualFold(share.TypedDataDigest, first.TypedDataDigest) {
			return fmt.Errorf("%s: share signs %s %s, but %s signs %s %s", path, share.PrimaryType, share.TypedDataDigest, sharePaths[0], first.PrimaryType, first.TypedDataDigest)
		}
		safeHash, err := attestation.SafeMessageHash(chainID, safe, common.HexToHash(share.TypedDataDigest))
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		if !strings.EqualFold(share.SafeMessageHash, safeHash.Hex()) {
			return fmt.Errorf("%s: safeMessageHash %s is not the Safe message hash %s of its digest", path, share.SafeMessageHash, safeHash.Hex())
		}
		sig, _ := hex.DecodeString(strings.TrimPrefix(share.Signature, "0x")) // ReadShare validated the hex
		signer, err := attestation.Recover(safeHash, sig)
		if err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
		owner := common.HexToAddress(share.Owner)
		if signer != owner {
			return fmt.Errorf("%s: signature recovers to %s, not the share's owner %s", path, signer.Hex(), owner.Hex())
		}
		if !pol.Safe.IsOwner(owner) {
			return fmt.Errorf("%s: %s is not an owner of the Safe in the policy", path, owner.Hex())
		}
		if _, dup := sigs[owner]; dup {
			return fmt.Errorf("%s: a second share from owner %s", path, owner.Hex())
		}
		sigs[owner] = sig
	}
	if len(sigs) < pol.Safe.Threshold {
		return fmt.Errorf("have shares from %d owner(s), but the Safe needs %d", len(sigs), pol.Safe.Threshold)
	}

	combined, err := attestation.CombineSafeSignatures(sigs)
	if err != nil {
		return err
	}
	result, err := output.New(safe.Hex(), first.PrimaryType, first.TypedDataDigest, "0x"+hex.EncodeToString(combined))
	if err != nil {
		return fmt.Errorf("building signed-result.json: %w", err)
	}
	outPath := *out
	if outPath == "" {
		outPath = filepath.Join(filepath.Dir(sharePaths[0]), "signed-result.json")
	}
	if err := output.Write(outPath, result); err != nil {
		return fmt.Errorf("writing signed-result.json: %w", err)
	}
	fmt.Fprintf(os.Stdout, "Combined %d owner signatures (threshold %d) for Safe %s. Wrote %s\n", len(sigs), pol.Safe.Threshold, safe.Hex(), outPath)
	return nil
}

// signDigest uses the selected keystore or hardware adapter to sign digest
// and returns the signature and signing key address
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
	// Verify the full chain and its anchor before unlocking or signing
	if err := keystore.VerifyAuditLogAnchored(f.keystorePath, anchorPath); err != nil {
		return nil, common.Address{}, fmt.Errorf("audit log integrity check failed, refusing to unlock or sign against this keystore: %w", err)
	}

	// Enforce lockout before prompting or attempting decryption
	if err := keystore.CheckThrottle(f.keystorePath); err != nil {
		return nil, common.Address{}, err
	}

	// Command-line passwords remain visible in process listings and shell history
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
		// Attempt to record every Load failure, including permission and KDF refusals.
		// Bookkeeping errors warn because the unlock has already failed.
		if throttleErr := keystore.RecordUnlockFailure(f.keystorePath); throttleErr != nil {
			fmt.Fprintln(os.Stderr, "signer: WARNING: failed to record unlock failure for throttling:", throttleErr)
		}
		if auditErr := keystore.AppendAuditEventTo(f.keystorePath, anchorPath, "unlock_failure", map[string]string{"reason": err.Error()}); auditErr != nil {
			fmt.Fprintln(os.Stderr, "signer: WARNING: failed to append unlock-failure audit event:", auditErr)
		}
		return nil, common.Address{}, fmt.Errorf("loading keystore: %w", err)
	}
	defer keystore.Zero(priv)

	// Success-path bookkeeping must complete before releasing a signature,
	// unless test mode explicitly permits warnings
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
			// Discard the signature if its audit entry cannot be durably recorded
			return nil, common.Address{}, auditFailure("signed", err)
		}
		fmt.Fprintln(os.Stderr, "signer: WARNING (--unsafe-test-mode): failed to append signed audit event:", err)
	}
	return sig, addr, nil
}

// checkPasswordFilePermissions requires owner-only access to the plaintext password
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

// readPassword reads from stdin and suppresses echo on a terminal
func readPassword(stdin *os.File) ([]byte, error) {
	return readPasswordPrompt(stdin, newStdinLineReader(stdin), "Keystore password: ")
}

// newStdinLineReader creates the shared reader for piped password prompts.
// Reuse it across prompts so read-ahead does not discard a later input line.
func newStdinLineReader(stdin *os.File) *bufio.Reader {
	return bufio.NewReader(stdin)
}

// readPasswordPrompt hides terminal input or reads a line from the shared
// buffer for piped stdin, where term.ReadPassword is unavailable
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
