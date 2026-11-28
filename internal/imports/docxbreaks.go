package imports

import (
	"errors"
	"fmt"
	"math"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// Finding the chapters of a Word document.
//
// A pure function of the block stream and the options: no clock, no randomness, no network, so the same file and options give the
// same cut. It runs on blocks, before compose(), so footnotes are renumbered for each file as before and a tracked change or a
// comment stays with its paragraph in whichever chapter it lands.
//
// Strong boundaries cut by themselves, all used together so a book that marks its chapters two ways is cut at both: a heading of
// the chapter level, a paragraph in a custom style named for chapters (used twice or more), a "Chapter 3" line with a look that
// says it is a title, and an unformatted "Chapter 2" that belongs to a run of numbered ones that already has strong members.
// Weak candidates (a page break, a section break, a lone numeral, an unformatted label outside a run) are only listed as
// suggestions: a page break is where the page changed, and a writer who never meant a chapter there is the common case. They
// cut the book only when the writer chooses them (docx_split, or "Start a chapter here" for one: docx_breaks).

// The ways to cut, as docx_split names them.
const (
	SplitAuto          = "auto"
	SplitHeading1      = "heading1"
	SplitHeading2      = "heading2"
	SplitStyle         = "style"
	SplitLabel         = "label"
	SplitPageBreaks    = "page_breaks"
	SplitSectionBreaks = "section_breaks"
	SplitNumerals      = "numerals"
	SplitSize          = "size"
	SplitNone          = "none"
)

// Limits of the cut.
const (
	maxSplitStyle    = 200
	MinSplitWords    = 500
	MaxSplitWords    = 20_000
	DefaultPieceW    = 5_000
	MaxExplicitBrks  = 5_000
	maxSuggestions   = 400 // listed in a preview: more are counted
	sizeSuggestMin   = 15_000
	manyChapters     = 200
	pageLikeWords    = 300
	frontRegionWords = 2000 // front matter is split by what it says only in a region this short
)

// Weights of the score that labels a boundary "sure" or "check". They start as the plan gives them and are tuned on the corpus.
var scoreWeights = struct {
	heading, label, format, formatMax, brk, short, run, sentence, long float64
	sure                                                               float64
}{heading: 0.55, label: 0.35, format: 0.1, formatMax: 0.2, brk: 0.2, short: 0.1, run: 0.1, sentence: -0.3, long: -0.4, sure: 0.6}

// BreakOption is a way to cut that exists in this file, with how many chapters it would make.
type BreakOption struct {
	Rule  string `json:"rule" enum:"auto,heading1,heading2,style,label,page_breaks,section_breaks,numerals,size,pattern,none"`
	Style string `json:"style,omitempty" desc:"For style: the style's name."`
	Count int    `json:"count" desc:"How many chapters it makes."`
	Weak  bool   `json:"weak,omitempty" desc:"A rule that never cuts under auto: choosing it is the writer's own act."`
}

// BreakSuggestion is a place a chapter could start that the import did not take by itself.
type BreakSuggestion struct {
	Ordinal int      `json:"ordinal" desc:"The paragraph's index among the document's paragraphs: what docx_breaks takes."`
	Rule    string   `json:"rule" enum:"page_breaks,section_breaks,numerals,label"`
	Line    string   `json:"line" desc:"The start of the paragraph, 80 characters at most. It is the manuscript's text: show it as text."`
	Why     []string `json:"why" desc:"Fixed reason codes (page_break, section_break, short, centred, bold, caps, larger, numeral, label)."`
	Words   int      `json:"words" desc:"How many words of the book come before it."`
	Score   float64  `json:"score"`
}

// BreakInfo says how a Word document was cut and what else it could be cut by.
type BreakInfo struct {
	Rule             string            `json:"rule" desc:"What was used. auto is told apart by chosen_by."`
	ChosenBy         string            `json:"chosen_by" enum:"heading,style,label,mixed,explicit,none_found,none" desc:"The kind of boundary that made the chapters under auto: heading, style, label, mixed (more than one), none_found (no strong boundary: one document), or explicit (the writer chose a rule)."`
	Level            int               `json:"level,omitempty" desc:"The heading level that cut, when headings did."`
	Style            string            `json:"style,omitempty" desc:"The style that cut, when one did."`
	Auto             int               `json:"auto" desc:"How many chapters and parts auto would make, whichever rule is in use."`
	Chapters         int               `json:"chapters" desc:"How many chapters and parts the rule made, not counting front and back matter."`
	Parts            int               `json:"parts,omitempty"`
	BreakBefore      int               `json:"break_before,omitempty" desc:"How many of them have a page or section break in front."`
	Added            []int             `json:"added,omitempty" desc:"The writer's own boundaries (docx_breaks), by ordinal."`
	Options          []BreakOption     `json:"options" desc:"The other ways to cut, only those that exist in this file."`
	Words            int               `json:"words" desc:"The book's words."`
	NoneFound        bool              `json:"none_found,omitempty" desc:"No chapter breaks were found by heading, style or label."`
	Suggestions      []BreakSuggestion `json:"suggestions,omitempty" desc:"Possible chapter starts, best first (the first 400)."`
	SuggestionsTotal int               `json:"suggestions_total,omitempty"`
	ByRule           map[string]int    `json:"by_rule,omitempty" desc:"How many suggestions each weak rule found."`
	Levels           int               `json:"heading_levels,omitempty" desc:"How many heading levels the file uses: with two or more, docx_headings parts has something to make parts of."`
	Pieces           int               `json:"pieces,omitempty" desc:"From 15,000 words: how many pieces of docx_split_words the size rule would make."`
	PieceWords       int               `json:"piece_words,omitempty"`
	Pattern          *PatternInfo      `json:"pattern,omitempty" desc:"With docx_split_pattern set: what the pattern matches, whether or not it is the way the book is cut."`
	Try              *PatternInfo      `json:"try,omitempty" desc:"With docx_split_try set: what the pattern being tried matches. Nothing is cut by it."`
	Hint             string            `json:"-"`
	intro            bool
	kept             int // titles kept in the text because they carry a footnote or a comment
}

// docNote is what the detector says of one file it made, handed to the plan by the file's path.
type docNote struct {
	kind      string // part, chapter, front_matter, back_matter: overrides what the name says
	title     string // a part's own title (its folder is named for a slug of it)
	why       []string
	score     float64
	ordinal   int
	left      bool   // left out unless the writer puts it back
	leftWhy   string // contents
	noTitle   bool
	numbered  string
	check     bool
	explicit  bool
	hasScore  bool
	lineTitle string // the chapter line's label, when the title holds text before it (see captionLabel)
	caption   string // the text before that label: kept in the chapter as its first paragraph when the title is renamed to the label
}

// docxCutOpts are the options the cut reads.
type docxCutOpts struct {
	headings, split, style string
	words                  int
	add                    []int
	projectTitle, author   string
	name                   string
	pattern, match         string // docx_split_pattern and docx_split_match
	try, tryMatch          string // docx_split_try: a pattern only tried
	look                   *lookAsk
}

func (o docxCutOpts) pieceWords() int {
	if o.words < MinSplitWords || o.words > MaxSplitWords {
		return DefaultPieceW
	}
	return o.words
}

// ---- reading the words of a label --------------------------------------------------------------------------------

var (
	chapterWords = `chapter|chapitre|kapitel|cap[ií]tulo|capitolo|hoofdstuk`
	partWords    = `part|book|partie|teil|parte|deel|livre`
	numberWords  = `one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty|thirty|forty|fifty|sixty|seventy|eighty|ninety`
	numToken     = `\d{1,4}|[IVXLCDM]{1,8}|(?:(?:twenty|thirty|forty|fifty|sixty|seventy|eighty|ninety)[- ])?(?:one|two|three|four|five|six|seven|eight|nine)|` + numberWords
	reLabel      = regexp.MustCompile(`(?i)^(` + chapterWords + `|` + partWords + `)\s+(` + numToken + `)(?:\s*[:.\-–—|]\s*(.+?))?\s*[.:]?$`)
	reLoneNum    = regexp.MustCompile(`(?i)^(\d{1,3}|[IVXLCDM]{1,8}|(?:(?:twenty|thirty|forty|fifty|sixty|seventy|eighty|ninety)[- ])?(?:one|two|three|four|five|six|seven|eight|nine)|` + numberWords + `)\.?$`)
	// a contents entry is a title, a run of dots (three or more) and a page: dots with no page after them are a trailing-off line, not a leader
	reLeader      = regexp.MustCompile(`\S\s*(?:\.{3,}|·{3,}|…+|(?:\.\s){3,})\s*(?:\d{1,4}|[ivxlcdm]{1,6})\s*$`)
	reDots        = regexp.MustCompile(`\.{3,}|·{3,}|…|(?:\.\s){3,}`)
	reTocPage     = regexp.MustCompile(`^.+\s(\d{1,4}|[ivxlcdm]{1,6})$`)
	reCaps8       = regexp.MustCompile(`^[IVXLCDM]{1,8}$`)
	rePartWord    = regexp.MustCompile(`^(?:` + partWords + `)$`)
	reChapterWord = regexp.MustCompile(`^(?:` + chapterWords + `|` + partWords + `)$`)
)

var wordValues = func() map[string]int {
	m := map[string]int{}
	for i, w := range strings.Split("one two three four five six seven eight nine ten eleven twelve thirteen fourteen fifteen sixteen seventeen eighteen nineteen twenty", " ") {
		m[w] = i + 1
	}
	for i, w := range strings.Split("thirty forty fifty sixty seventy eighty ninety", " ") {
		m[w] = 30 + 10*i
	}
	return m
}()

// numberValue reads a number a label gives: digits, an upper-case Roman numeral, or a number word. ok is false for anything else
// (a lower-case "mix" is not 1009).
func numberValue(s string) (int, bool) {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "."))
	if s == "" {
		return 0, false
	}
	if n, err := strconv.Atoi(s); err == nil {
		return n, true
	}
	if reCaps8.MatchString(s) {
		return romanValue(s)
	}
	l := strings.ToLower(strings.ReplaceAll(s, " ", "-"))
	if v, ok := wordValues[l]; ok {
		return v, true
	}
	if i := strings.IndexByte(l, '-'); i > 0 {
		a, b := wordValues[l[:i]], wordValues[l[i+1:]]
		if a >= 20 && b >= 1 && b <= 9 {
			return a + b, true
		}
	}
	return 0, false
}

