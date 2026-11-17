package mirror

import (
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/ops"
)

// ServiceName is the name the mirror is registered under in ops.Env.
const ServiceName = "mirror"

// StatusIn names the project.
type StatusIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in StatusIn) ProjectRef() string { return in.ProjectID }

// Item wraps a status.
type Item struct {
	Item Status `json:"item"`
}

// Register adds the mirror ops. The mirror itself is attached to the
// environment by the server (ops.Env.SetService); without it, as in a
// schema-only build, the ops report that the mirror is not running.
func Register(r *ops.Registry) {
	ops.Register(r, ops.Op{
		Name: "mirror.status", Method: "GET", Path: "/api/v1/projects/{id}/mirror",
		Desc: "How the project's Markdown mirror stands: whether a write is pending, when a file last changed, the last error, the file count, and a hash of the whole tree. " +
			"The mirror is a generated copy under data/mirror; the database stays canonical.",
		Auth: ops.RequireProject(ops.ProjectReader),
	}, status)
	ops.Register(r, ops.Op{
		Name: "mirror.rebuild", Method: "POST", Path: "/api/v1/projects/{id}/mirror/rebuild",
		Desc: "Rewrite the project's Markdown mirror now, comparing every file with what is on disk and repairing any that differ. Unchanged files are left alone. " +
			"Returns the status afterwards; a failed write is reported in last_error.",
		Auth: ops.RequireProject(ops.ProjectEditor), NoDryRun: true, // writes files
	}, rebuild)
	ops.Register(r, ops.Op{
		Name: "mirror.compendium.status", Method: "GET", Path: "/api/v1/compendiums/{id}/mirror",
		Desc: "How a Compendium's own Markdown mirror stands (see mirror.status). Only a Compendium that no project uses as its primary has a folder of its own, named compendium-<slug>--<id suffix> under data/mirror; " +
			"one that is a project's primary is written into that project's compendium/ folder, and this reports an empty directory for it.",
		Auth: ops.RequireCompendium(compaccess.Reader),
	}, compendiumStatus)
	ops.Register(r, ops.Op{
		Name: "mirror.compendium.rebuild", Method: "POST", Path: "/api/v1/compendiums/{id}/mirror/rebuild",
		Desc: "Rewrite a standalone Compendium's Markdown mirror now, comparing every file with what is on disk and repairing any that differ. Refused (409) for a Compendium that is a project's primary: rebuild that project instead.",
		Auth: ops.RequireCompendium(compaccess.Editor), NoDryRun: true, // writes files
	}, compendiumRebuild)
}

// CompendiumStatusIn names the Compendium.
type CompendiumStatusIn struct {
	ID string `path:"id" json:"-" desc:"Compendium id."`
}

// CompendiumRef lets the registry authorize by membership.
func (in CompendiumStatusIn) CompendiumRef() string { return in.ID }

func compendiumStatus(c *ops.Ctx, in CompendiumStatusIn) (Item, error) {
	m, err := service(c)
	if err != nil {
		return Item{}, err
	}
	return Item{Item: m.Status(in.ID)}, nil
}

func compendiumRebuild(c *ops.Ctx, in CompendiumStatusIn) (Item, error) {
	m, err := service(c)
	if err != nil {
		return Item{}, err
	}
	if !m.Enabled() {
		return Item{}, ops.Conflict("The Markdown mirror is switched off (SCRIOBH_MIRROR=off).")
	}
	st, err := m.Rebuild(c, in.ID)
	if err != nil {
		if ae := ops.AsError(err); ae.Code != ops.CodeInternal {
			return Item{}, ae
		}
		return Item{}, ops.Internal(err)
	}
	c.Audit(ops.AuditEntry{Action: "mirror.rebuild", TargetType: "compendium", TargetID: in.ID,
		Detail: map[string]any{"files": st.Files, "ok": st.LastError == ""}})
	return Item{Item: st}, nil
}

func service(c *ops.Ctx) (*Mirror, error) {
	m, ok := ops.Service[*Mirror](c.Env, ServiceName)
	if !ok || m == nil {
		return nil, ops.Conflict("The Markdown mirror is not running on this server.")
	}
	return m, nil
}

func status(c *ops.Ctx, in StatusIn) (Item, error) {
	m, err := service(c)
	if err != nil {
		return Item{}, err
	}
	return Item{Item: m.Status(in.ProjectID)}, nil
}

func rebuild(c *ops.Ctx, in StatusIn) (Item, error) {
	m, err := service(c)
	if err != nil {
		return Item{}, err
	}
	if !m.Enabled() {
		return Item{}, ops.Conflict("The Markdown mirror is switched off (SCRIOBH_MIRROR=off).")
	}
	st, err := m.Rebuild(c, in.ProjectID)
	if err != nil {
		if ae := ops.AsError(err); ae.Code != ops.CodeInternal {
			return Item{}, ae
		}
		return Item{}, ops.Internal(err)
	}
	c.Audit(ops.AuditEntry{Action: "mirror.rebuild", TargetType: "project", TargetID: in.ProjectID,
		Detail: map[string]any{"files": st.Files, "ok": st.LastError == ""}})
	return Item{Item: st}, nil
}
