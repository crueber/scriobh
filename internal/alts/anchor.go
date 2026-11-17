package alts

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/text"
)

// Edit says that bytes [Start, End) of the old body became NewLen bytes of the
// new one. An insertion has Start == End; a deletion has NewLen 0.
type Edit struct{ Start, End, NewLen int }

func (e Edit) delta() int { return e.NewLen - (e.End - e.Start) }

// Options are what Rebase needs to add or edit a variant.
type Options struct {
	Actor string
	Now   string
	NewID func(prefix string) string
	Limits
	// AllowOverlap keeps anchors that overlap each other. An alternative may not
	// overlap another (cycling one would destroy the other), so by default the
	// later of two is orphaned; the anchors of comments overlap by nature.
	AllowOverlap bool
}

// Report says what a Rebase did.
type Report struct {
	Moved      int // anchors found again away from where they were
	Adopted    int // anchors whose wording the writer edited in place
	Orphaned   int
	Reattached int // orphans that found their text again
	Placed     int // new items located in the body
	OrphanedID []string
	AdoptedID  []string
	// Exhausted is true when the search budget ran out: anchors that were still
	// looking for their text were orphaned rather than guessed at.
	Exhausted bool
}

// window finds the one edit that turns old into new: the bytes outside the
// common prefix and suffix.
func window(o, n string) []Edit {
	if o == n {
		return nil
	}
	p := 0
	for p < len(o) && p < len(n) && o[p] == n[p] {
		p++
	}
	for p > 0 && p < len(o) && !utf8.RuneStart(o[p]) {
		p--
	}
	s := 0
	for s < len(o)-p && s < len(n)-p && o[len(o)-1-s] == n[len(n)-1-s] {
		s++
	}
	for s > 0 && !utf8.RuneStart(o[len(o)-s]) {
		s--
	}
	return []Edit{{Start: p, End: len(o) - s, NewLen: (len(n) - s) - p}}
}

// Window is the single edit that turns old into new, for callers that want to
// know whether a text changed and where.
func Window(old, new string) []Edit { return window(old, new) }

func shiftBefore(edits []Edit, x int) int {
	d := 0
	for _, e := range edits {
		if e.End <= x {
			d += e.delta()
		}
	}
	return d
}

// newStart is where, in the new body, the text of edit e begins: its place in
// the old body moved by every other edit before it.
func newStart(edits []Edit, e Edit) int {
	d := 0
	for _, o := range edits {
		if o != e && o.End <= e.Start {
			d += o.delta()
		}
	}
	return e.Start + d
}

func overlaps(e Edit, a, b int) bool { return e.Start < b && e.End > a }

// ScanBudget bounds the bytes of text one Rebase searches (all its anchors
// together). A search is a linear scan, so a save cannot cost more than a fixed
// few tens of milliseconds however many anchors are looking for their text or
// however repetitive the text is; an anchor that has not found its text when the
// budget is spent is orphaned (Report.Exhausted), never guessed at.
const ScanBudget = 96 << 20

