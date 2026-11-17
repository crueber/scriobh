package site

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
)

// A project's published site lives at <root>/<name>/public, with a record of the
// files that were written beside it, at <root>/<name>/managed.json, outside the
// folder a web server serves. <name> is the project's slug and the last six
// characters of its id, chosen once and kept: renaming a project must not move
// the folder a proxy points at.
//
// Publishing writes the whole site to a sibling of public, then swaps the two
// with a rename, so a reader sees the old site or the new one, apart from the
// instant between the two renames. It replaces only a folder that is empty or
// that holds nothing but files it wrote; anything else is refused, never
// deleted. It never follows a symlink.

// Managed is the record of what a publish wrote.
type Managed struct {
	ProjectID string        `json:"project_id"`
	Hash      string        `json:"hash"`
	Files     []ManagedFile `json:"files"`
}

// ManagedFile is one published file.
type ManagedFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

// ErrUnmanaged is returned when the folder holds files this program did not
// write.
type ErrUnmanaged struct{ Paths []string }

func (e *ErrUnmanaged) Error() string {
	more := ""
	if len(e.Paths) > 5 {
		more = fmt.Sprintf(" and %d more", len(e.Paths)-5)
		e.Paths = e.Paths[:5]
	}
	return "the folder holds files Scríobh did not write (" + strings.Join(e.Paths, ", ") + more + "); move them away or publish elsewhere. Nothing was changed."
}

// ErrEdited is returned when a file Scríobh wrote has been changed by hand since:
// replacing it would lose the change.
type ErrEdited struct{ Paths []string }

func (e *ErrEdited) Error() string {
	more := ""
	if len(e.Paths) > 5 {
		more = fmt.Sprintf(" and %d more", len(e.Paths)-5)
		e.Paths = e.Paths[:5]
	}
	return "files Scríobh published have been changed since (" + strings.Join(e.Paths, ", ") + more + "). Publishing replaces them; send overwrite_edited to do so. Nothing was changed."
}

// DirOptions are the choices of one publish.
type DirOptions struct {
	// OverwriteEdited replaces files that were changed by hand since they were
	// published. Without it such a file stops the publish.
	OverwriteEdited bool
}

// Reason says in a few words why a write to the folder failed, for a person:
// no path, no system detail.
func Reason(err error) string {
	switch {
	case errors.Is(err, fs.ErrExist):
		return "two file names differ only in case or accents and this file system treats them as one file"
	case errors.Is(err, fs.ErrPermission):
		return "Scríobh may not write to the publish folder"
	case errors.Is(err, syscall.ENOSPC):
		return "the disk is full"
	case errors.Is(err, syscall.ENAMETOOLONG):
		return "a file name is too long for this file system"
	}
	return "the publish folder could not be written"
}

// ErrUnsafe is returned for a symlink or a path that is not what it must be.
var ErrUnsafe = errors.New("site: the target folder is not safe to write to")

// DirName is the folder name of a project: its slug and the tail of its id.
func DirName(slug, projectID string) string {
	s := Slugify(slug)
	if s == "" {
		s = "site"
	}
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return s + "--" + IDTail(projectID)
}

// IDTail is the last six letters and digits of an id, lower-cased.
func IDTail(id string) string {
	var b []byte
	for i := 0; i < len(id); i++ {
		c := id[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b = append(b, c)
		}
	}
	if len(b) > 6 {
		b = b[len(b)-6:]
	}
	for len(b) < 6 {
		b = append(b, '0')
	}
	return string(b)
}

