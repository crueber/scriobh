package docs

import (
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/domain/altstore"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Ghosting marks text as set aside: it stays in the document and in exports,
// and is left out of the word count, the search text and mention matching. The
// Markdown is the truth: a ghosted range is wrapped in a pair of inline tags
// (text.GhostOpen, text.GhostClose), so the index in the ghosts table is only
// a view that gives each ghost an id.

const maxGhostRanges = 50

// GhostListIn lists a document's ghosts.
type GhostListIn struct {
	ID string `path:"id" json:"-" desc:"Document id."`
}

// DocID lets the registry find the owning project.
func (in GhostListIn) DocID() string { return in.ID }

// GhostListOut is a document's ghosted ranges and what they do to its count.
type GhostListOut struct {
	Revision     int              `json:"revision"`
	Items        []altstore.Ghost `json:"items" desc:"In document order."`
	WordCount    int              `json:"word_count" desc:"Words counted: ghosted words left out."`
	GhostedWords int              `json:"ghosted_words" desc:"Words the ghosts hold."`
	RawWordCount int              `json:"raw_word_count" desc:"Words with the ghosted ones counted too."`
}

func ghostList(c *ops.Ctx, in GhostListIn) (GhostListOut, error) {
	d := c.DB().Read()
	var out GhostListOut
	var content string
	if err := d.QueryRowContext(c, `SELECT revision, content, word_count FROM documents WHERE id = ?`, in.ID).Scan(&out.Revision, &content, &out.WordCount); err != nil {
		return out, ops.NotFound("document")
	}
	items, err := altstore.LoadGhosts(d, in.ID)
	if err != nil {
		return out, err
	}
	out.Items = []altstore.Ghost{}
	for _, g := range items {
		out.Items = append(out.Items, g)
		out.GhostedWords += g.Words
	}
	out.RawWordCount = out.WordCount + out.GhostedWords
	return out, nil
}

// GhostMut is what the ghost ops return.
type GhostMut struct {
	Revision     int              `json:"revision"`
	ContentHash  string           `json:"content_hash"`
	WordCount    int              `json:"word_count" desc:"Words counted after the change."`
	GhostedWords int              `json:"ghosted_words"`
	Comment      string           `json:"comment" desc:"The alternatives comment that ends the text now, '' when none (anchors move when tags are inserted, so it can change)."`
	Unchanged    bool             `json:"unchanged"`
	Ghosts       []altstore.Ghost `json:"ghosts" desc:"The document's ghosts after the change."`
	Content      string           `json:"content,omitempty" desc:"The whole stored text, when with_content was set."`
}

// GhostCreateIn ghosts ranges of the text.
type GhostCreateIn struct {
	ID           string  `path:"id" json:"-" desc:"Document id."`
	BaseRevision int     `json:"base_revision" desc:"The revision the ranges were read from."`
	TabID        string  `json:"tab_id,omitempty" desc:"Browser tab id; enables the single-writer lease."`
	Ranges       []Range `json:"ranges" desc:"The text to ghost, up to 50 ranges. Each lies inside one paragraph or heading; for a selection across paragraphs send one range per paragraph. Leading and trailing white space is left out of a ghost."`
	WithContent  bool    `json:"with_content,omitempty"`
}

// DocID lets the registry find the owning project.
func (in GhostCreateIn) DocID() string { return in.ID }

func ghostCreate(c *ops.Ctx, l *leases, in GhostCreateIn) (GhostMut, error) {
	if len(in.Ranges) == 0 || len(in.Ranges) > maxGhostRanges {
		return GhostMut{}, ops.Invalid("ranges", fmt.Sprintf("Send between 1 and %d ranges.", maxGhostRanges))
	}
	var out GhostMut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		s, err := openAlt(c, tx, l, in.ID, in.TabID, in.BaseRevision)
		if err != nil {
			return err
		}
		type span struct{ a, b int }
		var spans []span
		for _, r := range in.Ranges {
			a, b, err := s.resolve(r)
			if err != nil {
				return err
			}
			for a < b && strings.ContainsRune(" \t\n", rune(s.body[a])) {
				a++
			}
			for b > a && strings.ContainsRune(" \t\n", rune(s.body[b-1])) {
				b--
			}
			if a >= b {
				return ops.Invalid("ranges", "A range with nothing but white space in it cannot be ghosted.")
			}
			if alts.TouchesMarker(s.body, a, b) {
				return ops.Invalid("ranges", "That range holds or cuts into a ghost marker. Revive the ghosted text first.")
			}
			if alts.CutsGrapheme(s.body, a) || alts.CutsGrapheme(s.body, b) {
				return ops.Invalid("ranges", "That range cuts a character in two (an emoji, a flag or a letter with its accent).")
			}
			spans = append(spans, span{a, b})
		}
		sort.Slice(spans, func(i, j int) bool { return spans[i].a < spans[j].a })
		for i := 1; i < len(spans); i++ {
			if spans[i].a < spans[i-1].b {
				return ops.Invalid("ranges", "Two ranges overlap.")
			}
		}
		// A ghost may hold an alternative; it may not start or end inside one.
		for _, it := range s.items {
			if it.Orphan || !it.Placed() {
				continue
			}
			as, ok1 := s.index().Byte(it.Start)
			ae, ok2 := s.index().Byte(it.End)
			if !ok1 || !ok2 {
				continue
			}
			for _, sp := range spans {
				if (sp.a > as && sp.a < ae) || (sp.b > as && sp.b < ae) {
					return ops.Conflict("That range starts or ends inside an alternative (" + alts.Clip(it.ChosenText()) + "). Ghost the whole of it, or resolve the alternative first.")
				}
			}
		}
		before, _ := text.Ghosts(s.body)
		var edits []alts.Edit
		nb := s.body
		for i := len(spans) - 1; i >= 0; i-- { // from the end, so earlier offsets stay true
			sp := spans[i]
			nb = nb[:sp.b] + text.GhostClose + nb[sp.b:]
			nb = nb[:sp.a] + text.GhostOpen + nb[sp.a:]
			edits = append(edits, alts.Edit{Start: sp.a, End: sp.a, NewLen: len(text.GhostOpen)}, alts.Edit{Start: sp.b, End: sp.b, NewLen: len(text.GhostClose)})
		}
		after, ok := text.Ghosts(nb)
		if !ok || len(after) != len(before)+len(spans) {
			return ops.Invalid("ranges", "Each range must lie inside one paragraph or heading of plain text (not code, a table, raw HTML or a note); for a selection across paragraphs send one range per paragraph.")
		}
		items, _ := alts.Rebase(s.body, nb, s.items, edits, s.ro)
		out, err = ghostFinish(s, nb, items, "create", in.WithContent)
		return err
	})
	return out, err
}

