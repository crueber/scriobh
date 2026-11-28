package aiwork

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/ai/analyse"
	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// The Lab marks; it never rewrites. A mark is a range of a document with a
// reason; a mark with a fix (a punctuation fix, a trim) carries the exact edit,
// which happens only when the writer accepts that mark, alone or in a chosen
// set. Marks come from two places and are merged here:
//
//   - local analysers (internal/ai/analyse): long sentences, hedge words and
//     punctuation. No model, no policy, no cost; they work with AI off.
//   - the model recipes lab.convoluted, lab.tone and lab.trim, run through
//     ai.run like any recipe, whose marks the result hook keeps.
//
// Marks are private to the person who made them. Every time they are read each
// mark's quote is looked up again in the text as it is now; one that cannot be
// found exactly once is stale, and a stale mark can only be dismissed.

const maxMarksPerDoc = 3000

// MarkOut is one mark.
type MarkOut struct {
	ID      string `json:"id"`
	RunID   string `json:"run_id" desc:"The model run, or lab_… for a batch of the local analysers."`
	Source  string `json:"source" enum:"model,local"`
	Kind    string `json:"kind" enum:"convoluted_sentence,tone_mismatch,punctuation_fix,long_sentence,hedge_words,trim_filler,trim_adverbs,trim_repetition,trim_dialogue_tags"`
	Reason  string `json:"reason"`
	Quote   string `json:"quote" desc:"The marked text, as the manuscript has it."`
	Action  string `json:"action" enum:"none,delete,replace" desc:"none: an observation, nothing to apply; accepting it only marks it resolved. delete: the words are deleted. replace: they are replaced by fix."`
	Fix     string `json:"fix,omitempty" desc:"With action replace: the exact new text."`
	Anchor  Anchor `json:"anchor"`
	Status  string `json:"status" enum:"open,accepted,dismissed"`
	AtRev   int    `json:"at_revision"`
	Created string `json:"created_at"`
}

// MarksOut is a document's marks.
type MarksOut struct {
	DocumentID string         `json:"document_id"`
	Revision   int            `json:"revision" desc:"The document's current revision; anchors are as of it."`
	Marks      []MarkOut      `json:"marks"`
	Counts     map[string]int `json:"counts" desc:"Open marks by kind."`
	Stale      int            `json:"stale" desc:"Open marks whose quote cannot be found exactly once any more."`
	Truncated  bool           `json:"truncated,omitempty"`
}

type markRow struct {
	MarkOut
	before, after string
	u1, u2        int
	hasFix        bool
}

func action(hasFix bool, fix string) string {
	switch {
	case !hasFix:
		return "none"
	case fix == "":
		return "delete"
	}
	return "replace"
}

