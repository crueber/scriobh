package docs

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/altstore"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Alternatives are annotations on text: the body of a document always holds
// the chosen wording, and the other wordings live in the tables (and in the
// comment that ends the stored Markdown). Every op here saves through the
// document, in one transaction, so the text, the comment, the search index and
// the history never disagree. See docs/OPEN-QUESTIONS.md "Phase 3.5
// alternatives decisions" and docs/EDITOR-SPIKE.md for what an editor must do
// with the results.
//
// Ranges are in UTF-16 code units of the body: the text before the comment,
// with line endings as LF (a document that carries alternatives is stored with
// LF only). A client that does not know offsets (the editor) names text by its
// Markdown source, the context around it and, if still ambiguous, which
// occurrence.

// ---- shapes ---------------------------------------------------------------------

// VariantOut is one wording.
type VariantOut struct {
	ID        string `json:"id"`
	Text      string `json:"text" desc:"Markdown source of the wording."`
	Source    string `json:"source" enum:"manual,generated" desc:"manual: written by a person. generated: suggested by a configured provider (Phase 4)."`
	Original  bool   `json:"original" desc:"The wording the text had when the alternative was made. It cannot be edited or removed."`
	Position  int    `json:"position" desc:"1-based place in the cycle."`
	AuthorID  string `json:"author_id,omitempty"`
	CreatedAt string `json:"created_at"`
}

// AltOut is one alternative.
type AltOut struct {
	ID              string       `json:"id"`
	Scope           string       `json:"scope" enum:"word,sentence,paragraph"`
	Status          string       `json:"status" enum:"active,orphaned" desc:"orphaned: the text it was anchored to is gone or changed beyond recognition; its wordings are kept. Reattach it with alternative.reanchor."`
	Start           int          `json:"start" desc:"Where the anchored text starts, in UTF-16 code units of the body. Meaningless when orphaned."`
	End             int          `json:"end"`
	Quote           string       `json:"quote" desc:"The chosen wording, which is what the anchored text says."`
	ChosenVariantID string       `json:"chosen_variant_id"`
	ChosenPosition  int          `json:"chosen_position" desc:"1-based place of the chosen wording in the cycle."`
	Before          string       `json:"before,omitempty" desc:"Up to 24 characters of Markdown before the anchor."`
	After           string       `json:"after,omitempty" desc:"Up to 24 characters after it."`
	Variants        []VariantOut `json:"variants"`
}

func altOut(it alts.Item) AltOut {
	o := AltOut{ID: it.ID, Scope: it.Scope, Status: alts.StatusActive, Start: it.Start, End: it.End, Quote: it.ChosenText(),
		ChosenVariantID: it.Chosen, Before: it.Before, After: it.After, Variants: make([]VariantOut, 0, len(it.Variants))}
	if it.Orphan {
		o.Status = alts.StatusOrphaned
	}
	for i, v := range it.Variants {
		o.Variants = append(o.Variants, VariantOut{ID: v.ID, Text: v.Text, Source: v.Source, Original: v.Original, Position: i + 1, AuthorID: v.By, CreatedAt: v.At})
		if v.ID == it.Chosen {
			o.ChosenPosition = i + 1
		}
	}
	return o
}

// AltLimitsOut are the limits in force.
type AltLimitsOut struct {
	PerDocument  int `json:"per_document"`
	PerAlt       int `json:"variants_per_alternative"`
	VariantBytes int `json:"variant_bytes"`
}

// AltListIn lists a document's alternatives.
type AltListIn struct {
	ID     string `path:"id" json:"-" desc:"Document id."`
	Status string `query:"status" enum:"active,orphaned" desc:"Only alternatives with this status (orphaned is the lost-anchors list)."`
}

// DocID lets the registry find the owning project.
func (in AltListIn) DocID() string { return in.ID }

// AltListOut is a document's alternatives with the revision they belong to.
type AltListOut struct {
	Revision    int          `json:"revision"`
	ContentHash string       `json:"content_hash"`
	Active      int          `json:"active"`
	Orphaned    int          `json:"orphaned"`
	Limits      AltLimitsOut `json:"limits"`
	Items       []AltOut     `json:"items" desc:"In document order, orphaned ones last."`
}

// Range names text in the body at the revision the client saw: by offsets, or
// by the Markdown source of the text with the context around it.
type Range struct {
	Start      *int   `json:"start,omitempty" desc:"Start, in UTF-16 code units of the body."`
	End        *int   `json:"end,omitempty" desc:"End (exclusive)."`
	Quote      string `json:"quote,omitempty" desc:"Instead of offsets: the Markdown source of the text (what an editor's serializer writes for the selection)."`
	Before     string `json:"before,omitempty" desc:"With quote: some Markdown source before it, to tell repeats apart."`
	After      string `json:"after,omitempty" desc:"With quote: some Markdown source after it."`
	Occurrence *int   `json:"occurrence,omitempty" desc:"With quote: which occurrence, counting from 0, when context cannot tell."`
}

