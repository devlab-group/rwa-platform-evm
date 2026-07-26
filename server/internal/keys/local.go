package keys

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ethereum/go-ethereum/accounts/keystore"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// localKeystoreProvider decrypts a password-encrypted go-ethereum keystore
// JSON file (the same format signer/internal/keystore reads, and what
// `cast wallet import` produces) once at Load, and again on Reload — the
// rotation story for this backend is "replace the file + password on
// disk, then call Reload (or restart)."
type localKeystoreProvider struct {
	path         string
	passwordFile string

	mu   sync.RWMutex
	key  *ecdsa.PrivateKey
	addr common.Address
}

func newLocalKeystoreProvider(role, dir, passwordFile string) (*localKeystoreProvider, error) {
	if dir == "" {
		return nil, fmt.Errorf("keys: local-keystore mode: KEYSTORE_DIR is required")
	}
	if passwordFile == "" {
		return nil, fmt.Errorf("keys: local-keystore mode: KEYSTORE_PASSWORD_FILE is required")
	}
	p := &localKeystoreProvider{path: filepath.Join(dir, role+".json"), passwordFile: passwordFile}
	if err := p.load(); err != nil {
		return nil, err
	}
	return p, nil
}

func (p *localKeystoreProvider) load() error {
	keyJSON, err := os.ReadFile(p.path)
	if err != nil {
		return fmt.Errorf("keys: local-keystore: reading %s: %w", p.path, err)
	}
	passwordRaw, err := os.ReadFile(p.passwordFile)
	if err != nil {
		return fmt.Errorf("keys: local-keystore: reading password file %s: %w", p.passwordFile, err)
	}
	password := strings.TrimRight(string(passwordRaw), "\r\n")

	decrypted, err := keystore.DecryptKey(keyJSON, password)
	if err != nil {
		// go-ethereum's error text does not include the password, so this
		// is safe to surface, but never wrap/log the password itself.
		return fmt.Errorf("keys: local-keystore: decrypting %s: %w", p.path, err)
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	p.key = decrypted.PrivateKey
	p.addr = decrypted.Address
	return nil
}

func (p *localKeystoreProvider) Address(context.Context) (common.Address, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.addr, nil
}

func (p *localKeystoreProvider) SignTx(_ context.Context, tx *types.Transaction, chainID *big.Int) (*types.Transaction, error) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return types.SignTx(tx, types.LatestSignerForChainID(chainID), p.key)
}

func (p *localKeystoreProvider) Reload(context.Context) error {
	return p.load()
}

// Close zeroes the decrypted in-memory private key. See Provider.Close's
// doc comment.
func (p *localKeystoreProvider) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	zeroECDSAKey(p.key)
	return nil
}
