// Package entities is the entity bible: characters, places, items,
// organisations and concepts, with aliases, typed relationships, custom fields,
// merge, and the project-scoped view of them (overrides, references, links).
//
// Every entity belongs to a Compendium, and every op that names an entity is
// authorized by the owning Compendium (RequireCompendiumVia): a project member
// who is not on the Compendium cannot read it, and someone who cannot see the
// Compendium gets the same 404 as for an entity that does not exist. Queries
// carry the Compendium id, so an id from another Compendium is simply not found.
//
// Effective entities (a Compendium's entity as one project sees it) are
// resolved in one place, scope.go, so a page cannot show the wrong version of a
// character.
package entities

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

const (
	maxName    = 200
	maxSummary = 1000
	maxBody    = 200 << 10 // bytes of Markdown in one entity
	maxAlias   = 200
	maxAliases = 100
	// maxAliasWords bounds how many words an alias may have. Mention matching
	// walks an alias word by word, so a very long one costs on every text it is
	// matched against; no real alias is longer than a short sentence.
	maxAliasWords = 12
	maxNotes      = 1000

	// ListDefault and ListMax bound a page of entities.
	ListDefault = 100
	ListMax     = 500

	// RedirectTTL is how long a merged entity's old id keeps resolving.
	RedirectTTL = 365 * 24 * time.Hour
)

var colorRe = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// Alias is another name an entity goes by.
type Alias struct {
	ID    string `json:"id" desc:"For removing it with entity.alias.remove."`
	Alias string `json:"alias"`
	Notes string `json:"notes"`
}

// Entity is a full entity sheet.
type Entity struct {
	ID           string            `json:"id"`
	CompendiumID string            `json:"compendium_id"`
	Kind         string            `json:"kind" enum:"character,place,item,org,concept"`
	Name         string            `json:"name"`
	SortName     string            `json:"sort_name" desc:"What lists sort by when it is not the name (a surname first). Empty means the name."`
	Summary      string            `json:"summary" desc:"One or two lines."`
	Body         string            `json:"body" desc:"Markdown."`
	Fields       map[string]string `json:"fields" desc:"Custom field values by key, as canonical strings (a number as 34, a date as YYYY-MM-DD)."`
	Aliases      []Alias           `json:"aliases"`
	Color        string            `json:"color" desc:"Empty, or #rrggbb."`
	Revision     int               `json:"revision" desc:"Increments on every change to the sheet itself; send it back as base_revision to refuse a stale write."`
	Trashed      bool              `json:"trashed"`
	TrashedAt    string            `json:"trashed_at,omitempty"`
	CreatedAt    string            `json:"created_at"`
	UpdatedAt    string            `json:"updated_at" desc:"Moves on any change, including aliases and relationships."`
	// RedirectedFrom is set when the id asked for was merged into this entity.
	RedirectedFrom string `json:"redirected_from,omitempty" desc:"Set when the id you asked for was merged into this entity; use this entity's id from now on."`
}

// Entry is an entity in a list: no body.
type Entry struct {
	ID        string            `json:"id"`
	Kind      string            `json:"kind" enum:"character,place,item,org,concept"`
	Name      string            `json:"name"`
	SortName  string            `json:"sort_name"`
	Summary   string            `json:"summary"`
	Aliases   []string          `json:"aliases"`
	Fields    map[string]string `json:"fields"`
	Color     string            `json:"color"`
	Trashed   bool              `json:"trashed"`
	UpdatedAt string            `json:"updated_at"`
}

// Item wraps one entity.
type Item struct {
	Item Entity `json:"item"`
}

const entityCols = `e.id, e.compendium_id, e.kind, e.name, e.sort_name, e.summary, e.body, e.fields_json, e.color, e.revision,
	e.trashed_at IS NOT NULL, COALESCE(e.trashed_at, ''), e.created_at, e.updated_at`

func scanEntity(row interface{ Scan(...any) error }) (Entity, error) {
	var e Entity
	var fields string
	err := row.Scan(&e.ID, &e.CompendiumID, &e.Kind, &e.Name, &e.SortName, &e.Summary, &e.Body, &fields, &e.Color, &e.Revision,
		&e.Trashed, &e.TrashedAt, &e.CreatedAt, &e.UpdatedAt)
	e.Fields = parseFields(fields)
	e.Aliases = []Alias{}
	return e, err
}