// Rebase moves the anchors of items from the old body to the new one. With no
// edits given it works out the one edit between the two bodies. Items with
// Start < 0 (read from a comment) are located in the new body, in the order
// given. The returned items are in the order given, with Start, End, contexts,
// chosen variant and Orphan brought up to date, and the report says what moved.
// Nothing is dropped and nothing is attached by a guess: an anchor whose text
// cannot be found with certainty is orphaned.
func Rebase(oldBody, newBody string, in []Item, edits []Edit, o Options) ([]Item, Report) {
	var rep Report
	items := make([]Item, len(in))
	for i, it := range in {
		items[i] = it.Clone()
	}
	if edits == nil {
		edits = window(oldBody, newBody)
	}
	sort.Slice(edits, func(i, j int) bool { return edits[i].Start < edits[j].Start })
	oix, nix := NewIndex(oldBody), NewIndex(newBody)
	type pos struct{ a, b int }
	placed := make(map[int]pos, len(items)) // index -> byte anchor in the new body
	wasOrphan := make([]bool, len(items))
	for i := range items {
		wasOrphan[i] = items[i].Orphan
	}
	f := &finder{body: newBody, budget: ScanBudget}

	orphan := func(i int) {
		items[i].Orphan = true
		rep.Orphaned++
		rep.OrphanedID = append(rep.OrphanedID, items[i].ID)
	}

	// A placement is checked as a new anchor would be (a word at word edges, a
	// whole paragraph, in prose and not in code, clear of ghost tags), however it
	// was found; the text is parsed once, and only if something needs checking.
	var blocks []text.Block
	blocksOK, blocksDone := false, false
	valid := func(it *Item, a, b int) bool {
		if !blocksDone {
			blocks, blocksOK = text.Blocks(newBody)
			blocksDone = true
		}
		if !blocksOK {
			return true // too irregular to check; nothing better is known
		}
		return CheckPlacement(newBody, a, b, it.Scope, blocks, o.Limits) == nil
	}

	// Phase 1: items that had a trusted anchor in the old body.
	for i := range items {
		it := &items[i]
		if !it.Placed() || it.Orphan {
			continue
		}
		a, ok1 := oix.Byte(it.Start)
		b, ok2 := oix.Byte(it.End)
		chosen := it.ChosenText()
		sane := ok1 && ok2 && a < b && b <= len(oldBody) && oldBody[a:b] == chosen
		touched := !sane
		for _, e := range edits {
			if sane && overlaps(e, a, b) {
				touched = true
				break
			}
		}
		// Typing that continues the anchored word at its edge belongs to the word.
		if sane && !touched {
			if na, nb, ok := continuation(edits, a, b, oldBody, newBody); ok {
				if adopt(it, newBody[na:nb], o) && valid(it, na, nb) {
					placed[i] = pos{na, nb}
					rep.Adopted++
					rep.AdoptedID = append(rep.AdoptedID, it.ID)
				} else {
					orphan(i)
				}
				continue
			}
		}
		if !touched {
			na, nb := a+shiftBefore(edits, a), b+shiftBefore(edits, a)
			if nb <= len(newBody) && newBody[na:nb] == chosen {
				placed[i] = pos{na, nb}
				continue
			}
			touched = true
		}
		if sane && deleted(edits, a, b) {
			orphan(i) // the text is gone and nothing took its place: nowhere to look
			continue
		}
		exp := 0
		if ok1 {
			exp = a + shiftBefore(edits, a)
		}
		if na, nb, vid, ok := f.locate(*it, locNearest, exp, 0, regionOf(edits, a, b, len(newBody)), [2]int{0, len(newBody)}); ok && valid(it, na, nb) {
			it.Chosen = vid
			placed[i] = pos{na, nb}
			rep.Moved++
			continue
		}
		if sane && interior(edits, a, b) {
			shift := shiftBefore(edits, a)
			inner := 0
			for _, e := range edits {
				if overlaps(e, a, b) {
					inner += e.delta()
				}
			}
			na, nb := a+shift, b+shift+inner
			if nb > na && nb <= len(newBody) {
				if adopt(it, newBody[na:nb], o) {
					placed[i] = pos{na, nb}
					rep.Adopted++
					rep.AdoptedID = append(rep.AdoptedID, it.ID)
					continue
				}
			}
		}
		orphan(i)
	}

	// Phase 2: new items, and orphans looking for their text again.
	cursor := 0
	win := editedWindow(edits, len(newBody))
	for i := range items {
		it := &items[i]
		if _, done := placed[i]; done {
			continue
		}
		switch {
		case !it.Placed():
			if na, nb, vid, ok := f.locate(*it, locFirst, cursor, cursor, [2]int{}, [2]int{0, len(newBody)}); ok && valid(it, na, nb) {
				it.Chosen = vid
				placed[i] = pos{na, nb}
				cursor = nb
				rep.Placed++
			} else {
				it.Start, it.End = 0, 0
				orphan(i)
			}
		case wasOrphan[i] && len(edits) > 0:
			// Only an exact, unique match brings an orphan back, only a change to
			// the text can have brought it back, and so it is looked for where the
			// text changed.
			if na, nb, vid, ok := f.locate(*it, locUnique, -1, 0, [2]int{}, win); ok && valid(it, na, nb) {
				it.Chosen = vid
				placed[i] = pos{na, nb}
				rep.Reattached++
			}
		}
	}
	rep.Exhausted = f.budget < 0

	// No two anchors may overlap: the later one is orphaned.
	order := make([]int, 0, len(placed))
	for i := range placed {
		order = append(order, i)
	}
	sort.Slice(order, func(x, y int) bool {
		if placed[order[x]].a != placed[order[y]].a {
			return placed[order[x]].a < placed[order[y]].a
		}
		return order[x] < order[y]
	})
	maxEnd := -1
	for _, i := range order {
		p := placed[i]
		if p.a < maxEnd && !o.AllowOverlap {
			delete(placed, i)
			orphan(i)
			continue
		}
		if p.b > maxEnd {
			maxEnd = p.b
		}
	}

	// Write the placements back, with fresh contexts.
	for i, p := range placed {
		it := &items[i]
		it.Orphan = false
		it.Start, it.End = nix.Unit(p.a), nix.Unit(p.b)
		it.Before, it.After = contextBefore(newBody, p.a), contextAfter(newBody, p.b)
	}
	return items, rep
}

