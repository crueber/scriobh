// Package attachments is the files a project or a Compendium keeps: images,
// PDFs and text, content-addressed on disk, deduplicated per owner, counted
// against quotas, and served only through an authorized route.
//
// Every op is registered twice, once under a project (project.attachment.*) and
// once under a Compendium (compendium.attachment.*), because an op declares one
// kind of authorization. Both registrations share these handlers, which scope
// every query by the owner the registry already authorized, so an attachment id
// from another owner is simply not found.
//
// The upload is the one op whose body is a file. It is registered with
// Op.RawBody: the REST binding authorizes the caller, then hands the handler the
// request body as a stream; the handler hashes it into a temporary file under
// data/files/.tmp with a hard size limit, decides what it is from its bytes
// (see sniff.go), and in one short transaction de-duplicates, checks the quotas
// and writes the row, after moving the file into place. A blob with no row is
// an orphan a sweep can remove; a row with no blob would be a broken image, so
// the file always goes first.
package attachments

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

// Limits are the size caps, in bytes. A zero quota means none; MaxBytes is
// always set.
type Limits struct {
	MaxBytes      int64 // the largest file
	UserBytes     int64 // what one person may have uploaded
	OwnerBytes    int64 // what one project or Compendium may hold
	InstanceBytes int64 // the whole instance
	// MaxInFlight is how many uploads one person may have being received at once;
	// 0 means no cap.
	MaxInFlight int
}

var (
	inFlightMu sync.Mutex
	inFlight   = map[string]int{}
)

// takeUploadSlot reserves one in-flight upload for a person.
func takeUploadSlot(user string, max int) (release func(), ok bool) {
	if max <= 0 {
		return func() {}, true
	}
	inFlightMu.Lock()
	defer inFlightMu.Unlock()
	if inFlight[user] >= max {
		return nil, false
	}
	inFlight[user]++
	return func() {
		inFlightMu.Lock()
		defer inFlightMu.Unlock()
		if inFlight[user]--; inFlight[user] <= 0 {
			delete(inFlight, user)
		}
	}, true
}

// preCheckQuota refuses an upload that cannot fit before a byte of it is read:
// the declared length (or, when none is declared, one byte) against what the
// owner, the person and the instance have already used. It is a courtesy that
// saves receiving up to the file limit for nothing; the check that counts is the
// one in the transaction.
func preCheckQuota(c *ops.Ctx, l Limits, kind, owner string, declared int64) error {
	add := declared
	if add < 1 {
		add = 1
	}
	for _, q := range []struct {
		scope string
		limit int64
	}{{"owner:" + owner, l.OwnerBytes}, {"user:" + c.P.UserID, l.UserBytes}, {"instance", l.InstanceBytes}} {
		if q.limit <= 0 {
			continue
		}
		var used int64
		_ = c.DB().Read().QueryRowContext(c, `SELECT bytes FROM file_usage WHERE scope = ?`, q.scope).Scan(&used)
		if used+add > q.limit {
			return &ops.Error{Code: ops.CodeQuota, Message: "That file would take " + quotaWho(q.scope, kind) + " over its storage limit.",
				Data: map[string]any{"limit_bytes": q.limit, "used_bytes": used}}
		}
	}
	return nil
}

func quotaWho(scope, kind string) string {
	switch {
	case strings.HasPrefix(scope, "owner:"):
		return "this " + kind
	case strings.HasPrefix(scope, "user:"):
		return "you"
	}
	return "this server"
}

// DefaultLimits are the defaults of the configuration: 25 MB a file, 2 GB a
// person, 1 GB a project or Compendium, no instance cap.
var DefaultLimits = Limits{MaxBytes: 25 << 20, UserBytes: 2 << 30, OwnerBytes: 1 << 30}

const (
	maxAlt      = 500
	maxName     = 200
	listDefault = 100
	listMax     = 500
)

// ---- shapes -----------------------------------------------------------------------------------

