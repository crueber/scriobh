package entities

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compaccess"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// A Compendium on its own: the files of its standalone mirror, and the Markdown
// bundle of its export. Both are the same set of files, rendered by the same
// code from one read transaction, so a bundle and a mirror of the same state
// are the same bytes (apart from the manifest's name).

// MaxExportEntities bounds one Compendium export.
const MaxExportEntities = 20_000

// CompendiumMeta is the Compendium row the files describe.
type CompendiumMeta struct {
	ID, Name, Slug, Kind, Description, State, UpdatedAt string
	Standalone                                          bool // no project has it as its primary
}

// CompendiumSet is a Compendium's files, ready to write.
type CompendiumSet struct {
	Meta     CompendiumMeta
	Files    []File           // the entity files, by path
	Other    []ProjectionFile // manifest, README, timeline placeholder
	Latest   string           // the newest updated_at among the Compendium and its entities
	Entities int
}

const compendiumReadme = `# A generated copy of a Compendium

This folder is a generated Markdown copy of a Compendium kept in Scríobh: the
shared Compendium for one story world or subject. Scríobh writes it after every
change and never reads it back; edit the Compendium in Scríobh.

- entities/ has one file per entity, in a folder per kind, with its aliases,
  summary, custom fields and relationships in the front matter.
- the manifest lists the Compendium's settings, its field definitions, its
  references to other Compendiums and every entity file with its SHA-256.
- timeline.md is the timeline, in story order; research/ is where research notes will be
  written.
`

// LoadCompendium reads a Compendium and renders its files. manifestName is the
// manifest's file name: compendium.json in the mirror, scriobh.json in an
// export. capped refuses a Compendium over MaxExportEntities.
func LoadCompendium(ctx context.Context, d *db.DB, compendiumID, manifestName, format string, capped bool) (*CompendiumSet, error) {
	tx, err := d.Read().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	return loadCompendiumFrom(ctx, tx, compendiumID, manifestName, format, capped)
}

