package review

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/comanchor"
	"github.com/crueber/scriobh/internal/ops"
	textpkg "github.com/crueber/scriobh/internal/text"
)

// What a Word file's tracked changes and comments become. They are written by
// the importer in its own transaction, through the same checks a person's
// suggestion or comment passes, with the Word author's name as a label: the
// author is not an account, so author_id stays empty and nothing is notified
// (a person who imports a file does not mail themselves a thousand notices).

// MaxLabel is the longest author label kept.
const MaxLabel = 100

// OpenSuggestions counts the open suggestions of a project.
func OpenSuggestions(tx *sql.Tx, projectID string) (int, error) {
	var n int
	err := tx.QueryRow(`SELECT count(*) FROM suggestions WHERE project_id = ? AND status = 'open'`, projectID).Scan(&n)
	return n, err
}

// CleanLabel makes an author's name from a Word file into a label: plain text,
// no control or invisible characters, bounded.
func CleanLabel(s string) string {
	s = guestFold(text1(s))
	// "Guest: Ann" is how a guest of a share link is shown: a Word author cannot look like one,
	// however the colon is written (a full-width one, a space before it, a small or a modifier one).
	for {
		t := strings.TrimSpace(s)
		if len(t) >= 5 && strings.EqualFold(t[:5], "guest") {
			rest := strings.TrimLeft(t[5:], " \t")
			if strings.HasPrefix(rest, ":") {
				s = strings.TrimSpace(rest[1:])
				continue
			}
		}
		s = t
		break
	}
	if utf8.RuneCountInString(s) > MaxLabel {
		r := []rune(s)
		s = string(r[:MaxLabel])
	}
	if s == "" {
		s = "Word"
	}
	return s
}

// guestFold maps the look-alikes of ASCII letters, spaces and the colon (full-width forms, the ideographic and
// other spaces, small and modifier colons) to the plain character, as NFKC would for these.
func guestFold(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r >= 0xFF01 && r <= 0xFF5E:
			return r - 0xFEE0
		case r == 0xFE55 || r == 0xA789 || r == 0x2236 || r == 0xFE13:
			return ':'
		case r == 0x3000 || r == 0x00A0 || (r >= 0x2000 && r <= 0x200A) || r == 0x202F || r == 0x205F:
			return ' '
		}
		return r
	}, s)
}

// ImportedSuggestion is one tracked change of a Word file, as a suggestion.
type ImportedSuggestion struct {
	ProjectID  string
	DocumentID string
	Label      string // the Word author's name
	CreatedAt  string // db time format; empty is now
	Changes    []ChangeIn
}

// ErrNotPlaced says a change could not be placed in the text or would never be
// applicable; the message is the reason.
var ErrNotPlaced = errors.New("the tracked change could not be placed")

// ImportSuggestionTx writes a suggestion in the caller's transaction, against
// the document's text now (its current revision). It returns ErrNotPlaced,
// wrapped with the reason, for a change that cannot be anchored or could never
// be applied, and writes nothing then.
func ImportSuggestionTx(c *ops.Ctx, tx *sql.Tx, in ImportedSuggestion) error {
	h, err := loadDocHead(c, tx, in.ProjectID, in.DocumentID)
	if err != nil {
		return err
	}
	if h.Deleted {
		return ops.Conflict("This document is in the trash.")
	}
	chs, err := buildChanges(c, tx, in.ProjectID, in.DocumentID, h, in.Changes, false)
	if err != nil {
		var e *ops.Error
		if errors.As(err, &e) && (e.Code == ops.CodeInvalid || e.Code == ops.CodeConflict) {
			return errors.Join(ErrNotPlaced, errors.New(e.Message))
		}
		return err
	}
	raw, _ := json.Marshal(chs)
	now := db.Now()
	at := in.CreatedAt
	if at == "" {
		at = now
	}
	_, err = tx.ExecContext(c, `INSERT INTO suggestions(id, project_id, document_id, author_id, author_label, status, base_revision, ops_json, note, created_at, updated_at) VALUES (?,?,?,NULL,?, 'open', ?,?, '', ?,?)`,
		db.NewID("sug"), in.ProjectID, in.DocumentID, CleanLabel(in.Label), h.Revision, string(raw), at, now)
	return err
}

