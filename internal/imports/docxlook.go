package imports

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/ops"
)

// Looking inside one chapter of a Word document: the paragraphs it could be split at, and what could start a chapter inside it.
//
// A look re-reads the staged file and runs the same cut the preview ran, so what it lists is the chapter as the preview shows it, and it
// writes nothing. It is asked for a chapter by its key (the file the cut made), a page at a time, so the text of a long chapter is
// never sent to the page whole: 40 paragraphs, each one line of 80 characters.

// Look modes.
const (
	LookSplit  = "split"  // the chapter's paragraphs, to split it at one
	LookInside = "inside" // what could start a chapter inside it
	lookRule   = "rule"   // the ordinals of one rule inside it (a bulk add)
)

// LookPageMax is the most a look lists at a time.
const (
	LookPageDefault = 100
	LookPageMax     = 1000
)

// lookAsk is what a look asks of the cut, and where it puts what it found.
type lookAsk struct {
	key          string
	mode         string
	rule         string
	after, count int
	size         int // piece size, for the size rule

	p    *piece   // the chapter, found by its path
	out  *LookOut // the answer
	ords []int    // for lookRule
}

// LookPara is a paragraph a chapter can be split at.
type LookPara struct {
	Ordinal int    `json:"ordinal" desc:"The paragraph's index among the document's paragraphs: what docx_breaks and the break change take."`
	Line    string `json:"line" desc:"The start of the paragraph, 80 characters at most. It is the manuscript's text: show it as text."`
	Words   int    `json:"words" desc:"How many words of the chapter come before it."`
	Title   bool   `json:"title,omitempty" desc:"A chapter started here takes this line as its title (it is short and looks like one), so the line leaves the text."`
}

// LookFound is how many places of one kind could start a chapter inside the chapter.
type LookFound struct {
	Rule  string `json:"rule" enum:"headings,page_breaks,section_breaks,labels,numerals,scenes,size"`
	Count int    `json:"count"`
}

// LookSug is a place a chapter could start inside a chapter.
type LookSug struct {
	Ordinal int      `json:"ordinal"`
	Rule    string   `json:"rule" enum:"headings,page_breaks,section_breaks,labels,numerals,scenes"`
	Line    string   `json:"line" desc:"The start of the paragraph, 80 characters at most. It is the manuscript's text: show it as text."`
	Why     []string `json:"why,omitempty" desc:"Fixed reason codes, as for the breaks' suggestions."`
	Words   int      `json:"words" desc:"How many words of the chapter come before it."`
}

// LookOut is what a look found in a chapter.
type LookOut struct {
	Key        string      `json:"key"`
	Title      string      `json:"title"`
	Words      int         `json:"words"`
	Mode       string      `json:"mode" enum:"split,inside"`
	After      int         `json:"after" desc:"How many of the list came before this page."`
	Total      int         `json:"total" desc:"How many are in the list."`
	Paragraphs []LookPara  `json:"paragraphs,omitempty" desc:"With mode split: the paragraphs the chapter can be split at, one page of them. The first paragraph of a chapter is not one: splitting there would leave nothing before it."`
	Found      []LookFound `json:"found,omitempty" desc:"With mode inside: how many places of each kind could start a chapter inside it."`
	Suggest    []LookSug   `json:"suggestions,omitempty" desc:"With mode inside: those places, one page of them, in reading order."`
	Pieces     int         `json:"pieces,omitempty" desc:"With mode inside: how many pieces of piece_words the size rule would make of it."`
	PieceWords int         `json:"piece_words,omitempty"`
}

// imageOnly says a paragraph holds nothing but pictures (each is the marker "[image]"): never a place to cut.
func imageOnly(text string) bool {
	const marker = `*\[image\]*`
	if !strings.Contains(text, marker) {
		return false
	}
	return strings.TrimSpace(strings.ReplaceAll(text, marker, "")) == ""
}

var lineMarks = strings.NewReplacer("**", "", "*", "", "\\", "", "~~", "", "`", "")

// plainLine is the start of a paragraph as a reader sees it: one line, the Markdown taken out, 80 characters at most.
func plainLine(b mdBlock) string {
	t := b.text
	if b.sig != nil && b.sig.plain != "" {
		t = b.sig.plain
	} else {
		t = lineMarks.Replace(t)
	}
	t = strings.Join(strings.Fields(noMarks(t)), " ")
	if utf8.RuneCountInString(t) > 80 {
		r := []rune(t)
		t = strings.TrimSpace(string(r[:80])) + "…"
	}
	return t
}

