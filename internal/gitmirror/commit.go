package gitmirror

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"golang.org/x/crypto/ssh"
)

// What is pushed. The mirror folder holds the manifest (project.json), a
// README that says the folder is generated, the manuscript and the notes. The
// repository gets the manifest under the name the importer recognises
// (scriobh.json), the manuscript, and the notes if the remote includes them.
// Nothing else: the files pushed are exactly the documents the manifest lists
// plus the manifest, never a walk of the folder.
const (
	// PublishedManifest is the manifest's name in the repository. The mirror
	// writes it as project.json; the importer reads either, and the repository
	// layout in docs/ARCHITECTURE.md calls it scriobh.json.
	PublishedManifest = "scriobh.json"
	mirrorManifest    = "project.json"

	maxFileBytes  = 16 << 20
	maxTotalBytes = 512 << 20
	maxDocuments  = 20_000

	// LocalBranch is the branch of the local repository that commits are
	// built on. The remote branch is whatever the remote is configured with.
	localBranch = "refs/heads/scriobh"
)

type manifestDoc struct {
	Path      string `json:"path"`
	Kind      string `json:"kind"`
	Title     string `json:"title"`
	WordCount int    `json:"word_count"`
}

// manifestFile is the part of project.json the push needs. Everything else in
// it is carried through untouched.
type manifestFile struct {
	SchemaVersion json.RawMessage   `json:"schema_version"`
	Format        json.RawMessage   `json:"format"`
	Project       json.RawMessage   `json:"project"`
	State         json.RawMessage   `json:"state"`
	IncludesNotes bool              `json:"includes_notes"`
	Documents     []json.RawMessage `json:"documents"`
	// Attachments are the files the manuscript links to, listed by the mirror
	// and pushed under attachments/. One too large to push is left out of the
	// list the repository gets, so the list never names a file that is not there.
	Attachments []json.RawMessage `json:"attachments,omitempty"`
}

// manifestAttachment is the part of an attachment's entry the push checks.
type manifestAttachment struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

var attachmentName = regexp.MustCompile(`^attachments/[A-Za-z0-9][A-Za-z0-9._-]{0,200}\.(png|jpg|gif|webp|pdf|txt|md)$`)

// pushableAttachment reports whether a path from the manifest may be pushed as
// an attachment: one file directly under attachments/, with a plain name and an
// extension the server keeps.
func pushableAttachment(p string) bool {
	return attachmentName.MatchString(p) && !strings.Contains(p, "..")
}

// scannable is what the secret check reads: every pushed file except the binary
// attachments, whose bytes would match a pattern by chance.
func (sn *snapshot) scannable() map[string][]byte {
	if len(sn.binary) == 0 {
		return sn.files
	}
	out := make(map[string][]byte, len(sn.files))
	for p, b := range sn.files {
		if !sn.binary[p] {
			out[p] = b
		}
	}
	return out
}

// snapshot is a consistent copy of what one remote should hold.
type snapshot struct {
	// files maps the repository path (prefix included) to its content.
	files map[string][]byte
	// docs lists the documents in reading order, with paths relative to the
	// prefix.
	docs []manifestDoc
	// mirrorHash is the mirror's own hash of the tree this was read from.
	mirrorHash string
	// hashes maps repository path to the SHA-256 of its content.
	hashes map[string]string
	// project is the project's own title, subtitle and author, from the manifest.
	project []string
	// binary marks the pushed files that are not text (images and PDFs).
	binary map[string]bool
	// skipped counts attachments left out for being over the file size limit.
	skipped int
}

// pushable reports whether a path from the manifest may be pushed: a Markdown
// file under manuscript/ or notes/, clean, with no hidden segments.
func pushable(p string) bool {
	if p == "" || len(p) > 1024 || !utf8.ValidString(p) || p != path.Clean(p) || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return false
	}
	top, rest, ok := strings.Cut(p, "/")
	if !ok || (top != "manuscript" && top != "notes") || !strings.HasSuffix(rest, ".md") {
		return false
	}
	for _, seg := range strings.Split(p, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasPrefix(seg, ".") {
			return false
		}
	}
	return true
}

