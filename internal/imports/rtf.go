package imports

import (
	"errors"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// A reader for RTF, the format of a Scrivener text document, written to be
// given anything: it reads the bytes once, left to right, does constant work for
// every byte it reads, and keeps only text.
//
//   - Every control word, symbol and brace costs the bytes it takes: there is no
//     rescanning, so a million control words are a million steps.
//   - Groups nest at most rtfMaxDepth deep; a deeper group is counted and its
//     content dropped. The state of a group is a few bytes.
//   - \bin and the data of a picture or an embedded object are skipped, not
//     buffered, and counted; so is every destination the reader does not know.
//   - The text kept is bounded (rtfMaxText), as are paragraphs, runs and notes: past
//     a bound the file is refused (text) or the formatting is dropped (runs).
//   - \uN and \'hh are decoded: surrogate pairs are joined, a lone surrogate is
//     U+FFFD, \ucN says how many characters follow a \u and are skipped, and the
//     code page is the document's own (\ansicpgN), for the single-byte ones.
//
// What it recognises, from the RTF specification and from what Cocoa and
// Scrivener write (real Scrivener files were not available to check against;
// anything not named here is text, or dropped and counted):
//
//	text, \par, \line, \tab, \~, \_, \-, named characters (\emdash, \lquote ...)
//	\b \i \ul (\ulnone off; every other \ul is an underline) \strike \v \plain \pard
//	\sN with the stylesheet's names, \outlinelevelN                       headings
//	\lsN, \ilvlN, the marker text of {\listtext ...} and {\*\pn ...}      list items
//	{\footnote ...}                                                       a footnote
//	{\field {\*\fldinst HYPERLINK "..."}{\fldrslt ...}}                   a link
//	annotations and comments: a group that holds \annotation, \Scrv_annot*, \Scrv_cmt* or \comment
//	{\pict ...}, {\NeXTGraphic ...}, {\object ...}                        an image marker
//	\trowd \cell \row                                                     a flattened table row
const (
	rtfMaxDepth = 64
	rtfMaxText  = 8 << 20 // bytes of text kept from one file
	rtfMaxParas = 400_000
	rtfMaxRuns  = 600_000 // runs kept from one file; past it formatting is dropped
	rtfMaxNotes = 5_000
	rtfMaxInstr = 4096
)

// rfmt is character formatting.
type rfmt struct{ b, i, u, s bool }

// rseg is a run of text with one formatting.
type rseg struct {
	text  string
	f     rfmt
	priv  bool   // an annotation or comment: a private note
	link  string // an address, or "scrivlnk:<id>" for a link inside the project
	note  int    // 1-based footnote number; text is then empty
	image bool   // an image was here
	pic   string // for an image: the file it names ({\NeXTGraphic name ...}, kept beside the text by Scrivener on a Mac)
}

// rpara is a paragraph.
type rpara struct {
	segs    []rseg
	heading int // 1-6, or 0
	quote   bool
	list    bool
	ordered bool
	level   int
	num     int
	row     bool // part of a table row
}

// rtfStats is what was seen and not kept.
type rtfStats struct {
	images, binBytes      int
	underline, hidden     int
	unknown, deepGroups   int
	annotations, comments int
	footnotes             int
	tables                int
	links, badLinks       int
	docLinks, cmtLinks    int
	badCP, flattened      int
	pictBytes             int
}

type rtfDoc struct {
	paras []rpara
	notes [][]rpara
	st    rtfStats
}

// rtfBuilder collects paragraphs: the body's, or one footnote's.
type rtfBuilder struct {
	paras []rpara
	segs  []rseg
	run   strings.Builder
	rf    rfmt
	rpriv bool
	rlink string
	rok   bool // a run is open
	bytes int
}

func (b *rtfBuilder) flush() {
	if !b.rok {
		return
	}
	if b.run.Len() > 0 {
		b.segs = append(b.segs, rseg{text: b.run.String(), f: b.rf, priv: b.rpriv, link: b.rlink})
	}
	b.run.Reset()
	b.rok = false
}

type rtfGroup struct {
	f       rfmt
	uc      int
	priv    bool
	skip    bool
	dest    uint8
	pstyle  int
	outline int // 1-based outline level, or 0
	ls      bool
	ilvl    int
	row     bool
	hidden  bool
	first   bool // nothing has been read in the group yet
	star    bool // the group opened with \*
	link    string
	cp      int
}

// Destinations a group can be.
const (
	dNone uint8 = iota
	dFldinst
	dListText
	dStylesheet
	dStyleEntry
	dField
	dFootnote
	dPict
	dNeXT // the file name of a {\NeXTGraphic name \width...} picture
)

type rtfReader struct {
	data []byte
	pos  int

	doc  *rtfDoc
	body *rtfBuilder
	note *rtfBuilder // the footnote being read, or nil
	top  *rtfBuilder

	stack []rtfGroup
	over  int // groups opened beyond rtfMaxDepth, not yet closed

	cp      int
	skipN   int // characters still to skip after a \u
	hi      rune
	text    int // bytes of text kept
	runs    int
	listBuf strings.Builder
	// picName collects the file name of a NeXTGraphic picture; picAt is where its image
	// segment is; afterPic eats the not sign a Mac writes after the picture's group.
	picName  strings.Builder
	picAt    *rtfBuilder
	picIdx   int
	afterPic bool
	instr    strings.Builder
	styles   map[int]string
	styleID  int // the style entry being read
	field    *rtfField
	err      error

	inUnder, inHidden bool
}

// rtfField is a field being read: its instruction, and what the result is.
type rtfField struct {
	instr string
}

var (
	errRTFTooMuchText = errors.New("a text document holds more text than the import limit")
	errRTFTooManyRuns = errors.New("a text document changes between plain text, links and private notes too often to read")
)

// parseRTF reads an RTF document. It returns an error only for something that
// is not RTF or holds more text than the limit.
func parseRTF(data []byte) (doc *rtfDoc, err error) {
	defer func() {
		if r := recover(); r != nil {
			doc, err = nil, errors.New("a text document could not be read")
		}
	}()
	data = trimBOM(data)
	i := 0
	for i < len(data) && (data[i] == ' ' || data[i] == '\r' || data[i] == '\n' || data[i] == '\t') {
		i++
	}
	if len(data)-i < 6 || string(data[i:i+5]) != "{\\rtf" {
		return nil, errors.New("not an RTF document")
	}
	r := &rtfReader{data: data, doc: &rtfDoc{}, cp: 1252, styles: map[int]string{}}
	r.body = &rtfBuilder{}
	r.top = r.body
	r.stack = make([]rtfGroup, 1, 16)
	r.stack[0] = rtfGroup{uc: 1, first: true, cp: 1252}
	r.run()
	if r.hi != 0 { // a high half at the very end
		r.emitRune(utf8.RuneError)
	}
	if r.err != nil {
		return nil, r.err
	}
	r.body.endPara(r, rtfGroup{})
	r.doc.paras = r.body.paras
	return r.doc, nil
}

func trimBOM(b []byte) []byte {
	if len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		return b[3:]
	}
	return b
}

