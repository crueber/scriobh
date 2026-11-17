package mirror

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/entities"
)

// What the mirror holds besides the manuscript, the notes and the bible:
//
//	research/<folder>--<id6>/<note>--<id6>.md   a project's own research notes and clippings, one file each
//	research/.../<folder>--<id6>/_folder.md      a folder's own text
//	compendium/research/<compendium>--<id6>/...  the research of every Compendium the project uses (read-only)
//	attachments/<sha12>-<name>.<ext>             the files its Markdown links to, byte for byte
//
// A standalone Compendium's folder has research/ and attachments/ the same way.
// Research front matter is hand-written, keys in a fixed order and every string
// JSON-quoted, so an unchanged note is the same bytes; nothing in it depends on
// the clock. The scratchpad is a person's own and is not here, and neither is
// the tag manager or the board: they are views, not content.
//
// The same machinery as the rest: the files are compared by hash and only
// changed ones are written, and the attachments are copied from data/files by
// the same rule (recorded by SHA-256, which is their name on disk too).

const (
	researchDir    = "research"
	attachmentsDir = "attachments"
	// projectResearch is where the research of the Compendiums a project uses is
	// projected; it sits under compendium/ but is rendered every time, not
	// carried forward with the entity files, because its fingerprint is not the
	// entities'.
	projectResearch = projectionPrefix + researchDir + "/"
)

// extFile is a binary file to copy into the mirror rather than render.
type extFile struct {
	Src  string
	SHA  string
	Size int64
}

type attachMeta struct {
	ID       string `json:"id"`
	Path     string `json:"path"`
	SHA256   string `json:"sha256"`
	Bytes    int64  `json:"bytes"`
	Mime     string `json:"mime"`
	Filename string `json:"filename"`
	Alt      string `json:"alt"`
}

type rnote struct {
	ID, Parent, Kind, Title, Body, Source, Retrieved, Quote, Status string
	Position                                                        float64
	Tags                                                            []string
}

func qs(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimRight(b.String(), "\n")
}

func (n rnote) markdown() []byte {
	var b bytes.Buffer
	b.WriteString("---\nid: " + qs(n.ID) + "\nkind: " + n.Kind + "\ntitle: " + qs(n.Title) + "\nstatus: " + n.Status + "\n")
	if len(n.Tags) > 0 {
		b.WriteString("tags:\n")
		for _, t := range n.Tags {
			b.WriteString("  - " + qs(t) + "\n")
		}
	}
	if n.Source != "" {
		b.WriteString("source: " + qs(n.Source) + "\n")
	}
	if n.Retrieved != "" {
		b.WriteString("retrieved: " + qs(n.Retrieved) + "\n")
	}
	if n.Quote != "" {
		b.WriteString("quote: " + qs(n.Quote) + "\n")
	}
	b.WriteString("---\n")
	if body := strings.TrimRight(strings.ReplaceAll(n.Body, "\r\n", "\n"), "\n"); body != "" {
		b.WriteString("\n" + body + "\n")
	}
	return b.Bytes()
}

func suffix6(id string) string {
	id = strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(id))
	if len(id) > 6 {
		id = id[len(id)-6:]
	}
	return id
}

// layoutResearch puts the notes under prefix (ending in a slash), folders as
// directories. Two names that still collide get a longer id suffix.
func layoutResearch(notes []rnote, prefix string) map[string][]byte {
	byID := map[string]rnote{}
	for _, n := range notes {
		byID[n.ID] = n
	}
	sorted := append([]rnote(nil), notes...)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].Position != sorted[j].Position {
			return sorted[i].Position < sorted[j].Position
		}
		return sorted[i].ID < sorted[j].ID
	})
	dirs := map[string]string{} // folder id -> its directory, with trailing slash
	used := map[string]bool{}
	pick := func(parentDir, slug, id, ext string) string {
		full := strings.ToLower(strings.NewReplacer("_", "", "-", "").Replace(id))
		for _, n := range []int{6, 12, len(full)} {
			if n > len(full) {
				n = len(full)
			}
			p := parentDir + slug + "--" + full[len(full)-n:] + ext
			if !used[p] {
				used[p] = true
				return p
			}
		}
		return parentDir + slug + "--" + full + ext
	}
	var dirOf func(id string, depth int) string
	dirOf = func(id string, depth int) string {
		if id == "" || depth > 20 {
			return prefix
		}
		if d, ok := dirs[id]; ok {
			return d
		}
		n, ok := byID[id]
		if !ok {
			return prefix
		}
		d := pick(dirOf(n.Parent, depth+1), entities.FileSlug(n.Title), n.ID, "")
		dirs[id] = d + "/"
		return dirs[id]
	}
	out := map[string][]byte{}
	for _, n := range sorted {
		if n.Kind == "folder" {
			out[dirOf(n.ID, 0)+"_folder.md"] = n.markdown()
			continue
		}
		out[pick(dirOf(n.Parent, 0), entities.FileSlug(n.Title), n.ID, ".md")] = n.markdown()
	}
	return out
}

