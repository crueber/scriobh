// Package comanchor is the part of review comments that a document's write
// path needs: where a comment sits in the text, and bringing every anchor up to
// date in the same transaction that changes the text. It is a leaf package (it
// imports no domain package) so that internal/domain/docs can call it, as it
// calls altstore, while the comment ops (internal/domain/review) import docs.
//
// An anchor is the alternatives anchor under another name: the range of the
// body in UTF-16 code units with the quote and the words around it, found again
// by alts.Rebase when the text changes. Comments differ from alternatives in
// three ways, each a choice in Options or the scope:
//
//   - anchors may overlap (two reviewers comment on the same sentence), so
//     Rebase runs with AllowOverlap;
//   - the range is any run of characters inside one block of prose, not a word
//     or a sentence (alts.ScopeSpan);
//   - when the writer edits the commented words in place the comment follows the
//     new words (Rebase adopts them), and keeps the text it was made on in
//     Original, so the thread still says what was commented.
//
// Nothing is ever attached by a guess: an anchor whose text cannot be found with
// certainty is orphaned. See docs/OPEN-QUESTIONS.md, "Phase 5 review and sharing
// decisions".
package comanchor

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/text"
)

// Anchor states.
const (
	StateNone     = "none"
	StateActive   = "active"
	StateOrphaned = "orphaned"
)

// MaxQuoteBytes bounds the text a comment is anchored to.
const MaxQuoteBytes = 4000

// MaxAnchoredPerDocument bounds the anchored threads of one document, so the
// re-anchoring a save does stays small whatever is done to it.
const MaxAnchoredPerDocument = 1000

// Anchor is where a thread sits.
type Anchor struct {
	State    string
	Start    int // UTF-16 code units of the body
	End      int
	Quote    string // the text there now
	Original string // the text when the comment was made
	Prefix   string // up to 24 characters of Markdown before
	Suffix   string
	Hash     string // sha256 of Quote, hex
	Revision int    // the document revision Start and End are valid for
}

// Body is the text a comment is anchored in: the document before its trailing
// alternatives comment, with line endings as LF (the measure of alternatives).
func Body(content string) string {
	return alts.Split(alts.NormalizeEOL(content)).Body
}

// QuoteHash is the stored hash of a quote.
func QuoteHash(q string) string {
	h := sha256.Sum256([]byte(q))
	return hex.EncodeToString(h[:])
}

// ErrRange is a range that cannot carry a comment; its message says why.
type ErrRange struct{ Msg string }

func (e *ErrRange) Error() string { return e.Msg }

func bad(msg string) error { return &ErrRange{Msg: msg} }

// Check says whether body[a:b] may carry a comment: not empty, inside one block of
// prose (not code, a table, raw HTML, front matter or a private note), trimmed,
// cut at character edges, clear of ghost and alternatives markers, and clear of
// anything hidden from a reader (a private note, ghosted text): the quote is
// stored with the comment and shown to everyone who can read it.
func Check(body string, a, b int) error {
	if a < 0 || b > len(body) || a >= b {
		return bad("The range is empty or outside the text.")
	}
	if b-a > MaxQuoteBytes {
		return bad("A comment can be anchored to at most 4,000 bytes of text; anchor it to a sentence, or comment on the document.")
	}
	blocks, ok := text.Blocks(body)
	if !ok {
		return bad("This text is too irregular to anchor a comment to.")
	}
	if err := alts.CheckPlacement(body, a, b, alts.ScopeSpan, blocks, alts.Limits{}); err != nil {
		return bad(err.Error())
	}
	if err := alts.CheckVariantText(body[a:b], alts.ScopeSpan, alts.Limits{}); err != nil {
		return bad("That range cannot carry a comment: " + strings.TrimPrefix(err.Error(), "A variant "))
	}
	if text.TouchesSpans(Hidden(body), a, b) {
		return bad("That range lies in, or reaches into, a private note, ghosted text or an HTML comment, which a comment cannot be anchored to.")
	}
	return nil
}

// New builds the anchor of body[a:b] at a revision. The range has been checked.
func New(body string, a, b, revision int) Anchor {
	ix := alts.NewIndex(body)
	before, after := alts.Contexts(body, a, b)
	q := body[a:b]
	return Anchor{State: StateActive, Start: ix.Unit(a), End: ix.Unit(b), Quote: q, Original: q, Prefix: before, Suffix: after, Hash: QuoteHash(q), Revision: revision}
}

