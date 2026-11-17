package pdfread

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ErrNoTextLayer is the refusal for a PDF that is pictures of pages: the
// importer reads text, it does not recognise letters in pictures.
var ErrNoTextLayer = errors.New("this PDF has no text layer: its pages are pictures (a scan). Scríobh reads the text a PDF carries and does no character recognition. Run the file through an OCR program first, or import the manuscript from its source")

// PageReport says how far one page can be trusted.
type PageReport struct {
	Page       int      `json:"page" desc:"1-based."`
	Confidence int      `json:"confidence" desc:"0 to 100."`
	Notes      []string `json:"notes,omitempty"`
}

// Report is the fidelity report: what was recovered and what was not, from
// measurements of the file.
type Report struct {
	Pages            int          `json:"pages"`
	TextPages        int          `json:"text_pages"`
	BlankPages       int          `json:"blank_pages"`
	PictureOnlyPages []int        `json:"picture_only_pages,omitempty"`
	PictureOnlyTotal int          `json:"picture_only_total"`
	Images           int          `json:"images" desc:"Pictures drawn on the pages; none is imported."`
	BodySize         float64      `json:"body_size_pt"`
	HeadingSizes     []float64    `json:"heading_sizes_pt,omitempty"`
	ChapterSource    string       `json:"chapter_source" desc:"outline, headings or none."`
	OutlineItems     int          `json:"outline_items"`
	OutlineUsed      int          `json:"outline_used"`
	TwoColumnPages   int          `json:"two_column_pages"`
	AmbiguousColumns []int        `json:"ambiguous_column_pages,omitempty"`
	HeadLines        int          `json:"running_head_lines_removed"`
	PageNumbers      int          `json:"page_number_lines_removed"`
	HeadRule         string       `json:"running_head_rule"`
	TOCPages         []int        `json:"contents_pages_removed,omitempty"`
	TOCLines         int          `json:"contents_lines_removed"`
	TableRows        int          `json:"table_rows_flattened"`
	TablePages       []int        `json:"table_pages,omitempty"`
	Notes            int          `json:"footnotes_found"`
	NotesLinked      int          `json:"footnotes_linked"`
	NotesUnlinked    int          `json:"footnotes_kept_as_paragraphs"`
	NoteMarkersLeft  int          `json:"superscripts_left_as_text"`
	HyphenJoined     int          `json:"hyphens_joined"`
	HyphenKept       int          `json:"hyphens_kept"`
	SoftHyphens      int          `json:"soft_hyphens_removed"`
	Ligatures        int          `json:"ligatures_expanded"`
	SceneBreaks      int          `json:"scene_breaks"`
	ListItems        int          `json:"list_items"`
	Quotes           int          `json:"block_quotes"`
	CodeLines        int          `json:"code_lines"`
	InlineCode       bool         `json:"inline_code_kept"`
	GapSceneBreaks   int          `json:"scene_breaks_from_space"`
	BoldRuns         int          `json:"bold_runs"`
	ItalicRuns       int          `json:"italic_runs"`
	RotatedChars     int          `json:"rotated_or_upside_down_chars_left_out"`
	VerticalChars    int          `json:"vertical_chars_left_out"`
	RTLPages         []int        `json:"right_to_left_pages,omitempty"`
	OCRPages         []int        `json:"invisible_text_pages,omitempty"`
	UnmappedChars    int          `json:"unmapped_chars_left_out"`
	WidthlessFonts   []int        `json:"pages_with_fonts_without_widths,omitempty"`
	TruncatedPages   []int        `json:"truncated_pages,omitempty"`
	JoinedAcross     int          `json:"paragraphs_joined_across_pages"`
	Confidence       int          `json:"confidence" desc:"The mean of the pages' confidence, 0 to 100."`
	LowConfidence    int          `json:"low_confidence_pages" desc:"Pages under 70."`
	PageDetail       []PageReport `json:"page_detail,omitempty" desc:"The least reliable pages (at most 100), worst first."`
	Facts            []string     `json:"facts,omitempty"`
}

// Result is what the analysis recovers.
type Result struct {
	Title, Subtitle, Author string
	BodyMono                bool // the text is set in a monospaced face: a monospaced run is not code
	Front                   []Block
	Chapters                []Chapter
	Report                  Report
}

