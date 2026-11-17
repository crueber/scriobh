package imports

import (
	"regexp"
	"strings"

	"golang.org/x/net/html"
)

// One pass over each XHTML file of a book, before anything is converted: where
// every element with an id sits (so a table of contents entry that names a
// fragment can cut the text there), where the headings are, which links go where
// (so footnotes can be found, and taken out of the flow), and how much text there
// is. It reads tokens, never builds a tree, and every number it keeps is bounded.

const (
	maxScanElems = 400_000   // elements recorded per file
	maxScanLinks = 200_000   // links recorded per file
	maxScanHeads = 100_000   // headings recorded per file
	maxScanBook  = 1_200_000 // elements recorded for the whole book
)

type xElem struct {
	tag        string
	id         string
	start, end int
	parent     int // index of the enclosing element, -1 at the top
	types      string
	role       string
	class      string
	dtitle     string // data-title: the raw title this program's own EPUB export writes on a section
	textBefore int
	noteBody   bool
	container  bool // a footnotes or endnotes container
}

type xLink struct {
	id, href, text string
	start, end     int
	types, role    string
	class          string
	elem           int
}

type xHead struct {
	level      int
	start, end int
	text       string
	textBefore int
	elem       int
}

// xScan is what one file holds.
type xScan struct {
	elems     []xElem
	byID      map[string]int
	links     []xLink
	heads     []xHead
	textBytes int // visible text, in bytes, white space not counted
	bodyTypes string
	titleTag  string
	images    int
	svgs      int
	body      int // offset of the first byte after <body ...>, 0 when there is none
	truncated bool
	size      int
}

var blockish = map[string]bool{
	"p": true, "div": true, "section": true, "article": true, "aside": true, "li": true, "dd": true, "dt": true, "blockquote": true,
	"h1": true, "h2": true, "h3": true, "h4": true, "h5": true, "h6": true, "td": true, "th": true, "figure": true, "header": true,
	"footer": true, "main": true, "nav": true, "ul": true, "ol": true, "table": true, "body": true, "pre": true, "hgroup": true,
}

// rawTextTags are the elements the HTML tokenizer reads to their closing tag. In
// XHTML one that is written self-closing (<script src="x"/>) is empty; the
// tokenizer would take everything after it for its content, so it is opened and
// closed explicitly first.
var rawSelfClose = regexp.MustCompile(`(?is)<((?:[a-z][a-z0-9]*:)?(?:script|style|title|textarea|iframe|noscript|noembed|noframes|xmp|plaintext))(\s[^<>]*?)?\s*/>`)

// prepareXHTML makes the text of a content file into what the walker and the scan
// both read: decoded, with self-closed raw-text elements spelled out. The same
// string is used for offsets everywhere.
func prepareXHTML(raw []byte) string {
	b, _ := decodeXML(append([]byte(nil), raw...))
	s := string(b)
	if !strings.Contains(s, "/>") {
		return s
	}
	return rawSelfClose.ReplaceAllString(s, "<$1$2></$1>")
}

func typeHas(types, want string) bool {
	for _, t := range strings.Fields(types) {
		if t == want {
			return true
		}
	}
	return false
}

func typeAny(types string, wants ...string) bool {
	for _, w := range wants {
		if typeHas(types, w) {
			return true
		}
	}
	return false
}

func classHas(class string, wants ...string) bool {
	for _, c := range strings.Fields(strings.ToLower(class)) {
		for _, w := range wants {
			if c == w {
				return true
			}
		}
	}
	return false
}

// noteBodyElem says an element is a footnote or endnote by what it declares.
func noteBodyElem(types, role string) bool {
	return typeAny(types, "footnote", "endnote", "rearnote", "note") || role == "doc-footnote" || role == "doc-endnote" || role == "doc-rearnote"
}

func noteContainerElem(types, role, class string) bool {
	return typeAny(types, "footnotes", "endnotes", "rearnotes") || role == "doc-footnotes" || role == "doc-endnotes" ||
		classHas(class, "footnotes", "endnotes", "footnote-list", "notes-section", "endnote-list")
}

