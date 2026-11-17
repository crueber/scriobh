// Package projects holds the project ops: create, list, get, update, the
// trash/archive/purge lifecycle, and the per-person pin and recents state.
package projects

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projectgate"
	"github.com/crueber/scriobh/internal/ops"
)

// Kinds and statuses mirror the CHECK constraints in migration 0200.
var Kinds = []string{"novel", "novella", "short", "collection", "nonfiction", "essay", "blog", "report", "blank"}

// CoverPalette is the fixed set of flat cover colours. A fixed palette lets
// templates use classes instead of style attributes, which the CSP forbids.
var CoverPalette = []string{"#2F5D50", "#7A4B2A", "#3B5B7A", "#6B3E4F", "#4B6B3A", "#5A4E7A", "#8A6420", "#4A4740"}

// Project is the wire shape of a project. The per-person fields (pinned,
// last_opened_at, last_document_id, role) describe the caller, not the project.
type Project struct {
	ID          string `json:"id"`
	Slug        string `json:"slug"`
	Address     string `json:"address" desc:"What a page address names this project by, for the caller: the slug, or slug~suffix (the last six characters of the id) when another project the caller can read has the same slug and this is not theirs. Slugs are unique per owner, not overall."`
	Title       string `json:"title"`
	Subtitle    string `json:"subtitle"`
	Kind        string `json:"kind"`
	Status      string `json:"status"`
	AuthorName  string `json:"author_name"`
	Genre       string `json:"genre"`
	Language    string `json:"language"`
	Description string `json:"description"`
	WordGoal    int    `json:"word_goal"`
	Deadline    string `json:"deadline" desc:"YYYY-MM-DD, or empty when there is none."`
	CoverColor  string `json:"cover_color"`
	Role        string `json:"role,omitempty"`

	WordCount     int `json:"word_count" desc:"Words in the project's live documents."`
	DocumentCount int `json:"document_count" desc:"Live documents."`

	Archived   bool   `json:"archived"`
	ArchivedAt string `json:"archived_at,omitempty"`
	Trashed    bool   `json:"trashed"`
	TrashedAt  string `json:"trashed_at,omitempty"`

	Pinned         bool   `json:"pinned" desc:"Pinned by the caller; per person."`
	LastOpenedAt   string `json:"last_opened_at" desc:"When the caller last opened the project; empty when never."`
	LastDocumentID string `json:"last_document_id" desc:"The live document the caller was last in; empty when none, for Continue where I left off."`

	CoverLayout bool   `json:"cover_layout" desc:"True when the project has its own cover, a picture for the front (project.cover_layout.get); false means the generated cover."`
	CoverThumb  string `json:"cover_thumb,omitempty" desc:"The address of the project's own cover, 480 px wide, for a list. Empty when the cover is the generated one."`
	CoverAlt    string `json:"cover_alt,omitempty" desc:"The project's own cover described for screen readers."`

	Protected bool `json:"protected,omitempty" desc:"True when the project has a passphrase (project.passphrase.get has the rest)."`
	Locked    bool `json:"locked,omitempty" desc:"True when it has a passphrase this session has not unlocked: only the title, the address and the person's own state are given, none of its text, counts or dates."`

	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// redactLocked removes everything of a protected project that is not its title and address: the lists that span
// projects show a locked one as a name that needs a passphrase (F-252).
func redactLocked(p Project) Project {
	return Project{ID: p.ID, Slug: p.Slug, Address: p.Address, Title: p.Title, Kind: p.Kind, CoverColor: p.CoverColor, Role: p.Role,
		Archived: p.Archived, Trashed: p.Trashed, Pinned: p.Pinned, CreatedAt: p.CreatedAt, Protected: true, Locked: true}
}

// markProtected sets Protected on projects that have a passphrase and redacts the ones this caller has not unlocked.
func markProtected(c *ops.Ctx, ps []Project) error {
	if len(ps) == 0 {
		return nil
	}
	gate, ok := ops.Service[*projectgate.Service](c.Env, projectgate.ServiceName)
	if !ok {
		if c.Env != nil && c.Env.ProjectGate != nil {
			return ops.Internal(errors.New("the passphrase gate is installed but its service is missing from this environment"))
		}
		return nil
	}
	ids := make([]string, len(ps))
	for i := range ps {
		ids[i] = ps[i].ID
	}
	states, err := gate.States(c, c.P, ids)
	if err != nil {
		return err
	}
	for i := range ps {
		if st, has := states[ps[i].ID]; has {
			if st.Locked {
				ps[i] = redactLocked(ps[i])
			} else {
				ps[i].Protected = true
			}
		}
	}
	return nil
}

// CreateIn creates a project.
type CreateIn struct {
	Title      string `json:"title" desc:"Project title, up to 200 characters."`
	Kind       string `json:"kind,omitempty" desc:"What the project is; presets structure later." enum:"novel,novella,short,collection,nonfiction,essay,blog,report,blank"`
	Subtitle   string `json:"subtitle,omitempty"`
	AuthorName string `json:"author_name,omitempty"`
	Language   string `json:"language,omitempty" desc:"BCP 47 tag, default en."`
	WordGoal   int    `json:"word_goal,omitempty" desc:"Target word count; 0 for none."`
	CoverColor string `json:"cover_color,omitempty" desc:"One of the fixed cover palette colours."`
}

// Validate normalises and checks the input.
func (in *CreateIn) Validate() error {
	in.Title = strings.TrimSpace(in.Title)
	switch {
	case in.Title == "":
		return ops.Invalid("title", "Give the project a title.")
	case utf8.RuneCountInString(in.Title) > 200:
		return ops.Invalid("title", "The title is limited to 200 characters.")
	}
	if in.Kind == "" {
		in.Kind = "blank"
	}
	if !contains(Kinds, in.Kind) {
		return ops.Invalid("kind", "Unknown project kind.")
	}
	in.Subtitle = strings.TrimSpace(in.Subtitle)
	in.AuthorName = strings.TrimSpace(in.AuthorName)
	if utf8.RuneCountInString(in.Subtitle) > 200 || utf8.RuneCountInString(in.AuthorName) > 200 {
		return ops.Invalid("subtitle", "Subtitle and author are limited to 200 characters.")
	}
	if in.Language == "" {
		in.Language = "en"
	}
	if len(in.Language) > 16 || strings.ContainsAny(in.Language, " <>\"'") {
		return ops.Invalid("language", "Use a language tag such as en or ga.")
	}
	if in.WordGoal < 0 || in.WordGoal > 10_000_000 {
		return ops.Invalid("word_goal", "The word goal must be between 0 and 10,000,000.")
	}
	if in.CoverColor == "" {
		in.CoverColor = CoverPalette[0]
	}
	in.CoverColor = strings.ToUpper(in.CoverColor)
	if !contains(CoverPalette, in.CoverColor) {
		return ops.Invalid("cover_color", "Pick a colour from the cover palette.")
	}
	return nil
}

// States a project list can show. Trashed wins over archived: a project that
// is both is only in the trash view.
const (
	StateActive   = "active"
	StateArchived = "archived"
	StateTrashed  = "trashed"
	StateAll      = "all"
)

// ListCap is the most projects one list call returns. The order depends on
// per-person state (pins, recents), so a stable cursor would have to be
// per-person too; a person with more than 500 projects narrows with q or state.
// ListOut.Truncated says when the cap was hit.
const ListCap = 500

// ListIn lists the caller's projects.
type ListIn struct {
	Q     string `query:"q" desc:"Filter by title (case-insensitive substring)."`
	State string `query:"state" desc:"active (default), archived, trashed or all. The trash view lists only projects the caller owns." enum:"active,archived,trashed,all"`
}

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

// ListOut is the project list.
type ListOut struct {
	Items     []Project `json:"items"`
	Truncated bool      `json:"truncated,omitempty" desc:"True when more than 500 projects matched and the rest were left out."`
}

// GetIn names one project.
type GetIn struct {
	ID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in GetIn) ProjectRef() string { return in.ID }

// Item wraps a single project.
type Item struct {
	Item Project `json:"item"`
}

// Register adds the project ops. d may be nil when only the schema is needed.
func Register(r *ops.Registry) {
	ops.Register(r, ops.Op{
		Name: "project.create", Method: "POST", Path: "/api/v1/projects", UI: "POST /ui/projects",
		Desc: "Create a project. The caller becomes its owner.", Auth: ops.Writer(), Status: 201,
	}, create)
	ops.Register(r, ops.Op{
		Name: "project.list", Method: "GET", Path: "/api/v1/projects",
		Desc: "List the projects the caller can open: pinned first, then most recently opened by the caller, then most recently edited. " +
			"The default state is active (neither archived nor trashed). Returns at most 500; narrow with q or state.",
		Auth: ops.Authenticated(),
	}, list)
	ops.Register(r, ops.Op{
		Name: "project.get", Method: "GET", Path: "/api/v1/projects/{id}",
		Desc: "Get one project the caller can open, with word and document counts and the caller's pin and last-opened state.",
		Auth: ops.RequireProject(ops.ProjectReader),
	}, get)
	registerLifecycle(r)
	registerMembers(r)
}

func create(c *ops.Ctx, in CreateIn) (Item, error) {
	id := db.NewID("prj")
	now := db.Now()
	var p Project
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		if _, err := InsertTx(tx, NewProject{ID: id, OwnerID: c.P.UserID, Now: now, CreateIn: in}); err != nil {
			return err
		}
		if _, err := docs.Seed(tx, id, in.Kind, now); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "project.created", ProjectID: id, Payload: map[string]any{"kind": in.Kind}})
		c.Audit(ops.AuditEntry{Action: "project.create", TargetType: "project", TargetID: id})
		if err := c.FlushTo(tx); err != nil {
			return err
		}
		var err error
		p, err = loadProject(c, tx, c.P.UserID, id)
		return err
	})
	if err != nil {
		return Item{}, err
	}
	return Item{Item: p}, nil
}

