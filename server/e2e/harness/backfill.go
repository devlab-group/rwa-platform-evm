package main

import (
	"context"
	"crypto/ecdsa"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/rwa-platform/server/internal/bindings"
)

// expectDeployLogsIndexed checks that backfill projects deployment-time logs. The factory
// marks the Vault Allowed before the indexer watches it, so that status must reach the
// wallet read model.
func expectDeployLogsIndexed(api *apiClient, cfg config, factory common.Address, deployTx common.Hash) error {
	if err := poll("the deploy transaction's StatusChanged in the wallet read model", 60*time.Second, 2*time.Second, func() (bool, error) {
		var wallets []walletStatus
		if err := api.getJSON("/api/v1/compliance/wallets", cfg.adminBearer, &wallets); err != nil {
			return false, err
		}
		for _, w := range wallets {
			if strings.EqualFold(w.Address, cfg.vaultAddr.Hex()) && w.Status == "Allowed" {
				return true, nil
			}
		}
		return false, nil
	}); err != nil {
		return err
	}
	return expectTxKinds(api, factory, map[string]string{deployTx.Hex(): "project_deployed"})
}

// replacementPrice differs from the deployed prices so the test can identify the update
const replacementPrice = 3_141_592

// useReplacementStrategy updates an unwatched strategy's price, then swaps the Vault to it.
// Backfill must recover the price update from before the swap.
func useReplacementStrategy(ctx context.Context, api *apiClient, chain *chainClient, cfg config, adminKey *ecdsa.PrivateKey) error {
	data, err := bindings.NewFixedPriceStrategy().PackSetPurchasePrice(big.NewInt(replacementPrice))
	if err != nil {
		return err
	}
	if _, err := chain.sendTx(ctx, adminKey, cfg.replacementStrategy, data); err != nil {
		return fmt.Errorf("submit setPurchasePrice: %w", err)
	}
	if data, err = bindings.NewVault().ABI.Pack("setStrategy", cfg.replacementStrategy); err != nil {
		return err
	}
	if _, err := chain.sendTx(ctx, adminKey, cfg.vaultAddr, data); err != nil {
		return fmt.Errorf("submit Vault.setStrategy: %w", err)
	}
	return poll("GET /project to report the price set before the swap", 120*time.Second, 3*time.Second, func() (bool, error) {
		var proj projectResponse
		if err := api.getJSON("/api/v1/project", "", &proj); err != nil {
			return false, err
		}
		if !strings.EqualFold(proj.Addresses.Strategy, cfg.replacementStrategy.Hex()) {
			return false, nil
		}
		if want := fmt.Sprint(replacementPrice); proj.PurchasePrice != want {
			return false, fmt.Errorf("purchasePricePerWholeToken = %q, want %s", proj.PurchasePrice, want)
		}
		return true, nil
	})
}

// adminDelayLifecycle schedules and cancels a Vault delay change, checking the decoded
// values against the scheduling block. Increasing the delay from zero takes effect after
// the new delay, capped by OpenZeppelin's five-day wait.
func adminDelayLifecycle(ctx context.Context, api *apiClient, chain *chainClient, cfg config, adminKey *ecdsa.PrivateKey) error {
	const adminDelay = 7200
	selector := func(signature string) []byte { return crypto.Keccak256([]byte(signature))[:4] }

	data := append(selector("changeDefaultAdminDelay(uint48)"), common.LeftPadBytes(big.NewInt(adminDelay).Bytes(), 32)...)
	scheduled, err := chain.sendTx(ctx, adminKey, cfg.vaultAddr, data)
	if err != nil {
		return fmt.Errorf("submit changeDefaultAdminDelay: %w", err)
	}
	header, err := chain.eth.HeaderByNumber(ctx, scheduled.BlockNumber)
	if err != nil {
		return err
	}
	wantEffect := header.Time + adminDelay
	if err := poll("GET /project to report the delay change on the vault", 60*time.Second, 3*time.Second, func() (bool, error) {
		var proj projectResponse
		if err := api.getJSON("/api/v1/project", "", &proj); err != nil {
			return false, err
		}
		for _, c := range proj.AdminDelayChanges {
			if c.Contract == "vault" {
				if c.NewDelay != adminDelay || c.EffectAt != wantEffect {
					return false, fmt.Errorf("vault change = %+v, want newDelay %d effectAt %d", c, adminDelay, wantEffect)
				}
				return true, nil
			}
		}
		return false, nil
	}); err != nil {
		return err
	}

	canceled, err := chain.sendTx(ctx, adminKey, cfg.vaultAddr, selector("rollbackDefaultAdminDelay()"))
	if err != nil {
		return fmt.Errorf("submit rollbackDefaultAdminDelay: %w", err)
	}
	if err := poll("GET /project to drop the cancelled delay change", 60*time.Second, 3*time.Second, func() (bool, error) {
		var proj projectResponse
		if err := api.getJSON("/api/v1/project", "", &proj); err != nil {
			return false, err
		}
		return len(proj.AdminDelayChanges) == 0, nil
	}); err != nil {
		return err
	}
	return expectTxKinds(api, cfg.vaultAddr, map[string]string{
		scheduled.TxHash.Hex(): "admin_delay_change_scheduled",
		canceled.TxHash.Hex():  "admin_delay_change_canceled",
	})
}
