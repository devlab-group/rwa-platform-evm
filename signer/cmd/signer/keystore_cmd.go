package main

import (
	"bytes"
	"crypto/ecdsa"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/rwa-platform/signer/internal/keystore"
)

// usageKeystore documents "signer keystore create"/"import". Both write this
// signer's own NativeFormat (Argon2id + AES-256-GCM, see
// internal/keystore/native.go), enforce keystore.ValidatePassword on the new
// password, and never print or export the private key -- there is
// deliberately no "keystore export" subcommand at all.
func usageKeystore() {
	fmt.Fprintln(os.Stderr, `Usage: signer keystore create --out <path> [flags]
       signer keystore import --privkey-file <path> --out <path> [flags]

create   generate a brand-new key and write it as an encrypted keystore file.
import   encrypt an existing private key (read from an owner-only-readable
         hex file) into a new keystore file.

Both write this signer's own Argon2id + AES-256-GCM keystore format
("rwa-argon2id-v1") and enforce this signer's minimum password policy on
the new password. There is no "export" subcommand: the private key never
leaves the keystore file except as a signature over a digest, via
"signer sign".

Flags (both subcommands):
  --out <path>            REQUIRED. output keystore file path. Refuses to
                           overwrite an existing file.
  --password-file <path>  read the new password from a file instead of an
                           interactive double-entry prompt (scripted/CI use
                           only). Must not be group- or world-readable.

import-only flags:
  --privkey-file <path>   REQUIRED for import. File containing the raw hex
                           private key to import (with or without a "0x"
                           prefix). Must not be group- or world-readable;
                           its contents are zeroed from memory as soon as
                           the key is parsed, and are never printed, logged,
                           or written anywhere else.`)
}

func runKeystoreCmd(args []string) error {
	if len(args) == 0 {
		usageKeystore()
		return fmt.Errorf("expected a keystore subcommand (create or import)")
	}
	switch args[0] {
	case "create":
		return runKeystoreCreate(args[1:])
	case "import":
		return runKeystoreImport(args[1:])
	case "-h", "--help", "help":
		usageKeystore()
		return nil
	default:
		usageKeystore()
		return fmt.Errorf("unknown keystore subcommand %q", args[0])
	}
}

type keystoreCreateFlags struct {
	out          string
	passwordFile string
	privkeyFile  string // import only
}

func parseKeystoreCreateFlags(name string, args []string, requirePrivkey bool) (keystoreCreateFlags, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	var f keystoreCreateFlags
	fs.StringVar(&f.out, "out", "", "output keystore file path")
	fs.StringVar(&f.passwordFile, "password-file", "", "file containing the new password")
	if requirePrivkey {
		fs.StringVar(&f.privkeyFile, "privkey-file", "", "file containing the raw hex private key to import")
	}
	if err := fs.Parse(args); err != nil {
		return f, err
	}
	if fs.NArg() != 0 {
		return f, fmt.Errorf("unexpected extra arguments: %v", fs.Args())
	}
	if f.out == "" {
		return f, fmt.Errorf("--out is required")
	}
	if requirePrivkey && f.privkeyFile == "" {
		return f, fmt.Errorf("--privkey-file is required for import")
	}
	return f, nil
}

func runKeystoreCreate(args []string) error {
	f, err := parseKeystoreCreateFlags("keystore create", args, false)
	if err != nil {
		return err
	}
	priv, err := crypto.GenerateKey()
	if err != nil {
		return fmt.Errorf("generating key: %w", err)
	}
	defer keystore.Zero(priv)
	return writeNewKeystore(priv, f)
}

func runKeystoreImport(args []string) error {
	f, err := parseKeystoreCreateFlags("keystore import", args, true)
	if err != nil {
		return err
	}
	// A private key may only be imported from a local, owner-only-readable
	// file -- the same permission policy as --password-file -- never as a CLI
	// argument, which would put it in process listings and shell history for
	// as long as those persist.
	if err := checkPasswordFilePermissions(f.privkeyFile); err != nil {
		return fmt.Errorf("--privkey-file: %w", err)
	}
	data, err := os.ReadFile(f.privkeyFile)
	if err != nil {
		return fmt.Errorf("reading --privkey-file: %w", err)
	}
	hexKey := strings.TrimPrefix(strings.TrimSpace(string(data)), "0x")
	keystore.ZeroBytes(data)

	priv, err := crypto.HexToECDSA(hexKey)
	if err != nil {
		return fmt.Errorf("--privkey-file does not contain a valid hex private key: %w", err)
	}
	defer keystore.Zero(priv)
	return writeNewKeystore(priv, f)
}

