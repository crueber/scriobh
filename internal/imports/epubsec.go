package imports

import (
	"fmt"
	"path"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/text"
)

// EpubInfo is what the preview says about the book itself, and the map from its
// files to the proposed documents (F-371): a screen shows the files and the
// headings as parts, chapters and matter, and the writer edits them with the
// ordinary edits of the preview (rename, kind, skip, merge_into, parent, split) and
// reorder.
type EpubInfo struct {
	Version      string     `json:"version" desc:"The EPUB version the package states (2.0, 3.0, 3.2 ...)."`
	Title        string     `json:"title,omitempty"`
	Subtitle     string     `json:"subtitle,omitempty"`
	Authors      []string   `json:"authors,omitempty"`
	Contributors []string   `json:"contributors,omitempty" desc:"Translators, editors, illustrators: named, not made authors."`
	Language     string     `json:"language,omitempty"`
	Identifier   string     `json:"identifier,omitempty" desc:"The book's own identifier (an ISBN or urn:uuid) as it states it."`
	Publisher    string     `json:"publisher,omitempty"`
	Date         string     `json:"date,omitempty"`
	Rights       string     `json:"rights,omitempty"`
	Description  string     `json:"description,omitempty"`
	Subjects     []string   `json:"subjects,omitempty"`
	Series       string     `json:"series,omitempty"`
	SeriesIndex  string     `json:"series_index,omitempty"`
	Direction    string     `json:"direction" enum:"ltr,rtl"`
	Generator    string     `json:"generator,omitempty" desc:"The program that made the book, when it says."`
	Strategy     string     `json:"strategy" enum:"toc,files,headings" desc:"How the text was cut into sections: by the table of contents, by file, or by heading."`
	Contents     string     `json:"contents,omitempty" desc:"Where the table of contents came from: navigation document, NCX, or none."`
	SHA256       string     `json:"sha256" desc:"Of the file; recorded with the edition."`
	Size         int64      `json:"size"`
	Label        string     `json:"label" desc:"The edition label proposed: the title and year. apply takes edition_label to change it."`
	Footnotes    int        `json:"footnotes" desc:"Footnotes found and written as Markdown footnotes."`
	Cover        *EpubCover `json:"cover,omitempty"`
	Files        []EpubFile `json:"files" desc:"The book's files in reading order, each with the document its text became (key), or why it is not imported. The first 5,000."`
}

