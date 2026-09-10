// Package serverwiring holds the chain-client, transaction-manager, and
// event-decoder construction shared by BOTH the platform server
// (cmd/platform) and the recovery CLI (cmd/opsctl), so a privileged recovery
// action taken from opsctl uses the EXACT same chain-safety, nonce
// coordination, and typed event dispatch the running server does. Before
// this package these were built independently in each
// command's package main: opsctl dialed the RPC without checking its chain
// ID, always constructed a lease-bypassing TxManager, and passed a nil
// decoder that fell back to a generic topic-only decode — so a DLQ retry
// could resolve a typed-decode failure with a meaningless generic event, and
// a `tx replace` could race a live replica on the same nonce or run against
// the wrong chain entirely.
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

// FeeCaps builds blockchain.FeeCaps from cfg's wei-string fields. config.Load
// already validated they parse as non-negative base-10 integers when
// non-empty, so SetString's ok result is only re-checked defensively here.
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

// IndexerAddresses is the ordered set of addresses the indexer scans — the
// deployed project's contracts (compliance, supply controller, vault,
// redemption escrow, token for Pausable pause/unpause, strategy for price
// updates) PLUS the factory (from bootstrap config, not the Project record)
// whose ProjectDeployed event the deployment projector adopts. Any unset
// address is skipped. The factory address is known from config at startup,
// unlike the deployed addresses which only exist once RWAFactory.deploy runs,
// so watching it is what lets the server OBSERVE a wallet-broadcast deployment.
func IndexerAddresses(addrs models.Addresses, factoryAddr string) []common.Address {
	var out []common.Address
	for _, a := range []string{addrs.Compliance, addrs.SupplyController, addrs.Vault, addrs.RedemptionEscrow, addrs.Token, addrs.Strategy, factoryAddr} {
		if a != "" {
			out = append(out, common.HexToAddress(a))
		}
	}
	return out
}

// ErrWrongChain is returned by DialChain when the RPC endpoint reports a
// chain ID other than the configured CHAIN_ID.
var ErrWrongChain = fmt.Errorf("serverwiring: RPC endpoint is on the wrong chain")

// DialChain connects cfg.ChainRPCURL and verifies the RPC-reported chain ID
// equals cfg.ChainID BEFORE returning the client. The
// configured CHAIN_ID is what transactions are signed against and the
// namespace the indexer labels logs under; an endpoint on a different chain
// would sign for / query the wrong network while otherwise looking healthy.
// A mismatch (or a dial/ChainID RPC failure) closes any opened connection and
// returns an error rather than handing back a client the caller might mutate
// state through. Every chain-touching opsctl command goes through this, so
// none can act against the wrong chain.
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

// CheckChainID verifies that client's RPC-reported chain ID equals
// wantChainID, returning ErrWrongChain on a mismatch. Split
// out from DialChain so the exact safety condition every chain-touching
// command relies on is unit-testable against a fake client.
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

// NewTxManager builds the TxManager per cfg.TxCoordinationMode, identically in
// both binaries: "in-process" (default) constructs it with no
// nonce lease, while "mongo-lease" wires repos.NonceLeases so concurrent
// replicas (and now opsctl's own `tx replace`) coordinate through a
// distributed, fenced lease instead of racing the same signer/nonce. Both
// paths honor the configured fee caps and DefaultMaxReplacementAttempts
// replacement policy.
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