// AltMut is what every op that changes a document's alternatives returns.
type AltMut struct {
	Revision    int         `json:"revision" desc:"The document's revision after the write."`
	ContentHash string      `json:"content_hash"`
	WordCount   int         `json:"word_count"`
	Comment     string      `json:"comment" desc:"The alternatives comment that now ends the text ('' when there is none). An editor holding the document swaps its copy of that last line for this one."`
	Unchanged   bool        `json:"unchanged" desc:"Nothing was written."`
	RevisionID  string      `json:"revision_id,omitempty" desc:"The history entry written for a choice (reason alternative)."`
	Alternative *AltOut     `json:"alternative,omitempty"`
	Replaced    *AltReplace `json:"replaced,omitempty"`
	Content     string      `json:"content,omitempty" desc:"The whole stored text, when with_content was set."`
}

// AltReplace says what a choice did to the body.
type AltReplace struct {
	Start  int    `json:"start" desc:"Where the replaced text started, in UTF-16 code units of the body."`
	End    int    `json:"end" desc:"Where the replaced text ended before the change."`
	NewEnd int    `json:"new_end" desc:"Where the new text ends."`
	Text   string `json:"text" desc:"The new text (Markdown source)."`
}

// altsPath is the REST prefix of the document's alternatives.
const altsPath = "/api/v1/docs/{id}/alternatives"

// ---- the session every writing op starts with ------------------------------------------

type altSession struct {
	c     *ops.Ctx
	tx    *sql.Tx
	id    string
	h     head
	parts alts.Parts
	body  string
	items []alts.Item
	lim   alts.Limits
	now   string
	ro    alts.Options
	ix    *alts.Index
	quiet bool // no alternative.updated event (the caller is not an alternatives op)
}

func openAlt(c *ops.Ctx, tx *sql.Tx, l *leases, id, tab string, base int) (*altSession, error) {
	h, err := loadHead(tx, id)
	if err != nil {
		return nil, err
	}
	if err := h.guard(c, l, id, tab, base); err != nil {
		return nil, err
	}
	items, err := altstore.Load(tx, id)
	if err != nil {
		return nil, err
	}
	lim := altstore.LimitsOf(c.Env)
	s := &altSession{c: c, tx: tx, id: id, h: h, items: items, lim: lim, now: db.Now()}
	s.parts = alts.Split(alts.NormalizeEOL(h.Content))
	if len(items) == 0 && s.parts.HasLine {
		// A last line with no alternatives behind it (not valid JSON, or a hand-
		// written one never read in) is an ordinary comment: it stays in the text.
		s.parts = alts.Parts{Body: alts.NormalizeEOL(h.Content)}
	}
	s.body = s.parts.Body
	s.ro = alts.Options{Actor: c.P.UserID, Now: s.now, NewID: db.NewID, Limits: lim}
	return s, nil
}

func (s *altSession) index() *alts.Index {
	if s.ix == nil {
		s.ix = alts.NewIndex(s.body)
	}
	return s.ix
}

func (s *altSession) find(altID string) (int, error) {
	for i, it := range s.items {
		if it.ID == altID {
			return i, nil
		}
	}
	if altID == "" {
		return 0, ops.Invalid("alternative_id", "Name the alternative.")
	}
	return 0, ops.NotFound("alternative")
}

func (s *altSession) byteRange(it alts.Item) (int, int, error) {
	a, ok1 := s.index().Byte(it.Start)
	b, ok2 := s.index().Byte(it.End)
	if !ok1 || !ok2 || a >= b || b > len(s.body) || s.body[a:b] != it.ChosenText() {
		return 0, 0, ops.Conflict("This alternative no longer matches the text. Reload the document and reattach it.")
	}
	return a, b, nil
}

// resolve turns a Range into byte offsets in the body.
func (s *altSession) resolve(r Range) (int, int, error) {
	if r.Start != nil || r.End != nil {
		if r.Start == nil || r.End == nil {
			return 0, 0, ops.Invalid("start", "Send both start and end, or a quote.")
		}
		a, ok1 := s.index().Byte(*r.Start)
		b, ok2 := s.index().Byte(*r.End)
		if !ok1 || !ok2 || a >= b {
			return 0, 0, ops.Invalid("start", "That range is empty, outside the text, or cuts a character in two (offsets are UTF-16 code units of the body).")
		}
		return a, b, nil
	}
	a, b, err := alts.ResolveQuote(s.body, r.Quote, r.Before, r.After, r.Occurrence)
	if err != nil {
		return 0, 0, ops.Invalid("quote", err.Error())
	}
	return a, b, nil
}

func invalid(field string, err error) error {
	var iv *alts.Invalid
	if asInvalid(err, &iv) {
		return ops.Invalid(field, iv.Msg)
	}
	return ops.Invalid(field, err.Error())
}

func asInvalid(err error, target **alts.Invalid) bool {
	if iv, ok := err.(*alts.Invalid); ok {
		*target = iv
		return true
	}
	return false
}

