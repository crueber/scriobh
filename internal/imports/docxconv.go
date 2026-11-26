package imports

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/review"
)

// DOCX import: a .docx becomes the folder of Markdown files the generic importer
// reads, so the preview, the edits, the token, the caps and the provenance are
// the folder import's own.
//
// What a Word document becomes:
//
//	Heading 1 (or any style with outline level 1)    a chapter: a file whose first line is the
//	                                                 heading. With headings=parts, Heading 1 is a
//	                                                 part (a folder) and Heading 2 a chapter.
//	deeper headings                                  Markdown headings inside the chapter (so
//	                                                 split_scenes=h2 turns Heading 2 into scenes)
//	Title, Subtitle, the title page's own styles     the project's title and subtitle, never text
//	text before the first heading                    a front matter document
//	paragraphs                                       Markdown paragraphs; bold, italic and
//	                                                 strikethrough kept; underline dropped (it is
//	                                                 reported); links kept for http, https, mailto
//	Scene Break style, a lone #, * * *, ---, ...     the canonical scene break (* * *); two or more
//	                                                 empty paragraphs between paragraphs do too
//	lists, quotes, code styles, tables               Markdown lists, block quotes, code blocks,
//	                                                 pipe tables
//	footnotes                                        Markdown footnotes at the end of the chapter
//	tracked changes                                  the final view: insertions kept, deletions
//	                                                 dropped; the number is reported
//	comments, images, text boxes, embedded objects,  not imported; the number of each is reported
//	headers and footers
//	macros                                           never run, never read; reported
//
// Text is escaped so nothing a Word document says can become Markdown or a
// Scríobh construct by accident: a "%%" is not a private note, a "[[" is not a
// wiki link, "<scriobh-ghost>" is not a ghost, a line that starts ":::" is not
// a directive.

// What a Word file's tracked changes become (docxtrack.go).
const (
	DocxSuggest = "suggest"
	DocxAccept  = "accept"
	DocxReject  = "reject"
)

const (
	viewFinal = iota
	viewOriginal
)

// What Heading 1 becomes.
const (
	DocxChapters = "chapters"
	DocxParts    = "parts"
)

var (
	reHeadingName = regexp.MustCompile(`^heading ([1-9])$`)
	reChapterTag  = regexp.MustCompile(`^Chapter \d+: (.+)$`)
	reSceneText   = regexp.MustCompile(`^(#|\*\s*\*\s*\*|\*{3,}|-{3,}|_{3,}|~{3,}|•\s*•\s*•|⁂|❦|❧|[—–-]\s*[—–-]\s*[—–-]|—)$`)
)

type mdKind uint8

const (
	bPara mdKind = iota
	bHeading
	bScene
	bQuote
	bCode
	bList
	bTable
)

type mdBlock struct {
	kind    mdKind
	level   int
	text    string
	ordered bool
	listLvl int
	listNum string
	start   int
	notes   []string
	revs    []blockRev
	cms     []blockComment
	sig     *blockSig // a paragraph or heading: what the chapter detector reads of it (docxbreaks.go)
}

// seg is a piece of a paragraph's text with its formatting.
type seg struct {
	text    string
	b, i, s bool
	code    bool
	link    string
	note    string
	brk     bool
	// priv is text that becomes a %%private note%% (converters other than DOCX
	// make some). pctLead and pctTrail say a private note touches the start or the
	// end of this text, so a percent sign there is escaped: the delimiters of a
	// note are then exactly its own, whatever the words around it say.
	priv, pctLead, pctTrail bool
	// wiki is a finished [[wiki link]] the converter made out of a title it
	// checked itself; it is written as it stands.
	wiki string
}

type fieldState struct {
	instr    strings.Builder
	inResult bool
}

type docxConv struct {
	styles styleTable
	nums   *numTable
	rels   map[string]string

	blocks   []mdBlock
	title    string
	subtitle string
	headings int // headings seen so far
	emptyRun int
	lastKind mdKind
	haveLast bool

	fields    []*fieldState
	fieldLink string

	noteText     map[string][]string // footnote id -> its paragraphs, rendered
	noteSeen     []string            // referenced by the paragraph being read
	listCounters map[string]int

	underline, tracked, images, badLinks, tables, gapScenes, paragraphs, drawings int

	// For the chapter detector (docxbreaks.go): a page or section break seen in an empty paragraph belongs to the next one.
	pendBrk, pendSect bool
	sigSlab           []blockSig
	numCount          map[string][]int // Word's automatic numbers, by list, to say what a numbered heading would print

	inNote   bool // reading a footnote: it has no notes of its own
	total    int  // bytes of Markdown made so far
	noteSize int  // bytes of footnote text kept
	overflow bool // more text than the import limit

	// Tracked changes and comments (docxtrack.go).
	mode      string // docxSuggest, docxAccept or docxReject
	baseView  int    // the view of text outside a paragraph that can carry suggestions
	view      int
	second    bool // reading the final view of a paragraph again, only for its replacements
	revMarks  bool // put revision markers in the text
	cmMarks   bool // put comment markers in the text
	inIns     int
	inDel     int
	revBase   int // clusters get global ids: this paragraph's first
	paraRevN  int
	nextRev   int
	clusters  map[int]*revCluster
	overflowC map[int]bool // clusters over the import's cap: taken as accepted
	clusterN  int
	wcomments map[string]*wordComment
	tn        trackedNotes
	fmtChange int
	ts        *trackedSet
	cmSeen    map[string]bool
}

