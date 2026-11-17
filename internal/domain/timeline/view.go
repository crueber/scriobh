package timeline

import (
	"database/sql"
	"strings"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/vocab"
	"github.com/crueber/scriobh/internal/ops"
)

// ---- timeline.project.view --------------------------------------------------------------------------------------

// ViewIn asks for a project's timeline.
type ViewIn struct {
	ID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in ViewIn) ProjectRef() string { return in.ID }

// Told is a scene of this book that tells an event.
type Told struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Kind       string  `json:"kind"`
	StoryDate  string  `json:"story_date,omitempty" desc:"The scene's own story date, as written."`
	Breadcrumb []Crumb `json:"breadcrumb"`
}

// ViewItem is one place on the project's timeline.
type ViewItem struct {
	Key            string       `json:"key" desc:"event:<id> or scene:<id>; the orders refer to items by key."`
	Kind           string       `json:"kind" enum:"event,scene" desc:"An event of the Compendium, or a scene that has a story date and no event."`
	ID             string       `json:"id"`
	Title          string       `json:"title"`
	StoryDate      string       `json:"story_date"`
	StoryDateEnd   string       `json:"story_date_end"`
	StoryLabel     string       `json:"story_label,omitempty"`
	Dated          bool         `json:"dated"`
	StoryIndex     int          `json:"story_index" desc:"Place in story order, from 0."`
	NarrativeIndex int          `json:"narrative_index" desc:"Place in this book's narrative order, from 0."`
	Description    string       `json:"description,omitempty" desc:"Events: Markdown."`
	Location       *vocab.Brief `json:"location,omitempty" desc:"Events: where it happened."`
	Participants   []PersonOut  `json:"participants,omitempty" desc:"Events: who took part, as this book names them."`
	Told           []Told       `json:"told,omitempty" desc:"Events: the scenes of this book that tell it, in reading order. Scene items: itself."`
	Revision       int          `json:"revision,omitempty"`
	Pov            *vocab.Brief `json:"pov,omitempty" desc:"Scene items: the point-of-view character, when linked and readable."`
	Breadcrumb     []Crumb      `json:"breadcrumb,omitempty" desc:"Scene items: where the scene sits in the manuscript."`
}

// ViewOut is the project's timeline.
type ViewOut struct {
	Items     []ViewItem `json:"items" desc:"Every event of the Compendiums the caller can read, and every dated scene no event covers, in story order."`
	Story     []string   `json:"story" desc:"Item keys in story order (the order of items)."`
	Narrative []string   `json:"narrative" desc:"Item keys in the order this book tells them: scenes at their reading place, each event where its first scene is, events no scene tells last; the book's own order of events, when set, permutes the events among the places events hold."`
	Explicit  bool       `json:"explicit" desc:"The book has set its own order of events (timeline.order.set)."`
	Unplaced  int        `json:"unplaced_scenes" desc:"Leaf scenes with neither a story date nor an event: not on the timeline yet."`
	Hidden    bool       `json:"hidden" desc:"The project draws on a Compendium the caller may not read; its events are left out and not counted."`
}

func (d *pdata) viewItems() []ViewItem {
	out := make([]ViewItem, 0, len(d.Items))
	for _, it := range d.Items {
		v := ViewItem{Key: it.Key, Kind: it.Kind, ID: it.ID, Title: it.Title, StoryDate: it.Date, StoryDateEnd: it.End, StoryLabel: it.Label,
			Dated: it.Iv.Dated(), StoryIndex: it.Story, NarrativeIndex: it.Nar}
		switch it.Kind {
		case "event":
			e := it.Event
			v.Description, v.Revision = e.Desc, e.Rev
			if b, ok := d.Names[e.Loc]; ok {
				v.Location = &b
			}
			for _, p := range e.People {
				if b, ok := d.Names[p.Entity]; ok {
					v.Participants = append(v.Participants, PersonOut{Entity: b, Role: p.Role})
				}
			}
			for _, s := range it.Told {
				v.Told = append(v.Told, Told{ID: s.ID, Title: s.Title, Kind: s.Kind, StoryDate: s.Date, Breadcrumb: d.crumbs(s.ID)})
			}
		case "scene":
			s := it.Scene
			v.Told = []Told{{ID: s.ID, Title: s.Title, Kind: s.Kind, StoryDate: s.Date, Breadcrumb: d.crumbs(s.ID)}}
			v.Breadcrumb = d.crumbs(s.ID)
			if b, ok := d.Names[s.Pov]; ok {
				v.Pov = &b
			}
		}
		out = append(out, v)
	}
	return out
}

