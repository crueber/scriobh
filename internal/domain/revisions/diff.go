package revisions

import (
	"strings"
	"unicode"
)

// A diff is computed in three passes so its cost is bounded by construction,
// not by luck:
//
//  1. the common prefix and suffix, in lines, are set aside (most saves touch
//     one place in a long chapter);
//  2. what is left is diffed line by line with Myers' algorithm, giving up
//     beyond maxLineEdits edits;
//  3. inside each changed hunk the words are diffed, unless the hunk is larger
//     than maxHunkTokens or the whole diff has used maxWork comparisons.
//
// Whatever is not refined falls back to "these lines were deleted, those were
// inserted", and the result says so (Coarse). The output is deterministic: the
// same two texts always give the same segments.
const (
	maxLineEdits  = 1000
	maxWordEdits  = 1000
	maxHunkTokens = 30000
	maxWork       = 30_000_000
)

// Segment is a run of text that is unchanged ("equal"), only in the newer text
// ("insert"), or only in the older one ("delete"). Concatenating the equal and
// delete segments gives the older text; the equal and insert segments give the
// newer.
type Segment struct {
	Op   string `json:"op" enum:"equal,insert,delete"`
	Text string `json:"text"`
}

// Stats summarises a diff in words.
type Stats struct {
	AddedWords   int `json:"added_words"`
	RemovedWords int `json:"removed_words"`
}

// DiffResult is the comparison of two texts.
type DiffResult struct {
	Segments []Segment `json:"segments"`
	Stats    Stats     `json:"stats"`
	Changed  bool      `json:"changed"`
	// Coarse is true when a size limit stopped the diff from refining some
	// change to individual words (or, rarely, lines): those changes are shown
	// as a larger delete followed by an insert.
	Coarse bool `json:"coarse"`
}

// Units a diff can be taken in.
const (
	UnitWord      = "word"
	UnitParagraph = "paragraph"
)

// Diff compares old with new. unit is UnitWord (changes inside a paragraph are
// shown word by word) or UnitParagraph (whole lines are deleted or inserted).
func Diff(oldText, newText, unit string) DiffResult {
	return diffCapped(oldText, newText, unit, maxWork)
}

// Budget is a work allowance shared by a series of diffs, so that the cost of
// comparing many pairs of texts (the chapters of two books) is bounded as a
// whole and not only for each pair. Work counts what the diff counts for
// itself (comparisons in the line and word passes), and the caller charges the
// linear cost of its own inputs; it is deterministic, so the same texts always
// get the same answer, which a clock would not give.
type Budget struct {
	// Work is what is left to spend. DiffWithin lowers it by what it used.
	Work int
}

// DiffWithin is Diff within a shared budget: no diff may use more than what is
// left, up to the usual per-diff limit, and what it uses is taken off. A diff
// that runs out of budget refines less (Coarse) exactly as one that reaches a
// per-diff limit does; it is never wrong. A caller that wants no work done once
// the budget is gone checks Work before it calls.
func DiffWithin(oldText, newText, unit string, b *Budget) DiffResult {
	limit := maxWork
	if b != nil && b.Work < limit {
		limit = b.Work
	}
	if limit < 1 {
		limit = 1
	}
	r, used := diffUsed(oldText, newText, unit, limit)
	if b != nil {
		b.Work -= used
	}
	return r
}

func diffCapped(oldText, newText, unit string, limit int) DiffResult {
	r, _ := diffUsed(oldText, newText, unit, limit)
	return r
}

func diffUsed(oldText, newText, unit string, limit int) (DiffResult, int) {
	d := &differ{words: unit != UnitParagraph, limit: limit}
	r := d.run(oldText, newText)
	return r, d.work
}

func (d *differ) run(oldText, newText string) DiffResult {
	if oldText == newText {
		if oldText != "" {
			d.emit("equal", oldText)
		}
		return d.result()
	}
	a, b := splitLines(oldText), splitLines(newText)
	pre := 0
	for pre < len(a) && pre < len(b) && a[pre] == b[pre] {
		pre++
	}
	suf := 0
	for suf < len(a)-pre && suf < len(b)-pre && a[len(a)-1-suf] == b[len(b)-1-suf] {
		suf++
	}
	d.emit("equal", strings.Join(a[:pre], ""))
	d.lines(a[pre:len(a)-suf], b[pre:len(b)-suf])
	d.emit("equal", strings.Join(a[len(a)-suf:], ""))
	return d.result()
}

