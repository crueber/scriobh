package mentions

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/domain/docref"
	"github.com/crueber/scriobh/internal/domain/links"
	"github.com/crueber/scriobh/internal/domain/vocab"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Register adds the mention ops. The merge step is registered by the registry
// (entities cannot be imported from here; see MergeCount and MergeApply).
func Register(r *ops.Registry) {
	ops.Register(r, ops.Op{Name: "mention.list", Method: "GET", Path: "/api/v1/docs/{id}/mentions",
		Desc: "The entities a document mentions, with how many times each occurs (as a name, as an alias, as an explicit [[link]]), a snippet, and with spans=true the place of every occurrence for highlighting, as UTF-16 offsets over the text with line endings normalised to LF (the measure doc.split takes). " +
			"Only entities the caller may read through a Compendium are returned; being on the project grants none of the Compendium, so a reader who is not on the Compendium gets an empty list with hidden: true. indexing is true while a rescan is owed, and the answer may be behind.",
		Auth: docref.Via(ops.ProjectReader)}, list)
	ops.Register(r, ops.Op{Name: "mention.by_entity", Method: "GET", Path: "/api/v1/projects/{id}/mentions",
		Desc: "Where an entity appears in this book: the documents that mention it, in reading order, with the count in each and a snippet around the first. Manuscript text only (parts, chapters, scenes) unless all=true adds notes and front and back matter. " +
			"entity is an id, or a name or alias among the entities the caller may read. Not found if the caller may not read it.",
		Auth: ops.RequireProject(ops.ProjectReader)}, byEntity)
	ops.Register(r, ops.Op{Name: "entity.first_appearance", Method: "GET", Path: "/api/v1/projects/{id}/first-appearance",
		Desc: "The first document, in reading order, that mentions an entity, per project (a Compendium is shared, so first appearance is a fact about one book). With entity it answers for that one; without it, for every entity the caller may read, each with the document or null, and how many documents and mentions it has. Manuscript text only unless all=true.",
		Auth: ops.RequireProject(ops.ProjectReader)}, firstAppearance)
	ops.Register(r, ops.Op{Name: "entity.appears_in", Method: "GET", Path: "/api/v1/entities/{id}/appears-in",
		Desc: "Which books mention this entity: for each project the caller can open, how many documents and mentions, and the first appearance. Projects the caller cannot open are counted in hidden_projects, never named. A key pinned to a Compendium sees counts only.",
		Auth: ops.RequireCompendiumVia(compaccess.Reader, resolveEntity)}, appearsIn)
	ops.Register(r, ops.Op{Name: "mention.rebuild", Method: "POST", Path: "/api/v1/projects/{id}/mentions/rebuild",
		Desc: "Rebuild this project's mention index from its text. The index is derived and always safe to rebuild; it is kept current by every save and by the background scan that follows a change to the entities. With dry_run it only counts the documents it would read. Project owner.",
		Auth: ops.RequireProject(ops.ProjectOwner)}, rebuild)
}

func resolveEntity(ctx context.Context, env *ops.Env, in any) (string, bool) {
	ref, ok := in.(interface{ EntityID() string })
	if !ok || env == nil || env.DB == nil {
		return "", false
	}
	var cid sql.NullString
	if err := env.DB.Read().QueryRowContext(ctx, `SELECT compendium_id FROM entities WHERE id = ?`, ref.EntityID()).Scan(&cid); err != nil {
		return "", false
	}
	return cid.String, cid.Valid
}

// owed reports whether the project's index is behind its vocabulary.
func owed(ctx context.Context, q querier, projectID string) bool {
	return IsOwed(ctx, q, projectID)
}

// IsOwed reports whether the project's index is behind its vocabulary or was never built.
func IsOwed(ctx context.Context, q Querier, projectID string) bool {
	var stamp, done string
	err := q.QueryRowContext(ctx, `SELECT stamp, indexed_stamp FROM mention_state WHERE project_id = ?`, projectID).Scan(&stamp, &done)
	if err == sql.ErrNoRows {
		// Never stamped: owed unless the project has nothing to index.
		var n int
		_ = q.QueryRowContext(ctx, `SELECT count(*) FROM documents WHERE project_id = ? AND kind <> 'research' LIMIT 1`, projectID).Scan(&n)
		return n > 0
	}
	return err == nil && stamp != done
}