// writeNewKeystore prompts for (or reads from --password-file) the new
// keystore's password, enforces keystore.ValidatePassword on it, and
// writes priv encrypted with keystore.CreateNative to f.out. It never
// prints priv or the password.
func writeNewKeystore(priv *ecdsa.PrivateKey, f keystoreCreateFlags) error {
	password, err := readNewPassword(f.passwordFile)
	if err != nil {
		return err
	}
	defer keystore.ZeroBytes(password)

	// keystore.CreateNative re-validates internally too, but checking here
	// first gives a clean error before any Argon2id work is done.
	if err := keystore.ValidatePassword(string(password)); err != nil {
		return err
	}

	data, err := keystore.CreateNative(priv, string(password))
	if err != nil {
		return fmt.Errorf("encrypting keystore: %w", err)
	}
	if err := createKeystoreFileExclusive(f.out, data); err != nil {
		return err
	}

	addr := crypto.PubkeyToAddress(priv.PublicKey)
	fmt.Fprintf(os.Stdout, "Wrote keystore for address %s to %s\n", addr.Hex(), f.out)
	return nil
}

// createKeystoreFileExclusive writes data to out as a brand-new file,
// refusing to overwrite or follow anything already at that path. It creates
// the file with O_CREATE|O_EXCL and no-follow semantics, verifies it is a
// regular file, sets mode 0600, and durably syncs it.
//
// The previous implementation checked os.Stat(out) and only afterwards
// called os.WriteFile: between those two calls (or if out is a symlink,
// dangling or not), a concurrent process or a maliciously pre-planted
// symlink could cause the write to land somewhere other than a fresh file
// at out. os.O_CREATE|os.O_EXCL closes that window: the OS opens and
// creates the file atomically and fails with an "already exists" error if
// anything -- an existing file OR an existing symlink of any kind -- is
// already at that path, without ever following a symlink to write through
// it (POSIX open(2): O_EXCL with O_CREAT on a pathname that already names
// a symlink fails, it does not resolve the link and use its target).
func createKeystoreFileExclusive(out string, data []byte) error {
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("--out %s already exists (or is a symlink); refusing to overwrite or follow it", out)
		}
		return fmt.Errorf("creating %s: %w", out, err)
	}
	// f was just created by the O_EXCL open above, so nothing else could
	// have raced in to replace it with e.g. a device file in between; this
	// is a defensive re-assertion of that invariant, not the primary
	// protection (O_EXCL is).
	info, err := f.Stat()
	if err != nil {
		f.Close()
		os.Remove(out)
		return fmt.Errorf("stat %s: %w", out, err)
	}
	if !info.Mode().IsRegular() {
		f.Close()
		os.Remove(out)
		return fmt.Errorf("%s is not a regular file; refusing to write a keystore there", out)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(out)
		return fmt.Errorf("writing %s: %w", out, err)
	}
	// The file mode passed to OpenFile is subject to the process umask, so
	// re-assert 0600 explicitly rather than trust the umask happened to
	// produce it.
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		os.Remove(out)
		return fmt.Errorf("chmod %s: %w", out, err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(out)
		return fmt.Errorf("syncing %s: %w", out, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", out, err)
	}
	return nil
}

// readNewPassword reads the password for a newly created keystore, either
// from --password-file or by prompting twice interactively and requiring
// both entries to match (a plain single prompt has no way to catch a typo
// that then locks the operator out of a key nobody else can recover).
func readNewPassword(passwordFile string) ([]byte, error) {
	if passwordFile != "" {
		if err := checkPasswordFilePermissions(passwordFile); err != nil {
			return nil, err
		}
		data, err := os.ReadFile(passwordFile)
		if err != nil {
			return nil, fmt.Errorf("reading --password-file: %w", err)
		}
		trimmed := bytes.TrimRight(data, "\r\n")
		password := append([]byte(nil), trimmed...)
		keystore.ZeroBytes(data)
		return password, nil
	}

	br := newStdinLineReader(os.Stdin)
	pw1, err := readPasswordPrompt(os.Stdin, br, "New keystore password: ")
	if err != nil {
		return nil, fmt.Errorf("reading password: %w", err)
	}
	pw2, err := readPasswordPrompt(os.Stdin, br, "Confirm password: ")
	if err != nil {
		keystore.ZeroBytes(pw1)
		return nil, fmt.Errorf("reading password confirmation: %w", err)
	}
	if !bytes.Equal(pw1, pw2) {
		keystore.ZeroBytes(pw1)
		keystore.ZeroBytes(pw2)
		return nil, fmt.Errorf("passwords do not match")
	}
	keystore.ZeroBytes(pw2)
	return pw1, nil
}
