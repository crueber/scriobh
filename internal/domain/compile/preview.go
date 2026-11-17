package compile

import (
	"math"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
	"github.com/crueber/scriobh/internal/text"
)

// PreviewIn asks what a compile would make.
type PreviewIn struct {
	ProjectID string          `path:"id" json:"-" desc:"Project id."`
	Preset    string          `json:"preset,omitempty" desc:"As for export.compile. Default shunn-us."`
	Options   publish.Options `json:"options,omitempty" desc:"Options that override the preset."`
	Pages     int             `json:"pages,omitempty" desc:"How many pages to return as blocks, 1 to 12 (default 3)."`
}

// ProjectRef names the project.
func (in PreviewIn) ProjectRef() string { return in.ProjectID }

// Validate bounds the page count.
func (in *PreviewIn) Validate() error {
	if in.Pages < 0 || in.Pages > maxPreviewPages {
		return ops.Invalid("pages", "pages is 1 to 12.")
	}
	return nil
}

const (
	maxPreviewPages = 12
	maxExcluded     = 200
	maxBlockText    = 6000
)

// TitlePage is what the title page says, line by line.
type TitlePage struct {
	Layout    string   `json:"layout" enum:"manuscript,book,none"`
	Contact   []string `json:"contact,omitempty" desc:"The contact block, top left (manuscript layout)."`
	WordCount string   `json:"word_count,omitempty" desc:"The word count line, top right ('about 84,300 words')."`
	Title     string   `json:"title"`
	Subtitle  string   `json:"subtitle,omitempty"`
	Byline    string   `json:"byline,omitempty" desc:"'by Name'."`
}

// Block is one piece of a previewed page.
type Block struct {
	Kind      string `json:"kind" enum:"heading,paragraph,scene_break,quote,list_item,code,table_row,drop" desc:"drop is the space above a chapter title."`
	Text      string `json:"text,omitempty" desc:"Plain text: emphasis is not shown."`
	Indent    bool   `json:"indent,omitempty" desc:"The paragraph's first line is indented."`
	Continues bool   `json:"continues,omitempty" desc:"A paragraph that began on the page before."`
	Lines     int    `json:"lines,omitempty" desc:"The space a drop takes, in lines."`
}

// Page is one estimated page.
type Page struct {
	Number    int     `json:"number"`
	Header    string  `json:"header,omitempty" desc:"The running header."`
	Footer    string  `json:"footer,omitempty"`
	TitlePage bool    `json:"title_page,omitempty"`
	Blocks    []Block `json:"blocks"`
}

// NotesExcluded counts the notes a compile leaves out.
type NotesExcluded struct {
	Documents int `json:"documents"`
	Words     int `json:"words"`
}

// PreviewOut is what a compile would contain. Nothing was written.
type PreviewOut struct {
	Preset        string          `json:"preset" desc:"The preset's id."`
	PresetName    string          `json:"preset_name"`
	Format        string          `json:"format" enum:"docx,epub,md,pdf,html-paged,odt,rtf,latex,rst,mediawiki"`
	Options       publish.Options `json:"options" desc:"Every option, resolved: the preset with your overrides."`
	InputHash     string          `json:"input_hash" desc:"What the export will record: the same input hash is the same content."`
	Documents     int             `json:"documents" desc:"Documents in the compile."`
	Words         int             `json:"words" desc:"Words in the compiled text."`
	ApproxWords   int             `json:"approx_words" desc:"Words rounded the way a manuscript title page states them."`
	PageCount     int             `json:"page_count" desc:"Pages: estimated for a DOCX (Word decides the layout), exact for a PDF (the preview runs the same layout as the export, so it is the page count of the file); 0 for formats that have none."`
	PageEstimated bool            `json:"page_estimated" desc:"True for a DOCX; false for a PDF, whose page_count is the real one."`
	Print         *PrintPreview   `json:"print,omitempty" desc:"PDF presets: the page geometry, how the pages are numbered and the text of the first pages as placed lines."`
	TitlePage     TitlePage       `json:"title_page"`
	RunningHeader string          `json:"running_header,omitempty" desc:"The header as it reads on page 2."`
	Outline       []OutlineItem   `json:"outline" desc:"The sections of the file in order, with their words and the page each is estimated to start on."`
	Pages         []Page          `json:"pages" desc:"The first pages, as blocks, laid out by an estimate of the preset's type and page (DOCX); empty for a PDF, which has print.pages instead."`
	Warnings      []Warning       `json:"warnings"`
	Excluded      []Excluded      `json:"excluded" desc:"Documents left out because they are marked do not compile (the first 200), with the reason."`
	ExcludedTotal int             `json:"excluded_total" desc:"Documents left out because they are marked do not compile, or sit inside one that is (notes aside)."`
	SwitchedOff   int             `json:"switched_off" desc:"Documents left out because the preset switches front or back matter off."`
	Notes         NotesExcluded   `json:"notes_excluded" desc:"Notes left out (notes: appendix puts them in)."`
}

