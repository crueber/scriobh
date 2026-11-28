package entities

import (
	"context"
	"database/sql"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// Sheet is an entity as the AI context builder sees it: the effective record of
// the project (the book's override applied), with the bible's body. It carries
// the never-send flag so the builder can refuse an excluded entity even when a
// caller names it, and still use its name to hide it from the text.
type Sheet struct {
	ID           string
	Kind         string
	Name         string
	Summary      string
	Body         string
	Fields       map[string]string
	Aliases      []string
	CompendiumID string
	AIExclude    bool
}

// ReadableSheets returns the entities of a project's scope that the caller may
// read: the same rule as every other read (membership of the Compendium; a key
// pinned to a project sees the primary Compendium only), applied by the same
// code. Entities of a Compendium the caller is not a member of are absent, not
// hidden. Relationships and the book's private override notes are not included.
func ReadableSheets(ctx context.Context, d *db.DB, p ops.Principal, projectID string) ([]Sheet, error) {
	tx, err := d.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := loadEffective(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	rows, _, err = readable(ctx, tx, p, rows)
	if err != nil {
		return nil, err
	}
	excluded := map[string]bool{}
	xr, err := tx.QueryContext(ctx, `SELECT e.id FROM entities e WHERE e.ai_exclude = 1 AND `+scopePredicate("e"), projectID)
	if err != nil {
		return nil, err
	}
	for xr.Next() {
		var id string
		if err := xr.Scan(&id); err != nil {
			xr.Close()
			return nil, err
		}
		excluded[id] = true
	}
	if err := xr.Err(); err != nil {
		_ = xr.Close()
		return nil, err
	}
	if err := xr.Close(); err != nil {
		return nil, err
	}
	out := make([]Sheet, 0, len(rows))
	for _, r := range rows {
		out = append(out, Sheet{ID: r.ID, Kind: r.Kind, Name: r.Name, Summary: r.Summary, Body: r.Body, Fields: nonEmpty(r.Fields),
			Aliases: r.Aliases, CompendiumID: r.CompendiumID, AIExclude: excluded[r.ID]})
	}
	return out, nil
}

// ScopeCompendium names the Compendium of an entity in a project's scope that
// the caller can read, or a not-found error (the same one whether the entity
// does not exist, is out of scope or is not the caller's to see).
func ScopeCompendium(ctx context.Context, d *db.DB, p ops.Principal, projectID, entityID string) (string, error) {
	return inScopeEntity(ctx, d.Read(), p, projectID, entityID)
}

// Name is one name or alias of an entity in a project's scope, with the entity
// it belongs to.
type Name struct {
	EntityID string
	Kind     string
	Surface  string
	Primary  bool
}

// ScopeNames returns every name and alias of every entity in a project's scope,
// whoever is asking and whether or not they may read the entity. It exists so
// that redaction can hide a name from the provider even when the caller has no
// access to the bible that holds it. The result is for hiding text only: it
// must never be returned to a caller or shown, except for names that already
// appear in text the caller is sending.
func ScopeNames(ctx context.Context, d *db.DB, projectID string) ([]Name, error) {
	tx, err := d.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := loadEffective(ctx, tx, projectID)
	if err != nil {
		return nil, err
	}
	var out []Name
	for _, r := range rows {
		out = append(out, Name{EntityID: r.ID, Kind: r.Kind, Surface: r.Name, Primary: true})
		if r.BaseName != r.Name { // the Compendium's own name too, when the book renamed the entity
			out = append(out, Name{EntityID: r.ID, Kind: r.Kind, Surface: r.BaseName})
		}
		for _, a := range r.Aliases {
			out = append(out, Name{EntityID: r.ID, Kind: r.Kind, Surface: a})
		}
	}
	return out, nil
}