// load reads one entity of a Compendium with its aliases.
func load(ctx context.Context, q queryer, compendiumID, id string) (Entity, error) {
	e, err := scanEntity(q.QueryRowContext(ctx, `SELECT `+entityCols+` FROM entities e WHERE e.id = ? AND e.compendium_id = ?`, id, compendiumID))
	if errors.Is(err, sql.ErrNoRows) {
		return Entity{}, ops.NotFound("entity")
	}
	if err != nil {
		return Entity{}, err
	}
	rows, err := q.QueryContext(ctx, `SELECT id, alias, notes FROM entity_aliases WHERE entity_id = ? ORDER BY alias_key, id`, id)
	if err != nil {
		return Entity{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var a Alias
		if err := rows.Scan(&a.ID, &a.Alias, &a.Notes); err != nil {
			return Entity{}, err
		}
		e.Aliases = append(e.Aliases, a)
	}
	return e, rows.Err()
}

// ---- authorization ----------------------------------------------------------------------------------

// entityID is implemented by op inputs that name an entity.
type entityID interface{ EntityID() string }

// resolveEntity finds the Compendium that owns the entity an input names.
// A merged entity's old id resolves (for a year) to the Compendium it was in,
// so entity.get can answer with where it went; every other op then finds no
// entity and says not found.
func resolveEntity(ctx context.Context, env *ops.Env, in any) (string, bool) {
	ref, ok := in.(entityID)
	if !ok || env == nil || env.DB == nil {
		return "", false
	}
	var cid sql.NullString
	err := env.DB.Read().QueryRowContext(ctx, `SELECT compendium_id FROM entities WHERE id = ?`, ref.EntityID()).Scan(&cid)
	if err == nil {
		return cid.String, cid.Valid // a project-local entity has no Compendium: not reachable here
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return "", false
	}
	var found string
	if err := env.DB.Read().QueryRowContext(ctx, `SELECT compendium_id FROM entity_redirects WHERE from_id = ? AND created_at >= ?`,
		ref.EntityID(), db.Format(db.NowTime().Add(-RedirectTTL))).Scan(&found); err != nil {
		return "", false
	}
	return found, true
}

// via is RequireCompendiumVia with the entity resolver.
func via(role string) ops.Auth { return ops.RequireCompendiumVia(role, resolveEntity) }

// ---- text for search -----------------------------------------------------------------------------------

// syncText rewrites the entity's search row: name, aliases, summary and body as
// plain text. It runs in the writer's transaction after every change to
// anything the row holds (the entity, its aliases, a merge).
func syncText(tx *sql.Tx, id string) error {
	var cid sql.NullString
	var name, summary, body string
	if err := tx.QueryRow(`SELECT compendium_id, name, summary, body FROM entities WHERE id = ?`, id).Scan(&cid, &name, &summary, &body); err != nil {
		return err
	}
	if !cid.Valid {
		return nil
	}
	var aliases sql.NullString
	if err := tx.QueryRow(`SELECT group_concat(alias, char(10)) FROM entity_aliases WHERE entity_id = ?`, id).Scan(&aliases); err != nil {
		return err
	}
	_, err := tx.Exec(`INSERT INTO entity_text(entity_id, compendium_id, name, aliases, summary, body) VALUES (?,?,?,?,?,?)
		ON CONFLICT(entity_id) DO UPDATE SET compendium_id = excluded.compendium_id, name = excluded.name, aliases = excluded.aliases,
			summary = excluded.summary, body = excluded.body`,
		id, cid.String, name, aliases.String, text.Plain(summary), text.Plain(body))
	return err
}

// touch moves an entity's updated_at (an alias or a relationship changed).
func touch(tx *sql.Tx, id, now string) error {
	_, err := tx.Exec(`UPDATE entities SET updated_at = ? WHERE id = ?`, now, id)
	return err
}

// ---- collisions ------------------------------------------------------------------------------------------

// clash finds a live entity of the Compendium, other than except, whose name or
// alias folds to key. A name and an alias may not collide: a mention of that
// word would not say which entity it means.
func clash(ctx context.Context, q queryer, compendiumID, key, except string) (id, name, via string, found bool, err error) {
	err = q.QueryRowContext(ctx, `SELECT id, name, 'name' FROM entities WHERE compendium_id = ? AND trashed_at IS NULL AND name_key = ? AND id <> ?
		UNION ALL
		SELECT e.id, e.name, 'alias' FROM entity_aliases a JOIN entities e ON e.id = a.entity_id
		WHERE e.compendium_id = ? AND e.trashed_at IS NULL AND a.alias_key = ? AND e.id <> ? LIMIT 1`,
		compendiumID, key, except, compendiumID, key, except).Scan(&id, &name, &via)
	if errors.Is(err, sql.ErrNoRows) {
		return "", "", "", false, nil
	}
	return id, name, via, err == nil, err
}

func clashError(what, word, id, name, via string) *ops.Error {
	return ops.ConflictWith(fmt.Sprintf("%s %q is already used as the %s of %q in this Compendium. Rename one, or merge the two entities.", what, word, via, name),
		map[string]any{"entity_id": id, "name": name, "via": via})
}

// ---- inputs -----------------------------------------------------------------------------------------------------

func checkLine(field, v string, max int, required bool) error {
	switch {
	case v == "" && required:
		return ops.Invalid(field, "This cannot be empty.")
	case utf8.RuneCountInString(v) > max:
		return ops.Invalid(field, fmt.Sprintf("This is limited to %d characters.", max))
	case strings.IndexFunc(v, unicode.IsControl) >= 0:
		return ops.Invalid(field, "This cannot contain line breaks or control characters.")
	}
	return nil
}

func checkText(field, v string, maxBytes int) error {
	if len(v) > maxBytes {
		return ops.Invalid(field, fmt.Sprintf("This is limited to %d KB.", maxBytes>>10))
	}
	if strings.ContainsRune(v, 0) || !utf8.ValidString(v) {
		return ops.Invalid(field, "This is not valid text.")
	}
	return nil
}

func checkColor(c string) error {
	if c != "" && !colorRe.MatchString(c) {
		return ops.Invalid("color", "A colour is empty or #rrggbb.")
	}
	return nil
}

// CreateIn creates an entity.
type CreateIn struct {
	ID       string         `path:"id" json:"-" desc:"Compendium id."`
	Kind     string         `json:"kind" enum:"character,place,item,org,concept" desc:"Fixed once created."`
	Name     string         `json:"name" desc:"Up to 200 characters. Unique among the Compendium's live entities, ignoring case and spacing, and not equal to anyone's alias."`
	SortName string         `json:"sort_name,omitempty" desc:"What lists sort by when it is not the name."`
	Summary  string         `json:"summary,omitempty" desc:"One or two lines, up to 1,000 characters."`
	Body     string         `json:"body,omitempty" desc:"Markdown, up to 200 KB."`
	Fields   map[string]any `json:"fields,omitempty" desc:"Custom field values by key. Each key must be defined for the kind (entity.field.set); an empty value leaves a field unset."`
	Aliases  []string       `json:"aliases,omitempty" desc:"Other names, up to 100. An alias another live entity uses as a name or alias is refused."`
	Color    string         `json:"color,omitempty" desc:"#rrggbb."`
}

// CompendiumRef lets the registry authorize by membership.
func (in CreateIn) CompendiumRef() string { return in.ID }

// Validate normalises and checks the input.
func (in *CreateIn) Validate() error {
	if !contains(Kinds, in.Kind) {
		return ops.Invalid("kind", "Kind must be one of: "+strings.Join(Kinds, ", ")+".")
	}
	in.Name = oneSpace(in.Name)
	in.SortName = oneSpace(in.SortName)
	in.Summary = strings.TrimSpace(in.Summary)
	if err := checkLine("name", in.Name, maxName, true); err != nil {
		return err
	}
	if err := checkLine("sort_name", in.SortName, maxName, false); err != nil {
		return err
	}
	if err := checkText("summary", in.Summary, maxSummary*4); err != nil || utf8.RuneCountInString(in.Summary) > maxSummary {
		return ops.Invalid("summary", "The summary is limited to 1,000 characters.")
	}
	if err := checkText("body", in.Body, maxBody); err != nil {
		return err
	}
	if err := checkColor(in.Color); err != nil {
		return err
	}
	var err error
	in.Aliases, err = cleanAliases(in.Aliases)
	return err
}

// checkAliasWords refuses an alias of more than maxAliasWords words.
func checkAliasWords(field, a string) error {
	if len(strings.Fields(a)) > maxAliasWords {
		return ops.Invalid(field, "An alias is at most 12 words. Use the part people actually say.")
	}
	return nil
}

func cleanAliases(in []string) ([]string, error) {
	if len(in) > maxAliases {
		return nil, ops.Invalid("aliases", "An entity can have at most 100 aliases.")
	}
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, a := range in {
		a = strings.Join(strings.Fields(a), " ")
		if err := checkLine("aliases", a, maxAlias, true); err != nil {
			return nil, err
		}
		if err := checkAliasWords("aliases", a); err != nil {
			return nil, err
		}
		k := text.FoldTitle(a)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, a)
	}
	return out, nil
}

