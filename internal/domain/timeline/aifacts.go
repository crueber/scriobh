package timeline

import (
	"context"
	"database/sql"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/vocab"
	"github.com/crueber/scriobh/internal/ops"
)

// FactEvent is an event as the continuity audit reads it: what the caller may
// read of it, by name, with the id a finding cites.
type FactEvent struct {
	ID       string
	Title    string
	Date     string
	End      string
	Location string   // the place's name, "" for none
	People   []string // names of the participants
}

// FactFinding is a finding of the deterministic check: the same data the
// timeline's own check shows.
type FactFinding struct {
	ID       string
	Kind     string
	Severity string
	Message  string
}

// Facts is what the AI context builder takes from the timeline for one caller.
type Facts struct {
	Events   []FactEvent
	Findings []FactFinding
	// Hidden: a Compendium the project draws on is not readable by the caller,
	// so its events are left out.
	Hidden    bool
	Truncated bool // more events than maxEvents
	// ExcludedEvents and ExcludedFindings count what was left out because it names
	// something flagged never-send (an entity, or a chapter): the provider must not
	// learn a never-send place, person or chapter title from the timeline either.
	ExcludedEvents, ExcludedFindings int
}

// AIFacts reads the events (in story order) and the findings of a project's
// timeline as the caller may read them, for the continuity audit's context.
// It reads; it changes nothing and applies no rule of its own.
func AIFacts(ctx context.Context, d *db.DB, p ops.Principal, projectID string, maxEvents int) (Facts, error) {
	tx, err := d.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return Facts{}, err
	}
	defer tx.Rollback()
	pd, err := loadProject(ctx, tx, vocab.ReaderOf(p), projectID)
	if err != nil {
		return Facts{}, err
	}
	out := Facts{Hidden: pd.Hidden}
	exEnt, exDoc, err := neverSend(ctx, tx, projectID)
	if err != nil {
		return Facts{}, err
	}
	exEvent := map[string]bool{} // events that name a never-send entity: all of them, not only the ones listed
	for _, e := range pd.Events {
		hit := exEnt[e.Loc]
		for _, pe := range e.People {
			hit = hit || exEnt[pe.Entity]
		}
		if hit {
			exEvent[e.ID] = true
		}
	}
	for _, e := range pd.Events {
		if e.Trashed {
			continue
		}
		if exEvent[e.ID] {
			out.ExcludedEvents++
			continue
		}
		if maxEvents > 0 && len(out.Events) >= maxEvents {
			out.Truncated = true
			break
		}
		fe := FactEvent{ID: e.ID, Title: e.Title, Date: e.Date, End: e.End}
		if b, ok := pd.Names[e.Loc]; ok {
			fe.Location = b.Name
		}
		for _, pe := range e.People {
			if b, ok := pd.Names[pe.Entity]; ok {
				fe.People = append(fe.People, b.Name)
			}
		}
		out.Events = append(out.Events, fe)
	}
	co, err := check(ctx, tx, pd)
	if err != nil {
		return Facts{}, err
	}
	for _, f := range co.Findings {
		hit := false
		for _, en := range f.Entities {
			hit = hit || exEnt[en.ID]
		}
		for _, ev := range f.Events {
			hit = hit || exEvent[ev.ID]
		}
		for _, sc := range f.Scenes {
			hit = hit || exDoc[sc.ID]
		}
		if hit {
			out.ExcludedFindings++
			continue
		}
		out.Findings = append(out.Findings, FactFinding{ID: f.ID, Kind: f.Kind, Severity: f.Severity, Message: f.Message})
	}
	return out, nil
}

// neverSend reads the ids of the entities and of the project's documents that are
// flagged never-send.
func neverSend(ctx context.Context, tx *sql.Tx, projectID string) (ents, docs map[string]bool, err error) {
	ents, docs = map[string]bool{}, map[string]bool{}
	for _, q := range []struct {
		sql  string
		args []any
		to   map[string]bool
	}{
		{`SELECT id FROM entities WHERE ai_exclude = 1`, nil, ents},
		{`SELECT id FROM documents WHERE project_id = ? AND ai_exclude = 1`, []any{projectID}, docs},
	} {
		rows, err := tx.QueryContext(ctx, q.sql, q.args...)
		if err != nil {
			return nil, nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, nil, err
			}
			q.to[id] = true
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, nil, err
		}
		if err := rows.Close(); err != nil {
			return nil, nil, err
		}
	}
	return ents, docs, nil
}
