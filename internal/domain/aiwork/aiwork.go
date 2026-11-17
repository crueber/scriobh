// Package aiwork is the layer of the AI feature set between the core
// (internal/domain/ai: providers, policy, context, the run pipeline) and the
// screens. The recipes themselves are in internal/ai; what is here is what
// happens to their results.
//
//   - A finished run is handed to a hook (hook.go) that keeps what must outlive
//     the response, anchored against the document the model read: a suggestion
//     held for 30 minutes, a review's notes, the Lab's marks.
//   - Nothing reaches the manuscript except through an accept op (accept.go,
//     lab.go, summary.go, review.go). An accept applies a text the SERVER
//     produced and holds; it never takes text from the caller. It writes through
//     the document's own save seam in one transaction, as one revision with the
//     reason ai-accepted, and the writer's goal earns nothing for the words.
//   - Everything kept here is private to the person who ran the recipe and goes
//     when the project, the document or the person goes.
//
// See docs/OPEN-QUESTIONS.md, "Phase 4 recipes decisions".
package aiwork

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/ops"
)

// PendingTTL is how long a suggestion waits to be accepted.
const PendingTTL = 30 * time.Minute

// Service is the recipes' layer.
type Service struct {
	ai     *aicore.Service
	leases *docs.Leases
}

type dbq interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func now() string { return db.Now() }

func (s *Service) expiry() string { return db.Format(db.NowTime().Add(PendingTTL)) }

var (
	itemIDRe = regexp.MustCompile(`^[a-z][0-9]{1,3}$`)
	runIDRe  = regexp.MustCompile(`^air_[0-9A-HJKMNP-TV-Z]{26}$`)
)

// ---- guards ---------------------------------------------------------------------------------

// mayAccept is the rule every accept (and every read of what a model made)
// follows: the server's switch is on, the project's policy is on and allows the
// action, and an API key only where the owner allowed agents. Nothing here
// reaches a provider; it keeps "off" meaning off for the results too.
func (s *Service) mayAccept(ctx context.Context, d *db.DB, p ops.Principal, projectID, action string) error {
	if s.ai == nil {
		return ops.Conflict("AI is not available on this server.")
	}
	if err := s.ai.Gate(ctx, d); err != nil {
		return err
	}
	pv, err := s.ai.PolicyOf(ctx, d, projectID)
	if err != nil {
		return ops.Internal(err)
	}
	switch {
	case !pv.On():
		return refuse("policy_off", "AI is off for this project, so a suggestion cannot be accepted. The project's owner can turn it on.")
	case action != "" && !pv.Allows(action):
		return refuse("action_not_allowed", "This project's AI policy does not allow “"+action+"” actions.")
	case p.Via == "apikey" && !pv.AgentsMayUse:
		return refuse("agents_not_allowed", "This project's owner has not allowed agents (API keys) to use AI on it.")
	}
	return nil
}

func refuse(code, msg string) *ops.Error {
	return &ops.Error{Code: ops.CodeForbidden, Message: msg, Data: map[string]any{"ai_code": code}}
}

var errGone = &ops.Error{Code: ops.CodeNotFound, Message: "That suggestion is not available. Suggestions are kept for 30 minutes, and only for the person who ran the action; run it again."}

// ---- the pending store ----------------------------------------------------------------------

type pendingRow struct {
	RunID, ProjectID, UserID, DocumentID, Recipe, Kind string
	DocRevision                                        int
	OutputHash, ItemsJSON, ExpiresAt                   string
}

// loadPending reads a suggestion held for this person on this project. A missing,
// expired, foreign or other project's one is the same answer.
func (s *Service) loadPending(ctx context.Context, q dbq, runID, userID, projectID string) (pendingRow, error) {
	var r pendingRow
	var doc sql.NullString
	err := q.QueryRowContext(ctx, `SELECT run_id, project_id, user_id, document_id, recipe, kind, doc_revision, output_hash, items_json, expires_at
		FROM ai_pending WHERE run_id = ? AND user_id = ? AND project_id = ? AND expires_at > ?`, runID, userID, projectID, now()).
		Scan(&r.RunID, &r.ProjectID, &r.UserID, &doc, &r.Recipe, &r.Kind, &r.DocRevision, &r.OutputHash, &r.ItemsJSON, &r.ExpiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return r, errGone
	}
	r.DocumentID = doc.String
	if err != nil {
		return r, err
	}
	plain, oerr := s.openItems(r.RunID, r.UserID, r.ItemsJSON)
	if oerr != nil {
		return r, errGone
	}
	r.ItemsJSON = plain
	return r, nil
}

// sealedPrefix marks a held row whose text is sealed.
const sealedPrefix = "s1:"

// sealItems seals the held text with the server's secret key, bound to the row
// (the run and the person), when the server has one: a copy of the database, a
// backup, does not then carry a suggestion in the clear. Without a key the text
// is kept as it is (ai.status says credentials_ok false).
func (s *Service) sealItems(runID, userID, plain string) string {
	if s.ai == nil || !s.ai.Box().Enabled() {
		return plain
	}
	sealed, err := s.ai.Box().Seal([]byte(plain), "ai_pending/"+runID+"/"+userID)
	if err != nil {
		return plain
	}
	return sealedPrefix + sealed
}

func (s *Service) openItems(runID, userID, stored string) (string, error) {
	if !strings.HasPrefix(stored, sealedPrefix) {
		return stored, nil
	}
	if s.ai == nil || !s.ai.Box().Enabled() {
		return "", errGone
	}
	b, err := s.ai.Box().Open(strings.TrimPrefix(stored, sealedPrefix), "ai_pending/"+runID+"/"+userID)
	return string(b), err
}

