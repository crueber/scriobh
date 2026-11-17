package publish

import (
	"archive/zip"
	"bytes"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/text"
)

// SectionKind says what a section of the book is.
type SectionKind uint8

// The section kinds, in the order a book has them.
const (
	SecFront   SectionKind = iota // front matter: no number
	SecPart                       // a part heading page
	SecChapter                    // a chapter
	SecBack                       // back matter
	SecNotes                      // the notes appendix's heading, with the notes as its body
)

// Section is one heading and what follows it, up to the next.
type Section struct {
	Kind  SectionKind
	Title string // as written; may be empty
	// Number is the chapter's number (1, 2, ...); 0 when the section has none.
	Number int
	// Level is how deep the heading sits: 1 for a part, or for a chapter in a
	// book without parts; 2 for a chapter in a part.
	Level int
	Nodes []text.Node
	Notes []text.Footnote
}

// Image is a picture a document links to that the caller has resolved and
// vouched for. Key in Book.Images is the destination exactly as the text
// writes it (/files/att_..., or a data: URI).
type Image struct {
	Data   []byte
	Mime   string // image/png, image/jpeg, image/gif, image/webp
	Ext    string // png, jpg, gif, webp
	Width  int    // pixels; 0 when unknown (the image is then left out)
	Height int
	// Hash is the hex SHA-256 of the picture as it was stored (before it was
	// cleaned of metadata and turned upright): it names the file in an EPUB and
	// is what a preview can know without reading the bytes.
	Hash string
	// Rotated says the pixels were turned upright because the file carried an
	// EXIF orientation, and written again.
	Rotated bool
	// Orient is the EXIF orientation of a JPEG (1 to 8); 0 means read it from
	// Data when there is Data, else 1. A PDF shows the picture upright.
	Orient int
}

// orientation is the orientation a PDF shows the image in.
func (img Image) orientation() int {
	if img.Orient >= 1 && img.Orient <= 8 {
		return img.Orient
	}
	if img.Ext == "jpg" && len(img.Data) > 0 {
		return JPEGOrientation(img.Data)
	}
	return 1
}

// upright is the size of the picture as it is shown: width and height swap for
// the orientations that turn it a quarter.
func (img Image) upright() (w, h int) {
	if img.orientation() >= 5 {
		return img.Height, img.Width
	}
	return img.Width, img.Height
}

// Contact is the author's block on a manuscript title page. It is personal
// data: it is written into the file and nowhere else.
type Contact struct {
	Name    string
	Address []string // lines
	Email   string
	Phone   string
}

// Book is a compiled manuscript.
type Book struct {
	Title    string
	Subtitle string
	Byline   string // the name on the title page
	Surname  string // for the running header
	Language string
	// Identifier is a stable identifier of the project (a UUID string).
	Identifier string
	// Modified is when the project last changed; every date in the file is this one.
	Modified time.Time
	Contact  Contact
	// Words is the compiled text's word count.
	Words int
	// InputHash identifies the content the book was compiled from; a PDF's
	// document id is derived from it. Empty is allowed.
	InputHash string
	// CoverSVG is the generated cover, for an EPUB.
	CoverSVG []byte
	// CoverImage is the project's own cover, a JPEG made for screens, for an EPUB. It takes the place of CoverSVG when
	// there is one; CoverAlt is its description for screen readers.
	CoverImage []byte
	CoverAlt   string
	Sections   []Section
	Images     map[string]Image
	// Warnings collects what a writer had to leave out (an image it could not
	// size, a table nested too deep); the compile pipeline reports them.
	Warnings []string
}

func (b *Book) warn(msg string) {
	for _, w := range b.Warnings {
		if w == msg {
			return
		}
	}
	b.Warnings = append(b.Warnings, msg)
}

// IsRTL reports whether a language tag is written right to left: by its
// language (Arabic, Persian, Hebrew, Urdu, Yiddish, Pashto, Sindhi, Uyghur,
// Dhivehi, Sorani Kurdish) or by an explicit script subtag (ku-Arab is
// right to left, ku and ku-Latn, Kurmanji in the Latin script, are not).
func IsRTL(lang string) bool {
	parts := strings.FieldsFunc(strings.ToLower(lang), func(r rune) bool { return r == '-' || r == '_' })
	if len(parts) == 0 {
		return false
	}
	for _, p := range parts[1:] {
		if len(p) == 4 { // a script subtag
			switch p {
			case "arab", "hebr", "thaa", "syrc", "adlm", "nkoo", "rohg", "mand", "samr":
				return true
			default:
				return false
			}
		}
	}
	switch parts[0] {
	case "ar", "he", "iw", "fa", "ur", "yi", "ps", "sd", "ug", "dv", "ckb":
		return true
	}
	return false
}