// docxToFiles converts a .docx into a folder of Markdown files, with the
// project facts the file states and what was left out.
func docxToFiles(data []byte, name, headings, changes string, cut docxCutOpts) (files []File, pre *prepass, err error) {
	// The HTTP binding recovers from a panic; the command line does not. A
	// document that breaks the reader is refused in both.
	defer func() {
		if r := recover(); r != nil {
			files, pre, err = nil, nil, errors.New("the document could not be read")
		}
	}()
	d, err := readDocx(data)
	if err != nil {
		return nil, nil, err
	}
	c, comments, styleLang, err := readConv(d, changes)
	if err != nil {
		return nil, nil, err
	}
	pi := c.prepareCut(d.core, styleLang, headings, name, &cut)
	files, notes, info, err := c.cutDocx(cut, pi)
	if err != nil {
		return nil, nil, err
	}
	if cut.look != nil { // a look at one chapter has what it asked for
		return nil, nil, nil
	}
	parts := info.intro
	c.finishTracked(comments)
	sum := sha256.Sum256(data)
	docxMeta := map[string]any{"headings": headings, "paragraphs": c.paragraphs, "comments": comments, "tracked_changes": c.tracked, "images": max(c.images, d.media), "tables": c.tables,
		"split": info.provenance(notes)}
	for k, v := range c.trackedMeta() {
		docxMeta[k] = v
	}
	pre = &prepass{layout: "docx", srcKind: "docx", srcHash: hex.EncodeToString(sum[:]), project: pi, meta: map[string]any{"docx": docxMeta}, tracked: c.ts, notes: notes, breaks: info}
	if c.ts.total > 0 || c.ts.nComments > 0 {
		c.ts.seal()
		pre.tokenMix = c.ts.hash
	}
	w := func(n int, format string, args ...any) {
		if n > 0 {
			pre.warnings = append(pre.warnings, fmt.Sprintf(format, args...))
		}
	}
	for _, line := range c.trackedWarnings(comments) {
		pre.warnings = append(pre.warnings, line)
	}
	w(max(c.images, d.media), "%d image(s) were not imported; each is marked in the text as [image].", max(c.images, d.media))
	w(c.drawings, "%d text box(es) or drawing(s) were left out.", c.drawings)
	w(d.embeddings, "%d embedded object(s) were left out.", d.embeddings)
	w(d.macros, "The file contains macros. They were never run and are not imported.")
	w(c.underline, "%d underlined passage(s) became plain text: Markdown has no underline. Underline in a manuscript often means italics; check them.", c.underline)
	w(c.badLinks, "%d link(s) to something other than a web or e-mail address kept their text only.", c.badLinks)
	w(c.gapScenes, "%d gap(s) of two or more empty paragraphs were read as scene breaks.", c.gapScenes)
	w(c.tables, "%d table(s) became Markdown tables.", c.tables)
	w(d.headersFooters, "Headers and footers (%d) are ignored.", d.headersFooters)
	if parts {
		pre.warnings = append(pre.warnings, "Text under a part heading, before its first chapter, became a chapter of its own.")
	}
	if info.kept > 0 {
		pre.warnings = append(pre.warnings, fmt.Sprintf("%d chapter title(s) carry a footnote or a comment, so the title line is also kept as the first line of the chapter and nothing is lost with it.", info.kept))
	}
	if info.Hint == "pages" {
		pre.warnings = append(pre.warnings, "These chapters look like pages, not chapters: there are very many of them, or most are short. Choose another way to cut, or join them.")
	}
	return files, pre, nil
}

// readConv reads the parts of a Word file into a converter that holds its blocks.
func readConv(d *docxFile, changes string) (c *docxConv, comments int, styleLang string, err error) {
	var styles styleTable
	styles, styleLang = parseStyles(d.styles)
	c = newDocxConv()
	c.styles, c.nums, c.rels = styles, parseNumbering(d.numbering), parseRels(d.rels)
	c.mode = changes
	if c.mode == "" {
		c.mode = DocxSuggest
	}
	c.baseView = viewFinal
	if c.mode == DocxReject {
		c.baseView = viewOriginal
	}
	c.view = c.baseView
	c.ts = newTracked(c.mode)
	c.wcomments = readWordComments(d.comments)
	c.readNotes(d.footnotes)
	comments = max(c.countComments(d.comments), len(c.wcomments))
	if err := c.readBody(d.document); err != nil {
		return nil, 0, "", err
	}
	return c, comments, styleLang, nil
}

// prepareCut makes the project's facts from what the file says of itself and sets the options of the cut that depend on them.
func (c *docxConv) prepareCut(core []byte, styleLang, headings, name string, cut *docxCutOpts) *ProjectInfo {
	title, creator, coreLang := parseCore(core)
	pi := &ProjectInfo{Title: noMarks(firstText(c.title, title)), Subtitle: noMarks(c.subtitle), Author: noMarks(creator), Language: firstText(coreLang, styleLang)}
	if pi.Title != "" {
		pi.Title, _ = cleanLine(pi.Title, 200)
	}
	pi.Subtitle, _ = cleanLine(pi.Subtitle, 200)
	pi.Author, _ = cleanLine(pi.Author, 200)
	if l := pi.Language; l == "" || len(l) > 16 || strings.ContainsAny(l, " <>\"'") {
		pi.Language = ""
	}
	cut.headings, cut.name = headings, name
	cut.projectTitle, cut.author = pi.Title, pi.Author
	if c.title != "" || parseCoreTitle(core) != "" {
		cut.projectTitle = noMarks(firstText(c.title, parseCoreTitle(core)))
	}
	return pi
}

// newDocxConv makes a converter with its tables empty. Its mode is the default
// until the caller sets it.
func newDocxConv() *docxConv {
	c := &docxConv{styles: styleTable{}, nums: &numTable{}, noteText: map[string][]string{}, listCounters: map[string]int{}, mode: DocxSuggest}
	c.clusters, c.overflowC, c.cmSeen = map[int]*revCluster{}, map[int]bool{}, map[string]bool{}
	c.wcomments = map[string]*wordComment{}
	c.ts = newTracked(c.mode)
	c.tn = c.ts.notes
	return c
}

