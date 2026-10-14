package dto

import (
	"testing"

	"github.com/rwa-platform/server/internal/dal/models"
)

func TestToTransactionResponse_ExplorerURL(t *testing.T) {
	tx := &models.Transaction{TxHash: "0xabc"}
	if got := ToTransactionResponse(tx, "https://sepolia.etherscan.io").ExplorerURL; got != "https://sepolia.etherscan.io/tx/0xabc" {
		t.Errorf("ExplorerURL = %q", got)
	}
	if got := ToTransactionResponse(tx, "").ExplorerURL; got != "" {
		t.Errorf("no explorer configured: ExplorerURL = %q, want empty", got)
	}
	// A transaction not broadcast yet has no hash to link to
	if got := ToTransactionResponse(&models.Transaction{}, "https://sepolia.etherscan.io").ExplorerURL; got != "" {
		t.Errorf("no hash: ExplorerURL = %q, want empty", got)
	}
}
