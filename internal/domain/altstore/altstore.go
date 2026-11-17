// Package altstore keeps the tables of alternatives and ghosts in step with a
// document's text. It is a leaf package: the document write path (docs) calls
// Reconcile from inside its transaction for every write that carries or may
// disturb alternatives, and the alternatives ops (package alternatives) use the
// same functions, so there is one definition of the invariant:
//
//	after every committed write to a document, alts.Serialize(Load(document))
//	is byte-equal to the comment line that ends documents.content, the body
//	holds the chosen wording of every active alternative at its anchor, and a
//	document with no alternatives has no such line.
//
// See docs/OPEN-QUESTIONS.md "Phase 3.5 alternatives decisions".
package altstore

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// ServiceLimits is the name the limits are registered under on the ops
// environment (ops.Env.SetService), so the write path reads the configured
// ones without a package-level variable.
const ServiceLimits = "alts.limits"

// LimitsOf returns the limits configured for the environment, or the defaults.
func LimitsOf(env *ops.Env) alts.Limits {
	if l, ok := ops.Service[alts.Limits](env, ServiceLimits); ok {
		return l
	}
	return alts.DefaultLimits
}

type querier interface {
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// Exists reports whether a document has any alternative.
func Exists(q querier, docID string) (bool, error) {
	var one int
	err := q.QueryRow(`SELECT 1 FROM alternatives WHERE document_id = ? LIMIT 1`, docID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Load reads a document's alternatives, in document order (orphans last).
func Load(q querier, docID string) ([]alts.Item, error) {
	rows, err := q.Query(`SELECT id, scope, status, anchor_start, anchor_end, anchor_prefix, anchor_suffix, COALESCE(chosen_variant_id,'')
		FROM alternatives WHERE document_id = ?`, docID)
	if err != nil {
		return nil, err
	}
	var items []alts.Item
	byID := map[string]int{}
	for rows.Next() {
		var it alts.Item
		var status string
		if err := rows.Scan(&it.ID, &it.Scope, &status, &it.Start, &it.End, &it.Before, &it.After, &it.Chosen); err != nil {
			rows.Close()
			return nil, err
		}
		it.Orphan = status == alts.StatusOrphaned
		byID[it.ID] = len(items)
		items = append(items, it)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, nil
	}
	vr, err := q.Query(`SELECT v.alternative_id, v.id, v.text, v.source, v.is_original, COALESCE(v.author_id,''), v.created_at
		FROM alternative_variants v JOIN alternatives a ON a.id = v.alternative_id
		WHERE a.document_id = ? ORDER BY v.alternative_id, v.position, v.id`, docID)
	if err != nil {
		return nil, err
	}
	defer vr.Close()
	for vr.Next() {
		var aid string
		var v alts.Variant
		var orig int
		if err := vr.Scan(&aid, &v.ID, &v.Text, &v.Source, &orig, &v.By, &v.At); err != nil {
			return nil, err
		}
		v.Original = orig == 1
		if i, ok := byID[aid]; ok {
			items[i].Variants = append(items[i].Variants, v)
		}
	}
	if err := vr.Err(); err != nil {
		return nil, err
	}
	alts.SortItems(items)
	return items, nil
}

// Mode is how a write relates to the stored alternatives.
type Mode int

const (
	// ModeUpdate is a save: the stored alternatives are kept and re-anchored. A
	// text without a comment keeps them; a comment that differs is applied.
	ModeUpdate Mode = iota
	// ModeReplace makes the alternatives what the text's comment says (a revision
	// restore, an import, a new document): no comment means none.
	ModeReplace
)

// In describes one write.
type In struct {
	DocID, ProjectID string
	OldContent       string // the stored text before this write ("" for a new document)
	NewContent       string // the text being written
	Mode             Mode
	// Strict refuses a trailing comment that is not valid (doc.update). Otherwise
	// the line is left as ordinary text.
	Strict bool
	// FreshIDs gives every item and variant read from the comment a new id: a
	// new document must not reuse the ids of the one the Markdown came from.
	FreshIDs bool
	// Edits, when the caller knows exactly what changed in the body, replaces
	// the guess the re-anchoring makes from the two texts.
	Edits  []alts.Edit
	Actor  string
	Now    string
	Limits alts.Limits
}

// Plan is what a write will do to a document's alternatives, worked out without
// writing anything: the text to store and the items it will hold.
type Plan struct {
	// Content is the text to store: the body and the canonical comment.
	Content string
	Items   []alts.Item
	Report  alts.Report
	// Destroyed is true when the write removes or rewords variant text that the
	// previous content held: the caller must keep that content in a revision.
	Destroyed bool
	// Stale is true when the text carried an older copy of the alternatives'
	// comment (one the server wrote, unedited, that no longer says what the
	// tables say): it was not applied, and the tables stand.
	Stale bool
	// Problem is set when the text ends with an alternatives comment that could
	// not be used (not valid, or refused by the checks). It is stored as an
	// ordinary comment, and this says why.
	Problem string
	old     []alts.Item
}

// Empty reports whether the plan leaves the text alone (no alternatives before,
// none after, no comment).
func (p Plan) Empty() bool { return len(p.Items) == 0 && len(p.old) == 0 }

// Reconcile brings the tables and the comment in step with a write, inside the
// caller's transaction, and returns the plan it carried out; Content is what
// must be stored in documents.content. A document with no alternatives and no
// comment in its text costs one indexed lookup. The document row must exist.
func Reconcile(tx *sql.Tx, in In) (Plan, error) {
	p, err := PlanSave(tx, in)
	if err != nil {
		return Plan{}, err
	}
	if err := p.Apply(tx, in.DocID, in.ProjectID, in.Actor, in.Now); err != nil {
		return Plan{}, err
	}
	return p, nil
}

// PlanSave works out a write against what is stored, reading the tables but
// changing nothing.
func PlanSave(q querier, in In) (Plan, error) {
	var stored []alts.Item
	if in.DocID != "" && (in.OldContent != "" || in.Mode == ModeReplace) {
		var err error
		if stored, err = Load(q, in.DocID); err != nil {
			return Plan{}, err
		}
	}
	return plan(in, stored, strings.Contains(in.NewContent, alts.Marker))
}

// PlanNew works out the alternatives of a document that does not exist yet
// (a created or imported one) from the comment in its text. It touches no
// database, so a bulk writer can do it before it opens its transaction.
func PlanNew(content, actor, now string, lim alts.Limits) (Plan, error) {
	in := In{NewContent: content, Mode: ModeReplace, FreshIDs: true, Actor: actor, Now: now, Limits: lim}
	return plan(in, nil, strings.Contains(content, alts.Marker))
}

// CheckComment says why the alternatives comment at the end of a text cannot be
// used, or "" when there is none or it can. The importer reports it in its
// preview.
func CheckComment(content string, lim alts.Limits) string {
	if !strings.Contains(content, alts.Marker) {
		return ""
	}
	p, _ := PlanNew(content, "", "", lim)
	return p.Problem
}

func plan(in In, stored []alts.Item, hasMarker bool) (Plan, error) {
	if len(stored) == 0 && !hasMarker {
		return Plan{Content: in.NewContent}, nil
	}
	lim := in.Limits
	content := alts.NormalizeEOL(in.NewContent)
	oldParts := alts.Split(alts.NormalizeEOL(in.OldContent))
	oldBody := oldParts.Body
	parts := alts.Split(content)
	line := parts.Line // the comment the text carries, when it carries one
	collapsed := false
	if strings.Count(content, alts.Marker) > 1 || (!parts.HasLine && strings.Contains(content, alts.Marker)) {
		// Text after the comment, or two comments: one canonical comment at the end.
		if body, l, ok := alts.Collapse(content, lim); ok {
			parts, line, collapsed = alts.Parts{Body: body}, l, true
		}
	}
	hasLine := line != ""
	mo := alts.MergeOptions{Actor: in.Actor, Now: in.Now, NewID: db.NewID, Limits: lim}
	ro := alts.Options{Actor: in.Actor, Now: in.Now, NewID: db.NewID, Limits: lim}

	var (
		cur      []alts.Item
		problem  string
		stale    bool
		unusable bool // the line is there but is not ours: an ordinary comment
	)
	reject := func(err error) error { // what to do with a comment that cannot be used
		// A line already in the stored text that could not be used then either (a
		// document made before, or imported, with a comment that did not pass) is
		// an ordinary comment, and saving the text again must go on working.
		same := oldParts.HasLine && oldParts.Line == line && !collapsed
		if in.Strict && !(same && len(stored) == 0) {
			return ops.Invalid("content", err.Error()+" Fix or delete the last line of the text (the alternatives comment).")
		}
		problem, unusable = err.Error(), true
		return nil
	}
	switch in.Mode {
	case ModeUpdate:
		cur = stored
		if hasLine {
			incoming, sig, perr := alts.ParseSig(line, lim)
			if perr == nil && !alts.Same(stored, incoming) {
				if sig != "" && sig == alts.Sig(incoming) && len(stored) > 0 {
					// The server's own comment, unedited, and not what the tables say now:
					// an older copy (a cached client, a "keep mine" resend). It does not
					// know what has been added or changed since, so it is not applied.
					stale = true
				} else {
					// Removals are honoured only from a comment that shows it saw what is
					// stored (its sig is the stored items'). A hand edit of an older copy,
					// or a hand-written line, can add and change but removes nothing.
					m := mo
					if len(stored) > 0 && (sig == "" || sig != alts.Sig(stored)) {
						m.KeepMissing, stale = true, true
					}
					if merged, merr := alts.Merge(stored, incoming, m); merr != nil {
						perr = merr
					} else {
						cur = merged
					}
				}
			}
			if perr != nil {
				if err := reject(perr); err != nil {
					return Plan{}, err
				}
			}
		}
	default: // ModeReplace
		oldBody = ""
		if hasLine {
			incoming, perr := alts.Parse(line, lim)
			if perr == nil {
				perr = alts.Freshen(incoming, mo, in.FreshIDs)
			}
			switch {
			case perr != nil && in.Strict:
				return Plan{}, ops.Invalid("content", perr.Error())
			case perr != nil:
				problem, unusable = perr.Error(), true
			default:
				cur = incoming
			}
		}
	}
	if unusable || (!hasLine && len(stored) == 0) {
		// Nothing of ours in the text. With no tables behind it, it is stored as it
		// came (not even its line endings change).
		if len(stored) == 0 {
			return Plan{Content: in.NewContent, Problem: problem}, nil
		}
		parts = alts.Parts{Body: content}
	}

	var final []alts.Item
	var rep alts.Report
	if len(cur) > 0 {
		var edits []alts.Edit
		if in.Mode == ModeUpdate {
			edits = in.Edits
		}
		final, rep = alts.Rebase(oldBody, parts.Body, cur, edits, ro)
	}
	p := Plan{Content: parts.Compose(alts.Serialize(final)), Items: final, Report: rep, old: stored, Stale: stale, Problem: problem}
	adopted := map[string]bool{}
	for _, id := range rep.AdoptedID {
		adopted[id] = true
	}
	oldText := map[string]string{}
	owner := map[string]string{}
	for _, it := range stored {
		for _, v := range it.Variants {
			oldText[v.ID], owner[v.ID] = v.Text, it.ID
		}
	}
	newText := map[string]string{}
	for _, it := range final {
		for _, v := range it.Variants {
			newText[v.ID] = v.Text
		}
	}
	// Variant text is destroyed when a wording is removed, or reworded through the
	// comment; a wording the writer retyped in the text itself (adopted) is the
	// ordinary history of the text, kept by the usual autosave rule.
	for id, t := range oldText {
		nt, ok := newText[id]
		if !ok || (nt != t && !adopted[owner[id]]) {
			p.Destroyed = true
			break
		}
	}
	return p, nil
}

// Apply writes the plan's items to the tables.
func (p Plan) Apply(tx *sql.Tx, docID, projectID, actor, now string) error {
	_, _, err := write(tx, docID, projectID, p.old, p.Items, actor, now)
	return err
}

// Write stores a set of items for a document (the ops build the new set and
// call this; Reconcile calls it for saves). It returns whether anything changed
// and whether variant text was lost.
func Write(tx *sql.Tx, docID, projectID string, old, final []alts.Item, actor, now string) (changed, destroyed bool, err error) {
	return write(tx, docID, projectID, old, final, actor, now)
}

func hashText(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

func write(tx *sql.Tx, docID, projectID string, old, final []alts.Item, actor, now string) (changed, destroyed bool, err error) {
	oldBy := make(map[string]alts.Item, len(old))
	for _, it := range old {
		oldBy[it.ID] = it
	}
	newBy := make(map[string]bool, len(final))
	for _, it := range final {
		newBy[it.ID] = true
	}
	for _, it := range old {
		if newBy[it.ID] {
			continue
		}
		if _, err = tx.Exec(`DELETE FROM alternatives WHERE id = ?`, it.ID); err != nil {
			return
		}
		changed, destroyed = true, true
	}
	var upd, insV *sql.Stmt
	defer func() {
		if upd != nil {
			upd.Close()
		}
		if insV != nil {
			insV.Close()
		}
	}()
	for _, it := range final {
		status := alts.StatusActive
		if it.Orphan {
			status = alts.StatusOrphaned
		}
		prev, existed := oldBy[it.ID]
		if !existed {
			changed = true
			if _, err = tx.Exec(`INSERT INTO alternatives(id, document_id, project_id, scope, status, anchor_start, anchor_end, anchor_prefix, anchor_suffix,
				anchor_hash, chosen_variant_id, created_by, created_at, updated_at)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,(SELECT id FROM users WHERE id = ?),?,?)`,
				it.ID, docID, projectID, it.Scope, status, max(it.Start, 0), max(it.End, 0), it.Before, it.After, hashText(it.ChosenText()), it.Chosen,
				actor, now, now); err != nil {
				return
			}
			if err = writeVariants(tx, it, nil, actor, now); err != nil {
				return
			}
			continue
		}
		if !sameVariants(prev.Variants, it.Variants) {
			changed = true
			if lostText(prev.Variants, it.Variants) {
				destroyed = true
			}
			if err = writeVariants(tx, it, prev.Variants, actor, now); err != nil {
				return
			}
		}
		if prev.Start == it.Start && prev.End == it.End && prev.Before == it.Before && prev.After == it.After &&
			prev.Chosen == it.Chosen && prev.Orphan == it.Orphan && sameVariants(prev.Variants, it.Variants) {
			continue
		}
		changed = true
		if upd == nil {
			if upd, err = tx.Prepare(`UPDATE alternatives SET status = ?, anchor_start = ?, anchor_end = ?, anchor_prefix = ?, anchor_suffix = ?,
				anchor_hash = ?, chosen_variant_id = ?, updated_at = ? WHERE id = ?`); err != nil {
				return
			}
		}
		hash := ""
		if prev.ChosenText() != it.ChosenText() || prev.Chosen != it.Chosen {
			hash = hashText(it.ChosenText())
		} else {
			if err = tx.QueryRow(`SELECT anchor_hash FROM alternatives WHERE id = ?`, it.ID).Scan(&hash); err != nil {
				return
			}
		}
		if _, err = upd.Exec(status, max(it.Start, 0), max(it.End, 0), it.Before, it.After, hash, it.Chosen, now, it.ID); err != nil {
			return
		}
	}
	return
}

func sameVariants(a, b []alts.Variant) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].Text != b[i].Text {
			return false
		}
	}
	return true
}

// lostText reports whether a variant that was in prev is gone or says something
// else in next.
func lostText(prev, next []alts.Variant) bool {
	now := make(map[string]string, len(next))
	for _, v := range next {
		now[v.ID] = v.Text
	}
	for _, v := range prev {
		if t, ok := now[v.ID]; !ok || t != v.Text {
			return true
		}
	}
	return false
}

func writeVariants(tx *sql.Tx, it alts.Item, prev []alts.Variant, actor, now string) error {
	keep := make(map[string]bool, len(it.Variants))
	for _, v := range it.Variants {
		keep[v.ID] = true
	}
	for _, v := range prev {
		if !keep[v.ID] {
			if _, err := tx.Exec(`DELETE FROM alternative_variants WHERE id = ?`, v.ID); err != nil {
				return err
			}
		}
	}
	for i, v := range it.Variants {
		orig := 0
		if v.Original {
			orig = 1
		}
		by := v.By
		if by == "" {
			by = actor
		}
		at := v.At
		if at == "" {
			at = now
		}
		src := v.Source
		if src == "" {
			src = alts.SourceManual
		}
		// The original's text is never part of the update: a trigger would refuse it.
		if _, err := tx.Exec(`INSERT INTO alternative_variants(id, alternative_id, text, source, is_original, position, author_id, created_at)
			VALUES (?,?,?,?,?,?,(SELECT id FROM users WHERE id = ?),?)
			ON CONFLICT(id) DO UPDATE SET position = excluded.position,
			  text = CASE WHEN alternative_variants.is_original = 1 THEN alternative_variants.text ELSE excluded.text END`,
			v.ID, it.ID, v.Text, src, orig, float64(i+1)*db.PositionGap, by, at); err != nil {
			return fmt.Errorf("altstore: variant %s: %w", v.ID, err)
		}
	}
	return nil
}