func firstText(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

// ---- footnotes and comments --------------------------------------------------------------------

func (c *docxConv) countComments(b []byte) int {
	if b == nil {
		return 0
	}
	n := 0
	dec := newDecoder(b)
	for {
		tok, err := dec.Token()
		if err != nil {
			return n
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Space == nsWord && se.Name.Local == "comment" {
			n++
		}
	}
}

// readNotes renders each footnote's paragraphs.
func (c *docxConv) readNotes(b []byte) {
	if b == nil {
		return
	}
	dec := newDecoder(b)
	for {
		tok, err := dec.Token()
		if err != nil {
			return
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Space != nsWord || se.Name.Local != "footnote" {
			continue
		}
		var rawID, typ string
		for _, a := range se.Attr {
			switch a.Name.Local {
			case "id":
				rawID = a.Value
			case "type":
				typ = a.Value
			}
		}
		budget := maxParaNodes
		n, err := readSubtree(dec, se, 0, &budget)
		if err != nil {
			return
		}
		id, ok := noteKey(rawID) // an id that is not a number is not a note: it is never written into the Markdown
		if !ok || (typ != "" && typ != "normal") {
			continue
		}
		var paras []string
		c.inNote = true
		for _, p := range n.kids {
			if p.name != "p" {
				continue
			}
			if c.mode == DocxSuggest {
				c.tn.Notes += c.scanRev(p).revs
			}
			segs := c.collectPara(p)
			if t := strings.TrimSpace(c.render(segs)); t != "" {
				paras = append(paras, escLine(strings.ReplaceAll(t, "\n", " ")))
			}
		}
		c.inNote = false
		c.noteSeen = nil
		for _, p := range paras {
			c.noteSize += len(p)
		}
		if len(paras) > 0 {
			c.noteText[id] = paras
		}
		if len(c.noteText) > maxNotes || c.noteSize > MaxTotalBytes {
			return
		}
	}
}

// maxNotes bounds the footnotes read from a document.
const maxNotes = 20_000

// noteKey is the canonical form of a footnote id: a plain number. Anything else
// is not a note, so no text from a document's ids can reach the Markdown.
func noteKey(raw string) (string, bool) {
	if raw == "" || len(raw) > 9 {
		return "", false
	}
	for _, r := range raw {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 1 {
		return "", false
	}
	return strconv.Itoa(n), true
}

// ---- the body --------------------------------------------------------------------------------------

func (c *docxConv) readBody(b []byte) error {
	dec := newDecoder(b)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return fmt.Errorf("word/document.xml is not valid: %v", err)
		}
		switch t := tok.(type) {
		case xml.Directive:
			return errors.New("word/document.xml has a DOCTYPE, which is not allowed")
		case xml.StartElement:
			if t.Name.Space != nsWord {
				continue
			}
			switch t.Name.Local {
			case "p":
				budget := maxParaNodes
				n, err := readSubtree(dec, t, 0, &budget)
				if err != nil {
					return err
				}
				c.paragraphs++
				if c.paragraphs > maxParagraphs {
					return errors.New("the document has more paragraphs than the import limit")
				}
				c.paragraph(n)
				if c.overflow {
					return fmt.Errorf("the document holds more text than the %d MB import limit", MaxTotalBytes>>20)
				}
			case "tbl":
				budget := maxParaNodes * 5
				n, err := readSubtree(dec, t, 0, &budget)
				if err != nil {
					return err
				}
				c.table(n)
			case "del", "moveFrom":
				c.tracked++
				if err := dec.Skip(); err != nil {
					return err
				}
			case "ins", "moveTo":
				c.tracked++
			}
		}
	}
}

// ---- inline ------------------------------------------------------------------------------------------

type fmtState struct {
	b, i, s bool
	code    bool
	link    string
}

func isOn(n *dnode) bool {
	if n == nil {
		return false
	}
	switch strings.ToLower(n.val()) {
	case "0", "false", "off", "none":
		return false
	}
	return true
}

// collectPara gathers a paragraph's text with its formatting.
func (c *docxConv) collectPara(p *dnode) []seg {
	var out []seg
	c.collect(p, fmtState{}, &out)
	return fixMarkSegs(out)
}

func (c *docxConv) collect(n *dnode, st fmtState, out *[]seg) {
	open := -1 // the revision cluster open at this level
	for i := 0; i < len(n.kids); i++ {
		k := n.kids[i]
		if k.name == "" {
			if k.other == "AlternateContent" {
				c.drawings++
			}
			continue
		}
		switch k.name {
		case "r":
			c.run(k, st, out)
		case "hyperlink":
			s := st
			if id := k.attr("r:id"); id != "" {
				target := c.rels[id]
				if t, ok := safeTarget(target); ok {
					s.link = t
				} else if target != "" {
					c.badLinks++
				}
			}
			c.collect(k, s, out)
		case "ins", "moveTo", "del", "moveFrom":
			c.revElement(n, i, st, out, &open)
		case "commentRangeStart", "commentRangeEnd":
			if c.cmMarks {
				if id, ok := noteKeyZero(k.attr("id")); ok {
					n, _ := strconv.Atoi(id)
					*out = append(*out, seg{text: mark(kComment, k.name == "commentRangeEnd", n)})
				}
			}
		case "smartTag", "fldSimple", "sdt", "sdtContent", "customXml", "bdo", "dir":
			c.collect(k, st, out)
		}
	}
}

func isRevName(n string) bool { return n == "ins" || n == "del" || n == "moveTo" || n == "moveFrom" }

// revElement reads a tracked insertion or deletion (or a move's two halves).
// Which text it shows depends on the view: the final view keeps what was
// inserted and drops what was deleted, the original view the reverse. With
// markers on, the start and the end of a run of adjacent changes by one author
// are put in the text, whatever the view shows, so the change can be found
// again; changes beside each other are one.
func (c *docxConv) revElement(parent *dnode, i int, st fmtState, out *[]seg, open *int) {
	k := parent.kids[i]
	isIns := k.name == "ins" || k.name == "moveTo"
	nested := c.inIns > 0 || c.inDel > 0
	if !nested {
		c.tracked++
		if k.name == "moveTo" || k.name == "moveFrom" {
			c.tn.Moves++
		}
	}
	eff := c.view
	marked := c.revMarks && !nested
	var author string
	if marked {
		author, _ = cleanLine(noMarks(k.attr("author")), review.MaxLabel)
		if *open < 0 {
			id := c.revBase + c.paraRevN
			c.paraRevN++
			if !c.second {
				if c.clusterN >= maxSuggestionsPerImport {
					c.overflowC[id] = true
					c.tn.OverCap++
				} else {
					c.clusterN++
					c.clusters[id] = &revCluster{author: author, date: wordTime(k.attr("date"))}
					if c.tn.Authors != nil {
						c.tn.Authors[authorOrWord(author)]++
					}
				}
			}
			*open = id
			if !c.overflowC[id] {
				*out = append(*out, seg{text: mark(kRev, false, id)})
			}
		}
		if c.overflowC[*open] {
			eff = viewFinal
		}
	}
	save := c.view
	c.view = eff
	if isIns {
		c.inIns++
	} else {
		c.inDel++
	}
	if (eff == viewFinal && c.inDel == 0) || (eff == viewOriginal && c.inIns == 0) {
		c.collect(k, st, out)
	}
	if isIns {
		c.inIns--
	} else {
		c.inDel--
	}
	c.view = save
	if marked {
		if !sameAuthorNext(parent, i, author) {
			if !c.overflowC[*open] {
				*out = append(*out, seg{text: mark(kRev, true, *open)})
			}
			*open = -1
		}
	}
}

func authorOrWord(a string) string {
	if a == "" {
		return "Word"
	}
	return a
}

// sameAuthorNext says the next element that matters after kid i is another
// tracked change by the same author.
func sameAuthorNext(parent *dnode, i int, author string) bool {
	for j := i + 1; j < len(parent.kids); j++ {
		k := parent.kids[j]
		switch k.name {
		case "bookmarkStart", "bookmarkEnd", "proofErr", "permStart", "permEnd":
			continue
		}
		if !isRevName(k.name) {
			return false
		}
		a, _ := cleanLine(noMarks(k.attr("author")), review.MaxLabel)
		return a == author
	}
	return false
}

func (c *docxConv) run(r *dnode, st fmtState, out *[]seg) {
	s := st
	if rp := r.child("rPr"); rp != nil {
		if isOn(rp.child("b")) {
			s.b = true
		}
		if isOn(rp.child("i")) {
			s.i = true
		}
		if isOn(rp.child("strike")) || isOn(rp.child("dstrike")) {
			s.s = true
		}
	}
	if rp := r.child("rPr"); rp != nil {
		if rs := rp.child("rStyle"); rs != nil {
			name, _, _, _ := c.styles.resolve(rs.val())
			l := strings.ToLower(name)
			if l == "code char" || l == "verbatim char" || l == "html code" || l == "source text" || l == "code" {
				s.code = true
			}
		}
	}
	underlined := false
	if rp := r.child("rPr"); rp != nil {
		if u := rp.child("u"); u != nil && u.val() != "none" && u.val() != "" && s.link == "" && c.fieldLink == "" {
			underlined = true
		}
	}
	emitted := false
	emit := func(sg seg) {
		sg.b, sg.i, sg.s, sg.code = s.b, s.i, s.s, s.code
		sg.link = s.link
		if sg.link == "" {
			sg.link = c.fieldLink
		}
		*out = append(*out, sg)
		emitted = true
	}
	for _, k := range r.kids {
		switch k.name {
		case "t":
			if f := c.topField(); f != nil && !f.inResult {
				continue // an instruction, not text
			}
			emit(seg{text: noMarks(k.text)})
		case "tab":
			emit(seg{text: " "})
		case "noBreakHyphen":
			emit(seg{text: "-"})
		case "br":
			if k.attr("type") == "" || k.attr("type") == "textWrapping" {
				emit(seg{brk: true})
			}
		case "footnoteReference":
			if id, ok := noteKey(k.attr("id")); ok && !c.inNote && c.inIns+c.inDel == 0 {
				emit(seg{note: id})
			}
		case "drawing", "pict", "object":
			c.images++
			emit(seg{text: "\x00image"})
		case "fldChar":
			switch k.attr("fldCharType") {
			case "begin":
				c.fields = append(c.fields, &fieldState{})
			case "separate":
				if f := c.topField(); f != nil {
					f.inResult = true
					if link := hyperlinkOf(f.instr.String()); link != "" {
						c.fieldLink = link
					}
				}
			case "end":
				if len(c.fields) > 0 {
					c.fields = c.fields[:len(c.fields)-1]
				}
				c.fieldLink = ""
			}
		case "instrText":
			if f := c.topField(); f != nil && !f.inResult {
				f.instr.WriteString(k.text)
			}
		case "delText":
			// the text of a deletion: only the original view shows it
			if c.view == viewOriginal && c.inDel > 0 && c.inIns == 0 {
				if f := c.topField(); f == nil || f.inResult {
					emit(seg{text: noMarks(k.text)})
				}
			}
		}
	}
	if underlined && emitted {
		c.underline++
	}
}

func (c *docxConv) topField() *fieldState {
	if len(c.fields) == 0 {
		return nil
	}
	return c.fields[len(c.fields)-1]
}

// safeTarget makes a link address that can sit in a Markdown link and be
// nothing else: a web or e-mail address, with every control character,
// white space, bracket, angle bracket, quote, backslash, backtick and bare
// percent sign encoded, so that no address, whatever bytes a document puts in
// it, can end the link, start a paragraph, a wiki link or a private note. An
// address that is not a valid URL is not a link.
func safeTarget(u string) (string, bool) {
	l := strings.ToLower(u)
	if len(u) > 2000 || !(strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "mailto:")) {
		return "", false
	}
	var b strings.Builder
	for i := 0; i < len(u); i++ {
		ch := u[i]
		switch {
		case ch <= 0x20 || ch == 0x7f || strings.IndexByte("[]<>\"'`|{}^()", ch) >= 0:
			fmt.Fprintf(&b, "%%%02X", ch)
		case ch == '%':
			if i+2 < len(u)+0 && isHex(u[i+1]) && isHex(u[i+2]) {
				b.WriteByte('%') // already an escape
			} else {
				b.WriteString("%25")
			}
		default:
			b.WriteByte(ch)
		}
	}
	out := b.String()
	if pu, err := url.Parse(out); err != nil || pu.Host == "" && !strings.HasPrefix(l, "mailto:") {
		return "", false
	}
	return out, true
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

// hyperlinkOf reads the address out of a HYPERLINK field's instruction.
func hyperlinkOf(instr string) string {
	f := strings.TrimSpace(instr)
	if !strings.HasPrefix(strings.ToUpper(f), "HYPERLINK") {
		return ""
	}
	if i := strings.IndexByte(f, '"'); i >= 0 {
		if j := strings.IndexByte(f[i+1:], '"'); j >= 0 {
			if t, ok := safeTarget(f[i+1 : i+1+j]); ok {
				return t
			}
		}
	}
	return ""
}

// plainOf is the text of segments with no formatting.
func plainOf(segs []seg) string {
	var b strings.Builder
	for _, s := range segs {
		switch {
		case s.brk:
			b.WriteByte(' ')
		case s.note != "", s.priv:
		case s.text == "\x00image":
		default:
			b.WriteString(stripMarkTokens(s.text))
		}
	}
	return collapseSpace(b.String())
}

// hasRevisionMark says the text holds one of the private-use characters that mark a revision or a comment in a paragraph (U+E000 to
// U+E007). Each is three bytes and starts with 0xEE, so a text without that byte has none and is not searched.
func hasRevisionMark(s string) bool {
	if strings.IndexByte(s, 0xEE) < 0 {
		return false
	}
	return strings.ContainsAny(s, "\uE000\uE001\uE002\uE003\uE004\uE005\uE006\uE007")
}

// collapseSpace is strings.Join(strings.Fields(s), " ") without the two copies: each run of white space, as unicode.IsSpace counts
// it, becomes one space, and the ends are trimmed. The words are copied byte for byte, invalid UTF-8 included.
func collapseSpace(s string) string {
	var out strings.Builder
	out.Grow(len(s))
	pending := false
	for i := 0; i < len(s); {
		c := s[i]
		size := 1
		space := false
		if c < utf8.RuneSelf {
			space = c == ' ' || (c >= '\t' && c <= '\r')
		} else {
			var r rune
			r, size = utf8.DecodeRuneInString(s[i:])
			space = unicode.IsSpace(r)
		}
		if space {
			pending = true
		} else {
			if pending && out.Len() > 0 {
				out.WriteByte(' ')
			}
			pending = false
			out.WriteString(s[i : i+size])
		}
		i += size
	}
	return out.String()
}

// render writes segments as Markdown: formatting that adjoins is one span, white
// space at a span's edge stays outside its markers, and every character that
// could be read as Markdown is escaped.
func (c *docxConv) render(segs []seg) string {
	var out strings.Builder
	i := 0
	for i < len(segs) {
		s := segs[i]
		switch {
		case s.priv:
			// A private note ends with its text block, and a percent sign that touches
			// it is escaped, so a note is exactly one pair of delimiters.
			var pt strings.Builder
			for i < len(segs) && segs[i].priv {
				if segs[i].brk {
					pt.WriteByte(' ')
				} else {
					pt.WriteString(segs[i].text)
				}
				i++
			}
			out.WriteString(privateNote(pt.String()))
			continue
		case s.wiki != "":
			out.WriteString(s.wiki)
			i++
			continue
		case s.brk:
			out.WriteString("\\\n")
			i++
			continue
		case s.note != "":
			if _, ok := c.noteText[s.note]; ok && !c.inNote {
				out.WriteString("[^" + s.note + "]")
				c.noteSeen = append(c.noteSeen, s.note)
			}
			i++
			continue
		case s.text == "\x00image":
			// A picture that is the whole paragraph is set in italics. Beside a word, the emphasis would not close (a closing
			// asterisk must touch a word on one side only), and the asterisks would print: the mark is plain text there.
			if out.Len() == 0 && i == len(segs)-1 {
				out.WriteString("*\\[image\\]*")
			} else {
				out.WriteString("\\[image\\]")
			}
			i++
			continue
		}
		j := i
		var text strings.Builder
		for j < len(segs) {
			t := segs[j]
			if t.brk || t.note != "" || t.priv || t.wiki != "" || t.text == "\x00image" || t.b != s.b || t.i != s.i || t.s != s.s || t.code != s.code || t.link != s.link {
				break
			}
			text.WriteString(t.text)
			j++
		}
		s.pctLead = i > 0 && segs[i-1].priv
		s.pctTrail = j < len(segs) && segs[j].priv
		out.WriteString(span(text.String(), s))
		i = j
	}
	return out.String()
}

func span(text string, f seg) string {
	text = spanSpaces.Replace(text)
	lead, core, trail := markEdges(text)
	if strings.TrimSpace(core) == "" {
		return escMD(text)
	}
	text = core
	body := escMDx(text, f.pctLead && lead == "", f.pctTrail && trail == "")
	if f.code {
		raw := codeDefuse.Replace(text)
		fence := "`"
		for strings.Contains(raw, fence) {
			fence += "`"
		}
		pad := ""
		if strings.HasPrefix(raw, "`") || strings.HasSuffix(raw, "`") {
			pad = " "
		}
		body = fence + pad + raw + pad + fence
	}
	if f.link != "" {
		body = "[" + body + "](" + escURL(f.link) + ")"
	}
	if f.s {
		body = "~~" + body + "~~"
	}
	if f.b && f.i {
		body = "***" + body + "***"
	} else if f.b {
		body = "**" + body + "**"
	} else if f.i {
		body = "*" + body + "*"
	}
	return lead + body + trail
}

var (
	spanSpaces = strings.NewReplacer("\r", "", "\n", " ", "\t", " ")
	urlEscaper = strings.NewReplacer("(", "%28", ")", "%29", " ", "%20", "<", "%3C", ">", "%3E", "\\", "%5C")
)

func escURL(u string) string { return urlEscaper.Replace(u) }

// escMD escapes text so it reads as the text it is.
func escMD(s string) string { return escMDx(s, false, false) }

// escMDx is escMD; with lead (trail) a percent sign that starts (ends) the text is
// escaped too, because a private note touches it.
func escMDx(s string, lead, trail bool) string {
	if !utf8.ValidString(s) { // a []rune of invalid UTF-8 holds U+FFFD for each bad byte: the text is read that way
		s = string([]rune(s))
	}
	if !strings.ContainsAny(s, "\\`*[]<>~|&_%") {
		return s
	}
	// The escapes are written between the spans of text they fall in, so the text is copied as bytes, not re-encoded rune by rune.
	var b strings.Builder
	b.Grow(len(s) + 8)
	last := 0
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		escape := false
		switch r {
		case '\\', '`', '*', '[', ']', '<', '>', '~', '|', '&':
			escape = true
		case '_':
			prev, _ := utf8.DecodeLastRuneInString(s[:i])
			next, _ := utf8.DecodeRuneInString(s[i+size:])
			prevAlnum := i > 0 && (unicode.IsLetter(prev) || unicode.IsDigit(prev))
			nextAlnum := i+size < len(s) && (unicode.IsLetter(next) || unicode.IsDigit(next))
			escape = !(prevAlnum && nextAlnum)
		case '%':
			nextPct := i+size < len(s) && s[i+size] == '%'
			prevPct := i > 0 && s[i-1] == '%'
			escape = (lead && i == 0) || (trail && i+size == len(s)) || nextPct || prevPct // "%%" is a private note
		}
		if escape {
			b.WriteString(s[last:i])
			b.WriteByte('\\')
			last = i
		}
		i += size
	}
	b.WriteString(s[last:])
	return b.String()
}

// escLine escapes what would make a line a block of its own: a heading, a
// list item, a rule, a fence, a directive, a table or a quote.
func escLine(s string) string {
	if lead := leadMarks(s); lead != "" { // markers in front are nothing: the line is escaped as it begins after them
		return lead + escLine(s[len(lead):])
	}
	// Leading white space is nothing in a paragraph, and four spaces would make
	// the line code: it is dropped, and the line is escaped as it then begins.
	s = strings.TrimLeft(s, " \t\u00a0")
	if s == "" {
		return s
	}
	r0, _ := utf8.DecodeRuneInString(s) // the first rune, as []rune would have it
	switch r0 {
	case '#', '+', '-', '=', ':', '~':
		return "\\" + s
	}
	if !unicode.IsDigit(r0) {
		return s
	}
	rs := []rune(s)
	if unicode.IsDigit(rs[0]) {
		j := 0
		for j < len(rs) && unicode.IsDigit(rs[j]) {
			j++
		}
		if j < len(rs) && (rs[j] == '.' || rs[j] == ')') && (j+1 == len(rs) || rs[j+1] == ' ') {
			return string(rs[:j]) + "\\" + string(rs[j:])
		}
	}
	return s
}

func escLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = escLine(l)
	}
	return strings.Join(lines, "\n")
}

