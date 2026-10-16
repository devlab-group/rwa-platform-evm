package income

import (
	"strconv"

	"github.com/rwa-platform/server/internal/dal/models"
)

func uitoa(n uint64) string { return strconv.FormatUint(n, 10) }

// earlier reports whether a precedes b in (block, logIndex) order
func earlier(a, b *models.ChainEvent) bool {
	if a.BlockNumber != b.BlockNumber {
		return a.BlockNumber < b.BlockNumber
	}
	return a.LogIndex < b.LogIndex
}
