// Package mentions finds where the entities of the bible are named in the
// manuscript, keeps that as an index, and answers the questions the editor
// asks of it: what is mentioned in this scene, where does this character
// appear, where did they first appear in this book.
//
// How it works, in one place:
//
//   - The vocabulary of a project is the names and aliases of the effective
//     entities in its scope (vocab.Load): its primary Compendium, the
//     Compendiums it references, linked entities, with the book's overrides
//     applied. A text.Matcher is built from it (whole words, Unicode folded,
//     possessives, longest match, see internal/text/match.go).
//   - The index is entity_mentions: one row per (document, entity), derived
//     from the document's stored plain text (doc_text, the same prose the
//     search index reads), so neither a save nor a rescan parses Markdown.
//     Explicit [[wiki links]] to an entity's name come from document_links.
//   - A document write (create, update, split, restore a version) updates its
//     rows inside the same transaction (Sync). Anything that changes the
//     vocabulary changes the project's stamp (triggers in migration 1520), and
//     a background worker rescans the projects whose stamp is ahead of the
//     last scan, reading the text outside the writer and writing only the rows
//     that changed in short chunks.
//   - Reads say when a rescan is owed (indexing: true), so a caller that has
//     just added a character knows the answer is still arriving.
//
// What counts: prose of chapters, scenes, parts, notes and front and back
// matter. Not counted: private notes, fenced and indented code, source blocks,
// research documents. Inline code counts (it is prose to the search index and
// the word counter). Titles and synopses are not indexed.
package mentions

import (
	"context"
	"database/sql"
	"sort"
	"sync"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/vocab"
	"github.com/crueber/scriobh/internal/text"
)

// Row is one (document, entity) of the index.
type Row struct {
	EntityID  string
	Count     int
	Names     int
	Aliases   int
	Explicit  int
	Ambiguous bool
	FirstPos  int
	Snippet   string
}

// engine is a project's matcher at one stamp.
type engine struct {
	stamp string
	vocab []vocab.Entry
	m     *text.Matcher
}

func buildEngine(stamp string, vocab []vocab.Entry) *engine {
	var ps []text.Phrase
	for i, e := range vocab {
		ps = append(ps, text.Phrase{Text: e.Name, Ref: i, Rank: e.Order})
		if e.Overridden && e.BaseName != e.Name {
			ps = append(ps, text.Phrase{Text: e.BaseName, Ref: i, Rank: e.Order, Alias: true})
		}
		for _, a := range e.Aliases {
			ps = append(ps, text.Phrase{Text: a, Ref: i, Rank: e.Order, Alias: true})
		}
	}
	return &engine{stamp: stamp, vocab: vocab, m: text.NewMatcher(ps)}
}

// ---- the cache ------------------------------------------------------------------------------------------------

// Engines are cached by project and stamp. A stamp is replaced whenever the
// vocabulary changes, so an entry is never wrong, only unused; the cache is
// bounded and forgets the oldest.
const cacheMax = 128

var cache = struct {
	sync.Mutex
	m     map[string]*engine
	order []string
}{m: map[string]*engine{}}

func cacheGet(project, stamp string) *engine {
	cache.Lock()
	defer cache.Unlock()
	if e := cache.m[project]; e != nil && e.stamp == stamp {
		return e
	}
	return nil
}

func cachePut(project string, e *engine) {
	cache.Lock()
	defer cache.Unlock()
	if _, ok := cache.m[project]; !ok {
		cache.order = append(cache.order, project)
		if len(cache.order) > cacheMax {
			delete(cache.m, cache.order[0])
			cache.order = cache.order[1:]
		}
	}
	cache.m[project] = e
}

func cacheForget(project string) {
	cache.Lock()
	defer cache.Unlock()
	delete(cache.m, project)
}

// Querier is what a transaction and a pool share.
type Querier = vocab.Querier

type querier = Querier

