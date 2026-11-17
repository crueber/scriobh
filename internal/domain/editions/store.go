package editions

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/ops"
)

// The source file of an edition lives beside the project's attachments but not
// among them:
//
//	<data>/files/projects/<project id>/editions/<sha256>.<ext>
//
// It is not an attachment because an attachment is something the text links to,
// is served to anyone who can read the project, and is copied into the mirror and
// the Git push. The source file is none of those: only the project's owner and
// editors can download it (edition.download), and no export, mirror or push
// carries its bytes. It sits under the project's folder, so purging the project
// removes it with the attachments (attachments' purge hook removes the whole
// folder), a backup carries it with the files tree, and a project in the trash
// keeps it.

// editionLocks serialise placing a file and taking one away, held from before the
// import's transaction until it ends, as the attachments' import holds its own:
// an import that fails removes the file it placed, and must never remove one a
// concurrent import of the same bytes has just made live. What must not overlap is
// two imports of the same bytes into the same project, so the lock is one of 64
// chosen by that pair: imports of different books do not wait for each other.
var editionLocks [64]sync.Mutex

func lockFor(project, sha string) *sync.Mutex {
	h := fnv.New32a()
	h.Write([]byte(project))
	h.Write([]byte{0})
	h.Write([]byte(sha))
	return &editionLocks[h.Sum32()%uint32(len(editionLocks))]
}

// Path is where a source file is kept.
func Path(dataDir, projectID, sha, ext string) (string, error) {
	if dataDir == "" || !db.ValidID(projectID) || !shaRe.MatchString(sha) {
		return "", errors.New("editions: bad file name")
	}
	switch ext {
	case "epub", "docx", "pdf", "zip":
	default:
		return "", errors.New("editions: bad file extension")
	}
	return filepath.Join(dataDir, "files", "projects", projectID, "editions", sha+"."+ext), nil
}

func tmpDir(dataDir string) string { return filepath.Join(dataDir, "files", ".tmp") }

// Staged is a source file made ready to keep: checked against the limits and
// written to a temporary file, before the import's transaction opens.
type Staged struct {
	env     *ops.Env
	project string
	dry     bool
	locked  bool
	mu      *sync.Mutex
	tmp     string
	final   string
	placed  bool
	// SHA256 and Size are the file's, as they will be recorded.
	SHA256 string
	Size   int64
	// Existing says the project already keeps these bytes: nothing new is
	// stored and nothing more is counted.
	Existing bool
	// counted says a row of the project already counts these bytes in its usage (the file may be missing from
	// the disk, as after a backup made without files: it is then put back, and not counted again).
	counted bool
	Ext     string
}

// Stage checks a source file against the size limit, the number of editions the
// project holds and the quotas (the project's, the person's, the server's: the
// same counters attachments use), and, unless dry, writes it to a temporary
// file. projectID may be a project that does not exist yet (an import's new
// project); its quota is then zero used. The caller must call Done.
func Stage(ctx context.Context, env *ops.Env, lim attachments.Limits, userID, projectID string, data []byte, ext string, dry bool) (*Staged, error) {
	if len(data) == 0 {
		return nil, ops.Invalid("file", "The file is empty.")
	}
	if len(data) > MaxFileBytes {
		return nil, &ops.Error{Code: ops.CodeTooLarge, Message: fmt.Sprintf("A source file of more than %d MB is not kept.", MaxFileBytes>>20),
			Data: map[string]any{"max_file_bytes": MaxFileBytes}}
	}
	sum := sha256.Sum256(data)
	s := &Staged{env: env, project: projectID, dry: dry, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data)), Ext: ext}
	final, err := Path(env.DataDir, projectID, s.SHA256, ext)
	if err != nil {
		return nil, ops.Internal(err)
	}
	s.final = final
	var have, same int
	if err := env.DB.Read().QueryRowContext(ctx, `SELECT count(*), COALESCE(SUM(CASE WHEN sha256 = ? AND has_file = 1 THEN 1 ELSE 0 END), 0) FROM editions WHERE project_id = ?`, s.SHA256, projectID).Scan(&have, &same); err != nil {
		return nil, ops.Internal(err) // a limit that cannot be read is not a limit that is met
	}
	if have >= MaxPerProject {
		return nil, ops.Conflict(fmt.Sprintf("This project already holds %d editions, which is the most it keeps.", MaxPerProject))
	}
	s.counted = same > 0
	s.Existing = same > 0
	if s.Existing {
		if fi, err := os.Lstat(final); err != nil || !fi.Mode().IsRegular() {
			s.Existing = false // the row is there and the file is not: this import puts it back
		}
	}
	if !s.counted {
		for _, q := range quotaScopes(lim, userID, projectID) {
			if q.limit <= 0 {
				continue
			}
			used, err := usageOf(ctx, env.DB.Read(), q.scope)
			if err != nil {
				return nil, ops.Internal(err)
			}
			if used+s.Size > q.limit {
				return nil, quotaRefusal(q.what, q.limit, used, s.Size)
			}
		}
	}
	if dry || s.Existing {
		return s, nil
	}
	dir := tmpDir(env.DataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, ops.Internal(err)
	}
	f, err := os.CreateTemp(dir, "edn-*")
	if err != nil {
		return nil, ops.Internal(err)
	}
	s.tmp = f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		s.Done(false)
		return nil, ops.Internal(err)
	}
	return s, nil
}