// Range is where a client says a comment goes: offsets (UTF-16 units of the body
// at the revision it holds) or the Markdown source of the text with the words
// around it.
type Range struct {
	Start      *int
	End        *int
	Quote      string
	Before     string
	After      string
	Occurrence *int
	// Plain says Quote, Before and After are text as a reader sees it rendered
	// (a selection in the reading view), not Markdown source. The selection is
	// looked for in the visible text only (text.PlainBlocks: not front matter,
	// private notes, ghosted text, link or image targets, a wiki link's target, raw
	// HTML or footnote bodies) and placed in the source through the map from that
	// text back to it, so formatting inside a selection is no obstacle and a phrase
	// that exists only where a reader cannot see cannot be found, or make a selection
	// ambiguous, or be confirmed.
	Plain bool
}

// ErrStale says the offsets belong to an older revision of the text.
var ErrStale = errors.New("comanchor: offsets from another revision")

// Resolve turns a Range into a checked byte range of body. Offsets are accepted
// only when atRevision is the document's current revision; a quote is found by
// exact search at any revision, told apart by its context and occurrence, and
// refused when it is ambiguous.
func Resolve(body string, r Range, currentRevision, atRevision int) (a, b int, err error) {
	if r.Start != nil || r.End != nil {
		if r.Start == nil || r.End == nil {
			return 0, 0, bad("Send both start and end, or a quote.")
		}
		if atRevision != currentRevision {
			return 0, 0, ErrStale
		}
		ix := alts.NewIndex(body)
		var ok1, ok2 bool
		a, ok1 = ix.Byte(*r.Start)
		b, ok2 = ix.Byte(*r.End)
		if !ok1 || !ok2 || a >= b {
			return 0, 0, bad("That range is empty, outside the text, or cuts a character in two (offsets are UTF-16 code units of the body).")
		}
	} else if r.Plain {
		if a, b, err = resolvePlain(body, r); err != nil {
			return 0, 0, err
		}
	} else {
		var qerr error
		if a, b, qerr = alts.ResolveQuote(body, r.Quote, r.Before, r.After, r.Occurrence); qerr != nil {
			return 0, 0, bad(qerr.Error())
		}
	}
	if err := Check(body, a, b); err != nil {
		return 0, 0, err
	}
	return a, b, nil
}

// ResolvePlain places a selection made on rendered text (r.Quote, with the words the reader saw around it) in the
// source, without the checks a comment's range must pass: a caller that has checks of its own (a suggestion's) uses
// it. Only the visible text is searched.
func ResolvePlain(body string, r Range) (int, int, error) { return resolvePlain(body, r) }

// squash collapses white space the way a browser shows it.
func squash(s string) string { return strings.Join(strings.Fields(s), " ") }

// resolvePlain places a selection made on rendered text. Every occurrence in the visible text
// is a candidate; the words the reader saw around it tell candidates apart, and more or fewer
// than one left is an error: nothing is guessed.
func resolvePlain(body string, r Range) (int, int, error) {
	sel := squash(r.Quote)
	if sel == "" {
		return 0, 0, bad("Name the text: a quote or a start and an end.")
	}
	blocks, ok := text.PlainBlocks(body)
	if !ok {
		return 0, 0, bad("This text is too irregular to anchor a comment to.")
	}
	before, after := squash(r.Before), squash(r.After)
	type cand struct {
		a, b  int
		exact bool
	}
	var all []cand
	for _, bl := range blocks {
		for from := 0; from <= len(bl.Text) && len(all) < 2000; {
			i := strings.Index(bl.Text[from:], sel)
			if i < 0 {
				break
			}
			at := from + i
			from = at + 1
			lo, hi, ok := bl.Source(at, at+len(sel))
			if !ok {
				continue
			}
			pre, post := strings.TrimRight(bl.Text[:at], " "), strings.TrimLeft(bl.Text[at+len(sel):], " ")
			exact := (before == "" || strings.HasSuffix(pre, before) || strings.HasSuffix(before, pre)) &&
				(after == "" || strings.HasPrefix(post, after) || strings.HasPrefix(after, post))
			all = append(all, cand{lo, hi, exact})
		}
	}
	if r.Occurrence != nil {
		if *r.Occurrence < 0 || *r.Occurrence >= len(all) {
			return 0, 0, bad("That text is not in the document at this revision.")
		}
		return all[*r.Occurrence].a, all[*r.Occurrence].b, nil
	}
	var exact []cand
	for _, c := range all {
		if c.exact {
			exact = append(exact, c)
		}
	}
	switch {
	case len(all) == 1:
		return all[0].a, all[0].b, nil
	case len(exact) == 1:
		return exact[0].a, exact[0].b, nil
	case len(all) == 0:
		return 0, 0, bad("That text is not in the document at this revision.")
	}
	return 0, 0, bad("That text occurs in several places; send the words around it.")
}

