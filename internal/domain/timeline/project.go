package timeline

import (
	"context"
	"sort"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/domain/docref"
	"github.com/crueber/scriobh/internal/domain/storydate"
	"github.com/crueber/scriobh/internal/domain/vocab"
)

// scene is a document of the book as the timeline reads it.
type scene struct {
	ID, ParentID, Kind, Title string
	Date, End                 string // the story date as written on the document
	Index                     int    // reading index among all live documents
	Leaf                      bool   // holds no other document
	Pov                       string // linked point-of-view entity id
}

// item is one place on the project's timeline: an event, or a scene that has a
// date and no event.
type item struct {
	Key    string // "event:<id>" or "scene:<id>"
	Kind   string // event or scene
	ID     string
	Title  string
	Date   string
	End    string
	Label  string
	Iv     storydate.Interval
	Event  *ev
	Scene  *scene   // for a scene item
	Told   []*scene // for an event: the scenes of this book that tell it, in reading order
	nar    float64  // derived narrative position
	hasNar bool
	Story  int // index in story order
	Nar    int // index in narrative order
}

// pdata is everything the project's timeline views are made of.
type pdata struct {
	ProjectID string
	Scenes    map[string]*scene
	Reading   []string // live document ids in reading order
	nodes     map[string]docref.Node
	Cids      []string // the Compendiums the caller may read
	Hidden    bool     // the project draws on a Compendium the caller may not read
	Events    []*ev    // live, in story order
	ByID      map[string]*ev
	Names     map[string]vocab.Brief // entities by id, as the book names them
	Explicit  []string               // the book's own order of events, when set
	Items     []*item                // story order
	Narrative []*item                // narrative order
	ByKey     map[string]*item
	Unplaced  int      // leaf scenes with neither a date nor an event
	Unread    []*scene // scenes whose story date is not a date the timeline can read
	Invalid   []*scene // scenes whose story date end is before the start
}