func romanValue(s string) (int, bool) {
	vals := map[byte]int{'I': 1, 'V': 5, 'X': 10, 'L': 50, 'C': 100, 'D': 500, 'M': 1000}
	total := 0
	for i := 0; i < len(s); i++ {
		v := vals[s[i]]
		if i+1 < len(s) && vals[s[i+1]] > v {
			total -= v
		} else {
			total += v
		}
	}
	if total <= 0 || roman(total, true) != s { // only a numeral written the usual way
		return 0, false
	}
	return total, true
}

// label is a line that says it opens a chapter or a part.
type label struct {
	word  string
	part  bool
	value int
	title string // after the separator, when the label has one
}

func matchLabel(s string) (label, bool) {
	m := reLabel.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil {
		return label{}, false
	}
	v, ok := numberValue(m[2])
	if !ok {
		return label{}, false
	}
	title := strings.TrimSpace(m[3])
	if title != "" {
		letters := false
		for _, r := range title {
			if unicode.IsLetter(r) {
				letters = true
				break
			}
		}
		if !letters || len(strings.Fields(title)) > 8 || reDots.MatchString(title) {
			return label{}, false
		}
	}
	w := strings.ToLower(m[1])
	return label{word: w, part: rePartWord.MatchString(w), value: v, title: title}, true
}

func isPartLabel(s string) bool {
	l, ok := matchLabel(s)
	return ok && l.part
}

// ---- what is not a boundary ------------------------------------------------------------------------------------------

// contentsLine says a line is a contents entry: a title and a page, usually with dot leaders between.
func contentsLine(s string) bool {
	s = strings.TrimSpace(s)
	n := len(strings.Fields(s))
	if s == "" || n > 14 {
		return false
	}
	if reLeader.MatchString(s) {
		return true
	}
	if _, bare := matchLabel(s); bare || n > 10 {
		return false
	}
	return reTocPage.MatchString(s)
}

// leaderEntry says a paragraph is a title, dots and a page, in no more words than a contents line has.
func leaderEntry(sg *blockSig) bool {
	return sg.words <= 14 && reLeader.MatchString(sg.plain)
}

func isContentsWord(s string) bool {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(s), ".:")) {
	case "contents", "table of contents", "contents page", "table des matières", "inhalt", "índice", "indice", "inhoud":
		return true
	}
	return false
}

// ---- the detector ----------------------------------------------------------------------------------------------------

type detector struct {
	c    *docxConv
	b    []mdBlock
	opt  docxCutOpts
	sigs []*blockSig

	inContents []bool
	dropped    []bool // a title page repeated, a byline, a book title at Heading 1
	median     int    // the body's text size, half-points

	headings  map[int]int // level -> count (chapter headings, not contents, not the book title)
	bookHead  int         // index of a book title at a heading, or -1
	partLvl   int         // heading level of the parts, 0 for none
	chapLvl   int         // heading level of the chapters
	base      int
	words     []int // words of each block
	total     int
	introMade bool // a part has text of its own before its first chapter
	frontEnd  int  // contents pages before this block are left out; after it they stay text

	byOrd       []int        // the blocks that can be boundaries, in reading order: an ordinal is found by binary search
	frontStarts map[int]bool // blocks where a named piece of front matter begins: the only breaks the front matter is split at
	cumWords    []int        // words before each block, made once
	kept        int          // titles kept in the text because they carry a footnote or a comment
	levels      int          // how many heading levels the file uses
	look        *lookAsk     // a look at one chapter: the files are named but not composed
}

// boundary is a place the cut is made.
type boundary struct {
	at        int
	ord       int
	part      bool
	rule      string // heading, style, label, run, page, section, numeral, explicit, size, first
	why       []string
	score     float64
	title     string
	noTitle   bool
	consumed  int // blocks the title takes (the boundary's own, and a title line after a label)
	level     int
	numbered  string
	style     string
	keep      bool   // the paragraphs its title was read from stay in the text: they carry a footnote or a comment
	lineTitle string // the label of the chapter line the title holds, when it holds text before one
	caption   string // the text before that label
	capTitle  bool   // the title is the label of a chapter line whose words stay in the text; the file keeps the "chapter" name
}

// hidden says block i is not part of any document's text: a title page repeated, or a contents page that is left out.
func (d *detector) hidden(i int) bool {
	return d.dropped[i] || (d.inContents[i] && i < d.frontEnd)
}

// ok says block i can be a boundary: a paragraph or heading that is not in a contents page and not a title page.
func (d *detector) ok(i int) bool {
	return d.sigs[i] != nil && !d.inContents[i] && !d.dropped[i]
}

func newDetector(c *docxConv, opt docxCutOpts) *detector {
	d := &detector{c: c, b: c.blocks, opt: opt, bookHead: -1, frontStarts: map[int]bool{}, look: opt.look}
	n := len(d.b)
	d.frontEnd = n
	d.sigs = make([]*blockSig, n)
	d.inContents = make([]bool, n)
	d.dropped = make([]bool, n)
	d.words = make([]int, n)
	for i, b := range d.b {
		if b.kind == bPara || b.kind == bHeading {
			d.sigs[i] = b.sig
		}
		w := 0 // a block with a signature carries its word count: only one without is counted here
		if d.sigs[i] != nil {
			w = d.sigs[i].words
		} else {
			w = len(strings.Fields(b.text))
		}
		d.words[i] = w
		d.total += w
	}
	d.bodySize()
	d.findContents()
	d.titlePage()
	d.headingLevels()
	return d
}

// bodySize is the median size of the text of the long paragraphs: a line bigger than that is "larger".
func (d *detector) bodySize() {
	var sizes []int
	for _, sg := range d.sigs {
		if sg != nil && sg.words >= 15 {
			s := sg.size
			if s == 0 {
				s = 22
			}
			sizes = append(sizes, s)
		}
	}
	d.median = 22
	if len(sizes) > 0 {
		sort.Ints(sizes)
		d.median = sizes[len(sizes)/2]
	}
}

func (d *detector) larger(sg *blockSig) bool {
	s := sg.size
	if s == 0 {
		s = 22
	}
	return s >= d.median+2
}

// findContents marks a contents page: a TOC field or style, a "Contents" line followed by lines that end in dot leaders or a page
// number, or three or more dot-leader lines in a row. Those paragraphs never reach the label rule.
func (d *detector) findContents() {
	n := len(d.b)
	for i := 0; i < n; i++ {
		sg := d.sigs[i]
		if sg == nil || d.inContents[i] {
			continue
		}
		start := false
		switch {
		case sg.toc:
			start = true
		case isContentsWord(sg.plain):
			entries := 0
			for j := i + 1; j < n && entries < 2; j++ {
				if d.sigs[j] == nil || d.b[j].kind != bPara || !(d.sigs[j].toc || contentsLine(d.sigs[j].plain)) {
					break
				}
				entries++
			}
			start = entries >= 2
		case d.b[i].kind == bPara && leaderEntry(sg):
			run := 0
			for j := i; j < n && d.sigs[j] != nil && d.b[j].kind == bPara && leaderEntry(d.sigs[j]); j++ {
				run++
			}
			start = run >= 3
		}
		if !start {
			continue
		}
		j := i
		for j < n && d.sigs[j] != nil && (j == i || d.sigs[j].toc || (d.b[j].kind == bPara && contentsLine(d.sigs[j].plain))) {
			d.inContents[j] = true
			j++
		}
		i = j - 1
	}
}

func fold(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }

// titlePage drops a first line that is the book's title again, and a byline after it: the project's title and author already say them.
func (d *detector) titlePage() {
	if d.opt.projectTitle == "" {
		return
	}
	for i := range d.b {
		if d.sigs[i] == nil || d.inContents[i] {
			continue
		}
		if d.b[i].kind == bPara && fold(d.sigs[i].plain) == fold(d.opt.projectTitle) {
			d.dropped[i] = true
			for j := i + 1; j < len(d.b); j++ {
				if d.sigs[j] == nil || d.inContents[j] {
					continue
				}
				a := fold(d.opt.author)
				if p := fold(d.sigs[j].plain); a != "" && d.b[j].kind == bPara && (p == a || p == "by "+a) {
					d.dropped[j] = true
				}
				break
			}
		}
		return // only the first line is looked at
	}
}

