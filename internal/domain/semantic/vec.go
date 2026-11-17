package semantic

import (
	"container/heap"
	"math"
)

// Vectors are stored as int8 with one multiplier each (see migration 1890): the
// vector is made unit length, scaled so that its largest number is 127, and
// rounded. For a unit query q the score
//
//	mult * sum(stored[i] * q[i]),  mult = 1 / sqrt(sum(stored[i]^2))
//
// is the cosine of the rounded vector with q: the length of what was stored is
// divided out, so the rounding changes the direction a little (a cosine error
// under 0.01 at the dimensions in use) and never the length. Ranking, which is
// all that is used, is unaffected except between near ties.
//
// Why int8 and not float32: 768 dimensions are 768 bytes, not 3,072, so 10,000
// chunks are 7.7 MB in the database, in a backup and in the copy held in memory
// for searching, not 30.7 MB. Every connection has an 8 MB page cache and RSS is
// a soak gate, so a search must not stream tens of megabytes through it.

// quantise returns the int8 form of v and its multiplier, or ok=false for a
// vector with no direction.
func quantise(v []float32) (q []int8, mult float32, ok bool) {
	var n2, maxAbs float64
	for _, x := range v {
		f := float64(x)
		n2 += f * f
		if a := math.Abs(f); a > maxAbs {
			maxAbs = a
		}
	}
	if n2 == 0 || maxAbs == 0 || math.IsInf(n2, 0) || math.IsNaN(n2) {
		return nil, 0, false
	}
	scale := 127 / maxAbs // the largest number maps to 127; the length cancels in mult
	q = make([]int8, len(v))
	var qn2 float64
	for i, x := range v {
		r := math.Round(float64(x) * scale)
		q[i] = int8(r)
		qn2 += r * r
	}
	if qn2 == 0 {
		return nil, 0, false
	}
	return q, float32(1 / math.Sqrt(qn2)), true
}

// unit returns v scaled to length 1, or ok=false for a vector with no usable direction. The scaling is done in
// float64 and relative to the largest number, so a vector of tiny (denormal) numbers does not overflow on the way:
// 1/length of such a vector is not representable, and a score made from it would be NaN.
func unit(v []float32) ([]float32, bool) {
	var maxAbs float64
	for _, x := range v {
		f := math.Abs(float64(x))
		if math.IsNaN(f) || math.IsInf(f, 0) {
			return nil, false
		}
		if f > maxAbs {
			maxAbs = f
		}
	}
	if maxAbs == 0 {
		return nil, false
	}
	var n2 float64
	for _, x := range v {
		r := float64(x) / maxAbs
		n2 += r * r
	}
	norm := math.Sqrt(n2) * maxAbs
	if norm == 0 || math.IsNaN(norm) || math.IsInf(norm, 0) {
		return nil, false
	}
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(float64(x) / norm)
	}
	return out, true
}

// dot is sum(a[i] * q[i]) for one stored vector. Four lanes, so the adds do not
// wait on each other.
func dot(a []int8, q []float32) float32 {
	var s0, s1, s2, s3 float32
	q = q[:len(a)]
	n := len(a) &^ 3
	for i := 0; i < n; i += 4 {
		s0 += float32(a[i]) * q[i]
		s1 += float32(a[i+1]) * q[i+1]
		s2 += float32(a[i+2]) * q[i+2]
		s3 += float32(a[i+3]) * q[i+3]
	}
	for i := n; i < len(a); i++ {
		s0 += float32(a[i]) * q[i]
	}
	return s0 + s1 + s2 + s3
}

// hit is a chunk and its score.
type hit struct {
	idx   int
	score float32
}

// topK keeps the k best hits in a min-heap: a candidate that does not beat the
// worst kept one costs a comparison.
type topK struct {
	k int
	h hitHeap
}

func newTopK(k int) *topK { return &topK{k: k, h: make(hitHeap, 0, k)} }

func (t *topK) offer(h hit) {
	if len(t.h) < t.k {
		heap.Push(&t.h, h)
		return
	}
	if better(h, t.h[0]) {
		t.h[0] = h
		heap.Fix(&t.h, 0)
	}
}

// better orders by score, then by chunk index, so equal scores come out in a
// fixed order.
func better(a, b hit) bool {
	if a.score != b.score {
		return a.score > b.score
	}
	return a.idx < b.idx
}

// sorted returns the kept hits, best first.
func (t *topK) sorted() []hit {
	out := make([]hit, len(t.h))
	copy(out, t.h)
	for i := 1; i < len(out); i++ { // k is small: insertion sort
		for j := i; j > 0 && better(out[j], out[j-1]); j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

type hitHeap []hit

func (h hitHeap) Len() int           { return len(h) }
func (h hitHeap) Less(i, j int) bool { return better(h[j], h[i]) } // the worst at the top
func (h hitHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *hitHeap) Push(x any)        { *h = append(*h, x.(hit)) }
func (h *hitHeap) Pop() any {
	old := *h
	n := len(old)
	x := old[n-1]
	*h = old[:n-1]
	return x
}
