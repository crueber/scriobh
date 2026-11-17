package aiwork

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/ops"
)

// The accept ops. Each applies a text the server holds from a run of the
// caller's own, never a text the caller sends. The write goes through the
// document's own seam (docs.ApplyAIEdits and friends), so a stale revision is
// the usual 409 with the current revision, and the whole accept (the document
// write, the history entry, the event, the ledger row) is one transaction.

func checkRun(runID string) error {
	if !runIDRe.MatchString(runID) {
		return ops.Invalid("run_id", "That is not a run id.")
	}
	return nil
}

func checkItem(id string, def string) (string, error) {
	if id == "" {
		return def, nil
	}
	if !itemIDRe.MatchString(id) {
		return "", ops.Invalid("suggestion_id", "That is not a suggestion id.")
	}
	return id, nil
}

// AcceptEditIn accepts one suggestion of an edit run.
type AcceptEditIn struct {
	ProjectID    string `path:"id" json:"-" desc:"Project id."`
	RunID        string `json:"run_id" desc:"The run that made the suggestion (its data.run_id)."`
	SuggestionID string `json:"suggestion_id,omitempty" desc:"Default s1."`
	BaseRevision int    `json:"base_revision" desc:"The revision of the document you hold. The suggestion applies to the text it was made from: if the document has changed, the suggestion is applied only where its original text is still found exactly once with all the context it had, and a 409 says so when it is not."`
	TabID        string `json:"tab_id,omitempty" desc:"Browser tab id; enables the single-writer lease."`
	ExpectHash   string `json:"expect_hash,omitempty" desc:"The text_hash of the suggestion you reviewed. A different text is refused."`
}

