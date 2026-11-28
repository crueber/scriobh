// Package research is the material a writer keeps beside the book: research
// notes, clippings, folders to keep them in, a board to sort them on, tags, and
// a private scratchpad.
//
// A note belongs to a project (project-local research) or to a Compendium
// (shared research). Every op is registered twice, project.research.* and
// compendium.research.*, over one set of handlers, because an op declares one
// kind of authorization; the handlers scope every query by the owner the
// registry has already authorized, so an id from another owner is not found.
// Being on a project grants nothing in its Compendium: a project reader who is
// not a Compendium member cannot read, list, search or hear about the
// Compendium's notes.
//
// Notes are not documents. They live in their own table so that a Compendium
// with no project can hold them, and so that they can never reach the
// manuscript tree, the outline, the word counts, the goals or the compile. They
// have a tree (folders, positions), a trash, a revision counter for safe saves,
// and a search index; they have no revision history and no wiki links.
//
// Nothing here fetches an address: a clipping is what the person pasted or typed.
package research

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Owner kinds, as in attachments.
const (
	OwnerProject    = attachments.OwnerProject
	OwnerCompendium = attachments.OwnerCompendium
)

// Kinds and statuses.
var (
	Kinds    = []string{"note", "clipping", "folder"}
	Statuses = []string{"inbox", "reading", "useful", "archived"}
	Colors   = []string{"slate", "clay", "ochre", "moss", "teal", "blue", "plum", "rose"}
)

const (
	maxTitle  = 300
	maxBody   = 1 << 20
	maxURL    = 2000
	maxQuote  = 20000
	maxTags   = 20
	maxTagLen = 40
	maxDepth  = 12
	// MaxListed bounds a research tree read.
	MaxListed = 20000
)

// Note is a full research note.
type Note struct {
	ID          string   `json:"id"`
	OwnerKind   string   `json:"owner_kind" enum:"project,compendium"`
	OwnerID     string   `json:"owner_id"`
	ParentID    string   `json:"parent_id" desc:"The folder it is in; empty at the top."`
	Kind        string   `json:"kind" enum:"note,clipping,folder"`
	Title       string   `json:"title"`
	Body        string   `json:"body" desc:"Markdown."`
	SourceURL   string   `json:"source_url" desc:"A clipping's address, as entered. The server never fetches it."`
	RetrievedOn string   `json:"retrieved_on" desc:"The date you read it, YYYY-MM-DD, or empty."`
	Quote       string   `json:"quote" desc:"The passage a clipping keeps."`
	Status      string   `json:"status" enum:"inbox,reading,useful,archived" desc:"The board column."`
	Tags        []string `json:"tags"`
	Position    float64  `json:"position"`
	Revision    int      `json:"revision" desc:"Send it back as base_revision to refuse a stale save."`
	WordCount   int      `json:"word_count"`
	Trashed     bool     `json:"trashed"`
	CreatedAt   string   `json:"created_at"`
	UpdatedAt   string   `json:"updated_at"`
}

// Entry is a note in a list: no body or quote.
type Entry struct {
	ID          string   `json:"id"`
	ParentID    string   `json:"parent_id"`
	Kind        string   `json:"kind" enum:"note,clipping,folder"`
	Title       string   `json:"title"`
	SourceURL   string   `json:"source_url"`
	RetrievedOn string   `json:"retrieved_on"`
	Status      string   `json:"status" enum:"inbox,reading,useful,archived"`
	Tags        []string `json:"tags"`
	Position    float64  `json:"position"`
	Revision    int      `json:"revision"`
	WordCount   int      `json:"word_count"`
	Trashed     bool     `json:"trashed"`
	UpdatedAt   string   `json:"updated_at"`
}

// Item wraps one note.
type Item struct {
	Item Note `json:"item"`
}

type owner struct{ kind, id string }

func (o owner) col() string {
	if o.kind == OwnerCompendium {
		return "compendium_id"
	}
	return "project_id"
}

func (o owner) event(typ string, payload map[string]any) ops.Event {
	e := ops.Event{Type: typ, Payload: payload}
	if o.kind == OwnerProject {
		e.ProjectID = o.id
	} else {
		e.CompendiumID = o.id
	}
	return e
}

