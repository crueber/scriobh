package timeline

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/domain/storydate"
	"github.com/crueber/scriobh/internal/domain/vocab"
	"github.com/crueber/scriobh/internal/ops"
)

// The contradiction finder. It reads only what is stored (events, scene dates,
// the mention index, two optional custom fields) and applies fixed rules: the
// same data always gives the same findings. Every finding is data: a kind, a
// severity, the things involved, and a sentence.
//
// Kinds:
//
//	location_conflict   error or warning: the same person is at two different
//	                             places at the same time. Both events must name
//	                             a place and a definite span (a day or finer, or
//	                             an explicit end); "1998" against "1998-04-12"
//	                             is vagueness, not a contradiction. It is an
//	                             error only when both events are timed (a
//	                             clock time) or have an explicit end: two
//	                             events on the same date can be hours apart, so
//	                             day against day is a warning to look at.
//	before_birth        error    an event a person takes part in is entirely
//	                             before their `born` date.
//	after_death         error    ... entirely after their `died` date.
//	invalid_range       error    a scene's story date end is before its start.
//	narrated_out_of_order warning something is told in this book after
//	                             something that happened later (a flashback is
//	                             legitimate, so this is for the writer to judge).
//	scene_before_first_event warning a scene dated before the first event a
//	                             person is recorded in mentions them (or is
//	                             told from their point of view).
//	unreadable_date     warning  scenes whose story date is not a date the
//	                             timeline can read; they are left off it.
//
// `born` and `died` are custom fields of an entity (type date, both optional,
// defined per Compendium with entity.field.set); a book's override of them
// applies in that book's check.

// EventRef names an event in a finding.
type EventRef struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	StoryDate string `json:"story_date,omitempty"`
}

// SceneRef names a scene in a finding.
type SceneRef struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	StoryDate  string  `json:"story_date,omitempty"`
	Breadcrumb []Crumb `json:"breadcrumb"`
}

// Finding is one contradiction or doubt.
type Finding struct {
	ID       string        `json:"id" desc:"Stable for the same kind and the same things involved."`
	Kind     string        `json:"kind" enum:"location_conflict,before_birth,after_death,invalid_range,narrated_out_of_order,scene_before_first_event,unreadable_date"`
	Severity string        `json:"severity" enum:"error,warning"`
	Message  string        `json:"message" desc:"A sentence a person can act on."`
	Events   []EventRef    `json:"events"`
	Scenes   []SceneRef    `json:"scenes"`
	Entities []vocab.Brief `json:"entities"`
}

// CheckOut is the result of a check.
type CheckOut struct {
	Findings []Finding `json:"findings" desc:"Errors first, then warnings; then by kind and id. At most 200 of a kind."`
	Errors   int       `json:"errors"`
	Warnings int       `json:"warnings"`
	// Truncated counts, by kind, the findings left out of Findings because a
	// kind is capped (MaxFindingsPerKind). When a scan stopped early the number
	// is a lower bound.
	Truncated map[string]int `json:"truncated" desc:"By kind, findings left out because a kind shows at most 200. A lower bound when the scan stopped early."`
	Events    int            `json:"events_checked"`
	Scenes    int            `json:"scenes_checked"`
	Hidden    bool           `json:"hidden" desc:"The project draws on a Compendium the caller may not read: its events and what they imply are left out."`
	Indexing  bool           `json:"indexing" desc:"The mention index owes a scan; scene findings that depend on mentions may be behind."`
}

// MaxFindingsPerKind caps how many findings of one kind a check returns, and
// maxLocationPairs how many overlapping pairs the location scan collects before
// it stops: thousands of long, overlapping events would otherwise be quadratic
// in both time and answer.
const (
	MaxFindingsPerKind = 200
	maxLocationPairs   = 2000
)

// CheckIn names a project.
type CheckIn struct {
	ID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in CheckIn) ProjectRef() string { return in.ID }