func (r *rtfReader) g() *rtfGroup { return &r.stack[len(r.stack)-1] }

func (r *rtfReader) run() {
	data := r.data
	for r.pos < len(data) && r.err == nil {
		c := data[r.pos]
		switch c {
		case '{':
			r.pos++
			if len(r.stack) >= rtfMaxDepth {
				r.over++
				r.doc.st.deepGroups++
				continue
			}
			g := *r.g()
			g.first, g.star = true, false
			switch g.dest {
			case dFldinst, dListText, dStylesheet: // a nested group is still part of it
			default:
				g.dest = dNone
			}
			r.stack = append(r.stack, g)
		case '}':
			r.pos++
			if r.over > 0 {
				r.over--
				continue
			}
			if len(r.stack) > 1 {
				if r.hi != 0 { // a high half that its group ended without
					r.emitRune(utf8.RuneError)
				}
				r.skipN = 0 // what a \u said to skip ends with its group
				r.closeGroup()
				r.stack = r.stack[:len(r.stack)-1]
			}
		case '\\':
			r.control()
		case '\r', '\n':
			r.pos++
		default:
			r.pos++
			if r.over > 0 || r.g().skip {
				r.g().first = false
				continue
			}
			r.g().first = false
			if r.skipN > 0 {
				r.skipN--
				continue
			}
			if c < 0x80 {
				r.emitRune(rune(c))
			} else {
				r.emitRune(r.decode(c))
			}
		}
	}
}

