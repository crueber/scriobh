package editiondiff

import (
	"context"
	"sort"
)

// unit is a Unit with what matching needs.
type unit struct {
	Unit
	idx     int
	words   int
	fp      []uint32
	tkey    string
	trimmed string
}

// trimTail is s without the blanks at its end, so that two texts that differ only in how many newlines end them are
// the same text.
func trimTail(s string) string {
	i := len(s)
	for i > 0 {
		switch s[i-1] {
		case '\n', ' ', '\t', '\r':
			i--
			continue
		}
		break
	}
	return s[:i]
}

type info struct {
	method string
	score  float64
	guess  bool
}

// Thresholds of the matching, in one place so the notes can state them.
const (
	// minSimilar is the text two chapters must share (Dice on shingles) to be paired by similarity alone.
	minSimilar = 0.35
	// minTitleSimilar is what a title the two books share is checked against when the title is not a position.
	minTitleSimilar = 0.10
	// minGenericSimilar is what two chapters called "Chapter 3" must share to be paired.
	minGenericSimilar = 0.30
	// minPartOf is how much of a chapter's text must be in another for the second to be a part of it (a split) or
	// for it to have been merged into it.
	minPartOf = 0.60
	// minShingles is the fewest shingles a chapter needs for similarity to mean anything.
	minShingles = 3
	// stopPostings: a shingle that is in more chapters than this on a side is a refrain (a scene break, a repeated
	// line) and tells nothing about which chapter is which.
	stopPostings = 8
	// topCandidates is how many partners each chapter is offered by the index.
	topCandidates = 3
)

type matcher struct {
	ctx         context.Context
	base, head  []*unit
	projectMode bool
	bm, hm      []int // partner index, -1 for none: bm[b] is the head b is paired with; hm[h] the base (the first of a merge)
	binfo       []info
	hinfo       []info
	pairBudget  int
	limited     bool
}

func newMatcher(ctx context.Context, base, head []*unit, projectMode bool, pairBudget int) *matcher {
	m := &matcher{ctx: ctx, base: base, head: head, projectMode: projectMode, pairBudget: pairBudget}
	m.bm = fill(len(base))
	m.hm = fill(len(head))
	m.binfo = make([]info, len(base))
	m.hinfo = make([]info, len(head))
	return m
}

func fill(n int) []int {
	s := make([]int, n)
	for i := range s {
		s[i] = -1
	}
	return s
}

func (m *matcher) pair(b, h int, in info) {
	m.bm[b], m.hm[h] = h, b
	m.binfo[b], m.hinfo[h] = in, in
}

// sim is how much two chapters share, and whether that means anything: a chapter with fewer than minShingles
// shingles (a title page, a part with no text) has no similarity, and two texts that are the same are as similar as
// can be.
func (m *matcher) sim(b, h int) (float64, bool) {
	ub, uh := m.base[b], m.head[h]
	if ub.trimmed == uh.trimmed {
		return 1, ub.trimmed != ""
	}
	if len(ub.fp) < minShingles || len(uh.fp) < minShingles {
		return 0, false
	}
	return dice(inter(ub.fp, uh.fp), len(ub.fp), len(uh.fp)), true
}

func (m *matcher) run() {
	m.recorded()
	m.byPath()
	m.byTitle()
	m.bySimilarity()
	m.splitsAndMerges()
}

// recorded pairs the chapters the import recorded as documents.
func (m *matcher) recorded() {
	if !m.projectMode {
		return
	}
	byID := map[string]int{}
	for _, h := range m.head {
		if h.ID != "" {
			byID[h.ID] = h.idx
		}
	}
	for _, b := range m.base {
		if b.Mapped == "" {
			continue
		}
		h, ok := byID[b.Mapped]
		if !ok {
			continue
		}
		if m.hm[h] == -1 {
			m.pair(b.idx, h, info{method: MethodRecorded, score: 1})
		} else { // several chapters became one document: a merge
			m.bm[b.idx] = h
			m.binfo[b.idx] = info{method: MethodRecorded, score: 1}
		}
	}
}