// CompendiumCheckIn names a Compendium.
type CompendiumCheckIn struct {
	ID string `path:"id" json:"-" desc:"Compendium id."`
}

// CompendiumRef lets the registry authorize by membership.
func (in CompendiumCheckIn) CompendiumRef() string { return in.ID }

type facts struct {
	born, died         map[string]storydate.Interval
	bornText, diedText map[string]string
}

func fieldFacts(ctx context.Context, q vocab.Querier, projectID string, entityIDs []string) (facts, error) {
	f := facts{born: map[string]storydate.Interval{}, died: map[string]storydate.Interval{}, bornText: map[string]string{}, diedText: map[string]string{}}
	if len(entityIDs) == 0 {
		return f, nil
	}
	args := append([]any{projectID}, anys(entityIDs)...)
	rows, err := q.QueryContext(ctx, `SELECT e.id, e.fields_json, COALESCE(o.fields_json, '{}') FROM entities e
		LEFT JOIN project_entity_overrides o ON o.project_id = ? AND o.entity_id = e.id
		WHERE e.id IN (`+placeholders(len(entityIDs))+`)`, args...)
	if err != nil {
		return f, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, base, over string
		if err := rows.Scan(&id, &base, &over); err != nil {
			return f, err
		}
		fields := map[string]string{}
		_ = json.Unmarshal([]byte(base), &fields)
		o := map[string]string{}
		_ = json.Unmarshal([]byte(over), &o)
		for k, v := range o {
			fields[k] = v // an empty override blanks the field for this book
		}
		if iv := storydate.Span(fields["born"], ""); iv.Dated() {
			f.born[id], f.bornText[id] = iv, fields["born"]
		}
		if iv := storydate.Span(fields["died"], ""); iv.Dated() {
			f.died[id], f.diedText[id] = iv, fields["died"]
		}
	}
	return f, rows.Err()
}

func findingID(kind string, ids ...string) string {
	sort.Strings(ids)
	sum := sha256.Sum256([]byte(kind + "\x00" + strings.Join(ids, "\x00")))
	return hex.EncodeToString(sum[:8])
}

