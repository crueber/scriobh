package text

import (
	"strings"

	"github.com/yuin/goldmark/ast"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/util"
)

// Structure is the manuscript as a small tree of neutral nodes, for the writers
// that cannot be handed HTML (DOCX) or want XHTML of their own (EPUB). It is
// built from the same parse tree RenderHTML walks, with the same rules:
//
//   - Raw HTML, inline and block, is shown as text, never interpreted. HTML
//     comments (the alternatives annotation among them) are dropped.
//   - Front matter is dropped. %%Private notes%% are dropped unless asked for.
//   - A link keeps its address only for http, https, mailto and in-page targets.
//   - An image keeps its raw destination; the writer decides what it may embed.
//   - Wiki links are their label. Strikethrough is read.
//
// Text is exactly what the reader sees: backslash escapes and character
// references are resolved, and a soft line break is a "\n" in the text that a
// writer treats as a space. No Markdown syntax remains in it.

// NodeKind is the kind of a block node.
type NodeKind uint8

// The block kinds.
const (
	NPara    NodeKind = iota // Inl
	NHeading                 // Level 1..6, Inl
	NQuote                   // Kids
	NList                    // Items, Ordered, Start
	NRule                    // a thematic break: a scene break
	NCode                    // Text, Lang
	NSource                  // Text: a block the editor holds as source (raw HTML, a directive), shown as written
	NTable                   // Rows
)

// InlineKind is the kind of an inline node.
type InlineKind uint8

// The inline kinds.
const (
	IText   InlineKind = iota // Text
	ICode                     // Text
	IBreak                    // a hard line break
	IEmph                     // Kids
	IStrong                   // Kids
	IStrike                   // Kids
	ILink                     // Text is the address, Kids the label
	IImage                    // Text is the destination as written, Alt the description
	INote                     // Num is the footnote's number
)

// Inline is one inline node.
type Inline struct {
	Kind InlineKind
	Text string
	Alt  string
	Num  int
	Kids []Inline
}

// Cell is one table cell.
type Cell struct {
	Align string // "", "left", "right", "center"
	Inl   []Inline
}

// Row is one table row.
type Row struct {
	Head  bool
	Cells []Cell
}

// Node is one block node.
type Node struct {
	Kind    NodeKind
	Level   int
	Inl     []Inline
	Kids    []Node
	Items   [][]Node
	Ordered bool
	Start   int
	Text    string
	Lang    string
	Rows    []Row
}

// Footnote is a footnote's text, with the number its reference shows.
type Footnote struct {
	Num   int
	Nodes []Node
}

// Doc is one document's structure.
type Doc struct {
	Nodes []Node
	Notes []Footnote
}

// StructOptions shapes Structure.
type StructOptions struct {
	// Private keeps the text of %%private notes%% as ordinary text. The compile
	// pipeline never sets it.
	Private bool
	// NoteStart is the number the footnotes of this document start after, so
	// the documents of one chapter, or of one book, number continuously.
	NoteStart int
	// NoteBudget is the bytes of footnote text that may still be copied (one
	// copy per reference). Nil gives this call MaxNoteCopies of its own; a
	// compile passes one budget through every document, so the copies of a whole
	// book are bounded, not those of each document.
	NoteBudget *int
}

// maxStructDepth bounds nesting (quotes inside lists inside quotes). Deeper
// text is flattened rather than recursed into: the parser allows what a
// writer's own element nesting must not.
const maxStructDepth = 24

// Structure builds md's structure.
func Structure(md string, o StructOptions) Doc {
	src := sanitize(body(normalize(md)))
	if !o.Private {
		src = dropSpanningNotes(src)
	}
	doc, ok := safeParseWith(src)
	if !ok {
		t := reComment.ReplaceAllString(string(src), "")
		if !o.Private {
			t = rePrivateSpan.ReplaceAllString(t, "")
		}
		t = strings.TrimSpace(t)
		if t == "" {
			return Doc{}
		}
		return Doc{Nodes: []Node{{Kind: NPara, Inl: []Inline{{Kind: IText, Text: t}}}}}
	}
	b := &irBuilder{htmlRenderer: htmlRenderer{src: src, o: HTMLOptions{Private: o.Private}, refs: map[string]int{}, defs: map[string]*footnoteDef{},
		hiddenRef: map[string]bool{}, hasPrivate: strings.Contains(string(src), "%%")}, start: o.NoteStart, next: o.NoteStart, budget: o.NoteBudget, defBytes: map[string]int{}}
	if b.budget == nil {
		own := MaxNoteCopies
		b.budget = &own
	}
	b.findDefs(doc)
	out := Doc{Nodes: b.blocks(doc, 0)}
	out.Notes = b.footnotes()
	return out
}