// ---- headings ---------------------------------------------------------------------------

// HeadingText is the text of a section's heading as the options ask for it:
// 'Chapter 3: Title', 'Chapter 3', the title, or nothing (for a chapter). A
// part, front matter and back matter show their titles.
func HeadingText(s Section, o Options) string {
	title := CleanLine(s.Title)
	if s.Kind != SecChapter {
		return title
	}
	label := o.ChapterLabel
	if label == "" {
		label = "Chapter"
	}
	num := ""
	if s.Number > 0 {
		num = label + " " + itoa(s.Number)
	}
	switch o.ChapterHeading {
	case "none":
		return ""
	case "number":
		if num != "" {
			return num
		}
		return title
	case "title":
		if title != "" {
			return title
		}
		return num
	default: // number-title
		switch {
		case num != "" && strings.EqualFold(title, num):
			return num // a title that only says "Chapter 1" is not repeated after it
		case num != "" && title != "":
			return num + ": " + title
		case num != "":
			return num
		}
		return title
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	neg := n < 0
	if neg {
		n = -n
	}
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}

// Thousands writes n with thousands separators: 80,000.
func Thousands(n int) string {
	s := itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// RoundedWords rounds a word count to the nearest hundred (to the nearest ten
// below a thousand), the way a manuscript title page states it.
func RoundedWords(n int) int {
	switch {
	case n < 100:
		return n
	case n < 1000:
		return (n + 5) / 10 * 10
	}
	return (n + 50) / 100 * 100
}

// SceneBreakText is what a scene break says.
func SceneBreakText(o Options) string {
	switch o.SceneBreak {
	case "stars":
		return "* * *"
	case "ornament":
		if t := strings.TrimSpace(o.Ornament); t != "" {
			return t
		}
		return "❦" // a floral heart, in a font that carries it
	}
	return "#"
}

// RunningTitle is the short title for a running header: HeaderTitle, else the
// title, cut at a word boundary when long.
func RunningTitle(b *Book, o Options) string {
	t := CleanLine(o.HeaderTitle)
	if t == "" {
		t = CleanLine(b.Title)
	}
	const max = 40
	if utf8.RuneCountInString(t) > max {
		r := []rune(t)
		cut := string(r[:max])
		if i := strings.LastIndexByte(cut, ' '); i > 10 {
			cut = cut[:i]
		}
		t = strings.TrimRight(cut, " ,;:-") + "…"
	}
	return t
}

// ---- zip ----------------------------------------------------------------------------------

// zipEntry is a file of an archive. Store keeps it uncompressed.
type zipEntry struct {
	name  string
	data  []byte
	store bool
}

// writeZip writes entries in the order given, with a fixed time, no extra
// fields and no owner. Same entries, same bytes.
func writeZip(entries []zipEntry, modified time.Time) ([]byte, error) {
	date, tm := dosTime(modified)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, e := range entries {
		method := zip.Deflate
		if e.store {
			method = zip.Store
		}
		h := &zip.FileHeader{Name: e.name, Method: method, ModifiedDate: date, ModifiedTime: tm}
		h.SetMode(0o644)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(e.data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// dosTime is the MS-DOS date and time of t (UTC), clamped to 1980 or later.
func dosTime(t time.Time) (date, tm uint16) {
	if t.IsZero() || t.Year() < 1980 {
		t = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	t = t.UTC()
	return uint16(t.Year()-1980)<<9 | uint16(t.Month())<<5 | uint16(t.Day()),
		uint16(t.Hour())<<11 | uint16(t.Minute())<<5 | uint16(t.Second()/2)
}

// File is a named file of a bundle.
type File struct {
	Name string
	Data []byte
}

// ZipStored writes files as a zip: sorted by name, stored without compression,
// with the fixed time, no extra fields and no owner.
func ZipStored(files []File, modified time.Time) ([]byte, error) {
	sorted := append([]File(nil), files...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })
	entries := make([]zipEntry, len(sorted))
	for i, f := range sorted {
		entries[i] = zipEntry{name: f.Name, data: f.Data, store: true}
	}
	return writeZip(entries, modified)
}
