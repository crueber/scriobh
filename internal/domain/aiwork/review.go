package aiwork

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/ops"
)

// Reviews: the notes of a critique, the findings of a continuity audit and the
// sheets an extraction proposes. They are kept for the person who ran the
// recipe, private to them (another member, the owner and administrators
// included, gets the same "not found" as for an id that does not exist), until
// they delete the review. A note is worked through over time: resolved or
// dismissed one at a time. Its anchor is looked up again against the current
// text every time it is read, and a note whose quote cannot be found exactly
// once is listed as stale, never moved by a guess.

const maxReviews = 100 // kept per person and project; the oldest go first

// Anchor says where a note or mark is now.
type Anchor struct {
	State string `json:"state" enum:"placed,unplaced,stale" desc:"placed: the quote was found, Start and End are its place now. unplaced: the model's quote was not in the text when the note was made. stale: it was, and the text has changed so that it cannot be found exactly once."`
	Start int    `json:"start,omitempty" desc:"UTF-16 code units of the body at the document's current revision."`
	End   int    `json:"end,omitempty"`
}

// NoteOut is one note or finding.
type NoteOut struct {
	ID       string       `json:"id"`
	Position int          `json:"position"`
	Category string       `json:"category"`
	Severity string       `json:"severity" enum:"low,medium,high"`
	Note     string       `json:"note" desc:"One or two sentences. Untrusted: show it as text."`
	Example  string       `json:"example,omitempty" desc:"At most 160 characters; a critique never gives more prose than that."`
	Trimmed  bool         `json:"trimmed,omitempty" desc:"A limit shortened the note or its example."`
	Quote    string       `json:"quote,omitempty"`
	Anchor   Anchor       `json:"anchor"`
	Sources  []lib.Source `json:"sources,omitempty" desc:"Continuity: the facts the finding cites (entity id and field, timeline event, timeline finding, earlier chapter), all of which were in the request."`
	Status   string       `json:"status" enum:"open,accepted,dismissed"`
	Sheet    *SheetOut    `json:"sheet,omitempty" desc:"Extraction: the proposed entity."`
}

// SheetOut is a proposed entity.
type SheetOut struct {
	lib.Sheet
	ExistsAs *ExistingRef `json:"exists_as,omitempty" desc:"A readable entity already has this name or alias."`
	Created  string       `json:"created_entity_id,omitempty"`
}

// ExistingRef names an entity.
type ExistingRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// ReviewCounts counts a review's notes by status.
type ReviewCounts struct {
	Open      int `json:"open"`
	Accepted  int `json:"accepted"`
	Dismissed int `json:"dismissed"`
}

// ReviewOut is a review with its notes.
type ReviewOut struct {
	Kind            string       `json:"kind" enum:"review"`
	ID              string       `json:"review_id"`
	ReviewKind      string       `json:"review_kind" enum:"critique,continuity,extraction"`
	Recipe          string       `json:"recipe"`
	RunID           string       `json:"run_id"`
	DocumentID      string       `json:"document_id,omitempty"`
	DocRevision     int          `json:"doc_revision" desc:"The revision the model read."`
	CurrentRevision int          `json:"current_revision,omitempty"`
	CreatedAt       string       `json:"created_at"`
	Dropped         int          `json:"dropped" desc:"Items the parser left out: unreadable, over the limit, or citing a source that was not sent."`
	Counts          ReviewCounts `json:"counts"`
	Notes           []NoteOut    `json:"notes"`
}

type noteRow struct {
	id       string
	pos      int
	cat, sev string
	body, ex string
	trimmed  bool
	quote    string
	before   string
	after    string
	u1, u2   int
	rev      int
	sources  string
	sheet    string
	status   string
	ref      string
}

