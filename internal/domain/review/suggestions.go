package review

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/comanchor"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Suggestions: a reviewer proposes a change to the text and an editor decides.
// The body of the document is never touched while a suggestion is open (the
// writing surface is sacred): a suggestion is a row beside the text, and
// accepting it is an edit made through the document's own save seam
// (docs.ApplyReviewEdits), in one transaction, as one revision with reason
// suggestion-accepted and no goal credit. Every check an accepted AI suggestion
// passes, a person's passes: the range may not touch a private note, ghosted
// text, an alternative or a marker, the replacement may not carry markers, HTML
// comments, control or direction-overriding characters or links the text did not
// have, and the structure of the text must survive.

// Limits.
const (
	MaxChangesPerSuggestion   = 50
	MaxSuggestionBytes        = 50000 // new text in one suggestion
	MaxSuggestionNote         = 2000
	MaxOpenSuggestionsPerProj = 2000
)

func registerSuggestions(r *ops.Registry, l *docs.Leases) {
	const base = "/api/v1/projects/{id}/suggestions"
	reviewer, editor := ops.RequireProject(ops.ProjectReviewer), ops.RequireProject(ops.ProjectEditor)
	ops.Register(r, ops.Op{Name: "suggestion.create", Method: "POST", Path: base, Status: 201, Auth: reviewer,
		Desc: "Propose a change to a document's text: up to 50 changes, each a range (offsets at base_revision, or a quote with the words around it) and the text to put there ('' deletes; where before or after puts the text beside the quote instead of replacing it). " +
			"Needs the reviewer role. base_revision must be the document's current revision (409 with current_revision otherwise). The same checks as accepting an AI suggestion run now, so a change that could never be applied is refused here: a range inside a private note, ghosted text or an alternative, text with markers, HTML comments, control or direction-overriding characters, or links the original did not have, or a change that would alter the document's structure. The text is not changed until an editor accepts."},
		func(c *ops.Ctx, in SuggestionCreateIn) (SuggestionItem, error) { return suggestCreate(c, in) })
	ops.Register(r, ops.Op{Name: "suggestion.list", Method: "GET", Path: base, Auth: reviewer,
		Desc: "List suggestions, newest first: all of the project's, or one document's. Filter by status (open, accepted, rejected, withdrawn, all) and author. stale says the document has changed since the suggestion was made (accepting then applies it only where its text is found again exactly once)."},
		func(c *ops.Ctx, in SuggestionListIn) (SuggestionListOut, error) { return suggestList(c, in) })
	ops.Register(r, ops.Op{Name: "suggestion.get", Method: "GET", Path: base + "/{suggestion_id}", Auth: reviewer,
		Desc: "One suggestion with its changes."},
		func(c *ops.Ctx, in SuggestionRef) (SuggestionItem, error) { return suggestGet(c, in) })
	ops.Register(r, ops.Op{Name: "suggestion.accept", Method: "POST", Path: base + "/{suggestion_id}/accept", Auth: editor,
		Desc: "Accept a suggestion: apply all of its changes to the document in one transaction, as one history entry with reason suggestion-accepted (restoring the entry before undoes it), with no word credit. Needs the editor role. " +
			"If the document has changed since the suggestion was made, each change applies only where its original text is found again with all the context it had, exactly once; if any change cannot be found, nothing is applied and the answer is 409 stale naming the changes (reject it, or ask for a new one). base_revision, when given, must be the document's current revision (409 with current_revision otherwise). The author is notified."},
		func(c *ops.Ctx, in SuggestionAcceptIn) (SuggestionAcceptOut, error) { return suggestAccept(c, l, in) })
	ops.Register(r, ops.Op{Name: "suggestion.reject", Method: "POST", Path: base + "/{suggestion_id}/reject", Auth: editor,
		Desc: "Reject an open suggestion. The text is not touched. The author is notified. Needs the editor role."},
		func(c *ops.Ctx, in SuggestionRef) (SuggestionItem, error) { return suggestDecide(c, in, "rejected") })
	ops.Register(r, ops.Op{Name: "suggestion.withdraw", Method: "POST", Path: base + "/{suggestion_id}/withdraw", Auth: reviewer,
		Desc: "Take back your own open suggestion."},
		func(c *ops.Ctx, in SuggestionRef) (SuggestionItem, error) { return suggestDecide(c, in, "withdrawn") })
	ops.Register(r, ops.Op{Name: "suggestion.accept_all", Method: "POST", Path: base + "/accept_all", Auth: editor,
		Desc: "Accept a chosen set of open suggestions of one document as one change: one transaction, one history entry. A suggestion applies whole or not at all. One whose text cannot be found again, or that overlaps a suggestion that comes before it (the one whose first change starts earlier wins, then the one made first), is skipped and listed. Send preview true to see exactly that plan and change nothing. Needs the editor role."},
		func(c *ops.Ctx, in SuggestionAcceptAllIn) (SuggestionAcceptAllOut, error) {
			return suggestAcceptAll(c, l, in)
		})
}

// ChangeIn is one proposed change.
type ChangeIn struct {
	Start      *int   `json:"start,omitempty" desc:"Offsets: start, in UTF-16 code units of the body at base_revision. start equal to end inserts."`
	End        *int   `json:"end,omitempty" desc:"Offsets: end (exclusive)."`
	Quote      string `json:"quote,omitempty" desc:"Instead of offsets: the Markdown source of the text this change replaces."`
	Before     string `json:"before,omitempty" desc:"With quote: some source before it, to tell repeats apart."`
	After      string `json:"after,omitempty" desc:"With quote: some source after it."`
	Occurrence *int   `json:"occurrence,omitempty" desc:"With quote: which occurrence, counting from 0, when context cannot tell."`
	Where      string `json:"where,omitempty" enum:"replace,before,after" desc:"With quote: replace (default) it with text, or insert text just before or after it and keep it."`
	Text       string `json:"text" desc:"The new text ('' deletes the range)."`
}

