package docs

import (
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/comanchor"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// The write seam for accepted AI suggestions. The AI ops (internal/domain/aiwork)
// decide WHAT may be written (a text a recorded run produced, held by the
// server); this file is HOW it is written, through the same session the
// alternatives and ghost ops use, so that anchors are rebased, ghosts stay in
// step with the text, the search text, links, mentions and history are written,
// and the writer's goal earns nothing for words a model wrote.
//
// Nothing here is called from an op that takes text from a client.

// Leases is the single-writer lease table of the document ops.
type Leases = leases

const leasesService = "docs.leases"

// LeasesOf returns the lease table docs.Register made, nil without one (a
// schema-only build). The AI ops capture it when they are registered.
func LeasesOf(env *ops.Env) *Leases {
	l, _ := ops.Service[*Leases](env, leasesService)
	return l
}

// ReasonAIAccepted is the history reason of an accepted suggestion.
const ReasonAIAccepted = "ai-accepted"

// MaxAIEdits bounds the edits of one accept (accept_all of a chosen set).
const MaxAIEdits = 500

// AIEdit replaces body[Start:End] (UTF-16 code units) with New. Expect is the
// exact source the range holds now; a different text refuses the whole write.
// Start == End inserts New.
type AIEdit struct {
	Start, End int
	Expect     string
	New        string
}

// AIEditsIn is a write of one or more edits as one revision.
type AIEditsIn struct {
	DocID        string
	ProjectID    string
	BaseRevision int
	TabID        string
	Edits        []AIEdit
	// Note is the history note. It names the recipe and the run, never text.
	Note string
}

// CheckAIText says whether a model-written text may enter a document where it
// replaces nothing: no control characters, no invisible or direction-overriding
// characters (text.IsInvisible: every format character but the joiners), no
// private-note, ghost, alternatives or HTML-comment markers, no link, image or
// address, and only well-formed UTF-8. An empty text is valid (a trim deletes).
func CheckAIText(s string) error { return CheckAIEdit("", s) }

// CheckAIEdit is CheckAIText for a text that replaces original: the links, images
// and addresses original already has may stay (text.LinkProblem), none may be added.
func CheckAIEdit(original, s string) error { return checkInsertion(original, s, "an AI suggestion") }

// CheckInsertion is CheckAIEdit for the text a person suggests: text that arrives
// from outside the writer's own hands is held to one rule, whoever or whatever
// wrote it.
func CheckInsertion(original, s string) error { return checkInsertion(original, s, "a suggestion") }

func checkInsertion(original, s, noun string) error {
	switch {
	case !utf8.ValidString(s):
		return ops.Invalid("text", "That text is not valid UTF-8.")
	case strings.Contains(s, "%%"):
		return ops.Invalid("text", "That text holds %% (a private note marker), which "+noun+" never inserts.")
	case strings.Contains(s, "scriobh:alts") || strings.Contains(s, text.GhostOpen) || strings.Contains(s, text.GhostClose):
		return ops.Invalid("text", "That text holds an alternatives or ghost marker, which "+noun+" never inserts.")
	case strings.Contains(s, "<!--") || strings.Contains(s, "-->"):
		return ops.Invalid("text", "That text holds an HTML comment, which "+noun+" never inserts.")
	}
	for _, r := range s {
		switch {
		case r == '\n' || r == '\t':
		case r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0):
			return ops.Invalid("text", "That text holds a control character.")
		case text.IsInvisible(r):
			return ops.Invalid("text", "That text holds an invisible or direction-overriding character, which "+noun+" never inserts.")
		case unicode.Is(unicode.Co, r) || r == utf8.RuneError:
			return ops.Invalid("text", "That text holds a character that cannot be shown.")
		}
	}
	if why := text.LinkProblem(s, original); why != "" {
		return ops.Invalid("text", "That text may not be inserted: "+why+".")
	}
	return nil
}

// blockLineRe finds the lines that start a block other than a paragraph: a
// heading, a quote, a list item, a fence, a table row, a rule, an indented
// code line, a ::: block.
var blockLineRe = regexp.MustCompile(`(?m)^(?:[ ]{0,3}(?:#{1,6}(?:[ \t]|$)|>|[-*+][ \t]|\d{1,9}[.)][ \t]|` + "```" + `|~~~|:::|\||(?:[-*_=][ \t]*){3,}$)|[ ]{4,}\S|\t+\S)`)