// Where is the SQL condition that scopes a query to the owner, with its argument.
func (o owner) where(alias string) (string, any) {
	if alias != "" {
		alias += "."
	}
	return alias + o.col() + " = ?", o.id
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// ---- validation ----------------------------------------------------------------------------------

func oneOf(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func cleanLine(s string) string {
	return strings.TrimSpace(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ", "\t", " ").Replace(s))
}

func cleanTitle(s string) (string, error) {
	s = cleanLine(s)
	if s == "" {
		s = "Untitled"
	}
	if utf8.RuneCountInString(s) > maxTitle {
		return "", ops.Invalid("title", "A title is limited to 300 characters.")
	}
	return s, nil
}

func cleanBody(s string) error {
	if len(s) > maxBody {
		return ops.Invalid("body", "A note is limited to 1 MB of Markdown.")
	}
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) {
		return ops.Invalid("body", "A note must be valid text.")
	}
	return nil
}

// cleanSource accepts an http or https address, or nothing. It is only ever
// stored and shown; a javascript: or data: address is refused so a link made
// from it cannot run anything.
func cleanSource(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if len(s) > maxURL {
		return "", ops.Invalid("source_url", "An address is limited to 2000 characters.")
	}
	u, err := url.Parse(s)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || strings.ContainsAny(s, " \t\r\n\x00") {
		return "", ops.Invalid("source_url", "Enter a web address starting with http:// or https://.")
	}
	return s, nil
}

func cleanRetrieved(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", nil
	}
	if _, err := time.Parse("2006-01-02", s); err != nil {
		return "", ops.Invalid("retrieved_on", "Use a date like 2026-03-14.")
	}
	return s, nil
}

func cleanQuote(s string) (string, error) {
	if utf8.RuneCountInString(s) > maxQuote || !utf8.ValidString(s) {
		return "", ops.Invalid("quote", "A quote is limited to 20,000 characters.")
	}
	return strings.TrimSpace(s), nil
}

// cleanTagNames trims, drops empties and folded duplicates, and checks the limits.
func cleanTagNames(names []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string
	for _, n := range names {
		n = cleanLine(n)
		if n == "" {
			continue
		}
		if utf8.RuneCountInString(n) > maxTagLen {
			return nil, ops.Invalid("tags", "A tag is limited to 40 characters.")
		}
		k := text.FoldTitle(n)
		if !seen[k] {
			seen[k] = true
			out = append(out, n)
		}
	}
	if len(out) > maxTags {
		return nil, ops.Invalid("tags", "A note can have 20 tags.")
	}
	return out, nil
}

// searchText writes the plain text the index reads, in the caller's transaction.
func writeText(tx *sql.Tx, o owner, id, title, source, body, quote string) error {
	plain := text.SearchBody(body)
	if quote != "" {
		plain += "\n" + text.SearchBody(quote)
	}
	var project, compendium any
	if o.kind == OwnerProject {
		project = o.id
	} else {
		compendium = o.id
	}
	_, err := tx.Exec(`INSERT INTO research_text(note_id, project_id, compendium_id, title, source, body) VALUES (?,?,?,?,?,?)
		ON CONFLICT(note_id) DO UPDATE SET title = excluded.title, source = excluded.source, body = excluded.body`,
		id, project, compendium, title, source, plain)
	return err
}

// ---- loading -----------------------------------------------------------------------------------

const noteCols = `n.id, COALESCE(n.parent_id,''), n.kind, n.title, n.body, n.source_url, n.retrieved_on, n.quote, n.status, n.position, n.revision,
	n.word_count, n.deleted_at IS NOT NULL, n.created_at, n.updated_at`

func scanNote(s interface{ Scan(...any) error }, o owner) (Note, error) {
	var n Note
	err := s.Scan(&n.ID, &n.ParentID, &n.Kind, &n.Title, &n.Body, &n.SourceURL, &n.RetrievedOn, &n.Quote, &n.Status, &n.Position, &n.Revision,
		&n.WordCount, &n.Trashed, &n.CreatedAt, &n.UpdatedAt)
	n.OwnerKind, n.OwnerID = o.kind, o.id
	n.Tags = []string{}
	return n, err
}

// load reads one note of the owner, with its tags. Trashed notes load: they can be restored.
func load(ctx context.Context, q queryer, o owner, id string) (Note, error) {
	cond, arg := o.where("n")
	n, err := scanNote(q.QueryRowContext(ctx, `SELECT `+noteCols+` FROM research_notes n WHERE n.id = ? AND `+cond, id, arg), o)
	if errors.Is(err, sql.ErrNoRows) {
		return Note{}, ops.NotFound("note")
	}
	if err != nil {
		return Note{}, err
	}
	tags, err := tagsOf(ctx, q, []string{id})
	if err != nil {
		return Note{}, err
	}
	if t := tags[id]; t != nil {
		n.Tags = t
	}
	return n, nil
}

// tagsOf returns the tag names of the given notes, sorted by folded name.
func tagsOf(ctx context.Context, q queryer, ids []string) (map[string][]string, error) {
	out := map[string][]string{}
	const chunk = 400
	for len(ids) > 0 {
		n := len(ids)
		if n > chunk {
			n = chunk
		}
		args := make([]any, n)
		ph := make([]string, n)
		for i, id := range ids[:n] {
			args[i], ph[i] = id, "?"
		}
		rows, err := q.QueryContext(ctx, `SELECT rt.note_id, t.name FROM research_tags rt JOIN tags t ON t.id = rt.tag_id
			WHERE rt.note_id IN (`+strings.Join(ph, ",")+`) ORDER BY rt.note_id, t.name_key`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, name string
			if err := rows.Scan(&id, &name); err != nil {
				rows.Close()
				return nil, err
			}
			out[id] = append(out[id], name)
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		ids = ids[n:]
	}
	return out, nil
}

// setTags makes a note's tags exactly names, creating tags that do not exist.
// It returns whether anything changed.
func setTags(tx *sql.Tx, o owner, noteID string, names []string, now string) (bool, error) {
	want := map[string]string{} // key -> name
	for _, n := range names {
		want[text.FoldTitle(n)] = n
	}
	rows, err := tx.Query(`SELECT t.id, t.name_key FROM research_tags rt JOIN tags t ON t.id = rt.tag_id WHERE rt.note_id = ?`, noteID)
	if err != nil {
		return false, err
	}
	have := map[string]string{} // key -> tag id
	for rows.Next() {
		var id, k string
		if err := rows.Scan(&id, &k); err != nil {
			rows.Close()
			return false, err
		}
		have[k] = id
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return false, err
	}
	if err := rows.Close(); err != nil {
		return false, err
	}
	changed := false
	for k, id := range have {
		if _, keep := want[k]; !keep {
			if _, err := tx.Exec(`DELETE FROM research_tags WHERE note_id = ? AND tag_id = ?`, noteID, id); err != nil {
				return false, err
			}
			changed = true
		}
	}
	keys := make([]string, 0, len(want))
	for k := range want {
		if _, ok := have[k]; !ok {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		id, err := tagID(tx, o, want[k], k, now)
		if err != nil {
			return false, err
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO research_tags(note_id, tag_id) VALUES (?,?)`, noteID, id); err != nil {
			return false, err
		}
		changed = true
	}
	return changed, nil
}

// tagID finds the owner's tag by folded name, creating it if there is none.
func tagID(tx *sql.Tx, o owner, name, key, now string) (string, error) {
	var id string
	err := tx.QueryRow(`SELECT id FROM tags WHERE `+o.col()+` = ? AND name_key = ?`, o.id, key).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", err
	}
	id = db.NewID("tag")
	var project, compendium any
	if o.kind == OwnerProject {
		project = o.id
	} else {
		compendium = o.id
	}
	_, err = tx.Exec(`INSERT INTO tags(id, project_id, compendium_id, name, name_key, color, created_at) VALUES (?,?,?,?,?,?,?)`,
		id, project, compendium, name, key, "slate", now)
	return id, err
}