// Attachment is one stored file. It carries no path: the bytes are fetched from
// url (a signed-in browser) or the download op (an API key).
type Attachment struct {
	ID        string `json:"id"`
	OwnerKind string `json:"owner_kind" enum:"project,compendium"`
	OwnerID   string `json:"owner_id"`
	Filename  string `json:"filename" desc:"Display name only; never a path."`
	Alt       string `json:"alt" desc:"Alt text for an image, a description for anything else."`
	Mime      string `json:"mime" desc:"Detected from the bytes, not taken from the client."`
	Kind      string `json:"kind" enum:"image,pdf,text"`
	Bytes     int64  `json:"bytes"`
	SHA256    string `json:"sha256"`
	Width     int    `json:"width,omitempty"`
	Height    int    `json:"height,omitempty"`
	URL       string `json:"url" desc:"The address to put in Markdown: ![alt](url). Served only to people who can read the owner."`
	Refs      int    `json:"refs" desc:"How many documents and notes link to it."`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Item wraps one attachment.
type Item struct {
	Item Attachment `json:"item"`
	// Duplicate is true when an upload matched bytes the owner already had; the
	// existing attachment is returned and nothing new is stored.
	Duplicate bool `json:"duplicate,omitempty"`
}

// OwnerIn names an owner: a project for the project.* ops and a Compendium for
// the compendium.* ones, the same field in both.
type OwnerIn struct {
	ID string `path:"id" json:"-" desc:"Project id (project.* ops) or Compendium id (compendium.* ops)."`
}

// ProjectRef and CompendiumRef let the registry authorize by membership.
func (in OwnerIn) ProjectRef() string    { return in.ID }
func (in OwnerIn) CompendiumRef() string { return in.ID }

// UploadIn is an upload. The file itself is the request body (the CLI sends it
// with --file @path); these are its metadata.
type UploadIn struct {
	ID       string `path:"id" json:"-" desc:"Project id (project.* ops) or Compendium id (compendium.* ops)."`
	Filename string `query:"filename" desc:"A name for the file, for display and download. The type is decided from the contents, and a name that contradicts them is refused."`
	Alt      string `query:"alt" desc:"Alt text for an image, or a short description."`
}

func (in UploadIn) ProjectRef() string    { return in.ID }
func (in UploadIn) CompendiumRef() string { return in.ID }

// ListIn lists an owner's attachments, newest first.
type ListIn struct {
	ID     string `path:"id" json:"-" desc:"Project id (project.* ops) or Compendium id (compendium.* ops)."`
	Kind   string `query:"kind" enum:"image,pdf,text" desc:"Only this kind."`
	Limit  int    `query:"limit" desc:"Results per page: 1 to 500, default 100."`
	Cursor string `query:"cursor" desc:"next_cursor from the previous page."`
}

func (in ListIn) ProjectRef() string    { return in.ID }
func (in ListIn) CompendiumRef() string { return in.ID }

// ListOut is a page of attachments.
type ListOut struct {
	Items      []Attachment `json:"items"`
	NextCursor string       `json:"next_cursor,omitempty"`
}

// AttIn names one attachment of an owner.
type AttIn struct {
	ID    string `path:"id" json:"-" desc:"Project id (project.* ops) or Compendium id (compendium.* ops)."`
	AttID string `path:"att_id" json:"-" desc:"Attachment id."`
}

func (in AttIn) ProjectRef() string    { return in.ID }
func (in AttIn) CompendiumRef() string { return in.ID }

// UpdateIn changes an attachment's name or alt text. The bytes never change.
type UpdateIn struct {
	ID       string  `path:"id" json:"-" desc:"Project id (project.* ops) or Compendium id (compendium.* ops)."`
	AttID    string  `path:"att_id" json:"-" desc:"Attachment id."`
	Alt      *string `json:"alt,omitempty"`
	Filename *string `json:"filename,omitempty" desc:"Display name; the extension follows the detected type."`
}

func (in UpdateIn) ProjectRef() string    { return in.ID }
func (in UpdateIn) CompendiumRef() string { return in.ID }

// DeleteIn deletes an attachment.
type DeleteIn struct {
	ID    string `path:"id" json:"-" desc:"Project id (project.* ops) or Compendium id (compendium.* ops)."`
	AttID string `path:"att_id" json:"-" desc:"Attachment id."`
	Force bool   `query:"force" desc:"Delete even though documents or notes still link to it; their images will show as broken."`
}

func (in DeleteIn) ProjectRef() string    { return in.ID }
func (in DeleteIn) CompendiumRef() string { return in.ID }

// DeleteOut says what a delete did.
type DeleteOut struct {
	Deleted bool `json:"deleted"`
	Refs    int  `json:"refs" desc:"Links that were still pointing at it."`
}

// Ref is one place an attachment is used.
type Ref struct {
	Kind    string `json:"kind" enum:"doc,research,entity,cover"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	Trashed bool   `json:"trashed"`
}

// RefsOut lists where an attachment is used.
type RefsOut struct {
	Items []Ref `json:"items"`
}

// FileOut is an attachment's bytes, for the download op. The browser route
// streams instead (GET /files/{id}).
type FileOut struct {
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
	Body        []byte `json:"body"`
}

// Download implements ops.Download.
func (f FileOut) Download() (string, string, []byte) { return f.Filename, f.ContentType, f.Body }

// Usage is storage in use against a limit.
type Usage struct {
	Bytes int64 `json:"bytes"`
	Files int64 `json:"files"`
	Limit int64 `json:"limit_bytes" desc:"0 means no limit."`
}

// UsageOut is an owner's usage, with what the caller and the instance have used.
type UsageOut struct {
	Owner    Usage  `json:"owner"`
	You      Usage  `json:"you"`
	MaxFile  int64  `json:"max_file_bytes"`
	Instance *Usage `json:"instance,omitempty" desc:"Present for administrators."`
}

// MyUsageIn is empty.
type MyUsageIn struct{}

// MyUsageOut is the caller's own usage across everything they uploaded.
type MyUsageOut struct {
	You      Usage  `json:"you"`
	MaxFile  int64  `json:"max_file_bytes"`
	Instance *Usage `json:"instance,omitempty" desc:"Present for administrators."`
}

// ---- registration -----------------------------------------------------------------------------

// Register adds the attachment ops. The limits are captured here, not read from
// the environment at call time, so a preview (which runs without services) sees
// the same ones.
func Register(r *ops.Registry, l Limits) {
	if l.MaxBytes <= 0 {
		l.MaxBytes = DefaultLimits.MaxBytes
	}
	if env := r.Env(); env != nil {
		env.SetService(ServiceLimits, l)
		hooksOf(env) // the hooks that were added before this ran are kept
		env.AddPurgeHook(purgeOwner(env.DataDir, OwnerProject))
		env.AddCompendiumPurgeHook(purgeOwner(env.DataDir, OwnerCompendium))
	}
	// A body of the largest file plus a little for a multipart-free request.
	maxBody := l.MaxBytes + 1<<10
	reg := func(kind string) {
		var prefix, base string
		var auth func(string) ops.Auth
		if kind == OwnerProject {
			prefix, base = "project.attachment.", "/api/v1/projects/{id}/attachments"
			auth = ops.RequireProject
		} else {
			prefix, base = "compendium.attachment.", "/api/v1/compendiums/{id}/attachments"
			auth = ops.RequireCompendium
		}
		editor, reader := auth("editor"), auth("reader")
		what := map[string]string{OwnerProject: "a project", OwnerCompendium: "a Compendium"}[kind]
		ops.Register(r, ops.Op{Name: prefix + "upload", Method: "POST", Path: base, Status: 201, RawBody: true, MaxBody: maxBody, NoDryRun: true, Auth: editor,
			Desc: "Upload a file to " + what + ". The request body is the file itself (the CLI: --file @path); the name and alt text are query parameters. " +
				"Images (PNG, JPEG, GIF, WebP), PDF, plain text and Markdown are kept; the type is decided from the bytes, SVG and anything executable are refused. " +
				"The same bytes uploaded twice are one attachment (duplicate: true). It cannot be previewed with --dry-run: it writes a file."},
			func(c *ops.Ctx, in UploadIn) (Item, error) { return upload(c, l, kind, in) })
		ops.Register(r, ops.Op{Name: prefix + "list", Method: "GET", Path: base, Auth: reader,
			Desc: "List " + what + "'s attachments, newest first."},
			func(c *ops.Ctx, in ListIn) (ListOut, error) { return list(c, kind, in) })
		ops.Register(r, ops.Op{Name: prefix + "get", Method: "GET", Path: base + "/{att_id}", Auth: reader,
			Desc: "Get one attachment's details (not its bytes: see download, or the url)."},
			func(c *ops.Ctx, in AttIn) (Item, error) { return get(c, kind, in) })
		ops.Register(r, ops.Op{Name: prefix + "update", Method: "PATCH", Path: base + "/{att_id}", Auth: editor,
			Desc: "Change an attachment's alt text or display name."},
			func(c *ops.Ctx, in UpdateIn) (Item, error) { return update(c, kind, in) })
		ops.Register(r, ops.Op{Name: prefix + "delete", Permanent: true, Method: "DELETE", Path: base + "/{att_id}", Auth: editor,
			Desc: "Delete an attachment and its file. Refused while documents or notes still link to it, unless force is set."},
			func(c *ops.Ctx, in DeleteIn) (DeleteOut, error) { return remove(c, kind, in) })
		ops.Register(r, ops.Op{Name: prefix + "refs", Method: "GET", Path: base + "/{att_id}/refs", Auth: reader,
			Desc: "Where an attachment is used: the documents and notes whose Markdown links to it."},
			func(c *ops.Ctx, in AttIn) (RefsOut, error) { return refs(c, kind, in) })
		ops.Register(r, ops.Op{Name: prefix + "download", Method: "GET", Path: base + "/{att_id}/download", Auth: reader,
			Desc: "Download an attachment's bytes (the CLI: --output FILE)."},
			func(c *ops.Ctx, in AttIn) (FileOut, error) { return download(c, kind, in) })
		usageBase := strings.Replace(base, "/attachments", "/attachment-usage", 1)
		ops.Register(r, ops.Op{Name: prefix + "usage", Method: "GET", Path: usageBase, Auth: reader,
			Desc: "Storage in use by " + what + ", by you, and its limits."},
			func(c *ops.Ctx, in OwnerIn) (UsageOut, error) { return usage(c, l, kind, in) })
	}
	reg(OwnerProject)
	reg(OwnerCompendium)
	ops.Register(r, ops.Op{Name: "attachment.usage", Method: "GET", Path: "/api/v1/files/usage", Auth: ops.Authenticated(),
		Desc: "What you have uploaded in all, the largest file allowed, and (for administrators) the instance's total."},
		func(c *ops.Ctx, _ MyUsageIn) (MyUsageOut, error) { return myUsage(c, l) })
}

// ---- helpers ----------------------------------------------------------------------------------

type row struct {
	a   Attachment
	ext string
}

func ownerCol(kind string) string {
	if kind == OwnerCompendium {
		return "compendium_id"
	}
	return "project_id"
}

const attCols = `a.id, a.sha256, a.bytes, a.mime, a.ext, a.kind, a.filename, a.alt, a.width, a.height, a.created_at, a.updated_at,
	(SELECT count(*) FROM attachment_refs r WHERE r.attachment_id = a.id)`

type scanner interface{ Scan(...any) error }

func scanAtt(s scanner, kind, owner string) (row, error) {
	var r row
	err := s.Scan(&r.a.ID, &r.a.SHA256, &r.a.Bytes, &r.a.Mime, &r.ext, &r.a.Kind, &r.a.Filename, &r.a.Alt, &r.a.Width, &r.a.Height,
		&r.a.CreatedAt, &r.a.UpdatedAt, &r.a.Refs)
	r.a.OwnerKind, r.a.OwnerID = kind, owner
	r.a.URL = "/files/" + r.a.ID
	return r, err
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func load(ctx context.Context, q queryer, kind, owner, id string) (row, error) {
	r, err := scanAtt(q.QueryRowContext(ctx, `SELECT `+attCols+` FROM attachments a WHERE a.id = ? AND a.`+ownerCol(kind)+` = ?`, id, owner), kind, owner)
	if errors.Is(err, sql.ErrNoRows) {
		return row{}, ops.NotFound("attachment")
	}
	return r, err
}

// displayName makes the stored name: cleaned, with an extension that matches
// the detected type.
func displayName(name string, k kindInfo) string {
	n := CleanName(name)
	if n == "download" || n == "" {
		return "file." + k.Ext
	}
	ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(n), "."))
	if want, ok := extKinds[ext]; ok && want.Kind == k.Kind && (k.Kind != "image" && k.Kind != "pdf" || want.Mime == k.Mime) {
		return n
	}
	for len(n)+len(k.Ext)+1 > maxName {
		_, size := utf8.DecodeLastRuneInString(n)
		n = n[:len(n)-size]
	}
	return n + "." + k.Ext
}

func cleanAlt(s string) (string, error) {
	s = strings.TrimSpace(strings.NewReplacer("\r\n", " ", "\n", " ", "\r", " ").Replace(s))
	if utf8.RuneCountInString(s) > maxAlt {
		return "", ops.Invalid("alt", "Alt text is limited to 500 characters.")
	}
	return s, nil
}

// headBuf keeps the first n bytes written to it.
type headBuf struct {
	b []byte
	n int
}

func (h *headBuf) Write(p []byte) (int, error) {
	if room := h.n - len(h.b); room > 0 {
		if len(p) < room {
			room = len(p)
		}
		h.b = append(h.b, p[:room]...)
	}
	return len(p), nil
}

// ---- ops --------------------------------------------------------------------------------------

func upload(c *ops.Ctx, l Limits, kind string, in UploadIn) (Item, error) {
	if c.DryRun() {
		return Item{}, ops.DryRunUnsupported("upload")
	}
	rb, ok := c.RawBody()
	if !ok {
		return Item{}, ops.Invalid("", "Send the file as the request body.")
	}
	if rb.Length > l.MaxBytes {
		return Item{}, tooBig(l)
	}
	alt, err := cleanAlt(in.Alt)
	if err != nil {
		return Item{}, err
	}
	release, ok := takeUploadSlot(c.P.UserID, l.MaxInFlight)
	if !ok {
		return Item{}, &ops.Error{Code: ops.CodeRateLimited, Message: "You already have uploads in progress; wait for one to finish.", RetryAfterSeconds: 5}
	}
	defer release()
	if err := preCheckQuota(c, l, kind, in.ID, rb.Length); err != nil {
		return Item{}, err
	}
	dir := tmpDir(c.Env.DataDir)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Item{}, ops.Internal(err)
	}
	tmp, err := os.CreateTemp(dir, "up-*")
	if err != nil {
		return Item{}, ops.Internal(err)
	}
	tmpName := tmp.Name()
	moved := false
	defer func() {
		_ = tmp.Close()
		if !moved {
			_ = os.Remove(tmpName)
		}
	}()
	h := sha256.New()
	head := &headBuf{n: HeadSize}
	n, err := io.Copy(io.MultiWriter(tmp, h, head), io.LimitReader(rb.R, l.MaxBytes+1))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			return Item{}, tooBig(l)
		}
		return Item{}, ops.Invalid("", "The upload was interrupted.")
	}
	if n > l.MaxBytes {
		return Item{}, tooBig(l)
	}
	if n == 0 {
		return Item{}, ops.Invalid("", "The file is empty.")
	}
	if err := tmp.Sync(); err != nil {
		return Item{}, ops.Internal(err)
	}
	if err := tmp.Close(); err != nil {
		return Item{}, ops.Internal(err)
	}
	sha := hex.EncodeToString(h.Sum(nil))
	k, err := detect(head.b, n, in.Filename, rb.ContentType)
	if err != nil {
		return Item{}, err
	}
	width, height, err := dimensions(tmpName, k)
	if err != nil {
		return Item{}, err
	}
	if k.Kind == "text" && n > HeadSize {
		// Detection read the start; a text file is valid text all the way through.
		if err := checkWholeText(tmpName); err != nil {
			return Item{}, err
		}
	}
	name := displayName(in.Filename, k)
	final, err := BlobPath(c.Env.DataDir, kind, in.ID, sha, k.Ext)
	if err != nil {
		return Item{}, ops.Internal(err)
	}

	if err := os.MkdirAll(filepath.Dir(final), 0o700); err != nil {
		return Item{}, ops.Internal(err)
	}
	fileMu.Lock()
	defer fileMu.Unlock()
	var out Item
	placed := false
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		var id string
		err := tx.QueryRow(`SELECT id FROM attachments WHERE `+ownerCol(kind)+` = ? AND sha256 = ?`, in.ID, sha).Scan(&id)
		switch {
		case err == nil:
			r, err := load(c, tx, kind, in.ID, id)
			if err != nil {
				return err
			}
			out = Item{Item: r.a, Duplicate: true}
			return nil
		case !errors.Is(err, sql.ErrNoRows):
			return err
		}
		if err := checkQuota(c, tx, l, kind, in.ID, n); err != nil {
			return err
		}
		if err := os.Rename(tmpName, final); err != nil {
			return err
		}
		moved, placed = true, true
		id = db.NewID("att")
		now := db.Now()
		var project, compendium any
		if kind == OwnerProject {
			project = in.ID
		} else {
			compendium = in.ID
		}
		if _, err := tx.Exec(`INSERT INTO attachments(id, project_id, compendium_id, sha256, bytes, mime, ext, kind, filename, alt, width, height, created_by, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			id, project, compendium, sha, n, k.Mime, k.Ext, k.Kind, name, alt, width, height, nullIfEmpty(c.P.UserID), now, now); err != nil {
			return err
		}
		r, err := load(c, tx, kind, in.ID, id)
		if err != nil {
			return err
		}
		out = Item{Item: r.a}
		ev := ops.Event{Type: "attachment.created", Payload: map[string]any{"attachment_id": id, "kind": k.Kind, "bytes": n}}
		setOwner(&ev, kind, in.ID)
		c.Emit(ev)
		c.Audit(ops.AuditEntry{Action: "attachment.upload", TargetType: "attachment", TargetID: id, Detail: map[string]any{"bytes": n, "kind": k.Kind}})
		return c.FlushTo(tx)
	})
	if err != nil {
		if placed {
			_ = os.Remove(final) // no row names it: do not leave an orphan
		}
		return Item{}, err
	}
	return out, nil
}