// pnote is a footnote found at the foot of a page.
type pnote struct {
	marker string
	runs   []Run
	label  string
	linked bool
	page   int
}

type pageInfo struct {
	lines  []*pline
	notes  []*pnote
	cols   int
	amb    bool
	conf   float64
	notes_ []string
	h, w   float64
}

var (
	labelRe    = regexp.MustCompile(`(?i)^(chapter|part|book|section|act|canto)\s+([0-9]+|[ivxlcdm]+|one|two|three|four|five|six|seven|eight|nine|ten|eleven|twelve|thirteen|fourteen|fifteen|sixteen|seventeen|eighteen|nineteen|twenty)\.?$`)
	noteMarkRe = regexp.MustCompile(`^(\d{1,3}|[*†‡§¶]{1,3})(?:[\s.)\]]+|$)`)
	chapPrefix = regexp.MustCompile(`(?i)^(chapter|part|book)\s+([0-9]+|[ivxlcdm]+)\s*[:.\-–—]\s*(.+)$`)
	ornamentRe = regexp.MustCompile(`^[\s*#•·–—_~=+⁂❦❧☙✻✽❖◆◇●○♦†‡§.\-⁕]{1,24}$`)
)

func isScene(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || utf8.RuneCountInString(s) > 24 {
		return false
	}
	if !ornamentRe.MatchString(s) {
		return false
	}
	// a lone dash or full stop is no ornament
	n := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	if n == 1 && strings.ContainsAny(s, ".-–—_~=+·•") {
		return false
	}
	return true
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	s := append([]float64(nil), v...)
	sort.Float64s(s)
	return s[len(s)/2]
}

