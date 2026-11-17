package attachments

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/imagesafe"
	"github.com/crueber/scriobh/internal/ops"
)

// Pictures an import brings (a Scrivener project's Files/Data, the images
// folder in a zip of a Ghost or WordPress export). They go through the same
// type policy as an upload, and are cleaned first: a picture that leaves a
// writer's old program carries where it was taken and what took it, and none of
// that belongs in the project. The importer stores them in its own transaction,
// so a picture and the documents that show it are created together or not at all.
//
//	PreparePicture   before anything else: sniff, clean, check, hash (no I/O)
//	NewImport        before the transaction: ids for the new ones, the ones the
//	                 project already holds, the size limit, the quota, temp files
//	LockFiles        held across the importer's transaction, as an upload holds it
//	Import.Commit    inside it: rows, files moved into place, events
//	Import.Done      after it: temp files removed, and on failure what was placed

// ServiceLimits names the configured limits in the op environment.
const ServiceLimits = "attachments.limits"

// LimitsOf returns the limits configured for the environment, or the defaults.
func LimitsOf(env *ops.Env) Limits {
	if l, ok := ops.Service[Limits](env, ServiceLimits); ok {
		return l
	}
	return DefaultLimits
}

// Picture is a cleaned picture ready to be stored.
type Picture struct {
	Name          string // a display name, cleaned
	Data          []byte // the cleaned bytes
	SHA256        string
	Ext, Mime     string
	Width, Height int
	// Orientation is the EXIF orientation the original had; Rotated says the
	// pixels were turned to match it before the EXIF was dropped.
	Orientation int
	Rotated     bool
}

// SniffPicture says which picture type the bytes are, by their structure: png,
// jpg, gif or webp, or "" (SVG, TIFF, HEIC and everything else are not kept).
func SniffPicture(b []byte) string {
	h := b[:min(len(b), HeadSize)]
	switch {
	case isPNG(h):
		return "png"
	case isJPEG(h):
		return "jpg"
	case isGIF(h):
		return "gif"
	case isWebP(h, int64(len(b))):
		return "webp"
	}
	return ""
}

// PreparePicture checks and cleans a picture from an import. The error says,
// in a few words, why it is not kept.
func PreparePicture(name string, data []byte, maxBytes int64) (Picture, error) {
	if len(data) == 0 {
		return Picture{}, errors.New("empty")
	}
	if maxBytes > 0 && int64(len(data)) > maxBytes {
		return Picture{}, errors.New("too large")
	}
	ext := SniffPicture(data)
	if ext == "" {
		return Picture{}, errors.New("not a PNG, JPEG, GIF or WebP picture")
	}
	clean, info, err := imagesafe.Clean(ext, data)
	if err != nil {
		if errors.Is(err, imagesafe.ErrTooLarge) {
			return Picture{}, errors.New("too many pixels")
		}
		return Picture{}, errors.New("damaged")
	}
	if maxBytes > 0 && int64(len(clean)) > maxBytes {
		return Picture{}, errors.New("too large")
	}
	// The cleaned bytes pass the upload's own check, as if they had been uploaded.
	k, err := detect(clean[:min(len(clean), HeadSize)], int64(len(clean)), "picture."+ext, "")
	if err != nil || k.Kind != "image" {
		return Picture{}, errors.New("not kept after cleaning")
	}
	if info.Width <= 0 || info.Height <= 0 || int64(info.Width)*int64(info.Height) > MaxPixels {
		return Picture{}, errors.New("too many pixels")
	}
	sum := sha256.Sum256(clean)
	return Picture{Name: displayName(name, k), Data: clean, SHA256: hex.EncodeToString(sum[:]), Ext: k.Ext, Mime: k.Mime,
		Width: info.Width, Height: info.Height, Orientation: info.Orientation, Rotated: info.Rotated}, nil
}

// Import is the pictures of one import into one project.
type Import struct {
	env     *ops.Env
	owner   string
	dry     bool
	items   []*importItem
	placed  []string
	locked  bool
	newSize int64
}

type importItem struct {
	pic      Picture
	id       string
	existing bool   // the project already holds these bytes, under id
	tmp      string // the staged file of a new one
	final    string
}

// ImportedPicture says where one picture ended up.
type ImportedPicture struct {
	ID       string
	URL      string
	Existing bool // already in the project: no new file
}