// check runs every rule over a project's data (or, with no project, over one
// Compendium's events alone).
func check(ctx context.Context, q vocab.Querier, d *pdata) (CheckOut, error) {
	out := CheckOut{Findings: []Finding{}, Truncated: map[string]int{}, Hidden: d.Hidden, Events: len(d.Events)}
	name := func(id string) vocab.Brief { return d.Names[id] }
	evRef := func(e *ev) EventRef { return EventRef{ID: e.ID, Title: e.Title, StoryDate: e.Date} }
	scRef := func(s *scene) SceneRef {
		return SceneRef{ID: s.ID, Title: s.Title, StoryDate: s.Date, Breadcrumb: d.crumbs(s.ID)}
	}
	add := func(f Finding) {
		if f.Events == nil {
			f.Events = []EventRef{}
		}
		if f.Scenes == nil {
			f.Scenes = []SceneRef{}
		}
		if f.Entities == nil {
			f.Entities = []vocab.Brief{}
		}
		out.Findings = append(out.Findings, f)
	}
	// Participants of every event, and who has facts to check.
	people := map[string][]*ev{}
	var ids []string
	for _, e := range d.Events {
		for _, p := range e.People {
			if _, ok := d.Names[p.Entity]; !ok {
				continue
			}
			if _, seen := people[p.Entity]; !seen {
				ids = append(ids, p.Entity)
			}
			people[p.Entity] = append(people[p.Entity], e)
		}
	}
	sort.Strings(ids)
	fx, err := fieldFacts(ctx, q, d.ProjectID, ids)
	if err != nil {
		return out, err
	}

	// location_conflict: one finding per pair of events, naming the people at both.
	type pair struct{ a, b string }
	together := map[pair][]string{}
	scanStopped := false
	for _, pid := range ids {
		if scanStopped {
			break
		}
		var es []*ev
		for _, e := range people[pid] {
			if e.Loc != "" && e.dated() && e.interval().Definite {
				es = append(es, e)
			}
		}
		sort.SliceStable(es, func(i, j int) bool { return es[i].Start < es[j].Start })
		for i := range es {
			if scanStopped {
				break
			}
			for j := i + 1; j < len(es); j++ {
				if es[j].Start > es[i].Finish { // sorted by start: nothing later overlaps this one
					break
				}
				if len(together) >= maxLocationPairs {
					scanStopped = true
					break
				}
				if es[i].Loc != es[j].Loc && es[i].interval().Overlaps(es[j].interval()) {
					a, b := es[i].ID, es[j].ID
					if a > b {
						a, b = b, a
					}
					together[pair{a, b}] = append(together[pair{a, b}], pid)
				}
			}
		}
	}
	var pairs []pair
	for p := range together {
		pairs = append(pairs, p)
	}
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].a+pairs[i].b < pairs[j].a+pairs[j].b })
	for _, p := range pairs {
		a, b := d.ByID[p.a], d.ByID[p.b]
		var who []vocab.Brief
		var names []string
		for _, pid := range together[p] {
			who = append(who, name(pid))
			names = append(names, name(pid).Name)
		}
		who = append(who, name(a.Loc), name(b.Loc))
		severity := "warning"
		if firm(a) && firm(b) {
			severity = "error"
		}
		add(Finding{ID: findingID("location_conflict", p.a, p.b), Kind: "location_conflict", Severity: severity,
			Message: join(names) + " cannot be at " + name(a.Loc).Name + " for \"" + a.Title + "\" (" + when(a) + ") and at " + name(b.Loc).Name + " for \"" + b.Title + "\" (" + when(b) + "): the two overlap.",
			Events:  []EventRef{evRef(a), evRef(b)}, Entities: who})
	}

	// before_birth and after_death.
	for _, pid := range ids {
		for _, e := range people[pid] {
			if !e.dated() {
				continue
			}
			iv := e.interval()
			if born, ok := fx.born[pid]; ok && iv.Before(born) {
				add(Finding{ID: findingID("before_birth", e.ID, pid), Kind: "before_birth", Severity: "error",
					Message: name(pid).Name + " takes part in \"" + e.Title + "\" (" + when(e) + "), before their birth (" + fx.bornText[pid] + ").",
					Events:  []EventRef{evRef(e)}, Entities: []vocab.Brief{name(pid)}})
			}
			if died, ok := fx.died[pid]; ok && died.Before(iv) {
				add(Finding{ID: findingID("after_death", e.ID, pid), Kind: "after_death", Severity: "error",
					Message: name(pid).Name + " takes part in \"" + e.Title + "\" (" + when(e) + "), after their death (" + fx.diedText[pid] + ").",
					Events:  []EventRef{evRef(e)}, Entities: []vocab.Brief{name(pid)}})
			}
		}
	}

	if d.ProjectID != "" {
		// invalid_range and unreadable_date: the scenes' own dates.
		for _, s := range d.Invalid {
			add(Finding{ID: findingID("invalid_range", s.ID), Kind: "invalid_range", Severity: "error",
				Message: "The scene \"" + s.Title + "\" ends (" + s.End + ") before it starts (" + s.Date + ").", Scenes: []SceneRef{scRef(s)}})
		}
		if len(d.Unread) > 0 {
			f := Finding{Kind: "unreadable_date", Severity: "warning"}
			var sid []string
			for i, s := range d.Unread {
				sid = append(sid, s.ID)
				if i < 100 {
					f.Scenes = append(f.Scenes, scRef(s))
				}
			}
			f.ID = findingID("unreadable_date", sid...)
			f.Message = itoa(len(d.Unread)) + " scene(s) have a story date the timeline cannot read, so they are left off it. Use a year, a month or a day such as 1998, 1998-04 or 1998-04-12."
			add(f)
		}
		// narrated_out_of_order: compare each thing the book tells with the one
		// it told just before. A step back in time (it ended before the previous
		// one began) is a flashback, or a flash-forward prologue followed by the
		// story proper; consecutive steps back are one run and one finding, so a
		// prologue is a single warning however many scenes follow it.
		var prev *item
		var run *Finding
		var runKeys []string
		flush := func() {
			if run != nil {
				run.ID = findingID("narrated_out_of_order", runKeys...)
				add(*run)
				run, runKeys = nil, nil
			}
		}
		addRef := func(f *Finding, x *item) {
			if x.Kind == "event" {
				f.Events = append(f.Events, evRef(x.Event))
			} else {
				f.Scenes = append(f.Scenes, scRef(x.Scene))
			}
		}
		for _, it := range d.Narrative {
			// Only what a scene tells has a place in the telling: an event no scene
			// tells (backstory) is not "told late" by sitting at the end of the list.
			if !it.hasNar || !it.Iv.Dated() || !it.Iv.Valid {
				continue
			}
			if prev != nil && it.Iv.Before(prev.Iv) {
				if run == nil {
					run = &Finding{Kind: "narrated_out_of_order", Severity: "warning",
						Message: "\"" + it.Title + "\" (" + itemWhen(it) + ") is told after \"" + prev.Title + "\" (" + itemWhen(prev) + "), which happens later. A flashback or a prologue that looks ahead is fine; this only says where the telling steps back in time."}
					runKeys = append(runKeys, prev.Key)
					addRef(run, prev)
				}
				runKeys = append(runKeys, it.Key)
				addRef(run, it)
			} else {
				flush()
			}
			prev = it
		}
		flush()
		// scene_before_first_event: a scene dated before the first event a person
		// is recorded in, that mentions them or is told from their point of view.
		first := map[string]*ev{}
		for _, pid := range ids {
			for _, e := range people[pid] {
				if e.dated() && (first[pid] == nil || e.Start < first[pid].Start) {
					first[pid] = e
				}
			}
		}
		if len(first) > 0 {
			rows, err := q.QueryContext(ctx, `SELECT document_id, entity_id FROM entity_mentions WHERE project_id = ? AND (count > 0 OR explicit > 0)`, d.ProjectID)
			if err != nil {
				return out, err
			}
			mentioned := map[string]map[string]bool{} // scene -> entities
			for rows.Next() {
				var doc, ent string
				if err := rows.Scan(&doc, &ent); err != nil {
					rows.Close()
					return out, err
				}
				if first[ent] != nil {
					if mentioned[doc] == nil {
						mentioned[doc] = map[string]bool{}
					}
					mentioned[doc][ent] = true
				}
			}
			if err := rows.Err(); err != nil {
				_ = rows.Close()
				return out, err
			}
			if err := rows.Close(); err != nil {
				return out, err
			}
			for _, s := range d.Scenes {
				if s.Pov != "" && first[s.Pov] != nil {
					if mentioned[s.ID] == nil {
						mentioned[s.ID] = map[string]bool{}
					}
					mentioned[s.ID][s.Pov] = true
				}
			}
			var sids []string
			for id := range mentioned {
				sids = append(sids, id)
			}
			sort.Strings(sids)
			for _, sid := range sids {
				s := d.Scenes[sid]
				if s == nil || !s.Leaf || strings.TrimSpace(s.Date) == "" {
					continue
				}
				iv := storydate.Span(s.Date, s.End)
				if !iv.Valid {
					iv = storydate.Span(s.Date, "")
				}
				if !iv.Dated() {
					continue
				}
				var eids []string
				for e := range mentioned[sid] {
					eids = append(eids, e)
				}
				sort.Strings(eids)
				for _, pid := range eids {
					fe := first[pid]
					if iv.Before(fe.interval()) {
						add(Finding{ID: findingID("scene_before_first_event", sid, pid), Kind: "scene_before_first_event", Severity: "warning",
							Message: "The scene \"" + s.Title + "\" (" + s.Date + ") features " + name(pid).Name + ", whose first recorded event, \"" + fe.Title + "\", is later (" + when(fe) + ").",
							Scenes:  []SceneRef{scRef(s)}, Events: []EventRef{evRef(fe)}, Entities: []vocab.Brief{name(pid)}})
					}
				}
			}
			var owedScan bool
			var stamp, done string
			if err := q.QueryRowContext(ctx, `SELECT stamp, indexed_stamp FROM mention_state WHERE project_id = ?`, d.ProjectID).Scan(&stamp, &done); err == nil {
				owedScan = stamp != done
			} else if err == sql.ErrNoRows {
				owedScan = len(d.Scenes) > 0
			}
			out.Indexing = owedScan
		}
		for _, s := range d.Scenes {
			if s.Leaf && (s.Kind == "scene" || s.Kind == "chapter") {
				out.Scenes++
			}
		}
	}
	sort.SliceStable(out.Findings, func(i, j int) bool {
		a, b := out.Findings[i], out.Findings[j]
		if a.Severity != b.Severity {
			return a.Severity == "error"
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.ID < b.ID
	})
	// Cap each kind, keeping the first in the order above (errors first), and say
	// how many were left out.
	kept := out.Findings[:0:0]
	perKind := map[string]int{}
	for _, f := range out.Findings {
		perKind[f.Kind]++
		if perKind[f.Kind] > MaxFindingsPerKind {
			out.Truncated[f.Kind]++
			continue
		}
		kept = append(kept, f)
	}
	if scanStopped {
		out.Truncated["location_conflict"] += 1 // the scan stopped early: at least one more exists
	}
	out.Findings = kept
	for _, f := range out.Findings {
		if f.Severity == "error" {
			out.Errors++
		} else {
			out.Warnings++
		}
	}
	return out, nil
}