// decode reads one byte of the document's code page.
func (r *rtfReader) decode(b byte) rune {
	if cm := charmapOf(r.cp); cm != nil {
		return cm.DecodeByte(b)
	}
	r.doc.st.badCP++
	return utf8.RuneError
}

func charmapOf(cp int) *charmap.Charmap {
	switch cp {
	case 437:
		return charmap.CodePage437
	case 850:
		return charmap.CodePage850
	case 852:
		return charmap.CodePage852
	case 866:
		return charmap.CodePage866
	case 874:
		return charmap.Windows874
	case 1250:
		return charmap.Windows1250
	case 1251:
		return charmap.Windows1251
	case 0, 1252:
		return charmap.Windows1252
	case 1253:
		return charmap.Windows1253
	case 1254:
		return charmap.Windows1254
	case 1255:
		return charmap.Windows1255
	case 1256:
		return charmap.Windows1256
	case 1257:
		return charmap.Windows1257
	case 1258:
		return charmap.Windows1258
	case 10000:
		return charmap.Macintosh
	}
	return nil
}

// ctl is a control word's name and parameter.
func (r *rtfReader) control() {
	data := r.data
	r.pos++ // the backslash
	if r.pos >= len(data) {
		return
	}
	c := data[r.pos]
	skipping := r.over > 0 || r.g().skip
	switch {
	case c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z':
		start := r.pos
		for r.pos < len(data) && (data[r.pos] >= 'a' && data[r.pos] <= 'z' || data[r.pos] >= 'A' && data[r.pos] <= 'Z') {
			r.pos++
		}
		// Scrivener's own words (Scrv_annot, Scrv_cmt ...) have an underscore, which the
		// specification does not allow in a name; they are read whole so their text is not
		// taken for the text of the paragraph.
		if r.pos-start == 4 && string(data[start:r.pos]) == "Scrv" && r.pos < len(data) && data[r.pos] == '_' {
			for r.pos < len(data) && (data[r.pos] >= 'a' && data[r.pos] <= 'z' || data[r.pos] >= 'A' && data[r.pos] <= 'Z' || data[r.pos] == '_') {
				r.pos++
			}
		}
		name := ""
		if r.pos-start <= 32 {
			name = string(data[start:r.pos])
		}
		param, has := 0, false
		neg := false
		if r.pos < len(data) && data[r.pos] == '-' {
			neg = true
			r.pos++
		}
		ds := r.pos
		for r.pos < len(data) && data[r.pos] >= '0' && data[r.pos] <= '9' {
			if r.pos-ds < 10 {
				param = param*10 + int(data[r.pos]-'0')
			}
			has = true
			r.pos++
		}
		if r.pos-ds > 10 {
			param = 1 << 30
		}
		if neg {
			param = -param
		}
		if !has && neg { // a lone minus belongs to the text that follows
			r.pos--
		}
		if r.pos < len(data) && data[r.pos] == ' ' {
			r.pos++
		}
		if name == "bin" { // always, even in a skipped group: the bytes may hold braces
			n := max(param, 0)
			rest := len(data) - r.pos
			n = min(n, rest)
			r.pos += n
			r.doc.st.binBytes += n
			r.g().first = false
			return
		}
		if r.over > 0 {
			return
		}
		if r.skipN > 0 && !skipping { // a control word in the place of a fallback character counts as one
			r.skipN--
			if name != "u" {
				return
			}
			r.skipN++
		}
		r.word(name, param, has, skipping)
	case c == '\'':
		r.pos++
		if r.pos+2 <= len(data) {
			h, ok1 := hexv(data[r.pos])
			l, ok2 := hexv(data[r.pos+1])
			if ok1 && ok2 {
				r.pos += 2
				r.g().first = false
				if skipping {
					return
				}
				if r.skipN > 0 {
					r.skipN--
					return
				}
				r.emitRune(r.decode(byte(h<<4 | l)))
				return
			}
		}
	case c == '*':
		r.pos++
		if r.over == 0 {
			if g := r.g(); g.first {
				g.star = true
			}
		}
	case c == '\\' || c == '{' || c == '}':
		r.pos++
		r.g().first = false
		if skipping {
			return
		}
		if r.skipN > 0 {
			r.skipN--
			return
		}
		r.emitRune(rune(c))
	case c == '\r' || c == '\n': // a backslash before a line end is \par
		r.pos++
		if !skipping {
			r.par()
		}
	case c == '~':
		r.pos++
		r.g().first = false
		if !skipping {
			r.emitRune(' ')
		}
	case c == '_':
		r.pos++
		r.g().first = false
		if !skipping {
			r.emitRune('-')
		}
	default: // \- \: \| and anything else: no text
		r.pos++
		r.g().first = false
	}
}

