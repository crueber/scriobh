package pdfread

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

type margin struct {
	l, r float64
	just bool // most lines run to the right edge: the text is justified
}

type bmeta struct {
	first int     // global index of the block's first line
	size  float64 // heading: the line size
	label string  // "Chapter 3" held back from a heading
	lines int
}

type assembler struct {
	res   *Result
	pis   []*pageInfo
	lines []*pline
	body  float64
	lead  float64

	marg   map[[2]int]margin
	blocks []Block
	meta   []bmeta

	cur      *Block
	curMeta  *bmeta
	curMinX  float64 // leftmost line of the open paragraph
	curL     float64 // the left margin of its text block
	curLines int
	curBytes int     // text in the open paragraph
	itemX    float64 // the open list item's marker line
	lastNum  int     // the last numbered item
	bodyMono bool
	breaks   map[int]bool // lines a paragraph must break before (where a bookmark points)
	byPage   map[int][]*pline
	sorted   []firstAt // blocks by the first line they hold
	topY     float64   // where a page's text usually starts and ends
	botY     float64
	prev     *pline
	label    string
	labelAt  int
	queue    []*pnote
	byLabel  map[string]*pnote
}

func newAssembler(res *Result, pis []*pageInfo, lines []*pline, body, lead float64) *assembler {
	a := &assembler{res: res, pis: pis, lines: lines, body: body, lead: lead, marg: map[[2]int]margin{}, byLabel: map[string]*pnote{}}
	for _, pi := range pis {
		for _, n := range pi.notes {
			a.byLabel[n.label] = n
		}
	}
	a.breaks = map[int]bool{}
	a.byPage = map[int][]*pline{}
	for _, l := range lines {
		a.byPage[l.page] = append(a.byPage[l.page], l)
	}
	mono, all := 0, 0
	for _, l := range lines {
		n := len(l.text)
		all += n
		if l.mono {
			mono += n
		}
	}
	a.bodyMono = all > 0 && mono*2 > all
	var tops, bots []float64
	for _, ls := range a.byPage {
		if len(ls) >= 8 {
			tops = append(tops, ls[0].y)
			bots = append(bots, ls[len(ls)-1].y)
		}
	}
	if len(tops) >= 3 {
		a.topY, a.botY = median(tops), median(bots)
	}
	return a
}

// breakAt makes the line a bookmark points at begin a block of its own.
func (a *assembler) breakAt(outline []OutlineItem) {
	for _, it := range outline {
		if it.Page < 0 || it.Page >= len(a.pis) {
			continue
		}
		if li := a.lineAt(it); li >= 0 {
			a.breaks[li] = true
		}
	}
}

// margins of the text block a line sits in: the usual left edge, and the usual
// right edge of the lines that run to it. All the text blocks of a page are
// measured in one pass over its lines.
func (a *assembler) margins(l *pline) margin {
	k := [2]int{l.page, l.region}
	if m, ok := a.marg[k]; ok {
		return m
	}
	type acc struct {
		counts   map[int]int
		x0s, x1s []float64
	}
	regs := map[int]*acc{}
	for _, o := range a.pis[l.page].lines {
		if o.removed != "" || o.fnote || math.Abs(o.size-a.body) > 0.15*a.body {
			continue
		}
		r := regs[o.region]
		if r == nil {
			r = &acc{counts: map[int]int{}}
			regs[o.region] = r
		}
		r.counts[int(math.Round(o.x0))]++
		r.x1s = append(r.x1s, o.x1)
		r.x0s = append(r.x0s, o.x0)
	}
	for _, o := range a.pis[l.page].lines {
		if _, done := a.marg[[2]int{l.page, o.region}]; done {
			continue
		}
		m := margin{l: o.x0, r: o.x1}
		if r := regs[o.region]; r != nil && len(r.x0s) > 0 {
			bc := -1
			for x, c := range r.counts {
				if c > bc || c == bc && float64(x) < m.l {
					bc, m.l = c, float64(x)
				}
			}
			m.r = percentile(r.x1s, 0.9)
			near := 0
			for _, x := range r.x1s {
				if math.Abs(x-m.r) < 0.6*a.body {
					near++
				}
			}
			m.just = len(r.x1s) >= 6 && near*2 >= len(r.x1s)
		}
		a.marg[[2]int{l.page, o.region}] = m
	}
	return a.marg[k]
}