// finish writes the new body and items: the tables, then the text with its
// comment through the document write path.
func (s *altSession) finish(body string, items []alts.Item, o commitOpts, kind string, focus string, withContent bool) (AltMut, error) {
	parts := s.parts
	parts.Body = body
	line := alts.Serialize(items)
	content := parts.Compose(line)
	if content == s.h.Content {
		out := AltMut{Revision: s.h.Revision, ContentHash: s.h.Hash, WordCount: s.h.Words, Comment: line, Unchanged: true}
		out.fill(items, focus)
		if withContent {
			out.Content = content
		}
		return out, nil
	}
	_, destroyed, err := altstore.Write(s.tx, s.id, s.h.ProjectID, s.items, items, s.c.P.UserID, s.now)
	if err != nil {
		return AltMut{}, err
	}
	if destroyed && o.Keep == "" {
		o.Keep = "Before alternative wordings were removed or reworded"
	}
	cm, err := commit(s.c, s.tx, s.id, s.h, content, s.now, o)
	if err != nil {
		return AltMut{}, err
	}
	if !s.quiet {
		s.c.Emit(ops.Event{Type: "alternative.updated", ProjectID: s.h.ProjectID, DocumentID: s.id,
			Payload: map[string]any{"kind": kind, "revision": cm.Revision, "count": len(items)}})
	}
	if err := s.c.FlushTo(s.tx); err != nil {
		return AltMut{}, err
	}
	out := AltMut{Revision: cm.Revision, ContentHash: cm.Hash, WordCount: cm.Words, Comment: line, RevisionID: cm.RevisionID}
	out.fill(items, focus)
	if withContent {
		out.Content = content
	}
	return out, nil
}

func (m *AltMut) fill(items []alts.Item, focus string) {
	for _, it := range items {
		if it.ID == focus {
			o := altOut(it)
			m.Alternative = &o
		}
	}
}

// swap puts newText where the chosen text of item i stands and makes variant
// chosen its chosen wording; everything after it shifts by the exact
// difference. oldText is what the anchor holds now. It returns the new body
// and items.
func (s *altSession) swap(items []alts.Item, i int, oldText, newText, chosen string) (string, []alts.Item, *AltReplace, error) {
	it := items[i]
	a, ok1 := s.index().Byte(it.Start)
	b, ok2 := s.index().Byte(it.End)
	if !ok1 || !ok2 || a >= b || b > len(s.body) || s.body[a:b] != oldText {
		return "", nil, nil, ops.Conflict("This alternative no longer matches the text. Reload the document and reattach it.")
	}
	nb := s.body[:a] + newText + s.body[b:]
	if !structureKept(s.body, nb, a, b, len(newText)) {
		return "", nil, nil, ops.Invalid("variant_id", "That wording would change the structure of the text (a heading or list mark, a quote or a blank line).")
	}
	var others []alts.Item
	for j, o := range items {
		if j != i {
			others = append(others, o)
		}
	}
	others, _ = alts.Rebase(s.body, nb, others, []alts.Edit{{Start: a, End: b, NewLen: len(newText)}}, s.ro)
	it = it.Clone()
	it.Chosen = chosen
	it.End = it.Start + alts.Len16(newText)
	it.Before, it.After = alts.Contexts(nb, a, a+len(newText))
	rep := &AltReplace{Start: items[i].Start, End: items[i].End, NewEnd: it.End, Text: newText}
	return nb, append(others, it), rep, nil
}

// structureKept checks that replacing body[a:b] (now newLen bytes) left the
// text blocks around it as they were: the same blocks, of the same kinds. Only
// the lines around the change are parsed.
func structureKept(old, nw string, a, b, newLen int) bool {
	lo := strings.LastIndex(old[:a], "\n\n")
	if lo < 0 {
		lo = 0
	} else {
		lo += 2
	}
	hiOld := len(old)
	if i := strings.Index(old[b:], "\n\n"); i >= 0 {
		hiOld = b + i
	}
	hiNew := hiOld + (newLen - (b - a))
	if hiNew < lo || hiNew > len(nw) {
		return false
	}
	bo, ok1 := text.Blocks(old[lo:hiOld])
	bn, ok2 := text.Blocks(nw[lo:hiNew])
	if !ok1 || !ok2 {
		return true
	}
	return alts.SameBlocks(bo, bn)
}

func overlapsActive(items []alts.Item, skip int, a, b int, ix *alts.Index) (alts.Item, bool) {
	for j, o := range items {
		if j == skip || o.Orphan || !o.Placed() {
			continue
		}
		oa, ok1 := ix.Byte(o.Start)
		ob, ok2 := ix.Byte(o.End)
		if ok1 && ok2 && a < ob && b > oa {
			return o, true
		}
	}
	return alts.Item{}, false
}

func variantIndex(it alts.Item, id string) int {
	for i, v := range it.Variants {
		if v.ID == id {
			return i
		}
	}
	return -1
}

