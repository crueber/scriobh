// Package compile turns a project's manuscript into a file to send or to read:
// the US Standard Manuscript or a UK manuscript as a DOCX, a styled DOCX, an
// EPUB 3, or plain Markdown.
//
// Compiling is selection first and writing second. Selection is the same for
// every format and is what the preview shows: which documents are in (those
// marked "do not compile", the notes, and anything inside them are not), in
// what order (front matter first, back matter last), with what headings
// (chapter numbers and titles by the preset), what between scenes (a break, in
// the preset's style), and what is left out and why. Writing is
// internal/publish, which takes the selection as a Book and the options as they
// are. The preview and the export share one resolve step, so the warnings and
// the input hash are the same in both.
//
// Ghosted text and each document's alternatives comment are never in a compile
// (ghosts are omitted unless the preset keeps them), and private notes never
// are. Manuscript text is untrusted: it reaches the file only through
// text.Structure and the writers' escaping.
package compile

import (
	"github.com/crueber/scriobh/internal/publish"
)

// Built-in preset ids.
const (
	ShunnUS             = "shunn-us"
	ShunnUSCourier      = "shunn-us-courier"
	UKManuscript        = "uk-manuscript"
	UKManuscriptCourier = "uk-manuscript-courier"
	PlainMarkdown       = "plain-markdown"
	StyledDOCX          = "styled-docx"
	EPUB3               = "epub3"
	Print6x9            = "print-6x9"
	Print55x85          = "print-5.5x8.5"
	Print5x8            = "print-5x8"
	PrintA5             = "print-a5"
	ProofLetter         = "proof-letter"
	ProofA4             = "proof-a4"
	PagedHTML           = "paged-html"
)

// Preset is a named set of options.
type Preset struct {
	ID          string          `json:"id" desc:"A built-in id (shunn-us, uk-manuscript, ...) or a custom preset's id (cpr_...)."`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Builtin     bool            `json:"builtin"`
	Base        string          `json:"base,omitempty" desc:"A custom preset's built-in starting point."`
	Format      string          `json:"format" enum:"docx,epub,md,pdf,html-paged,odt,rtf,latex,rst,mediawiki"`
	Options     publish.Options `json:"options" desc:"Every option, resolved."`
	UpdatedAt   string          `json:"updated_at,omitempty"`
}

func manuscript(font string, o publish.Options) publish.Options {
	base := publish.Options{
		Format: "docx", Notes: "none", Ghosts: "omit",
		FrontMatter: publish.True(), BackMatter: publish.True(), TitlePage: publish.True(), PartPages: publish.True(),
		TheEnd: publish.False(), SceneTitles: publish.False(), LinkURLs: publish.False(), Justify: publish.False(), Cover: publish.False(),
		ChapterHeading: "number-title", ChapterLabel: "Chapter", SceneBreak: "hash", Ornament: "",
		Font: font, FontSize: 12, LineSpacing: 2, IndentInches: publish.F(0.5), SpaceAfterPt: publish.F(0),
		PageSize: "letter", MarginInches: 1, Header: "surname-title-page", HeaderUpper: publish.True(), PageNumbers: "header",
		ChapterStart: "new-page", ChapterDrop: publish.F(3), TitleLayout: "manuscript", WordCount: "about",
	}
	return publish.Merge(base, o)
}

// print is a book set for a printer: Liberation Serif, justified, the first
// paragraph of a chapter not indented, chapters on right-hand pages with a drop,
// a running head, a contents page, footnotes at the foot of the page.
func print(size string, fontSize float64, margins [4]float64, gutter, drop, indent float64, o publish.Options) publish.Options {
	base := publish.Options{
		Format: "pdf", Notes: "none", Ghosts: "omit",
		FrontMatter: publish.True(), BackMatter: publish.True(), TitlePage: publish.True(), PartPages: publish.True(),
		TheEnd: publish.False(), SceneTitles: publish.False(), LinkURLs: publish.False(), Justify: publish.True(), Cover: publish.False(),
		ChapterHeading: "number-title", ChapterLabel: "Chapter", SceneBreak: "ornament",
		Font: "Liberation Serif", FontSize: fontSize, LineSpacing: 1.1, IndentInches: publish.F(indent), SpaceAfterPt: publish.F(0),
		PageSize: size, MarginTop: publish.F(margins[0]), MarginBottom: publish.F(margins[1]), MarginOutside: publish.F(margins[2]), MarginInside: publish.F(margins[3]),
		Gutter: publish.F(gutter), PageNumbers: "footer", ChapterStart: "recto", ChapterDrop: publish.F(drop), TitleLayout: "book",
		RunningHeads: publish.True(), DropCap: publish.False(), ChapterOrnament: publish.False(), TOC: publish.True(), CopyrightPage: publish.True(),
		Hyphenate: publish.False(), CropMarks: publish.False(), Footnotes: "page", HeaderUpper: publish.False(),
	}
	return publish.Merge(base, o)
}

