package aiwork

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/db"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/docref"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/ops"
)

// Summaries. A summary is a suggestion for a document's synopsis; accepting it
// writes the synopsis through doc.update's own save (revision-checked, the same
// hooks and event), which earns no word credit because it changes no text.
//
// Rolling summaries run the manuscript chapter by chapter, each using the
// previous chapter's accepted summary as context. Every send is previewed and
// bound to its hash, so the job cannot run on its own: the server keeps the plan
// and the position (resumable, cancellable), the client asks for the next step,
// previews it, runs it and accepts it. The whole job's estimate and a check
// against the caps are made up front, when the plan is made.

// SummaryAcceptIn accepts a summary.
type SummaryAcceptIn struct {
	ProjectID    string `path:"id" json:"-" desc:"Project id."`
	RunID        string `json:"run_id"`
	Apply        string `json:"apply,omitempty" enum:"both,short,long" desc:"Which synopsis to write. Default both."`
	BaseRevision int    `json:"base_revision"`
	TabID        string `json:"tab_id,omitempty"`
	ExpectHash   string `json:"expect_hash,omitempty" desc:"The text_hash of the suggestion you reviewed."`
	JobID        string `json:"job_id,omitempty" desc:"A rolling summary job: the step for this document is marked done."`
}

// ProjectRef lets the registry authorize by membership.
func (in SummaryAcceptIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shape.
func (in *SummaryAcceptIn) Validate() error {
	if err := checkRun(in.RunID); err != nil {
		return err
	}
	switch in.Apply {
	case "", "both", "short", "long":
	default:
		return ops.Invalid("apply", "Apply both, short or long.")
	}
	if in.BaseRevision < 1 {
		return ops.Invalid("base_revision", "Send the revision you last saw.")
	}
	if in.JobID != "" && !strings.HasPrefix(in.JobID, "job_") {
		return ops.Invalid("job_id", "That is not a job id.")
	}
	return nil
}

// SummaryAcceptOut says what was written.
type SummaryAcceptOut struct {
	Applied    bool   `json:"applied"`
	Already    bool   `json:"already,omitempty"`
	RunID      string `json:"run_id"`
	DocumentID string `json:"document_id"`
	Revision   int    `json:"revision"`
	Wrote      struct {
		Short bool `json:"short"`
		Long  bool `json:"long"`
	} `json:"wrote"`
	JobDone bool `json:"job_done,omitempty"`
}

func (s *Service) summaryAccept(c *ops.Ctx, in SummaryAcceptIn) (SummaryAcceptOut, error) {
	out := SummaryAcceptOut{RunID: in.RunID}
	if act, rev, _, ok := ledger(c, c.DB().Read(), in.RunID, "s1", c.P.UserID); ok {
		if act == "rejected" {
			return out, ops.Conflict("That summary was rejected.")
		}
		out.Already, out.Revision = true, rev
		return out, nil
	}
	err := c.DB().WithTx(c, func(t *sql.Tx) error {
		purgeExpired(c, t)
		p, err := s.loadPending(c, t, in.RunID, c.P.UserID, in.ProjectID)
		if err != nil {
			return err
		}
		if p.Kind != "summary" {
			return ops.Invalid("run_id", "That run did not make a summary.")
		}
		if err := s.mayAccept(c, c.DB(), c.P, in.ProjectID, lib.ActionSummaries); err != nil {
			return err
		}
		if err := verifyHash(c, t, p.RunID, p.UserID, p.OutputHash); err != nil {
			return err
		}
		var items []SummarySuggestion
		if json.Unmarshal([]byte(p.ItemsJSON), &items) != nil || len(items) != 1 {
			return errGone
		}
		it := items[0]
		if sha(it.Short+"\x00"+it.Long) != it.TextHash {
			return errGone
		}
		if in.ExpectHash != "" && in.ExpectHash != it.TextHash {
			return ops.ConflictWith("The summary is not the text you reviewed, so it was not applied.", map[string]any{"reason": "hash"})
		}
		d, err := loadDoc(c, t, in.ProjectID, p.DocumentID)
		if err != nil {
			return err
		}
		ui := docs.UpdateIn{ID: d.ID, BaseRevision: in.BaseRevision, TabID: in.TabID}
		if it.Short != "" && in.Apply != "long" {
			v := it.Short
			ui.SynopsisShort, out.Wrote.Short = &v, true
		}
		if it.Long != "" && in.Apply != "short" {
			v := it.Long
			ui.SynopsisLong, out.Wrote.Long = &v, true
		}
		if !out.Wrote.Short && !out.Wrote.Long {
			return ops.Invalid("apply", "The suggestion has nothing to write for that choice.")
		}
		c.Audit(ops.AuditEntry{Action: "ai.accept", TargetType: "document", TargetID: d.ID, Detail: map[string]any{"run_id": in.RunID, "recipe": p.Recipe, "project_id": in.ProjectID}})
		if err := docs.UpdateTx(c, t, s.leases, ui); err != nil {
			return err
		}
		var rev int
		if err := t.QueryRowContext(c, `SELECT revision FROM documents WHERE id = ?`, d.ID).Scan(&rev); err != nil {
			return err
		}
		if err := record(c, t, in.RunID, "s1", in.ProjectID, c.P.UserID, "accepted", rev, ""); err != nil {
			return err
		}
		dropPendingIfDone(c, t, in.RunID, []string{"s1"})
		out.Applied, out.DocumentID, out.Revision = true, d.ID, rev
		if in.JobID != "" {
			done, err := advanceJob(c, t, in.ProjectID, c.P.UserID, in.JobID, d.ID, in.RunID, "done")
			if err != nil {
				return err
			}
			out.JobDone = done
		}
		return nil
	})
	return out, mapErr(err)
}

// ---- rolling jobs ----------------------------------------------------------------------------------

type step struct {
	DocumentID string `json:"document_id"`
	Title      string `json:"title"`
	State      string `json:"state" enum:"todo,done,skipped"`
	RunID      string `json:"run_id,omitempty"`
}

// Estimate is what a whole job is expected to take, made when it was planned.
type Estimate struct {
	Chapters     int    `json:"chapters"`
	InputTokens  int    `json:"input_tokens" desc:"Summed over the chapters, by the same heuristic the preview uses; the previous-summary context is included."`
	OutputTokens int    `json:"output_tokens_max" desc:"The longest answer allowed for each chapter, summed: what the caps are checked against."`
	CostMicros   *int64 `json:"max_cost_micros,omitempty" desc:"Worst case in micro-units, when the price is known and the provider is not local; absent otherwise."`
	Local        bool   `json:"local"`
	Approximate  bool   `json:"approximate" desc:"Always true: each chapter is previewed, with its exact payload and cost, when it is run."`
}

// JobOut is a rolling summary job.
type JobOut struct {
	JobID     string   `json:"job_id"`
	Status    string   `json:"status" enum:"open,done,cancelled,none" desc:"none: you have no open job to resume."`
	Position  int      `json:"position" desc:"Chapters finished or skipped."`
	Total     int      `json:"total"`
	Steps     []step   `json:"steps"`
	Estimate  Estimate `json:"estimate"`
	CreatedAt string   `json:"created_at"`
}

func loadJob(ctx context.Context, q dbq, projectID, userID, id string) (JobOut, error) {
	var j JobOut
	var plan, est string
	query := `SELECT id, status, plan_json, estimate_json, created_at FROM ai_jobs WHERE project_id = ? AND user_id = ?`
	args := []any{projectID, userID}
	if id != "" {
		query += ` AND id = ?`
		args = append(args, id)
	} else {
		query += ` AND status = 'open' ORDER BY created_at DESC LIMIT 1`
	}
	err := q.QueryRowContext(ctx, query, args...).Scan(&j.JobID, &j.Status, &plan, &est, &j.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return j, ops.NotFound("job")
	}
	if err != nil {
		return j, err
	}
	_ = json.Unmarshal([]byte(plan), &j.Steps)
	_ = json.Unmarshal([]byte(est), &j.Estimate)
	j.Total = len(j.Steps)
	for _, st := range j.Steps {
		if st.State != "todo" {
			j.Position++
		}
	}
	if j.Steps == nil {
		j.Steps = []step{}
	}
	return j, nil
}

// advanceJob records the outcome of the step for a document and closes the job
// when none is left. It reports whether the job is now done.
func advanceJob(ctx context.Context, t dbq, projectID, userID, jobID, docID, runID, state string) (bool, error) {
	j, err := loadJob(ctx, t, projectID, userID, jobID)
	if err != nil {
		return false, err
	}
	if j.Status != "open" {
		return false, nil
	}
	left := 0
	for i := range j.Steps {
		if j.Steps[i].DocumentID == docID && j.Steps[i].State == "todo" {
			j.Steps[i].State, j.Steps[i].RunID = state, runID
		}
		if j.Steps[i].State == "todo" {
			left++
		}
	}
	status := "open"
	if left == 0 {
		status = "done"
	}
	_, err = t.ExecContext(ctx, `UPDATE ai_jobs SET plan_json = ?, status = ?, updated_at = ? WHERE id = ?`, toJSON(j.Steps), status, now(), jobID)
	return status == "done", err
}

// RollingStartIn plans a rolling summary.
type RollingStartIn struct {
	ProjectID    string   `path:"id" json:"-" desc:"Project id."`
	DocumentIDs  []string `json:"document_ids,omitempty" desc:"The chapters to summarise. Empty: every chapter."`
	KeepExisting bool     `json:"keep_existing,omitempty" desc:"Leave out chapters that already have a long synopsis."`
}

// ProjectRef lets the registry authorize by membership.
func (in RollingStartIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shape.
func (in *RollingStartIn) Validate() error {
	if len(in.DocumentIDs) > 300 {
		return ops.Invalid("document_ids", "At most 300 chapters.")
	}
	return nil
}

// JobItem wraps a job. A plan that is refused (policy, providers, the caps) is
// not an error: no job is made and Decision says why, as a preview does.
type JobItem struct {
	Item     JobOut           `json:"item"`
	Decision *aicore.Decision `json:"decision,omitempty" desc:"Present when a plan was asked for: allowed false means no job was made, with the code and the reason (policy_off, action_not_allowed, no_provider, budget_exceeded, ...)."`
}

func denied(code, msg string) JobItem {
	return JobItem{Item: JobOut{Steps: []step{}}, Decision: &aicore.Decision{Allowed: false, Code: code, Message: msg}}
}

// asDecision turns a refusal by a rule into a decision; anything else stays an error.
func asDecision(err error) (JobItem, bool) {
	var oe *ops.Error
	if errors.As(err, &oe) && oe.Code == ops.CodeForbidden {
		code, _ := oe.Data["ai_code"].(string)
		return denied(code, oe.Message), true
	}
	return JobItem{}, false
}

const rollingMaxOut = 700 // the summary recipe's output ceiling

func (s *Service) rollingStart(c *ops.Ctx, in RollingStartIn) (JobItem, error) {
	if err := s.mayAccept(c, c.DB(), c.P, in.ProjectID, lib.ActionSummaries); err != nil {
		if d, ok := asDecision(err); ok {
			return d, nil
		}
		return JobItem{}, err
	}
	rows, err := c.DB().Read().QueryContext(c, `SELECT id, COALESCE(parent_id,''), kind, title, position, length(CAST(content AS BLOB)), (trim(content, ' ' || char(9) || char(10) || char(13)) = ''), synopsis_long, ai_exclude FROM documents
		WHERE project_id = ? AND deleted_at IS NULL AND kind <> 'research'`, in.ProjectID)
	if err != nil {
		return JobItem{}, ops.Internal(err)
	}
	type chap struct {
		title string
		toks  int
		hasLn bool
		ex    bool
		empty bool
		kind  string
	}
	byID := map[string]chap{}
	var nodes []docref.Node
	for rows.Next() {
		var n docref.Node
		var size, empty int
		var syn string
		var ex int
		if err := rows.Scan(&n.ID, &n.ParentID, &n.Kind, &n.Title, &n.Position, &size, &empty, &syn, &ex); err != nil {
			rows.Close()
			return JobItem{}, ops.Internal(err)
		}
		nodes = append(nodes, n)
		byID[n.ID] = chap{title: n.Title, toks: size/3 + 1, hasLn: strings.TrimSpace(syn) != "", ex: ex == 1, kind: n.Kind, empty: empty == 1} // the text is not loaded: its size is enough for an estimate
	}
	if err := rows.Close(); err != nil {
		return JobItem{}, ops.Internal(err)
	}
	want := map[string]bool{}
	for _, id := range in.DocumentIDs {
		ch, ok := byID[id]
		if !ok || ch.kind != "chapter" {
			return JobItem{}, ops.Invalid("document_ids", "Every document must be a chapter of this project.")
		}
		want[id] = true
	}
	var steps []step
	inTok := 0
	for _, id := range docref.Order(nodes) {
		ch := byID[id]
		if ch.kind != "chapter" || ch.ex || ch.empty || (len(want) > 0 && !want[id]) || (in.KeepExisting && ch.hasLn) {
			continue
		}
		steps = append(steps, step{DocumentID: id, Title: ch.title, State: "todo"})
		inTok += ch.toks + 900 // the framing, the brief and the previous summary
	}
	if len(steps) == 0 {
		return JobItem{}, ops.Invalid("document_ids", "There is no chapter to summarise.")
	}
	// The first chapter is previewed for real: the policy, the providers and the caps
	// answer for the job, and the price (when known) scales to the others.
	pv, err := s.ai.Preview(c, c.DB(), c.P, aicore.RunIn{ProjectID: in.ProjectID, Recipe: "summary.chapter", DocumentID: steps[0].DocumentID})
	if err != nil {
		return JobItem{}, err
	}
	p := pv.Item
	if !p.Decision.Allowed {
		return denied(p.Decision.Code, p.Decision.Message), nil
	}
	est := Estimate{Chapters: len(steps), InputTokens: inTok, OutputTokens: len(steps) * rollingMaxOut, Local: p.Cost.Local, Approximate: true}
	if p.Cost.Known && !p.Cost.Local && p.Cost.MaxMicros != nil && p.InputTokens+p.MaxTokens > 0 {
		perTok := float64(*p.Cost.MaxMicros) / float64(p.InputTokens+p.MaxTokens)
		v := int64(perTok * float64(inTok+est.OutputTokens))
		est.CostMicros = &v
	}
	if p.Budget != nil {
		total := int64(inTok + est.OutputTokens)
		for _, l := range p.Budget.Lines {
			if l.MaxTokens > 0 && l.MaxTokens-l.SpentTokens-l.ReservedTokens < total {
				return denied("budget_exceeded", fmt.Sprintf("The whole job needs about %d tokens and the %s cap has %d left this month. Summarise fewer chapters, or ask an administrator for more room.", total, l.Scope, max(0, l.MaxTokens-l.SpentTokens-l.ReservedTokens))), nil
			}
			if est.CostMicros != nil && l.MaxCostMicros > 0 && l.MaxCostMicros-l.SpentCostMicros-l.ReservedCostMicros < *est.CostMicros {
				return denied("budget_exceeded", fmt.Sprintf("The whole job could cost up to %d micro-units and the %s cap has %d left this month. Summarise fewer chapters.", *est.CostMicros, l.Scope, max(0, l.MaxCostMicros-l.SpentCostMicros-l.ReservedCostMicros))), nil
			}
		}
	}
	var out JobOut
	err = c.DB().WithTx(c, func(t *sql.Tx) error {
		// one open job at a time per person and project: planning again replaces it
		if _, err := t.ExecContext(c, `UPDATE ai_jobs SET status = 'cancelled', updated_at = ? WHERE project_id = ? AND user_id = ? AND status = 'open'`, now(), in.ProjectID, c.P.UserID); err != nil {
			return err
		}
		id := db.NewID("job")
		if _, err := t.ExecContext(c, `INSERT INTO ai_jobs(id, project_id, user_id, kind, status, plan_json, estimate_json, created_at, updated_at) VALUES (?,?,?,'rolling_summary','open',?,?,?,?)`,
			id, in.ProjectID, c.P.UserID, toJSON(steps), toJSON(est), now(), now()); err != nil {
			return err
		}
		j, err := loadJob(c, t, in.ProjectID, c.P.UserID, id)
		out = j
		return err
	})
	return JobItem{Item: out, Decision: &aicore.Decision{Allowed: true}}, mapErr(err)
}

// RollingGetIn reads a job.
type RollingGetIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	JobID     string `query:"job_id" desc:"Empty: your open job on this project, to resume it."`
}

// ProjectRef lets the registry authorize by membership.
func (in RollingGetIn) ProjectRef() string { return in.ProjectID }

// Validate checks the id.
func (in *RollingGetIn) Validate() error {
	if in.JobID != "" {
		return checkID("job_id", in.JobID, "job_")
	}
	return nil
}

func (s *Service) rollingGet(c *ops.Ctx, in RollingGetIn) (JobItem, error) {
	if err := s.gateRead(c, in.ProjectID); err != nil {
		return JobItem{}, err
	}
	j, err := loadJob(c, c.DB().Read(), in.ProjectID, c.P.UserID, in.JobID)
	var oe *ops.Error
	if in.JobID == "" && errors.As(err, &oe) && oe.Code == ops.CodeNotFound {
		return JobItem{Item: JobOut{Steps: []step{}, Status: "none"}}, nil // nothing to resume
	}
	return JobItem{Item: j}, mapErr(err)
}

// RollingNextIn asks for the next step.
type RollingNextIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	JobID     string `json:"job_id"`
}