type differ struct {
	words  bool
	inSent bool // the changed region is being diffed sentence by sentence (a paragraph too long to diff word by word at once)
	limit  int  // comparisons this diff may use; maxWork unless a budget is lower
	work   int
	coarse bool
	segs   []Segment
}

func (d *differ) emit(op, text string) {
	if text == "" {
		return
	}
	if n := len(d.segs); n > 0 && d.segs[n-1].Op == op {
		d.segs[n-1].Text += text
		return
	}
	d.segs = append(d.segs, Segment{Op: op, Text: text})
}

func (d *differ) result() DiffResult {
	r := DiffResult{Segments: d.segs, Coarse: d.coarse}
	if r.Segments == nil {
		r.Segments = []Segment{}
	}
	for _, s := range d.segs {
		switch s.Op {
		case "insert":
			r.Stats.AddedWords += countWords(s.Text)
			r.Changed = true
		case "delete":
			r.Stats.RemovedWords += countWords(s.Text)
			r.Changed = true
		}
	}
	return r
}

// lines diffs two runs of lines that share no prefix or suffix.
func (d *differ) lines(a, b []string) {
	switch {
	case len(a) == 0 && len(b) == 0:
		return
	case len(a) == 0:
		d.emit("insert", strings.Join(b, ""))
		return
	case len(b) == 0:
		d.emit("delete", strings.Join(a, ""))
		return
	}
	ia, ib := intern(a, b)
	script, ok := myers(ia, ib, maxLineEdits, &d.work, d.limit)
	if !ok {
		d.coarse = true
		d.emit("delete", strings.Join(a, ""))
		d.emit("insert", strings.Join(b, ""))
		return
	}
	// Walk the script, gathering each run of changes into a hunk.
	i, j := 0, 0
	var delLines, insLines []string
	flush := func() {
		if len(delLines) > 0 || len(insLines) > 0 {
			d.hunk(delLines, insLines)
			delLines, insLines = nil, nil
		}
	}
	for _, op := range script {
		switch op {
		case opEqual:
			flush()
			d.emit("equal", a[i])
			i++
			j++
		case opDelete:
			delLines = append(delLines, a[i])
			i++
		case opInsert:
			insLines = append(insLines, b[j])
			j++
		}
	}
	flush()
}

// hunk emits one changed region, refined to words when that is allowed and
// affordable.
func (d *differ) hunk(del, ins []string) {
	oldText, newText := strings.Join(del, ""), strings.Join(ins, "")
	if !d.words || len(del) == 0 || len(ins) == 0 {
		d.emit("delete", oldText)
		d.emit("insert", newText)
		return
	}
	ta, tb := tokenize(oldText), tokenize(newText)
	if len(ta)+len(tb) > maxHunkTokens && !d.inSent && d.work <= d.limit {
		// One paragraph too long to diff word by word at once (a chapter set as a single block): cut both texts at the
		// ends of sentences and diff those as lines, so each changed sentence is refined to its words, as the same text
		// in many paragraphs would be.
		if sa, sb := splitSentences(oldText), splitSentences(newText); len(sa) > 1 || len(sb) > 1 {
			d.inSent = true
			d.lines(sa, sb)
			d.inSent = false
			return
		}
	}
	if len(ta)+len(tb) > maxHunkTokens || d.work > d.limit {
		d.coarse = true
		d.emit("delete", oldText)
		d.emit("insert", newText)
		return
	}
	// Trim the words the hunk's lines share at either end.
	pre := 0
	for pre < len(ta) && pre < len(tb) && ta[pre] == tb[pre] {
		pre++
	}
	suf := 0
	for suf < len(ta)-pre && suf < len(tb)-pre && ta[len(ta)-1-suf] == tb[len(tb)-1-suf] {
		suf++
	}
	d.emit("equal", strings.Join(ta[:pre], ""))
	ma, mb := ta[pre:len(ta)-suf], tb[pre:len(tb)-suf]
	switch {
	case len(ma) == 0:
		d.emit("insert", strings.Join(mb, ""))
	case len(mb) == 0:
		d.emit("delete", strings.Join(ma, ""))
	default:
		ia, ib := intern(ma, mb)
		script, ok := myers(ia, ib, maxWordEdits, &d.work, d.limit)
		if !ok {
			d.coarse = true
			d.emit("delete", strings.Join(ma, ""))
			d.emit("insert", strings.Join(mb, ""))
		} else {
			i, j := 0, 0
			// Deletions are emitted before insertions within one change.
			var del, ins strings.Builder
			flush := func() {
				d.emit("delete", del.String())
				d.emit("insert", ins.String())
				del.Reset()
				ins.Reset()
			}
			for _, op := range script {
				switch op {
				case opEqual:
					flush()
					d.emit("equal", ma[i])
					i++
					j++
				case opDelete:
					del.WriteString(ma[i])
					i++
				case opInsert:
					ins.WriteString(mb[j])
					j++
				}
			}
			flush()
		}
	}
	d.emit("equal", strings.Join(ta[len(ta)-suf:], ""))
}

