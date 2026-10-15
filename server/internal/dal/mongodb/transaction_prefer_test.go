package mongodb

import (
	"testing"

	"github.com/rwa-platform/server/internal/dal/models"
)

func TestPreferSubmittedBeatsEventDerivedRegardlessOfID(t *testing.T) {
	evt := &models.Transaction{ID: models.EventDerivedTxIDPrefix + "abc"}
	sub := &models.Transaction{ID: "f0000000-uuid"}
	for _, in := range [][]*models.Transaction{{evt, sub}, {sub, evt}} {
		if got := preferSubmitted(in); got != sub {
			t.Errorf("got %s, want the submitted record", got.ID)
		}
	}
	if got := preferSubmitted([]*models.Transaction{evt}); got != evt {
		t.Error("event-derived record should be the fallback")
	}
	if preferSubmitted(nil) != nil {
		t.Error("empty input should give nil")
	}
}
