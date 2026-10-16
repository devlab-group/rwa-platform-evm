// Package serverwiring shares chain clients, transaction managers and event decoders
// between cmd/platform and cmd/opsctl, keeping recovery chain checks, nonce
// coordination, fee caps and typed dispatch consistent
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
	"github.com/rwa-platform/server/internal/income"
	"github.com/rwa-platform/server/internal/indexer"
	"github.com/rwa-platform/server/internal/redemption"
	"github.com/rwa-platform/server/internal/sales"
	"github.com/rwa-platform/server/internal/strategy"
	"github.com/rwa-platform/server/internal/token"
)

// FeeCaps converts cfg's validated, non-negative base-10 wei strings to
// blockchain.FeeCaps; empty strings mean no cap
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

// IndexerAddresses lists configured compliance, supply controller, Vault, escrow,
// token, strategy, income distributor (rwa-v3) and factory addresses, skipping unset
// values. The token emits transfer and enforcement/governance events; a Fixed strategy
// emits price updates; the income distributor emits distribution events. Watching the
// startup-configured factory allows wallet-deployment adoption before child addresses
// exist.
func IndexerAddresses(addrs models.Addresses, factoryAddr string) []common.Address {
	var out []common.Address
	for _, a := range []string{addrs.Compliance, addrs.SupplyController, addrs.Vault, addrs.RedemptionEscrow, addrs.Token, addrs.Strategy, addrs.IncomeDistributor, factoryAddr} {
		if a != "" {
			out = append(out, common.HexToAddress(a))
		}
	}
	return out
}

// ErrWrongChain is returned when the RPC endpoint reports a chain ID other than
// the configured CHAIN_ID
var ErrWrongChain = fmt.Errorf("serverwiring: RPC endpoint is on the wrong chain")

// DialChain connects to cfg.ChainRPCURL, requires cfg.ChainID and closes on mismatch or
// RPC failure. cmd/platform and chain-touching opsctl commands use it so indexed events
// and signed transactions target the configured chain.
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

// CheckChainID returns ErrWrongChain on an ID mismatch or propagates RPC errors;
// separating it from DialChain allows fake-client tests
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

// NewTxManager uses cfg.TxCoordinationMode: "in-process" has no nonce lease;
// "mongo-lease" coordinates replicas and opsctl `tx replace` via repos.NonceLeases.
// Both apply configured fee caps and DefaultMaxReplacementAttempts.
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

// BuildDecoder dispatches by configured contract address for cmd/platform and opsctl.
// Child-contract decoders fall back to shared governance decoding only on "unknown";
// errors propagate. Governance covers role/admin transfers and token pause/unpause.
//
// Unknown child events remain "unknown". Unrecognized factory events and logs from
// unconfigured addresses use generic topic0 names ("unknown" without topics). DLQ
// retries share the live decoder's typing.
func BuildDecoder(addrs models.Addresses, factoryAddr string) indexer.EventDecoder {
	complianceAddr := strings.ToLower(addrs.Compliance)
	controllerAddr := strings.ToLower(addrs.SupplyController)
	vaultAddr := strings.ToLower(addrs.Vault)
	escrowAddr := strings.ToLower(addrs.RedemptionEscrow)
	strategyAddr := strings.ToLower(addrs.Strategy)
	tokenAddr := strings.ToLower(addrs.Token)
	incomeAddr := strings.ToLower(addrs.IncomeDistributor)
	factory := strings.ToLower(factoryAddr)

	// withGovernance tries governance only when primary returns "unknown" without error
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
		case incomeAddr:
			if incomeAddr != "" {
				return withGovernance(income.DecodeLog)(log)
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

// factoryBindings holds both parsed factory ABIs for decodeProjectDeployed
var factoryBindings = bindings.NewFactory()

// decodeProjectDeployed types the factory event of either generation for
// project.ReconcileDeployment: 0x-hex projectId/profileDigest, the deployed addresses
// (incomeDistributor for rwa-v3 only) and version. Other factory logs use generic decoding.
func decodeProjectDeployed(log types.Log) (string, map[string]any, error) {
	f := factoryBindings
	if len(log.Topics) == 0 || !f.IsProjectDeployed(log.Topics[0]) {
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
	data := map[string]any{
		"projectId": ev.ProjectID.Hex(), "profileDigest": ev.ProfileDigest.Hex(),
		"token": ev.Token.Hex(), "compliance": ev.Compliance.Hex(),
		"supplyController": ev.SupplyController.Hex(), "vault": ev.Vault.Hex(),
		"redemptionEscrow": ev.RedemptionEscrow.Hex(), "strategy": ev.Strategy.Hex(),
		"version": ev.Version,
	}
	if ev.Layout == bindings.FactoryVersionV3 {
		data["incomeDistributor"] = ev.IncomeDistributor.Hex()
	}
	return "ProjectDeployed", data, nil
}

// complianceEventNames lists compliance.DecodeLog outputs; internal/compliance has no
// EventNames variable
var complianceEventNames = []string{"StatusChanged"}

// KnownEventNames lists typed events for configured contracts; empty means only generic
// decoding is available. Any child enables role/admin-transfer events; only the token
// enables Paused/Unpaused.
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
	add(addrs.IncomeDistributor, income.EventNames)
	add(addrs.Token, append([]string{"Paused", "Unpaused"}, token.EventNames...))
	// Every child can emit role and two-step admin-transfer events
	for _, a := range []string{addrs.Compliance, addrs.SupplyController, addrs.Vault, addrs.RedemptionEscrow, addrs.Strategy, addrs.Token, addrs.IncomeDistributor} {
		add(a, []string{"RoleGranted", "RoleRevoked", "DefaultAdminTransferScheduled", "DefaultAdminTransferCanceled"})
	}
	return set
}

// ErrGenericDecode means StrictDecoder found no known typed event name for the
// configured contracts
var ErrGenericDecode = fmt.Errorf("serverwiring: log did not decode to a known typed contract event")

// StrictDecoder wraps BuildDecoder, rejecting names outside KnownEventNames. opsctl
// uses it for DLQ retry: RetryDeadLetter re-records decode failures instead of
// resolving them, keeping generic events out of typed-projector recovery.
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

// IndexerUnsafe reads chainID's ReconciliationRequired checkpoint flag; opsctl refuses
// mutations while it is set after a deep reorg. A missing checkpoint returns false
// (normal before polling); repository errors propagate.
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
