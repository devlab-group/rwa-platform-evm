// Command opsctl is the audited operator CLI. It's a CLI rather than a set
// of new HTTP routes specifically to avoid touching the frozen
// api/openapi.yaml contract.
//
// opsctl is NOT credential-gated: the old admin-key check was vacuous — the
// binary reads the platform's own --config file (chain RPC, Mongo URI/DB, hot
// keys), so whoever can run it already holds every secret the check would have
// compared against. Access control is therefore filesystem/host access to the
// config, exactly as for `platform` itself. Every subcommand still:
//   - connects to the SAME MongoDB (or in-memory store, for
//     PERSISTENCE_MODE=memory dev) the running platform server uses, so its
//     writes are immediately visible to it;
//   - records exactly one audit_logs entry per invocation (category "opsctl",
//     actor = "opsctl:"+an attributable label from --actor or the OS user),
//     whether it succeeds or fails — see commands.go's opsCtx.audited.
//
// Usage:
//
//	opsctl --config <path> [--actor <label>] tx replace --id=<txID> --role=<relayer|compliance> --bump-percent=<n>
//	opsctl --config <path> indexer reset-checkpoint --trusted-block=<n> --trusted-hash=0x...
//	opsctl --config <path> dlq list
//	opsctl --config <path> dlq inspect --id=<id>
//	opsctl --config <path> dlq retry --id=<id>
//	opsctl --config <path> dlq dismiss --id=<id>
//	opsctl --config <path> ipfs retry --id=<id> --file=<path>
//	opsctl --config <path> ipfs restore-local --id=<id>
//
// Every subcommand reads the platform's normal --config YAML file
// (internal/config), same as `platform`/`reindex` — chain RPC, Mongo
// URI/DB, contract addresses, key-provider mode, etc.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/user"
	"strings"
	"time"

	"github.com/rwa-platform/server/internal/auditlog"
	"github.com/rwa-platform/server/internal/blockchain"
	"github.com/rwa-platform/server/internal/config"
	"github.com/rwa-platform/server/internal/dal"
	"github.com/rwa-platform/server/internal/dal/memory"
	"github.com/rwa-platform/server/internal/dal/models"
	"github.com/rwa-platform/server/internal/dal/mongodb"
	"github.com/rwa-platform/server/internal/dal/repository"
	"github.com/rwa-platform/server/internal/indexer"
	"github.com/rwa-platform/server/internal/ipfs"
	"github.com/rwa-platform/server/internal/keys"
	"github.com/rwa-platform/server/internal/serverwiring"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	top := flag.NewFlagSet("opsctl", flag.ContinueOnError)
	configPath := top.String("config", "", "path to the YAML configuration file (required)")
	actorFlag := top.String("actor", "", "attributable label recorded as the audit actor for this invocation (defaults to the OS user, else \"opsctl\")")
	if err := top.Parse(args); err != nil {
		return 2
	}
	rest := top.Args()
	if len(rest) < 2 {
		printUsage()
		return 2
	}
	group, action, rest := rest[0], rest[1], rest[2:]

	if *configPath == "" {
		fmt.Fprintln(os.Stderr, "opsctl: --config <path> is required")
		return 2
	}
	cfg, err := config.LoadFile(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "opsctl: config: %v\n", err)
		return 1
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	repos, closeRepos, err := connectRepos(ctx, cfg)
	if err != nil {
		fmt.Fprintf(os.Stderr, "opsctl: connecting to storage: %v\n", err)
		return 1
	}
	defer closeRepos()

	o := opsCtx{
		ctx: ctx, repos: repos, audit: auditlog.New(repos.AuditLogs),
		actor: auditActor(*actorFlag), out: os.Stdout,
	}

	if err := dispatch(o, cfg, group, action, rest); err != nil {
		fmt.Fprintf(os.Stderr, "opsctl: %v\n", err)
		return 1
	}
	return 0
}

// auditActor derives the audit-actor label for an invocation. opsctl is not
// credential-gated (see the package doc), so this is purely for attribution,
// not authorization: an explicit --actor wins, else the OS user, else the
// literal "opsctl". Always prefixed "opsctl:" so opsctl-originated audit
// entries are distinguishable from the platform server's own actors.
func auditActor(actorFlag string) string {
	label := strings.TrimSpace(actorFlag)
	if label == "" {
		if u, err := user.Current(); err == nil && strings.TrimSpace(u.Username) != "" {
			label = strings.TrimSpace(u.Username)
		}
	}
	if label == "" {
		label = "opsctl"
	}
	return "opsctl:" + label
}