func loadMarks(ctx context.Context, q dbq, projectID, userID, docID, status, kind string, ids []string, limit int) ([]markRow, error) {
	qs := `SELECT id, run_id, source, kind, reason, quote, before_ctx, after_ctx, start_u16, end_u16, at_revision, has_fix, fix, status, created_at
		FROM ai_marks WHERE project_id = ? AND user_id = ? AND document_id = ?`
	args := []any{projectID, userID, docID}
	if status != "" && status != "all" {
		qs += ` AND status = ?`
		args = append(args, status)
	}
	if kind != "" {
		qs += ` AND kind = ?`
		args = append(args, kind)
	}
	if len(ids) > 0 {
		qs += ` AND id IN (?` + strings.Repeat(",?", len(ids)-1) + `)`
		for _, id := range ids {
			args = append(args, id)
		}
	}
	qs += ` ORDER BY start_u16, end_u16, id LIMIT ?`
	args = append(args, limit)
	rows, err := q.QueryContext(ctx, qs, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []markRow
	for rows.Next() {
		var m markRow
		var hf int
		var fix string
		if err := rows.Scan(&m.ID, &m.RunID, &m.Source, &m.Kind, &m.Reason, &m.Quote, &m.before, &m.after, &m.u1, &m.u2, &m.AtRev, &hf, &fix, &m.Status, &m.Created); err != nil {
			return nil, err
		}
		m.hasFix = hf == 1
		m.Action, m.Fix = action(m.hasFix, fix), fix
		out = append(out, m)
	}
	return out, rows.Err()
}

// anchorMarks looks every mark up in the document as it is now.
func anchorMarks(d docState, marks []markRow) ([]markRow, map[string]span) {
	ix := alts.NewIndex(d.Body)
	spans := map[string]span{}
	for i := range marks {
		m := &marks[i]
		sp, ok := locateExact(d.Body, ix, m.Quote, m.before, m.after, &span{U1: m.u1, U2: m.u2}, m.AtRev == d.Revision)
		if ok {
			m.Anchor = Anchor{State: "placed", Start: sp.U1, End: sp.U2}
			spans[m.ID] = sp
		} else {
			m.Anchor = Anchor{State: "stale"}
		}
	}
	return marks, spans
}

func marksOut(d docState, marks []markRow, limit int) MarksOut {
	marks, _ = anchorMarks(d, marks)
	out := MarksOut{DocumentID: d.ID, Revision: d.Revision, Marks: []MarkOut{}, Counts: map[string]int{}, Truncated: len(marks) >= limit}
	for _, m := range marks {
		out.Marks = append(out.Marks, m.MarkOut)
		if m.Status == "open" {
			out.Counts[m.Kind]++
			if m.Anchor.State == "stale" {
				out.Stale++
			}
		}
	}
	return out
}

// ---- making marks ----------------------------------------------------------------------------

// settled is the set of marks the person has already dealt with on a document,
// by kind and quote and context: a dismissed mark stays dismissed when the
// analysers or the model find the same thing again, and so does an observation
// that was resolved.
type settled map[string]bool

func markKey(kind, quote, before, after string) string {
	return kind + "\x00" + quote + "\x00" + before + "\x00" + after
}

func loadSettled(ctx context.Context, q dbq, userID, docID string) (settled, error) {
	rows, err := q.QueryContext(ctx, `SELECT kind, quote, before_ctx, after_ctx FROM ai_marks WHERE document_id = ? AND user_id = ? AND (status = 'dismissed' OR (status = 'accepted' AND has_fix = 0))`, docID, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := settled{}
	for rows.Next() {
		var k, qt, b, a string
		if err := rows.Scan(&k, &qt, &b, &a); err != nil {
			return nil, err
		}
		out[markKey(k, qt, b, a)] = true
	}
	return out, rows.Err()
}

// insertMark stores a mark unless the person already settled the same one.
// quote must be body[a:b]. It reports whether a row was written.
func insertMark(ctx context.Context, t dbq, skip settled, projectID, userID, docID, runID, source, kind, reason string, body string, ix *alts.Index, a, b, rev int, hasFix bool, fix string) (bool, error) {
	before, after := paragraphContext(body, a, b)
	if skip[markKey(kind, body[a:b], before, after)] {
		return false, nil
	}
	_, err := t.ExecContext(ctx, `INSERT INTO ai_marks(id, project_id, user_id, document_id, run_id, source, kind, reason, quote, before_ctx, after_ctx, start_u16, end_u16, at_revision, has_fix, fix, status, created_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,'open',?)`,
		db.NewID("mrk"), projectID, userID, docID, runID, source, kind, reason, body[a:b], before, after, ix.Unit(a), ix.Unit(b), rev, b2i(hasFix), fix, now())
	return err == nil, err
}

const (
	maxSettledMarks = 1000 // settled marks kept per person and document, the newest
	settledMarkDays = 90   // and none older than this
)

// openMarks counts the person's open marks on a document: the cap is on those,
// not on what has been settled.
func openMarks(ctx context.Context, q dbq, docID, userID string) int {
	var n int
	_ = q.QueryRowContext(ctx, `SELECT count(*) FROM ai_marks WHERE document_id = ? AND user_id = ? AND status = 'open'`, docID, userID).Scan(&n)
	return n
}

// pruneSettled removes settled marks that are old or beyond the newest
// maxSettledMarks of a document.
func pruneSettled(ctx context.Context, q dbq, docID, userID string) {
	old := db.Format(db.NowTime().Add(-settledMarkDays * 24 * time.Hour))
	_, _ = q.ExecContext(ctx, `DELETE FROM ai_marks WHERE document_id = ? AND user_id = ? AND status <> 'open' AND resolved_at <> '' AND resolved_at < ?`, docID, userID, old)
	_, _ = q.ExecContext(ctx, `DELETE FROM ai_marks WHERE document_id = ? AND user_id = ? AND status <> 'open' AND id NOT IN
		(SELECT id FROM ai_marks WHERE document_id = ? AND user_id = ? AND status <> 'open' ORDER BY resolved_at DESC, id DESC LIMIT ?)`, docID, userID, docID, userID, maxSettledMarks)
}

// supersede removes the person's open marks of these kinds and source for a
// document: a new run replaces what the last one found rather than piling up.
// Marks already accepted or dismissed are kept.
func supersede(ctx context.Context, t dbq, projectID, userID, docID, source string, kinds []string) {
	if len(kinds) == 0 {
		return
	}
	q := `DELETE FROM ai_marks WHERE project_id = ? AND user_id = ? AND document_id = ? AND source = ? AND status = 'open' AND kind IN (?` + strings.Repeat(",?", len(kinds)-1) + `)`
	args := []any{projectID, userID, docID, source}
	for _, k := range kinds {
		args = append(args, k)
	}
	_, _ = t.ExecContext(ctx, q, args...)
}

func (s *Service) labResult(ctx context.Context, d *db.DB, info aicore.ResultInfo) (aicore.ResultReply, error) {
	var md lib.MarksData
	if len(info.Out.Data) == 0 || json.Unmarshal(info.Out.Data, &md) != nil || info.DocumentID == "" {
		return none(), nil
	}
	ix := alts.NewIndex(info.Body)
	var out MarksOut
	unplaced := 0
	err := tx(ctx, d, func(t *sql.Tx) error {
		kinds := map[string]bool{}
		for _, m := range md.Marks {
			kinds[m.Kind] = true
		}
		var ks []string
		for k := range kinds {
			ks = append(ks, k)
		}
		sort.Strings(ks)
		supersede(ctx, t, info.ProjectID, info.UserID, info.DocumentID, "model", ks)
		cnt := openMarks(ctx, t, info.DocumentID, info.UserID)
		skip, err := loadSettled(ctx, t, info.UserID, info.DocumentID)
		if err != nil {
			return err
		}
		hidden := text.HiddenSpans(info.Body)
		for _, m := range md.Marks {
			sp, ok := locate(info.Body, ix, m.Quote, m.Before, m.After, nil, false)
			if !ok || text.TouchesSpans(hidden, sp.A, sp.B) || cnt >= maxMarksPerDoc {
				unplaced++
				continue
			}
			trim := strings.HasPrefix(m.Kind, "trim_")
			wrote, err := insertMark(ctx, t, skip, info.ProjectID, info.UserID, info.DocumentID, info.RunID, "model", m.Kind, m.Reason, info.Body, ix, sp.A, sp.B, info.DocRevision, trim, "")
			if err != nil {
				return err
			}
			if wrote {
				cnt++
			}
		}
		pruneSettled(ctx, t, info.DocumentID, info.UserID)
		doc, err := loadDoc(ctx, t, info.ProjectID, info.DocumentID)
		if err != nil {
			return err
		}
		rows, err := loadMarks(ctx, t, info.ProjectID, info.UserID, info.DocumentID, "open", "", nil, 1000)
		if err != nil {
			return err
		}
		var mine []markRow
		for _, r := range rows {
			if r.RunID == info.RunID {
				mine = append(mine, r)
			}
		}
		out = marksOut(doc, mine, 1000)
		return nil
	})
	if err != nil {
		return aicore.ResultReply{}, err
	}
	var warn []string
	if unplaced > 0 {
		warn = append(warn, fmt.Sprintf("%d mark(s) were left out because their quote could not be found exactly once in the text.", unplaced))
	}
	return reply(struct {
		Kind     string `json:"kind"`
		RunID    string `json:"run_id"`
		Unplaced int    `json:"unplaced"`
		MarksOut
	}{"marks", info.RunID, unplaced, out}, warn...)
}

// ---- local analysis ------------------------------------------------------------------------------

// LabAnalyseIn runs the local analysers.
type LabAnalyseIn struct {
	ProjectID  string   `path:"id" json:"-" desc:"Project id."`
	DocumentID string   `json:"document_id"`
	Kinds      []string `json:"kinds,omitempty" desc:"long_sentence, hedge_words, punctuation_fix. Empty: all three."`
	LongWords  int      `json:"long_words,omitempty" desc:"A sentence over this many words is marked long. Default 40; 15 to 200."`
}

// ProjectRef lets the registry authorize by membership.
func (in LabAnalyseIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shape.
func (in *LabAnalyseIn) Validate() error {
	if in.DocumentID == "" || !db.ValidID(in.DocumentID) {
		return ops.Invalid("document_id", "Name the document.")
	}
	for _, k := range in.Kinds {
		if k != analyse.KindLong && k != analyse.KindHedge && k != analyse.KindPunct {
			return ops.Invalid("kinds", "The local analysers find long_sentence, hedge_words and punctuation_fix.")
		}
	}
	if in.LongWords != 0 && (in.LongWords < 15 || in.LongWords > 200) {
		return ops.Invalid("long_words", "A long sentence is 15 to 200 words.")
	}
	return nil
}

// labAnalyse reads the document and runs the analysers outside any transaction
// (they are the slow part: up to a second on a very large text), then takes the
// writer for the few statements that replace the marks, after checking that the
// document is still the one that was read. If it changed meanwhile it reads
// again, a few times, and then says the document is changing.
func (s *Service) labAnalyse(c *ops.Ctx, in LabAnalyseIn) (MarksOut, error) {
	kinds := in.Kinds
	if len(kinds) == 0 {
		kinds = []string{analyse.KindLong, analyse.KindHedge, analyse.KindPunct}
	}
	for attempt := 0; attempt < 3; attempt++ {
		d, err := loadDoc(c, c.DB().Read(), in.ProjectID, in.DocumentID)
		if err != nil {
			return MarksOut{}, mapErr(err)
		}
		if d.Trashed {
			return MarksOut{}, ops.Conflict("This document is in the trash. Restore it first.")
		}
		var lang string
		_ = c.DB().Read().QueryRowContext(c, `SELECT language FROM projects WHERE id = ?`, in.ProjectID).Scan(&lang)
		marks, ok := analyse.Run(d.Body, analyse.Options{Kinds: kinds, Language: lang, LongWords: in.LongWords})
		if !ok {
			return MarksOut{}, ops.Invalid("document_id", "This text is too irregular to analyse.")
		}
		var out MarksOut
		changed := false
		err = c.DB().WithTx(c, func(t *sql.Tx) error {
			var rev int
			var hash string
			if err := t.QueryRowContext(c, `SELECT revision, content_hash FROM documents WHERE id = ?`, d.ID).Scan(&rev, &hash); err != nil {
				return err
			}
			if rev != d.Revision || hash != d.Hash {
				changed = true
				return nil
			}
			supersede(c, t, in.ProjectID, c.P.UserID, d.ID, "local", kinds)
			cnt := openMarks(c, t, d.ID, c.P.UserID)
			skip, err := loadSettled(c, t, c.P.UserID, d.ID)
			if err != nil {
				return err
			}
			ix := alts.NewIndex(d.Body)
			run := db.NewID("lab")
			for _, m := range marks {
				if cnt >= maxMarksPerDoc {
					break
				}
				fix := ""
				if m.Fix != nil {
					fix = *m.Fix
				}
				wrote, err := insertMark(c, t, skip, in.ProjectID, c.P.UserID, d.ID, run, "local", m.Kind, m.Reason, d.Body, ix, m.Start, m.End, d.Revision, m.Fix != nil, fix)
				if err != nil {
					return err
				}
				if wrote {
					cnt++
				}
			}
			pruneSettled(c, t, d.ID, c.P.UserID)
			rows, err := loadMarks(c, t, in.ProjectID, c.P.UserID, d.ID, "open", "", nil, 1000)
			if err != nil {
				return err
			}
			out = marksOut(d, rows, 1000)
			return nil
		})
		if err != nil {
			return MarksOut{}, mapErr(err)
		}
		if !changed {
			return out, nil
		}
	}
	return MarksOut{}, ops.Conflict("The document is changing under the analysis. Try again in a moment.")
}

// LabMarksIn lists marks.
type LabMarksIn struct {
	ProjectID  string `path:"id" json:"-" desc:"Project id."`
	DocumentID string `query:"document_id"`
	Status     string `query:"status" enum:"open,accepted,dismissed,all" desc:"Default open."`
	Kind       string `query:"kind"`
}

// ProjectRef lets the registry authorize by membership.
func (in LabMarksIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shape.
func (in *LabMarksIn) Validate() error {
	if in.DocumentID == "" || !db.ValidID(in.DocumentID) {
		return ops.Invalid("document_id", "Name the document.")
	}
	if in.Kind != "" {
		ok := false
		for _, k := range lib.MarkKinds {
			ok = ok || k == in.Kind
		}
		if !ok {
			return ops.Invalid("kind", "Unknown mark kind.")
		}
	}
	return nil
}

func (s *Service) labMarks(c *ops.Ctx, in LabMarksIn) (MarksOut, error) {
	if err := s.gateAgents(c, in.ProjectID); err != nil {
		return MarksOut{}, err
	}
	d, err := loadDoc(c, c.DB().Read(), in.ProjectID, in.DocumentID)
	if err != nil {
		return MarksOut{}, mapErr(err)
	}
	st := in.Status
	if st == "" {
		st = "open"
	}
	rows, err := loadMarks(c, c.DB().Read(), in.ProjectID, c.P.UserID, in.DocumentID, st, in.Kind, nil, 1000)
	if err != nil {
		return MarksOut{}, ops.Internal(err)
	}
	return marksOut(d, rows, 1000), nil
}

// ---- dismiss ------------------------------------------------------------------------------------------

// LabDismissIn dismisses marks.
type LabDismissIn struct {
	ProjectID  string   `path:"id" json:"-" desc:"Project id."`
	DocumentID string   `json:"document_id"`
	MarkIDs    []string `json:"mark_ids" desc:"1 to 500 marks of yours."`
}

// ProjectRef lets the registry authorize by membership.
func (in LabDismissIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shape.
func (in *LabDismissIn) Validate() error {
	if in.DocumentID == "" || !db.ValidID(in.DocumentID) {
		return ops.Invalid("document_id", "Name the document.")
	}
	return checkMarkIDs(in.MarkIDs, 500)
}

func checkMarkIDs(ids []string, max int) error {
	if len(ids) == 0 || len(ids) > max {
		return ops.Invalid("mark_ids", fmt.Sprintf("Send between 1 and %d marks.", max))
	}
	seen := map[string]bool{}
	for _, id := range ids {
		if !strings.HasPrefix(id, "mrk_") || !db.ValidID(id) {
			return ops.Invalid("mark_ids", "That is not a mark id.")
		}
		if seen[id] {
			return ops.Invalid("mark_ids", "A mark is named twice.")
		}
		seen[id] = true
	}
	return nil
}

// LabDismissOut says how many were dismissed.
type LabDismissOut struct {
	Dismissed int `json:"dismissed"`
	Open      int `json:"open" desc:"Open marks left on the document."`
}

func (s *Service) labDismiss(c *ops.Ctx, in LabDismissIn) (LabDismissOut, error) {
	var out LabDismissOut
	err := c.DB().WithTx(c, func(t *sql.Tx) error {
		rows, err := loadMarks(c, t, in.ProjectID, c.P.UserID, in.DocumentID, "all", "", in.MarkIDs, 500)
		if err != nil {
			return err
		}
		if len(rows) != len(in.MarkIDs) {
			return ops.NotFound("mark")
		}
		for _, m := range rows {
			if m.Status != "open" {
				continue
			}
			if _, err := t.ExecContext(c, `UPDATE ai_marks SET status = 'dismissed', resolved_at = ? WHERE id = ?`, now(), m.ID); err != nil {
				return err
			}
			out.Dismissed++
		}
		return t.QueryRowContext(c, `SELECT count(*) FROM ai_marks WHERE document_id = ? AND user_id = ? AND status = 'open'`, in.DocumentID, c.P.UserID).Scan(&out.Open)
	})
	return out, mapErr(err)
}

// ---- accepting -------------------------------------------------------------------------------------------

// Skip says why a chosen mark was left out of an accept.
type Skip struct {
	MarkID string `json:"mark_id"`
	Reason string `json:"reason" enum:"stale,overlap,hidden,not_open"`
}

type planned struct {
	m    markRow
	sp   span // the range edited (a deletion may include one neighbouring space)
	edit docs.AIEdit
}

// plan works out, deterministically, what accepting a set of marks does to a
// document: each mark is looked up in the text as it is now (not found exactly
// once: stale); a deletion takes along one neighbouring space so no double space
// is left; marks are taken in document order, and a mark that overlaps one
// already taken is skipped (the earlier start wins, then the longer, then the
// lower id). Marks with no fix are only resolved.
func plan(d docState, rows []markRow) (edits []planned, resolve []markRow, skipped []Skip) {
	ix := alts.NewIndex(d.Body)
	hidden := text.HiddenSpans(d.Body)
	type cand struct {
		m  markRow
		sp span
	}
	var cands []cand
	for _, m := range rows {
		if m.Status != "open" {
			skipped = append(skipped, Skip{m.ID, "not_open"})
			continue
		}
		sp, ok := locateExact(d.Body, ix, m.Quote, m.before, m.after, &span{U1: m.u1, U2: m.u2}, m.AtRev == d.Revision)
		if !ok {
			skipped = append(skipped, Skip{m.ID, "stale"})
			continue
		}
		if !m.hasFix {
			resolve = append(resolve, m)
			continue
		}
		if m.Action == "delete" {
			sp = extendDeletion(d.Body, ix, sp)
		}
		if text.TouchesSpans(hidden, sp.A, sp.B) {
			skipped = append(skipped, Skip{m.ID, "hidden"})
			continue
		}
		cands = append(cands, cand{m, sp})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		a, b := cands[i], cands[j]
		if a.sp.A != b.sp.A {
			return a.sp.A < b.sp.A
		}
		if a.sp.B != b.sp.B {
			return a.sp.B > b.sp.B
		}
		return a.m.ID < b.m.ID
	})
	end := -1
	for _, c := range cands {
		if c.sp.A < end {
			skipped = append(skipped, Skip{c.m.ID, "overlap"})
			continue
		}
		end = c.sp.B
		edits = append(edits, planned{m: c.m, sp: c.sp, edit: docs.AIEdit{Start: c.sp.U1, End: c.sp.U2, Expect: d.Body[c.sp.A:c.sp.B], New: c.m.Fix}})
	}
	return edits, resolve, skipped
}

// rebaseMarks moves the open marks of a document along with the edits this code
// has just made to it: a mark that lies wholly after an edit shifts by what the
// edit added or removed; one that lies before it stays; one the edit touches is
// left as it is (it will find its text again or go stale). The marks follow
// only when they still say exactly what the new text says at the new place.
func rebaseMarks(ctx context.Context, t dbq, old, nw docState, edits []planned) error {
	rows, err := t.QueryContext(ctx, `SELECT id, quote, start_u16, end_u16 FROM ai_marks WHERE document_id = ? AND status = 'open' AND at_revision = ?`, old.ID, old.Revision)
	if err != nil {
		return err
	}
	type mk struct {
		id, quote string
		u1, u2    int
	}
	var ms []mk
	for rows.Next() {
		var m mk
		if err := rows.Scan(&m.id, &m.quote, &m.u1, &m.u2); err != nil {
			rows.Close()
			return err
		}
		ms = append(ms, m)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	oix, nix := alts.NewIndex(old.Body), alts.NewIndex(nw.Body)
	for _, m := range ms {
		a, ok1 := oix.Byte(m.u1)
		b, ok2 := oix.Byte(m.u2)
		if !ok1 || !ok2 {
			continue
		}
		shift, touched := 0, false
		for _, e := range edits {
			switch {
			case e.sp.B <= a:
				shift += len(e.edit.New) - (e.sp.B - e.sp.A)
			case e.sp.A < b:
				touched = true
			}
		}
		na, nb := a+shift, b+shift
		if touched || na < 0 || nb > len(nw.Body) || nw.Body[na:nb] != m.quote {
			continue
		}
		before, after := paragraphContext(nw.Body, na, nb)
		if _, err := t.ExecContext(ctx, `UPDATE ai_marks SET start_u16 = ?, end_u16 = ?, before_ctx = ?, after_ctx = ?, at_revision = ? WHERE id = ?`,
			nix.Unit(na), nix.Unit(nb), before, after, nw.Revision, m.id); err != nil {
			return err
		}
	}
	return nil
}

// extendDeletion widens a deleted range by one neighbouring space, so that
// removing "very" from "a very big" leaves "a big". At the start of a line the
// space after goes; before punctuation or the end of a line the space before.
func extendDeletion(body string, ix *alts.Index, sp span) span {
	a, b := sp.A, sp.B
	before := a == 0 || body[a-1] == '\n' || body[a-1] == ' ' || body[a-1] == '\t'
	switch {
	case b < len(body) && body[b] == ' ' && before:
		b++
	case a > 0 && body[a-1] == ' ' && (b == len(body) || strings.IndexByte(".,;:!?)\n", body[b]) >= 0):
		a--
	}
	return span{A: a, B: b, U1: ix.Unit(a), U2: ix.Unit(b)}
}

// LabAcceptIn accepts one mark.
type LabAcceptIn struct {
	ProjectID    string `path:"id" json:"-" desc:"Project id."`
	DocumentID   string `json:"document_id"`
	MarkID       string `json:"mark_id"`
	BaseRevision int    `json:"base_revision" desc:"The revision of the document you hold; a different one is a 409."`
	TabID        string `json:"tab_id,omitempty"`
}

// ProjectRef lets the registry authorize by membership.
func (in LabAcceptIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shape.
func (in *LabAcceptIn) Validate() error {
	if in.DocumentID == "" || !db.ValidID(in.DocumentID) {
		return ops.Invalid("document_id", "Name the document.")
	}
	if err := checkMarkIDs([]string{in.MarkID}, 1); err != nil {
		return err
	}
	if in.BaseRevision < 1 {
		return ops.Invalid("base_revision", "Send the revision you last saw.")
	}
	return nil
}

// LabAcceptAllIn accepts a chosen set of marks as one change.
type LabAcceptAllIn struct {
	ProjectID    string   `path:"id" json:"-" desc:"Project id."`
	DocumentID   string   `json:"document_id"`
	MarkIDs      []string `json:"mark_ids" desc:"The marks to accept, 1 to 300, all of one document."`
	BaseRevision int      `json:"base_revision"`
	TabID        string   `json:"tab_id,omitempty"`
}

// ProjectRef lets the registry authorize by membership.
func (in LabAcceptAllIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shape.
func (in *LabAcceptAllIn) Validate() error {
	if in.DocumentID == "" || !db.ValidID(in.DocumentID) {
		return ops.Invalid("document_id", "Name the document.")
	}
	if err := checkMarkIDs(in.MarkIDs, 300); err != nil {
		return err
	}
	if in.BaseRevision < 1 {
		return ops.Invalid("base_revision", "Send the revision you last saw.")
	}
	return nil
}

// LabAcceptOut is what an accept of marks did.
type LabAcceptOut struct {
	DocumentID string   `json:"document_id"`
	Applied    []string `json:"applied" desc:"Marks whose edit is in the text now."`
	Resolved   []string `json:"resolved" desc:"Marks with no edit (observations), marked accepted."`
	Skipped    []Skip   `json:"skipped" desc:"Marks left open or untouched, and why: stale (not found exactly once now), overlap (another chosen mark covers the same words), hidden (a private note or ghost), not_open."`
	Revision   int      `json:"revision" desc:"The document's revision after the write (unchanged when nothing was written)."`
	RevisionID string   `json:"revision_id,omitempty" desc:"One history entry (reason ai-accepted) for the whole set."`
	WordCount  int      `json:"word_count,omitempty"`
	Comment    string   `json:"comment,omitempty"`
	Changed    bool     `json:"changed" desc:"The text changed."`
}

// LabPreviewOut is what accepting a set of marks would do, worked out on the
// server from the text as it is now, writing nothing.
type LabPreviewOut struct {
	DocumentID   string              `json:"document_id"`
	Revision     int                 `json:"revision"`
	Applied      []string            `json:"applied"`
	Resolved     []string            `json:"resolved"`
	Skipped      []Skip              `json:"skipped"`
	Edits        int                 `json:"edits"`
	Diff         []revisions.Segment `json:"diff,omitempty" desc:"The combined change, word by word, over the part of the text it touches (from the first edit's paragraph to the last edit's)."`
	DiffNote     string              `json:"diff_note,omitempty"`
	AddedWords   int                 `json:"added_words"`
	RemovedWords int                 `json:"removed_words"`
}

// labSources reports whether a set of marks includes any a model made (those
// follow the project's policy; local ones never do).
func hasModel(rows []markRow) bool {
	for _, m := range rows {
		if m.Source == "model" {
			return true
		}
	}
	return false
}

func (s *Service) labPreview(c *ops.Ctx, in LabAcceptAllIn) (LabPreviewOut, error) {
	var out LabPreviewOut
	d, err := loadDoc(c, c.DB().Read(), in.ProjectID, in.DocumentID)
	if err != nil {
		return out, mapErr(err)
	}
	rows, err := loadMarks(c, c.DB().Read(), in.ProjectID, c.P.UserID, in.DocumentID, "all", "", in.MarkIDs, 300)
	if err != nil {
		return out, ops.Internal(err)
	}
	if len(rows) != len(in.MarkIDs) {
		return out, ops.NotFound("mark")
	}
	edits, resolve, skipped := plan(d, rows)
	out = LabPreviewOut{DocumentID: d.ID, Revision: d.Revision, Applied: []string{}, Resolved: []string{}, Skipped: nonNil(skipped), Edits: len(edits)}
	for _, e := range edits {
		out.Applied = append(out.Applied, e.m.ID)
	}
	for _, m := range resolve {
		out.Resolved = append(out.Resolved, m.ID)
	}
	if len(edits) > 0 {
		lo, hi := edits[0].sp.A, edits[len(edits)-1].sp.B
		lo = strings.LastIndex(d.Body[:lo], "\n") + 1
		if i := strings.Index(d.Body[hi:], "\n"); i >= 0 {
			hi += i
		} else {
			hi = len(d.Body)
		}
		if hi-lo > 200<<10 {
			out.DiffNote = "The change spreads over too much text for a diff; apply it and review the history entry."
		} else {
			var nb strings.Builder
			at := lo
			for _, e := range edits {
				nb.WriteString(d.Body[at:e.sp.A])
				nb.WriteString(e.edit.New)
				at = e.sp.B
			}
			nb.WriteString(d.Body[at:hi])
			df := revisions.Diff(d.Body[lo:hi], nb.String(), revisions.UnitWord)
			out.Diff, out.AddedWords, out.RemovedWords = df.Segments, df.Stats.AddedWords, df.Stats.RemovedWords
		}
	}
	return out, nil
}

func nonNil(s []Skip) []Skip {
	if s == nil {
		return []Skip{}
	}
	return s
}

func (s *Service) labAcceptAll(c *ops.Ctx, in LabAcceptAllIn) (LabAcceptOut, error) {
	out := LabAcceptOut{DocumentID: in.DocumentID, Applied: []string{}, Resolved: []string{}, Skipped: []Skip{}}
	err := c.DB().WithTx(c, func(t *sql.Tx) error {
		d, err := loadDoc(c, t, in.ProjectID, in.DocumentID)
		if err != nil {
			return err
		}
		if d.Revision != in.BaseRevision {
			return ops.ConflictWith("This document changed since you opened it.", map[string]any{"reason": "revision", "current_revision": d.Revision})
		}
		rows, err := loadMarks(c, t, in.ProjectID, c.P.UserID, in.DocumentID, "all", "", in.MarkIDs, 300)
		if err != nil {
			return err
		}
		if len(rows) != len(in.MarkIDs) {
			return ops.NotFound("mark")
		}
		if hasModel(rows) {
			if err := s.mayAccept(c, c.DB(), c.P, in.ProjectID, lib.ActionLab); err != nil {
				return err
			}
		}
		edits, resolve, skipped := plan(d, rows)
		out.Skipped, out.Revision = nonNil(skipped), d.Revision
		if len(edits) > 0 {
			var es []docs.AIEdit
			for _, e := range edits {
				es = append(es, e.edit)
			}
			c.Audit(ops.AuditEntry{Action: "ai.lab.accept", TargetType: "document", TargetID: d.ID, Detail: map[string]any{"project_id": in.ProjectID, "count": len(es)}})
			m, err := docs.ApplyAIEdits(c, t, s.leases, docs.AIEditsIn{DocID: d.ID, ProjectID: in.ProjectID, BaseRevision: in.BaseRevision, TabID: in.TabID, Edits: es,
				Note: fmt.Sprintf("AI Lab: %d marks accepted", len(es))})
			if err != nil {
				return err
			}
			out.Revision, out.RevisionID, out.WordCount, out.Comment, out.Changed = m.Revision, m.RevisionID, m.WordCount, m.Comment, !m.Unchanged
			if nd, err := loadDoc(c, t, in.ProjectID, in.DocumentID); err == nil {
				if err := rebaseMarks(c, t, d, nd, edits); err != nil {
					return err
				}
			}
			for _, e := range edits {
				if _, err := t.ExecContext(c, `UPDATE ai_marks SET status = 'accepted', resolved_at = ? WHERE id = ?`, now(), e.m.ID); err != nil {
					return err
				}
				out.Applied = append(out.Applied, e.m.ID)
			}
		}
		for _, m := range resolve {
			if _, err := t.ExecContext(c, `UPDATE ai_marks SET status = 'accepted', resolved_at = ? WHERE id = ?`, now(), m.ID); err != nil {
				return err
			}
			out.Resolved = append(out.Resolved, m.ID)
		}
		return nil
	})
	return out, mapErr(err)
}

func (s *Service) labAccept(c *ops.Ctx, in LabAcceptIn) (LabAcceptOut, error) {
	out, err := s.labAcceptAll(c, LabAcceptAllIn{ProjectID: in.ProjectID, DocumentID: in.DocumentID, MarkIDs: []string{in.MarkID}, BaseRevision: in.BaseRevision, TabID: in.TabID})
	if err != nil {
		return out, err
	}
	if len(out.Skipped) == 1 {
		switch out.Skipped[0].Reason {
		case "stale":
			return LabAcceptOut{}, ops.ConflictWith("The text this mark was made for has changed or cannot be found exactly once, so nothing was applied. Dismiss the mark, or run the Lab again.", map[string]any{"reason": "stale", "current_revision": out.Revision})
		case "hidden":
			return LabAcceptOut{}, ops.Conflict("This mark touches a private note or ghosted text, which the Lab never changes.")
		case "not_open":
			return out, nil // idempotent: it was settled before
		}
	}
	return out, nil
}

var errNoMark = errors.New("no mark")