func loadCompendiumFrom(ctx context.Context, q Querier, cid, manifestName, format string, capped bool) (*CompendiumSet, error) {
	var m CompendiumMeta
	var trashed, archived bool
	var projectsUsing int
	if err := q.QueryRowContext(ctx, `SELECT id, name, slug, kind, description, updated_at, trashed_at IS NOT NULL, archived_at IS NOT NULL,
			(SELECT count(*) FROM projects p WHERE p.compendium_id = compendiums.id AND p.deleted_at IS NULL)
		FROM compendiums WHERE id = ? AND deleted_at IS NULL`, cid).Scan(&m.ID, &m.Name, &m.Slug, &m.Kind, &m.Description, &m.UpdatedAt, &trashed, &archived, &projectsUsing); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, ops.NotFound("compendium")
		}
		return nil, err
	}
	m.State = "active"
	switch {
	case trashed:
		m.State = "trashed"
	case archived:
		m.State = "archived"
	}
	m.Standalone = projectsUsing == 0
	set := &CompendiumSet{Meta: m, Latest: m.UpdatedAt}
	var n int
	if err := q.QueryRowContext(ctx, `SELECT count(*) FROM entities WHERE compendium_id = ? AND trashed_at IS NULL`, cid).Scan(&n); err != nil {
		return nil, err
	}
	if capped && n > MaxExportEntities {
		return nil, ops.Invalid("", fmt.Sprintf("This Compendium has %d entities; an export is limited to %d.", n, MaxExportEntities))
	}
	rows, err := q.QueryContext(ctx, `SELECT id, kind, name, sort_key, summary, body, fields_json, color, updated_at FROM entities
		WHERE compendium_id = ? AND trashed_at IS NULL ORDER BY sort_key, id`, cid)
	if err != nil {
		return nil, err
	}
	var items []FileEntity
	index := map[string]int{}
	for rows.Next() {
		var e FileEntity
		var fields, at string
		if err := rows.Scan(&e.ID, &e.Kind, &e.Name, &e.SortKey, &e.Summary, &e.Body, &fields, &e.Color, &at); err != nil {
			rows.Close()
			return nil, err
		}
		e.Fields = nonEmpty(parseFields(fields))
		if at > set.Latest {
			set.Latest = at
		}
		index[e.ID] = len(items)
		items = append(items, e)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	arows, err := q.QueryContext(ctx, `SELECT a.entity_id, a.alias FROM entity_aliases a JOIN entities e ON e.id = a.entity_id
		WHERE e.compendium_id = ? AND e.trashed_at IS NULL ORDER BY a.entity_id, a.alias_key, a.id`, cid)
	if err != nil {
		return nil, err
	}
	for arows.Next() {
		var id, alias string
		if err := arows.Scan(&id, &alias); err != nil {
			arows.Close()
			return nil, err
		}
		if i, ok := index[id]; ok {
			items[i].Aliases = append(items[i].Aliases, alias)
		}
	}
	if err := arows.Err(); err != nil {
		_ = arows.Close()
		return nil, err
	}
	if err := arows.Close(); err != nil {
		return nil, err
	}
	rrows, err := q.QueryContext(ctx, `SELECT r.from_entity_id, r.to_entity_id, r.label, r.inverse_label, r.symmetric, f.name, t.name
		FROM entity_relations r JOIN entities f ON f.id = r.from_entity_id JOIN entities t ON t.id = r.to_entity_id
		WHERE r.compendium_id = ? AND f.trashed_at IS NULL AND t.trashed_at IS NULL`, cid)
	if err != nil {
		return nil, err
	}
	for rrows.Next() {
		var from, to, label, inverse, fname, tname string
		var sym bool
		if err := rrows.Scan(&from, &to, &label, &inverse, &sym, &fname, &tname); err != nil {
			rrows.Close()
			return nil, err
		}
		if i, ok := index[from]; ok {
			items[i].Relation = append(items[i].Relation, label+": "+tname)
		}
		if i, ok := index[to]; ok {
			l := label
			if !sym && inverse != "" {
				l = inverse
			}
			items[i].Relation = append(items[i].Relation, l+": "+fname)
		}
	}
	if err := rrows.Err(); err != nil {
		_ = rrows.Close()
		return nil, err
	}
	if err := rrows.Close(); err != nil {
		return nil, err
	}
	for i := range items {
		sort.Strings(items[i].Relation)
	}
	set.Files = Layout(items, "entities/")
	set.Entities = len(set.Files)

	defRows, err := q.QueryContext(ctx, `SELECT kind, key, label, type, choices_json FROM entity_field_defs WHERE compendium_id = ? ORDER BY kind, position, key`, cid)
	if err != nil {
		return nil, err
	}
	var defsOut []FieldDef
	for defRows.Next() {
		var f FieldDef
		var ch string
		if err := defRows.Scan(&f.Kind, &f.Key, &f.Label, &f.Type, &ch); err != nil {
			defRows.Close()
			return nil, err
		}
		f.Choices = []string{}
		_ = json.Unmarshal([]byte(ch), &f.Choices)
		defsOut = append(defsOut, f)
	}
	if err := defRows.Err(); err != nil {
		_ = defRows.Close()
		return nil, err
	}
	if err := defRows.Close(); err != nil {
		return nil, err
	}
	refRows, err := q.QueryContext(ctx, `SELECT t.id, t.name FROM compendium_refs r JOIN compendiums t ON t.id = r.target_compendium_id
		WHERE r.owner_kind = 'compendium' AND r.owner_id = ? AND t.deleted_at IS NULL ORDER BY t.name, t.id`, cid)
	if err != nil {
		return nil, err
	}
	type refOut struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	refsOut := []refOut{}
	for refRows.Next() {
		var r refOut
		if err := refRows.Scan(&r.ID, &r.Name); err != nil {
			refRows.Close()
			return nil, err
		}
		refsOut = append(refsOut, r)
	}
	if err := refRows.Err(); err != nil {
		_ = refRows.Close()
		return nil, err
	}
	if err := refRows.Close(); err != nil {
		return nil, err
	}
	if defsOut == nil {
		defsOut = []FieldDef{}
	}
	type entOut struct {
		ID     string `json:"id"`
		Path   string `json:"path"`
		Kind   string `json:"kind"`
		Name   string `json:"name"`
		SHA256 string `json:"sha256"`
	}
	ents := make([]entOut, len(set.Files))
	for i, f := range set.Files {
		ents[i] = entOut{ID: f.EntityID, Path: f.Path, Kind: f.Kind, Name: f.Name, SHA256: f.SHA256}
	}
	manifest := map[string]any{
		"schema_version":    1,
		"format":            format,
		"compendium":        map[string]any{"id": m.ID, "slug": m.Slug, "name": m.Name, "kind": m.Kind, "description": m.Description, "state": m.State},
		"field_definitions": defsOut,
		"references":        refsOut,
		"entities":          ents,
	}
	mj, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return nil, err
	}
	tl, err := timelineFile(ctx, q, cid)
	if err != nil {
		return nil, err
	}
	set.Other = []ProjectionFile{
		{Path: manifestName, Content: append(mj, '\n')},
		{Path: "README.md", Content: []byte(compendiumReadme)},
		{Path: "timeline.md", Content: tl},
	}
	return set, nil
}