func view(c *ops.Ctx, in ViewIn) (ViewOut, error) {
	tx, err := c.DB().Read().BeginTx(c, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ViewOut{}, err
	}
	defer tx.Rollback()
	d, err := loadProject(c, tx, vocab.ReaderOf(c.P), in.ID)
	if err != nil {
		return ViewOut{}, err
	}
	out := ViewOut{Items: d.viewItems(), Story: []string{}, Narrative: []string{}, Explicit: len(d.Explicit) > 0, Unplaced: d.Unplaced, Hidden: d.Hidden}
	for _, it := range d.Items {
		out.Story = append(out.Story, it.Key)
	}
	for _, it := range d.Narrative {
		out.Narrative = append(out.Narrative, it.Key)
	}
	return out, nil
}

// ---- timeline.order.get and timeline.order.set --------------------------------------------------------------

// OrderIn names a project.
type OrderIn struct {
	ID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in OrderIn) ProjectRef() string { return in.ID }

// OrderOut is a project's narrative order of events.
type OrderOut struct {
	Events   []string `json:"events" desc:"Event ids in the order this book tells them (the order of timeline.project.view's narrative, events only)."`
	Explicit []string `json:"explicit" desc:"The order the book set by hand, if any; empty when the order is derived from the scenes."`
}

func orderGet(c *ops.Ctx, in OrderIn) (OrderOut, error) {
	tx, err := c.DB().Read().BeginTx(c, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return OrderOut{}, err
	}
	defer tx.Rollback()
	d, err := loadProject(c, tx, vocab.ReaderOf(c.P), in.ID)
	if err != nil {
		return OrderOut{}, err
	}
	out := OrderOut{Events: []string{}, Explicit: []string{}}
	for _, it := range d.Narrative {
		if it.Kind == "event" {
			out.Events = append(out.Events, it.ID)
		}
	}
	if len(d.Explicit) > 0 {
		out.Explicit = d.Explicit
	}
	return out, nil
}

// OrderSetIn sets the book's own order of events.
type OrderSetIn struct {
	ID       string   `path:"id" json:"-" desc:"Project id."`
	EventIDs []string `json:"event_ids,omitempty" desc:"Events in the order this book tells them. Events not named follow, in their derived order. Each must be an event of a Compendium the caller can read and this book uses."`
	Reset    bool     `json:"reset,omitempty" desc:"Forget the book's own order and go back to the order the scenes give."`
}

// ProjectRef lets the registry authorize by membership.
func (in OrderSetIn) ProjectRef() string { return in.ID }

// Validate checks the shape.
func (in *OrderSetIn) Validate() error {
	if in.Reset && len(in.EventIDs) > 0 {
		return ops.Invalid("reset", "Either reset the order or give one, not both.")
	}
	if !in.Reset && len(in.EventIDs) == 0 {
		return ops.Invalid("event_ids", "Give the events in order, or reset.")
	}
	if len(in.EventIDs) > 5000 {
		return ops.Invalid("event_ids", "Too many events.")
	}
	seen := map[string]bool{}
	for _, id := range in.EventIDs {
		if seen[id] {
			return ops.Invalid("event_ids", "An event can be named once.")
		}
		seen[id] = true
	}
	return nil
}

func orderSet(c *ops.Ctx, in OrderSetIn) (OrderOut, error) {
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		d, err := loadProject(c, tx, vocab.ReaderOf(c.P), in.ID)
		if err != nil {
			return err
		}
		for _, id := range in.EventIDs {
			if _, ok := d.ByID[id]; !ok {
				return ops.NotFound("event") // unreadable and missing look the same
			}
		}
		// Only the positions of events the caller can read are theirs to replace: the
		// rest keep the place whoever set them gave them.
		for id := range d.ByID {
			if _, err := tx.Exec(`DELETE FROM timeline_narrative WHERE project_id = ? AND event_id = ?`, in.ID, id); err != nil {
				return err
			}
		}
		for i, id := range in.EventIDs {
			if _, err := tx.Exec(`INSERT INTO timeline_narrative(project_id, event_id, position) VALUES (?,?,?)`, in.ID, id, float64(i+1)*db.PositionGap); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`UPDATE projects SET updated_at = ? WHERE id = ?`, db.Now(), in.ID); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "timeline.order.set", ProjectID: in.ID, Payload: map[string]any{"events": len(in.EventIDs), "reset": in.Reset}})
		c.Audit(ops.AuditEntry{Action: "timeline.order.set", TargetType: "project", TargetID: in.ID, Detail: map[string]any{"events": len(in.EventIDs)}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return OrderOut{}, err
	}
	return orderGet(c, OrderIn{ID: in.ID})
}

