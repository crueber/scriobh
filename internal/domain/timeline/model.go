// Package timeline is the story world's timeline: events with story dates,
// the people who took part, where it happened, and the scenes that tell it.
//
// One timeline belongs to a Compendium, so every book that uses the Compendium
// reads the same events. Each book keeps its own narrative order (the order it
// tells things in) beside the story order (when they happened); the project
// view shows both, and timeline.check finds the places where the two, or the
// facts, contradict each other, by fixed rules.
//
// Dates are the partial, ISO-like dates of package storydate. Access follows
// the Compendium: an event is the bible's, so it is read through Compendium
// membership; a project's view shows only the events of the Compendiums the
// caller may read. Scenes are the book's; linking one to an event needs both.
package timeline

import (
	"context"
	"database/sql"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/domain/storydate"
	"github.com/crueber/scriobh/internal/domain/vocab"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Limits.
const (
	maxTitle        = 200
	maxLabel        = 120
	maxRole         = 60
	maxDescription  = 20_000
	maxParticipants = 100
	// ListMax caps one page of events.
	ListMax = 500
)

// ev is an event row with its people and linked scenes.
type ev struct {
	ID, Compendium, Title, Desc string
	Date, End, Label            string
	Start, Finish               string // sort keys; Start is "" when undated
	Loc                         string // location entity id, "" for none
	Rev                         int
	At                          string
	Trashed                     bool
	People                      []person
	Docs                        []docLink
}

type person struct{ Entity, Role string }

type docLink struct{ Doc, Project string }

func (e *ev) interval() storydate.Interval { return storydate.Span(e.Date, e.End) }

// dated reports whether the event has a story date.
func (e *ev) dated() bool { return e.Start != "" }

// storyLess is the story order: dated events by start, then end; undated
// after every dated one; ties by title (folded) then id. Deterministic.
func storyLess(aStart, aEnd, aTitle, aID, bStart, bEnd, bTitle, bID string) bool {
	if (aStart == "") != (bStart == "") {
		return aStart != ""
	}
	if aStart != bStart {
		return aStart < bStart
	}
	if aEnd != bEnd {
		return aEnd < bEnd
	}
	if at, bt := text.FoldTitle(aTitle), text.FoldTitle(bTitle); at != bt {
		return at < bt
	}
	return aID < bID
}

func sortStory(es []*ev) {
	sort.SliceStable(es, func(i, j int) bool {
		a, b := es[i], es[j]
		return storyLess(a.Start, a.Finish, a.Title, a.ID, b.Start, b.Finish, b.Title, b.ID)
	})
}

func placeholders(n int) string {
	if n == 0 {
		return ""
	}
	return strings.TrimSuffix(strings.Repeat("?,", n), ",")
}

func anys(ss []string) []any {
	out := make([]any, len(ss))
	for i, s := range ss {
		out[i] = s
	}
	return out
}

// loadEvents reads the events of the given Compendiums with their people and
// scene links, in three queries. Trashed events are included only on request.
func loadEvents(ctx context.Context, q vocab.Querier, cids []string, trashed bool) ([]*ev, error) {
	if len(cids) == 0 {
		return nil, nil
	}
	where := ` WHERE compendium_id IN (` + placeholders(len(cids)) + `)`
	if !trashed {
		where += ` AND trashed_at IS NULL`
	}
	rows, err := q.QueryContext(ctx, `SELECT id, compendium_id, title, description, story_date, story_date_end, story_label, sort_start, sort_end,
		COALESCE(location_entity_id,''), revision, updated_at, trashed_at IS NOT NULL FROM timeline_events`+where, anys(cids)...)
	if err != nil {
		return nil, err
	}
	var out []*ev
	byID := map[string]*ev{}
	for rows.Next() {
		e := &ev{}
		if err := rows.Scan(&e.ID, &e.Compendium, &e.Title, &e.Desc, &e.Date, &e.End, &e.Label, &e.Start, &e.Finish, &e.Loc, &e.Rev, &e.At, &e.Trashed); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, e)
		byID[e.ID] = e
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return out, nil
	}
	// People and links, by Compendium rather than by a long list of event ids.
	prows, err := q.QueryContext(ctx, `SELECT p.event_id, p.entity_id, p.role FROM timeline_event_people p JOIN timeline_events e ON e.id = p.event_id
		WHERE e.compendium_id IN (`+placeholders(len(cids))+`) ORDER BY p.event_id, p.created_at, p.entity_id`, anys(cids)...)
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
	if err := prows.Err(); err != nil {
		_ = prows.Close()
		return nil, err
	}
	if err := prows.Close(); err != nil {
		return nil, err
	}
	drows, err := q.QueryContext(ctx, `SELECT l.event_id, l.document_id, l.project_id FROM timeline_event_docs l JOIN timeline_events e ON e.id = l.event_id
		WHERE e.compendium_id IN (`+placeholders(len(cids))+`) ORDER BY l.event_id, l.document_id`, anys(cids)...)
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
	return out, drows.Close()
}