func join(s []string) string {
	sort.Strings(s)
	switch len(s) {
	case 0:
		return ""
	case 1:
		return s[0]
	}
	return strings.Join(s[:len(s)-1], ", ") + " and " + s[len(s)-1]
}

// firm reports whether an event's span is pinned down tightly enough that an
// overlap is a contradiction rather than a coincidence of dates: it has an
// explicit end or a clock time.
func firm(e *ev) bool {
	return e.End != "" || strings.Contains(e.Date, "T")
}

func when(e *ev) string {
	if e.Date == "" {
		return "undated"
	}
	if e.End != "" {
		return e.Date + " to " + e.End
	}
	return e.Date
}

func itemWhen(it *item) string {
	if it.End != "" {
		return it.Date + " to " + it.End
	}
	return it.Date
}

func check1(c *ops.Ctx, in CheckIn) (CheckOut, error) {
	tx, err := c.DB().Read().BeginTx(c, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CheckOut{}, err
	}
	defer tx.Rollback()
	d, err := loadProject(c, tx, vocab.ReaderOf(c.P), in.ID)
	if err != nil {
		return CheckOut{}, err
	}
	return check(c, tx, d)
}

func checkCompendium(c *ops.Ctx, in CompendiumCheckIn) (CheckOut, error) {
	tx, err := c.DB().Read().BeginTx(c, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return CheckOut{}, err
	}
	defer tx.Rollback()
	d := &pdata{Scenes: map[string]*scene{}, ByID: map[string]*ev{}, Names: map[string]vocab.Brief{}, ByKey: map[string]*item{}, nodes: nil}
	if d.Events, err = loadEvents(c, tx, []string{in.ID}, false); err != nil {
		return CheckOut{}, err
	}
	sortStory(d.Events)
	for _, e := range d.Events {
		d.ByID[e.ID] = e
	}
	if d.Names, err = compBriefs(c, tx, []string{in.ID}); err != nil {
		return CheckOut{}, err
	}
	return check(c, tx, d)
}
