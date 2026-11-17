package search

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/text"
)

// BatchSize is how many documents Rebuild reports progress for at a time.
const BatchSize = 500

// Rebuild re-derives doc_text from the documents (title, short synopsis and
// the plain text of the body, as the document ops write them) and rebuilds the
// FTS5 index from it. It is the repair for an index that has drifted or been
// damaged, and the way to pick up a change to how text is extracted.
//
// The work is one transaction, so a failure leaves the old index in place.
// The triggers that follow doc_text into the index are set aside while
// doc_text is rewritten (they would update a damaged index row by row) and put
// back before the commit. progress is called after every BatchSize documents
// and at the end; it may be nil. Run it with the server stopped: it holds the
// single writer for as long as it takes.
func Rebuild(ctx context.Context, d *db.DB, progress func(done, total int)) error {
	return d.WithTx(ctx, func(tx *sql.Tx) error {
		var total int
		if err := tx.QueryRow(`SELECT count(*) FROM documents`).Scan(&total); err != nil {
			return err
		}
		var exists int
		if err := tx.QueryRow(`SELECT count(*) FROM sqlite_master WHERE name = 'doc_fts'`).Scan(&exists); err != nil {
			return err
		}
		if exists == 0 {
			return fmt.Errorf("the doc_fts table is missing; restore it from a backup or re-create it with the 0900 migration, then run this again")
		}
		// Set the triggers aside.
		rows, err := tx.QueryContext(ctx, `SELECT name, sql FROM sqlite_master WHERE type = 'trigger' AND tbl_name = 'doc_text'`)
		if err != nil {
			return err
		}
		var names, ddl []string
		for rows.Next() {
			var n, s string
			if err := rows.Scan(&n, &s); err != nil {
				rows.Close()
				return err
			}
			names, ddl = append(names, n), append(ddl, s)
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return err
		}
		rows.Close()
		for _, n := range names {
			if _, err := tx.Exec(`DROP TRIGGER ` + quoteIdent(n)); err != nil {
				return err
			}
		}
		// Rewrite doc_text from the documents, in batches so progress is real.
		if _, err := tx.Exec(`DELETE FROM doc_text`); err != nil {
			return err
		}
		done, last := 0, ""
		for {
			batch, err := tx.QueryContext(ctx, `SELECT id, project_id, title, synopsis_short, content FROM documents WHERE id > ? ORDER BY id LIMIT ?`, last, BatchSize)
			if err != nil {
				return err
			}
			type row struct{ id, project, title, syn, content string }
			var got []row
			for batch.Next() {
				var r row
				if err := batch.Scan(&r.id, &r.project, &r.title, &r.syn, &r.content); err != nil {
					batch.Close()
					return err
				}
				got = append(got, r)
			}
			if err := batch.Err(); err != nil {
				batch.Close()
				return err
			}
			batch.Close()
			if len(got) == 0 {
				break
			}
			for _, r := range got {
				if _, err := tx.Exec(`INSERT INTO doc_text(document_id, project_id, title, synopsis, body) VALUES (?,?,?,?,?)`,
					r.id, r.project, r.title, r.syn, text.SearchBody(r.content)); err != nil {
					return err
				}
				last = r.id
			}
			done += len(got)
			if progress != nil {
				progress(done, total)
			}
		}
		if _, err := tx.Exec(`INSERT INTO doc_fts(doc_fts) VALUES('rebuild')`); err != nil {
			return err
		}
		for _, s := range ddl {
			if _, err := tx.Exec(s); err != nil {
				return err
			}
		}
		if progress != nil && done == 0 {
			progress(0, total)
		}
		if err := rebuildEntities(ctx, tx); err != nil {
			return err
		}
		// The mention index reads doc_text, which was just rewritten: every
		// project owes a scan against it.
		if _, err := tx.Exec(`INSERT INTO mention_state(project_id, stamp) SELECT id, lower(hex(randomblob(8))) FROM projects WHERE deleted_at IS NULL
			ON CONFLICT(project_id) DO UPDATE SET stamp = excluded.stamp`); err != nil {
			return err
		}
		if err := rebuildResearch(ctx, tx); err != nil {
			return err
		}
		return rebuildStash(ctx, tx)
	})
}

// rebuildEntities re-derives entity_text (name, aliases, plain summary and
// body) from the entities and rebuilds entity_fts, the same way Rebuild does
// for documents: the triggers are set aside while the rows are rewritten and
// put back before the commit.
func rebuildEntities(ctx context.Context, tx *sql.Tx) error {
	rows, err := tx.QueryContext(ctx, `SELECT name, sql FROM sqlite_master WHERE type = 'trigger' AND tbl_name = 'entity_text'`)
	if err != nil {
		return err
	}
	var names, ddl []string
	for rows.Next() {
		var n, s string
		if err := rows.Scan(&n, &s); err != nil {
			rows.Close()
			return err
		}
		names, ddl = append(names, n), append(ddl, s)
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, n := range names {
		if _, err := tx.Exec(`DROP TRIGGER ` + quoteIdent(n)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`DELETE FROM entity_text`); err != nil {
		return err
	}
	erows, err := tx.QueryContext(ctx, `SELECT e.id, e.compendium_id, e.name, e.summary, e.body,
			COALESCE((SELECT group_concat(a.alias, char(10)) FROM entity_aliases a WHERE a.entity_id = e.id), '')
		FROM entities e WHERE e.compendium_id IS NOT NULL ORDER BY e.id`)
	if err != nil {
		return err
	}
	type row struct{ id, cid, name, summary, body, aliases string }
	var got []row
	for erows.Next() {
		var r row
		if err := erows.Scan(&r.id, &r.cid, &r.name, &r.summary, &r.body, &r.aliases); err != nil {
			erows.Close()
			return err
		}
		got = append(got, r)
	}
	if err := erows.Close(); err != nil {
		return err
	}
	for _, r := range got {
		if _, err := tx.Exec(`INSERT INTO entity_text(entity_id, compendium_id, name, aliases, summary, body) VALUES (?,?,?,?,?,?)`,
			r.id, r.cid, r.name, r.aliases, text.Plain(r.summary), text.Plain(r.body)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`INSERT INTO entity_fts(entity_fts) VALUES('rebuild')`); err != nil {
		return err
	}
	for _, s := range ddl {
		if _, err := tx.Exec(s); err != nil {
			return err
		}
	}
	return nil
}

func quoteIdent(s string) string { return `"` + strings.ReplaceAll(s, `"`, `""`) + `"` }