// byPath pairs a chapter with the document, or the chapter of the other edition, from the same source file.
func (m *matcher) byPath() {
	byPath := map[string][]int{}
	for _, h := range m.head {
		if m.hm[h.idx] == -1 && h.Path != "" {
			byPath[h.Path] = append(byPath[h.Path], h.idx)
		}
	}
	for _, b := range m.base {
		if m.bm[b.idx] != -1 || b.Path == "" {
			continue
		}
		best, bestSim := -1, -1.0
		var bestAvail bool
		for _, h := range byPath[b.Path] {
			if m.hm[h] != -1 {
				continue
			}
			s, avail := m.sim(b.idx, h)
			if !m.projectMode {
				// between two editions the same file name is only a hint: it must agree with the text or the title
				tk := b.tkey
				if !(s >= minGenericSimilar && avail || tk != "" && tk == m.head[h].tkey && !genericTitle(tk)) {
					continue
				}
			}
			if s > bestSim {
				best, bestSim, bestAvail = h, s, avail
			}
		}
		if best < 0 {
			continue
		}
		if m.projectMode {
			m.pair(b.idx, best, info{method: MethodPath, score: 0.95})
		} else {
			s := bestSim
			if !bestAvail {
				s = 0
			}
			m.pair(b.idx, best, info{method: MethodPath, score: 0.6 + 0.35*minf(1, s/0.6), guess: true})
		}
	}
}

func minf(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}

