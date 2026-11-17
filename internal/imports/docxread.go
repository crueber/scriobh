package imports

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Reading a DOCX. A .docx is a zip of XML parts. This reads only the parts it
// needs (the body, the styles, the numbering, the footnotes, the comments, the
// relationships and the core properties), never runs or opens anything else,
// and trusts no size in the archive: the central directory is checked first,
// every part is read through a limit, and XML is decoded as a stream with a
// depth limit and without entities. Macros (vbaProject.bin) and embedded
// objects are counted, never read.

const (
	nsWord = "http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	nsRel  = "http://schemas.openxmlformats.org/officeDocument/2006/relationships"

	maxDocxPart     = 96 << 20  // one XML part, inflated
	maxDocxInflated = 160 << 20 // all the parts read
	maxXMLDepth     = 80
	maxParaNodes    = 400_000 // tokens in one paragraph (or one table)
	maxParaText     = 8 << 20 // bytes of text in one paragraph (or one table)
	maxParagraphs   = 1_000_000
)

// docxFile is the parts of a DOCX the importer reads.
type docxFile struct {
	document, styles, numbering, footnotes, comments, rels, core []byte
	media, embeddings, macros                                    int
	headersFooters                                               int
}

func readDocx(data []byte) (*docxFile, error) {
	if err := checkZipDirectory(data); err != nil {
		return nil, err
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("that is not a readable .docx file (it is not a zip)")
	}
	d := &docxFile{}
	var budget int64 = maxDocxInflated
	read := func(zf *zip.File) ([]byte, error) {
		if zf.UncompressedSize64 > maxDocxPart {
			return nil, fmt.Errorf("%s is larger than the import limit", zf.Name)
		}
		rc, err := zf.Open()
		if err != nil {
			return nil, fmt.Errorf("%s cannot be read", zf.Name)
		}
		defer rc.Close()
		b, err := io.ReadAll(io.LimitReader(rc, maxDocxPart+1))
		if err != nil {
			return nil, fmt.Errorf("%s cannot be read", zf.Name)
		}
		if len(b) > maxDocxPart {
			return nil, fmt.Errorf("%s is larger than the import limit", zf.Name)
		}
		budget -= int64(len(b))
		if budget < 0 {
			return nil, errors.New("the document holds more text than the import limit")
		}
		return b, nil
	}
	for _, zf := range zr.File {
		name := zf.Name
		lower := strings.ToLower(name)
		switch {
		case name == "word/document.xml":
			d.document, err = read(zf)
		case name == "word/styles.xml":
			d.styles, err = read(zf)
		case name == "word/numbering.xml":
			d.numbering, err = read(zf)
		case name == "word/footnotes.xml":
			d.footnotes, err = read(zf)
		case name == "word/comments.xml":
			d.comments, err = read(zf)
		case name == "word/_rels/document.xml.rels":
			d.rels, err = read(zf)
		case name == "docProps/core.xml":
			d.core, err = read(zf)
		case strings.HasPrefix(lower, "word/media/"):
			d.media++
		case strings.HasPrefix(lower, "word/embeddings/"):
			d.embeddings++
		case strings.Contains(lower, "vbaproject"):
			d.macros++
		case strings.HasPrefix(lower, "word/header") || strings.HasPrefix(lower, "word/footer"):
			d.headersFooters++
		}
		if err != nil {
			return nil, err
		}
	}
	if d.document == nil {
		return nil, errors.New("that is not a Word document: it has no word/document.xml")
	}
	return d, nil
}

// dnode is a small DOM node: the elements of one paragraph or table.
type dnode struct {
	name  string // local name, for elements in the WordprocessingML namespace; "" otherwise
	other string // local name of an element in any other namespace
	attrs map[string]string
	kids  []*dnode
	text  string
}

func (n *dnode) attr(k string) string { return n.attrs[k] }

func (n *dnode) child(name string) *dnode {
	for _, k := range n.kids {
		if k.name == name {
			return k
		}
	}
	return nil
}

func (n *dnode) val() string {
	if n == nil {
		return ""
	}
	return n.attrs["val"]
}

// newDecoder makes the strict, entity-free decoder every part is read with.
func newDecoder(b []byte) *xml.Decoder {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = true
	dec.Entity = nil
	return dec
}