// PrintPreview is what the layout of a PDF found.
type PrintPreview struct {
	publish.LayoutMetrics
	FrontPages int                  `json:"front_pages" desc:"Pages numbered in roman before the body."`
	BodyStart  int                  `json:"body_start" desc:"The physical page that is numbered 1."`
	Leaves     int                  `json:"leaves" desc:"Sheets of paper: pages / 2."`
	Pages      []publish.LayoutPage `json:"pages" desc:"The first pages (the pages argument) as lines with their positions, in points from the top left of the trim box."`
}

func preview(c *ops.Ctx, in PreviewIn) (PreviewOut, error) {
	r, err := resolve(c, in.ProjectID, in.Preset, in.Options, false)
	if err != nil {
		return PreviewOut{}, err
	}
	n := in.Pages
	if n == 0 {
		n = 3
	}
	o := r.opts
	b := r.b.book
	out := PreviewOut{Preset: r.preset.ID, PresetName: r.preset.Name, Format: o.Format, Options: o, InputHash: r.hash,
		Documents: len(r.b.held), Words: r.b.words, ApproxWords: publish.RoundedWords(r.b.words),
		Outline: r.b.outline, Warnings: r.b.warnings, Notes: NotesExcluded{Documents: r.b.notesDocs, Words: r.b.notesWord},
		Excluded: r.b.excluded, ExcludedTotal: len(r.b.excluded) - r.b.switchedOff, SwitchedOff: r.b.switchedOff, Pages: []Page{}}
	if out.Outline == nil {
		out.Outline = []OutlineItem{}
	}
	if out.Warnings == nil {
		out.Warnings = []Warning{}
	}
	if out.Excluded == nil {
		out.Excluded = []Excluded{}
	}
	if len(out.Excluded) > maxExcluded {
		out.Excluded = out.Excluded[:maxExcluded]
	}
	out.TitlePage = titlePage(b, o)
	if o.Format == "pdf" {
		previewPDF(c, &out, b, o, n, r.b.words)
		return out, nil
	}
	if o.Format == "html-paged" {
		return out, nil // a browser decides the pages
	}
	pg := paginate(b, o, n)
	out.Pages = pg.pages
	if o.Format == "docx" {
		out.PageCount, out.PageEstimated = pg.total, true
		for i := range out.Outline {
			out.Outline[i].Page = pg.starts[i]
		}
		out.RunningHeader = publish.HeaderText(b, o, 2)
	}
	return out, nil
}