// GhostReviveIn brings ghosted text back.
type GhostReviveIn struct {
	ID           string   `path:"id" json:"-" desc:"Document id."`
	BaseRevision int      `json:"base_revision"`
	TabID        string   `json:"tab_id,omitempty" desc:"Browser tab id; enables the single-writer lease."`
	GhostIDs     []string `json:"ghost_ids,omitempty" desc:"Revive these ghosts (ids from ghost.list)."`
	Start        *int     `json:"start,omitempty" desc:"Or revive every ghost that overlaps this range, in UTF-16 code units of the body."`
	End          *int     `json:"end,omitempty"`
	All          bool     `json:"all,omitempty" desc:"Or revive every ghost in the document."`
	WithContent  bool     `json:"with_content,omitempty"`
}

// DocID lets the registry find the owning project.
func (in GhostReviveIn) DocID() string { return in.ID }

func ghostRevive(c *ops.Ctx, l *leases, in GhostReviveIn) (GhostMut, error) {
	var out GhostMut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		s, err := openAlt(c, tx, l, in.ID, in.TabID, in.BaseRevision)
		if err != nil {
			return err
		}
		if !in.All && len(in.GhostIDs) == 0 && in.Start == nil {
			return ops.Invalid("ghost_ids", "Name the ghosts (ghost_ids), a range (start and end), or send all.")
		}
		spans, ok := text.Ghosts(s.body)
		if !ok && len(spans) == 0 {
			return ops.Invalid("ghost_ids", "This text is too irregular to find its ghosts.")
		}
		rows, err := altstore.LoadGhosts(tx, in.ID)
		if err != nil {
			return err
		}
		wantID := map[string]bool{}
		for _, id := range in.GhostIDs {
			wantID[id] = true
		}
		byStart := map[int]string{}
		for _, g := range rows {
			byStart[g.Start] = g.ID
		}
		ix := s.index()
		var rs, re int
		if in.Start != nil && in.End != nil {
			var ok1, ok2 bool
			if rs, ok1 = ix.Byte(*in.Start); !ok1 {
				return ops.Invalid("start", "That position is not in the text.")
			}
			if re, ok2 = ix.Byte(*in.End); !ok2 {
				return ops.Invalid("end", "That position is not in the text.")
			}
		}
		var edits []alts.Edit
		nb := s.body
		picked := 0
		for i := len(spans) - 1; i >= 0; i-- {
			sp := spans[i]
			take := in.All || wantID[byStart[ix.Unit(sp.OpenEnd)]]
			if !take && in.Start != nil && in.End != nil {
				take = sp.OpenStart < re && sp.CloseEnd > rs
			}
			if !take {
				continue
			}
			picked++
			nb = nb[:sp.CloseStart] + nb[sp.CloseEnd:]
			nb = nb[:sp.OpenStart] + nb[sp.OpenEnd:]
			edits = append(edits, alts.Edit{Start: sp.CloseStart, End: sp.CloseEnd}, alts.Edit{Start: sp.OpenStart, End: sp.OpenEnd})
		}
		if picked == 0 {
			return ops.NotFound("ghost")
		}
		items, _ := alts.Rebase(s.body, nb, s.items, edits, s.ro)
		out, err = ghostFinish(s, nb, items, "revive", in.WithContent)
		return err
	})
	return out, err
}

