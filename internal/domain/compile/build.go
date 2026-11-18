package compile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/export"
	"github.com/crueber/scriobh/internal/publish"
	"github.com/crueber/scriobh/internal/text"
)

// Warning is something the writer should know before the file goes out.
type Warning struct {
	Code       string `json:"code" desc:"A stable token: chapter_untitled, front_matter_untitled, scene_break_at_end, scene_break_at_start, empty_document, not_compiled, notes_excluded, no_author, no_contact, image_missing, image_remote, image_skipped, nothing_to_compile; and from a PDF: missing_glyph, script_unsupported, font_substituted, loose_lines, overfull_line, widow_orphan, image_low_resolution, image_not_embedded, gutter_small, few_pages, layout_limit, table_columns, table_images, nesting_flattened, note_not_set, layout_refused; from a cover: spine_narrow, odd_page_count."`
	Message    string `json:"message"`
	DocumentID string `json:"document_id,omitempty"`
	Title      string `json:"title,omitempty"`
	// Part is set on a cover's warnings: the side of the cover it is about.
	Part string `json:"part,omitempty" enum:"front,back,spine,wrap" desc:"A cover warning only: which part of the cover it is about (the spine, the barcode's back, a wrap or a side). Empty for every other warning and for a cover warning about the cover as a whole."`
}

// Excluded is a document the compile leaves out because it is marked "do not
// compile", or sits inside one that is.
type Excluded struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Kind   string `json:"kind"`
	Reason string `json:"reason"`
}

// OutlineItem is one heading of the compiled book.
type OutlineItem struct {
	Kind      string   `json:"kind" enum:"front_matter,part,chapter,back_matter,notes"`
	Title     string   `json:"title" desc:"The document's own title."`
	Heading   string   `json:"heading" desc:"What the heading says in the file; empty when the preset prints none."`
	Number    int      `json:"number,omitempty" desc:"The chapter's number."`
	Level     int      `json:"level" desc:"1 or 2: a chapter in a part is level 2."`
	Words     int      `json:"words"`
	Documents []string `json:"documents" desc:"The ids of the documents this section is made of, in order."`
	Page      int      `json:"page" desc:"The physical page it starts on, counted from 1 over every page of the file, blank ones too: an estimate for a DOCX preset, exact for a PDF preset. 0 when the format has no pages. The number printed on that page is label."`
	Label     string   `json:"label,omitempty" desc:"PDF presets: the page number a reader sees on that page (roman in the front matter, then arabic from 1)."`
}

// built is a resolved compile: the book, and everything the preview and the
// manifest say about it.
type built struct {
	book        *publish.Book
	held        []export.Held
	outline     []OutlineItem
	warnings    []Warning
	excluded    []Excluded
	skipped     int      // documents left out because they are marked not to compile
	switchedOff int      // documents left out because the preset switches front or back matter off
	meta        []string // per held document: its title, kind and role, for the input hash
	notesDocs   int
	notesWord   int
	words       int
	empty       bool
}

// Contact is the author's manuscript contact block.
type Contact struct {
	Name    string `json:"name" desc:"The name for the contact block; empty: the byline."`
	Surname string `json:"surname" desc:"For the running header; empty: the last word of the byline."`
	Address string `json:"address" desc:"Lines separated by newlines."`
	Email   string `json:"email"`
	Phone   string `json:"phone"`
}