// oneSpace trims a one-line value and collapses every run of whitespace to one space.
func oneSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

func sortKey(name, sortName string) string {
	if sortName != "" {
		return text.FoldTitle(sortName)
	}
	return text.FoldTitle(name)
}

// Insert writes an entity and its aliases and search row in the caller's
// transaction. The input must have passed CreateIn.Validate. Used by the op and
// by bulk loaders.
func Insert(ctx context.Context, tx *sql.Tx, in CreateIn, now string) (string, error) {
	nameKey := text.FoldTitle(in.Name)
	if cid, name, v, found, err := clash(ctx, tx, in.ID, nameKey, ""); err != nil {
		return "", err
	} else if found {
		return "", clashError("The name", in.Name, cid, name, v)
	}
	d, err := loadDefs(ctx, tx, in.ID, in.Kind)
	if err != nil {
		return "", err
	}
	fields, err := applyFields(d, nil, in.Fields, false)
	if err != nil {
		return "", err
	}
	id := db.NewID("ent")
	if _, err := tx.Exec(`INSERT INTO entities(id, compendium_id, kind, name, name_key, sort_name, sort_key, summary, body, fields_json, color, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`, id, in.ID, in.Kind, in.Name, nameKey, in.SortName, sortKey(in.Name, in.SortName),
		in.Summary, in.Body, marshalFields(fields), in.Color, now, now); err != nil {
		if db.IsUniqueViolation(err) {
			return "", ops.Conflict("An entity with that name already exists in this Compendium.")
		}
		return "", err
	}
	for _, a := range in.Aliases {
		if err := insertAlias(ctx, tx, in.ID, id, a, "", now); err != nil {
			return "", err
		}
	}
	return id, syncText(tx, id)
}

