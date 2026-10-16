// Package income decodes IncomeDistributor events, builds the rwa-v3 income snapshots
// (entitlements, Merkle tree, canonical document) and derives distribution and claim
// state from indexed events
package income

import (
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/rwa-platform/server/internal/bindings"
)

// EventNames lists the IncomeDistributor events the read model consumes
var EventNames = []string{"DistributionCreated", "IncomeClaimed", "UnclaimedReclaimed", "ClaimDeadlineExtended"}

// distributor is parsed once rather than per decoded log
var distributor = bindings.NewIncomeDistributor()

// DecodeLog is an indexer.EventDecoder for EventNames. IDs, amounts, blocks and
// deadlines become decimal strings, addresses and hashes 0x-hex strings. Other logs
// return "unknown" for serverwiring's governance fallback.
func DecodeLog(log types.Log) (string, map[string]any, error) {
	d := distributor
	if len(log.Topics) == 0 {
		return "unknown", nil, nil
	}
	topic0 := log.Topics[0]

	switch topic0 {
	case d.EventID("DistributionCreated"):
		ev, err := d.UnpackDistributionCreated(log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return "DistributionCreated", map[string]any{
			"id": ev.ID.String(), "merkleRoot": ev.MerkleRoot.Hex(), "total": ev.Total.String(),
			"recordBlock": uitoa(ev.RecordBlock), "claimDeadline": uitoa(ev.ClaimDeadline),
			"snapshotURI": ev.SnapshotURI, "snapshotSha256": ev.SnapshotSha256.Hex(), "caller": ev.Caller.Hex(),
		}, nil
	case d.EventID("IncomeClaimed"):
		ev, err := d.UnpackIncomeClaimed(log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return "IncomeClaimed", map[string]any{
			"id": ev.ID.String(), "account": ev.Account.Hex(), "amount": ev.Amount.String(),
		}, nil
	case d.EventID("UnclaimedReclaimed"):
		ev, err := d.UnpackUnclaimedReclaimed(log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return "UnclaimedReclaimed", map[string]any{
			"id": ev.ID.String(), "treasury": ev.Treasury.Hex(), "amount": ev.Amount.String(),
		}, nil
	case d.EventID("ClaimDeadlineExtended"):
		ev, err := d.UnpackClaimDeadlineExtended(log.Data, log.Topics)
		if err != nil {
			return "", nil, err
		}
		return "ClaimDeadlineExtended", map[string]any{
			"id": ev.ID.String(), "previousDeadline": uitoa(ev.PreviousDeadline),
			"newDeadline": uitoa(ev.NewDeadline), "caller": ev.Caller.Hex(),
		}, nil
	default:
		return "unknown", map[string]any{"topic0": topic0.Hex()}, nil
	}
}