// reviewView reads a review the person owns, with its anchors looked up against
// the document now.
func reviewView(ctx context.Context, q dbq, projectID, userID, reviewID string, withDup func() map[string]ExistingRef) (ReviewOut, error) {
	var out ReviewOut
	var doc sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id, kind, recipe, run_id, document_id, doc_revision, dropped, created_at FROM ai_reviews WHERE id = ? AND user_id = ? AND project_id = ?`,
		reviewID, userID, projectID).Scan(&out.ID, &out.ReviewKind, &out.Recipe, &out.RunID, &doc, &out.DocRevision, &out.Dropped, &out.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return out, ops.NotFound("review")
	}
	if err != nil {
		return out, err
	}
	out.Kind, out.DocumentID, out.Notes = "review", doc.String, []NoteOut{}
	rows, err := q.QueryContext(ctx, `SELECT id, position, category, severity, body, example, trimmed, quote, before_ctx, after_ctx, start_u16, end_u16, at_revision, sources_json, sheet_json, status, result_ref
		FROM ai_notes WHERE review_id = ? ORDER BY position`, reviewID)
	if err != nil {
		return out, err
	}
	var nrs []noteRow
	for rows.Next() {
		var n noteRow
		var tr int
		if err := rows.Scan(&n.id, &n.pos, &n.cat, &n.sev, &n.body, &n.ex, &tr, &n.quote, &n.before, &n.after, &n.u1, &n.u2, &n.rev, &n.sources, &n.sheet, &n.status, &n.ref); err != nil {
			rows.Close()
			return out, err
		}
		n.trimmed = tr == 1
		nrs = append(nrs, n)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return out, err
	}
	if err := rows.Close(); err != nil {
		return out, err
	}
	var body string
	var ix *alts.Index
	if doc.Valid {
		if d, err := loadDoc(ctx, q, projectID, doc.String); err == nil {
			out.CurrentRevision, body = d.Revision, d.Body
			ix = alts.NewIndex(body)
		}
	}
	var dups map[string]ExistingRef
	for _, n := range nrs {
		no := NoteOut{ID: n.id, Position: n.pos, Category: n.cat, Severity: n.sev, Note: n.body, Example: n.ex, Trimmed: n.trimmed, Quote: n.quote, Status: n.status}
		switch n.status {
		case "open":
			out.Counts.Open++
		case "accepted":
			out.Counts.Accepted++
		default:
			out.Counts.Dismissed++
		}
		if n.sources != "" && n.sources != "[]" {
			_ = json.Unmarshal([]byte(n.sources), &no.Sources)
		}
		switch {
		case n.quote == "":
			no.Anchor = Anchor{State: "unplaced"}
		case n.u1 < 0 || ix == nil:
			no.Anchor = Anchor{State: "unplaced"}
		default:
			hint := &span{U1: n.u1, U2: n.u2}
			if sp, ok := locate(body, ix, n.quote, n.before, n.after, hint, n.rev == out.CurrentRevision); ok {
				no.Anchor = Anchor{State: "placed", Start: sp.U1, End: sp.U2}
			} else {
				no.Anchor = Anchor{State: "stale"}
			}
		}
		if n.sheet != "" {
			var sh lib.Sheet
			if json.Unmarshal([]byte(n.sheet), &sh) == nil {
				so := &SheetOut{Sheet: sh, Created: n.ref}
				if withDup != nil && n.status == "open" {
					if dups == nil {
						dups = withDup()
					}
					if ex, ok := dups[strings.ToLower(sh.Name)]; ok {
						so.ExistsAs = &ex
					}
				}
				no.Sheet = so
			}
		}
		out.Notes = append(out.Notes, no)
	}
	return out, nil
}

func (s *Service) reviewResult(ctx context.Context, d *db.DB, info aicore.ResultInfo, kind string) (aicore.ResultReply, error) {
	var nd lib.NotesData
	if len(info.Out.Data) == 0 || json.Unmarshal(info.Out.Data, &nd) != nil || info.DocumentID == "" {
		return none(), nil
	}
	ix := alts.NewIndex(info.Body)
	var view ReviewOut
	err := tx(ctx, d, func(t *sql.Tx) error {
		id := db.NewID("rev")
		if _, err := t.ExecContext(ctx, `INSERT INTO ai_reviews(id, project_id, user_id, document_id, run_id, recipe, kind, doc_revision, dropped, created_at) VALUES (?,?,?,?,?,?,?,?,?,?)`,
			id, info.ProjectID, info.UserID, info.DocumentID, info.RunID, info.Recipe.ID, kind, info.DocRevision, nd.Dropped, now()); err != nil {
			return err
		}
		for i, n := range nd.Notes {
			u1, u2 := -1, -1
			var before, after string
			if n.Quote != "" {
				if sp, ok := locate(info.Body, ix, n.Quote, n.Before, n.After, nil, false); ok && inSelection(info, sp) {
					u1, u2 = sp.U1, sp.U2
					before, after = alts.Contexts(info.Body, sp.A, sp.B)
				}
			}
			src, _ := json.Marshal(n.Sources)
			if _, err := t.ExecContext(ctx, `INSERT INTO ai_notes(id, review_id, position, category, severity, body, example, trimmed, quote, before_ctx, after_ctx, start_u16, end_u16, at_revision, sources_json)
				VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, db.NewID("not"), id, i+1, n.Category, n.Severity, n.Text, n.Example, b2i(n.Trimmed), n.Quote, before, after, u1, u2, info.DocRevision, string(src)); err != nil {
				return err
			}
		}
		pruneReviews(ctx, t, info.UserID, info.ProjectID)
		v, err := reviewView(ctx, t, info.ProjectID, info.UserID, id, nil)
		view = v
		return err
	})
	if err != nil {
		return aicore.ResultReply{}, err
	}
	return reply(view)
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

