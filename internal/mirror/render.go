package mirror

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"hash"
	"regexp"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/domain/entities"
	"github.com/crueber/scriobh/internal/domain/export"
)

// SchemaVersion is the version of project.json. Version 2 added the outline
// metadata of every document (synopses, scene metadata, target, compile flag).
// Version 3 adds pov_entity and location_entity (the id and the book's name of
// the entity a scene's point of view or location is linked to; absent when
// none is). The pov and location text stays, as the display fallback.
const SchemaVersion = 3

// layoutVersion is mixed into every fingerprint, so changing how the mirror
// lays files out makes every mirror stale and rewritten once at the next start.
const layoutVersion = 3

// includeNotes: notes are part of the mirror. There is no per-project switch
// yet; when there is, it belongs in the fingerprint and the manifest.
const includeNotes = true

// ManifestName is the project manifest's file name.
const ManifestName = "project.json"

// ReadmeName is the file that says the folder is generated.
const ReadmeName = "README.md"

const readme = `# A generated copy

This folder is a generated Markdown copy of a project kept in Scríobh. Scríobh
writes it after every change; it never reads it back. Edits made here are not
imported and will be overwritten the next time the project changes.

- manuscript/ holds the manuscript, numbered in reading order. A part or chapter
  that has children becomes a folder, and its own text is the file that starts
  with 00.
- notes/ holds the project's notes.
- compendium/ (only when the project uses a Compendium) is a read-only copy of
  that material (entities, and later research and the timeline) with this
  book's overrides applied. It has a README of its own.
- project.json lists every document with its id, path, title, status, outline
  details (synopses, point of view, location, story dates, word target) and a
  SHA-256 of its text.

To change the work, open it in Scríobh. To bring changes in from elsewhere, use
an explicit import.
`

type manifestProject struct {
	ID       string `json:"id"`
	Slug     string `json:"slug"`
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Author   string `json:"author"`
	Kind     string `json:"kind"`
	Language string `json:"language"`
	Genre    string `json:"genre"`
	Status   string `json:"status"`
}

// manifestDoc is one document in project.json. The outline fields are what a
// re-import or a clone from Git needs to rebuild the outline; none of them is
// in the Markdown file. Field order is fixed, so the bytes are stable.
type manifestDoc struct {
	ID               string            `json:"id"`
	Path             string            `json:"path"`
	Kind             string            `json:"kind"`
	Title            string            `json:"title"`
	Status           string            `json:"status"`
	SynopsisShort    string            `json:"synopsis_short"`
	SynopsisLong     string            `json:"synopsis_long"`
	Pov              string            `json:"pov"`
	PovEntity        *export.EntityRef `json:"pov_entity,omitempty"`
	Location         string            `json:"location"`
	LocationEntity   *export.EntityRef `json:"location_entity,omitempty"`
	StoryDate        string            `json:"story_date"`
	StoryDateEnd     string            `json:"story_date_end"`
	TargetWords      int               `json:"target_words"`
	IncludeInCompile bool              `json:"include_in_compile"`
	WordCount        int               `json:"word_count"`
	SHA256           string            `json:"sha256"`
}

// manifest is project.json. It holds nothing about when it was written: the
// same project state is always the same bytes, so a rewrite is a no-op.
type manifest struct {
	SchemaVersion int             `json:"schema_version"`
	Format        string          `json:"format"`
	Project       manifestProject `json:"project"`
	State         string          `json:"state"`
	IncludesNotes bool            `json:"includes_notes"`
	Documents     []manifestDoc   `json:"documents"`
	// Attachments are the files the manuscript links to, copied under
	// attachments/. The Markdown still says /files/att_...; this maps each id to
	// its file. Absent when there are none, so a project without any is the same
	// bytes as before attachments existed.
	Attachments []attachMeta `json:"attachments,omitempty"`
	// Cover is the project's own cover layout (docs/COVERS.md), without ids; its pictures are in cover/. Absent when the
	// project uses the generated cover, so it is the same bytes as before covers existed.
	Cover *manifestCover `json:"cover,omitempty"`
	// Editions are the books the project was imported from, as metadata (the file, its hash and size, the
	// label and date). The source file itself is never copied into the mirror, so never into a Git push.
	// Absent when there are none, so a project that was never imported from a book is the same bytes as before.
	Editions []export.EditionMeta `json:"editions,omitempty"`
}