var builtins = []Preset{
	{ID: ShunnUS, Name: "US Standard Manuscript (Shunn)", Builtin: true,
		Description: "Times New Roman 12 pt, double-spaced, 1-inch margins, a title page with your contact block and word count, a running header of Surname / TITLE / page from page 2, each chapter on a new page a third of the way down, # for scene breaks, a half-inch first-line indent.",
		Options:     manuscript("Times New Roman", publish.Options{})},
	{ID: ShunnUSCourier, Name: "US Standard Manuscript (Shunn), Courier", Builtin: true,
		Description: "The same, in Courier New 12 pt.",
		Options:     manuscript("Courier New", publish.Options{})},
	{ID: UKManuscript, Name: "UK manuscript", Builtin: true,
		Description: "A4, Times New Roman 12 pt, double-spaced, 1-inch margins, a title page with a word count line, a running header of Surname / Title / page from page 2, chapters on new pages, # for scene breaks.",
		Options:     manuscript("Times New Roman", publish.Options{PageSize: "a4", Header: "surname-title-page", HeaderUpper: publish.False(), WordCount: "label", Language: "en-GB"})},
	{ID: UKManuscriptCourier, Name: "UK manuscript, Courier", Builtin: true,
		Description: "The same, in Courier New 12 pt.",
		Options:     manuscript("Courier New", publish.Options{PageSize: "a4", Header: "surname-title-page", HeaderUpper: publish.False(), WordCount: "label", Language: "en-GB"})},
	{ID: StyledDOCX, Name: "Styled DOCX (reading copy)", Builtin: true,
		Description: "Georgia 11 pt, 1.15 line spacing, justified, headings in bold, a centred title page, page numbers in the footer: for a beta reader or a proof copy, not a submission.",
		Options: publish.Options{Format: "docx", Notes: "none", Ghosts: "omit",
			FrontMatter: publish.True(), BackMatter: publish.True(), TitlePage: publish.True(), PartPages: publish.True(),
			TheEnd: publish.False(), SceneTitles: publish.False(), LinkURLs: publish.False(), Justify: publish.True(), Cover: publish.False(),
			ChapterHeading: "number-title", ChapterLabel: "Chapter", SceneBreak: "stars",
			Font: "Georgia", FontSize: 11, LineSpacing: 1.15, IndentInches: publish.F(0.3), SpaceAfterPt: publish.F(0),
			PageSize: "letter", MarginInches: 1, Header: "none", HeaderUpper: publish.False(), PageNumbers: "footer",
			ChapterStart: "new-page", ChapterDrop: publish.F(1.5), TitleLayout: "book", WordCount: "none"}},
	{ID: EPUB3, Name: "EPUB 3", Builtin: true,
		Description: "A reflowable e-book: cover, title page, contents, chapters, front and back matter, footnotes at the end of each chapter, * * * for scene breaks. System fonts only, no colours: it takes on the reader's own.",
		Options: publish.Options{Format: "epub", Notes: "none", Ghosts: "omit",
			FrontMatter: publish.True(), BackMatter: publish.True(), TitlePage: publish.True(), PartPages: publish.True(),
			TheEnd: publish.False(), SceneTitles: publish.False(), LinkURLs: publish.False(), Justify: publish.False(), Cover: publish.True(),
			ChapterHeading: "number-title", ChapterLabel: "Chapter", SceneBreak: "stars", IndentInches: publish.F(0.25), TitleLayout: "book"}},
	{ID: Print6x9, Name: "Print, 6 x 9 in", Builtin: true,
		Description: "A print-ready PDF for a 6 x 9 inch paperback: Liberation Serif 11 pt on 14 pt, justified, mirrored margins with a 0.15 inch gutter, chapters on right-hand pages with a drop, running heads (author left, title right), page numbers at the foot, roman front matter, a contents page, footnotes at the foot of the page, an ornament for scene breaks. Fonts are embedded.",
		Options:     print("6x9", 11, [4]float64{0.75, 0.85, 0.6, 0.75}, 0.15, 1.6, 0.2, publish.Options{})},
	{ID: Print55x85, Name: "Print, 5.5 x 8.5 in", Builtin: true,
		Description: "The same for a 5.5 x 8.5 inch trade paperback, at 10.5 pt.",
		Options:     print("5.5x8.5", 10.5, [4]float64{0.7, 0.8, 0.55, 0.7}, 0.15, 1.45, 0.19, publish.Options{})},
	{ID: Print5x8, Name: "Print, 5 x 8 in", Builtin: true,
		Description: "The same for a 5 x 8 inch paperback, at 10.5 pt.",
		Options:     print("5x8", 10.5, [4]float64{0.65, 0.75, 0.5, 0.65}, 0.15, 1.3, 0.18, publish.Options{})},
	{ID: PrintA5, Name: "Print, A5", Builtin: true,
		Description: "The same for an A5 (148 x 210 mm) paperback, at 10.5 pt.",
		Options:     print("a5", 10.5, [4]float64{0.75, 0.85, 0.6, 0.7}, 0.15, 1.5, 0.19, publish.Options{Language: "en-GB"})},
	{ID: ProofLetter, Name: "Proof, US Letter", Builtin: true,
		Description: "A proof to print at home on US Letter and read with a pen: 12 pt, one-inch margins with a quarter-inch gutter, chapters on a new page, the same running heads and numbers as the print presets.",
		Options:     print("letter", 12, [4]float64{1, 1, 1, 1}, 0.25, 2, 0.3, publish.Options{ChapterStart: "new-page", LineSpacing: 1.2})},
	{ID: ProofA4, Name: "Proof, A4", Builtin: true,
		Description: "The same on A4.",
		Options:     print("a4", 12, [4]float64{1, 1, 1, 1}, 0.25, 2, 0.3, publish.Options{ChapterStart: "new-page", LineSpacing: 1.2, Language: "en-GB"})},
	{ID: PagedHTML, Name: "Paged HTML, 6 x 9 in", Builtin: true,
		Description: "One self-contained HTML file with CSS paged media for the 6 x 9 inch page, to print to PDF from a browser. Browsers do part of paged media: footnotes become endnotes and the contents have no page numbers; use the print PDF presets for a printer.",
		Options:     print("6x9", 11, [4]float64{0.75, 0.85, 0.6, 0.75}, 0.15, 1.6, 0.2, publish.Options{Format: "html-paged"})},
	{ID: PlainMarkdown, Name: "Plain Markdown", Builtin: true,
		Description: "One Markdown file of the chosen text: the title, headings, scene breaks as * * *. No front matter, no private notes, no alternatives, no ghosted text.",
		Options: publish.Options{Format: "md", Notes: "none", Ghosts: "omit",
			FrontMatter: publish.True(), BackMatter: publish.True(), TitlePage: publish.True(), PartPages: publish.True(),
			TheEnd: publish.False(), SceneTitles: publish.False(), LinkURLs: publish.False(),
			ChapterHeading: "title", ChapterLabel: "Chapter", SceneBreak: "stars"}},
}

func init() {
	for i := range builtins {
		builtins[i].Format = builtins[i].Options.Format
	}
}

// Builtins returns the built-in presets, copied.
func Builtins() []Preset {
	out := make([]Preset, len(builtins))
	for i, p := range builtins {
		p.Options = publish.Merge(p.Options, publish.Options{})
		out[i] = p
	}
	return out
}

// Builtin returns one built-in preset.
func Builtin(id string) (Preset, bool) {
	for _, p := range Builtins() {
		if p.ID == id {
			return p, true
		}
	}
	return Preset{}, false
}
