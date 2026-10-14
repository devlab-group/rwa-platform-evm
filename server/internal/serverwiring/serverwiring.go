// Package serverwiring builds the chain client, transaction manager and event
// decoder shared by the platform server (cmd/platform) and the recovery CLI
// (cmd/opsctl), so a recovery action from opsctl uses the same chain check,
// nonce coordination and typed event dispatch as the running server
package serverwiring

import (
	"context"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/assets"
	"github.com/rwa-platform/server/internal/bindings"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/compliance"
	"github.com/rwa-platform/server/internal/config"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/governance"
	"github.com/rwa-platform/server/internal/indexer"
	"github.com/rwa-platform/server/internal/redemption"
	"github.com/rwa-platform/server/internal/sales"
	"github.com/rwa-platform/server/internal/strategy"
	"github.com/rwa-platform/server/internal/token"
)

// FeeCaps builds blockchain.FeeCaps from cfg's wei strings. An empty string
// means no cap. Config loading has already checked that a non-empty value is a
// non-negative base-10 integer.
func FeeCaps(cfg config.Config) blockchain.FeeCaps {
	parse := func(s string) *big.Int {
		if s == "" {
			return nil
		}
		n, ok := new(big.Int).SetString(s, 10)
		if !ok {
			return nil
		}
		return n
	}
	return blockchain.FeeCaps{
		MaxFeePerGas: parse(cfg.MaxFeePerGasWei),
		MaxTipPerGas: parse(cfg.MaxTipPerGasWei),
		MaxTotalCost: parse(cfg.MaxTxTotalCostWei),
	}
}

// IndexerAddresses returns the addresses the indexer scans: the project's
// compliance, supply controller, vault, redemption escrow, token (pause and
// unpause) and strategy (price updates) contracts, then the factory. Unset
// addresses are skipped. The factory address comes from config at startup,
// before any project contract exists, so watching its ProjectDeployed event
// lets the server observe a deployment broadcast from a wallet.
func IndexerAddresses(addrs models.Addresses, factoryAddr string) []common.Address {
	var out []common.Address
	for _, a := range []string{addrs.Compliance, addrs.SupplyController, addrs.Vault, addrs.RedemptionEscrow, addrs.Token, addrs.Strategy, factoryAddr} {
		if a != "" {
			out = append(out, common.HexToAddress(a))
		}
	}
	return out
}

// ErrWrongChain is returned when the RPC endpoint reports a chain ID other than
// the configured CHAIN_ID
var ErrWrongChain = fmt.Errorf("serverwiring: RPC endpoint is on the wrong chain")

// DialChain connects to cfg.ChainRPCURL and returns the client only if the
// endpoint reports cfg.ChainID, which is the chain transactions are signed for
// and the chain ID the indexer stores events under. On a mismatch or an RPC
// failure it closes the connection and returns an error. The platform and the
// chain-touching opsctl commands connect through it.
func DialChain(ctx context.Context, cfg config.Config) (*blockchain.RPCClient, error) {
	if cfg.ChainRPCURL == "" {
		return nil, fmt.Errorf("serverwiring: CHAIN_RPC_URL is not configured")
	}
	client, err := blockchain.Dial(ctx, cfg.ChainRPCURL)
	if err != nil {
		return nil, err
	}
	if err := CheckChainID(ctx, client, cfg.ChainID); err != nil {
		client.Close()
		return nil, err
	}
	return client, nil
}

// CheckChainID returns ErrWrongChain unless client reports wantChainID. DialChain
// calls it, and it is separate so tests can run it against a fake client.
func CheckChainID(ctx context.Context, client blockchain.Client, wantChainID int64) error {
	id, err := client.ChainID(ctx)
	if err != nil {
		return fmt.Errorf("serverwiring: chain ID check: %w", err)
	}
	if id == nil || id.Int64() != wantChainID {
		return fmt.Errorf("%w: RPC reports chainId %v but CHAIN_ID=%d is configured", ErrWrongChain, id, wantChainID)
	}
	return nil
}