// Analyse turns the pages of a PDF into chapters. It refuses a PDF that is
// pictures of pages.
func Analyse(ctx context.Context, pages []*Page, outline []OutlineItem, info Info) (*Result, error) {
	res := &Result{Title: info.Title, Author: info.Author}
	rep := &res.Report
	rep.Pages = len(pages)
	rep.OutlineItems = len(outline)
	rep.HeadRule = "A line in the top or bottom 8% of a page that stands apart from the text (more than 1.25 lines clear of its neighbour) and whose text, with digits read as one mark, is the same on at least 3 pages is a running head or foot and is removed. A line in those bands that is only a number, a roman numeral, \"Page N\" or \"N of M\" is a page number and is removed wherever it stands."
	body := bodySize(pages)
	rep.BodySize = body
	pis := make([]*pageInfo, len(pages))
	nonBlank := 0
	for i, p := range pages {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pi := &pageInfo{conf: 1, h: p.H, w: p.W}
		pis[i] = pi
		rep.Images += len(p.Images)
		pi.lines, pi.cols, pi.amb = pageLines(p, body)
		invisible := hasInvisible(p)
		p.Frags = nil // the lines hold the pieces now
		if len(pi.lines) == 0 {
			pi.conf = 0
			if p.Truncated {
				rep.TruncatedPages = append(rep.TruncatedPages, i+1)
				pi.note("a limit was reached on this page (or a stream of it could not be read); nothing was read from it")
				continue // an unread page is no evidence that the file is a scan
			}
			if len(p.Images) > 0 || p.Glyphs > 0 {
				rep.PictureOnlyTotal++
				if len(rep.PictureOnlyPages) < 100 {
					rep.PictureOnlyPages = append(rep.PictureOnlyPages, i+1)
				}
				nonBlank++
				pi.note("no text layer on this page (a picture or only unmapped characters); nothing was read from it")
			} else {
				rep.BlankPages++
				pi.conf = 1
			}
			continue
		}
		nonBlank++
		rep.TextPages++
		rep.RotatedChars += p.Rotated
		rep.VerticalChars += p.Vertical
		rep.UnmappedChars += p.Unmapped
		if p.Rotated > 0 {
			pi.penalty(math.Min(0.3, 0.6*float64(p.Rotated)/float64(max(1, p.Glyphs))), fmt.Sprintf("%d characters set at an angle or upside down were left out", p.Rotated))
		}
		if p.Vertical > 0 {
			pi.penalty(0.4, fmt.Sprintf("%d characters in vertical writing were left out", p.Vertical))
		}
		if p.Unmapped > 0 {
			pi.penalty(math.Min(0.6, 3*float64(p.Unmapped)/float64(max(1, p.Glyphs))), fmt.Sprintf("%d characters had no known meaning and were left out", p.Unmapped))
		}
		if p.RTL > 0 {
			rep.RTLPages = append(rep.RTLPages, i+1)
			pi.penalty(0.35, "right-to-left text: it is kept in the order the file stores it, which is not always the reading order")
		}
		if invisible {
			rep.OCRPages = append(rep.OCRPages, i+1)
			pi.penalty(0.2, "the text is invisible (an OCR layer over a picture of the page); its accuracy is the recogniser's")
		}
		if p.WidthlessFont {
			rep.WidthlessFonts = append(rep.WidthlessFonts, i+1)
			pi.penalty(0.1, "a font without widths: spaces between words are less certain")
		}
		if p.Truncated {
			rep.TruncatedPages = append(rep.TruncatedPages, i+1)
			pi.penalty(0.4, "a limit was reached on this page; part of it was not read")
		}
		if p.FontsDamaged {
			pi.penalty(0.2, "a font's character map was cut at its limit")
		}
		if pi.cols == 2 {
			rep.TwoColumnPages++
		}
		if pi.amb {
			rep.AmbiguousColumns = append(rep.AmbiguousColumns, i+1)
			pi.penalty(0.3, "the page may be set in columns or a table; it was read as one column")
		}
		if len(p.Images) > 0 {
			pi.note(fmt.Sprintf("%d picture(s) on this page were not imported", len(p.Images)))
		}
	}
	if rep.TextPages == 0 || rep.TextPages*2 < nonBlank {
		return nil, ErrNoTextLayer
	}
	// The leading of the body: the usual distance between neighbouring lines.
	var dys []float64
	for _, pi := range pis {
		for k := 1; k < len(pi.lines); k++ {
			a, b := pi.lines[k-1], pi.lines[k]
			if a.region == b.region && a.col == b.col {
				if dy := a.y - b.y; dy > 0.8*body && dy < 2.2*body && math.Abs(a.size-body) < 0.15*body && math.Abs(b.size-body) < 0.15*body {
					dys = append(dys, dy)
				}
			}
		}
	}
	lead := median(dys)
	if lead == 0 {
		lead = 1.2 * body
	}
	stripFurniture(pis, pages, rep, lead)
	stripContents(pis, rep)
	// Foot notes.
	var all []*pline
	var allNotes []*pnote
	for i, pi := range pis {
		pi.footnotes(pages[i], body, rep, allNotes)
		allNotes = append(allNotes, pi.notes...)
		for _, l := range pi.lines {
			if l.removed == "" && !l.fnote {
				l.idx = len(all)
				all = append(all, l)
			}
		}
	}
	b := newAssembler(res, pis, all, body, lead)
	b.breakAt(outline)
	res.BodyMono = b.bodyMono
	if err := b.run(ctx); err != nil {
		return nil, err
	}
	// Page confidence.
	for i, pi := range pis {
		pi.fold(i, rep)
	}
	rep.Confidence = 0
	if n := rep.Pages - rep.BlankPages; n > 0 {
		sum := 0.0
		for i, pi := range pis {
			if len(pi.lines) > 0 || len(pages[i].Images) > 0 || pages[i].Glyphs > 0 {
				sum += pi.conf
			}
		}
		rep.Confidence = int(math.Round(100 * sum / float64(n)))
	}
	b.chapters(outline)
	return res, nil
}

func hasInvisible(p *Page) bool {
	n := 0
	for _, f := range p.Frags {
		if f.Invisible {
			n++
		}
	}
	return n > 0 && n >= len(p.Frags)/2
}

func (pi *pageInfo) note(s string) { pi.notes_ = append(pi.notes_, s) }
func (pi *pageInfo) penalty(v float64, s string) {
	pi.conf -= v
	if pi.conf < 0 {
		pi.conf = 0
	}
	pi.note(s)
}