// readSnapshot reads the mirror under Hold, so every file belongs to one state
// of the project, and releases it before returning: no network call is ever
// made while the mirror is held.
func (s *Service) readSnapshot(projectID string, includeNotes bool, prefix string) (*snapshot, *Error) {
	if s.o.Source == nil {
		return nil, gerr(CodeMirrorOff, "The Markdown mirror is not running on this server, and the push is built from it.")
	}
	dir, release, err := s.o.Source.Hold(projectID)
	if err != nil {
		if strings.Contains(err.Error(), "switched off") {
			return nil, gerr(CodeMirrorOff, "The Markdown mirror is switched off (SCRIOBH_MIRROR=off), and the push is built from it.")
		}
		return nil, &Error{Code: CodeNoMirror, Message: "The project's Markdown mirror has not been written yet. It is written a few seconds after a change; the push will be tried again.", Retry: true, cause: err}
	}
	defer release()
	hash := s.o.Source.Status(projectID).TreeHash

	raw, err := os.ReadFile(filepath.Join(dir, mirrorManifest))
	if err != nil {
		return nil, &Error{Code: CodeNoMirror, Message: "The mirror has no project.json yet.", Retry: true, cause: err}
	}
	var mf manifestFile
	if err := json.Unmarshal(raw, &mf); err != nil {
		return nil, &Error{Code: CodeInternal, Message: "The mirror's project.json cannot be read; run a mirror rebuild.", cause: err}
	}
	if len(mf.Documents) > maxDocuments {
		return nil, gerr(CodeTooLarge, "The project has more than %d documents; it is too large to push.", maxDocuments)
	}
	sn := &snapshot{files: map[string][]byte{}, hashes: map[string]string{}, mirrorHash: hash}
	var pf struct{ Title, Subtitle, Author string }
	_ = json.Unmarshal(mf.Project, &pf)
	for _, t := range []string{pf.Title, pf.Subtitle, pf.Author} {
		if t != "" {
			sn.project = append(sn.project, t)
		}
	}
	var total int64
	var kept []json.RawMessage
	for _, rd := range mf.Documents {
		var d manifestDoc
		if err := json.Unmarshal(rd, &d); err != nil {
			return nil, &Error{Code: CodeInternal, Message: "The mirror's project.json lists a document that cannot be read; run a mirror rebuild.", cause: err}
		}
		if !pushable(d.Path) {
			// The message does not name the path: it can belong to the bible, which a
			// person reading this error may not be allowed to see. The log has it.
			s.log.Warn("git: the mirror lists a file that is not pushed", "project", projectID, "path", shorten(d.Path))
			return nil, gerr(CodeInternal, "The mirror lists a file that is not pushed. Run a mirror rebuild; if it persists, the server log names it.")
		}
		if !includeNotes && strings.HasPrefix(d.Path, "notes/") {
			continue
		}
		full := filepath.Join(dir, filepath.FromSlash(d.Path))
		fi, err := os.Lstat(full)
		if err != nil || !fi.Mode().IsRegular() {
			return nil, &Error{Code: CodeNoMirror, Message: "A file the mirror lists is missing; run a mirror rebuild.", Retry: true, cause: errors.New("missing " + d.Path)}
		}
		if fi.Size() > maxFileBytes {
			return nil, gerr(CodeTooLarge, "%s is larger than %d MB; it is too large to push.", shorten(d.Path), maxFileBytes>>20)
		}
		b, err := os.ReadFile(full)
		if err != nil {
			return nil, &Error{Code: CodeNoMirror, Message: "A file in the mirror could not be read.", Retry: true, cause: err}
		}
		total += int64(len(b))
		if total > maxTotalBytes {
			return nil, gerr(CodeTooLarge, "The manuscript is larger than %d MB; it is too large to push.", maxTotalBytes>>20)
		}
		sn.files[join(prefix, d.Path)] = b
		sn.docs = append(sn.docs, d)
		kept = append(kept, rd)
	}
	var keptAtt []json.RawMessage
	for _, ra := range mf.Attachments {
		var a manifestAttachment
		if err := json.Unmarshal(ra, &a); err != nil || !pushableAttachment(a.Path) {
			return nil, gerr(CodeInternal, "The mirror lists an attachment that is not pushed; run a mirror rebuild.")
		}
		full := filepath.Join(dir, filepath.FromSlash(a.Path))
		// An attachment is left out, with its manifest entry, when it cannot be
		// pushed as it is: too large, or missing or damaged in the mirror (the
		// mirror skips a file whose source is gone, for instance after a restore
		// made without files). One missing image must not stop a manuscript from
		// being pushed for ever, and the published list never names a file that is
		// not in the repository.
		fi, err := os.Lstat(full)
		if err != nil || !fi.Mode().IsRegular() || fi.Size() > maxFileBytes {
			sn.skipped++
			continue
		}
		b, err := os.ReadFile(full)
		if err != nil {
			sn.skipped++
			continue
		}
		if sum := sha256.Sum256(b); hex.EncodeToString(sum[:]) != a.SHA256 {
			sn.skipped++
			continue
		}
		total += int64(len(b))
		if total > maxTotalBytes {
			return nil, gerr(CodeTooLarge, "The manuscript and its attachments are larger than %d MB; they are too large to push.", maxTotalBytes>>20)
		}
		sn.files[join(prefix, a.Path)] = b
		if !strings.HasSuffix(a.Path, ".txt") && !strings.HasSuffix(a.Path, ".md") {
			if sn.binary == nil {
				sn.binary = map[string]bool{}
			}
			sn.binary[join(prefix, a.Path)] = true
		}
		keptAtt = append(keptAtt, ra)
	}
	out := raw
	if !includeNotes || sn.skipped > 0 {
		mf.Attachments = keptAtt
		if !includeNotes {
			mf.IncludesNotes = false
			mf.Documents = kept
			if mf.Documents == nil {
				mf.Documents = []json.RawMessage{}
			}
		}
		j, err := json.MarshalIndent(mf, "", "  ")
		if err != nil {
			return nil, &Error{Code: CodeInternal, Message: "The manifest could not be prepared.", cause: err}
		}
		out = append(j, '\n')
	}
	sn.files[join(prefix, PublishedManifest)] = out
	for p, b := range sn.files {
		sum := sha256.Sum256(b)
		sn.hashes[p] = hex.EncodeToString(sum[:])
	}
	return sn, nil
}