// ImportedComment is a comment of a Word file.
type ImportedComment struct {
	ProjectID  string
	DocumentID string
	Label      string
	Body       string
	CreatedAt  string
	// Quote, Before and After place the comment on the text it was made on; with
	// no Quote, or one that cannot be found exactly, the comment is on the
	// document as a whole.
	Quote, Before, After string
	// RequireAnchor: with a Quote that cannot be placed, write nothing and say
	// ErrNotPlaced (a caller with several candidate documents tries the next).
	RequireAnchor bool
}

// ImportCommentTx writes a comment in the caller's transaction. anchored says
// whether it was placed on text. A comment over the project's limit is refused
// with a conflict, as any other would be.
func ImportCommentTx(ctx context.Context, tx *sql.Tx, in ImportedComment) (anchored bool, err error) {
	body, e := CleanBody(in.Body, MaxBody)
	if e != nil {
		return false, e
	}
	var total int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM comments WHERE project_id = ? AND deleted_at IS NULL`, in.ProjectID).Scan(&total); err != nil {
		return false, err
	}
	if total >= MaxCommentsPerProject {
		return false, ops.Conflict("This project holds the most comments it can (" + itoa(MaxCommentsPerProject) + ").")
	}
	var content string
	var rev int
	var deleted sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT content, revision, deleted_at FROM documents WHERE id = ? AND project_id = ?`, in.DocumentID, in.ProjectID).Scan(&content, &rev, &deleted)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ops.NotFound("document")
	}
	if err != nil {
		return false, err
	}
	if deleted.Valid {
		return false, ops.Conflict("This document is in the trash.")
	}
	var anchor comanchor.Anchor
	state := comanchor.StateNone
	if in.Quote != "" {
		var open int
		if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM comments WHERE document_id = ? AND parent_id IS NULL AND anchor_state <> 'none' AND status = 'open' AND deleted_at IS NULL`, in.DocumentID).Scan(&open); err != nil {
			return false, err
		}
		if open < comanchor.MaxAnchoredPerDocument {
			b := comanchor.Body(content)
			if a, z, rerr := comanchor.Resolve(b, comanchor.Range{Quote: in.Quote, Before: in.Before, After: in.After}, rev, rev); rerr == nil {
				anchor = comanchor.New(b, a, z, rev)
				state = comanchor.StateActive
			}
		}
	}
	if in.RequireAnchor && in.Quote != "" && state != comanchor.StateActive {
		return false, ErrNotPlaced
	}
	now := db.Now()
	at := in.CreatedAt
	if at == "" {
		at = now
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO comments(id, project_id, document_id, parent_id, kind, author_id, share_id, guest_name, body, status,
		anchor_state, anchor_start, anchor_end, anchor_quote, anchor_original, anchor_prefix, anchor_suffix, anchor_hash, anchor_revision, created_at, updated_at, client_key)
		VALUES (?,?,?,NULL,'comment',NULL,NULL,?,?, 'open', ?,?,?,?,?,?,?,?,?,?,?,NULL)`,
		db.NewID("cmt"), in.ProjectID, in.DocumentID, CleanLabel(in.Label), body,
		state, anchor.Start, anchor.End, anchor.Quote, anchor.Original, anchor.Prefix, anchor.Suffix, anchor.Hash, anchor.Revision, at, now)
	return state == comanchor.StateActive, err
}