func readTx(c *ops.Ctx) (*sql.Tx, error) {
	return c.DB().Read().BeginTx(c, &sql.TxOptions{ReadOnly: true})
}

// ---- mention.list ------------------------------------------------------------------------------------------------

// ListIn names a document.
type ListIn struct {
	ID    string `path:"id" json:"-" desc:"Document id."`
	Spans bool   `query:"spans" desc:"Also return the place of every occurrence (the editor asks for these to highlight)."`
}

// DocID lets the registry find the owning project.
func (in ListIn) DocID() string { return in.ID }

// Span is one occurrence in the Markdown.
type Span struct {
	Start int    `json:"start" desc:"UTF-16 code units from the start of the text with CRLF normalised to LF."`
	End   int    `json:"end"`
	Kind  string `json:"kind" enum:"name,alias" desc:"Whether the words are the entity's name or an alias."`
}

// Mention is one entity a document mentions.
type Mention struct {
	Entity    vocab.Brief   `json:"entity"`
	Count     int           `json:"count" desc:"Occurrences of the entity's name or an alias in the prose (the shown text of a link is prose)."`
	Names     int           `json:"names" desc:"Of count, found as the entity's name."`
	Aliases   int           `json:"aliases" desc:"Of count, found as an alias."`
	Explicit  int           `json:"explicit" desc:"Occurrences of [[links]] that name the entity. A link that shows other words is only counted here."`
	Ambiguous bool          `json:"ambiguous,omitempty" desc:"Another entity the caller can read answers to the same words; the one named here has precedence (primary Compendium, then references in the order added, then links)."`
	Also      []vocab.Brief `json:"also,omitempty" desc:"The other readable entities the words could mean."`
	Snippet   string        `json:"snippet,omitempty" desc:"Plain text around the first occurrence."`
	Spans     []Span        `json:"spans,omitempty"`
}

// ListOut lists a document's mentions in the order they first occur.
type ListOut struct {
	Items         []Mention `json:"items"`
	Hidden        bool      `json:"hidden" desc:"Entities in this document are left out because the caller may not read their Compendium. They are not counted or named."`
	Indexing      bool      `json:"indexing" desc:"A rescan is owed: the entities changed since the index was built, so counts may be behind."`
	SpansComplete bool      `json:"spans_complete" desc:"Only meaningful with spans=true: every occurrence was placed. False when some text could not be located (counts are still right), and always false without spans."`
}

// others lists the readable entities, apart from self, that share a name or
// alias with it.
func (g *engine) others(vis *vocab.Visible, self int) []vocab.Brief {
	seen := map[int]bool{self: true}
	var out []vocab.Brief
	e := g.vocab[self]
	words := append([]string{e.Name, e.BaseName}, e.Aliases...)
	for _, w := range words {
		for _, c := range g.m.LookupText(w) {
			if seen[c.Ref] {
				continue
			}
			seen[c.Ref] = true
			if x, ok := vis.ByID[g.vocab[c.Ref].ID]; ok {
				out = append(out, x.Brief())
			}
		}
	}
	return out
}

// VisibleAmbiguity returns a check that says whether another entity the reader
// may read answers to the same words as the given entity. The stored flag
// counts every entity in the project's scope, readable or not, so it is never
// shown as it stands: a reader who may not read the other meaning must not
// learn that it exists.
func VisibleAmbiguity(ctx context.Context, q Querier, vis *vocab.Visible, projectID string) func(entityID string) bool {
	g, ok, err := engineFor(ctx, q, projectID)
	if err != nil || !ok {
		return func(string) bool { return false }
	}
	return func(entityID string) bool {
		i := g.index(entityID)
		return i >= 0 && len(g.others(vis, i)) > 0
	}
}