func join(prefix, p string) string {
	if prefix == "" {
		return p
	}
	return prefix + "/" + p
}

// ---- the local repository ------------------------------------------------------

// repoDir is where a remote's local repository lives. The project id is part of
// the path so that purging a project removes it without needing the rows the
// purge has already cascaded away.
func (s *Service) repoDir(projectID, remoteID string) string {
	return filepath.Join(s.o.DataDir, "git", projectID, remoteID)
}

// openLocal opens, or creates, the remote's local repository: a bare repository
// that only ever receives commits built from a mirror snapshot. It is never a
// working tree, never pulled into, and holds no remote configuration.
func (s *Service) openLocal(projectID, remoteID string) (*git.Repository, error) {
	dir := s.repoDir(projectID, remoteID)
	if repo, err := git.PlainOpen(dir); err == nil {
		return repo, nil
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(filepath.Join(s.o.DataDir, "git"), 0o700)
	return git.PlainInit(dir, true)
}

// openLocalIfExists opens the local repository without creating it.
func (s *Service) openLocalIfExists(projectID, remoteID string) (*git.Repository, error) {
	if _, err := os.Stat(s.repoDir(projectID, remoteID)); err != nil {
		return nil, nil
	}
	return git.PlainOpen(s.repoDir(projectID, remoteID))
}

// commitKnown reports whether a commit id is in the repository.
func commitKnown(repo *git.Repository, sha string) bool {
	if !plumbing.IsHash(sha) {
		return false
	}
	_, err := repo.CommitObject(plumbing.NewHash(sha))
	return err == nil
}

// treeFiles flattens a commit's tree into path -> blob hash.
func treeFiles(repo *git.Repository, c *object.Commit) (map[string]plumbing.Hash, error) {
	out := map[string]plumbing.Hash{}
	if c == nil {
		return out, nil
	}
	t, err := c.Tree()
	if err != nil {
		return nil, err
	}
	err = t.Files().ForEach(func(f *object.File) error {
		out[f.Name] = f.Hash
		return nil
	})
	return out, err
}

// writeBlob stores content and returns its hash; an existing blob is not rewritten.
func writeBlob(st storer.EncodedObjectStorer, data []byte) (plumbing.Hash, error) {
	h := plumbing.ComputeHash(plumbing.BlobObject, data)
	if st.HasEncodedObject(h) == nil {
		return h, nil
	}
	o := st.NewEncodedObject()
	o.SetType(plumbing.BlobObject)
	w, err := o.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := w.Write(data); err != nil {
		_ = w.Close()
		return plumbing.ZeroHash, err
	}
	if err := w.Close(); err != nil {
		return plumbing.ZeroHash, err
	}
	return st.SetEncodedObject(o)
}

type dirNode struct {
	files map[string]plumbing.Hash
	dirs  map[string]*dirNode
}

func newDir() *dirNode {
	return &dirNode{files: map[string]plumbing.Hash{}, dirs: map[string]*dirNode{}}
}

// writeTree stores the tree for files and returns its hash. Entries are in
// Git's order (bytewise by name, a directory compared as if its name ended in
// a slash): a mis-ordered tree hashes fine locally and is refused by servers.
func writeTree(st storer.EncodedObjectStorer, files map[string][]byte) (plumbing.Hash, error) {
	root := newDir()
	for p, b := range files {
		h, err := writeBlob(st, b)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		parts := strings.Split(p, "/")
		n := root
		for _, seg := range parts[:len(parts)-1] {
			c := n.dirs[seg]
			if c == nil {
				c = newDir()
				n.dirs[seg] = c
			}
			n = c
		}
		n.files[parts[len(parts)-1]] = h
	}
	return writeDir(st, root)
}

func writeDir(st storer.EncodedObjectStorer, n *dirNode) (plumbing.Hash, error) {
	type ent struct {
		sortKey string
		e       object.TreeEntry
	}
	var ents []ent
	for name, h := range n.files {
		ents = append(ents, ent{name, object.TreeEntry{Name: name, Mode: filemode.Regular, Hash: h}})
	}
	for name, d := range n.dirs {
		h, err := writeDir(st, d)
		if err != nil {
			return plumbing.ZeroHash, err
		}
		ents = append(ents, ent{name + "/", object.TreeEntry{Name: name, Mode: filemode.Dir, Hash: h}})
	}
	sort.Slice(ents, func(i, j int) bool { return ents[i].sortKey < ents[j].sortKey })
	t := &object.Tree{}
	for _, e := range ents {
		t.Entries = append(t.Entries, e.e)
	}
	o := st.NewEncodedObject()
	if err := t.Encode(o); err != nil {
		return plumbing.ZeroHash, err
	}
	return st.SetEncodedObject(o)
}

// treeHashOf computes the hash a tree of files would have, without writing it.
// It is how "same snapshot, same tree" is checked, and how an unchanged
// snapshot is recognised before anything is written.
func treeHashOf(files map[string][]byte) (plumbing.Hash, error) {
	return writeTree(discardStore{}, files)
}

// identity is the author and committer of every commit of a remote.
type identity struct{ name, email string }

// identityFor is the author and committer of a remote's commits: the name and
// email the owner set, else a neutral one. The default carries nothing about
// the person (not their email address, not the host): a repository can be
// public, and its commits are forever.
func (s *Service) identityFor(r *remoteRow) identity {
	if r.AuthorName != "" && r.AuthorEmail != "" {
		return identity{r.AuthorName, r.AuthorEmail}
	}
	return identity{"Scríobh", "noreply@scriobh"}
}

// commitResult is what building a commit for a snapshot came to.
type commitResult struct {
	head      plumbing.Hash // the local head after this call
	parent    plumbing.Hash // its parent, zero for the first commit
	created   bool          // a new commit was made
	tree      plumbing.Hash
	changed   []changedFile
	bytes     int64
	subject   string
	filesDiff int
}

// changedFile is a document a commit added, changed or removed.
type changedFile struct {
	Path   string `json:"path"`
	Change string `json:"change" enum:"added,changed,removed"`
	Delta  int    `json:"words" desc:"The change in words: positive for added text, negative for removed."`
}

// commitSnapshot builds one commit from sn on top of the local head. When the
// snapshot's tree equals the head's tree it makes no commit: no change, no push.
// The commit's time is now (the push time); everything else about it is a
// function of the snapshot, the identity and the previous head.
func (s *Service) commitSnapshot(repo *git.Repository, sn *snapshot, id identity, title, prefix string, now time.Time, signer ssh.Signer) (*commitResult, error) {
	var head *object.Commit
	if ref, err := repo.Reference(localBranch, true); err == nil {
		if c, err := repo.CommitObject(ref.Hash()); err == nil {
			head = c
		}
	}
	newTree, err := writeTree(repo.Storer, sn.files)
	if err != nil {
		return nil, err
	}
	res := &commitResult{tree: newTree}
	if head != nil {
		res.parent = head.Hash
		res.head = head.Hash
		if head.TreeHash == newTree {
			return res, nil
		}
	}
	oldFiles, err := treeFiles(repo, head)
	if err != nil {
		return nil, err
	}
	oldDocs := map[string]manifestDoc{}
	if head != nil {
		oldDocs = docsAt(repo, oldFiles, prefix)
	}
	res.changed, res.bytes, res.filesDiff = diffFiles(sn, oldFiles, oldDocs, prefix)
	msg, subject := commitMessage(title, sn, res.changed, head == nil, oldDocs)
	res.subject = subject
	sig := object.Signature{Name: id.name, Email: id.email, When: now.UTC().Truncate(time.Second)}
	c := &object.Commit{Author: sig, Committer: sig, Message: msg, TreeHash: newTree}
	if head != nil {
		c.ParentHashes = []plumbing.Hash{head.Hash}
	}
	if signer != nil {
		// The signature covers the commit as git would verify it: the object without the gpgsig header.
		so := repo.Storer.NewEncodedObject()
		if err := c.EncodeWithoutSignature(so); err != nil {
			return nil, err
		}
		r, err := so.Reader()
		if err != nil {
			return nil, err
		}
		unsigned, err := io.ReadAll(r)
		_ = r.Close()
		if err != nil {
			return nil, err
		}
		if c.PGPSignature, err = signSSH(signer, unsigned); err != nil {
			return nil, err
		}
	}
	o := repo.Storer.NewEncodedObject()
	if err := c.Encode(o); err != nil {
		return nil, err
	}
	h, err := repo.Storer.SetEncodedObject(o)
	if err != nil {
		return nil, err
	}
	if err := repo.Storer.SetReference(plumbing.NewHashReference(localBranch, h)); err != nil {
		return nil, err
	}
	res.head, res.created = h, true
	return res, nil
}

// docsAt reads the documents listed by the manifest of a tree.
func docsAt(repo *git.Repository, files map[string]plumbing.Hash, prefix string) map[string]manifestDoc {
	out := map[string]manifestDoc{}
	h, ok := files[join(prefix, PublishedManifest)]
	if !ok {
		return out
	}
	blob, err := repo.BlobObject(h)
	if err != nil {
		return out
	}
	r, err := blob.Reader()
	if err != nil {
		return out
	}
	defer r.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(r); err != nil {
		return out
	}
	var mf manifestFile
	if json.Unmarshal(buf.Bytes(), &mf) != nil {
		return out
	}
	for _, rd := range mf.Documents {
		var d manifestDoc
		if json.Unmarshal(rd, &d) == nil && d.Path != "" {
			out[d.Path] = d
		}
	}
	return out
}

// diffFiles compares the snapshot with the previous tree. Paths are relative to
// the prefix in the result. changed lists documents only, in reading order
// (new and changed), then removed ones by path; bytes counts the content of
// every file that is new or different; n counts every file that differs, the
// manifest included.
func diffFiles(sn *snapshot, old map[string]plumbing.Hash, oldDocs map[string]manifestDoc, prefix string) (changed []changedFile, bytes int64, n int) {
	newBlob := map[string]plumbing.Hash{}
	for p, b := range sn.files {
		newBlob[p] = plumbing.ComputeHash(plumbing.BlobObject, b)
	}
	for p, h := range newBlob {
		if oh, ok := old[p]; !ok || oh != h {
			n++
			bytes += int64(len(sn.files[p]))
		}
	}
	for p := range old {
		if _, ok := newBlob[p]; !ok {
			n++
		}
	}
	seen := map[string]bool{}
	for _, d := range sn.docs {
		full := join(prefix, d.Path)
		seen[d.Path] = true
		oh, had := old[full]
		switch {
		case !had:
			changed = append(changed, changedFile{d.Path, "added", d.WordCount})
		case oh != newBlob[full]:
			changed = append(changed, changedFile{d.Path, "changed", d.WordCount - oldDocs[d.Path].WordCount})
		}
	}
	var gone []string
	for p := range old {
		rel := p
		if prefix != "" {
			if !strings.HasPrefix(p, prefix+"/") {
				continue
			}
			rel = strings.TrimPrefix(p, prefix+"/")
		}
		if pushable(rel) && !seen[rel] {
			gone = append(gone, rel)
		}
	}
	sort.Strings(gone)
	for _, p := range gone {
		changed = append(changed, changedFile{p, "removed", -oldDocs[p].WordCount})
	}
	return changed, bytes, n
}

const maxBodyLines = 100

// commitMessage builds the subject and the whole message. The subject reads
// like `The Salt Road · ch 12: +214 words`; the body lists what changed in a
// fixed order. Nothing in it depends on the time or the machine.
func commitMessage(title string, sn *snapshot, changed []changedFile, first bool, oldDocs map[string]manifestDoc) (msg, subject string) {
	title = strings.TrimSpace(title)
	if title == "" {
		title = "Untitled"
	}
	delta := 0
	for _, c := range changed {
		delta += c.Delta
	}
	chapterNo := map[string]int{}
	n := 0
	byPath := map[string]manifestDoc{}
	for _, d := range sn.docs {
		byPath[d.Path] = d
		if d.Kind == "chapter" {
			n++
			chapterNo[d.Path] = n
		}
	}
	var what string
	switch {
	case first:
		what = "first push"
		delta = 0
		for _, d := range sn.docs {
			delta += d.WordCount
		}
	case len(changed) == 0:
		what = "project details"
	case len(changed) == 1:
		c := changed[0]
		d, ok := byPath[c.Path]
		if !ok {
			d = oldDocs[c.Path]
		}
		switch {
		case c.Change == "removed":
			what = "removed " + quote(d.Title)
		case d.Kind == "chapter" && chapterNo[c.Path] > 0:
			what = fmt.Sprintf("ch %d", chapterNo[c.Path])
		default:
			what = quote(d.Title)
		}
	default:
		allChapters := true
		for _, c := range changed {
			d, ok := byPath[c.Path]
			if !ok {
				d = oldDocs[c.Path]
			}
			if d.Kind != "chapter" {
				allChapters = false
			}
		}
		if allChapters {
			what = fmt.Sprintf("%d chapters", len(changed))
		} else {
			what = fmt.Sprintf("%d documents", len(changed))
		}
	}
	subject = fmt.Sprintf("%s · %s: %s", title, what, words(delta, first))
	var b strings.Builder
	b.WriteString(subject)
	b.WriteString("\n")
	if len(changed) > 0 {
		b.WriteString("\n")
		for i, c := range changed {
			if i == maxBodyLines {
				fmt.Fprintf(&b, "… and %d more\n", len(changed)-maxBodyLines)
				break
			}
			d, ok := byPath[c.Path]
			if !ok {
				d = oldDocs[c.Path]
			}
			fmt.Fprintf(&b, "%s: %s (%s) %s\n", c.Change, c.Path, quote(d.Title), signed(c.Delta))
		}
	}
	return b.String(), subject
}

func quote(t string) string {
	t = strings.Join(strings.Fields(t), " ")
	r := []rune(t)
	if len(r) > 60 {
		t = string(r[:60]) + "…"
	}
	if t == "" {
		t = "untitled"
	}
	return "“" + t + "”"
}

func signed(n int) string {
	if n > 0 {
		return fmt.Sprintf("+%d", n)
	}
	return fmt.Sprintf("%d", n)
}

func words(n int, first bool) string {
	if first {
		return fmt.Sprintf("%d words", n)
	}
	return signed(n) + " words"
}

// discardStore is an EncodedObjectStorer that keeps nothing, to hash a tree.
type discardStore struct{}

func (discardStore) NewEncodedObject() plumbing.EncodedObject { return &plumbing.MemoryObject{} }
func (discardStore) SetEncodedObject(o plumbing.EncodedObject) (plumbing.Hash, error) {
	return o.Hash(), nil
}
func (discardStore) HasEncodedObject(plumbing.Hash) error { return plumbing.ErrObjectNotFound }
func (discardStore) EncodedObjectSize(plumbing.Hash) (int64, error) {
	return 0, plumbing.ErrObjectNotFound
}
func (discardStore) EncodedObject(plumbing.ObjectType, plumbing.Hash) (plumbing.EncodedObject, error) {
	return nil, plumbing.ErrObjectNotFound
}
func (discardStore) IterEncodedObjects(plumbing.ObjectType) (storer.EncodedObjectIter, error) {
	return nil, plumbing.ErrObjectNotFound
}
func (discardStore) AddAlternate(string) error { return nil }
