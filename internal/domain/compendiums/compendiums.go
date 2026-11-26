// Package compendiums holds the Compendium ops: create, list, get, update,
// the trash/archive/purge lifecycle, membership, and the links between a
// Compendium and projects (the primary, and references).
//
// A Compendium is a first-class library item that exists with or without a
// project. Access to it is its own (compaccess): a project member gets nothing
// from being on the project.
package compendiums

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/keys"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// Kinds mirror the CHECK constraint in migration 1500.
var Kinds = []string{"fiction", "nonfiction"}

const (
	maxName        = 200
	maxDescription = 5000
)

// Compendium is the wire shape of a Compendium. Role describes the caller.
type Compendium struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Name        string `json:"name"`
	Kind        string `json:"kind" enum:"fiction,nonfiction"`
	Description string `json:"description"`
	Role        string `json:"role" desc:"The caller's role: owner, editor or reader. An instance admin acts as owner."`

	EntityCount int `json:"entity_count" desc:"Live entities."`

	Archived   bool   `json:"archived"`
	ArchivedAt string `json:"archived_at,omitempty"`
	Trashed    bool   `json:"trashed"`
	TrashedAt  string `json:"trashed_at,omitempty"`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Item wraps one Compendium.
type Item struct {
	Item Compendium `json:"item"`
}

// ---- reading ----------------------------------------------------------------------

const compendiumCols = `c.id, c.slug, c.name, c.kind, c.description,
	COALESCE(m.role, CASE WHEN u.role = 'admin' THEN 'owner' END, ''),
	(SELECT count(*) FROM entities e WHERE e.compendium_id = c.id AND e.trashed_at IS NULL),
	c.archived_at IS NOT NULL, COALESCE(c.archived_at, ''), c.trashed_at IS NOT NULL, COALESCE(c.trashed_at, ''),
	c.created_at, c.updated_at`

const compendiumFrom = ` FROM compendiums c
	JOIN users u ON u.id = ?1
	LEFT JOIN compendium_members m ON m.compendium_id = c.id AND m.user_id = u.id`

