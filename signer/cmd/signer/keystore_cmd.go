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

// usageKeystore documents create/import, which write NativeFormat
// (Argon2id/AES-256-GCM) and enforce keystore.ValidatePassword without printing private
// keys; no export subcommand exists
func usageKeystore() {
	fmt.Fprintln(os.Stderr, `Usage: signer keystore create --out <path> [flags]
       signer keystore import --privkey-file <path> --out <path> [flags]

create   generate a brand-new key and write it as an encrypted keystore file.
import   encrypt an existing private key (read from an owner-only-readable
         hex file) into a new keystore file.

Both write this signer's own Argon2id + AES-256-GCM keystore format
("rwa-argon2id-v1") and enforce this signer's minimum password policy on
the new password. There is no "export" subcommand; the signer uses the key only
to sign digests with "signer sign".

Flags (both subcommands):
  --out <path>            REQUIRED. output keystore file path. Refuses to
                           overwrite an existing file.
  --password-file <path>  read the new password from a file instead of an
                           interactive double-entry prompt (scripted/CI use
                           only). Must not be group- or world-readable.

import-only flags:
  --privkey-file <path>   REQUIRED for import. File containing the raw hex
                           private key to import (with or without a "0x"
                           prefix). Must not be group- or world-readable.
                           The buffer it is read into is zeroed once the key
                           text is extracted, and the key is never printed,
                           logged, or written anywhere else.`)
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
	// Read private keys from owner-only files, like --password-file; CLI arguments expose
	// them in process listings and shell history
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

// writeNewKeystore reads a password, enforces keystore.ValidatePassword, encrypts priv
// with CreateNative and writes f.out without printing secrets
func writeNewKeystore(priv *ecdsa.PrivateKey, f keystoreCreateFlags) error {
	password, err := readNewPassword(f.passwordFile)
	if err != nil {
		return err
	}
	defer keystore.ZeroBytes(password)

	// Validate before CreateNative for a clear error before Argon2id work
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

// createKeystoreFileExclusive uses O_CREATE|O_EXCL to reject existing entries,
// including dangling symlinks, without a check-then-write race. It requires a regular
// file, sets 0600 and syncs data.
func createKeystoreFileExclusive(out string, data []byte) error {
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return fmt.Errorf("--out %s already exists (or is a symlink); refusing to overwrite or follow it", out)
		}
		return fmt.Errorf("creating %s: %w", out, err)
	}
	// O_EXCL already ensures a new file; verify its type too
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
	// Set 0600 explicitly because umask affects OpenFile's mode
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

// readNewPassword reads --password-file or requires two matching prompt entries to
// avoid locking the operator out through a typo
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