func manualVariant(text string, c *ops.Ctx, now string) alts.Variant {
	return alts.Variant{ID: db.NewID("alv"), Text: text, Source: alts.SourceManual, By: c.P.UserID, At: now}
}

// ---- alternative.list ------------------------------------------------------------------------

func altList(c *ops.Ctx, in AltListIn) (AltListOut, error) {
	d := c.DB().Read()
	var out AltListOut
	var deleted sql.NullString
	err := d.QueryRowContext(c, `SELECT revision, content_hash, deleted_at FROM documents WHERE id = ?`, in.ID).Scan(&out.Revision, &out.ContentHash, &deleted)
	if err != nil {
		return out, ops.NotFound("document")
	}
	items, err := altstore.Load(d, in.ID)
	if err != nil {
		return out, err
	}
	out.Items = []AltOut{}
	for _, it := range items {
		if it.Orphan {
			out.Orphaned++
		} else {
			out.Active++
		}
		if in.Status != "" && in.Status != altStatus(it) {
			continue
		}
		out.Items = append(out.Items, altOut(it))
	}
	lim := altstore.LimitsOf(c.Env)
	out.Limits = AltLimitsOut{PerDocument: lim.PerDoc, PerAlt: lim.VariantsPer, VariantBytes: lim.MaxVariantSz}
	return out, nil
}

func altStatus(it alts.Item) string {
	if it.Orphan {
		return alts.StatusOrphaned
	}
	return alts.StatusActive
}

// ---- alternative.create --------------------------------------------------------------------------

// AltCreateIn makes an alternative over a range of the text.
type AltCreateIn struct {
	ID           string   `path:"id" json:"-" desc:"Document id."`
	BaseRevision int      `json:"base_revision" desc:"The revision the range was read from."`
	TabID        string   `json:"tab_id,omitempty" desc:"Browser tab id; enables the single-writer lease. Leave empty for API clients."`
	Scope        string   `json:"scope" enum:"word,sentence,paragraph"`
	Start        *int     `json:"start,omitempty" desc:"Start of the range, in UTF-16 code units of the body."`
	End          *int     `json:"end,omitempty" desc:"End of the range (exclusive)."`
	Quote        string   `json:"quote,omitempty" desc:"Instead of offsets: the Markdown source of the text."`
	Before       string   `json:"before,omitempty" desc:"With quote: Markdown source before it, to tell repeats apart."`
	After        string   `json:"after,omitempty" desc:"With quote: Markdown source after it."`
	Occurrence   *int     `json:"occurrence,omitempty" desc:"With quote: which occurrence (from 0), when context cannot tell."`
	Variants     []string `json:"variants,omitempty" desc:"Other wordings to add now. The text in the range is the original and stays chosen."`
	WithContent  bool     `json:"with_content,omitempty" desc:"Return the whole stored text."`
}

// DocID lets the registry find the owning project.
func (in AltCreateIn) DocID() string { return in.ID }

func altCreate(c *ops.Ctx, l *leases, in AltCreateIn) (AltMut, error) {
	var out AltMut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		s, err := openAlt(c, tx, l, in.ID, in.TabID, in.BaseRevision)
		if err != nil {
			return err
		}
		if len(s.items) >= s.lim.PerDoc {
			return ops.Invalid("scope", fmt.Sprintf("A document holds at most %d alternatives.", s.lim.PerDoc))
		}
		a, b, err := s.resolve(Range{Start: in.Start, End: in.End, Quote: in.Quote, Before: in.Before, After: in.After, Occurrence: in.Occurrence})
		if err != nil {
			return err
		}
		blocks, ok := text.Blocks(s.body)
		if !ok {
			return ops.Invalid("scope", "This text is too irregular to anchor an alternative to.")
		}
		if err := alts.CheckRange(s.body, a, b, in.Scope, blocks, s.lim); err != nil {
			return invalid("scope", err)
		}
		if o, hit := overlapsActive(s.items, -1, a, b, s.index()); hit {
			return ops.Conflict("That range overlaps another alternative (" + alts.Clip(o.ChosenText()) + "). Alternatives do not nest: resolve or reattach it first.")
		}
		orig := alts.Variant{ID: db.NewID("alv"), Text: s.body[a:b], Original: true, Source: alts.SourceManual, By: c.P.UserID, At: s.now}
		it := alts.Item{ID: db.NewID("alt"), Scope: in.Scope, Start: s.index().Unit(a), End: s.index().Unit(b), Chosen: orig.ID, Variants: []alts.Variant{orig}}
		it.Before, it.After = alts.Contexts(s.body, a, b)
		for _, t := range in.Variants {
			it.Variants = append(it.Variants, manualVariant(t, c, s.now))
		}
		if err := alts.ValidateItem(it, s.lim); err != nil {
			return invalid("variants", err)
		}
		out, err = s.finish(s.body, append(append([]alts.Item(nil), s.items...), it), commitOpts{NoCredit: true}, "create", it.ID, in.WithContent)
		return err
	})
	return out, err
}

