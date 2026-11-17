package entities

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"sort"
	"strings"
)

// Fingerprints summarise, from metadata only (no entity text is read), the
// stored state a mirror folder depends on. The mirror compares one with the
// value it recorded when it last wrote, so a Compendium that changed while the
// server was down, or a layout change, is found and rewritten at the next
// start, and an unchanged one costs a handful of queries and no writes.
//
// Entity rows are covered by (id, updated_at): every op that changes what a
// file shows (the sheet, an alias, a relationship, a merge) moves updated_at.

type compendiumHash struct {
	entities string // what a project's projection depends on: name, state, entity rows
	full     string // what a standalone mirror depends on: also settings, fields, references
}

func newHash(tag string) hash.Hash {
	h := sha256.New()
	fmt.Fprintf(h, "%s\n", tag)
	return h
}

func sum(h hash.Hash) string { return hex.EncodeToString(h.Sum(nil)) }

// inList is " AND col IN (?,?,…)" and its arguments for a list of ids, or
// nothing at all for a nil list (every row).
func inList(col string, ids []string) (string, []any) {
	if ids == nil {
		return "", nil
	}
	if len(ids) == 0 {
		return " AND 1 = 0", nil
	}
	args := make([]any, len(ids))
	ph := make([]string, len(ids))
	for i, id := range ids {
		args[i], ph[i] = id, "?"
	}
	return " AND " + col + " IN (" + strings.Join(ph, ",") + ")", args
}