func (a *assembler) isHeadingLine(l *pline, next *pline) bool {
	if l.text == "" || utf8.RuneCountInString(l.text) > 160 {
		return false
	}
	if l.size >= 1.18*a.body {
		return true
	}
	// A bold line of its own, with no full stop: a run of text under a sub-heading rule.
	if l.bold && l.size >= 0.97*a.body && utf8.RuneCountInString(l.text) <= 80 {
		last, _ := utf8.DecodeLastRuneInString(l.text)
		if last != '.' && last != ',' && last != ';' && last != ':' {
			above := a.prev == nil || a.prev.page != l.page || a.prev.region != l.region || a.prev.y-l.y > 1.3*a.lead
			below := next == nil || next.page != l.page || next.region != l.region || l.y-next.y > 1.3*a.lead
			return above && below
		}
	}
	return false
}

func (a *assembler) flush() {
	if a.cur == nil {
		return
	}
	trimRuns(a.cur)
	if a.cur.Kind == BlockPara && a.curLines >= 2 && a.curMinX > a.curL+1.5*a.body {
		a.cur.Kind = BlockQuote
		a.res.Report.Quotes++
	}
	for _, r := range a.cur.Runs {
		if n := a.byLabel[r.Note]; r.Note != "" && n != nil {
			nr := append([]Run(nil), n.runs...)
			nb := Block{Runs: nr}
			trimRuns(&nb)
			a.cur.Notes = append(a.cur.Notes, Note{Label: n.label, Runs: nb.Runs})
		}
	}
	if len(a.cur.Runs) > 0 || a.cur.Kind == BlockScene {
		a.blocks = append(a.blocks, *a.cur)
		a.meta = append(a.meta, *a.curMeta)
	}
	a.cur, a.curMeta = nil, nil
	// notes whose markers were not found in the text follow the paragraph they sit under
	for _, n := range a.queue {
		p := Block{Kind: BlockPara, Page: n.page, Runs: append([]Run{{Text: n.marker + " ", Italic: true}}, n.runs...)}
		trimRuns(&p)
		a.blocks = append(a.blocks, p)
		a.meta = append(a.meta, bmeta{first: -1})
		a.res.Report.NotesUnlinked++
	}
	a.queue = a.queue[:0]
}

func trimRuns(b *Block) {
	for len(b.Runs) > 0 && b.Runs[len(b.Runs)-1].Note == "" && strings.TrimSpace(b.Runs[len(b.Runs)-1].Text) == "" {
		b.Runs = b.Runs[:len(b.Runs)-1]
	}
	if len(b.Runs) > 0 && b.Runs[len(b.Runs)-1].Note == "" {
		b.Runs[len(b.Runs)-1].Text = strings.TrimRight(b.Runs[len(b.Runs)-1].Text, " ")
	}
	for len(b.Runs) > 0 && b.Runs[0].Note == "" && strings.TrimSpace(b.Runs[0].Text) == "" {
		b.Runs = b.Runs[1:]
	}
	if len(b.Runs) > 0 && b.Runs[0].Note == "" {
		b.Runs[0].Text = strings.TrimLeft(b.Runs[0].Text, " ")
	}
}

func startsLower(s string) bool {
	for _, r := range s {
		return unicode.IsLower(r)
	}
	return false
}