func hexv(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10, true
	}
	return 0, false
}

var rtfChars = map[string]rune{
	"emdash": '—', "endash": '–', "bullet": '•', "lquote": '‘', "rquote": '’',
	"ldblquote": '“', "rdblquote": '”', "emspace": ' ', "enspace": ' ', "qmspace": ' ',
	"zwj": '\u200D', "zwnj": '\u200C',
}

// skipDests are the destinations whose content is never text.
var skipDests = map[string]bool{
	"fonttbl": true, "colortbl": true, "expandedcolortbl": true, "info": true, "header": true, "footer": true,
	"headerl": true, "headerr": true, "headerf": true, "footerl": true, "footerr": true, "footerf": true,
	"themedata": true, "colorschememap": true, "colorschememapping": true, "datastore": true, "generator": true,
	"latentstyles": true, "listtable": true, "listoverridetable": true, "revtbl": true, "rsidtbl": true,
	"xmlnstbl": true, "pgdsctbl": true, "template": true, "private": true, "fchars": true, "lchars": true,
	"shpinst": true, "bkmkstart": true, "bkmkend": true, "atnid": true, "atnauthor": true, "atnref": true,
	"atndate": true, "atntime": true, "atnicn": true, "atrfstart": true, "atrfend": true, "txfm": true,
	"userprops": true, "mmathPr": true, "wgrffmtfilter": true, "ftnsep": true, "ftnsepc": true, "aftnsep": true,
	"aftnsepc": true, "ftncn": true, "aftncn": true, "falt": true, "panose": true, "pnseclvl": true,
	"background": true, "result": true, "objclass": true, "objname": true, "fldtype": true,
	"objsect": true, "sp": true, "sn": true, "sv": true,
}

