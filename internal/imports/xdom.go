package imports

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/unicode"
)

// A small XML reader for the files of Scrivener (the binder) and WordPress (a
// WXR export): a strict, entity-free decoder over text that has been turned
// into UTF-8 first, a depth limit, and a budget that every token of every kind
// is paid for from, so nothing a file nests, repeats or pads can make the reader
// do more than a constant amount of work for each byte it is given.

const (
	xmlMaxDepth = 80
)

// xnode is an element: its name (with a prefix tag for the WordPress
// namespaces), attributes, children and the text directly inside it.
type xnode struct {
	name  string
	attrs map[string]string
	kids  []*xnode
	text  string
}

// attr returns an attribute by local name ("" when absent).
func (n *xnode) attr(k string) string {
	if n == nil {
		return ""
	}
	return n.attrs[k]
}

// child returns the first child with the name.
func (n *xnode) child(name string) *xnode {
	if n == nil {
		return nil
	}
	for _, k := range n.kids {
		if k.name == name {
			return k
		}
	}
	return nil
}

// childText is the trimmed text of the first child with the name.
func (n *xnode) childText(name string) string {
	if c := n.child(name); c != nil {
		return strings.TrimSpace(c.text)
	}
	return ""
}

// rawText is the text of the first child with the name, as it is, or "" when
// there is no such child.
func (n *xnode) rawText(name string) string {
	if c := n.child(name); c != nil {
		return c.text
	}
	return ""
}

// each calls fn for every child with the name.
func (n *xnode) each(name string, fn func(*xnode)) {
	if n == nil {
		return
	}
	for _, k := range n.kids {
		if k.name == name {
			fn(k)
		}
	}
}

// xmlLimits are what one parse may spend.
type xmlLimits struct {
	tokens int // tokens of every kind
	text   int // bytes of text kept
	depth  int // nesting; xmlMaxDepth when zero
}

var (
	errXMLTokens = errors.New("the file is too large or too intricate to read")
	errXMLText   = errors.New("the file holds more text than the import limit")
	errXMLDeep   = errors.New("the file is nested too deeply")
	errXMLDoctyp = errors.New("a DOCTYPE or entity definition is not allowed")
)

// decodeXML turns the bytes of an XML file into UTF-8: a byte-order mark or a
// UTF-16 file is read as that, a declared single-byte encoding is decoded, a
// file with invalid UTF-8 and no encoding is read as Windows-1252, control
// characters XML forbids become spaces. notes says what was done.
func decodeXML(b []byte) (out []byte, notes []string) {
	enc := ""
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		b = b[3:]
	case bytes.HasPrefix(b, []byte{0xFF, 0xFE}), bytes.HasPrefix(b, []byte{0xFE, 0xFF}):
		enc = "UTF-16 (with a byte-order mark)"
		d, err := unicode.UTF16(unicode.LittleEndian, unicode.UseBOM).NewDecoder().Bytes(b)
		if err == nil {
			b = d
		}
	case len(b) >= 4 && b[0] == '<' && b[1] == 0 && b[2] == '?' && b[3] == 0:
		enc = "UTF-16"
		if d, err := unicode.UTF16(unicode.LittleEndian, unicode.IgnoreBOM).NewDecoder().Bytes(b); err == nil {
			b = d
		}
	case len(b) >= 4 && b[0] == 0 && b[1] == '<' && b[2] == 0 && b[3] == '?':
		enc = "UTF-16"
		if d, err := unicode.UTF16(unicode.BigEndian, unicode.IgnoreBOM).NewDecoder().Bytes(b); err == nil {
			b = d
		}
	}
	if enc != "" {
		notes = append(notes, "The file was "+enc+" and was converted to UTF-8.")
	}
	if !utf8.Valid(b) {
		label := declaredEncoding(b)
		cm := charmapByLabel(label)
		if cm == nil {
			cm = charmap.Windows1252
			label = "windows-1252"
		}
		if d, err := cm.NewDecoder().Bytes(b); err == nil {
			b = d
			notes = append(notes, fmt.Sprintf("The file is not UTF-8; it was read as %s.", label))
		} else {
			b = bytes.ToValidUTF8(b, []byte("\uFFFD"))
			notes = append(notes, "The file is not valid UTF-8; the bad bytes were replaced.")
		}
	}
	// The decoder must not try to convert again: the declaration now lies.
	if i := bytes.Index(b, []byte("?>")); i > 0 && i < 300 && bytes.HasPrefix(bytes.TrimLeft(b, " \t\r\n"), []byte("<?xml")) {
		head := string(b[:i])
		if k := strings.Index(strings.ToLower(head), "encoding"); k >= 0 {
			// Drop the encoding pseudo-attribute (whatever its quotes).
			rest := head[k:]
			if q := strings.IndexAny(rest, "\"'"); q >= 0 {
				if q2 := strings.IndexByte(rest[q+1:], rest[q]); q2 >= 0 {
					head = head[:k] + rest[q+1+q2+1:]
					b = append([]byte(head), b[i:]...)
				}
			}
		}
	}
	// XML 1.0 forbids most control characters; exporters write them anyway.
	bad := 0
	for _, c := range b {
		if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
			bad++
		}
	}
	if bad > 0 {
		cp := make([]byte, len(b))
		copy(cp, b)
		for i, c := range cp {
			if c < 0x20 && c != '\t' && c != '\n' && c != '\r' {
				cp[i] = ' '
			}
		}
		b = cp
		notes = append(notes, fmt.Sprintf("%d control character(s) that XML does not allow were replaced by spaces.", bad))
	}
	return b, notes
}