func create(c *ops.Ctx, in CreateIn) (Item, error) {
	var out Entity
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		id, err := Insert(c, tx, in, db.Now())
		if err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "entity.created", CompendiumID: in.ID, Payload: map[string]any{"entity_id": id, "kind": in.Kind}})
		if err := c.FlushTo(tx); err != nil {
			return err
		}
		out, err = load(c, tx, in.ID, id)
		return err
	})
	if err != nil {
		return Item{}, err
	}
	return Item{Item: out}, nil
}

// ---- get -------------------------------------------------------------------------------------------------------------

// GetIn names one entity.
type GetIn struct {
	ID string `path:"id" json:"-" desc:"Entity id. An id that was merged into another entity still answers, with redirected_from set, for a year."`
}

// EntityID lets the registry find the owning Compendium.
func (in GetIn) EntityID() string { return in.ID }

// Relation is a relationship as read from one entity.
type Relation struct {
	ID        string `json:"id" desc:"For removing it with entity.relation.remove."`
	Direction string `json:"direction" enum:"out,in,both" desc:"out: this entity is the source; in: it is the target; both: a symmetric relationship."`
	OtherID   string `json:"other_id"`
	OtherName string `json:"other_name"`
	Kind      string `json:"kind" enum:"family,ally,rival,mentor,romantic,member,custom"`
	Label     string `json:"label" desc:"How the relationship reads from this entity: \"mother of\" on the source, the inverse label on the target."`
	Notes     string `json:"notes"`
	CreatedAt string `json:"created_at"`
}

// Detail is an entity with its relationships.
type Detail struct {
	Entity
	Relations          []Relation `json:"relations" desc:"Relationships with live entities, up to 500."`
	RelationsTruncated bool       `json:"relations_truncated,omitempty"`
}

// DetailItem wraps a Detail.
type DetailItem struct {
	Item Detail `json:"item"`
}