// irBuilder reuses the HTML renderer's private-note plan and footnote tables.
//
// Every reference to a footnote gets a footnote of its own, numbered in the
// order the references come: Markdown lets one note be referenced twice, but a
// printed page and a Word footnote cannot point at the same note twice, so the
// note's text is copied for each reference. Notes inside notes are dropped (a
// footnote has no footnotes), and the copies are bounded.
type irBuilder struct {
	htmlRenderer
	start    int
	next     int
	refList  []noteRef
	inNote   bool
	budget   *int // bytes of note text that may still be copied
	defBytes map[string]int
}

type noteRef struct {
	num   int
	label string
}

const (
	maxNoteRefs = 20_000
	// MaxNoteCopies bounds the footnote text copied for repeated references.
	MaxNoteCopies = 16 << 20
)

func (b *irBuilder) defSize(label string) int {
	if n, ok := b.defBytes[label]; ok {
		return n
	}
	n := 64
	if d, ok := b.defs[label]; ok {
		n += len(plainBlock(d, b.src))
	}
	b.defBytes[label] = n
	return n
}

func (b *irBuilder) footnotes() []Footnote {
	if len(b.defs) == 0 {
		return nil
	}
	b.inNote = true
	var out []Footnote
	seen := map[string]bool{}
	for _, r := range b.refList {
		seen[r.label] = true
		out = append(out, Footnote{Num: r.num, Nodes: b.blocks(b.defs[r.label], 1)})
	}
	// Notes nothing refers to: kept after the rest, numbered on, for a reader
	// that wants every word; the writers leave out what no reference points at.
	for _, l := range b.defOrder {
		if !seen[l] && !b.hiddenRef[l] && *b.budget > 0 {
			b.next++
			*b.budget -= b.defSize(l)
			out = append(out, Footnote{Num: b.next, Nodes: b.blocks(b.defs[l], 1)})
		}
	}
	return out
}

func (b *irBuilder) blocks(n ast.Node, depth int) []Node {
	var out []Node
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		out = append(out, b.block(c, depth)...)
	}
	return out
}

