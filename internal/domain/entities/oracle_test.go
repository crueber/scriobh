package entities

import (
	"context"
	"database/sql"
	"strings"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// OracleList is the project list as it was before the database filtered and paged it: every effective entity of the project's scope is read,
// with its body and relationships, then made readable, filtered and paged in Go. The database answer is tested against it.
func OracleList(ctx context.Context, d *sql.DB, p ops.Principal, in ProjectListIn) (ProjectListOut, error) {
	tx, err := d.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ProjectListOut{}, err
	}
	defer tx.Rollback()
	rows, err := loadEffective(ctx, tx, in.ID)
	if err != nil {
		return ProjectListOut{}, err
	}
	all := len(rows)
	rows, roles, err := readable(ctx, tx, p, rows)
	if err != nil {
		return ProjectListOut{}, err
	}
	out := ProjectListOut{Items: []ProjectEntity{}, Hidden: len(rows) < all}
	k := text.FoldTitle(in.Q)
	var match []eff
	for _, r := range rows {
		if in.Kind != "" && r.Kind != in.Kind || in.Compendium != "" && r.CompendiumID != in.Compendium {
			continue
		}
		if k != "" {
			hit := strings.Contains(text.FoldTitle(r.Name), k) || strings.Contains(text.FoldTitle(r.BaseName), k)
			for _, a := range r.Aliases {
				hit = hit || strings.Contains(text.FoldTitle(a), k)
			}
			if !hit {
				continue
			}
		}
		match = append(match, r)
	}
	out.Total = len(match)
	if in.Offset < len(match) {
		end := in.Offset + in.Limit
		if end > len(match) {
			end = len(match)
		}
		for _, r := range match[in.Offset:end] {
			out.Items = append(out.Items, r.wire(roles, false))
		}
		if end < len(match) {
			out.NextOffset = end
		}
	}
	return out, nil
}

// OracleGet is the project's entity lookup as it was before the by-id read: the whole book is read and made readable, then the entity is
// found by id, then by name or alias.
func OracleGet(ctx context.Context, d *sql.DB, p ops.Principal, projectID, ref string) (ProjectItem, error) {
	tx, err := d.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ProjectItem{}, err
	}
	defer tx.Rollback()
	rows, err := loadEffective(ctx, tx, projectID)
	if err != nil {
		return ProjectItem{}, err
	}
	rows, roles, err := readable(ctx, tx, p, rows)
	if err != nil {
		return ProjectItem{}, err
	}
	x, ok := findIn(rows, ref)
	if !ok {
		return ProjectItem{}, ops.NotFound("entity")
	}
	return ProjectItem{Item: x.wire(roles, true)}, nil
}
