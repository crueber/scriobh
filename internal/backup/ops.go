package backup

import (
	"errors"
	"os"

	"github.com/crueber/scriobh/internal/ops"
)

// ServiceName is the name the service is registered under in ops.Env.
const ServiceName = "backup"

// MaxDownloadBytes caps backup.download: an op's file is held in memory. A
// bigger archive is on disk in data/backups and is copied from there.
const MaxDownloadBytes = 256 << 20

// ListIn lists backups.
type ListIn struct{}

// ListOut is the listing.
type ListOut struct {
	Items []Item `json:"items" desc:"Newest first. Only archives in data/backups with a generated name are listed."`
}

// CreateIn takes a backup now.
type CreateIn struct{}

// CreateOut describes the backup written.
type CreateOut struct {
	Item     Item   `json:"item"`
	SHA256   string `json:"sha256"`
	Projects int    `json:"projects"`
	Docs     int    `json:"documents"`
}

// DownloadIn names a backup by id.
type DownloadIn struct {
	ID string `path:"id" json:"-" desc:"A backup id from backup.list."`
}

// File is the archive. Over REST it is the file itself; elsewhere it is this
// JSON with the archive base64-encoded in body.
type File struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Bytes       int    `json:"bytes"`
	Body        []byte `json:"body" desc:"The archive. The REST route returns it as the response body instead of JSON."`
}

// Download makes File an ops.Download.
func (f File) Download() (string, string, []byte) { return f.Filename, f.ContentType, f.Body }

// Register adds the backup ops. The service is attached to the environment by
// the server (ops.Env.SetService).
func Register(r *ops.Registry) {
	ops.Register(r, ops.Op{
		Name: "backup.list", Method: "GET", Path: "/api/v1/admin/backups",
		Desc: "List the backups in the data directory, newest first: manual, scheduled and pre-restore. Administrators only.",
		Auth: ops.Admin(),
	}, list)
	ops.Register(r, ops.Op{
		Name: "backup.create", Method: "POST", Path: "/api/v1/admin/backups", Status: 201,
		Desc: "Take a backup now: a consistent archive of the database, files, mirror and conflict copies, written to data/backups. " +
			"It never holds the secret key. Works while the server runs. Administrators only.",
		Auth: ops.Admin(), NoDryRun: true, // writes an archive
	}, create)
	ops.Register(r, ops.Op{
		Name: "backup.download", Method: "GET", Path: "/api/v1/admin/backups/{id}",
		Desc: "Download one backup by the id backup.list gave. Never takes a path. Archives over 256 MB are not served here; copy them from data/backups. Administrators only.",
		Auth: ops.Admin(),
	}, download)
}

func service(c *ops.Ctx) (*Service, error) {
	s, ok := ops.Service[*Service](c.Env, ServiceName)
	if !ok || s == nil {
		return nil, ops.Conflict("Backups are not available on this server.")
	}
	return s, nil
}

func list(c *ops.Ctx, _ ListIn) (ListOut, error) {
	s, err := service(c)
	if err != nil {
		return ListOut{}, err
	}
	items, err := s.List()
	if err != nil {
		return ListOut{}, ops.Internal(err)
	}
	return ListOut{Items: items}, nil
}

func create(c *ops.Ctx, _ CreateIn) (CreateOut, error) {
	s, err := service(c)
	if err != nil {
		return CreateOut{}, err
	}
	r, err := s.Create(c, "")
	if err != nil {
		return CreateOut{}, ops.Internal(err)
	}
	id := r.Name[:len(r.Name)-len(".zip")]
	c.Audit(ops.AuditEntry{Action: "backup.create", TargetType: "backup", TargetID: id,
		Detail: map[string]any{"bytes": r.Bytes, "projects": r.Manifest.Counts.Projects, "documents": r.Manifest.Counts.Documents}})
	return CreateOut{Item: Item{ID: id, Kind: "manual", CreatedAt: r.CreatedAt, Bytes: r.Bytes}, SHA256: r.SHA256,
		Projects: r.Manifest.Counts.Projects, Docs: r.Manifest.Counts.Documents}, nil
}

func download(c *ops.Ctx, in DownloadIn) (File, error) {
	s, err := service(c)
	if err != nil {
		return File{}, err
	}
	if !ValidID(in.ID) {
		return File{}, ops.Invalid("id", "That is not a backup id. Use one from the backup list.")
	}
	p, err := s.Path(in.ID)
	if errors.Is(err, ErrNoBackup) {
		return File{}, ops.NotFound("backup")
	}
	if err != nil {
		return File{}, ops.Internal(err)
	}
	fi, err := os.Stat(p)
	if err != nil {
		return File{}, ops.NotFound("backup")
	}
	if fi.Size() > MaxDownloadBytes {
		return File{}, ops.Invalid("id", "This backup is too large to download here. Copy it from the backups folder of the data directory.")
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return File{}, ops.Internal(err)
	}
	return File{Filename: in.ID + ".zip", ContentType: "application/zip", Bytes: len(b), Body: b}, nil
}