func (c Contact) block() publish.Contact {
	var lines []string
	for _, l := range strings.Split(c.Address, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return publish.Contact{Name: c.Name, Address: lines, Email: c.Email, Phone: c.Phone}
}

// imageResolver turns the destinations of the images a text links to into
// images that are safe to embed.
type imageResolver func(keys []string) (map[string]publish.Image, []Warning)

// docText is a document's text as it is compiled: the alternatives comment is
// gone (the chosen wording is the text), ghosted text is omitted or kept as
// ordinary text, and what remains is what is hashed.
func docText(content, ghosts string) string {
	t := alts.BodyWithoutComment(content)
	return text.StripGhosts(t, ghosts != "keep")
}

type docRef struct {
	d    *export.SourceDoc
	role int // 0 front, 1 main, 2 back, 3 notes
}

const (
	roleFront = iota
	roleMain
	roleBack
	roleNotes
)

// build selects and orders the documents of src and assembles the book.
func build(src *export.Source, o publish.Options, contact Contact, resolve imageResolver) *built {
	b := &built{}
	docs := src.Docs
	byID := make(map[string]*export.SourceDoc, len(docs))
	for i := range docs {
		byID[docs[i].ID] = &docs[i]
	}
	// Excluded by flag: the document or any ancestor says "do not compile".
	flagged := map[string]int8{}
	var isFlagged func(d *export.SourceDoc, depth int) bool
	isFlagged = func(d *export.SourceDoc, depth int) bool {
		if v, ok := flagged[d.ID]; ok {
			return v < 0
		}
		flagged[d.ID] = 1
		ex := !d.Include
		if !ex && depth < 10_000 {
			if p, ok := byID[d.ParentID]; ok && p != d {
				ex = isFlagged(p, depth+1)
			}
		}
		if ex {
			flagged[d.ID] = -1
		}
		return ex
	}
	// The role of a document: front matter and back matter bring everything inside
	// them along, as an excluded document does.
	roles := map[string]int{}
	var roleOf func(d *export.SourceDoc, depth int) int
	roleOf = func(d *export.SourceDoc, depth int) int {
		if r, ok := roles[d.ID]; ok {
			return r
		}
		roles[d.ID] = roleMain // a parent cycle cannot recurse
		r := roleMain
		switch d.Kind {
		case "front_matter":
			r = roleFront
		case "back_matter":
			r = roleBack
		default:
			if p, ok := byID[d.ParentID]; ok && p != d && depth < 10_000 {
				if pr := roleOf(p, depth+1); pr == roleFront || pr == roleBack {
					r = pr
				}
			}
		}
		roles[d.ID] = r
		return r
	}
	notes := o.Notes == "appendix"
	var ordered []docRef
	var front, main, back, appendix []docRef
	children := map[string]int{}  // compiled documents inside each document
	plainKids := map[string]int{} // of them, those that are not themselves front or back matter
	for i := range docs {
		d := &docs[i]
		if isFlagged(d, 0) {
			b.skipped++
			if !d.Note { // notes are reported as notes, whatever their flag
				reason := "marked do not compile"
				if d.Include {
					reason = "inside a document marked do not compile"
				}
				b.excluded = append(b.excluded, Excluded{ID: d.ID, Title: d.Title, Kind: d.Kind, Reason: reason})
			}
			continue
		}
		if d.Note {
			if !notes {
				b.notesDocs++
				b.notesWord += d.WordCount
				continue
			}
			appendix = append(appendix, docRef{d, roleNotes})
			continue
		}
		switch roleOf(d, 0) {
		case roleFront:
			if !publish.On(o.FrontMatter) {
				reason := "front matter is switched off in this preset"
				if d.Kind != "front_matter" {
					reason = "inside front matter, which is switched off in this preset"
				}
				b.excluded = append(b.excluded, Excluded{ID: d.ID, Title: d.Title, Kind: d.Kind, Reason: reason})
				b.switchedOff++
				continue
			}
			front = append(front, docRef{d, roleFront})
		case roleBack:
			if !publish.On(o.BackMatter) {
				reason := "back matter is switched off in this preset"
				if d.Kind != "back_matter" {
					reason = "inside back matter, which is switched off in this preset"
				}
				b.excluded = append(b.excluded, Excluded{ID: d.ID, Title: d.Title, Kind: d.Kind, Reason: reason})
				b.switchedOff++
				continue
			}
			back = append(back, docRef{d, roleBack})
		default:
			main = append(main, docRef{d, roleMain})
		}
		if d.ParentID != "" {
			children[d.ParentID]++
			if d.Kind != "front_matter" && d.Kind != "back_matter" {
				plainKids[d.ParentID]++
			}
		}
	}
	ordered = append(append(append(append(ordered, front...), main...), back...), appendix...)

	chapterNo := 0
	noteStart := 0
	var secs []*publish.Section
	var secDocs [][]string
	var secWords []int
	var cur int = -1 // index of the section scenes join
	curRole := -1
	words := 0
	hasPart := false

	newSection := func(s publish.Section, id string) int {
		secs = append(secs, &s)
		secDocs = append(secDocs, []string{id})
		secWords = append(secWords, 0)
		return len(secs) - 1
	}
	noteBudget := text.MaxNoteCopies // shared by every document: the copies of the whole book are bounded
	structure := func(t string) text.Doc {
		doc := text.Structure(t, text.StructOptions{NoteStart: noteStart, NoteBudget: &noteBudget})
		noteStart += len(doc.Notes)
		return doc
	}
	for _, r := range ordered {
		d := r.d
		if r.role != curRole {
			cur, curRole = -1, r.role
		}
		t := docText(d.Content, o.Ghosts)
		held := export.Held{ID: d.ID, SHA256: export.HashOf(t)}
		hasText := strings.TrimSpace(t) != ""
		title := strings.TrimSpace(d.Title)
		if !hasText && title == "" {
			b.warn("empty_document", "A document has no title and no text and was left out.", d.ID, "")
			continue
		}
		var doc text.Doc
		var w int
		if hasText {
			doc = structure(t)
			w = text.WordCount(t)
		}
		words += w
		switch {
		case r.role == roleNotes:
			if cur < 0 {
				cur = newSection(publish.Section{Kind: publish.SecNotes, Title: "Notes", Level: 1}, d.ID)
			} else {
				secDocs[cur] = append(secDocs[cur], d.ID)
			}
			if title != "" {
				secs[cur].Nodes = append(secs[cur].Nodes, text.Node{Kind: text.NHeading, Level: 1, Inl: []text.Inline{{Kind: text.IText, Text: title}}})
			}
			secs[cur].Nodes = append(secs[cur].Nodes, doc.Nodes...)
			secs[cur].Notes = append(secs[cur].Notes, doc.Notes...)
			secWords[cur] += w
		case r.role == roleFront || r.role == roleBack:
			kind := publish.SecFront
			if r.role == roleBack {
				kind = publish.SecBack
			}
			if d.Kind == "scene" || (d.Kind != "front_matter" && d.Kind != "back_matter" && d.Kind != "chapter" && d.Kind != "part") {
				// a scene inside front or back matter joins the page it is in
				if !hasText {
					b.warn("empty_document", "A scene has no text and was left out.", d.ID, title)
					continue
				}
				if cur < 0 {
					cur = newSection(publish.Section{Kind: kind, Level: 1}, d.ID)
				} else {
					secDocs[cur] = append(secDocs[cur], d.ID)
				}
				s := secs[cur]
				switch {
				case publish.Set(o.SceneTitles) && title != "":
					s.Nodes = append(s.Nodes, text.Node{Kind: text.NHeading, Level: 1, Inl: []text.Inline{{Kind: text.IText, Text: title}}})
				case len(s.Nodes) > 0:
					s.Nodes = append(s.Nodes, text.Node{Kind: text.NRule})
				}
				s.Nodes = append(s.Nodes, doc.Nodes...)
				s.Notes = append(s.Notes, doc.Notes...)
				secWords[cur] += w
				break
			}
			if !hasText && plainKids[d.ID] == 0 && children[d.ID] > 0 {
				break // a folder of front matter: its documents are the sections
			}
			if title == "" {
				b.warn("front_matter_untitled", "A front or back matter document has no title, so it has no heading.", d.ID, "")
			}
			i := newSection(publish.Section{Kind: kind, Title: title, Level: 1, Nodes: doc.Nodes, Notes: doc.Notes}, d.ID)
			secWords[i] = w
			cur = i
		case d.Kind == "part":
			if publish.On(o.PartPages) {
				hasPart = true
				i := newSection(publish.Section{Kind: publish.SecPart, Title: title, Level: 1, Nodes: doc.Nodes, Notes: doc.Notes}, d.ID)
				secWords[i] = w
				cur = -1
			} else if hasText {
				i := newSection(publish.Section{Kind: publish.SecChapter, Level: 1, Nodes: doc.Nodes, Notes: doc.Notes}, d.ID)
				secWords[i] = w
				cur = i
			}
		case d.Kind == "chapter":
			chapterNo++
			if title == "" && (o.ChapterHeading == "" || o.ChapterHeading == "title" || o.ChapterHeading == "number-title") {
				b.warn("chapter_untitled", fmt.Sprintf("Chapter %d has no title, so its heading is the number alone.", chapterNo), d.ID, "")
			}
			i := newSection(publish.Section{Kind: publish.SecChapter, Title: title, Number: chapterNo, Level: 1, Nodes: doc.Nodes, Notes: doc.Notes}, d.ID)
			secWords[i] = w
			cur = i
		default: // a scene
			if !hasText {
				b.warn("empty_document", "A scene has no text and was left out.", d.ID, title)
				continue
			}
			if cur < 0 {
				cur = newSection(publish.Section{Kind: publish.SecChapter, Level: 1}, d.ID)
			} else {
				secDocs[cur] = append(secDocs[cur], d.ID)
			}
			s := secs[cur]
			switch {
			case publish.Set(o.SceneTitles) && title != "":
				s.Nodes = append(s.Nodes, text.Node{Kind: text.NHeading, Level: 1, Inl: []text.Inline{{Kind: text.IText, Text: title}}})
			case len(s.Nodes) > 0:
				s.Nodes = append(s.Nodes, text.Node{Kind: text.NRule})
			}
			s.Nodes = append(s.Nodes, doc.Nodes...)
			s.Notes = append(s.Notes, doc.Notes...)
			secWords[cur] += w
		}
		b.held = append(b.held, held)
		b.meta = append(b.meta, fmt.Sprintf("%s\x00%s\x00%d", d.Title, d.Kind, r.role))
	}
	for i, s := range secs {
		if s.Kind == publish.SecChapter && hasPart {
			// Chapters in a book with parts sit one level down; the implicit
			// chapters of loose scenes stay at the top.
			if s.Number > 0 {
				s.Level = 2
			}
		}
		// A break at the very start or end of a section is a mistake in the text, not a design.
		if n := len(s.Nodes); n > 0 {
			if s.Nodes[n-1].Kind == text.NRule {
				b.warn("scene_break_at_end", "A scene break ends "+quoteTitle(sectionName(*s))+"; nothing follows it.", secDocs[i][len(secDocs[i])-1], s.Title)
			}
			if s.Nodes[0].Kind == text.NRule {
				b.warn("scene_break_at_start", "A scene break begins "+quoteTitle(sectionName(*s))+"; nothing precedes it.", secDocs[i][0], s.Title)
			}
		}
	}
	// The book.
	book := &publish.Book{Images: map[string]publish.Image{}}
	for _, s := range secs {
		book.Sections = append(book.Sections, *s)
	}
	b.words = words
	b.empty = len(secs) == 0
	b.book = book
	for i, s := range secs {
		kind := map[publish.SectionKind]string{publish.SecFront: "front_matter", publish.SecPart: "part", publish.SecChapter: "chapter", publish.SecBack: "back_matter", publish.SecNotes: "notes"}[s.Kind]
		b.outline = append(b.outline, OutlineItem{Kind: kind, Title: s.Title, Heading: publish.HeadingText(*s, o), Number: s.Number, Level: s.Level,
			Words: secWords[i], Documents: secDocs[i]})
	}
	if b.skipped > 0 {
		b.warn("not_compiled", fmt.Sprintf("%d %s marked do not compile (or inside one) %s left out.", b.skipped, countWord(b.skipped, "document", "documents"), countWord(b.skipped, "was", "were")), "", "")
	}
	if b.notesDocs > 0 {
		b.warn("notes_excluded", fmt.Sprintf("%d %s of notes, %d %s, %s not in the compile.", b.notesDocs, countWord(b.notesDocs, "document", "documents"), b.notesWord, countWord(b.notesWord, "word", "words"), countWord(b.notesDocs, "is", "are")), "", "")
	}
	// Images.
	if resolve != nil {
		var keys []string
		seen := map[string]bool{}
		collectImages(book.Sections, func(k string) {
			if !seen[k] {
				seen[k] = true
				keys = append(keys, k)
			}
		})
		if len(keys) > 0 {
			imgs, ws := resolve(keys)
			for k, v := range imgs {
				book.Images[k] = v
			}
			b.warnings = append(b.warnings, ws...)
		}
	}
	// Title page facts.
	book.Title = strings.TrimSpace(src.Project.Title)
	book.Subtitle = strings.TrimSpace(src.Project.Subtitle)
	// The name on the title page is the byline option, else the project's author.
	// A manuscript (DOCX) may fall back to the name in the contact block, as it
	// always has; print, a cover and a book's metadata never do: a contact block
	// is for an agent or an editor, not for a printed book. contact: false
	// keeps the block out of everything.
	byline := firstNonEmpty(o.Byline, src.Project.Author)
	if byline == "" && o.Format == "docx" && publish.On(o.Contact) {
		byline = contact.Name
	}
	book.Byline = publish.CleanLine(byline)
	surname := lastWord(book.Byline)
	if publish.On(o.Contact) {
		surname = firstNonEmpty(contact.Surname, surname)
	}
	book.Surname = publish.CleanLine(surname)
	book.Language = src.Project.Language
	book.Identifier = projectUUID(src.Project.ID)
	book.Modified = parseTime(src.Project.UpdatedAt)
	if publish.On(o.Contact) && o.Format != "pdf" && o.Format != "html-paged" {
		book.Contact = contact.block()
	}
	book.Words = words
	if book.Byline == "" && publish.On(o.TitlePage) {
		b.warn("no_author", "The project has no author name, so the title page has no byline. Set one in the project's details.", "", "")
	}
	if o.TitleLayout == "manuscript" && publish.On(o.TitlePage) && strings.TrimSpace(contact.Name) == "" && strings.TrimSpace(contact.Email) == "" && strings.TrimSpace(contact.Address) == "" {
		b.warn("no_contact", "A manuscript title page shows your contact details, and none are set.", "", "")
	}
	if b.empty {
		b.warn("nothing_to_compile", "There is nothing to compile: no document is included.", "", "")
	}
	return b
}

func (b *built) warn(code, msg, docID, title string) {
	b.warnings = append(b.warnings, Warning{Code: code, Message: msg, DocumentID: docID, Title: title})
}

func sectionName(s publish.Section) string {
	if t := strings.TrimSpace(s.Title); t != "" {
		return t
	}
	if s.Number > 0 {
		return fmt.Sprintf("chapter %d", s.Number)
	}
	return "a section"
}

func quoteTitle(s string) string { return "“" + s + "”" }

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func lastWord(s string) string {
	f := strings.Fields(s)
	if len(f) == 0 {
		return ""
	}
	return f[len(f)-1]
}

// projectUUID is a stable UUID (version 5 layout) derived from the project id,
// so an EPUB's identifier does not change between compiles and is not the id.
func projectUUID(id string) string {
	sum := sha256.Sum256([]byte("scriobh:epub:" + id))
	b := sum[:16]
	b[6] = b[6]&0x0f | 0x50
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

func parseTime(s string) time.Time {
	t, err := db.Parse(s)
	if err != nil {
		return time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return t.UTC()
}

// collectImages calls fn with the destination of every image in the sections.
func collectImages(secs []publish.Section, fn func(string)) {
	var inl func([]text.Inline)
	inl = func(in []text.Inline) {
		for _, x := range in {
			if x.Kind == text.IImage {
				fn(x.Text)
			}
			inl(x.Kids)
		}
	}
	var walk func([]text.Node)
	walk = func(ns []text.Node) {
		for _, n := range ns {
			inl(n.Inl)
			walk(n.Kids)
			for _, it := range n.Items {
				walk(it)
			}
			for _, r := range n.Rows {
				for _, c := range r.Cells {
					inl(c.Inl)
				}
			}
		}
	}
	for _, s := range secs {
		walk(s.Nodes)
		for _, f := range s.Notes {
			walk(f.Nodes)
		}
	}
}

// inputHash is the hash of everything that decides the file's content: the
// preset and the resolved options, the project's title, byline and language,
// the contact block (hashed into the whole, never kept), and the id and
// compiled-text hash of each document in order.
func inputHash(presetID string, o publish.Options, src *export.Source, contact Contact, b *built) string {
	h := sha256.New()
	oj, _ := json.Marshal(o)
	// The contact block is in the hash only when it can reach the file: not in a
	// print file, and not when contact is off.
	cj := []byte("null")
	if publish.On(o.Contact) && o.Format != "pdf" && o.Format != "html-paged" {
		cj, _ = json.Marshal(contact)
	}
	p := src.Project
	// Everything that changes a byte of the file: the options, the title page and
	// cover facts, the date the file carries, the contact block, each document's
	// text, title, kind and role in order, and the images that were embedded.
	fmt.Fprintf(h, "scriobh-compile/3\x00%s\x00", publish.EngineVersion)
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00%s\x00", presetID, oj, p.Title, p.Subtitle, b.book.Byline, p.Author, p.Language, p.Kind, p.CoverColor, p.UpdatedAt, cj)
	for i, held := range b.held {
		fmt.Fprintf(h, "%s\x00%s\x00%s\n", held.ID, held.SHA256, b.meta[i])
	}
	keys := make([]string, 0, len(b.book.Images))
	for k := range b.book.Images {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(h, "image\x00%s\x00%s\n", k, b.book.Images[k].Hash)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// countWord is the singular for exactly one and the plural otherwise.
func countWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