// queryRower is what *sql.DB and *sql.Tx share. Ops that write load the
// result through their own transaction: the read pool cannot see rows that are
// not committed yet, and a nested WithTx would wait for itself forever.
type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// projectCols and projectFrom are the one shape every read of a project uses,
// so list and get cannot drift. ?1 is the caller. The document totals come
// from a subquery over only the projects in scope (scope is a SQL fragment
// naming them), so a list is one statement and never a query per project.
const projectCols = `p.id, p.slug, p.title, p.subtitle, p.kind, p.status, p.author_name, p.genre, p.language,
	p.description, p.word_goal, COALESCE(p.deadline, ''), p.cover_color,
	COALESCE(m.role, CASE WHEN u.role = 'admin' THEN 'owner' END, ''),
	COALESCE(dc.words, 0), COALESCE(dc.n, 0),
	p.archived_at IS NOT NULL, COALESCE(p.archived_at, ''), p.trashed_at IS NOT NULL, COALESCE(p.trashed_at, ''),
	COALESCE(s.pinned, 0), COALESCE(s.last_opened_at, ''),
	COALESCE((SELECT d.id FROM documents d WHERE d.id = s.last_document_id AND d.project_id = p.id AND d.deleted_at IS NULL), ''),
	p.created_at, p.updated_at,
	COALESCE((SELECT cl.hash FROM cover_layouts cl WHERE cl.project_id = p.id AND cl.hash <> '' AND (cl.shape = 'wrap' OR json_extract(cl.front_json, '$.mode') IN ('image', 'page'))), ''),
	COALESCE((SELECT cl.alt FROM cover_layouts cl WHERE cl.project_id = p.id AND cl.hash <> ''), '')`

