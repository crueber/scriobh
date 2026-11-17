package editiondiff

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/domain/revisions"
)

// Limits bound the cost of a comparison. They are fields so a test can shrink
// them; Defaults are what the ops use.
type Limits struct {
	// MaxUnits is the most chapters one side may have (an edition records at most 5,000).
	MaxUnits int
	// MaxChapterBytes is the largest chapter text that is diffed. A larger one is compared by equality alone.
	MaxChapterBytes int
	// MaxLines is the most lines (paragraphs) of one chapter that are diffed on one side: the diff holds the lines of
	// both texts in memory, and a megabyte of one-letter paragraphs is a million of them.
	MaxLines int
	// MaxTotalBytes is the most text one side may hold at all; more is refused, before any matching.
	MaxTotalBytes int
	// FingerprintBudget is the number of shingles the two sides together may keep: past it only a fraction of the
	// shingles, chosen by their hash, is kept (the same fraction on both sides).
	FingerprintBudget int
	// PairBudget is how many (shingle, chapter) pairs the index may walk to find partners.
	PairBudget int
	// DiffBudget is the work of every diff of the comparison together (see revisions.Budget). The linear cost of a
	// chapter's lines and bytes is charged to it too.
	DiffBudget int
	// MinSharedToDiff: when the two sides share less than one part in this many of their words (and hold at least
	// MinWordsForTooDifferent between them), no chapter is diffed.
	MinSharedToDiff         int
	MinWordsForTooDifferent int
	// Join puts the chapters of a merged step back together on the first text's side; the import's own merge by
	// default.
	Join func(a, b string) string
}

// Defaults are the limits of the ops.
func Defaults() Limits {
	return Limits{
		MaxUnits:                5000,
		MaxChapterBytes:         2 << 20,
		MaxLines:                250_000,
		MaxTotalBytes:           96 << 20,
		FingerprintBudget:       600_000,
		PairBudget:              8_000_000,
		DiffBudget:              60_000_000,
		MinSharedToDiff:         10,
		MinWordsForTooDifferent: 2000,
		Join:                    joinPlain,
	}
}

func joinPlain(a, b string) string {
	if strings.TrimSpace(b) == "" {
		return a
	}
	if strings.TrimSpace(a) == "" {
		return b
	}
	return strings.TrimRight(a, "\n") + "\n\n" + strings.TrimLeft(b, "\n")
}

// ErrTooLarge says a side is larger than a comparison takes.
var ErrTooLarge = errors.New("editiondiff: too large to compare")

// Input is two texts of a book as lists of chapters in reading order.
type Input struct {
	Base, Head []Unit
	// ProjectMode: the first list is an edition's chapters with their recorded mapping, the second the documents
	// of the project, with their source paths.
	ProjectMode bool
	// Detail is the key of the step whose hunks are wanted; empty for none.
	Detail string
	Limits Limits
}

// Compare matches the chapters of the two lists and diffs the pairs.
func Compare(ctx context.Context, in Input) (*Result, error) {
	lim := in.Limits
	if lim.MaxUnits == 0 {
		lim = Defaults()
	}
	if lim.Join == nil {
		lim.Join = joinPlain
	}
	if len(in.Base) > lim.MaxUnits || len(in.Head) > lim.MaxUnits {
		return nil, fmt.Errorf("%w: more than %d chapters on a side", ErrTooLarge, lim.MaxUnits)
	}
	var tb, th int
	for i := range in.Base {
		tb += len(in.Base[i].Text)
	}
	for i := range in.Head {
		th += len(in.Head[i].Text)
	}
	if tb > lim.MaxTotalBytes || th > lim.MaxTotalBytes {
		return nil, fmt.Errorf("%w: more than %d MiB of text on a side", ErrTooLarge, lim.MaxTotalBytes>>20)
	}
	c := &comparer{ctx: ctx, in: in, lim: lim, budget: &revisions.Budget{Work: lim.DiffBudget}}
	return c.run()
}

type comparer struct {
	ctx    context.Context
	in     Input
	lim    Limits
	base   []*unit
	head   []*unit
	budget *revisions.Budget
	spent  bool
}