// loadOne reads one event with its people and scene links: three queries
// scoped to the event, however large its Compendium is.
func loadOne(ctx context.Context, q vocab.Querier, id string) (*ev, error) {
	e := &ev{}
	err := q.QueryRowContext(ctx, `SELECT id, compendium_id, title, description, story_date, story_date_end, story_label, sort_start, sort_end,
		COALESCE(location_entity_id,''), revision, updated_at, trashed_at IS NOT NULL FROM timeline_events WHERE id = ?`, id).
		Scan(&e.ID, &e.Compendium, &e.Title, &e.Desc, &e.Date, &e.End, &e.Label, &e.Start, &e.Finish, &e.Loc, &e.Rev, &e.At, &e.Trashed)
	if err == sql.ErrNoRows {
		return nil, ops.NotFound("event")
	}
	if err != nil {
		return nil, err
	}
	prows, err := q.QueryContext(ctx, `SELECT entity_id, role FROM timeline_event_people WHERE event_id = ? ORDER BY created_at, entity_id`, id)
	if err != nil {
		return nil, err
	}
	for prows.Next() {
		var p person
		if err := prows.Scan(&p.Entity, &p.Role); err != nil {
			prows.Close()
			return nil, err
		}
		e.People = append(e.People, p)
	}
	if err := prows.Err(); err != nil {
		_ = prows.Close()
		return nil, err
	}
	if err := prows.Close(); err != nil {
		return nil, err
	}
	drows, err := q.QueryContext(ctx, `SELECT document_id, project_id FROM timeline_event_docs WHERE event_id = ? ORDER BY document_id`, id)
	if err != nil {
		return nil, err
	}
	defer drows.Close()
	for drows.Next() {
		var l docLink
		if err := drows.Scan(&l.Doc, &l.Project); err != nil {
			return nil, err
		}
		e.Docs = append(e.Docs, l)
	}
	return e, drows.Err()
}

// scopeCompendiums are the Compendiums a project's timeline draws on: its
// primary and the ones it references, primary first. primary names the first
// ("" when the project has none).
func scopeCompendiums(ctx context.Context, q vocab.Querier, projectID string) (ids []string, primary string, err error) {
	rows, err := q.QueryContext(ctx, `SELECT c.id FROM projects p JOIN compendiums c ON c.id = p.compendium_id
			WHERE p.id = ?1 AND c.deleted_at IS NULL AND c.trashed_at IS NULL
		UNION ALL
		SELECT c.id FROM compendium_refs r JOIN compendiums c ON c.id = r.target_compendium_id
			WHERE r.owner_kind = 'project' AND r.owner_id = ?1 AND c.deleted_at IS NULL AND c.trashed_at IS NULL`, projectID)
	if err != nil {
		return nil, "", err
	}
	defer rows.Close()
	var out []string
	seen := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, "", err
		}
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, "", err
	}
	_ = q.QueryRowContext(ctx, `SELECT COALESCE(compendium_id, '') FROM projects WHERE id = ?`, projectID).Scan(&primary)
	if primary != "" && !seen[primary] {
		primary = "" // trashed: not in scope
	}
	return out, primary, nil
}

