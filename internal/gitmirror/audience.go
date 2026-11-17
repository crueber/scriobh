package gitmirror

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path"
	"strings"

	"github.com/crueber/scriobh/internal/domain/share"
	"github.com/crueber/scriobh/internal/text"
)

// The reader's view of a project, for a remote whose audience is public (F-362).
//
// A public remote carries what a reader of the book sees and nothing else. The rules are the ones the
// static site and the compile use, applied to the mirror's files:
//
//   - notes are never pushed, whatever include_notes says (the strictest wins);
//   - a document marked do not compile is out, and so is everything inside it;
//   - with only_done, a document whose status is not done is out, and everything inside it;
//   - a document's text is share.Clean(text, omit): the alternatives comment is gone (the chosen wording is the
//     text), ghosted text is gone, and what the reading view leaves out is cut (front matter, %%private
//     notes%%, HTML comments, footnotes referenced only from a private note). A text that cannot be placed by
//     the parser is withheld, never pushed as it is (failing closed is the rule);
//   - the manifest is rebuilt from an allow-list: titles, paths, kinds and word counts of what is pushed. The
//     mirror's synopses, point of view, location, story dates, targets, entity references (names from the
//     bible), ids and statuses are not in it;
//   - an attachment is pushed only when a pushed document links to it;
//   - the project's own cover (docs/COVERS.md) is pushed only when the remote's include_cover is on, which for a
//     public remote is a person's decision at a browser, with the remote's name typed (F-861), and off until then:
//     how each side is placed and the pictures, which the cover upload has cleaned of EXIF and GPS (the cover's alt
//     text is not in the public manifest, as an attachment's is not).
//
// The database is asked as well as the mirror: a document that the database says is out (do not compile, or
// inside one that is) is out even when the mirror has not caught up with the change yet. A document the
// database does not know is judged by the mirror's manifest alone.

// Why a document is withheld from a public remote.
const (
	WithheldNote       = "note"
	WithheldNoCompile  = "do_not_compile"
	WithheldNotDone    = "not_done"
	WithheldUnreadable = "cannot_be_cleaned"
	WithheldTrashed    = "trashed"
	WithheldStale      = "stale_mirror"
)

// Withheld names a document a public remote does not carry, and why.
type Withheld struct {
	Path   string `json:"path" desc:"The document's path in the mirror."`
	Title  string `json:"title"`
	Reason string `json:"reason" enum:"note,do_not_compile,not_done,cannot_be_cleaned,trashed,stale_mirror"`
}

// dbDoc is what the database says about a document, for the second opinion.
type dbDoc struct {
	parent, kind, status, hash string
	include, trashed           bool
}

func (s *Service) dbDocs(ctx context.Context, projectID string) map[string]dbDoc {
	out := map[string]dbDoc{}
	// Trashed documents are read too: a document that was marked do not compile and then trashed is still out, and so
	// is everything that hangs from it, while the mirror's manifest (which may be behind) still lists it.
	rows, err := s.o.DB.Read().QueryContext(ctx, `SELECT id, COALESCE(parent_id,''), kind, status, include_in_compile, content_hash, deleted_at IS NOT NULL FROM documents
		WHERE project_id = ? AND compendium_id IS NULL AND kind <> 'research'`, projectID)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var d dbDoc
		var inc, tr int
		if rows.Scan(&id, &d.parent, &d.kind, &d.status, &inc, &d.hash, &tr) == nil {
			d.include, d.trashed = inc != 0, tr != 0
			out[id] = d
		}
	}
	return out
}

// vetoed reports whether the database puts a document out: itself or an ancestor marked do not compile, or (with
// onlyDone) not done.
func vetoed(id string, docs map[string]dbDoc, onlyDone bool) (reason string, out bool) {
	for depth, cur := 0, id; cur != "" && depth < 10_000; depth++ {
		d, ok := docs[cur]
		if !ok {
			return "", false
		}
		if d.trashed {
			return WithheldTrashed, true
		}
		if !d.include {
			return WithheldNoCompile, true
		}
		if onlyDone && d.status != "done" {
			return WithheldNotDone, true
		}
		cur = d.parent
	}
	return "", false
}

