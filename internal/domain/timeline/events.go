package timeline

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/storydate"
	"github.com/crueber/scriobh/internal/domain/vocab"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Participant is one person (an entity) taking part in an event.
type Participant struct {
	Entity string `json:"entity" desc:"An entity of this Compendium: its id, or its name or alias."`
	Role   string `json:"role,omitempty" desc:"What they did there, up to 60 characters: host, witness, victim."`
}

// PersonOut is a participant as read.
type PersonOut struct {
	Entity vocab.Brief `json:"entity"`
	Role   string      `json:"role,omitempty"`
}

// SceneLink is a scene that tells an event.
type SceneLink struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Kind         string `json:"kind"`
	ProjectID    string `json:"project_id"`
	ProjectTitle string `json:"project_title"`
	StoryDate    string `json:"story_date,omitempty" desc:"The scene's own story date, free text."`
}

// Event is a timeline event as read.
type Event struct {
	ID           string       `json:"id"`
	CompendiumID string       `json:"compendium_id"`
	Title        string       `json:"title"`
	Description  string       `json:"description" desc:"Markdown."`
	StoryDate    string       `json:"story_date" desc:"YYYY, YYYY-MM, YYYY-MM-DD or YYYY-MM-DDTHH:MM; empty for undated."`
	StoryDateEnd string       `json:"story_date_end" desc:"The same, for an event that lasts; empty for a moment."`
	StoryLabel   string       `json:"story_label" desc:"Free text shown with the date, for a world with its own calendar. Never used to order."`
	Dated        bool         `json:"dated"`
	Location     *vocab.Brief `json:"location,omitempty"`
	Participants []PersonOut  `json:"participants"`
	Scenes       []SceneLink  `json:"scenes" desc:"The scenes that tell it, in the projects the caller can open."`
	Revision     int          `json:"revision"`
	UpdatedAt    string       `json:"updated_at"`
	Trashed      bool         `json:"trashed"`
}

// EventItem wraps one event.
type EventItem struct {
	Item Event `json:"item"`
}

// ---- resolving an event's Compendium ------------------------------------------------------------------------------

type eventIDer interface{ EventID() string }

func resolveEvent(ctx context.Context, env *ops.Env, in any) (string, bool) {
	ref, ok := in.(eventIDer)
	if !ok || env == nil || env.DB == nil {
		return "", false
	}
	var cid string
	if err := env.DB.Read().QueryRowContext(ctx, `SELECT compendium_id FROM timeline_events WHERE id = ?`, ref.EventID()).Scan(&cid); err != nil {
		return "", false
	}
	return cid, true
}

func via(role string) ops.Auth { return ops.RequireCompendiumVia(role, resolveEvent) }

// ---- reading ---------------------------------------------------------------------------------------------------------

// visibleProject reports whether the caller may open a project, honouring a
// key's pin: a key pinned to a Compendium reaches no project, one pinned to a
// project reaches that project.
func visibleProject(c *ops.Ctx, projectID string) bool {
	switch {
	case c.P.CompendiumID != "":
		return false
	case c.P.ProjectID != "" && c.P.ProjectID != projectID:
		return false
	case c.Env.ProjectRole == nil:
		return false
	}
	_, ok := c.Env.ProjectRole(c, c.P.UserID, projectID)
	return ok
}

