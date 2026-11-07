package mongodb

import (
	"context"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/rwa-platform/server/internal/dal/models"
)

func TestPurchaseOrderFilter_BuyerIsAnExactLowercaseMatch(t *testing.T) {
	raw, err := bson.MarshalExtJSON(purchaseOrderFilter("Pending", "0xAbC", ""), false, false)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	if !strings.Contains(got, `"buyerKey":"0xabc"`) {
		t.Errorf("filter %s does not match the lowercase buyerKey exactly", got)
	}
	if strings.Contains(got, "regex") {
		t.Errorf("filter %s uses a regular expression, which no index can serve", got)
	}
}

func TestPurchaseOrderDoc_StoresBuyerKeyAndReadsBackAsAnOrder(t *testing.T) {
	order := models.PurchaseOrder{ID: "7", Buyer: "0xAbC", Status: models.PurchaseOrderPending, CreatedAt: 5}
	raw, err := bson.Marshal(purchaseOrderDoc{PurchaseOrder: order, BuyerKey: "0xabc"})
	if err != nil {
		t.Fatal(err)
	}
	var fields bson.M
	if err := bson.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if fields["_id"] != "7" || fields["buyerKey"] != "0xabc" || fields["buyer"] != "0xAbC" {
		t.Errorf("stored fields = %v, want _id 7, buyer as given and buyerKey lowercased", fields)
	}
	var back models.PurchaseOrder
	if err := bson.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if back != order {
		t.Errorf("read back %+v, want %+v", back, order)
	}
}

// TestLivePurchaseOrderBuyerFilterUsesItsIndex checks that the planner serves the public
// buyer filter from the buyerKey index
func TestLivePurchaseOrderBuyerFilterUsesItsIndex(t *testing.T) {
	db := liveDB(t)
	ctx := context.Background()
	if err := EnsureIndexes(ctx, db); err != nil {
		t.Fatal(err)
	}
	repo := &purchaseOrderRepo{coll: db.Collection(collPurchaseOrders)}
	for _, buyer := range []string{"0xAlice", "0xBob"} {
		if err := repo.Upsert(ctx, &models.PurchaseOrder{ID: buyer, Buyer: buyer, Status: models.PurchaseOrderPending}); err != nil {
			t.Fatal(err)
		}
	}

	var plan bson.M
	err := db.RunCommand(ctx, bson.D{
		{Key: "explain", Value: bson.D{
			{Key: "find", Value: collPurchaseOrders},
			{Key: "filter", Value: purchaseOrderFilter("", "0xALICE", "")},
			{Key: "sort", Value: purchaseOrderSort},
		}},
		{Key: "verbosity", Value: "queryPlanner"},
	}).Decode(&plan)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := bson.MarshalExtJSON(plan["queryPlanner"], false, false)
	if err != nil {
		t.Fatal(err)
	}
	winning := string(raw)
	if i := strings.Index(winning, `"rejectedPlans"`); i >= 0 {
		winning = winning[:i]
	}
	if !strings.Contains(winning, `"indexName":"buyerKey_1_createdAt_-1__id_-1"`) {
		t.Errorf("winning plan does not use the buyerKey index: %s", winning)
	}
}
