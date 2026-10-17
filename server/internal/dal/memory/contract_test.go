package memory

import (
	"testing"

	"github.com/rwa-platform/server/internal/dal/repotest"
)

func TestRepositoryContract(t *testing.T) {
	repotest.Run(t, New())
}
