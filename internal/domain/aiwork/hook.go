package aiwork

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/revisions"
	xtext "github.com/crueber/scriobh/internal/text"
)

// The result hook: what a finished run becomes. Each branch turns the parsed
// answer of one family of recipes into the shape a screen uses and keeps what
// has to outlive the response. A branch never inserts anything into a document.

func sha(s string) string {
	h := sha256.Sum256([]byte(s))
	return hex.EncodeToString(h[:])
}

var editRecipes = map[string]bool{"rewrite": true, "tighten": true, "expand": true, "simplify": true, "tone": true, "grammar": true, "continue": true, "describe": true, "dialogue": true}

func (s *Service) onResult(ctx context.Context, d *db.DB, info aicore.ResultInfo) (aicore.ResultReply, error) {
	id := info.Recipe.ID
	switch {
	case editRecipes[id]:
		return s.editResult(ctx, d, info)
	case id == "alternatives":
		return s.alternativesResult(ctx, d, info)
	case id == "chat":
		return s.chatResult(ctx, d, info)
	case id == "critique.chapter" || id == "critique.selection":
		return s.reviewResult(ctx, d, info, "critique")
	case id == "continuity.chapter" || id == "continuity.selection":
		return s.reviewResult(ctx, d, info, "continuity")
	case id == "extract.chapter" || id == "extract.selection":
		return s.extractResult(ctx, d, info)
	case strings.HasPrefix(id, "lab."):
		return s.labResult(ctx, d, info)
	case id == "summary.chapter":
		return s.summaryResult(ctx, d, info)
	}
	return aicore.ResultReply{}, nil
}

func none(warn ...string) aicore.ResultReply {
	return aicore.ResultReply{Handled: true, Warnings: warn}
}

func reply(v any, warn ...string) (aicore.ResultReply, error) {
	b, err := json.Marshal(v)
	if err != nil {
		return aicore.ResultReply{}, err
	}
	return aicore.ResultReply{Handled: true, Data: b, Warnings: warn}, nil
}

// ---- selection edits --------------------------------------------------------------------

// Suggestion is one proposed change to the text, for a diff review. It is held
// by the server for 30 minutes; accepting it (ai.accept.edit) applies this text,
// which the caller cannot change.
type Suggestion struct {
	ID    string `json:"id" desc:"Send it to ai.accept.edit or ai.accept.reject."`
	Op    string `json:"op" enum:"replace,insert_after" desc:"replace: the original range is replaced. insert_after: the text follows the original range (Continue)."`
	Start int    `json:"start" desc:"Start of the original range, in UTF-16 code units of the body at base_revision. White space at the ends of the selection is left out."`
	End   int    `json:"end"`
	// Original is the Markdown source of the range.
	Original string              `json:"original"`
	Text     string              `json:"text" desc:"The suggested text, as plain Markdown. Untrusted: show it as text."`
	TextHash string              `json:"text_hash" desc:"SHA-256 of text. Send it back as expect_hash to be sure the text accepted is the text reviewed."`
	Diff     []revisions.Segment `json:"diff" desc:"Word-level difference, original to suggestion: equal, delete and insert segments."`
	Words    struct {
		Original int `json:"original"`
		Text     int `json:"text"`
	} `json:"words"`
}

// EditResult is the data of a selection recipe's run.
type EditResult struct {
	Kind         string       `json:"kind" enum:"edit"`
	RunID        string       `json:"run_id"`
	DocumentID   string       `json:"document_id"`
	BaseRevision int          `json:"base_revision" desc:"The document revision the model read."`
	Suggestions  []Suggestion `json:"suggestions"`
	ExpiresAt    string       `json:"expires_at" desc:"After this the server no longer holds the suggestion."`
}

type editItem struct {
	ID       string `json:"id"`
	Op       string `json:"op"`
	Start    int    `json:"start"`
	End      int    `json:"end"`
	Original string `json:"original"`
	Before   string `json:"before"`
	After    string `json:"after"`
	Text     string `json:"text"`
	TextHash string `json:"text_hash"`
}

func (it editItem) wire() Suggestion {
	sg := Suggestion{ID: it.ID, Op: it.Op, Start: it.Start, End: it.End, Original: it.Original, Text: it.Text, TextHash: it.TextHash}
	if it.Op == "insert_after" {
		sg.Diff = []revisions.Segment{{Op: "equal", Text: it.Original}, {Op: "insert", Text: it.Text}}
	} else {
		sg.Diff = revisions.Diff(it.Original, it.Text, revisions.UnitWord).Segments
	}
	sg.Words.Original, sg.Words.Text = len(strings.Fields(it.Original)), len(strings.Fields(it.Text))
	if sg.Diff == nil {
		sg.Diff = []revisions.Segment{}
	}
	return sg
}