func scan(row interface{ Scan(...any) error }) (Compendium, error) {
	var c Compendium
	err := row.Scan(&c.ID, &c.Slug, &c.Name, &c.Kind, &c.Description, &c.Role, &c.EntityCount,
		&c.Archived, &c.ArchivedAt, &c.Trashed, &c.TrashedAt, &c.CreatedAt, &c.UpdatedAt)
	return c, err
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// load reads one Compendium as userID sees it. Trashed and archived ones load:
// the trash view needs them. Anyone without access gets not found.
func load(ctx context.Context, q queryRower, userID, id string) (Compendium, error) {
	c, err := scan(q.QueryRowContext(ctx, `SELECT `+compendiumCols+compendiumFrom+`
		WHERE c.id = ?2 AND c.deleted_at IS NULL AND u.deleted_at IS NULL AND (m.user_id IS NOT NULL OR u.role = 'admin')`, userID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Compendium{}, ops.NotFound("compendium")
	}
	return c, err
}

// ---- create ---------------------------------------------------------------------------

// CreateIn creates a Compendium.
type CreateIn struct {
	Name          string `json:"name" desc:"Up to 200 characters."`
	Kind          string `json:"kind,omitempty" enum:"fiction,nonfiction" desc:"Default fiction."`
	Description   string `json:"description,omitempty" desc:"Up to 5,000 characters of Markdown."`
	ProjectID     string `json:"project_id,omitempty" desc:"Make this the primary Compendium of a project the caller owns that has none yet."`
	StarterFields *bool  `json:"starter_fields,omitempty" desc:"Define the starter custom fields (a character's role, age, appearance, voice, wants, fears, secrets and arc; a place's region; and so on). Default true."`
}

// Validate normalises and checks the input.
func (in *CreateIn) Validate() error {
	in.Name = strings.TrimSpace(in.Name)
	if err := checkName(in.Name); err != nil {
		return err
	}
	if in.Kind == "" {
		in.Kind = "fiction"
	}
	if !contains(Kinds, in.Kind) {
		return ops.Invalid("kind", "A Compendium is fiction or nonfiction.")
	}
	in.Description = strings.TrimSpace(in.Description)
	if utf8.RuneCountInString(in.Description) > maxDescription {
		return ops.Invalid("description", "The description is limited to 5,000 characters.")
	}
	return nil
}

func checkName(n string) error {
	switch {
	case n == "":
		return ops.Invalid("name", "Give the Compendium a name.")
	case utf8.RuneCountInString(n) > maxName:
		return ops.Invalid("name", "The name is limited to 200 characters.")
	case strings.IndexFunc(n, unicode.IsControl) >= 0:
		return ops.Invalid("name", "The name cannot contain line breaks or control characters.")
	}
	return nil
}

// ListIn lists the caller's Compendiums.
type ListIn struct {
	Q     string `query:"q" desc:"Filter by name (case-insensitive substring)."`
	State string `query:"state" desc:"active (default), archived, trashed or all. The trash view lists only Compendiums the caller owns." enum:"active,archived,trashed,all"`
}

// States a list can show.
const (
	StateActive   = "active"
	StateArchived = "archived"
	StateTrashed  = "trashed"
	StateAll      = "all"
)

// ListCap is the most Compendiums one list returns.
const ListCap = 500

// Validate defaults and checks the state.
func (in *ListIn) Validate() error {
	switch in.State {
	case "":
		in.State = StateActive
	case StateActive, StateArchived, StateTrashed, StateAll:
	default:
		return ops.Invalid("state", "State must be active, archived, trashed or all.")
	}
	return nil
}

// ListOut is the Compendium list.
type ListOut struct {
	Items     []Compendium `json:"items"`
	Truncated bool         `json:"truncated,omitempty" desc:"True when more than 500 matched and the rest were left out."`
}

// GetIn names one Compendium.
type GetIn struct {
	ID string `path:"id" json:"-" desc:"Compendium id."`
}

// CompendiumRef lets the registry authorize by membership.
func (in GetIn) CompendiumRef() string { return in.ID }

// ProjectUse is one project that uses the Compendium.
type ProjectUse struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Relation string `json:"relation" enum:"primary,reference" desc:"primary: it is the project's primary Compendium. reference: the project references the whole Compendium."`
}

// Detail is a Compendium with what depends on it.
type Detail struct {
	Compendium
	MemberCount    int          `json:"member_count"`
	Projects       []ProjectUse `json:"projects" desc:"Projects the caller can open that use this Compendium. Deleting the Compendium removes it from all of them."`
	HiddenProjects int          `json:"hidden_projects" desc:"Projects that use it and that the caller cannot open. Counted, never named."`
	References     []Reference  `json:"references" desc:"Other Compendiums this one references, read-only. Only those the caller can read are listed; the rest are counted in hidden_references."`
	HiddenRefs     int          `json:"hidden_references"`
	ReferencedBy   int          `json:"referenced_by" desc:"How many other Compendiums reference this one."`
}

// Reference is a whole-Compendium reference.
type Reference struct {
	ID           string `json:"id" desc:"The reference id, for removing it."`
	CompendiumID string `json:"compendium_id"`
	Name         string `json:"name"`
	Mode         string `json:"mode" enum:"reference"`
}

// DetailItem wraps a Detail.
type DetailItem struct {
	Item Detail `json:"item"`
}

// Register adds the Compendium ops.
func Register(r *ops.Registry) {
	registerManage(r)
	// A person may pin a key to a Compendium they can read. The key acts as
	// them, so it can never do more there than they can; its scope narrows it
	// further.
	keys.CompendiumAccess = func(ctx context.Context, d *db.DB, userID, compendiumID string) bool {
		return compaccess.CanRead(ctx, d.Read(), userID, compendiumID)
	}
	ops.Register(r, ops.Op{
		Name: "compendium.create", Method: "POST", Path: "/api/v1/compendiums", Status: 201,
		Desc: "Create a Compendium: the shared Compendium (entities, and later research and a timeline) for one story world or subject. It exists with or without a project. " +
			"The caller becomes its owner. Pass project_id to make it the primary Compendium of a project the caller owns that has none; a project has at most one primary.",
		Auth: ops.Writer(),
	}, create)
	ops.Register(r, ops.Op{
		Name: "compendium.list", Method: "GET", Path: "/api/v1/compendiums",
		Desc: "List the Compendiums the caller is a member of, most recently changed first. The default state is active (neither archived nor trashed). Returns at most 500; narrow with q or state.",
		Auth: ops.Authenticated(),
	}, list)
	ops.Register(r, ops.Op{
		Name: "compendium.get", Method: "GET", Path: "/api/v1/compendiums/{id}",
		Desc: "Get one Compendium with what depends on it: the projects that use it (named only when the caller can open them), the Compendiums it references and how many reference it, and the member count.",
		Auth: ops.RequireCompendium(compaccess.Reader),
	}, get)
	ops.Register(r, ops.Op{
		Name: "project.compendium.create", Method: "POST", Path: "/api/v1/projects/{id}/compendium", Status: 201,
		Desc: "Create a Compendium for a project and make it the project's primary, in one step. The project must have none yet and the caller must own it. " +
			"This is how a key restricted to one project builds a Compendium: the key reaches the project's primary Compendium, so it can then add entities with entity.create. " +
			"An unrestricted caller can use compendium.create with project_id instead.",
		Auth: ops.RequireProject(ops.ProjectOwner),
	}, createForProject)
	registerLifecycle(r)
	registerMembers(r)
	registerRefs(r)
}

func create(c *ops.Ctx, in CreateIn) (Item, error) { return createCompendium(c, in, true) }

// createCompendium makes a Compendium and, when in.ProjectID is set, attaches it
// as that project's primary. checkOwner makes it verify the caller owns the
// project; project.compendium.create has already authorized that through the
// project's own auth (which also holds a project-pinned key to its project).
func createCompendium(c *ops.Ctx, in CreateIn, checkOwner bool) (Item, error) {
	var out Compendium
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		if in.ProjectID != "" && checkOwner {
			if err := requireProjectOwner(c, in.ProjectID); err != nil {
				return err
			}
			// compendium.create names the project in its body, so the registry did not see it: a project in the
			// trash is read-only (project.compendium.create is refused before it gets here).
			var trashed int
			if err := tx.QueryRowContext(c, `SELECT count(*) FROM projects WHERE id = ? AND trashed_at IS NOT NULL`, in.ProjectID).Scan(&trashed); err != nil {
				return err
			}
			if trashed > 0 {
				return ops.ProjectInTrash()
			}
		}
		id, err := InsertTx(c, tx, in, db.Now())
		if err != nil {
			return err
		}
		if err := c.FlushTo(tx); err != nil {
			return err
		}
		out, err = load(c, tx, c.P.UserID, id)
		return err
	})
	if err != nil {
		return Item{}, err
	}
	return Item{Item: out}, nil
}