func declaredEncoding(b []byte) string {
	head := b
	if len(head) > 200 {
		head = head[:200]
	}
	s := strings.ToLower(string(head))
	i := strings.Index(s, "encoding")
	if i < 0 {
		return ""
	}
	rest := s[i+8:]
	q := strings.IndexAny(rest, "\"'")
	if q < 0 {
		return ""
	}
	e := strings.IndexByte(rest[q+1:], rest[q])
	if e < 0 {
		return ""
	}
	return strings.TrimSpace(rest[q+1 : q+1+e])
}

func charmapByLabel(l string) *charmap.Charmap {
	switch l {
	case "iso-8859-1", "latin1", "latin-1", "windows-1252", "cp1252", "us-ascii", "ascii":
		return charmap.Windows1252
	case "iso-8859-2":
		return charmap.ISO8859_2
	case "iso-8859-15":
		return charmap.ISO8859_15
	case "windows-1250":
		return charmap.Windows1250
	case "windows-1251":
		return charmap.Windows1251
	case "macintosh", "mac", "macroman":
		return charmap.Macintosh
	}
	return nil
}

func newXMLDecoder(b []byte) *xml.Decoder {
	dec := xml.NewDecoder(bytes.NewReader(b))
	dec.Strict = true
	dec.Entity = nil
	dec.CharsetReader = func(_ string, in io.Reader) (io.Reader, error) { return in, nil }
	return dec
}

// Per element, the attributes and the children kept: a document that holds more
// has the rest counted (under "(attributes)" and "(children)") and left out.
const (
	maxXMLAttrs = 64
	maxXMLKids  = 100_000
	maxXMLTag   = 1 << 20 // bytes of one tag, attributes included
)

// nodeOf makes the node of an element. The map of attributes is made only when
// there is one to keep.
func (r *xreader) nodeOf(t xml.StartElement) *xnode {
	n := &xnode{name: tagged(t.Name)}
	for _, a := range t.Attr {
		if a.Name.Space == "xmlns" || a.Name.Local == "xmlns" {
			continue
		}
		if n.attrs == nil {
			n.attrs = make(map[string]string, 4)
		}
		if _, dup := n.attrs[a.Name.Local]; dup {
			continue
		}
		if len(n.attrs) >= maxXMLAttrs {
			r.skipped["(attributes)"]++
			continue
		}
		n.attrs[a.Name.Local] = a.Value
	}
	return n
}

var errXMLTag = errors.New("an element has more than 1 MiB of attributes")

// checkTagSizes refuses a file with a tag longer than maxXMLTag. The decoder reads
// every attribute of a tag into memory before it hands the tag over, so this is
// checked on the bytes first: one pass, looking for each tag's end no further than the
// limit, and skipping the contents of CDATA, comments and processing instructions
// (a post's HTML has tags of its own).
func checkTagSizes(b []byte) error {
	for i := 0; i < len(b); {
		j := bytes.IndexByte(b[i:], '<')
		if j < 0 {
			return nil
		}
		i += j
		rest := b[i:]
		skip := func(end string) bool {
			k := bytes.Index(rest, []byte(end))
			if k < 0 {
				i = len(b)
				return true
			}
			i += k + len(end)
			return true
		}
		switch {
		case bytes.HasPrefix(rest, []byte("<![CDATA[")):
			skip("]]>")
			continue
		case bytes.HasPrefix(rest, []byte("<!--")):
			skip("-->")
			continue
		case bytes.HasPrefix(rest, []byte("<?")):
			skip("?>")
			continue
		}
		k := bytes.IndexByte(rest[:min(len(rest), maxXMLTag+1)], '>')
		if k < 0 {
			if len(rest) > maxXMLTag {
				return errXMLTag
			}
			return nil
		}
		i += k + 1
	}
	return nil
}

// tagged names an element: the local name, with the prefix the WordPress
// namespaces are always written with ("wp:post_id", "content:encoded").
func tagged(n xml.Name) string {
	switch {
	case strings.Contains(n.Space, "/modules/content"):
		return "content:" + n.Local
	case strings.Contains(n.Space, "wordpress.org/export") && strings.HasSuffix(n.Space, "/excerpt/"):
		return "excerpt:" + n.Local
	case strings.Contains(n.Space, "wordpress.org/export"):
		return "wp:" + n.Local
	case strings.Contains(n.Space, "purl.org/dc/"):
		return "dc:" + n.Local
	}
	return n.Local
}