// inSelection says whether a located quote lies inside the run's selection (any
// quote does when the run had none).
func inSelection(info aicore.ResultInfo, sp span) bool {
	return info.Sel == nil || (sp.U1 >= info.Sel.Start && sp.U2 <= info.Sel.End)
}

func pruneReviews(ctx context.Context, q dbq, userID, projectID string) {
	_, _ = q.ExecContext(ctx, `DELETE FROM ai_reviews WHERE user_id = ? AND project_id = ? AND id NOT IN
		(SELECT id FROM ai_reviews WHERE user_id = ? AND project_id = ? ORDER BY created_at DESC, id DESC LIMIT ?)`, userID, projectID, userID, projectID, maxReviews)
}

// ---- extraction -----------------------------------------------------------------------------

func (s *Service) extractResult(ctx context.Context, d *db.DB, info aicore.ResultInfo) (aicore.ResultReply, error) {
	var sd lib.SheetsData
	if len(info.Out.Data) == 0 || json.Unmarshal(info.Out.Data, &sd) != nil || info.DocumentID == "" {
		return none(), nil
	}
	var view ReviewOut
	err := tx(ctx, d, func(t *sql.Tx) error {
		id := db.NewID("rev")
		if _, err := t.ExecContext(ctx, `INSERT INTO ai_reviews(id, project_id, user_id, document_id, run_id, recipe, kind, doc_revision, dropped, created_at) VALUES (?,?,?,?,?,?,'extraction',?,?,?)`,
			id, info.ProjectID, info.UserID, info.DocumentID, info.RunID, info.Recipe.ID, info.DocRevision, sd.Dropped, now()); err != nil {
			return err
		}
		for i, sh := range sd.Sheets {
			raw, _ := json.Marshal(sh)
			if _, err := t.ExecContext(ctx, `INSERT INTO ai_notes(id, review_id, position, category, severity, body, at_revision, sheet_json) VALUES (?,?,?,?,?,?,?,?)`,
				db.NewID("not"), id, i+1, sh.Kind, "medium", sh.Summary, info.DocRevision, string(raw)); err != nil {
				return err
			}
		}
		pruneReviews(ctx, t, info.UserID, info.ProjectID)
		v, err := reviewView(ctx, t, info.ProjectID, info.UserID, id, nil)
		view = v
		return err
	})
	if err != nil {
		return aicore.ResultReply{}, err
	}
	return reply(view)
}

// ---- ops ----------------------------------------------------------------------------------------