func text1(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = textpkg.StripInvisible(s)
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || r == 0x7f || r >= 0x80 && r < 0xa0 {
			r = ' '
		}
		b.WriteRune(r)
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// PlaceableAfterOthers says, for each of several suggestions made on body at
// one revision, whether it can be accepted whatever the others have already been
// and in whatever order. The first suggestion accepted is placed by its stored
// offsets; every later one is found again by the context stored with it, exactly
// once (suggestion.accept on a changed document). So a suggestion is sound only
// if its change is found at its own place, and nowhere else, by that rule in the
// original text and in every state reachable by accepting some of the others: all
// subsets when there are up to six suggestions, and for more the states in which
// the nearby ones, those that contain its words, all of the others and a few
// other subsets have been accepted. One that is not sound cannot be accepted one
// at a time in the review screens; the importer takes such a change as accepted.
func PlaceableAfterOthers(body string, sugs [][]ChangeIn) []bool {
	b := PlaceBudget
	return PlaceableAfterOthersWithin(body, sugs, &b)
}

// PlaceBudget is the work a document's checks may do, in kilobytes of text handled, over all the rounds of the
// importer (a caller that asks again after taking some suggestions as accepted passes the same counter).
const PlaceBudget = 400_000

// PlaceableAfterOthersWithin is PlaceableAfterOthers with the work counted against *budget: when it is spent,
// the suggestions not yet checked in full are reported as not placeable (the importer takes them as accepted).
func PlaceableAfterOthersWithin(body string, sugs [][]ChangeIn, budget *int) []bool {
	ix := alts.NewIndex(body)
	n := len(sugs)
	ok := make([]bool, n)
	sts := make([][]stored, n)
	pos := make([][]int, n) // byte offset of each change in the original body
	edits := make([][]impEdit, n)
	for i, chs := range sugs {
		ok[i] = true
		for _, ch := range chs {
			a, b, kind, err := resolveChange(body, ix, ch, false)
			if err != nil {
				ok[i] = false
				break
			}
			s := stored{Kind: kind, U1: ix.Unit(a), U2: ix.Unit(b), Original: body[a:b], Text: ch.Text}
			if kind == "insert" {
				s.Before, s.After = alts.Contexts(body, a, a)
			} else {
				s.Before, s.After = alts.ParagraphContext(body, a, b)
			}
			sts[i] = append(sts[i], s)
			pos[i] = append(pos[i], a)
			edits[i] = append(edits[i], impEdit{a, b, ch.Text, body[a:b]})
		}
	}
	// Work is counted in kilobytes of text handled (each state builds the edited text and searches it), and bounded
	// for the whole call: a suggestion not checked in full when the budget runs out is not vouched for.
	cost := len(body)/1024 + 1
	// found: is every change of suggestion g located, at its own place, in the body that has the edits of the
	// suggestions in applied made?
	found := func(g int, applied []int) bool {
		*budget -= cost
		var es []impEdit
		for _, h := range applied {
			es = append(es, edits[h]...)
		}
		sort.SliceStable(es, func(i, j int) bool { return es[i].a < es[j].a })
		cur, cix := body, ix
		if len(es) > 0 {
			var sb strings.Builder
			sb.Grow(len(body))
			last := 0
			for _, e := range es {
				if e.a < last { // two edits over each other: not a state the review could reach
					return false
				}
				sb.WriteString(body[last:e.a])
				sb.WriteString(e.text)
				last = e.b
			}
			sb.WriteString(body[last:])
			cur = sb.String()
			cix = alts.NewIndex(cur)
		}
		for k, st := range sts[g] {
			// where its place has moved to: by what the edits before it added or took away
			shift := 0
			for _, e := range es {
				if e.b <= pos[g][k] && !(e.a == e.b && e.a == pos[g][k] && st.Kind == "insert") {
					shift += len(e.text) - (e.b - e.a)
				}
			}
			want := pos[g][k] + shift
			if st.Kind == "insert" {
				// the insertion point is found by the words around it: it must be where it should be
				if at, f := locateInsert(cur, st.Before, st.After); !f || at != want {
					return false
				}
			} else if sp, f := alts.LocateIn(cur, cix, st.Original, st.Before, st.After, nil, false, false); !f || sp.A != want {
				return false
			}
		}
		return true
	}
	for g := 0; g < n; g++ {
		if ok[g] && (*budget < 0 || !found(g, nil)) { // in the original text itself
			ok[g] = false
		}
	}
	// What can make a suggestion's place ambiguous or move it is another suggestion near it, or one whose edit
	// leaves, in its own surroundings, the very text the suggestion is found by (its quote with the context stored
	// with it): an edit anywhere else changes neither what is found nor where (but for a shift, which found
	// accounts for), so only those are tried in and out of each state.
	const margin = 1500
	edited := make([]string, n) // each suggestion's edit made, with the text around it
	for h := 0; h < n; h++ {
		if len(edits[h]) == 0 {
			continue
		}
		lo, hi := len(body), 0
		for _, e := range edits[h] {
			lo, hi = min(lo, e.a), max(hi, e.b)
		}
		lo, hi = max(0, lo-margin), min(len(body), hi+margin)
		var sb strings.Builder
		last, good := lo, true
		for _, e := range edits[h] { // in the order of the file
			if e.a < last || e.b > hi {
				good = false
				break
			}
			sb.WriteString(body[last:e.a])
			sb.WriteString(e.text)
			last = e.b
		}
		if good {
			sb.WriteString(body[last:hi])
			edited[h] = sb.String()
		} else {
			edited[h] = body[lo:hi] + " " + joinTexts(edits[h])
		}
	}
	for g := 0; g < n; g++ {
		if !ok[g] {
			continue
		}
		var needles []string
		for _, st := range sts[g] {
			needles = append(needles, st.Before+st.Original+st.After)
		}
		var rel []int
		for h := 0; h < n; h++ {
			if h == g || !ok[h] {
				continue
			}
			rel1 := near1(pos[g], pos[h], 400+maxOrig(sts[g]))
			for _, nd := range needles {
				if rel1 || nd == "" {
					break
				}
				rel1 = strings.Contains(edited[h], nd)
			}
			if rel1 {
				rel = append(rel, h)
			}
		}
		for _, s := range subsetsToTry(g, rel) {
			if *budget < 0 || !found(g, s) {
				ok[g] = false
				break
			}
		}
	}
	return ok
}

// subsetsToTry lists the states to check for suggestion g, given the suggestions that can matter to it: every
// non-empty subset when there are at most five; otherwise each alone, all of them and three pseudo-random subsets
// (sampled, not exhaustive).
func subsetsToTry(g int, rel []int) [][]int {
	if len(rel) <= 5 {
		var out [][]int
		for mask := 1; mask < 1<<len(rel); mask++ {
			var s []int
			for i, h := range rel {
				if mask>>i&1 == 1 {
					s = append(s, h)
				}
			}
			out = append(out, s)
		}
		return out
	}
	var states [][]int
	for _, h := range rel {
		states = append(states, []int{h})
	}
	states = append(states, rel)
	seed := uint32(g)*2654435761 + 1
	for k := 0; k < 3; k++ {
		var s []int
		for _, h := range rel {
			seed = seed*1664525 + 1013904223
			if seed>>16&1 == 1 {
				s = append(s, h)
			}
		}
		states = append(states, s)
	}
	return states
}

func near1(a, b []int, d int) bool {
	for _, x := range a {
		for _, y := range b {
			if x-y <= d && y-x <= d {
				return true
			}
		}
	}
	return false
}

// impEdit is one change as a byte range of the original text.
type impEdit struct {
	a, b int
	text string
	orig string
}

func maxOrig(sts []stored) int {
	m := 0
	for _, s := range sts {
		m = max(m, len(s.Original))
	}
	return m
}

func joinTexts(es []impEdit) string {
	var sb strings.Builder
	for _, e := range es {
		sb.WriteString(e.text)
	}
	return sb.String()
}