// ProjectRef lets the registry authorize by membership.
func (in RollingNextIn) ProjectRef() string { return in.ProjectID }

// Validate checks the id.
func (in *RollingNextIn) Validate() error { return checkID("job_id", in.JobID, "job_") }

// NextOut is the next step of a job: the request to preview and run.
type NextOut struct {
	Job   JobOut `json:"job"`
	Step  *step  `json:"step,omitempty" desc:"Absent when the job is finished or cancelled."`
	RunIn *struct {
		Recipe     string            `json:"recipe"`
		DocumentID string            `json:"document_id"`
		Params     map[string]string `json:"params"`
	} `json:"run_in,omitempty" desc:"The fields of ai.context.preview and ai.run for this step (default chips: the chapter, the previous chapter's summary, the brief, the style guide). Preview it, run it, then accept it with ai.summary.accept and this job_id."`
}

func (s *Service) rollingNext(c *ops.Ctx, in RollingNextIn) (NextOut, error) {
	j, err := loadJob(c, c.DB().Read(), in.ProjectID, c.P.UserID, in.JobID)
	if err != nil {
		return NextOut{}, mapErr(err)
	}
	out := NextOut{Job: j}
	if j.Status != "open" {
		return out, nil
	}
	for i := range j.Steps {
		if j.Steps[i].State == "todo" {
			st := j.Steps[i]
			out.Step = &st
			out.RunIn = &struct {
				Recipe     string            `json:"recipe"`
				DocumentID string            `json:"document_id"`
				Params     map[string]string `json:"params"`
			}{"summary.chapter", st.DocumentID, map[string]string{"length": "both"}}
			break
		}
	}
	return out, nil
}

