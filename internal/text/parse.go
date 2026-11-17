package text

import (
	"bytes"
	"regexp"
	"strings"
	"sync"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// The Markdown the editor models is CommonMark plus a handful of constructs
// of its own (web/editor/src/md-plugins.js). Everything the editor holds as a
// "source block" (raw HTML, tables, front matter, `:::` directives, link
// reference definitions) is invisible to the counter and to search, so the
// parser here only has to find where those blocks start and stop. The inline
// constructs that matter are wiki links and footnote references.
//
// goldmark parses the CommonMark part. Four small extensions teach it the
// rest: a directive block, a footnote definition container, and one inline
// parser for `[^label]` and `[[Title|alias]]`.

var (
	kindDirective   = ast.NewNodeKind("ScriobhDirective")
	kindFootnoteDef = ast.NewNodeKind("ScriobhFootnoteDef")
	kindWikiLink    = ast.NewNodeKind("ScriobhWikiLink")
	kindFootnoteRef = ast.NewNodeKind("ScriobhFootnoteRef")
	kindLiteral     = ast.NewNodeKind("ScriobhLiteral")

	// The directive blocks that were opened but never closed, by the source
	// offset of their opening line, so the document can be parsed again with
	// them treated as ordinary text (an unclosed `:::` is not a block).
	unclosedKey = parser.NewContextKey()
	disabledKey = parser.NewContextKey()
)

// directive is a `:::name` ... `:::` block: held verbatim, so never text.
type directive struct {
	ast.BaseBlock
	colons  int
	start   int
	stop    int // end of the last line read, so a renderer can show the block as written
	closed  bool
	noBlank bool // an empty line ends the search for the closing line
	broken  bool
}

func (*directive) Kind() ast.NodeKind { return kindDirective }
func (*directive) IsRaw() bool        { return true }
func (n *directive) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

// footnoteDef is `[^label]: ...` with its indented continuation blocks.
type footnoteDef struct {
	ast.BaseBlock
	label string
}

func (*footnoteDef) Kind() ast.NodeKind { return kindFootnoteDef }
func (n *footnoteDef) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

// wikiLink is `[[Title]]` or `[[Title|alias]]`; label is what it shows.
type wikiLink struct {
	ast.BaseInline
	label  string
	target string // the trimmed title before the bar
	alias  string // the trimmed text after the bar, or ""
	at, n  int    // where the link starts in the source, and its length in bytes
}

func (*wikiLink) Kind() ast.NodeKind { return kindWikiLink }
func (n *wikiLink) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"label": n.label}, nil)
}

// footnoteRef is `[^label]`: a zero-width atom in the editor.
type footnoteRef struct {
	ast.BaseInline
	label string
}

func (*footnoteRef) Kind() ast.NodeKind { return kindFootnoteRef }
func (n *footnoteRef) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

// literal holds the children of a link the editor refuses to make (an unsafe
// destination): the brackets and the destination stay in the text.
type literal struct{ ast.BaseInline }

func (*literal) Kind() ast.NodeKind { return kindLiteral }
func (n *literal) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, nil, nil)
}

// ---- directive ----------------------------------------------------------------

type directiveParser struct{}

func (directiveParser) Trigger() []byte { return []byte{':'} }

func (directiveParser) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	line, segment := reader.PeekLine()
	pos := pc.BlockOffset()
	if pos < 0 {
		return nil, parser.NoChildren
	}
	rest := trimEOL(line[pos:])
	n := 0
	for n < len(rest) && rest[n] == ':' {
		n++
	}
	if n < 3 {
		return nil, parser.NoChildren
	}
	tail := rest[n:]
	if len(tail) > 0 {
		// `[ \t]*[^\s:].*`: some text after the colons.
		i := 0
		for i < len(tail) && (tail[i] == ' ' || tail[i] == '\t') {
			i++
		}
		if i == len(tail) || tail[i] == ':' {
			return nil, parser.NoChildren
		}
		if r := firstRune(tail[i:]); isJSSpace(r) {
			return nil, parser.NoChildren
		}
	}
	d := &directive{colons: n, start: segment.Start, stop: segment.Stop}
	// The editor's parser looks for the closing line with a test that an empty
	// line fails inside a list item or a footnote (its indentation, zero, is
	// less than the container's), so there a directive cannot span a blank
	// line: it is ordinary text. Mirrored here so the counts agree.
	switch parent.Kind() {
	case ast.KindListItem, kindFootnoteDef:
		d.noBlank = true
	}
	if off, ok := pc.Get(disabledKey).(map[int]bool); ok && (off[d.start] || off[-1]) {
		return nil, parser.NoChildren
	}
	reader.AdvanceToEOL() // the whole line is part of the block
	return d, parser.NoChildren
}