func projectFrom(scope string) string {
	return ` FROM projects p
	JOIN users u ON u.id = ?1
	LEFT JOIN project_members m ON m.project_id = p.id AND m.user_id = u.id
	LEFT JOIN user_project_state s ON s.project_id = p.id AND s.user_id = u.id
	LEFT JOIN (SELECT project_id, SUM(word_count) AS words, COUNT(*) AS n FROM documents
		WHERE deleted_at IS NULL AND ` + scope + ` GROUP BY project_id) dc ON dc.project_id = p.id`
}

func scan(row interface{ Scan(...any) error }) (Project, error) {
	var p Project
	var coverHash, coverAlt string
	err := row.Scan(&p.ID, &p.Slug, &p.Title, &p.Subtitle, &p.Kind, &p.Status, &p.AuthorName, &p.Genre, &p.Language,
		&p.Description, &p.WordGoal, &p.Deadline, &p.CoverColor, &p.Role, &p.WordCount, &p.DocumentCount,
		&p.Archived, &p.ArchivedAt, &p.Trashed, &p.TrashedAt, &p.Pinned, &p.LastOpenedAt, &p.LastDocumentID,
		&p.CreatedAt, &p.UpdatedAt, &coverHash, &coverAlt)
	if err == nil && len(coverHash) >= 12 {
		// The version in the address is for the browser's cache; project.cover revalidates by its own ETag as well.
		p.CoverLayout, p.CoverAlt = true, coverAlt
		p.CoverThumb = "/api/v1/projects/" + p.ID + "/cover?size=thumb&v=" + coverHash[:12]
	}
	return p, err
}

