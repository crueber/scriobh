package db

// Positions are REAL gaps. New siblings are appended a full gap after the
// last one; an insert between two takes the midpoint; when a midpoint would
// collide the caller rebalances the sibling set.

// PositionGap is the spacing used for appends and rebalancing.
const PositionGap = 65536.0

// minPositionGap is the smallest distance worth bisecting. Below it the
// siblings should be rebalanced.
const minPositionGap = 1.0 / 1024

// PositionAfter returns a position after last. Pass 0 for an empty list.
func PositionAfter(last float64) float64 {
	return last + PositionGap
}

// PositionBetween returns a position strictly between a and b and whether
// the siblings need a rebalance first. Pass b <= a to mean "no upper
// neighbour" (append) and a < 0 to mean "no lower neighbour" (prepend).
func PositionBetween(a, b float64) (pos float64, needsRebalance bool) {
	switch {
	case b <= a && a >= 0:
		return a + PositionGap, false
	case a < 0:
		if b <= minPositionGap*2 {
			return b / 2, true
		}
		return b / 2, false
	}
	if b-a < minPositionGap*2 {
		return a + (b-a)/2, true
	}
	return a + (b-a)/2, false
}

// Rebalanced returns n evenly spaced positions starting at one gap.
func Rebalanced(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = float64(i+1) * PositionGap
	}
	return out
}