// ChangeOut is a change as kept.
type ChangeOut struct {
	Kind     string `json:"kind" enum:"replace,insert"`
	Start    int    `json:"start" desc:"UTF-16 code units of the body at base_revision."`
	End      int    `json:"end"`
	Original string `json:"original,omitempty" desc:"The text being replaced."`
	Text     string `json:"text" desc:"The new text."`
	// With plain=true on suggestion.list for one document, for an open suggestion made on the text as it still is:
	// the range as a reading view shows it, so a page that does not parse Markdown can mark it.
	OriginalPlain string `json:"original_plain,omitempty" desc:"With plain=true: the replaced text without its formatting marks, as rendered (empty for an insertion)."`
	BeforePlain   string `json:"before_plain,omitempty" desc:"With plain=true: up to 24 characters of rendered text before the range (private notes and ghosted text never included)."`
	AfterPlain    string `json:"after_plain,omitempty" desc:"With plain=true: up to 24 characters after it."`
}

// stored is a change as it sits in ops_json.
type stored struct {
	Kind     string `json:"kind"`
	U1       int    `json:"u1"`
	U2       int    `json:"u2"`
	Original string `json:"original,omitempty"`
	Before   string `json:"before,omitempty"`
	After    string `json:"after,omitempty"`
	Text     string `json:"text"`
}

// SuggestionOut is one suggestion.
type SuggestionOut struct {
	ID               string      `json:"id"`
	DocumentID       string      `json:"document_id"`
	Author           AuthorOut   `json:"author"`
	Status           string      `json:"status" enum:"open,accepted,rejected,withdrawn"`
	BaseRevision     int         `json:"base_revision"`
	DocumentRevision int         `json:"document_revision" desc:"The document's revision now."`
	Stale            bool        `json:"stale" desc:"The document has changed since the suggestion was made."`
	Note             string      `json:"note,omitempty"`
	Changes          []ChangeOut `json:"changes"`
	CreatedAt        string      `json:"created_at"`
	DecidedAt        string      `json:"decided_at,omitempty"`
	DecidedBy        string      `json:"decided_by,omitempty" desc:"Name of who decided."`
	AppliedRevision  int         `json:"applied_revision,omitempty" desc:"The document revision the acceptance made."`
	RevisionID       string      `json:"revision_id,omitempty"`
	CanWithdraw      bool        `json:"can_withdraw" desc:"For you now: yours and open."`
	CanDecide        bool        `json:"can_decide" desc:"For you now: you are an editor or the owner and it is open."`
}

// SuggestionItem wraps one.
type SuggestionItem struct {
	Item SuggestionOut `json:"item"`
}

// SuggestionCreateIn makes a suggestion.
type SuggestionCreateIn struct {
	ProjectID    string     `path:"id" json:"-" desc:"Project id."`
	DocumentID   string     `json:"document_id" desc:"The document."`
	BaseRevision int        `json:"base_revision" desc:"The document's revision you read; it must be current."`
	Changes      []ChangeIn `json:"changes" desc:"One to fifty changes, in any order; they may not overlap."`
	Note         string     `json:"note,omitempty" desc:"Why, up to 2,000 characters."`
	Plain        bool       `json:"plain,omitempty" desc:"Every change's quote, before and after are the text as a reader sees it rendered (a selection in the reading view), not Markdown source: looked for in the visible text only (never front matter, private notes, ghosted text, link targets or footnotes) and placed in the source, formatting inside it included. Offsets are not allowed with it. text is still the Markdown to put there."`
}

// ProjectRef lets the registry authorize by membership.
func (in SuggestionCreateIn) ProjectRef() string { return in.ProjectID }

// Validate checks the shape.
func (in *SuggestionCreateIn) Validate() error {
	if in.DocumentID == "" {
		return ops.Invalid("document_id", "Name the document.")
	}
	if in.BaseRevision < 1 {
		return ops.Invalid("base_revision", "Send the revision you read.")
	}
	if len(in.Changes) == 0 || len(in.Changes) > MaxChangesPerSuggestion {
		return ops.Invalid("changes", fmt.Sprintf("Send between 1 and %d changes.", MaxChangesPerSuggestion))
	}
	if utf8.RuneCountInString(in.Note) > MaxSuggestionNote {
		return ops.Invalid("note", "A note is limited to 2,000 characters.")
	}
	return nil
}

type docHead struct {
	Content, Title string
	Revision       int
	Deleted        bool
}

func loadDocHead(c *ops.Ctx, tx *sql.Tx, projectID, docID string) (docHead, error) {
	var h docHead
	var del sql.NullString
	err := tx.QueryRowContext(c, `SELECT content, title, revision, deleted_at FROM documents WHERE id = ? AND project_id = ?`, docID, projectID).Scan(&h.Content, &h.Title, &h.Revision, &del)
	if errors.Is(err, sql.ErrNoRows) {
		return h, ops.NotFound("document")
	}
	h.Deleted = del.Valid
	return h, err
}

// Propose is suggestion.create for a caller that has already authorized the person (the op that turns a difference
// between two editions of the book into suggestions). It runs the same checks, the same caps and the same refusals, in
// its own transaction.
func Propose(c *ops.Ctx, in SuggestionCreateIn) (SuggestionItem, error) {
	if err := in.Validate(); err != nil {
		return SuggestionItem{}, err
	}
	return suggestCreate(c, in)
}

// ProposeQuiet is Propose without the notification of the people who decide, for a caller that makes several
// suggestions in one act and tells them once (NotifyProposed).
func ProposeQuiet(c *ops.Ctx, in SuggestionCreateIn) (SuggestionItem, error) {
	if err := in.Validate(); err != nil {
		return SuggestionItem{}, err
	}
	return suggestCreateOpt(c, in, false)
}