func stripNS(tag string) string {
	if i := strings.IndexByte(tag, ':'); i > 0 {
		return tag[i+1:]
	}
	return tag
}

// scanXHTML reads src once. budget is how many elements the whole book may
// still record; it is reduced by what this file used.
func scanXHTML(src string, budget *int) *xScan {
	sc := &xScan{byID: map[string]int{}, size: len(src)}
	z := html.NewTokenizer(strings.NewReader(src))
	pos := 0
	var stack []int // open elements, as indexes into sc.elems
	skipText := ""  // inside script, style or title: its text is not the book's
	inHead := false
	inLink := -1
	headOpen := -1
	text := 0
	record := func(tag string, attrs map[string]string, start int, selfClose bool) int {
		if len(sc.elems) >= maxScanElems || *budget <= 0 {
			sc.truncated = true
			return -1
		}
		*budget--
		parent := -1
		if len(stack) > 0 {
			parent = stack[len(stack)-1]
		}
		e := xElem{tag: tag, id: attrs["id"], start: start, end: -1, parent: parent, types: attrs["epub:type"], role: attrs["role"], class: attrs["class"], dtitle: attrs["data-title"], textBefore: text}
		e.noteBody = noteBodyElem(e.types, e.role)
		e.container = noteContainerElem(e.types, e.role, e.class)
		sc.elems = append(sc.elems, e)
		i := len(sc.elems) - 1
		if e.id != "" {
			if _, dup := sc.byID[e.id]; !dup {
				sc.byID[e.id] = i
			}
		}
		return i
	}
	for {
		tt := z.Next()
		raw := z.Raw()
		start := pos
		pos += len(raw)
		if tt == html.ErrorToken {
			break
		}
		switch tt {
		case html.TextToken:
			if skipText != "" {
				if skipText == "title" && sc.titleTag == "" {
					sc.titleTag = cleanText(string(z.Text()), 200)
				}
				continue
			}
			t := strings.TrimSpace(string(z.Text()))
			if t == "" || inHead {
				continue
			}
			text += len(t)
			sc.textBytes += len(t)
			if inLink >= 0 && len(sc.links[inLink].text) < 100 {
				sc.links[inLink].text += t
			}
			if headOpen >= 0 && len(sc.heads[headOpen].text) < 600 {
				if sc.heads[headOpen].text != "" {
					sc.heads[headOpen].text += " "
				}
				sc.heads[headOpen].text += t
			}
		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := stripNS(string(name))
			var attrs map[string]string
			if hasAttr {
				attrs = map[string]string{}
				for {
					k, v, more := z.TagAttr()
					if _, dup := attrs[string(k)]; !dup && len(attrs) < 32 {
						attrs[string(k)] = string(v)
					}
					if !more {
						break
					}
				}
			}
			self := tt == html.SelfClosingTagToken || voidTags[tag]
			switch tag {
			case "script", "style", "title":
				if !self {
					skipText = tag
				}
			case "head":
				inHead = !self
			case "img", "image":
				sc.images++
			case "svg":
				sc.svgs++
			case "body":
				sc.bodyTypes = strings.TrimSpace(attrs["epub:type"] + " " + attrs["role"])
				sc.body = pos
			}
			if len(stack) >= htmlMaxDepth {
				continue
			}
			i := record(tag, attrs, start, self)
			if i < 0 {
				continue
			}
			if tag == "a" && attrs["href"] != "" && len(sc.links) < maxScanLinks {
				sc.links = append(sc.links, xLink{id: attrs["id"], href: attrs["href"], start: start, end: -1, types: attrs["epub:type"], role: attrs["role"], class: attrs["class"], elem: i})
				if !self {
					inLink = len(sc.links) - 1
				}
			}
			if len(tag) == 2 && tag[0] == 'h' && tag[1] >= '1' && tag[1] <= '6' && len(sc.heads) < maxScanHeads && !self {
				sc.heads = append(sc.heads, xHead{level: int(tag[1] - '0'), start: start, end: -1, textBefore: text, elem: i})
				headOpen = len(sc.heads) - 1
			}
			if self {
				sc.elems[i].end = pos
			} else {
				stack = append(stack, i)
			}
		case html.EndTagToken:
			name, _ := z.TagName()
			tag := stripNS(string(name))
			if skipText != "" && tag == skipText {
				skipText = ""
			}
			if tag == "head" {
				inHead = false
			}
			for j := len(stack) - 1; j >= 0; j-- {
				if sc.elems[stack[j]].tag != tag {
					continue
				}
				for k := len(stack) - 1; k >= j; k-- {
					e := &sc.elems[stack[k]]
					e.end = pos
					if e.tag == "a" {
						for li := len(sc.links) - 1; li >= 0 && li >= len(sc.links)-4; li-- {
							if sc.links[li].elem == stack[k] {
								sc.links[li].end = pos
								if inLink == li {
									inLink = -1
								}
							}
						}
					}
					if headOpen >= 0 && sc.heads[headOpen].elem == stack[k] {
						sc.heads[headOpen].end = pos
						headOpen = -1
					}
				}
				stack = stack[:j]
				break
			}
		}
	}
	for _, i := range stack {
		if sc.elems[i].end < 0 {
			sc.elems[i].end = len(src)
		}
	}
	for i := range sc.links {
		if sc.links[i].end < 0 {
			sc.links[i].end = sc.elems[sc.links[i].elem].end
		}
	}
	for i := range sc.heads {
		if sc.heads[i].end < 0 {
			sc.heads[i].end = sc.elems[sc.heads[i].elem].end
		}
	}
	for i := range sc.elems {
		if sc.elems[i].end < 0 {
			sc.elems[i].end = len(src)
		}
	}
	return sc
}