// xreader reads nodes under one budget. Elements named in skip are counted and
// thrown away unread (a post's comments: they are reported, never imported, and a
// post can have a hundred thousand).
type xreader struct {
	dec     *xml.Decoder
	tokens  int
	text    int
	skip    map[string]bool
	skipped map[string]int
	depth   int
}

func newXReader(b []byte, lim xmlLimits, skip ...string) *xreader {
	r := &xreader{dec: newXMLDecoder(b), tokens: lim.tokens, text: lim.text, skipped: map[string]int{}, depth: lim.depth}
	if r.depth == 0 {
		r.depth = xmlMaxDepth
	}
	if len(skip) > 0 {
		r.skip = map[string]bool{}
		for _, s := range skip {
			r.skip[s] = true
		}
	}
	return r
}

func (r *xreader) pay() error {
	if r.tokens--; r.tokens < 0 {
		return errXMLTokens
	}
	return nil
}

// discard reads to the end of the element just opened, keeping nothing.
func (r *xreader) discard(depth int) error {
	for open := 1; open > 0; {
		tok, err := r.dec.Token()
		if err != nil {
			return err
		}
		if err := r.pay(); err != nil {
			return err
		}
		switch tok.(type) {
		case xml.StartElement:
			if open++; depth+open > r.depth {
				return errXMLDeep
			}
		case xml.EndElement:
			open--
		case xml.Directive:
			return errXMLDoctyp
		}
	}
	return nil
}

// node reads the element that start opened with everything in it.
func (r *xreader) node(start xml.StartElement, depth int, want map[string]bool) (*xnode, error) {
	n := r.nodeOf(start)
	if depth > r.depth {
		return nil, errXMLDeep
	}
	var tb strings.Builder
	for {
		tok, err := r.dec.Token()
		if err != nil {
			return nil, err
		}
		if err := r.pay(); err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if r.skip != nil && r.skip[tagged(t.Name)] {
				r.skipped[tagged(t.Name)]++
				if err := r.discard(depth + 1); err != nil {
					return nil, err
				}
				continue
			}
			// Only the children wanted are built (want is set for the element a stream
			// hands over: a post, the channel), and no element keeps more than maxXMLKids.
			if unwanted := want != nil && !want[tagged(t.Name)]; unwanted || len(n.kids) >= maxXMLKids {
				if unwanted {
					r.skipped["(unwanted)"]++ // what a reader does not use: counted, never a warning
				} else {
					r.skipped["(children)"]++ // past what one element keeps
				}
				if err := r.discard(depth + 1); err != nil {
					return nil, err
				}
				continue
			}
			k, err := r.node(t, depth+1, nil)
			if err != nil {
				return nil, err
			}
			n.kids = append(n.kids, k)
		case xml.CharData:
			if r.text -= len(t); r.text < 0 {
				return nil, errXMLText
			}
			tb.Write(t)
		case xml.EndElement:
			n.text = tb.String()
			return n, nil
		case xml.Directive:
			return nil, errXMLDoctyp
		}
	}
}

// parseXML reads a whole document into a tree.
func parseXML(b []byte, lim xmlLimits) (*xnode, error) {
	if err := checkTagSizes(b); err != nil {
		return nil, err
	}
	r := newXReader(b, lim)
	for {
		tok, err := r.dec.Token()
		if err != nil {
			return nil, xmlErr(err)
		}
		if err := r.pay(); err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n, err := r.node(t, 1, nil)
			return n, xmlErr(err)
		case xml.Directive:
			return nil, errXMLDoctyp
		}
	}
}

func xmlErr(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, errXMLTokens), errors.Is(err, errXMLText), errors.Is(err, errXMLDeep), errors.Is(err, errXMLDoctyp):
		return err
	}
	return fmt.Errorf("the file is not well-formed XML (%v)", err)
}

// streamXML reads a document and calls fn with each element named item, one at a
// time, wherever it is, so only one is in memory. Everything else is read and
// thrown away under the same budget and the same depth limit. Elements named in
// skip are counted inside each item and not kept; the counts are returned.
func streamXML(b []byte, item string, lim xmlLimits, skip []string, want map[string]bool, fn func(*xnode) error) (map[string]int, error) {
	if err := checkTagSizes(b); err != nil {
		return nil, err
	}
	r := newXReader(b, lim, skip...)
	depth := 0
	for {
		tok, err := r.dec.Token()
		if err == io.EOF {
			return r.skipped, nil
		}
		if err != nil {
			return r.skipped, xmlErr(err)
		}
		if err := r.pay(); err != nil {
			return r.skipped, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth++; depth > r.depth {
				return r.skipped, errXMLDeep
			}
			if tagged(t.Name) == item {
				n, err := r.node(t, depth+1, want)
				if err != nil {
					return r.skipped, xmlErr(err)
				}
				depth--
				if err := fn(n); err != nil {
					return r.skipped, err
				}
			}
		case xml.EndElement:
			depth--
		case xml.Directive:
			return r.skipped, errXMLDoctyp
		}
	}
}