// InsertTx makes a Compendium owned by the caller in the caller's transaction
// and, when in.ProjectID is set, makes it that project's primary (the project
// must have none). The caller has authorized the project and validated in. An
// import uses it to give a new project the bible its character sheets go into.
func InsertTx(c *ops.Ctx, tx *sql.Tx, in CreateIn, now string) (string, error) {
	id := db.NewID("cmp")
	slug, err := freeSlug(tx, c.P.UserID, projects.Slugify(in.Name))
	if err != nil {
		return "", err
	}
	if in.ProjectID != "" {
		var primary sql.NullString
		if err := tx.QueryRow(`SELECT compendium_id FROM projects WHERE id = ? AND deleted_at IS NULL`, in.ProjectID).Scan(&primary); err != nil {
			return "", ops.NotFound("project")
		}
		if primary.Valid {
			return "", ops.Conflict("That project already has a primary Compendium. A project has at most one; change it with project.compendium.set.")
		}
	}
	if in.Kind == "" {
		in.Kind = "fiction"
	}
	if _, err := tx.Exec(`INSERT INTO compendiums(id, owner_id, name, slug, kind, description, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?)`,
		id, c.P.UserID, in.Name, slug, in.Kind, in.Description, now, now); err != nil {
		return "", err
	}
	if _, err := tx.Exec(`INSERT INTO compendium_members(compendium_id, user_id, role, created_at) VALUES (?,?,?,?)`,
		id, c.P.UserID, compaccess.Owner, now); err != nil {
		return "", err
	}
	if in.StarterFields == nil || *in.StarterFields {
		if err := entities.SeedStarterFields(tx, id, now); err != nil {
			return "", err
		}
	}
	ev := ops.Event{Type: "compendium.created", CompendiumID: id, Payload: map[string]any{"kind": in.Kind}}
	if in.ProjectID != "" {
		if _, err := tx.Exec(`UPDATE projects SET compendium_id = ? WHERE id = ?`, id, in.ProjectID); err != nil {
			return "", err
		}
		ev.ProjectID = in.ProjectID
		ev.Payload["project_id"] = in.ProjectID
	}
	c.Emit(ev)
	c.Audit(ops.AuditEntry{Action: "compendium.create", TargetType: "compendium", TargetID: id})
	return id, nil
}