// headingLevels settles which heading level makes the chapters, whether the first heading is only the book's title, and whether the
// top level holds parts. docx_headings stays an override: chapters is the lowest level present, parts is that level and the next.
func (d *detector) headingLevels() {
	d.headings = map[int]int{}
	min1 := 0
	first := -1
	for i, b := range d.b {
		if b.kind != bHeading || !d.ok(i) {
			continue
		}
		d.headings[b.level]++
		if min1 == 0 || b.level < min1 {
			min1 = b.level
		}
		if first < 0 {
			first = i
		}
	}
	if min1 == 0 {
		return
	}
	switch d.opt.headings {
	case DocxChapters:
		d.base, d.chapLvl = min1, min1
		return
	case DocxParts:
		if d.headings[min1+1] > 0 {
			d.partLvl, d.chapLvl, d.base = min1, min1+1, min1
		} else {
			d.base, d.chapLvl = min1, min1
		}
		return
	}
	// The lowest level that occurs twice, else the lowest there is.
	base := 0
	for l := 1; l <= 6; l++ {
		if d.headings[l] >= 2 {
			base = l
			break
		}
	}
	if base == 0 {
		base = min1
	}
	// A book title at Heading 1: one heading above the chapter level, first, with no text before it.
	if min1 < base && d.headings[min1] == 1 {
		for i, b := range d.b {
			if b.kind == bHeading && d.ok(i) && b.level == min1 {
				before := false
				for j := 0; j < i; j++ {
					if d.ok(j) {
						before = true
					}
				}
				if !before {
					d.bookHead = i
					d.dropped[i] = true
					d.headings[min1] = 0
					min1 = 0
					for l := 1; l <= 6; l++ {
						if d.headings[l] > 0 {
							min1 = l
							break
						}
					}
				}
				break
			}
		}
	}
	// Parts: two or more headings of one level that are part labels, with a deeper level that carries the chapters, or chapters at the same level.
	partsAt := map[int]int{}
	for i, b := range d.b {
		if b.kind == bHeading && d.ok(i) && isPartLabel(d.sigs[i].plain) {
			partsAt[b.level]++
		}
	}
	pl := 0
	for l := 1; l <= 6; l++ {
		if partsAt[l] >= 2 {
			pl = l
			break
		}
	}
	d.base, d.chapLvl = base, base
	if pl > 0 {
		d.partLvl = pl
		d.chapLvl = pl
		for l := pl + 1; l <= 6; l++ {
			if d.headings[l] > 0 {
				d.chapLvl = l
				break
			}
		}
		d.base = pl
	}
}

// ---- boundaries by rule ----------------------------------------------------------------------------------------------

// signals says what in a paragraph's look marks it as a title.
func (d *detector) signals(sg *blockSig) (why []string, formatted, hasBreak bool) {
	if sg.center {
		why = append(why, "centred")
		formatted = true
	}
	if sg.bold {
		why = append(why, "bold")
		formatted = true
	}
	if sg.caps {
		why = append(why, "caps")
		formatted = true
	}
	if d.larger(sg) {
		why = append(why, "larger")
		formatted = true
	}
	if sg.before >= 600 {
		why = append(why, "space")
	}
	if sg.brBefore || sg.pageBefore {
		why = append(why, "page_break")
		hasBreak = true
	}
	if sg.sectBefore {
		why = append(why, "section_break")
		hasBreak = true
	}
	return
}

func (d *detector) scoreOf(sg *blockSig, base float64, labelLine, runMember bool) float64 {
	s := base
	f := 0.0
	for _, on := range []bool{sg.center, sg.bold, sg.caps, d.larger(sg), sg.before >= 600} {
		if on {
			f += scoreWeights.format
		}
	}
	s += math.Min(f, scoreWeights.formatMax)
	if sg.brBefore || sg.pageBefore || sg.sectBefore {
		s += scoreWeights.brk
	}
	if sg.words <= 12 && !sg.full {
		s += scoreWeights.short
	}
	if runMember {
		s += scoreWeights.run
	}
	if sg.full && !labelLine {
		s += scoreWeights.sentence
	}
	if sg.words >= 20 {
		s += scoreWeights.long
	}
	return math.Max(0, math.Min(1, math.Round(s*100)/100))
}

// isChapterStyle says a style is named for chapters: a whole word of its name is chapter, title, heading or part, and it is not one
// of Word's own title styles.
func isChapterStyle(name string) (ok, part bool) {
	switch name {
	case "", "title", "subtitle", "title page", "book title", "document title", "toc heading", "contents heading", "chapter opening", "heading", "normal", "body text":
		return false, false
	}
	if reHeadingName.MatchString(name) {
		return false, false
	}
	words := strings.FieldsFunc(name, func(r rune) bool { return !unicode.IsLetter(r) })
	for _, w := range words {
		switch w {
		case "table", "figure", "caption", "subtitle", "subhead", "subheading", "epigraph", "quote", "note", "footnote", "endnote", "toc", "contents", "index", "equation":
			return false, false // a title of something in a chapter, not of a chapter
		}
	}
	for _, w := range words {
		switch w {
		case "chapter", "title", "heading":
			ok = true
		case "part":
			ok, part = true, true
		}
	}
	return
}

// candidates are the boundaries every rule would make, found once: the preview lists them all.
type candidates struct {
	heading       map[int][]boundary // by level
	styles        map[string][]boundary
	styleOrder    []string
	labels        []boundary // formatted (strong) label lines, and promoted ones
	loose         []boundary // unformatted labels outside a run
	pages         []boundary
	sections      []boundary
	numerals      []boundary
	chapterHeads  []boundary // strong headings at the chosen levels, parts included
	runPromoted   int
	allLabelLines []boundary // every label line, formatted or not (docx_split=label)
}

func (d *detector) boundaryAt(i int, rule string) boundary {
	sg := d.sigs[i]
	return boundary{at: i, ord: sg.ord, rule: rule, consumed: 1}
}

func (d *detector) find() *candidates {
	cs := &candidates{heading: map[int][]boundary{}, styles: map[string][]boundary{}}
	firstContent := -1
	for i := range d.b {
		if d.ok(i) && firstContent < 0 {
			firstContent = i
		}
	}
	styleCount := map[string]int{}
	for i, b := range d.b {
		if b.kind == bPara && d.ok(i) {
			if ok, _ := isChapterStyle(d.sigs[i].style); ok && d.sigs[i].words <= 20 {
				styleCount[d.sigs[i].style]++
			}
		}
	}
	type lab struct {
		i int
		l label
		s *blockSig
		f bool // has a look that says title
		w []string
	}
	var labs []lab
	for i, b := range d.b {
		if !d.ok(i) {
			continue
		}
		sg := d.sigs[i]
		switch b.kind {
		case bHeading:
			bd := d.boundaryAt(i, "heading")
			bd.level = b.level
			bd.part = false
			bd.why = []string{fmt.Sprintf("heading:%d", b.level)}
			bd.numbered = sg.numLabel
			cs.heading[b.level] = append(cs.heading[b.level], bd)
			if l, ok := matchLabel(sg.plain); ok {
				labs = append(labs, lab{i: i, l: l, s: sg, f: true})
			}
			continue
		case bPara:
		default:
			continue
		}
		if sg.words > 40 {
			continue
		}
		// a custom style named for chapters
		if ok, part := isChapterStyle(sg.style); ok && styleCount[sg.style] >= 2 && sg.words <= 20 {
			bd := d.boundaryAt(i, "style")
			bd.part, bd.style = part, sg.style
			bd.numbered = sg.numLabel
			bd.why = []string{"style:" + sg.style}
			if _, ok := cs.styles[sg.style]; !ok {
				cs.styleOrder = append(cs.styleOrder, sg.style)
			}
			cs.styles[sg.style] = append(cs.styles[sg.style], bd)
		}
		// a label line
		if l, ok := matchLabel(sg.plain); ok {
			why, formatted, hasBreak := d.signals(sg)
			labs = append(labs, lab{i: i, l: l, s: sg, f: formatted || hasBreak, w: why})
		}
	}
	// label lines: those with a look that says title are strong; an unformatted one joins a numbered run that has strong members
	sort.Slice(labs, func(a, b int) bool { return labs[a].i < labs[b].i })
	strong := make([]bool, len(labs))
	promoted := make([]bool, len(labs))
	for k, x := range labs {
		strong[k] = x.f
	}
	for k := 1; k < len(labs); k++ {
		if !strong[k] && strong[k-1] && labs[k-1].l.word == labs[k].l.word && labs[k-1].l.value+1 == labs[k].l.value {
			strong[k], promoted[k] = true, true
		}
	}
	for k := len(labs) - 2; k >= 0; k-- {
		if !strong[k] && strong[k+1] && labs[k+1].l.word == labs[k].l.word && labs[k+1].l.value == labs[k].l.value+1 {
			strong[k], promoted[k] = true, true
		}
	}
	for k, x := range labs {
		if d.b[x.i].kind != bPara {
			continue
		}
		bd := d.boundaryAt(x.i, "label")
		bd.part = x.l.part
		bd.why = append([]string{"label"}, x.w...)
		cs.allLabelLines = append(cs.allLabelLines, bd)
		switch {
		case x.f:
			bd.score = d.scoreOf(x.s, scoreWeights.label, true, false)
			cs.labels = append(cs.labels, bd)
		case promoted[k]:
			bd.rule = "run"
			bd.why = []string{"label", "run"}
			bd.score = d.scoreOf(x.s, scoreWeights.label, true, true)
			cs.labels = append(cs.labels, bd)
			cs.runPromoted++
		default:
			bd.why = []string{"label"}
			bd.score = d.scoreOf(x.s, scoreWeights.label, true, false)
			cs.loose = append(cs.loose, bd)
		}
	}
	// weak: page breaks, section breaks, lone numerals
	for i, b := range d.b {
		if !d.ok(i) || (b.kind != bPara && b.kind != bHeading) {
			continue
		}
		sg := d.sigs[i]
		if i != firstContent {
			why, _, _ := d.signals(sg)
			var w []string
			if sg.words <= 12 && !sg.full {
				w = append(w, "short")
			}
			if sg.brBefore || sg.pageBefore {
				bd := d.boundaryAt(i, "page")
				bd.why = append(append([]string{}, why...), w...)
				bd.score = d.scoreOf(sg, 0.1, false, false)
				cs.pages = append(cs.pages, bd)
			}
			if sg.sectBefore {
				bd := d.boundaryAt(i, "section")
				bd.why = append(append([]string{}, why...), w...)
				bd.score = d.scoreOf(sg, 0.1, false, false)
				cs.sections = append(cs.sections, bd)
			}
		}
	}
	cs.numerals = d.numeralRuns()
	return cs
}