func (b *irBuilder) block(n ast.Node, depth int) []Node {
	if depth > maxStructDepth {
		// Too deep to nest: what is inside is shown as paragraphs of its own text.
		if n.Type() == ast.TypeInline {
			return nil
		}
		if t := strings.TrimSpace(plainBlock(n, b.src)); t != "" {
			return []Node{{Kind: NPara, Inl: []Inline{{Kind: IText, Text: t}}}}
		}
		return nil
	}
	switch v := n.(type) {
	case *footnoteDef:
		return nil
	case *directive:
		return []Node{{Kind: NSource, Text: strings.TrimRight(string(b.src[v.start:v.stop]), "\r\n")}}
	case *ast.Heading:
		lv := v.Level
		if lv > 6 {
			lv = 6
		}
		return []Node{{Kind: NHeading, Level: lv, Inl: b.inlineBlock2(v)}}
	case *ast.Paragraph:
		inl := b.inlineBlock2(v)
		if len(inl) == 0 {
			return nil
		}
		return []Node{{Kind: NPara, Inl: inl}}
	case *ast.TextBlock:
		inl := b.inlineBlock2(v)
		if len(inl) == 0 {
			return nil
		}
		return []Node{{Kind: NPara, Inl: inl}}
	case *ast.Blockquote:
		return []Node{{Kind: NQuote, Kids: b.blocks(v, depth+1)}}
	case *ast.List:
		l := Node{Kind: NList, Ordered: v.IsOrdered(), Start: 1}
		if v.IsOrdered() && v.Start >= 0 {
			l.Start = v.Start
		}
		for it := v.FirstChild(); it != nil; it = it.NextSibling() {
			l.Items = append(l.Items, b.blocks(it, depth+1))
		}
		return []Node{l}
	case *ast.ListItem:
		return b.blocks(v, depth+1)
	case *ast.ThematicBreak:
		return []Node{{Kind: NRule}}
	case *ast.FencedCodeBlock:
		lang := ""
		if l := v.Language(b.src); l != nil {
			lang = safeClass(string(l))
		}
		return []Node{{Kind: NCode, Lang: lang, Text: codeText(v, b.src)}}
	case *ast.CodeBlock:
		return []Node{{Kind: NCode, Text: codeText(v, b.src)}}
	case *ast.HTMLBlock:
		var raw strings.Builder
		for i := 0; i < v.Lines().Len(); i++ {
			ln := v.Lines().At(i)
			raw.Write(ln.Value(b.src))
		}
		if v.HasClosure() {
			raw.Write(v.ClosureLine.Value(b.src))
		}
		t := strings.TrimRight(raw.String(), "\r\n")
		if isComment(t) {
			return nil
		}
		return []Node{{Kind: NSource, Text: t}}
	case *extast.Table:
		t := Node{Kind: NTable}
		for c := v.FirstChild(); c != nil; c = c.NextSibling() {
			switch r := c.(type) {
			case *extast.TableHeader:
				t.Rows = append(t.Rows, b.row(r, true))
			case *extast.TableRow:
				t.Rows = append(t.Rows, b.row(r, false))
			}
		}
		return []Node{t}
	default:
		if n.Type() == ast.TypeInline {
			return nil
		}
		return b.blocks(n, depth+1)
	}
}

func (b *irBuilder) row(r ast.Node, head bool) Row {
	row := Row{Head: head}
	for c := r.FirstChild(); c != nil; c = c.NextSibling() {
		cell, ok := c.(*extast.TableCell)
		if !ok {
			continue
		}
		al := ""
		switch cell.Alignment {
		case extast.AlignLeft:
			al = "left"
		case extast.AlignRight:
			al = "right"
		case extast.AlignCenter:
			al = "center"
		}
		row.Cells = append(row.Cells, Cell{Align: al, Inl: b.inlineBlock2(cell)})
	}
	return row
}

func codeText(n ast.Node, src []byte) string {
	var s strings.Builder
	for i := 0; i < n.Lines().Len(); i++ {
		ln := n.Lines().At(i)
		s.Write(ln.Value(src))
	}
	return strings.TrimRight(s.String(), "\r\n")
}

// plainBlock is the text of a node too deeply nested to keep its shape.
func plainBlock(n ast.Node, src []byte) string {
	var parts []string
	var walk func(x ast.Node)
	walk = func(x ast.Node) {
		for c := x.FirstChild(); c != nil; c = c.NextSibling() {
			switch c.(type) {
			case *ast.Paragraph, *ast.TextBlock, *ast.Heading:
				if t := plainOf(c, src); t != "" {
					parts = append(parts, t)
				}
			default:
				walk(c)
			}
		}
	}
	walk(n)
	return strings.Join(parts, "\n")
}

// ---- inline ---------------------------------------------------------------------------

func (b *irBuilder) inlineBlock2(n ast.Node) []Inline {
	b.plan = nil
	if b.hasPrivate {
		b.plan = b.planPrivate(n)
	}
	out := b.inlines(n)
	b.plan = nil
	return trimInlines(out)
}

func (b *irBuilder) inlines(n ast.Node) []Inline {
	var out []Inline
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		out = append(out, b.inline(c)...)
	}
	return out
}

// hidden reports whether a leaf sits in a private note that is not wanted.
func (b *irBuilder) hiddenLeaf(n ast.Node) bool { return b.private(n) && !b.o.Private }

func (b *irBuilder) wrap(kind InlineKind, n ast.Node) []Inline {
	kids := b.inlines(n)
	if len(kids) == 0 {
		return nil
	}
	return []Inline{{Kind: kind, Kids: kids}}
}

