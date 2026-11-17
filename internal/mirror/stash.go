package mirror

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/domain/entities"
)

// The stash: a project's overflow, written as
//
//	stash/<title-slug>--<id6>.md     one file per live item, in the order of the stash
//
// beside the manuscript and the research. It is a copy for the person who reads
// the folder (and the backup of the folder); it is not part of a Git push, which
// carries exactly the documents the manifest lists, and it is not part of any
// export. A trashed item is not written. The front matter is hand-written in a
// fixed key order, every string JSON-quoted, so an unchanged item is the same
// bytes.

const stashDir = "stash"

type stashRow struct {
	ID, Kind, Title, Content, Source string
	Tags                             []string
	Position                         float64
}

func (s stashRow) markdown() []byte {
	var b bytes.Buffer
	b.WriteString("---\nid: " + qs(s.ID) + "\nkind: " + s.Kind + "\ntitle: " + qs(s.Title) + "\n")
	if len(s.Tags) > 0 {
		b.WriteString("tags:\n")
		for _, t := range s.Tags {
			b.WriteString("  - " + qs(t) + "\n")
		}
	}
	if s.Source != "" {
		b.WriteString("source_document: " + qs(s.Source) + "\n")
	}
	b.WriteString("---\n")
	if body := strings.TrimRight(strings.ReplaceAll(s.Content, "\r\n", "\n"), "\n"); body != "" {
		b.WriteString("\n" + body + "\n")
	}
	return b.Bytes()
}

// loadStash reads a project's live stash items in order.
func loadStash(ctx context.Context, q Querier, projectID string) ([]stashRow, error) {
	rows, err := q.QueryContext(ctx, `SELECT id, kind, title, content, tags, COALESCE(source_document_id,''), position
		FROM stash_items WHERE project_id = ? AND deleted_at IS NULL ORDER BY position, id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []stashRow
	for rows.Next() {
		var s stashRow
		var tags string
		if err := rows.Scan(&s.ID, &s.Kind, &s.Title, &s.Content, &tags, &s.Source, &s.Position); err != nil {
			return nil, err
		}
		if tags != "" {
			_ = json.Unmarshal([]byte(tags), &s.Tags)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// layoutStash names the files. Two titles that slug alike get a longer id suffix.
func layoutStash(items []stashRow) map[string][]byte {
	out := map[string][]byte{}
	used := map[string]bool{}
	sorted := append([]stashRow(nil), items...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].ID < sorted[j].ID })
	for _, s := range sorted {
		slug := entities.FileSlug(s.Title)
		if s.Title == "" {
			slug = entities.FileSlug(firstWords(s.Content))
		}
		full := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(s.ID))
		name := ""
		for _, n := range []int{6, 12, len(full)} {
			if n > len(full) {
				n = len(full)
			}
			p := stashDir + "/" + slug + "--" + full[len(full)-n:] + ".md"
			if !used[p] {
				name = p
				break
			}
		}
		if name == "" {
			name = stashDir + "/" + slug + "--" + full + ".md"
		}
		used[name] = true
		out[name] = s.markdown()
	}
	return out
}

func firstWords(s string) string {
	f := strings.Fields(s)
	if len(f) > 6 {
		f = f[:6]
	}
	return strings.Join(f, " ")
}

// stashFingerprints summarises, per project, what the stash would write. A
// project with no live items has no entry, so a mirror that never had a stash
// is fingerprinted exactly as before.
func stashFingerprints(ctx context.Context, q Querier, only string) (map[string]string, error) {
	where, args := "", []any(nil)
	if only != "" {
		where, args = ` AND project_id = ?`, []any{only}
	}
	rows, err := q.QueryContext(ctx, `SELECT project_id, id, kind, title, tags, content_hash, COALESCE(source_document_id,''), position
		FROM stash_items WHERE deleted_at IS NULL`+where+` ORDER BY 1, id`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bufs := map[string]*bytes.Buffer{}
	for rows.Next() {
		var pid, id, kind, title, tags, hash, src string
		var pos float64
		if err := rows.Scan(&pid, &id, &kind, &title, &tags, &hash, &src, &pos); err != nil {
			return nil, err
		}
		b := bufs[pid]
		if b == nil {
			b = &bytes.Buffer{}
			bufs[pid] = b
		}
		fmt.Fprintf(b, "s\x00%q\x00%q\x00%q\x00%q\x00%q\x00%q\x00%v\n", id, kind, title, tags, hash, src, pos)
	}
	out := make(map[string]string, len(bufs))
	for pid, b := range bufs {
		out[pid] = "stash\x00" + b.String()
	}
	return out, rows.Err()
}
