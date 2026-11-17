// Package publish writes a compiled book as a DOCX, an EPUB 3 or Markdown.
//
// It is pure: a Book (sections of the neutral structure internal/text builds)
// and Options go in, bytes come out. Nothing here reads the database, the
// clock or the disk, so the same input is the same bytes: parts are written in
// a fixed order, ids come from reading order, times are the ones the caller
// puts in the Book, and nothing about the person who exports reaches the file.
//
// Manuscript text is untrusted. Every character that reaches an XML part is
// escaped and stripped of what XML cannot hold; nothing is written as markup
// that was not written by this package; no relationship is ever external; and
// the only binary content is images the caller has already vouched for.
package publish

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Options shapes a compile. The zero value of a field means "the preset's
// choice" when overrides are merged onto a preset (Merge); the pointer fields
// are the ones where false or zero is a real choice.
type Options struct {
	Format string `json:"format,omitempty" enum:"docx,epub,md,pdf,html-paged,odt,rtf,latex,rst,mediawiki" desc:"The file: docx, epub, md (Markdown), pdf (print-ready, laid out by this program) or html-paged (one HTML file with CSS paged media, to print from a browser). odt, rtf, latex, rst and mediawiki are written by the Pandoc program from the compile Markdown, and only while an administrator has switched the Pandoc bridge on."`

	// What goes in.
	Notes          string `json:"notes,omitempty" enum:"none,appendix" desc:"Notes documents (the ones you keep for yourself): none (default), or appendix (after the back matter, under a Notes heading)."`
	Ghosts         string `json:"ghosts,omitempty" enum:"keep,omit" desc:"Ghosted text: omit (the compile presets' default) leaves it out; keep prints it as ordinary text."`
	FrontMatter    *bool  `json:"front_matter,omitempty" desc:"Front matter documents, first (default true)."`
	BackMatter     *bool  `json:"back_matter,omitempty" desc:"Back matter documents, last (default true)."`
	TitlePage      *bool  `json:"title_page,omitempty" desc:"A title page (default true)."`
	PartPages      *bool  `json:"part_pages,omitempty" desc:"A heading for each part (default true). false folds a part's own text into what follows and shows no part heading."`
	TheEnd         *bool  `json:"the_end,omitempty" desc:"'THE END' after the last chapter (default false)."`
	SceneTitles    *bool  `json:"scene_titles,omitempty" desc:"Print the titles of scenes as subheadings (default false: a scene is a break, not a heading)."`
	LinkURLs       *bool  `json:"link_urls,omitempty" desc:"Write a link's address in parentheses after its text (default false). DOCX never carries a live link."`
	ChapterHeading string `json:"chapter_heading,omitempty" enum:"number-title,number,title,none" desc:"What a chapter heading says: 'Chapter 3: Title', 'Chapter 3', the title alone, or nothing."`
	ChapterLabel   string `json:"chapter_label,omitempty" desc:"The word before a chapter number (default Chapter), up to 24 characters."`
	SceneBreak     string `json:"scene_break,omitempty" enum:"hash,stars,ornament" desc:"A scene break is # (manuscript standard), * * * or the ornament text."`
	Ornament       string `json:"ornament,omitempty" desc:"The text of an ornament scene break, up to 8 characters (default a centred asterism-like line of three dots)."`
	Byline         string `json:"byline,omitempty" desc:"The name on the title page, when it is not the project's author name (a pen name)."`
	HeaderTitle    string `json:"header_title,omitempty" desc:"The short title in the running header; default the title."`

	// DOCX page and type.
	Font         string   `json:"font,omitempty" enum:"Times New Roman,Courier New,Georgia,Cambria,Garamond,Palatino Linotype,Liberation Serif,Liberation Mono" desc:"The body font. A DOCX names a font and the reader's machine supplies it. A PDF embeds one: Liberation Serif (the default; metric-compatible with Times New Roman) or Liberation Mono (Courier New); the other names fall back to Liberation Serif with a warning."`
	FontSize     float64  `json:"font_size,omitempty" desc:"Points, 8 to 16."`
	LineSpacing  float64  `json:"line_spacing,omitempty" desc:"1 single, 2 double; 1 to 3."`
	IndentInches *float64 `json:"indent_inches,omitempty" desc:"First-line indent of a paragraph, 0 to 2 inches. The first paragraph after a heading or a scene break is never indented."`
	SpaceAfterPt *float64 `json:"space_after_pt,omitempty" desc:"Space after a paragraph in points, 0 to 36."`
	PageSize     string   `json:"page_size,omitempty" enum:"letter,a4,a5,5x8,5.25x8,5.5x8.5,6x9" desc:"The page (trim size for print): letter, a4, a5, or a trade size in inches (5x8, 5.25x8, 5.5x8.5, 6x9). DOCX supports every size too."`
	MarginInches float64  `json:"margin_inches,omitempty" desc:"All four margins, 0.5 to 2 inches."`
	Header       string   `json:"header,omitempty" enum:"surname-title-page,title-page,none" desc:"The running header from page 2: 'Surname / Title / 12', 'Title / 12', or none."`
	HeaderUpper  *bool    `json:"header_upper,omitempty" desc:"Upper-case the title in the running header (Shunn's format does)."`
	PageNumbers  string   `json:"page_numbers,omitempty" enum:"header,footer,none" desc:"Where page numbers go when the header is none or for a book: in the header, a centred footer, or nowhere."`
	ChapterStart string   `json:"chapter_start,omitempty" enum:"new-page,recto,continuous" desc:"Each chapter starts a new page, or (recto, PDF) on the next right-hand page with a blank page inserted when needed, or runs on."`
	ChapterDrop  *float64 `json:"chapter_drop,omitempty" desc:"How far down the page a chapter title starts, in inches, 0 to 5."`
	TitleLayout  string   `json:"title_layout,omitempty" enum:"manuscript,book" desc:"manuscript: contact block top left, word count top right, title a third of the way down. book: title, subtitle and author centred."`
	WordCount    string   `json:"word_count,omitempty" enum:"about,label,none" desc:"The title page's word count: 'about 80,000 words', 'Word count: 80,000' or none."`
	Justify      *bool    `json:"justify,omitempty" desc:"Justify the body text (a book; never a manuscript)."`
	Language     string   `json:"language,omitempty" desc:"Overrides the project's language tag (BCP 47), up to 16 characters."`
	OwnCover     *bool    `json:"own_cover,omitempty" desc:"EPUB: use the project's own cover (project.cover_layout.get) when it has one. Default true; false puts the generated cover in its place. A release for a public Git remote follows that remote's include_cover."`
	Cover        *bool    `json:"cover,omitempty" desc:"EPUB: put the cover first: a cover page and the cover image the shops and readers use. It is the project's own cover when it has one (project.cover_layout.get), else the generated cover. Default true. KDP takes the cover as a separate upload and asks for no cover page in the file, so turn this off for a file bound for KDP."`
	Contact      *bool    `json:"contact,omitempty" desc:"DOCX manuscript title page: show your contact block (default true). false leaves the block off the page and leaves the word count top right."`
	Clear        []string `json:"clear,omitempty" desc:"Options to reset to empty when this is laid over a preset: byline, header_title, ornament, language, chapter_label, imprint. An empty string in a field means 'not set', so this is how to clear one."`

	// Print: the PDF and paged HTML formats.
	MarginTop       *float64 `json:"margin_top,omitempty" desc:"Print: the top margin in inches, 0.25 to 3 (default: margin_inches)."`
	MarginBottom    *float64 `json:"margin_bottom,omitempty" desc:"Print: the bottom margin in inches, 0.25 to 3."`
	MarginOutside   *float64 `json:"margin_outside,omitempty" desc:"Print: the outside margin (the edge away from the spine) in inches, 0.25 to 3."`
	MarginInside    *float64 `json:"margin_inside,omitempty" desc:"Print: the inside margin (the edge at the spine) in inches, 0.25 to 3, before the gutter is added."`
	Gutter          *float64 `json:"gutter_inches,omitempty" desc:"Print: extra space added to the inside margin for the binding, 0 to 1.5 inches. Printers ask for more of it in a thicker book; the preview warns when the inside margin is under the commonly asked minimum for the page count."`
	Footnotes       string   `json:"footnotes,omitempty" enum:"page,chapter,book" desc:"Print: footnotes at the foot of the page they are on (page, the default), as endnotes at the end of each chapter (chapter), or all together after the last chapter (book)."`
	RunningHeads    *bool    `json:"running_heads,omitempty" desc:"Print: a running head on each page except chapter openers and blank pages: the author on left-hand pages, the title on right-hand pages (default true)."`
	DropCap         *bool    `json:"drop_cap,omitempty" desc:"Print: the first letter of a chapter's first paragraph as a three-line initial (default false). Skipped, with the text left as it is, when the paragraph does not start with a letter."`
	ChapterOrnament *bool    `json:"chapter_ornament,omitempty" desc:"Print: the ornament under a chapter heading (default false). The ornament is the scene-break ornament."`
	TOC             *bool    `json:"toc,omitempty" desc:"Print: a contents page after the front matter, with the page each part and chapter starts on (default true)."`
	Hyphenate       *bool    `json:"hyphenate,omitempty" desc:"Print: break words at the end of a line (default false). Not built: setting it is refused until a hyphenation dictionary ships."`
	CropMarks       *bool    `json:"crop_marks,omitempty" desc:"Print: a proof with crop marks: the page grows by a 0.125 inch bleed and a quarter-inch slug, the marks are drawn outside the trim and the trim and bleed boxes are set (default false)."`
	CopyrightPage   *bool    `json:"copyright_page,omitempty" desc:"Print: a copyright page after the title page (default true)."`
	Imprint         string   `json:"imprint,omitempty" desc:"Print: the text of the copyright page, up to 8 lines and 600 characters. Default: 'Copyright © Name' and 'All rights reserved.', with no year (the program never reads the clock; put the year in this text)."`
}