func (b *irBuilder) inline(n ast.Node) []Inline {
	switch v := n.(type) {
	case *ast.Text:
		return b.textNode(v)
	case *ast.String:
		if b.hiddenLeaf(n) {
			return nil
		}
		return []Inline{{Kind: IText, Text: string(v.Value)}}
	case *rawText:
		if b.hiddenLeaf(n) {
			return nil
		}
		return []Inline{{Kind: IText, Text: resolveText(v.v)}}
	case *ast.CodeSpan:
		if b.hiddenLeaf(n) {
			return nil
		}
		var s strings.Builder
		for c := v.FirstChild(); c != nil; c = c.NextSibling() {
			if t, ok := c.(*ast.Text); ok {
				s.WriteString(strings.ReplaceAll(string(t.Segment.Value(b.src)), "\n", " "))
			}
		}
		return []Inline{{Kind: ICode, Text: s.String()}}
	case *ast.Emphasis:
		if v.Level >= 2 {
			return b.wrap(IStrong, v)
		}
		return b.wrap(IEmph, v)
	case *extast.Strikethrough:
		return b.wrap(IStrike, v)
	case *ast.Link:
		kids := b.inlines(v)
		if len(kids) == 0 {
			return nil
		}
		href, ok := safeHref(v.Destination)
		if !ok {
			return kids
		}
		return []Inline{{Kind: ILink, Text: unescapeHref(href), Kids: kids}}
	case *ast.AutoLink:
		if b.hiddenLeaf(n) {
			return nil
		}
		label := string(v.Label(b.src))
		if href, ok := safeHref(v.URL(b.src)); ok {
			return []Inline{{Kind: ILink, Text: unescapeHref(href), Kids: []Inline{{Kind: IText, Text: label}}}}
		}
		return []Inline{{Kind: IText, Text: label}}
	case *ast.Image:
		if b.hiddenLeaf(n) {
			return nil
		}
		return []Inline{{Kind: IImage, Text: strings.TrimSpace(unescapeAll(v.Destination)), Alt: b.noPrivate(plainOf(v, b.src))}}
	case *ast.RawHTML:
		if b.hiddenLeaf(n) {
			return nil
		}
		var raw strings.Builder
		for i := 0; i < v.Segments.Len(); i++ {
			seg := v.Segments.At(i)
			raw.Write(seg.Value(b.src))
		}
		if strings.HasPrefix(raw.String(), "<!--") {
			return nil
		}
		return []Inline{{Kind: IText, Text: raw.String()}}
	case *literal:
		return b.inlines(v)
	case *wikiLink:
		if b.hiddenLeaf(n) {
			return nil
		}
		return []Inline{{Kind: IText, Text: b.noPrivate(v.label)}}
	case *footnoteRef:
		if b.private(n) && !b.o.Private {
			b.hiddenRef[v.label] = true
			return nil
		}
		if _, ok := b.defs[v.label]; !ok || b.inNote {
			return nil // a reference with no note says nothing in a printed page, and a note has no notes
		}
		size := b.defSize(v.label)
		if len(b.refList) >= maxNoteRefs || *b.budget < size {
			return nil
		}
		*b.budget -= size
		b.next++
		b.refList = append(b.refList, noteRef{b.next, v.label})
		return []Inline{{Kind: INote, Num: b.next}}
	default:
		return b.inlines(n)
	}
}

func (b *irBuilder) textNode(v *ast.Text) []Inline {
	segs := []span{{start: v.Segment.Start, stop: v.Segment.Stop}}
	if b.plan != nil {
		if s, ok := b.plan.text[v]; ok {
			segs = s
		}
	}
	var out []Inline
	var sb strings.Builder
	flush := func() {
		if sb.Len() > 0 {
			out = append(out, Inline{Kind: IText, Text: sb.String()})
			sb.Reset()
		}
	}
	lastHidden := false
	for _, s := range segs {
		if s.stop <= s.start {
			continue
		}
		switch {
		case s.private && !b.o.Private:
			lastHidden = true
		default:
			sb.WriteString(resolveText(b.src[s.start:s.stop]))
			lastHidden = false
		}
	}
	switch {
	case v.HardLineBreak():
		flush()
		out = append(out, Inline{Kind: IBreak})
	case v.SoftLineBreak():
		if !lastHidden {
			sb.WriteString("\n")
		}
	}
	flush()
	return out
}