// connectRepos mirrors cmd/reindex's storage wiring: PERSISTENCE_MODE=memory
// uses an in-memory store (dev only — never durable, matches
// cmd/platform's own refusal to allow it in production), otherwise connects
// to the configured MongoDB and fails loudly if it's unreachable rather
// than silently falling back to a throwaway in-memory store the way
// cmd/platform's REQUEST-SERVING path does — an ops tool operating on the
// wrong store is far worse than one that just refuses to start.
func connectRepos(ctx context.Context, cfg config.Config) (*repository.Repositories, func(), error) {
	if cfg.PersistenceMode == "memory" {
		return memory.New(), func() {}, nil
	}
	client, err := dal.Connect(ctx, cfg.MongoURI)
	if err != nil {
		return nil, nil, err
	}
	return mongodb.New(client.Database(cfg.MongoDB)), func() { _ = client.Disconnect(context.Background()) }, nil
}

// dialChain connects the chain RPC client every chain-touching subcommand
// needs (tx replace, indexer reset-checkpoint, indexer/DLQ retry — the
// canonical-header check both go through), verifying the endpoint is on the
// configured CHAIN_ID before returning it (see serverwiring.DialChain). The
// caller owns Close().
func dialChain(ctx context.Context, cfg config.Config) (*blockchain.RPCClient, error) {
	return serverwiring.DialChain(ctx, cfg)
}

// projectAddresses loads the deployed contract set from the single DB
// Project record. Config is bootstrap-only now (only the factory address
// lives there); every deployed address comes from this record, which the
// running platform's indexer keeps live. A missing record is an error — a
// recovery command that needs the typed decoders / indexer address set
// cannot operate without knowing which contracts were deployed.
func projectAddresses(o opsCtx) (models.Addresses, error) {
	p, err := o.repos.Projects.Get(o.ctx)
	if err != nil {
		return models.Addresses{}, fmt.Errorf("loading project record (deployed addresses are sourced from the DB, not config): %w", err)
	}
	return p.Addresses, nil
}

// buildIndexer constructs the same *indexer.Indexer shape
// cmd/platform.startBackgroundLoops uses (source, checkpoints, events,
// chainID, addresses, DLQ) — enough for ResetToTrustedCheckpoint/
// RetryDeadLetter, which only touch those fields. Never Polled by opsctl.
// The scanned address set + typed decoder are derived from the DB Project
// record's addresses (config is bootstrap-only), the same source the
// platform now uses.
//
// The decoder is serverwiring.StrictDecoder — the SAME
// address-dispatched typed decode the platform uses, but returning an error
// (rather than a generic topic-only event) for anything that is not a known
// typed contract event. That makes DLQ retry refuse to RESOLVE an entry it
// can only decode generically (indexer.RetryDeadLetter re-records, not
// resolves, an entry whose decode errors), instead of the previous nil→
// defaultDecode that always "succeeded" with a meaningless generic event.
func buildIndexer(cfg config.Config, addrs models.Addresses, repos *repository.Repositories, source indexer.ChainSource) *indexer.Indexer {
	return indexer.New(source, repos.IndexerCheckpoints, repos.ChainEvents, cfg.ChainID, serverwiring.IndexerAddresses(addrs, cfg.FactoryAddress), cfg.StartBlock, serverwiring.StrictDecoder(addrs, cfg.FactoryAddress),
		indexer.WithDeadLetterQueue(repos.IndexerDeadLetters))
}

// buildReplicationManager mirrors cmd/platform's newIPFSReplicationManager
// (main.go) — duplicated rather than imported since that helper is
// unexported in package main of a different command.
func buildReplicationManager(cfg config.Config, repos *repository.Repositories) (*ipfs.ReplicationManager, error) {
	if cfg.IPFSAPIURL == "" {
		return nil, fmt.Errorf("IPFS_API_URL is not configured")
	}
	local := ipfs.NewKuboClient(cfg.IPFSAPIURL)
	var backups []ipfs.Destination
	if cfg.IPFSBackupArchiveDir != "" {
		archive, err := ipfs.NewFileArchiveClient(cfg.IPFSBackupArchiveDir)
		if err != nil {
			return nil, err
		}
		backups = append(backups, ipfs.Destination{Name: "archive", Client: archive})
	}
	if cfg.IPFSBackupKuboURL != "" {
		backups = append(backups, ipfs.Destination{Name: "backup-kubo", Client: ipfs.NewKuboClient(cfg.IPFSBackupKuboURL)})
	}
	if len(backups) == 0 {
		return nil, fmt.Errorf("no backup destination configured (IPFS_BACKUP_ARCHIVE_DIR or IPFS_BACKUP_KUBO_URL)")
	}
	return ipfs.NewReplicationManager(local, backups, cfg.IPFSReplicationThreshold, repos.Publications), nil
}