// loadProject reads one project as userID sees it. A member sees their own
// role; an instance admin without a membership row acts as owner (the same
// rule as RoleFunc). Anyone else gets ErrNoRows, reported as not found.
// Trashed and archived projects load: the trash view needs them.
func loadProject(ctx context.Context, q queryRower, userID, projectID string) (Project, error) {
	p, err := scan(q.QueryRowContext(ctx, `SELECT `+projectCols+projectFrom(`project_id = ?2`)+`
		WHERE p.id = ?2 AND p.deleted_at IS NULL AND u.deleted_at IS NULL AND (m.user_id IS NOT NULL OR u.role = 'admin')
		  AND (p.trashed_at IS NULL OR u.role = 'admin' OR m.role = 'owner')`,
		userID, projectID))
	if errors.Is(err, sql.ErrNoRows) {
		return Project{}, ops.NotFound("project")
	}
	return p, err
}

func list(c *ops.Ctx, in ListIn) (ListOut, error) {
	q := `SELECT ` + projectCols + projectFrom(`project_id IN (SELECT project_id FROM project_members WHERE user_id = ?1)`) +
		` WHERE m.user_id IS NOT NULL AND p.deleted_at IS NULL`
	args := []any{c.P.UserID}
	switch in.State {
	case StateActive:
		q += ` AND p.trashed_at IS NULL AND p.archived_at IS NULL`
	case StateArchived:
		q += ` AND p.trashed_at IS NULL AND p.archived_at IS NOT NULL`
	case StateTrashed:
		// Only the owner can empty or restore the trash, so only they see it.
		q += ` AND p.trashed_at IS NOT NULL AND m.role = 'owner'`
	case StateAll:
		q += ` AND (p.trashed_at IS NULL OR m.role = 'owner')` // the same rule: others never see the owner's trash
	}
	if f := strings.TrimSpace(in.Q); f != "" {
		q += ` AND p.title LIKE ? ESCAPE '\'`
		args = append(args, "%"+likeEscape(f)+"%")
	}
	q += ` ORDER BY COALESCE(s.pinned, 0) DESC, COALESCE(s.last_opened_at, '') DESC, p.updated_at DESC, p.id DESC LIMIT ` + itoa(ListCap+1)
	rows, err := c.DB().Read().QueryContext(c, q, args...)
	if err != nil {
		return ListOut{}, err
	}
	defer rows.Close()
	out := ListOut{Items: []Project{}}
	for rows.Next() {
		p, err := scan(rows)
		if err != nil {
			return ListOut{}, err
		}
		out.Items = append(out.Items, p)
	}
	if err := rows.Err(); err != nil {
		return ListOut{}, err
	}
	if len(out.Items) > ListCap {
		out.Items, out.Truncated = out.Items[:ListCap], true
	}
	if err := addresses(c, out.Items); err != nil {
		return ListOut{}, err
	}
	if err := markProtected(c, out.Items); err != nil {
		return ListOut{}, err
	}
	return out, nil
}

