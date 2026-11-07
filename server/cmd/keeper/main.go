// Command keeper settles forward-priced purchase orders on a schedule. Run it separately from
// the platform with its own config and a dedicated gas wallet; see keeper.example.yaml.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/ethereum/go-ethereum/common"

	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/keeper"
	"github.com/rwa-platform/server/internal/keys"
)

func main() {
	configPath := flag.String("config", "", "path to the keeper YAML file (required)")
	once := flag.Bool("once", false, "run one pass and exit, for cron")
	flag.Parse()
	if err := run(*configPath, *once); err != nil {
		log.Fatalf("keeper: %v", err)
	}
}

func run(configPath string, once bool) error {
	if configPath == "" {
		return errors.New("--config is required")
	}
	cfg, err := keeper.LoadConfig(configPath)
	if err != nil {
		return fmt.Errorf("config: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	client, err := blockchain.Dial(ctx, cfg.RPCURL)
	if err != nil {
		return fmt.Errorf("dial RPC: %w", err)
	}
	defer client.Close()
	chainID, err := client.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("read chain id: %w", err)
	}
	if chainID.Int64() != cfg.ChainID {
		return fmt.Errorf("chain_id is %d but the RPC endpoint is on %s", cfg.ChainID, chainID)
	}

	signer, err := keys.Load("keeper", cfg.KeyConfig())
	if err != nil {
		return fmt.Errorf("load key: %w", err)
	}
	if signer == nil {
		return errors.New("no key configured: set keys.keeper_key or another keys.provider_mode")
	}
	defer signer.Close()
	address, err := signer.Address(ctx)
	if err != nil {
		return fmt.Errorf("read key address: %w", err)
	}
	log.Printf("keeper: %s settling orders on Vault %s", address, cfg.VaultAddress)

	k := keeper.New(client, signer, chainID, common.HexToAddress(cfg.VaultAddress), cfg.FeeCaps)
	// Abort startup if the initial order pass fails
	if err := k.Pass(ctx); err != nil {
		return err
	}
	if once {
		return nil
	}
	k.Run(ctx, cfg.PollInterval)
	return nil
}