// Stat is what AfterWrite did.
type Stat struct {
	Anchored int
	Moved    int
	Adopted  int
	Orphaned int
	Restored int
}

type row struct {
	id                                        string
	start, end, revision                      int
	quote, original, prefix, suffix, hash, st string
}

// AfterWrite brings the anchors of a document's threads up to date after its text
// changed from oldContent to newContent (the revision it now has). It runs in the
// write's transaction, so a comment is never read against text it has not been
// re-anchored to; a document with no anchored threads costs one indexed query.
// Every write path that changes documents.content calls it.
func AfterWrite(tx *sql.Tx, docID, oldContent, newContent string, revision int, now string) (Stat, error) {
	return afterWrite(tx, docID, oldContent, newContent, revision, now, false)
}

// AfterWriteKeepingLost is AfterWrite for a write that has just orphaned some comments on purpose (a
// split orphans the comments on the text it moves): those are left as they are and not looked for again
// in the same transaction.
func AfterWriteKeepingLost(tx *sql.Tx, docID, oldContent, newContent string, revision int, now string) (Stat, error) {
	return afterWrite(tx, docID, oldContent, newContent, revision, now, true)
}

// maxProcessed bounds the threads one save re-anchors: the open ones are capped at creation
// (MaxAnchoredPerDocument) and so are the resolved ones; anything beyond is made orphaned, so no
// comment ever claims a place it was not checked at.
const maxProcessed = 2 * MaxAnchoredPerDocument