// BuildDecoder dispatches each log to the right contract's typed decoder by
// address (compliance / supply-controller / vault / redemption-escrow /
// strategy / token), so the indexer populates every read model. Because every
// project contract can also emit the generic OZ governance events
// (RoleGranted/RoleRevoked, and the token's Paused/Unpaused), each
// contract-specific decoder is wrapped so a log its own switch returns as
// "unknown" falls back to the shared governance decoder before the final
// generic topic0-only decode. The token's own decoder covers its ERC-7943
// enforcement events, with Paused/Unpaused and the role events reached through
// that same governance fallback. This is the
// SAME production dispatch the running platform uses; the recovery CLI must
// use it too, or a DLQ retry would replay through a generic decoder that never
// reaches the typed projectors.
func BuildDecoder(addrs models.Addresses, factoryAddr string) indexer.EventDecoder {
	complianceAddr := strings.ToLower(addrs.Compliance)
	controllerAddr := strings.ToLower(addrs.SupplyController)
	vaultAddr := strings.ToLower(addrs.Vault)
	escrowAddr := strings.ToLower(addrs.RedemptionEscrow)
	strategyAddr := strings.ToLower(addrs.Strategy)
	tokenAddr := strings.ToLower(addrs.Token)
	factory := strings.ToLower(factoryAddr)

	// withGovernance runs a contract-specific decoder first and, only when it
	// returns "unknown" (its own switch didn't recognize the topic0), retries
	// through the shared governance decoder — so a RoleGranted/RoleRevoked on
	// any contract is typed rather than falling through to the generic bucket.
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

// decodeProjectDeployed types the factory's ProjectDeployed event into a
// ChainEvent the deployment projector (project.ReconcileDeployment) consumes.
// projectId/profileDigest are emitted as 0x-hex so the projector can match
// them against the stored profile's derived values; the six deployed addresses
// and version round out the data. The factory emits no other event the server
// tracks, so anything else routes to the generic bucket.
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

// complianceEventNames are the ComplianceRegistry event names
// compliance.DecodeLog produces. Unlike the other workflow packages,
// internal/compliance exposes no EventNames var, so the single known name is
// listed here (kept next to the other KnownEventNames sources so a future
// addition is obvious).
var complianceEventNames = []string{"StatusChanged"}

// KnownEventNames is the set of typed event names the deployed contracts can
// produce — the union across whichever addresses are set. An
// empty result means NO typed contract is configured, i.e. only the generic
// decoder is available. Every project contract can also emit the generic OZ
// governance events (RoleGranted/RoleRevoked), so those are known whenever any
// contract address is set; the token additionally emits Paused/Unpaused.
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
	// RoleGranted/RoleRevoked and the two-step DEFAULT_ADMIN transfer events
	// can appear on any project contract.
	for _, a := range []string{addrs.Compliance, addrs.SupplyController, addrs.Vault, addrs.RedemptionEscrow, addrs.Strategy, addrs.Token} {
		add(a, []string{"RoleGranted", "RoleRevoked", "DefaultAdminTransferScheduled", "DefaultAdminTransferCanceled"})
	}
	return set
}

// ErrGenericDecode is returned by StrictDecoder for a log that BuildDecoder
// could only resolve to a generic/topic-only event — i.e. NOT one of the
// configured contracts' known typed events.
var ErrGenericDecode = fmt.Errorf("serverwiring: log did not decode to a known typed contract event")

// StrictDecoder wraps BuildDecoder so that a log which only decodes to a
// generic/topic-only result returns an ERROR instead of a nameless event
// result. opsctl uses this for DLQ retry: because indexer.
// RetryDeadLetter re-records (does NOT resolve) a DLQ entry whose decode
// errors, this makes "the decoded event name must be one of the configured
// contract's known events" a precondition for resolving the entry — without
// touching indexer.go. A DLQ entry that still cannot be typed stays in the
// queue for further investigation rather than being resolved with a
// meaningless generic event that never reaches a typed projector.
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

// IndexerUnsafe reports whether chainID's indexer checkpoint is currently in
// ReconciliationRequired state — a reorg deeper than the automatic-recovery
// policy left the chain-derived read models frozen mid-divergence. opsctl
// consults this to refuse chain mutations while the platform's indexer is
// unsafe, except for the specific recovery command that exists to clear that
// state. No checkpoint yet is NOT unsafe — that is
// the normal pre-first-poll state, not a divergence.
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