// Querier is what the readers here need.
type Querier = entities.Querier

// loadResearch reads an owner's live notes. col is project_id or compendium_id.
func loadResearch(ctx context.Context, q Querier, col, id string) ([]rnote, error) {
	rows, err := q.QueryContext(ctx, `SELECT n.id, COALESCE(n.parent_id,''), n.kind, n.title, n.body, n.source_url, n.retrieved_on, n.quote, n.status, n.position,
		COALESCE((SELECT group_concat(t.name, char(31)) FROM (SELECT t.name FROM research_tags rt JOIN tags t ON t.id = rt.tag_id WHERE rt.note_id = n.id ORDER BY t.name_key) t), '')
		FROM research_notes n WHERE n.`+col+` = ? AND n.deleted_at IS NULL ORDER BY n.id`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []rnote
	for rows.Next() {
		var n rnote
		var tags string
		if err := rows.Scan(&n.ID, &n.Parent, &n.Kind, &n.Title, &n.Body, &n.Source, &n.Retrieved, &n.Quote, &n.Status, &n.Position, &tags); err != nil {
			return nil, err
		}
		if tags != "" {
			n.Tags = strings.Split(tags, "\x1f")
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// scopeCompendiums lists the live Compendiums a project projects (its primary
// and the ones it references), with their folder names.
func scopeCompendiums(ctx context.Context, q Querier, projectID string) (map[string]string, error) {
	rows, err := q.QueryContext(ctx, `SELECT c.id, c.slug FROM compendiums c WHERE c.deleted_at IS NULL AND c.trashed_at IS NULL AND (
		c.id = (SELECT compendium_id FROM projects WHERE id = ?1) OR
		c.id IN (SELECT target_compendium_id FROM compendium_refs WHERE owner_kind = 'project' AND owner_id = ?1))`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]string{}
	for rows.Next() {
		var id, slug string
		if err := rows.Scan(&id, &slug); err != nil {
			return nil, err
		}
		out[id] = strings.TrimPrefix(compendiumDirName(id, slug), compendiumDirPrefix)
	}
	return out, rows.Err()
}

// loadAttachments reads the attachments of an owner that something links to.
func (m *Mirror) loadAttachments(ctx context.Context, kind, id string) (map[string]extFile, []attachMeta, error) {
	col := "project_id"
	if kind == attachments.OwnerCompendium {
		col = "compendium_id"
	}
	rows, err := m.o.DB.Read().QueryContext(ctx, `SELECT a.id, a.sha256, a.bytes, a.mime, a.ext, a.filename, a.alt FROM attachments a
		WHERE a.`+col+` = ? AND EXISTS (SELECT 1 FROM attachment_refs r WHERE r.attachment_id = a.id AND r.ref_kind <> 'cover') ORDER BY a.id`, id)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	ext := map[string]extFile{}
	var metas []attachMeta
	used := map[string]bool{}
	for rows.Next() {
		var a attachMeta
		var e string
		if err := rows.Scan(&a.ID, &a.SHA256, &a.Bytes, &a.Mime, &e, &a.Filename, &a.Alt); err != nil {
			return nil, nil, err
		}
		src, err := attachments.BlobPath(m.o.DataDir, kind, id, a.SHA256, e)
		if err != nil {
			continue
		}
		base := strings.TrimSuffix(a.Filename, filepath.Ext(a.Filename))
		name := attachmentsDir + "/" + a.SHA256[:12] + "-" + entities.FileSlug(base) + "." + e
		if used[name] {
			name = attachmentsDir + "/" + a.SHA256 + "." + e
		}
		used[name] = true
		a.Path = name
		ext[name] = extFile{Src: src, SHA: a.SHA256, Size: a.Bytes}
		metas = append(metas, a)
	}
	return ext, metas, rows.Err()
}

// extras gathers everything beyond the manuscript and the entity files for one
// project or standalone Compendium: rendered files to merge in, files to copy,
// and the attachment list for the manifest.
func (m *Mirror) extras(ctx context.Context, id string) (files map[string][]byte, ext map[string]extFile, metas []attachMeta, cov *manifestCover, err error) {
	files = map[string][]byte{}
	kind, col := attachments.OwnerProject, "project_id"
	if isCompendiumID(id) {
		kind, col = attachments.OwnerCompendium, "compendium_id"
	}
	notes, err := loadResearch(ctx, m.o.DB.Read(), col, id)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for p, b := range layoutResearch(notes, researchDir+"/") {
		files[p] = b
	}
	if kind == attachments.OwnerProject {
		items, err := loadStash(ctx, m.o.DB.Read(), id)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		for p, b := range layoutStash(items) {
			files[p] = b
		}
		scope, err := scopeCompendiums(ctx, m.o.DB.Read(), id)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		for cid, dir := range scope {
			cn, err := loadResearch(ctx, m.o.DB.Read(), "compendium_id", cid)
			if err != nil {
				return nil, nil, nil, nil, err
			}
			for p, b := range layoutResearch(cn, projectResearch+dir+"/") {
				files[p] = b
			}
		}
	}
	ext, metas, err = m.loadAttachments(ctx, kind, id)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if kind == attachments.OwnerProject {
		cext, c, err := m.loadCover(ctx, id)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		for p, f := range cext {
			ext[p] = f
		}
		cov = c
	}
	return files, ext, metas, cov, nil
}

// extrasFingerprints summarises, from metadata only, what research and
// attachments each project and Compendium would write. An owner with none has
// no entry, so a mirror that never had any is fingerprinted exactly as before.
func extrasFingerprints(ctx context.Context, q Querier, only string) (map[string]string, error) {
	hs := map[string]*bytes.Buffer{}
	add := func(id, line string) {
		b := hs[id]
		if b == nil {
			b = &bytes.Buffer{}
			hs[id] = b
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	// Which Compendiums does each project project? Found first, because a
	// fingerprint taken for one project must hold the research of the Compendiums
	// it uses, exactly as the one taken for every project does: start-up compares
	// the two.
	srows0, err := q.QueryContext(ctx, `SELECT s.pid, s.cid, c.slug FROM (
		SELECT p.id AS pid, c.id AS cid FROM projects p JOIN compendiums c ON c.id = p.compendium_id
		UNION SELECT r.owner_id, r.target_compendium_id FROM compendium_refs r WHERE r.owner_kind = 'project') s
		JOIN compendiums c ON c.id = s.cid AND c.deleted_at IS NULL AND c.trashed_at IS NULL ORDER BY s.pid, s.cid`)
	if err != nil {
		return nil, err
	}
	type pair struct{ pid, cid, slug string }
	var pairs []pair
	for srows0.Next() {
		var p pair
		if err := srows0.Scan(&p.pid, &p.cid, &p.slug); err != nil {
			srows0.Close()
			return nil, err
		}
		if only == "" || p.pid == only {
			pairs = append(pairs, p)
		}
	}
	if err := srows0.Close(); err != nil {
		return nil, err
	}
	where := ""
	var args []any
	if only != "" {
		owners := []string{only}
		for _, p := range pairs {
			owners = append(owners, p.cid)
		}
		ph := make([]string, len(owners))
		for i, o := range owners {
			ph[i] = "?"
			args = append(args, o)
		}
		where = ` AND COALESCE(n.project_id, n.compendium_id) IN (` + strings.Join(ph, ",") + `)`
	}
	// Research, per owner. The tag names are part of a note's file.
	rows, err := q.QueryContext(ctx, `SELECT COALESCE(n.project_id, n.compendium_id), n.id, COALESCE(n.parent_id,''), n.kind, n.title, n.status, n.position,
		n.content_hash, n.source_url, n.retrieved_on,
		COALESCE((SELECT group_concat(t.name, char(31)) FROM (SELECT t.name FROM research_tags rt JOIN tags t ON t.id = rt.tag_id WHERE rt.note_id = n.id ORDER BY t.name_key) t), '')
		FROM research_notes n WHERE n.deleted_at IS NULL`+where+` ORDER BY 1, n.id`, args...)
	if err != nil {
		return nil, err
	}
	research := map[string]*bytes.Buffer{}
	for rows.Next() {
		var owner, id, parent, kind, title, status, hash, src, ret, tags string
		var pos float64
		if err := rows.Scan(&owner, &id, &parent, &kind, &title, &status, &pos, &hash, &src, &ret, &tags); err != nil {
			rows.Close()
			return nil, err
		}
		b := research[owner]
		if b == nil {
			b = &bytes.Buffer{}
			research[owner] = b
		}
		fmt.Fprintf(b, "r\x00%q\x00%q\x00%q\x00%q\x00%q\x00%v\x00%q\x00%q\x00%q\x00%q\n", id, parent, kind, title, status, pos, hash, src, ret, tags)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	for owner, b := range research {
		add(owner, "research\x00"+b.String())
	}
	// A project also writes the research of the Compendiums it uses.
	for _, p := range pairs {
		if b, ok := research[p.cid]; ok {
			add(p.pid, "via\x00"+p.cid+"\x00"+strings.TrimPrefix(compendiumDirName(p.cid, p.slug), compendiumDirPrefix)+"\x00"+b.String())
		}
	}
	// A project's stash.
	sf, err := stashFingerprints(ctx, q, only)
	if err != nil {
		return nil, err
	}
	for pid, line := range sf {
		add(pid, line)
	}
	// Attachments something links to.
	awhere := ""
	if only != "" {
		awhere = ` AND (a.project_id = ?1 OR a.compendium_id = ?1)`
	}
	arows, err := q.QueryContext(ctx, `SELECT COALESCE(a.project_id, a.compendium_id), a.id, a.sha256, a.filename, a.alt FROM attachments a
		WHERE EXISTS (SELECT 1 FROM attachment_refs r WHERE r.attachment_id = a.id AND r.ref_kind <> 'cover')`+awhere+` ORDER BY 1, a.id`, args...)
	if err != nil {
		return nil, err
	}
	for arows.Next() {
		var owner, id, sha, name, alt string
		if err := arows.Scan(&owner, &id, &sha, &name, &alt); err != nil {
			arows.Close()
			return nil, err
		}
		add(owner, fmt.Sprintf("a\x00%q\x00%q\x00%q\x00%q", id, sha, name, alt))
	}
	if err := arows.Close(); err != nil {
		return nil, err
	}
	// A project's own cover: the layout's hash stands for the layout and for the pictures it names.
	cwhere := ""
	var cargs []any
	if only != "" {
		cwhere, cargs = ` AND project_id = ?`, []any{only}
	}
	crows, err := q.QueryContext(ctx, `SELECT project_id, hash FROM cover_layouts WHERE hash <> ''`+cwhere+` ORDER BY project_id`, cargs...)
	if err != nil {
		return nil, err
	}
	for crows.Next() {
		var pid, h string
		if err := crows.Scan(&pid, &h); err != nil {
			crows.Close()
			return nil, err
		}
		add(pid, "cover\x00"+h)
	}
	if err := crows.Close(); err != nil {
		return nil, err
	}
	out := make(map[string]string, len(hs))
	for id, b := range hs {
		sum := sha256.Sum256(b.Bytes())
		out[id] = hex.EncodeToString(sum[:])
	}
	return out, nil
}

// compendiumFingerprints is the entities' fingerprint of each standalone
// Compendium with what its research and attachments add.
func compendiumFingerprints(ctx context.Context, q Querier, only string) (map[string]string, error) {
	fps, err := entities.CompendiumFingerprints(ctx, q, only)
	if err != nil {
		return nil, err
	}
	ex, err := extrasFingerprints(ctx, q, only)
	if err != nil {
		return nil, err
	}
	for id, fp := range fps {
		if x := ex[id]; x != "" {
			sum := sha256.Sum256([]byte(fp + "\x00extras\x00" + x))
			fps[id] = hex.EncodeToString(sum[:])
		}
	}
	return fps, nil
}

// copyIn puts a stored attachment in the mirror. The bytes are hashed as they
// are copied; a file that does not match its name (damaged, or missing) is
// skipped with an error the caller logs, not written.
func (m *Mirror) copyIn(e extFile, dest string) error {
	src, err := os.Open(e.Src)
	if err != nil {
		return err
	}
	defer src.Close()
	if err := os.MkdirAll(m.tmpDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(m.tmpDir, "a-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	h := sha256.New()
	_, err = io.Copy(io.MultiWriter(tmp, h), src)
	if err == nil {
		err = tmp.Sync()
	}
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err == nil && hex.EncodeToString(h.Sum(nil)) != e.SHA {
		err = fmt.Errorf("the stored file does not match its hash")
	}
	if err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := m.fs.MkdirAll(filepath.Dir(dest)); err != nil {
		_ = os.Remove(name)
		return err
	}
	if err := m.fs.Rename(name, dest); err != nil {
		_ = os.Remove(name)
		return err
	}
	return nil
}

// fileHash hashes a file on disk, for comparing a copy against its source.
func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
