package typography

import (
	"database/sql"
	"encoding/json"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/account"
	"github.com/crueber/scriobh/internal/ops"
)

// Register adds the typography ops.
//
// project.typography.* is the owner's and an editor's to change and any member's to read; a key with write scope may set
// it (it is a project setting like the others) but it is not an MCP tool: an agent reads and writes text, not how a
// person's page looks (see the exclusions in internal/mcp). account.typography.* is a person's own, for any signed-in
// caller; a key pinned to a project or Compendium cannot reach it.
func Register(r *ops.Registry) {
	read := ops.RequireProject(ops.ProjectReader)
	edit := ops.RequireProject(ops.ProjectEditor)
	ops.Register(r, ops.Op{
		Name: "project.typography.get", Method: "GET", Path: "/api/v1/projects/{id}/typography",
		Desc: "How the project's writing and reading views are set: what the project sets, the caller's own override, the effective result, and the fixed schema of what may be chosen. Any member. A project that sets nothing looks exactly as before.",
		Auth: read,
	}, projectGet)
	ops.Register(r, ops.Op{
		Name: "project.typography.set", Method: "PUT", Path: "/api/v1/projects/{id}/typography",
		Desc: "Set the project's typography, replacing what it had: typeface, size (a percentage of the reader's own, 100 to 140), line height (1.3 to 2), measure (40 to 80 characters), paragraph style and scene-break style. Fields left out are not set. Owner or editor. " +
			"Only values from the fixed lists are accepted (no CSS, URL or font); the readable minimums are enforced. Applies on screen in the writing and reading views when a page loads, never to exports, compile, print or the static site. Each person may override it for themselves with account.typography.set.",
		Auth: edit,
	}, projectSet)
	ops.Register(r, ops.Op{
		Name: "project.typography.reset", Method: "POST", Path: "/api/v1/projects/{id}/typography/reset",
		Desc: "Remove the project's typography, so its views are set as the application ships them. Owner or editor. Nothing is lost that cannot be chosen again.",
		Auth: edit,
	}, projectReset)
	me := ops.Authenticated()
	ops.Register(r, ops.Op{
		Name: "account.typography.get", Method: "GET", Path: "/api/v1/me/typography",
		Desc: "The caller's own typography override, which wins over every project's in their own browser, and the fixed schema.",
		Auth: me,
	}, accountGet)
	ops.Register(r, ops.Op{
		Name: "account.typography.set", Method: "PUT", Path: "/api/v1/me/typography",
		Desc: "Set the caller's typography override, replacing what it had. Same fields and limits as project.typography.set. Fields left out follow the project. It applies to every project in the caller's own browser; it never changes what anyone else sees.",
		Auth: me,
	}, accountSet)
	ops.Register(r, ops.Op{
		Name: "account.typography.reset", Method: "POST", Path: "/api/v1/me/typography/reset",
		Desc: "Remove the caller's typography override, so each project's own typography applies.",
		Auth: me,
	}, accountReset)
}

// Item is what is stored for one layer.
type Item struct {
	Settings  Settings `json:"settings" desc:"The choices; a field that is absent is not set."`
	Set       bool     `json:"set" desc:"True when this layer has any choice stored."`
	UpdatedAt string   `json:"updated_at,omitempty"`
}

// ProjectIn names a project.
type ProjectIn struct {
	ID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in ProjectIn) ProjectRef() string { return in.ID }

// Out is the answer of every op.
type Out struct {
	Project   *Item    `json:"project,omitempty" desc:"What the project sets."`
	Mine      Item     `json:"mine" desc:"The caller's own override."`
	Effective Settings `json:"effective" desc:"The project's choices with the caller's override over them; a field left out is the application's own (or the caller's editor setting)."`
	Schema    Schema   `json:"schema"`
}

// SetIn replaces a project's typography.
type SetIn struct {
	ID         string `path:"id" json:"-" desc:"Project id."`
	Font       string `json:"font,omitempty" enum:"serif,sans,mono,system-serif,system-sans,system-mono" desc:"Typeface: Newsreader, Instrument Sans or IBM Plex Mono (already part of the application), or the system's serif, sans or monospace. Nothing is downloaded."`
	Size       int    `json:"size,omitempty" desc:"Body size as a percentage of the reader's own text size, 100 to 140 in steps of 5. Never below 100."`
	LineHeight string `json:"line_height,omitempty" desc:"Line height as a multiple of the size: 1.3 to 2 in steps of 0.05, written without a trailing zero (\"1.3\", \"1.35\", \"2\")."`
	Measure    int    `json:"measure,omitempty" desc:"Characters per line: 40, 45, 50, 55, 60, 66, 70, 75 or 80."`
	Paragraphs string `json:"paragraphs,omitempty" enum:"spacing,indent" desc:"spacing (the default) or indent: a first-line indent and no gap."`
	SceneBreak string `json:"scene_break,omitempty" enum:"ornament,rule" desc:"ornament (three asterisks, the default) or rule (a short rule)."`
}

func (in SetIn) settings() Settings {
	return Settings{Font: in.Font, Size: in.Size, LineHeight: in.LineHeight, Measure: in.Measure, Paragraphs: in.Paragraphs, SceneBreak: in.SceneBreak}
}

// ProjectRef lets the registry authorize by membership.
func (in SetIn) ProjectRef() string { return in.ID }

// Validate checks the fields.
func (in *SetIn) Validate() error { return in.settings().Validate() }

