package db

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// DescribeSchema describes a database's structure in a form that is the same
// however the database got there, so a fresh install and an upgraded database
// can be compared: each table as its sorted column descriptions (an ALTER TABLE
// ADD COLUMN appends where a fresh CREATE TABLE puts it in the middle, so the
// stored SQL text legitimately differs) plus its foreign keys, and indexes,
// triggers and views by name with their normalised SQL. Full-text tables and
// their shadow tables are named only.
func DescribeSchema(ctx context.Context, d *DB) (map[string]string, error) {
	out := map[string]string{}
	rows, err := d.Read().QueryContext(ctx, `SELECT type, name, tbl_name, COALESCE(sql,'') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		return nil, err
	}
	type obj struct{ typ, name, tbl, sql string }
	var objs []obj
	for rows.Next() {
		var o obj
		if err := rows.Scan(&o.typ, &o.name, &o.tbl, &o.sql); err != nil {
			rows.Close()
			return nil, err
		}
		objs = append(objs, o)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	lines := func(q string, n int) ([]string, error) {
		r, err := d.Read().QueryContext(ctx, q)
		if err != nil {
			return nil, err
		}
		defer r.Close()
		var parts []string
		for r.Next() {
			vals := make([]any, n)
			ptrs := make([]any, n)
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := r.Scan(ptrs...); err != nil {
				return nil, err
			}
			var sb strings.Builder
			for i, v := range vals {
				if i > 0 {
					sb.WriteByte(' ')
				}
				fmt.Fprintf(&sb, "%v", v)
			}
			parts = append(parts, sb.String())
		}
		sort.Strings(parts)
		return parts, r.Err()
	}
	for _, o := range objs {
		key := o.typ + " " + o.name
		switch o.typ {
		case "table":
			if strings.HasPrefix(o.name, "doc_fts") || strings.HasSuffix(o.name, "_fts") || strings.Contains(o.name, "_fts_") {
				out[key] = "(virtual table or shadow: " + o.name + ")"
				continue
			}
			cols, err := lines(fmt.Sprintf(`SELECT name, type, "notnull", 'dflt='||COALESCE(dflt_value,''), 'pk='||pk FROM pragma_table_info('%s')`, o.name), 5)
			if err != nil {
				return nil, err
			}
			fks, err := lines(fmt.Sprintf(`SELECT 'fk', "from", "table", COALESCE("to",''), on_update, on_delete FROM pragma_foreign_key_list('%s')`, o.name), 6)
			if err != nil {
				return nil, err
			}
			// UNIQUE and PRIMARY KEY constraints, by their columns: the automatic
			// index names they get depend on the order the table was built in.
			uq, err := lines(fmt.Sprintf(`SELECT 'constraint', il.origin, il."unique", (SELECT group_concat(name, ',') FROM (SELECT name FROM pragma_index_info(il.name) ORDER BY seqno))
				FROM pragma_index_list('%s') il WHERE il.origin <> 'c'`, o.name), 4)
			if err != nil {
				return nil, err
			}
			out[key] = strings.Join(append(append(cols, fks...), uq...), "; ")
		default:
			out[key] = strings.Join(strings.Fields(o.sql), " ")
		}
	}
	return out, nil
}