// loadCompendiumHashes hashes the given Compendiums (nil: all of them).
func loadCompendiumHashes(ctx context.Context, q Querier, only []string) (map[string]compendiumHash, error) {
	type row struct {
		name, slug, kind, desc, at string
		trashed, archived          bool
		ent, full                  hash.Hash
	}
	rows := map[string]*row{}
	var order []string
	where, args := inList("id", only)
	crows, err := q.QueryContext(ctx, `SELECT id, name, slug, kind, description, updated_at, trashed_at IS NOT NULL, archived_at IS NOT NULL
		FROM compendiums WHERE deleted_at IS NULL`+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	for crows.Next() {
		var id string
		r := &row{}
		if err := crows.Scan(&id, &r.name, &r.slug, &r.kind, &r.desc, &r.at, &r.trashed, &r.archived); err != nil {
			crows.Close()
			return nil, err
		}
		r.ent = newHash("compendium-entities 1")
		r.full = newHash("compendium 1")
		fmt.Fprintf(r.ent, "c\x00%q\x00%v\n", r.name, r.trashed)
		fmt.Fprintf(r.full, "c\x00%q\x00%q\x00%q\x00%q\x00%v\x00%v\n", r.name, r.slug, r.kind, r.desc, r.trashed, r.archived)
		rows[id] = r
		order = append(order, id)
	}
	if err := crows.Close(); err != nil {
		return nil, err
	}
	ewhere, _ := inList("compendium_id", only)
	erows, err := q.QueryContext(ctx, `SELECT compendium_id, id, updated_at FROM entities WHERE compendium_id IS NOT NULL AND trashed_at IS NULL`+ewhere+` ORDER BY compendium_id, id`, args...)
	if err != nil {
		return nil, err
	}
	for erows.Next() {
		var cid, id, at string
		if err := erows.Scan(&cid, &id, &at); err != nil {
			erows.Close()
			return nil, err
		}
		if r := rows[cid]; r != nil {
			fmt.Fprintf(r.ent, "e\x00%s\x00%s\n", id, at)
			fmt.Fprintf(r.full, "e\x00%s\x00%s\n", id, at)
		}
	}
	if err := erows.Close(); err != nil {
		return nil, err
	}
	drows, err := q.QueryContext(ctx, `SELECT compendium_id, kind, key, label, type, choices_json FROM entity_field_defs WHERE 1 = 1`+ewhere+` ORDER BY compendium_id, kind, position, key`, args...)
	if err != nil {
		return nil, err
	}
	for drows.Next() {
		var cid, kind, key, label, typ, ch string
		if err := drows.Scan(&cid, &kind, &key, &label, &typ, &ch); err != nil {
			drows.Close()
			return nil, err
		}
		if r := rows[cid]; r != nil {
			fmt.Fprintf(r.full, "f\x00%s\x00%s\x00%q\x00%s\x00%s\n", kind, key, label, typ, ch)
		}
	}
	if err := drows.Close(); err != nil {
		return nil, err
	}
	rrows, err := q.QueryContext(ctx, `SELECT r.owner_id, r.target_compendium_id, t.name FROM compendium_refs r JOIN compendiums t ON t.id = r.target_compendium_id
		WHERE r.owner_kind = 'compendium' ORDER BY r.owner_id, t.name, t.id`)
	if err != nil {
		return nil, err
	}
	for rrows.Next() {
		var owner, target, name string
		if err := rrows.Scan(&owner, &target, &name); err != nil {
			rrows.Close()
			return nil, err
		}
		if r := rows[owner]; r != nil {
			fmt.Fprintf(r.full, "r\x00%s\x00%q\n", target, name)
		}
	}
	if err := rrows.Close(); err != nil {
		return nil, err
	}
	var stamps map[string]string
	if h := timeline(); h != nil && h.CompendiumStamps != nil {
		var err error
		if stamps, err = h.CompendiumStamps(ctx, q, only); err != nil {
			return nil, err
		}
	}
	out := make(map[string]compendiumHash, len(rows))
	for _, id := range order {
		r := rows[id]
		// A Compendium with no events hashes as it did before the timeline existed.
		if st := stamps[id]; st != "" {
			fmt.Fprintf(r.ent, "timeline\x00%s\n", st)
			fmt.Fprintf(r.full, "timeline\x00%s\n", st)
		}
		out[id] = compendiumHash{entities: sum(r.ent), full: sum(r.full)}
	}
	return out, nil
}

// ProjectFingerprints returns, for each project that has any Compendium
// material in scope (a primary, a reference or a link), a hash of what its
// compendium/ projection depends on. A project with none is absent, so the
// mirror's own fingerprint for it is unchanged by this feature. only limits it
// to one project; an empty string means all.
func ProjectFingerprints(ctx context.Context, q Querier, only string) (map[string]string, error) {
	var relevant []string // nil: every Compendium; for one project, only those it draws on
	if only != "" {
		relevant = []string{}
		rrows, err := q.QueryContext(ctx, `SELECT compendium_id FROM projects WHERE id = ?1 AND compendium_id IS NOT NULL
			UNION SELECT target_compendium_id FROM compendium_refs WHERE owner_kind = 'project' AND owner_id = ?1
			UNION SELECT e.compendium_id FROM project_entity_links l JOIN entities e ON e.id = l.entity_id WHERE l.project_id = ?1 AND e.compendium_id IS NOT NULL`, only)
		if err != nil {
			return nil, err
		}
		for rrows.Next() {
			var id string
			if err := rrows.Scan(&id); err != nil {
				rrows.Close()
				return nil, err
			}
			relevant = append(relevant, id)
		}
		if err := rrows.Close(); err != nil {
			return nil, err
		}
	}
	hs, err := loadCompendiumHashes(ctx, q, relevant)
	if err != nil {
		return nil, err
	}
	type proj struct {
		h       hash.Hash
		primary string
		refs    []string
	}
	ps := map[string]*proj{}
	var order []string
	where, args := "", []any(nil)
	if only != "" {
		where, args = " AND id = ?", []any{only}
	}
	prow, err := q.QueryContext(ctx, `SELECT id, COALESCE(compendium_id, '') FROM projects WHERE deleted_at IS NULL`+where+` ORDER BY id`, args...)
	if err != nil {
		return nil, err
	}
	for prow.Next() {
		var id string
		p := &proj{h: newHash("projection 1")}
		if err := prow.Scan(&id, &p.primary); err != nil {
			prow.Close()
			return nil, err
		}
		ps[id] = p
		order = append(order, id)
	}
	if err := prow.Close(); err != nil {
		return nil, err
	}
	scoped := func(col string) string {
		if only == "" {
			return ""
		}
		return " AND " + col + " = ?"
	}
	var oargs []any
	if only != "" {
		oargs = []any{only}
	}
	refRows, err := q.QueryContext(ctx, `SELECT owner_id, target_compendium_id, created_at FROM compendium_refs WHERE owner_kind = 'project'`+scoped("owner_id")+` ORDER BY owner_id, created_at, target_compendium_id`, oargs...)
	if err != nil {
		return nil, err
	}
	for refRows.Next() {
		var owner, target, at string
		if err := refRows.Scan(&owner, &target, &at); err != nil {
			refRows.Close()
			return nil, err
		}
		if p := ps[owner]; p != nil {
			p.refs = append(p.refs, target)
		}
	}
	if err := refRows.Close(); err != nil {
		return nil, err
	}
	used := map[string]bool{}
	for _, id := range order {
		p := ps[id]
		if p.primary != "" {
			used[id] = true
			fmt.Fprintf(p.h, "primary\x00%s\x00%s\n", p.primary, hs[p.primary].entities)
		}
		for _, t := range p.refs {
			used[id] = true
			fmt.Fprintf(p.h, "ref\x00%s\x00%s\n", t, hs[t].entities)
		}
	}
	lrows, err := q.QueryContext(ctx, `SELECT l.project_id, e.id, e.updated_at, e.trashed_at IS NOT NULL, c.trashed_at IS NOT NULL, c.name
		FROM project_entity_links l JOIN entities e ON e.id = l.entity_id JOIN compendiums c ON c.id = e.compendium_id
		WHERE 1 = 1`+scoped("l.project_id")+` ORDER BY l.project_id, e.id`, oargs...)
	if err != nil {
		return nil, err
	}
	for lrows.Next() {
		var pid, eid, at, cname string
		var et, ct bool
		if err := lrows.Scan(&pid, &eid, &at, &et, &ct, &cname); err != nil {
			lrows.Close()
			return nil, err
		}
		if p := ps[pid]; p != nil {
			used[pid] = true
			fmt.Fprintf(p.h, "link\x00%s\x00%s\x00%v\x00%v\x00%q\n", eid, at, et, ct, cname)
		}
	}
	if err := lrows.Close(); err != nil {
		return nil, err
	}
	orows, err := q.QueryContext(ctx, `SELECT project_id, entity_id, updated_at FROM project_entity_overrides WHERE 1 = 1`+scoped("project_id")+` ORDER BY project_id, entity_id`, oargs...)
	if err != nil {
		return nil, err
	}
	for orows.Next() {
		var pid, eid, at string
		if err := orows.Scan(&pid, &eid, &at); err != nil {
			orows.Close()
			return nil, err
		}
		if p := ps[pid]; p != nil && used[pid] {
			fmt.Fprintf(p.h, "override\x00%s\x00%s\n", eid, at)
		}
	}
	if err := orows.Close(); err != nil {
		return nil, err
	}
	var pstamps map[string]string
	// A project with no Compendium in scope has no compendium/ folder, so no timeline file: do not even ask.
	if h := timeline(); h != nil && h.ProjectStamps != nil && (only == "" || used[only]) {
		var err error
		if pstamps, err = h.ProjectStamps(ctx, q, only); err != nil {
			return nil, err
		}
	}
	out := map[string]string{}
	for id := range used {
		if st := pstamps[id]; st != "" {
			fmt.Fprintf(ps[id].h, "timeline\x00%s\n", st)
		}
		out[id] = sum(ps[id].h)
	}
	return out, nil
}

// CompendiumFingerprints returns a hash for each Compendium that has a
// standalone mirror: one no project has as its primary. only limits it to one.
func CompendiumFingerprints(ctx context.Context, q Querier, only string) (map[string]string, error) {
	var ids []string
	if only != "" {
		ids = []string{only}
	}
	hs, err := loadCompendiumHashes(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	rows, err := q.QueryContext(ctx, `SELECT DISTINCT compendium_id FROM projects WHERE compendium_id IS NOT NULL AND deleted_at IS NULL`)
	if err != nil {
		return nil, err
	}
	taken := map[string]bool{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		taken[id] = true
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	out := map[string]string{}
	keys := make([]string, 0, len(hs))
	for id := range hs {
		keys = append(keys, id)
	}
	sort.Strings(keys)
	for _, id := range keys {
		if !taken[id] {
			out[id] = hs[id].full
		}
	}
	return out, nil
}