// selectionItem makes the held item for a replacement of the selection of a run,
// or says why there is none.
func selectionItem(info aicore.ResultInfo, text, op string) (editItem, string) {
	if info.Sel == nil || info.DocumentID == "" {
		return editItem{}, "There was no selection, so the answer is not offered as a change to the text."
	}
	ix := alts.NewIndex(info.Body)
	a, ok1 := ix.Byte(info.Sel.Start)
	b, ok2 := ix.Byte(info.Sel.End)
	if !ok1 || !ok2 || a >= b {
		return editItem{}, "The selection could not be placed, so the answer is not offered as a change to the text."
	}
	a, b = trimSpan(info.Body, a, b)
	if a >= b {
		return editItem{}, "The selection holds only white space."
	}
	orig := info.Body[a:b]
	if hasHidden(orig) || xtext.TouchesSpans(xtext.HiddenSpans(info.Body), a, b) {
		return editItem{}, "The selection holds a private note or ghosted text, which the model did not see and a suggestion may not replace. Select text without them."
	}
	if text == orig {
		return editItem{}, "The suggestion is the same as the text."
	}
	it := editItem{ID: "s1", Op: op, Start: ix.Unit(a), End: ix.Unit(b), Original: orig, Text: text, TextHash: sha(text)}
	it.Before, it.After = paragraphContext(info.Body, a, b)
	return it, ""
}