func ghostFinish(s *altSession, body string, items []alts.Item, kind string, withContent bool) (GhostMut, error) {
	parts := s.parts
	parts.Body = body
	line := alts.Serialize(items)
	content := parts.Compose(line)
	var out GhostMut
	if content != s.h.Content {
		if _, _, err := altstore.Write(s.tx, s.id, s.h.ProjectID, s.items, items, s.c.P.UserID, s.now); err != nil {
			return out, err
		}
		cm, err := commit(s.c, s.tx, s.id, s.h, content, s.now, commitOpts{NoCredit: true})
		if err != nil {
			return out, err
		}
		s.c.Emit(ops.Event{Type: "ghost.updated", ProjectID: s.h.ProjectID, DocumentID: s.id,
			Payload: map[string]any{"kind": kind, "revision": cm.Revision}})
		if err := s.c.FlushTo(s.tx); err != nil {
			return out, err
		}
		out.Revision, out.ContentHash, out.WordCount = cm.Revision, cm.Hash, cm.Words
	} else {
		out.Revision, out.ContentHash, out.WordCount, out.Unchanged = s.h.Revision, s.h.Hash, s.h.Words, true
	}
	out.Comment = line
	gs, err := altstore.LoadGhosts(s.tx, s.id)
	if err != nil {
		return out, err
	}
	out.Ghosts = []altstore.Ghost{}
	for _, g := range gs {
		out.Ghosts = append(out.Ghosts, g)
		out.GhostedWords += g.Words
	}
	if withContent {
		out.Content = content
	}
	return out, nil
}

func registerGhosts(r *ops.Registry, l *leases) {
	viaDoc := func(role string) ops.Auth { return ops.RequireProjectVia(role, resolveDoc) }
	ops.Register(r, ops.Op{Name: "ghost.list", Method: "GET", Path: "/api/v1/docs/{id}/ghosts",
		Desc: "List a document's ghosted ranges, with the word count, the words ghosted and the raw count with them.", Auth: viaDoc(ops.ProjectReader)}, ghostList)
	ops.Register(r, ops.Op{Name: "ghost.create", Method: "POST", Path: "/api/v1/docs/{id}/ghosts", Status: 201,
		Desc: "Ghost ranges of the text: they stay in the document and in exports, and are left out of the word count, search and mentions. Wraps each range in a pair of ghost tags; nothing is deleted.", Auth: viaDoc(ops.ProjectEditor)},
		func(c *ops.Ctx, in GhostCreateIn) (GhostMut, error) { return ghostCreate(c, l, in) })
	ops.Register(r, ops.Op{Name: "ghost.revive", Method: "POST", Path: "/api/v1/docs/{id}/ghosts/revive",
		Desc: "Revive ghosted text: remove the ghost tags of the named ghosts, of the ghosts overlapping a range, or of all of them. The words count again.", Auth: viaDoc(ops.ProjectEditor)},
		func(c *ops.Ctx, in GhostReviveIn) (GhostMut, error) { return ghostRevive(c, l, in) })
}