func (c *comparer) prepare(us []Unit) []*unit {
	out := make([]*unit, len(us))
	for i := range us {
		u := &unit{Unit: us[i], idx: i}
		u.trimmed = trimTail(u.Text)
		u.tkey = titleKey(u.Title)
		u.words = countWords(u.Text)
		out[i] = u
	}
	return out
}

func (c *comparer) run() (*Result, error) {
	c.base, c.head = c.prepare(c.in.Base), c.prepare(c.in.Head)
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	// The same sampling step on both sides.
	var wb, wh int
	for _, u := range c.base {
		wb += u.words
	}
	for _, u := range c.head {
		wh += u.words
	}
	step := uint32(1)
	if tot := wb + wh; tot > c.lim.FingerprintBudget {
		step = uint32((tot + c.lim.FingerprintBudget - 1) / c.lim.FingerprintBudget)
	}
	for _, side := range [][]*unit{c.base, c.head} {
		for _, u := range side {
			if len(u.Text) <= c.lim.MaxChapterBytes*2 {
				u.fp = fingerprint(u.Text, step)
			}
			if err := c.ctx.Err(); err != nil {
				return nil, err
			}
		}
	}
	m := newMatcher(c.ctx, c.base, c.head, c.in.ProjectMode, c.lim.PairBudget)
	m.run()
	if err := c.ctx.Err(); err != nil {
		return nil, err
	}
	res := &Result{Mode: "editions", Steps: []Step{}}
	if c.in.ProjectMode {
		res.Mode = "project"
	}
	if m.limited {
		res.Notices = append(res.Notices, "The books repeat so much text that some chapters could only be paired by name.")
	}
	if step > 1 {
		res.Notices = append(res.Notices, fmt.Sprintf("The books are very large: chapters were paired by a sample of one run of words in %d.", step))
	}
	steps := c.groups(m)
	// How much of each book is paired at all: two texts are too different when neither has a tenth of its words in a
	// paired chapter (a small edition in a large project, or a project cut to a few chapters, is covered on one side).
	var pb, ph int
	for _, s := range steps {
		if len(s.baseKeys) > 0 && len(s.headKeys) > 0 {
			for _, i := range s.baseKeys {
				pb += c.base[i].words
			}
			for _, i := range s.headKeys {
				ph += c.head[i].words
			}
		}
	}
	if wb+wh >= c.lim.MinWordsForTooDifferent && pb*c.lim.MinSharedToDiff < wb && ph*c.lim.MinSharedToDiff < wh {
		res.TooDifferent = true
	}
	c.order(steps, m)
	for i := range steps {
		if err := c.ctx.Err(); err != nil {
			return nil, err
		}
		c.diffStep(&steps[i], res.TooDifferent)
	}
	for i := range steps {
		steps[i].Index = i + 1
		steps[i].Key = c.keyOf(&steps[i])
	}
	res.Steps = steps
	res.Summary = summarize(steps, wb, wh)
	res.Partial = c.spent
	if res.Partial {
		res.Notices = append(res.Notices, "The comparison used up its budget: some chapters differ but were not compared word by word.")
	}
	if res.TooDifferent {
		res.Notices = append(res.Notices, "These two texts have too little in common to compare chapter by chapter. Their chapters are listed as paired, added or removed, and none was compared word by word.")
	}
	return res, nil
}

// spent is set when a step was left undiffed for want of budget.
func (c *comparer) setSpent() { c.spent = true }

func (c *comparer) keyOf(s *Step) string {
	var b strings.Builder
	if len(s.Base) > 0 {
		b.WriteString("b" + strconv.Itoa(s.Base[0].Ord))
	}
	b.WriteString(":")
	if len(s.Head) > 0 {
		b.WriteString("h" + c.head[s.headKeys[0]].Key)
	}
	return b.String()
}