// ---- paragraphs --------------------------------------------------------------------------------------

func (c *docxConv) paragraph(p *dnode) {
	ppr := p.child("pPr")
	styleID := ""
	var numID string
	var numLvl int
	outline := 0
	border := false
	if ppr != nil {
		styleID = ppr.child("pStyle").val()
		if o := ppr.child("outlineLvl"); o != nil {
			if v, err := strconv.Atoi(o.val()); err == nil && v >= 0 && v <= 8 {
				outline = v + 1
			}
		}
		if np := ppr.child("numPr"); np != nil {
			numID = np.child("numId").val()
			numLvl = clampLvl(np.child("ilvl").val())
		}
		if bd := ppr.child("pBdr"); bd != nil && bd.child("bottom") != nil {
			border = true
		}
	}
	name, styleOutline, styleNum, styleNumLvl := c.styles.resolve(styleID)
	lname := strings.ToLower(name)
	if outline == 0 {
		outline = styleOutline
	}
	if outline == 0 {
		if m := reHeadingName.FindStringSubmatch(lname); m != nil {
			outline, _ = strconv.Atoi(m[1])
		}
	}
	if numID == "" {
		numID, numLvl = styleNum, max(0, styleNumLvl)
	}
	if numID == "0" {
		numID = ""
	}
	switch lname {
	case "list bullet", "list number":
		if numID == "" {
			numID = "-" + lname
		}
	}
	isCode := lname == "code" || lname == "source code" || lname == "preformatted text" || lname == "html preformatted" || lname == "macro text"
	look := c.lookOf(p, ppr, styleID, lname)

	// Which text, and with what markers: see docxtrack.go.
	scan := c.scanRev(p)
	c.fmtChange += scan.fmtChanges
	if c.mode == DocxSuggest && (scan.markIns || scan.markDel) {
		// A paragraph whose mark was inserted or deleted: wholly new, wholly cut, or only joined or split.
		save := c.snapshot()
		po := plainOf(c.collectView(p, viewOriginal, false, false))
		pf := plainOf(c.collectView(p, viewFinal, false, false))
		c.restore(save)
		switch {
		case po == "" && pf != "":
			c.tn.InsertedParas++
		case pf == "" && po != "":
			c.tn.DeletedParas++
		default:
			c.tn.ParaMarks++
		}
	}
	inlineOK := outline == 0 && !isCode && !c.inNote
	var segs []seg
	var rendered, renderedFinal string
	plainF := ""
	var revs []blockRev
	var cms []blockComment
	c.noteSeen = nil
	switch {
	case scan.revs == 0 && len(scan.refs) == 0 && !scan.ranges:
		segs = c.collectPara(p)
		rendered = c.render(segs)
	default:
		dual := c.mode == DocxSuggest && scan.revs > 0 && inlineOK && !scan.markIns && !scan.markDel
		if c.mode == DocxSuggest && scan.revs > 0 && !dual && !scan.markIns && !scan.markDel {
			switch {
			case outline > 0:
				c.tn.Headings += scan.revs
			case isCode:
				c.tn.Code += scan.revs
			}
		}
		if dual {
			segsO, segsF, why := c.dualSegs(p)
			if why == "" {
				segs = segsO
				plainF = plainOf(segsF)
				rendered = c.render(segsO)
				notes0 := c.noteSeen
				save := c.snapshot()
				c.second = true
				noteSave := c.noteSeen
				renderedF := c.render(segsF)
				c.noteSeen = noteSave
				c.second = false
				c.restore(save)
				_ = notes0
				renderedFinal = renderedF
				revs, cms = c.blockMarks(rendered, renderedF, scan)
				break
			}
			// A whole paragraph inserted or deleted: the final view.
			switch why {
			case "inserted":
				c.tn.InsertedParas++
			case "deleted":
				c.tn.DeletedParas++
			}
		}
		c.view, c.revMarks, c.cmMarks = c.baseView, false, inlineOK
		segs = c.collectPara(p)
		c.cmMarks = false
		rendered = c.render(segs)
		_, cms = c.blockMarks(rendered, rendered, scan)
	}
	notes := c.noteSeen
	c.noteSeen = nil
	plain := plainOf(segs)
	hasObject := false
	for _, s := range segs {
		if s.text == "\x00image" || s.note != "" {
			hasObject = true
		}
	}
	empty := plain == "" && !hasObject

	// The title page's own paragraphs are facts about the project, not text.
	if c.headings == 0 {
		switch lname {
		case "title":
			if c.title == "" && plain != "" {
				c.title = plain
				return
			}
		case "subtitle":
			if c.subtitle == "" && plain != "" {
				c.subtitle = plain
				return
			}
		case "byline", "contact block", "word count", "title page":
			return
		}
	}
	if lname == "chapter opening" || (empty && styleID == "ChapterOpening") {
		return // the writer's own spacer above a chapter title
	}
	if empty && scan.markIns && c.baseView == viewOriginal {
		return // a paragraph that was inserted: not in the original
	}
	if empty && scan.markDel && c.baseView == viewFinal && plain == "" && !hasObject && scan.revs > 0 {
		return // a paragraph that was deleted whole: gone, not a blank line
	}
	if empty && !border {
		if look.brAny || look.sect {
			// a page or section break held in an empty paragraph is no blank line: it belongs to what comes next
			c.pendBrk = c.pendBrk || look.brAny
			c.pendSect = c.pendSect || look.sect
			return
		}
		c.emptyRun++
		return
	}
	gap := c.emptyRun
	c.emptyRun = 0
	// What the detector keeps of this paragraph; the breaks before it are taken, and those after it wait for the next.
	mkSig := func(outl int) *blockSig {
		sg := c.sigOf(look, plain, plainF, outl, lname, c.paragraphs)
		sg.brBefore = c.pendBrk || look.brStart
		sg.sectBefore = c.pendSect
		if sg.plain == "" && (sg.brBefore || sg.sectBefore || sg.pageBefore) {
			// a long paragraph after a break may be offered as a place a chapter could start: keep its first line to show
			t := plain
			if plainF != "" {
				t = plainF
			}
			sg.plain = strings.Clone(headOf(t, 160))
		}
		return sg
	}
	afterBlock := func() {
		c.pendBrk, c.pendSect = look.brEnd, look.sect
	}
	text := func(escape func(string) string) (string, []blockRev, []blockComment) {
		t, rv, cm := rendered, revs, cms
		if len(rv) == 0 && len(cm) == 0 && !hasRevisionMark(t) {
			return escape(t), nil, nil
		}
		esc := escape(t)
		clean, marks := extractMarks(esc)
		rv, cm = placeMarks(rv, cm, marks, clean)
		if len(rv) > 0 && renderedFinal != "" {
			// What is proposed must give the final text when every suggestion is accepted: the text that was read with the
			// changes in place is the check. Where it does not (a change at the edge of bold or italic text, for one), the
			// paragraph is taken as the reviewer left it, and said.
			want, _ := extractMarks(escape(renderedFinal))
			if applyAll(clean, rv) != want {
				c.tn.Boundary += len(rv)
				for i := range cm {
					cm[i].hasRange = false
				}
				return want, nil, cm
			}
		}
		return clean, rv, cm
	}
	// A paragraph in a style named for chapters that Word numbers ("Chapter %1") is a chapter's title, not a list item.
	chapterNum := false
	if numID != "" && outline == 0 && !isCode {
		if ok, _ := isChapterStyle(lname); ok && len(strings.Fields(plain)) <= 20 && plain != "" {
			chapterNum = true
		}
	}
	switch {
	case outline > 0 && plain != "":
		c.headings++
		lv := min(outline, 6)
		title := plain
		if m := reChapterTag.FindStringSubmatch(title); m != nil {
			title = m[1]
		}
		sg := mkSig(outline)
		sg.numLabel = c.numLabelOf(numID, numLvl)
		c.add(mdBlock{kind: bHeading, level: lv, text: title, sig: sg}, 0)
		afterBlock()
	case lname == "scene break" || border && plain == "" || reSceneText.MatchString(plain):
		c.add(mdBlock{kind: bScene}, 0)
	case strings.Contains(lname, "quote") || lname == "block text":
		t, rv, cm := text(escLines)
		c.add(mdBlock{kind: bQuote, text: t, notes: notes, revs: rv, cms: cm}, gap)
	case isCode:
		c.add(mdBlock{kind: bCode, text: strings.ReplaceAll(plainRaw(segs), "\n", " ")}, gap)
	case numID != "" && !chapterNum:
		lv := c.nums.level(numID, numLvl)
		ordered := !lv.bullet
		if strings.HasPrefix(numID, "-") {
			ordered = lname == "list number"
		}
		key := numID + "/" + strconv.Itoa(numLvl)
		num, started := c.listCounters[key]
		if !started {
			num = lv.start - 1
			if num < 0 {
				num = 0
			}
		}
		num++
		c.listCounters[key] = num
		t, rv, cm := text(escLine2)
		c.add(mdBlock{kind: bList, text: t, ordered: ordered, listLvl: min(numLvl, 8), listNum: numID, start: num, notes: notes, revs: rv, cms: cm}, 0)
	default:
		t, rv, cm := text(escLines)
		sg := mkSig(0)
		if chapterNum {
			sg.numLabel = c.numLabelOf(numID, numLvl) // a numbered chapter title: Word's number is not part of the title
		}
		c.add(mdBlock{kind: bPara, text: t, notes: notes, revs: rv, cms: cm, sig: sg}, gap)
		afterBlock()
	}
}

