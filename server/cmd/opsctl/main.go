// Command opsctl is the operator CLI for recovery actions: replacing a stuck
// transaction, resetting the indexer checkpoint, handling dead-letter queue
// entries, and re-running IPFS replication.
//
// opsctl does not authenticate its caller. It reads the platform's own --config
// file, which holds the chain RPC, Mongo and hot key settings, so anyone who can
// run it already holds those secrets. Access control is host and file access to
// that config, as for `platform`.
//
// The mutating subcommands (everything except dlq list and dlq inspect) write
// to the same MongoDB as the platform, or to a throwaway in-memory store when
// mongo.persistence_mode: memory. Each one records an audit_logs entry with category
// "opsctl" and actor "opsctl:" plus the --actor label or OS user. An intent
// entry is written before the action and a result entry after it, whether it
// succeeds or fails; see opsCtx.audited.
//
// Usage:
//
//	opsctl --config <path> [--actor <label>] tx replace --id=<txID> --role=compliance --bump-percent=<n>
//	opsctl --config <path> indexer reset-checkpoint --trusted-block=<n> --trusted-hash=0x...
//	opsctl --config <path> dlq list
//	opsctl --config <path> dlq inspect --id=<id>
//	opsctl --config <path> dlq retry --id=<id>
//	opsctl --config <path> dlq dismiss --id=<id>
//	opsctl --config <path> ipfs retry --id=<id> --file=<path>
//	opsctl --config <path> ipfs restore-local --id=<id>
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

// auditActor returns the audit actor label: the --actor value, else the OS
// user, else "opsctl", always prefixed with "opsctl:". The label attributes
// entries and does not authorize anything.
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

// connectRepos uses development memory storage only when configured explicitly.
// Otherwise MongoDB connection failures return an error without falling back
// to a different store
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

// dialChain connects the chain RPC client for tx replace, indexer
// reset-checkpoint and dlq retry, and verifies the endpoint serves the
// configured chain ID (see serverwiring.DialChain). The caller closes it.
func dialChain(ctx context.Context, cfg config.Config) (*blockchain.RPCClient, error) {
	return serverwiring.DialChain(ctx, cfg)
}

// projectAddresses loads the deployed contract addresses from the Project
// record in the database; config holds only the factory address. A missing
// record is an error, because the typed decoders and the indexer's address set
// depend on it.
func projectAddresses(o opsCtx) (models.Addresses, error) {
	p, err := o.repos.Projects.Get(o.ctx)
	if err != nil {
		return models.Addresses{}, fmt.Errorf("loading project record (deployed addresses are sourced from the DB, not config): %w", err)
	}
	return p.Addresses, nil
}

// buildIndexer builds an indexer with the platform's source, stores, address
// set and dead-letter queue, enough for ResetToTrustedCheckpoint and
// RetryDeadLetter. opsctl never polls it. The address set and decoder come from
// the Project record's addresses.
//
// The decoder is serverwiring.StrictDecoder, which decodes by address like the
// platform's decoder but returns an error instead of a generic topic-only event
// for anything that is not a known typed event. RetryDeadLetter records a
// decode error as another failure instead of resolving the entry, so a retry
// never resolves an entry it could only decode generically.
func buildIndexer(cfg config.Config, addrs models.Addresses, repos *repository.Repositories, source indexer.ChainSource) *indexer.Indexer {
	return indexer.New(source, repos.IndexerCheckpoints, repos.ChainEvents, cfg.ChainID, serverwiring.IndexerAddresses(addrs, cfg.FactoryAddress), cfg.StartBlock, serverwiring.StrictDecoder(addrs, cfg.FactoryAddress),
		indexer.WithDeadLetterQueue(repos.IndexerDeadLetters))
}

// buildReplicationManager builds the replication manager as cmd/platform's
// newIPFSReplicationManager does. That helper is unexported in another main
// package, so this is a copy. Unlike the platform, it returns an error when no
// backup destination is configured.
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

// loadRoleSigner loads role's hot key through internal/keys with the platform's
// KEY_PROVIDER_MODE, so `tx replace` signs with the same key the server uses.
// Only compliance is valid, because it is the only hot key the platform uses to
// submit transactions.
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

// refuseIfIndexerUnsafe refuses an action while the indexer is paused in
// ReconciliationRequired, unless the operator passes --allow-unsafe, because
// the chain-derived read models are frozen and may be stale. `indexer
// reset-checkpoint` does not call it, since that command clears the state.
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
		role := fs.String("role", "", "hot key role to sign the replacement with (compliance)")
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
		// Refuse to broadcast a replacement while chain-derived state is frozen,
		// unless overridden
		if err := refuseIfIndexerUnsafe(o, cfg, *allowUnsafe); err != nil {
			return err
		}
		signer, err := loadRoleSigner(*role, cfg)
		if err != nil {
			return err
		}
		defer signer.Close()
		// NewTxManager applies TX_COORDINATION_MODE, so a mongo-lease deployment
		// shares the nonce lease with the running replicas
		txs := serverwiring.NewTxManager(cfg, client, o.repos)
		return runTxReplace(o, txs, signer, *id, *bump)

	case "indexer reset-checkpoint":
		fs := flag.NewFlagSet("indexer reset-checkpoint", flag.ContinueOnError)
		block := fs.Uint64("trusted-block", 0, "the last block to keep - every event after it is deleted")
		hash := fs.String("trusted-hash", "", "the trusted block's hash, independently verified by the operator (required)")
		if err := fs.Parse(args); err != nil {
			return err
		}
		if *hash == "" {
			return fmt.Errorf("indexer reset-checkpoint requires --trusted-hash (independently verify it against a block explorer or another trusted node)")
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
		// Not gated by refuseIfIndexerUnsafe, because this command clears
		// ReconciliationRequired
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
		// Refuse when no typed contract is deployed: only the generic decoder
		// would be available, and its events never reach a typed projector.
		// With typed contracts, StrictDecoder still refuses to resolve an entry
		// it can only decode generically.
		if len(serverwiring.KnownEventNames(addrs, cfg.FactoryAddress)) == 0 {
			return fmt.Errorf("dlq retry refused: the project record has no typed contract addresses, so only the generic decoder is available")
		}
		client, err := dialChain(o.ctx, cfg)
		if err != nil {
			return err
		}
		defer client.Close()
		// Replaying an event while the indexer is paused acts on an unreconciled view
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
		file := fs.String("file", "", "path to the original content - MUST hash to the record's already-recorded CID")
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

  tx replace          --id=<txID> --role=compliance [--bump-percent=20]
  indexer reset-checkpoint --trusted-block=<n> --trusted-hash=0x...
  dlq list
  dlq inspect         --id=<id>
  dlq retry           --id=<id>
  dlq dismiss         --id=<id>
  ipfs retry          --id=<id> --file=<path>
  ipfs restore-local  --id=<id>

opsctl does not authenticate its caller. It reads the platform's --config file, so host
access to that file is the access boundary. --actor only labels the audit trail.`)
}