func endsSentence(s string) bool {
	s = strings.TrimRight(s, " ")
	if s == "" {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(s)
	switch r {
	case '.', '!', '?', '"', '”', '’', '\'', ')', ']', '…', ':', ';', '—', '»':
		return true
	}
	return false
}

// runsFor makes a line's runs fit to put in a block: raised text that was not
// matched to a note becomes ordinary text.
func (a *assembler) runsFor(l *pline) []Run {
	var out []Run
	for _, r := range l.runs {
		if s, ok := isSupRun(r); ok {
			a.res.Report.NoteMarkersLeft++
			r = Run{Text: s}
		}
		r.Text = strings.ReplaceAll(r.Text, "\x01", " ")
		out = append(out, r)
	}
	return out
}

// appendLine adds a line to the open paragraph, joining a word split across
// lines only when that is safe.
func (a *assembler) appendLine(l *pline) {
	b, rep := a.cur, &a.res.Report
	runs := a.runsFor(l)
	if len(b.Runs) == 0 {
		b.Runs = runs
		return
	}
	last := &b.Runs[len(b.Runs)-1]
	if last.Note != "" || len(runs) == 0 {
		b.Runs = append(b.Runs, Run{Text: " "})
		b.Runs = append(b.Runs, runs...)
		return
	}
	t := last.Text
	first := ""
	if runs[0].Note == "" {
		first = runs[0].Text
	}
	switch {
	case strings.HasSuffix(t, "­"):
		last.Text = strings.TrimSuffix(t, "­")
		rep.SoftHyphens++
		rep.HyphenJoined++
		b.Dehyphenated++
	case strings.HasSuffix(t, "-") && len(strings.TrimSuffix(t, "-")) > 0:
		stem := strings.TrimSuffix(t, "-")
		w := lastWord(stem)
		sr, _ := utf8.DecodeLastRuneInString(stem)
		switch {
		case first != "" && utf8.RuneCountInString(w) >= 2 && unicode.IsLower(sr) && startsLower(first) && !noJoinPrefixes[strings.ToLower(w)]:
			last.Text = stem
			rep.HyphenJoined++
			b.Dehyphenated++
		default:
			rep.HyphenKept++ // a hyphen at a line's end that may be a real one stays, with no space after it
		}
	case strings.HasSuffix(t, "—") || strings.HasSuffix(t, "–"):
		// a dash at a line's end joins without a space
	default:
		last.Text = strings.TrimRight(t, " ") + " "
		if first != "" {
			runs[0].Text = strings.TrimLeft(first, " ")
		}
	}
	b.Runs = append(b.Runs, runs...)
	b.Runs = mergeAdjacent(b.Runs)
}

func mergeAdjacent(runs []Run) []Run {
	out := runs[:0]
	for _, r := range runs {
		if n := len(out); n > 0 && r.Note == "" && out[n-1].Note == "" && out[n-1].Bold == r.Bold && out[n-1].Italic == r.Italic && out[n-1].Mono == r.Mono {
			out[n-1].Text += r.Text
			continue
		}
		out = append(out, r)
	}
	return out
}

// A paragraph with no break in it is cut when it is very long (at the end of a sentence past the first, anywhere past the second), so that a
// page of text set with no indent and no space does not become one block of the whole book.
const (
	softParaBytes = 24 << 10
	maxParaBytes  = 160 << 10
)

// newPara says whether l begins a paragraph, given the line before it.
func (a *assembler) newPara(p, l *pline) bool {
	m := a.margins(l)
	sameBox := p.page == l.page && p.region == l.region
	indent := l.x0 > m.l+0.6*a.body && (!sameBox || math.Abs(p.x0-l.x0) > 0.4*a.body)
	if !sameBox {
		if p.page != l.page && a.topY > 0 && (p.y > a.botY+1.5*a.lead || l.y < a.topY-1.5*a.lead) {
			return true // a paragraph that runs on to the next page ends at the foot of one and starts at the head of the next
		}
		pm := a.margins(p)
		short := pm.just && p.x1 < pm.r-2.5*a.body && endsSentence(p.text)
		if p.page != l.page && short {
			return true
		}
		return indent || (p.page == l.page && short)
	}
	dy := p.y - l.y
	switch {
	case dy > 1.45*a.lead:
		return true
	case indent:
		return true
	case m.just && p.x1 < m.r-2.5*a.body && endsSentence(p.text) && l.x0 <= m.l+0.6*a.body && dy > 0.9*a.lead:
		return true
	}
	return false
}

func (a *assembler) run(ctx context.Context) error {
	rep := &a.res.Report
	// Notes found on a page whose marker was not linked wait for the paragraph that ends their page.
	pageNotes := func(pg int) {
		for _, n := range a.pis[pg].notes {
			if !n.linked {
				a.queue = append(a.queue, n)
			}
		}
	}
	var curPage = -1
	for i, l := range a.lines {
		if i&1023 == 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
		}
		var next *pline
		if i+1 < len(a.lines) {
			next = a.lines[i+1]
		}
		if l.page != curPage {
			if curPage >= 0 {
				pageNotes(curPage)
			}
			curPage = l.page
		}
		if a.breaks[l.idx] {
			a.flush()
			a.prev = nil
		}
		switch {
		case l.mono && !a.bodyMono && !isScene(l.text):
			a.pushLabel()
			if a.cur == nil || a.cur.Kind != BlockCode || a.prev == nil || a.prev.page != l.page || a.prev.y-l.y > 1.8*a.lead {
				a.flush()
				a.cur = &Block{Kind: BlockCode, Page: l.page}
				a.curMeta = &bmeta{first: l.idx}
			} else {
				a.cur.Runs = append(a.cur.Runs, Run{Text: "\n"})
			}
			a.cur.Runs = append(a.cur.Runs, Run{Text: strings.ReplaceAll(strings.TrimRight(l.rawText(), " "), "\x01", "  ")})
			rep.CodeLines++
			a.curMeta.lines++
			a.prev = l
			continue
		case isScene(l.text):
			a.flush()
			if n := len(a.blocks); n == 0 || a.blocks[n-1].Kind != BlockScene {
				a.blocks = append(a.blocks, Block{Kind: BlockScene, Page: l.page})
				a.meta = append(a.meta, bmeta{first: l.idx})
				rep.SceneBreaks++
			}
			a.prev = l
			continue
		case labelRe.MatchString(l.text):
			a.flush()
			if a.label != "" { // two labels in a row: the first stands alone
				a.pushLabel()
			}
			a.label, a.labelAt = titleCase(strings.TrimRight(strings.TrimSpace(l.text), ".")), l.idx
			a.curMeta = nil
			a.prev = l
			continue
		case a.isHeadingLine(l, next):
			if a.cur != nil && a.cur.Kind == BlockHeading && a.prev != nil && a.prev.page == l.page && a.prev.region == l.region &&
				math.Abs(a.prev.size-l.size) < 0.6 && a.prev.y-l.y < 1.8*l.size {
				a.cur.Runs = append(a.cur.Runs, Run{Text: " " + l.text})
				a.curMeta.lines++
				a.prev = l
				continue
			}
			a.flush()
			a.cur = &Block{Kind: BlockHeading, Page: l.page, Runs: []Run{{Text: l.text}}}
			a.curMeta = &bmeta{first: l.idx, size: l.size, label: a.label, lines: 1}
			if a.label != "" {
				a.curMeta.first = a.labelAt
				a.label = ""
			}
			a.prev = l
			continue
		case l.gaps >= 1 && a.tableRun(i):
			a.flush()
			a.pushLabel()
			var cells []Run
			for _, r := range l.runs {
				if _, ok := isSupRun(r); ok {
					continue
				}
				r.Text = strings.ReplaceAll(strings.ReplaceAll(r.Text, "\x01 ", " | "), "\x01", " | ")
				cells = append(cells, r)
			}
			a.blocks = append(a.blocks, Block{Kind: BlockRow, Page: l.page, Runs: cells})
			a.meta = append(a.meta, bmeta{first: l.idx})
			rep.TableRows++
			if n := len(rep.TablePages); n == 0 || rep.TablePages[n-1] != l.page+1 {
				if n < 100 {
					rep.TablePages = append(rep.TablePages, l.page+1)
				}
				a.pis[l.page].penalty(0.15, "table-like text was flattened to one paragraph per row")
			}
			a.prev = l
			continue
		}
		// body text
		a.pushLabel()
		if ord, num, rest, ok := itemOf(l, a.lastNum, a.cur != nil && a.cur.Kind == BlockItem); ok {
			a.flush()
			a.cur = &Block{Kind: BlockItem, Page: l.page, Ordered: ord, Num: num}
			a.curMeta = &bmeta{first: l.idx}
			a.itemX = l.x0
			if ord {
				a.lastNum = num
			} else {
				a.lastNum = 0
			}
			a.cur.Runs = a.runsAfterMarker(l, rest)
			rep.ListItems++
			a.curMeta.lines++
			a.prev = l
			continue
		}
		if a.cur != nil && a.cur.Kind == BlockItem && a.prev != nil && a.prev.page == l.page && a.prev.region == l.region && a.prev.y-l.y <= 1.3*a.lead && l.x0 >= a.itemX+0.3*a.body {
			a.appendLine(l) // the rest of a list item
			a.curMeta.lines++
			a.prev = l
			continue
		}
		if a.cur == nil || a.cur.Kind != BlockPara {
			a.flush()
			a.cur = &Block{Kind: BlockPara, Page: l.page}
			a.curMeta = &bmeta{first: l.idx}
			a.curMinX, a.curL, a.curLines, a.curBytes = l.x0, a.margins(l).l, 0, 0
			a.appendLine(l)
		} else if a.prev != nil && (a.newPara(a.prev, l) || a.curBytes > maxParaBytes || a.curBytes > softParaBytes && endsSentence(a.prev.text)) {
			// a long gap in the middle of a page, with room for a break, is a scene break
			gapScene := a.prev.page == l.page && a.prev.region == l.region && a.prev.y-l.y > 2.4*a.lead && !isScene(a.prev.text)
			a.flush()
			if gapScene {
				a.blocks = append(a.blocks, Block{Kind: BlockScene, Page: l.page})
				a.meta = append(a.meta, bmeta{first: l.idx})
				rep.SceneBreaks++
				rep.GapSceneBreaks++
			}
			a.cur = &Block{Kind: BlockPara, Page: l.page}
			a.curMeta = &bmeta{first: l.idx}
			a.curMinX, a.curL, a.curLines, a.curBytes = l.x0, a.margins(l).l, 0, 0
			a.appendLine(l)
		} else {
			if a.prev != nil && a.prev.page != l.page {
				rep.JoinedAcross++
			}
			a.curMinX = math.Min(a.curMinX, l.x0)
			a.appendLine(l)
		}
		a.curLines++
		a.curBytes += len(l.text)
		a.curMeta.lines++
		a.prev = l
	}
	if curPage >= 0 {
		pageNotes(curPage)
	}
	a.pushLabel()
	a.flush()
	return nil
}