// numeralRuns finds lone numerals (I, II, III; 1, 2, 3; One, Two) that are centred or bold, three or more in sequence.
func (d *detector) numeralRuns() []boundary {
	type nm struct {
		i, v int
	}
	var all []nm
	for i, b := range d.b {
		if !d.ok(i) || b.kind != bPara {
			continue
		}
		sg := d.sigs[i]
		if !(sg.center || sg.bold) || sg.words != 1 && !(sg.words == 2 && strings.Contains(sg.plain, "-")) {
			continue
		}
		if m := reLoneNum.FindStringSubmatch(sg.plain); m != nil {
			if v, ok := numberValue(m[1]); ok {
				all = append(all, nm{i, v})
			}
		}
	}
	var out []boundary
	for s := 0; s < len(all); {
		e := s + 1
		for e < len(all) && all[e].v == all[e-1].v+1 {
			e++
		}
		if e-s >= 3 {
			for _, x := range all[s:e] {
				bd := d.boundaryAt(x.i, "numeral")
				why, _, _ := d.signals(d.sigs[x.i])
				bd.why = append([]string{"numeral"}, why...)
				bd.score = d.scoreOf(d.sigs[x.i], 0.1, true, true)
				out = append(out, bd)
			}
		}
		s = e
	}
	return out
}

// ---- the cut ---------------------------------------------------------------------------------------------------------

// strongSet is what auto cuts at: headings of the chapter level, styles, labels, and run members. Parts are flagged.
func (d *detector) strongSet(cs *candidates) []boundary {
	var out []boundary
	for lvl, hs := range cs.heading {
		for _, bd := range hs {
			switch {
			case d.partLvl > 0 && lvl == d.partLvl && (d.opt.headings == DocxParts || isPartLabel(d.sigs[bd.at].plain)):
				bd.part = true // under the override, the top headings are parts whatever they say
			case lvl > d.chapLvl && d.chapLvl > 0:
				continue // deeper than a chapter: stays inside it
			}
			bd.score = d.scoreOf(d.sigs[bd.at], scoreWeights.heading, d.sigs[bd.at].words <= 12, false)
			if d.sigs[bd.at].words > 12 {
				bd.score = math.Min(bd.score, 0.5)
			} else {
				bd.score = math.Max(bd.score, scoreWeights.sure)
			}
			out = append(out, bd)
		}
	}
	for _, name := range cs.styleOrder {
		for _, bd := range cs.styles[name] {
			bd.score = math.Max(d.scoreOf(d.sigs[bd.at], scoreWeights.heading, true, false), scoreWeights.sure)
			out = append(out, bd)
		}
	}
	out = append(out, cs.labels...)
	return dedupe(out)
}

// bareLabel says a boundary's own line is only "Chapter 3": a number, with no title of its own.
func (d *detector) bareLabel(bd boundary) bool {
	sg := d.sigs[bd.at]
	if sg == nil {
		return false
	}
	l, ok := matchLabel(sg.plain)
	return ok && l.title == ""
}

// mergeAdjacent joins a boundary into the next when no text lies between them, so a chapter is never empty for want of a line:
// "Chapter 3" over a title in another style, or a heading over a second heading. A bare "Chapter 3" gives way to the title that
// follows it (as a label over a title line always has); two titles are joined; two bare labels are two chapters. The writer's own
// breaks, parts and the size cut are never joined.
func (d *detector) mergeAdjacent(in []boundary) []boundary {
	var out []boundary
	for _, b := range in {
		if n := len(out); n > 0 {
			a := &out[n-1]
			if a.rule != "explicit" && b.rule != "explicit" && a.rule != "size" && b.rule != "size" && !a.part && !b.part && d.noTextBetween(a.at+a.consumed, b.at) &&
				!(d.bareLabel(*a) && d.bareLabel(b)) {
				switch {
				case d.bareLabel(*a) || a.title == "":
					a.title = b.title
				case d.bareLabel(b) || b.title == "":
				default:
					a.title = a.title + ": " + b.title
				}
				a.noTitle = a.noTitle && b.noTitle
				if a.numbered == "" {
					a.numbered = b.numbered
				}
				a.why = append(append([]string{}, a.why...), "title_line")
				a.consumed = b.at + b.consumed - a.at
				a.keep = false
				continue
			}
		}
		out = append(out, b)
	}
	return out
}

// noTextBetween says the blocks in [from, to) hold no words that belong to a document.
func (d *detector) noTextBetween(from, to int) bool {
	for i := from; i < to && i < len(d.b); i++ {
		if !d.hidden(i) && d.words[i] > 0 {
			return false
		}
	}
	return true
}

// keepCarriers keeps in the text the paragraphs a title was read from when one carries a footnote or a comment: the line is then
// also the first line of the chapter, so neither is lost with it, and the writer is told.
func (d *detector) keepCarriers(bd *boundary) {
	for k := 0; k < bd.consumed; k++ {
		i := bd.at + k
		if i < len(d.b) && d.b[i].kind != bHeading && (len(d.b[i].notes) > 0 || len(d.b[i].cms) > 0) {
			bd.keep = true
			d.kept++
			return
		}
	}
}

// dedupe keeps one boundary for a block, the first rule that took it, in reading order.
func dedupe(in []boundary) []boundary {
	sort.SliceStable(in, func(a, b int) bool { return in[a].at < in[b].at })
	var out []boundary
	for _, bd := range in {
		if len(out) > 0 && out[len(out)-1].at == bd.at {
			if bd.part {
				out[len(out)-1].part = true
			}
			continue
		}
		out = append(out, bd)
	}
	return out
}

// firstFrontIndex is the index of the first block that can be text (not a dropped title page or a contents page).
func titleLike(sg *blockSig) bool {
	return sg != nil && sg.plain != "" && sg.words <= 12 && !sg.full
}

// titleOf finds the title a boundary gives its chapter, and how many blocks it takes.
func (d *detector) titleOf(bd *boundary) {
	sg := d.sigs[bd.at]
	b := d.b[bd.at]
	bd.consumed = 1
	if b.kind == bHeading {
		bd.title = b.text
		bd.caption, bd.lineTitle = chapterLineOf(b.text)
		return
	}
	if l, ok := matchLabel(sg.plain); ok {
		switch {
		case bd.part:
			bd.title = strings.TrimSpace(strings.TrimRight(sg.plain, ".:"))
		case l.title != "":
			bd.title = l.title
		default:
			bd.title = strings.TrimSpace(strings.TrimRight(sg.plain, ".:"))
			// a label alone on its line, then a title line that looks like one
			for j := bd.at + 1; j < len(d.b); j++ {
				if d.sigs[j] == nil && d.b[j].kind != bScene {
					break
				}
				if d.sigs[j] == nil || !d.ok(j) || d.b[j].kind != bPara {
					break
				}
				nx := d.sigs[j]
				_, hasLabel := matchLabel(nx.plain)
				if !hasLabel && nx.words > 0 && nx.words <= 10 && !nx.full && (nx.center || nx.bold || nx.caps || d.larger(nx)) {
					bd.title = nx.plain
					bd.consumed = j - bd.at + 1
					bd.why = append(bd.why, "title_line")
				}
				break
			}
		}
		return
	}
	switch {
	case bd.rule == "style":
		bd.title = sg.plain
		bd.caption, bd.lineTitle = chapterLineOf(sg.plain)
	case titleLike(sg):
		bd.title = sg.plain
		bd.caption, bd.lineTitle = chapterLineOf(sg.plain)
	default:
		// a paragraph with no title of its own that ends in a chapter line ("I hope Mr. Bingley will like it. CHAPTER II.") is
		// titled by its label, not by its place among the chapters; the paragraph stays in the text
		if _, label, ok := captionLabel(sg.plain); ok {
			bd.title, bd.capTitle, bd.consumed = label, true, 0
			return
		}
		bd.noTitle, bd.consumed = true, 0
	}
}

// reCaption is a chapter line that ends a title: some text, then a chapter word and its number, and the full stop that may close it.
var reCaption = regexp.MustCompile(`(?i)^(.*\S)\s+((?:` + chapterWords + `)\s+(` + numToken + `))\.?$`)

// captionLabel reads the chapter line at the end of a title. caption is the text before it, label is the line itself, title-cased
// ("CHAPTER II." gives "Chapter II"). It is ok only for a capitalised chapter word, a number the label reads (digits, a Roman
// numeral in capitals, a number word), and some letters before it: a sentence that ends "... chapter two." is not a chapter line.
func captionLabel(s string) (caption, label string, ok bool) {
	m := reCaption.FindStringSubmatch(strings.TrimSpace(s))
	if m == nil || strings.IndexFunc(m[1], unicode.IsLetter) < 0 || len(strings.Fields(m[1])) > 30 {
		return "", "", false
	}
	if r, _ := utf8.DecodeRuneInString(m[2]); !unicode.IsUpper(r) {
		return "", "", false
	}
	if _, ok := numberValue(m[3]); !ok {
		return "", "", false
	}
	return strings.TrimSpace(m[1]), titleCase(m[2]), true
}

// chapterLineOf is the text before the chapter line a title holds, and the label of that line; both are "" when the title holds none.
func chapterLineOf(s string) (caption, label string) {
	caption, label, ok := captionLabel(s)
	if !ok {
		return "", ""
	}
	return caption, label
}

var (
	frontTitles = map[string]bool{"title page": true, "copyright": true, "copyright page": true, "dedication": true, "epigraph": true, "foreword": true, "preface": true, "contents": true, "table of contents": true, "front matter": true}
	backTitles  = map[string]bool{"acknowledgments": true, "acknowledgements": true, "acknowledgment": true, "acknowledgement": true, "about the author": true, "about the authors": true, "glossary": true, "afterword": true, "author's note": true, "authors note": true, "author’s note": true, "back matter": true, "also by": true, "appendix": true}
)