// ---- timeline.scene.link and timeline.scene.unlink ---------------------------------------------------------------

// LinkIn ties a scene to an event.
type LinkIn struct {
	ID      string `path:"id" json:"-" desc:"Document id: the scene that tells the event."`
	EventID string `json:"event_id" desc:"The event."`
}

// DocID lets the registry find the owning project.
func (in LinkIn) DocID() string { return in.ID }

// UnlinkIn removes the tie.
type UnlinkIn struct {
	ID      string `path:"id" json:"-" desc:"Document id."`
	EventID string `query:"event_id" desc:"The event."`
}

// DocID lets the registry find the owning project.
func (in UnlinkIn) DocID() string { return in.ID }

// LinkOut says what changed.
type LinkOut struct {
	DocumentID string `json:"document_id"`
	EventID    string `json:"event_id"`
	Linked     bool   `json:"linked" desc:"The scene tells the event now."`
	Changed    bool   `json:"changed" desc:"False when it already was so."`
}

func setLink(c *ops.Ctx, docID, eventID string, on bool) (LinkOut, error) {
	if strings.TrimSpace(eventID) == "" {
		return LinkOut{}, ops.Invalid("event_id", "Say which event.")
	}
	out := LinkOut{DocumentID: docID, EventID: eventID, Linked: on}
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var projectID, kind string
		var deleted sql.NullString
		if err := tx.QueryRow(`SELECT project_id, kind, deleted_at FROM documents WHERE id = ?`, docID).Scan(&projectID, &kind, &deleted); err != nil || kind == "research" {
			return ops.NotFound("document")
		}
		if deleted.Valid {
			return ops.Conflict("This document is in the trash.")
		}
		scope, primary, err := scopeCompendiums(c, tx, projectID)
		if err != nil {
			return err
		}
		readable, _ := readableCompendiums(c, tx, vocab.ReaderOf(c.P), scope, primary)
		var cid string
		var trashed bool
		if err := tx.QueryRow(`SELECT compendium_id, trashed_at IS NOT NULL FROM timeline_events WHERE id = ?`, eventID).Scan(&cid, &trashed); err != nil {
			return ops.NotFound("event")
		}
		ok := false
		for _, r := range readable {
			ok = ok || r == cid
		}
		if !ok {
			// Unreadable, outside the book's Compendiums and missing look the same.
			return ops.NotFound("event")
		}
		if trashed && on {
			return ops.Conflict("This event is in the trash. Restore it first.")
		}
		now := db.Now()
		var res sql.Result
		if on {
			res, err = tx.Exec(`INSERT OR IGNORE INTO timeline_event_docs(event_id, document_id, project_id, created_at) VALUES (?,?,?,?)`, eventID, docID, projectID, now)
		} else {
			res, err = tx.Exec(`DELETE FROM timeline_event_docs WHERE event_id = ? AND document_id = ?`, eventID, docID)
		}
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		out.Changed = n > 0
		if !out.Changed {
			return nil
		}
		if _, err := tx.Exec(`UPDATE projects SET updated_at = ? WHERE id = ?`, now, projectID); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "timeline.scene.linked", ProjectID: projectID, DocumentID: docID, Payload: map[string]any{"event_id": eventID, "linked": on}})
		return c.FlushTo(tx)
	})
	return out, err
}

func link(c *ops.Ctx, in LinkIn) (LinkOut, error)     { return setLink(c, in.ID, in.EventID, true) }
func unlink(c *ops.Ctx, in UnlinkIn) (LinkOut, error) { return setLink(c, in.ID, in.EventID, false) }