func nodeOf(t xml.StartElement) *dnode {
	n := &dnode{attrs: map[string]string{}}
	if t.Name.Space == nsWord {
		n.name = t.Name.Local
	} else {
		n.other = t.Name.Local
	}
	for _, a := range t.Attr {
		switch a.Name.Space {
		case nsWord, "":
			n.attrs[a.Name.Local] = a.Value
		case nsRel:
			n.attrs["r:"+a.Name.Local] = a.Value
		}
	}
	return n
}

// readSubtree reads the element that start opened, with everything inside it.
func readSubtree(dec *xml.Decoder, start xml.StartElement, depth int, budget *int) (*dnode, error) {
	left := maxParaText
	return readSub(dec, start, depth, budget, &left)
}

// readSub reads an element's subtree. Every token of every kind, a comment or a
// run of character data as much as an element, is paid for from budget, and the
// text kept is bounded by textLeft: a w:t made of a million pieces, or of forty
// megabytes, is refused rather than built.
func readSub(dec *xml.Decoder, start xml.StartElement, depth int, budget, textLeft *int) (*dnode, error) {
	n := nodeOf(start)
	if depth > maxXMLDepth {
		return nil, errors.New("the document is nested too deeply")
	}
	var tb strings.Builder
	keep := n.name == "t" || n.name == "delText" || n.name == "instrText"
	for {
		tok, err := dec.Token()
		if err != nil {
			return nil, err
		}
		*budget--
		if *budget < 0 {
			return nil, errors.New("a paragraph or table is too large")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			k, err := readSub(dec, t, depth+1, budget, textLeft)
			if err != nil {
				return nil, err
			}
			n.kids = append(n.kids, k)
		case xml.CharData:
			if keep {
				*textLeft -= len(t)
				if *textLeft < 0 {
					return nil, errors.New("a paragraph or table holds more text than the import limit")
				}
				tb.Write(t)
			}
		case xml.EndElement:
			n.text = tb.String()
			return n, nil
		case xml.Directive:
			return nil, errors.New("a DOCTYPE is not allowed")
		}
	}
}

// styleInfo is what the importer needs of a paragraph style.
type styleInfo struct {
	name      string
	basedOn   string
	outline   int // 1-based heading level from the style's outline level, 0 for none
	hasOutl   bool
	numID     string
	numLevel  int
	monospace bool
}

type styleTable map[string]*styleInfo

func parseStyles(b []byte) (styleTable, string) {
	t := styleTable{}
	lang := ""
	if b == nil {
		return t, lang
	}
	dec := newDecoder(b)
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		if se.Name.Space == nsWord && se.Name.Local == "style" {
			budget := 5000
			n, err := readSubtree(dec, se, 0, &budget)
			if err != nil {
				break
			}
			si := &styleInfo{name: n.child("name").val(), basedOn: n.child("basedOn").val()}
			if p := n.child("pPr"); p != nil {
				if o := p.child("outlineLvl"); o != nil {
					if v, err := strconv.Atoi(o.val()); err == nil && v >= 0 && v <= 8 {
						si.outline, si.hasOutl = v+1, true
					}
				}
				if np := p.child("numPr"); np != nil {
					si.numID = np.child("numId").val()
					si.numLevel, _ = strconv.Atoi(np.child("ilvl").val())
					si.numLevel = max(0, si.numLevel)
				}
			}
			if r := n.child("rPr"); r != nil {
				if f := r.child("rFonts"); f != nil && strings.Contains(strings.ToLower(f.attr("ascii")), "courier") && strings.Contains(strings.ToLower(si.name), "code") {
					si.monospace = true
				}
			}
			t[n.attr("styleId")] = si
		} else if se.Name.Space == nsWord && se.Name.Local == "lang" && lang == "" {
			for _, a := range se.Attr {
				if a.Name.Local == "val" {
					lang = a.Value
				}
			}
		}
	}
	return t, lang
}

// resolve follows basedOn for the outline level and the numbering.
func (t styleTable) resolve(id string) (name string, outline int, numID string, numLvl int) {
	for depth := 0; id != "" && depth < 12; depth++ {
		s, ok := t[id]
		if !ok {
			break
		}
		if name == "" {
			name = s.name
		}
		if outline == 0 && s.hasOutl {
			outline = s.outline
		}
		if numID == "" && s.numID != "" {
			numID, numLvl = s.numID, s.numLevel
		}
		id = s.basedOn
	}
	return
}