// loadProject reads a project's timeline for one reader (the zero Reader: the
// mirror, which shows everything the project's owner wired in).
func loadProject(ctx context.Context, q vocab.Querier, r vocab.Reader, projectID string) (*pdata, error) {
	d := &pdata{ProjectID: projectID, Scenes: map[string]*scene{}, ByID: map[string]*ev{}, Names: map[string]vocab.Brief{}, ByKey: map[string]*item{}, nodes: map[string]docref.Node{}}
	rows, err := q.QueryContext(ctx, `SELECT id, COALESCE(parent_id,''), kind, title, position, story_date, story_date_end, COALESCE(pov_entity_id,'')
		FROM documents WHERE project_id = ? AND deleted_at IS NULL AND kind <> 'research'`, projectID)
	if err != nil {
		return nil, err
	}
	var nodes []docref.Node
	for rows.Next() {
		var s scene
		var pos float64
		if err := rows.Scan(&s.ID, &s.ParentID, &s.Kind, &s.Title, &pos, &s.Date, &s.End, &s.Pov); err != nil {
			rows.Close()
			return nil, err
		}
		x := s
		d.Scenes[s.ID] = &x
		n := docref.Node{ID: s.ID, ParentID: s.ParentID, Kind: s.Kind, Title: s.Title, Position: pos}
		nodes = append(nodes, n)
		d.nodes[s.ID] = n
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	d.Reading = docref.Order(nodes)
	hasKids := map[string]bool{}
	for _, s := range d.Scenes {
		if _, ok := d.Scenes[s.ParentID]; ok {
			hasKids[s.ParentID] = true
		}
	}
	for i, id := range d.Reading {
		s := d.Scenes[id]
		s.Index, s.Leaf = i, !hasKids[id]
	}
	scope, primary, err := scopeCompendiums(ctx, q, projectID)
	if err != nil {
		return nil, err
	}
	d.Cids, d.Hidden = readableCompendiums(ctx, q, r, scope, primary)
	if len(d.Cids) > 0 {
		if d.Events, err = loadEvents(ctx, q, d.Cids, false); err != nil {
			return nil, err
		}
		sortStory(d.Events)
		for _, e := range d.Events {
			d.ByID[e.ID] = e
		}
		all, err := vocab.Load(ctx, q, projectID)
		if err != nil {
			return nil, err
		}
		readable := map[string]bool{}
		for _, c := range d.Cids {
			readable[c] = true
		}
		for _, en := range all {
			if readable[en.CompendiumID] {
				d.Names[en.ID] = en.Brief()
			}
		}
	}
	// The book's own order of events.
	orows, err := q.QueryContext(ctx, `SELECT event_id FROM timeline_narrative WHERE project_id = ? ORDER BY position, event_id`, projectID)
	if err != nil {
		return nil, err
	}
	for orows.Next() {
		var id string
		if err := orows.Scan(&id); err != nil {
			orows.Close()
			return nil, err
		}
		if _, ok := d.ByID[id]; ok {
			d.Explicit = append(d.Explicit, id)
		}
	}
	if err := orows.Close(); err != nil {
		return nil, err
	}
	d.build()
	return d, nil
}

// build makes the items and the two orders.
func (d *pdata) build() {
	linked := map[string]bool{}
	for _, e := range d.Events {
		it := &item{Key: "event:" + e.ID, Kind: "event", ID: e.ID, Title: e.Title, Date: e.Date, End: e.End, Label: e.Label, Iv: e.interval(), Event: e}
		for _, l := range e.Docs {
			if l.Project != d.ProjectID {
				continue
			}
			if s, ok := d.Scenes[l.Doc]; ok {
				it.Told = append(it.Told, s)
				linked[s.ID] = true
			}
		}
		sort.Slice(it.Told, func(i, j int) bool { return it.Told[i].Index < it.Told[j].Index })
		if len(it.Told) > 0 {
			it.nar, it.hasNar = float64(it.Told[0].Index)-0.5, true
		}
		d.Items = append(d.Items, it)
	}
	var scenes []*scene
	for _, id := range d.Reading {
		scenes = append(scenes, d.Scenes[id])
	}
	for _, s := range scenes {
		if !s.Leaf || (s.Kind != "scene" && s.Kind != "chapter") || linked[s.ID] {
			continue
		}
		switch {
		case strings.TrimSpace(s.Date) == "":
			d.Unplaced++
		default:
			if _, ok := storydate.Parse(s.Date); !ok {
				d.Unread = append(d.Unread, s)
				continue
			}
			iv := storydate.Span(s.Date, s.End)
			if !iv.Valid {
				d.Invalid = append(d.Invalid, s)
				iv = storydate.Span(s.Date, "")
			}
			d.Items = append(d.Items, &item{Key: "scene:" + s.ID, Kind: "scene", ID: s.ID, Title: s.Title, Date: s.Date, End: s.End, Iv: iv, Scene: s,
				nar: float64(s.Index), hasNar: true})
		}
	}
	// Story order.
	sort.SliceStable(d.Items, func(i, j int) bool {
		a, b := d.Items[i], d.Items[j]
		return storyLess(a.Iv.Start, a.Iv.End, a.Title, a.Key, b.Iv.Start, b.Iv.End, b.Title, b.Key)
	})
	for i, it := range d.Items {
		it.Story = i
		d.ByKey[it.Key] = it
	}
	// Narrative order: scenes at their reading place, each event just before
	// the first scene that tells it, events nothing tells after everything, in
	// story order. An explicit order of the book then permutes the events
	// among the places events already hold; scenes never move.
	seq := append([]*item(nil), d.Items...)
	sort.SliceStable(seq, func(i, j int) bool {
		a, b := seq[i], seq[j]
		if a.hasNar != b.hasNar {
			return a.hasNar
		}
		if a.hasNar && a.nar != b.nar {
			return a.nar < b.nar
		}
		return a.Story < b.Story
	})
	if len(d.Explicit) > 0 {
		var slots []int
		derived := map[string]int{}
		for i, it := range seq {
			if it.Kind == "event" {
				slots = append(slots, i)
				derived[it.ID] = len(derived)
			}
		}
		var evs []*item
		seen := map[string]bool{}
		for _, id := range d.Explicit {
			if it, ok := d.ByKey["event:"+id]; ok && !seen[id] {
				seen[id] = true
				evs = append(evs, it)
			}
		}
		var rest []*item
		for _, i := range slots {
			if !seen[seq[i].ID] {
				rest = append(rest, seq[i])
			}
		}
		evs = append(evs, rest...)
		for k, i := range slots {
			seq[i] = evs[k]
		}
	}
	for i, it := range seq {
		it.Nar = i
	}
	d.Narrative = seq
}

// ref names a scene with its breadcrumb.
func (d *pdata) crumbs(id string) []Crumb {
	out := []Crumb{}
	n, ok := d.nodes[id]
	if !ok {
		return out
	}
	for p, hops := n.ParentID, 0; p != "" && hops < 64; hops++ {
		a, ok := d.nodes[p]
		if !ok {
			break
		}
		out = append([]Crumb{{ID: a.ID, Title: a.Title, Kind: a.Kind}}, out...)
		p = a.ParentID
	}
	return out
}

// Crumb is one ancestor in a breadcrumb.
type Crumb struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Kind  string `json:"kind"`
}

// ---- stamps and rendering for the mirror -----------------------------------------------------------------------------

func itoa(n int) string { return strconv.Itoa(n) }