// blockLines are the lines of s that would start a block that is not a paragraph.
func blockLines(s string) []string {
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		if blockLineRe.MatchString(ln) {
			out = append(out, ln)
		}
	}
	return out
}

// newBlockLines says whether fragment holds a block-starting line that the
// original text it replaces did not hold (the same line, character for
// character, may stay).
func newBlockLines(original, fragment string) bool {
	have := map[string]bool{}
	for _, l := range blockLines(original) {
		have[l] = true
	}
	for _, l := range blockLines(fragment) {
		if !have[l] {
			return true
		}
	}
	return false
}

// structureKeptStrict is structureKept that fails closed: a text the block parser
// cannot place is not accepted.
func structureKeptStrict(old, nw string, a, b, newLen int) bool {
	if strings.ContainsAny(old, "\x00\r") || strings.ContainsAny(nw, "\x00\r") {
		return false // the block parser reads such text with other offsets: not placeable
	}
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
	return ok1 && ok2 && alts.SameBlocks(bo, bn)
}

// insertKept checks an insertion at byte a of old (newLen bytes now in nw): the
// point lies in a text block, and the blocks around it are the same ones with
// more paragraphs, none of them a heading, a list item or a quote line.
func insertKept(old, nw string, a, newLen int) bool {
	if strings.ContainsAny(old, "\x00\r") || strings.ContainsAny(nw, "\x00\r") {
		return false // the block parser reads such text with other offsets: not placeable
	}
	lo := strings.LastIndex(old[:a], "\n\n")
	if lo < 0 {
		lo = 0
	} else {
		lo += 2
	}
	hiOld := len(old)
	if i := strings.Index(old[a:], "\n\n"); i >= 0 {
		hiOld = a + i
	}
	hiNew := hiOld + newLen
	if hiNew > len(nw) {
		return false
	}
	bo, ok1 := text.Blocks(old[lo:hiOld])
	bn, ok2 := text.Blocks(nw[lo:hiNew])
	if !ok1 || !ok2 {
		return false
	}
	inside := false
	for _, bl := range bo {
		if a-lo >= bl.Start && a-lo <= bl.End {
			inside = true
			// a line break put into a list item or a quote line would end the block it sits in
			if !bl.Top && strings.Contains(nw[a:a+newLen], "\n") {
				return false
			}
		}
	}
	if !inside {
		return false
	}
	count := func(bs []text.Block) (heads, nested int) {
		for _, x := range bs {
			if x.Heading {
				heads++
			}
			if !x.Top {
				nested++
			}
		}
		return
	}
	h1, n1 := count(bo)
	h2, n2 := count(bn)
	return len(bn) >= len(bo) && h1 == h2 && n1 == n2
}

// ApplyAIEdits writes edits to a document in the caller's transaction as one
// revision (reason ai-accepted, no goal credit, the text before kept first).
// The range of an edit may not hold or cut into a ghost, a private note or an
// alternative's anchor; a replacement may not change the document's blocks
// (a heading, a list, a quote, a blank line appear or go).
func ApplyAIEdits(c *ops.Ctx, tx *sql.Tx, l *Leases, in AIEditsIn) (AltMut, error) {
	return applyEdits(c, tx, l, in, editPolicy{Reason: ReasonAIAccepted, Keep: "Before an AI suggestion was accepted", Noun: "an AI suggestion"})
}

// ReasonSuggestionAccepted is the history reason of a collaborator's suggestion
// an editor accepted.
const ReasonSuggestionAccepted = "suggestion-accepted"

// ApplyReviewEdits is ApplyAIEdits for the accepted suggestion of a person: the
// same checks (revision, lease, hidden ranges, alternatives, structure, the
// safety of the replacement text), one revision with reason suggestion-accepted,
// the text before kept first, and no goal credit: the words are a collaborator's,
// and accepting them is an editing decision, not writing.
func ApplyReviewEdits(c *ops.Ctx, tx *sql.Tx, l *Leases, in AIEditsIn) (AltMut, error) {
	return applyEdits(c, tx, l, in, editPolicy{Reason: ReasonSuggestionAccepted, Keep: "Before a suggestion was accepted", Noun: "a suggestion"})
}