// ProjectRef lets the registry authorize by membership.
func (in AcceptEditIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shape.
func (in *AcceptEditIn) Validate() error {
	if err := checkRun(in.RunID); err != nil {
		return err
	}
	var err error
	if in.SuggestionID, err = checkItem(in.SuggestionID, "s1"); err != nil {
		return err
	}
	if in.BaseRevision < 1 {
		return ops.Invalid("base_revision", "Send the revision you last saw.")
	}
	return nil
}

// AcceptOut is what an accept returns.
type AcceptOut struct {
	Applied      bool   `json:"applied" desc:"False when this suggestion had already been accepted (already is then true): nothing was written."`
	Already      bool   `json:"already,omitempty"`
	RunID        string `json:"run_id"`
	SuggestionID string `json:"suggestion_id"`
	DocumentID   string `json:"document_id"`
	Revision     int    `json:"revision" desc:"The document's revision after the write."`
	RevisionID   string `json:"revision_id,omitempty" desc:"The history entry (reason ai-accepted); restoring the one before undoes the change."`
	ContentHash  string `json:"content_hash,omitempty"`
	WordCount    int    `json:"word_count,omitempty"`
	Comment      string `json:"comment,omitempty" desc:"The alternatives comment that now ends the text, as alternative ops return it."`
	Replaced     *struct {
		Start  int `json:"start" desc:"UTF-16 code units of the body before the change."`
		End    int `json:"end"`
		NewEnd int `json:"new_end"`
	} `json:"replaced,omitempty"`
}

func (s *Service) acceptEdit(c *ops.Ctx, in AcceptEditIn) (AcceptOut, error) {
	out := AcceptOut{RunID: in.RunID, SuggestionID: in.SuggestionID}
	if act, rev, rid, ok := ledger(c, c.DB().Read(), in.RunID, in.SuggestionID, c.P.UserID); ok {
		if act == "rejected" {
			return out, ops.Conflict("That suggestion was rejected.")
		}
		out.Already, out.Revision, out.RevisionID = true, rev, rid
		return out, nil
	}
	err := c.DB().WithTx(c, func(t *sql.Tx) error {
		purgeExpired(c, t)
		if act, rev, rid, ok := ledger(c, t, in.RunID, in.SuggestionID, c.P.UserID); ok {
			if act == "rejected" {
				return ops.Conflict("That suggestion was rejected.")
			}
			out.Already, out.Revision, out.RevisionID = true, rev, rid
			return nil
		}
		p, err := s.loadPending(c, t, in.RunID, c.P.UserID, in.ProjectID)
		if err != nil {
			return err
		}
		if p.Kind != "edit" && p.Kind != "chat" {
			return ops.Invalid("run_id", "That run did not make a change to the text.")
		}
		if err := s.mayAccept(c, c.DB(), c.P, in.ProjectID, actionOf(p.Recipe)); err != nil {
			return err
		}
		if err := verifyHash(c, t, p.RunID, p.UserID, p.OutputHash); err != nil {
			return err
		}
		var items []editItem
		if err := json.Unmarshal([]byte(p.ItemsJSON), &items); err != nil {
			return errGone
		}
		var it editItem
		found := false
		for _, x := range items {
			if x.ID == in.SuggestionID {
				it, found = x, true
			}
		}
		if !found {
			return ops.NotFound("suggestion")
		}
		if sha(it.Text) != it.TextHash {
			return errGone // the held text is not what was recorded
		}
		if in.ExpectHash != "" && in.ExpectHash != it.TextHash {
			return ops.ConflictWith("The suggestion is not the text you reviewed, so it was not applied.", map[string]any{"reason": "hash"})
		}
		d, err := loadDoc(c, t, in.ProjectID, p.DocumentID)
		if err != nil {
			return err
		}
		ix := alts.NewIndex(d.Body)
		sp, ok := locateExact(d.Body, ix, it.Original, it.Before, it.After, &span{U1: it.Start, U2: it.End}, p.DocRevision == d.Revision)
		if !ok {
			return ops.ConflictWith("The text the suggestion was made for has changed or cannot be found exactly once, so it was not applied.", map[string]any{"reason": "stale", "current_revision": d.Revision})
		}
		e := docs.AIEdit{Start: sp.U1, End: sp.U2, Expect: it.Original, New: it.Text}
		if it.Op == "insert_after" {
			sep := " "
			if strings.HasPrefix(it.Text, "\n") {
				sep = ""
			}
			e = docs.AIEdit{Start: sp.U2, End: sp.U2, Expect: "", New: sep + it.Text}
		}
		c.Audit(ops.AuditEntry{Action: "ai.accept", TargetType: "document", TargetID: d.ID, Detail: map[string]any{"run_id": in.RunID, "recipe": p.Recipe, "project_id": in.ProjectID}})
		m, err := docs.ApplyAIEdits(c, t, s.leases, docs.AIEditsIn{DocID: d.ID, ProjectID: in.ProjectID, BaseRevision: in.BaseRevision, TabID: in.TabID,
			Edits: []docs.AIEdit{e}, Note: fmt.Sprintf("AI suggestion accepted: %s, run %s", p.Recipe, p.RunID)})
		if err != nil {
			return err
		}
		if err := record(c, t, in.RunID, in.SuggestionID, in.ProjectID, c.P.UserID, "accepted", m.Revision, m.RevisionID); err != nil {
			return err
		}
		dropPendingIfDone(c, t, in.RunID, ids(items))
		out.Applied, out.DocumentID, out.Revision, out.RevisionID, out.ContentHash, out.WordCount, out.Comment = true, d.ID, m.Revision, m.RevisionID, m.ContentHash, m.WordCount, m.Comment
		out.Replaced = &struct {
			Start  int `json:"start" desc:"UTF-16 code units of the body before the change."`
			End    int `json:"end"`
			NewEnd int `json:"new_end"`
		}{e.Start, e.End, e.Start + alts.Len16(e.New)}
		return nil
	})
	return out, mapErr(err)
}

func ids(items []editItem) []string {
	var out []string
	for _, it := range items {
		out = append(out, it.ID)
	}
	return out
}

func actionOf(recipe string) string {
	if r, ok := lib.LookupRecipe(recipe); ok {
		return r.Action
	}
	return ""
}

// ---- reject ---------------------------------------------------------------------------------

// AcceptRejectIn dismisses suggestions of a run.
type AcceptRejectIn struct {
	ProjectID    string `path:"id" json:"-" desc:"Project id."`
	RunID        string `json:"run_id"`
	SuggestionID string `json:"suggestion_id,omitempty" desc:"One suggestion or candidate. Empty: every one of the run that is still open."`
}

// ProjectRef lets the registry authorize by membership.
func (in AcceptRejectIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shape.
func (in *AcceptRejectIn) Validate() error {
	if err := checkRun(in.RunID); err != nil {
		return err
	}
	if in.SuggestionID != "" && !itemIDRe.MatchString(in.SuggestionID) {
		return ops.Invalid("suggestion_id", "That is not a suggestion id.")
	}
	return nil
}

// AcceptRejectOut says how many were rejected.
type AcceptRejectOut struct {
	Rejected int `json:"rejected"`
	Open     int `json:"open" desc:"Suggestions of the run still waiting."`
}

// itemIDs lists the item ids of a held row, whatever its kind.
func itemIDs(p pendingRow) []string {
	switch p.Kind {
	case "alternatives":
		var a altItems
		_ = json.Unmarshal([]byte(p.ItemsJSON), &a)
		var out []string
		for _, c := range a.Candidates {
			out = append(out, c.ID)
		}
		return out
	default:
		var raw []struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal([]byte(p.ItemsJSON), &raw)
		var out []string
		for _, r := range raw {
			out = append(out, r.ID)
		}
		return out
	}
}

func (s *Service) acceptReject(c *ops.Ctx, in AcceptRejectIn) (AcceptRejectOut, error) {
	var out AcceptRejectOut
	err := c.DB().WithTx(c, func(t *sql.Tx) error {
		purgeExpired(c, t)
		p, err := s.loadPending(c, t, in.RunID, c.P.UserID, in.ProjectID)
		if err != nil {
			return err
		}
		all := itemIDs(p)
		pick := all
		if in.SuggestionID != "" {
			pick = nil
			for _, id := range all {
				if id == in.SuggestionID {
					pick = []string{id}
				}
			}
			if pick == nil {
				return ops.NotFound("suggestion")
			}
		}
		for _, id := range pick {
			if _, _, _, ok := ledger(c, t, in.RunID, id, c.P.UserID); ok {
				continue
			}
			if err := record(c, t, in.RunID, id, in.ProjectID, c.P.UserID, "rejected", 0, ""); err != nil {
				return err
			}
			out.Rejected++
		}
		for _, id := range all {
			if _, _, _, ok := ledger(c, t, in.RunID, id, c.P.UserID); !ok {
				out.Open++
			}
		}
		if out.Open == 0 {
			_, _ = t.ExecContext(c, `DELETE FROM ai_pending WHERE run_id = ?`, in.RunID)
		}
		return nil
	})
	return out, mapErr(err)
}

// ---- alternatives -------------------------------------------------------------------------------

// AcceptAltIn adds candidates to an alternative.
type AcceptAltIn struct {
	ProjectID     string   `path:"id" json:"-" desc:"Project id."`
	RunID         string   `json:"run_id"`
	CandidateIDs  []string `json:"candidate_ids,omitempty" desc:"The candidates to add. Empty: every one still open."`
	AlternativeID string   `json:"alternative_id,omitempty" desc:"Add them to this alternative. Empty: make an alternative over the text the run was made for (its original wording stays chosen)."`
	BaseRevision  int      `json:"base_revision"`
	TabID         string   `json:"tab_id,omitempty"`
	Choose        string   `json:"choose,omitempty" desc:"A candidate id to put in the text in the same save. Empty: the text does not change."`
}

// ProjectRef lets the registry authorize by membership.
func (in AcceptAltIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shape.
func (in *AcceptAltIn) Validate() error {
	if err := checkRun(in.RunID); err != nil {
		return err
	}
	if len(in.CandidateIDs) > 5 {
		return ops.Invalid("candidate_ids", "At most 5 candidates.")
	}
	for _, id := range append(append([]string{}, in.CandidateIDs...), in.Choose) {
		if id != "" && !itemIDRe.MatchString(id) {
			return ops.Invalid("candidate_ids", "That is not a candidate id.")
		}
	}
	if in.AlternativeID != "" && !strings.HasPrefix(in.AlternativeID, "alt_") {
		return ops.Invalid("alternative_id", "That is not an alternative id.")
	}
	if in.BaseRevision < 1 {
		return ops.Invalid("base_revision", "Send the revision you last saw.")
	}
	return nil
}

// AcceptAltOut is the alternatives write with what was added.
type AcceptAltOut struct {
	docs.AltMut
	RunID   string   `json:"run_id"`
	Added   []string `json:"added" desc:"Candidates now wordings of the alternative, marked source generated."`
	Skipped []string `json:"skipped,omitempty" desc:"Candidates already accepted or rejected before."`
	Already bool     `json:"already,omitempty"`
}

func (s *Service) acceptAlternative(c *ops.Ctx, in AcceptAltIn) (AcceptAltOut, error) {
	out := AcceptAltOut{RunID: in.RunID, Added: []string{}}
	err := c.DB().WithTx(c, func(t *sql.Tx) error {
		purgeExpired(c, t)
		p, err := s.loadPending(c, t, in.RunID, c.P.UserID, in.ProjectID)
		if err != nil {
			return err
		}
		if p.Kind != "alternatives" {
			return ops.Invalid("run_id", "That run did not make alternatives.")
		}
		if err := s.mayAccept(c, c.DB(), c.P, in.ProjectID, lib.ActionAlternatives); err != nil {
			return err
		}
		if err := verifyHash(c, t, p.RunID, p.UserID, p.OutputHash); err != nil {
			return err
		}
		var ai altItems
		if json.Unmarshal([]byte(p.ItemsJSON), &ai) != nil {
			return errGone
		}
		want := in.CandidateIDs
		if len(want) == 0 {
			for _, cd := range ai.Candidates {
				want = append(want, cd.ID)
			}
		}
		byID := map[string]Candidate{}
		for _, cd := range ai.Candidates {
			byID[cd.ID] = cd
		}
		var texts, picked []string
		for _, id := range want {
			cd, ok := byID[id]
			if !ok {
				return ops.NotFound("candidate")
			}
			if sha(cd.Text) != cd.TextHash {
				return errGone
			}
			if _, _, _, done := ledger(c, t, in.RunID, id, c.P.UserID); done {
				out.Skipped = append(out.Skipped, id)
				continue
			}
			texts, picked = append(texts, cd.Text), append(picked, id)
		}
		if len(texts) == 0 {
			out.Already = len(out.Skipped) > 0
			return nil
		}
		choose := 0
		if in.Choose != "" {
			for i, id := range picked {
				if id == in.Choose {
					choose = i + 1
				}
			}
			if choose == 0 {
				return ops.Invalid("choose", "Choose one of the candidates you are adding.")
			}
		}
		d, err := loadDoc(c, t, in.ProjectID, p.DocumentID)
		if err != nil {
			return err
		}
		ain := docs.AIAltIn{DocID: d.ID, ProjectID: in.ProjectID, BaseRevision: in.BaseRevision, TabID: in.TabID, AlternativeID: in.AlternativeID,
			Scope: ai.Scope, Variants: texts, Choose: choose}
		if in.AlternativeID == "" {
			o := ai.Original
			ix := alts.NewIndex(d.Body)
			sp, ok := locateExact(d.Body, ix, o.Original, o.Before, o.After, &span{U1: o.Start, U2: o.End}, p.DocRevision == d.Revision)
			if !ok {
				return ops.ConflictWith("The text the candidates were made for has changed or cannot be found exactly once, so nothing was added.", map[string]any{"reason": "stale", "current_revision": d.Revision})
			}
			ain.Start, ain.End, ain.Expect = sp.U1, sp.U2, o.Original
		}
		c.Audit(ops.AuditEntry{Action: "ai.accept", TargetType: "document", TargetID: d.ID, Detail: map[string]any{"run_id": in.RunID, "recipe": p.Recipe, "project_id": in.ProjectID, "count": len(texts)}})
		m, err := docs.AddGeneratedVariants(c, t, s.leases, ain)
		if err != nil {
			return err
		}
		for _, id := range picked {
			if err := record(c, t, in.RunID, id, in.ProjectID, c.P.UserID, "accepted", m.Revision, m.RevisionID); err != nil {
				return err
			}
		}
		var all []string
		for _, cd := range ai.Candidates {
			all = append(all, cd.ID)
		}
		dropPendingIfDone(c, t, in.RunID, all)
		out.AltMut, out.Added = m, picked
		return nil
	})
	return out, mapErr(err)
}

var _ = db.Now