func (a *assembler) pushLabel() {
	if a.label == "" {
		return
	}
	a.blocks = append(a.blocks, Block{Kind: BlockHeading, Level: 0, Runs: []Run{{Text: a.label}}, Page: a.pageOfIdx(a.labelAt)})
	a.meta = append(a.meta, bmeta{first: a.labelAt, size: -1, label: a.label, lines: 1})
	a.label = ""
}

func (a *assembler) pageOfIdx(i int) int {
	if i >= 0 && i < len(a.lines) {
		return a.lines[i].page
	}
	return 0
}

// tableRun says line i is one of at least three consecutive lines that are
// cells set at the same place.
func (a *assembler) tableRun(i int) bool {
	li := a.lines[i]
	near := func(o *pline) bool {
		return o.page == li.page && o.gaps >= 1 && (li.gaps >= 2 && o.gaps >= 2 || math.Abs(o.cut-li.cut) < 3)
	}
	cnt := 1
	for j := i - 1; j >= 0 && cnt < 3 && near(a.lines[j]); j-- {
		cnt++
	}
	for j := i + 1; j < len(a.lines) && cnt < 3 && near(a.lines[j]); j++ {
		cnt++
	}
	return cnt >= 3
}

var itemRe = regexp.MustCompile(`^([•·▪‣◦]|\d{1,3}[.)])\s+(\S.*)$`)