func nonce() string {
	var b [6]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func lstatDir(p string) (exists bool, err error) {
	fi, err := os.Lstat(p)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if fi.Mode()&os.ModeSymlink != 0 || !fi.IsDir() {
		return true, fmt.Errorf("%w: %s is not a plain folder", ErrUnsafe, filepath.Base(p))
	}
	return true, nil
}

// FindDir returns the folder name already used for a project under root (found
// by the tail of its id), or "" when there is none.
func FindDir(root, projectID string) (string, error) {
	ents, err := os.ReadDir(root)
	if errors.Is(err, fs.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	suffix := "--" + IDTail(projectID)
	for _, e := range ents { // ReadDir is sorted
		if e.IsDir() && e.Type()&os.ModeSymlink == 0 && strings.HasSuffix(e.Name(), suffix) {
			if m, err := readManaged(filepath.Join(root, e.Name(), "managed.json")); err == nil && m.ProjectID == projectID {
				return e.Name(), nil
			}
		}
	}
	return "", nil
}

func readManaged(p string) (*Managed, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var m Managed
	if err := json.NewDecoder(io.LimitReader(f, 64<<20)).Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// safeRel reports whether a published path is a plain relative file path.
func safeRel(p string) bool {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") || strings.ContainsRune(p, 0) || len(p) > 220 {
		return false
	}
	for _, s := range strings.Split(p, "/") {
		if s == "" || s == "." || s == ".." || strings.HasPrefix(s, ".") {
			return false
		}
	}
	return true
}

// PublishResult says what a publish did.
type PublishResult struct {
	Name     string // the folder under the root
	Files    int
	Bytes    int
	Replaced bool // a previous site was replaced
}

// WriteDir publishes files to <root>/<name>/public. name must be a plain folder
// name; use FindDir first to keep one that exists.
func WriteDir(root, name, projectID, hash string, files []File) (*PublishResult, error) {
	return WriteDirWith(root, name, projectID, hash, files, DirOptions{})
}

// WriteDirWith is WriteDir with options.
func WriteDirWith(root, name, projectID, hash string, files []File, o DirOptions) (*PublishResult, error) {
	if name == "" || strings.ContainsAny(name, `/\`) || strings.HasPrefix(name, ".") || strings.ContainsRune(name, 0) {
		return nil, fmt.Errorf("%w: bad folder name", ErrUnsafe)
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return nil, err
	}
	real, err := filepath.EvalSymlinks(root) // the administrator's own choice: a symlinked root is theirs
	if err != nil {
		return nil, err
	}
	proj := filepath.Join(real, name)
	if ok, err := lstatDir(proj); err != nil {
		return nil, err
	} else if !ok {
		if err := os.Mkdir(proj, 0o755); err != nil {
			return nil, err
		}
	}
	_ = os.Chmod(proj, 0o755)
	cleanLeftovers(proj)
	pub := filepath.Join(proj, "public")
	exists, err := lstatDir(pub)
	if err != nil {
		return nil, err
	}
	if exists {
		if err := checkManaged(proj, pub, projectID, !o.OverwriteEdited); err != nil {
			return nil, err
		}
	}
	for _, f := range files {
		if !safeRel(f.Path) {
			return nil, fmt.Errorf("%w: path %q", ErrUnsafe, f.Path)
		}
	}
	tmp := filepath.Join(proj, ".public.tmp-"+nonce())
	if err := os.Mkdir(tmp, 0o755); err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(tmp)
		}
	}()
	m := Managed{ProjectID: projectID, Hash: hash}
	total := 0
	for _, f := range files {
		dst := filepath.Join(tmp, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return nil, err
		}
		if err := writeNew(dst, f.Data); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(f.Data)
		m.Files = append(m.Files, ManagedFile{Path: f.Path, SHA256: hex.EncodeToString(sum[:]), Bytes: len(f.Data)})
		total += len(f.Data)
	}
	sort.Slice(m.Files, func(i, j int) bool { return m.Files[i].Path < m.Files[j].Path })
	// Directories made by MkdirAll honour the umask: make them what a server needs.
	_ = filepath.WalkDir(tmp, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() {
			_ = os.Chmod(p, 0o755)
		}
		return nil
	})
	mj, _ := json.MarshalIndent(m, "", " ")
	newManifest := filepath.Join(proj, "managed.json.new")
	if err := writeNew2(newManifest, mj); err != nil {
		return nil, err
	}
	var old string
	if exists {
		old = filepath.Join(proj, ".public.old-"+nonce())
		if err := os.Rename(pub, old); err != nil {
			_ = os.Remove(newManifest)
			return nil, err
		}
	}
	if err := os.Rename(tmp, pub); err != nil {
		if old != "" {
			_ = os.Rename(old, pub) // put the old site back
		}
		_ = os.Remove(newManifest)
		return nil, err
	}
	cleanup = false
	if err := os.Rename(newManifest, filepath.Join(proj, "managed.json")); err != nil {
		return nil, err
	}
	if old != "" {
		_ = os.RemoveAll(old)
	}
	syncDir(proj)
	return &PublishResult{Name: name, Files: len(files), Bytes: total, Replaced: exists}, nil
}

func writeNew(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	_ = f.Sync()
	if err := f.Close(); err != nil {
		return err
	}
	return os.Chmod(path, 0o644)
}

// writeNew2 writes a file that may be left over from a failed publish.
func writeNew2(path string, data []byte) error {
	_ = os.Remove(path)
	return writeNew(path, data)
}

func syncDir(p string) {
	if d, err := os.Open(p); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

// cleanLeftovers removes the temporary folders a crashed publish left, by their
// own names only.
func cleanLeftovers(proj string) {
	ents, err := os.ReadDir(proj)
	if err != nil {
		return
	}
	for _, e := range ents {
		n := e.Name()
		if (strings.HasPrefix(n, ".public.tmp-") || strings.HasPrefix(n, ".public.old-")) && e.IsDir() && e.Type()&os.ModeSymlink == 0 {
			_ = os.RemoveAll(filepath.Join(proj, n))
		}
	}
}

// checkManaged verifies that everything in pub is a regular file Scríobh wrote
// (listed in the managed record), and that there is no symlink or special file.
func checkManaged(proj, pub, projectID string, refuseEdited bool) error {
	known := map[string]map[string]bool{} // path: the hashes Scríobh wrote there
	found := false
	for _, name := range []string{"managed.json", "managed.json.new"} {
		m, err := readManaged(filepath.Join(proj, name))
		if err != nil {
			continue
		}
		if m.ProjectID != projectID {
			return fmt.Errorf("%w: this folder belongs to another project", ErrUnsafe)
		}
		found = true
		for _, f := range m.Files {
			if known[f.Path] == nil {
				known[f.Path] = map[string]bool{}
			}
			known[f.Path][f.SHA256] = true
		}
	}
	var unmanaged, edited []string
	err := filepath.WalkDir(pub, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if p == pub {
			return nil
		}
		rel, _ := filepath.Rel(pub, p)
		rel = filepath.ToSlash(rel)
		if d.Type()&os.ModeSymlink != 0 || (!d.IsDir() && !d.Type().IsRegular()) {
			return fmt.Errorf("%w: %s is a link or a special file", ErrUnsafe, rel)
		}
		if d.IsDir() {
			return nil
		}
		if !found || known[rel] == nil {
			unmanaged = append(unmanaged, rel)
			return nil
		}
		if refuseEdited {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(b)
			if !known[rel][hex.EncodeToString(sum[:])] {
				edited = append(edited, rel)
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if len(unmanaged) > 0 {
		sort.Strings(unmanaged)
		return &ErrUnmanaged{Paths: unmanaged}
	}
	if len(edited) > 0 {
		sort.Strings(edited)
		return &ErrEdited{Paths: edited}
	}
	return nil
}

// RemoveDir removes a project's published site: the public folder (only when it
// holds nothing but what was written), the record and the leftovers, then the
// project's folder if it is empty. It is for a project that is deleted for
// good. Anything it does not recognise is left where it is.
func RemoveDir(root, name, projectID string) error {
	real, err := filepath.EvalSymlinks(root)
	if err != nil {
		return nil // no root: nothing was published
	}
	proj := filepath.Join(real, name)
	if ok, err := lstatDir(proj); err != nil || !ok {
		return err
	}
	m, err := readManaged(filepath.Join(proj, "managed.json"))
	if err != nil || m.ProjectID != projectID {
		return nil
	}
	cleanLeftovers(proj)
	pub := filepath.Join(proj, "public")
	if ok, err := lstatDir(pub); err != nil {
		return err
	} else if ok {
		if err := checkManaged(proj, pub, projectID, false); err != nil {
			return err
		}
		if err := os.RemoveAll(pub); err != nil {
			return err
		}
	}
	_ = os.Remove(filepath.Join(proj, "managed.json"))
	_ = os.Remove(filepath.Join(proj, "managed.json.new"))
	_ = os.Remove(proj) // only if empty
	return nil
}