// True and False are pointers for Options literals.
func True() *bool  { b := true; return &b }
func False() *bool { b := false; return &b }

// F is a pointer to a float for Options literals.
func F(v float64) *float64 { return &v }

// On reports a boolean option that defaults to true when unset.
func On(b *bool) bool { return b == nil || *b }

// Set reports a boolean option that defaults to false when unset.
func Set(b *bool) bool { return b != nil && *b }

func (o Options) indent() float64 {
	if o.IndentInches == nil {
		return 0
	}
	return *o.IndentInches
}

func (o Options) spaceAfter() float64 {
	if o.SpaceAfterPt == nil {
		return 0
	}
	return *o.SpaceAfterPt
}

func (o Options) drop() float64 {
	if o.ChapterDrop == nil {
		return 0
	}
	return *o.ChapterDrop
}

// Merge returns base with every field that over sets laid on top.
func Merge(base, over Options) Options {
	s := func(dst *string, v string) {
		if v != "" {
			*dst = v
		}
	}
	f := func(dst *float64, v float64) {
		if v != 0 {
			*dst = v
		}
	}
	b := func(dst **bool, v *bool) {
		if v != nil {
			x := *v
			*dst = &x
		}
	}
	p := func(dst **float64, v *float64) {
		if v != nil {
			x := *v
			*dst = &x
		}
	}
	out := base
	// copy the base's pointers so the result shares nothing with it
	for _, d := range []**bool{&out.FrontMatter, &out.BackMatter, &out.TitlePage, &out.PartPages, &out.TheEnd, &out.SceneTitles, &out.LinkURLs, &out.Justify, &out.Cover, &out.OwnCover, &out.HeaderUpper, &out.Contact, &out.RunningHeads, &out.DropCap, &out.ChapterOrnament, &out.TOC, &out.Hyphenate, &out.CropMarks, &out.CopyrightPage} {
		if *d != nil {
			x := **d
			*d = &x
		}
	}
	for _, d := range []**float64{&out.IndentInches, &out.SpaceAfterPt, &out.ChapterDrop, &out.MarginTop, &out.MarginBottom, &out.MarginOutside, &out.MarginInside, &out.Gutter} {
		if *d != nil {
			x := **d
			*d = &x
		}
	}
	s(&out.Format, over.Format)
	s(&out.Notes, over.Notes)
	s(&out.Ghosts, over.Ghosts)
	b(&out.FrontMatter, over.FrontMatter)
	b(&out.BackMatter, over.BackMatter)
	b(&out.TitlePage, over.TitlePage)
	b(&out.PartPages, over.PartPages)
	b(&out.TheEnd, over.TheEnd)
	b(&out.SceneTitles, over.SceneTitles)
	b(&out.LinkURLs, over.LinkURLs)
	s(&out.ChapterHeading, over.ChapterHeading)
	s(&out.ChapterLabel, over.ChapterLabel)
	s(&out.SceneBreak, over.SceneBreak)
	s(&out.Ornament, over.Ornament)
	s(&out.Byline, over.Byline)
	s(&out.HeaderTitle, over.HeaderTitle)
	s(&out.Font, over.Font)
	f(&out.FontSize, over.FontSize)
	f(&out.LineSpacing, over.LineSpacing)
	p(&out.IndentInches, over.IndentInches)
	p(&out.SpaceAfterPt, over.SpaceAfterPt)
	s(&out.PageSize, over.PageSize)
	f(&out.MarginInches, over.MarginInches)
	s(&out.Header, over.Header)
	s(&out.PageNumbers, over.PageNumbers)
	s(&out.ChapterStart, over.ChapterStart)
	p(&out.ChapterDrop, over.ChapterDrop)
	s(&out.TitleLayout, over.TitleLayout)
	s(&out.WordCount, over.WordCount)
	b(&out.Justify, over.Justify)
	b(&out.HeaderUpper, over.HeaderUpper)
	s(&out.Language, over.Language)
	b(&out.Cover, over.Cover)
	b(&out.OwnCover, over.OwnCover)
	b(&out.Contact, over.Contact)
	p(&out.MarginTop, over.MarginTop)
	p(&out.MarginBottom, over.MarginBottom)
	p(&out.MarginOutside, over.MarginOutside)
	p(&out.MarginInside, over.MarginInside)
	p(&out.Gutter, over.Gutter)
	s(&out.Footnotes, over.Footnotes)
	b(&out.RunningHeads, over.RunningHeads)
	b(&out.DropCap, over.DropCap)
	b(&out.ChapterOrnament, over.ChapterOrnament)
	b(&out.TOC, over.TOC)
	b(&out.Hyphenate, over.Hyphenate)
	b(&out.CropMarks, over.CropMarks)
	b(&out.CopyrightPage, over.CopyrightPage)
	s(&out.Imprint, over.Imprint)
	for _, name := range over.Clear {
		switch name {
		case "byline":
			out.Byline = ""
		case "header_title":
			out.HeaderTitle = ""
		case "ornament":
			out.Ornament = ""
		case "language":
			out.Language = ""
		case "chapter_label":
			out.ChapterLabel = ""
		case "imprint":
			out.Imprint = ""
		}
	}
	out.Clear = nil // a resolved set of options has nothing left to clear
	return out
}