// itemOf says whether a line begins a list item: a bullet, or a number that
// follows the one before (or is 1).
func itemOf(l *pline, last int, inList bool) (ordered bool, num int, rest string, ok bool) {
	m := itemRe.FindStringSubmatch(l.text)
	if m == nil {
		return
	}
	if r := m[1]; r[0] >= '0' && r[0] <= '9' {
		n, _ := strconv.Atoi(strings.TrimRight(r, ".)"))
		if n != 1 && !(inList && n == last+1) {
			return
		}
		return true, n, m[2], true
	}
	return false, 0, m[2], true
}

// runsAfterMarker is a line's runs without the marker that opens it.
func (a *assembler) runsAfterMarker(l *pline, rest string) []Run {
	runs := a.runsFor(l)
	if len(runs) == 0 {
		return []Run{{Text: rest}}
	}
	first := runs[0].Text
	cut := len(first) - len(strings.TrimLeft(first, " "))
	t := strings.TrimLeft(first, " ")
	if m := itemRe.FindStringSubmatch(t + " x"); m != nil && len(t) >= len(m[1]) {
		t = strings.TrimLeft(t[len(m[1]):], " ")
	} else {
		// the marker is a run of its own
		runs = runs[1:]
		for len(runs) > 0 && strings.TrimSpace(runs[0].Text) == "" {
			runs = runs[1:]
		}
		if len(runs) > 0 {
			runs[0].Text = strings.TrimLeft(runs[0].Text, " ")
		}
		return runs
	}
	_ = cut
	runs[0].Text = t
	return runs
}