// editedWindow is the span of the new body the edits wrote, hull of them all.
func editedWindow(edits []Edit, n int) [2]int {
	if len(edits) == 0 {
		return [2]int{0, 0}
	}
	first, last := edits[0], edits[len(edits)-1]
	lo := newStart(edits, first)
	hi := newStart(edits, last) + last.NewLen
	return [2]int{max(0, lo), min(n, max(hi, lo))}
}

// deleted reports whether an edit removed all of [a,b) and put nothing in its
// place.
func deleted(edits []Edit, a, b int) bool {
	for _, e := range edits {
		if e.NewLen == 0 && e.Start <= a && e.End >= b {
			return true
		}
	}
	return false
}

// continuation recognises the writer typing on at the edge of an anchored word:
// one insertion exactly at the anchor's end (or start) whose first (last)
// characters continue the word the anchor ends (starts) with. It returns the
// anchor extended over those characters, in the new body.
func continuation(edits []Edit, a, b int, oldBody, newBody string) (int, int, bool) {
	var hit *Edit
	for i := range edits {
		e := &edits[i]
		if e.Start == e.End && (e.Start == a || e.Start == b) && e.NewLen > 0 {
			if hit != nil {
				return 0, 0, false
			}
			hit = e
		}
	}
	if hit == nil {
		return 0, 0, false
	}
	shift := shiftBefore(edits, a)
	if hit.Start == b && a != b {
		last, _ := utf8.DecodeLastRuneInString(oldBody[a:b])
		at := newStart(edits, *hit) // where the inserted text begins
		if at+hit.NewLen > len(newBody) || !wordRune(last) || unspaced(last) {
			return 0, 0, false
		}
		ins := newBody[at : at+hit.NewLen]
		k := 0
		for _, r := range ins {
			if !wordRune(r) || unspaced(r) {
				break
			}
			k += utf8.RuneLen(r)
		}
		if k == 0 {
			return 0, 0, false
		}
		return a + shift, at + k, true
	}
	if hit.Start == a {
		first, _ := utf8.DecodeRuneInString(oldBody[a:b])
		na := a + shift // the anchored text, after the inserted text
		start := na - hit.NewLen
		if start < 0 || !wordRune(first) || unspaced(first) {
			return 0, 0, false
		}
		ins := newBody[start:na]
		k := 0
		for len(ins) > k {
			r, size := utf8.DecodeLastRuneInString(ins[:len(ins)-k])
			if !wordRune(r) || unspaced(r) {
				break
			}
			k += size
		}
		if k == 0 {
			return 0, 0, false
		}
		return na - k, b + shift, true
	}
	return 0, 0, false
}

// regionOf is where, in the new body, the edits that touch [a,b) put their text,
// with some room either side: where a rewritten anchor can have gone.
func regionOf(edits []Edit, a, b, n int) [2]int {
	lo, hi := -1, -1
	for _, e := range edits {
		if !overlaps(e, a, b) {
			continue
		}
		start := newStart(edits, e)
		end := start + e.NewLen
		if lo < 0 || start < lo {
			lo = start
		}
		if end > hi {
			hi = end
		}
	}
	if lo < 0 {
		lo, hi = a, b
	}
	lo -= nearBytes
	if lo < 0 {
		lo = 0
	}
	hi += nearBytes
	if hi > n {
		hi = n
	}
	return [2]int{lo, hi}
}