// addresses fills Address on each project: its slug, or slug~suffix when it is not the caller's own and the caller can
// read another project with the same slug. The bare slug always belongs to the caller's own (IDBySlug prefers it).
func addresses(c *ops.Ctx, ps []Project) error {
	if len(ps) == 0 {
		return nil
	}
	rows, err := c.DB().Read().QueryContext(c, `SELECT p.slug, count(*) FROM projects p JOIN project_members m ON m.project_id = p.id
		WHERE m.user_id = ? AND p.deleted_at IS NULL AND (p.trashed_at IS NULL OR m.role = 'owner') GROUP BY p.slug HAVING count(*) > 1`, c.P.UserID)
	if err != nil {
		return err
	}
	defer rows.Close()
	dup := map[string]bool{}
	for rows.Next() {
		var s string
		var n int
		if err := rows.Scan(&s, &n); err != nil {
			return err
		}
		dup[s] = true
	}
	for i := range ps {
		ps[i].Address = ps[i].Slug
		if dup[ps[i].Slug] && ps[i].Role != ops.ProjectOwner {
			id := ps[i].ID
			if len(id) > 6 {
				id = id[len(id)-6:]
			}
			ps[i].Address = ps[i].Slug + "~" + id
		}
	}
	return rows.Err()
}

// Addressed returns the project with its Slug replaced by its Address, so a page built from it links to the right one.
func (p Project) Addressed() Project {
	if p.Address != "" {
		p.Slug = p.Address
	}
	return p
}

func get(c *ops.Ctx, in GetIn) (Item, error) {
	p, err := loadProject(c, c.DB().Read(), c.P.UserID, in.ID)
	if err != nil {
		return Item{}, err
	}
	ps := []Project{p}
	if err := addresses(c, ps); err != nil {
		return Item{}, err
	}
	if err := markProtected(c, ps); err != nil {
		return Item{}, err
	}
	return Item{Item: ps[0]}, nil
}

// RoleFunc resolves project roles for the registry, and so decides what every
// op may touch. Instance admins act as owners of every project; everyone else
// needs a membership row.
//
// A trashed project belongs to its owner: the owner (and an instance admin)
// keeps the owner role, so restore, purge and reading the project still work;
// every other member gets "not found", the same answer as for a project that
// does not exist, and so cannot save, restore a version or import into it.
// Archived projects stay writable. The owner can still call content ops on
// their own trashed project; closing that would need a per-op "needs a live
// project" flag in internal/ops, because this function does not see which op
// is asking.
func RoleFunc(d *db.DB) ops.ProjectRoleFunc {
	return func(ctx context.Context, userID, projectID string) (string, bool) {
		var role string
		err := d.Read().QueryRowContext(ctx, `SELECT CASE WHEN u.role = 'admin' THEN 'owner' ELSE m.role END
			FROM projects p
			JOIN users u ON u.id = ?1
			LEFT JOIN project_members m ON m.project_id = p.id AND m.user_id = u.id
			WHERE p.id = ?2 AND p.deleted_at IS NULL AND u.deleted_at IS NULL
			  AND (u.role = 'admin' OR m.role IS NOT NULL)
			  AND (p.trashed_at IS NULL OR u.role = 'admin' OR m.role = 'owner')`, userID, projectID).Scan(&role)
		if err != nil {
			return "", false
		}
		return role, true
	}
}