func titleCase(s string) string {
	f := strings.Fields(s)
	for i, w := range f {
		if i == 0 || true {
			rs := []rune(strings.ToLower(w))
			if len(rs) > 0 && !isRomanNumeral(w) {
				rs[0] = unicode.ToUpper(rs[0])
			} else if isRomanNumeral(w) {
				rs = []rune(strings.ToUpper(w))
			}
			f[i] = string(rs)
		}
	}
	return strings.Join(f, " ")
}

// ---- chapters ----------------------------------------------------------------------------

func normTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// cleanTitle drops "Chapter 3:" from a title, as the Word importer does.
func cleanTitle(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if m := chapPrefix.FindStringSubmatch(s); m != nil {
		return m[3]
	}
	return s
}

func (a *assembler) chapters(outline []OutlineItem) {
	rep := &a.res.Report
	defer func() {
		for _, c := range a.res.Chapters {
			a.countEmphasis(c.Blocks)
		}
		a.countEmphasis(a.res.Front)
		rep.finishDetail()
	}()
	// heading classes by size
	sizes := map[int]int{}
	for k, b := range a.blocks {
		if b.Kind == BlockHeading && a.meta[k].size > 0 {
			sizes[int(math.Round(a.meta[k].size*2))]++
		}
	}
	var keys []int
	for k := range sizes {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(keys)))
	rank := map[int]int{}
	for i, k := range keys {
		rank[k] = i + 1
		if i < 4 {
			rep.HeadingSizes = append(rep.HeadingSizes, float64(k)/2)
		}
	}
	for k := range a.blocks {
		if a.blocks[k].Kind == BlockHeading && a.meta[k].size > 0 {
			a.blocks[k].Level = rank[int(math.Round(a.meta[k].size*2))]
		}
	}
	// The title page: a lone largest heading on the first pages.
	a.titlePage(sizes, keys)
	if starts, ok := a.fromOutline(outline); ok {
		rep.ChapterSource = "outline"
		a.split(starts)
		return
	}
	starts := a.fromHeadings(keys, sizes)
	if len(starts) > 0 {
		rep.ChapterSource = "headings"
	} else {
		rep.ChapterSource = "none"
	}
	a.split(starts)
}

func (a *assembler) countEmphasis(bs []Block) {
	for _, b := range bs {
		if b.Kind != BlockPara {
			continue
		}
		for _, r := range b.Runs {
			if r.Bold {
				a.res.Report.BoldRuns++
			}
			if r.Italic {
				a.res.Report.ItalicRuns++
			}
		}
	}
}

type cstart struct {
	block int // index into a.blocks
	title string
	part  bool
	drop  bool // the block at block is the title: not repeated in the text
}

// titlePage removes a title page: the first page with few lines whose largest
// heading is not repeated. Its heading is the project's title when the file
// names none; the next short line is a subtitle.
func (a *assembler) titlePage(sizes map[int]int, keys []int) {
	if len(a.blocks) == 0 {
		return
	}
	pg := a.blocks[0].Page
	end := 0
	for end < len(a.blocks) && a.blocks[end].Page == pg {
		end++
	}
	if end > 8 || end == len(a.blocks) {
		return
	}
	head := -1
	for k := 0; k < end; k++ {
		if a.blocks[k].Kind == BlockHeading && a.meta[k].size > 0 && sizes[int(math.Round(a.meta[k].size*2))] == 1 {
			head = k
			break
		}
	}
	if head < 0 {
		return
	}
	// The first page must have no running paragraph of body length.
	for k := 0; k < end; k++ {
		if a.blocks[k].Kind == BlockPara && utf8.RuneCountInString(a.blocks[k].Text()) > 400 {
			return
		}
	}
	if a.res.Title == "" {
		a.res.Title = cleanTitle(a.blocks[head].Text())
	}
	for k := head + 1; k < end; k++ {
		t := a.blocks[k].Text()
		if a.res.Subtitle == "" && utf8.RuneCountInString(t) <= 120 && normTitle(t) != normTitle(a.res.Author) && normTitle(t) != normTitle(a.res.Title) && a.blocks[k].Kind != BlockScene {
			a.res.Subtitle = t
			break
		}
	}
	a.blocks = a.blocks[end:]
	a.meta = a.meta[end:]
	a.res.Report.Facts = append(a.res.Report.Facts, "The first page was read as a title page: its title and subtitle were used for the project and its text was not imported.")
}