// RollingSkipIn skips the current step.
type RollingSkipIn = RollingNextIn

func (s *Service) rollingSkip(c *ops.Ctx, in RollingNextIn) (JobItem, error) {
	var out JobOut
	err := c.DB().WithTx(c, func(t *sql.Tx) error {
		j, err := loadJob(c, t, in.ProjectID, c.P.UserID, in.JobID)
		if err != nil {
			return err
		}
		for _, st := range j.Steps {
			if st.State == "todo" {
				if _, err := advanceJob(c, t, in.ProjectID, c.P.UserID, in.JobID, st.DocumentID, "", "skipped"); err != nil {
					return err
				}
				break
			}
		}
		out, err = loadJob(c, t, in.ProjectID, c.P.UserID, in.JobID)
		return err
	})
	return JobItem{Item: out}, mapErr(err)
}

func (s *Service) rollingCancel(c *ops.Ctx, in RollingNextIn) (JobItem, error) {
	var out JobOut
	err := c.DB().WithTx(c, func(t *sql.Tx) error {
		if _, err := loadJob(c, t, in.ProjectID, c.P.UserID, in.JobID); err != nil {
			return err
		}
		if _, err := t.ExecContext(c, `UPDATE ai_jobs SET status = 'cancelled', updated_at = ? WHERE id = ? AND status = 'open'`, now(), in.JobID); err != nil {
			return err
		}
		var err error
		out, err = loadJob(c, t, in.ProjectID, c.P.UserID, in.JobID)
		return err
	})
	return JobItem{Item: out}, mapErr(err)
}