// wire turns events into their read form: entities named by the Compendium's
// own records, scenes only of projects the caller can open.
func wire(c *ops.Ctx, q vocab.Querier, es []*ev) ([]Event, error) {
	cids := map[string]bool{}
	for _, e := range es {
		cids[e.Compendium] = true
	}
	var list []string
	for id := range cids {
		list = append(list, id)
	}
	// The places and people the events name, not every entity of the Compendium: a page of events reads only its own entities.
	ents := map[string]bool{}
	for _, e := range es {
		if e.Loc != "" {
			ents[e.Loc] = true
		}
		for _, p := range e.People {
			ents[p.Entity] = true
		}
	}
	var entIDs []string
	for id := range ents {
		entIDs = append(entIDs, id)
	}
	briefs, err := compBriefsOf(c, q, list, entIDs)
	if err != nil {
		return nil, err
	}
	// Documents of visible projects, in two lookups however many events.
	docIDs := map[string]bool{}
	canOpen := map[string]bool{}
	for _, e := range es {
		for _, l := range e.Docs {
			if _, seen := canOpen[l.Project]; !seen {
				canOpen[l.Project] = visibleProject(c, l.Project)
			}
			if canOpen[l.Project] {
				docIDs[l.Doc] = true
			}
		}
	}
	docs := map[string]SceneLink{}
	var ids []string
	for id := range docIDs {
		ids = append(ids, id)
	}
	for from := 0; from < len(ids); from += 500 { // one query per 500 documents, not one per document
		chunk := ids[from:min(from+500, len(ids))]
		rows, err := q.QueryContext(c, `SELECT d.id, d.title, d.kind, d.project_id, p.title, d.story_date FROM documents d JOIN projects p ON p.id = d.project_id
			WHERE d.id IN (`+placeholders(len(chunk))+`) AND p.deleted_at IS NULL AND d.deleted_at IS NULL`, anys(chunk)...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var sl SceneLink
			if err := rows.Scan(&sl.ID, &sl.Title, &sl.Kind, &sl.ProjectID, &sl.ProjectTitle, &sl.StoryDate); err != nil {
				rows.Close()
				return nil, err
			}
			docs[sl.ID] = sl
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	out := make([]Event, 0, len(es))
	for _, e := range es {
		o := Event{ID: e.ID, CompendiumID: e.Compendium, Title: e.Title, Description: e.Desc, StoryDate: e.Date, StoryDateEnd: e.End, StoryLabel: e.Label,
			Dated: e.dated(), Participants: []PersonOut{}, Scenes: []SceneLink{}, Revision: e.Rev, UpdatedAt: e.At, Trashed: e.Trashed}
		if b, ok := briefs[e.Loc]; ok {
			o.Location = &b
		}
		for _, p := range e.People {
			if b, ok := briefs[p.Entity]; ok {
				o.Participants = append(o.Participants, PersonOut{Entity: b, Role: p.Role})
			}
		}
		for _, l := range e.Docs {
			if s, ok := docs[l.Doc]; ok {
				o.Scenes = append(o.Scenes, s)
			}
		}
		out = append(out, o)
	}
	return out, nil
}

// ---- timeline.event.get ----------------------------------------------------------------------------------------

// GetIn names one event.
type GetIn struct {
	ID string `path:"id" json:"-" desc:"Event id."`
}

// EventID lets the registry find the owning Compendium.
func (in GetIn) EventID() string { return in.ID }

func get(c *ops.Ctx, in GetIn) (EventItem, error) {
	tx, err := c.DB().Read().BeginTx(c, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return EventItem{}, err
	}
	defer tx.Rollback()
	e, err := loadOne(c, tx, in.ID)
	if err != nil {
		return EventItem{}, err
	}
	w, err := wire(c, tx, []*ev{e})
	if err != nil {
		return EventItem{}, err
	}
	return EventItem{Item: w[0]}, nil
}

// ---- timeline.event.list -------------------------------------------------------------------------------------

// ListIn lists a Compendium's timeline.
type ListIn struct {
	ID      string `path:"id" json:"-" desc:"Compendium id."`
	Entity  string `query:"entity" desc:"Only events this entity (an id, name or alias) took part in or happened at."`
	From    string `query:"from" desc:"Only events that end on or after this date (a partial date: 1998, 1998-04, 1998-04-12)."`
	To      string `query:"to" desc:"Only events that start on or before this date."`
	Project string `query:"project" desc:"Only events a scene of this project tells. The caller must be able to open the project."`
	Scene   string `query:"scene" desc:"Only events this scene (a document id) tells."`
	Q       string `query:"q" desc:"Only events where every word matches the title, the story label, the story date, the place's name or a person's name, ignoring case."`
	Trash   bool   `query:"trash" desc:"List the trashed events instead."`
	Limit   int    `query:"limit" desc:"Page size, 1 to 500; default 100."`
	Offset  int    `query:"offset" desc:"Events to skip."`
}

// CompendiumRef lets the registry authorize by membership.
func (in ListIn) CompendiumRef() string { return in.ID }

// Validate defaults and checks.
func (in *ListIn) Validate() error {
	switch {
	case in.Limit <= 0:
		in.Limit = 100
	case in.Limit > ListMax:
		in.Limit = ListMax
	}
	if in.Offset < 0 {
		return ops.Invalid("offset", "The offset cannot be negative.")
	}
	if utf8.RuneCountInString(in.Q) > 200 {
		return ops.Invalid("q", "That search is too long.")
	}
	for f, v := range map[string]string{"from": in.From, "to": in.To} {
		if v != "" {
			if _, ok := storydate.Parse(v); !ok {
				return ops.Invalid(f, "Use a date such as 1998, 1998-04 or 1998-04-12.")
			}
		}
	}
	return nil
}

// ListOut is a page of events in story order.
type ListOut struct {
	Items      []Event `json:"items" desc:"In story order: dated events by start then end, undated last, ties by title."`
	Total      int     `json:"total"`
	NextOffset int     `json:"next_offset,omitempty"`
}

func list(c *ops.Ctx, in ListIn) (ListOut, error) {
	tx, err := c.DB().Read().BeginTx(c, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ListOut{}, err
	}
	defer tx.Rollback()
	var entityID string
	if strings.TrimSpace(in.Entity) != "" {
		id, ok, err := findEntity(c, tx, in.ID, in.Entity)
		if err != nil {
			return ListOut{}, err
		}
		if !ok {
			return ListOut{Items: []Event{}}, nil // nothing can involve an entity that is not there
		}
		entityID = id
	}
	if in.Project != "" && !visibleProject(c, in.Project) {
		return ListOut{}, ops.NotFound("project")
	}
	var lo, hi string
	if d, ok := storydate.Parse(in.From); ok {
		lo = d.StartKey()
	}
	if d, ok := storydate.Parse(in.To); ok {
		hi = d.EndKey()
	}
	where, args := listWhere(in, entityID, lo, hi)
	var total int
	if err := tx.QueryRowContext(c, `SELECT count(*) FROM timeline_events e WHERE `+where, args...).Scan(&total); err != nil {
		return ListOut{}, err
	}
	out := ListOut{Items: []Event{}, Total: total}
	if in.Offset >= total {
		return out, nil
	}
	// The page's ids in story order, then the page's own rows: the database sorts and cuts the list, and only the page is read in full.
	ids, err := storyPage(c, tx, where, args, in.Limit, in.Offset)
	if err != nil {
		return ListOut{}, err
	}
	es, err := loadEventsIn(c, tx, ids)
	if err != nil {
		return ListOut{}, err
	}
	w, err := wire(c, tx, es)
	if err != nil {
		return ListOut{}, err
	}
	out.Items = w
	if end := in.Offset + in.Limit; end < total {
		out.NextOffset = end
	}
	return out, nil
}

// listWhere is the filter of timeline.event.list as SQL on timeline_events e. Every test is one the Go list made before: a dated event
// for a date range, the entity as the place or a participant, the project's scenes, the search words (each may match any one field, and a
// name counts only while the entity is live).
func listWhere(in ListIn, entityID, lo, hi string) (string, []any) {
	var b strings.Builder
	args := []any{in.ID}
	b.WriteString(`e.compendium_id = ?`)
	if in.Trash {
		b.WriteString(` AND e.trashed_at IS NOT NULL`)
	} else {
		b.WriteString(` AND e.trashed_at IS NULL`)
	}
	if entityID != "" {
		b.WriteString(` AND (e.location_entity_id = ? OR e.id IN (SELECT event_id FROM timeline_event_people WHERE entity_id = ?))`)
		args = append(args, entityID, entityID)
	}
	if lo != "" || hi != "" {
		b.WriteString(` AND e.sort_start <> ''`)
		if lo != "" {
			b.WriteString(` AND e.sort_end >= ?`)
			args = append(args, lo)
		}
		if hi != "" {
			b.WriteString(` AND e.sort_start <= ?`)
			args = append(args, hi)
		}
	}
	if in.Project != "" {
		// An uncorrelated IN, not a correlated EXISTS: the planner then reads the links once through their index, where a correlated
		// probe would read the whole project's links for every event.
		b.WriteString(` AND e.id IN (SELECT event_id FROM timeline_event_docs WHERE project_id = ?)`)
		args = append(args, in.Project)
	}
	if in.Scene != "" {
		b.WriteString(` AND e.id IN (SELECT event_id FROM timeline_event_docs WHERE document_id = ?)`)
		args = append(args, in.Scene)
	}
	for _, w := range strings.Fields(in.Q) {
		k := text.FoldTitle(w)
		if k == "" {
			continue
		}
		pat := "%" + likeEscape(k) + "%"
		b.WriteString(` AND (scr_fold(e.title) LIKE ? ESCAPE '\' OR scr_fold(e.story_label) LIKE ? ESCAPE '\' OR e.story_date LIKE ? ESCAPE '\'` +
			` OR EXISTS (SELECT 1 FROM entities en WHERE en.id = e.location_entity_id AND en.trashed_at IS NULL AND en.name_key LIKE ? ESCAPE '\')` +
			` OR EXISTS (SELECT 1 FROM timeline_event_people p JOIN entities en ON en.id = p.entity_id WHERE p.event_id = e.id AND en.trashed_at IS NULL AND en.name_key LIKE ? ESCAPE '\'))`)
		args = append(args, pat, pat, pat, pat, pat)
	}
	return b.String(), args
}

// likeEscape makes a folded word a literal in a LIKE pattern.
func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// storyPage is the ids of one page of the events the filter keeps, in story order: the order storyLess gives (dated first, by start then
// end, then the folded title, then the id).
func storyPage(c *ops.Ctx, q vocab.Querier, where string, args []any, limit, offset int) ([]string, error) {
	rows, err := q.QueryContext(c, `SELECT e.id FROM timeline_events e WHERE `+where+
		` ORDER BY (e.sort_start = ''), e.sort_start, e.sort_end, scr_fold(e.title), e.id LIMIT ? OFFSET ?`, append(args, limit, offset)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// loadEventsIn reads the events with these ids, with their people and scene links, in the order of ids. Ids that are not there are skipped.
func loadEventsIn(ctx context.Context, q vocab.Querier, ids []string) ([]*ev, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT id, compendium_id, title, description, story_date, story_date_end, story_label, sort_start, sort_end,
		COALESCE(location_entity_id,''), revision, updated_at, trashed_at IS NOT NULL FROM timeline_events WHERE id IN (`+placeholders(len(ids))+`)`, anys(ids)...)
	if err != nil {
		return nil, err
	}
	byID := map[string]*ev{}
	for rows.Next() {
		e := &ev{}
		if err := rows.Scan(&e.ID, &e.Compendium, &e.Title, &e.Desc, &e.Date, &e.End, &e.Label, &e.Start, &e.Finish, &e.Loc, &e.Rev, &e.At, &e.Trashed); err != nil {
			rows.Close()
			return nil, err
		}
		byID[e.ID] = e
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	prows, err := q.QueryContext(ctx, `SELECT p.event_id, p.entity_id, p.role FROM timeline_event_people p WHERE p.event_id IN (`+placeholders(len(ids))+`)
		ORDER BY p.event_id, p.created_at, p.entity_id`, anys(ids)...)
	if err != nil {
		return nil, err
	}
	for prows.Next() {
		var id string
		var p person
		if err := prows.Scan(&id, &p.Entity, &p.Role); err != nil {
			prows.Close()
			return nil, err
		}
		if e := byID[id]; e != nil {
			e.People = append(e.People, p)
		}
	}
	if err := prows.Close(); err != nil {
		return nil, err
	}
	drows, err := q.QueryContext(ctx, `SELECT l.event_id, l.document_id, l.project_id FROM timeline_event_docs l WHERE l.event_id IN (`+placeholders(len(ids))+`)
		ORDER BY l.event_id, l.document_id`, anys(ids)...)
	if err != nil {
		return nil, err
	}
	for drows.Next() {
		var id string
		var l docLink
		if err := drows.Scan(&id, &l.Doc, &l.Project); err != nil {
			drows.Close()
			return nil, err
		}
		if e := byID[id]; e != nil {
			e.Docs = append(e.Docs, l)
		}
	}
	if err := drows.Close(); err != nil {
		return nil, err
	}
	out := make([]*ev, 0, len(ids))
	for _, id := range ids {
		if e := byID[id]; e != nil {
			out = append(out, e)
		}
	}
	return out, nil
}

// ---- writing -----------------------------------------------------------------------------------------------------

// fields are the cleaned values of an event.
type fields struct {
	title, desc, date, end, label string
	start, finish                 string
}

func orEmpty(v any) any {
	if v == nil {
		return ""
	}
	return v
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// clean checks and canonicalises the text fields of an event.
func clean(title, desc, date, end, label string) (fields, error) {
	var f fields
	f.title = oneLine(title)
	if f.title == "" {
		return f, ops.Invalid("title", "An event needs a title.")
	}
	if utf8.RuneCountInString(f.title) > maxTitle {
		return f, ops.Invalid("title", "Titles are limited to 200 characters.")
	}
	if len(desc) > maxDescription || !utf8.ValidString(desc) {
		return f, ops.Invalid("description", "The description is too long (20,000 characters) or not valid text.")
	}
	f.desc = desc
	f.label = oneLine(label)
	if utf8.RuneCountInString(f.label) > maxLabel {
		return f, ops.Invalid("story_label", "The label is limited to 120 characters.")
	}
	if strings.TrimSpace(date) != "" {
		c, ok := storydate.Canonical(date)
		if !ok {
			return f, ops.Invalid("story_date", "Use a date such as 1998, 1998-04, 1998-04-12 or 1998-04-12T19:30 (a year may be negative for before year 0).")
		}
		f.date = c
	}
	if strings.TrimSpace(end) != "" {
		if f.date == "" {
			return f, ops.Invalid("story_date_end", "An event with an end needs a story date for its start.")
		}
		c, ok := storydate.Canonical(end)
		if !ok {
			return f, ops.Invalid("story_date_end", "Use a date such as 1998, 1998-04, 1998-04-12 or 1998-04-12T19:30.")
		}
		f.end = c
	}
	if f.date != "" {
		iv := storydate.Span(f.date, f.end)
		if !iv.Valid {
			return f, ops.Invalid("story_date_end", "The end is before the start.")
		}
		f.start, f.finish = iv.Start, iv.End
	}
	return f, nil
}

func checkParticipants(ps []Participant) error {
	if len(ps) > maxParticipants {
		return ops.Invalid("participants", "An event has at most 100 participants.")
	}
	for _, p := range ps {
		if strings.TrimSpace(p.Entity) == "" {
			return ops.Invalid("participants", "Name an entity for each participant.")
		}
		if utf8.RuneCountInString(oneLine(p.Role)) > maxRole {
			return ops.Invalid("participants", "A role is limited to 60 characters.")
		}
	}
	return nil
}

// resolvePeople turns participants into entity ids of the Compendium, once
// each, in the order given.
func resolvePeople(ctx context.Context, q vocab.Querier, cid string, ps []Participant) ([]person, error) {
	out := make([]person, 0, len(ps))
	seen := map[string]bool{}
	for _, p := range ps {
		id, ok, err := findEntity(ctx, q, cid, p.Entity)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ops.Invalid("participants", "No entity in this Compendium is called "+oneLine(p.Entity)+".")
		}
		if seen[id] {
			return nil, ops.Invalid("participants", "An entity can take part once.")
		}
		seen[id] = true
		out = append(out, person{id, oneLine(p.Role)})
	}
	return out, nil
}

func replacePeople(tx *sql.Tx, eventID string, ps []person, now string) error {
	if _, err := tx.Exec(`DELETE FROM timeline_event_people WHERE event_id = ?`, eventID); err != nil {
		return err
	}
	for _, p := range ps {
		if _, err := tx.Exec(`INSERT INTO timeline_event_people(event_id, entity_id, role, created_at) VALUES (?,?,?,?)`, eventID, p.Entity, p.Role, now); err != nil {
			return err
		}
	}
	return nil
}

// CreateIn adds an event.
type CreateIn struct {
	ID           string        `path:"id" json:"-" desc:"Compendium id."`
	Title        string        `json:"title" desc:"Up to 200 characters."`
	Description  string        `json:"description,omitempty" desc:"Markdown, up to 20,000 characters."`
	StoryDate    string        `json:"story_date,omitempty" desc:"YYYY, YYYY-MM, YYYY-MM-DD or YYYY-MM-DDTHH:MM (a leading minus is before year 0). Empty for an event whose date is not known."`
	StoryDateEnd string        `json:"story_date_end,omitempty" desc:"The same, when the event lasts. Not before the start."`
	StoryLabel   string        `json:"story_label,omitempty" desc:"How the world's own calendar says it (\"Midsummer, Year 312\"). Shown, never used to order."`
	Location     string        `json:"location,omitempty" desc:"An entity of this Compendium (usually a place): id, name or alias."`
	Participants []Participant `json:"participants,omitempty"`
}

// CompendiumRef lets the registry authorize by membership.
func (in CreateIn) CompendiumRef() string { return in.ID }

// Validate checks what can be checked without the database.
func (in *CreateIn) Validate() error {
	if _, err := clean(in.Title, in.Description, in.StoryDate, in.StoryDateEnd, in.StoryLabel); err != nil {
		return err
	}
	return checkParticipants(in.Participants)
}

func create(c *ops.Ctx, in CreateIn) (EventItem, error) {
	f, err := clean(in.Title, in.Description, in.StoryDate, in.StoryDateEnd, in.StoryLabel)
	if err != nil {
		return EventItem{}, err
	}
	var id string
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		loc, err := resolveLocation(c, tx, in.ID, in.Location)
		if err != nil {
			return err
		}
		people, err := resolvePeople(c, tx, in.ID, in.Participants)
		if err != nil {
			return err
		}
		now := db.Now()
		id = db.NewID("tle")
		if _, err := tx.Exec(`INSERT INTO timeline_events(id, compendium_id, title, description, story_date, story_date_end, story_label, sort_start, sort_end,
			location_entity_id, revision, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?,?,1,?,?)`,
			id, in.ID, f.title, f.desc, f.date, f.end, f.label, f.start, f.finish, loc, now, now); err != nil {
			return err
		}
		if err := replacePeople(tx, id, people, now); err != nil {
			return err
		}
		c.Emit(ops.Event{Type: "timeline.event.created", CompendiumID: in.ID, Payload: map[string]any{"event_id": id}})
		c.Audit(ops.AuditEntry{Action: "timeline.event.create", TargetType: "timeline_event", TargetID: id})
		return c.FlushTo(tx)
	})
	if err != nil {
		return EventItem{}, err
	}
	return get(c, GetIn{ID: id})
}

func resolveLocation(ctx context.Context, q vocab.Querier, cid, ref string) (any, error) {
	if strings.TrimSpace(ref) == "" {
		return nil, nil
	}
	id, ok, err := findEntity(ctx, q, cid, ref)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ops.Invalid("location", "No entity in this Compendium is called "+oneLine(ref)+".")
	}
	return id, nil
}

// UpdateIn changes an event. Only what is sent changes.
type UpdateIn struct {
	ID           string         `path:"id" json:"-" desc:"Event id."`
	BaseRevision int            `json:"base_revision,omitempty" desc:"The revision you last read; the change is refused if it moved. 0 skips the check."`
	Title        *string        `json:"title,omitempty"`
	Description  *string        `json:"description,omitempty"`
	StoryDate    *string        `json:"story_date,omitempty" desc:"Empty clears the date (and the end)."`
	StoryDateEnd *string        `json:"story_date_end,omitempty" desc:"Empty clears the end."`
	StoryLabel   *string        `json:"story_label,omitempty"`
	Location     *string        `json:"location,omitempty" desc:"An entity of the Compendium; empty clears it."`
	Participants *[]Participant `json:"participants,omitempty" desc:"Replaces the whole list; send [] to clear it."`
}

// EventID lets the registry find the owning Compendium.
func (in UpdateIn) EventID() string { return in.ID }

// Validate checks what can be checked without the database.
func (in *UpdateIn) Validate() error {
	if in.Participants != nil {
		return checkParticipants(*in.Participants)
	}
	return nil
}

func update(c *ops.Ctx, in UpdateIn) (EventItem, error) {
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var e ev
		var loc sql.NullString
		if err := tx.QueryRow(`SELECT compendium_id, title, description, story_date, story_date_end, story_label, COALESCE(location_entity_id,''), revision, trashed_at IS NOT NULL
			FROM timeline_events WHERE id = ?`, in.ID).Scan(&e.Compendium, &e.Title, &e.Desc, &e.Date, &e.End, &e.Label, &loc.String, &e.Rev, &e.Trashed); err != nil {
			return ops.NotFound("event")
		}
		if e.Trashed {
			return ops.Conflict("This event is in the trash. Restore it before editing.")
		}
		if in.BaseRevision > 0 && in.BaseRevision != e.Rev {
			return ops.ConflictWith("This event changed since you read it. Read it again and re-apply your change.", map[string]any{"revision": e.Rev})
		}
		title, desc, date, end, label := e.Title, e.Desc, e.Date, e.End, e.Label
		if in.Title != nil {
			title = *in.Title
		}
		if in.Description != nil {
			desc = *in.Description
		}
		if in.StoryDate != nil {
			date = *in.StoryDate
			if strings.TrimSpace(date) == "" {
				end = ""
			}
		}
		if in.StoryDateEnd != nil {
			end = *in.StoryDateEnd
		}
		if in.StoryLabel != nil {
			label = *in.StoryLabel
		}
		f, err := clean(title, desc, date, end, label)
		if err != nil {
			return err
		}
		locID := any(nil)
		if loc.String != "" {
			locID = loc.String
		}
		if in.Location != nil {
			if locID, err = resolveLocation(c, tx, e.Compendium, *in.Location); err != nil {
				return err
			}
		}
		var people []person
		peopleSame := true
		if in.Participants != nil {
			if people, err = resolvePeople(c, tx, e.Compendium, *in.Participants); err != nil {
				return err
			}
			cur, err := loadOne(c, tx, in.ID)
			if err != nil {
				return err
			}
			have := map[person]bool{}
			for _, p := range cur.People {
				have[p] = true
			}
			peopleSame = len(have) == len(people)
			for _, p := range people {
				peopleSame = peopleSame && have[p]
			}
		}
		// A change that changes nothing writes nothing: no revision bump (an open
		// editor's next save would be refused) and no event.
		if peopleSame && f.title == e.Title && f.desc == e.Desc && f.date == e.Date && f.end == e.End && f.label == e.Label && fmt.Sprint(orEmpty(locID)) == loc.String {
			return nil
		}
		now := db.Now()
		if _, err := tx.Exec(`UPDATE timeline_events SET title = ?, description = ?, story_date = ?, story_date_end = ?, story_label = ?, sort_start = ?, sort_end = ?,
			location_entity_id = ?, revision = revision + 1, updated_at = ? WHERE id = ?`,
			f.title, f.desc, f.date, f.end, f.label, f.start, f.finish, locID, now, in.ID); err != nil {
			return err
		}
		if in.Participants != nil && !peopleSame {
			if err := replacePeople(tx, in.ID, people, now); err != nil {
				return err
			}
		}
		c.Emit(ops.Event{Type: "timeline.event.updated", CompendiumID: e.Compendium, Payload: map[string]any{"event_id": in.ID}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return EventItem{}, err
	}
	return get(c, GetIn{ID: in.ID})
}

// StateIn names an event to trash or restore.
type StateIn struct {
	ID string `path:"id" json:"-" desc:"Event id."`
}

// EventID lets the registry find the owning Compendium.
func (in StateIn) EventID() string { return in.ID }

func trashEvent(c *ops.Ctx, in StateIn) (EventItem, error)   { return setTrashed(c, in, true) }
func restoreEvent(c *ops.Ctx, in StateIn) (EventItem, error) { return setTrashed(c, in, false) }

// setTrashed moves an event to the trash or back. Its people, scenes and
// place are kept, so a restore is exact. It is idempotent.
func setTrashed(c *ops.Ctx, in StateIn, on bool) (EventItem, error) {
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var cid string
		var trashed bool
		if err := tx.QueryRow(`SELECT compendium_id, trashed_at IS NOT NULL FROM timeline_events WHERE id = ?`, in.ID).Scan(&cid, &trashed); err != nil {
			return ops.NotFound("event")
		}
		if trashed == on {
			return nil
		}
		now := db.Now()
		var err error
		typ, action := "timeline.event.restored", "timeline.event.restore"
		if on {
			typ, action = "timeline.event.trashed", "timeline.event.trash"
			_, err = tx.Exec(`UPDATE timeline_events SET trashed_at = ?, updated_at = ?, revision = revision + 1 WHERE id = ?`, now, now, in.ID)
		} else {
			_, err = tx.Exec(`UPDATE timeline_events SET trashed_at = NULL, updated_at = ?, revision = revision + 1 WHERE id = ?`, now, in.ID)
		}
		if err != nil {
			return err
		}
		c.Emit(ops.Event{Type: typ, CompendiumID: cid, Payload: map[string]any{"event_id": in.ID}})
		c.Audit(ops.AuditEntry{Action: action, TargetType: "timeline_event", TargetID: in.ID})
		return c.FlushTo(tx)
	})
	if err != nil {
		return EventItem{}, err
	}
	return get(c, GetIn{ID: in.ID})
}