func (r *rtfReader) word(name string, param int, has, skipping bool) {
	g := r.g()
	first, star := g.first, g.star
	g.first = false
	if first || star {
		// A destination: the first word of a group.
		switch {
		case name == "fldinst":
			g.dest = dFldinst
			return
		case name == "datafield":
			g.skip = true
			return
		case name == "fldrslt":
			if r.field != nil && r.field.instr != "" {
				g.link = r.linkOf(r.field.instr)
			}
			return
		case name == "field":
			g.dest = dField
			r.field = &rtfField{}
			r.instr.Reset()
			return
		case name == "footnote":
			r.startNote(g)
			return
		case name == "listtext" || name == "pntext":
			g.dest = dListText
			r.listBuf.Reset()
			return
		case name == "pn" || name == "pnseclvl":
			g.skip = true
			return
		case name == "stylesheet":
			g.dest = dStylesheet
			return
		case name == "shppict":
			// A picture and its stand-in for readers that do not know it: the picture is
			// inside this group and counts there; the stand-in (nonshppict) is skipped.
			return
		case name == "NeXTGraphic" && !skipping && r.over == 0:
			// The picture is a file beside the text; its name is the group's text up to the next control word.
			r.imageHere()
			r.picAt, r.picIdx = r.top, len(r.top.segs)-1
			r.picName.Reset()
			g.dest = dNeXT
			return
		case name == "pict" || name == "NeXTGraphic" || name == "nonshppict" || name == "object" || name == "objdata" || name == "blipuid":
			if (name == "pict" || name == "NeXTGraphic" || name == "object") && !skipping {
				r.imageHere()
			}
			g.skip, g.dest = true, dPict
			return
		case isPrivWord(name):
			g.priv = true
			r.countAnnot(name)
			return
		case skipDests[name]:
			g.skip = true
			return
		case star && g.dest != dStylesheet:
			// An ignorable destination nobody here knows: its content is not text.
			g.skip = true
			if !skipping {
				r.doc.st.unknown++
			}
			return
		}
	}
	if skipping {
		return
	}
	if g.dest == dStylesheet && name == "s" && has {
		// {\s1 ... heading 1;} is a style entry; its name is the text up to the ;
		r.styleID = param
		g.dest = dStyleEntry
		g.skip = false
		r.listBuf.Reset()
		return
	}
	switch name {
	case "par", "sect":
		r.par()
	case "line":
		r.addSeg(rseg{text: "\n"})
	case "tab":
		r.emitRune(' ')
	case "cell":
		r.emitRune(' ')
		r.emitRune('|')
		r.emitRune(' ')
	case "row":
		r.par()
	case "trowd":
		r.doc.st.tables++
		g.row = true
	case "intbl":
		g.row = true
	case "pard":
		g.pstyle, g.outline, g.ls, g.ilvl, g.row = 0, 0, false, 0, false
	case "plain":
		g.f = rfmt{}
	case "b":
		g.f.b = !has || param != 0
	case "i":
		g.f.i = !has || param != 0
	case "strike", "striked":
		g.f.s = !has || param != 0
	case "ul", "uld", "uldb", "ulw", "ulth", "uldash", "ulwave", "ulhwave", "ululdbwave", "ulthdash":
		on := !has || param != 0
		g.f.u = on
	case "ulnone":
		g.f.u = false
	case "v":
		g.hidden = !has || param != 0
	case "s":
		if has {
			g.pstyle = param
		}
	case "outlinelevel":
		if has && param >= 0 && param <= 8 {
			g.outline = param + 1
		}
	case "ls":
		g.ls = true
	case "ilvl":
		g.ilvl = min(max(param, 0), 8)
	case "uc":
		g.uc = min(max(param, 0), 16)
	case "u":
		r.unicode(param)
	case "ansicpg":
		if has {
			r.cp = param
		}
	case "ansi":
		r.cp = 1252
	case "mac":
		r.cp = 10000
	case "pc":
		r.cp = 437
	case "pca":
		r.cp = 850
	default:
		if c, ok := rtfChars[name]; ok {
			r.emitRune(c)
		} else if isPrivWord(name) {
			g.priv = true
			r.countAnnot(name)
		}
	}
}

// isPrivWord says a control word marks an annotation or a comment.
func isPrivWord(name string) bool {
	switch name {
	case "annotation", "comment":
		return true
	}
	return strings.HasPrefix(name, "Scrv_annot") || strings.HasPrefix(name, "Scrv_cmt") || strings.HasPrefix(name, "Scrv_comment")
}

func (r *rtfReader) countAnnot(name string) {
	if strings.Contains(name, "cmt") || strings.Contains(name, "omment") {
		r.doc.st.comments++
	} else {
		r.doc.st.annotations++
	}
}

// unicode handles \uN: a 16-bit signed value, a surrogate half or a character.
func (r *rtfReader) unicode(n int) {
	g := r.g()
	if n < 0 {
		n += 65536
	}
	if n < 0 || n > 0xFFFF {
		n = 0xFFFD
	}
	v := rune(n)
	if v == 0xFFFE || v == 0xFFFF {
		v = utf8.RuneError
	}
	switch {
	case v >= 0xD800 && v <= 0xDBFF:
		if r.hi != 0 { // a high half after a high half
			r.emitRune(utf8.RuneError)
		}
		r.hi = v
	case v >= 0xDC00 && v <= 0xDFFF:
		if r.hi != 0 {
			v = 0x10000 + (r.hi-0xD800)<<10 + (v - 0xDC00)
			r.hi = 0
			r.emitRune(v)
		} else {
			r.emitRune(utf8.RuneError)
		}
	default:
		if r.hi != 0 { // a high half with no low half
			r.emitRune(utf8.RuneError)
			r.hi = 0
		}
		r.emitRune(v)
	}
	r.skipN = g.uc
}