func (s *Service) hold(ctx context.Context, d *db.DB, info aicore.ResultInfo, kind string, items any) error {
	return tx(ctx, d, func(t *sql.Tx) error {
		purgeExpired(ctx, t)
		if info.Out.Text == "" {
			return fmt.Errorf("a run with no text has nothing to hold")
		}
		_, err := t.ExecContext(ctx, `INSERT INTO ai_pending(run_id, project_id, user_id, document_id, recipe, kind, doc_revision, output_hash, items_json, created_at, expires_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(run_id) DO NOTHING`,
			info.RunID, info.ProjectID, info.UserID, nullStr(info.DocumentID), info.Recipe.ID, kind, info.DocRevision, sha(info.Out.Text), s.sealItems(info.RunID, info.UserID, toJSON(items)), now(), s.expiry())
		return err
	})
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func (s *Service) editResult(ctx context.Context, d *db.DB, info aicore.ResultInfo) (aicore.ResultReply, error) {
	var ed lib.EditData
	if len(info.Out.Data) == 0 || json.Unmarshal(info.Out.Data, &ed) != nil || ed.Text == "" {
		return none(), nil
	}
	if info.Out.Incomplete {
		return none("The answer was cut off, so it is shown as text but not offered as a change."), nil
	}
	it, why := selectionItem(info, ed.Text, ed.Op)
	if why != "" {
		return none(why), nil
	}
	if err := s.hold(ctx, d, info, "edit", []editItem{it}); err != nil {
		return aicore.ResultReply{}, err
	}
	return reply(EditResult{Kind: "edit", RunID: info.RunID, DocumentID: info.DocumentID, BaseRevision: info.DocRevision, Suggestions: []Suggestion{it.wire()}, ExpiresAt: s.expiry()})
}

// ---- chat -------------------------------------------------------------------------------

func (s *Service) chatResult(ctx context.Context, d *db.DB, info aicore.ResultInfo) (aicore.ResultReply, error) {
	var cd lib.ChatData
	if len(info.Out.Data) == 0 || json.Unmarshal(info.Out.Data, &cd) != nil || cd.Replacement == "" {
		return reply(EditResult{Kind: "edit", RunID: info.RunID, DocumentID: info.DocumentID, BaseRevision: info.DocRevision, Suggestions: []Suggestion{}})
	}
	if info.Out.Incomplete {
		return none("The answer was cut off, so its proposed wording is not offered as a change."), nil
	}
	it, why := selectionItem(info, cd.Replacement, "replace")
	if why != "" {
		return reply(EditResult{Kind: "edit", RunID: info.RunID, DocumentID: info.DocumentID, BaseRevision: info.DocRevision, Suggestions: []Suggestion{}}, why)
	}
	if err := s.hold(ctx, d, info, "chat", []editItem{it}); err != nil {
		return aicore.ResultReply{}, err
	}
	return reply(EditResult{Kind: "edit", RunID: info.RunID, DocumentID: info.DocumentID, BaseRevision: info.DocRevision, Suggestions: []Suggestion{it.wire()}, ExpiresAt: s.expiry()})
}

// ---- alternatives -----------------------------------------------------------------------

// Candidate is one proposed wording of an alternative.
type Candidate struct {
	ID       string `json:"id" desc:"Send it to ai.accept.alternative."`
	Text     string `json:"text" desc:"Untrusted: show it as text."`
	TextHash string `json:"text_hash"`
}

// CandidatesResult is the data of the alternatives recipe's run: candidates, not
// alternatives. ai.accept.alternative adds the ones the writer picks, marked as
// generated, without changing the text.
type CandidatesResult struct {
	Kind         string      `json:"kind" enum:"alternatives"`
	RunID        string      `json:"run_id"`
	DocumentID   string      `json:"document_id"`
	BaseRevision int         `json:"base_revision"`
	Scope        string      `json:"scope" enum:"word,sentence,paragraph"`
	Start        int         `json:"start"`
	End          int         `json:"end"`
	Original     string      `json:"original"`
	Candidates   []Candidate `json:"candidates"`
	ExpiresAt    string      `json:"expires_at"`
}

type altItems struct {
	Scope      string      `json:"scope"`
	Original   editItem    `json:"original"`
	Candidates []Candidate `json:"candidates"`
}

func (s *Service) alternativesResult(ctx context.Context, d *db.DB, info aicore.ResultInfo) (aicore.ResultReply, error) {
	var ad lib.AlternativesData
	if len(info.Out.Data) == 0 || json.Unmarshal(info.Out.Data, &ad) != nil || len(ad.Alternatives) == 0 {
		return none(), nil
	}
	base, why := selectionItem(info, "\x00", "replace") // the range and its source; the text is not used
	if why != "" && why != "The suggestion is the same as the text." {
		return none(why), nil
	}
	scope := info.Params["scope"]
	if scope == "" {
		scope = "sentence"
	}
	items := altItems{Scope: scope, Original: base}
	for i, t := range ad.Alternatives {
		items.Candidates = append(items.Candidates, Candidate{ID: fmt.Sprintf("c%d", i+1), Text: t, TextHash: sha(t)})
	}
	if err := s.hold(ctx, d, info, "alternatives", items); err != nil {
		return aicore.ResultReply{}, err
	}
	return reply(CandidatesResult{Kind: "alternatives", RunID: info.RunID, DocumentID: info.DocumentID, BaseRevision: info.DocRevision, Scope: scope,
		Start: base.Start, End: base.End, Original: base.Original, Candidates: items.Candidates, ExpiresAt: s.expiry()})
}

// ---- summaries --------------------------------------------------------------------------

// SummarySuggestion is a proposed synopsis.
type SummarySuggestion struct {
	ID       string `json:"id"`
	Short    string `json:"short,omitempty"`
	Long     string `json:"long,omitempty"`
	TextHash string `json:"text_hash"`
}

// SummaryResult is the data of the summary recipe's run. ai.summary.accept
// writes the synopsis.
type SummaryResult struct {
	Kind         string            `json:"kind" enum:"summary"`
	RunID        string            `json:"run_id"`
	DocumentID   string            `json:"document_id"`
	BaseRevision int               `json:"base_revision"`
	Suggestion   SummarySuggestion `json:"suggestion"`
	Current      struct {
		Short string `json:"short"`
		Long  string `json:"long"`
	} `json:"current" desc:"What the synopsis says now, for the review."`
	ExpiresAt string `json:"expires_at"`
}

func (s *Service) summaryResult(ctx context.Context, d *db.DB, info aicore.ResultInfo) (aicore.ResultReply, error) {
	var sd lib.SummaryData
	if len(info.Out.Data) == 0 || json.Unmarshal(info.Out.Data, &sd) != nil || (sd.Short == "" && sd.Long == "") {
		return none(), nil
	}
	if info.Out.Incomplete {
		return none("The answer was cut off, so the summary is shown as text but not offered for the synopsis."), nil
	}
	if info.DocumentID == "" {
		return none(), nil
	}
	sg := SummarySuggestion{ID: "s1", Short: sd.Short, Long: sd.Long, TextHash: sha(sd.Short + "\x00" + sd.Long)}
	if err := s.hold(ctx, d, info, "summary", []SummarySuggestion{sg}); err != nil {
		return aicore.ResultReply{}, err
	}
	out := SummaryResult{Kind: "summary", RunID: info.RunID, DocumentID: info.DocumentID, BaseRevision: info.DocRevision, Suggestion: sg, ExpiresAt: s.expiry()}
	_ = d.Read().QueryRowContext(ctx, `SELECT synopsis_short, synopsis_long FROM documents WHERE id = ?`, info.DocumentID).Scan(&out.Current.Short, &out.Current.Long)
	return reply(out)
}
