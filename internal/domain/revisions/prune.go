package revisions

import (
	"context"
	"database/sql"
	"strconv"
	"time"

	"github.com/crueber/scriobh/internal/db"
)

// Policy says how long history is kept.
//
//   - A named snapshot (reason manual) and any version someone labelled are kept
//     forever. These are the pinned versions.
//   - The newest revision of every document is always kept, so the autosave
//     throttle and "is the head recorded" keep reading a real row.
//   - Every other revision (autosave, restore, snapshot, import, and the rest,
//     alternative) is kept in full for AutosaveDays, then thinned to the newest
//     one of each UTC day when KeepDaily is set, or dropped when it is not.
type Policy struct {
	AutosaveDays int  `json:"autosave_days"`
	KeepDaily    bool `json:"keep_daily"`
}

// DefaultPolicy is DATA-MODEL's rule: 30 days in full, then one per day.
func DefaultPolicy() Policy { return Policy{AutosaveDays: 30, KeepDaily: true} }

// Settings keys (the settings table, edited by an administrator).
const (
	SettingAutosaveDays = "revisions.autosave_days"
	SettingKeepDaily    = "revisions.keep_daily"
)

// LoadPolicy reads the policy from settings, falling back to the default for
// anything unset or unreadable.
func LoadPolicy(ctx context.Context, d *db.DB) Policy {
	p := DefaultPolicy()
	get := func(key string) (string, bool) {
		var v string
		err := d.Read().QueryRowContext(ctx, `SELECT value FROM settings WHERE key = ?`, key).Scan(&v)
		return v, err == nil
	}
	if v, ok := get(SettingAutosaveDays); ok {
		if n, err := strconv.Atoi(v); err == nil && n >= 1 && n <= 36500 {
			p.AutosaveDays = n
		}
	}
	if v, ok := get(SettingKeepDaily); ok {
		if b, err := strconv.ParseBool(v); err == nil {
			p.KeepDaily = b
		}
	}
	return p
}

// PruneResult reports what a prune did (or, with DryRun, would do).
type PruneResult struct {
	Documents int  `json:"documents" desc:"Documents that had revisions to remove."`
	Removed   int  `json:"removed" desc:"Revisions removed."`
	Blobs     int  `json:"blobs" desc:"Stored texts no revision refers to any more, freed."`
	Bytes     int  `json:"bytes" desc:"Stored bytes freed."`
	DryRun    bool `json:"dry_run"`
}

// Scope limits a prune. Both empty means every document.
type Scope struct {
	ProjectID  string
	DocumentID string
}

// Prune applies the policy as of now. It works one document per short
// transaction, so a sweep never holds the writer for long.
func Prune(ctx context.Context, d *db.DB, p Policy, now time.Time, scope Scope, dry bool) (PruneResult, error) {
	res := PruneResult{DryRun: dry}
	cutoff := db.Format(now.AddDate(0, 0, -p.AutosaveDays))
	q := `SELECT DISTINCT r.document_id FROM document_revisions r`
	var args []any
	where := ` WHERE r.created_at < ? AND r.reason != 'manual' AND r.label = ''`
	args = append(args, cutoff)
	switch {
	case scope.DocumentID != "":
		where += ` AND r.document_id = ?`
		args = append(args, scope.DocumentID)
	case scope.ProjectID != "":
		q += ` JOIN documents d ON d.id = r.document_id`
		where += ` AND d.project_id = ?`
		args = append(args, scope.ProjectID)
	}
	rows, err := d.Read().QueryContext(ctx, q+where, args...)
	if err != nil {
		return res, err
	}
	var docs []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return res, err
		}
		docs = append(docs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	for _, id := range docs {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		var r PruneResult
		err := d.WithTx(ctx, func(tx *sql.Tx) error {
			var err error
			r, err = pruneDoc(tx, id, p, cutoff, dry)
			return err
		})
		if err != nil {
			return res, err
		}
		if r.Removed > 0 {
			res.Documents++
		}
		res.Removed += r.Removed
		res.Blobs += r.Blobs
		res.Bytes += r.Bytes
	}
	return res, nil
}

// Sweep prunes every document with the stored policy; the server calls it on
// its housekeeping tick.
func Sweep(ctx context.Context, d *db.DB) (PruneResult, error) {
	return Prune(ctx, d, LoadPolicy(ctx, d), db.NowTime(), Scope{}, false)
}

func pruneDoc(tx *sql.Tx, docID string, p Policy, cutoff string, dry bool) (PruneResult, error) {
	res := PruneResult{DryRun: dry}
	rows, err := tx.Query(`SELECT id, created_at, reason, label FROM document_revisions WHERE document_id = ? ORDER BY created_at DESC, id DESC`, docID)
	if err != nil {
		return res, err
	}
	type row struct{ id, at, reason, label string }
	var all []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.id, &r.at, &r.reason, &r.label); err != nil {
			rows.Close()
			return res, err
		}
		all = append(all, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return res, err
	}
	// The newest is the one most recently written, the same row the autosave
	// throttle reads (store.latest), not the one with the latest shown time.
	var newest string
	if err := tx.QueryRow(`SELECT id FROM document_revisions WHERE document_id = ? ORDER BY recorded_at DESC, id DESC LIMIT 1`, docID).Scan(&newest); err != nil {
		return res, err
	}
	var drop []string
	seenDay := map[string]bool{}
	for _, r := range all { // newest shown first
		switch {
		case r.id == newest:
			seenDay[r.at[:10]] = true // kept, so its day already has a survivor
			continue
		case pinned(r.reason, r.label), r.at >= cutoff:
			continue
		case p.KeepDaily && !seenDay[r.at[:10]]:
			seenDay[r.at[:10]] = true // the newest of its day: the day's last word
			continue
		}
		drop = append(drop, r.id)
	}
	if len(drop) == 0 {
		return res, nil
	}
	res.Removed = len(drop)
	// Freed blobs: stored texts only the dropped revisions refer to.
	dropped := map[string]bool{}
	for _, id := range drop {
		dropped[id] = true
	}
	kept := map[string]bool{}
	hashes := map[string]string{}
	hrows, err := tx.Query(`SELECT id, content_hash FROM document_revisions WHERE document_id = ?`, docID)
	if err != nil {
		return res, err
	}
	for hrows.Next() {
		var id, h string
		if err := hrows.Scan(&id, &h); err != nil {
			hrows.Close()
			return res, err
		}
		hashes[id] = h
		if !dropped[id] {
			kept[h] = true
		}
	}
	hrows.Close()
	if err := hrows.Err(); err != nil {
		return res, err
	}
	freed := map[string]bool{}
	for _, id := range drop {
		if h := hashes[id]; !kept[h] {
			freed[h] = true
		}
	}
	for h := range freed {
		var size int
		if err := tx.QueryRow(`SELECT length(content) FROM revision_blobs WHERE document_id = ? AND content_hash = ?`, docID, h).Scan(&size); err == nil {
			res.Bytes += size
			res.Blobs++
		}
	}
	if dry {
		return res, nil
	}
	for _, id := range drop {
		if _, err := tx.Exec(`DELETE FROM document_revisions WHERE id = ?`, id); err != nil {
			return res, err
		}
	}
	for h := range freed {
		if _, err := tx.Exec(`DELETE FROM revision_blobs WHERE document_id = ? AND content_hash = ?`, docID, h); err != nil {
			return res, err
		}
	}
	return res, nil
}