// NewImport gives every picture its id (a new one, or the attachment the
// project already holds with the same bytes), checks the quotas, and, unless
// it is a dry run, writes the new ones to temporary files. projectID may be a
// project that does not exist yet (a new project's id).
func NewImport(ctx context.Context, env *ops.Env, l Limits, userID, projectID string, pics []Picture, dry bool) (*Import, error) {
	im := &Import{env: env, owner: projectID, dry: dry}
	seen := map[string]*importItem{}
	for _, p := range pics {
		if it, ok := seen[p.SHA256]; ok {
			im.items = append(im.items, it)
			continue
		}
		it := &importItem{pic: p}
		var id string
		err := env.DB.Read().QueryRowContext(ctx, `SELECT id FROM attachments WHERE project_id = ? AND sha256 = ?`, projectID, p.SHA256).Scan(&id)
		switch {
		case err == nil:
			it.id, it.existing = id, true
		case errors.Is(err, sql.ErrNoRows):
			it.id = db.NewID("att")
			im.newSize += int64(len(p.Data))
		default:
			return nil, ops.Internal(err)
		}
		seen[p.SHA256] = it
		im.items = append(im.items, it)
	}
	for _, q := range []struct {
		scope string
		limit int64
		what  string
	}{{"owner:" + projectID, l.OwnerBytes, "this project"}, {"user:" + userID, l.UserBytes, "you"}, {"instance", l.InstanceBytes, "this server"}} {
		if q.limit <= 0 || im.newSize == 0 {
			continue
		}
		var used int64
		_ = env.DB.Read().QueryRowContext(ctx, `SELECT bytes FROM file_usage WHERE scope = ?`, q.scope).Scan(&used)
		if used+im.newSize > q.limit {
			return nil, quotaRefusal(q.what, q.limit, used, im.newSize)
		}
	}
	if dry {
		return im, nil
	}
	dir := tmpDir(env.DataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, ops.Internal(err)
	}
	for _, it := range im.items {
		if it.existing || it.tmp != "" {
			continue
		}
		final, err := BlobPath(env.DataDir, OwnerProject, projectID, it.pic.SHA256, it.pic.Ext)
		if err != nil {
			im.Done(false)
			return nil, ops.Internal(err)
		}
		f, err := os.CreateTemp(dir, "imp-*")
		if err != nil {
			im.Done(false)
			return nil, ops.Internal(err)
		}
		it.tmp, it.final = f.Name(), final
		_, err = f.Write(it.pic.Data)
		if err == nil {
			err = f.Sync()
		}
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			im.Done(false)
			return nil, ops.Internal(err)
		}
	}
	return im, nil
}

func quotaRefusal(who string, limit, used, add int64) error {
	return &ops.Error{Code: ops.CodeQuota,
		Message: fmt.Sprintf("The pictures in this import (%.1f MB) would take %s over the storage limit. Import again with pictures left out, or make room first.", float64(add)/1e6, who),
		Data:    map[string]any{"limit_bytes": limit, "used_bytes": used, "pictures_bytes": add}}
}

// Pictures says where each picture of NewImport's list goes, in its order.
func (im *Import) Pictures() []ImportedPicture {
	out := make([]ImportedPicture, len(im.items))
	for i, it := range im.items {
		out[i] = ImportedPicture{ID: it.id, URL: "/files/" + it.id, Existing: it.existing}
	}
	return out
}

// NewBytes is what the new pictures add to the project.
func (im *Import) NewBytes() int64 { return im.newSize }

// LockFiles takes the lock uploads and deletions take around placing and
// removing files. An import holds it across its whole transaction, taken
// before the transaction (the order an upload takes them in).
func (im *Import) LockFiles() {
	if !im.locked && !im.dry { // a dry run places nothing, and holds the writer already
		fileMu.Lock()
		im.locked = true
	}
}

// Commit writes the rows in the importer's transaction and moves the new files
// into place. The attachment rows must exist before the documents that show
// them, so their references are recorded.
func (im *Import) Commit(c *ops.Ctx, tx *sql.Tx, l Limits) error {
	if !im.locked && !im.dry {
		return errors.New("attachments: Commit without LockFiles")
	}
	done := map[string]bool{}
	var add int64
	for _, it := range im.items {
		if done[it.id] {
			continue
		}
		done[it.id] = true
		if it.existing {
			var n int
			if err := tx.QueryRow(`SELECT count(*) FROM attachments WHERE id = ? AND project_id = ?`, it.id, im.owner).Scan(&n); err != nil {
				return err
			}
			if n == 0 {
				return ops.Conflict("A picture in this project changed while it was being imported. Try again.")
			}
			continue
		}
		add += int64(len(it.pic.Data))
	}
	if err := checkQuota(c, tx, l, OwnerProject, im.owner, add); err != nil {
		return err
	}
	now := db.Now()
	done = map[string]bool{}
	for _, it := range im.items {
		if it.existing || done[it.id] {
			continue
		}
		done[it.id] = true
		if !im.dry {
			if err := os.MkdirAll(filepath.Dir(it.final), 0o700); err != nil {
				return err
			}
			if _, err := os.Stat(it.final); errors.Is(err, os.ErrNotExist) {
				if err := os.Rename(it.tmp, it.final); err != nil {
					return err
				}
				im.placed = append(im.placed, it.final)
			}
		}
		if _, err := tx.Exec(`INSERT INTO attachments(id, project_id, compendium_id, sha256, bytes, mime, ext, kind, filename, alt, width, height, created_by, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			it.id, im.owner, nil, it.pic.SHA256, len(it.pic.Data), it.pic.Mime, it.pic.Ext, "image", it.pic.Name, "", it.pic.Width, it.pic.Height, nullIfEmpty(c.P.UserID), now, now); err != nil {
			if db.IsUniqueViolation(err) {
				return ops.Conflict("A picture in this project changed while it was being imported. Try again.")
			}
			return err
		}
		c.Emit(ops.Event{Type: "attachment.created", ProjectID: im.owner, Payload: map[string]any{"attachment_id": it.id, "kind": "image", "bytes": len(it.pic.Data), "imported": true}})
	}
	if len(done) > 0 {
		c.Audit(ops.AuditEntry{Action: "attachment.import", TargetType: "project", TargetID: im.owner, Detail: map[string]any{"pictures": len(done), "bytes": add}})
	}
	return nil
}

// Done ends the import: temporary files go, the lock is released, and, when the
// transaction did not commit, the files this import placed are removed (a file
// that was there before it is never touched).
func (im *Import) Done(committed bool) {
	if im == nil {
		return
	}
	for _, it := range im.items {
		if it.tmp != "" {
			_ = os.Remove(it.tmp)
		}
	}
	if !committed {
		for _, p := range im.placed {
			_ = os.Remove(p)
		}
	}
	im.placed = nil
	if im.locked {
		im.locked = false
		fileMu.Unlock()
	}
}