func (g *engine) index(id string) int {
	for i, e := range g.vocab {
		if e.ID == id {
			return i
		}
	}
	return -1
}

func list(c *ops.Ctx, in ListIn) (ListOut, error) {
	tx, err := readTx(c)
	if err != nil {
		return ListOut{}, err
	}
	defer tx.Rollback()
	var projectID, kind string
	if err := tx.QueryRowContext(c, `SELECT project_id, kind FROM documents WHERE id = ?`, in.ID).Scan(&projectID, &kind); err != nil {
		return ListOut{}, ops.NotFound("document")
	}
	out := ListOut{Items: []Mention{}}
	if kind == "research" {
		return out, nil
	}
	vis, err := vocab.LoadVisible(c, tx, vocab.ReaderOf(c.P), projectID)
	if err != nil {
		return ListOut{}, err
	}
	out.Indexing = owed(c, tx, projectID)
	have, err := LoadRows(c, tx, in.ID)
	if err != nil {
		return ListOut{}, err
	}
	g, ok, err := engineFor(c, tx, projectID)
	if err != nil {
		return ListOut{}, err
	}
	type item struct {
		m   Mention
		pos int
		idx int
	}
	var items []item
	for id, r := range have {
		e, ok := vis.ByID[id]
		if !ok {
			out.Hidden = true
			continue
		}
		m := Mention{Entity: e.Brief(), Count: r.Count, Names: r.Names, Aliases: r.Aliases, Explicit: r.Explicit, Snippet: r.Snippet}
		idx := -1
		if ok {
			idx = g.index(id)
		}
		if r.Ambiguous && idx >= 0 {
			// Only say it when the other meaning is one the caller can see.
			if also := g.others(vis, idx); len(also) > 0 {
				m.Ambiguous, m.Also = true, also
			}
		}
		items = append(items, item{m, r.FirstPos, idx})
	}
	sort.Slice(items, func(i, j int) bool {
		if items[i].pos != items[j].pos {
			return items[i].pos < items[j].pos
		}
		return items[i].m.Entity.ID < items[j].m.Entity.ID
	})
	byIdx := map[int]*Mention{}
	for i := range items {
		out.Items = append(out.Items, items[i].m)
	}
	for i := range out.Items {
		for _, it := range items {
			if it.m.Entity.ID == out.Items[i].Entity.ID && it.idx >= 0 {
				byIdx[it.idx] = &out.Items[i]
			}
		}
	}
	if in.Spans && ok && !g.m.Empty() {
		var content string
		if err := tx.QueryRowContext(c, `SELECT content FROM documents WHERE id = ?`, in.ID).Scan(&content); err != nil {
			return ListOut{}, err
		}
		spans, complete := text.Spans(content, g.m)
		out.SpansComplete = complete
		for _, s := range spans {
			m := byIdx[s.Cands[0].Ref]
			if m == nil {
				continue // an entity the caller cannot read, or one the index has not caught up with
			}
			k := "name"
			if s.Cands[0].Alias {
				k = "alias"
			}
			m.Spans = append(m.Spans, Span{Start: s.Start, End: s.End, Kind: k})
		}
	}
	return out, nil
}

// LoadRows reads a document's index rows, by entity id.
func LoadRows(ctx context.Context, q Querier, docID string) (map[string]Row, error) {
	return loadRows(ctx, q, docID)
}

// ---- mention.by_entity -----------------------------------------------------------------------------------------------

// ByEntityIn asks where an entity appears.
type ByEntityIn struct {
	ID     string `path:"id" json:"-" desc:"Project id."`
	Entity string `query:"entity" desc:"An entity id, or a name or alias."`
	All    bool   `query:"all" desc:"Also notes and front and back matter."`
	Limit  int    `query:"limit" desc:"Documents per page, default 100, at most 500."`
	Offset int    `query:"offset" desc:"Documents to skip."`
}

// ProjectRef lets the registry authorize by membership.
func (in ByEntityIn) ProjectRef() string { return in.ID }