// ReviewListIn lists the caller's reviews.
type ReviewListIn struct {
	ProjectID  string `path:"id" json:"-" desc:"Project id."`
	DocumentID string `query:"document_id" desc:"Only reviews of this document."`
	Kind       string `query:"kind" enum:"critique,continuity,extraction"`
}

// ProjectRef lets the registry authorize by membership.
func (in ReviewListIn) ProjectRef() string { return in.ProjectID }

// ReviewBrief is a review in a list.
type ReviewBrief struct {
	ID          string       `json:"review_id"`
	ReviewKind  string       `json:"review_kind"`
	Recipe      string       `json:"recipe"`
	DocumentID  string       `json:"document_id,omitempty"`
	DocTitle    string       `json:"document_title,omitempty"`
	DocRevision int          `json:"doc_revision"`
	CreatedAt   string       `json:"created_at"`
	Counts      ReviewCounts `json:"counts"`
}

// ReviewListOut lists reviews, newest first.
type ReviewListOut struct {
	Items []ReviewBrief `json:"items"`
}

func (s *Service) reviewList(c *ops.Ctx, in ReviewListIn) (ReviewListOut, error) {
	if err := s.gateRead(c, in.ProjectID); err != nil {
		return ReviewListOut{}, err
	}
	q := `SELECT r.id, r.kind, r.recipe, COALESCE(r.document_id,''), COALESCE(d.title,''), r.doc_revision, r.created_at,
		COALESCE(SUM(n.status='open'),0), COALESCE(SUM(n.status='accepted'),0), COALESCE(SUM(n.status='dismissed'),0)
		FROM ai_reviews r LEFT JOIN documents d ON d.id = r.document_id LEFT JOIN ai_notes n ON n.review_id = r.id
		WHERE r.user_id = ? AND r.project_id = ?`
	args := []any{c.P.UserID, in.ProjectID}
	if in.DocumentID != "" {
		q += ` AND r.document_id = ?`
		args = append(args, in.DocumentID)
	}
	if in.Kind != "" {
		q += ` AND r.kind = ?`
		args = append(args, in.Kind)
	}
	q += ` GROUP BY r.id ORDER BY r.created_at DESC, r.id DESC LIMIT ?`
	args = append(args, maxReviews)
	rows, err := c.DB().Read().QueryContext(c, q, args...)
	if err != nil {
		return ReviewListOut{}, ops.Internal(err)
	}
	defer rows.Close()
	out := ReviewListOut{Items: []ReviewBrief{}}
	for rows.Next() {
		var b ReviewBrief
		if err := rows.Scan(&b.ID, &b.ReviewKind, &b.Recipe, &b.DocumentID, &b.DocTitle, &b.DocRevision, &b.CreatedAt, &b.Counts.Open, &b.Counts.Accepted, &b.Counts.Dismissed); err != nil {
			return out, ops.Internal(err)
		}
		out.Items = append(out.Items, b)
	}
	return out, nil
}

// ReviewGetIn names a review.
type ReviewGetIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	ReviewID  string `query:"review_id" desc:"A review id from ai.review.list or a run's data."`
}

// ProjectRef lets the registry authorize by membership.
func (in ReviewGetIn) ProjectRef() string { return in.ProjectID }

// Validate checks the id.
func (in *ReviewGetIn) Validate() error { return checkID("review_id", in.ReviewID, "rev_") }

// ReviewItem wraps a review.
type ReviewItem struct {
	Item ReviewOut `json:"item"`
}

func checkID(field, id, prefix string) error {
	if !strings.HasPrefix(id, prefix) || !db.ValidID(id) {
		return ops.Invalid(field, "That is not a valid id.")
	}
	return nil
}

func (s *Service) reviewGet(c *ops.Ctx, in ReviewGetIn) (ReviewItem, error) {
	if err := s.gateRead(c, in.ProjectID); err != nil {
		return ReviewItem{}, err
	}
	v, err := reviewView(c, c.DB().Read(), in.ProjectID, c.P.UserID, in.ReviewID, s.dupFinder(c, in.ProjectID))
	return ReviewItem{Item: v}, mapErr(err)
}