// numbering maps a numId and level to bullet or ordered, and the start.
type numTable struct {
	abstract map[string]map[int]numLevel // abstractNumId -> level
	num      map[string]string           // numId -> abstractNumId
	override map[string]map[int]int      // numId -> level -> start override
}

type numLevel struct {
	bullet bool
	start  int
}

func parseNumbering(b []byte) *numTable {
	nt := &numTable{abstract: map[string]map[int]numLevel{}, num: map[string]string{}, override: map[string]map[int]int{}}
	if b == nil {
		return nt
	}
	dec := newDecoder(b)
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		se, ok := tok.(xml.StartElement)
		if !ok || se.Name.Space != nsWord {
			continue
		}
		switch se.Name.Local {
		case "abstractNum":
			budget := 20000
			n, err := readSubtree(dec, se, 0, &budget)
			if err != nil {
				return nt
			}
			lv := map[int]numLevel{}
			for _, l := range n.kids {
				if l.name != "lvl" {
					continue
				}
				i, _ := strconv.Atoi(l.attr("ilvl"))
				start := 1
				if s := l.child("start"); s != nil {
					if v, err := strconv.Atoi(s.val()); err == nil {
						start = v
					}
				}
				lv[i] = numLevel{bullet: l.child("numFmt").val() == "bullet" || l.child("numFmt").val() == "none", start: start}
			}
			nt.abstract[n.attr("abstractNumId")] = lv
		case "num":
			budget := 2000
			n, err := readSubtree(dec, se, 0, &budget)
			if err != nil {
				return nt
			}
			id := n.attr("numId")
			nt.num[id] = n.child("abstractNumId").val()
			for _, o := range n.kids {
				if o.name != "lvlOverride" {
					continue
				}
				i, _ := strconv.Atoi(o.attr("ilvl"))
				if s := o.child("startOverride"); s != nil {
					if v, err := strconv.Atoi(s.val()); err == nil {
						if nt.override[id] == nil {
							nt.override[id] = map[int]int{}
						}
						nt.override[id][i] = v
					}
				}
			}
		}
	}
	return nt
}

func (nt *numTable) level(numID string, lvl int) numLevel {
	l := nt.abstract[nt.num[numID]][lvl]
	if o, ok := nt.override[numID][lvl]; ok {
		l.start = o
	}
	if _, ok := nt.abstract[nt.num[numID]][lvl]; !ok && nt.num[numID] == "" {
		return numLevel{bullet: true, start: 1}
	}
	return l
}

// parseRels returns the targets of the external hyperlink relationships by id.
func parseRels(b []byte) map[string]string {
	out := map[string]string{}
	if b == nil {
		return out
	}
	dec := newDecoder(b)
	for {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		if se, ok := tok.(xml.StartElement); ok && se.Name.Local == "Relationship" {
			var id, target, typ, mode string
			for _, a := range se.Attr {
				switch a.Name.Local {
				case "Id":
					id = a.Value
				case "Target":
					target = a.Value
				case "Type":
					typ = a.Value
				case "TargetMode":
					mode = a.Value
				}
			}
			if strings.HasSuffix(typ, "/hyperlink") && strings.EqualFold(mode, "External") {
				out[id] = target
			}
		}
	}
	return out
}

// parseCore reads the title, the creator and the language of docProps/core.xml.
func parseCore(b []byte) (title, creator, lang string) {
	if b == nil {
		return
	}
	dec := newDecoder(b)
	var cur string
	var ti, cr, la strings.Builder
	left := 4096 // a title is a line: past this the rest is ignored, not built
	for n := 0; n < 100_000; n++ {
		tok, err := dec.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			cur = t.Name.Local
		case xml.CharData:
			if left <= 0 {
				continue
			}
			s := strings.TrimSpace(string(t))
			left -= len(s)
			switch cur {
			case "title":
				ti.WriteString(s)
			case "creator":
				cr.WriteString(s)
			case "language":
				la.WriteString(s)
			}
		case xml.EndElement:
			cur = ""
		}
	}
	title, creator, lang = ti.String(), cr.String(), la.String()
	return
}