// editPolicy is what differs between the writers that use the edit seam.
type editPolicy struct {
	Reason, Keep, Noun string
}

func applyEdits(c *ops.Ctx, tx *sql.Tx, l *Leases, in AIEditsIn, pol editPolicy) (AltMut, error) {
	if len(in.Edits) == 0 || len(in.Edits) > MaxAIEdits {
		return AltMut{}, ops.Invalid("edits", fmt.Sprintf("Send between 1 and %d edits.", MaxAIEdits))
	}
	var pid string
	if err := tx.QueryRow(`SELECT project_id FROM documents WHERE id = ?`, in.DocID).Scan(&pid); err != nil || pid != in.ProjectID {
		return AltMut{}, ops.NotFound("document")
	}
	s, err := openAlt(c, tx, l, in.DocID, in.TabID, in.BaseRevision)
	if err != nil {
		return AltMut{}, err
	}
	s.quiet = true
	nb, ae, err := planEdits(s, in.Edits, pol.Noun)
	if err != nil {
		return AltMut{}, err
	}
	if nb == s.body {
		return s.finish(s.body, s.items, commitOpts{}, "ai", "", false)
	}
	items := s.items
	if len(items) > 0 {
		items, _ = alts.Rebase(s.body, nb, s.items, ae, s.ro)
	}
	return s.finish(nb, items, commitOpts{Reason: pol.Reason, Note: in.Note, NoCredit: true, Keep: pol.Keep}, "ai", "", false)
}

// planEdits checks edits against the text of an open document and builds the text with
// them applied (the edits right to left, as byte edits for the re-anchoring). It writes
// nothing: CheckReviewEdits calls it to refuse a suggestion when it is made, applyEdits
// to write it when it is accepted, so the two cannot disagree.
func planEdits(s *altSession, in []AIEdit, noun string) (string, []alts.Edit, error) {
	type span struct{ a, b int }
	hidden := comanchor.Hidden(s.body) // private notes, ghosted text and HTML comments: none of them is ever changed by a suggestion
	edits := append([]AIEdit(nil), in...)
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].Start < edits[j].Start })
	spans := make([]span, len(edits))
	for i, e := range edits {
		if e.End < e.Start {
			return "", nil, ops.Invalid("edits", "A range ends before it starts.")
		}
		a, ok1 := s.index().Byte(e.Start)
		b, ok2 := s.index().Byte(e.End)
		if !ok1 || !ok2 || b > len(s.body) {
			return "", nil, ops.Conflict("The text changed since the suggestion was made: the range is no longer inside it.")
		}
		if s.body[a:b] != e.Expect {
			return "", nil, ops.ConflictWith("The text changed since the suggestion was made, so it was not applied.", map[string]any{"reason": "stale", "current_revision": s.h.Revision})
		}
		if err := checkInsertion(e.Expect, e.New, noun); err != nil {
			return "", nil, err
		}
		if a < b && newBlockLines(e.Expect, e.New) || a == b && len(blockLines(e.New)) > 0 {
			return "", nil, ops.Invalid("edits", "That change would start a heading, a list, a quote, a fence or a table the text did not have.")
		}
		if alts.CutsGrapheme(s.body, a) || alts.CutsGrapheme(s.body, b) {
			return "", nil, ops.Invalid("edits", "A range cuts a character in two.")
		}
		if text.TouchesSpans(hidden, a, b) {
			return "", nil, ops.Conflict("A range lies in, or reaches into, a private note or ghosted text, which " + noun + " never changes.")
		}
		if alts.TouchesMarker(s.body, a, b) {
			return "", nil, ops.Conflict("A range holds or cuts into ghosted text. Revive the ghosted text first, or choose another range.")
		}
		if strings.Contains(s.body[a:b], "%%") {
			return "", nil, ops.Conflict("A range holds a private note, which " + noun + " never replaces.")
		}
		if i > 0 && a < spans[i-1].b {
			return "", nil, ops.Invalid("edits", "Two edits overlap.")
		}
		for _, it := range s.items {
			if it.Orphan || !it.Placed() {
				continue
			}
			oa, ok1 := s.index().Byte(it.Start)
			ob, ok2 := s.index().Byte(it.End)
			if !ok1 || !ok2 {
				continue
			}
			if (a < b && a < ob && b > oa) || (a == b && a > oa && a < ob) {
				return "", nil, ops.Conflict("A range overlaps an alternative (" + alts.Clip(it.ChosenText()) + "). Resolve it first, or choose another range.")
			}
		}
		spans[i] = span{a, b}
	}
	nb := s.body
	var ae []alts.Edit
	for i := len(edits) - 1; i >= 0; i-- { // right to left: the offsets before an edit do not move
		e, sp := edits[i], spans[i]
		prev := nb
		nb = nb[:sp.a] + e.New + nb[sp.b:]
		if sp.a < sp.b && !structureKeptStrict(prev, nb, sp.a, sp.b, len(e.New)) || sp.a == sp.b && !insertKept(prev, nb, sp.a, len(e.New)) {
			return "", nil, ops.Invalid("edits", "That change would alter the structure of the text (a heading or list mark, a quote or a blank line).")
		}
		ae = append(ae, alts.Edit{Start: sp.a, End: sp.b, NewLen: len(e.New)})
	}
	return nb, ae, nil
}