// EpubCover says what became of the cover picture.
type EpubCover struct {
	Path   string `json:"path,omitempty"`
	Kept   bool   `json:"kept" desc:"The cover is stored as an attachment of the project (it is not a document)."`
	Reason string `json:"reason,omitempty" desc:"Why it was not kept."`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

// EpubFile is one file of the book's reading order.
type EpubFile struct {
	Path     string `json:"path" desc:"Path inside the EPUB."`
	Key      string `json:"key,omitempty" desc:"The key of the proposed document that holds the text this file starts or continues; empty when the file is not imported."`
	Kind     string `json:"kind,omitempty" enum:"part,chapter,scene,note,front_matter,back_matter"`
	Title    string `json:"title,omitempty" desc:"What the file's first heading says."`
	Words    int    `json:"words,omitempty" desc:"An estimate from the size of its text."`
	Headings int    `json:"headings,omitempty"`
	Notes    int    `json:"notes,omitempty" desc:"Footnotes taken out of the file."`
	Skipped  string `json:"skipped,omitempty" desc:"Why the file is not imported."`

	chapter int
}

const maxListedFiles = 5000

// fileMap lists the book's files with what became of each. The keys are filled
// in once the documents have paths (finalizeEpub).
func fileMap(docs []*epubDoc, secs []*esec, skipped []EpubFile) []EpubFile {
	var out []EpubFile
	owner := map[int]int{} // doc -> the chapter (index of the edition's list) of the first section holding it
	for _, s := range secs {
		if s.skipWhy != "" {
			continue
		}
		for _, d := range s.files {
			if _, ok := owner[d]; !ok {
				owner[d] = s.chapter
			}
		}
	}
	for di, d := range docs {
		f := EpubFile{Path: d.path, Words: d.sc.textBytes / 6, Headings: len(d.sc.heads), Notes: d.notesConsumed, chapter: -1}
		if h, ok := d.sc.firstHeadingIn(0, len(d.src)); ok {
			f.Title = cleanText(h.text, 120)
		}
		switch {
		case d.skip != "":
			f.Skipped = d.skip
		default:
			if i, ok := owner[di]; ok {
				f.chapter = i
			} else {
				f.Skipped = "no text"
			}
		}
		out = append(out, f)
		if len(out) >= maxListedFiles {
			break
		}
	}
	for _, f := range skipped {
		if f.Skipped != "" && !seenFile(out, f.Path) && len(out) < maxListedFiles {
			f.chapter = -1
			out = append(out, f)
		}
	}
	return out
}

func seenFile(fs []EpubFile, p string) bool {
	for _, f := range fs {
		if f.Path == p {
			return true
		}
	}
	return false
}

// convertSection writes a section's text as Markdown into the sink, takes its
// title, removes a heading that only repeats it, and rebases the headings left.
func convertSection(sk *sink, stats *htmlStats, pb *picBox, book *epubBook, docs []*epubDoc, notes map[string]*epubNote, classFmt map[string]cssFmt, s *esec, ours bool, strategy string) error {
	dataTitle := ""
	for _, p := range s.pieces {
		d := docs[p.doc]
		// the raw title this program's own export writes on a section: on the section that opens the piece
		if dataTitle == "" {
			for i := d.sc.firstElemFrom(p.a); i < len(d.sc.elems) && d.sc.elems[i].start < p.b && i < d.sc.firstElemFrom(p.a)+64; i++ {
				if t := d.sc.elems[i].dtitle; t != "" {
					dataTitle = cleanText(t, 300)
					break
				}
			}
		}
		html := d.piece(p.a, p.b)
		dd := d
		stats.pic = func(src, alt string) (seg, bool) {
			rp, _, ok := resolveRef(dd.path, src)
			if !ok {
				return seg{}, false
			}
			f := book.get(rp)
			if f == nil || f.data == nil {
				pb.missing++
				return seg{}, false
			}
			pic, _ := pb.add(f.name, f.data, path.Base(f.name))
			if pic == nil {
				return seg{}, false
			}
			return pic.seg(alt), true
		}
		o := htmlOpts{xhtml: true, classFmt: classFmt}
		o.noteLink = func(href string) (string, string, bool) {
			rp, frag, ok := resolveRef(dd.path, href)
			if !ok || frag == "" {
				return "", "", false
			}
			n := notes[rp+"#"+frag]
			if n == nil {
				return "", "", false
			}
			return rp + "#" + frag, n.html, true
		}
		if ours {
			o.dropClasses = map[string]bool{"theend": true}
		}
		if err := htmlToBlocks(sk, html, o, stats); err != nil {
			return err
		}
	}
	blocks := sk.c.blocks
	// a scene break at either end is nothing
	for len(blocks) > 0 && blocks[0].kind == bScene {
		blocks = blocks[1:]
	}
	for len(blocks) > 0 && blocks[len(blocks)-1].kind == bScene {
		blocks = blocks[:len(blocks)-1]
	}
	headText, headLevel := "", 0
	if len(blocks) > 0 && blocks[0].kind == bHeading && blocks[0].level <= 3 {
		headText, headLevel = blocks[0].text, blocks[0].level
	}
	switch {
	case dataTitle != "":
		s.title, s.titleSrc = dataTitle, "data-title"
	case cleanText(s.cut.label, 300) != "":
		s.title, s.titleSrc = cleanText(s.cut.label, 300), "toc"
	case headText != "":
		s.title, s.titleSrc = cleanText(headText, 300), "heading"
	default:
		s.title, s.titleSrc = s.fileTitle(docs, book.meta.title), "file"
	}
	if headLevel > 0 && (s.titleSrc == "heading" || headingRepeats(headText, s.title) || reNumLabel.MatchString(strings.TrimSpace(headText))) {
		blocks = blocks[1:]
	} else {
		headLevel = max(headLevel, 0)
	}
	base := 0
	switch {
	case ours && headLevel > 0:
		base = headLevel
	case ours:
		base = 1
	case headLevel > 0:
		base = headLevel - 1
	}
	if base > 0 {
		out := make([]mdBlock, len(blocks))
		copy(out, blocks)
		for i := range out {
			if out[i].kind == bHeading {
				out[i].level = max(1, out[i].level-base)
			}
		}
		blocks = out
	}
	sk.c.blocks = blocks
	s.body = sk.take()
	if len(s.body) > MaxFileBytes {
		// A document is at most 2 MiB, and a chapter that is more is left out here, at the preview, with the reason:
		// the folder importer would drop it later, and its edition record would then name text that is not imported.
		s.skipWhy = fmt.Sprintf("larger than the %d MiB a document may be", MaxFileBytes>>20)
		s.body = ""
	}
	s.words = text.WordCount(s.body)
	s.source = docs[s.cut.doc].path
	switch {
	case s.cut.frag != "":
		s.source += "#" + s.cut.frag
	case s.cut.off > 0:
		s.source += "#@" + strconv.Itoa(s.cut.off)
	}
	if strings.TrimSpace(s.body) == "" && s.titleSrc == "file" && docs[s.cut.doc].sc.images == 0 {
		s.skipWhy = "no text"
	}
	return nil
}

// headingRepeats says a heading says what the title says (or the title is part of it).
func headingRepeats(heading, title string) bool {
	h, t := foldTitle(heading), foldTitle(title)
	if h == "" || t == "" {
		return false
	}
	return h == t || strings.Contains(h, t) || strings.Contains(t, h)
}

func foldTitle(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127 && !isSpaceOrPunct(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func isSpaceOrPunct(r rune) bool {
	switch r {
	case '‘', '’', '“', '”', '–', '—', '…', ' ', '•', '·':
		return true
	}
	return false
}

// fileTitle is a title for a section nothing names: a heading inside its own text,
// else what the file calls itself (its <title>, its name).
func (s *esec) fileTitle(docs []*epubDoc, bookTitle string) string {
	if len(s.pieces) > 0 {
		p := s.pieces[0]
		if h, ok := docs[p.doc].sc.firstHeadingIn(p.a, p.b); ok && cleanText(h.text, 300) != "" {
			return cleanText(h.text, 300)
		}
		d := docs[p.doc]
		if t := d.sc.titleTag; t != "" && !strings.EqualFold(t, bookTitle) && !genericTitle(t) {
			return t
		}
		return prettify(strings.NewReplacer("_", " ", "-", " ").Replace(d.stem))
	}
	return docs[s.cut.doc].docTitle(bookTitle)
}
