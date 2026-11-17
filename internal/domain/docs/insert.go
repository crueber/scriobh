package docs

import (
	"database/sql"

	"strings"

	"github.com/crueber/scriobh/internal/alts"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/altstore"
	"github.com/crueber/scriobh/internal/text"
)

// Prepared is a document body with everything derived from it computed:
// the hash, the word and character counts and the search text. Computing it is
// the expensive part of inserting a document (a parse of the Markdown), so a
// bulk writer such as the importer does it before it opens its transaction,
// where the single writer would otherwise be held.
type Prepared struct {
	Content string
	Hash    string
	Words   int
	Chars   int
	Search  string
	// Links are the wiki links in the text, parsed here so a bulk writer does
	// not parse inside its transaction.
	Links []text.WikiLink
	// Plan is what the text's alternatives comment (if it has one) becomes: the
	// content above is already its canonical form, and InsertTx writes the rows.
	Plan altstore.Plan
}

// Prepare derives what a stored document needs from its Markdown, with the
// default alternatives limit.
func Prepare(content string) Prepared { return PrepareWith(content, alts.DefaultLimits) }

// PrepareWith is Prepare with the configured limits on alternatives.
func PrepareWith(content string, lim alts.Limits) Prepared {
	var plan altstore.Plan
	if strings.Contains(content, alts.Marker) {
		if p, err := altstore.PlanNew(content, "", db.Now(), lim); err == nil {
			plan, content = p, p.Content
		}
	}
	return Prepared{
		Plan:    plan,
		Content: content,
		Hash:    hashContent(content),
		Words:   text.WordCount(content),
		Chars:   text.CharCount(content),
		Search:  text.SearchBody(content),
		Links:   text.WikiLinks(content),
	}
}

// NewDoc is one document to insert.
type NewDoc struct {
	ID        string // db.NewID("doc") when empty
	ProjectID string
	ParentID  string // empty for the top level
	Kind      string // one of Kinds
	Status    string // one of Statuses; empty is draft
	Title     string // already cleaned
	Synopsis  string // the one-line synopsis, already cleaned
	// Outline metadata, already cleaned and within the limits of doc.update.
	// The zero values are the defaults of doc.create.
	SynopsisLong        string
	Pov, Location       string
	StoryDate, StoryEnd string
	TargetWords         int
	ExcludeFromCompile  bool
	Position            float64 // the caller assigns positions (db.PositionGap apart)
	Body                Prepared
	By                  string // the user who wrote the text; empty when unknown
	// DeferLinks leaves the document's wiki links to the caller. A bulk writer
	// sets it, calls links.Reresolve once for every title it inserted, and after
	// its transaction commits stores the links with links.SyncBatch (one tree
	// read per chunk instead of one per document, and the writer released
	// between chunks).
	DeferLinks bool
}

// InsertTx writes one document and its search text inside the caller's
// transaction, exactly as doc.create does: revision 1, counts, hash, search
// row. It does not touch the project's updated_at and does not emit events;
// the caller does both once for the whole batch. The caller has already
// checked the project, the parent and the kind.
func InsertTx(tx *sql.Tx, d NewDoc, now string) (string, error) {
	if d.ID == "" {
		d.ID = db.NewID("doc")
	}
	if d.Status == "" {
		d.Status = "draft"
	}
	var parent any
	if d.ParentID != "" {
		parent = d.ParentID
	}
	include := 1
	if d.ExcludeFromCompile {
		include = 0
	}
	if _, err := tx.Exec(`INSERT INTO documents(id, project_id, parent_id, kind, title, content, content_hash, synopsis_short, status,
		position, revision, word_count, char_count, content_at, content_by, created_at, updated_at,
		synopsis_long, pov, location, story_date, story_date_end, target_words, include_in_compile)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		d.ID, d.ProjectID, parent, d.Kind, d.Title, d.Body.Content, d.Body.Hash, d.Synopsis, d.Status,
		d.Position, 1, d.Body.Words, d.Body.Chars, now, nullIfEmpty(d.By), now, now,
		d.SynopsisLong, d.Pov, d.Location, d.StoryDate, d.StoryEnd, d.TargetWords, include); err != nil {
		return "", err
	}
	if err := writeTextBody(tx, d.ID, d.ProjectID, d.Title, d.Synopsis, d.Body.Search); err != nil {
		return "", err
	}
	if err := d.Body.Plan.Apply(tx, d.ID, d.ProjectID, d.By, now); err != nil {
		return "", err
	}
	if strings.Contains(d.Body.Content, text.GhostOpen) {
		if err := altstore.SyncGhosts(tx, d.ID, d.ProjectID, d.Body.Content, d.By, now); err != nil {
			return "", err
		}
	}
	// Wiki links in the new text, and links elsewhere that were waiting for this title.
	if !d.DeferLinks {
		if err := afterCreate(tx, d.ProjectID, d.ID, d.Title, d.Body.Content, now); err != nil {
			return "", err
		}
	}
	return d.ID, nil
}