// Validate checks the shape.
func (in *ByEntityIn) Validate() error {
	if strings.TrimSpace(in.Entity) == "" {
		return ops.Invalid("entity", "Say which entity: an id, a name or an alias.")
	}
	switch {
	case in.Limit <= 0:
		in.Limit = 100
	case in.Limit > 500:
		in.Limit = 500
	}
	if in.Offset < 0 {
		return ops.Invalid("offset", "The offset cannot be negative.")
	}
	return nil
}

// Appearance is one document that mentions an entity.
type Appearance struct {
	Document  links.Ref `json:"document"`
	Count     int       `json:"count"`
	Explicit  int       `json:"explicit"`
	Ambiguous bool      `json:"ambiguous,omitempty"`
	Snippet   string    `json:"snippet,omitempty" desc:"Plain text around the first occurrence in the document."`
}

// ByEntityOut is a page of appearances in reading order.
type ByEntityOut struct {
	Entity     vocab.Brief  `json:"entity"`
	Items      []Appearance `json:"items"`
	Documents  int          `json:"documents" desc:"Documents that mention it, in all pages."`
	Mentions   int          `json:"mentions" desc:"Occurrences in all of them."`
	NextOffset int          `json:"next_offset,omitempty"`
	Indexing   bool         `json:"indexing" desc:"A rescan is owed; counts may be behind."`
}

func byEntity(c *ops.Ctx, in ByEntityIn) (ByEntityOut, error) {
	tx, err := readTx(c)
	if err != nil {
		return ByEntityOut{}, err
	}
	defer tx.Rollback()
	vis, err := vocab.LoadVisible(c, tx, vocab.ReaderOf(c.P), in.ID)
	if err != nil {
		return ByEntityOut{}, err
	}
	e, ok := vis.Find(strings.TrimSpace(in.Entity))
	if !ok {
		return ByEntityOut{}, ops.NotFound("entity")
	}
	t, err := loadTree(c, tx, in.ID)
	if err != nil {
		return ByEntityOut{}, err
	}
	rows, err := tx.QueryContext(c, `SELECT document_id, count, explicit, ambiguous, snippet FROM entity_mentions WHERE entity_id = ? AND project_id = ?`, e.ID, in.ID)
	if err != nil {
		return ByEntityOut{}, err
	}
	type hit struct {
		doc string
		a   Appearance
	}
	var hits []hit
	out := ByEntityOut{Entity: e.Brief(), Items: []Appearance{}}
	visibleOther := VisibleAmbiguity(c, tx, vis, in.ID)(e.ID)
	for rows.Next() {
		var h hit
		var amb int
		if err := rows.Scan(&h.doc, &h.a.Count, &h.a.Explicit, &amb, &h.a.Snippet); err != nil {
			rows.Close()
			return ByEntityOut{}, err
		}
		n, live := t.byID[h.doc]
		if !live || (!in.All && !manuscript(n.Kind)) {
			continue
		}
		h.a.Ambiguous = amb == 1 && visibleOther
		hits = append(hits, h)
	}
	if err := rows.Close(); err != nil {
		return ByEntityOut{}, err
	}
	sort.Slice(hits, func(i, j int) bool { return t.order[hits[i].doc] < t.order[hits[j].doc] })
	out.Documents = len(hits)
	for _, h := range hits {
		out.Mentions += max(h.a.Count, h.a.Explicit)
	}
	for i := in.Offset; i < len(hits) && len(out.Items) < in.Limit; i++ {
		h := hits[i]
		h.a.Document, _ = t.ref(h.doc)
		out.Items = append(out.Items, h.a)
	}
	if end := in.Offset + len(out.Items); end < len(hits) {
		out.NextOffset = end
	}
	out.Indexing = owed(c, tx, in.ID)
	return out, nil
}

// ---- entity.first_appearance -------------------------------------------------------------------------------------

