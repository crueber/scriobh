package timeline

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/vocab"
)

// The timeline as Markdown: timeline.md in a Compendium's mirror folder and in
// a project's compendium/ folder. Generated, deterministic (the same state is
// the same bytes, and nothing depends on the clock), never read back.

const timelineHeader = "# Timeline\n\nGenerated from the timeline in Scríobh, in story order. Edit the timeline there: this file is overwritten.\n"

// dateLine is how an event's date reads in a heading.
func dateLine(date, end, label string) string {
	switch {
	case date == "" && label == "":
		return "Undated"
	case date == "":
		return label
	}
	s := date
	if end != "" {
		s += " to " + end
	}
	if label != "" {
		s += " (" + label + ")"
	}
	return s
}

func writeEvent(b *strings.Builder, e *ev, names map[string]vocab.Brief, told []*scene, crumbs func(string) []Crumb) {
	fmt.Fprintf(b, "\n## %s: %s\n\n- id: %s\n", dateLine(e.Date, e.End, e.Label), oneLine(e.Title), e.ID)
	if n, ok := names[e.Loc]; ok {
		fmt.Fprintf(b, "- where: %s\n", n.Name)
	}
	var who []string
	for _, p := range e.People {
		if n, ok := names[p.Entity]; ok {
			s := n.Name
			if p.Role != "" {
				s += " (" + p.Role + ")"
			}
			who = append(who, s)
		}
	}
	if len(who) > 0 {
		fmt.Fprintf(b, "- who: %s\n", strings.Join(who, ", "))
	}
	if len(told) > 0 {
		var ts []string
		for _, s := range told {
			path := []string{}
			for _, c := range crumbs(s.ID) {
				path = append(path, oneLine(c.Title))
			}
			path = append(path, oneLine(s.Title))
			ts = append(ts, strings.Join(path, " > "))
		}
		fmt.Fprintf(b, "- told in: %s\n", strings.Join(ts, "; "))
	}
	if strings.TrimSpace(e.Desc) != "" {
		b.WriteString("\n" + strings.TrimRight(e.Desc, "\n") + "\n")
	}
}

func renderCompendium(ctx context.Context, q entities.Querier, cid string) ([]byte, error) {
	es, err := loadEvents(ctx, q, []string{cid}, false)
	if err != nil || len(es) == 0 {
		return nil, err
	}
	names, err := compBriefs(ctx, q, []string{cid})
	if err != nil {
		return nil, err
	}
	sortStory(es)
	var b strings.Builder
	b.WriteString(timelineHeader)
	for _, e := range es {
		writeEvent(&b, e, names, nil, nil)
	}
	return []byte(b.String()), nil
}

func renderProject(ctx context.Context, q entities.Querier, projectID string) ([]byte, error) {
	d, err := loadProject(ctx, q, vocab.Reader{}, projectID)
	if err != nil {
		return nil, err
	}
	if len(d.Items) == 0 {
		return nil, nil
	}
	var b strings.Builder
	b.WriteString(timelineHeader)
	for _, it := range d.Items {
		if it.Kind == "event" {
			writeEvent(&b, it.Event, d.Names, it.Told, d.crumbs)
			continue
		}
		fmt.Fprintf(&b, "\n## %s: %s\n\n- scene: %s\n", dateLine(it.Date, it.End, ""), oneLine(it.Title), it.ID)
	}
	b.WriteString("\n## The order this book tells it\n\n")
	for i, it := range d.Narrative {
		fmt.Fprintf(&b, "%d. %s (%s)\n", i+1, oneLine(it.Title), dateLine(it.Date, it.End, it.Label))
	}
	if len(d.Explicit) > 0 {
		b.WriteString("\nThe order of events was set by hand for this book.\n")
	}
	return []byte(b.String()), nil
}

// ---- fingerprints ------------------------------------------------------------------------------------------------------