// interior reports whether every edit that touches [a,b) lies within it, and
// there is at least one: the writer edited the anchored wording itself.
func interior(edits []Edit, a, b int) bool {
	n := 0
	for _, e := range edits {
		if !overlaps(e, a, b) {
			continue
		}
		n++
		if e.Start < a || e.End > b {
			return false
		}
	}
	return n > 0
}

// adopt makes text the wording the anchor now holds. If it is the text of a
// variant that variant is chosen; otherwise a non-original chosen variant takes
// it, and an original is left as it was while the text becomes a new variant.
// It reports false when the text cannot be a variant or no room is left.
func adopt(it *Item, txt string, o Options) bool {
	lim := o.Limits.orDefault()
	if CheckVariantText(txt, it.Scope, lim) != nil {
		return false
	}
	for _, v := range it.Variants {
		if v.Text == txt {
			it.Chosen = v.ID
			return true
		}
	}
	for i, v := range it.Variants {
		if v.ID == it.Chosen && !v.Original {
			it.Variants[i].Text = txt
			return true
		}
	}
	if len(it.Variants) >= lim.VariantsPer {
		return false
	}
	nv := Variant{ID: o.NewID("alv"), Text: txt, Source: SourceManual, By: o.Actor, At: o.Now}
	it.Variants = append(it.Variants, nv)
	it.Chosen = nv.ID
	return true
}

// ---- finding text ---------------------------------------------------------------

type locMode int

const (
	locNearest locMode = iota // nearest to an expected place; partial context allowed close by
	locUnique                 // the only exact match
	locFirst                  // the first exact match at or after a point
)

const (
	nearBytes = 64
	// minContext is the least context (in characters) a partial match may rest
	// on: a few characters of it say nothing about which of two like phrases
	// the anchor was on.
	minContext = 12
	longQuote  = 24
	// maxHits bounds how many places one search collects, and maxTried how many
	// wordings of one alternative are looked for.
	maxHits  = 2000
	maxTried = 6
)

// finder searches one body within a scan budget.
type finder struct {
	body   string
	budget int
}

// hits returns the byte offsets of q for each place needle (q with some
// context) occurs, at or after from, with the match inside body[lo:hi], at most
// limit of them. It charges the bytes it scans to the budget.
func (f *finder) hits(needle string, lead, from, lo, hi, limit int) []int {
	var out []int
	if hi > len(f.body) {
		hi = len(f.body)
	}
	start := max(from-lead, lo, 0)
	for start <= hi && f.budget >= 0 {
		sub := f.body[start:hi]
		i := strings.Index(sub, needle)
		if i < 0 {
			f.budget -= len(sub)
			break
		}
		f.budget -= i + len(needle)
		at := start + i
		if at+lead >= from {
			out = append(out, at+lead)
			if len(out) >= limit {
				break
			}
		}
		start = at + 1
		for start < len(f.body) && !utf8.RuneStart(f.body[start]) {
			start++
		}
	}
	return out
}

// locate finds where one of the item's variants stands in the body. The chosen
// variant is tried first, then the others (an edit may have produced another
// variant's text). win bounds where a unique search may look. It returns the
// byte range and the variant that matched.
func (f *finder) locate(it Item, mode locMode, exp, from int, region, win [2]int) (int, int, string, bool) {
	order := make([]Variant, 0, len(it.Variants))
	if v, ok := it.ChosenVariant(); ok {
		order = append(order, v)
	}
	for _, v := range it.Variants {
		if v.ID != it.Chosen {
			order = append(order, v)
		}
	}
	if len(order) > maxTried {
		order = order[:maxTried]
	}
	for _, v := range order {
		if v.Text == "" || f.budget < 0 {
			continue
		}
		if a, ok := f.find(v.Text, it.Before, it.After, mode, exp, from, region, win); ok {
			return a, a + len(v.Text), v.ID, true
		}
	}
	return 0, 0, "", false
}