// render turns a snapshot into the files of one project directory, keyed by
// slash-separated path relative to it.
func render(s *export.Snapshot) (map[string][]byte, error) { return renderWith(s, nil, nil) }

// renderWith is render with the project's attachments listed in the manifest.
func renderWith(s *export.Snapshot, att []attachMeta, cov *manifestCover) (map[string][]byte, error) {
	files := make(map[string][]byte, len(s.Files)+2)
	m := manifest{
		SchemaVersion: SchemaVersion,
		Format:        "scriobh-mirror",
		Project: manifestProject{ID: s.Project.ID, Slug: s.Project.Slug, Title: s.Project.Title, Subtitle: s.Project.Subtitle,
			Author: s.Project.Author, Kind: s.Project.Kind, Language: s.Project.Language, Genre: s.Project.Genre, Status: s.Project.Status},
		State:         "active",
		IncludesNotes: includeNotes,
		Documents:     make([]manifestDoc, 0, len(s.Files)),
		Attachments:   att,
		Cover:         cov,
		Editions:      s.Editions,
	}
	switch {
	case s.Project.Trashed:
		m.State = "trashed"
	case s.Project.Archived:
		m.State = "archived"
	}
	for _, f := range s.Files {
		files[f.Path] = f.Content
		m.Documents = append(m.Documents, manifestDoc{ID: f.DocID, Path: f.Path, Kind: f.Kind, Title: f.Title, Status: f.Status,
			SynopsisShort: f.SynopsisShort, SynopsisLong: f.SynopsisLong, Pov: f.Pov, PovEntity: f.PovEntity, Location: f.Location, LocationEntity: f.LocationEntity,
			StoryDate: f.StoryDate, StoryDateEnd: f.StoryDateEnd, TargetWords: f.TargetWords, IncludeInCompile: f.IncludeInCompile,
			WordCount: f.WordCount, SHA256: f.SHA256})
	}
	// Not HTML-escaped: a synopsis with an ampersand or an angle bracket is
	// read by people in a repository, and the encoding is as stable either way.
	var mj bytes.Buffer
	enc := json.NewEncoder(&mj)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, err
	}
	files[ManifestName] = mj.Bytes()
	files[ReadmeName] = []byte(readme)
	return files, nil
}

var unsafeName = regexp.MustCompile(`[^a-z0-9-]+`)

// dirName is the project's folder under data/mirror. Slugs are unique per
// owner, not per instance, so two projects can share one; the last six
// characters of the project id (the random end of its ULID) make the name
// unambiguous and let the folder be found from the id alone.
func dirName(projectID, slug string) string {
	s := strings.Trim(unsafeName.ReplaceAllString(strings.ToLower(slug), "-"), "-")
	if s == "" {
		s = "project"
	}
	if len(s) > 48 {
		s = strings.Trim(s[:48], "-")
	}
	return s + "--" + idSuffix(projectID)
}

func idSuffix(id string) string {
	id = strings.ToLower(unsafeName.ReplaceAllString(strings.ToLower(id), ""))
	if len(id) > 6 {
		id = id[len(id)-6:]
	}
	if id == "" {
		id = "x"
	}
	return id
}

// treeHash is the hash of what the mirror holds: every path with the hash of
// its content, in order. It is the "in sync" value status reports.
func treeHash(files map[string]string) string {
	names := make([]string, 0, len(files))
	for n := range files {
		names = append(names, n)
	}
	sort.Strings(names)
	h := sha256.New()
	for _, n := range names {
		fmt.Fprintf(h, "%s\x00%s\n", n, files[n])
	}
	return hex.EncodeToString(h.Sum(nil))
}