// plainRaw is a code paragraph's text exactly as written.
func plainRaw(segs []seg) string {
	var b strings.Builder
	for _, s := range segs {
		if s.brk {
			b.WriteByte('\n')
		} else if s.note == "" && !s.priv && s.text != "\x00image" {
			b.WriteString(s.text)
		}
	}
	return b.String()
}

// escLine2 escapes a list item's text. Its first line follows the marker, and
// "- # text" is a heading inside the item, so it is escaped like any other line.
func escLine2(s string) string { return escLines(s) }

// add appends a block; a gap of two or more empty paragraphs between two body
// paragraphs is a scene break.
func (c *docxConv) add(b mdBlock, gap int) {
	if b.kind == bCode {
		b.text = codeDefuse.Replace(b.text)
	}
	if gap >= 2 && c.haveLast && c.lastKind == bPara && b.kind == bPara {
		c.blocks = append(c.blocks, mdBlock{kind: bScene})
		c.gapScenes++
	}
	if b.kind == bScene && len(c.blocks) > 0 && c.blocks[len(c.blocks)-1].kind == bScene {
		return // one break, however it was marked
	}
	if c.total += len(b.text); c.total > 2*MaxTotalBytes {
		c.overflow = true
		return
	}
	c.blocks = append(c.blocks, b)
	c.lastKind, c.haveLast = b.kind, true
	if b.kind != bList {
		// a list ends where something else begins: the next list starts again
		for k := range c.listCounters {
			delete(c.listCounters, k)
		}
	}
}

