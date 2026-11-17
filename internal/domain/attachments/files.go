package attachments

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// Where the bytes live, all of it derived from ids and hashes the server made,
// never from anything a client sent:
//
//	<data>/files/projects/<project id>/attachments/<sha256>.<ext>
//	<data>/files/compendiums/<compendium id>/attachments/<sha256>.<ext>
//	<data>/files/.tmp/                      uploads in flight, swept at start
//
// Nothing here is a directory listing a route can reach: files are served by
// attachment id, through an authorized route.

var (
	shaRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
	extRe = regexp.MustCompile(`^(png|jpg|gif|webp|pdf|txt|md)$`)

	// fileMu serialises the short critical sections that put a blob in place and
	// take one away, so a delete's cleanup can never remove a file a concurrent
	// upload of the same bytes has just made live again. Uploads stream and hash
	// outside it.
	fileMu sync.Mutex
)

// ownerDir is the folder of everything stored for one owner.
func ownerDir(dataDir, ownerKind, ownerID string) (string, error) {
	if dataDir == "" || !db.ValidID(ownerID) {
		return "", errors.New("attachments: bad owner")
	}
	switch ownerKind {
	case OwnerProject:
		return filepath.Join(dataDir, "files", "projects", ownerID), nil
	case OwnerCompendium:
		return filepath.Join(dataDir, "files", "compendiums", ownerID), nil
	}
	return "", errors.New("attachments: bad owner kind")
}

// BlobPath is where an attachment's bytes are kept.
func BlobPath(dataDir, ownerKind, ownerID, sha, ext string) (string, error) {
	if !shaRe.MatchString(sha) || !extRe.MatchString(ext) {
		return "", errors.New("attachments: bad file name")
	}
	d, err := ownerDir(dataDir, ownerKind, ownerID)
	if err != nil {
		return "", err
	}
	return filepath.Join(d, "attachments", sha+"."+ext), nil
}

// OpenRegular opens a stored file for reading and nothing else: the path must
// have no symbolic link in it anywhere below data/files, the file must be a
// regular file, and what was opened must be what was checked (a link planted
// between the check and the open is caught by comparing the two). A file
// planted in the folder by hand, or a link to something elsewhere, is refused as
// if it were missing.
func OpenRegular(dataDir, path string) (*os.File, os.FileInfo, error) {
	root, err := filepath.EvalSymlinks(filepath.Join(dataDir, "files"))
	if err != nil {
		return nil, nil, err
	}
	rel, err := filepath.Rel(filepath.Join(dataDir, "files"), path)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return nil, nil, errors.New("attachments: outside the files folder")
	}
	want := filepath.Join(root, rel)
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, nil, err
	}
	if real != want {
		return nil, nil, errors.New("attachments: a link in the path")
	}
	li, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !li.Mode().IsRegular() {
		return nil, nil, errors.New("attachments: not a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || !os.SameFile(li, fi) {
		f.Close()
		return nil, nil, errors.New("attachments: the file changed while it was opened")
	}
	return f, fi, nil
}

func tmpDir(dataDir string) string { return filepath.Join(dataDir, "files", ".tmp") }

// SweepTemp removes uploads that were in flight when the process stopped.
func SweepTemp(dataDir string) {
	ents, err := os.ReadDir(tmpDir(dataDir))
	if err != nil {
		return
	}
	for _, e := range ents {
		_ = os.Remove(filepath.Join(tmpDir(dataDir), e.Name()))
	}
}

// removeBlob deletes a stored file after the row that named it is gone. It
// checks, under the same lock uploads take, that no row names it again.
func removeBlob(ctx context.Context, env *ops.Env, ownerKind, ownerID, sha, ext string) {
	p, err := BlobPath(env.DataDir, ownerKind, ownerID, sha, ext)
	if err != nil {
		return
	}
	fileMu.Lock()
	defer fileMu.Unlock()
	col := "project_id"
	if ownerKind == OwnerCompendium {
		col = "compendium_id"
	}
	var n int
	if err := env.DB.Read().QueryRowContext(ctx, `SELECT count(*) FROM attachments WHERE `+col+` = ? AND sha256 = ?`, ownerID, sha).Scan(&n); err != nil || n > 0 {
		return
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) && env.Log != nil {
		env.Log.Warn("attachment file not removed", "err", err)
	}
}

// purgeOwner removes everything stored for an owner that has been purged.
func purgeOwner(dataDir, ownerKind string) func(ctx context.Context, id string) {
	return func(_ context.Context, id string) {
		d, err := ownerDir(dataDir, ownerKind, id)
		if err != nil {
			return
		}
		fileMu.Lock()
		defer fileMu.Unlock()
		_ = os.RemoveAll(d)
	}
}