// NewTxManager builds the TxManager for cfg.TxCoordinationMode. "in-process"
// uses no nonce lease. "mongo-lease" uses repos.NonceLeases, so replicas and
// opsctl's `tx replace` coordinate through a shared lease instead of racing on
// a nonce. Both apply the configured fee caps and DefaultMaxReplacementAttempts.
func NewTxManager(cfg config.Config, chainClient blockchain.Client, repos *repository.Repositories) blockchain.TxManager {
	chainID := big.NewInt(cfg.ChainID)
	feeMode := blockchain.FeeMode(cfg.FeeMode)
	feeCaps := FeeCaps(cfg)
	if cfg.TxCoordinationMode == "mongo-lease" {
		return blockchain.NewTxManagerWithLease(chainClient, repos.Transactions, chainID, feeMode, feeCaps,
			blockchain.DefaultMaxReplacementAttempts, repos.NonceLeases, cfg.TxLeaseTTL)
	}
	return blockchain.NewTxManagerWithFeeCaps(chainClient, repos.Transactions, chainID, feeMode, feeCaps)
}

// BuildDecoder dispatches each log by address to the typed decoder for the
// compliance, supply controller, vault, redemption escrow, strategy, token or
// factory contract. Every project contract can emit the OpenZeppelin governance
// events (role grants and revocations, admin transfers, and the token's
// Paused and Unpaused), so a log that a contract's own decoder reports as
// "unknown" is retried with the shared governance decoder. A log that still has
// no match, or comes from an unconfigured address, becomes a generic event named
// by its topic0. The platform and opsctl both use this decoder, so a DLQ retry
// types events the same way the live indexer does.
func BuildDecoder(addrs models.Addresses, factoryAddr string) indexer.EventDecoder {
	complianceAddr := strings.ToLower(addrs.Compliance)
	controllerAddr := strings.ToLower(addrs.SupplyController)
	vaultAddr := strings.ToLower(addrs.Vault)
	escrowAddr := strings.ToLower(addrs.RedemptionEscrow)
	strategyAddr := strings.ToLower(addrs.Strategy)
	tokenAddr := strings.ToLower(addrs.Token)
	factory := strings.ToLower(factoryAddr)

	// withGovernance runs primary first and retries with the governance decoder
	// only when primary returns "unknown"
	withGovernance := func(primary indexer.EventDecoder) indexer.EventDecoder {
		return func(log types.Log) (string, map[string]any, error) {
			name, data, err := primary(log)
			if err != nil || name != "unknown" {
				return name, data, err
			}
			return governance.DecodeLog(log)
		}
	}

	return func(log types.Log) (string, map[string]any, error) {
		switch strings.ToLower(log.Address.Hex()) {
		case complianceAddr:
			if complianceAddr != "" {
				return withGovernance(compliance.DecodeLog)(log)
			}
		case controllerAddr:
			if controllerAddr != "" {
				return withGovernance(assets.DecodeLog)(log)
			}
		case vaultAddr:
			if vaultAddr != "" {
				return withGovernance(sales.DecodeLog)(log)
			}
		case escrowAddr:
			if escrowAddr != "" {
				return withGovernance(redemption.DecodeLog)(log)
			}
		case strategyAddr:
			if strategyAddr != "" {
				return withGovernance(strategy.DecodeLog)(log)
			}
		case tokenAddr:
			if tokenAddr != "" {
				return withGovernance(token.DecodeLog)(log)
			}
		case factory:
			if factory != "" {
				return decodeProjectDeployed(log)
			}
		}
		name := "unknown"
		if len(log.Topics) > 0 {
			name = log.Topics[0].Hex()
		}
		return name, map[string]any{"data": "0x" + common.Bytes2Hex(log.Data)}, nil
	}
}

