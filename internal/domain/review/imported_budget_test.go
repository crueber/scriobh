package review_test

import (
	"testing"

	"github.com/crueber/scriobh/internal/domain/review"
)

// PlaceableAfterOthers is bounded: when the work it may do is spent, it vouches for nothing it has not checked.
func TestPlaceableAfterOthersSpentBudgetVouchesForNothing(t *testing.T) {
	body := "The boat came in at dawn and the harbour was quiet, and nobody spoke of the winter at all."
	sugs := [][]review.ChangeIn{
		{{Quote: "dawn", Before: "came in at ", After: " and the", Text: "first light"}},
		{{Quote: "winter", Before: "spoke of the ", After: " at all", Text: "summer"}},
	}
	if ok := review.PlaceableAfterOthers(body, sugs); !ok[0] || !ok[1] {
		t.Fatalf("two clear changes: %v", ok)
	}
	spent := -1
	if ok := review.PlaceableAfterOthersWithin(body, sugs, &spent); ok[0] || ok[1] {
		t.Errorf("with the budget spent: %v", ok)
	}
	// the work of each check is counted against the budget it is given
	left := review.PlaceBudget
	review.PlaceableAfterOthersWithin(body, sugs, &left)
	if left >= review.PlaceBudget {
		t.Errorf("the budget was not charged: %d", left)
	}
}