func get(c *ops.Ctx, in GetIn) (DetailItem, error) {
	q := c.DB().Read()
	var cid sql.NullString
	id := in.ID
	redirected := ""
	if err := q.QueryRowContext(c, `SELECT compendium_id FROM entities WHERE id = ?`, id).Scan(&cid); err != nil {
		if !errors.Is(err, sql.ErrNoRows) {
			return DetailItem{}, err
		}
		var to string
		if err := q.QueryRowContext(c, `SELECT to_id FROM entity_redirects WHERE from_id = ? AND created_at >= ?`, id, db.Format(db.NowTime().Add(-RedirectTTL))).Scan(&to); err != nil {
			return DetailItem{}, ops.NotFound("entity")
		}
		redirected, id = in.ID, to
		if err := q.QueryRowContext(c, `SELECT compendium_id FROM entities WHERE id = ?`, id).Scan(&cid); err != nil {
			return DetailItem{}, ops.NotFound("entity")
		}
	}
	// The registry authorized the Compendium the asked-for id lives in; a
	// redirect target is in the same one (a merge never crosses Compendiums).
	e, err := load(c, q, cid.String, id)
	if err != nil {
		return DetailItem{}, err
	}
	e.RedirectedFrom = redirected
	rels, truncated, err := loadRelations(c, q, cid.String, id)
	if err != nil {
		return DetailItem{}, err
	}
	return DetailItem{Item: Detail{Entity: e, Relations: rels, RelationsTruncated: truncated}}, nil
}

// ---- list ---------------------------------------------------------------------------------------------------------------

// ListIn lists a Compendium's entities.
type ListIn struct {
	ID     string `path:"id" json:"-" desc:"Compendium id."`
	Kind   string `query:"kind" desc:"Only one kind." enum:"character,place,item,org,concept"`
	Q      string `query:"q" desc:"Names and aliases containing these characters, ignoring case."`
	Name   string `query:"name" desc:"The entity whose name or alias is exactly this, ignoring case and spacing: the way to turn a name into an id."`
	State  string `query:"state" desc:"live (default), trashed or all." enum:"live,trashed,all"`
	Limit  int    `query:"limit" desc:"Page size, 1 to 500; default 100."`
	Cursor string `query:"cursor" desc:"next_cursor from the previous page."`
}

// CompendiumRef lets the registry authorize by membership.
func (in ListIn) CompendiumRef() string { return in.ID }

// Validate defaults and checks.
func (in *ListIn) Validate() error {
	if in.Kind != "" && !contains(Kinds, in.Kind) {
		return ops.Invalid("kind", "Kind must be one of: "+strings.Join(Kinds, ", ")+".")
	}
	switch in.State {
	case "":
		in.State = "live"
	case "live", "trashed", "all":
	default:
		return ops.Invalid("state", "State must be live, trashed or all.")
	}
	switch {
	case in.Limit <= 0:
		in.Limit = ListDefault
	case in.Limit > ListMax:
		in.Limit = ListMax
	}
	if len(in.Q) > 200 || len(in.Name) > 400 {
		return ops.Invalid("q", "That is too long.")
	}
	return nil
}

// ListOut is a page of entities in sort order.
type ListOut struct {
	Items      []Entry `json:"items"`
	Total      int     `json:"total" desc:"Every entity matching the filters, not just this page."`
	NextCursor string  `json:"next_cursor,omitempty" desc:"Pass as cursor for the next page; absent on the last."`
}

type cursor struct {
	K  string `json:"k"`
	ID string `json:"i"`
}