func (f *finder) find(q, before, after string, mode locMode, exp, from int, region, win [2]int) (int, bool) {
	n := len(f.body)
	switch mode {
	case locFirst:
		if h := f.hits(before+q+after, len(before), from, 0, n, 1); len(h) > 0 {
			return h[0], true
		}
		return 0, false
	case locUnique:
		// Only text the edit wrote can be a new match, so look there (a match
		// reaches as far as its context either side), and if that finds one place,
		// check once that it is the only one in the whole text.
		pad := len(before) + len(q) + len(after)
		lo, hi := max(0, win[0]-pad), min(n, win[1]+pad)
		if c := f.hits(before+q+after, len(before), 0, lo, hi, 2); len(c) == 1 {
			if all := f.hits(before+q+after, len(before), 0, 0, n, 2); len(all) == 1 {
				return c[0], true
			}
			return 0, false
		}
		if len(q) >= longQuote {
			lo, hi = max(0, win[0]-len(q)), min(n, win[1]+len(q))
			if c := f.hits(q, 0, 0, lo, hi, 2); len(c) == 1 {
				if all := f.hits(q, 0, 0, 0, n, 2); len(all) == 1 {
					return c[0], true
				}
			}
		}
		return 0, false
	}
	// locNearest
	full := f.hits(before+q+after, len(before), from, 0, n, maxHits)
	if best, ok := nearest(full, exp); ok {
		return best, true
	}
	// The surroundings changed. Look in the region the edit rewrote, with less
	// context (never little: a few characters of it do not say which of two like
	// phrases this was, and never none), and accept a hit only when it is the
	// only one there.
	inRegion := func(needle string, lead int) (int, bool) {
		var near []int
		for _, h := range f.hits(needle, lead, 0, max(0, region[0]-len(needle)), min(n, region[1]+len(needle)), 8) {
			if h >= region[0] && h <= region[1] {
				near = append(near, h)
			}
		}
		if len(near) == 1 {
			return near[0], true
		}
		return 0, false
	}
	for _, k := range []int{ContextRunes, minContext} {
		if bk := tailRunes(before, k); utf8.RuneCountInString(bk) >= minContext {
			if h, ok := inRegion(bk+q, len(bk)); ok {
				return h, true
			}
		}
		if ak := headRunes(after, k); utf8.RuneCountInString(ak) >= minContext {
			if h, ok := inRegion(q+ak, 0); ok {
				return h, true
			}
		}
	}
	// A long passage that occurs once in the whole text is the same passage,
	// wherever it moved to.
	if len(q) >= longQuote {
		if all := f.hits(q, 0, 0, 0, n, 2); len(all) == 1 {
			return all[0], true
		}
	}
	return 0, false
}

func tailRunes(s string, n int) string {
	i := len(s)
	for k := 0; k < n && i > 0; k++ {
		_, size := utf8.DecodeLastRuneInString(s[:i])
		i -= size
	}
	return s[i:]
}

func headRunes(s string, n int) string {
	i := 0
	for k := 0; k < n && i < len(s); k++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i]
}

// nearest picks the hit closest to exp; two equally close hits are no answer.
func nearest(hs []int, exp int) (int, bool) {
	if len(hs) == 0 {
		return 0, false
	}
	best, bd, tie := hs[0], abs(hs[0]-exp), false
	for _, h := range hs[1:] {
		d := abs(h - exp)
		switch {
		case d < bd:
			best, bd, tie = h, d, false
		case d == bd:
			tie = true
		}
	}
	return best, !tie
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ResolveQuote finds the range a client means: the text quote, in the body,
// told apart by the context the client saw around it (a suffix of before, a
// prefix of after) and, if still ambiguous, by occurrence (0-based). It returns
// byte offsets.
func ResolveQuote(body, quote, before, after string, occurrence *int) (int, int, error) {
	if quote == "" {
		return 0, 0, bad("Name the text: a quote or a start and an end.")
	}
	var all []int
	for start := 0; start <= len(body); {
		i := strings.Index(body[start:], quote)
		if i < 0 {
			break
		}
		all = append(all, start+i)
		if len(all) >= maxHits {
			break
		}
		start += i + 1
		for start < len(body) && !utf8.RuneStart(body[start]) {
			start++
		}
	}
	switch {
	case len(all) == 0:
		return 0, 0, bad("That text is not in the document at this revision.")
	case occurrence != nil:
		if *occurrence < 0 || *occurrence >= len(all) {
			return 0, 0, bad("The text occurs %d times; occurrence %d does not exist.", len(all), *occurrence)
		}
		return all[*occurrence], all[*occurrence] + len(quote), nil
	case len(all) == 1:
		return all[0], all[0] + len(quote), nil
	}
	bestScore, bestAt, ties := -1, 0, 0
	for _, a := range all {
		score := commonSuffix(before, body[:a]) + commonPrefix(after, body[a+len(quote):])
		switch {
		case score > bestScore:
			bestScore, bestAt, ties = score, a, 1
		case score == bestScore:
			ties++
		}
	}
	if ties > 1 || bestScore <= 0 {
		return 0, 0, bad("The text occurs %d times; send the text around it (before and after) or an occurrence number.", len(all))
	}
	return bestAt, bestAt + len(quote), nil
}

func commonSuffix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[len(a)-1-n] == b[len(b)-1-n] {
		n++
	}
	return n
}