func stampOf(lines []string) string {
	sort.Strings(lines)
	h := sha256.New()
	for _, l := range lines {
		h.Write([]byte(l))
		h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// compendiumStamps hashes each Compendium's events (id, revision, update time,
// state): every change to an event, its people or its place moves them.
func compendiumStamps(ctx context.Context, q entities.Querier, only []string) (map[string]string, error) {
	where, args := "", []any(nil)
	if only != nil {
		if len(only) == 0 {
			return map[string]string{}, nil
		}
		where, args = " WHERE compendium_id IN ("+placeholders(len(only))+")", anys(only)
	}
	rows, err := q.QueryContext(ctx, `SELECT compendium_id, id, revision, updated_at, trashed_at IS NOT NULL FROM timeline_events`+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	lines := map[string][]string{}
	for rows.Next() {
		var cid, id, at string
		var rev int
		var trashed bool
		if err := rows.Scan(&cid, &id, &rev, &at, &trashed); err != nil {
			return nil, err
		}
		lines[cid] = append(lines[cid], fmt.Sprintf("%s %d %s %v", id, rev, at, trashed))
	}
	out := map[string]string{}
	for cid, l := range lines {
		out[cid] = stampOf(l)
	}
	return out, rows.Err()
}

// projectStamps hashes what a project's timeline file depends on beyond its
// Compendiums' events (the mirror carries a project's compendium/ files
// forward while the projection's hash is unchanged, so anything the file shows
// must move it): which scenes tell which events, the book's own order, and the
// outline tuple of every live document (id, place, kind, title, story dates,
// point of view), which decides the scenes' titles, breadcrumbs, reading order
// and dates. Document text is left out on purpose: saving a chapter must not
// render this file. Only projects that have something on a timeline appear; the
// others hash as they always did.
func projectStamps(ctx context.Context, q entities.Querier, only string) (map[string]string, error) {
	pw, dw, args := "", "", []any(nil)
	if only != "" {
		pw, dw, args = " AND p.id = ?", " AND project_id = ?", []any{only}
	}
	qual, err := q.QueryContext(ctx, `SELECT p.id FROM projects p WHERE p.deleted_at IS NULL`+pw+` AND (
		EXISTS (SELECT 1 FROM timeline_events e WHERE e.compendium_id = p.compendium_id)
		OR EXISTS (SELECT 1 FROM compendium_refs r JOIN timeline_events e ON e.compendium_id = r.target_compendium_id WHERE r.owner_kind = 'project' AND r.owner_id = p.id)
		OR EXISTS (SELECT 1 FROM documents d WHERE d.project_id = p.id AND d.deleted_at IS NULL AND d.story_date <> ''))`, args...)
	if err != nil {
		return nil, err
	}
	lines := map[string][]string{}
	for qual.Next() {
		var id string
		if err := qual.Scan(&id); err != nil {
			qual.Close()
			return nil, err
		}
		lines[id] = []string{}
	}
	if err := qual.Close(); err != nil {
		return nil, err
	}
	if len(lines) == 0 {
		return map[string]string{}, nil
	}
	where := ""
	if only != "" {
		where = " WHERE project_id = ?"
	}
	drows, err := q.QueryContext(ctx, `SELECT project_id, id, COALESCE(parent_id,''), kind, title, position, story_date, story_date_end, COALESCE(pov_entity_id,'')
		FROM documents WHERE deleted_at IS NULL AND kind <> 'research'`+dw, args...)
	if err != nil {
		return nil, err
	}
	for drows.Next() {
		var pid, id, parent, kind, title, sd, se, pov string
		var pos float64
		if err := drows.Scan(&pid, &id, &parent, &kind, &title, &pos, &sd, &se, &pov); err != nil {
			drows.Close()
			return nil, err
		}
		if _, ok := lines[pid]; ok {
			lines[pid] = append(lines[pid], fmt.Sprintf("d %s %s %s %q %v %q %q %s", id, parent, kind, title, pos, sd, se, pov))
		}
	}
	if err := drows.Close(); err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT project_id, event_id, document_id FROM timeline_event_docs`+where, args...)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var pid, eid, did string
		if err := rows.Scan(&pid, &eid, &did); err != nil {
			rows.Close()
			return nil, err
		}
		if _, ok := lines[pid]; ok {
			lines[pid] = append(lines[pid], "l "+eid+" "+did)
		}
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	nrows, err := q.QueryContext(ctx, `SELECT project_id, event_id, position FROM timeline_narrative`+where, args...)
	if err != nil {
		return nil, err
	}
	for nrows.Next() {
		var pid, eid string
		var pos float64
		if err := nrows.Scan(&pid, &eid, &pos); err != nil {
			nrows.Close()
			return nil, err
		}
		if _, ok := lines[pid]; ok {
			lines[pid] = append(lines[pid], fmt.Sprintf("n %s %v", eid, pos))
		}
	}
	if err := nrows.Close(); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for pid, l := range lines {
		out[pid] = stampOf(l)
	}
	return out, nil
}