func mapErr(err error) error {
	if err == nil {
		return nil
	}
	var oe *ops.Error
	if errors.As(err, &oe) {
		return oe
	}
	return ops.Internal(err)
}

// gateRead is the rule for reading and settling kept results: the kill switch,
// and, for an API key, the owner's say that agents may use AI on the project
// (a key is not a person at the desk; what a model made is not for it to read
// unless the owner allowed agents). Reading one's own notes is not a send, so a
// project whose AI is off still shows what it kept, and the person can delete it.
func (s *Service) gateRead(c *ops.Ctx, projectID string) error {
	if s.ai == nil {
		return nil
	}
	if err := s.ai.Gate(c, c.DB()); err != nil {
		return err
	}
	return s.gateAgents(c, projectID)
}

// gateAgents is the agents rule alone: an API key reads what is kept only where
// the owner allowed agents to use AI. It does not look at the kill switch, for
// the parts that work with AI off (the Lab's local marks).
func (s *Service) gateAgents(c *ops.Ctx, projectID string) error {
	if s.ai == nil || c.P.Via != "apikey" {
		return nil
	}
	pv, err := s.ai.PolicyOf(c, c.DB(), projectID)
	if err != nil {
		return ops.Internal(err)
	}
	if !pv.AgentsMayUse {
		return refuse("agents_not_allowed", "This project's owner has not allowed agents (API keys) to use AI on it.")
	}
	return nil
}

// NoteActIn names a note.
type NoteActIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	NoteID    string `json:"note_id"`
}

// ProjectRef lets the registry authorize by membership.
func (in NoteActIn) ProjectRef() string { return in.ProjectID }

// Validate checks the id.
func (in *NoteActIn) Validate() error { return checkID("note_id", in.NoteID, "not_") }

// NoteActOut says what became of a note, and what is left of its review.
type NoteActOut struct {
	NoteID   string       `json:"note_id"`
	ReviewID string       `json:"review_id"`
	Status   string       `json:"status" enum:"open,accepted,dismissed"`
	Counts   ReviewCounts `json:"counts"`
}

func (s *Service) noteAct(status string) func(*ops.Ctx, NoteActIn) (NoteActOut, error) {
	return func(c *ops.Ctx, in NoteActIn) (NoteActOut, error) {
		if err := s.gateRead(c, in.ProjectID); err != nil {
			return NoteActOut{}, err
		}
		var out NoteActOut
		err := c.DB().WithTx(c, func(t *sql.Tx) error {
			var cur, kind string
			err := t.QueryRowContext(c, `SELECT n.review_id, n.status, r.kind FROM ai_notes n JOIN ai_reviews r ON r.id = n.review_id
				WHERE n.id = ? AND r.user_id = ? AND r.project_id = ?`, in.NoteID, c.P.UserID, in.ProjectID).Scan(&out.ReviewID, &cur, &kind)
			if errors.Is(err, sql.ErrNoRows) {
				return ops.NotFound("note")
			}
			if err != nil {
				return err
			}
			if kind == "extraction" && status == "accepted" {
				return ops.Invalid("note_id", "A proposed entity is accepted with ai.extract.accept: it names the Compendium it goes into.")
			}
			if cur != "open" && cur != status {
				return ops.Conflict("That note was already " + cur + ".")
			}
			if cur == "open" {
				if _, err := t.ExecContext(c, `UPDATE ai_notes SET status = ?, resolved_at = ? WHERE id = ?`, status, now(), in.NoteID); err != nil {
					return err
				}
			}
			out.NoteID, out.Status = in.NoteID, status
			return t.QueryRowContext(c, `SELECT COALESCE(SUM(status='open'),0), COALESCE(SUM(status='accepted'),0), COALESCE(SUM(status='dismissed'),0) FROM ai_notes WHERE review_id = ?`, out.ReviewID).
				Scan(&out.Counts.Open, &out.Counts.Accepted, &out.Counts.Dismissed)
		})
		return out, mapErr(err)
	}
}