func commonPrefix(a, b string) int {
	n := 0
	for n < len(a) && n < len(b) && a[n] == b[n] {
		n++
	}
	return n
}

// ---- scope ------------------------------------------------------------------------

// markers are the strings no alternative or ghost may cut into or hold.
var markers = []string{text.GhostOpen, text.GhostClose, "scriobh:alts"}

// TouchesMarker reports whether body[a:b] holds or cuts into a ghost tag or the
// alternatives marker: any byte of one inside the range, so a range that begins
// or ends in the middle of a tag is as refused as one that holds it whole.
func TouchesMarker(body string, a, b int) bool {
	for _, m := range markers {
		lo, hi := max(0, a-len(m)+1), min(len(body), b+len(m)-1)
		for from := lo; from < hi; {
			i := strings.Index(body[from:hi], m)
			if i < 0 {
				break
			}
			p := from + i
			if p < b && p+len(m) > a {
				return true
			}
			from = p + 1
		}
	}
	return false
}

// CheckRange says whether body[a:b] may be the anchor of a new alternative of
// the scope: where it sits is right (CheckPlacement) and its shape is within the
// limits of the scope (a word has no spaces and at most 100 characters, a
// sentence at most 4,000 bytes, a paragraph the variant limit).
func CheckRange(body string, a, b int, scope string, blocks []text.Block, lim Limits) error {
	lim = lim.orDefault()
	if scope == ScopeSpan {
		return bad("Unknown scope %q: use word, sentence or paragraph.", scope) // the shape of a comment, not of an alternative
	}
	if err := CheckPlacement(body, a, b, scope, blocks, lim); err != nil {
		return err
	}
	q := body[a:b]
	if err := CheckVariantText(q, scope, lim); err != nil && scope != ScopeParagraph {
		return err
	}
	switch scope {
	case ScopeWord:
		if strings.IndexFunc(q, isJSSpace) >= 0 {
			return bad("A word has no spaces in it; use the sentence scope for a longer piece.")
		}
		if utf8.RuneCountInString(q) > 100 {
			return bad("A word is at most 100 characters.")
		}
	case ScopeSentence:
		if len(q) > 4000 {
			return bad("A sentence is at most 4,000 bytes.")
		}
	case ScopeParagraph:
		if len(q) > lim.MaxVariantSz {
			return bad("A paragraph alternative is limited to %d bytes.", lim.MaxVariantSz)
		}
	}
	return nil
}