// engineFor returns the project's engine at its current stamp, reading the
// stamp and, on a cold cache, the vocabulary from one snapshot (q is one
// transaction), so a vocabulary is never filed under a stamp it does not
// belong to. ok is false when the project has no stamp yet.
func engineFor(ctx context.Context, q querier, projectID string) (g *engine, ok bool, err error) {
	var stamp string
	switch err := q.QueryRowContext(ctx, `SELECT stamp FROM mention_state WHERE project_id = ?`, projectID).Scan(&stamp); {
	case err == sql.ErrNoRows:
		return nil, false, nil
	case err != nil:
		return nil, false, err
	}
	if g := cacheGet(projectID, stamp); g != nil {
		return g, true, nil
	}
	vocab, err := vocab.Load(ctx, q, projectID)
	if err != nil {
		return nil, false, err
	}
	g = buildEngine(stamp, vocab)
	cachePut(projectID, g)
	return g, true, nil
}

// ---- scanning one document --------------------------------------------------------------------------------------

// noPosition is the FirstPos of a row that has no occurrence in the prose
// (only an explicit link): it sorts after every real position.
const noPosition = 1 << 30

// LinkRef is a wiki link of a document as the index reads it.
type LinkRef struct {
	Target     string
	Snippet    string
	Occurrence int
}

// scan finds the rows of one document: its prose, then its explicit links.
func (g *engine) scan(body string, links []LinkRef) []Row {
	if g.m.Empty() {
		return nil
	}
	rows := map[int]*Row{}
	get := func(i int) *Row {
		r := rows[i]
		if r == nil {
			r = &Row{EntityID: g.vocab[i].ID}
			rows[i] = r
		}
		return r
	}
	runes, at := 0, 0 // rune offset of byte at, advanced as hits come in order
	g.m.Find(body, func(h text.Match) {
		r := get(h.Cands[0].Ref)
		if r.Count == 0 {
			runes += utf8.RuneCountInString(body[at:h.Start])
			at = h.Start
			r.FirstPos = runes
			r.Snippet = text.SnippetAt(body, h.Start, h.End)
		}
		r.Count++
		if h.Cands[0].Alias {
			r.Aliases++
		} else {
			r.Names++
		}
		if len(h.Cands) > 1 {
			r.Ambiguous = true
		}
	})
	// Explicit links. A link whose shown text is the entity's own words is in
	// the prose scan already; one that shows other words ([[Máire|the
	// captain]]) is a mention only the link can see. The link index keeps one
	// row per target, so it cannot say which occurrences show other words:
	// links are counted in Explicit and never added to Count. A document that
	// only links an entity has a row with Count 0 and Explicit above 0.
	for _, l := range links {
		cands := g.m.LookupText(l.Target)
		if len(cands) == 0 {
			continue
		}
		r := get(cands[0].Ref)
		r.Explicit += l.Occurrence
		if len(cands) > 1 {
			r.Ambiguous = true
		}
		if r.Snippet == "" {
			r.Snippet = l.Snippet
			r.FirstPos = noPosition
		}
	}
	out := make([]Row, 0, len(rows))
	for _, r := range rows {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].EntityID < out[j].EntityID })
	return out
}