func hashBytes(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// fingerprints summarises the stored state each project's mirror depends on,
// from metadata only: the project's fields and, per live document, its id,
// place, title, status, outline metadata and stored content hash. No document text is read (the synopses are, and are short), so
// checking a hundred projects at start-up costs two queries. only restricts it
// to one project; an empty string means all of them.
func fingerprints(ctx context.Context, q entities.Querier, only string) (fps, projections map[string]string, err error) {
	hashes := map[string]hash.Hash{}
	get := func(id string) hash.Hash {
		h := hashes[id]
		if h == nil {
			h = sha256.New()
			fmt.Fprintf(h, "layout %d notes %v\n", layoutVersion, includeNotes)
			hashes[id] = h
		}
		return h
	}
	var args []any
	pw, dw := "", ""
	if only != "" {
		pw, dw = " AND id = ?", " AND project_id = ?"
		args = []any{only}
	}
	rows, err := q.QueryContext(ctx, `SELECT id, slug, title, subtitle, author_name, kind, language, genre, status,
		trashed_at IS NOT NULL, archived_at IS NOT NULL FROM projects WHERE deleted_at IS NULL`+pw+` ORDER BY id`, args...)
	if err != nil {
		return nil, nil, err
	}
	for rows.Next() {
		var id, slug, title, sub, author, kind, lang, genre, status string
		var trashed, archived bool
		if err := rows.Scan(&id, &slug, &title, &sub, &author, &kind, &lang, &genre, &status, &trashed, &archived); err != nil {
			rows.Close()
			return nil, nil, err
		}
		fmt.Fprintf(get(id), "p\x00%q\x00%q\x00%q\x00%q\x00%q\x00%q\x00%q\x00%q\x00%q\x00%v\x00%v\n", id, slug, title, sub, author, kind, lang, genre, status, trashed, archived)
	}
	if err := rows.Close(); err != nil {
		return nil, nil, err
	}
	drows, err := q.QueryContext(ctx, `SELECT project_id, id, COALESCE(parent_id,''), kind, title, status, position, content_hash,
		synopsis_short, synopsis_long, pov, location, story_date, story_date_end, target_words, include_in_compile,
		COALESCE(pov_entity_id, ''), COALESCE(location_entity_id, '')
		FROM documents WHERE deleted_at IS NULL AND compendium_id IS NULL AND kind <> 'research'`+dw+` ORDER BY project_id, id`, args...)
	if err != nil {
		return nil, nil, err
	}
	for drows.Next() {
		var pid, id, parent, kind, title, status, hash string
		var synS, synL, pov, loc, sd, se, povE, locE string
		var target, include int
		var pos float64
		if err := drows.Scan(&pid, &id, &parent, &kind, &title, &status, &pos, &hash, &synS, &synL, &pov, &loc, &sd, &se, &target, &include, &povE, &locE); err != nil {
			drows.Close()
			return nil, nil, err
		}
		h, ok := hashes[pid]
		if !ok {
			continue // a document of a project that is gone or hidden
		}
		fmt.Fprintf(h, "d\x00%q\x00%q\x00%q\x00%q\x00%q\x00%v\x00%q\x00%q\x00%q\x00%q\x00%q\x00%q\x00%q\x00%d\x00%d\x00%q\x00%q\n",
			id, parent, kind, title, status, pos, hash, synS, synL, pov, loc, sd, se, target, include, povE, locE)
	}
	if err := drows.Close(); err != nil {
		return nil, nil, err
	}
	// What the compendium/ projection depends on joins the fingerprint of a
	// project that has any Compendium material; for one that has none the
	// fingerprint is exactly what it was before Compendiums existed.
	projections, err = entities.ProjectFingerprints(ctx, q, only)
	if err != nil {
		return nil, nil, err
	}
	extras, err := extrasFingerprints(ctx, q, only)
	if err != nil {
		return nil, nil, err
	}
	// The editions a project was imported from are in its manifest, so they are in its fingerprint (and a
	// project with none has the fingerprint it always had).
	erows, err := q.QueryContext(ctx, `SELECT project_id, id, kind, label, identifier, sha256, size, imported_at FROM editions`+strings.Replace(dw, " AND", " WHERE", 1)+` ORDER BY project_id, imported_at, id`, args...)
	if err != nil {
		return nil, nil, err
	}
	for erows.Next() {
		var pid, id, kind, label, ident, sum, at string
		var size int64
		if err := erows.Scan(&pid, &id, &kind, &label, &ident, &sum, &size, &at); err != nil {
			erows.Close()
			return nil, nil, err
		}
		if h, ok := hashes[pid]; ok {
			fmt.Fprintf(h, "e\x00%q\x00%q\x00%q\x00%q\x00%q\x00%d\x00%q\n", id, kind, label, ident, sum, size, at)
		}
	}
	if err := erows.Close(); err != nil {
		return nil, nil, err
	}
	out := make(map[string]string, len(hashes))
	for id, h := range hashes {
		if x := projections[id]; x != "" {
			fmt.Fprintf(h, "projection\x00%s\n", x)
		}
		if x := extras[id]; x != "" {
			fmt.Fprintf(h, "extras\x00%s\n", x)
		}
		out[id] = hex.EncodeToString(h.Sum(nil))
	}
	return out, projections, nil
}