func list(c *ops.Ctx, in ListIn) (ListOut, error) {
	where := ` WHERE e.compendium_id = ?`
	args := []any{in.ID}
	switch in.State {
	case "live":
		where += ` AND e.trashed_at IS NULL`
	case "trashed":
		where += ` AND e.trashed_at IS NOT NULL`
	}
	if in.Kind != "" {
		where += ` AND e.kind = ?`
		args = append(args, in.Kind)
	}
	if k := text.FoldTitle(in.Name); k != "" {
		where += ` AND (e.name_key = ? OR EXISTS (SELECT 1 FROM entity_aliases a WHERE a.entity_id = e.id AND a.alias_key = ?))`
		args = append(args, k, k)
	}
	if k := text.FoldTitle(in.Q); k != "" {
		pat := "%" + likeEscape(k) + "%"
		where += ` AND (e.name_key LIKE ? ESCAPE '\' OR EXISTS (SELECT 1 FROM entity_aliases a WHERE a.entity_id = e.id AND a.alias_key LIKE ? ESCAPE '\'))`
		args = append(args, pat, pat)
	}
	out := ListOut{Items: []Entry{}}
	if err := c.DB().Read().QueryRowContext(c, `SELECT count(*) FROM entities e`+where, args...).Scan(&out.Total); err != nil {
		return ListOut{}, err
	}
	page := where
	pargs := append([]any{}, args...)
	if in.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(in.Cursor)
		var cur cursor
		if err != nil || json.Unmarshal(raw, &cur) != nil {
			return ListOut{}, ops.Invalid("cursor", "That cursor is not valid.")
		}
		page += ` AND (e.sort_key > ? OR (e.sort_key = ? AND e.id > ?))`
		pargs = append(pargs, cur.K, cur.K, cur.ID)
	}
	rows, err := c.DB().Read().QueryContext(c, `SELECT e.id, e.kind, e.name, e.sort_name, e.sort_key, e.summary, e.fields_json, e.color, e.trashed_at IS NOT NULL, e.updated_at
		FROM entities e`+page+` ORDER BY e.sort_key, e.id LIMIT ?`, append(pargs, in.Limit+1)...)
	if err != nil {
		return ListOut{}, err
	}
	var keys []string
	byID := map[string]*Entry{}
	for rows.Next() {
		var e Entry
		var key, fields string
		if err := rows.Scan(&e.ID, &e.Kind, &e.Name, &e.SortName, &key, &e.Summary, &fields, &e.Color, &e.Trashed, &e.UpdatedAt); err != nil {
			rows.Close()
			return ListOut{}, err
		}
		e.Fields, e.Aliases = parseFields(fields), []string{}
		out.Items = append(out.Items, e)
		keys = append(keys, key)
	}
	if err := rows.Close(); err != nil {
		return ListOut{}, err
	}
	if len(out.Items) > in.Limit {
		out.Items = out.Items[:in.Limit]
		raw, _ := json.Marshal(cursor{K: keys[in.Limit-1], ID: out.Items[in.Limit-1].ID})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	for i := range out.Items {
		byID[out.Items[i].ID] = &out.Items[i]
	}
	return out, fillAliases(c, c.DB().Read(), out.Items, byID)
}