// CheckReviewEdits says whether edits could be applied to a document now, as an
// accepted suggestion would be (planEdits: the same range, hidden-text, alternative,
// text-safety and structure checks), without writing anything. base is the revision
// the edits were made at and must be current.
func CheckReviewEdits(c *ops.Ctx, tx *sql.Tx, projectID, docID string, base int, edits []AIEdit) error {
	if len(edits) == 0 || len(edits) > MaxAIEdits {
		return ops.Invalid("changes", fmt.Sprintf("Send between 1 and %d changes.", MaxAIEdits))
	}
	var pid string
	if err := tx.QueryRow(`SELECT project_id FROM documents WHERE id = ?`, docID).Scan(&pid); err != nil || pid != projectID {
		return ops.NotFound("document")
	}
	s, err := openAlt(c, tx, nil, docID, "", base)
	if err != nil {
		return err
	}
	_, _, err = planEdits(s, edits, "a suggestion")
	return err
}

// AIAltIn adds model-written wordings to an alternative, or makes the
// alternative over a range first.
type AIAltIn struct {
	DocID        string
	ProjectID    string
	BaseRevision int
	TabID        string
	// AlternativeID adds to an existing alternative. Empty makes a new one over
	// Start..End (UTF-16 of the body), whose current text must be Expect.
	AlternativeID string
	Scope         string
	Start, End    int
	Expect        string
	Variants      []string
	// Choose, from 1, writes that variant into the text in the same save. 0
	// leaves the chosen wording alone.
	Choose int
}