// loadRoleSigner loads role's configured hot key via internal/keys — the
// SAME KEY_PROVIDER_MODE (raw/local-keystore/vault/kms-mock) the running
// server itself uses (mirrors cmd/platform's loadSigner), so `tx replace`
// signs with the platform's actual configured compliance key rather than a
// separate ad hoc credential an operator would otherwise have to paste in on
// the command line. Only compliance is valid here — after deployment and
// minting moved to the admin's wallet, it is the ONLY hot key cmd/platform
// wires to submit transactions via TxManager.
func loadRoleSigner(role string, cfg config.Config) (keys.Provider, error) {
	var rawHex string
	switch role {
	case "compliance":
		rawHex = cfg.ComplianceKeyHex
	default:
		return nil, fmt.Errorf("unknown --role %q (want compliance)", role)
	}
	return keys.Load(role, keys.Config{
		Mode: keys.Mode(cfg.KeyProviderMode), RawHexKey: rawHex,
		KeystoreDir: cfg.KeystoreDir, PasswordFile: cfg.KeystorePasswordFile,
		VaultAddr: cfg.VaultAddr, VaultToken: cfg.VaultToken, VaultKeyPrefix: cfg.VaultKeyPrefix,
		KMSMockSeedHex: cfg.KMSMockSeedHex,
	})
}

// refuseIfIndexerUnsafe blocks a chain mutation while the platform's indexer
// is paused mid-divergence (ReconciliationRequired), unless the operator
// explicitly opts in with --allow-unsafe. A recovery action
// taken while chain-derived read models are frozen can act on a not-yet-
// reconciled view; the ONE command exempt from this is
// `indexer reset-checkpoint`, which exists precisely to clear that state and
// therefore never calls this.
func refuseIfIndexerUnsafe(o opsCtx, cfg config.Config, allowUnsafe bool) error {
	unsafe, err := serverwiring.IndexerUnsafe(o.ctx, o.repos, cfg.ChainID)
	if err != nil {
		return fmt.Errorf("checking indexer safety state: %w", err)
	}
	if unsafe && !allowUnsafe {
		return fmt.Errorf("indexer is paused (ReconciliationRequired) for chain %d; resolve it with `indexer reset-checkpoint` first, or pass --allow-unsafe to override", cfg.ChainID)
	}
	return nil
}

