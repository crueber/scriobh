package server

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"slices"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/imports"
)

// The view of an import draft's review: everything the review's templates draw, built from the draft and its preview. The words
// come from the catalogue (import.*) in the page's language; the text of a manuscript (titles, file names) is set as text.

// rowPage is how many rows of the proposed tree are drawn at a time, so a book of a thousand documents stays light.
const rowPage = 200

// impView is the review body.
type impView struct {
	Lang      string
	CSRF      string
	ID        string
	Rev       int
	Token     string
	Kind      string // the draft's: docx, epub, pdf, zip, folder, pandoc, file or git
	Into      *projects.Project
	PageURL   string // the review page: /import/d/ID, or under /p/slug
	Act       string // /ui/import/d/ID
	Status    string
	Pending   bool   // a Git clone still being made
	Failed    string // why a clone, or the import, cannot go on
	Hint      string // advice for it
	Retry     bool   // offer to look again (a reader that was busy)
	Done      *imports.DraftResult
	DoneLinks []impLink
	UndoSrc   *impSource  // a result that was added to a project: the offer to take it back
	Already   *impAlready // a file the project has had before
	Reuse     []impReuse  // earlier reviews of this file whose changes can be used here

	Layout        string
	Label         string
	Docs          int
	Words         int
	Warnings      []string
	Hidden        int // hidden files and folders left out
	Folded        bool
	ReadOnlyN     int    // documents too long to open for writing: the read-only limit
	AckBig        bool   // the writer said to import them anyway
	ReadOnlyLimit string // the read-only limit, with its separators

	Rows     []impRow
	RowsAll  int
	MoreHref string
	More     int // rows still to show
	Shown    int // rows drawn so far

	Left     []impLeft
	Skipped  impSkipped
	Research []impResearch
	ResN     int
	Report   []impKV

	Book *impBook
	Pdf  *impPdf
	Word *impWord
	Cut  *impCut // a Word document: how it was cut and the other ways
	MapN bool    // the chapters heading and its note: a book that was cut

	Opts impOpts

	Title, ProjKind, Author string
	ProjectKinds            []projects.KindOption

	Split             *impSplit  // the open row's paragraphs, to split it at one
	Inside            *impInside // what is inside the long chapter asked about
	Reveal            string     // a chapter that must be among the rows drawn
	PatTyped          bool       // the pattern field shows what was typed, not what is kept
	PatText, PatMatch string

	SugAfter   int // how many possible chapter starts the page lists so far
	SugAt      int // the one that takes focus after "Show more"
	SugOrd     int // the place a chapter was just started at: focus goes to the next, or to the new chapter
	Stashed    []string
	CanUndo    bool
	Live       string // what a screen reader is told after a change
	Notice     string
	NoticeKind string
	Focus      string
	OpenKey    string
}

// T and N give the words in the page's language.
func (v *impView) T(key string, args ...any) string { return i18n.Default.T(v.Lang, key, args...) }
func (v *impView) N(key string, n int, args ...any) string {
	return i18n.Default.N(v.Lang, key, n, args...)
}

// Foc marks the control that takes focus after the swap.
func (v *impView) Foc(rid, act string) template.HTMLAttr {
	if v.Focus == "imp-c-"+rid+"-"+act {
		return " data-focus"
	}
	return ""
}

// FocS marks the Edit summary of a row when it is the one that takes focus.
func (v *impView) FocS(rid string) template.HTMLAttr {
	if v.Focus == "imp-s-"+rid {
		return " data-focus"
	}
	return ""
}

// Num writes a number with thousands separators.
func (v *impView) Num(n int) string { return commas(n) }

type impLink struct {
	Href, Text string
	Solid      bool
}

type impKV struct{ K, V string }

// impRow is one proposed document.
type impRow struct {
	Key, RID, Title, Kind, KindName string
	Words, Depth                    int
	Licence                         bool // the Project Gutenberg licence set apart: Put back is on the row
	Warnings                        []string
	Headings                        int
	Split                           bool
	Open                            bool
	Panel                           *impPanel
	First, Last                     bool
	Siblings                        int
	Index                           int
	Level                           int
	FirstLine                       string // the start of its text
	Tables, Cells                   int    // Word tables in it, and their non-empty cells: not in Words
	Why                             string // why the cut is here, in words
	WordNum                         string // the number Word prints for it
	Check, NoTitle                  bool
	Suggest                         string // its title in title case, when it is in capitals
	Long                            bool   // a Word chapter long enough to look inside it for breaks
	LookOpen                        bool   // what is inside it is drawn
}

// impPanel is a row's edit controls.
type impPanel struct {
	BookLike         bool
	CanJoin, CanNest bool
	JoinWhy, NestWhy string
	IsPart           bool
	Targets          []impTarget
	Ends             bool // a long list: the two ends are one press away
	Docx             bool // a Word chapter: its own breaks (join with the one above, split at a paragraph, front and back matter)
	CanPrev          bool
	Licence          bool // the Project Gutenberg licence set apart: its button says Put back
	PrevWhy          string
	SplitOpen        bool
}

type impTarget struct{ Key, Text string }

type impLeft struct {
	Key, RID, Text string
}

type impSkipped struct {
	Total   int
	Reasons []impKV
	List    []imports.Skipped
	More    int
}

type impResearch struct {
	Key, RID, Title, Meta string
	Depth                 int
}