func (a *assembler) fromHeadings(keys []int, sizes map[int]int) []cstart {
	var hs []int
	for k, b := range a.blocks {
		if b.Kind == BlockHeading {
			hs = append(hs, k)
		}
	}
	if len(hs) == 0 {
		return nil
	}
	// level counts
	cnt := map[int]int{}
	labelOnly := 0
	for _, k := range hs {
		if a.meta[k].size <= 0 {
			labelOnly++
			continue
		}
		cnt[a.blocks[k].Level]++
	}
	chapLevel, partLevel := 0, 0
	levels := []int{}
	for l := range cnt {
		levels = append(levels, l)
	}
	sort.Ints(levels)
	switch {
	case len(levels) == 0:
	case len(levels) == 1:
		chapLevel = levels[0]
	default:
		top, second := levels[0], levels[1]
		// A part heading is followed at once by a chapter heading.
		adj := 0
		for _, k := range hs {
			if a.blocks[k].Level == top && k+1 < len(a.blocks) && a.blocks[k+1].Kind == BlockHeading && a.blocks[k+1].Level == second {
				adj++
			}
		}
		if cnt[top] >= 1 && cnt[second] >= 2 && adj*100 >= 60*cnt[top] && cnt[top] < cnt[second] {
			partLevel, chapLevel = top, second
		} else if cnt[top] >= 2 || len(levels) == 2 && cnt[second] < 2 {
			chapLevel = top
		} else {
			chapLevel = second // a single largest heading that is not a title: a book title inside the text
		}
	}
	var out []cstart
	for _, k := range hs {
		b := a.blocks[k]
		part := false
		switch {
		case a.meta[k].size <= 0:
			l := strings.ToLower(b.Text())
			part = strings.HasPrefix(l, "part ") || strings.HasPrefix(l, "book ")
		case b.Level == chapLevel:
		case partLevel != 0 && b.Level == partLevel:
			part = true
		default:
			continue
		}
		out = append(out, cstart{block: k, title: cleanTitle(b.Text()), part: part, drop: true})
	}
	return out
}

// fromOutline uses the bookmarks as the chapter list when they say enough.
func (a *assembler) fromOutline(outline []OutlineItem) ([]cstart, bool) {
	rep := &a.res.Report
	var items []OutlineItem
	for _, it := range outline {
		if it.Page >= 0 && it.Page < len(a.pis) {
			items = append(items, it)
		}
	}
	if len(items) < 2 {
		return nil, false
	}
	count := map[int]int{}
	for _, it := range items {
		count[it.Level]++
	}
	// the level that holds the chapters
	partLevel, chapLevel := -1, -1
	switch {
	case count[0] >= 2:
		chapLevel = 0
		// Parts: a top level whose entries own chapters at the next.
		owns := 0
		for i, it := range items {
			if it.Level == 0 && i+1 < len(items) && items[i+1].Level == 1 {
				owns++
			}
		}
		if owns >= 2 && owns*2 >= count[0] && count[1] >= count[0] {
			partLevel, chapLevel = 0, 1
		}
	case count[0] == 1 && count[1] >= 2:
		chapLevel = 1
	default:
		return nil, false
	}
	var out []cstart
	for k, it := range items {
		isPart := it.Level == partLevel && partLevel >= 0
		// A top-level entry that owns no chapters (a dedication, an afterword) is a section of its own, not a part.
		if isPart && !(k+1 < len(items) && items[k+1].Level == chapLevel) {
			isPart = false
			if it.Level != chapLevel {
				out = append(out, a.outlineStart(it, false)...)
				continue
			}
		}
		if it.Level != chapLevel && !isPart {
			continue
		}
		out = append(out, a.outlineStart(it, isPart)...)
	}
	if len(out) < 2 {
		return nil, false
	}
	// Bookmarks out of reading order are put in it; two at the same place are one.
	sort.SliceStable(out, func(i, j int) bool { return out[i].block < out[j].block })
	dedup := out[:0]
	for _, s := range out {
		if n := len(dedup); n > 0 && dedup[n-1].block == s.block {
			if s.part && !dedup[n-1].part {
				dedup[n-1] = s
			}
			continue
		}
		dedup = append(dedup, s)
	}
	out = dedup
	rep.OutlineUsed = len(out)
	// The heading the bookmark points at is its title, not text.
	for i := range out {
		b := a.blocks[out[i].block]
		if b.Kind == BlockHeading {
			nt, ot := normTitle(b.Text()), normTitle(out[i].title)
			if nt != "" && ot != "" && (strings.Contains(ot, nt) || strings.Contains(nt, ot)) || a.meta[out[i].block].size <= 0 {
				out[i].drop = true
			}
		}
	}
	return out, true
}