// fillAliases loads the alias names of a page of entries in one query.
func fillAliases(ctx context.Context, q queryer, items []Entry, byID map[string]*Entry) error {
	if len(items) == 0 {
		return nil
	}
	ids := make([]any, len(items))
	ph := make([]string, len(items))
	for i, e := range items {
		ids[i], ph[i] = e.ID, "?"
	}
	rows, err := q.QueryContext(ctx, `SELECT entity_id, alias FROM entity_aliases WHERE entity_id IN (`+strings.Join(ph, ",")+`) ORDER BY alias_key, id`, ids...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id, alias string
		if err := rows.Scan(&id, &alias); err != nil {
			return err
		}
		if e := byID[id]; e != nil {
			e.Aliases = append(e.Aliases, alias)
		}
	}
	return rows.Err()
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// ---- update -----------------------------------------------------------------------------------------------------------------

// UpdateIn changes an entity. A nil field is left alone.
type UpdateIn struct {
	ID           string         `path:"id" json:"-" desc:"Entity id."`
	BaseRevision *int           `json:"base_revision,omitempty" desc:"The revision you last saw; a different current revision is refused with 409 and the current one."`
	Name         *string        `json:"name,omitempty"`
	SortName     *string        `json:"sort_name,omitempty"`
	Summary      *string        `json:"summary,omitempty"`
	Body         *string        `json:"body,omitempty" desc:"Markdown."`
	Fields       map[string]any `json:"fields,omitempty" desc:"Custom field values to set by key; the rest stay. An empty value clears that field."`
	Color        *string        `json:"color,omitempty" desc:"#rrggbb, or empty for none."`
}

// EntityID lets the registry find the owning Compendium.
func (in UpdateIn) EntityID() string { return in.ID }

// Validate trims and checks every field that was sent.
func (in *UpdateIn) Validate() error {
	trim := func(p **string, f func(string) string) {
		if *p != nil {
			v := f(**p)
			*p = &v
		}
	}
	trim(&in.Name, oneSpace)
	trim(&in.SortName, oneSpace)
	trim(&in.Summary, strings.TrimSpace)
	if in.Name != nil {
		if err := checkLine("name", *in.Name, maxName, true); err != nil {
			return err
		}
	}
	if in.SortName != nil {
		if err := checkLine("sort_name", *in.SortName, maxName, false); err != nil {
			return err
		}
	}
	if in.Summary != nil {
		if err := checkText("summary", *in.Summary, maxSummary*4); err != nil || utf8.RuneCountInString(*in.Summary) > maxSummary {
			return ops.Invalid("summary", "The summary is limited to 1,000 characters.")
		}
	}
	if in.Body != nil {
		if err := checkText("body", *in.Body, maxBody); err != nil {
			return err
		}
	}
	if in.Color != nil {
		if err := checkColor(*in.Color); err != nil {
			return err
		}
	}
	return nil
}

func update(c *ops.Ctx, in UpdateIn) (Item, error) {
	var out Entity
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		cid, ok := ownerOf(c, tx, in.ID)
		if !ok {
			return ops.NotFound("entity")
		}
		cur, err := load(c, tx, cid, in.ID)
		if err != nil {
			return err
		}
		if in.BaseRevision != nil && *in.BaseRevision != cur.Revision {
			return ops.ConflictWith("This entity changed since you read it. Read it again and re-apply your change.", map[string]any{"revision": cur.Revision})
		}
		name, sortName, summary, body, color := cur.Name, cur.SortName, cur.Summary, cur.Body, cur.Color
		if in.Name != nil {
			name = *in.Name
		}
		if in.SortName != nil {
			sortName = *in.SortName
		}
		if in.Summary != nil {
			summary = *in.Summary
		}
		if in.Body != nil {
			body = *in.Body
		}
		if in.Color != nil {
			color = *in.Color
		}
		fields := cur.Fields
		if len(in.Fields) > 0 {
			d, err := loadDefs(c, tx, cid, cur.Kind)
			if err != nil {
				return err
			}
			if fields, err = applyFields(d, cur.Fields, in.Fields, false); err != nil {
				return err
			}
		}
		fj := marshalFields(fields)
		if name == cur.Name && sortName == cur.SortName && summary == cur.Summary && body == cur.Body && color == cur.Color && fj == marshalFields(cur.Fields) {
			out = cur
			return nil
		}
		nameKey := text.FoldTitle(name)
		if name != cur.Name && !cur.Trashed {
			if oid, oname, v, found, err := clash(c, tx, cid, nameKey, in.ID); err != nil {
				return err
			} else if found {
				return clashError("The name", name, oid, oname, v)
			}
		}
		now := db.Now()
		if _, err := tx.Exec(`UPDATE entities SET name = ?, name_key = ?, sort_name = ?, sort_key = ?, summary = ?, body = ?, fields_json = ?, color = ?,
			revision = revision + 1, updated_at = ? WHERE id = ?`, name, nameKey, sortName, sortKey(name, sortName), summary, body, fj, color, now, in.ID); err != nil {
			if db.IsUniqueViolation(err) {
				return ops.Conflict("An entity with that name already exists in this Compendium.")
			}
			return err
		}
		if err := syncText(tx, in.ID); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "entity.updated", CompendiumID: cid, Payload: map[string]any{"entity_id": in.ID}})
		if err := c.FlushTo(tx); err != nil {
			return err
		}
		out, err = load(c, tx, cid, in.ID)
		return err
	})
	if err != nil {
		return Item{}, err
	}
	return Item{Item: out}, nil
}

// ownerOf finds an entity's Compendium inside the writer's transaction. An
// entity id that was merged away is not an entity: only entity.get follows it.
func ownerOf(ctx context.Context, q queryer, id string) (string, bool) {
	var cid sql.NullString
	if err := q.QueryRowContext(ctx, `SELECT compendium_id FROM entities WHERE id = ?`, id).Scan(&cid); err != nil {
		return "", false
	}
	return cid.String, cid.Valid
}

// ---- trash and restore ---------------------------------------------------------------------------------------------------------

// StateIn names one entity for trash or restore.
type StateIn struct {
	ID string `path:"id" json:"-" desc:"Entity id."`
}

// EntityID lets the registry find the owning Compendium.
func (in StateIn) EntityID() string { return in.ID }

func trash(c *ops.Ctx, in StateIn) (Item, error) { return setTrashed(c, in, true) }

func restore(c *ops.Ctx, in StateIn) (Item, error) { return setTrashed(c, in, false) }