// usageOf is the bytes a scope holds; none is zero, and an error is an error (a quota that cannot be read fails closed).
func usageOf(ctx context.Context, q Querier, scope string) (int64, error) {
	var used int64
	err := q.QueryRowContext(ctx, `SELECT bytes FROM file_usage WHERE scope = ?`, scope).Scan(&used)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return used, err
}

type quotaScope struct {
	scope string
	limit int64
	what  string
}

func quotaScopes(l attachments.Limits, userID, projectID string) []quotaScope {
	return []quotaScope{{"owner:" + projectID, l.OwnerBytes, "this project"}, {"user:" + userID, l.UserBytes, "you"}, {"instance", l.InstanceBytes, "this server"}}
}

func quotaRefusal(who string, limit, used, add int64) error {
	return &ops.Error{Code: ops.CodeQuota,
		Message: fmt.Sprintf("Keeping the source file (%.1f MB) would take %s over the storage limit. Make room first.", float64(add)/1e6, who),
		Data:    map[string]any{"limit_bytes": limit, "used_bytes": used, "file_bytes": add}}
}

// Lock takes the lock an import holds from before its transaction until Done.
func (s *Staged) Lock() {
	if s != nil && !s.locked && !s.dry {
		s.mu = lockFor(s.project, s.SHA256)
		s.mu.Lock()
		s.locked = true
	}
}

// Place, inside the import's transaction and before Insert, checks the quotas
// again against the counters (so two imports cannot both squeeze under one) and
// moves the file into place. It does nothing for a dry run or for bytes the
// project already keeps.
func (s *Staged) Place(c *ops.Ctx, tx *sql.Tx, lim attachments.Limits) error {
	if s == nil || s.Existing {
		return nil
	}
	if !s.dry && !s.locked {
		return errors.New("editions: Place without Lock")
	}
	for _, q := range quotaScopes(lim, c.P.UserID, s.project) {
		if q.limit <= 0 || s.counted {
			continue
		}
		used, err := usageOf(c, tx, q.scope)
		if err != nil {
			return ops.Internal(err)
		}
		if used+s.Size > q.limit {
			return quotaRefusal(q.what, q.limit, used, s.Size)
		}
	}
	if s.dry {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(s.final), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(s.final); errors.Is(err, os.ErrNotExist) {
		if err := os.Rename(s.tmp, s.final); err != nil {
			return err
		}
		s.placed = true
	}
	return nil
}

// Done ends the staging: the temporary file goes, the lock is released, and,
// when the transaction did not commit, the file this import placed is removed
// (one that was there before is never touched).
func (s *Staged) Done(committed bool) {
	if s == nil {
		return
	}
	if s.tmp != "" {
		_ = os.Remove(s.tmp)
	}
	if !committed && s.placed {
		_ = os.Remove(s.final)
	}
	s.placed = false
	if s.locked {
		s.locked = false
		s.mu.Unlock()
	}
}

// ErrNoFile says the edition's source file is not kept, or is not there.
var ErrNoFile = errors.New("editions: the source file is not available")

// Open reads an edition's source file, intact: it must be a regular file under
// data/files with no link in its path, of the recorded size and hash. Anything
// else (missing, replaced, damaged) is ErrNoFile.
func Open(env *ops.Env, e Edition) ([]byte, error) {
	if !e.HasFile {
		return nil, ErrNoFile
	}
	ext := e.Kind
	if e.Kind == KindMarkdown {
		ext = "zip"
	}
	p, err := Path(env.DataDir, e.ProjectID, e.SHA256, ext)
	if err != nil {
		return nil, ErrNoFile
	}
	f, _, err := attachments.OpenRegular(env.DataDir, p)
	if err != nil {
		return nil, ErrNoFile
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, e.Size+1))
	if err != nil || int64(len(b)) != e.Size {
		return nil, ErrNoFile
	}
	if SumOf(b) != e.SHA256 {
		return nil, ErrNoFile
	}
	return b, nil
}

// Present says whether an edition's source file is still on disk: a regular file of the recorded size under data/files
// with no link in its path. It does not read the file, so it is cheap enough for a page; Open still checks the hash, and
// a file that passes here and fails there is the one case the download itself has to answer.
func Present(env *ops.Env, e Edition) bool {
	if !e.HasFile {
		return false
	}
	ext := e.Kind
	if e.Kind == KindMarkdown {
		ext = "zip"
	}
	p, err := Path(env.DataDir, e.ProjectID, e.SHA256, ext)
	if err != nil {
		return false
	}
	f, fi, err := attachments.OpenRegular(env.DataDir, p)
	if err != nil {
		return false
	}
	f.Close()
	return fi.Size() == e.Size
}

// LockIndex is which of the import locks a project and a file hash take.
func LockIndex(project, sha string) int {
	m := lockFor(project, sha)
	for i := range editionLocks {
		if &editionLocks[i] == m {
			return i
		}
	}
	return -1
}