func titlePage(b *publish.Book, o publish.Options) TitlePage {
	if !publish.On(o.TitlePage) {
		return TitlePage{Layout: "none", Title: b.Title}
	}
	tp := TitlePage{Layout: o.TitleLayout, Title: publish.CleanLine(b.Title), Subtitle: publish.CleanLine(b.Subtitle)}
	if tp.Layout == "" {
		tp.Layout = "manuscript"
	}
	if by := publish.CleanLine(b.Byline); by != "" {
		tp.Byline = "by " + by
	}
	switch o.WordCount {
	case "none":
	case "label":
		tp.WordCount = "Word count: " + publish.Thousands(publish.RoundedWords(b.Words))
	default:
		if tp.Layout == "manuscript" {
			tp.WordCount = "about " + publish.Thousands(publish.RoundedWords(b.Words)) + " words"
		}
	}
	if tp.Layout == "manuscript" && publish.On(o.Contact) {
		name := b.Contact.Name
		if strings.TrimSpace(name) == "" {
			name = b.Byline
		}
		if name = publish.CleanLine(name); name != "" {
			tp.Contact = append(tp.Contact, name)
		}
		for _, l := range b.Contact.Address {
			if l = publish.CleanLine(l); l != "" {
				tp.Contact = append(tp.Contact, l)
			}
		}
		if e := publish.CleanLine(b.Contact.Email); e != "" {
			tp.Contact = append(tp.Contact, e)
		}
		if p := publish.CleanLine(b.Contact.Phone); p != "" {
			tp.Contact = append(tp.Contact, p)
		}
	}
	return tp
}

// ---- estimated pagination -------------------------------------------------------------------

// The estimate. A page holds as many lines as its height allows at the type
// size and line spacing; a line holds as many characters as its width allows
// at an average character width (half an em for a proportional serif face, six
// tenths for Courier). Chapters start where the preset starts them. It is the
// right size, not the right page: Word, with the writer's own fonts, decides.
type metrics struct {
	cpl     int     // characters per line
	lines   float64 // lines per page
	lineH   float64 // points per line
	indent  int     // first-line indent in characters
	after   float64 // space after a paragraph, in lines
	drop    float64 // lines above a chapter title
	newPage bool
}

func metricsOf(o publish.Options) metrics {
	size := o.FontSize
	if size == 0 {
		size = 12
	}
	spacing := o.LineSpacing
	if spacing == 0 {
		spacing = 2
	}
	width, height := 8.5, 11.0
	if o.PageSize == "a4" {
		width, height = 8.27, 11.69
	}
	margin := o.MarginInches
	if margin == 0 {
		margin = 1
	}
	em := 0.5
	if o.Font == "Courier New" {
		em = 0.6
	}
	m := metrics{newPage: o.ChapterStart != "continuous"}
	m.cpl = int(math.Max(10, math.Floor((width-2*margin)*72/(size*em))))
	m.lineH = size * 1.15 * spacing
	m.lines = math.Floor((height - 2*margin) * 72 / m.lineH)
	if m.lines < 4 {
		m.lines = 4
	}
	ind := 0.5
	if o.IndentInches != nil {
		ind = *o.IndentInches
	}
	m.indent = int(math.Round(ind * 72 / (size * em)))
	if o.SpaceAfterPt != nil {
		m.after = *o.SpaceAfterPt / m.lineH
	}
	if o.ChapterDrop != nil && m.newPage {
		m.drop = *o.ChapterDrop * 72 / m.lineH
	}
	return m
}

type paged struct {
	pages  []Page
	total  int
	starts []int // the page each outline item starts on
}

type pager struct {
	m      metrics
	o      publish.Options
	b      *publish.Book
	keep   int
	page   int
	used   float64
	cur    *Page
	pages  []Page
	titled bool
}

func (p *pager) newPage() {
	p.page++
	p.used = 0
	if p.page <= p.keep {
		pg := Page{Number: p.page, Blocks: []Block{}}
		if p.page >= 2 {
			pg.Header = publish.HeaderText(p.b, p.o, p.page)
			pg.Footer = publish.FooterText(p.o, p.page)
		}
		p.pages = append(p.pages, pg)
		p.cur = &p.pages[len(p.pages)-1]
	} else {
		p.cur = nil
	}
}

func (p *pager) add(bl Block, lines float64) {
	if p.page == 0 {
		p.newPage()
	}
	p.used += lines
	if p.cur != nil {
		p.cur.Blocks = append(p.cur.Blocks, bl)
	}
}