func normTitle(s string) string { return strings.Trim(fold(s), ".:") }

func isBackTitle(s string) bool {
	t := normTitle(s)
	if backTitles[t] || strings.HasPrefix(t, "also by ") || strings.HasPrefix(t, "other books by ") {
		return true
	}
	if rest, ok := strings.CutPrefix(t, "appendix "); ok && len(strings.Fields(rest)) <= 2 {
		return true
	}
	return false
}

// pieceOf is one document of the cut.
type piece struct {
	bd            *boundary // nil: front matter, or the text before the first boundary
	from          int       // first block of the body
	to            int       // one past the last
	title         string
	kind          string
	part          bool
	left          bool
	leftWhy       string
	ord           int
	noTitle       bool
	titleBlock    int
	hasTitleBlock bool
	trimScene     bool   // the writer's own cut (or a cut by size) follows: a scene break that ends the text is not the end of a chapter
	slugWord      string // a chapter with no title of its own is named for its paragraph and this word, not for "Chapter N": its place changes when a break is added above it
	lineTitle     string
	caption       string
	capTitle      bool
	licence       bool // the Project Gutenberg licence text after its END line: one back matter document (licenceApart)
}

// cutErr is a cut that is refused in a way the page says in its own words: a fixed reason, and the number it is about.
type cutErr struct {
	reason, msg string
	n           int
}

func (e *cutErr) Error() string { return e.msg }

// cutError is a cut's error as an op's: the refusals the page knows carry their reason, and anything else is the converter's own words.
func cutError(err error) error { return cutErrorIn("docx", err) }

// cutErrorIn is cutError for a source that is not a Word file: the field it names is the source's.
func cutErrorIn(field string, err error) error {
	var ce *cutErr
	var pe *patternError
	switch {
	case errors.As(err, &ce):
		x := invalidWhy(field, ce.msg, ce.reason)
		if ce.n > 0 {
			x.Data["n"], x.Data["max"] = ce.n, MaxMarkdown
		}
		return x
	case errors.As(err, &pe):
		return pe.asOps()
	}
	return ops.Invalid(field, err.Error())
}

// cut makes the documents. It returns the files, the notes the plan reads by path, and what the preview says of the cut.
func (c *docxConv) cutDocx(opt docxCutOpts, pi *ProjectInfo) ([]File, map[string]*docNote, *BreakInfo, error) {
	d := newDetector(c, opt)
	cs := d.find()
	if d.bookHead >= 0 && c.title == "" {
		pi.Title, _ = cleanLine(noMarks(d.b[d.bookHead].text), 200)
	}
	info := &BreakInfo{Words: d.total, ByRule: map[string]int{}}
	split := opt.split
	if split == "" {
		split = SplitAuto
	}
	info.Rule = split
	if opt.pattern != "" && split != SplitPattern { // a pattern that is kept but not the way the book is cut: what it matches
		if _, pinfo, perr := d.patternBoundaries(opt); perr == nil {
			info.Pattern = pinfo
		}
	}
	if opt.try != "" { // a pattern that is only being tried: what it matches, and nothing is cut by it
		if _, pinfo, perr := d.patternBoundaries(docxCutOpts{pattern: opt.try, match: opt.tryMatch}); perr == nil {
			info.Try = pinfo
		}
	}
	var chosen []boundary
	weakRule := false
	switch split {
	case SplitAuto:
		chosen = d.strongSet(cs)
	case SplitHeading1, SplitHeading2:
		lvl := 1
		if split == SplitHeading2 {
			lvl = 2
		}
		for i, b := range d.b {
			if b.kind == bHeading && d.ok(i) && b.level == lvl {
				bd := d.boundaryAt(i, "heading")
				bd.level = lvl
				bd.why = []string{fmt.Sprintf("heading:%d", lvl)}
				bd.score = scoreWeights.sure
				bd.numbered = d.sigs[i].numLabel
				chosen = append(chosen, bd)
			}
		}
		info.Level = lvl
	case SplitStyle:
		for i, b := range d.b {
			if b.kind == bPara && d.ok(i) && strings.EqualFold(d.sigs[i].style, opt.style) && opt.style != "" {
				bd := d.boundaryAt(i, "style")
				bd.style = d.sigs[i].style
				bd.numbered = d.sigs[i].numLabel
				bd.why = []string{"style:" + d.sigs[i].style}
				bd.score = scoreWeights.sure
				chosen = append(chosen, bd)
			}
		}
		info.Style = strings.ToLower(opt.style)
	case SplitLabel:
		chosen = append(chosen, cs.allLabelLines...)
		for i := range chosen {
			chosen[i].score = d.scoreOf(d.sigs[chosen[i].at], scoreWeights.label, true, false)
		}
		for _, bd := range d.headingsAsLabels() {
			chosen = append(chosen, bd)
		}
	case SplitPageBreaks:
		chosen, weakRule = cs.pages, true
	case SplitSectionBreaks:
		chosen, weakRule = cs.sections, true
	case SplitNumerals:
		chosen, weakRule = cs.numerals, true
	case SplitSize:
		chosen, weakRule = d.sizeBreaks(opt.pieceWords()), true
	case SplitPattern:
		bds, pinfo, perr := d.patternBoundaries(opt)
		switch {
		case perr != nil:
			return nil, nil, nil, perr
		case pinfo != nil && pinfo.Refused != "":
			return nil, nil, nil, &cutErr{reason: "pattern_too_much", msg: "A pattern of that size cannot be tried on this much short text. Make the pattern shorter, or use one that begins the line"}
		}
		chosen, weakRule = bds, true
		info.Pattern = pinfo
	case SplitNone:
	default:
		return nil, nil, nil, fmt.Errorf("docx_split is not one of the ways to cut")
	}
	chosen = dedupe(chosen)
	auto := split == SplitAuto
	if !auto {
		for i := range chosen {
			chosen[i].part = false
		}
	}
	// a cut that would make more documents than an import takes is refused in the writer's words, before anything costs more
	tooMany := func(n int) error {
		return &cutErr{reason: "too_many_docs", n: n, msg: fmt.Sprintf("that way of cutting would make %s documents, and an import takes at most %s. Choose another way to cut it", commaInt(n), commaInt(MaxMarkdown))}
	}
	if len(chosen) >= MaxMarkdown {
		return nil, nil, nil, tooMany(len(chosen) + 1)
	}
	strongCount := len(chosen)
	// the writer's own boundaries
	var added []int
	have := make(map[int]bool, len(chosen)+len(opt.add))
	for _, x := range chosen {
		have[x.at] = true
	}
	for _, ord := range opt.add {
		i := d.blockAtOrdinal(ord)
		if i < 0 || have[i] {
			continue
		}
		have[i] = true
		bd := d.boundaryAt(i, "explicit")
		bd.why = []string{"explicit"}
		bd.score = scoreWeights.sure
		chosen = append(chosen, bd)
		added = append(added, ord)
	}
	chosen = dedupe(chosen)
	if len(chosen) >= MaxMarkdown {
		return nil, nil, nil, tooMany(len(chosen) + 1)
	}
	info.Added = added
	for i := range chosen {
		if chosen[i].rule == "size" {
			continue
		}
		d.titleOf(&chosen[i])
	}
	chosen = d.mergeAdjacent(chosen)
	for i := range chosen {
		d.keepCarriers(&chosen[i])
	}
	noneFound := auto && strongCount == 0
	info.NoneFound = noneFound
	// what was made
	kinds := map[string]bool{}
	for _, bd := range chosen {
		if bd.rule == "explicit" {
			continue
		}
		switch bd.rule {
		case "heading":
			kinds["heading"] = true
		case "style":
			kinds["style"] = true
		case "label", "run":
			kinds["label"] = true
		}
		if bd.part {
			info.Parts++
		}
	}
	switch {
	case !auto && split != SplitNone:
		info.ChosenBy = "explicit"
	case split == SplitNone:
		info.ChosenBy = "none"
	case noneFound:
		info.ChosenBy = "none_found"
	case len(kinds) > 1:
		info.ChosenBy = "mixed"
	case kinds["heading"]:
		info.ChosenBy = "heading"
		info.Level = d.chapLvl
	case kinds["style"]:
		info.ChosenBy = "style"
		for _, bd := range chosen {
			if bd.rule == "style" {
				info.Style = bd.style
				break
			}
		}
	case kinds["label"]:
		info.ChosenBy = "label"
	default:
		info.ChosenBy = "none_found"
	}
	if auto && len(kinds) == 1 && kinds["heading"] {
		info.Level = d.chapLvl
	}
	for _, bd := range chosen {
		info.Chapters++
		sg := d.sigs[bd.at]
		if sg != nil && (sg.brBefore || sg.pageBefore || sg.sectBefore) {
			info.BreakBefore++
		}
	}
	if (weakRule || split == SplitNone || noneFound) && len(chosen) > 0 {
		info.Chapters++ // the text before the first boundary is a chapter of its own
	}
	info.Auto = len(d.strongSet(cs))
	info.Levels = len(d.headings)
	d.options(cs, info, split, opt)

	// pieces
	if !noneFound && len(chosen) > 0 {
		d.frontEnd = chosen[0].at
	}
	pieces := d.pieces(chosen, weakRule || split == SplitNone || noneFound, split, opt, pi)
	d.suggestions(cs, chosen, info, split, noneFound)
	// the front, the contents, the parts and their intros count too, before any file is made
	files := 0
	for _, p := range pieces {
		if !p.part || hasContent(d.slice(p.from, p.to)) {
			files++
		}
	}
	if files > MaxMarkdown {
		return nil, nil, nil, tooMany(files)
	}
	info.kept = d.kept
	files2, notes, err := d.writeFiles(pieces)
	if err != nil {
		return nil, nil, nil, err
	}
	info.intro = d.introMade
	if d.look != nil {
		d.look.fill(d, cs, chosen)
	}
	// a tracked change in a title is taken as the reviewer left it; counted like a heading's
	for _, bd := range chosen {
		for k := 0; k < bd.consumed && !bd.keep; k++ {
			i := bd.at + k
			if i < len(d.b) {
				if n := len(d.b[i].revs); n > 0 && d.b[i].kind != bHeading {
					c.tn.Headings += n
				}
			}
		}
	}
	if d.bookHead >= 0 {
		c.tn.Headings += len(d.b[d.bookHead].revs)
	}
	for i := range d.b {
		if d.dropped[i] && d.b[i].kind != bHeading {
			c.tn.Headings += len(d.b[i].revs)
		}
	}
	// pages, not chapters
	if (weakRule || len(added) > 0) && !noneFound {
		n := len(chosen)
		med := 0
		if n > 0 {
			ws := make([]int, 0, n)
			for _, p := range pieces {
				if !p.left && p.bd != nil {
					ws = append(ws, d.wordsIn(p.from, p.to))
				}
			}
			sort.Ints(ws)
			if len(ws) > 0 {
				med = ws[len(ws)/2]
			}
		}
		if n > manyChapters || (d.total >= 5000 && med > 0 && med < pageLikeWords) {
			info.Hint = "pages"
		}
	}
	return files2, notes, info, nil
}