// decodeProjectDeployed types the factory's ProjectDeployed event for the
// deployment projector (project.ReconcileDeployment). projectId and
// profileDigest are 0x-prefixed hex so the projector can compare them with the
// stored profile, and the data also holds the six deployed addresses and the
// version. The server tracks no other factory event, so any other log becomes a
// generic event.
func decodeProjectDeployed(log types.Log) (string, map[string]any, error) {
	f := bindings.NewFactory()
	if len(log.Topics) == 0 || log.Topics[0] != f.EventID("ProjectDeployed") {
		name := "unknown"
		if len(log.Topics) > 0 {
			name = log.Topics[0].Hex()
		}
		return name, map[string]any{"data": "0x" + common.Bytes2Hex(log.Data)}, nil
	}
	ev, err := f.UnpackProjectDeployed(log.Data, log.Topics)
	if err != nil {
		return "", nil, err
	}
	return "ProjectDeployed", map[string]any{
		"projectId": ev.ProjectID.Hex(), "profileDigest": ev.ProfileDigest.Hex(),
		"token": ev.Token.Hex(), "compliance": ev.Compliance.Hex(),
		"supplyController": ev.SupplyController.Hex(), "vault": ev.Vault.Hex(),
		"redemptionEscrow": ev.RedemptionEscrow.Hex(), "strategy": ev.Strategy.Hex(),
		"version": ev.Version,
	}, nil
}

// complianceEventNames lists the event names compliance.DecodeLog produces.
// internal/compliance has no EventNames variable
var complianceEventNames = []string{"StatusChanged"}

// KnownEventNames returns the typed event names that the configured contracts
// can produce. An empty set means no typed contract is configured and only the
// generic decoder is available. The role and admin transfer events are known
// whenever any project contract address is set, and Paused and Unpaused are
// known for the token.
func KnownEventNames(addrs models.Addresses, factoryAddr string) map[string]bool {
	set := map[string]bool{}
	add := func(addr string, names []string) {
		if addr == "" {
			return
		}
		for _, n := range names {
			set[n] = true
		}
	}
	add(factoryAddr, []string{"ProjectDeployed"})
	add(addrs.Compliance, complianceEventNames)
	add(addrs.SupplyController, assets.EventNames)
	add(addrs.Vault, sales.EventNames)
	add(addrs.RedemptionEscrow, redemption.EventNames)
	add(addrs.Strategy, strategy.EventNames)
	add(addrs.Token, append([]string{"Paused", "Unpaused"}, token.EventNames...))
	// Role and two-step default admin transfer events can appear on any project contract
	for _, a := range []string{addrs.Compliance, addrs.SupplyController, addrs.Vault, addrs.RedemptionEscrow, addrs.Strategy, addrs.Token} {
		add(a, []string{"RoleGranted", "RoleRevoked", "DefaultAdminTransferScheduled", "DefaultAdminTransferCanceled"})
	}
	return set
}

// ErrGenericDecode is returned by StrictDecoder for a log that BuildDecoder
// decodes only to a generic event, so not to a known typed event of a
// configured contract
var ErrGenericDecode = fmt.Errorf("serverwiring: log did not decode to a known typed contract event")

// StrictDecoder wraps BuildDecoder and returns an error for a log that decodes
// only to a generic event. opsctl uses it for DLQ retry: because
// indexer.RetryDeadLetter re-records an entry whose decode fails instead of
// resolving it, an entry that cannot be typed stays in the queue and is not
// resolved with a generic event that no typed projector consumes.
func StrictDecoder(addrs models.Addresses, factoryAddr string) indexer.EventDecoder {
	base := BuildDecoder(addrs, factoryAddr)
	known := KnownEventNames(addrs, factoryAddr)
	return func(log types.Log) (string, map[string]any, error) {
		name, data, err := base(log)
		if err != nil {
			return name, data, err
		}
		if !known[name] {
			return name, data, fmt.Errorf("%w: %q (address %s)", ErrGenericDecode, name, log.Address.Hex())
		}
		return name, data, nil
	}
}

// IndexerUnsafe reports whether the indexer checkpoint for chainID is in
// ReconciliationRequired, which means a reorg deeper than automatic recovery
// handles has frozen the chain-derived read models. opsctl uses it to refuse
// mutations while that is the case. A missing checkpoint is not unsafe, because
// it is the normal state before the first poll.
func IndexerUnsafe(ctx context.Context, repos *repository.Repositories, chainID int64) (bool, error) {
	cp, err := repos.IndexerCheckpoints.Get(ctx, chainID, indexer.CheckpointAddress)
	if err != nil {
		if err == repository.ErrNotFound {
			return false, nil
		}
		return false, err
	}
	return cp.ReconciliationRequired, nil
}