// ProjectCreateIn creates a project's primary Compendium.
type ProjectCreateIn struct {
	ID            string `path:"id" json:"-" desc:"Project id."`
	Name          string `json:"name" desc:"Up to 200 characters."`
	Kind          string `json:"kind,omitempty" enum:"fiction,nonfiction" desc:"Default fiction."`
	Description   string `json:"description,omitempty" desc:"Up to 5,000 characters of Markdown."`
	StarterFields *bool  `json:"starter_fields,omitempty" desc:"Define the starter custom fields. Default true."`
}

// ProjectRef lets the registry authorize by membership.
func (in ProjectCreateIn) ProjectRef() string { return in.ID }

// Validate normalises and checks the input, as compendium.create does.
func (in *ProjectCreateIn) Validate() error {
	c := CreateIn{Name: in.Name, Kind: in.Kind, Description: in.Description}
	if err := c.Validate(); err != nil {
		return err
	}
	in.Name, in.Kind, in.Description = c.Name, c.Kind, c.Description
	return nil
}

func createForProject(c *ops.Ctx, in ProjectCreateIn) (Item, error) {
	return createCompendium(c, CreateIn{Name: in.Name, Kind: in.Kind, Description: in.Description, ProjectID: in.ID, StarterFields: in.StarterFields}, false)
}

// requireProjectOwner checks the caller owns the project, with the same 404 as
// for a project they cannot see. Pinned keys are refused: wiring a project to a
// Compendium changes who can be shown what.
func requireProjectOwner(c *ops.Ctx, projectID string) error {
	if c.P.Pinned() {
		return ops.Forbidden("a key restricted to one project or compendium cannot attach Compendiums")
	}
	if c.Env.ProjectRole == nil {
		return ops.NotFound("project")
	}
	role, ok := c.Env.ProjectRole(c, c.P.UserID, projectID)
	if !ok {
		return ops.NotFound("project")
	}
	if role != ops.ProjectOwner {
		return ops.Forbidden("only the project's owner can do this")
	}
	return nil
}

func list(c *ops.Ctx, in ListIn) (ListOut, error) {
	q := `SELECT ` + compendiumCols + compendiumFrom + ` WHERE m.user_id IS NOT NULL AND c.deleted_at IS NULL`
	args := []any{c.P.UserID}
	switch in.State {
	case StateActive:
		q += ` AND c.trashed_at IS NULL AND c.archived_at IS NULL`
	case StateArchived:
		q += ` AND c.trashed_at IS NULL AND c.archived_at IS NOT NULL`
	case StateTrashed:
		q += ` AND c.trashed_at IS NOT NULL AND m.role = 'owner'` // only the owner can empty or restore the trash
	case StateAll:
		q += ` AND (c.trashed_at IS NULL OR m.role = 'owner')`
	}
	if f := strings.TrimSpace(in.Q); f != "" {
		q += ` AND c.name LIKE ? ESCAPE '\'`
		args = append(args, "%"+likeEscape(f)+"%")
	}
	q += ` ORDER BY c.updated_at DESC, c.id DESC LIMIT ` + itoa(ListCap+1)
	rows, err := c.DB().Read().QueryContext(c, q, args...)
	if err != nil {
		return ListOut{}, err
	}
	defer rows.Close()
	out := ListOut{Items: []Compendium{}}
	for rows.Next() {
		it, err := scan(rows)
		if err != nil {
			return ListOut{}, err
		}
		out.Items = append(out.Items, it)
	}
	if err := rows.Err(); err != nil {
		return ListOut{}, err
	}
	if len(out.Items) > ListCap {
		out.Items, out.Truncated = out.Items[:ListCap], true
	}
	return out, nil
}