var clearable = []string{"byline", "header_title", "ornament", "language", "chapter_label", "imprint"}

var (
	fonts     = []string{"Times New Roman", "Courier New", "Georgia", "Cambria", "Garamond", "Palatino Linotype", "Liberation Serif", "Liberation Mono"}
	setFormat = []string{"docx", "epub", "md", "pdf", "html-paged", "odt", "rtf", "latex", "rst", "mediawiki"}
	pageSizes = []string{"letter", "a4", "a5", "5x8", "5.25x8", "5.5x8.5", "6x9"}
)

func contains(set []string, v string) bool {
	for _, s := range set {
		if s == v {
			return true
		}
	}
	return false
}

// FieldError names the option that is wrong.
type FieldError struct{ Field, Message string }

func (e *FieldError) Error() string { return e.Field + ": " + e.Message }

func bad(field, msg string) error { return &FieldError{field, msg} }

// Validate checks every option that was set, and cleans the free text. Run it
// on overrides, and again on the merged result.
func (o *Options) Validate() error {
	enum := func(field, v string, set ...string) error {
		if v != "" && !contains(set, v) {
			return bad(field, field+" is one of: "+strings.Join(set, ", ")+".")
		}
		return nil
	}
	checks := []error{
		enum("format", o.Format, setFormat...),
		enum("notes", o.Notes, "none", "appendix"),
		enum("ghosts", o.Ghosts, "keep", "omit"),
		enum("chapter_heading", o.ChapterHeading, "number-title", "number", "title", "none"),
		enum("scene_break", o.SceneBreak, "hash", "stars", "ornament"),
		enum("font", o.Font, fonts...),
		enum("page_size", o.PageSize, pageSizes...),
		enum("header", o.Header, "surname-title-page", "title-page", "none"),
		enum("page_numbers", o.PageNumbers, "header", "footer", "none"),
		enum("chapter_start", o.ChapterStart, "new-page", "recto", "continuous"),
		enum("footnotes", o.Footnotes, "page", "chapter", "book"),
		enum("title_layout", o.TitleLayout, "manuscript", "book"),
		enum("word_count", o.WordCount, "about", "label", "none"),
	}
	for _, err := range checks {
		if err != nil {
			return err
		}
	}
	for _, name := range o.Clear {
		if !contains(clearable, name) {
			return bad("clear", "clear names options to reset: "+strings.Join(clearable, ", ")+".")
		}
	}
	rng := func(field string, v, lo, hi float64) error {
		if v != 0 && (v < lo || v > hi || v != v) {
			return bad(field, fmt.Sprintf("%s is between %g and %g.", field, lo, hi))
		}
		return nil
	}
	if err := rng("font_size", o.FontSize, 8, 16); err != nil {
		return err
	}
	if err := rng("line_spacing", o.LineSpacing, 1, 3); err != nil {
		return err
	}
	if err := rng("margin_inches", o.MarginInches, 0.5, 2); err != nil {
		return err
	}
	for _, c := range []struct {
		f      string
		v      *float64
		lo, hi float64
	}{{"indent_inches", o.IndentInches, 0, 2}, {"space_after_pt", o.SpaceAfterPt, 0, 36}, {"chapter_drop", o.ChapterDrop, 0, 5},
		{"margin_top", o.MarginTop, 0.25, 3}, {"margin_bottom", o.MarginBottom, 0.25, 3}, {"margin_outside", o.MarginOutside, 0.25, 3},
		{"margin_inside", o.MarginInside, 0.25, 3}, {"gutter_inches", o.Gutter, 0, 1.5}} {
		if c.v != nil && (*c.v < c.lo || *c.v > c.hi || *c.v != *c.v) {
			return bad(c.f, fmt.Sprintf("%s is between %g and %g.", c.f, c.lo, c.hi))
		}
	}
	line := func(field string, v *string, max int) error {
		*v = CleanLine(*v)
		if utf8.RuneCountInString(*v) > max {
			return bad(field, fmt.Sprintf("%s is limited to %d characters.", field, max))
		}
		return nil
	}
	if err := line("chapter_label", &o.ChapterLabel, 24); err != nil {
		return err
	}
	if err := line("ornament", &o.Ornament, 8); err != nil {
		return err
	}
	if err := line("byline", &o.Byline, 200); err != nil {
		return err
	}
	if err := line("header_title", &o.HeaderTitle, 80); err != nil {
		return err
	}
	if err := line("language", &o.Language, 16); err != nil {
		return err
	}
	if err := o.checkMeasure(); err != nil {
		return err
	}
	if Set(o.Hyphenate) {
		return bad("hyphenate", "Hyphenation is not built: leave hyphenate off.")
	}
	if o.Imprint != "" {
		var lines []string
		for _, l := range strings.Split(CleanXML(o.Imprint), "\n") {
			if l = CleanLine(l); l != "" {
				lines = append(lines, l)
			}
		}
		o.Imprint = strings.Join(lines, "\n")
		if len(lines) > 8 || utf8.RuneCountInString(o.Imprint) > 600 {
			return bad("imprint", "imprint is limited to 8 lines and 600 characters.")
		}
	}
	if o.Language != "" && !validLang(o.Language) {
		return bad("language", "Use a language tag such as en, en-GB or ga.")
	}
	return nil
}