func loadRows(ctx context.Context, q querier, docID string) (map[string]Row, error) {
	rows, err := q.QueryContext(ctx, `SELECT entity_id, count, names, aliases, explicit, ambiguous, first_pos, snippet FROM entity_mentions WHERE document_id = ?`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Row{}
	for rows.Next() {
		var r Row
		var amb int
		if err := rows.Scan(&r.EntityID, &r.Count, &r.Names, &r.Aliases, &r.Explicit, &amb, &r.FirstPos, &r.Snippet); err != nil {
			return nil, err
		}
		r.Ambiguous = amb == 1
		out[r.EntityID] = r
	}
	return out, rows.Err()
}

type execer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// apply makes a document's stored rows equal to want, touching only the rows
// that differ. It returns how many statements it ran.
func apply(tx execer, projectID, docID string, have map[string]Row, want []Row) (int, error) {
	n := 0
	seen := make(map[string]bool, len(want))
	for _, w := range want {
		seen[w.EntityID] = true
		old, ok := have[w.EntityID]
		if ok && old == w {
			continue
		}
		amb := 0
		if w.Ambiguous {
			amb = 1
		}
		var err error
		if ok {
			_, err = tx.Exec(`UPDATE entity_mentions SET count = ?, names = ?, aliases = ?, explicit = ?, ambiguous = ?, first_pos = ?, snippet = ? WHERE document_id = ? AND entity_id = ?`,
				w.Count, w.Names, w.Aliases, w.Explicit, amb, w.FirstPos, w.Snippet, docID, w.EntityID)
		} else {
			_, err = tx.Exec(`INSERT INTO entity_mentions(document_id, entity_id, project_id, count, names, aliases, explicit, ambiguous, first_pos, snippet) VALUES (?,?,?,?,?,?,?,?,?,?)`,
				docID, w.EntityID, projectID, w.Count, w.Names, w.Aliases, w.Explicit, amb, w.FirstPos, w.Snippet)
		}
		if err != nil {
			return n, err
		}
		n++
	}
	for id := range have {
		if !seen[id] {
			if _, err := tx.Exec(`DELETE FROM entity_mentions WHERE document_id = ? AND entity_id = ?`, docID, id); err != nil {
				return n, err
			}
			n++
		}
	}
	return n, nil
}

func linksOf(ctx context.Context, q querier, docID string) ([]LinkRef, error) {
	rows, err := q.QueryContext(ctx, `SELECT link_text, snippet, occurrences FROM document_links WHERE from_id = ? AND kind = 'wiki'`, docID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LinkRef
	for rows.Next() {
		var l LinkRef
		if err := rows.Scan(&l.Target, &l.Snippet, &l.Occurrence); err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// ---- the write path --------------------------------------------------------------------------------------------

// Sync brings one document's mention rows up to date with its stored text, in
// the caller's transaction. The document ops call it after writing the text
// and the links, so what it reads is what was just saved. A project with no
// entities in scope costs two small reads and, if the document ever had rows,
// one delete.
func Sync(tx *sql.Tx, projectID, docID string) error {
	ctx := context.Background()
	var kind, body string
	switch err := tx.QueryRowContext(ctx, `SELECT d.kind, COALESCE(t.body, '') FROM documents d LEFT JOIN doc_text t ON t.document_id = d.id WHERE d.id = ?`, docID).Scan(&kind, &body); {
	case err == sql.ErrNoRows:
		return nil
	case err != nil:
		return err
	}
	if kind == "research" {
		return nil
	}
	g, ok, err := engineFor(ctx, tx, projectID)
	if err != nil {
		return err
	}
	if !ok {
		// The first write the index sees for this project: record that a full
		// scan is owed, and index this document under the new stamp.
		if err := MarkDirty(tx, projectID); err != nil {
			return err
		}
		if g, ok, err = engineFor(ctx, tx, projectID); err != nil || !ok {
			return err
		}
	}
	have, err := loadRows(ctx, tx, docID)
	if err != nil {
		return err
	}
	if g.m.Empty() {
		if len(have) == 0 {
			return nil
		}
		_, err := tx.Exec(`DELETE FROM entity_mentions WHERE document_id = ?`, docID)
		return err
	}
	links, err := linksOf(ctx, tx, docID)
	if err != nil {
		return err
	}
	_, err = apply(tx, projectID, docID, have, g.scan(body, links))
	return err
}

// MarkDirty records that the project's index is owed a rescan: its stamp
// becomes a new value. It is how a bulk writer (an import) hands the work to
// the worker. Safe inside a dry run: the stamp rolls back with everything else.
func MarkDirty(tx execer, projectID string) error {
	_, err := tx.Exec(`INSERT INTO mention_state(project_id, stamp) SELECT id, ? FROM projects WHERE id = ?
		ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp`, db.NewID("mst"), projectID)
	return err
}