// setTrashed trashes or restores an entity. Trashing frees its name and
// aliases for other entities; restoring takes them back, and is refused if
// someone else has used one in the meantime. It is idempotent.
func setTrashed(c *ops.Ctx, in StateIn, on bool) (Item, error) {
	var out Entity
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		cid, ok := ownerOf(c, tx, in.ID)
		if !ok {
			return ops.NotFound("entity")
		}
		cur, err := load(c, tx, cid, in.ID)
		if err != nil {
			return err
		}
		if cur.Trashed == on {
			out = cur
			return nil
		}
		now := db.Now()
		if on {
			_, err = tx.Exec(`UPDATE entities SET trashed_at = ?, updated_at = ? WHERE id = ?`, now, now, in.ID)
		} else {
			keys := []string{text.FoldTitle(cur.Name)}
			for _, a := range cur.Aliases {
				keys = append(keys, text.FoldTitle(a.Alias))
			}
			for _, k := range keys {
				if oid, oname, v, found, cerr := clash(c, tx, cid, k, in.ID); cerr != nil {
					return cerr
				} else if found {
					return clashError("Restoring this entity would reuse", k, oid, oname, v)
				}
			}
			_, err = tx.Exec(`UPDATE entities SET trashed_at = NULL, updated_at = ? WHERE id = ?`, now, in.ID)
		}
		if err != nil {
			return err
		}
		typ, action := "entity.restored", "entity.restore"
		if on {
			typ, action = "entity.trashed", "entity.trash"
		}
		c.Emit(ops.Event{Type: typ, CompendiumID: cid, Payload: map[string]any{"entity_id": in.ID}})
		c.Audit(ops.AuditEntry{Action: action, TargetType: "entity", TargetID: in.ID})
		if err := c.FlushTo(tx); err != nil {
			return err
		}
		out, err = load(c, tx, cid, in.ID)
		return err
	})
	if err != nil {
		return Item{}, err
	}
	return Item{Item: out}, nil
}

// Register adds the entity ops.
func Register(r *ops.Registry) {
	ops.Register(r, ops.Op{
		Name: "entity.create", Method: "POST", Path: "/api/v1/compendiums/{id}/entities", Status: 201,
		Desc: "Create an entity (character, place, item, org or concept) in a Compendium, with a summary, a Markdown body, typed custom fields and aliases. " +
			"The name is unique among the Compendium's live entities ignoring case and spacing, and no alias may equal another entity's name or alias; a clash is a 409 that names the other entity. Editor.",
		Auth: ops.RequireCompendium(compaccess.Editor),
	}, create)
	ops.Register(r, ops.Op{
		Name: "entity.list", Method: "GET", Path: "/api/v1/compendiums/{id}/entities",
		Desc: "List a Compendium's entities in sort order, a page at a time, without bodies. Filter by kind, by q (names and aliases containing it) or by name (the entity called exactly this, by name or alias: how to turn a name into an id). Trashed ones with state=trashed.",
		Auth: ops.RequireCompendium(compaccess.Reader),
	}, list)
	ops.Register(r, ops.Op{
		Name: "entity.get", Method: "GET", Path: "/api/v1/entities/{id}",
		Desc: "Get one entity sheet with its aliases, custom fields and relationships. An id that was merged into another entity answers with the target and redirected_from, for a year.",
		Auth: via(compaccess.Reader),
	}, get)
	ops.Register(r, ops.Op{
		Name: "entity.update", Method: "PATCH", Path: "/api/v1/entities/{id}",
		Desc: "Change an entity's name, sort name, summary, body, colour or custom field values. Send only what changes. The kind is fixed. Pass base_revision to refuse a stale write. Editor.",
		Auth: via(compaccess.Editor),
	}, update)
	ops.Register(r, ops.Op{
		Name: "entity.trash", Method: "POST", Path: "/api/v1/entities/{id}/trash",
		Desc: "Move an entity to the trash: it leaves lists, search, projects and the mirror, and its name and aliases are free to reuse. Nothing is deleted. Editor.",
		Auth: via(compaccess.Editor),
	}, trash)
	ops.Register(r, ops.Op{
		Name: "entity.restore", Method: "POST", Path: "/api/v1/entities/{id}/restore",
		Desc: "Take an entity out of the trash. Refused with a 409 naming the other entity if its name or an alias has been used since. Editor.",
		Auth: via(compaccess.Editor),
	}, restore)
	registerAliases(r)
	registerRelations(r)
	registerFields(r)
	registerMerge(r)
	registerScope(r)
	registerReferenced(r)
	registerExport(r)
}