// NotifyProposed tells the project's owner and editors, once, of suggestions made quietly: one notification each,
// naming the first of them. The person who made them is not told.
func NotifyProposed(c *ops.Ctx, projectID, documentID, suggestionID string) error {
	return c.DB().WithTx(c, func(tx *sql.Tx) error {
		rows, err := tx.QueryContext(c, `SELECT user_id FROM project_members WHERE project_id = ? AND role IN ('owner','editor')`, projectID)
		if err != nil {
			return err
		}
		var who []string
		for rows.Next() {
			var u string
			if err := rows.Scan(&u); err != nil {
				rows.Close()
				return err
			}
			who = append(who, u)
		}
		rows.Close()
		now := db.Now()
		for _, u := range who {
			if err := notify(tx, u, projectID, "suggestion", "", suggestionID, documentID, c.P.UserID, now); err != nil {
				return err
			}
		}
		return nil
	})
}

func suggestCreate(c *ops.Ctx, in SuggestionCreateIn) (SuggestionItem, error) {
	return suggestCreateOpt(c, in, true)
}

func suggestCreateOpt(c *ops.Ctx, in SuggestionCreateIn, tell bool) (SuggestionItem, error) {
	note := strings.TrimSpace(in.Note)
	var id string
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		h, err := loadDocHead(c, tx, in.ProjectID, in.DocumentID)
		if err != nil {
			return err
		}
		if h.Deleted {
			return ops.Conflict("This document is in the trash. Restore it to suggest changes.")
		}
		if h.Revision != in.BaseRevision {
			return ops.ConflictWith("This document changed since you read it.", map[string]any{"reason": "revision", "current_revision": h.Revision})
		}
		var open int
		if err := tx.QueryRowContext(c, `SELECT count(*) FROM suggestions WHERE project_id = ? AND status = 'open'`, in.ProjectID).Scan(&open); err != nil {
			return err
		}
		if open >= MaxOpenSuggestionsPerProj {
			return ops.Conflict("This project holds the most open suggestions it can; decide some first.")
		}
		chs, err := buildChanges(c, tx, in.ProjectID, in.DocumentID, h, in.Changes, in.Plain)
		if err != nil {
			return err
		}
		raw, _ := json.Marshal(chs)
		now := db.Now()
		id = db.NewID("sug")
		if _, err := tx.ExecContext(c, `INSERT INTO suggestions(id, project_id, document_id, author_id, status, base_revision, ops_json, note, created_at, updated_at) VALUES (?,?,?,?, 'open', ?,?,?,?,?)`,
			id, in.ProjectID, in.DocumentID, c.P.UserID, in.BaseRevision, string(raw), note, now, now); err != nil {
			return err
		}
		if tell {
			// the people who decide: the owner and the editors
			rows, err := tx.QueryContext(c, `SELECT user_id FROM project_members WHERE project_id = ? AND role IN ('owner','editor')`, in.ProjectID)
			if err != nil {
				return err
			}
			var who []string
			for rows.Next() {
				var u string
				if err := rows.Scan(&u); err != nil {
					rows.Close()
					return err
				}
				who = append(who, u)
			}
			if err := rows.Err(); err != nil {
				rows.Close()
				return err
			}
			rows.Close()
			for _, u := range who {
				if err := notify(tx, u, in.ProjectID, "suggestion", "", id, in.DocumentID, c.P.UserID, now); err != nil {
					return err
				}
			}
		}
		c.Emit(ops.Event{Type: "suggestion.created", ProjectID: in.ProjectID, DocumentID: in.DocumentID, Payload: map[string]any{"suggestion_id": id}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return SuggestionItem{}, err
	}
	return suggestionByID(c, c.DB().Read(), in.ProjectID, id)
}

// buildChanges resolves the changes against the document's text now, and runs
// every check an accepted suggestion passes, so a change that could never be
// applied is refused here.
func buildChanges(c *ops.Ctx, tx *sql.Tx, projectID, docID string, h docHead, changes []ChangeIn, plain bool) ([]stored, error) {
	body := comanchor.Body(h.Content)
	ix := alts.NewIndex(body)
	var chs []stored
	var edits []docs.AIEdit
	total := 0
	for i, ch := range changes {
		a, b, kind, err := resolveChange(body, ix, ch, plain)
		if err != nil {
			return nil, withIndex(err, i)
		}
		orig := body[a:b]
		if orig == ch.Text && ch.Where != "before" && ch.Where != "after" {
			return nil, ops.Invalid("changes", fmt.Sprintf("Change %d replaces text with itself.", i+1))
		}
		if kind == "insert" && ch.Text == "" {
			return nil, ops.Invalid("changes", fmt.Sprintf("Change %d inserts nothing.", i+1))
		}
		total += len(ch.Text)
		s := stored{Kind: kind, U1: ix.Unit(a), U2: ix.Unit(b), Original: orig, Text: ch.Text}
		if kind == "insert" {
			s.Before, s.After = alts.Contexts(body, a, a)
		} else {
			s.Before, s.After = alts.ParagraphContext(body, a, b)
		}
		chs = append(chs, s)
		edits = append(edits, docs.AIEdit{Start: s.U1, End: s.U2, Expect: orig, New: ch.Text})
	}
	if total > MaxSuggestionBytes {
		return nil, ops.Invalid("changes", "A suggestion holds at most 50,000 bytes of new text.")
	}
	if err := docs.CheckReviewEdits(c, tx, projectID, docID, h.Revision, edits); err != nil {
		return nil, err
	}
	return chs, nil
}

func withIndex(err error, i int) error {
	var e *ops.Error
	if errors.As(err, &e) && e.Code == ops.CodeInvalid {
		return ops.Invalid("changes", fmt.Sprintf("Change %d: %s", i+1, e.Message))
	}
	return err
}

// resolveChange turns a ChangeIn into a byte range of the body and the kind.
func resolveChange(body string, ix *alts.Index, ch ChangeIn, plain bool) (a, b int, kind string, err error) {
	if ch.Start != nil || ch.End != nil {
		if plain {
			return 0, 0, "", ops.Invalid("changes", "With plain, name the text by a quote, not by offsets.")
		}
		if ch.Start == nil || ch.End == nil {
			return 0, 0, "", ops.Invalid("changes", "Send both start and end, or a quote.")
		}
		var ok1, ok2 bool
		a, ok1 = ix.Byte(*ch.Start)
		b, ok2 = ix.Byte(*ch.End)
		if !ok1 || !ok2 || b < a || b > len(body) {
			return 0, 0, "", ops.Invalid("changes", "That range is outside the text or cuts a character in two (offsets are UTF-16 code units of the body).")
		}
		kind = "replace"
		if a == b {
			kind = "insert"
		}
		return a, b, kind, nil
	}
	var e error
	if plain {
		a, b, e = comanchor.ResolvePlain(body, comanchor.Range{Quote: ch.Quote, Before: ch.Before, After: ch.After, Occurrence: ch.Occurrence, Plain: true})
	} else {
		a, b, e = alts.ResolveQuote(body, ch.Quote, ch.Before, ch.After, ch.Occurrence)
	}
	if e != nil {
		return 0, 0, "", ops.Invalid("changes", e.Error())
	}
	switch ch.Where {
	case "", "replace":
		if plain {
			na, nb, ok := fitMarkup(body, a, b)
			if !ok {
				return 0, 0, "", ops.Invalid("changes", "That selection starts or ends inside formatting (italics, bold or a link). Select the whole formatted words, or the words around them.")
			}
			a, b = na, nb
			if err := visibleOnly(body, a, b); err != nil {
				return 0, 0, "", err
			}
		}
		return a, b, "replace", nil
	case "before":
		return a, a, "insert", nil
	case "after":
		return b, b, "insert", nil
	}
	return 0, 0, "", ops.Invalid("changes", "where is replace, before or after.")
}

var reInlineLink = regexp.MustCompile(`\[[^\]\n]*\]\([^)\n]*\)`)

// visibleOnly refuses a plain range whose source holds something the reader never saw: a private note, ghosted text, an HTML
// comment (an owner's aside), or a link address that the range covers only in part. Accepting such a suggestion would delete
// what nobody saw proposed to be deleted. The words the reviewer selected are the words that go.
func visibleOnly(body string, a, b int) error {
	if text.TouchesSpans(comanchor.Hidden(body), a, b) {
		return ops.Invalid("changes", "That selection has text in it that the page does not show (a private note, ghosted text or a comment in the source). Select the words on either side of it instead.")
	}
	for _, m := range reInlineLink.FindAllStringIndex(body, -1) {
		ls, le := m[0], m[1]
		if b <= ls || a >= le {
			continue
		}
		closeBr := strings.Index(body[ls:le], "](") + ls
		if a >= ls+1 && b <= closeBr || a <= ls && b >= le { // inside the link's words, or the whole link
			continue
		}
		return ops.Invalid("changes", "That selection covers only part of a link, including its address. Select inside the link's words, or the whole link and the words around it.")
	}
	return nil
}

// fitMarkup makes the source range of a selection one that can be replaced without leaving half of a pair of
// formatting marks behind. A plain selection runs from its first visible byte to its last, so "quiet house" in
// "*quiet* house" is "quiet* house": the range is widened over the marks right outside it when that makes its text
// the same as what was selected, and refused when no choice does.
func fitMarkup(body string, a, b int) (int, int, bool) {
	want := strings.Join(strings.Fields(text.Plain(body[a:b])), " ")
	vis := ""
	if bl, ok := text.PlainBlocks(body); ok {
		for _, p := range bl {
			if p.Start <= a && b <= p.End {
				if i, j, ok := p.Visible(a, b); ok {
					vis = p.Text[i:j]
				}
				break
			}
		}
	}
	if vis == "" || want == vis {
		return a, b, true
	}
	isMark := func(c byte) bool { return c == '*' || c == '_' || c == '~' || c == '`' }
	la, rb := a, b
	for la > 0 && isMark(body[la-1]) {
		la--
	}
	for rb < len(body) && isMark(body[rb]) {
		rb++
	}
	for _, r := range [][2]int{{la, b}, {a, rb}, {la, rb}} {
		if strings.Join(strings.Fields(text.Plain(body[r[0]:r[1]])), " ") == vis {
			return r[0], r[1], true
		}
	}
	return 0, 0, false
}

// ---- reading ----------------------------------------------------------------------------------

type sugRow struct {
	ID, ProjectID, DocumentID, Status, OpsJSON, Note, CreatedAt string
	AuthorID, DecidedBy, DecidedAt, RevisionID                  sql.NullString
	AuthorName                                                  string
	AuthorLabel                                                 string
	AuthorGone                                                  bool
	DeciderName                                                 string
	Base, DocRev                                                int
	Applied                                                     sql.NullInt64
}

const sugCols = `s.id, s.project_id, s.document_id, s.status, s.ops_json, s.note, s.created_at, s.author_id, s.decided_by, s.decided_at, s.revision_id,
	COALESCE(NULLIF(u.display_name,''), 'Member'), COALESCE(u.deleted_at IS NOT NULL, 0), COALESCE(NULLIF(du.display_name,''), 'Member'), s.base_revision, d.revision, s.applied_revision, s.author_label`

const sugFrom = ` FROM suggestions s JOIN documents d ON d.id = s.document_id LEFT JOIN users u ON u.id = s.author_id LEFT JOIN users du ON du.id = s.decided_by`

func scanSug(sc interface{ Scan(...any) error }) (sugRow, error) {
	var r sugRow
	err := sc.Scan(&r.ID, &r.ProjectID, &r.DocumentID, &r.Status, &r.OpsJSON, &r.Note, &r.CreatedAt, &r.AuthorID, &r.DecidedBy, &r.DecidedAt, &r.RevisionID,
		&r.AuthorName, &r.AuthorGone, &r.DeciderName, &r.Base, &r.DocRev, &r.Applied, &r.AuthorLabel)
	return r, err
}

func sugOut(r sugRow, v viewer) SuggestionOut {
	o := SuggestionOut{ID: r.ID, DocumentID: r.DocumentID, Status: r.Status, BaseRevision: r.Base, DocumentRevision: r.DocRev, Stale: r.Status == "open" && r.Base != r.DocRev,
		Note: r.Note, Changes: []ChangeOut{}, CreatedAt: r.CreatedAt, DecidedAt: r.DecidedAt.String, RevisionID: r.RevisionID.String}
	if r.AuthorID.Valid && !r.AuthorGone {
		o.Author = AuthorOut{Kind: "user", UserID: r.AuthorID.String, Name: r.AuthorName}
	} else if !r.AuthorID.Valid && r.AuthorLabel != "" {
		o.Author = AuthorOut{Kind: "label", Name: r.AuthorLabel}
	} else {
		o.Author = AuthorOut{Kind: "removed", Name: "Removed user"}
	}
	if r.DecidedBy.Valid {
		o.DecidedBy = r.DeciderName
	}
	if r.Applied.Valid {
		o.AppliedRevision = int(r.Applied.Int64)
	}
	var chs []stored
	_ = json.Unmarshal([]byte(r.OpsJSON), &chs)
	for _, s := range chs {
		o.Changes = append(o.Changes, ChangeOut{Kind: s.Kind, Start: s.U1, End: s.U2, Original: s.Original, Text: s.Text})
	}
	open := r.Status == "open"
	o.CanWithdraw = open && v.UserID != "" && r.AuthorID.Valid && r.AuthorID.String == v.UserID
	o.CanDecide = open && atLeast(v.Role, ops.ProjectEditor)
	return o
}

func suggestionByID(c *ops.Ctx, q querier, projectID, id string) (SuggestionItem, error) {
	row := q.QueryRowContext(c, `SELECT `+sugCols+sugFrom+` WHERE s.id = ? AND s.project_id = ?`, id, projectID)
	r, err := scanSug(row)
	if errors.Is(err, sql.ErrNoRows) {
		return SuggestionItem{}, ops.NotFound("suggestion")
	}
	if err != nil {
		return SuggestionItem{}, err
	}
	return SuggestionItem{Item: sugOut(r, viewerOf(c, projectID))}, nil
}

// SuggestionRef names a suggestion.
type SuggestionRef struct {
	ProjectID    string `path:"id" json:"-" desc:"Project id."`
	SuggestionID string `path:"suggestion_id" json:"-" desc:"Suggestion id."`
}

// ProjectRef lets the registry authorize by membership.
func (in SuggestionRef) ProjectRef() string { return in.ProjectID }

func suggestGet(c *ops.Ctx, in SuggestionRef) (SuggestionItem, error) {
	return suggestionByID(c, c.DB().Read(), in.ProjectID, in.SuggestionID)
}

// SuggestionListIn lists suggestions.
type SuggestionListIn struct {
	ProjectID  string `path:"id" json:"-" desc:"Project id."`
	DocumentID string `query:"document_id"`
	Status     string `query:"status" enum:"open,accepted,rejected,withdrawn,all" desc:"Default open."`
	Author     string `query:"author" desc:"A user id, or me."`
	Limit      int    `query:"limit" desc:"Per page (default 50, at most 200)."`
	Offset     int    `query:"offset"`
	Plain      bool   `query:"plain" desc:"With document_id: also give each change of an open suggestion that was made on the text as it still is as the reader sees it rendered (original_plain, before_plain, after_plain), for a reading view that does not parse Markdown."`
}

// ProjectRef lets the registry authorize by membership.
func (in SuggestionListIn) ProjectRef() string { return in.ProjectID }

// SuggestionListOut is a page.
type SuggestionListOut struct {
	Items []SuggestionOut `json:"items"`
	Total int             `json:"total"`
}

func suggestList(c *ops.Ctx, in SuggestionListIn) (SuggestionListOut, error) {
	where, args := `WHERE s.project_id = ? AND d.deleted_at IS NULL`, []any{in.ProjectID}
	if in.DocumentID != "" {
		where += ` AND s.document_id = ?`
		args = append(args, in.DocumentID)
	}
	switch in.Status {
	case "", "open":
		where += ` AND s.status = 'open'`
	case "all":
	case "accepted", "rejected", "withdrawn":
		where += ` AND s.status = ?`
		args = append(args, in.Status)
	default:
		return SuggestionListOut{}, ops.Invalid("status", "Use open, accepted, rejected, withdrawn or all.")
	}
	if in.Author != "" {
		a := in.Author
		if a == "me" {
			a = c.P.UserID
		}
		where += ` AND s.author_id = ?`
		args = append(args, a)
	}
	limit, offset := pageOf(in.Limit, in.Offset)
	out := SuggestionListOut{Items: []SuggestionOut{}}
	q := c.DB().Read()
	if err := q.QueryRowContext(c, `SELECT count(*)`+sugFrom+` `+where, args...).Scan(&out.Total); err != nil {
		return out, err
	}
	rows, err := q.QueryContext(c, `SELECT `+sugCols+sugFrom+` `+where+` ORDER BY s.created_at DESC, s.id DESC LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	v := viewerOf(c, in.ProjectID)
	for rows.Next() {
		r, err := scanSug(rows)
		if err != nil {
			return out, err
		}
		out.Items = append(out.Items, sugOut(r, v))
	}
	if err := rows.Err(); err != nil {
		return out, err
	}
	if in.Plain && in.DocumentID != "" {
		var content string
		if err := q.QueryRowContext(c, `SELECT content FROM documents WHERE id = ? AND project_id = ?`, in.DocumentID, in.ProjectID).Scan(&content); err == nil {
			addPlainChanges(out.Items, content)
		}
	}
	return out, nil
}

// addPlainChanges fills the plain forms of the changes of the open suggestions of one document that were made on
// the text as it is now (offsets mean what they meant: the quote is still exactly there).
func addPlainChanges(items []SuggestionOut, content string) {
	body := comanchor.Body(content)
	hinter := comanchor.NewHinter(body)
	ix := alts.NewIndex(body)
	for i := range items {
		if items[i].Status != "open" || items[i].Stale {
			continue
		}
		for j := range items[i].Changes {
			ch := &items[i].Changes[j]
			a, ok1 := ix.Byte(ch.Start)
			b, ok2 := ix.Byte(ch.End)
			if !ok1 || !ok2 || a > b || b > len(body) || body[a:b] != ch.Original {
				continue
			}
			p := hinter.For(a, b)
			ch.OriginalPlain, ch.BeforePlain, ch.AfterPlain = p.Quote, p.Before, p.After
		}
	}
}

// ---- deciding ---------------------------------------------------------------------------------

func suggestDecide(c *ops.Ctx, in SuggestionRef, to string) (SuggestionItem, error) {
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(c, `SELECT `+sugCols+sugFrom+` WHERE s.id = ? AND s.project_id = ?`, in.SuggestionID, in.ProjectID)
		r, err := scanSug(row)
		if errors.Is(err, sql.ErrNoRows) {
			return ops.NotFound("suggestion")
		}
		if err != nil {
			return err
		}
		if to == "withdrawn" && (!r.AuthorID.Valid || r.AuthorID.String != c.P.UserID) {
			return ops.Forbidden("only the author can withdraw a suggestion")
		}
		if r.Status != "open" {
			return ops.ConflictWith("This suggestion was already decided ("+r.Status+").", map[string]any{"reason": "decided", "status": r.Status})
		}
		now := db.Now()
		if _, err := tx.ExecContext(c, `UPDATE suggestions SET status = ?, decided_by = ?, decided_at = ?, updated_at = ? WHERE id = ?`, to, c.P.UserID, now, now, r.ID); err != nil {
			return err
		}
		if to == "rejected" && r.AuthorID.Valid {
			if err := notify(tx, r.AuthorID.String, in.ProjectID, "suggestion-decided", "", r.ID, r.DocumentID, c.P.UserID, now); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(c, `DELETE FROM notifications WHERE suggestion_id = ? AND kind = 'suggestion'`, r.ID); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "suggestion." + to, ProjectID: in.ProjectID, DocumentID: r.DocumentID, Payload: map[string]any{"suggestion_id": r.ID}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return SuggestionItem{}, err
	}
	return suggestionByID(c, c.DB().Read(), in.ProjectID, in.SuggestionID)
}

// SuggestionAcceptIn accepts one suggestion.
type SuggestionAcceptIn struct {
	ProjectID    string `path:"id" json:"-" desc:"Project id."`
	SuggestionID string `path:"suggestion_id" json:"-" desc:"Suggestion id."`
	BaseRevision int    `json:"base_revision,omitempty" desc:"The document's current revision as you hold it; 409 with current_revision if it is not. Omit to apply against whatever the document is now."`
	TabID        string `json:"tab_id,omitempty" desc:"Browser tab id; enables the single-writer lease."`
}

// ProjectRef lets the registry authorize by membership.
func (in SuggestionAcceptIn) ProjectRef() string { return in.ProjectID }

// SuggestionAcceptOut is the decision and what it wrote.
type SuggestionAcceptOut struct {
	Item        SuggestionOut `json:"item"`
	Revision    int           `json:"revision" desc:"The document's revision after the write."`
	RevisionID  string        `json:"revision_id,omitempty" desc:"The history entry (reason suggestion-accepted)."`
	ContentHash string        `json:"content_hash,omitempty"`
	WordCount   int           `json:"word_count,omitempty"`
	Comment     string        `json:"comment,omitempty" desc:"The alternatives comment that now ends the text, as alternative ops return it."`
}

// planned is a suggestion with each change placed in the document as it is now.
type planned struct {
	r     sugRow
	chs   []stored
	edits []docs.AIEdit
	first int // start of the first change, UTF-16
}

// place finds each change of a suggestion in the text as it stands. At the
// revision the suggestion was made on, the stored range is the place. Otherwise
// the original text must be found again with ALL the context stored with it,
// exactly once (an insertion: its context either side exactly once); a quote that
// is still in the text once, with other words around it, is not the same place.
func place(r sugRow, body string, ix *alts.Index, docRev int) (planned, []int) {
	var chs []stored
	_ = json.Unmarshal([]byte(r.OpsJSON), &chs)
	p := planned{r: r, chs: chs, first: -1}
	var bad []int
	for i, ch := range chs {
		e := docs.AIEdit{Expect: ch.Original, New: ch.Text}
		if r.Base == docRev {
			e.Start, e.End = ch.U1, ch.U2
		} else if ch.Kind == "insert" {
			at, ok := locateInsert(body, ch.Before, ch.After)
			if !ok {
				bad = append(bad, i)
				continue
			}
			e.Start, e.End = ix.Unit(at), ix.Unit(at)
		} else {
			sp, ok := alts.LocateIn(body, ix, ch.Original, ch.Before, ch.After, &alts.Span{U1: ch.U1, U2: ch.U2}, false, false)
			if !ok {
				bad = append(bad, i)
				continue
			}
			e.Start, e.End = sp.U1, sp.U2
		}
		p.edits = append(p.edits, e)
		if p.first < 0 || e.Start < p.first {
			p.first = e.Start
		}
	}
	return p, bad
}

// locateInsert finds the one place where the words stored before and after an
// insertion point meet. Little context is no answer: fewer than 12 characters
// either side together do not say which of two like places this was.
func locateInsert(body, before, after string) (int, bool) {
	needle := before + after
	if utf8.RuneCountInString(needle) < 12 {
		return 0, false
	}
	i := strings.Index(body, needle)
	if i < 0 || strings.Contains(body[i+1:], needle) {
		return 0, false
	}
	return i + len(before), true
}

func suggestAccept(c *ops.Ctx, l *docs.Leases, in SuggestionAcceptIn) (SuggestionAcceptOut, error) {
	var out SuggestionAcceptOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		out = SuggestionAcceptOut{}
		row := tx.QueryRowContext(c, `SELECT `+sugCols+sugFrom+` WHERE s.id = ? AND s.project_id = ?`, in.SuggestionID, in.ProjectID)
		r, err := scanSug(row)
		if errors.Is(err, sql.ErrNoRows) {
			return ops.NotFound("suggestion")
		}
		if err != nil {
			return err
		}
		if r.Status != "open" {
			return ops.ConflictWith("This suggestion was already decided ("+r.Status+").", map[string]any{"reason": "decided", "status": r.Status})
		}
		h, err := loadDocHead(c, tx, in.ProjectID, r.DocumentID)
		if err != nil {
			return err
		}
		if h.Deleted {
			return ops.Conflict("This document is in the trash. Restore it before accepting.")
		}
		if in.BaseRevision != 0 && in.BaseRevision != h.Revision {
			return ops.ConflictWith("This document changed since you opened it.", map[string]any{"reason": "revision", "current_revision": h.Revision})
		}
		body := comanchor.Body(h.Content)
		p, bad := place(r, body, alts.NewIndex(body), h.Revision)
		if len(bad) > 0 {
			return ops.ConflictWith("The text this suggestion was made for has changed, or cannot be found exactly once, so nothing was applied. Reject it and ask for a new one.",
				map[string]any{"reason": "stale", "current_revision": h.Revision, "unlocated": bad})
		}
		m, err := docs.ApplyReviewEdits(c, tx, l, docs.AIEditsIn{DocID: r.DocumentID, ProjectID: in.ProjectID, BaseRevision: h.Revision, TabID: in.TabID, Edits: p.edits, Note: "Suggestion " + r.ID + " accepted"})
		if err != nil {
			return err
		}
		now := db.Now()
		if _, err := tx.ExecContext(c, `UPDATE suggestions SET status = 'accepted', decided_by = ?, decided_at = ?, updated_at = ?, applied_revision = ?, revision_id = ? WHERE id = ?`,
			c.P.UserID, now, now, m.Revision, nullStr(m.RevisionID), r.ID); err != nil {
			return err
		}
		if r.AuthorID.Valid {
			if err := notify(tx, r.AuthorID.String, in.ProjectID, "suggestion-decided", "", r.ID, r.DocumentID, c.P.UserID, now); err != nil {
				return err
			}
		}
		if _, err := tx.ExecContext(c, `DELETE FROM notifications WHERE suggestion_id = ? AND kind = 'suggestion'`, r.ID); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "suggestion.accepted", ProjectID: in.ProjectID, DocumentID: r.DocumentID, Payload: map[string]any{"suggestion_id": r.ID, "revision": m.Revision}})
		c.Audit(ops.AuditEntry{Action: "suggestion.accept", TargetType: "document", TargetID: r.DocumentID, Detail: map[string]any{"suggestion_id": r.ID, "project_id": in.ProjectID}})
		out.Revision, out.RevisionID, out.ContentHash, out.WordCount, out.Comment = m.Revision, m.RevisionID, m.ContentHash, m.WordCount, m.Comment
		return c.FlushTo(tx)
	})
	if err != nil {
		return SuggestionAcceptOut{}, err
	}
	it, err := suggestionByID(c, c.DB().Read(), in.ProjectID, in.SuggestionID)
	out.Item = it.Item
	return out, err
}

// SuggestionAcceptAllIn accepts a set of one document's suggestions.
type SuggestionAcceptAllIn struct {
	ProjectID     string   `path:"id" json:"-" desc:"Project id."`
	DocumentID    string   `json:"document_id" desc:"The document; every suggestion named must be one of its open suggestions."`
	SuggestionIDs []string `json:"suggestion_ids" desc:"The suggestions to accept (at most 200)."`
	BaseRevision  int      `json:"base_revision,omitempty" desc:"As for suggestion.accept."`
	TabID         string   `json:"tab_id,omitempty"`
	Preview       bool     `json:"preview,omitempty" desc:"Work out the plan and change nothing."`
}

// ProjectRef lets the registry authorize by membership.
func (in SuggestionAcceptAllIn) ProjectRef() string { return in.ProjectID }

// Validate bounds the set.
func (in *SuggestionAcceptAllIn) Validate() error {
	if in.DocumentID == "" {
		return ops.Invalid("document_id", "Name the document.")
	}
	if len(in.SuggestionIDs) == 0 || len(in.SuggestionIDs) > 200 {
		return ops.Invalid("suggestion_ids", "Send between 1 and 200 suggestions.")
	}
	return nil
}

// Skipped says why a suggestion was left out of an accept_all.
type Skipped struct {
	SuggestionID string `json:"suggestion_id"`
	Reason       string `json:"reason" enum:"stale,overlap,invalid,decided" desc:"stale: its text was not found again exactly once. overlap: it overlaps one that comes before it. invalid: it can no longer be applied (a check failed). decided: it is not open."`
	Message      string `json:"message,omitempty"`
}

// SuggestionAcceptAllOut is the plan or the result.
type SuggestionAcceptAllOut struct {
	Preview     bool      `json:"preview"`
	Applied     []string  `json:"applied" desc:"Suggestion ids, in the order they sit in the text. For a preview: the ones that would be."`
	Skipped     []Skipped `json:"skipped"`
	Revision    int       `json:"revision" desc:"The document's revision after the write (the current one for a preview)."`
	RevisionID  string    `json:"revision_id,omitempty"`
	ContentHash string    `json:"content_hash,omitempty"`
	WordCount   int       `json:"word_count,omitempty"`
	Comment     string    `json:"comment,omitempty"`
}

func suggestAcceptAll(c *ops.Ctx, l *docs.Leases, in SuggestionAcceptAllIn) (SuggestionAcceptAllOut, error) {
	var out SuggestionAcceptAllOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		out = SuggestionAcceptAllOut{Preview: in.Preview, Applied: []string{}, Skipped: []Skipped{}}
		h, err := loadDocHead(c, tx, in.ProjectID, in.DocumentID)
		if err != nil {
			return err
		}
		if h.Deleted {
			return ops.Conflict("This document is in the trash. Restore it before accepting.")
		}
		if in.BaseRevision != 0 && in.BaseRevision != h.Revision {
			return ops.ConflictWith("This document changed since you opened it.", map[string]any{"reason": "revision", "current_revision": h.Revision})
		}
		out.Revision = h.Revision
		body := comanchor.Body(h.Content)
		ix := alts.NewIndex(body)
		seen := map[string]bool{}
		var cands []planned
		for _, id := range in.SuggestionIDs {
			if seen[id] {
				continue
			}
			seen[id] = true
			row := tx.QueryRowContext(c, `SELECT `+sugCols+sugFrom+` WHERE s.id = ? AND s.project_id = ? AND s.document_id = ?`, id, in.ProjectID, in.DocumentID)
			r, err := scanSug(row)
			if errors.Is(err, sql.ErrNoRows) {
				return ops.NotFound("suggestion")
			}
			if err != nil {
				return err
			}
			if r.Status != "open" {
				out.Skipped = append(out.Skipped, Skipped{SuggestionID: id, Reason: "decided", Message: "It is " + r.Status + "."})
				continue
			}
			p, bad := place(r, body, ix, h.Revision)
			if len(bad) > 0 {
				out.Skipped = append(out.Skipped, Skipped{SuggestionID: id, Reason: "stale", Message: "Its text was not found again exactly once."})
				continue
			}
			cands = append(cands, p)
		}
		// Overlaps: the suggestion whose first change starts earlier wins, then the one made first.
		sort.SliceStable(cands, func(i, j int) bool {
			if cands[i].first != cands[j].first {
				return cands[i].first < cands[j].first
			}
			return cands[i].r.ID < cands[j].r.ID
		})
		type span struct{ a, b int }
		var taken []span
		overlaps := func(e docs.AIEdit) bool {
			for _, t := range taken {
				switch {
				case e.Start == e.End && t.a == t.b:
					if e.Start == t.a {
						return true
					}
				case e.Start == e.End:
					// an insertion at the start of a replaced range is a clash too: "very " before
					// "quiet" and "quiet" replaced are two answers to the same place, and whether they
					// could both apply would depend on the order they are written in
					if e.Start >= t.a && e.Start < t.b {
						return true
					}
				case t.a == t.b:
					if t.a >= e.Start && t.a < e.End {
						return true
					}
				default:
					if e.Start < t.b && e.End > t.a {
						return true
					}
				}
			}
			return false
		}
		var chosen []planned
		var edits []docs.AIEdit
		for _, p := range cands {
			clash := false
			// the suggestion's own changes may not overlap each other either
			for i, e := range p.edits {
				if overlaps(e) {
					clash = true
					break
				}
				for _, f := range p.edits[:i] {
					if e.Start < f.End && e.End > f.Start || e.Start == f.Start && e.End == f.End {
						clash = true
					}
				}
			}
			if clash {
				out.Skipped = append(out.Skipped, Skipped{SuggestionID: p.r.ID, Reason: "overlap", Message: "It overlaps a suggestion that comes before it."})
				continue
			}
			// Each one is tried with what is already chosen, by the very checks the write makes: a set
			// that would fail as a whole (two changes that together alter the structure) never reaches
			// the write, the one that fails is skipped with its reason, and a preview and an apply
			// agree because they run this same loop.
			if err := docs.CheckReviewEdits(c, tx, in.ProjectID, in.DocumentID, h.Revision, append(append([]docs.AIEdit(nil), edits...), p.edits...)); err != nil {
				out.Skipped = append(out.Skipped, Skipped{SuggestionID: p.r.ID, Reason: "invalid", Message: ops.AsError(err).Message})
				continue
			}
			for _, e := range p.edits {
				taken = append(taken, span{e.Start, e.End})
			}
			chosen = append(chosen, p)
			edits = append(edits, p.edits...)
			out.Applied = append(out.Applied, p.r.ID)
		}
		sort.Strings(out.Applied)
		sort.SliceStable(out.Skipped, func(i, j int) bool { return out.Skipped[i].SuggestionID < out.Skipped[j].SuggestionID })
		if len(chosen) == 0 {
			return nil
		}
		if in.Preview {
			// the whole set must be applicable together, or the preview says so
			return docs.CheckReviewEdits(c, tx, in.ProjectID, in.DocumentID, h.Revision, edits)
		}
		m, err := docs.ApplyReviewEdits(c, tx, l, docs.AIEditsIn{DocID: in.DocumentID, ProjectID: in.ProjectID, BaseRevision: h.Revision, TabID: in.TabID, Edits: edits,
			Note: fmt.Sprintf("%d suggestions accepted", len(chosen))})
		if err != nil {
			return err
		}
		now := db.Now()
		for _, p := range chosen {
			if _, err := tx.ExecContext(c, `UPDATE suggestions SET status = 'accepted', decided_by = ?, decided_at = ?, updated_at = ?, applied_revision = ?, revision_id = ? WHERE id = ?`,
				c.P.UserID, now, now, m.Revision, nullStr(m.RevisionID), p.r.ID); err != nil {
				return err
			}
			if p.r.AuthorID.Valid {
				if err := notify(tx, p.r.AuthorID.String, in.ProjectID, "suggestion-decided", "", p.r.ID, in.DocumentID, c.P.UserID, now); err != nil {
					return err
				}
			}
			if _, err := tx.ExecContext(c, `DELETE FROM notifications WHERE suggestion_id = ? AND kind = 'suggestion'`, p.r.ID); err != nil {
				return err
			}
		}
		c.Emit(ops.Event{Type: "suggestion.accepted", ProjectID: in.ProjectID, DocumentID: in.DocumentID, Payload: map[string]any{"count": len(chosen), "revision": m.Revision}})
		c.Audit(ops.AuditEntry{Action: "suggestion.accept_all", TargetType: "document", TargetID: in.DocumentID, Detail: map[string]any{"count": len(chosen), "project_id": in.ProjectID}})
		out.Revision, out.RevisionID, out.ContentHash, out.WordCount, out.Comment = m.Revision, m.RevisionID, m.ContentHash, m.WordCount, m.Comment
		return c.FlushTo(tx)
	})
	return out, err
}