// paragraph lays out text, splitting it across pages, with the orphan and
// widow rule of two lines.
func (p *pager) paragraph(kind, txt string, indent bool, extraIndent int) {
	txt = strings.Join(strings.Fields(txt), " ")
	if txt == "" {
		return
	}
	rs := []rune(txt)
	pos := 0 // the rest of the paragraph is rs[pos:]: it is walked, never re-sliced or re-converted
	width := p.m.cpl - extraIndent
	if width < 8 {
		width = 8
	}
	first := 0
	if indent {
		first = p.m.indent
	}
	continued := false
	piece := func(from, to int) string {
		if p.cur == nil {
			return "" // a page past the ones asked for is counted, not kept
		}
		return clip(strings.TrimSpace(string(rs[from:to])))
	}
	for pos < len(rs) {
		avail := p.m.lines - p.used
		if p.page == 0 || avail < 1 {
			p.newPage()
			avail = p.m.lines
		}
		rem := len(rs) - pos
		total := int(math.Ceil(float64(rem+first) / float64(width)))
		if total < 1 {
			total = 1
		}
		fit := int(math.Floor(avail))
		if total <= fit {
			p.add(Block{Kind: kind, Text: piece(pos, len(rs)), Indent: indent && !continued, Continues: continued}, float64(total)+p.m.after)
			return
		}
		if fit < 2 || total-fit < 2 && fit < 3 && total > 2 {
			// not enough room to leave two lines at the foot: the paragraph starts the next page
			if p.used > 0 {
				p.newPage()
				continue
			}
		}
		cut := fit*width - first
		if cut < 1 {
			cut = 1
		}
		if cut > rem {
			cut = rem
		}
		// cut at a space
		at := cut
		for at > cut/2 && at < rem && rs[pos+at] != ' ' {
			at--
		}
		if at <= cut/2 {
			at = cut
		}
		p.add(Block{Kind: kind, Text: piece(pos, pos+at), Indent: indent && !continued, Continues: continued}, float64(fit))
		pos += at
		for pos < len(rs) && rs[pos] == ' ' {
			pos++
		}
		continued = true
		first = 0
		p.newPage()
	}
}

func clip(s string) string {
	if utf8.RuneCountInString(s) <= maxBlockText {
		return s
	}
	return string([]rune(s)[:maxBlockText])
}

func (p *pager) nodes(nodes []text.Node, level int, quote int) {
	afterBreak := true
	for _, n := range nodes {
		switch n.Kind {
		case text.NPara:
			kind := "paragraph"
			if quote > 0 {
				kind = "quote"
			}
			p.paragraph(kind, text.InlinePlain(n.Inl), !afterBreak && quote == 0, quote*4)
			afterBreak = false
		case text.NHeading:
			p.paragraph("heading", text.InlinePlain(n.Inl), false, 0)
			afterBreak = true
		case text.NRule:
			if p.page == 0 || p.m.lines-p.used < 1 {
				p.newPage()
			}
			p.add(Block{Kind: "scene_break", Text: publish.SceneBreakText(p.o)}, 1+p.m.after)
			afterBreak = true
		case text.NQuote:
			p.nodes(n.Kids, level, quote+1)
			afterBreak = true
		case text.NList:
			for _, it := range n.Items {
				for _, k := range it {
					if k.Kind == text.NPara {
						p.paragraph("list_item", text.InlinePlain(k.Inl), false, 4)
					} else {
						p.nodes([]text.Node{k}, level, quote)
					}
				}
			}
			afterBreak = true
		case text.NCode, text.NSource:
			for _, l := range strings.Split(n.Text, "\n") {
				p.paragraph("code", l, false, 4)
			}
			afterBreak = true
		case text.NTable:
			for _, r := range n.Rows {
				var cells []string
				for _, c := range r.Cells {
					cells = append(cells, text.InlinePlain(c.Inl))
				}
				p.paragraph("table_row", strings.Join(cells, " | "), false, 0)
			}
			afterBreak = true
		}
	}
}