// fold adds a page's confidence to the report (the least reliable are listed).
func (pi *pageInfo) fold(i int, rep *Report) {
	c := int(math.Round(100 * pi.conf))
	if c < 70 {
		rep.LowConfidence++
	}
	if c < 100 || len(pi.notes_) > 0 {
		rep.PageDetail = append(rep.PageDetail, PageReport{Page: i + 1, Confidence: c, Notes: pi.notes_})
	}
	if len(rep.PageDetail) > 4000 {
		sort.SliceStable(rep.PageDetail, func(a, b int) bool { return rep.PageDetail[a].Confidence < rep.PageDetail[b].Confidence })
		rep.PageDetail = rep.PageDetail[:100]
	}
}

// finishDetail keeps the 100 least reliable pages, worst first.
func (r *Report) finishDetail() {
	sort.SliceStable(r.PageDetail, func(a, b int) bool {
		if r.PageDetail[a].Confidence != r.PageDetail[b].Confidence {
			return r.PageDetail[a].Confidence < r.PageDetail[b].Confidence
		}
		return r.PageDetail[a].Page < r.PageDetail[b].Page
	})
	if len(r.PageDetail) > 100 {
		r.PageDetail = r.PageDetail[:100]
	}
	for i := range r.PageDetail {
		if len(r.PageDetail[i].Notes) > 6 {
			r.PageDetail[i].Notes = r.PageDetail[i].Notes[:6]
		}
	}
}

// stripFurniture removes running heads, feet and page numbers.
func stripFurniture(pis []*pageInfo, pages []*Page, rep *Report, lead float64) {
	type cand struct {
		l  *pline
		pi *pageInfo
	}
	seen := map[string]map[int]bool{}
	var cands []cand
	for i, pi := range pis {
		band := 0.08 * pi.h
		for k, l := range pi.lines {
			top, bot := l.y > pi.h-band, l.y < band
			if !top && !bot {
				continue
			}
			if isNumberOnly(l.text) || pageWord(l.text) {
				l.removed = "pageno"
				rep.PageNumbers++
				continue
			}
			// stands apart from its neighbour in the same band's direction
			apart := true
			if top && k+1 < len(pi.lines) {
				apart = l.y-pi.lines[k+1].y > 1.25*lead
			}
			if bot && k > 0 {
				apart = pi.lines[k-1].y-l.y > 1.25*lead
			}
			if !apart {
				continue
			}
			key := shape(l.text)
			if key == "" {
				continue
			}
			if seen[key] == nil {
				seen[key] = map[int]bool{}
			}
			seen[key][i] = true
			cands = append(cands, cand{l, pi})
		}
	}
	for _, c := range cands {
		if len(seen[shape(c.l.text)]) >= 3 && c.l.removed == "" {
			c.l.removed = "head"
			rep.HeadLines++
		}
	}
}

// stripContents removes a table of contents: on a page where most lines end in
// a page number after leaders (or, with no leaders, in numbers that only go
// up, as a table of contents' do and an index's do not), those lines and the
// "Contents" heading over them. Other lines of the page stay.
func stripContents(pis []*pageInfo, rep *Report) {
	for i, pi := range pis {
		var live, match []*pline
		leaders := 0
		for _, l := range pi.lines {
			if l.removed != "" {
				continue
			}
			live = append(live, l)
			if l.leader {
				leaders++
				match = append(match, l)
			} else if endsInPageNumber(l) {
				match = append(match, l)
			}
		}
		if len(live) < 4 || len(match) < 4 || len(match)*100 < 55*len(live) {
			continue
		}
		if leaders < 4 && !ascending(match) {
			continue
		}
		for _, l := range match {
			l.removed = "toc"
			rep.TOCLines++
		}
		for _, l := range live {
			if l.removed == "" && contentsHeading(l.text) {
				l.removed = "toc"
				rep.TOCLines++
			}
		}
		rep.TOCPages = append(rep.TOCPages, i+1)
	}
}

func contentsHeading(s string) bool {
	switch strings.ToLower(strings.Trim(strings.TrimSpace(s), ".:")) {
	case "contents", "table of contents", "contents of the book":
		return true
	}
	return false
}

// ascending says the trailing numbers of the lines mostly do not go down, and
// that no line is index-style ("term, 12, 45").
func ascending(ls []*pline) bool {
	prev, pairs, down := -1, 0, 0
	for _, l := range ls {
		f := strings.Fields(l.text)
		if len(f) < 2 {
			return false
		}
		last := f[len(f)-1]
		if strings.HasSuffix(f[len(f)-2], ",") {
			return false
		}
		n, err := strconv.Atoi(strings.Trim(last, "()[]."))
		if err != nil {
			continue
		}
		if prev >= 0 {
			pairs++
			if n < prev {
				down++
			}
		}
		prev = n
	}
	return pairs >= 3 && down*5 <= pairs
}