// impBook is what the preview says of an EPUB.
type impBook struct {
	Facts        []impKV
	Cut          string
	Cover        string
	UseCover     bool
	UseCoverNote string
	Label        string
	Files        []impFile
	FilesN       int
	FilesMore    int
}

type impFile struct{ Path, Why string }

// impPdf is what the preview says of a PDF.
type impPdf struct {
	Facts []impKV
	Lines []impTick
	Pages []impKV
	PageN int
}

type impTick struct {
	OK   bool
	Text string
}

// impWord is what the preview says of a Word file's tracked changes and comments.
type impWord struct {
	Say     string
	Rule    string
	Items   []string
	Why     []string
	Authors []string
}

// impOpts are the controls for how the file is read, drawn for the layout the preview found.
type impOpts struct {
	Show                                   bool
	Obsidian, SplitScenes                  bool
	HasObsidian                            bool
	Docx, Epub, Scriv, WP, Ghost, Pandoc   bool
	Headings, Changes                      string
	Sections, Pages                        string
	ScrivResearch, ScrivNotes, ScrivSheets string
	Pictures                               string
	WPStatus, WPTypes                      string
	GhostStatus, GhostTypes                string
	PandocFormat                           string
	PandocFormats                          []impTarget
}

func rid(key string) string {
	h := sha256.Sum256([]byte(key))
	return "r" + hex.EncodeToString(h[:5])
}

var kindNames = map[string]string{"chapter": "Chapter", "part": "Part", "scene": "Scene", "note": "Note", "front_matter": "Front matter", "back_matter": "Back matter"}

// kindOptions are the kinds a document can be changed to.
var kindOptions = []impTarget{{"chapter", "Chapter"}, {"part", "Part"}, {"scene", "Scene"}, {"note", "Note"}, {"front_matter", "Front matter"}, {"back_matter", "Back matter"}}

func (v *impView) KindOptions() []impTarget { return kindOptions }

// layoutLabel is what a source is called in the summary.
func (v *impView) layoutLabel(layout string) string {
	switch layout {
	case "epub":
		return v.T("import.name_epub")
	case "pdf":
		return v.T("import.name_pdf")
	case "scrivener":
		return v.T("import.name_scrivener")
	case "wordpress":
		return v.T("import.name_wordpress")
	case "ghost":
		return v.T("import.name_ghost")
	case "docx":
		return v.T("import.name_docx")
	case "pandoc":
		return v.T("import.name_pandoc")
	case "text":
		return v.T("import.name_text")
	case "obsidian":
		return v.T("import.name_obsidian")
	case "scriobh":
		return v.T("import.name_scriobh")
	}
	return v.T("import.name_folder")
}

// fill builds the view of a preview in the draft's state.
func (v *impView) fill(pv *imports.PreviewOut, dr imports.DraftView, rows int) {
	v.Layout = pv.Layout
	v.Label = v.layoutLabel(pv.Layout)
	v.Docs, v.Words = pv.Documents, pv.Words
	v.Warnings = pv.Warnings
	v.Hidden = pv.SkippedByReason["hidden file or folder"]
	v.Title, v.ProjKind, v.Author = pick3(dr.Edits.Project.Title, pv.Project.Title), pv.Project.Kind, pick3(dr.Edits.Project.Author, pv.Project.Author)
	if dr.Edits.Project.Kind != "" {
		v.ProjKind = dr.Edits.Project.Kind
	}
	v.ProjectKinds = projects.KindOptions()
	if !knownKind(v.ProjKind) {
		v.ProjKind = "blank"
	}
	v.buildRows(pv, rows)
	// What was left out can be brought back; the preview no longer lists it, so the draft remembers it.
	keys := make([]string, 0, len(dr.Edits.Gone))
	for k := range dr.Edits.Gone {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		g := dr.Edits.Gone[k]
		t := g.Title
		switch {
		case g.Into != "":
			t += " " + v.T("import.joined_into", "t", g.Into)
		case g.Inside > 0:
			t += " " + v.T("import.and_inside", "n", g.Inside)
		}
		v.Left = append(v.Left, impLeft{Key: k, RID: rid(k), Text: t})
	}
	// What the import left out on its own (a contents page) is listed the same way, to be put back.
	for _, l := range pv.LeftOut {
		if _, gone := dr.Edits.Gone[l.Key]; gone {
			continue
		}
		v.Left = append(v.Left, impLeft{Key: l.Key, RID: rid(l.Key), Text: l.Title + " " + v.T("import.left_"+l.Why, "n", l.Words)})
	}
	v.Skipped = impSkipped{Total: pv.SkippedTotal, List: pv.Skipped}
	if pv.SkippedTotal > len(pv.Skipped) {
		v.Skipped.More = pv.SkippedTotal - len(pv.Skipped)
	}
	rs := make([]string, 0, len(pv.SkippedByReason))
	for r := range pv.SkippedByReason {
		rs = append(rs, r)
	}
	sort.Strings(rs)
	for _, r := range rs {
		v.Skipped.Reasons = append(v.Skipped.Reasons, impKV{r, commas(pv.SkippedByReason[r])})
	}
	v.research(pv)
	v.report(pv)
	switch {
	case pv.Layout == "epub" && pv.Epub != nil:
		v.Book = v.epub(pv, dr)
	case pv.Layout == "pdf":
		v.Pdf = v.pdf(pv)
	case pv.Layout == "docx":
		v.Word = v.word(pv)
		v.Cut = v.cut(pv, dr)
	}
	if v.Cut == nil && pv.Breaks != nil { // a PDF, a Pandoc document or a text file that the detector cut
		v.Cut = v.cut(pv, dr)
	}
	v.MapN = pv.Layout == "epub" || pv.Layout == "pdf" || pv.Layout == "docx" || pv.Layout == "pandoc"
	v.Folded = (pv.Layout == "pdf" || pv.Layout == "docx") && len(pv.Warnings) > 1
	v.options(pv, dr)
	for _, n := range pv.Nodes {
		if n.Words >= docs.ReadOnlyWords {
			v.ReadOnlyN++
		}
	}
	for _, k := range sortedStrings(dr.Edits.Stash) {
		v.Stashed = append(v.Stashed, dr.Edits.Stash[k].Title)
	}
	if v.Inside != nil {
		for _, g := range dr.Edits.Gone {
			if g.To == v.Inside.Key {
				v.Inside.Joined++
			}
		}
	}
	v.AckBig = dr.Options.AckBig
	v.ReadOnlyLimit = commas(docs.ReadOnlyWords)
}