func (directiveParser) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	d := node.(*directive)
	if d.closed {
		return parser.Close
	}
	line, seg := reader.PeekLine()
	if d.noBlank && len(trimEOL(line)) == 0 {
		d.broken = true
		return parser.Close
	}
	d.stop = seg.Stop
	rest := bytes.TrimLeft(trimEOL(line), " \t")
	if closer(rest, d.colons) {
		d.closed = true
	}
	reader.AdvanceToEOL()
	return parser.Continue | parser.NoChildren
}

// closer reports whether line is `:::` with at least need colons and nothing
// else but trailing blanks.
func closer(line []byte, need int) bool {
	n := 0
	for n < len(line) && line[n] == ':' {
		n++
	}
	if n < need {
		return false
	}
	return len(bytes.Trim(line[n:], " \t")) == 0
}

func (directiveParser) Close(node ast.Node, reader text.Reader, pc parser.Context) {
	d := node.(*directive)
	if d.closed {
		return
	}
	list, _ := pc.Get(unclosedKey).([]int)
	pc.Set(unclosedKey, append(list, d.start))
}

func (directiveParser) CanInterruptParagraph() bool { return true }
func (directiveParser) CanAcceptIndentedLine() bool { return false }

// ---- footnote definition -------------------------------------------------------

type footnoteDefParser struct{}

func (footnoteDefParser) Trigger() []byte { return []byte{'['} }

func (footnoteDefParser) Open(parent ast.Node, reader text.Reader, pc parser.Context) (ast.Node, parser.State) {
	line, segment := reader.PeekLine()
	pos := pc.BlockOffset()
	if pos < 0 || pos+4 > len(line) || line[pos] != '[' || line[pos+1] != '^' {
		return nil, parser.NoChildren
	}
	// The editor's table keeps reading rows until something that can end a
	// block quote does, and a footnote definition is not one of those.
	if last := pc.LastOpenedBlock(); last.Node != nil {
		if p, ok := last.Node.(*ast.Paragraph); ok && tableInProgress(p, reader.Source()) {
			return nil, parser.NoChildren
		}
	}
	i := pos + 2
	for ; i < len(line); i++ {
		c := line[i]
		if c == ' ' || c == '\t' || c == '[' || c == '\n' || c == '\r' {
			return nil, parser.NoChildren
		}
		if c == ']' {
			break
		}
	}
	if i == pos+2 || i+1 >= len(line) || line[i+1] != ':' {
		return nil, parser.NoChildren
	}
	next := i + 2
	padding := segment.Padding
	for next < len(line) && (line[next] == ' ' || line[next] == '\t') {
		next++
	}
	if next >= len(line) || line[next] == '\n' || line[next] == '\r' {
		reader.AdvanceToEOL()
		return &footnoteDef{label: string(line[pos+2 : i])}, parser.HasChildren
	}
	reader.AdvanceAndSetPadding(next, padding)
	return &footnoteDef{label: string(line[pos+2 : i])}, parser.HasChildren
}

var reTableDelim = regexp.MustCompile(`^[ \t]*\|?[ \t]*:?-+:?[ \t]*(?:\|[ \t]*:?-+:?[ \t]*)*\|?[ \t]*$`)

// tableInProgress reports whether the open paragraph is the start of a table:
// a header row with a pipe, then a delimiter row.
func tableInProgress(p *ast.Paragraph, src []byte) bool {
	lines := p.Lines()
	if lines.Len() < 2 {
		return false
	}
	first, second := lines.At(0), lines.At(1)
	return bytes.IndexByte(first.Value(src), '|') >= 0 &&
		reTableDelim.Match(bytes.TrimRight(second.Value(src), "\r\n"))
}

func (footnoteDefParser) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	line, _ := reader.PeekLine()
	if util.IsBlank(line) {
		return parser.Continue | parser.HasChildren
	}
	childpos, padding := util.IndentPosition(line, reader.LineOffset(), 4)
	if childpos < 0 {
		return parser.Close
	}
	reader.AdvanceAndSetPadding(childpos, padding)
	return parser.Continue | parser.HasChildren
}

func (footnoteDefParser) Close(ast.Node, text.Reader, parser.Context) {}
func (footnoteDefParser) CanInterruptParagraph() bool                 { return true }
func (footnoteDefParser) CanAcceptIndentedLine() bool                 { return false }

// ---- wiki links and footnote references -----------------------------------------

// maxBracketSpan is the longest `[^label]` or `[[Title|alias]]` that is read.
const maxBracketSpan = 1100

type bracketParser struct{}

func (bracketParser) Trigger() []byte { return []byte{'['} }