func dispatch(o opsCtx, cfg config.Config, group, action string, args []string) error {
	switch group + " " + action {
	case "tx replace":
		fs := flag.NewFlagSet("tx replace", flag.ContinueOnError)
		id := fs.String("id", "", "transaction id to replace")
		role := fs.String("role", "", "hot key role to sign the replacement with (relayer, compliance)")
		bump := fs.Int64("bump-percent", 20, "fee bump percentage over the original attempt")
		allowUnsafe := fs.Bool("allow-unsafe", false, "proceed even if the platform indexer is paused (ReconciliationRequired)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *id == "" || *role == "" {
			return fmt.Errorf("tx replace requires --id and --role")
		}
		client, err := dialChain(o.ctx, cfg)
		if err != nil {
			return err
		}
		defer client.Close()
		// Refuse to broadcast a replacement while chain-derived
		// state is frozen mid-reorg, unless explicitly overridden.
		if err := refuseIfIndexerUnsafe(o, cfg, *allowUnsafe); err != nil {
			return err
		}
		signer, err := loadRoleSigner(*role, cfg)
		if err != nil {
			return err
		}
		defer signer.Close()
		// Honor TX_COORDINATION_MODE (mongo-lease) so this
		// replacement participates in the SAME distributed nonce lease a live
		// replica uses, instead of bypassing it with a bare fee-cap manager.
		txs := serverwiring.NewTxManager(cfg, client, o.repos)
		return runTxReplace(o, txs, signer, *id, *bump)

	case "indexer reset-checkpoint":
		fs := flag.NewFlagSet("indexer reset-checkpoint", flag.ContinueOnError)
		block := fs.Uint64("trusted-block", 0, "the last block to keep — every event after it is deleted")
		hash := fs.String("trusted-hash", "", "the trusted block's hash, independently verified by the operator (required)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *hash == "" {
			return fmt.Errorf("indexer reset-checkpoint requires --trusted-hash (independently verify it — see the command's doc comment)")
		}
		addrs, err := projectAddresses(o)
		if err != nil {
			return err
		}
		client, err := dialChain(o.ctx, cfg)
		if err != nil {
			return err
		}
		defer client.Close()
		// Deliberately NOT gated by refuseIfIndexerUnsafe: this command is the
		// recovery that clears ReconciliationRequired.
		idx := buildIndexer(cfg, addrs, o.repos, client)
		return runIndexerResetCheckpoint(o, idx, client, *block, *hash)

	case "dlq list":
		return runDLQList(o)

	case "dlq inspect":
		fs := flag.NewFlagSet("dlq inspect", flag.ContinueOnError)
		id := fs.String("id", "", "dead letter entry id")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *id == "" {
			return fmt.Errorf("dlq inspect requires --id")
		}
		return runDLQInspect(o, *id)

	case "dlq retry":
		fs := flag.NewFlagSet("dlq retry", flag.ContinueOnError)
		id := fs.String("id", "", "dead letter entry id")
		allowUnsafe := fs.Bool("allow-unsafe", false, "proceed even if the platform indexer is paused (ReconciliationRequired)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *id == "" {
			return fmt.Errorf("dlq retry requires --id")
		}
		addrs, err := projectAddresses(o)
		if err != nil {
			return err
		}
		// Refuse when only the generic decoder is available (no
		// typed contract deployed) — replaying through it would resolve the
		// entry with a meaningless generic event that never reaches a typed
		// projector. With a typed contract deployed, StrictDecoder (see
		// buildIndexer) still refuses to RESOLVE any single entry it can only
		// decode generically.
		if len(serverwiring.KnownEventNames(addrs, cfg.FactoryAddress)) == 0 {
			return fmt.Errorf("dlq retry refused: the project record has no typed contract addresses, so only the generic decoder is available")
		}
		client, err := dialChain(o.ctx, cfg)
		if err != nil {
			return err
		}
		defer client.Close()
		// Replaying an event into the read models while the
		// indexer is paused mid-reorg acts on a not-yet-reconciled view.
		if err := refuseIfIndexerUnsafe(o, cfg, *allowUnsafe); err != nil {
			return err
		}
		idx := buildIndexer(cfg, addrs, o.repos, client)
		return runDLQRetry(o, idx, *id)

	case "dlq dismiss":
		fs := flag.NewFlagSet("dlq dismiss", flag.ContinueOnError)
		id := fs.String("id", "", "dead letter entry id")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *id == "" {
			return fmt.Errorf("dlq dismiss requires --id")
		}
		return runDLQDismiss(o, *id)

	case "ipfs retry":
		fs := flag.NewFlagSet("ipfs retry", flag.ContinueOnError)
		id := fs.String("id", "", "publication id")
		file := fs.String("file", "", "path to the original content — MUST hash to the record's already-recorded CID")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *id == "" || *file == "" {
			return fmt.Errorf("ipfs retry requires --id and --file")
		}
		data, err := os.ReadFile(*file)
		if err != nil {
			return err
		}
		rm, err := buildReplicationManager(cfg, o.repos)
		if err != nil {
			return err
		}
		return runIPFSRetry(o, rm, *id, data)

	case "ipfs restore-local":
		fs := flag.NewFlagSet("ipfs restore-local", flag.ContinueOnError)
		id := fs.String("id", "", "publication id")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *id == "" {
			return fmt.Errorf("ipfs restore-local requires --id")
		}
		rm, err := buildReplicationManager(cfg, o.repos)
		if err != nil {
			return err
		}
		return runIPFSRestoreLocal(o, rm, *id)

	default:
		printUsage()
		return fmt.Errorf("unknown command %q", strings.TrimSpace(group+" "+action))
	}
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `opsctl --config <path> [--actor <label>] <group> <action> [flags]

  tx replace          --id=<txID> --role=<relayer|compliance> [--bump-percent=20]
  indexer reset-checkpoint --trusted-block=<n> --trusted-hash=0x...
  dlq list
  dlq inspect         --id=<id>
  dlq retry           --id=<id>
  dlq dismiss         --id=<id>
  ipfs retry          --id=<id> --file=<path>
  ipfs restore-local  --id=<id>

opsctl is not credential-gated — it reads the platform's own --config file, so host/file
access to that config IS the access boundary. --actor only labels the audit trail.`)
}