// ---- variants ---------------------------------------------------------------------------------------

// AltVariantAddIn adds a wording.
type AltVariantAddIn struct {
	ID             string `path:"id" json:"-" desc:"Document id."`
	BaseRevision   int    `json:"base_revision"`
	TabID          string `json:"tab_id,omitempty" desc:"Browser tab id; enables the single-writer lease."`
	AlternativeID  string `json:"alternative_id"`
	Text           string `json:"text" desc:"Markdown source of the new wording."`
	AfterVariantID string `json:"after_variant_id,omitempty" desc:"Place it after this wording; default last. Use this to put a removed wording back where it was."`
	WithContent    bool   `json:"with_content,omitempty"`
}

// DocID lets the registry find the owning project.
func (in AltVariantAddIn) DocID() string { return in.ID }

func altVariantAdd(c *ops.Ctx, l *leases, in AltVariantAddIn) (AltMut, error) {
	var out AltMut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		s, err := openAlt(c, tx, l, in.ID, in.TabID, in.BaseRevision)
		if err != nil {
			return err
		}
		i, err := s.find(in.AlternativeID)
		if err != nil {
			return err
		}
		items := cloneItems(s.items)
		it := &items[i]
		if len(it.Variants) >= s.lim.VariantsPer {
			return ops.Invalid("text", fmt.Sprintf("An alternative holds at most %d wordings.", s.lim.VariantsPer))
		}
		nv := manualVariant(in.Text, c, s.now)
		at := len(it.Variants)
		if in.AfterVariantID != "" {
			j := variantIndex(*it, in.AfterVariantID)
			if j < 0 {
				return ops.NotFound("variant")
			}
			at = j + 1
		}
		it.Variants = append(it.Variants[:at], append([]alts.Variant{nv}, it.Variants[at:]...)...)
		if err := alts.ValidateItem(*it, s.lim); err != nil {
			return invalid("text", err)
		}
		out, err = s.finish(s.body, items, commitOpts{NoCredit: true}, "add_variant", it.ID, in.WithContent)
		return err
	})
	return out, err
}

// AltVariantEditIn changes the words of a wording.
type AltVariantEditIn struct {
	ID            string `path:"id" json:"-" desc:"Document id."`
	BaseRevision  int    `json:"base_revision"`
	TabID         string `json:"tab_id,omitempty" desc:"Browser tab id; enables the single-writer lease."`
	AlternativeID string `json:"alternative_id"`
	VariantID     string `json:"variant_id"`
	Text          string `json:"text" desc:"The new words. The original cannot be edited. Editing the chosen wording rewrites the text of the document too."`
	WithContent   bool   `json:"with_content,omitempty"`
}

// DocID lets the registry find the owning project.
func (in AltVariantEditIn) DocID() string { return in.ID }

func altVariantEdit(c *ops.Ctx, l *leases, in AltVariantEditIn) (AltMut, error) {
	var out AltMut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		s, err := openAlt(c, tx, l, in.ID, in.TabID, in.BaseRevision)
		if err != nil {
			return err
		}
		i, err := s.find(in.AlternativeID)
		if err != nil {
			return err
		}
		items := cloneItems(s.items)
		it := &items[i]
		j := variantIndex(*it, in.VariantID)
		if j < 0 {
			return ops.NotFound("variant")
		}
		if it.Variants[j].Original {
			return ops.Conflict("The original wording cannot be edited. Add a wording instead.")
		}
		old := it.Variants[j].Text
		if old == in.Text {
			out, err = s.finish(s.body, items, commitOpts{}, "edit_variant", it.ID, in.WithContent)
			return err
		}
		it.Variants[j].Text = in.Text
		if err := alts.ValidateItem(*it, s.lim); err != nil {
			return invalid("text", err)
		}
		opts := commitOpts{NoCredit: true}
		body := s.body
		if it.Chosen == in.VariantID && !it.Orphan {
			var rep *AltReplace
			body, items, rep, err = s.swap(items, i, old, in.Text, in.VariantID)
			if err != nil {
				return err
			}
			opts.Reason, opts.Note = revisions.ReasonAlternative, fmt.Sprintf("Edited the chosen %s wording", it.Scope)
			opts.NoCredit = false // the writer wrote these words
			out, err = s.finish(body, items, opts, "edit_variant", it.ID, in.WithContent)
			out.Replaced = rep
			return err
		}
		out, err = s.finish(body, items, opts, "edit_variant", it.ID, in.WithContent)
		return err
	})
	return out, err
}

// AltVariantRemoveIn removes a wording.
type AltVariantRemoveIn struct {
	ID            string `path:"id" json:"-" desc:"Document id."`
	BaseRevision  int    `json:"base_revision"`
	TabID         string `json:"tab_id,omitempty" desc:"Browser tab id; enables the single-writer lease."`
	AlternativeID string `json:"alternative_id"`
	VariantID     string `json:"variant_id"`
	WithContent   bool   `json:"with_content,omitempty"`
}