// cum is the words before each block (and in all, last), made once.
func (d *detector) cum() []int {
	if d.cumWords == nil {
		d.cumWords = make([]int, len(d.b)+1)
		for i := range d.b {
			d.cumWords[i+1] = d.cumWords[i] + d.words[i]
		}
	}
	return d.cumWords
}

func (d *detector) wordsIn(from, to int) int {
	n := 0
	for i := from; i < to && i < len(d.words); i++ {
		n += d.words[i]
	}
	return n
}

func (d *detector) blockAtOrdinal(ord int) int {
	if d.byOrd == nil {
		d.byOrd = []int{}
		for i, sg := range d.sigs {
			if sg != nil && d.ok(i) {
				d.byOrd = append(d.byOrd, i)
			}
		}
	}
	k := sort.Search(len(d.byOrd), func(x int) bool { return d.sigs[d.byOrd[x]].ord >= ord })
	if k < len(d.byOrd) && !imageOnly(d.b[d.byOrd[k]].text) { // a paragraph of pictures only is never where a chapter starts
		return d.byOrd[k]
	}
	return -1
}

// headingsAsLabels gives headings whose text is a label, for docx_split=label.
func (d *detector) headingsAsLabels() []boundary {
	var out []boundary
	for i, b := range d.b {
		if b.kind == bHeading && d.ok(i) {
			if l, ok := matchLabel(d.sigs[i].plain); ok {
				bd := d.boundaryAt(i, "label")
				bd.part = l.part
				bd.why = []string{"label"}
				bd.score = scoreWeights.sure
				out = append(out, bd)
			}
		}
	}
	return out
}

// sizeBreaks cuts the book into pieces of about n words, at the nearest scene break within a fifth, else at a paragraph's end.
func (d *detector) sizeBreaks(n int) []boundary { return d.sizeBreaksIn(0, len(d.b), n) }

// sizeBreaksIn is sizeBreaks for the blocks in [from, to): one chapter's, or the whole book's.
func (d *detector) sizeBreaksIn(from, to, n int) []boundary {
	cum := d.cum()
	total := cum[to] - cum[from]
	if total < 2*n {
		return nil
	}
	var out []boundary
	last := from // block index where the current piece starts
	start := from
	for last < to {
		target := cum[start] + n
		if target >= cum[to]-n/5 {
			break
		}
		cut := -1
		best := math.MaxInt
		lo, hi := cum[start]+n*8/10, cum[start]+n*12/10
		for i := last; i < to; i++ {
			if cum[i+1] > hi {
				break
			}
			if d.b[i].kind == bScene && cum[i+1] >= lo {
				if dist := abs(cum[i+1] - target); dist < best {
					best, cut = dist, i+1
				}
			}
		}
		if cut < 0 {
			for i := last; i < to; i++ {
				if cum[i+1] >= target {
					cut = i + 1
					break
				}
			}
		}
		// the piece starts at the next block that is a paragraph or heading
		for cut >= 0 && cut < to && (d.sigs[cut] == nil || !d.ok(cut)) {
			cut++
		}
		if cut < 0 || cut >= to {
			break
		}
		bd := d.boundaryAt(cut, "size")
		bd.why = []string{"size"}
		bd.score = scoreWeights.sure
		bd.noTitle = true
		out = append(out, bd)
		last, start = cut, cut
	}
	return out
}

func abs(a int) int {
	if a < 0 {
		return -a
	}
	return a
}

// options lists the other ways to cut that exist in this file.
func (d *detector) options(cs *candidates, info *BreakInfo, split string, opt docxCutOpts) {
	add := func(rule, style string, n int, weak bool) {
		if n > 0 {
			info.Options = append(info.Options, BreakOption{Rule: rule, Style: style, Count: n, Weak: weak})
		}
	}
	add(SplitHeading1, "", len(d.headingBoundaries(1)), false)
	add(SplitHeading2, "", len(d.headingBoundaries(2)), false)
	for _, s := range cs.styleOrder {
		add(SplitStyle, s, len(cs.styles[s]), false)
	}
	add(SplitLabel, "", len(cs.allLabelLines)+len(d.headingsAsLabels()), false)
	add(SplitPageBreaks, "", len(cs.pages), true)
	add(SplitSectionBreaks, "", len(cs.sections), true)
	add(SplitNumerals, "", len(cs.numerals), true)
	if d.total >= 2*opt.pieceWords() {
		add(SplitSize, "", len(d.sizeBreaks(opt.pieceWords()))+1, true)
	}
	info.PieceWords = opt.pieceWords()
	if d.total >= sizeSuggestMin {
		info.Pieces = len(d.sizeBreaks(opt.pieceWords())) + 1
	}
}

func (d *detector) headingBoundaries(lvl int) []int {
	var out []int
	for i, b := range d.b {
		if b.kind == bHeading && d.ok(i) && b.level == lvl {
			out = append(out, i)
		}
	}
	return out
}

// suggestions lists the weak candidates that are not already chapters, best first.
func (d *detector) suggestions(cs *candidates, chosen []boundary, info *BreakInfo, split string, noneFound bool) {
	taken := map[int]bool{}
	for _, bd := range chosen {
		taken[bd.at] = true
	}
	for k := range d.frontStarts {
		taken[k] = true // where a named piece of front matter begins is a cut already made
	}
	var all []BreakSuggestion
	cum := d.cum()
	add := func(rule, key string, list []boundary) {
		if split == key {
			return
		}
		n := 0
		for _, bd := range list {
			if taken[bd.at] {
				continue
			}
			n++
			sg := d.sigs[bd.at]
			line, _ := cleanLine(sg.plain, 80)
			if sg.plain == "" {
				line = ""
			}
			all = append(all, BreakSuggestion{Ordinal: bd.ord, Rule: rule, Line: line, Why: bd.why, Words: cum[bd.at], Score: bd.score})
		}
		if n > 0 {
			info.ByRule[rule] = n
		}
	}
	add("page_breaks", SplitPageBreaks, cs.pages)
	add("section_breaks", SplitSectionBreaks, cs.sections)
	add("numerals", SplitNumerals, cs.numerals)
	add("label", SplitLabel, cs.loose)
	sort.SliceStable(all, func(a, b int) bool {
		if all[a].Ordinal == all[b].Ordinal {
			return false
		}
		return all[a].Ordinal < all[b].Ordinal
	})
	// one line a place, whichever rule found it
	var uniq []BreakSuggestion
	for _, s := range all {
		if len(uniq) > 0 && uniq[len(uniq)-1].Ordinal == s.Ordinal {
			continue
		}
		uniq = append(uniq, s)
	}
	info.SuggestionsTotal = len(uniq)
	if len(uniq) > maxSuggestions {
		uniq = uniq[:maxSuggestions]
	}
	info.Suggestions = uniq
	if len(info.ByRule) == 0 {
		info.ByRule = nil
	}
}

// ---- documents ------------------------------------------------------------------------------------------------------

// pieces turns the boundaries into documents, in reading order.
// reGutenbergEnd is the END line of a Project Gutenberg download. The licence text follows it.
var reGutenbergEnd = regexp.MustCompile(`(?i)^\*\*\*\s*END OF (THE|THIS) PROJECT GUTENBERG EBOOK\b`)

// licenceApart sets the Project Gutenberg licence text apart: from its END line to the end of the file it becomes one back matter
// document, and the piece it was in ends before the line. The START line and the header before it are front matter already (they sit
// in the front region), so they are not moved. The search starts at from: the first chapter, or the top of a book cut into one document.
// An END line that starts its own piece is not cut, and a file with no such line is not changed.
func (d *detector) licenceApart(ps []*piece, from int) []*piece {
	at := -1
	for i := from; i < len(d.b); i++ {
		if d.b[i].kind == bPara && d.ok(i) && reGutenbergEnd.MatchString(strings.TrimSpace(d.sigs[i].plain)) {
			at = i
			break
		}
	}
	if at < 0 {
		return ps
	}
	for _, p := range ps {
		if p.left || p.from >= at || at >= p.to {
			continue
		}
		p.to = at
		return append(ps, &piece{from: at, to: len(d.b), kind: "back_matter", title: "Project Gutenberg licence", licence: true, ord: d.ordNear(at)})
	}
	return ps
}

