package publish

// Line breaking: Knuth and Plass's total-fit algorithm, in integers.
//
// A paragraph is a list of items (box, glue, penalty). The algorithm finds the
// set of breaks that makes the whole paragraph look best (it may leave one
// line looser so that the next is not squeezed), not just the next line. It
// runs in up to three passes with a rising tolerance; if none finds a
// solution, or the paragraph is too long to afford the search, a first-fit
// breaker is used. Ratios are in thousandths: 1000 means the glue stretches by
// its whole stretch.

const (
	linePenalty   = 10
	adjDemerits   = 10000
	maxKPItems    = 60_000 // beyond this a paragraph is broken first-fit (about 25,000 words)
	infRatio      = int64(1) << 40
	maxActiveKeep = 64
)

type node struct {
	pos  int32
	line int32
	fit  int8
	dem  int64
	prev int32
}

type breaker struct {
	items   []item
	W, Y, Z []mpt
	next    []int32 // next[i]: the first box at or after i, len(items) when none
	lf      lineFn
	dropN   int
	justify bool
}

func newBreaker(items []item, lf lineFn, dropN int, justify bool) *breaker {
	n := len(items)
	b := &breaker{items: items, lf: lf, dropN: dropN, justify: justify}
	b.W, b.Y, b.Z = make([]mpt, n+1), make([]mpt, n+1), make([]mpt, n+1)
	b.next = make([]int32, n+2)
	for i, it := range items {
		b.W[i+1], b.Y[i+1], b.Z[i+1] = b.W[i], b.Y[i], b.Z[i]
		switch it.kind {
		case iBox:
			b.W[i+1] += it.w
		case iGlue:
			b.W[i+1] += it.w
			b.Y[i+1] += it.y
			b.Z[i+1] += it.z
		}
	}
	b.next[n], b.next[n+1] = int32(n), int32(n)
	for i := n - 1; i >= 0; i-- {
		if items[i].kind == iBox {
			b.next[i] = int32(i)
		} else {
			b.next[i] = b.next[i+1]
		}
	}
	return b
}

func (b *breaker) legal(i int) bool {
	it := &b.items[i]
	switch it.kind {
	case iGlue:
		return i > 0 && b.items[i-1].kind == iBox
	case iPenalty:
		return it.pen < penInf
	}
	return false
}

// ratio computes the stretch ratio of the line that runs from item s to the
// break at b with measure L, and whether the line is wider than it can shrink.
func (b *breaker) ratio(s, at int, L mpt) int64 {
	width := b.W[at] - b.W[s]
	if it := b.items[at]; it.kind == iPenalty {
		width += it.w
	}
	switch {
	case width < L:
		y := b.Y[at] - b.Y[s]
		if y > 0 {
			r := (L - width) * 1000 / y
			if r > 1<<30 {
				r = 1 << 30
			}
			return r
		}
		return infRatio
	case width > L:
		z := b.Z[at] - b.Z[s]
		if z > 0 {
			return -(width - L) * 1000 / z
		}
		return -infRatio
	}
	return 0
}

func badness(r int64) int64 {
	if r < 0 {
		r = -r
	}
	if r > 30000 {
		return 10000
	}
	bd := r * r * r / 10_000_000
	if bd > 10000 {
		return 10000
	}
	return bd
}

// raggedBadness scores a line that is not justified by how short it falls.
func raggedBadness(width, L mpt) int64 {
	if width >= L || L <= 0 {
		return 0
	}
	x := (L - width) * 1000 / L
	return x * x / 2500
}

func fitness(r int64) int8 {
	switch {
	case r < -500:
		return 0
	case r <= 500:
		return 1
	case r <= 1000:
		return 2
	}
	return 3
}

type cand struct {
	dem  int64
	from int32
	set  bool
}