// DocID lets the registry find the owning project.
func (in AltVariantRemoveIn) DocID() string { return in.ID }

func altVariantRemove(c *ops.Ctx, l *leases, in AltVariantRemoveIn) (AltMut, error) {
	var out AltMut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		s, err := openAlt(c, tx, l, in.ID, in.TabID, in.BaseRevision)
		if err != nil {
			return err
		}
		i, err := s.find(in.AlternativeID)
		if err != nil {
			return err
		}
		items := cloneItems(s.items)
		it := &items[i]
		j := variantIndex(*it, in.VariantID)
		if j < 0 {
			return ops.NotFound("variant")
		}
		if it.Variants[j].Original {
			return ops.Conflict("The original wording cannot be removed.")
		}
		opts := commitOpts{NoCredit: true}
		body := s.body
		var rep *AltReplace
		if it.Chosen == in.VariantID && !it.Orphan {
			// Removing the wording that is in the text puts the original back.
			orig, _ := it.Original()
			opts.Reason, opts.Note = revisions.ReasonAlternative, fmt.Sprintf("Removed the chosen %s wording; the original is back", it.Scope)
			old := it.Variants[j].Text
			it.Variants = append(it.Variants[:j], it.Variants[j+1:]...)
			body, items, rep, err = s.swap(items, i, old, orig.Text, orig.ID)
			if err != nil {
				return err
			}
		} else {
			it.Variants = append(it.Variants[:j], it.Variants[j+1:]...)
			if it.Chosen == in.VariantID { // an orphan's chosen wording: fall back to the original
				orig, _ := it.Original()
				it.Chosen = orig.ID
			}
		}
		out, err = s.finish(body, items, opts, "remove_variant", it.ID, in.WithContent)
		out.Replaced = rep
		return err
	})
	return out, err
}

// AltVariantReorderIn moves a wording in the cycle.
type AltVariantReorderIn struct {
	ID             string `path:"id" json:"-" desc:"Document id."`
	BaseRevision   int    `json:"base_revision"`
	TabID          string `json:"tab_id,omitempty" desc:"Browser tab id; enables the single-writer lease."`
	AlternativeID  string `json:"alternative_id"`
	VariantID      string `json:"variant_id"`
	AfterVariantID string `json:"after_variant_id,omitempty" desc:"Place it after this wording; empty to put it first."`
}

// DocID lets the registry find the owning project.
func (in AltVariantReorderIn) DocID() string { return in.ID }

func altVariantReorder(c *ops.Ctx, l *leases, in AltVariantReorderIn) (AltMut, error) {
	var out AltMut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		s, err := openAlt(c, tx, l, in.ID, in.TabID, in.BaseRevision)
		if err != nil {
			return err
		}
		i, err := s.find(in.AlternativeID)
		if err != nil {
			return err
		}
		items := cloneItems(s.items)
		it := &items[i]
		j := variantIndex(*it, in.VariantID)
		if j < 0 {
			return ops.NotFound("variant")
		}
		moved := it.Variants[j]
		rest := append(append([]alts.Variant(nil), it.Variants[:j]...), it.Variants[j+1:]...)
		at := 0
		if in.AfterVariantID != "" {
			k := -1
			for n, v := range rest {
				if v.ID == in.AfterVariantID {
					k = n
				}
			}
			if k < 0 {
				return ops.NotFound("variant")
			}
			at = k + 1
		}
		it.Variants = append(rest[:at], append([]alts.Variant{moved}, rest[at:]...)...)
		out, err = s.finish(s.body, items, commitOpts{NoCredit: true}, "reorder_variant", it.ID, false)
		return err
	})
	return out, err
}

func cloneItems(in []alts.Item) []alts.Item {
	out := make([]alts.Item, len(in))
	for i, it := range in {
		out[i] = it.Clone()
	}
	return out
}

// ---- alternative.choose -----------------------------------------------------------------------------

// AltChooseIn makes one wording the text.
type AltChooseIn struct {
	ID            string `path:"id" json:"-" desc:"Document id."`
	BaseRevision  int    `json:"base_revision"`
	TabID         string `json:"tab_id,omitempty" desc:"Browser tab id; enables the single-writer lease."`
	AlternativeID string `json:"alternative_id"`
	VariantID     string `json:"variant_id,omitempty" desc:"The wording to put in the text."`
	Step          string `json:"step,omitempty" enum:"next,previous,original" desc:"Instead of a wording: the next or previous one in the cycle (it wraps), or the original. Choosing the original or the previous wording is how a choice is undone."`
	WithContent   bool   `json:"with_content,omitempty"`
}

// DocID lets the registry find the owning project.
func (in AltChooseIn) DocID() string { return in.ID }