func pick3(vals ...string) string {
	for _, s := range vals {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func knownKind(k string) bool {
	for _, o := range projects.Kinds {
		if o == k {
			return true
		}
	}
	return false
}

// buildRows lists the proposed documents in reading order, as many as asked for and at least up to the row that is open.
func (v *impView) buildRows(pv *imports.PreviewOut, rows int) {
	n := len(pv.Nodes)
	v.RowsAll = n
	depth := map[string]int{"": 0}
	kids := map[string][]string{}
	for _, nd := range pv.Nodes {
		kids[nd.ParentKey] = append(kids[nd.ParentKey], nd.Key)
	}
	titles := map[string]string{}
	for _, nd := range pv.Nodes {
		titles[nd.Key] = nd.Title
	}
	limit := max(rows, rowPage) // what the page showed so far is kept, and a short tree grown by a change is not cut to what it was
	for i, nd := range pv.Nodes {
		if (nd.Key == v.OpenKey || nd.Key == v.Reveal) && i >= limit {
			limit = i + 1
		}
	}
	bookLike := pv.Layout == "epub" || pv.Layout == "pdf" || pv.Layout == "docx" || pv.Layout == "pandoc"
	for i, nd := range pv.Nodes {
		d := depth[nd.ParentKey] + 1
		depth[nd.Key] = d
		if i >= limit {
			continue
		}
		sib := kids[nd.ParentKey]
		at := 0
		for j, k := range sib {
			if k == nd.Key {
				at = j
			}
		}
		r := impRow{Key: nd.Key, RID: rid(nd.Key), Title: nd.Title, Kind: nd.Kind, KindName: kindNames[nd.Kind], Words: nd.Words, Depth: d - 1, Warnings: nd.Warnings,
			Headings: nd.Headings, Split: nd.Split, First: at == 0, Last: at == len(sib)-1, Siblings: len(sib), Index: at, Level: d,
			Why: v.why(nd), WordNum: nd.WordNumber, Check: nd.Check, NoTitle: nd.NoTitle, Suggest: nd.SuggestedTitle, Tables: nd.Tables, Cells: nd.Cells,
			Licence: slices.Contains(nd.Why, "licence")}
		if nd.FirstLine != "" {
			r.FirstLine = nd.FirstLine
		}
		if r.KindName == "" {
			r.KindName = nd.Kind
		}
		r.Open = nd.Key == v.OpenKey
		r.Long = pv.Layout == "docx" && docxLong(nd.Words)
		r.LookOpen = pv.Layout == "docx" && v.Inside != nil && v.Inside.Key == nd.Key
		if r.Open {
			r.Panel = v.panel(pv, nd, len(kids[nd.Key]) > 0, bookLike, len(sib))
			if pv.Layout == "docx" {
				r.Panel.Docx = true
				r.Panel.Licence = slices.Contains(nd.Why, "licence")
				r.Panel.SplitOpen = v.Split != nil && v.Split.Key == nd.Key
				if to, why := imports.JoinTarget(pv, nd.Key); to != nil {
					r.Panel.CanPrev = true
				} else {
					r.Panel.PrevWhy = v.T("import.prev_" + why)
				}
			}
		}
		v.Rows = append(v.Rows, r)
	}
	v.Shown = len(v.Rows)
	if limit < n {
		v.More = n - limit
		v.MoreHref = fmt.Sprintf("%s?rows=%d", v.PageURL, limit+rowPage)
	}
}

// panel is the edit controls of one row. What a chapter can be nested or joined into is everything but itself and what is inside it.
func (v *impView) panel(pv *imports.PreviewOut, nd imports.Node, hasKids, bookLike bool, siblings int) *impPanel {
	p := &impPanel{BookLike: bookLike, IsPart: nd.Kind == "part", Ends: siblings > 3}
	if !bookLike {
		return p
	}
	no := map[string]bool{nd.Key: true}
	title := map[string]string{}
	for _, x := range pv.Nodes {
		title[x.Key] = x.Title
	}
	for _, x := range pv.Nodes {
		if no[x.ParentKey] {
			no[x.Key] = true
		}
		if !no[x.Key] {
			t := x.Title
			if x.ParentKey != "" && title[x.ParentKey] != "" {
				t = v.T("import.opt_in", "t", x.Title, "p", title[x.ParentKey])
			}
			p.Targets = append(p.Targets, impTarget{x.Key, t})
		}
	}
	p.CanNest, p.CanJoin = true, true
	switch {
	case nd.Kind == "part" || hasKids:
		p.JoinWhy = v.T("import.join_part")
		p.CanJoin = false
	case len(p.Targets) == 0:
		p.JoinWhy = v.T("import.join_none")
		p.CanJoin = false
	}
	if len(p.Targets) == 0 && nd.ParentKey == "" {
		p.NestWhy = v.T("import.nest_none")
		p.CanNest = false
	}
	if len(p.Targets) == 0 {
		p.CanJoin = false
	}
	return p
}

// research lists the research notes a converted source would write.
func (v *impView) research(pv *imports.PreviewOut) {
	v.ResN = pv.ResearchTotal
	depth := map[string]int{"": 0}
	for _, n := range pv.Research {
		depth[n.Key] = depth[n.ParentKey] + 1
		meta := fmt.Sprintf("%s %s", commas(n.Words), pluralWord(n.Words, "word", "words"))
		if n.Kind == "folder" {
			meta = "Folder"
		}
		switch n.Candidate {
		case "character":
			meta += " · character sheet"
		case "place":
			meta += " · place sheet"
		}
		v.Research = append(v.Research, impResearch{Key: n.Key, RID: rid("r:" + n.Key), Title: n.Title, Meta: meta, Depth: depth[n.Key] - 1})
	}
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// report is the converter's counts by name: a line for each that is not nothing.
func (v *impView) report(pv *imports.PreviewOut) {
	if len(pv.Report) == 0 {
		return
	}
	b, _ := json.Marshal(pv.Report)
	var rep map[string]any
	_ = json.Unmarshal(b, &rep)
	empty := func(x any) bool {
		switch t := x.(type) {
		case nil:
			return true
		case float64:
			return t == 0
		case bool:
			return !t
		case string:
			return t == ""
		case map[string]any:
			return len(t) == 0
		case []any:
			return len(t) == 0
		}
		return false
	}
	label := func(k string) string {
		k = strings.ReplaceAll(k, "_", " ")
		if k == "" {
			return k
		}
		return strings.ToUpper(k[:1]) + k[1:]
	}
	add := func(k string, x any) {
		if !empty(x) {
			v.Report = append(v.Report, impKV{label(k), repVal(x)})
		}
	}
	keys := make([]string, 0, len(rep))
	for k := range rep {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		if m, ok := rep[k].(map[string]any); ok {
			ks := make([]string, 0, len(m))
			for j := range m {
				ks = append(ks, j)
			}
			sort.Strings(ks)
			for _, j := range ks {
				add(j, m[j])
			}
		} else {
			add(k, rep[k])
		}
	}
	if len(v.Report) > 80 {
		v.Report = v.Report[:80]
	}
}

func repVal(x any) string {
	switch t := x.(type) {
	case nil:
		return ""
	case []any:
		var s []string
		for i, e := range t {
			if i >= 20 {
				s = append(s, "…")
				break
			}
			s = append(s, repVal(e))
		}
		return strings.Join(s, ", ")
	case map[string]any:
		ks := make([]string, 0, len(t))
		for k := range t {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		var s []string
		for i, k := range ks {
			if i >= 30 {
				break
			}
			s = append(s, k+" "+repVal(t[k]))
		}
		return strings.Join(s, ", ")
	case float64:
		return commas(int(t))
	case bool:
		return "yes"
	}
	return fmt.Sprint(x)
}

func (v *impView) epub(pv *imports.PreviewOut, dr imports.DraftView) *impBook {
	e := pv.Epub
	b := &impBook{}
	nz := func(s string) string {
		if s == "" {
			return v.T("import.not_stated")
		}
		return s
	}
	lang := e.Language
	if lang != "" && e.Direction == "rtl" {
		lang += " (" + v.T("import.rtl") + ")"
	}
	title := nz(e.Title)
	if e.Subtitle != "" {
		title += ": " + e.Subtitle
	}
	b.Facts = []impKV{{v.T("import.f_title"), title}, {v.T("import.f_author"), nz(strings.Join(e.Authors, ", "))}, {v.T("import.f_lang"), nz(lang)}}
	if e.Version != "" {
		b.Facts = append(b.Facts, impKV{v.T("import.f_version"), e.Version})
	}
	b.Facts = append(b.Facts, impKV{v.T("import.f_notes"), commas(e.Footnotes)})
	if e.Publisher != "" {
		b.Facts = append(b.Facts, impKV{v.T("import.f_publisher"), e.Publisher})
	}
	src := map[string]string{"nav": v.T("import.src_nav"), "ncx": v.T("import.src_ncx")}[e.Contents]
	from := ""
	if src != "" {
		from = " (" + src + ")"
	}
	cut := map[string]string{"toc": v.T("import.cut_toc", "from", from), "files": v.T("import.cut_files"), "headings": v.T("import.cut_headings")}[e.Strategy]
	if cut == "" {
		cut = e.Strategy
	}
	auto := ""
	if dr.Options.EpubSections == "" || dr.Options.EpubSections == "auto" {
		auto = v.T("import.cut_auto")
	}
	b.Cut = v.T("import.cut_used", "cut", auto+strings.ToLower(cut[:min(1, len(cut))])+cut[min(1, len(cut)):])
	c := e.Cover
	switch {
	case c == nil || (!c.Kept && c.Reason == "" && c.Path == ""):
		b.Cover = v.T("import.cover_none")
	case c.Kept:
		b.Cover = v.T("import.cover_kept")
	default:
		b.Cover = v.T("import.cover_not", "reason", c.Reason)
	}
	if v.Into == nil && c != nil && c.CanUse {
		b.UseCover = true
	} else if c != nil && c.Kept && c.UseReason != "" {
		b.UseCoverNote = v.T("import.use_not", "reason", c.UseReason)
	}
	b.Label = dr.Options.EditionLabel
	if b.Label == "" {
		b.Label = e.Label
	}
	title2 := map[string]string{}
	for _, n := range pv.Nodes {
		title2[n.Key] = n.Title
	}
	b.FilesN = len(e.Files)
	for i, f := range e.Files {
		if i >= 300 {
			b.FilesMore = len(e.Files) - 300
			break
		}
		why := ""
		if f.Skipped != "" {
			why = v.T("import.file_skipped", "reason", f.Skipped)
		} else {
			t := title2[f.Key]
			if t == "" {
				t = f.Title
			}
			if t == "" {
				t = f.Path
			}
			why = v.T("import.file_becomes", "title", t)
		}
		b.Files = append(b.Files, impFile{f.Path, why})
	}
	return b
}

func (v *impView) pdf(pv *imports.PreviewOut) *impPdf {
	raw, _ := json.Marshal(pv.Report)
	var rep struct {
		PDF struct {
			Report map[string]any `json:"report"`
		} `json:"pdf"`
	}
	_ = json.Unmarshal(raw, &rep)
	r := rep.PDF.Report
	if r == nil {
		r = map[string]any{}
	}
	num := func(k string) int { f, _ := r[k].(float64); return int(f) }
	list := func(k string) []any { l, _ := r[k].([]any); return l }
	pages := func(a []any) string {
		var s []string
		for i, x := range a {
			if i >= 12 {
				return strings.Join(s, ", ") + v.T("import.and_more", "n", len(a)-12)
			}
			s = append(s, repVal(x))
		}
		return strings.Join(s, ", ")
	}
	p := &impPdf{}
	src := map[string]string{"outline": v.T("import.pdf_outline"), "headings": v.T("import.pdf_headings"), "none": v.T("import.pdf_nochap")}[fmt.Sprint(r["chapter_source"])]
	p.Facts = []impKV{{v.T("import.pdf_pages"), v.T("import.pdf_pages_v", "n", commas(num("pages")), "t", commas(num("text_pages")))},
		{v.T("import.pdf_conf"), v.T("import.pdf_conf_v", "c", num("confidence"), "l", commas(num("low_confidence_pages")))}}
	if src != "" {
		p.Facts = append(p.Facts, impKV{v.T("import.pdf_chapters"), src})
	}
	tick := func(ok bool, t string) { p.Lines = append(p.Lines, impTick{ok, t}) }
	if n := num("table_rows_flattened"); n > 0 {
		tick(false, v.T("import.pdf_tables", "n", commas(n), "p", pages(list("table_pages"))))
	} else {
		tick(true, v.T("import.pdf_tables0"))
	}
	if n := num("images"); n > 0 {
		tick(false, v.T("import.pdf_images", "n", commas(n)))
	} else {
		tick(true, v.T("import.pdf_images0"))
	}
	tick(false, v.T("import.pdf_links"))
	two, amb := num("two_column_pages"), list("ambiguous_column_pages")
	if two > 0 || len(amb) > 0 {
		pg := pages(amb)
		if pg == "" {
			pg = v.T("import.none")
		}
		tick(false, v.T("import.pdf_cols", "n", commas(two), "p", pg))
	} else {
		tick(true, v.T("import.pdf_cols0"))
	}
	if n := num("picture_only_total"); n > 0 {
		tick(false, v.T("import.pdf_scan", "n", commas(n)))
	} else {
		tick(true, v.T("import.pdf_scan0"))
	}
	if l := list("right_to_left_pages"); len(l) > 0 {
		tick(false, v.T("import.pdf_rtl", "p", pages(l)))
	}
	if n := num("unmapped_chars_left_out"); n > 0 {
		tick(false, v.T("import.pdf_unmapped", "n", commas(n)))
	}
	if l := list("truncated_pages"); len(l) > 0 {
		tick(false, v.T("import.pdf_cut", "p", pages(l)))
	}
	if n := num("running_head_lines_removed") + num("page_number_lines_removed"); n > 0 {
		rule, _ := r["running_head_rule"].(string)
		tick(true, v.T("import.pdf_heads", "n", commas(n), "rule", rule))
	}
	if num("contents_lines_removed") > 0 {
		tick(true, v.T("import.pdf_toc", "p", pages(list("contents_pages_removed"))))
	}
	bad := list("page_detail")
	p.PageN = len(bad)
	for i, x := range bad {
		if i >= 40 {
			break
		}
		m, _ := x.(map[string]any)
		pg, _ := m["page"].(float64)
		cf, _ := m["confidence"].(float64)
		var notes []string
		if ns, ok := m["notes"].([]any); ok {
			for j, nn := range ns {
				if j >= 3 {
					break
				}
				notes = append(notes, fmt.Sprint(nn))
			}
		}
		p.Pages = append(p.Pages, impKV{v.T("import.pdf_page", "n", int(pg), "c", int(cf)), strings.Join(notes, " ")})
	}
	return p
}

func (v *impView) word(pv *imports.PreviewOut) *impWord {
	raw, _ := json.Marshal(pv.Report)
	var rep struct {
		Docx struct {
			Tracked map[string]any `json:"tracked"`
			Mode    string         `json:"changes_mode"`
		} `json:"docx"`
	}
	_ = json.Unmarshal(raw, &rep)
	t := rep.Docx.Tracked
	if t == nil {
		t = map[string]any{}
	}
	mode := rep.Docx.Mode
	if mode == "" {
		mode = "suggest"
	}
	num := func(k string) int { f, _ := t[k].(float64); return int(f) }
	if num("revisions") == 0 && num("comments_read") == 0 && pv.Suggestions == 0 && pv.Comments == 0 {
		return nil
	}
	w := &impWord{}
	w.Say = map[string]string{"suggest": v.T("import.tr_suggest"), "accept": v.T("import.tr_accept"), "reject": v.T("import.tr_reject")}[mode]
	if mode == "suggest" {
		w.Rule = v.T("import.tr_rule")
		w.Items = append(w.Items, v.T("import.tr_count", "n", commas(pv.Suggestions)))
	}
	w.Items = append(w.Items, v.T("import.tr_comments", "n", commas(pv.Comments)))
	if n := num("comments_not_imported"); n > 0 {
		w.Items = append(w.Items, v.T("import.tr_comments_lost", "n", commas(n)))
	}
	if n := num("revisions"); mode != "suggest" && n > 0 {
		w.Items = append(w.Items, v.T("import.tr_revs", "n", commas(n)))
	}
	if n := num("taken_as_accepted"); mode == "suggest" && n > 0 {
		w.Items = append(w.Items, v.T("import.tr_taken", "n", commas(n)))
		for _, x := range []struct{ k, key string }{{"inserted_paragraphs", "tr_ins"}, {"deleted_paragraphs", "tr_del"}, {"paragraph_marks", "tr_mark"}, {"in_headings", "tr_head"}, {"in_tables", "tr_table"},
			{"in_footnotes", "tr_note"}, {"in_code", "tr_code"}, {"over_cap", "tr_cap"}, {"not_placed", "tr_place"}, {"refused_by_review_checks", "tr_refused"},
			{"author_conflicts", "tr_conflict"}, {"at_formatting_edges", "tr_edge"}} {
			if c := num(x.k); c > 0 {
				w.Why = append(w.Why, v.T("import."+x.key, "n", commas(c)))
			}
		}
	}
	if pairs, ok := t["author_conflict_pairs"].([]any); ok && mode == "suggest" && len(pairs) > 0 {
		var ps []string
		for _, q := range pairs {
			var as []string
			for _, a := range strings.Split(fmt.Sprint(q), " / ") {
				as = append(as, v.T("import.word_name", "name", a))
			}
			ps = append(ps, strings.Join(as, " / "))
		}
		w.Items = append(w.Items, v.T("import.tr_pairs", "pairs", strings.Join(ps, "; ")))
	}
	if n := num("moves"); n > 0 {
		w.Items = append(w.Items, v.T("import.tr_moves", "n", commas(n)))
	}
	if n := num("formatting_changes_ignored"); n > 0 {
		w.Items = append(w.Items, v.T("import.tr_fmt", "n", commas(n)))
	}
	if au, ok := t["authors"].(map[string]any); ok {
		names := make([]string, 0, len(au))
		for a := range au {
			names = append(names, a)
		}
		sort.Strings(names)
		for i, a := range names {
			if i >= 20 {
				break
			}
			c, _ := au[a].(float64)
			w.Authors = append(w.Authors, v.T("import.word_name", "name", a)+", "+v.T("import.tr_n", "n", commas(int(c))))
		}
	}
	return w
}

// options draws the controls that belong to the layout the preview found.
func (v *impView) options(pv *imports.PreviewOut, dr imports.DraftView) {
	o := dr.Options
	v.Opts = impOpts{SplitScenes: o.SplitScenes == "h2", Obsidian: o.Layout == "obsidian"}
	switch pv.Layout {
	case "docx":
		v.Opts.Docx = true
		v.Opts.Headings, v.Opts.Changes = orDefault(o.DocxHeadings, "chapters"), orDefault(o.DocxChanges, "suggest")
	case "epub":
		v.Opts.Epub = true
		v.Opts.Sections, v.Opts.Pages = orDefault(o.EpubSections, "auto"), orDefault(o.EpubPages, "skip")
	case "scrivener":
		v.Opts.Scriv = true
		v.Opts.ScrivResearch, v.Opts.ScrivNotes, v.Opts.ScrivSheets = orDefault(o.ScrivResearch, "notes"), orDefault(o.ScrivNotes, "private"), orDefault(o.ScrivSheets, "notes")
		v.Opts.Pictures = orDefault(o.Pictures, "import")
	case "wordpress":
		v.Opts.WP = true
		v.Opts.WPStatus, v.Opts.WPTypes, v.Opts.Pictures = orDefault(o.WPStatus, "published"), orDefault(o.WPTypes, "posts"), orDefault(o.Pictures, "import")
	case "ghost":
		v.Opts.Ghost = true
		v.Opts.GhostStatus, v.Opts.GhostTypes, v.Opts.Pictures = orDefault(o.GhostStatus, "published"), orDefault(o.GhostTypes, "posts"), orDefault(o.Pictures, "import")
	case "pandoc":
		v.Opts.Pandoc = true
		v.Opts.Headings, v.Opts.Pictures, v.Opts.PandocFormat = orDefault(o.DocxHeadings, "chapters"), orDefault(o.Pictures, "skip"), o.PandocFormat
	case "pdf":
	default:
		v.Opts.HasObsidian = v.Kind == "zip" || v.Kind == "folder"
	}
	v.Opts.Show = true
}

func orDefault(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

// ---- how a Word document was cut ---------------------------------------------------------------------------------------

// impCut is the "how it was cut" panel and what goes with it when nothing was found.
type impCut struct {
	Say                string
	Radios             []impRadio
	Weak               string // what a weak rule is, in words, when one is listed
	None               bool
	NoneSay            string
	Found              []string
	Sugs               []impSug
	SugMore            int
	SugMoreHref        string
	SugMoreN, SugAt    int
	SugUnlisted        int
	Bulk               []impBulk
	Piece              *impBulk
	Caps               int
	Lines              int // chapters whose title holds text before a chapter line: offered from two
	Levels             int
	PartsOn            bool
	Big                int
	Added              int
	Pages              bool
	FocusRadio, FocusH bool
	Pat                impPat
}

// impReuse is one offer: an earlier review of the same file, by its id, and the words of its button.
type impReuse struct{ ID, Label string }

// impPat is the pattern the writer typed, and what it matches.
type impPat struct {
	Text    string
	Regex   bool
	Has     bool // a pattern is being tried: the section is open and says what it matches
	Typed   bool // the field shows what was typed and refused
	Say     string
	Lines   []string
	Refused bool
}

type impRadio struct {
	Value, Label string
	Checked      bool
}

type impSug struct {
	Ordinal int
	Line    string
	Why     string
	Pos     string
	Focus   bool
}

type impBulk struct{ Value, Text string }

// why says, in the page's words, why a cut is where it is, from the detector's fixed codes.
func (v *impView) why(nd imports.Node) string {
	var out []string
	for _, c := range nd.Why {
		k, arg, _ := strings.Cut(c, ":")
		switch k {
		case "heading":
			out = append(out, v.T("import.why_heading", "n", arg))
		case "style":
			out = append(out, v.T("import.why_style", "s", arg))
		case "label", "run", "title_line", "centred", "bold", "caps", "larger", "space", "page_break", "section_break", "numeral", "size", "explicit", "short", "pattern", "licence":
			out = append(out, v.T("import.why_"+k))
		}
	}
	if nd.WordNumber != "" {
		out = append(out, v.T("import.why_numbered", "n", nd.WordNumber))
	}
	if len(out) == 0 {
		return ""
	}
	return strings.Join(out, ", ")
}

// cut builds the panel from the preview's breaks.
func (v *impView) cut(pv *imports.PreviewOut, dr imports.DraftView) *impCut {
	b := pv.Breaks
	if b == nil {
		return nil
	}
	c := &impCut{Added: len(b.Added), Levels: b.Levels, PartsOn: dr.Options.DocxHeadings == "parts"}
	switch b.ChosenBy {
	case "heading":
		c.Say = v.T("import.cut_heading", "n", commas(b.Chapters), "l", b.Level)
	case "style":
		c.Say = v.T("import.cut_style", "n", commas(b.Chapters), "s", b.Style)
	case "label":
		c.Say = v.T("import.cut_label", "n", commas(b.Chapters))
	case "mixed":
		c.Say = v.T("import.cut_mixed", "n", commas(b.Chapters))
	case "explicit":
		c.Say = v.T("import.cut_explicit", "n", commas(b.Chapters), "rule", v.T("import.cut_rule_"+b.Rule, "s", b.Style, "w", commas(b.PieceWords)))
	case "none":
		c.Say = v.T("import.cut_none")
	default:
		c.None = true
		if len(b.Added) > 0 {
			c.Say = v.N("import.cut_only_yours", len(b.Added))
		} else {
			c.NoneSay = v.T("import.cut_none_found", "w", commas(b.Words))
		}
	}
	if c.Say != "" && b.BreakBefore > 0 && b.ChosenBy != "explicit" && !c.None {
		if b.BreakBefore == b.Chapters {
			c.Say += " " + v.T("import.cut_each_break")
		} else {
			c.Say += " " + v.N("import.cut_some_break", b.BreakBefore)
		}
	}
	if b.Parts > 0 {
		c.Say += " " + v.N("import.cut_parts", b.Parts)
	}
	if len(b.Added) > 0 && !c.None {
		c.Say += " " + v.N("import.cut_added", len(b.Added))
	}
	// the radio group: what is in use first, then the other ways, only those that exist
	weak := false
	add := func(value, label string, checked bool) {
		c.Radios = append(c.Radios, impRadio{Value: value, Label: label, Checked: checked})
	}
	autoLabel := v.T("import.cut_r_auto", "n", commas(b.Auto))
	if b.Auto == 0 {
		autoLabel = v.T("import.cut_r_auto0")
	}
	add("auto", autoLabel, b.Rule == "auto")
	for _, o := range b.Options {
		switch o.Rule {
		case "heading1", "heading2":
			add(o.Rule, v.T("import.cut_r_"+o.Rule, "n", commas(o.Count)), b.Rule == o.Rule)
		case "style":
			add("style:"+o.Style, v.T("import.cut_r_style", "s", o.Style, "n", commas(o.Count)), b.Rule == "style" && strings.EqualFold(b.Style, o.Style))
		case "label":
			add("label", v.T("import.cut_r_label", "n", commas(o.Count)), b.Rule == "label")
		case "page_breaks", "section_breaks", "numerals":
			weak = true
			add(o.Rule, v.T("import.cut_r_"+o.Rule, "n", commas(o.Count)), b.Rule == o.Rule)
		case "size":
			weak = true
			add("size", v.T("import.cut_r_size", "w", commas(b.PieceWords), "n", commas(o.Count)), b.Rule == "size")
		}
	}
	if dr.Options.DocxPattern != "" {
		n := 0
		if b.Pattern != nil {
			n = b.Pattern.Count
		}
		add("pattern", v.T("import.cut_r_pattern", "n", commas(n)), b.Rule == "pattern")
	}
	add("none", v.T("import.cut_r_none"), b.Rule == "none")
	if weak {
		c.Weak = v.T("import.cut_weak")
	}
	c.FocusRadio = v.Focus == "imp-cut"
	// nothing found by heading, style or label: what was found, the places a chapter could start, and the bulk choices
	if c.None {
		for _, r := range []string{"page_breaks", "section_breaks", "numerals", "label"} {
			if n := b.ByRule[r]; n > 0 {
				c.Found = append(c.Found, v.N("import.found_"+r, n))
				c.Bulk = append(c.Bulk, impBulk{r, v.T("import.use_all_"+r, "n", commas(n))})
			}
		}
		if b.Pieces > 1 {
			c.Piece = &impBulk{"size", v.T("import.use_pieces", "w", commas(b.PieceWords), "n", commas(b.Pieces))}
		}
		show := 40 // places a chapter could start, listed 40 at a time
		total := len(b.Suggestions)
		end := min(max(v.SugAfter, show), total)
		for i, s := range b.Suggestions[:end] {
			why := []string{}
			for _, k := range s.Why {
				switch k {
				case "page_break", "section_break", "short", "centred", "bold", "caps", "larger", "numeral", "label":
					why = append(why, v.T("import.why_"+k))
				}
			}
			pct := 0
			if b.Words > 0 {
				pct = s.Words * 100 / b.Words
			}
			c.Sugs = append(c.Sugs, impSug{Ordinal: s.Ordinal, Line: s.Line, Why: strings.Join(why, ", "), Pos: v.T("import.sug_pos", "w", commas(s.Words), "p", pct), Focus: (v.Focus == "imp-sugmore" && i == v.SugAt)})
		}
		if end < total {
			c.SugMore = total - end
			c.SugMoreHref = fmt.Sprintf("%s?sug=%d&at=%d", v.PageURL, end+show, end)
			c.SugMoreN, c.SugAt = end+show, end
		}
		c.SugUnlisted = b.SuggestionsTotal - total
		if v.Focus == "imp-sug" {
			// after "Start a chapter here": the next place to start one, else the chapter just made
			for i := range c.Sugs {
				if c.Sugs[i].Ordinal > v.SugOrd {
					c.Sugs[i].Focus = true
					break
				}
			}
			if !slices.ContainsFunc(c.Sugs, func(x impSug) bool { return x.Focus }) {
				v.Focus = ""
				for _, n := range pv.Nodes {
					if n.Ordinal >= v.SugOrd {
						v.Focus = "imp-s-" + rid(n.Key)
						break
					}
				}
				c.FocusH = v.Focus == ""
			}
		}
	}
	for _, n := range pv.Nodes {
		if n.SuggestedTitle != "" {
			c.Caps++
		}
		if n.LineTitle != "" {
			c.Lines++
		}
		if n.Words >= docs.ReadOnlyWords {
			c.Big++
		}
	}
	c.Pages = b.Hint == "pages"
	c.Pat = impPat{Text: dr.Options.DocxPattern, Regex: dr.Options.DocxMatch == imports.MatchRegex}
	if dr.Options.DocxTry != "" { // the field holds what is being tried until a pattern is chosen
		c.Pat.Text, c.Pat.Regex = dr.Options.DocxTry, dr.Options.DocxTryMatch == imports.MatchRegex
	}
	if v.PatTyped {
		c.Pat.Text, c.Pat.Regex, c.Pat.Typed = v.PatText, v.PatMatch == imports.MatchRegex, true
	}
	if p := b.Try; p != nil && dr.Options.DocxTry != "" {
		c.Pat.Has = true
		switch {
		case p.Refused != "":
			c.Pat.Refused, c.Pat.Say = true, v.T("import.pat_refused")
		case p.Count == 0:
			c.Pat.Say = v.T("import.pat_none")
		case p.Count <= len(p.First):
			c.Pat.Say = v.N("import.pat_matches", p.Count)
		default:
			c.Pat.Say = v.T("import.pat_first", "n", commas(p.Count))
		}
		for _, h := range p.First {
			c.Pat.Lines = append(c.Pat.Lines, h.Line)
		}
	}
	return c
}

// sortedStrings is the keys of a map, in order.
func sortedStrings[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