// lastLine is the end of a document, one line: the last 80 characters of its last paragraph, with the Markdown taken out.
func lastLine(body string) string {
	lines := strings.Split(strings.TrimRight(body, "\n"), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if t == "" || strings.HasPrefix(t, "%%") || strings.HasPrefix(t, "[^") || strings.HasPrefix(lines[i], "    ") || strings.HasPrefix(lines[i], "\t") || t == "* * *" || strings.HasPrefix(t, "---") || strings.HasPrefix(t, "|") || strings.HasPrefix(t, "#") {
			continue
		}
		t = strings.Join(strings.Fields(lineMarks.Replace(t)), " ")
		if t == "" {
			continue
		}
		if utf8.RuneCountInString(t) > 80 {
			r := []rune(t)
			t = "…" + strings.TrimSpace(string(r[len(r)-80:]))
		}
		return t
	}
	return ""
}

// fill answers a look from the chapter the cut made. It is called after the files are named, with the cut's candidates and boundaries.
func (a *lookAsk) fill(d *detector, cs *candidates, chosen []boundary) {
	p := a.p
	if p == nil {
		return
	}
	cum := d.cum()
	isB := make(map[int]bool, len(chosen))
	for _, bd := range chosen {
		isB[bd.at] = true
	}
	// can a chapter start at block i: a paragraph or heading of this chapter's text, with text before it in the chapter
	can := func(i int) bool {
		return i >= p.from && i < p.to && !d.hidden(i) && d.sigs[i] != nil && d.ok(i) && !isB[i] && !imageOnly(d.b[i].text) && cum[i]-cum[p.from] > 0
	}
	out := &LookOut{Key: a.key, Title: p.title, Words: cum[min(p.to, len(d.b))] - cum[p.from], Mode: a.mode, After: a.after}
	a.out = out
	count := a.count
	if count < 1 {
		count = LookPageDefault
	}
	count = min(count, LookPageMax)
	after := max(a.after, 0)
	switch a.mode {
	case LookSplit:
		total := 0
		for i := p.from; i < p.to && i < len(d.b); i++ {
			if !can(i) {
				continue
			}
			if total >= after && len(out.Paragraphs) < count {
				bd := d.boundaryAt(i, "explicit")
				d.titleOf(&bd)
				out.Paragraphs = append(out.Paragraphs, LookPara{Ordinal: d.sigs[i].ord, Line: plainLine(d.b[i]), Words: cum[i] - cum[p.from], Title: bd.consumed > 0 && !bd.noTitle})
			}
			total++
		}
		out.Total = total
	default:
		by := d.insideCandidates(cs, p, can)
		if a.mode == lookRule {
			if a.rule == "size" {
				for _, bd := range d.sizeBreaksIn(p.from, min(p.to, len(d.b)), a.pieceWords()) {
					if can(bd.at) {
						a.ords = append(a.ords, bd.ord)
					}
				}
				return
			}
			for _, bd := range by[a.rule] {
				a.ords = append(a.ords, bd.ord)
				if len(a.ords) > MaxExplicitBrks {
					break
				}
			}
			return
		}
		// what was found, a line for each place whichever rule found it
		for _, r := range insideRules {
			if n := len(by[r]); n > 0 {
				out.Found = append(out.Found, LookFound{Rule: r, Count: n})
			}
		}
		pw := a.pieceWords()
		if out.Words >= 2*pw {
			if n := len(d.sizeBreaksIn(p.from, min(p.to, len(d.b)), pw)); n > 0 {
				out.Found = append(out.Found, LookFound{Rule: "size", Count: n + 1})
				out.Pieces, out.PieceWords = n+1, pw
			}
		}
		seen := map[int]bool{}
		var all []LookSug
		for _, r := range insideRules {
			for _, bd := range by[r] {
				if seen[bd.at] {
					continue
				}
				seen[bd.at] = true
				line, _ := cleanLine(d.sigs[bd.at].plain, 80)
				if line == "" {
					line = plainLine(d.b[bd.at])
				}
				all = append(all, LookSug{Ordinal: bd.ord, Rule: r, Line: line, Why: bd.why, Words: cum[bd.at] - cum[p.from]})
			}
		}
		sort.SliceStable(all, func(x, y int) bool { return all[x].Ordinal < all[y].Ordinal })
		out.Total = len(all)
		if after < len(all) {
			out.Suggest = all[after:min(len(all), after+count)]
		}
	}
}

func (a *lookAsk) pieceWords() int {
	return docxCutOpts{words: a.size}.pieceWords()
}

// insideRules are the kinds of place a chapter could start inside a chapter, in the order a place is credited to the first that found it.
var insideRules = []string{"headings", "page_breaks", "section_breaks", "labels", "numerals", "scenes"}