// FirstIn asks for first appearances in a project.
type FirstIn struct {
	ID     string `path:"id" json:"-" desc:"Project id."`
	Entity string `query:"entity" desc:"One entity (an id, a name or an alias); without it, every entity the caller may read."`
	All    bool   `query:"all" desc:"Count notes and front and back matter too."`
}

// ProjectRef lets the registry authorize by membership.
func (in FirstIn) ProjectRef() string { return in.ID }

// First is one entity's first appearance in the project.
type First struct {
	Entity    vocab.Brief `json:"entity"`
	Document  *links.Ref  `json:"document,omitempty" desc:"The first document in reading order that mentions it; absent when none does."`
	Documents int         `json:"documents" desc:"How many documents mention it."`
	Mentions  int         `json:"mentions" desc:"Occurrences in all of them."`
}

// FirstOut lists first appearances.
type FirstOut struct {
	Items    []First `json:"items"`
	Hidden   bool    `json:"hidden" desc:"The project reaches entities the caller may not read; they are left out and not counted."`
	Indexing bool    `json:"indexing" desc:"A rescan is owed; the answers may be behind."`
}

func firstAppearance(c *ops.Ctx, in FirstIn) (FirstOut, error) {
	tx, err := readTx(c)
	if err != nil {
		return FirstOut{}, err
	}
	defer tx.Rollback()
	vis, err := vocab.LoadVisible(c, tx, vocab.ReaderOf(c.P), in.ID)
	if err != nil {
		return FirstOut{}, err
	}
	out := FirstOut{Items: []First{}, Hidden: vis.Hidden}
	var want []vocab.Entry
	if ref := strings.TrimSpace(in.Entity); ref != "" {
		e, ok := vis.Find(ref)
		if !ok {
			return FirstOut{}, ops.NotFound("entity")
		}
		want = []vocab.Entry{e}
	} else {
		want = vis.Entries
	}
	t, err := loadTree(c, tx, in.ID)
	if err != nil {
		return FirstOut{}, err
	}
	rows, err := tx.QueryContext(c, `SELECT entity_id, document_id, max(count, explicit) FROM entity_mentions WHERE project_id = ?`, in.ID)
	if err != nil {
		return FirstOut{}, err
	}
	type agg struct {
		first         string
		docs, mention int
	}
	got := map[string]*agg{}
	for rows.Next() {
		var eid, doc string
		var n int
		if err := rows.Scan(&eid, &doc, &n); err != nil {
			rows.Close()
			return FirstOut{}, err
		}
		node, live := t.byID[doc]
		if !live || (!in.All && !manuscript(node.Kind)) {
			continue
		}
		a := got[eid]
		if a == nil {
			a = &agg{first: doc}
			got[eid] = a
		}
		a.docs++
		a.mention += n
		if t.order[doc] < t.order[a.first] {
			a.first = doc
		}
	}
	if err := rows.Close(); err != nil {
		return FirstOut{}, err
	}
	for _, e := range want {
		f := First{Entity: e.Brief()}
		if a := got[e.ID]; a != nil {
			r, _ := t.ref(a.first)
			f.Document, f.Documents, f.Mentions = &r, a.docs, a.mention
		}
		out.Items = append(out.Items, f)
	}
	out.Indexing = owed(c, tx, in.ID)
	return out, nil
}

// ---- entity.appears_in -------------------------------------------------------------------------------------------

// AppearsIn names an entity.
type AppearsIn struct {
	ID string `path:"id" json:"-" desc:"Entity id."`
}

// EntityID lets the registry find the owning Compendium.
func (in AppearsIn) EntityID() string { return in.ID }

// ProjectAppearance is one book's view of an entity.
type ProjectAppearance struct {
	ProjectID string     `json:"project_id"`
	Title     string     `json:"title"`
	Documents int        `json:"documents"`
	Mentions  int        `json:"mentions"`
	First     *links.Ref `json:"first,omitempty" desc:"The first manuscript document that mentions it, in reading order."`
	Indexing  bool       `json:"indexing" desc:"A rescan is owed for this project; counts may be behind."`
}

