package editiondiff

import (
	"context"
	"regexp"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/domain/comanchor"
	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

// docInfo is what the write ops need to know about a document of the project's side.
type docInfo struct {
	ID        string
	Revision  int
	Body      string // comanchor.Body of the content: what suggestion offsets are measured in
	Composite bool   // the text of the unit is the document's with its scenes put back in, so no one range of the document is the unit
	Title     string
}

// sideData is one side as loaded.
type sideData struct {
	Units []Unit
	Docs  map[string]*docInfo // by unit key, the project's side only
	// Missing pictures: units whose text names a picture the project does not hold.
	Source string
}

var rePic = regexp.MustCompile(`scriobh-picture:([0-9a-f]{64})`)

// loadEditionRecord reads an edition's chapters and baselines, as the import recorded them.
func loadEditionRecord(ctx context.Context, q editions.Querier, e editions.Edition, lim Limits) (sideData, error) {
	chs, err := editions.Chapters(ctx, q, e.ID)
	if err != nil {
		return sideData{}, err
	}
	out := sideData{Source: "record", Units: make([]Unit, 0, len(chs))}
	total := 0
	for _, c := range chs {
		txt, err := editions.Baseline(ctx, q, e.ID, c.Ord)
		if err != nil {
			return sideData{}, err
		}
		if total += len(txt); total > lim.MaxTotalBytes {
			return sideData{}, ErrTooLarge
		}
		out.Units = append(out.Units, Unit{Key: "e" + itoa(c.Ord), Ord: c.Ord, Mapped: c.DocumentID, Path: c.SourcePath, Kind: c.Kind, Title: c.Title, Parent: c.ParentOrd - 1, Text: txt})
	}
	return out, nil
}

// loadEditionFile reads an edition's source file again with the import's reader.
func loadEditionFile(ctx context.Context, env *ops.Env, e editions.Edition, sections, pages string, lim Limits) (sideData, error) {
	data, err := editions.Open(env, e)
	if err != nil {
		return sideData{}, err
	}
	bk, err := imports.ReadEpub(ctx, data, sections, pages)
	if err != nil {
		return sideData{}, err
	}
	out := sideData{Source: "file", Units: make([]Unit, 0, len(bk.Chapters))}
	total := 0
	for _, c := range bk.Chapters {
		if total += len(c.Markdown); total > lim.MaxTotalBytes {
			return sideData{}, ErrTooLarge
		}
		out.Units = append(out.Units, Unit{Key: "e" + itoa(c.Ord), Ord: c.Ord, Path: c.SourcePath, Kind: c.Kind, Title: c.Title, Parent: c.ParentOrd - 1, Text: c.Markdown})
	}
	return out, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// manuscriptKinds are the documents that are the book's text.
const manuscriptKinds = `'part','chapter','scene','front_matter','back_matter'`

type docRow struct {
	id, parent, kind, title, path, source, content string
	pos                                            float64
	rev                                            int
}

// loadProject reads the project's manuscript in reading order. A document's source path is carried only when the import
// that made it is the one that made the edition (sourceID): two imports of books from one tool name their files alike. Scenes the import made from a chapter's file are put
// back into the chapter's text for the chapters named in mapped, so that a book imported with split_scenes compares
// as the book it came from.
func loadProject(ctx context.Context, q editions.Querier, projectID, sourceID string, mapped map[string]bool, lim Limits) (sideData, error) {
	var n, size int64
	if err := q.QueryRowContext(ctx, `SELECT count(*), COALESCE(sum(length(CAST(content AS BLOB))),0) FROM documents
		WHERE project_id = ? AND deleted_at IS NULL AND compendium_id IS NULL AND kind IN (`+manuscriptKinds+`)`, projectID).Scan(&n, &size); err != nil {
		return sideData{}, err
	}
	if n > int64(lim.MaxUnits) || size > int64(lim.MaxTotalBytes) {
		return sideData{}, ErrTooLarge
	}
	rows, err := q.QueryContext(ctx, `SELECT d.id, COALESCE(d.parent_id,''), d.kind, d.title, d.position, d.content, d.revision, COALESCE(s.source_path,''), COALESCE(s.source_id,'')
		FROM documents d LEFT JOIN document_sources s ON s.document_id = d.id
		WHERE d.project_id = ? AND d.deleted_at IS NULL AND d.compendium_id IS NULL AND d.kind IN (`+manuscriptKinds+`)`, projectID)
	if err != nil {
		return sideData{}, err
	}
	defer rows.Close()
	byID := map[string]*docRow{}
	kids := map[string][]*docRow{}
	var all []*docRow
	for rows.Next() {
		var r docRow
		if err := rows.Scan(&r.id, &r.parent, &r.kind, &r.title, &r.pos, &r.content, &r.rev, &r.path, &r.source); err != nil {
			return sideData{}, err
		}
		byID[r.id] = &r
		all = append(all, &r)
	}
	if err := rows.Err(); err != nil {
		return sideData{}, err
	}
	var roots []*docRow
	for _, r := range all {
		if _, ok := byID[r.parent]; ok && r.parent != "" {
			kids[r.parent] = append(kids[r.parent], r)
		} else {
			roots = append(roots, r)
		}
	}
	order := func(s []*docRow) {
		sort.Slice(s, func(i, j int) bool {
			if s[i].pos != s[j].pos {
				return s[i].pos < s[j].pos
			}
			return s[i].id < s[j].id
		})
	}
	order(roots)
	// scenes of a mapped chapter that came from the same file: folded into it
	folded := map[string]bool{}
	text := map[string]string{}
	composite := map[string]bool{}
	for _, r := range all {
		body := comanchor.Body(r.content)
		text[r.id] = body
		if !mapped[r.id] || r.source == "" {
			continue
		}
		order(kids[r.id])
		var sb strings.Builder
		sb.WriteString(body)
		any := false
		for _, k := range kids[r.id] {
			if k.kind != "scene" || k.source == "" || k.source != r.source { // a scene the same import made
				continue
			}
			any = true
			folded[k.id] = true
			t := strings.TrimRight(sb.String(), "\n")
			sb.Reset()
			sb.WriteString(t)
			sb.WriteString("\n\n## " + k.title + "\n\n" + comanchor.Body(k.content))
		}
		if any {
			text[r.id] = sb.String()
			composite[r.id] = true
		}
	}
	out := sideData{Source: "project", Docs: map[string]*docInfo{}}
	index := map[string]int{}
	var walk func(r *docRow)
	walk = func(r *docRow) {
		if !folded[r.id] {
			index[r.id] = len(out.Units)
			parent := -1
			if i, ok := index[r.parent]; ok {
				parent = i
			}
			path := ""
			if sourceID != "" && r.source == sourceID { // a path means something only for the import the edition was made by
				path = r.path
			}
			out.Units = append(out.Units, Unit{Key: r.id, ID: r.id, Path: path, Kind: r.kind, Title: r.title, Parent: parent, Text: text[r.id]})
			out.Docs[r.id] = &docInfo{ID: r.id, Revision: r.rev, Body: comanchor.Body(r.content), Composite: composite[r.id], Title: r.title}
		}
		order(kids[r.id])
		for _, k := range kids[r.id] {
			walk(k)
		}
	}
	for _, r := range roots {
		walk(r)
	}
	return out, nil
}

// pictureIDs maps the SHA-256 of a picture's cleaned bytes to the project's attachment that holds it.
func pictureIDs(ctx context.Context, q editions.Querier, projectID string) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT sha256, id FROM attachments WHERE project_id = ? AND kind = 'image'`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var sha, id string
		if err := rows.Scan(&sha, &id); err != nil {
			return nil, err
		}
		m[sha] = id
	}
	return m, rows.Err()
}

// toProjectPictures rewrites the pictures of an edition's text, named by the hash of their bytes, as the addresses of
// the project's attachments that hold them, so that a chapter that was imported with its pictures compares as it
// was. A picture the project does not hold is left as the hash.
func toProjectPictures(ctx context.Context, q editions.Querier, projectID string, units []Unit) error {
	var ids map[string]string
	for i := range units {
		if !strings.Contains(units[i].Text, "scriobh-picture:") {
			continue
		}
		if ids == nil {
			var err error
			if ids, err = pictureIDs(ctx, q, projectID); err != nil {
				return err
			}
		}
		units[i].Text = rePic.ReplaceAllStringFunc(units[i].Text, func(m string) string {
			if id, ok := ids[m[len("scriobh-picture:"):]]; ok {
				return "/files/" + id
			}
			return m
		})
	}
	return nil
}