// groups turns the pairing into steps: the connected pieces of the pairing (a split and a merge are pieces with more
// than one chapter on one side), then the chapters left alone.
func (c *comparer) groups(m *matcher) []Step {
	nb, nh := len(c.base), len(c.head)
	headsOf := make([][]int, nb) // heads linked to a base
	basesOf := make([][]int, nh)
	for b := 0; b < nb; b++ {
		if h := m.bm[b]; h >= 0 {
			headsOf[b] = append(headsOf[b], h)
			basesOf[h] = append(basesOf[h], b)
		}
	}
	for h := 0; h < nh; h++ {
		if b := m.hm[h]; b >= 0 && m.bm[b] != h {
			headsOf[b] = append(headsOf[b], h)
			basesOf[h] = append(basesOf[h], b)
		}
	}
	seenB, seenH := make([]bool, nb), make([]bool, nh)
	var steps []Step
	var visit func(b, h int, bs, hs *[]int)
	visit = func(b, h int, bs, hs *[]int) {
		if b >= 0 {
			if seenB[b] {
				return
			}
			seenB[b] = true
			*bs = append(*bs, b)
			for _, x := range headsOf[b] {
				visit(-1, x, bs, hs)
			}
			return
		}
		if seenH[h] {
			return
		}
		seenH[h] = true
		*hs = append(*hs, h)
		for _, x := range basesOf[h] {
			visit(x, -1, bs, hs)
		}
	}
	for b := 0; b < nb; b++ {
		if seenB[b] {
			continue
		}
		var bs, hs []int
		visit(b, -1, &bs, &hs)
		sort.Ints(bs)
		sort.Ints(hs)
		st := Step{baseKeys: bs, headKeys: hs}
		if len(hs) > 0 {
			// the pairing is as sure as its weakest member: a merged or split step that has one guessed chapter in it is a guess
			var infos []info
			for _, b := range bs {
				infos = append(infos, m.binfo[b])
			}
			if len(hs) > 1 {
				for _, h := range hs {
					infos = append(infos, m.hinfo[h])
				}
			}
			in := weakest(infos)
			st.Match = &Match{Method: in.method, Score: round3(in.score), Guess: in.guess}
		}
		steps = append(steps, st)
	}
	for h := 0; h < nh; h++ {
		if !seenH[h] {
			steps = append(steps, Step{headKeys: []int{h}})
		}
	}
	return steps
}

var methodRank = map[string]int{MethodRecorded: 0, MethodPath: 1, MethodTitle: 2, MethodSimilarity: 3}

// weakest is the pairing of a group of chapters: the weakest method of its members, the lowest score, and a guess if
// any member is one.
func weakest(infos []info) info {
	out := infos[0]
	for _, in := range infos[1:] {
		if methodRank[in.method] > methodRank[out.method] {
			out.method = in.method
		}
		if in.score < out.score {
			out.score = in.score
		}
		out.guess = out.guess || in.guess
	}
	return out
}

func round3(f float64) float64 { return float64(int(f*1000+0.5)) / 1000 }

// order puts the steps in the reading order of the second text; a removed chapter follows the nearest chapter before
// it that still has a place.
func (c *comparer) order(steps []Step, m *matcher) {
	pos := make([]float64, len(steps))
	headPos := make([]float64, len(c.base)) // position in the second text of each paired chapter of the first
	for i := range headPos {
		headPos[i] = -1
	}
	for i := range steps {
		if len(steps[i].headKeys) > 0 {
			pos[i] = float64(steps[i].headKeys[0])
			for _, b := range steps[i].baseKeys {
				headPos[b] = pos[i]
			}
		}
	}
	for i := range steps {
		if len(steps[i].headKeys) > 0 {
			continue
		}
		b := steps[i].baseKeys[0]
		p := -1.0
		for j := b - 1; j >= 0; j-- {
			if headPos[j] >= 0 {
				p = headPos[j]
				break
			}
		}
		pos[i] = p + 0.5 + float64(b)/float64(len(c.base)+1)*0.001
	}
	idx := make([]int, len(steps))
	for i := range idx {
		idx[i] = i
	}
	sort.SliceStable(idx, func(a, b int) bool { return pos[idx[a]] < pos[idx[b]] })
	out := make([]Step, len(steps))
	for i, j := range idx {
		out[i] = steps[j]
	}
	copy(steps, out)
	c.flagMoves(steps, m)
}