func afterWrite(tx *sql.Tx, docID, oldContent, newContent string, revision int, now string, keepLost bool) (Stat, error) {
	var st Stat
	rows, err := tx.Query(`SELECT id, anchor_start, anchor_end, anchor_quote, anchor_original, anchor_prefix, anchor_suffix, anchor_hash, anchor_state, anchor_revision
		FROM comments WHERE document_id = ? AND parent_id IS NULL AND anchor_state IN ('active','orphaned') AND deleted_at IS NULL
		ORDER BY anchor_start, id LIMIT ?`, docID, maxProcessed+1)
	if err != nil {
		return st, err
	}
	var cur []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.start, &r.end, &r.quote, &r.original, &r.prefix, &r.suffix, &r.hash, &r.st, &r.revision); err != nil {
			rows.Close()
			return st, err
		}
		cur = append(cur, r)
	}
	if err := rows.Close(); err != nil {
		return st, err
	}
	if len(cur) > maxProcessed {
		cur = cur[:maxProcessed]
		if _, err := tx.Exec(`UPDATE comments SET anchor_state = 'orphaned', updated_at = ? WHERE id IN (SELECT id FROM comments WHERE document_id = ? AND parent_id IS NULL AND anchor_state IN ('active','orphaned') AND deleted_at IS NULL
			ORDER BY anchor_start, id LIMIT -1 OFFSET ?) AND anchor_state = 'active'`, now, docID, maxProcessed); err != nil {
			return st, err
		}
	}
	if keepLost {
		kept := cur[:0]
		for _, r := range cur {
			if r.st != StateOrphaned {
				kept = append(kept, r)
			}
		}
		cur = kept
	}
	if len(cur) == 0 {
		return st, nil
	}
	oldBody, newBody := Body(oldContent), Body(newContent)
	if oldBody == newBody {
		// Only the alternatives line (or nothing) changed: no anchor moves.
		_, err := tx.Exec(`UPDATE comments SET anchor_revision = ? WHERE document_id = ? AND parent_id IS NULL AND anchor_state = 'active' AND deleted_at IS NULL`, revision, docID)
		return st, err
	}
	items := make([]alts.Item, len(cur))
	for i, r := range cur {
		items[i] = alts.Item{ID: r.id, Scope: alts.ScopeSpan, Start: r.start, End: r.end, Before: r.prefix, After: r.suffix, Chosen: "q",
			Orphan: r.st == StateOrphaned, Variants: []alts.Variant{{ID: "q", Text: r.quote, Original: true, Source: alts.SourceManual}}}
	}
	moved, rep := alts.Rebase(oldBody, newBody, items, nil, alts.Options{NewID: db.NewID, Now: now, AllowOverlap: true})
	_ = rep
	hidden := Hidden(newBody)
	ix := alts.NewIndex(newBody)
	// A comment whose commented words stood in two places before the edit, and which the edit
	// touched, may be found again only where ALL of the context it had still surrounds it. Rebase
	// would also accept "the one copy of this long passage left in the text", and that is the other
	// copy, not the place that was commented on.
	var oldIx *alts.Index
	win := alts.Window(oldBody, newBody)
	hopped := func(r row, it alts.Item) bool {
		if it.Orphan || len(win) == 0 {
			return false
		}
		if r.st == StateOrphaned {
			// A lost comment comes back only where ALL of the context it last had surrounds its
			// words again (an undo, a restore): a like passage that is the only copy left is
			// not the place that was commented on.
			na, ok1 := ix.Byte(it.Start)
			nb, ok2 := ix.Byte(it.End)
			return !ok1 || !ok2 || !(strings.HasSuffix(newBody[:na], r.prefix) && strings.HasPrefix(newBody[nb:], r.suffix))
		}
		if v, ok := it.ChosenVariant(); !ok || v.Text != r.quote {
			return false // the writer edited the words themselves: adopted, not found elsewhere
		}
		if oldIx == nil {
			oldIx = alts.NewIndex(oldBody)
		}
		oa, ok1 := oldIx.Byte(r.start)
		ob, ok2 := oldIx.Byte(r.end)
		if !ok1 || !ok2 || !(win[0].Start < ob && win[0].End > oa) {
			return false // the edit did not touch it: it only moved with the text around it
		}
		if strings.Count(oldBody, r.quote) < 2 {
			return false // it was the only copy: where it is now is where it went
		}
		na, ok1 := ix.Byte(it.Start)
		nb, ok2 := ix.Byte(it.End)
		return !ok1 || !ok2 || !(strings.HasSuffix(newBody[:na], r.prefix) && strings.HasPrefix(newBody[nb:], r.suffix))
	}
	for i, it := range moved {
		r := cur[i]
		if hopped(r, it) {
			it.Orphan = true
		}
		n := row{id: r.id, start: it.Start, end: it.End, revision: revision, prefix: it.Before, suffix: it.After, original: r.original, st: StateActive}
		if v, ok := it.ChosenVariant(); ok {
			n.quote = v.Text
		}
		if it.Orphan {
			n.st = StateOrphaned
			n.quote, n.start, n.end, n.prefix, n.suffix = r.quote, r.start, r.end, r.prefix, r.suffix
		} else if len(hidden) > 0 {
			if a, ok1 := ix.Byte(it.Start); ok1 {
				if b, ok2 := ix.Byte(it.End); ok2 && text.TouchesSpans(hidden, a, b) {
					// The words are now ghosted or inside a private note: not a place a
					// comment may stand. It keeps its last anchor and waits to be re-anchored.
					n.st = StateOrphaned
					n.quote, n.start, n.end, n.prefix, n.suffix = r.quote, r.start, r.end, r.prefix, r.suffix
				}
			}
		}
		n.hash = r.hash
		if n.st == StateActive && n.quote != r.quote {
			n.hash = QuoteHash(n.quote)
			st.Adopted++
		}
		switch {
		case n.st == StateOrphaned && r.st != StateOrphaned:
			st.Orphaned++
		case n.st == StateActive && r.st == StateOrphaned:
			st.Restored++
		}
		if n.st == StateActive {
			st.Anchored++
			if n.start != r.start || n.end != r.end {
				st.Moved++
			}
		}
		unchanged := n.st == r.st && n.start == r.start && n.end == r.end && n.quote == r.quote && n.prefix == r.prefix && n.suffix == r.suffix
		if unchanged && n.st == StateOrphaned {
			continue // a lost anchor stays as it was
		}
		if unchanged {
			if _, err := tx.Exec(`UPDATE comments SET anchor_revision = ? WHERE id = ?`, revision, r.id); err != nil {
				return st, err
			}
			continue
		}
		if _, err := tx.Exec(`UPDATE comments SET anchor_state = ?, anchor_start = ?, anchor_end = ?, anchor_quote = ?, anchor_prefix = ?, anchor_suffix = ?,
			anchor_hash = ?, anchor_revision = ?, updated_at = ? WHERE id = ?`,
			n.st, n.start, n.end, n.quote, n.prefix, n.suffix, n.hash, revision, now, r.id); err != nil {
			return st, err
		}
	}
	return st, nil
}

// Clip shortens a quote for a message.
func Clip(s string) string {
	if utf8.RuneCountInString(s) <= 40 {
		return s
	}
	r := []rune(s)
	return string(r[:40]) + "..."
}
