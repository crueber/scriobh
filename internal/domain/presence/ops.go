package presence

import (
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// Register adds the presence ops. They are for a person at a browser: a key neither sends nor receives presence, and
// none of them is an MCP tool. None counts as activity (see ops.Auth.Passive): a page left open must not keep a
// session or an unlock of a protected project alive past its idle limit.
func Register(r *ops.Registry, svc *Service) {
	if env := r.Env(); env != nil {
		env.SetService(ServiceName, svc)
	}
	auth := ops.RequireProject(ops.ProjectReader).ForSessions().AsPassive()
	ops.Register(r, ops.Op{
		Name: "presence.beat", Method: "POST", Path: "/api/v1/projects/{id}/presence",
		Desc: "Say that you have this project open (and optionally which document) and learn who else is here. The page sends it every 20 seconds while it is open and visible; a person is shown for 60 seconds after their last beat. " +
			"Only for a person who has switched presence.share on (it is off for a new account): when it is off nothing is recorded and the answer says so, because sharing is symmetric: no one sees you and you see no one. " +
			"Nothing is stored: the table is in memory, never in the database, a backup, an export or an event. No typing, cursor or text is ever sent. A reader sees no one; a reviewer sees who is here; an owner or an editor also sees which document. " +
			"Browser session only. It does not count as activity for the session's or a protected project's idle limit.",
		Auth: auth, NoDryRun: true, Ephemeral: true, // it changes what a running process remembers; an Idempotency-Key is ignored: nothing is stored or replayed
	}, func(c *ops.Ctx, in BeatIn) (View, error) { return svc.Beat(c, c.P, in.ID, in.DocumentID) })
	ops.Register(r, ops.Op{
		Name: "presence.list", Method: "GET", Path: "/api/v1/projects/{id}/presence",
		Desc: "Who else is on this project right now, as you may be told: the same answer as presence.beat without saying you are here. Empty and enabled false when presence.share is off for you. Browser session only; not activity.",
		Auth: auth, Ephemeral: true,
	}, func(c *ops.Ctx, in ListIn) (View, error) { return svc.ListFor(c, c.P, in.ID) })
	ops.Register(r, ops.Op{
		Name: "presence.leave", Method: "POST", Path: "/api/v1/projects/{id}/presence/leave",
		Desc: "Say that you have closed the project's page: you are no longer shown. A POST so a page can send it as it unloads. Browser session only. Without it you are gone within 60 seconds anyway.",
		Auth: auth, NoDryRun: true, Ephemeral: true,
	}, func(c *ops.Ctx, in LeaveIn) (LeaveOut, error) {
		if c.P.SessionID != "" && c.P.KeyID == "" {
			svc.Leave(in.ID, c.P.UserID)
		}
		return LeaveOut{Left: true}, nil
	})
}

// BeatIn is a heartbeat.
type BeatIn struct {
	ID         string `path:"id" json:"-" desc:"Project id."`
	DocumentID string `json:"document_id,omitempty" desc:"The document the page has open. Ignored, without saying why, unless it is a document of this project that is not in the trash. Never a title: the server reads that."`
}

// ProjectRef lets the registry authorize by membership.
func (in BeatIn) ProjectRef() string { return in.ID }

// Validate refuses a value that cannot be an id.
func (in *BeatIn) Validate() error {
	if in.DocumentID != "" && !db.ValidID(in.DocumentID) {
		return ops.Invalid("document_id", "That is not a document id.")
	}
	return nil
}

// ListIn names the project.
type ListIn struct {
	ID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in ListIn) ProjectRef() string { return in.ID }

// LeaveIn names the project.
type LeaveIn struct {
	ID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in LeaveIn) ProjectRef() string { return in.ID }

// LeaveOut confirms.
type LeaveOut struct {
	Left bool `json:"left"`
}