func altChoose(c *ops.Ctx, l *leases, in AltChooseIn) (AltMut, error) {
	var out AltMut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		s, err := openAlt(c, tx, l, in.ID, in.TabID, in.BaseRevision)
		if err != nil {
			return err
		}
		i, err := s.find(in.AlternativeID)
		if err != nil {
			return err
		}
		it := s.items[i]
		if it.Orphan {
			return ops.Conflict("This alternative has lost its place in the text. Reattach it first (alternative.reanchor).")
		}
		cur := variantIndex(it, it.Chosen)
		target := -1
		switch {
		case in.VariantID != "":
			if target = variantIndex(it, in.VariantID); target < 0 {
				return ops.NotFound("variant")
			}
		case in.Step == "next":
			target = (cur + 1) % len(it.Variants)
		case in.Step == "previous":
			target = (cur - 1 + len(it.Variants)) % len(it.Variants)
		case in.Step == "original":
			for n, v := range it.Variants {
				if v.Original {
					target = n
				}
			}
		default:
			return ops.Invalid("variant_id", "Name a wording (variant_id) or a step: next, previous or original.")
		}
		if target == cur {
			out, err = s.finish(s.body, s.items, commitOpts{}, "choose", it.ID, in.WithContent)
			return err
		}
		nv := it.Variants[target]
		body, items, rep, err := s.swap(s.items, i, it.ChosenText(), nv.Text, nv.ID)
		if err != nil {
			return err
		}
		note := fmt.Sprintf("Alternative (%s): wording %d of %d", it.Scope, target+1, len(it.Variants))
		out, err = s.finish(body, items, commitOpts{Reason: revisions.ReasonAlternative, Note: note, NoCredit: true}, "choose", it.ID, in.WithContent)
		out.Replaced = rep
		return err
	})
	return out, err
}

// ---- alternative.delete -----------------------------------------------------------------------------

// AltDeleteIn resolves an alternative.
type AltDeleteIn struct {
	ID            string `path:"id" json:"-" desc:"Document id."`
	BaseRevision  int    `json:"base_revision"`
	TabID         string `json:"tab_id,omitempty" desc:"Browser tab id; enables the single-writer lease."`
	AlternativeID string `json:"alternative_id"`
	WithContent   bool   `json:"with_content,omitempty"`
}

// DocID lets the registry find the owning project.
func (in AltDeleteIn) DocID() string { return in.ID }

func altDelete(c *ops.Ctx, l *leases, in AltDeleteIn) (AltMut, error) {
	var out AltMut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		s, err := openAlt(c, tx, l, in.ID, in.TabID, in.BaseRevision)
		if err != nil {
			return err
		}
		i, err := s.find(in.AlternativeID)
		if err != nil {
			return err
		}
		it := s.items[i]
		var rest []alts.Item
		for j, o := range s.items {
			if j != i {
				rest = append(rest, o)
			}
		}
		// The text stays as it is: the chosen wording is what the document says.
		// The wordings that go are kept in the revision recorded before.
		note := fmt.Sprintf("Resolved a %s alternative: kept the chosen wording, dropped %d others", it.Scope, len(it.Variants)-1)
		out, err = s.finish(s.body, rest, commitOpts{Reason: revisions.ReasonAlternative, Note: note, NoCredit: true,
			Keep: "Before an alternative was resolved"}, "delete", "", in.WithContent)
		return err
	})
	return out, err
}

// ---- alternative.reanchor ---------------------------------------------------------------------------

// AltReanchorIn attaches an alternative to a range.
type AltReanchorIn struct {
	ID            string `path:"id" json:"-" desc:"Document id."`
	BaseRevision  int    `json:"base_revision"`
	TabID         string `json:"tab_id,omitempty" desc:"Browser tab id; enables the single-writer lease."`
	AlternativeID string `json:"alternative_id"`
	Start         *int   `json:"start,omitempty" desc:"Start of the new range, in UTF-16 code units of the body."`
	End           *int   `json:"end,omitempty"`
	Quote         string `json:"quote,omitempty" desc:"Instead of offsets: the Markdown source of the text."`
	Before        string `json:"before,omitempty"`
	After         string `json:"after,omitempty"`
	Occurrence    *int   `json:"occurrence,omitempty"`
	WithContent   bool   `json:"with_content,omitempty"`
}

// DocID lets the registry find the owning project.
func (in AltReanchorIn) DocID() string { return in.ID }