// MirrorFiles is every file of the standalone mirror, by path.
func (s *CompendiumSet) MirrorFiles() map[string][]byte {
	out := make(map[string][]byte, len(s.Files)+len(s.Other))
	for _, f := range s.Files {
		out[f.Path] = f.Content
	}
	for _, f := range s.Other {
		out[f.Path] = f.Content
	}
	return out
}

// DirSlug is the slug a standalone mirror folder is named from.
func (s *CompendiumSet) DirSlug() string { return s.Meta.Slug }

// ---- the export op -----------------------------------------------------------------------------------------------------------

func registerExport(r *ops.Registry) {
	ops.Register(r, ops.Op{
		Name: "compendium.export", Method: "POST", Path: "/api/v1/compendiums/{id}/export", Status: 200,
		Desc: "Export a Compendium as a Markdown bundle (a zip): one file per entity with front matter (name, kind, aliases, summary, custom fields, relationships), a manifest of the Compendium's settings, field definitions and every file's SHA-256, and a README. " +
			"Deterministic: the same state gives the same bytes. Recorded with a manifest of what it held. Works with no project open. Editor.",
		Auth: ops.RequireCompendium(compaccess.Editor),
	}, export)
}

// ExportIn asks for a Compendium export.
type ExportIn struct {
	ID string `path:"id" json:"-" desc:"Compendium id."`
}

// CompendiumRef lets the registry authorize by membership.
func (in ExportIn) CompendiumRef() string { return in.ID }