// pass runs the algorithm with a tolerance (a badness) and returns the breaks
// (item indices, in order) or nil when no set of lines is that good.
func (b *breaker) pass(tolerance int64) []int32 {
	n := len(b.items)
	nodes := make([]node, 1, 256)
	nodes[0] = node{pos: -1, fit: 1, prev: -1}
	active := []int32{0}
	classes := b.dropN + 1
	best := make([][4]cand, classes)
	var final int32 = -1
	var finalDem int64 = 1 << 62
	for at := 0; at < n; at++ {
		if !b.legal(at) {
			continue
		}
		it := &b.items[at]
		forced := it.kind == iPenalty && it.pen == -penInf // every line must end here
		isEnd := at == n-1
		for c := range best {
			best[c] = [4]cand{}
		}
		var minDem int64 = 1 << 62
		kept := active[:0]
		for _, ai := range active {
			a := nodes[ai]
			s := int(b.next[a.pos+1])
			_, L := b.lf(int(a.line))
			r := b.ratio(s, at, L)
			if r < -1000 {
				continue // too long: this start is spent
			}
			if !forced {
				kept = append(kept, ai)
			}
			var bad int64
			if b.justify || r < 0 {
				bad = badness(r)
			} else {
				width := b.W[at] - b.W[s]
				if forced {
					bad = 0
				} else {
					bad = raggedBadness(width, L)
				}
				r = 0
			}
			if bad > tolerance {
				continue
			}
			f := fitness(r)
			d := int64(linePenalty) + bad
			d *= d
			if it.kind == iPenalty {
				p := int64(it.pen)
				switch {
				case p >= 0:
					d += p * p
				case p > -penInf:
					d -= p * p
				}
			}
			if abs8(f-a.fit) > 1 {
				d += adjDemerits
			}
			d += a.dem
			key := int(a.line) + 1
			if key >= classes {
				key = classes - 1
			}
			if !best[key][f].set || d < best[key][f].dem {
				best[key][f] = cand{dem: d, from: ai, set: true}
			}
			if d < minDem {
				minDem = d
			}
		}
		active = kept
		if minDem < 1<<62 {
			for key := 0; key < classes; key++ {
				for f := int8(0); f < 4; f++ {
					c := best[key][f]
					if !c.set || c.dem > minDem+adjDemerits {
						continue
					}
					nodes = append(nodes, node{pos: int32(at), line: nodes[c.from].line + 1, fit: f, dem: c.dem, prev: c.from})
					idx := int32(len(nodes) - 1)
					if isEnd {
						if c.dem < finalDem {
							final, finalDem = idx, c.dem
						}
					} else {
						active = append(active, idx)
					}
				}
			}
		}
		if isEnd {
			break
		}
		if len(active) == 0 {
			return nil
		}
		if len(active) > maxActiveKeep*classes*4 {
			// A paragraph that never lets a line fill (a single unbreakable
			// stretch): keep the best few so the search stays bounded.
			sortActive(nodes, active)
			active = active[:maxActiveKeep]
		}
	}
	if final < 0 {
		return nil
	}
	var rev []int32
	for k := final; nodes[k].prev >= 0; k = nodes[k].prev {
		rev = append(rev, nodes[k].pos)
	}
	out := make([]int32, len(rev))
	for i := range rev {
		out[i] = rev[len(rev)-1-i]
	}
	return out
}

func abs8(a int8) int8 {
	if a < 0 {
		return -a
	}
	return a
}

func sortActive(nodes []node, active []int32) {
	// insertion sort by demerits: the slice is small when this runs
	for i := 1; i < len(active); i++ {
		for j := i; j > 0 && nodes[active[j]].dem < nodes[active[j-1]].dem; j-- {
			active[j], active[j-1] = active[j-1], active[j]
		}
	}
}

// greedy breaks first-fit: each line takes as much as fits.
func (b *breaker) greedy() []int32 {
	n := len(b.items)
	var out []int32
	pos := 0 // start of the next line's search (an item index)
	for pos < n {
		s := int(b.next[pos])
		if s >= n {
			break
		}
		_, L := b.lf(len(out))
		lastFit := -1
		firstLegal := -1
		for i := s; i < n; i++ {
			if !b.legal(i) {
				continue
			}
			if firstLegal < 0 {
				firstLegal = i
			}
			it := b.items[i]
			if it.kind == iPenalty && it.pen == -penInf {
				lastFit = i // a hard break, or the end
				break
			}
			if b.ratio(s, i, L) < -1000 {
				break
			}
			lastFit = i
		}
		if lastFit < 0 {
			lastFit = firstLegal // an over-long piece: it overflows rather than loops
			if lastFit < 0 {
				lastFit = n - 1
			}
		}
		out = append(out, int32(lastFit))
		pos = lastFit + 1
		if lastFit >= n-1 {
			break
		}
	}
	return out
}

// breakParagraph breaks the paragraph into lines. loose counts justified
// lines stretched past one and a half times their glue; overfull counts lines
// wider than their measure that could not shrink to fit.
func breakParagraph(items []item, lf lineFn, dropN int, justify bool) (lines []lineRec, loose, overfull int) {
	if len(items) == 0 {
		return nil, 0, 0
	}
	b := newBreaker(items, lf, dropN, justify)
	var breaks []int32
	if len(items) <= maxKPItems {
		for _, tol := range []int64{100, 800, 10000} {
			if breaks = b.pass(tol); breaks != nil {
				break
			}
		}
	}
	if breaks == nil {
		breaks = b.greedy()
	}
	prev := -1
	n := len(items)
	for i, at := range breaks {
		s := int(b.next[prev+1])
		indent, L := lf(i)
		lr := lineRec{from: int32(s), to: at, indent: indent, width: L}
		it := items[at]
		lr.last = int(at) == n-1
		lr.forced = !lr.last && it.kind == iPenalty && it.pen == -penInf
		lr.nat = b.W[at] - b.W[s]
		r := b.ratio(s, int(at), L)
		switch {
		case r >= infRatio/2, r <= -infRatio/2:
			// no glue to stretch (a line of one long word), or none to shrink
			if r < 0 {
				overfull++
			}
			r = 0
		case lr.last || lr.forced:
			if r > 0 {
				r = 0
			}
		case !justify && r > 0:
			r = 0
		}
		if r < -1000 {
			overfull++
			r = -1000
		}
		lr.r = r
		if justify && !lr.last && !lr.forced && r > 1500 {
			loose++
		}
		lines = append(lines, lr)
		prev = int(at)
	}
	return lines, loose, overfull
}