func endsInPageNumber(l *pline) bool {
	f := strings.Fields(l.text)
	if len(f) < 2 {
		return false
	}
	last := f[len(f)-1]
	return isNumberOnly(last) && l.gaps > 0
}

func (pi *pageInfo) liveLines() []*pline {
	var out []*pline
	for _, l := range pi.lines {
		if l.removed == "" && !l.fnote {
			out = append(out, l)
		}
	}
	return out
}

// footnotes finds the notes at the foot of a page: small text under the body,
// starting with a marker.
func (pi *pageInfo) footnotes(p *Page, body float64, rep *Report, earlier []*pnote) {
	live := pi.liveLines()
	if len(live) < 3 {
		return
	}
	start := -1
	for k := len(live) - 1; k >= 0; k-- {
		l := live[k]
		if l.size <= 0.88*body && l.y < 0.45*pi.h && !isScene(l.text) {
			start = k
			continue
		}
		break
	}
	if start <= 0 { // no body above, or none of it small
		return
	}
	first := live[start]
	if m := noteMarkRe.FindStringSubmatch(first.text); m == nil && !startsWithSup(first) {
		return // text that is merely small (a caption, an epigraph) is not a note
	}
	var cur *pnote
	for _, l := range live[start:] {
		l.fnote = true
		marker, runs := noteStart(l)
		if marker != "" {
			cur = &pnote{marker: marker, page: p.Index, label: fmt.Sprintf("p%dn%s", p.Index+1, marker)}
			pi.notes = append(pi.notes, cur)
			cur.runs = runs
			continue
		}
		if cur == nil { // a note continued from the page before
			if n := len(earlier); n > 0 {
				cur = earlier[n-1]
			} else {
				cur = &pnote{marker: "", page: p.Index, label: fmt.Sprintf("p%dn0", p.Index+1)}
				pi.notes = append(pi.notes, cur)
			}
		}
		cur.runs = append(cur.runs, Run{Text: " "})
		cur.runs = append(cur.runs, plainRuns(l)...)
	}
	rep.Notes += len(pi.notes)
	// markers in the body of this page
	for _, l := range live[:start] {
		for ri, r := range l.runs {
			if s, ok := isSupRun(r); ok {
				for _, n := range pi.notes {
					if n.marker == s && !n.linked {
						l.runs[ri] = Run{Note: n.label}
						n.linked = true
						rep.NotesLinked++
						break
					}
				}
			}
		}
	}
	pi.penaltyIf(len(pi.notes) > 0, 0.03, fmt.Sprintf("%d footnote(s) were taken from the foot of the page", len(pi.notes)))
}

func (pi *pageInfo) penaltyIf(c bool, v float64, s string) {
	if c {
		pi.penalty(v, s)
	}
}

func startsWithSup(l *pline) bool {
	if len(l.runs) == 0 {
		return false
	}
	_, ok := isSupRun(l.runs[0])
	return ok
}

// noteStart reads a note's marker from the start of its first line.
func noteStart(l *pline) (string, []Run) {
	runs := append([]Run(nil), l.runs...)
	if len(runs) > 0 {
		if s, ok := isSupRun(runs[0]); ok {
			return s, plainRunsOf(runs[1:])
		}
		if m := noteMarkRe.FindStringSubmatch(strings.TrimLeft(runs[0].Text, " ")); m != nil && !strings.HasPrefix(runs[0].Text, supOpen) {
			t := strings.TrimLeft(runs[0].Text, " ")
			runs[0].Text = strings.TrimLeft(t[len(m[0]):], " ")
			return m[1], plainRunsOf(runs)
		}
	}
	return "", nil
}

func plainRuns(l *pline) []Run { return plainRunsOf(l.runs) }

func plainRunsOf(runs []Run) []Run {
	var out []Run
	for _, r := range runs {
		if s, ok := isSupRun(r); ok {
			r = Run{Text: s}
		}
		out = append(out, r)
	}
	return out
}