func tooBig(l Limits) error {
	return &ops.Error{Code: ops.CodeTooLarge, Message: "That file is larger than the server accepts.", Data: map[string]any{"max_file_bytes": l.MaxBytes}}
}

func setOwner(ev *ops.Event, kind, id string) {
	if kind == OwnerProject {
		ev.ProjectID = id
	} else {
		ev.CompendiumID = id
	}
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// checkQuota refuses an upload that would take the owner, the person or the
// instance over a quota. It reads the counters the triggers keep, inside the
// writer's transaction.
func checkQuota(c *ops.Ctx, tx *sql.Tx, l Limits, kind, owner string, add int64) error {
	used := func(scope string) int64 {
		var b int64
		_ = tx.QueryRow(`SELECT bytes FROM file_usage WHERE scope = ?`, scope).Scan(&b)
		return b
	}
	for _, q := range []struct {
		scope string
		limit int64
		what  string
	}{
		{"owner:" + owner, l.OwnerBytes, "This " + kind + " has reached its storage limit."},
		{"user:" + c.P.UserID, l.UserBytes, "You have reached your storage limit."},
		{"instance", l.InstanceBytes, "This server has reached its storage limit."},
	} {
		if q.limit > 0 && used(q.scope)+add > q.limit {
			return &ops.Error{Code: ops.CodeQuota, Message: q.what, Data: map[string]any{"limit_bytes": q.limit, "used_bytes": used(q.scope)}}
		}
	}
	return nil
}

func list(c *ops.Ctx, kind string, in ListIn) (ListOut, error) {
	limit := in.Limit
	switch {
	case limit <= 0:
		limit = listDefault
	case limit > listMax:
		limit = listMax
	}
	q := `SELECT ` + attCols + ` FROM attachments a WHERE a.` + ownerCol(kind) + ` = ?`
	args := []any{in.ID}
	if in.Kind != "" {
		if in.Kind != "image" && in.Kind != "pdf" && in.Kind != "text" {
			return ListOut{}, ops.Invalid("kind", "Kind is image, pdf or text.")
		}
		q += ` AND a.kind = ?`
		args = append(args, in.Kind)
	}
	if in.Cursor != "" {
		raw, err := base64.RawURLEncoding.DecodeString(in.Cursor)
		var cur struct{ At, ID string }
		if err != nil || json.Unmarshal(raw, &cur) != nil || cur.ID == "" {
			return ListOut{}, ops.Invalid("cursor", "That cursor is not valid.")
		}
		q += ` AND (a.created_at < ? OR (a.created_at = ? AND a.id < ?))`
		args = append(args, cur.At, cur.At, cur.ID)
	}
	q += ` ORDER BY a.created_at DESC, a.id DESC LIMIT ?`
	args = append(args, limit+1)
	rows, err := c.DB().Read().QueryContext(c, q, args...)
	if err != nil {
		return ListOut{}, err
	}
	defer rows.Close()
	out := ListOut{Items: []Attachment{}}
	for rows.Next() {
		r, err := scanAtt(rows, kind, in.ID)
		if err != nil {
			return ListOut{}, err
		}
		out.Items = append(out.Items, r.a)
	}
	if err := rows.Err(); err != nil {
		return ListOut{}, err
	}
	if len(out.Items) > limit {
		out.Items = out.Items[:limit]
		last := out.Items[limit-1]
		raw, _ := json.Marshal(struct{ At, ID string }{last.CreatedAt, last.ID})
		out.NextCursor = base64.RawURLEncoding.EncodeToString(raw)
	}
	return out, nil
}

func get(c *ops.Ctx, kind string, in AttIn) (Item, error) {
	r, err := load(c, c.DB().Read(), kind, in.ID, in.AttID)
	return Item{Item: r.a}, err
}

func update(c *ops.Ctx, kind string, in UpdateIn) (Item, error) {
	var out Item
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		r, err := load(c, tx, kind, in.ID, in.AttID)
		if err != nil {
			return err
		}
		alt, name := r.a.Alt, r.a.Filename
		if in.Alt != nil {
			if alt, err = cleanAlt(*in.Alt); err != nil {
				return err
			}
		}
		if in.Filename != nil {
			name = displayName(*in.Filename, kindInfo{Mime: r.a.Mime, Ext: r.ext, Kind: r.a.Kind})
		}
		if alt == r.a.Alt && name == r.a.Filename {
			out = Item{Item: r.a}
			return nil
		}
		now := db.Now()
		if _, err := tx.Exec(`UPDATE attachments SET alt = ?, filename = ?, updated_at = ? WHERE id = ?`, alt, name, now, r.a.ID); err != nil {
			return err
		}
		r.a.Alt, r.a.Filename, r.a.UpdatedAt = alt, name, now
		out = Item{Item: r.a}
		ev := ops.Event{Type: "attachment.updated", Payload: map[string]any{"attachment_id": r.a.ID}}
		setOwner(&ev, kind, in.ID)
		c.Emit(ev)
		return c.FlushTo(tx)
	})
	return out, err
}