// AppearsOut lists the books that mention an entity.
type AppearsOut struct {
	Items          []ProjectAppearance `json:"items"`
	HiddenProjects int                 `json:"hidden_projects" desc:"Projects that mention it which the caller cannot open. Counted, never named."`
}

func appearsIn(c *ops.Ctx, in AppearsIn) (AppearsOut, error) {
	tx, err := readTx(c)
	if err != nil {
		return AppearsOut{}, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(c, `SELECT m.project_id, p.title, m.document_id, max(m.count, m.explicit) FROM entity_mentions m
		JOIN projects p ON p.id = m.project_id AND p.deleted_at IS NULL AND p.trashed_at IS NULL
		JOIN documents d ON d.id = m.document_id AND d.deleted_at IS NULL AND d.kind IN ('part','chapter','scene')
		WHERE m.entity_id = ? ORDER BY m.project_id`, in.ID)
	if err != nil {
		return AppearsOut{}, err
	}
	type agg struct {
		title string
		docs  []string
		n     int
	}
	by := map[string]*agg{}
	var order []string
	for rows.Next() {
		var pid, title, doc string
		var n int
		if err := rows.Scan(&pid, &title, &doc, &n); err != nil {
			rows.Close()
			return AppearsOut{}, err
		}
		a := by[pid]
		if a == nil {
			a = &agg{title: title}
			by[pid] = a
			order = append(order, pid)
		}
		a.docs = append(a.docs, doc)
		a.n += n
	}
	if err := rows.Close(); err != nil {
		return AppearsOut{}, err
	}
	out := AppearsOut{Items: []ProjectAppearance{}}
	for _, pid := range order {
		role, ok := "", false
		switch {
		case c.P.CompendiumID != "": // a key pinned to a Compendium reaches no project
		case c.P.ProjectID != "" && c.P.ProjectID != pid: // nor a project pinned elsewhere
		case c.Env.ProjectRole != nil:
			role, ok = c.Env.ProjectRole(c, c.P.UserID, pid)
		}
		if !ok || role == "" {
			out.HiddenProjects++
			continue
		}
		a := by[pid]
		pa := ProjectAppearance{ProjectID: pid, Title: a.title, Documents: len(a.docs), Mentions: a.n, Indexing: owed(c, tx, pid)}
		t, err := loadTree(c, tx, pid)
		if err != nil {
			return AppearsOut{}, err
		}
		first := a.docs[0]
		for _, d := range a.docs {
			if t.order[d] < t.order[first] {
				first = d
			}
		}
		if r, ok := t.ref(first); ok {
			pa.First = &r
		}
		out.Items = append(out.Items, pa)
	}
	return out, nil
}

// ---- mention.rebuild ----------------------------------------------------------------------------------------------

// RebuildIn names a project.
type RebuildIn struct {
	ID     string `path:"id" json:"-" desc:"Project id."`
	DryRun bool   `json:"dry_run,omitempty" desc:"Only count the documents that would be read."`
}

// ProjectRef lets the registry authorize by membership.
func (in RebuildIn) ProjectRef() string { return in.ID }

// RebuildOut says what the rebuild did.
type RebuildOut struct {
	DryRun bool `json:"dry_run,omitempty"`
	Stats
}

func rebuild(c *ops.Ctx, in RebuildIn) (RebuildOut, error) {
	if in.DryRun || c.DryRun() {
		var n int
		if err := c.DB().Read().QueryRowContext(c, `SELECT count(*) FROM documents WHERE project_id = ? AND kind <> 'research'`, in.ID).Scan(&n); err != nil {
			return RebuildOut{}, err
		}
		return RebuildOut{DryRun: true, Stats: Stats{Projects: 1, Documents: n}}, nil
	}
	if err := MarkAllDirty(c, c.DB(), in.ID); err != nil {
		return RebuildOut{}, err
	}
	cacheForget(in.ID)
	st, err := ScanProject(c, c.DB(), in.ID)
	if err != nil {
		return RebuildOut{}, err
	}
	return RebuildOut{Stats: st}, nil
}