func get(c *ops.Ctx, in GetIn) (DetailItem, error) {
	d, err := loadDetail(c, c.DB().Read(), c.P, in.ID)
	if err != nil {
		return DetailItem{}, err
	}
	return DetailItem{Item: d}, nil
}

type querier interface {
	queryRower
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

// A pinned key gets counts and no names: a Compendium key reaches no project,
// a project key one, and neither is told the titles of other projects that use
// the Compendium or the names of other Compendiums it references.
func loadDetail(ctx context.Context, q querier, p ops.Principal, id string) (Detail, error) {
	userID, pinned := p.UserID, p.Pinned()
	cmp, err := load(ctx, q, userID, id)
	if err != nil {
		return Detail{}, err
	}
	d := Detail{Compendium: cmp, Projects: []ProjectUse{}, References: []Reference{}}
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM compendium_members WHERE compendium_id = ?`, id).Scan(&d.MemberCount); err != nil {
		return Detail{}, err
	}
	// Projects that use it, as primary or by reference, split into the ones the
	// caller can open and a bare count of the rest.
	rows, err := q.QueryContext(ctx, `SELECT p.id, p.title, 'primary', m.user_id IS NOT NULL OR u.role = 'admin'
		FROM projects p JOIN users u ON u.id = ?1
		LEFT JOIN project_members m ON m.project_id = p.id AND m.user_id = u.id
		WHERE p.compendium_id = ?2 AND p.deleted_at IS NULL
		UNION ALL
		SELECT p.id, p.title, 'reference', m.user_id IS NOT NULL OR u.role = 'admin'
		FROM compendium_refs r JOIN projects p ON p.id = r.owner_id AND r.owner_kind = 'project'
		JOIN users u ON u.id = ?1
		LEFT JOIN project_members m ON m.project_id = p.id AND m.user_id = u.id
		WHERE r.target_compendium_id = ?2 AND p.deleted_at IS NULL
		ORDER BY 3, 2, 1`, userID, id)
	if err != nil {
		return Detail{}, err
	}
	for rows.Next() {
		var u ProjectUse
		var visible bool
		if err := rows.Scan(&u.ID, &u.Title, &u.Relation, &visible); err != nil {
			rows.Close()
			return Detail{}, err
		}
		if visible && !pinned {
			d.Projects = append(d.Projects, u)
		} else {
			d.HiddenProjects++
		}
	}
	if err := rows.Close(); err != nil {
		return Detail{}, err
	}
	rrows, err := q.QueryContext(ctx, `SELECT r.id, t.id, t.name, r.mode FROM compendium_refs r JOIN compendiums t ON t.id = r.target_compendium_id
		WHERE r.owner_kind = 'compendium' AND r.owner_id = ? AND t.deleted_at IS NULL ORDER BY t.name, t.id`, id)
	if err != nil {
		return Detail{}, err
	}
	var targets []Reference
	for rrows.Next() {
		var r Reference
		if err := rrows.Scan(&r.ID, &r.CompendiumID, &r.Name, &r.Mode); err != nil {
			rrows.Close()
			return Detail{}, err
		}
		targets = append(targets, r)
	}
	if err := rrows.Close(); err != nil {
		return Detail{}, err
	}
	for _, r := range targets {
		if compaccess.CanRead(ctx, q, userID, r.CompendiumID) && !pinned {
			d.References = append(d.References, r)
		} else {
			d.HiddenRefs++
		}
	}
	err = q.QueryRowContext(ctx, `SELECT count(*) FROM compendium_refs WHERE owner_kind = 'compendium' AND target_compendium_id = ?`, id).Scan(&d.ReferencedBy)
	return d, err
}

// ---- helpers -----------------------------------------------------------------------------

func freeSlug(tx *sql.Tx, ownerID, base string) (string, error) {
	slug := base
	for i := 2; i < 1000; i++ {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM compendiums WHERE owner_id = ? AND slug = ?`, ownerID, slug).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return slug, nil
		}
		slug = base + "-" + itoa(i)
	}
	return "", ops.Conflict("could not find a free slug for this name")
}

func itoa(i int) string {
	if i < 10 {
		return string(rune('0' + i))
	}
	return itoa(i/10) + string(rune('0'+i%10))
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
