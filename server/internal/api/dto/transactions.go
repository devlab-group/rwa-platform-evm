package dto

import (
	"github.com/rwa-platform/server/internal/dal/models"
)

// TransactionResponse exposes transaction identity, status and replacement lineage.
// Optional fields are omitted at their zero values.
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

// ToTransactionResponse preserves the stored status and adds an explorer link
// when both explorerURL and the transaction hash are set
func ToTransactionResponse(tx *models.Transaction, explorerURL string) TransactionResponse {
	r := TransactionResponse{
		TxHash: tx.TxHash, Kind: tx.Kind, Status: string(tx.Status), BlockNumber: tx.BlockNumber,
		Sender: tx.From, Nonce: tx.Nonce, ReplacedBy: tx.ReplacedBy, ReplacementCount: tx.ReplacementCount,
	}
	if explorerURL != "" && tx.TxHash != "" {
		r.ExplorerURL = explorerURL + "/tx/" + tx.TxHash
	}
	return r
}