// IDBySlug resolves a slug to a project id among the projects userID is a
// member of, preferring their own. A slug that belongs to someone else's
// project is indistinguishable from one that does not exist.
func IDBySlug(ctx context.Context, d *db.DB, userID, slug string) (string, bool) {
	// Slugs are unique per owner, so a person who owns "Salt Road" and edits another's "Salt Road" has two: the bare
	// slug is their own, and slug~suffix (the last six characters of the id) names any other.
	suffix := ""
	if i := strings.LastIndex(slug, "~"); i > 0 {
		slug, suffix = slug[:i], slug[i+1:]
	}
	var id string
	if err := d.Read().QueryRowContext(ctx, `SELECT p.id FROM projects p JOIN project_members m ON m.project_id = p.id
		WHERE p.slug = ? AND m.user_id = ? AND p.deleted_at IS NULL AND (p.trashed_at IS NULL OR m.role = 'owner')
		  AND (? = '' OR substr(p.id, -6) = ?)
		ORDER BY (p.owner_id = m.user_id) DESC, p.updated_at DESC LIMIT 1`, slug, userID, suffix, suffix).Scan(&id); err != nil {
		return "", false
	}
	return id, true
}

// Slugify makes a URL-safe slug, folding common Latin diacritics (Scríobh
// becomes scriobh) and collapsing everything else to single hyphens.
func Slugify(title string) string {
	var b strings.Builder
	dash := true
	for _, r := range title {
		r = fold(unicode.ToLower(r))
		switch {
		case r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			dash = false
		case !dash:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	if s == "" {
		s = "project"
	}
	return s
}

var foldMap = map[rune]rune{
	'á': 'a', 'à': 'a', 'â': 'a', 'ä': 'a', 'ã': 'a', 'å': 'a', 'é': 'e', 'è': 'e', 'ê': 'e', 'ë': 'e',
	'í': 'i', 'ì': 'i', 'î': 'i', 'ï': 'i', 'ó': 'o', 'ò': 'o', 'ô': 'o', 'ö': 'o', 'õ': 'o', 'ø': 'o',
	'ú': 'u', 'ù': 'u', 'û': 'u', 'ü': 'u', 'ñ': 'n', 'ç': 'c', 'ý': 'y', 'ÿ': 'y', 'ß': 's',
}

func fold(r rune) rune {
	if m, ok := foldMap[r]; ok {
		return m
	}
	return r
}

func freeSlug(tx *sql.Tx, ownerID, base string) (string, error) {
	slug := base
	for i := 2; i < 1000; i++ {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM projects WHERE owner_id = ? AND slug = ?`, ownerID, slug).Scan(&n); err != nil {
			return "", err
		}
		if n == 0 {
			return slug, nil
		}
		slug = base + "-" + itoa(i)
	}
	return "", ops.Conflict("could not find a free slug for this title")
}

func itoa(i int) string {
	const digits = "0123456789"
	if i < 10 {
		return string(digits[i])
	}
	return itoa(i/10) + string(digits[i%10])
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

// PaletteSwatch is one cover colour for the wizard.
type PaletteSwatch struct {
	Hex   string
	Class string
}

// Swatches lists the cover palette with the CSS class each colour uses.
func Swatches() []PaletteSwatch {
	out := make([]PaletteSwatch, len(CoverPalette))
	for i, h := range CoverPalette {
		out[i] = PaletteSwatch{Hex: h, Class: "cover-" + itoa(i)}
	}
	return out
}

// KindOption is one project kind for the wizard.
type KindOption struct{ Value, Label string }

// KindOptions lists the kinds with display labels.
func KindOptions() []KindOption {
	labels := map[string]string{"novel": "Novel", "novella": "Novella", "short": "Short story", "collection": "Collection",
		"nonfiction": "Non-fiction book", "essay": "Essay", "blog": "Blog series", "report": "Report", "blank": "Blank"}
	out := make([]KindOption, len(Kinds))
	for i, k := range Kinds {
		out[i] = KindOption{Value: k, Label: labels[k]}
	}
	return out
}