// ownerFolder returns the folder a document owns when its file is the text of a document that has children: the
// mirror writes such a document as <folder>/00-<slug>.md ahead of its children (the zeros are all zeros, the
// width of the numbers beside it). A leaf has no folder. Everything under the folder is inside the document.
func ownerFolder(p string) (string, bool) {
	dir, file := path.Split(p)
	dir = strings.TrimSuffix(dir, "/")
	if dir == "" || dir == "manuscript" || dir == "notes" {
		return "", false
	}
	i := strings.IndexByte(file, '-')
	if i < 2 || strings.Trim(file[:i], "0") != "" {
		return "", false
	}
	return dir, true
}

// publicManifest is the manifest of a public remote.
type publicManifest struct {
	SchemaVersion json.RawMessage    `json:"schema_version"`
	Format        string             `json:"format"`
	Project       publicProject      `json:"project"`
	State         string             `json:"state"`
	IncludesNotes bool               `json:"includes_notes"`
	Documents     []publicDoc        `json:"documents"`
	Attachments   []publicAttachment `json:"attachments,omitempty"`
	Cover         *coverMeta         `json:"cover,omitempty"`
}

// publicAttachment is an attachment's entry from an allow-list: where it is and how to check it. Not the alt text,
// not the name the file had when it was uploaded and not its media type (the extension says that): the mirror's
// path holds a slug of the upload's name, so a public remote names the file by its hash instead.
type publicAttachment struct {
	ID     string `json:"id"`
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

type publicProject struct {
	Title    string `json:"title"`
	Subtitle string `json:"subtitle"`
	Author   string `json:"author"`
	Kind     string `json:"kind"`
	Language string `json:"language"`
}

type publicDoc struct {
	Path             string `json:"path"`
	Kind             string `json:"kind"`
	Title            string `json:"title"`
	IncludeInCompile bool   `json:"include_in_compile"`
	WordCount        int    `json:"word_count"`
	SHA256           string `json:"sha256"`
}

// inManifest is the part of the mirror's manifest the filter reads.
type inManifest struct {
	SchemaVersion json.RawMessage `json:"schema_version"`
	Project       struct {
		Title, Subtitle, Author, Kind, Language, Genre string
	} `json:"project"`
	State       string            `json:"state"`
	Documents   []inDoc           `json:"documents"`
	Attachments []json.RawMessage `json:"attachments"`
	Cover       json.RawMessage   `json:"cover"`
}

type inDoc struct {
	ID               string `json:"id"`
	Path             string `json:"path"`
	Kind             string `json:"kind"`
	Title            string `json:"title"`
	Status           string `json:"status"`
	IncludeInCompile *bool  `json:"include_in_compile"`
}

// restrict returns the reader's view of sn for remote r. The snapshot it is given holds the notes too (so that
// they can be reported as withheld); none of them is in what it returns.
func (s *Service) restrict(ctx context.Context, r *remoteRow, sn *snapshot) (*snapshot, []Withheld, *Error) {
	prefix := r.PathPrefix
	raw, ok := sn.files[join(prefix, PublishedManifest)]
	if !ok {
		return nil, nil, gerr(CodeInternal, "The mirror has no manifest to build the public view from.")
	}
	var in inManifest
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, nil, &Error{Code: CodeInternal, Message: "The mirror's manifest cannot be read; run a mirror rebuild.", cause: err}
	}
	docs := s.dbDocs(ctx, r.ProjectID)

	// Pass one: which documents are out, and which folders they take with them.
	out := make([]string, len(in.Documents))
	var folders []string
	for i, d := range in.Documents {
		switch {
		case d.Kind == "note" || strings.HasPrefix(d.Path, "notes/"):
			out[i] = WithheldNote
		case d.IncludeInCompile != nil && !*d.IncludeInCompile:
			out[i] = WithheldNoCompile
		case r.OnlyDone && d.Status != "done":
			out[i] = WithheldNotDone
		}
		if out[i] == "" {
			if why, v := vetoed(d.ID, docs, r.OnlyDone); v {
				out[i] = why
			}
		}
		if out[i] != "" && out[i] != WithheldNote {
			if f, ok := ownerFolder(d.Path); ok {
				folders = append(folders, f+"/")
			}
		}
	}
	for i, d := range in.Documents {
		if out[i] != "" {
			continue
		}
		for _, f := range folders {
			if strings.HasPrefix(d.Path, f) {
				out[i] = outerReason(in.Documents, out, f)
				break
			}
		}
	}

	// Pass two: the text of what is left.
	files := map[string][]byte{}
	var kept []publicDoc
	var keptDocs []manifestDoc
	var withheld []Withheld
	var all bytes.Buffer
	for i, d := range in.Documents {
		if out[i] != "" {
			withheld = append(withheld, Withheld{Path: d.Path, Title: d.Title, Reason: out[i]})
			continue
		}
		b, ok := sn.files[join(prefix, d.Path)]
		if !ok {
			withheld = append(withheld, Withheld{Path: d.Path, Title: d.Title, Reason: WithheldUnreadable})
			continue
		}
		clean, ok := share.Clean(string(b), "omit")
		if !ok {
			withheld = append(withheld, Withheld{Path: d.Path, Title: d.Title, Reason: WithheldUnreadable})
			continue
		}
		if dd, known := docs[d.ID]; known && dd.hash != "" {
			// The text the mirror holds is not the text the database holds now (a save, a note made private, that the
			// mirror has not caught up with): it is left out until it has.
			if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != dd.hash {
				withheld = append(withheld, Withheld{Path: d.Path, Title: d.Title, Reason: WithheldStale})
				continue
			}
		}
		sum := sha256.Sum256([]byte(clean))
		wc := text.WordCount(clean)
		files[join(prefix, d.Path)] = []byte(clean)
		kept = append(kept, publicDoc{Path: d.Path, Kind: d.Kind, Title: d.Title, IncludeInCompile: true, WordCount: wc, SHA256: hex.EncodeToString(sum[:])})
		keptDocs = append(keptDocs, manifestDoc{Path: d.Path, Kind: d.Kind, Title: d.Title, WordCount: wc})
		all.WriteString(clean)
		all.WriteByte('\n')
	}

	// Attachments: only those a pushed document links to.
	var atts []publicAttachment
	binary := map[string]bool{}
	for _, ra := range in.Attachments {
		var a publicAttachment
		if json.Unmarshal(ra, &a) != nil || a.ID == "" || len(a.SHA256) < 16 || !pushableAttachment(a.Path) {
			continue
		}
		full := join(prefix, a.Path)
		b, ok := sn.files[full]
		if !ok || !bytes.Contains(all.Bytes(), []byte(a.ID)) {
			continue
		}
		a.Path = "attachments/" + a.SHA256[:16] + path.Ext(a.Path)
		if !pushableAttachment(a.Path) {
			continue
		}
		files[join(prefix, a.Path)] = b
		if sn.binary[full] {
			binary[join(prefix, a.Path)] = true
		}
		atts = append(atts, a)
	}

	// The cover: the layout and its pictures, as the mirror holds them (the snapshot read them whole or not at all).
	var cover *coverMeta
	if len(in.Cover) > 0 {
		var c coverMeta
		if json.Unmarshal(in.Cover, &c) == nil {
			complete := true
			for _, f := range c.files() {
				if _, ok := sn.files[join(prefix, f)]; !ok || !coverName.MatchString(f) {
					complete = false
				}
			}
			if complete {
				for _, f := range c.files() {
					files[join(prefix, f)] = sn.files[join(prefix, f)]
					binary[join(prefix, f)] = true
				}
				cover = &c
			}
		}
	}

	m := publicManifest{SchemaVersion: in.SchemaVersion, Format: "scriobh-mirror", State: "active",
		Project:   publicProject{Title: in.Project.Title, Subtitle: in.Project.Subtitle, Author: in.Project.Author, Kind: in.Project.Kind, Language: in.Project.Language},
		Documents: kept, Attachments: atts, Cover: cover}
	if m.Documents == nil {
		m.Documents = []publicDoc{}
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(m); err != nil {
		return nil, nil, &Error{Code: CodeInternal, Message: "The manifest could not be prepared.", cause: err}
	}
	files[join(prefix, PublishedManifest)] = buf.Bytes()

	res := &snapshot{files: files, docs: keptDocs, mirrorHash: sn.mirrorHash, hashes: map[string]string{}, project: sn.project, skipped: sn.skipped}
	if len(binary) > 0 {
		res.binary = binary
	}
	for p, b := range files {
		sum := sha256.Sum256(b)
		res.hashes[p] = hex.EncodeToString(sum[:])
	}
	return res, withheld, nil
}

// outerReason gives a document inside an excluded folder the reason the folder's owner has.
func outerReason(docs []inDoc, out []string, folder string) string {
	for i, d := range docs {
		if out[i] != "" && out[i] != WithheldNote {
			if f, ok := ownerFolder(d.Path); ok && f+"/" == folder {
				return out[i]
			}
		}
	}
	return WithheldNoCompile
}

// snapshotFor reads the snapshot a remote carries: the whole mirror's documents for a private remote, the
// reader's view for a public one.
func (s *Service) snapshotFor(ctx context.Context, r *remoteRow) (*snapshot, []Withheld, *Error) {
	if r.Audience != AudiencePrivate {
		if e := s.mirrorCurrent(ctx, r.ProjectID); e != nil {
			return nil, nil, e
		}
		// Notes are read so that the plan can say they are withheld; restrict drops them, and nothing of them is pushed.
		sn, e := s.readSnapshot(r.ProjectID, true, r.IncludeCover, r.PathPrefix)
		if e != nil {
			return nil, nil, e
		}
		return s.restrict(ctx, r, sn)
	}
	sn, e := s.readSnapshot(r.ProjectID, r.IncludeNotes, r.IncludeCover, r.PathPrefix)
	return sn, nil, e
}

// withheldCounts counts the withheld documents by reason: what a push row records, never a title or a path.
func withheldCounts(w []Withheld) map[string]int {
	m := map[string]int{}
	for _, x := range w {
		m[x.Reason]++
	}
	return m
}

// currency is what a mirror that can say whether it is current offers (mirror.Mirror does).
type currency interface {
	Current(ctx context.Context, projectID string) (bool, error)
}

// CodeMirrorBehind is the retryable refusal to build a public push from a mirror that is not current.
const CodeMirrorBehind = "mirror_behind"

// mirrorCurrent refuses (retryably) while the mirror is pending or differs from the database. It is asked for a
// public audience before anything is read, by every path that builds one: a push of any trigger, a force push, the
// first push of a publish, the plan and the Markdown edition. Text the mirror holds from before a change (a note
// made private, a document marked do not compile) is not published in the meantime.
func (s *Service) mirrorCurrent(ctx context.Context, projectID string) *Error {
	c, ok := s.o.Source.(currency)
	if !ok {
		return nil
	}
	cur, err := c.Current(ctx, projectID)
	if err != nil || !cur {
		return &Error{Code: CodeMirrorBehind, Message: "The Markdown mirror is a few seconds behind your latest change, and a public copy is not built from a mirror that is out of date. It will be tried again.", Retry: true, cause: err}
	}
	return nil
}