// CheckPlacement says whether body[a:b] is a place an alternative may stand,
// whatever it says: trimmed, inside one paragraph or heading of prose (not code,
// a table or raw HTML), clear of ghost tags and the alternatives marker, cut at
// character edges (never inside an emoji sequence) and, except for a paragraph,
// at word edges; a paragraph is a whole top-level block. A wording that is a
// phrase ("very quiet" for a word) is a legal place; it is the shape limits of
// CheckRange that a new alternative must also meet. Placement from a comment, a
// restore or a relocation is checked with this.
func CheckPlacement(body string, a, b int, scope string, blocks []text.Block, lim Limits) error {
	lim = lim.orDefault()
	if a < 0 || b > len(body) || a >= b {
		return bad("The range is empty or outside the text.")
	}
	if !utf8.RuneStart(body[a]) || (b < len(body) && !utf8.RuneStart(body[b])) || CutsGrapheme(body, a) || CutsGrapheme(body, b) {
		return bad("The range cuts a character in two.")
	}
	q := body[a:b]
	if jsTrim(q) != q {
		return bad("The range cannot start or end with a space or a line break.")
	}
	if TouchesMarker(body, a, b) {
		return bad("The range holds or cuts into a ghost or alternatives marker. Revive the ghosted text first.")
	}
	var in *text.Block
	i := sort.Search(len(blocks), func(i int) bool { return blocks[i].End > a })
	if i < len(blocks) && blocks[i].Start <= a && b <= blocks[i].End {
		in = &blocks[i]
	}
	if in == nil {
		return bad("The range must lie inside one paragraph or heading of plain text (not code, a table, raw HTML, front matter or a note).")
	}
	if strings.Contains(q, "\n") && !in.Contiguous {
		return bad("The range spans lines of a quote or list; select within one line.")
	}
	switch scope {
	case ScopeWord, ScopeSentence:
		if splitsWord(body, a) || splitsWord(body, b) {
			return bad("The range starts or ends inside a word.")
		}
	case ScopeParagraph:
		if !in.Top || in.Start != a || in.End != b {
			return bad("A paragraph alternative covers one whole paragraph, from its first character to its last.")
		}
	case ScopeSpan:
		// any range inside one block, at character edges
	default:
		return bad("Unknown scope %q: use word, sentence or paragraph.", scope)
	}
	return nil
}

// splitsWord reports whether the cut at byte offset at falls between two
// letters of one word (spaced scripts only: Han, Kana and Thai have no spaces,
// so any cut is allowed there).
func splitsWord(s string, at int) bool {
	if at <= 0 || at >= len(s) {
		return false
	}
	l, _ := utf8.DecodeLastRuneInString(s[:at])
	r, _ := utf8.DecodeRuneInString(s[at:])
	return wordRune(l) && wordRune(r) && !unspaced(l) && !unspaced(r)
}

func wordRune(r rune) bool {
	return unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r)
}

func unspaced(r rune) bool {
	return unicode.Is(unicode.Han, r) || unicode.Is(unicode.Hiragana, r) || unicode.Is(unicode.Katakana, r) || unicode.Is(unicode.Thai, r)
}

func regionalIndicator(r rune) bool { return r >= 0x1F1E6 && r <= 0x1F1FF }

// extender reports whether r only ever continues the character before it: a
// combining mark, a variation selector, a skin-tone modifier, a joiner, a tag
// character or the keycap mark.
func extender(r rune) bool {
	switch {
	case r == 0x200D, r == 0x20E3:
		return true
	case r >= 0xFE00 && r <= 0xFE0F, r >= 0xE0100 && r <= 0xE01EF:
		return true
	case r >= 0x1F3FB && r <= 0x1F3FF:
		return true
	case r >= 0xE0020 && r <= 0xE007F:
		return true
	}
	return unicode.IsMark(r)
}

// CutsGrapheme reports whether a cut at byte offset at of s falls inside one
// user-perceived character: before a joiner, variation selector, skin-tone
// modifier, keycap mark, tag or combining mark; after a joiner; or between the
// two halves of a flag.
func CutsGrapheme(s string, at int) bool {
	if at <= 0 || at >= len(s) {
		return false
	}
	l, _ := utf8.DecodeLastRuneInString(s[:at])
	r, _ := utf8.DecodeRuneInString(s[at:])
	if extender(r) {
		return true
	}
	if l == 0x200D && !isJSSpace(r) {
		return true
	}
	if regionalIndicator(l) && regionalIndicator(r) {
		n := 0
		for i := at; i > 0; {
			c, size := utf8.DecodeLastRuneInString(s[:i])
			if !regionalIndicator(c) {
				break
			}
			n++
			i -= size
		}
		return n%2 == 1
	}
	return false
}

// SameBlocks reports whether two bodies have the same sequence of text blocks
// (count, kind), the check that a replacement left the structure alone.
func SameBlocks(a, b []text.Block) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].Heading != b[i].Heading || a[i].Top != b[i].Top {
			return false
		}
	}
	return true
}
