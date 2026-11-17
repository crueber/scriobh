package ai

import (
	"context"
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/domain/docref"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// The context builder: it turns what a person asked to include into chips, and
// is the one place that decides whether a piece of text may be sent.
//
//   - Everything is read inside the project: a document, note or entity of
//     another project, or one the caller cannot read, is "not available", with
//     the same words whether it exists or not.
//   - A document, research note or entity flagged never-send is refused when it
//     is named, and left out (and listed as skipped, with the reason) when it
//     would have come in by itself, as the previous chapter or an auto-detected
//     entity does.
//   - Entities come from entities.ReadableSheets, the same visibility rule as
//     every other read: the Compendium's membership, and a project key's pin.
//   - A private scratchpad is never read here. Nothing in this file touches it.
//   - Nothing is cut to fit: a piece that is too large is an error that names
//     it, so what the person approved is whole.
//   - The text sent is the document's chosen wording: the alternatives comment,
//     ghosted text and %%private notes%% are removed.

// Selection addresses a range of the target document with the convention the
// alternatives ops use: UTF-16 offsets into the body at a revision, or the
// Markdown source of the text with the text around it.
type Selection struct {
	BaseRevision *int   `json:"base_revision,omitempty" desc:"The document revision the range was read from. Required with start and end; a changed document is a 409."`
	Start        *int   `json:"start,omitempty" desc:"Start of the range, in UTF-16 code units of the body."`
	End          *int   `json:"end,omitempty" desc:"End of the range (exclusive)."`
	Quote        string `json:"quote,omitempty" desc:"Instead of offsets: the Markdown source of the text."`
	Before       string `json:"before,omitempty" desc:"With quote: source before it, to tell repeats apart."`
	After        string `json:"after,omitempty" desc:"With quote: source after it."`
	Occurrence   *int   `json:"occurrence,omitempty" desc:"With quote: which occurrence (from 0), when context cannot tell."`
}

// ChipSpec asks for one piece of context.
type ChipSpec struct {
	Kind       string   `json:"kind" enum:"selection,sentence,paragraph,document,previous_summary,previous_document,synopsis,brief,style_guide,entities,research,outline,summaries,timeline,instruction"`
	DocumentID string   `json:"document_id,omitempty" desc:"For document, synopsis and previous_*: another document than the target."`
	IDs        []string `json:"ids,omitempty" desc:"For entities and research: the ids."`
	Auto       bool     `json:"auto,omitempty" desc:"For entities: also those whose name appears in the selection or the document."`
	Text       string   `json:"text,omitempty" desc:"For instruction: your own words, up to 4,000 characters."`
}

// Skipped is something that was not included, and why.
type Skipped struct {
	Kind    string `json:"kind"`
	Source  string `json:"source,omitempty"`
	Reason  string `json:"reason" enum:"never_send,no_target,empty,too_many,not_available"`
	Message string `json:"message"`
}

type docRow struct {
	ID, ParentID, Kind, Title, Content, SynShort, SynLong string
	Revision                                              int
	Exclude                                               bool
	Position                                              float64
}

type loader struct {
	s         *Service
	ctx       context.Context
	d         *db.DB
	p         ops.Principal
	projectID string
	pol       policy
	cfg       Config

	title, subtitle, kind, genre, language, desc string

	docs    map[string]*docRow
	sheets  []entities.Sheet
	sheetOK bool
	nodes   []docref.Node
	nodeEx  map[string]bool
	nodeSyn map[string]string
	treeOK  bool

	skipped []Skipped
}

func (l *loader) projectInfo() *ops.Error {
	var trashed, deleted sql.NullString
	err := l.d.Read().QueryRowContext(l.ctx, `SELECT title, subtitle, kind, genre, language, description, trashed_at, deleted_at FROM projects WHERE id = ?`, l.projectID).
		Scan(&l.title, &l.subtitle, &l.kind, &l.genre, &l.language, &l.desc, &trashed, &deleted)
	if err != nil || trashed.Valid || deleted.Valid {
		return ops.NotFound("project")
	}
	return nil
}

func (l *loader) loadDoc(id, field string, explicit bool) (*docRow, *ops.Error) {
	if x, ok := l.docs[id]; ok {
		return x, l.excludedErr(x, field, explicit)
	}
	var x docRow
	var ex int
	err := l.d.Read().QueryRowContext(l.ctx, `SELECT id, COALESCE(parent_id,''), kind, title, content, synopsis_short, synopsis_long, revision, ai_exclude, position
		FROM documents WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, id, l.projectID).
		Scan(&x.ID, &x.ParentID, &x.Kind, &x.Title, &x.Content, &x.SynShort, &x.SynLong, &x.Revision, &ex, &x.Position)
	if err != nil {
		return nil, ops.Invalid(field, "That document is not part of this project.")
	}
	x.Exclude = ex == 1
	if l.docs == nil {
		l.docs = map[string]*docRow{}
	}
	l.docs[id] = &x
	return &x, l.excludedErr(&x, field, explicit)
}

func (l *loader) excludedErr(x *docRow, field string, explicit bool) *ops.Error {
	if x.Exclude {
		return ops.Invalid(field, "“"+clipText(x.Title, 60)+"” is marked never-send, so it cannot be part of an AI request. Lift the flag to include it.")
	}
	return nil
}

func (l *loader) skip(kind, source, reason, msg string) {
	l.skipped = append(l.skipped, Skipped{Kind: kind, Source: source, Reason: reason, Message: msg})
}

func (l *loader) loadSheets() error {
	if l.sheetOK {
		return nil
	}
	sh, err := entities.ReadableSheets(l.ctx, l.d, l.p, l.projectID)
	if err != nil {
		return err
	}
	l.sheets, l.sheetOK = sh, true
	return nil
}

func (l *loader) loadTree() error {
	if l.treeOK {
		return nil
	}
	rows, err := l.d.Read().QueryContext(l.ctx, `SELECT id, COALESCE(parent_id,''), kind, title, position, synopsis_short, ai_exclude FROM documents WHERE project_id = ? AND deleted_at IS NULL`, l.projectID)
	if err != nil {
		return err
	}
	defer rows.Close()
	l.nodeEx, l.nodeSyn = map[string]bool{}, map[string]string{}
	for rows.Next() {
		var n docref.Node
		var syn string
		var ex int
		if err := rows.Scan(&n.ID, &n.ParentID, &n.Kind, &n.Title, &n.Position, &syn, &ex); err != nil {
			return err
		}
		l.nodes = append(l.nodes, n)
		l.nodeEx[n.ID], l.nodeSyn[n.ID] = ex == 1, syn
	}
	l.treeOK = true
	return rows.Err()
}

// ---- the chosen wording ----------------------------------------------------------------------------

var privateNote = regexp.MustCompile(`(?s)%%.*?%%`)

// canonText is the text of a document as it should read to the model, and as it may be embedded: the chosen wording
// only. It is the one rule for what may leave the server in an AI request or an embedding request (and the
// documents say so: docs/AI.md, "What is never sent"). Removed, in this order:
//
//   - front matter;
//   - the alternatives comment;
//   - the author's %%private notes%% (one that is never closed runs to the end of its paragraph, so a half-typed
//     note is never sent), and ghosted text with its tags;
//   - then, by the renderer's own parse (text.CleanMarkdownHiding, the function a share link, a public
//     repository and an edition use): HTML comments, the cells and rows a hidden span leaves, and every
//     footnote definition that is referenced only from text that was removed, a private note or ghosted text
//     included.
//
// When the text cannot be placed by the parser the answer is "": nothing is sent for it (failing closed).
func canonText(content string) string {
	s := alts.BodyWithoutComment(alts.NormalizeEOL(text.WithoutFrontMatter(content)))
	hidden := footnotesIn(s, hiddenSpans(s))
	s = canonCut(s)
	out, ok := text.CleanMarkdownHiding(s, hidden)
	if !ok {
		return ""
	}
	return out
}

var footnoteLabel = regexp.MustCompile(`\[\^([^\]\s\[]+)\]`)

// footnotesIn is the set of footnote labels referenced inside the spans of body.
func footnotesIn(body string, spans [][2]int) map[string]bool {
	var labels map[string]bool
	for _, sp := range spans {
		for _, m := range footnoteLabel.FindAllStringSubmatch(body[sp[0]:sp[1]], -1) {
			if labels == nil {
				labels = map[string]bool{}
			}
			labels[m[1]] = true
		}
	}
	return labels
}

// canonCut is the cut of canonText that needs no parse: the alternatives comment, private notes and ghosted text.
// It is what a selection's slices get, with HTML comments scanned out (a slice is not a document: it has no front matter and no footnotes).
func canonCut(content string) string {
	s := alts.BodyWithoutComment(alts.NormalizeEOL(content))
	s = cutSpans(s, hiddenSpans(s), 0, len(s))
	s = text.StripGhosts(s, true)
	return stripPrivate(s)
}

func stripPrivate(s string) string {
	if !strings.Contains(s, "%%") {
		return s
	}
	s = privateNote.ReplaceAllString(s, "")
	if !strings.Contains(s, "%%") {
		return s
	}
	paras := strings.Split(s, "\n\n")
	for i, p := range paras {
		if j := strings.Index(p, "%%"); j >= 0 {
			paras[i] = p[:j]
		}
	}
	return strings.Join(paras, "\n\n")
}

// hiddenSpans are the byte ranges of a body that are never sent: the author's
// %%private notes%% (one that is never closed runs to the end of its paragraph) and
// ghosted text, markers included.
func hiddenSpans(body string) [][2]int {
	var spans [][2]int
	for i := 0; i < len(body); {
		j := strings.Index(body[i:], "%%")
		if j < 0 {
			break
		}
		s := i + j
		if k := strings.Index(body[s+2:], "%%"); k >= 0 {
			i = s + 2 + k + 2
		} else if k := strings.Index(body[s:], "\n\n"); k >= 0 {
			i = s + k
		} else {
			i = len(body)
		}
		spans = append(spans, [2]int{s, i})
	}
	gs, _ := text.Ghosts(body)
	for _, g := range gs {
		spans = append(spans, [2]int{g.OpenStart, g.CloseEnd})
	}
	// The parser can decline to place ghosts in text it cannot read cleanly, and a tag with no
	// partner is no ghost to it; a selection must not depend on that. The tags are also paired
	// by a plain scan, and an open tag with no close hides the rest of its paragraph.
	for i := 0; ; {
		o := strings.Index(body[i:], text.GhostOpen)
		if o < 0 {
			break
		}
		o += i
		after := o + len(text.GhostOpen)
		if c := strings.Index(body[after:], text.GhostClose); c >= 0 {
			i = after + c + len(text.GhostClose)
		} else if p := strings.Index(body[after:], "\n\n"); p >= 0 {
			i = after + p
		} else {
			i = len(body)
		}
		spans = append(spans, [2]int{o, i})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	return spans
}

// canonSlice is canonText of body[a:b] with the hidden spans cut out first.
func canonSlice(body string, hidden [][2]int, a, b int) string {
	return stripHTMLComments(canonCut(cutSpans(body, hidden, a, b)))
}

var htmlComment = regexp.MustCompile(`(?s)<!--.*?(?:-->|$)`)

// stripHTMLComments removes HTML comments, one that is never closed to the end of the text, from a slice of a
// document. A slice is not parsed (the parser's reading of a fragment is not the document's), so this is a plain
// scan: a comment inside a code span of a selection goes too, which costs a model nothing it needs.
func stripHTMLComments(s string) string {
	if !strings.Contains(s, "<!--") {
		return s
	}
	return htmlComment.ReplaceAllString(s, "")
}

// cutSpans is body[a:b] without the spans.
func cutSpans(body string, hidden [][2]int, a, b int) string {
	var sb strings.Builder
	pos := a
	for _, h := range hidden {
		if h[1] <= pos || h[0] >= b {
			continue
		}
		if h[0] > pos {
			sb.WriteString(body[pos:h[0]])
		}
		pos = max(pos, h[1])
	}
	if pos < b {
		sb.WriteString(body[pos:b])
	}
	return sb.String()
}

// ---- selection ------------------------------------------------------------------------------------------

type resolvedSel struct {
	doc       *docRow
	body      string
	a, b      int // byte offsets in body
	text      string
	sentence  string
	paragraph string
}

func (l *loader) resolveSelection(docID string, sel *Selection) (*resolvedSel, *ops.Error) {
	d, e := l.loadDoc(docID, "document_id", true)
	if e != nil {
		return nil, e
	}
	body := alts.Split(alts.NormalizeEOL(d.Content)).Body
	var a, b int
	switch {
	case sel.Start != nil && sel.End != nil:
		if sel.BaseRevision == nil {
			return nil, ops.Invalid("selection.base_revision", "Say which revision of the document the range was read from.")
		}
		if *sel.BaseRevision != d.Revision {
			return nil, ops.ConflictWith("The document changed since you selected this text. Select it again.", map[string]any{"revision": d.Revision})
		}
		ix := alts.NewIndex(body)
		var ok1, ok2 bool
		a, ok1 = ix.Byte(*sel.Start)
		b, ok2 = ix.Byte(*sel.End)
		if !ok1 || !ok2 || a >= b {
			return nil, ops.Invalid("selection", "The range is not inside the document.")
		}
	case sel.Quote != "":
		if sel.BaseRevision != nil && *sel.BaseRevision != d.Revision {
			return nil, ops.ConflictWith("The document changed since you selected this text. Select it again.", map[string]any{"revision": d.Revision})
		}
		var err error
		if a, b, err = alts.ResolveQuote(body, sel.Quote, sel.Before, sel.After, sel.Occurrence); err != nil {
			return nil, ops.Invalid("selection", err.Error())
		}
	default:
		return nil, ops.Invalid("selection", "Give the selection as a start and an end, or as a quote.")
	}
	if b-a > l.cfg.MaxChipBytes {
		return nil, tooLarge("The selection is larger than the limit for one piece of context.")
	}
	r := &resolvedSel{doc: d, body: body, a: a, b: b}
	// What is hidden is found on the whole body, then taken out of the slice: a selection can start
	// inside a %%private note%% or a ghost whose opening marker is outside it, and slicing first
	// would leave the rest of it in.
	hidden := hiddenSpans(body)
	r.text = strings.TrimSpace(canonSlice(body, hidden, a, b))
	ps, pe := paragraphBounds(body, a, b)
	r.paragraph = strings.TrimSpace(canonSlice(body, hidden, ps, pe))
	ss, se := sentenceBounds(body, a, b, ps, pe)
	r.sentence = strings.TrimSpace(canonSlice(body, hidden, ss, se))
	return r, nil
}

// paragraphBounds widens [a,b) to the paragraphs it touches (blank-line separated).
func paragraphBounds(body string, a, b int) (int, int) {
	s := 0
	if i := strings.LastIndex(body[:a], "\n\n"); i >= 0 {
		s = i + 2
	}
	e := len(body)
	if i := strings.Index(body[b:], "\n\n"); i >= 0 {
		e = b + i
	}
	return s, e
}

// sentenceBounds widens [a,b) to whole sentences inside the paragraph.
func sentenceBounds(body string, a, b, ps, pe int) (int, int) {
	end := func(r rune) bool { return r == '.' || r == '!' || r == '?' || r == '…' }
	s := ps
	for i := a; i > ps; {
		r, n := utf8.DecodeLastRuneInString(body[:i])
		i -= n
		if end(r) && i < a-1 {
			next, _ := utf8.DecodeRuneInString(body[i+n:])
			if unicode.IsSpace(next) || next == '"' || next == '”' || next == ')' {
				s = i + n
				break
			}
		}
	}
	for s < a && unicode.IsSpace(rune(body[s])) {
		s++
	}
	e := pe
	for i := b; i < pe; {
		r, n := utf8.DecodeRuneInString(body[i:])
		i += n
		if end(r) {
			// include closing quotes and brackets
			for i < pe {
				q, qn := utf8.DecodeRuneInString(body[i:])
				if q == '"' || q == '”' || q == ')' || q == '\'' || q == '’' {
					i += qn
				} else {
					break
				}
			}
			e = i
			break
		}
	}
	return s, e
}

// ---- entity helpers ---------------------------------------------------------------------------------------

func hasWord(hay, needle string) bool {
	if needle == "" {
		return false
	}
	from := 0
	for {
		i := strings.Index(hay[from:], needle)
		if i < 0 {
			return false
		}
		i += from
		j := i + len(needle)
		okL := i == 0 || !isWordByte(hay, i, true)
		okR := j >= len(hay) || !isWordByte(hay, j, false)
		if okL && okR {
			return true
		}
		from = i + 1
		for from < len(hay) && !utf8.RuneStart(hay[from]) {
			from++
		}
	}
}

func isWordByte(s string, i int, before bool) bool {
	var r rune
	if before {
		r, _ = utf8.DecodeLastRuneInString(s[:i])
	} else {
		r, _ = utf8.DecodeRuneInString(s[i:])
	}
	return unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_'
}

func sheetText(sh entities.Sheet) string {
	var b strings.Builder
	b.WriteString(sh.Name + " (" + sh.Kind + ")\n")
	if len(sh.Aliases) > 0 {
		b.WriteString("Also called: " + strings.Join(sh.Aliases, ", ") + "\n")
	}
	if s := strings.TrimSpace(sh.Summary); s != "" {
		b.WriteString("Summary: " + s + "\n")
	}
	keys := make([]string, 0, len(sh.Fields))
	for k := range sh.Fields {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		b.WriteString(k + ": " + sh.Fields[k] + "\n")
	}
	if body := strings.TrimSpace(canonText(sh.Body)); body != "" {
		b.WriteString("\n" + body + "\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// maxEntities bounds the entity chips of one request.
const maxEntities = 40

// ---- chips ---------------------------------------------------------------------------------------------------

// loadChips loads every requested chip. target and sel describe the document
// and selection the request is about (either may be absent).
func (l *loader) loadChips(specs []ChipSpec, targetID string, sel *resolvedSel) ([]lib.Chip, *ops.Error) {
	var out []lib.Chip
	seen := map[string]bool{}
	// A running total, so a request for sixty large notes is refused as soon as the budget is
	// spent, not after all sixty were read and held. The request limit is checked again on the
	// assembled messages; this one keeps what is loaded in step with it.
	total := 0
	over := func() *ops.Error {
		if total > l.cfg.MaxRequestBytes {
			return tooLarge(fmt.Sprintf("The context chosen is over %d KB, the most one request sends. Remove some of it.", l.cfg.MaxRequestBytes>>10))
		}
		return nil
	}
	add := func(c lib.Chip) *ops.Error {
		key := c.Kind + "\x00" + c.Source
		if c.Source != "" && seen[key] {
			return nil
		}
		seen[key] = true
		if strings.TrimSpace(c.Text) == "" {
			l.skip(c.Kind, c.Source, "empty", c.Label+" is empty, so it is not included.")
			return nil
		}
		c.Bytes = len(c.Text)
		if c.Bytes > l.cfg.MaxChipBytes {
			return tooLarge(fmt.Sprintf("%s is %d KB, over the %d KB limit for one piece of context. Choose a smaller part, or leave it out.", c.Label, c.Bytes>>10, l.cfg.MaxChipBytes>>10))
		}
		c.Tokens = lib.EstimateTokens(c.Text)
		out = append(out, c)
		total += c.Bytes
		return over()
	}
	for i, sp := range specs {
		if e := over(); e != nil {
			return nil, e
		}
		field := fmt.Sprintf("chips[%d]", i)
		switch sp.Kind {
		case lib.ChipSelection, lib.ChipSentence, lib.ChipParagraph:
			if sel == nil {
				l.skip(sp.Kind, "", "no_target", "There is no selection, so there is nothing to include.")
				continue
			}
			c := lib.Chip{Kind: sp.Kind, Source: sel.doc.ID}
			switch sp.Kind {
			case lib.ChipSelection:
				c.Label, c.Text = "Selected text", sel.text
			case lib.ChipSentence:
				c.Label, c.Text = "The sentence around the selection", sel.sentence
			default:
				c.Label, c.Text = "The paragraph around the selection", sel.paragraph
			}
			if e := add(c); e != nil {
				return nil, e
			}
		case lib.ChipDocument, lib.ChipSynopsis, lib.ChipPreviousSummary, lib.ChipPreviousDocument:
			id := sp.DocumentID
			explicit := id != ""
			if id == "" {
				id = targetID
			}
			if id == "" {
				l.skip(sp.Kind, "", "no_target", "There is no document to read this from.")
				continue
			}
			d, e := l.loadDoc(id, field+".document_id", true)
			if e != nil {
				if explicit || sp.Kind == lib.ChipDocument || sp.Kind == lib.ChipSynopsis {
					return nil, e
				}
			}
			if d == nil {
				continue
			}
			switch sp.Kind {
			case lib.ChipDocument:
				if e := add(lib.Chip{Kind: sp.Kind, Label: "Document: " + clipText(d.Title, 60), Source: d.ID, Text: strings.TrimSpace(canonText(d.Content))}); e != nil {
					return nil, e
				}
			case lib.ChipSynopsis:
				s := strings.TrimSpace(d.SynLong)
				if s == "" {
					s = strings.TrimSpace(d.SynShort)
				}
				if e := add(lib.Chip{Kind: sp.Kind, Label: "Synopsis of " + clipText(d.Title, 60), Source: d.ID, Text: s}); e != nil {
					return nil, e
				}
			default:
				prev, perr := l.previousOf(d)
				if perr != nil {
					return nil, ops.Internal(perr)
				}
				if prev == nil {
					l.skip(sp.Kind, "", "no_target", "There is no earlier "+d.Kind+" before this one.")
					continue
				}
				if l.nodeEx[prev.ID] { // it would have come in by itself: left out, and said so
					l.skip(sp.Kind, prev.ID, "never_send", "The earlier "+d.Kind+" is marked never-send, so it is not included.")
					continue
				}
				pd, e := l.loadDoc(prev.ID, field, false)
				if e != nil {
					l.skip(sp.Kind, prev.ID, "never_send", "The earlier "+d.Kind+" is marked never-send, so it is not included.")
					continue
				}
				if sp.Kind == lib.ChipPreviousSummary {
					s := strings.TrimSpace(pd.SynLong)
					if s == "" {
						s = strings.TrimSpace(pd.SynShort)
					}
					if e := add(lib.Chip{Kind: sp.Kind, Label: "Summary of the previous " + pd.Kind + ": " + clipText(pd.Title, 60), Source: pd.ID, Text: s}); e != nil {
						return nil, e
					}
				} else if e := add(lib.Chip{Kind: sp.Kind, Label: "The previous " + pd.Kind + ": " + clipText(pd.Title, 60), Source: pd.ID, Text: strings.TrimSpace(canonText(pd.Content))}); e != nil {
					return nil, e
				}
			}
		case lib.ChipBrief:
			var b strings.Builder
			b.WriteString("Title: " + l.title + "\n")
			if l.subtitle != "" {
				b.WriteString("Subtitle: " + l.subtitle + "\n")
			}
			b.WriteString("Kind: " + l.kind + "\n")
			if l.genre != "" {
				b.WriteString("Genre: " + l.genre + "\n")
			}
			b.WriteString("Language: " + l.language + "\n")
			if s := strings.TrimSpace(l.desc); s != "" {
				b.WriteString("\n" + s + "\n")
			}
			if e := add(lib.Chip{Kind: sp.Kind, Label: "Project brief", Text: strings.TrimSpace(b.String())}); e != nil {
				return nil, e
			}
		case lib.ChipStyleGuide:
			if e := add(lib.Chip{Kind: sp.Kind, Label: "Style guide (the project's, set by its owner)", Text: strings.TrimSpace(l.pol.StyleGuide)}); e != nil {
				return nil, e
			}
		case lib.ChipInstruction:
			if e := add(lib.Chip{Kind: sp.Kind, Label: "Your instruction", Text: strings.TrimSpace(sp.Text)}); e != nil {
				return nil, e
			}
		case lib.ChipOutline:
			if err := l.loadTree(); err != nil {
				return nil, ops.Internal(err)
			}
			if e := add(lib.Chip{Kind: sp.Kind, Label: "Outline", Text: l.outlineText()}); e != nil {
				return nil, e
			}
		case lib.ChipEntities:
			if err := l.loadSheets(); err != nil {
				return nil, ops.Internal(err)
			}
			byID := map[string]entities.Sheet{}
			for _, sh := range l.sheets {
				byID[sh.ID] = sh
			}
			var picked []entities.Sheet
			have := map[string]bool{}
			for _, id := range sp.IDs {
				sh, ok := byID[id]
				if !ok {
					return nil, ops.Invalid(field+".ids", "One of those entities is not available to you in this project.")
				}
				if sh.AIExclude {
					return nil, ops.Invalid(field+".ids", "“"+clipText(sh.Name, 60)+"” is marked never-send, so it cannot be part of an AI request. Lift the flag to include it.")
				}
				if !have[id] {
					have[id] = true
					picked = append(picked, sh)
				}
			}
			if sp.Auto {
				hay := strings.ToLower(l.mentionText(sel, targetID))
				for _, sh := range l.sheets {
					if have[sh.ID] {
						continue
					}
					names := append([]string{sh.Name}, sh.Aliases...)
					hit := false
					for _, n := range names {
						hit = hit || hasWord(hay, strings.ToLower(n))
					}
					if !hit {
						continue
					}
					if sh.AIExclude {
						l.skip(lib.ChipEntities, sh.ID, "never_send", "An entity named in the text is marked never-send, so its sheet is not included.")
						continue
					}
					have[sh.ID] = true
					picked = append(picked, sh)
				}
			}
			if len(picked) > maxEntities {
				for _, sh := range picked[maxEntities:] {
					l.skip(lib.ChipEntities, sh.ID, "too_many", "Only "+fmt.Sprint(maxEntities)+" entity sheets go in one request; this one was left out.")
				}
				picked = picked[:maxEntities]
			}
			sort.SliceStable(picked, func(i, j int) bool { return picked[i].Name < picked[j].Name })
			for _, sh := range picked {
				if e := add(lib.Chip{Kind: lib.ChipEntities, Label: "Entity: " + clipText(sh.Name, 60), Source: sh.ID, Text: sheetText(sh)}); e != nil {
					return nil, e
				}
			}
		case lib.ChipSummaries:
			if targetID == "" {
				l.skip(sp.Kind, "", "no_target", "There is no document to take the earlier chapters of.")
				continue
			}
			txt, err := l.earlierSummaries(targetID)
			if err != nil {
				return nil, ops.Internal(err)
			}
			if e := add(lib.Chip{Kind: sp.Kind, Label: "Summaries of the earlier chapters", Text: txt}); e != nil {
				return nil, e
			}
		case lib.ChipTimeline:
			txt, err := l.timelineText()
			if err != nil {
				return nil, ops.Internal(err)
			}
			if e := add(lib.Chip{Kind: sp.Kind, Label: "Timeline events and checks", Text: txt}); e != nil {
				return nil, e
			}
		case lib.ChipResearch:
			if len(sp.IDs) == 0 {
				return nil, ops.Invalid(field+".ids", "Name the research notes to include.")
			}
			for _, id := range sp.IDs {
				if e := over(); e != nil {
					return nil, e
				}
				c, e := l.loadResearch(id, field+".ids")
				if e != nil {
					return nil, e
				}
				if e := add(c); e != nil {
					return nil, e
				}
			}
		default:
			return nil, ops.Invalid(field+".kind", "Unknown chip kind.")
		}
	}
	return out, nil
}

// mentionText is the text auto-detection looks for names in: the selection, or
// else the target document.
func (l *loader) mentionText(sel *resolvedSel, targetID string) string {
	if sel != nil {
		return sel.text + "\n" + sel.paragraph
	}
	if targetID != "" {
		if d, e := l.loadDoc(targetID, "document_id", false); d != nil && e == nil {
			return canonText(d.Content)
		}
	}
	return ""
}

func (l *loader) previousOf(d *docRow) (*docref.Node, error) {
	if err := l.loadTree(); err != nil {
		return nil, err
	}
	order := docref.Order(l.nodes)
	byID := map[string]docref.Node{}
	for _, n := range l.nodes {
		byID[n.ID] = n
	}
	var prev *docref.Node
	for _, id := range order {
		if id == d.ID {
			return prev, nil
		}
		if n := byID[id]; n.Kind == d.Kind {
			n := n
			prev = &n
		}
	}
	return nil, nil
}

func (l *loader) outlineText() string {
	order := docref.Order(l.nodes)
	byID := map[string]docref.Node{}
	for _, n := range l.nodes {
		byID[n.ID] = n
	}
	depth := func(id string) int {
		n := 0
		for p := byID[id].ParentID; p != "" && n < 8; p = byID[p].ParentID {
			n++
		}
		return n
	}
	var b strings.Builder
	for _, id := range order {
		n := byID[id]
		if (n.Kind != "part" && n.Kind != "chapter" && n.Kind != "scene") || l.nodeEx[id] {
			continue
		}
		b.WriteString(strings.Repeat("  ", depth(id)) + "- " + n.Title)
		if s := strings.TrimSpace(l.nodeSyn[id]); s != "" {
			b.WriteString(": " + s)
		}
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

// loadResearch reads one research note: a note of this project, or a note of
// the project's primary or referenced Compendium that the caller may read (a
// project-pinned key reaches the primary Compendium only).
func (l *loader) loadResearch(id, field string) (lib.Chip, *ops.Error) {
	notAvail := ops.Invalid(field, "That research note is not available to you in this project.")
	var pid, cid sql.NullString
	var kind, title, body, quote string
	var ex int
	err := l.d.Read().QueryRowContext(l.ctx, `SELECT project_id, compendium_id, kind, title, body, quote, ai_exclude FROM research_notes WHERE id = ? AND deleted_at IS NULL`, id).
		Scan(&pid, &cid, &kind, &title, &body, &quote, &ex)
	if err != nil || kind == "folder" {
		return lib.Chip{}, notAvail
	}
	switch {
	case pid.Valid && pid.String == l.projectID:
	case cid.Valid:
		if !l.s.compendiumInScope(l.ctx, l.d.Read(), l.projectID, cid.String) || !compaccess.CanRead(l.ctx, l.d.Read(), l.p.UserID, cid.String) {
			return lib.Chip{}, notAvail
		}
		if l.p.ProjectID != "" {
			var primary sql.NullString
			_ = l.d.Read().QueryRowContext(l.ctx, `SELECT compendium_id FROM projects WHERE id = ?`, l.projectID).Scan(&primary)
			if !primary.Valid || primary.String != cid.String {
				return lib.Chip{}, notAvail
			}
		}
	default:
		return lib.Chip{}, notAvail
	}
	if ex == 1 {
		return lib.Chip{}, ops.Invalid(field, "“"+clipText(title, 60)+"” is marked never-send, so it cannot be part of an AI request. Lift the flag to include it.")
	}
	var b strings.Builder
	b.WriteString(title + "\n")
	if q := strings.TrimSpace(quote); q != "" {
		b.WriteString("\nQuoted passage: " + q + "\n")
	}
	b.WriteString("\n" + strings.TrimSpace(canonText(body)))
	return lib.Chip{Kind: lib.ChipResearch, Label: "Research: " + clipText(title, 60), Source: id, Text: strings.TrimSpace(b.String())}, nil
}