func altReanchor(c *ops.Ctx, l *leases, in AltReanchorIn) (AltMut, error) {
	var out AltMut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		s, err := openAlt(c, tx, l, in.ID, in.TabID, in.BaseRevision)
		if err != nil {
			return err
		}
		i, err := s.find(in.AlternativeID)
		if err != nil {
			return err
		}
		a, b, err := s.resolve(Range{Start: in.Start, End: in.End, Quote: in.Quote, Before: in.Before, After: in.After, Occurrence: in.Occurrence})
		if err != nil {
			return err
		}
		items := cloneItems(s.items)
		it := &items[i]
		blocks, ok := text.Blocks(s.body)
		if !ok {
			return ops.Invalid("quote", "This text is too irregular to anchor an alternative to.")
		}
		if err := alts.CheckRange(s.body, a, b, it.Scope, blocks, s.lim); err != nil {
			return invalid("quote", err)
		}
		if o, hit := overlapsActive(items, i, a, b, s.index()); hit {
			return ops.Conflict("That range overlaps another alternative (" + alts.Clip(o.ChosenText()) + ").")
		}
		txt := s.body[a:b]
		j := -1
		for n, v := range it.Variants {
			if v.Text == txt {
				j = n
			}
		}
		if j < 0 {
			if len(it.Variants) >= s.lim.VariantsPer {
				return ops.Invalid("quote", fmt.Sprintf("An alternative holds at most %d wordings.", s.lim.VariantsPer))
			}
			it.Variants = append(it.Variants, manualVariant(txt, c, s.now))
			j = len(it.Variants) - 1
		}
		it.Chosen = it.Variants[j].ID
		it.Orphan = false
		it.Start, it.End = s.index().Unit(a), s.index().Unit(b)
		it.Before, it.After = alts.Contexts(s.body, a, b)
		out, err = s.finish(s.body, items, commitOpts{NoCredit: true}, "reanchor", it.ID, in.WithContent)
		return err
	})
	return out, err
}

// ---- registration ----------------------------------------------------------------------------------------

func registerAlternatives(r *ops.Registry, l *leases) {
	viaDoc := func(role string) ops.Auth { return ops.RequireProjectVia(role, resolveDoc) }
	editor := viaDoc(ops.ProjectEditor)
	ops.Register(r, ops.Op{Name: "alternative.list", Method: "GET", Path: altsPath,
		Desc: "List a document's alternatives: each anchored range with its wordings, which one is in the text, and the lost-anchors list (status orphaned). Returns the revision the anchors belong to.",
		Auth: viaDoc(ops.ProjectReader)}, altList)
	ops.Register(r, ops.Op{Name: "alternative.create", Method: "POST", Path: altsPath, Status: 201,
		Desc: "Make an alternative over a word, a sentence or a paragraph of the text: the text there becomes its original wording, kept chosen, and other wordings can be added. Name the range by offsets or by the Markdown source of the text with its context.", Auth: editor},
		func(c *ops.Ctx, in AltCreateIn) (AltMut, error) { return altCreate(c, l, in) })
	ops.Register(r, ops.Op{Name: "alternative.add_variant", Method: "POST", Path: altsPath + "/variants", Status: 201,
		Desc: "Add a wording to an alternative.", Auth: editor},
		func(c *ops.Ctx, in AltVariantAddIn) (AltMut, error) { return altVariantAdd(c, l, in) })
	ops.Register(r, ops.Op{Name: "alternative.edit_variant", Method: "PATCH", Path: altsPath + "/variants",
		Desc: "Change the words of a wording (never the original). Editing the chosen wording rewrites the text of the document too.", Auth: editor},
		func(c *ops.Ctx, in AltVariantEditIn) (AltMut, error) { return altVariantEdit(c, l, in) })
	ops.Register(r, ops.Op{Name: "alternative.remove_variant", Method: "POST", Path: altsPath + "/variants/remove",
		Desc: "Remove a wording (never the original). Removing the chosen one puts the original back in the text. Add the same words back with after_variant_id to undo.", Auth: editor},
		func(c *ops.Ctx, in AltVariantRemoveIn) (AltMut, error) { return altVariantRemove(c, l, in) })
	ops.Register(r, ops.Op{Name: "alternative.reorder_variant", Method: "POST", Path: altsPath + "/variants/reorder",
		Desc: "Move a wording to another place in the cycle.", Auth: editor},
		func(c *ops.Ctx, in AltVariantReorderIn) (AltMut, error) { return altVariantReorder(c, l, in) })
	ops.Register(r, ops.Op{Name: "alternative.choose", Method: "POST", Path: altsPath + "/choose",
		Desc: "Cycle: write a wording into the text in place of the chosen one, in one step that records a history entry (reason alternative). Choose the original or the previous wording to undo.", Auth: editor},
		func(c *ops.Ctx, in AltChooseIn) (AltMut, error) { return altChoose(c, l, in) })
	ops.Register(r, ops.Op{Name: "alternative.delete", Method: "POST", Path: altsPath + "/delete",
		Desc: "Resolve an alternative: keep the chosen wording in the text and drop the other wordings. They stay in the history entry recorded first.", Auth: editor},
		func(c *ops.Ctx, in AltDeleteIn) (AltMut, error) { return altDelete(c, l, in) })
	ops.Register(r, ops.Op{Name: "alternative.reanchor", Method: "POST", Path: altsPath + "/reanchor",
		Desc: "Attach an alternative (usually an orphaned one) to a range of the text. If the text there is one of its wordings that one becomes chosen, otherwise the text is added as a new wording. It never rewrites the text.", Auth: editor},
		func(c *ops.Ctx, in AltReanchorIn) (AltMut, error) { return altReanchor(c, l, in) })
	registerGhosts(r, l)
	registerDragBack(r, l)
}