// AddGeneratedVariants records Variants as wordings with source generated.
// The text of the document does not change unless Choose asks for it.
func AddGeneratedVariants(c *ops.Ctx, tx *sql.Tx, l *Leases, in AIAltIn) (AltMut, error) {
	if len(in.Variants) == 0 || len(in.Variants) > 10 {
		return AltMut{}, ops.Invalid("variants", "Send between 1 and 10 wordings.")
	}
	if in.Choose < 0 || in.Choose > len(in.Variants) {
		return AltMut{}, ops.Invalid("choose", "That wording is not in the list.")
	}
	var pid string
	if err := tx.QueryRow(`SELECT project_id FROM documents WHERE id = ?`, in.DocID).Scan(&pid); err != nil || pid != in.ProjectID {
		return AltMut{}, ops.NotFound("document")
	}
	s, err := openAlt(c, tx, l, in.DocID, in.TabID, in.BaseRevision)
	if err != nil {
		return AltMut{}, err
	}
	gen := func(t string) alts.Variant {
		return alts.Variant{ID: db.NewID("alv"), Text: t, Source: alts.SourceGenerated, By: c.P.UserID, At: s.now}
	}
	items := cloneItems(s.items)
	var it *alts.Item
	idx := -1
	if in.AlternativeID != "" {
		i, err := s.find(in.AlternativeID)
		if err != nil {
			return AltMut{}, err
		}
		idx, it = i, &items[i]
		if it.Orphan {
			return AltMut{}, ops.Conflict("This alternative has lost its place in the text. Reattach it first.")
		}
		if a, b, err := s.byteRange(*it); err != nil || s.body[a:b] != it.ChosenText() {
			return AltMut{}, ops.Conflict("This alternative no longer matches the text. Reload the document and reattach it.")
		}
	} else {
		if len(items) >= s.lim.PerDoc {
			return AltMut{}, ops.Invalid("scope", fmt.Sprintf("A document holds at most %d alternatives.", s.lim.PerDoc))
		}
		a, ok1 := s.index().Byte(in.Start)
		b, ok2 := s.index().Byte(in.End)
		if !ok1 || !ok2 || a >= b || b > len(s.body) {
			return AltMut{}, ops.Conflict("The text changed since the suggestion was made: the range is no longer inside it.")
		}
		if s.body[a:b] != in.Expect {
			return AltMut{}, ops.ConflictWith("The text changed since the suggestion was made, so nothing was added.", map[string]any{"reason": "stale", "current_revision": s.h.Revision})
		}
		blocks, ok := text.Blocks(s.body)
		if !ok {
			return AltMut{}, ops.Invalid("scope", "This text is too irregular to anchor an alternative to.")
		}
		if err := alts.CheckRange(s.body, a, b, in.Scope, blocks, s.lim); err != nil {
			return AltMut{}, invalid("scope", err)
		}
		if o, hit := overlapsActive(s.items, -1, a, b, s.index()); hit {
			return AltMut{}, ops.Conflict("That range overlaps another alternative (" + alts.Clip(o.ChosenText()) + "). Alternatives do not nest: resolve or reattach it first.")
		}
		orig := alts.Variant{ID: db.NewID("alv"), Text: s.body[a:b], Original: true, Source: alts.SourceManual, By: c.P.UserID, At: s.now}
		n := alts.Item{ID: db.NewID("alt"), Scope: in.Scope, Start: s.index().Unit(a), End: s.index().Unit(b), Chosen: orig.ID, Variants: []alts.Variant{orig}}
		n.Before, n.After = alts.Contexts(s.body, a, b)
		items = append(items, n)
		idx = len(items) - 1
		it = &items[idx]
	}
	// what the wordings may keep of the text: its own links, nothing new
	origText := in.Expect
	if in.AlternativeID != "" {
		if o, ok := it.Original(); ok {
			origText = o.Text
		}
	}
	for _, v := range in.Variants {
		if err := CheckAIEdit(origText, v); err != nil {
			return AltMut{}, err
		}
		if newBlockLines(origText, v) {
			return AltMut{}, ops.Invalid("variants", "A wording may not start a heading, a list, a quote, a fence or a table the text did not have.")
		}
	}
	var added []alts.Variant
	have := map[string]bool{}
	for _, v := range it.Variants {
		have[v.Text] = true
	}
	for _, t := range in.Variants {
		if have[t] {
			continue // the text already holds that wording
		}
		have[t] = true
		if len(it.Variants) >= s.lim.VariantsPer {
			return AltMut{}, ops.Invalid("variants", fmt.Sprintf("An alternative holds at most %d wordings.", s.lim.VariantsPer))
		}
		nv := gen(t)
		it.Variants = append(it.Variants, nv)
		added = append(added, nv)
	}
	if err := alts.ValidateItem(*it, s.lim); err != nil {
		return AltMut{}, invalid("variants", err)
	}
	opts := commitOpts{NoCredit: true}
	body := s.body
	var rep *AltReplace
	if in.Choose > 0 {
		want := in.Variants[in.Choose-1]
		var target alts.Variant
		for _, v := range it.Variants {
			if v.Text == want {
				target = v
			}
		}
		if target.ID != "" && target.ID != it.Chosen {
			body, items, rep, err = s.swap(items, idx, it.ChosenText(), target.Text, target.ID)
			if err != nil {
				return AltMut{}, err
			}
			opts.Reason, opts.Note = ReasonAIAccepted, fmt.Sprintf("AI wording chosen (alternative, %s)", it.Scope)
			opts.Keep = "Before an AI wording was chosen"
		}
	}
	out, err := s.finish(body, items, opts, "add_variant", it.ID, false)
	out.Replaced = rep
	return out, err
}