// noPrivate removes the %%private notes%% inside a string that was read whole
// (an image's description, a wiki link's label), by the pairing rule: the
// delimiters in order, two by two, an empty pair and an unpaired last one left.
func (b *irBuilder) noPrivate(s string) string {
	if b.o.Private || !strings.Contains(s, "%%") {
		return s
	}
	var at []int
	for i := 0; i+1 < len(s); {
		if s[i] == '%' && s[i+1] == '%' {
			at = append(at, i)
			i += 2
		} else {
			i++
		}
	}
	var out strings.Builder
	last := 0
	for h := 0; h+1 < len(at); {
		a, c := at[h], at[h+1]
		if c == a+2 {
			h += 2
			continue
		}
		out.WriteString(s[last:a])
		last = c + 2
		h += 2
	}
	out.WriteString(s[last:])
	return out.String()
}

// resolveText resolves raw inline text's backslash escapes and character
// references, as writeText does for HTML.
func resolveText(raw []byte) string {
	if !strings.ContainsAny(string(raw), "\\&") {
		return string(raw)
	}
	var s strings.Builder
	for i := 0; i < len(raw); {
		switch c := raw[i]; {
		case c == '\\' && i+1 < len(raw) && util.IsPunct(raw[i+1]):
			s.WriteByte(raw[i+1])
			i += 2
		case c == '&':
			if dec, n := entity(raw[i:]); n > 0 {
				s.WriteString(dec)
				i += n
			} else {
				s.WriteByte(c)
				i++
			}
		default:
			s.WriteByte(c)
			i++
		}
	}
	return s.String()
}

// unescapeHref undoes the HTML escaping safeHref applied to an address that was
// meant for an attribute.
func unescapeHref(h string) string {
	r := strings.NewReplacer("&amp;", "&", "&lt;", "<", "&gt;", ">", "&#34;", `"`, "&#39;", "'", "&quot;", `"`)
	return r.Replace(h)
}

// trimInlines drops a trailing soft break and the whitespace that ends a block.
func trimInlines(in []Inline) []Inline {
	for len(in) > 0 {
		last := &in[len(in)-1]
		if last.Kind == IBreak {
			in = in[:len(in)-1]
			continue
		}
		if last.Kind == IText {
			last.Text = strings.TrimRight(last.Text, " \t\n")
			if last.Text == "" {
				in = in[:len(in)-1]
				continue
			}
		}
		break
	}
	for len(in) > 0 && in[0].Kind == IText {
		in[0].Text = strings.TrimLeft(in[0].Text, " \t\n")
		if in[0].Text == "" {
			in = in[1:]
			continue
		}
		break
	}
	return in
}

// PlainText is the visible text of nodes, one paragraph per line, for counts,
// previews and tests.
func PlainText(nodes []Node) string {
	var parts []string
	var walk func([]Node)
	walk = func(ns []Node) {
		for _, n := range ns {
			switch n.Kind {
			case NPara, NHeading:
				parts = append(parts, InlinePlain(n.Inl))
			case NCode, NSource:
				parts = append(parts, n.Text)
			case NQuote:
				walk(n.Kids)
			case NList:
				for _, it := range n.Items {
					walk(it)
				}
			case NTable:
				for _, r := range n.Rows {
					for _, c := range r.Cells {
						parts = append(parts, InlinePlain(c.Inl))
					}
				}
			}
		}
	}
	walk(nodes)
	return strings.Join(parts, "\n")
}

// InlinePlain is the visible text of inline nodes.
func InlinePlain(in []Inline) string {
	var s strings.Builder
	var walk func([]Inline)
	walk = func(xs []Inline) {
		for _, x := range xs {
			switch x.Kind {
			case IText, ICode:
				s.WriteString(x.Text)
			case IBreak:
				s.WriteString("\n")
			case IImage:
				// an image has no visible text
			default:
				walk(x.Kids)
			}
		}
	}
	walk(in)
	return s.String()
}