// emitRune adds a character to wherever text goes now.
func (r *rtfReader) emitRune(c rune) {
	g := r.g()
	if r.over > 0 || g.skip {
		return
	}
	switch g.dest {
	case dFldinst:
		if r.instr.Len() < rtfMaxInstr {
			r.instr.WriteRune(c)
		}
		return
	case dListText:
		if r.listBuf.Len() < 64 {
			r.listBuf.WriteRune(c)
		}
		return
	case dStyleEntry:
		if c == ';' {
			r.styles[r.styleID] = strings.ToLower(strings.TrimSpace(r.listBuf.String()))
			g.dest = dStylesheet
			return
		}
		if r.listBuf.Len() < 128 {
			r.listBuf.WriteRune(c)
		}
		return
	case dStylesheet, dPict:
		return
	case dNeXT:
		if r.picName.Len() < 255 && c != '\n' && c != '\r' {
			r.picName.WriteRune(c)
		}
		return
	}
	if r.afterPic {
		r.afterPic = false
		if c == '\u00ac' {
			return
		}
	}
	if g.hidden {
		if !r.inHidden {
			r.doc.st.hidden++
			r.inHidden = true
		}
		return
	}
	r.inHidden = false
	switch {
	case c == 0 || c == 0xFFFC: // the object replacement character: an image or attachment was here
		if c == 0xFFFC {
			r.imageHere()
		}
		return
	case c == '\u2028' || c == '\u2029':
		c = '\n'
	case c < 0x20 && c != '\n' && c != '\t':
		c = ' '
	case c == '\t':
		c = ' '
	case c == '\u00A0':
		c = ' '
	}
	if r.hi != 0 { // a high half that no low half followed
		r.hi = 0
		if c != utf8.RuneError {
			r.emitRune(utf8.RuneError)
		}
	}
	b := r.top
	if r.text += utf8.RuneLen(c); r.text > rtfMaxText {
		r.err = errRTFTooMuchText
		return
	}
	if c == '\n' {
		r.addSeg(rseg{text: "\n"})
		return
	}
	f := g.f
	if f.u {
		if !r.inUnder {
			r.doc.st.underline++
			r.inUnder = true
		}
		f.u = false // Markdown has none: the text is plain, and the count says so
	} else {
		r.inUnder = false
	}
	same := b.rok && b.rpriv == g.priv && b.rlink == g.link
	if same && b.rf == f {
		b.run.WriteRune(c)
		return
	}
	if same && r.runs >= rtfMaxRuns {
		// Too many runs: the formatting goes and the text stays, in the run that is open.
		// A run is never merged across a private note or a link: past the limit those
		// still start a run, up to half as many again, and then the file is refused.
		r.doc.st.flattened++
		b.run.WriteRune(c)
		return
	}
	b.flush()
	if r.runs++; r.runs > rtfMaxRuns+rtfMaxRuns/2 {
		r.err = errRTFTooManyRuns
		return
	}
	b.rok, b.rf, b.rpriv, b.rlink = true, f, g.priv, g.link
	b.run.WriteRune(c)
}

// addSeg adds a non-text segment (a line break, an image, a note mark) to the paragraph.
func (r *rtfReader) addSeg(s rseg) {
	if r.over > 0 || r.g().skip {
		return
	}
	switch r.g().dest {
	case dFldinst, dListText, dStyleEntry, dStylesheet, dPict, dNeXT:
		return
	}
	b := r.top
	b.flush()
	if r.runs++; r.runs > rtfMaxRuns+rtfMaxRuns/2 {
		return
	}
	b.segs = append(b.segs, s)
}

func (r *rtfReader) imageHere() {
	r.doc.st.images++
	if r.over > 0 {
		return
	}
	b := r.top
	b.flush()
	if r.runs++; r.runs > rtfMaxRuns+rtfMaxRuns/2 {
		return
	}
	b.segs = append(b.segs, rseg{image: true})
}

// par ends the current paragraph.
func (r *rtfReader) par() {
	r.top.endPara(r, *r.g())
}