func remove(c *ops.Ctx, kind string, in DeleteIn) (DeleteOut, error) {
	var out DeleteOut
	var gone row
	var afters []func()
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		afters = nil // a retried transaction starts again
		r, err := load(c, tx, kind, in.ID, in.AttID)
		if err != nil {
			return err
		}
		refs, err := allRefs(c, tx, kind, in.ID, r.a.ID)
		if err != nil {
			return err
		}
		r.a.Refs = len(refs)
		if r.a.Refs > 0 && !in.Force {
			return ops.ConflictWith("Documents, notes, entries or the cover still use this attachment. Remove the links first, or delete it anyway with force.", map[string]any{"refs": r.a.Refs})
		}
		if kind == OwnerProject && r.a.Refs > 0 {
			// A forced delete of a cover picture returns that side of the cover to the generated one, in this transaction.
			var err error
			if afters, err = runDeleteHooks(c, tx, in.ID, r.a.ID); err != nil {
				return err
			}
		}
		if _, err := tx.Exec(`DELETE FROM attachments WHERE id = ?`, r.a.ID); err != nil {
			return err
		}
		gone, out = r, DeleteOut{Deleted: true, Refs: r.a.Refs}
		ev := ops.Event{Type: "attachment.deleted", Payload: map[string]any{"attachment_id": r.a.ID, "refs": r.a.Refs}}
		setOwner(&ev, kind, in.ID)
		c.Emit(ev)
		c.Audit(ops.AuditEntry{Action: "attachment.delete", TargetType: "attachment", TargetID: r.a.ID, Detail: map[string]any{"refs": r.a.Refs}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return DeleteOut{}, err
	}
	// The row is gone and committed: now the bytes, unless a concurrent upload
	// of the same bytes has made them live again. Never in a preview.
	if !c.DryRun() {
		removeBlob(c, c.Env, kind, in.ID, gone.a.SHA256, gone.ext)
		for _, f := range afters {
			f()
		}
	}
	return out, nil
}

func refs(c *ops.Ctx, kind string, in AttIn) (RefsOut, error) {
	if _, err := load(c, c.DB().Read(), kind, in.ID, in.AttID); err != nil {
		return RefsOut{}, err
	}
	items, err := allRefs(c, c.DB().Read(), kind, in.ID, in.AttID)
	return RefsOut{Items: items}, err
}

// allRefs is where an attachment is used, for a decision that matters (a delete,
// the list of uses). The recorded references are a hint; the text is the truth.
// A reference is recorded when a document or note is saved, so a link that got
// there another way (a bulk import, a body written before the attachment
// existed, an entity's body, a record lost) has no row. So the owner's text is
// searched for the id as well: documents and research notes, and for a
// Compendium its entities too. The search is a substring match on one id, bounded
// to 500 hits, and runs only for a delete or a listing of uses.
func allRefs(ctx context.Context, q queryer, kind, owner, id string) ([]Ref, error) {
	const limit = 500
	seen := map[string]bool{}
	out := []Ref{}
	add := func(r Ref) {
		if k := r.Kind + "/" + r.ID; !seen[k] && len(out) < limit {
			seen[k] = true
			out = append(out, r)
		}
	}
	rows, err := q.QueryContext(ctx, `SELECT r.ref_kind, r.ref_id,
		COALESCE(d.title, n.title, CASE WHEN r.ref_kind = 'cover' THEN 'Cover' ELSE '' END), COALESCE(d.deleted_at IS NOT NULL, n.deleted_at IS NOT NULL, 0)
		FROM attachment_refs r
		LEFT JOIN documents d ON r.ref_kind = 'doc' AND d.id = r.ref_id
		LEFT JOIN research_notes n ON r.ref_kind = 'research' AND n.id = r.ref_id
		WHERE r.attachment_id = ? ORDER BY r.ref_kind, r.ref_id`, id)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var x Ref
		if err := rows.Scan(&x.Kind, &x.ID, &x.Title, &x.Trashed); err != nil {
			rows.Close()
			return nil, err
		}
		add(x)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	pat := "%/files/" + strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(id) + "%"
	scan := func(query, refKind string, args ...any) error {
		rs, err := q.QueryContext(ctx, query, args...)
		if err != nil {
			return err
		}
		defer rs.Close()
		for rs.Next() {
			var x Ref
			x.Kind = refKind
			if err := rs.Scan(&x.ID, &x.Title, &x.Trashed); err != nil {
				return err
			}
			add(x)
		}
		return rs.Err()
	}
	col := ownerCol(kind)
	if kind == OwnerProject {
		if err := scan(`SELECT id, title, deleted_at IS NOT NULL FROM documents WHERE project_id = ? AND content LIKE ? ESCAPE '\' LIMIT 500`, RefDoc, owner, pat); err != nil {
			return nil, err
		}
	}
	if err := scan(`SELECT id, title, deleted_at IS NOT NULL FROM research_notes WHERE `+col+` = ? AND body LIKE ? ESCAPE '\' LIMIT 500`, RefResearch, owner, pat); err != nil {
		return nil, err
	}
	if kind == OwnerCompendium {
		if err := scan(`SELECT id, name, trashed_at IS NOT NULL FROM entities WHERE compendium_id = ? AND (body LIKE ? ESCAPE '\' OR summary LIKE ? ESCAPE '\') LIMIT 500`, "entity", owner, pat, pat); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Open finds an attachment's file for a reader that has already been
// authorized for the owner. The path is rebuilt from the row.
func Open(ctx context.Context, env *ops.Env, kind, owner, id string) (Attachment, string, error) {
	r, err := load(ctx, env.DB.Read(), kind, owner, id)
	if err != nil {
		return Attachment{}, "", err
	}
	p, err := BlobPath(env.DataDir, kind, owner, r.a.SHA256, r.ext)
	if err != nil {
		return Attachment{}, "", ops.Internal(err)
	}
	return r.a, p, nil
}

// Locate finds which owner an attachment belongs to, for the route that serves
// files by id alone. ok is false for an unknown id.
func Locate(ctx context.Context, d *db.DB, id string) (kind, owner string, ok bool) {
	var p, cm sql.NullString
	if err := d.Read().QueryRowContext(ctx, `SELECT project_id, compendium_id FROM attachments WHERE id = ?`, id).Scan(&p, &cm); err != nil {
		return "", "", false
	}
	if p.Valid {
		return OwnerProject, p.String, true
	}
	return OwnerCompendium, cm.String, true
}

func download(c *ops.Ctx, kind string, in AttIn) (FileOut, error) {
	a, path, err := Open(c, c.Env, kind, in.ID, in.AttID)
	if err != nil {
		return FileOut{}, err
	}
	f, _, err := OpenRegular(c.Env.DataDir, path)
	if err != nil {
		return FileOut{}, ops.NotFound("attachment") // missing, a link, or planted: all the same
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, a.Bytes+1))
	if err != nil || int64(len(b)) != a.Bytes {
		return FileOut{}, ops.NotFound("attachment")
	}
	return FileOut{Filename: a.Filename, ContentType: a.Mime, Body: b}, nil
}

func usageOf(ctx context.Context, q queryer, scope string) Usage {
	var u Usage
	_ = q.QueryRowContext(ctx, `SELECT bytes, files FROM file_usage WHERE scope = ?`, scope).Scan(&u.Bytes, &u.Files)
	return u
}

func usage(c *ops.Ctx, l Limits, kind string, in OwnerIn) (UsageOut, error) {
	q := c.DB().Read()
	out := UsageOut{Owner: usageOf(c, q, "owner:"+in.ID), You: usageOf(c, q, "user:"+c.P.UserID), MaxFile: l.MaxBytes}
	out.Owner.Limit, out.You.Limit = l.OwnerBytes, l.UserBytes
	if c.P.Role == ops.RoleAdmin && !c.P.Pinned() {
		i := usageOf(c, q, "instance")
		i.Limit = l.InstanceBytes
		out.Instance = &i
	}
	return out, nil
}

func myUsage(c *ops.Ctx, l Limits) (MyUsageOut, error) {
	q := c.DB().Read()
	out := MyUsageOut{You: usageOf(c, q, "user:"+c.P.UserID), MaxFile: l.MaxBytes}
	out.You.Limit = l.UserBytes
	if c.P.Role == ops.RoleAdmin {
		i := usageOf(c, q, "instance")
		i.Limit = l.InstanceBytes
		out.Instance = &i
	}
	return out, nil
}
