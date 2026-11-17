package timeline

import (
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/domain/docref"
	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/ops"
)

// Register adds the timeline ops and installs the timeline's renderers and
// fingerprints into the Compendium mirror and export.
func Register(r *ops.Registry) {
	entities.RegisterTimeline(entities.TimelineHook{
		Compendium: renderCompendium, Project: renderProject,
		CompendiumStamps: compendiumStamps, ProjectStamps: projectStamps,
	})
	ops.Register(r, ops.Op{Name: "timeline.event.create", Method: "POST", Path: "/api/v1/compendiums/{id}/timeline/events", Status: 201,
		Desc: "Add an event to a Compendium's timeline: a title, an optional story date (YYYY, YYYY-MM, YYYY-MM-DD or with a time; partial dates are fine) and end, a free-text label for a world with its own calendar, a place, and who took part with a role each. Participants and the place are entities of this Compendium, by id, name or alias. Compendium editor.",
		Auth: ops.RequireCompendium(compaccess.Editor)}, create)
	ops.Register(r, ops.Op{Name: "timeline.event.list", Method: "GET", Path: "/api/v1/compendiums/{id}/timeline/events",
		Desc: "A Compendium's timeline in story order (dated events by start then end, undated last, ties by title), paged. Filter by an entity that took part or was the place, by a date range (events that overlap it), by a project whose scenes tell them, or list the trash. Scenes are shown only for projects the caller can open.",
		Auth: ops.RequireCompendium(compaccess.Reader)}, list)
	ops.Register(r, ops.Op{Name: "timeline.event.get", Method: "GET", Path: "/api/v1/timeline/events/{id}",
		Desc: "One event with its people, place and the scenes that tell it (only those in projects the caller can open).",
		Auth: via(compaccess.Reader)}, get)
	ops.Register(r, ops.Op{Name: "timeline.event.update", Method: "PATCH", Path: "/api/v1/timeline/events/{id}",
		Desc: "Change an event. Only what is sent changes; participants, when sent, replace the whole list. base_revision refuses a stale write. Compendium editor.",
		Auth: via(compaccess.Editor)}, update)
	ops.Register(r, ops.Op{Name: "timeline.event.delete", Method: "DELETE", Path: "/api/v1/timeline/events/{id}",
		Desc: "Move an event to the trash. Its people, place and scene links are kept, so timeline.event.restore brings it back exactly. Permanent removal happens only when the Compendium is purged. Compendium editor.",
		Auth: via(compaccess.Editor)}, trashEvent)
	ops.Register(r, ops.Op{Name: "timeline.event.restore", Method: "POST", Path: "/api/v1/timeline/events/{id}/restore",
		Desc: "Bring a trashed event back. Does nothing if it is not in the trash. Compendium editor.",
		Auth: via(compaccess.Editor)}, restoreEvent)
	ops.Register(r, ops.Op{Name: "timeline.project.view", Method: "GET", Path: "/api/v1/projects/{id}/timeline",
		Desc: "A book's timeline: the events of the Compendiums the caller can read that the book draws on, and the dated scenes no event covers, merged in story order, with the book's narrative order beside it (the order it tells things in). Scenes that tell an event are listed under it. Events of a Compendium the caller may not read are left out (hidden: true).",
		Auth: ops.RequireProject(ops.ProjectReader)}, view)
	ops.Register(r, ops.Op{Name: "timeline.order.get", Method: "GET", Path: "/api/v1/projects/{id}/timeline/order",
		Desc: "The order this book tells its events in: derived from where its scenes sit unless the book set its own.",
		Auth: ops.RequireProject(ops.ProjectReader)}, orderGet)
	ops.Register(r, ops.Op{Name: "timeline.order.set", Method: "PUT", Path: "/api/v1/projects/{id}/timeline/order",
		Desc: "Set this book's own order of events (event_ids, first told first; events not named follow in their derived order), or reset it to the order the scenes give. It permutes the events among the places events already hold in the narrative; scenes never move. The events must be readable by the caller and in a Compendium the book uses. Project editor.",
		Auth: ops.RequireProject(ops.ProjectEditor)}, orderSet)
	ops.Register(r, ops.Op{Name: "timeline.check", Method: "GET", Path: "/api/v1/projects/{id}/timeline/check",
		Desc: "Find contradictions in a book's timeline by fixed rules: one person at two places at once, an event before a person's `born` date or after their `died` date, a scene dated before the first event a person is in that features them, a scene told after something that happens later (a warning: flashbacks are legitimate), a scene that ends before it starts, scenes whose date cannot be read. Each finding names what is involved and says it in a sentence. Events and people of Compendiums the caller may not read are not considered.",
		Auth: ops.RequireProject(ops.ProjectReader)}, check1)
	ops.Register(r, ops.Op{Name: "timeline.compendium.check", Method: "GET", Path: "/api/v1/compendiums/{id}/timeline/check",
		Desc: "The event-only checks for a Compendium's own timeline (a place conflict, an event before a birth or after a death), without any book's scenes.",
		Auth: ops.RequireCompendium(compaccess.Reader)}, checkCompendium)
	ops.Register(r, ops.Op{Name: "timeline.scene.link", Method: "POST", Path: "/api/v1/docs/{id}/timeline-events", Status: 200,
		Desc: "Say that this scene tells an event. The event must be readable by the caller and belong to a Compendium the scene's book uses. Does nothing if it already does. Project editor.",
		Auth: docref.Via(ops.ProjectEditor)}, link)
	ops.Register(r, ops.Op{Name: "timeline.scene.unlink", Method: "DELETE", Path: "/api/v1/docs/{id}/timeline-events",
		Desc: "Say that this scene no longer tells an event. Does nothing if it did not. Project editor.",
		Auth: docref.Via(ops.ProjectEditor)}, unlink)
}