// ExportFile is the export. Over REST it is the file itself; elsewhere it is
// this JSON, with the file base64-encoded in body.
type ExportFile struct {
	ExportID    string `json:"export_id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Bytes       int    `json:"bytes"`
	SHA256      string `json:"sha256"`
	Entities    int    `json:"entities" desc:"How many entities the export holds."`
	Body        []byte `json:"body" desc:"The file. The REST route returns it as the response body instead of JSON."`
}

// Download makes ExportFile an ops.Download.
func (f ExportFile) Download() (string, string, []byte) { return f.Filename, f.ContentType, f.Body }

func export(c *ops.Ctx, in ExportIn) (ExportFile, error) {
	set, err := LoadCompendium(c, c.DB(), in.ID, "scriobh.json", "scriobh-compendium-bundle", true)
	if err != nil {
		return ExportFile{}, err
	}
	files := map[string][]byte{}
	for p, b := range set.MirrorFiles() {
		files[p] = b
	}
	body, err := zipFiles(files, set.Latest)
	if err != nil {
		return ExportFile{}, ops.Internal(err)
	}
	sum := sha256.Sum256(body)
	name := projects.Slugify(set.Meta.Name) + "-compendium.zip"
	file := ExportFile{ExportID: db.NewID("exp"), Filename: name, ContentType: "application/zip", Bytes: len(body),
		SHA256: hex.EncodeToString(sum[:]), Entities: set.Entities, Body: body}
	type held struct {
		ID     string `json:"id"`
		SHA256 string `json:"sha256"`
	}
	heldEnts := make([]held, 0, len(set.Files))
	for _, f := range set.Files {
		heldEnts = append(heldEnts, held{f.EntityID, f.SHA256})
	}
	mj, err := json.Marshal(map[string]any{"compendium_id": in.ID, "entities": heldEnts})
	if err != nil {
		return ExportFile{}, ops.Internal(err)
	}
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO compendium_exports(id, compendium_id, user_id, preset, format, filename, bytes, sha256, manifest_json, created_at)
			VALUES (?,?,?,?,?,?,?,?,?,?)`, file.ExportID, in.ID, c.P.UserID, "bundle", "md", name, file.Bytes, file.SHA256, string(mj), db.Now()); err != nil {
			return err
		}
		c.Audit(ops.AuditEntry{Action: "export.run", TargetType: "compendium", TargetID: in.ID,
			Detail: map[string]any{"export_id": file.ExportID, "format": "md", "entities": file.Entities, "bytes": file.Bytes}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return ExportFile{}, err
	}
	return file, nil
}

// zipFiles writes a zip with sorted names, no compression, a fixed time and no
// owner or extra fields, so equal files are equal bytes.
func zipFiles(files map[string][]byte, stamp string) ([]byte, error) {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	date, tm := dosTime(stamp)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, n := range names {
		h := &zip.FileHeader{Name: n, Method: zip.Store, ModifiedDate: date, ModifiedTime: tm}
		h.SetMode(0o644)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(files[n]); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

func dosTime(stored string) (date, tm uint16) {
	t, err := db.Parse(stored)
	if err != nil || t.Year() < 1980 {
		t = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	t = t.UTC()
	return uint16(t.Year()-1980)<<9 | uint16(t.Month())<<5 | uint16(t.Day()),
		uint16(t.Hour())<<11 | uint16(t.Minute())<<5 | uint16(t.Second()/2)
}

// projectionIndex is compendium/index.json of a project's mirror: what the
// projection draws on, and each entity file with its hash.
func projectionIndex(sources []source, files []File, rows []eff) []byte {
	type srcOut struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Role string `json:"role"`
	}
	type entOut struct {
		ID         string `json:"id"`
		Path       string `json:"path"`
		Kind       string `json:"kind"`
		Name       string `json:"name"`
		Compendium string `json:"compendium"`
		Via        string `json:"via"`
		Overridden bool   `json:"overridden"`
		SHA256     string `json:"sha256"`
	}
	byID := map[string]eff{}
	for _, r := range rows {
		byID[r.ID] = r
	}
	srcs := make([]srcOut, len(sources))
	for i, s := range sources {
		srcs[i] = srcOut{s.ID, s.Name, s.Role}
	}
	ents := make([]entOut, 0, len(files))
	for _, f := range files {
		r := byID[f.EntityID]
		ents = append(ents, entOut{f.EntityID, f.Path, f.Kind, f.Name, r.CompendiumName, r.Via, r.Override != nil, f.SHA256})
	}
	b, _ := json.MarshalIndent(map[string]any{"schema_version": 1, "format": "scriobh-compendium-projection", "sources": srcs, "entities": ents}, "", "  ")
	return append(b, '\n')
}