// flagMoves marks the paired chapters whose place changed: those outside the longest run of pairs that keep their
// order, and those whose parent is a different chapter.
func (c *comparer) flagMoves(steps []Step, m *matcher) {
	type pr struct{ step, base, head int }
	var prs []pr
	for i := range steps {
		if len(steps[i].baseKeys) > 0 && len(steps[i].headKeys) > 0 {
			prs = append(prs, pr{i, steps[i].baseKeys[0], steps[i].headKeys[0]})
		}
	}
	sort.Slice(prs, func(a, b int) bool { return prs[a].base < prs[b].base })
	// longest increasing subsequence of head positions, in base order
	n := len(prs)
	tails := make([]int, 0, n)
	prev := make([]int, n)
	at := make([]int, 0, n)
	for i := 0; i < n; i++ {
		x := prs[i].head
		j := sort.Search(len(tails), func(k int) bool { return prs[tails[k]].head >= x })
		if j > 0 {
			prev[i] = tails[j-1]
		} else {
			prev[i] = -1
		}
		if j == len(tails) {
			tails = append(tails, i)
		} else {
			tails[j] = i
		}
		at = append(at, j)
	}
	keep := make([]bool, n)
	if len(tails) > 0 {
		for i := tails[len(tails)-1]; i >= 0; i = prev[i] {
			keep[i] = true
		}
	}
	for i, p := range prs {
		if !keep[i] {
			steps[p.step].Moved = true
		}
	}
	// a different parent
	for _, p := range prs {
		b, h := c.base[p.base], c.head[p.head]
		if b.Parent >= 0 && h.Parent >= 0 {
			pb, ph := m.bm[b.Parent], h.Parent
			if pb >= 0 && pb != ph {
				steps[p.step].Moved = true
			}
		} else if b.Parent >= 0 && h.Parent < 0 && m.bm[b.Parent] >= 0 {
			steps[p.step].Moved = true
		}
	}
}

func refOf(u *unit) Ref {
	return Ref{Ord: u.Ord, DocumentID: u.ID, Kind: u.Kind, Title: u.Title, SourcePath: u.Path, Words: u.words}
}

// diffStep fills a step from its chapters: refs, words, status, and the diff of the text.
func (c *comparer) diffStep(s *Step, tooDifferent bool) {
	var btxt, htxt string
	for i, k := range s.baseKeys {
		u := c.base[k]
		s.Base = append(s.Base, refOf(u))
		if i == 0 {
			btxt = u.Text
		} else {
			btxt = c.lim.Join(btxt, u.Text)
		}
		s.Words.Base += u.words
	}
	for i, k := range s.headKeys {
		u := c.head[k]
		s.Head = append(s.Head, refOf(u))
		if i == 0 {
			htxt = u.Text
		} else {
			htxt = joinPlain(htxt, u.Text)
		}
		s.Words.Head += u.words
	}
	if s.Base == nil {
		s.Base = []Ref{}
	}
	if s.Head == nil {
		s.Head = []Ref{}
	}
	if s.Match != nil {
		// the title as written: a change of case or punctuation is a change (titleKey is for matching only)
		s.Renamed = strings.Join(strings.Fields(s.Base[0].Title), " ") != strings.Join(strings.Fields(s.Head[0].Title), " ")
		if sm, ok := c.pairSim(s); ok {
			s.Match.Similarity = round3(sm)
		}
	}
	switch {
	case len(s.baseKeys) == 0:
		s.Status, s.Compared = StatusAdded, ComparedNone
		s.Words.Added = s.Words.Head
		return
	case len(s.headKeys) == 0:
		s.Status, s.Compared = StatusRemoved, ComparedNone
		s.Words.Removed = s.Words.Base
		return
	}
	b, h := trimTail(btxt), trimTail(htxt)
	one := len(s.baseKeys) == 1 && len(s.headKeys) == 1
	switch {
	case len(s.baseKeys) > 1:
		s.Status = StatusMerged
	case len(s.headKeys) > 1:
		s.Status = StatusSplit
	}
	if b == h {
		s.Compared = ComparedEqual
		if one {
			s.Status = StatusUnchanged
			if s.Moved && !s.Renamed {
				s.Status = StatusMoved
			} else if s.Renamed {
				s.Status = StatusEdited
			}
		}
		return
	}
	if one {
		s.Status = StatusEdited
	}
	if tooDifferent {
		s.Compared = ComparedSkipped
		return
	}
	if len(b) > c.lim.MaxChapterBytes || len(h) > c.lim.MaxChapterBytes {
		s.Compared, s.Note = ComparedNot, "A chapter is larger than a diff takes; the texts differ and were not compared."
		return
	}
	if b != "" {
		b += "\n" // every line ends the same way, so the last paragraph is not a change for want of a newline
	}
	if h != "" {
		h += "\n"
	}
	lb, lh := strings.Count(b, "\n"), strings.Count(h, "\n")
	if lb > c.lim.MaxLines || lh > c.lim.MaxLines {
		s.Compared, s.Note = ComparedNot, "A chapter has more paragraphs than a diff takes; the texts differ and were not compared."
		return
	}
	charge := 8*(lb+lh+2) + (len(b)+len(h))/16
	if c.budget.Work < charge+1000 {
		s.Compared, s.Note = ComparedNot, "The comparison used up its budget before this chapter."
		c.setSpent()
		return
	}
	c.budget.Work -= charge
	hunks, coarse := c.hunks(b, h, c.in.Detail != "" && c.in.Detail == c.keyOfPre(s))
	s.Compared = ComparedFull
	if coarse {
		s.Compared, s.Note = ComparedCoarse, "A size limit stopped part of this chapter being compared word by word; those changes are shown as larger ones."
	}
	for i := range hunks {
		s.Words.Added += hunks[i].Words.Added
		s.Words.Removed += hunks[i].Words.Removed
	}
	s.Changes = len(hunks)
	if c.in.Detail != "" && c.in.Detail == c.keyOfPre(s) {
		s.Hunks = hunks
	}
	if len(hunks) == 0 { // the texts differ in spacing only
		s.Compared = ComparedEqual
		s.Note = "The texts differ only in spacing."
		if one {
			s.Status = StatusUnchanged
			if s.Moved && !s.Renamed {
				s.Status = StatusMoved
			} else if s.Renamed {
				s.Status = StatusEdited
			}
		}
	}
}

