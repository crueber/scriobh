package ai

import (
	"context"
	"encoding/json"
	"fmt"

	lib "github.com/crueber/scriobh/internal/ai"
	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// The result hook is how the recipes' layer (internal/domain/aiwork) takes a
// finished run: it keeps what must outlive the response (a suggestion held for
// a short time, a review's notes, the Lab's marks) and turns the model's parsed
// answer into the shape a screen uses, anchored against the document as the
// request was built from it. It lives here, inside Run, so that a streamed run
// and a plain run give the same result, and it runs after the run has settled:
// whatever it does cannot touch the run record or the budget.

// SelInfo is the selection of a request, resolved on the server.
type SelInfo struct {
	// Start and End are UTF-16 code units of the body at the revision the
	// request was built from.
	Start, End int
	// Source is the Markdown the range holds (what was sent is its chosen
	// wording, with private notes and ghosted text left out).
	Source string
}

// ResultInfo is a finished run, handed to the hook.
type ResultInfo struct {
	UserID, ProjectID, RunID string
	Recipe                   lib.Recipe
	Params                   map[string]string
	DocumentID               string
	// DocRevision, Body and Title are the target document as the request saw it.
	DocRevision int
	Body        string
	Title       string
	Sel         *SelInfo
	// Chips are the pieces of context that were sent (before redaction).
	Chips []lib.Chip
	// Out is the run as the pipeline built it: the restored text, the parsed data.
	Out RunOut
}

// ResultReply is the hook's answer. Handled says the hook owns this recipe's
// result: Data then replaces the parsed data (empty: there is none), and the
// warnings are added.
type ResultReply struct {
	Handled  bool
	Data     json.RawMessage
	Warnings []string
}

// ResultHook receives a finished run.
type ResultHook func(ctx context.Context, d *db.DB, info ResultInfo) (ResultReply, error)

// SetResultHook installs the hook. There is one.
func (s *Service) SetResultHook(h ResultHook) {
	s.mu.Lock()
	s.hook = h
	s.mu.Unlock()
}

func (s *Service) deliver(ctx context.Context, d *db.DB, p ops.Principal, b *built, in RunIn, run runRow, out *RunOut) {
	s.mu.Lock()
	h := s.hook
	s.mu.Unlock()
	if h == nil || (out.Text == "" && out.Status != "ok") {
		return
	}
	info := ResultInfo{UserID: p.UserID, ProjectID: in.ProjectID, RunID: run.ID, Recipe: b.recipe, Params: b.params, DocumentID: in.DocumentID,
		Chips: b.chips, Out: *out}
	if b.doc != nil {
		body := alts.Split(alts.NormalizeEOL(b.doc.Content)).Body
		info.DocRevision, info.Body, info.Title = b.doc.Revision, body, b.doc.Title
		if b.sel != nil {
			ix := alts.NewIndex(b.sel.body)
			info.Sel = &SelInfo{Start: ix.Unit(b.sel.a), End: ix.Unit(b.sel.b), Source: b.sel.body[b.sel.a:b.sel.b]}
			info.Body = b.sel.body
		}
	}
	func() {
		defer func() {
			if r := recover(); r != nil {
				s.log.Error("ai: the result hook panicked", "recipe", b.recipe.ID, "panic", fmt.Sprint(r))
				out.Data = nil
				out.Warnings = append(out.Warnings, "The result could not be prepared for review; the text is shown as it came and nothing was kept.")
			}
		}()
		rep, err := h(context.WithoutCancel(ctx), d, info)
		if err != nil {
			s.log.Error("ai: the result hook failed", "recipe", b.recipe.ID, "err", err)
			out.Data = nil
			out.Warnings = append(out.Warnings, "The result could not be kept for review; the text is shown as it came.")
			return
		}
		if rep.Handled {
			out.Data = rep.Data
		}
		out.Warnings = append(out.Warnings, rep.Warnings...)
	}()
}