func paginate(b *publish.Book, o publish.Options, keep int) paged {
	m := metricsOf(o)
	p := &pager{m: m, o: o, b: b, keep: keep}
	out := paged{starts: make([]int, len(b.Sections))}
	// The title page is page 1.
	if publish.On(o.TitlePage) {
		p.newPage()
		if p.cur != nil {
			p.cur.TitlePage = true
			p.cur.Header, p.cur.Footer = "", ""
			tp := titlePage(b, o)
			for _, l := range tp.Contact {
				p.cur.Blocks = append(p.cur.Blocks, Block{Kind: "paragraph", Text: l})
			}
			if tp.WordCount != "" {
				p.cur.Blocks = append(p.cur.Blocks, Block{Kind: "paragraph", Text: tp.WordCount})
			}
			p.cur.Blocks = append(p.cur.Blocks, Block{Kind: "heading", Text: tp.Title})
			if tp.Subtitle != "" {
				p.cur.Blocks = append(p.cur.Blocks, Block{Kind: "paragraph", Text: tp.Subtitle})
			}
			if tp.Byline != "" {
				p.cur.Blocks = append(p.cur.Blocks, Block{Kind: "paragraph", Text: tp.Byline})
			}
		}
		p.used = m.lines // the page is full: the text starts on the next one
	}
	for i, s := range b.Sections {
		heading := publish.HeadingText(s, o)
		if m.newPage && (i > 0 || publish.On(o.TitlePage) || p.page == 0) {
			if p.page > 0 && p.used > 0 || p.page == 0 {
				p.newPage()
			}
		} else if i == 0 && publish.On(o.TitlePage) {
			p.newPage()
		}
		if p.page == 0 {
			p.newPage()
		}
		out.starts[i] = p.page
		if m.newPage && m.drop > 0 {
			p.add(Block{Kind: "drop", Lines: int(math.Round(m.drop))}, m.drop)
		}
		level := s.Level
		if level < 1 {
			level = 1
		}
		if heading != "" {
			p.paragraph("heading", heading, false, 0)
			p.used += 1 // a line of air under a heading
		}
		bodyLevel := level
		if heading == "" {
			bodyLevel = level - 1
		}
		p.nodes(s.Nodes, bodyLevel, 0)
	}
	out.pages = p.pages
	if out.pages == nil {
		out.pages = []Page{}
	}
	out.total = p.page
	if p.used == 0 && p.page > 0 && p.page > 1 {
		out.total = p.page - 1
	}
	return out
}

// previewPDF fills the preview of a PDF from the real layout: the page count
// is the file's, the pages each part starts on are exact, and the warnings are
// the ones the writer will report.
func previewPDF(c *ops.Ctx, out *PreviewOut, b *publish.Book, o publish.Options, n, words int) {
	if err := tooBigForPDF(gateOf(c), words); err != nil {
		out.Warnings = append(out.Warnings, Warning{Code: "pdf_too_large", Message: err.Error() + " The export will be refused."})
		return
	}
	l, err := publish.LayoutPDFContext(c, b, o)
	if err != nil {
		out.Warnings = append(out.Warnings, Warning{Code: "layout_refused", Message: err.Error()})
		return
	}
	if t := l.Truncated(); len(t) > 0 {
		out.Warnings = append(out.Warnings, Warning{Code: "layout_limit", Message: "The export will be refused: " + strings.Join(t, "; ") + "."})
	}
	out.PageCount, out.PageEstimated = l.PageCount(), false
	for i := range out.Outline {
		out.Outline[i].Page = 0
	}
	// the outline lists the sections in order; the layout numbers them the same way
	for i := range out.Outline {
		out.Outline[i].Page = l.SectionPage(i)
		if out.Outline[i].Page > 0 {
			out.Outline[i].Label = l.PageLabel(out.Outline[i].Page - 1)
		}
	}
	pp := &PrintPreview{LayoutMetrics: l.Metrics(), FrontPages: l.FrontPages(), BodyStart: l.BodyStart(), Leaves: l.PageCount() / 2, Pages: []publish.LayoutPage{}}
	for i := 0; i < l.PageCount() && i < n; i++ {
		pp.Pages = append(pp.Pages, l.Page(i, 80))
	}
	out.Print = pp
	for _, w := range l.Warnings {
		out.Warnings = append(out.Warnings, Warning{Code: w.Code, Message: w.Message})
	}
	out.RunningHeader = publish.CleanLine(b.Byline) + " | " + publish.RunningTitle(b, o)
}