func purgeExpired(ctx context.Context, q dbq) {
	_, _ = q.ExecContext(ctx, `DELETE FROM ai_pending WHERE expires_at <= ?`, now())
}

// verifyHash checks that the held answer is the one the run recorded: the hash
// the hook stored is the run's own output hash (the ledger of the core), so a row
// that was changed or planted would not match.
func verifyHash(ctx context.Context, q dbq, runID, userID, want string) error {
	var got string
	if err := q.QueryRowContext(ctx, `SELECT output_hash FROM ai_runs WHERE id = ? AND user_id = ? AND status IN ('ok','error')`, runID, userID).Scan(&got); err != nil || got == "" || got != want {
		return errGone
	}
	return nil
}

// ledger reads what became of one suggestion of a run. With a user id it reads
// only that person's: another person's run id must answer exactly as an unknown
// one does. (The empty user id is for the cleanup after the person's own accept.)
func ledger(ctx context.Context, q dbq, runID, itemID, userID string) (action string, revision int, revID string, found bool) {
	err := q.QueryRowContext(ctx, `SELECT action, revision, revision_id FROM ai_accepts WHERE run_id = ? AND item_id = ? AND (? = '' OR user_id = ?)`, runID, itemID, userID, userID).Scan(&action, &revision, &revID)
	return action, revision, revID, err == nil
}

func record(ctx context.Context, q dbq, runID, itemID, projectID, userID, action string, revision int, revID string) error {
	_, err := q.ExecContext(ctx, `INSERT INTO ai_accepts(run_id, item_id, project_id, user_id, action, revision, revision_id, created_at) VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(run_id, item_id) DO NOTHING`, runID, itemID, projectID, userID, action, revision, revID, now())
	return err
}

// dropPendingIfDone removes the held row once every suggestion of it has been
// accepted or rejected.
func dropPendingIfDone(ctx context.Context, q dbq, runID string, ids []string) {
	for _, id := range ids {
		if _, _, _, ok := ledger(ctx, q, runID, id, ""); !ok {
			return
		}
	}
	_, _ = q.ExecContext(ctx, `DELETE FROM ai_pending WHERE run_id = ?`, runID)
}

// ---- documents and anchors --------------------------------------------------------------------

type docState struct {
	ID, Title, Content, Body string
	Revision                 int
	Hash                     string
	Trashed                  bool
}

// loadDoc reads a document of the project; one of another project, or one that
// does not exist, is not found.
func loadDoc(ctx context.Context, q dbq, projectID, docID string) (docState, error) {
	var d docState
	var del sql.NullString
	err := q.QueryRowContext(ctx, `SELECT id, title, content, content_hash, revision, deleted_at FROM documents WHERE id = ? AND project_id = ?`, docID, projectID).
		Scan(&d.ID, &d.Title, &d.Content, &d.Hash, &d.Revision, &del)
	if errors.Is(err, sql.ErrNoRows) {
		return d, ops.NotFound("document")
	}
	if err != nil {
		return d, err
	}
	d.Trashed = del.Valid
	d.Body = alts.Split(alts.NormalizeEOL(d.Content)).Body
	return d, nil
}

// span is a range of a body: bytes, and the same in UTF-16 units.
type span = alts.Span

// locate finds a quote in a body without guessing. With a hint (offsets from the
// revision the quote was read at) the quote must be exactly there when the
// revision has not changed; otherwise, or when the hint no longer holds, the
// quote is looked up with its context and must be found once: an ambiguous or
// missing quote is not found.
func locate(body string, ix *alts.Index, quote, before, after string, hint *span, hintOK bool) (span, bool) {
	return locateIn(body, ix, quote, before, after, hint, hintOK, true)
}

// locateExact is locate for a range that may be edited: the quote must be found
// with ALL of the context stored with it (the words before and after, exactly),
// once. A quote that is still in the text once, with different words around it,
// is not the same place: a deletion must never land on a lookalike.
func locateExact(body string, ix *alts.Index, quote, before, after string, hint *span, hintOK bool) (span, bool) {
	return locateIn(body, ix, quote, before, after, hint, hintOK, false)
}

// maxOccurrences bounds the scan for a quote that repeats. A quote that repeats
// more often than that is not unique: the scan fails closed.
const maxOccurrences = alts.MaxOccurrences

func locateIn(body string, ix *alts.Index, quote, before, after string, hint *span, hintOK, uniqueOK bool) (span, bool) {
	return alts.LocateIn(body, ix, quote, before, after, hint, hintOK, uniqueOK)
}

// The paragraph markers and the context rule live in internal/alts, shared with the
// suggestions of people (internal/domain/review).
const (
	paraStart = alts.ParaStart
	paraEnd   = alts.ParaEnd
)

func paragraphContext(body string, a, b int) (before, after string) {
	return alts.ParagraphContext(body, a, b)
}

// hasHidden reports whether a source range holds what the model never saw or
// may never replace: a private note marker, a ghost or alternatives marker.
func hasHidden(src string) bool {
	return strings.Contains(src, "%%") || strings.Contains(src, "scriobh-ghost") || strings.Contains(src, "scriobh:alts")
}

// trimSpan narrows [a,b) of body to exclude white space at both ends.
func trimSpan(body string, a, b int) (int, int) {
	for a < b && (body[a] == ' ' || body[a] == '\t' || body[a] == '\n') {
		a++
	}
	for b > a && (body[b-1] == ' ' || body[b-1] == '\t' || body[b-1] == '\n') {
		b--
	}
	return a, b
}

func toJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "null"
	}
	return string(b)
}

func tx(ctx context.Context, d *db.DB, fn func(*sql.Tx) error) error { return d.WithTx(ctx, fn) }