// insideCandidates are the places a chapter could start inside one, by rule: the weak rules (a page break, a section break, an unformatted
// label, a lone numeral), a heading deeper than the chapter level, and the paragraph after a scene break. Each list is bounded.
func (d *detector) insideCandidates(cs *candidates, p *piece, can func(int) bool) map[string][]boundary {
	by := map[string][]boundary{}
	add := func(rule string, bd boundary) {
		if can(bd.at) {
			by[rule] = append(by[rule], bd)
		}
	}
	for lvl, hs := range cs.heading {
		if d.chapLvl > 0 && lvl > d.chapLvl {
			for _, bd := range hs {
				add("headings", bd)
			}
		}
	}
	for _, bd := range cs.pages {
		add("page_breaks", bd)
	}
	for _, bd := range cs.sections {
		add("section_breaks", bd)
	}
	for _, bd := range cs.loose {
		add("labels", bd)
	}
	for _, bd := range cs.numerals {
		add("numerals", bd)
	}
	for i := p.from; i < p.to && i < len(d.b); i++ {
		if d.b[i].kind != bScene {
			continue
		}
		for j := i + 1; j < p.to && j < len(d.b); j++ {
			if d.sigs[j] != nil {
				if can(j) {
					bd := d.boundaryAt(j, "scene")
					bd.why = []string{"scene_break"}
					add("scenes", bd)
				}
				break
			}
		}
	}
	// the places are in reading order whichever way the candidates were gathered (the heading levels come from a map), and only then
	// bounded, so the same file always lists the same places
	for r, list := range by {
		sort.SliceStable(list, func(a, b int) bool { return list[a].at < list[b].at })
		if len(list) > MaxExplicitBrks+1 {
			list = list[:MaxExplicitBrks+1]
		}
		by[r] = list
	}
	return by
}

// lookBook is a Word file read once for the looks at its chapters: the blocks, and the facts the cut takes from the file.
type lookBook struct {
	mu        sync.Mutex // a look runs the cut on the blocks, which keeps a few counts of its own
	c         *docxConv
	core      []byte
	styleLang string
	used      time.Time
}

// The books held for looking: a few, for a few minutes, and only those of a size that keeps the whole cache small. "Show more" and the next
// look at the same chapter then read nothing from the disk and parse nothing.
const (
	lookBooks       = 2
	lookBookTTL     = 5 * time.Minute
	lookBookMaxPara = 200_000
)

var (
	lookMu    sync.Mutex
	lookCache = map[string]*lookBook{}
)

// lookBookFor gives the book of a staged file: from the cache, or read from the file (once, under the reader limit).
func lookBookFor(ctx context.Context, c *ops.Ctx, dr *Draft, changes string) (*lookBook, error) {
	key := dr.SHA256 + "|" + changes
	now := time.Now()
	lookMu.Lock()
	for k, b := range lookCache {
		if now.Sub(b.used) > lookBookTTL {
			delete(lookCache, k)
		}
	}
	b := lookCache[key]
	if b != nil {
		b.used = now
	}
	lookMu.Unlock()
	if b != nil && dr.SHA256 != "" {
		return b, nil
	}
	ss, err := loadSource(c, dr)
	if err != nil {
		return nil, err
	}
	d, err := readDocx(ss.docx)
	if err != nil {
		return nil, ops.Invalid("docx", err.Error())
	}
	conv, _, styleLang, err := readConv(d, changes)
	if err != nil {
		return nil, ops.Invalid("docx", err.Error())
	}
	b = &lookBook{c: conv, core: d.core, styleLang: styleLang, used: now}
	if dr.SHA256 != "" && conv.paragraphs <= lookBookMaxPara {
		lookMu.Lock()
		if len(lookCache) >= lookBooks {
			var oldest string
			for k, x := range lookCache {
				if oldest == "" || x.used.Before(lookCache[oldest].used) {
					oldest = k
				}
			}
			delete(lookCache, oldest)
		}
		lookCache[key] = b
		lookMu.Unlock()
	}
	return b, nil
}

// lookDraft runs a look on a draft's staged Word file, in the state of its options. Only a pattern that is the way the book is cut is
// looked at: one that is only kept, or being tried, changes nothing a look lists and costs nothing here.
func lookDraft(ctx context.Context, c *ops.Ctx, dr *Draft, o DraftOptions, ask *lookAsk) (err error) {
	if dr.Kind != StageDocx {
		return invalidWhy("look", "A chapter can be looked into in a Word document.", "look_kind")
	}
	release, err := acquire(ctx)
	if err != nil {
		return err
	}
	defer release()
	defer func() {
		if r := recover(); r != nil {
			err = ops.Invalid("docx", "the document could not be read")
		}
	}()
	book, err := lookBookFor(ctx, c, dr, o.DocxChanges)
	if err != nil {
		return err
	}
	cut := o.conv().cutOpts()
	cut.try, cut.tryMatch = "", ""
	if o.DocxSplit != SplitPattern {
		cut.pattern, cut.match = "", ""
	}
	cut.look = ask
	book.mu.Lock()
	defer book.mu.Unlock()
	pi := book.c.prepareCut(book.core, book.styleLang, strings.TrimPrefix(o.DocxHeadings, "auto"), dr.Name, &cut)
	if _, _, _, err := book.c.cutDocx(cut, pi); err != nil {
		return cutError(err)
	}
	if ask.out == nil {
		return ops.NotFound("document")
	}
	return nil
}