// readableCompendiums keeps the Compendiums the reader may read. The zero
// Reader is the mirror, which is a projection of everything the project's
// owner wired in and is not per reader. A key pinned to a project reaches the
// project's primary Compendium only.
func readableCompendiums(ctx context.Context, q vocab.Querier, r vocab.Reader, cids []string, primary string) (keep []string, hidden bool) {
	for _, c := range cids {
		if r.UserID == "" {
			keep = append(keep, c)
			continue
		}
		if r.Project != "" && c != primary {
			hidden = true
			continue
		}
		if role, ok := compaccess.Role(ctx, q, r.UserID, c); ok && compaccess.AtLeast(role, compaccess.Reader) {
			keep = append(keep, c)
		} else {
			hidden = true
		}
	}
	return keep, hidden
}

// ---- entities in a Compendium ----------------------------------------------------------------------------------------

// compBriefs reads the live entities of one Compendium by id, with the
// Compendium's own names (no book is in play).
func compBriefs(ctx context.Context, q vocab.Querier, cids []string) (map[string]vocab.Brief, error) {
	out := map[string]vocab.Brief{}
	if len(cids) == 0 {
		return out, nil
	}
	rows, err := q.QueryContext(ctx, `SELECT id, name, kind, color, summary FROM entities WHERE compendium_id IN (`+placeholders(len(cids))+`) AND trashed_at IS NULL`, anys(cids)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var b vocab.Brief
		if err := rows.Scan(&b.ID, &b.Name, &b.Kind, &b.Color, &b.Summary); err != nil {
			return nil, err
		}
		out[b.ID] = b
	}
	return out, rows.Err()
}

// compBriefsOf is compBriefs for the entities with these ids only, read in chunks of 500.
func compBriefsOf(ctx context.Context, q vocab.Querier, cids, ids []string) (map[string]vocab.Brief, error) {
	out := map[string]vocab.Brief{}
	if len(cids) == 0 {
		return out, nil
	}
	for from := 0; from < len(ids); from += 500 {
		chunk := ids[from:min(from+500, len(ids))]
		args := append(anys(chunk), anys(cids)...)
		rows, err := q.QueryContext(ctx, `SELECT id, name, kind, color, summary FROM entities WHERE id IN (`+placeholders(len(chunk))+`)
			AND compendium_id IN (`+placeholders(len(cids))+`) AND trashed_at IS NULL`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var b vocab.Brief
			if err := rows.Scan(&b.ID, &b.Name, &b.Kind, &b.Color, &b.Summary); err != nil {
				rows.Close()
				return nil, err
			}
			out[b.ID] = b
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// findEntity resolves an entity id, or a name or alias, among the live
// entities of one Compendium. Names and aliases are unique within a Compendium
// (the entity ops refuse a clash), so a name has at most one meaning; if the
// data ever says otherwise the answer is an ambiguity error, never a pick.
func findEntity(ctx context.Context, q vocab.Querier, cid, ref string) (id string, ok bool, err error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", false, nil
	}
	err = q.QueryRowContext(ctx, `SELECT id FROM entities WHERE id = ? AND compendium_id = ? AND trashed_at IS NULL`, ref, cid).Scan(&id)
	switch {
	case err == nil:
		return id, true, nil
	case err != sql.ErrNoRows:
		return "", false, err
	}
	k := text.FoldTitle(ref)
	rows, err := q.QueryContext(ctx, `SELECT id, name, kind FROM entities WHERE compendium_id = ?1 AND trashed_at IS NULL AND name_key = ?2
		UNION SELECT e.id, e.name, e.kind FROM entity_aliases a JOIN entities e ON e.id = a.entity_id WHERE e.compendium_id = ?1 AND e.trashed_at IS NULL AND a.alias_key = ?2
		ORDER BY 1`, cid, k)
	if err != nil {
		return "", false, err
	}
	defer rows.Close()
	var cands []vocab.Entry
	for rows.Next() {
		var e vocab.Entry
		if err := rows.Scan(&e.ID, &e.Name, &e.Kind); err != nil {
			return "", false, err
		}
		cands = append(cands, e)
	}
	if err := rows.Err(); err != nil {
		return "", false, err
	}
	switch len(cands) {
	case 0:
		return "", false, nil
	case 1:
		return cands[0].ID, true, nil
	}
	return "", false, vocab.AmbiguousError(ref, cands)
}
