package dto

import (
	"github.com/rwa-platform/server/internal/dal/models"
)

// TransactionResponse mirrors components.schemas.Transaction.
//
// sender/nonce/replacedBy/replacementCount are additive operator-intervention
// fields (transaction id, nonce, sender, replacement lineage) so the SPA can
// show identity and replacement lineage for
// needs_intervention/nonce_consumed_externally transactions. All are omitempty
// to preserve the existing minimal payload for the common case.
type TransactionResponse struct {
	TxHash           string `json:"txHash"`
	Kind             string `json:"kind"`
	Status           string `json:"status"`
	BlockNumber      uint64 `json:"blockNumber,omitempty"`
	ExplorerURL      string `json:"explorerUrl,omitempty"`
	Sender           string `json:"sender,omitempty"`
	Nonce            uint64 `json:"nonce,omitempty"`
	ReplacedBy       string `json:"replacedBy,omitempty"`
	ReplacementCount int    `json:"replacementCount,omitempty"`
}

// ToTransactionResponse maps one transaction record onto its API view. Status
// is passed through raw, so a new models.TxStatus reaches clients without a
// change here.
func ToTransactionResponse(tx *models.Transaction) TransactionResponse {
	return TransactionResponse{
		TxHash: tx.TxHash, Kind: tx.Kind, Status: string(tx.Status), BlockNumber: tx.BlockNumber,
		Sender: tx.From, Nonce: tx.Nonce, ReplacedBy: tx.ReplacedBy, ReplacementCount: tx.ReplacementCount,
	}
}