func (d *detector) pieces(chosen []boundary, firstIsChapter bool, split string, opt docxCutOpts, pi *ProjectInfo) []*piece {
	n := len(d.b)
	var ps []*piece
	firstAt := n // the end of the front region: the first boundary
	if len(chosen) > 0 {
		firstAt = chosen[0].at
	}
	whole := len(chosen) == 0
	front := d.frontPieces(0, firstAt, split, whole || firstIsChapter)
	if whole {
		// no chapters: one document, whatever is not a contents page (which is left out)
		for _, f := range front {
			if f.left {
				ps = append(ps, f)
			}
		}
		if body := d.restPiece(0, n); body != nil {
			// the book's own title, else the file's name, else Chapter 1
			body.title = pi.Title
			if body.title == "" {
				body.title = strings.TrimSpace(prettify(strings.TrimSuffix(path.Base(strings.ReplaceAll(opt.name, "\\", "/")), ".docx")))
			}
			if body.title == "" || body.title == "." {
				body.title = "Chapter 1"
			}
			ps = append(ps, body)
		}
		return d.orderPieces(d.licenceApart(ps, 0))
	}
	if firstIsChapter {
		// a rule that makes chapters of pages: the text before the first boundary is a chapter too, not front matter
		for _, f := range front {
			if f.left {
				ps = append(ps, f)
			}
		}
		if first := d.restPiece(0, firstAt); first != nil {
			first.trimScene = len(chosen) > 0 && writersCut(chosen[0].rule)
			d.firstTitle(first, firstAt)
			ps = append(ps, first)
		}
	} else {
		ps = append(ps, front...)
	}
	for k := range chosen {
		bd := &chosen[k]
		end := n
		if k+1 < len(chosen) {
			end = chosen[k+1].at
		}
		p := &piece{bd: bd, from: bd.at + bd.consumed, to: end, title: bd.title, kind: "chapter", noTitle: bd.noTitle, lineTitle: bd.lineTitle, caption: bd.caption, capTitle: bd.capTitle}
		if k+1 < len(chosen) {
			p.trimScene = writersCut(chosen[k+1].rule)
		}
		if bd.consumed == 0 || bd.keep {
			p.from = bd.at
		}
		if bd.part {
			p.part, p.kind = true, "part"
		}
		ps = append(ps, p)
	}
	ps = d.licenceApart(ps, firstAt)
	// a chapter with no title of its own is "Chapter N", by its place among the chapters; a piece of a size cut is "Section N"
	nCh, nSec := 0, 0
	for _, p := range ps {
		if p.part || p.left || p.kind != "chapter" {
			continue
		}
		nCh++
		if p.bd != nil && p.bd.rule == "size" || p.bd == nil && split == SplitSize {
			nSec++
			p.title, p.noTitle, p.slugWord = fmt.Sprintf("Section %d", nSec), false, "section"
			continue
		}
		if p.title == "" {
			p.title, p.noTitle = fmt.Sprintf("Chapter %d", nCh), true
		}
		if p.noTitle || p.capTitle {
			p.slugWord = "chapter" // the key of a chapter does not move when its title changes
		}
	}
	d.kindsFromTitles(ps)
	return d.orderPieces(ps)
}

// firstTitle gives the first chapter of a rule that cuts at pages its title: its first line when that looks like a title, else
// "Chapter N" (said so). The line is the title and leaves the text.
func (d *detector) firstTitle(p *piece, firstAt int) {
	for i := p.from; i < firstAt; i++ {
		if d.hidden(i) || d.sigs[i] == nil {
			continue
		}
		sg := d.sigs[i]
		_, formatted, hasBreak := d.signals(sg)
		if d.b[i].kind == bPara && titleLike(sg) && (formatted || hasBreak) {
			p.title = sg.plain
			if l, ok := matchLabel(sg.plain); ok && l.title != "" {
				p.title = l.title
			}
			p.from = i + 1
			p.titleBlock = i
			p.hasTitleBlock = true
		}
		break
	}
}

// restPiece is the document of the blocks in [from, to) that are text: not a dropped title page and not a contents page. It is nil
// when none is.
func (d *detector) restPiece(from, to int) *piece {
	var idx []int
	for i := from; i < to; i++ {
		if d.hidden(i) {
			continue
		}
		idx = append(idx, i)
	}
	if len(idx) == 0 {
		return nil
	}
	p := &piece{from: from, to: to, kind: "chapter"}
	p.ord = d.ordNear(idx[0])
	if !hasContent(d.slice(from, to)) {
		return nil
	}
	return p
}

// slice is the blocks of [from, to) that belong to the text.
func (d *detector) slice(from, to int) []mdBlock {
	var out []mdBlock
	for i := from; i < to && i < len(d.b); i++ {
		if d.hidden(i) {
			continue
		}
		out = append(out, d.b[i])
	}
	return out
}

func (d *detector) ordNear(i int) int {
	for j := i; j < len(d.b); j++ {
		if d.sigs[j] != nil {
			return d.sigs[j].ord
		}
	}
	for j := i - 1; j >= 0; j-- {
		if d.sigs[j] != nil {
			return d.sigs[j].ord
		}
	}
	return 0
}

var reDedication = regexp.MustCompile(`(?i)^(for|to|in memory of|in loving memory of|dedicated to)\b`)

// frontPieces splits the text before the first chapter: a contents page (left out), and the rest at page breaks, section breaks and
// the styles of front matter, each named for what it is.
func (d *detector) frontPieces(from, to int, split string, wholeIsBody bool) []*piece {
	var ps []*piece
	// contents pages
	i := from
	for i < to {
		if d.inContents[i] {
			j := i
			for j < to && d.inContents[j] {
				j++
			}
			p := &piece{from: i, to: j, title: "Contents", kind: "front_matter", left: true, leftWhy: "contents"}
			p.ord = d.ordNear(i)
			ps = append(ps, p)
			i = j
			continue
		}
		i++
	}
	if wholeIsBody {
		return ps
	}
	// the rest: one piece of Front Matter, unless something in it names itself (copyright, dedication, epigraph, a front style) in a
	// region short enough to be a few pages. A page break or a section break alone never splits it: those are listed as suggestions.
	start := -1
	curNamed := false
	named := func(k int) bool { return d.frontName(k, k+1) != "Front Matter" }
	flush := func(end int) {
		if start < 0 {
			return
		}
		var idx []int
		for k := start; k < end; k++ {
			if !d.hidden(k) {
				idx = append(idx, k)
			}
		}
		if len(idx) > 0 && hasContent(d.slice(start, end)) {
			p := &piece{from: start, to: end, kind: "front_matter"}
			p.title = d.frontName(start, end)
			p.ord = d.ordNear(idx[0])
			ps = append(ps, p)
		}
		start = -1
	}
	short := d.wordsIn(from, to) <= frontRegionWords
	for k := from; k < to; k++ {
		if d.hidden(k) {
			if start >= 0 && d.inContents[k] {
				flush(k)
			}
			continue
		}
		sg := d.sigs[k]
		if start < 0 {
			start, curNamed = k, named(k)
			continue
		}
		if !short || sg == nil {
			continue
		}
		brk := sg.brBefore || sg.pageBefore || sg.sectBefore
		styleStart := frontStyle(sg.style) && !frontStyle(d.styleOf(k-1))
		if styleStart || (brk && (named(k) || curNamed)) {
			flush(k)
			d.frontStarts[k] = true
			start, curNamed = k, named(k)
		}
	}
	flush(to)
	return ps
}

func (d *detector) styleOf(i int) string {
	for j := i; j >= 0; j-- {
		if d.sigs[j] != nil {
			return d.sigs[j].style
		}
	}
	return ""
}

func frontStyle(s string) bool {
	switch s {
	case "dedication", "epigraph", "copyright", "title page":
		return true
	}
	return false
}

// frontName says what a piece of front matter is: copyright, dedication, epigraph, or just front matter.
func (d *detector) frontName(from, to int) string {
	var text strings.Builder
	style := ""
	centre := false
	words := 0
	for k := from; k < to; k++ {
		if sg := d.sigs[k]; sg != nil && !d.hidden(k) {
			if style == "" {
				style, centre = sg.style, sg.center
			}
			text.WriteString(sg.plain + " ")
			words += sg.words
		}
	}
	t := strings.ToLower(text.String())
	if words > 400 && !frontStyle(style) {
		return "Front Matter" // pages of text are not a copyright page, whatever they contain
	}
	switch {
	case style == "dedication" || (words <= 40 && centre && reDedication.MatchString(strings.TrimSpace(t))):
		return "Dedication"
	case style == "epigraph":
		return "Epigraph"
	case style == "copyright" || strings.Contains(t, "all rights reserved") || strings.Contains(t, "©") || strings.Contains(t, "isbn") || strings.HasPrefix(strings.TrimSpace(t), "copyright"):
		return "Copyright"
	case style == "title page":
		return "Title Page"
	}
	return "Front Matter"
}

// kindsFromTitles makes leading chapters that are named front matter front matter, and trailing ones named back matter back matter.
// A "Preface" or "Appendix" in the middle of the book stays a chapter.
func (d *detector) kindsFromTitles(ps []*piece) {
	var chapters []*piece
	for _, p := range ps {
		if p.kind == "chapter" && !p.part && !p.left {
			chapters = append(chapters, p)
		}
	}
	for _, p := range chapters {
		if !frontTitles[normTitle(p.title)] {
			break
		}
		p.kind = "front_matter"
	}
	for i := len(chapters) - 1; i >= 0; i-- {
		p := chapters[i]
		if p.kind == "front_matter" || !isBackTitle(p.title) {
			break
		}
		p.kind = "back_matter"
	}
}

// orderPieces puts documents in reading order. Pieces already are; the front matter comes first by construction.
func (d *detector) orderPieces(ps []*piece) []*piece {
	sort.SliceStable(ps, func(a, b int) bool { return ps[a].from < ps[b].from })
	return ps
}