// byTitle pairs chapters that share a title, when the text does not say they are different chapters.
func (m *matcher) byTitle() {
	bs := map[string][]int{}
	hs := map[string][]int{}
	allB, allH := map[string]int{}, map[string]int{} // how many chapters of the whole side have the title
	for _, b := range m.base {
		allB[b.tkey]++
		if m.bm[b.idx] == -1 {
			bs[b.tkey] = append(bs[b.tkey], b.idx)
		}
	}
	for _, h := range m.head {
		allH[h.tkey]++
		if m.hm[h.idx] == -1 {
			hs[h.tkey] = append(hs[h.tkey], h.idx)
		}
	}
	keys := make([]string, 0, len(bs))
	for k := range bs {
		if _, ok := hs[k]; ok && k != "" {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	type cand struct {
		b, h  int
		s     float64
		avail bool
	}
	for _, k := range keys {
		if m.ctx.Err() != nil {
			return
		}
		B, H := bs[k], hs[k]
		if len(B)*len(H) > 4096 { // a title used on hundreds of chapters on both sides is no title
			continue
		}
		generic := genericTitle(k)
		// a title that names one chapter on each side, and is not a position, is the same chapter even when it was
		// rewritten throughout; a title used more than once needs some text in common
		unique := !generic && allB[k] == 1 && allH[k] == 1
		var cands []cand
		for _, b := range B {
			for _, h := range H {
				s, avail := m.sim(b, h)
				ok := false
				switch {
				case unique:
					ok = true
				case generic:
					ok = avail && s >= minGenericSimilar ||
						len(B) == 1 && len(H) == 1 && m.base[b].trimmed == "" && m.head[h].trimmed == "" // two empty pages with one name
				default:
					ok = !avail || s >= minTitleSimilar
				}
				if ok {
					cands = append(cands, cand{b, h, s, avail})
				}
			}
		}
		sort.SliceStable(cands, func(i, j int) bool {
			if cands[i].s != cands[j].s {
				return cands[i].s > cands[j].s
			}
			if cands[i].b != cands[j].b {
				return cands[i].b < cands[j].b
			}
			return cands[i].h < cands[j].h
		})
		for _, c := range cands {
			if m.bm[c.b] != -1 || m.hm[c.h] != -1 {
				continue
			}
			sc := 0.7
			if c.avail {
				sc = 0.6 + 0.35*minf(1, c.s/0.6)
			}
			m.pair(c.b, c.h, info{method: MethodTitle, score: sc, guess: true})
		}
	}
}

type candidate struct {
	b, h   int
	ov     int
	d      float64
	nb, nh int // sizes (refrains left out) of the chapter the candidate was searched for, and of its partner
}

// candidates are, for each chapter in freeB, the few chapters of freeH that share the most of its shingles, found
// through an index of the shingles so that no two chapters are compared that share none.
func (m *matcher) candidates(freeB, freeH []int) []candidate {
	post := map[uint32][]int32{}
	hcount := map[uint32]uint16{}
	for _, h := range freeH {
		for _, x := range m.head[h].fp {
			if c := hcount[x]; c < 1<<15 {
				hcount[x] = c + 1
			}
			if p := post[x]; len(p) <= stopPostings {
				post[x] = append(p, int32(h))
			}
		}
	}
	bcount := map[uint32]uint16{}
	for _, b := range freeB {
		for _, x := range m.base[b].fp {
			if c := bcount[x]; c < 1<<15 {
				bcount[x] = c + 1
			}
		}
	}
	// A refrain (a shingle in many chapters of either side) says nothing about which chapter is which, so it is left
	// out of the sizes as well as the overlap: two chapters that share everything but a refrain are the same chapter.
	stop := func(x uint32) bool { return hcount[x] > stopPostings || bcount[x] > stopPostings }
	eff := func(fp []uint32) int {
		n := 0
		for _, x := range fp {
			if !stop(x) {
				n++
			}
		}
		return n
	}
	heff := map[int]int{}
	for _, h := range freeH {
		heff[h] = eff(m.head[h].fp)
	}
	var out []candidate
	counts := map[int32]int32{}
	for _, b := range freeB {
		if m.ctx.Err() != nil || m.pairBudget <= 0 {
			if m.pairBudget <= 0 {
				m.limited = true
			}
			break
		}
		clear(counts)
		for _, x := range m.base[b].fp {
			if stop(x) {
				continue
			}
			p := post[x]
			if len(p) == 0 {
				continue
			}
			m.pairBudget -= len(p)
			for _, h := range p {
				counts[h]++
			}
		}
		eb := eff(m.base[b].fp)
		var cs []candidate
		for h, n := range counts {
			cs = append(cs, candidate{b: b, h: int(h), ov: int(n), d: dice(int(n), eb, heff[int(h)]), nb: eb, nh: heff[int(h)]})
		}
		sort.Slice(cs, func(i, j int) bool {
			if cs[i].d != cs[j].d {
				return cs[i].d > cs[j].d
			}
			return cs[i].h < cs[j].h
		})
		if len(cs) > topCandidates {
			cs = cs[:topCandidates]
		}
		out = append(out, cs...)
	}
	return out
}

func (m *matcher) free() (fb, fh []int) {
	for _, b := range m.base {
		if m.bm[b.idx] == -1 && len(b.fp) >= minShingles {
			fb = append(fb, b.idx)
		}
	}
	for _, h := range m.head {
		if m.hm[h.idx] == -1 && len(h.fp) >= minShingles {
			fh = append(fh, h.idx)
		}
	}
	return
}

// bySimilarity pairs what is left by the text they share, best first.
func (m *matcher) bySimilarity() {
	fb, fh := m.free()
	if len(fb) == 0 || len(fh) == 0 {
		return
	}
	cs := m.candidates(fb, fh)
	sort.SliceStable(cs, func(i, j int) bool {
		if cs[i].d != cs[j].d {
			return cs[i].d > cs[j].d
		}
		if cs[i].ov != cs[j].ov {
			return cs[i].ov > cs[j].ov
		}
		if cs[i].b != cs[j].b {
			return cs[i].b < cs[j].b
		}
		return cs[i].h < cs[j].h
	})
	for _, c := range cs {
		if c.d < minSimilar {
			break
		}
		if m.bm[c.b] != -1 || m.hm[c.h] != -1 {
			continue
		}
		m.pair(c.b, c.h, info{method: MethodSimilarity, score: c.d, guess: true})
	}
}

// splitsAndMerges finds what the one-to-one pairing left: a chapter whose text went into several chapters (the
// first part may still be paired with it), and several chapters whose text went into one.
func (m *matcher) splitsAndMerges() {
	// A split: an unpaired chapter of the second text that is mostly the text of one chapter of the first.
	var fh []int
	for _, h := range m.head {
		if m.hm[h.idx] == -1 && len(h.fp) >= minShingles {
			fh = append(fh, h.idx)
		}
	}
	var allB []int
	for _, b := range m.base {
		if len(b.fp) >= minShingles {
			allB = append(allB, b.idx)
		}
	}
	if len(fh) > 0 && len(allB) > 0 {
		// candidates of each unpaired head among every chapter of the first text, by containment in the head
		parts := map[int][]candidate{} // base -> heads that are mostly its text
		for _, c := range m.candidatesFrom(fh, allB, true) {
			if float64(c.ov)/float64(max1(c.nb)) >= minPartOf {
				parts[c.b] = append(parts[c.b], c)
			}
		}
		bs := make([]int, 0, len(parts))
		for b := range parts {
			bs = append(bs, b)
		}
		sort.Ints(bs)
		for _, b := range bs {
			cs := parts[b]
			if m.bm[b] == -1 && len(cs) < 2 {
				continue // one piece of an unpaired chapter is not a split
			}
			sort.Slice(cs, func(i, j int) bool { return cs[i].h < cs[j].h })
			for _, c := range cs {
				if m.hm[c.h] != -1 {
					continue
				}
				if h0 := m.bm[b]; h0 >= 0 && m.shared(m.head[h0].fp, m.head[c.h].fp) >= 0.5 {
					continue // a copy of what the chapter's partner already holds is another chapter, not a part of this one
				}
				if m.bm[b] == -1 {
					m.pair(b, c.h, info{method: MethodSimilarity, score: minf(1, float64(c.ov)/float64(max1(c.nb))), guess: true})
					continue
				}
				m.hm[c.h] = b
				m.hinfo[c.h] = info{method: MethodSimilarity, score: minf(1, float64(c.ov)/float64(max1(c.nb))), guess: true}
			}
		}
	}
	// A merge: an unpaired chapter of the first text that is mostly the text of one chapter of the second.
	var fb []int
	for _, b := range m.base {
		if m.bm[b.idx] == -1 && len(b.fp) >= minShingles {
			fb = append(fb, b.idx)
		}
	}
	var allH []int
	for _, h := range m.head {
		if len(h.fp) >= minShingles && !m.isSplitExtra(h.idx) {
			allH = append(allH, h.idx)
		}
	}
	if len(fb) > 0 && len(allH) > 0 {
		into := map[int][]candidate{}
		for _, c := range m.candidates(fb, allH) {
			if float64(c.ov)/float64(max1(c.nb)) >= minPartOf {
				into[c.h] = append(into[c.h], c)
			}
		}
		hs := make([]int, 0, len(into))
		for h := range into {
			hs = append(hs, h)
		}
		sort.Ints(hs)
		for _, h := range hs {
			cs := into[h]
			if m.hm[h] == -1 && len(cs) < 2 {
				continue
			}
			sort.Slice(cs, func(i, j int) bool { return cs[i].b < cs[j].b })
			for _, c := range cs {
				if m.bm[c.b] != -1 {
					continue
				}
				if b0 := m.hm[h]; b0 >= 0 && m.shared(m.base[b0].fp, m.base[c.b].fp) >= 0.5 {
					continue // the same text twice in the first book is not two chapters merged
				}
				sc := minf(1, float64(c.ov)/float64(max1(c.nb)))
				if m.hm[h] == -1 {
					m.pair(c.b, h, info{method: MethodSimilarity, score: sc, guess: true})
					continue
				}
				m.bm[c.b] = h
				m.binfo[c.b] = info{method: MethodSimilarity, score: sc, guess: true}
			}
		}
	}
}

// isSplitExtra says h is a part of a split (paired to a base that is paired to another head).
func (m *matcher) isSplitExtra(h int) bool {
	b := m.hm[h]
	return b >= 0 && m.bm[b] != h
}

// candidatesFrom is candidates with the roles swapped: for each chapter of the second text, the chapters of the
// first that share the most of its shingles.
func (m *matcher) candidatesFrom(freeH, allB []int, _ bool) []candidate {
	swap := &matcher{ctx: m.ctx, base: m.head, head: m.base, pairBudget: m.pairBudget}
	out := swap.candidates(freeH, allB)
	m.pairBudget = swap.pairBudget
	if swap.limited {
		m.limited = true
	}
	for i := range out { // back to base, head
		out[i].b, out[i].h = out[i].h, out[i].b
	}
	return out
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}

// shared is the part of b's runs of words that a has too.
func (m *matcher) shared(a, b []uint32) float64 {
	if len(b) == 0 {
		return 0
	}
	return float64(inter(a, b)) / float64(len(b))
}