func (bracketParser) Parse(parent ast.Node, block text.Reader, pc parser.Context) ast.Node {
	line, seg := block.PeekLine()
	if len(line) < 3 || line[0] != '[' {
		return nil
	}
	// Look no further than a footnote label or a wiki link title can run: past
	// that the answer is no, and scanning to the end of a very long line for
	// every `[` would make a line of brackets quadratic.
	if len(line) > maxBracketSpan {
		line = line[:maxBracketSpan]
	}
	end := bytes.IndexAny(line, "\r\n")
	if end < 0 {
		end = len(line)
	}
	line = line[:end]
	switch line[1] {
	case '^': // [^label]: no spaces, no nested brackets, not empty
		for i := 2; i < len(line); i++ {
			switch line[i] {
			case ' ', '\t', '[':
				return nil
			case ']':
				if i == 2 {
					return nil
				}
				block.Advance(i + 1)
				return &footnoteRef{label: string(line[2:i])}
			}
		}
	case '[': // [[target]] or [[target|alias]]
		for i := 2; i < len(line); i++ {
			switch line[i] {
			case '[':
				return nil
			case ']':
				if i+1 >= len(line) || line[i+1] != ']' {
					return nil
				}
				inner := string(line[2:i])
				target, alias := inner, ""
				if bar := strings.IndexByte(inner, '|'); bar >= 0 {
					target, alias = inner[:bar], jsTrim(inner[bar+1:])
				}
				target = jsTrim(target)
				if target == "" {
					return nil
				}
				label := alias
				if label == "" {
					label = target
				}
				block.Advance(i + 2)
				return &wikiLink{label: label, target: target, alias: alias, at: seg.Start, n: i + 2}
			}
		}
	}
	return nil
}

// rawText is text taken straight from the source, escapes and character
// references still unresolved (the tail of a refused link).
type rawText struct {
	ast.BaseInline
	v []byte
}

var kindRawText = ast.NewNodeKind("ScriobhRawText")

func (*rawText) Kind() ast.NodeKind { return kindRawText }
func (n *rawText) Dump(source []byte, level int) {
	ast.DumpHelper(n, source, level, map[string]string{"text": string(n.v)}, nil)
}

// ---- the parser ---------------------------------------------------------------------

var (
	parserOnce sync.Once
	theParser  parser.Parser
	htmlOnce   sync.Once
	htmlParser parser.Parser
)

// newParser returns the one shared parser behind the counts and the search
// text: CommonMark, GFM tables (held as source blocks), and the editor's own
// constructs.
func newParser() parser.Parser {
	parserOnce.Do(func() { theParser = buildParser(false) })
	return theParser
}

// newHTMLParser is newParser plus strikethrough: the editor reads `~~x~~` as
// marked text, which changes no count, so only the HTML renderer asks for it.
func newHTMLParser() parser.Parser {
	htmlOnce.Do(func() { htmlParser = buildParser(true) })
	return htmlParser
}

func buildParser(strike bool) parser.Parser {
	inline := parser.DefaultInlineParsers()
	for i, v := range inline {
		if lp, ok := v.Value.(parser.InlineParser); ok && v.Priority == 200 {
			inline[i] = util.Prioritized(safeLinks{inner: lp}, 200)
		}
	}
	blocks := append(parser.DefaultBlockParsers(),
		util.Prioritized(directiveParser{}, 50),
		util.Prioritized(footnoteDefParser{}, 51))
	inline = append(inline, util.Prioritized(bracketParser{}, 150))
	if strike {
		inline = append(inline, util.Prioritized(extension.NewStrikethroughParser(), 500))
	}
	transformers := parser.DefaultParagraphTransformers()
	for i, v := range transformers {
		if pt, ok := v.Value.(parser.ParagraphTransformer); ok {
			transformers[i] = util.Prioritized(safeRefs{inner: pt}, v.Priority)
		}
	}
	transformers = append(transformers, util.Prioritized(safeTables{inner: extension.NewTableParagraphTransformer()}, 200))
	return parser.NewParser(
		parser.WithBlockParsers(blocks...),
		parser.WithInlineParsers(inline...),
		parser.WithParagraphTransformers(transformers...),
		parser.WithASTTransformers(util.Prioritized(extension.NewTableASTTransformer(), 0)),
	)
}

// parseDoc parses Markdown (already normalised) into a goldmark tree with the
// editor's constructs. An unclosed directive is not a block, so a document that
// has one is parsed again with that opening line treated as text.
func parseDoc(src []byte) ast.Node { return parseWith(newParser(), src) }

func parseWith(p parser.Parser, src []byte) ast.Node {
	disabled := map[int]bool{}
	for pass := 0; ; pass++ {
		if pass == 4 {
			// A hostile document of many unclosed openers would cost a pass each.
			disabled[-1] = true
		}
		pc := parser.NewContext()
		pc.Set(disabledKey, disabled)
		doc := p.Parse(text.NewReader(src), parser.WithContext(pc))
		open, _ := pc.Get(unclosedKey).([]int)
		if len(open) == 0 {
			return doc
		}
		for _, at := range open {
			disabled[at] = true
		}
	}
}

func trimEOL(b []byte) []byte { return bytes.TrimRight(b, "\r\n") }
