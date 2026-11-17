package text

import (
	"bytes"
	"regexp"
	"strings"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// safeLinks wraps goldmark's link parser. The editor's parser refuses a link or
// image whose destination is javascript:, vbscript:, file: or a data: URL that
// is not an image (markdown-it's validateLink); the brackets and the
// destination then stay in the text, and so count as words.
type safeLinks struct{ inner parser.InlineParser }

func (p safeLinks) Trigger() []byte { return p.inner.Trigger() }

// CloseBlock lets the link parser clear the label state it keeps per block.
func (p safeLinks) CloseBlock(parent ast.Node, block text.Reader, pc parser.Context) {
	if cb, ok := p.inner.(parser.CloseBlocker); ok {
		cb.CloseBlock(parent, block, pc)
	}
}

func (p safeLinks) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, seg := block.PeekLine()
	closing := len(line) > 0 && line[0] == ']'
	n := p.inner.Parse(parent, block, pc)
	if !closing || n == nil {
		return n
	}
	var dest []byte
	open := "["
	switch v := n.(type) {
	case *ast.Link:
		dest = v.Destination
	case *ast.Image:
		dest, open = v.Destination, "!["
	default:
		return n
	}
	if validDestination(dest) {
		return n
	}
	lit := &literal{}
	lit.AppendChild(lit, ast.NewString([]byte(open)))
	for c := n.FirstChild(); c != nil; c = n.FirstChild() {
		n.RemoveChild(n, c)
		lit.AppendChild(lit, c)
	}
	if _, pos := block.Position(); pos.Start > seg.Start && pos.Start <= len(block.Source()) {
		lit.AppendChild(lit, &rawText{v: block.Source()[seg.Start:pos.Start]})
	}
	return lit
}

// safeRefs wraps goldmark's link reference definitions. A definition whose
// destination the editor refuses is not a definition there: the paragraph that
// starts with one stays a paragraph, and its words count.
type safeRefs struct{ inner parser.ParagraphTransformer }

var reRefDef = regexp.MustCompile(`^ {0,3}\[(?:[^\]\\]|\\.)+\]:[ \t]*(?:<((?:[^<>\n\\]|\\.)*)>|(\S+))`)

func (p safeRefs) Transform(node *ast.Paragraph, reader text.Reader, pc parser.Context) {
	if lines := node.Lines(); lines.Len() > 0 {
		seg := lines.At(0)
		if m := reRefDef.FindSubmatch(seg.Value(reader.Source())); m != nil {
			dest := m[1]
			if dest == nil {
				dest = m[2]
			}
			if !validDestination(dest) {
				return
			}
		}
	}
	p.inner.Transform(node, reader, pc)
}

// safeTables wraps goldmark's table transformer. The editor's table needs a
// pipe in its header row (`x` over `-|` is text there, a table in goldmark).
type safeTables struct{ inner parser.ParagraphTransformer }

func (p safeTables) Transform(node *ast.Paragraph, reader text.Reader, pc parser.Context) {
	lines := node.Lines()
	for i := 1; i < lines.Len(); i++ {
		seg := lines.At(i)
		if !reTableDelim.Match(bytes.TrimRight(seg.Value(reader.Source()), "\r\n")) {
			continue
		}
		head := lines.At(i - 1)
		if bytes.IndexByte(head.Value(reader.Source()), '|') < 0 {
			return
		}
		break
	}
	p.inner.Transform(node, reader, pc)
}

var (
	reGoodData  = regexp.MustCompile(`^data:image/(?:gif|png|jpeg|webp);`)
	reBadScheme = regexp.MustCompile(`^(?:vbscript|javascript|file|data):`)
)

// validDestination is markdown-it's validateLink, applied to the destination
// as written (escapes and character references resolved) and again with the
// control characters a browser ignores inside a scheme removed.
func validDestination(raw []byte) bool {
	dest := unescapeAll(raw)
	ok := func(u string) bool {
		u = strings.ToLower(jsTrim(u))
		return reGoodData.MatchString(u) || !reBadScheme.MatchString(u)
	}
	return ok(dest) && ok(strings.Map(func(r rune) rune {
		if r <= 0x20 || (r >= 0x7F && r <= 0x9F) {
			return -1
		}
		return r
	}, dest))
}

// unescapeAll resolves backslash escapes and character references.
func unescapeAll(raw []byte) string {
	var b strings.Builder
	for i := 0; i < len(raw); {
		switch {
		case raw[i] == '\\' && i+1 < len(raw) && util.IsPunct(raw[i+1]):
			b.WriteByte(raw[i+1])
			i += 2
		case raw[i] == '&':
			if dec, n := entity(raw[i:]); n > 0 {
				b.WriteString(dec)
				i += n
			} else {
				b.WriteByte('&')
				i++
			}
		default:
			b.WriteByte(raw[i])
			i++
		}
	}
	return b.String()
}