// AccountSetIn replaces the caller's override.
type AccountSetIn struct {
	Font       string `json:"font,omitempty" enum:"serif,sans,mono,system-serif,system-sans,system-mono" desc:"Typeface: Newsreader, Instrument Sans or IBM Plex Mono (already part of the application), or the system's serif, sans or monospace. Nothing is downloaded."`
	Size       int    `json:"size,omitempty" desc:"Body size as a percentage of the reader's own text size, 100 to 140 in steps of 5. Never below 100."`
	LineHeight string `json:"line_height,omitempty" desc:"Line height as a multiple of the size: 1.3 to 2 in steps of 0.05, written without a trailing zero (\"1.3\", \"1.35\", \"2\")."`
	Measure    int    `json:"measure,omitempty" desc:"Characters per line: 40, 45, 50, 55, 60, 66, 70, 75 or 80."`
	Paragraphs string `json:"paragraphs,omitempty" enum:"spacing,indent" desc:"spacing (the default) or indent: a first-line indent and no gap."`
	SceneBreak string `json:"scene_break,omitempty" enum:"ornament,rule" desc:"ornament (three asterisks, the default) or rule (a short rule)."`
}

func (in AccountSetIn) settings() Settings {
	return Settings{Font: in.Font, Size: in.Size, LineHeight: in.LineHeight, Measure: in.Measure, Paragraphs: in.Paragraphs, SceneBreak: in.SceneBreak}
}

// Validate checks the fields.
func (in *AccountSetIn) Validate() error { return in.settings().Validate() }

// EmptyIn is an op with nothing to send.
type EmptyIn struct{}

func load(c *ops.Ctx, table, key, id string) (Item, error) {
	var raw, at string
	err := c.DB().Read().QueryRowContext(c, `SELECT settings_json, updated_at FROM `+table+` WHERE `+key+` = ?`, id).Scan(&raw, &at)
	if err == sql.ErrNoRows {
		return Item{}, nil
	}
	if err != nil {
		return Item{}, err
	}
	s := Decode(raw)
	return Item{Settings: s, Set: !s.IsZero(), UpdatedAt: at}, nil
}

func answer(c *ops.Ctx, projectID string) (Out, error) {
	out := Out{Schema: TheSchema()}
	var mine Item
	var err error
	// A key is not the person at a browser: it is not told their own override (a choice of large text or a tall line height can
	// hint at an access need), and the effective result it gets is the project's own.
	hide := c.P.KeyID != "" && projectID != "" // the project's ops; the person's own ops (account.typography.*) answer a key as they always do
	if !hide {
		if mine, err = load(c, "user_typography", "user_id", c.P.UserID); err != nil {
			return Out{}, err
		}
	}
	out.Mine = mine
	var proj Item
	if projectID != "" {
		if proj, err = load(c, "project_typography", "project_id", projectID); err != nil {
			return Out{}, err
		}
		out.Project = &proj
	}
	if hide {
		out.Effective = proj.Settings
	} else {
		out.Effective = Effective(proj.Settings, mine.Settings, account.Prefs(c, c.DB(), c.P.UserID))
	}
	return out, nil
}

func projectGet(c *ops.Ctx, in ProjectIn) (Out, error) { return answer(c, in.ID) }

func projectSet(c *ops.Ctx, in SetIn) (Out, error) {
	if in.settings().IsZero() {
		return projectReset(c, ProjectIn{ID: in.ID})
	}
	raw, _ := json.Marshal(in.settings())
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(c, `INSERT INTO project_typography(project_id, settings_json, updated_by, updated_at) VALUES (?,?,?,?)
			ON CONFLICT(project_id) DO UPDATE SET settings_json = excluded.settings_json, updated_by = excluded.updated_by, updated_at = excluded.updated_at`,
			in.ID, string(raw), c.P.UserID, db.Now()); err != nil {
			return err
		}
		c.Audit(ops.AuditEntry{Action: "project.typography.set", TargetType: "project", TargetID: in.ID})
		return c.FlushTo(tx)
	})
	if err != nil {
		return Out{}, err
	}
	return answer(c, in.ID)
}

func projectReset(c *ops.Ctx, in ProjectIn) (Out, error) {
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		res, err := tx.ExecContext(c, `DELETE FROM project_typography WHERE project_id = ?`, in.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n > 0 {
			c.Audit(ops.AuditEntry{Action: "project.typography.reset", TargetType: "project", TargetID: in.ID})
		}
		return c.FlushTo(tx)
	})
	if err != nil {
		return Out{}, err
	}
	return answer(c, in.ID)
}

func accountGet(c *ops.Ctx, _ EmptyIn) (Out, error) { return answer(c, "") }

func accountSet(c *ops.Ctx, in AccountSetIn) (Out, error) {
	if in.settings().IsZero() {
		return accountReset(c, EmptyIn{})
	}
	raw, _ := json.Marshal(in.settings())
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(c, `INSERT INTO user_typography(user_id, settings_json, updated_at) VALUES (?,?,?)
			ON CONFLICT(user_id) DO UPDATE SET settings_json = excluded.settings_json, updated_at = excluded.updated_at`, c.P.UserID, string(raw), db.Now())
		return err
	})
	if err != nil {
		return Out{}, err
	}
	return answer(c, "")
}

func accountReset(c *ops.Ctx, _ EmptyIn) (Out, error) {
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(c, `DELETE FROM user_typography WHERE user_id = ?`, c.P.UserID)
		return err
	})
	if err != nil {
		return Out{}, err
	}
	return answer(c, "")
}