// keyOfPre is the key of a step before its refs are final (the same text keyOf makes).
func (c *comparer) keyOfPre(s *Step) string {
	var b strings.Builder
	if len(s.baseKeys) > 0 {
		b.WriteString("b" + strconv.Itoa(c.base[s.baseKeys[0]].Ord))
	}
	b.WriteString(":")
	if len(s.headKeys) > 0 {
		b.WriteString("h" + c.head[s.headKeys[0]].Key)
	}
	return b.String()
}

// pairSim is the similarity of the paired chapters of a step: of the first to the first, or of the whole.
func (c *comparer) pairSim(s *Step) (float64, bool) {
	if len(s.baseKeys) == 1 && len(s.headKeys) == 1 {
		b, h := c.base[s.baseKeys[0]], c.head[s.headKeys[0]]
		if b.trimmed == h.trimmed {
			return 1, b.trimmed != ""
		}
		if len(b.fp) >= minShingles && len(h.fp) >= minShingles {
			return dice(inter(b.fp, h.fp), len(b.fp), len(h.fp)), true
		}
		return 0, false
	}
	var fb, fh [][]uint32
	for _, k := range s.baseKeys {
		fb = append(fb, c.base[k].fp)
	}
	for _, k := range s.headKeys {
		fh = append(fh, c.head[k].fp)
	}
	a, b := mergeFP(fb), mergeFP(fh)
	if len(a) < minShingles || len(b) < minShingles {
		return 0, false
	}
	return dice(inter(a, b), len(a), len(b)), true
}

func mergeFP(parts [][]uint32) []uint32 {
	var all []uint32
	for _, p := range parts {
		all = append(all, p...)
	}
	sort.Slice(all, func(i, j int) bool { return all[i] < all[j] })
	out := all[:0]
	for i, v := range all {
		if i == 0 || v != all[i-1] {
			out = append(out, v)
		}
	}
	return out
}