// outlineStart makes the chapter start a bookmark gives, or none when it does not resolve.
func (a *assembler) outlineStart(it OutlineItem, part bool) []cstart {
	li := a.lineAt(it)
	if li < 0 {
		return nil
	}
	bi := a.blockFrom(li)
	if bi < 0 {
		return nil
	}
	return []cstart{{block: bi, title: cleanTitle(it.Title), part: part}}
}

// lineAt finds the first line a bookmark points at: on its page, the first
// line at or below the target's height (the page's first line without one).
func (a *assembler) lineAt(it OutlineItem) int {
	for pg := it.Page; pg < len(a.pis) && pg <= it.Page+3; pg++ {
		for _, l := range a.byPage[pg] {
			if pg == it.Page && it.Y >= 0 && l.y > it.Y+0.8*l.size {
				continue
			}
			return l.idx
		}
	}
	return -1
}

type firstAt struct{ line, block int }

// blockFrom is the first block that begins at or after a line.
func (a *assembler) blockFrom(line int) int {
	if a.sorted == nil {
		for k, m := range a.meta {
			if m.first >= 0 {
				a.sorted = append(a.sorted, firstAt{m.first, k})
			}
		}
		sort.SliceStable(a.sorted, func(i, j int) bool { return a.sorted[i].line < a.sorted[j].line })
		if a.sorted == nil {
			a.sorted = []firstAt{}
		}
	}
	i := sort.Search(len(a.sorted), func(i int) bool { return a.sorted[i].line >= line })
	if i == len(a.sorted) {
		return -1
	}
	best := a.sorted[i].block
	for j := i + 1; j < len(a.sorted) && a.sorted[j].line == a.sorted[i].line; j++ {
		best = min(best, a.sorted[j].block)
	}
	return best
}

// split cuts the blocks at the starts into the front matter and the chapters.
func (a *assembler) split(starts []cstart) {
	res := a.res
	if len(starts) == 0 {
		var all []Block
		all = append(all, a.blocks...)
		res.Chapters = []Chapter{{Title: firstText(res.Title, "Imported text"), Blocks: a.downlevel(all, 0), Source: "none"}}
		return
	}
	res.Front = a.downlevel(a.blocks[:starts[0].block], 0)
	for i, s := range starts {
		end := len(a.blocks)
		if i+1 < len(starts) {
			end = starts[i+1].block
		}
		from := s.block
		if s.drop {
			from++
		}
		if from > end {
			from = end
		}
		ch := Chapter{Title: s.title, Part: s.part, Page: a.blocks[s.block].Page, Source: res.Report.ChapterSource}
		if ch.Title == "" {
			ch.Title = fmt.Sprintf("Untitled %d", i+1)
		}
		ch.Blocks = a.downlevel(a.blocks[from:end], 1)
		res.Chapters = append(res.Chapters, ch)
	}
	// text before the first chapter that is only a scene break or empty is nothing
	if len(res.Front) == 1 && res.Front[0].Kind == BlockScene {
		res.Front = nil
	}
}

func firstText(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// downlevel copies blocks, putting headings inside a chapter at level 2 and
// below (the chapter's own title is the first level).
func (a *assembler) downlevel(bs []Block, base int) []Block {
	out := make([]Block, len(bs))
	copy(out, bs)
	for i := range out {
		if out[i].Kind == BlockHeading {
			l := out[i].Level
			if l < 1 {
				l = 1
			}
			out[i].Level = min(l+1, 6)
		}
	}
	return out
}