// ReviewDeleteIn names a review.
type ReviewDeleteIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	ReviewID  string `query:"review_id"`
}

// ProjectRef lets the registry authorize by membership.
func (in ReviewDeleteIn) ProjectRef() string { return in.ProjectID }

// Validate checks the id.
func (in *ReviewDeleteIn) Validate() error { return checkID("review_id", in.ReviewID, "rev_") }

// DeletedOut says what was deleted.
type DeletedOut struct {
	Deleted int `json:"deleted"`
}

func (s *Service) reviewDelete(c *ops.Ctx, in ReviewDeleteIn) (DeletedOut, error) {
	if err := s.gateRead(c, in.ProjectID); err != nil {
		return DeletedOut{}, err
	}
	var res int64
	err := c.DB().WithTx(c, func(t *sql.Tx) error {
		r, err := t.ExecContext(c, `DELETE FROM ai_reviews WHERE id = ? AND user_id = ? AND project_id = ?`, in.ReviewID, c.P.UserID, in.ProjectID)
		if err != nil {
			return err
		}
		res, err = r.RowsAffected()
		return err
	})
	if err != nil {
		return DeletedOut{}, ops.Internal(err)
	}
	if res == 0 {
		return DeletedOut{}, ops.NotFound("review")
	}
	return DeletedOut{Deleted: int(res)}, nil
}

// dupFinder looks up, when asked, the entities of the project's scope that the
// caller may read, by lower-cased name and alias, for an extraction review.
func (s *Service) dupFinder(c *ops.Ctx, projectID string) func() map[string]ExistingRef {
	return func() map[string]ExistingRef {
		out := map[string]ExistingRef{}
		sheets, err := entities.ReadableSheets(c, c.DB(), c.P, projectID)
		if err != nil {
			return out
		}
		for _, sh := range sheets {
			out[strings.ToLower(sh.Name)] = ExistingRef{ID: sh.ID, Name: sh.Name}
			for _, a := range sh.Aliases {
				out[strings.ToLower(a)] = ExistingRef{ID: sh.ID, Name: sh.Name}
			}
		}
		return out
	}
}

// ExtractAcceptIn accepts a proposed entity.
type ExtractAcceptIn struct {
	ProjectID    string `path:"id" json:"-" desc:"Project id."`
	NoteID       string `json:"note_id" desc:"The note of an extraction review."`
	CompendiumID string `json:"compendium_id" desc:"The Compendium to create the entity in: the project's own or one it references, and one you may edit."`
}