// blockOf is the element a cut at element i begins: i itself when it is a block,
// else the nearest block it sits in.
func (sc *xScan) blockOf(i int) int {
	for j := i; j >= 0; j = sc.elems[j].parent {
		if blockish[sc.elems[j].tag] {
			return j
		}
		if sc.elems[j].parent < 0 {
			break
		}
	}
	return i
}

// linksIn lists the links that begin inside [a, b).
func (sc *xScan) linksIn(a, b int) []int {
	lo, hi := 0, len(sc.links)
	for lo < hi {
		m := (lo + hi) / 2
		if sc.links[m].start < a {
			lo = m + 1
		} else {
			hi = m
		}
	}
	var out []int
	for i := lo; i < len(sc.links) && sc.links[i].start < b; i++ {
		out = append(out, i)
		if len(out) > 1000 {
			break
		}
	}
	return out
}

// firstHeadingIn is the first heading that begins in [a, b).
func (sc *xScan) firstHeadingIn(a, b int) (xHead, bool) {
	lo, hi := 0, len(sc.heads)
	for lo < hi {
		m := (lo + hi) / 2
		if sc.heads[m].start < a {
			lo = m + 1
		} else {
			hi = m
		}
	}
	if lo < len(sc.heads) && sc.heads[lo].start < b {
		return sc.heads[lo], true
	}
	return xHead{}, false
}

// firstElemFrom is the index of the first element that begins at or after a.
func (sc *xScan) firstElemFrom(a int) int {
	lo, hi := 0, len(sc.elems)
	for lo < hi {
		m := (lo + hi) / 2
		if sc.elems[m].start < a {
			lo = m + 1
		} else {
			hi = m
		}
	}
	return lo
}

// textAt is how much text comes before offset a (the text count of the element
// that begins there, or at the nearest earlier recorded element).
func (sc *xScan) textAt(a int) int {
	lo, hi := 0, len(sc.elems)
	for lo < hi {
		m := (lo + hi) / 2
		if sc.elems[m].start < a {
			lo = m + 1
		} else {
			hi = m
		}
	}
	if lo < len(sc.elems) {
		return sc.elems[lo].textBefore
	}
	return sc.textBytes
}

// textBetween approximates the visible text in [a, b) of the scan's own file.
func (sc *xScan) textBetween(a, b int) int {
	d := sc.textAt(b) - sc.textAt(a)
	if b >= sc.size {
		d = sc.textBytes - sc.textAt(a)
	}
	return max(d, 0)
}