// splitSentences splits after a sentence-ending mark and the spaces that follow it, so joining the pieces gives the text
// back exactly.
func splitSentences(s string) []string {
	var out []string
	start := 0
	for i := 0; i < len(s); i++ {
		if (s[i] == '.' || s[i] == '!' || s[i] == '?') && i+1 < len(s) && s[i+1] == ' ' {
			j := i + 1
			for j < len(s) && s[j] == ' ' {
				j++
			}
			out = append(out, s[start:j])
			start, i = j, j-1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// splitLines splits after each newline, so joining the pieces gives the text
// back exactly.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.SplitAfter(s, "\n")
}

// tokenize splits text into alternating runs of whitespace and non-whitespace,
// so joining the tokens gives the text back exactly and a word diff never
// changes spacing it did not have to.
func tokenize(s string) []string {
	var out []string
	start := 0
	space := false
	for i, r := range s {
		sp := unicode.IsSpace(r)
		if i == 0 {
			space = sp
			continue
		}
		if sp != space {
			out = append(out, s[start:i])
			start = i
			space = sp
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

func countWords(s string) int {
	n := 0
	for _, f := range strings.Fields(s) {
		if strings.ContainsFunc(f, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsNumber(r) }) {
			n++
		}
	}
	return n
}

// intern maps the strings of both sides to small integers so comparisons in the
// inner loop are integer compares.
func intern(a, b []string) (ia, ib []int) {
	ids := make(map[string]int, len(a)+len(b))
	conv := func(xs []string) []int {
		out := make([]int, len(xs))
		for i, x := range xs {
			id, ok := ids[x]
			if !ok {
				id = len(ids)
				ids[x] = id
			}
			out[i] = id
		}
		return out
	}
	return conv(a), conv(b)
}

type editOp uint8

const (
	opEqual editOp = iota
	opDelete
	opInsert
)

// myers returns the shortest edit script turning a into b, or ok=false when it
// needs more than maxD edits (or the shared work budget runs out). Deletions
// come before insertions at equal cost, which keeps the output stable.
func myers(a, b []int, maxD int, work *int, limit0 int) (script []editOp, ok bool) {
	n, m := len(a), len(b)
	limit := n + m
	if maxD < limit {
		limit = maxD
	}
	off := limit + 1
	v := make([]int, 2*limit+4)
	var trace [][]int32 // trace[d] is v as it stood when round d began, k in [-d-1, d+1]
	for d := 0; d <= limit; d++ {
		snap := make([]int32, 2*d+3)
		for i := range snap {
			snap[i] = int32(v[off-d-1+i])
		}
		trace = append(trace, snap)
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && v[off+k-1] < v[off+k+1]) {
				x = v[off+k+1]
			} else {
				x = v[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && a[x] == b[y] {
				x++
				y++
				*work++
			}
			*work++
			v[off+k] = x
			if x >= n && y >= m {
				return backtrack(trace, a, b, d), true
			}
		}
		if *work > limit0*2 {
			return nil, false
		}
	}
	return nil, false
}

func backtrack(trace [][]int32, a, b []int, dEnd int) []editOp {
	x, y := len(a), len(b)
	rev := make([]editOp, 0, len(a)+len(b))
	at := func(snap []int32, d, k int) int { return int(snap[k+d+1]) }
	for d := dEnd; d > 0; d-- {
		snap := trace[d]
		k := x - y
		var prevK int
		if k == -d || (k != d && at(snap, d, k-1) < at(snap, d, k+1)) {
			prevK = k + 1
		} else {
			prevK = k - 1
		}
		prevX := at(snap, d, prevK)
		prevY := prevX - prevK
		for x > prevX && y > prevY {
			rev = append(rev, opEqual)
			x--
			y--
		}
		if x == prevX {
			rev = append(rev, opInsert)
			y--
		} else {
			rev = append(rev, opDelete)
			x--
		}
	}
	for x > 0 && y > 0 {
		rev = append(rev, opEqual)
		x--
		y--
	}
	// reverse
	for i, j := 0, len(rev)-1; i < j; i, j = i+1, j-1 {
		rev[i], rev[j] = rev[j], rev[i]
	}
	return rev
}