func (b *rtfBuilder) endPara(r *rtfReader, g rtfGroup) {
	b.flush()
	if len(b.segs) == 0 {
		return
	}
	// A paragraph of nothing but white space is nothing.
	blank := true
	for _, s := range b.segs {
		if s.image || s.note != 0 || strings.TrimSpace(strings.ReplaceAll(s.text, "\n", "")) != "" {
			blank = false
			break
		}
	}
	if blank {
		b.segs = b.segs[:0]
		return
	}
	if len(b.paras) >= rtfMaxParas {
		r.err = errRTFTooMuchText
		return
	}
	p := rpara{segs: append([]rseg(nil), b.segs...), row: g.row}
	b.segs = b.segs[:0]
	name := r.styles[g.pstyle]
	level := g.outline
	if level == 0 {
		switch {
		case strings.HasPrefix(name, "heading ") && len(name) == 9 && name[8] >= '1' && name[8] <= '9':
			level = int(name[8] - '0')
		case name == "title":
			level = 1
		case name == "subtitle":
			level = 2
		}
	}
	switch {
	case level > 0:
		p.heading = min(level, 6)
	case strings.Contains(name, "quote") || name == "block text":
		p.quote = true
	case g.ls:
		p.list, p.level = true, g.ilvl
		p.ordered, p.num = markerOf(r.listBuf.String())
	}
	r.listBuf.Reset()
	b.paras = append(b.paras, p)
}

// markerOf reads a list marker's text: a number or letter with a full stop or
// bracket is an ordered item (and its number), anything else is a bullet.
func markerOf(m string) (ordered bool, num int) {
	m = strings.TrimSpace(m)
	if m == "" {
		return false, 0
	}
	n := 0
	i := 0
	for i < len(m) && m[i] >= '0' && m[i] <= '9' && i < 9 {
		n = n*10 + int(m[i]-'0')
		i++
	}
	if i > 0 && i < len(m) && (m[i] == '.' || m[i] == ')') {
		return true, n
	}
	if len(m) >= 2 && len(m) <= 4 && (m[len(m)-1] == '.' || m[len(m)-1] == ')') {
		return true, 1
	}
	return false, 0
}

// startNote begins a footnote: its text goes to a builder of its own, and the
// body gets a reference.
func (r *rtfReader) startNote(g *rtfGroup) {
	if r.note != nil || len(r.doc.notes) >= rtfMaxNotes || g.skip {
		g.skip = true // a note inside a note, or one too many
		return
	}
	g.dest = dFootnote
	r.doc.st.footnotes++
	r.top.flush()
	r.note = &rtfBuilder{}
	r.top = r.note
}

// closeGroup finishes what the group being closed was.
func (r *rtfReader) closeGroup() {
	g := r.g()
	switch {
	case g.dest == dNeXT:
		// The name ends the picture's group; it names a file of the same item.
		if b := r.picAt; b != nil && r.picIdx >= 0 && r.picIdx < len(b.segs) && b.segs[r.picIdx].image {
			b.segs[r.picIdx].pic = strings.TrimSpace(r.picName.String())
		}
		r.picAt, r.afterPic = nil, true
	case g.dest == dFootnote && r.note != nil:
		nb := r.note
		nb.endPara(r, *g)
		r.top, r.note = r.body, nil
		r.doc.notes = append(r.doc.notes, nb.paras)
		r.body.flush()
		r.body.segs = append(r.body.segs, rseg{note: len(r.doc.notes)})
	case g.dest == dField:
		r.field = nil
	case g.dest == dFldinst && r.field != nil:
		// The instruction belongs to the field around this group; the result group that
		// follows it reads it.
		r.field.instr = r.instr.String()
	}
}

// linkOf makes a link target out of a field instruction, or "" when the
// instruction is not a hyperlink to somewhere safe.
func (r *rtfReader) linkOf(instr string) string {
	f := strings.TrimSpace(instr)
	if !strings.HasPrefix(strings.ToUpper(f), "HYPERLINK") {
		return ""
	}
	i := strings.IndexByte(f, '"')
	if i < 0 {
		return ""
	}
	j := strings.IndexByte(f[i+1:], '"')
	if j < 0 {
		return ""
	}
	u := f[i+1 : i+1+j]
	lu := strings.ToLower(u)
	switch {
	case strings.HasPrefix(lu, "scrivlnk://"):
		id := u[len("scrivlnk://"):]
		if k := strings.IndexAny(id, "?#/"); k >= 0 {
			id = id[:k]
		}
		if len(id) > 64 || id == "" {
			r.doc.st.badLinks++
			return ""
		}
		r.doc.st.docLinks++
		return "scrivlnk:" + id
	case strings.HasPrefix(lu, "scrivcmt://"):
		r.doc.st.cmtLinks++
		return ""
	}
	if t, ok := safeTarget(u); ok {
		r.doc.st.links++
		return t
	}
	r.doc.st.badLinks++
	return ""
}