// ProjectRef lets the registry authorize by membership.
func (in ExtractAcceptIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shape.
func (in *ExtractAcceptIn) Validate() error {
	if err := checkID("note_id", in.NoteID, "not_"); err != nil {
		return err
	}
	if in.CompendiumID == "" || !db.ValidID(in.CompendiumID) {
		return ops.Invalid("compendium_id", "Name the Compendium to create the entity in.")
	}
	return nil
}

// ExtractAcceptOut says what was created.
type ExtractAcceptOut struct {
	NoteID        string   `json:"note_id"`
	EntityID      string   `json:"entity_id"`
	CompendiumID  string   `json:"compendium_id"`
	Already       bool     `json:"already,omitempty" desc:"This sheet was accepted before; nothing was created again."`
	DroppedFields []string `json:"dropped_fields,omitempty" desc:"Proposed fields the Compendium does not define for this kind: they were left out."`
}

func (s *Service) extractAccept(c *ops.Ctx, in ExtractAcceptIn) (ExtractAcceptOut, error) {
	if err := s.mayAccept(c, c.DB(), c.P, in.ProjectID, lib.ActionExtraction); err != nil {
		return ExtractAcceptOut{}, err
	}
	// The Compendium must be one this project draws on, and one the caller may edit.
	var n int
	_ = c.DB().Read().QueryRowContext(c, `SELECT count(*) FROM (SELECT compendium_id AS id FROM projects WHERE id = ?1 AND compendium_id IS NOT NULL
		UNION SELECT target_compendium_id FROM compendium_refs WHERE owner_kind = 'project' AND owner_id = ?1) WHERE id = ?2`, in.ProjectID, in.CompendiumID).Scan(&n)
	role, ok := compaccess.Role(c, c.DB().Read(), c.P.UserID, in.CompendiumID)
	if n == 0 || !ok || !compaccess.AtLeast(role, compaccess.Editor) {
		return ExtractAcceptOut{}, ops.NotFound("compendium")
	}
	// a key pinned to a project reaches that project's primary Compendium only, as everywhere else
	if c.P.ProjectID != "" && (c.Env.ProjectPrimary == nil || c.Env.ProjectPrimary(c, c.P.ProjectID) != in.CompendiumID) {
		return ExtractAcceptOut{}, ops.NotFound("compendium")
	}
	if c.Env.CompendiumTrashed != nil && c.Env.CompendiumTrashed(c, in.CompendiumID) {
		return ExtractAcceptOut{}, ops.Conflict("This Compendium is in the trash. Restore it to change anything in it.")
	}
	var out ExtractAcceptOut
	err := c.DB().WithTx(c, func(t *sql.Tx) error {
		var status, sheet, ref string
		err := t.QueryRowContext(c, `SELECT n.status, n.sheet_json, n.result_ref FROM ai_notes n JOIN ai_reviews r ON r.id = n.review_id
			WHERE n.id = ? AND r.user_id = ? AND r.project_id = ? AND r.kind = 'extraction'`, in.NoteID, c.P.UserID, in.ProjectID).Scan(&status, &sheet, &ref)
		if errors.Is(err, sql.ErrNoRows) {
			return ops.NotFound("note")
		}
		if err != nil {
			return err
		}
		out.NoteID, out.CompendiumID = in.NoteID, in.CompendiumID
		switch status {
		case "accepted":
			out.EntityID, out.Already = ref, true
			return nil
		case "dismissed":
			return ops.Conflict("That sheet was dismissed.")
		}
		var sh lib.Sheet
		if json.Unmarshal([]byte(sheet), &sh) != nil || sh.Name == "" {
			return ops.Invalid("note_id", "That note holds no proposed entity.")
		}
		ci := entities.CreateIn{ID: in.CompendiumID, Kind: sh.Kind, Name: sh.Name, Summary: sh.Summary, Aliases: sh.Aliases}
		if err := ci.Validate(); err != nil {
			return err
		}
		withFields := ci
		if len(sh.Fields) > 0 {
			withFields.Fields = map[string]any{}
			for k, v := range sh.Fields {
				withFields.Fields[k] = v
			}
		}
		id, err := entities.Insert(c, t, withFields, now())
		var oe *ops.Error
		if err != nil && len(sh.Fields) > 0 && errors.As(err, &oe) && oe.Code == ops.CodeInvalid && strings.HasPrefix(oe.Field, "fields") {
			for k := range sh.Fields {
				out.DroppedFields = append(out.DroppedFields, k)
			}
			id, err = entities.Insert(c, t, ci, now())
		}
		if err != nil {
			return err // a name or alias an entity already has is a 409 that names it
		}
		out.EntityID = id
		if _, err := t.ExecContext(c, `UPDATE ai_notes SET status = 'accepted', result_ref = ?, resolved_at = ? WHERE id = ?`, id, now(), in.NoteID); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "entity.created", CompendiumID: in.CompendiumID, Payload: map[string]any{"entity_id": id, "kind": sh.Kind}})
		c.Audit(ops.AuditEntry{Action: "ai.extract.accept", TargetType: "entity", TargetID: id, Detail: map[string]any{"project_id": in.ProjectID, "compendium_id": in.CompendiumID}})
		return c.FlushTo(t)
	})
	return out, mapErr(err)
}

func (s *Service) extractDismiss(c *ops.Ctx, in NoteActIn) (NoteActOut, error) {
	return s.noteAct("dismissed")(c, in)
}

var _ = lib.ActionExtraction