// ---- tables --------------------------------------------------------------------------------------------

// escPipes escapes the cell separator in a table cell's text. Text outside code
// is already escaped; a code span's pipes are not.
func escPipes(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '|' && (i == 0 || s[i-1] != '\\') {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func (c *docxConv) table(t *dnode) {
	var tableNotes []string
	var rows [][]string
	cols := 0
	for _, tr := range t.kids {
		if tr.name != "tr" {
			continue
		}
		var cells []string
		for _, tc := range tr.kids {
			if tc.name != "tc" {
				continue
			}
			span := 1
			if pr := tc.child("tcPr"); pr != nil {
				if gs := pr.child("gridSpan"); gs != nil {
					if v, err := strconv.Atoi(gs.val()); err == nil && v > 1 && v < 64 {
						span = v
					}
				}
				if vm := pr.child("vMerge"); vm != nil && vm.val() != "restart" {
					cells = append(cells, "")
					continue
				}
			}
			var parts []string
			for _, p := range tc.kids {
				if p.name != "p" {
					continue
				}
				if c.mode == DocxSuggest {
					c.tn.Tables += c.scanRev(p).revs
				}
				c.noteSeen = nil
				segs := c.collectPara(p)
				rendered := strings.TrimSpace(strings.ReplaceAll(c.render(segs), "\n", " "))
				tableNotes = append(tableNotes, c.noteSeen...)
				c.noteSeen = nil
				if rendered != "" {
					parts = append(parts, rendered)
				}
			}
			cell := escPipes(strings.Join(parts, " "))
			cells = append(cells, cell)
			for k := 1; k < span; k++ {
				cells = append(cells, "")
			}
		}
		if len(cells) > 0 {
			rows = append(rows, cells)
			cols = max(cols, len(cells))
		}
	}
	if len(rows) == 0 || cols == 0 || cols > 63 {
		return
	}
	var b strings.Builder
	line := func(cells []string) {
		b.WriteString("|")
		for i := 0; i < cols; i++ {
			v := ""
			if i < len(cells) {
				v = cells[i]
			}
			b.WriteString(" " + v + " |")
		}
		b.WriteString("\n")
	}
	line(rows[0])
	b.WriteString("|")
	for i := 0; i < cols; i++ {
		b.WriteString(" --- |")
	}
	b.WriteString("\n")
	for _, r := range rows[1:] {
		line(r)
	}
	c.tables++
	c.emptyRun = 0
	c.add(mdBlock{kind: bTable, text: strings.TrimRight(b.String(), "\n"), notes: tableNotes}, 0)
}

// ---- files ----------------------------------------------------------------------------------------------

type dchapter struct {
	title  string
	blocks []mdBlock
	level  int
}

type dpart struct {
	title    string
	intro    []mdBlock
	chapters []*dchapter
}

func (c *docxConv) files(name, headings string) ([]File, bool, error) {
	min1 := 0
	has := map[int]bool{}
	for _, b := range c.blocks {
		if b.kind == bHeading {
			has[b.level] = true
			if min1 == 0 || b.level < min1 {
				min1 = b.level
			}
		}
	}
	parts := headings == DocxParts && min1 > 0 && has[min1+1]
	var front []mdBlock
	var chapters []*dchapter // chapters mode, and part-less chapters in parts mode
	var ps []*dpart
	var curChapter *dchapter
	var curPart *dpart
	chapLevel := min1
	if parts {
		chapLevel = min1 + 1
	}
	for _, b := range c.blocks {
		if b.kind == bHeading && min1 > 0 {
			switch {
			case parts && b.level == min1:
				curPart = &dpart{title: b.text}
				ps = append(ps, curPart)
				curChapter = nil
				continue
			case b.level == chapLevel:
				ch := &dchapter{title: b.text, level: chapLevel}
				if parts && curPart != nil {
					curPart.chapters = append(curPart.chapters, ch)
				} else {
					chapters = append(chapters, ch)
				}
				curChapter = ch
				continue
			case b.level > chapLevel && (curChapter != nil):
				b.level = b.level - chapLevel + 1
				curChapter.blocks = append(curChapter.blocks, b)
				continue
			}
		}
		switch {
		case curChapter != nil:
			curChapter.blocks = append(curChapter.blocks, b)
		case curPart != nil:
			curPart.intro = append(curPart.intro, b)
		default:
			if b.kind == bHeading && min1 > 0 && b.level > chapLevel {
				b.level = b.level - chapLevel + 1
			}
			front = append(front, b)
		}
	}
	var out []File
	total := 0
	add := func(path, title string, blocks []mdBlock) error {
		// The size is known before the file is made: the blocks, and at worst
		// every footnote once (a note referenced from a block is written once per
		// file).
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
	// Names: a number from reading order, then the slug of the title.
	count := len(chapters)
	for _, p := range ps {
		count += 1 + len(p.chapters)
	}
	width := max(2, len(strconv.Itoa(count+1)))
	slug := func(s string) string {
		v := projects.Slugify(s)
		if v == "" {
			return "untitled"
		}
		return v
	}
	if len(front) > 0 && hasContent(front) {
		if err := add("00-front-matter.md", "", front); err != nil {
			return nil, false, err
		}
	}
	n := 0
	for _, ch := range chapters {
		n++
		if err := add(fmt.Sprintf("%0*d-%s.md", width, n, slug(ch.title)), ch.title, ch.blocks); err != nil {
			return nil, false, err
		}
	}
	for _, p := range ps {
		n++
		dir := fmt.Sprintf("%0*d-%s", width, n, slug(p.title))
		pw := max(2, len(strconv.Itoa(len(p.chapters)+1)))
		if hasContent(p.intro) {
			if err := add(dir+"/00-"+slug(p.title)+".md", p.title, p.intro); err != nil {
				return nil, false, err
			}
		}
		for i, ch := range p.chapters {
			if err := add(fmt.Sprintf("%s/%0*d-%s.md", dir, pw, i+1, slug(ch.title)), ch.title, ch.blocks); err != nil {
				return nil, false, err
			}
		}
		if len(p.chapters) == 0 && !hasContent(p.intro) {
			if err := add(dir+"/00-"+slug(p.title)+".md", p.title, nil); err != nil {
				return nil, false, err
			}
		}
	}
	if len(out) == 0 {
		return nil, false, errors.New("the document has no text to import")
	}
	return out, parts, nil
}

func hasContent(bs []mdBlock) bool {
	for _, b := range bs {
		if b.kind != bHeading && b.kind != bScene || b.kind == bHeading && strings.TrimSpace(b.text) != "" {
			return true
		}
	}
	return false
}

// compose writes one file: its title as a first-level heading, then the blocks,
// then the footnotes they refer to.
func (c *docxConv) compose(title string, blocks []mdBlock) string {
	var b strings.Builder
	if t := strings.TrimSpace(title); t != "" {
		t = strings.TrimRight(t, "# ")
		b.WriteString("# " + t + "\n")
	}
	var notes []string
	seen := map[string]bool{}
	first := true
	sep := func() {
		if !first || b.Len() > 0 {
			b.WriteString("\n")
		}
		first = false
	}
	for i := 0; i < len(blocks); i++ {
		bl := blocks[i]
		switch bl.kind {
		case bPara:
			sep()
			b.WriteString(bl.text + "\n")
		case bHeading:
			sep()
			b.WriteString(strings.Repeat("#", min(bl.level, 6)) + " " + strings.TrimRight(strings.ReplaceAll(escLine3(bl.text), "\n", " "), "# ") + "\n")
		case bScene:
			sep()
			b.WriteString("* * *\n")
		case bQuote:
			sep()
			j := i
			for j < len(blocks) && blocks[j].kind == bQuote {
				if j > i {
					b.WriteString(">\n")
				}
				for _, l := range strings.Split(blocks[j].text, "\n") {
					b.WriteString("> " + l + "\n")
				}
				notes = append(notes, blocks[j].notes...)
				j++
			}
			i = j - 1
			continue
		case bCode:
			sep()
			j := i
			var lines []string
			for j < len(blocks) && blocks[j].kind == bCode {
				lines = append(lines, blocks[j].text)
				j++
			}
			fence := "```"
			for strings.Contains(strings.Join(lines, "\n"), fence) {
				fence += "`"
			}
			b.WriteString(fence + "\n" + strings.Join(lines, "\n") + "\n" + fence + "\n")
			i = j - 1
			continue
		case bList:
			sep()
			j := i
			for j < len(blocks) && blocks[j].kind == bList && blocks[j].listNum == bl.listNum {
				it := blocks[j]
				marker := "- "
				if it.ordered {
					marker = strconv.Itoa(it.start) + ". "
				}
				indent := strings.Repeat("    ", it.listLvl)
				lines := strings.Split(it.text, "\n")
				b.WriteString(indent + marker + lines[0] + "\n")
				for _, l := range lines[1:] {
					b.WriteString(indent + strings.Repeat(" ", len(marker)) + l + "\n")
				}
				notes = append(notes, it.notes...)
				j++
			}
			i = j - 1
			continue
		case bTable:
			sep()
			b.WriteString(bl.text + "\n")
		}
		notes = append(notes, bl.notes...)
	}
	// Footnote definitions, numbered by first reference within the file.
	label := map[string]int{}
	var order []string
	for _, id := range notes {
		if !seen[id] && len(order) < maxNotes {
			seen[id] = true
			order = append(order, id)
			label[id] = len(order)
		}
	}
	text := b.String()
	if len(order) > 0 {
		// Renumbered in one pass over the text: the ids are plain numbers, and a
		// bracket in the document's own words is escaped, so only a marker this
		// program wrote matches.
		text = reNoteMarker.ReplaceAllStringFunc(text, func(m string) string {
			if n, ok := label[m[2:len(m)-1]]; ok {
				return "[^" + strconv.Itoa(n) + "]"
			}
			return m
		})
		var fb strings.Builder
		fb.WriteString("\n")
		for _, id := range order {
			paras := c.noteText[id]
			for k, p := range paras {
				if k == 0 {
					fb.WriteString("[^" + strconv.Itoa(label[id]) + "]: " + p + "\n")
				} else {
					fb.WriteString("\n    " + p + "\n")
				}
			}
		}
		text += fb.String()
	}
	return text
}

var reNoteMarker = regexp.MustCompile(`\[\^[0-9]{1,9}\]`)

// escLine3 escapes a heading's text: it follows its own # marks, so only what
// could end it or start something inside it needs a backslash.
func escLine3(s string) string {
	return escMD(strings.TrimSpace(s))
}