func validLang(s string) bool {
	if s == "" || len(s) > 16 {
		return false
	}
	for _, r := range s {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-') {
			return false
		}
	}
	return true
}

// CleanLine makes free text one line of characters XML can hold: control
// characters and bidirectional overrides are dropped and white space is
// collapsed.
func CleanLine(s string) string {
	return strings.Join(strings.Fields(CleanXML(s)), " ")
}

// CleanXML removes what an XML 1.0 document cannot hold (most control
// characters, lone surrogates, U+FFFE and U+FFFF) and the bidirectional
// overrides and embeddings, which can make text read in a different order than
// it is stored. Tabs and line feeds stay.
func CleanXML(s string) string {
	clean := true
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 && s[i] != '\t' && s[i] != '\n' || s[i] >= 0x80 || s[i] == 0x7f {
			clean = false
			break
		}
	}
	if clean {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if validXMLChar(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func validXMLChar(r rune) bool {
	switch {
	case r == '\t' || r == '\n':
		return true
	case r < 0x20, r == 0x7f:
		return false
	case r >= 0x80 && r <= 0x9f: // C1 controls: legal in XML 1.0 but never text
		return false
	case r == utf8.RuneError:
		return true // a replacement character is text; invalid bytes were already turned into it by the range loop
	case r >= 0xD800 && r <= 0xDFFF, r == 0xFFFE, r == 0xFFFF, r > 0x10FFFF:
		return false
	case r >= 0x202A && r <= 0x202E: // embeddings and overrides
		return false
	}
	return true
}

// checkMeasure refuses a print page whose text would be too narrow to set: the
// margins, the gutter and the indent together must leave a measure of two inches
// and a text block of three inches. It looks only at options that name a page
// size (a merged set of options does); an override alone cannot be judged.
func (o Options) checkMeasure() error {
	if o.Format != "pdf" && o.Format != "html-paged" {
		return nil
	}
	w, h, ok := PageSize(o.PageSize)
	if !ok {
		return nil
	}
	p := resolveParamsRaw(o, w, h)
	in := func(v mpt) float64 { return float64(v) / float64(inch) }
	if p.textW < 2*inch {
		return bad("margin_inside", fmt.Sprintf("With a %s page and these margins and gutter the text would be only %.2f inches wide; it needs at least 2. Narrow the margins or the gutter, or choose a larger page.", o.PageSize, in(p.textW)))
	}
	if p.textH < 3*inch {
		return bad("margin_top", fmt.Sprintf("With a %s page and these margins the text would be only %.2f inches tall; it needs at least 3.", o.PageSize, in(p.textH)))
	}
	if o.IndentInches != nil && inches(*o.IndentInches) > p.textW/3 {
		return bad("indent_inches", fmt.Sprintf("An indent of %.2f inches is more than a third of the %.2f-inch text width; use a smaller indent.", *o.IndentInches, in(p.textW)))
	}
	return nil
}