func (d *detector) slug(s string) string {
	v := projects.Slugify(s)
	if v == "" {
		return "untitled"
	}
	return v
}

// writeFiles composes each piece into a Markdown file named for the paragraph it starts at, so a file's key does not change when
// another boundary is added or taken away. A part is a folder; the chapters after it are in it.
func (d *detector) writeFiles(ps []*piece) ([]File, map[string]*docNote, error) {
	c := d.c
	notes := map[string]*docNote{}
	var out []File
	total := 0
	used := map[int]bool{}
	name := func(ord int, title, word string) (int, string) {
		for used[ord] {
			ord++
		}
		used[ord] = true
		if word == "" {
			word = d.slug(title)
		}
		return ord, fmt.Sprintf("%07d-%s", ord, word)
	}
	add := func(path, title string, blocks []mdBlock) error {
		if d.look != nil { // a look names the files and composes none
			out = append(out, File{Path: path})
			return nil
		}
		est := len(title)
		for _, bl := range blocks {
			est += len(bl.text) + 8
		}
		if est+c.noteSize+total > MaxTotalBytes+MaxTotalBytes/2 {
			return fmt.Errorf("the document is larger than the %d MB import limit", MaxTotalBytes>>20)
		}
		text := c.placeFile(path, title, blocks)
		total += len(text)
		if total > MaxTotalBytes {
			return fmt.Errorf("the document is larger than the %d MB import limit", MaxTotalBytes>>20)
		}
		out = append(out, File{Path: path, Data: []byte(text)})
		return nil
	}
	partDir := ""
	for _, p := range ps {
		ord := p.ord
		if p.bd != nil {
			ord = p.bd.ord
		}
		blocks := d.slice(p.from, p.to)
		for p.trimScene && len(blocks) > 0 && blocks[len(blocks)-1].kind == bScene {
			blocks = blocks[:len(blocks)-1] // the scene break that ended the text is the break between chapters now
		}
		if p.left { // a contents page's own lines: they are left out of the import, and come back whole when put back
			blocks = nil
			for i := p.from; i < p.to && i < len(d.b); i++ {
				if !d.dropped[i] {
					blocks = append(blocks, d.b[i])
				}
			}
		}
		note := &docNote{kind: p.kind, ordinal: ord, left: p.left, leftWhy: p.leftWhy, noTitle: p.noTitle, lineTitle: p.lineTitle, caption: p.caption}
		if p.licence {
			note.why = []string{"licence"}
		}
		if p.bd != nil {
			note.why, note.score, note.hasScore = p.bd.why, p.bd.score, true
			note.numbered, note.explicit = p.bd.numbered, p.bd.rule == "explicit"
			note.check = p.bd.score < scoreWeights.sure
		}
		if p.part {
			o, base := name(ord, p.title, p.slugWord)
			partDir = base
			notes[base+"/"] = &docNote{kind: "part", title: p.title, ordinal: ord, why: note.why, score: note.score, hasScore: note.hasScore, check: note.check}
			if hasContent(blocks) {
				d.introMade = true
				pth := partDir + "/" + fmt.Sprintf("%07d-%s.md", o, d.slug(p.title))
				if err := add(pth, p.title, blocks); err != nil {
					return nil, nil, err
				}
				if d.look != nil && d.look.key == pth {
					d.look.p = p
				}
				note.kind = "chapter"
				notes[pth] = note
			}
			continue
		}
		if p.kind != "chapter" {
			partDir = "" // front and back matter are at the top level
		}
		if !p.left && p.bd == nil && !hasContent(blocks) {
			continue
		}
		_, base := name(ord, p.title, p.slugWord)
		pth := base + ".md"
		if partDir != "" {
			pth = partDir + "/" + pth
		}
		if err := add(pth, p.title, blocks); err != nil {
			return nil, nil, err
		}
		if d.look != nil && d.look.key == pth {
			d.look.p = p
		}
		note.ordinal = ord
		notes[pth] = note
		if p.part {
			continue
		}
	}
	if len(out) == 0 {
		return nil, nil, fmt.Errorf("the document has no text to import")
	}
	return out, notes, nil
}

// ---- titles in capitals ---------------------------------------------------------------------------------------------

// capsTitle says a title is written in capitals only: three letters or more, none lower case.
func capsTitle(s string) bool {
	letters := 0
	for _, r := range s {
		if unicode.IsLower(r) {
			return false
		}
		if unicode.IsUpper(r) {
			letters++
		}
	}
	return letters >= 3
}

// titleCase is the title in title case, with small words in lower case as prettify has them. A Roman numeral stays as it is.
func titleCase(s string) string {
	words := strings.Fields(s)
	for i, w := range words {
		var b strings.Builder
		up := true
		core := strings.Trim(w, "\"'“”‘’()[]:;,.!?—–-")
		if _, isNum := romanValue(core); isNum && core != "" && (len(words) == 1 || i > 0 && reChapterWord.MatchString(strings.Trim(strings.ToLower(words[i-1]), ":.-—–"))) {
			continue // a Roman numeral stays where it stands as a chapter's number: "CHAPTER XIV", not "THE MIX" or "MILL"
		}
		for _, r := range strings.ToLower(w) {
			switch {
			case up && unicode.IsLetter(r):
				b.WriteRune(unicode.ToUpper(r))
				up = false
			case r == '-' || r == '–' || r == '—' || r == '/':
				b.WriteRune(r)
				up = true
			default:
				b.WriteRune(r)
				if unicode.IsLetter(r) || unicode.IsDigit(r) {
					up = false
				}
			}
		}
		t := b.String()
		if i > 0 && i < len(words)-1 && smallWords[strings.ToLower(core)] && !strings.HasSuffix(words[i-1], ":") {
			t = strings.ToLower(w)
		}
		words[i] = t
	}
	return strings.Join(words, " ")
}

var (
	firstLineMarks = strings.NewReplacer("**", "", "*", "", "\\", "", "~~", "", "`", "")
	tableCellMarks = strings.NewReplacer("**", "", "*", "", "\\", "")
)

// firstLine is the start of a document, one line of at most 80 characters, with the Markdown that marks it taken out.
func firstLine(body string) string {
	for _, l := range strings.SplitN(body, "\n", 40) {
		t := strings.TrimSpace(l)
		if t == "" || strings.HasPrefix(t, "%%") || strings.HasPrefix(t, "<") || strings.HasPrefix(t, "[^") || t == "* * *" || strings.HasPrefix(t, "---") || strings.HasPrefix(t, "|") {
			continue
		}
		t = strings.TrimLeft(t, "#>-+* \t0123456789.")
		t = firstLineMarks.Replace(t)
		t = strings.Join(strings.Fields(t), " ")
		if t == "" {
			continue
		}
		if utf8.RuneCountInString(t) > 80 {
			r := []rune(t)
			t = strings.TrimSpace(string(r[:80])) + "…"
		}
		return t
	}
	return ""
}

var _ = docs.WarnWords

// tableStats counts the Word tables in a document's Markdown: each run of pipe rows is one table, its separator row is not a row, and
// an empty cell (a blank one, or the rest of a span) is not counted. first is the first row's cells as text, joined with " · ", for
// a preview line when the document has no text of its own. Fenced code is not a table. Only the review reads it; no word is counted
// here.
func tableStats(body string) (tables, cells int, first string) {
	inTable, inFence := false, false
	for _, l := range strings.Split(body, "\n") {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "```") {
			inFence, inTable = !inFence, false
			continue
		}
		if inFence || !strings.HasPrefix(t, "|") {
			inTable = false
			continue
		}
		row := tableCells(t)
		if isSeparatorRow(row) {
			continue
		}
		if !inTable {
			tables++
			inTable = true
		}
		var texts []string
		for _, c := range row {
			if c != "" {
				cells++
				texts = append(texts, c)
			}
		}
		if first == "" && tables == 1 && len(texts) > 0 {
			first = tableCellMarks.Replace(strings.Join(texts, " · "))
		}
	}
	return tables, cells, first
}

// tableCells splits one pipe row into its cells, with \| kept inside a cell.
func tableCells(row string) []string {
	row = strings.TrimPrefix(strings.TrimSuffix(row, "|"), "|")
	var out []string
	for _, c := range strings.Split(strings.ReplaceAll(row, `\|`, "\x00"), "|") {
		out = append(out, strings.TrimSpace(strings.ReplaceAll(c, "\x00", `\|`)))
	}
	return out
}

// isSeparatorRow says a row is the --- line under a table's first row.
func isSeparatorRow(cells []string) bool {
	n := 0
	for _, c := range cells {
		if c == "" {
			continue
		}
		if strings.Trim(c, ":-") != "" || !strings.Contains(c, "-") {
			return false
		}
		n++
	}
	return n > 0
}

// provenance is what the project's source records of the cut: the rule and the paragraphs the boundaries are at. The text of the
// file is not in it.
func (b *BreakInfo) provenance(notes map[string]*docNote) map[string]any {
	var ords []int
	for _, n := range notes {
		if n.hasScore && n.kind != "part" || n.kind == "part" {
			ords = append(ords, n.ordinal)
		}
	}
	sort.Ints(ords)
	if len(ords) > 1000 {
		ords = ords[:1000]
	}
	return map[string]any{"rule": b.Rule, "chosen_by": b.ChosenBy, "chapters": b.Chapters, "boundaries": ords}
}

// commaInt writes a number with thousands separators.
func commaInt(n int) string {
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

// writersCut says a boundary is a cut the writer chose (or one by size, or by a page or section break or a lone number), not the
// chapter's own heading, style or label: before one of those a scene break that ends the text is a mark between scenes that is now a
// mark between chapters, and it is not kept as the last line of the chapter.
func writersCut(rule string) bool {
	switch rule {
	case "heading", "style", "label", "run":
		return false
	}
	return true
}