// hunks diffs two texts paragraph by paragraph, then word by word inside each changed run, within the budget. With
// detail it keeps the words of each hunk and where it sits in the second text.
func (c *comparer) hunks(b, h string, detail bool) ([]Hunk, bool) {
	r := revisions.DiffWithin(b, h, revisions.UnitParagraph, c.budget)
	coarse := r.Coarse
	var out []Hunk
	hpos := 0
	var del, ins strings.Builder
	hstart := 0
	flush := func(before string) {
		d, i := del.String(), ins.String()
		del.Reset()
		ins.Reset()
		if strings.TrimSpace(d) == "" && strings.TrimSpace(i) == "" {
			return
		}
		hk := Hunk{Index: len(out) + 1, HeadStart: hstart, HeadEnd: hstart + len(i)}
		switch {
		case strings.TrimSpace(d) != "" && strings.TrimSpace(i) != "":
			hk.Kind = HunkReplace
		case strings.TrimSpace(i) != "": // the first text has only blank lines here
			hk.Kind = HunkAdded
		default:
			hk.Kind = HunkRemoved
		}
		switch hk.Kind {
		case HunkReplace:
			c.budget.Work -= (len(d) + len(i)) / 16
			if c.budget.Work > 1000 {
				w := revisions.DiffWithin(d, i, revisions.UnitWord, c.budget)
				hk.Words = Words{Added: w.Stats.AddedWords, Removed: w.Stats.RemovedWords}
				if w.Coarse {
					coarse = true
				}
				if detail {
					for _, sg := range w.Segments {
						hk.Segments = append(hk.Segments, Segment{Op: sg.Op, Text: sg.Text})
					}
				}
			} else {
				coarse = true
				hk.Words = Words{Added: countWords(i), Removed: countWords(d)}
				if detail {
					hk.Segments = []Segment{{Op: "delete", Text: d}, {Op: "insert", Text: i}}
				}
			}
		case HunkAdded:
			hk.Words.Added = countWords(i)
		default:
			hk.Words.Removed = countWords(d)
		}
		if detail {
			hk.Base, hk.Head, hk.Before = d, i, before
		}
		sum := sha256.Sum256([]byte(d + "\x00" + i))
		hk.ID = hex.EncodeToString(sum[:6])
		out = append(out, hk)
	}
	started := false
	lastEqual := ""
	for _, sg := range r.Segments {
		switch sg.Op {
		case "equal":
			if started {
				flush(lastEqual)
				started = false
			}
			lastEqual = lastParagraph(sg.Text)
			hpos += len(sg.Text)
		case "delete":
			if !started {
				started, hstart = true, hpos
			}
			del.WriteString(sg.Text)
		case "insert":
			if !started {
				started, hstart = true, hpos
			}
			ins.WriteString(sg.Text)
			hpos += len(sg.Text)
		}
	}
	if started {
		flush(lastEqual)
	}
	// ids repeat when the same difference is made twice: number the repeats
	seen := map[string]int{}
	for i := range out {
		seen[out[i].ID]++
		if n := seen[out[i].ID]; n > 1 {
			out[i].ID += "-" + strconv.Itoa(n)
		}
	}
	return out, coarse
}

// lastParagraph is the last non-blank line of a run of text, shortened.
func lastParagraph(s string) string {
	s = strings.TrimRight(s, "\n ")
	if i := strings.LastIndex(s, "\n"); i >= 0 {
		s = s[i+1:]
	}
	if len(s) > 200 {
		s = s[len(s)-200:]
		for len(s) > 0 && s[0]&0xC0 == 0x80 {
			s = s[1:]
		}
	}
	return s
}

func summarize(steps []Step, wb, wh int) Summary {
	s := Summary{Steps: len(steps), BaseWords: wb, HeadWords: wh}
	for _, st := range steps {
		switch st.Status {
		case StatusUnchanged:
			s.Unchanged++
		case StatusEdited:
			s.Edited++
		case StatusMoved:
			s.Moved++
		case StatusSplit:
			s.Split++
		case StatusMerged:
			s.Merged++
		case StatusAdded:
			s.Added++
		case StatusRemoved:
			s.Removed++
		}
		if st.Compared == ComparedNot || st.Compared == ComparedSkipped {
			s.Uncounted++
		} else {
			s.WordsAdded += st.Words.Added
			s.WordsRemoved += st.Words.Removed
		}
		s.Changes += st.Changes
	}
	return s
}
