package coverlayout

import (
	"database/sql"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/imagesafe"
	"github.com/crueber/scriobh/internal/ops"
)

// ProjectIn names the project.
type ProjectIn struct {
	ID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef lets the registry authorize by membership.
func (in ProjectIn) ProjectRef() string { return in.ID }

// View is a layout as the API shows it, with what it measures and says.
type View struct {
	Shape     string        `json:"shape" enum:"sides,wrap"`
	Trim      string        `json:"trim"`
	Front     *Side         `json:"front,omitempty"`
	Back      *Side         `json:"back,omitempty"`
	Wrap      *Wrap         `json:"wrap,omitempty"`
	Alt       string        `json:"alt"`
	Revision  int           `json:"revision" desc:"Send this back with project.cover_layout.set."`
	Hash      string        `json:"hash"`
	Pictures  []PictureInfo `json:"pictures" desc:"Each picture the layout places, with its resolution at the trim and, for an image side, what the crop cuts away."`
	Measured  *Measured     `json:"measured,omitempty" desc:"A wrap picture's measured resolution and spine, and the panels cut from it."`
	Warnings  []Warning     `json:"warnings"`
	FrontURL  string        `json:"front_url,omitempty" desc:"The front as a JPEG (project.cover). Empty when the front is the generated cover."`
	BackURL   string        `json:"back_url,omitempty" desc:"The back as a JPEG, when the layout gives it a picture."`
	ThumbURL  string        `json:"thumb_url,omitempty" desc:"The front 480 px wide."`
	CreatedAt string        `json:"created_at"`
	UpdatedAt string        `json:"updated_at"`
}

// Item wraps the layout, which is null when the project uses the generated cover.
type Item struct {
	Layout *View `json:"layout" desc:"null when the project uses the generated cover."`
}

func coverURL(projectID, query string) string {
	return "/api/v1/projects/" + projectID + "/cover" + query
}

// view builds the API shape of a layout: measured against its pictures, with the warnings.
func view(l *Layout, look func(string) (Picture, bool), rtl bool, strict bool) (View, error) {
	an, err := analyze(l, look, rtl, strict)
	if err != nil {
		return View{}, err
	}
	v := View{Shape: l.Shape, Trim: l.Trim, Alt: l.Alt, Revision: l.Revision, Hash: l.Hash, Pictures: an.Pictures, Measured: an.Wrap, Warnings: an.Warnings,
		CreatedAt: l.CreatedAt, UpdatedAt: l.UpdatedAt}
	if v.Pictures == nil {
		v.Pictures = []PictureInfo{}
	}
	if v.Warnings == nil {
		v.Warnings = []Warning{}
	}
	if l.Shape == ShapeWrap {
		w := *l.Wrap
		v.Wrap = &w
	} else {
		f, b := l.Front, l.Back
		v.Front, v.Back = &f, &b
	}
	k := key(l, rtl)[:12]
	if l.HasFront() {
		v.FrontURL = coverURL(l.ProjectID, "?v="+k)
		v.ThumbURL = coverURL(l.ProjectID, "?size=thumb&v="+k)
	}
	if l.HasBack() {
		v.BackURL = coverURL(l.ProjectID, "?part=back&v="+k)
	}
	return v, nil
}

// Refused turns a Refusal into the error an op returns, with its stable code in the data.
func Refused(err error) error { return refuse(err) }

// ---- register -------------------------------------------------------------------------------------

// Register adds the cover layout ops. The limits are captured here, as the attachment ops capture theirs, because an
// upload's body limit is fixed when the op is registered.
func Register(r *ops.Registry, l attachments.Limits) {
	if l.MaxBytes <= 0 {
		l.MaxBytes = attachments.DefaultLimits.MaxBytes
	}
	if env := r.Env(); env != nil {
		attachments.AddDeleteHook(env, forcedDelete)
	}
	ed, rd := ops.RequireProject(ops.ProjectEditor), ops.RequireProject(ops.ProjectReader)
	ops.Register(r, ops.Op{Name: "project.cover_layout.get", Method: "GET", Path: "/api/v1/projects/{id}/cover-layout", Auth: rd,
		Desc: "The project's own cover layout: its shape (sides or wrap), trim, front and back, alt text, revision, each picture's resolution at the trim, the measured spine of a wrap and the panels cut from it, the warnings and the addresses of the front, back and thumbnail. layout is null when the project uses the generated cover."},
		func(c *ops.Ctx, in ProjectIn) (Item, error) { return get(c, in) })
	ops.Register(r, ops.Op{Name: "project.cover_layout.upload", Method: "POST", Path: "/api/v1/projects/{id}/cover-layout/images", Status: 201, RawBody: true, MaxBody: l.MaxBytes + 1<<10, NoDryRun: true, Auth: ed,
		Desc: "Upload a cover picture. The request body is the picture itself (the CLI: --file @path); part says which part of the cover it is for: front, back or wrap. " +
			"A PNG or a JPEG is accepted and cleaned (no EXIF, no GPS, no thumbnail) into a project attachment that a layout may then name; the answer is its id, size, resolution at the trim and any warnings. " +
			"A wrap is measured at once (the resolution from its height, the spine from its width) and refused when it is not a wrap for the trim: under 150 dpi, or a spine under zero or over 2.5 in. " +
			"CMYK JPEGs, SVG, GIF, WebP, a picture over 24 megapixels, a progressive JPEG too large to decode within the server's memory budget (save a baseline JPEG) and a PNG over 16 megapixels (send a JPEG) are refused with a sentence that says why. It cannot be previewed with --dry-run: it writes a file."},
		func(c *ops.Ctx, in UploadIn) (UploadOut, error) { return upload(c, l, in) })
	ops.Register(r, ops.Op{Name: "project.cover_layout.set", Method: "PUT", Path: "/api/v1/projects/{id}/cover-layout", Auth: ed,
		Desc: "Set the project's cover layout: the whole layout at once, with the revision you read (0 when there is none yet; a stale revision is a conflict). " +
			"shape sides takes a front (generated, image or page) and an optional back; shape wrap takes one wrap picture and its bleed. " +
			"Only pictures this project's cover upload made may be named. alt is required when a picture is used. A preview (--dry-run) returns the warnings and writes nothing."},
		func(c *ops.Ctx, in SetIn) (View, error) { return set(c, in) })
	ops.Register(r, ops.Op{Name: "project.cover_layout.clear", Method: "DELETE", Path: "/api/v1/projects/{id}/cover-layout", Auth: ed,
		Desc: "Go back to the generated cover. The pictures stay as attachments until you delete them."},
		func(c *ops.Ctx, in ProjectIn) (ClearOut, error) { return clearLayout(c, in) })
	ops.Register(r, ops.Op{Name: "project.cover_layout.check", Method: "POST", Path: "/api/v1/projects/{id}/cover-layout/check", Scope: ops.ScopeRead, Auth: rd,
		Desc: "Check the layout against a print trim and a printer's profile (kdp, ingram or generic): each picture's resolution, a wrap's measured spine against the spine the page count and the paper give, and the profile's figures. Writes nothing."},
		func(c *ops.Ctx, in CheckIn) (CheckOut, error) { return check(c, in) })
}

// ---- get -------------------------------------------------------------------------------------------

func get(c *ops.Ctx, in ProjectIn) (Item, error) {
	q := c.DB().Read()
	l, err := Load(c, q, in.ID)
	if err != nil {
		return Item{}, err
	}
	if l == nil {
		return Item{}, nil
	}
	pr, err := loadProject(c, q, in.ID)
	if err != nil {
		return Item{}, ops.Internal(err)
	}
	v, err := view(l, newPictureSet(c, q, in.ID).look, pr.rtl(), false)
	if err != nil {
		return Item{}, refuse(err)
	}
	return Item{Layout: &v}, nil
}

// ---- set -------------------------------------------------------------------------------------------

// SetIn is the whole layout.
type SetIn struct {
	ID       string `path:"id" json:"-" desc:"Project id."`
	Revision int    `json:"revision" desc:"The revision you read: 0 when the project has no layout yet. A different one is a conflict."`
	Shape    string `json:"shape" enum:"sides,wrap"`
	Trim     string `json:"trim,omitempty" desc:"6x9 (default), 5.5x8.5, 5.25x8, 5x8, a5, letter or a4."`
	Front    *Side  `json:"front,omitempty" desc:"Sides only. Default generated."`
	Back     *Side  `json:"back,omitempty" desc:"Sides only. Default none."`
	Wrap     *Wrap  `json:"wrap,omitempty" desc:"Wrap only."`
	Alt      string `json:"alt,omitempty" desc:"The cover described in a sentence for screen readers. Required when a picture is used."`
}

// ProjectRef names the project.
func (in SetIn) ProjectRef() string { return in.ID }

func set(c *ops.Ctx, in SetIn) (View, error) {
	var out View
	var sweepKey string
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		v, k, err := SetTx(c, tx, in)
		if err != nil {
			return err
		}
		out, sweepKey = v, k
		return c.FlushTo(tx)
	})
	if err != nil {
		return View{}, err
	}
	if sweepKey != "" && !c.DryRun() {
		Sweep(c.Env.DataDir, in.ID, sweepKey)
		// The first look at the cover is what a person does next: have its pictures ready (after the commit, inside the
		// gate like any other, and best effort).
		Warm(c, c.Env, in.ID)
	}
	return out, nil
}

// SetTx is set inside the caller's transaction, for an op that makes a project and its cover together (the EPUB
// import). It emits the event and the audit entry but does not flush them: the caller's own flush does. The second
// result is the cache key the layout now has, "" when nothing changed; the caller sweeps the others after it commits.
func SetTx(c *ops.Ctx, tx *sql.Tx, in SetIn) (View, string, error) {
	cur, err := Load(c, tx, in.ID)
	if err != nil {
		return View{}, "", err
	}
	have, tomb := 0, 0
	if cur != nil {
		have = cur.Revision
	} else {
		tomb = tombRevision(c, tx, in.ID)
	}
	// The revision is the one the writer read. With no layout that is 0 (never had one) or the revision a cleared one left
	// behind: either says "I saw none", and none is what there is. A layout made and cleared and made again has a higher
	// revision than any reader of the first holds.
	if in.Revision != have && !(cur == nil && (in.Revision == 0 || in.Revision == tomb)) {
		return View{}, "", ops.ConflictWith("The cover changed since you loaded it. Load it again and apply your change to that.", map[string]any{"revision": max(have, tomb)})
	}
	pr, err := loadProject(c, tx, in.ID)
	if err != nil {
		return View{}, "", ops.NotFound("project")
	}
	pics := newPictureSet(c, tx, in.ID)
	l := &Layout{ProjectID: in.ID, Shape: in.Shape, Trim: in.Trim, Alt: in.Alt, Wrap: in.Wrap}
	if in.Front != nil {
		l.Front = *in.Front
	}
	if in.Back != nil {
		l.Back = *in.Back
	}
	if l.Wrap != nil {
		w := *l.Wrap
		l.Wrap = &w
	}
	if err := l.Normalize(pics.look, pr.CoverColor); err != nil {
		return View{}, "", refuse(err)
	}
	l.Hash = l.ComputeHash(pics.look)
	if _, err := analyze(l, pics.look, pr.rtl(), true); err != nil { // a wrap that is not a wrap for this trim is not saved
		return View{}, "", refuse(err)
	}
	now := db.Now()
	changed := cur == nil || cur.Hash != l.Hash
	switch {
	case !changed:
		l.Revision, l.CreatedAt, l.UpdatedAt = cur.Revision, cur.CreatedAt, cur.UpdatedAt // nothing changed: no new revision, no event
	case cur == nil && tomb > 0: // made again after it was cleared: the revision goes on from where it was
		l.Revision, l.CreatedAt, l.UpdatedAt = tomb+1, now, now
		_, err = tx.Exec(`UPDATE cover_layouts SET shape = ?, trim = ?, front_json = ?, back_json = ?, wrap_json = ?, alt = ?, revision = ?, hash = ?, created_by = ?, created_at = ?, updated_at = ? WHERE project_id = ?`,
			l.Shape, l.Trim, sideJSON(l.Front), backJSON(l), wrapJSON(l.Wrap), l.Alt, l.Revision, l.Hash, nullIfEmpty(c.P.UserID), now, now, in.ID)
	case cur == nil:
		l.Revision, l.CreatedAt, l.UpdatedAt = 1, now, now
		_, err = tx.Exec(`INSERT INTO cover_layouts(project_id, shape, trim, front_json, back_json, wrap_json, alt, revision, hash, created_by, created_at, updated_at)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, in.ID, l.Shape, l.Trim, sideJSON(l.Front), backJSON(l), wrapJSON(l.Wrap), l.Alt, l.Revision, l.Hash, nullIfEmpty(c.P.UserID), now, now)
	default:
		l.Revision, l.CreatedAt, l.UpdatedAt = cur.Revision+1, cur.CreatedAt, now
		_, err = tx.Exec(`UPDATE cover_layouts SET shape = ?, trim = ?, front_json = ?, back_json = ?, wrap_json = ?, alt = ?, revision = ?, hash = ?, updated_at = ? WHERE project_id = ?`,
			l.Shape, l.Trim, sideJSON(l.Front), backJSON(l), wrapJSON(l.Wrap), l.Alt, l.Revision, l.Hash, now, in.ID)
	}
	if err != nil {
		return View{}, "", err
	}
	sweepKey := ""
	if changed {
		if err := syncRefs(tx, in.ID, l); err != nil {
			return View{}, "", err
		}
		c.Emit(ops.Event{Type: "cover_layout.updated", ProjectID: in.ID, Payload: map[string]any{"revision": l.Revision, "layout": l.Shape}})
		c.Audit(ops.AuditEntry{Action: "cover_layout.set", TargetType: "project", TargetID: in.ID, Detail: map[string]any{"shape": l.Shape, "revision": l.Revision}})
		sweepKey = key(l, pr.rtl())
	}
	v, err := view(l, pics.look, pr.rtl(), true)
	if err != nil {
		return View{}, "", refuse(err)
	}
	return v, sweepKey, nil
}

// UseAsFront makes a project's cover a layout of one front image, from a picture the project already holds, inside
// the caller's transaction: the EPUB import's "use as this project's cover". The picture was cleaned by the same
// imagesafe pass the cover upload runs (the import cleans every picture), and is checked here against what a cover
// may be; the bytes are in data. It refuses with the sentence that says why the picture is not a cover.
func UseAsFront(c *ops.Ctx, tx *sql.Tx, projectID, attachmentID, alt string, data []byte) error {
	if err := CheckPicture(data); err != nil {
		return refuse(err)
	}
	if _, err := tx.Exec(`UPDATE attachments SET cover_clean = 1 WHERE id = ? AND project_id = ? AND kind = 'image'`, attachmentID, projectID); err != nil {
		return err
	}
	front := Side{Mode: ModeImage, Attachment: attachmentID}
	_, _, err := SetTx(c, tx, SetIn{ID: projectID, Shape: ShapeSides, Front: &front, Alt: alt})
	return err
}

func backJSON(l *Layout) string {
	if l.Shape == ShapeWrap {
		return `{"mode":"none"}`
	}
	return sideJSON(l.Back)
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ---- clear -----------------------------------------------------------------------------------------

// ClearOut says whether there was a layout to clear.
type ClearOut struct {
	Cleared bool `json:"cleared" desc:"False when the project already used the generated cover."`
}

func clearLayout(c *ops.Ctx, in ProjectIn) (ClearOut, error) {
	var out ClearOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		cur, err := Load(c, tx, in.ID)
		if err != nil || cur == nil {
			return err
		}
		if err := tombstone(tx, in.ID, cur.Revision, db.Now()); err != nil {
			return err
		}
		if err := syncRefs(tx, in.ID, nil); err != nil {
			return err
		}
		out.Cleared = true
		c.Emit(ops.Event{Type: "cover_layout.cleared", ProjectID: in.ID, Payload: map[string]any{"revision": cur.Revision}})
		c.Audit(ops.AuditEntry{Action: "cover_layout.clear", TargetType: "project", TargetID: in.ID})
		return c.FlushTo(tx)
	})
	if err != nil {
		return ClearOut{}, err
	}
	if out.Cleared && !c.DryRun() {
		Sweep(c.Env.DataDir, in.ID, "")
	}
	return out, nil
}

// forcedDelete returns the sides whose picture is being deleted (a forced attachment delete) to the generated cover,
// and a wrap whose picture is deleted to no layout at all. It runs in the delete's own transaction.
func forcedDelete(c *ops.Ctx, tx *sql.Tx, projectID, attachmentID string) (func(), error) {
	cur, err := Load(c, tx, projectID)
	if err != nil || cur == nil {
		return nil, err
	}
	names := false
	for _, id := range cur.attachments() {
		names = names || id == attachmentID
	}
	if !names {
		return nil, nil
	}
	dataDir := c.Env.DataDir
	if cur.Shape == ShapeWrap {
		if err := tombstone(tx, projectID, cur.Revision, db.Now()); err != nil {
			return nil, err
		}
		if _, err := tx.Exec(`DELETE FROM attachment_refs WHERE ref_kind = ? AND ref_id = ?`, attachments.RefCover, projectID); err != nil {
			return nil, err
		}
		c.Emit(ops.Event{Type: "cover_layout.cleared", ProjectID: projectID, Payload: map[string]any{"revision": cur.Revision}})
		return func() { Sweep(dataDir, projectID, "") }, nil
	}
	if cur.Front.Attachment == attachmentID {
		cur.Front = Side{Mode: ModeGenerated}
	}
	if cur.Back.Attachment == attachmentID {
		cur.Back = Side{Mode: ModeNone}
	}
	pr, err := loadProject(c, tx, projectID)
	if err != nil {
		return nil, err
	}
	pics := newPictureSet(c, tx, projectID)
	// the deleted picture is still in the table here: the hash must not name it
	pics.cache[attachmentID] = nil
	cur.Hash = cur.ComputeHash(pics.look)
	cur.Revision++
	now := db.Now()
	if _, err := tx.Exec(`UPDATE cover_layouts SET front_json = ?, back_json = ?, revision = ?, hash = ?, updated_at = ? WHERE project_id = ?`,
		sideJSON(cur.Front), sideJSON(cur.Back), cur.Revision, cur.Hash, now, projectID); err != nil {
		return nil, err
	}
	if err := syncRefs(tx, projectID, cur); err != nil {
		return nil, err
	}
	c.Emit(ops.Event{Type: "cover_layout.updated", ProjectID: projectID, Payload: map[string]any{"revision": cur.Revision, "layout": cur.Shape}})
	keep := key(cur, pr.rtl())
	// Whatever was made from the deleted picture goes once the delete has committed, even when the layout no longer
	// makes a picture of its own (a front that is generated again has none to replace it).
	return func() { Sweep(dataDir, projectID, keep) }, nil
}

// ---- upload ----------------------------------------------------------------------------------------

// UploadIn is a cover picture. The picture itself is the request body.
type UploadIn struct {
	ID       string `path:"id" json:"-" desc:"Project id."`
	Part     string `query:"part" enum:"front,back,wrap" desc:"Which part of the cover the picture is for. A wrap is measured at once."`
	Trim     string `query:"trim" desc:"The trim the cover is made for, to give the resolution and measure a wrap against: 6x9 (default, or the layout's), 5.5x8.5, 5.25x8, 5x8, a5, letter or a4."`
	BleedIn  string `query:"bleed_in" desc:"A wrap's bleed in inches (0 to 0.5), for measuring it. Default 0.125, or the layout's."`
	Filename string `query:"filename" desc:"A name for the picture, for display."`
}

// ProjectRef names the project.
func (in UploadIn) ProjectRef() string { return in.ID }

// UploadOut says what the upload made.
type UploadOut struct {
	AttachmentID string    `json:"attachment_id" desc:"Name this in a layout's front, back or wrap."`
	Duplicate    bool      `json:"duplicate,omitempty" desc:"The project already held these cleaned bytes."`
	Part         string    `json:"part"`
	Mime         string    `json:"mime"`
	Bytes        int64     `json:"bytes" desc:"The size of the cleaned picture."`
	Width        int       `json:"width_px"`
	Height       int       `json:"height_px"`
	Rotated      bool      `json:"rotated,omitempty" desc:"The picture said it was turned (EXIF orientation) and was turned upright before the EXIF was dropped."`
	Trim         string    `json:"trim"`
	DPI          float64   `json:"dpi" desc:"At the trim: the picture's height over the trim's height (a wrap's measured resolution, bleed included)."`
	Measured     *Measured `json:"measured,omitempty" desc:"A wrap only: the measured spine and the panels cut from it."`
	Warnings     []Warning `json:"warnings"`
}

var uploadSlots = make(chan struct{}, 4)

func upload(c *ops.Ctx, l attachments.Limits, in UploadIn) (UploadOut, error) {
	if c.DryRun() {
		return UploadOut{}, ops.DryRunUnsupported("project.cover_layout.upload")
	}
	switch in.Part {
	case "front", "back", "wrap":
	default:
		return UploadOut{}, ops.Invalid("part", "part is front, back or wrap.")
	}
	rb, ok := c.RawBody()
	if !ok {
		return UploadOut{}, ops.Invalid("", "Send the picture as the request body.")
	}
	if rb.Length > l.MaxBytes {
		return UploadOut{}, &ops.Error{Code: ops.CodeTooLarge, Message: "That file is larger than the server accepts.", Data: map[string]any{"max_file_bytes": l.MaxBytes}}
	}
	cur, err := Load(c, c.DB().Read(), in.ID)
	if err != nil {
		return UploadOut{}, err
	}
	trimName, bleed := DefaultTrim, DefaultBleedIn
	if cur != nil {
		trimName = cur.Trim
		if cur.Wrap != nil {
			bleed = cur.Wrap.Bleed()
		}
	}
	if in.Trim != "" {
		trimName = in.Trim
	}
	trim, ok := LookupTrim(trimName)
	if !ok {
		return UploadOut{}, ops.Invalid("trim", "The trim is one of "+strings.Join(Trims, ", ")+".")
	}
	if in.BleedIn != "" {
		v, err := strconv.ParseFloat(in.BleedIn, 64)
		if err != nil || math.IsNaN(v) || v < 0 || v > MaxBleedIn {
			return UploadOut{}, ops.Invalid("bleed_in", fmt.Sprintf("bleed_in is a number of inches, 0 to %g.", MaxBleedIn))
		}
		bleed = v
	}
	pr, err := loadProject(c, c.DB().Read(), in.ID)
	if err != nil {
		return UploadOut{}, ops.NotFound("project")
	}
	select {
	case uploadSlots <- struct{}{}:
		defer func() { <-uploadSlots }()
	default:
		return UploadOut{}, &ops.Error{Code: ops.CodeRateLimited, Message: "Other cover pictures are being received; wait for one to finish.", RetryAfterSeconds: 5}
	}
	data, err := io.ReadAll(io.LimitReader(rb.R, l.MaxBytes+1))
	if err != nil {
		return UploadOut{}, ops.Invalid("", "The upload was interrupted.")
	}
	if int64(len(data)) > l.MaxBytes {
		return UploadOut{}, &ops.Error{Code: ops.CodeTooLarge, Message: "That file is larger than the server accepts.", Data: map[string]any{"max_file_bytes": l.MaxBytes}}
	}
	if len(data) == 0 {
		return UploadOut{}, ops.Invalid("", "The file is empty.")
	}
	if err := CheckPicture(data); err != nil {
		return UploadOut{}, refuse(err)
	}
	name := in.Filename
	if name == "" {
		name = "cover-" + in.Part
	}
	// A rotated JPEG is decoded to be turned upright: that takes its share of the decode memory, like any derivative.
	guard := func(n int64) (func(), error) { return decodeMem.acquire(c, n+decodeRoom) }
	pic, err := attachments.PreparePictureGuarded(name+"."+extOf(data), data, l.MaxBytes, MaxDecodeBytes, guard)
	if err != nil {
		var oe *ops.Error
		if errors.As(err, &oe) {
			return UploadOut{}, oe // the server is busy: tell the caller so, not that the picture is damaged
		}
		return UploadOut{}, ops.Invalid("", "The picture could not be cleaned ("+err.Error()+"): it is damaged, or too large to keep.")
	}
	// The cleaned picture is the one that is stored and measured; it may have been turned upright.
	if err := CheckPicture(pic.Data); err != nil {
		return UploadOut{}, refuse(err)
	}
	pic.CoverClean = true
	out := UploadOut{Part: in.Part, Mime: pic.Mime, Bytes: int64(len(pic.Data)), Width: pic.Width, Height: pic.Height, Rotated: pic.Rotated, Trim: trim.Name, Warnings: []Warning{}}
	if in.Part == "wrap" {
		g, err := MeasureWrap(pic.Width, pic.Height, trim, bleed, pr.rtl())
		if err != nil {
			return UploadOut{}, refuse(err)
		}
		out.DPI, out.Measured = round2(g.DPI), measured(g, bleed)
		if g.DPI < PrintDPI {
			out.Warnings = append(out.Warnings, Warning{Code: "cover_low_resolution", Part: "wrap",
				Message: fmt.Sprintf("This wrap is %.0f dpi at %s; 300 is advised for print.", g.DPI, trimLabel(trim))})
		}
	} else {
		out.DPI = round2(float64(pic.Height) / trim.H)
		if out.DPI < PrintDPI {
			out.Warnings = append(out.Warnings, Warning{Code: "cover_low_resolution", Part: in.Part,
				Message: fmt.Sprintf("This picture is %.0f dpi at the height of a %s page; 300 is advised for print.", out.DPI, trimLabel(trim))})
		}
		if loss := CropLoss(pic.Width, pic.Height, trim); loss > 0.005 {
			out.Warnings = append(out.Warnings, Warning{Code: "cover_cropped", Part: in.Part,
				Message: fmt.Sprintf("As an image side it loses %.0f%% to fit a %s page; as a page side with fit inside it keeps all of it.", loss*100, trimLabel(trim))})
		}
	}
	icc, hadProfile := profileOf(pic.Data)
	if hadProfile && icc == nil {
		out.Warnings = append(out.Warnings, Warning{Code: "cover_profile_dropped", Part: in.Part,
			Message: "This picture carries a colour profile that the cover leaves out (it is over 1 MB, or not an RGB profile). The cover's pictures are then shown without one, and a reader that ignores profiles shows the colours as they are. Converting the picture to sRGB first is the surest."})
	}
	if !isSRGB(pic.Data, icc) {
		out.Warnings = append(out.Warnings, Warning{Code: "cover_profile_not_srgb", Part: in.Part,
			Message: "This picture carries a colour profile that is not sRGB. The cover keeps the profile with the picture, but a reader or a preview that ignores profiles shows the colours differently from your screen. Converting the picture to sRGB first is the surest."})
	}
	im, err := attachments.NewImport(c, c.Env, l, c.P.UserID, in.ID, []attachments.Picture{pic}, false)
	if err != nil {
		return UploadOut{}, err
	}
	committed := false
	defer func() { im.Done(committed) }()
	im.LockFiles()
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		if err := im.Commit(c, tx, l); err != nil {
			return err
		}
		return c.FlushTo(tx)
	})
	if err != nil {
		return UploadOut{}, err
	}
	committed = true
	p := im.Pictures()[0]
	out.AttachmentID, out.Duplicate = p.ID, p.Existing
	return out, nil
}

func extOf(b []byte) string {
	if e := attachments.SniffPicture(b); e != "" {
		return e
	}
	return "bin"
}

// CheckPicture sniffs a picture and refuses what a cover may not be, each with the sentence that says why: not a
// picture of a kind that is kept, SVG, GIF, a CMYK JPEG, more than 24 megapixels, a progressive JPEG over the decode budget, a PNG over 16 megapixels.
func CheckPicture(b []byte) error {
	ext := attachments.SniffPicture(b)
	switch ext {
	case "png", "jpg":
	case "gif", "webp":
		return &Refusal{"cover_picture_type", "A GIF or a WebP is not accepted as a cover: a printer wants a PNG or a JPEG, and an EPUB reader may not show a WebP. Save the picture as a PNG or a JPEG."}
	default:
		h := strings.ToLower(string(b[:min(len(b), 1024)]))
		if strings.Contains(h, "<svg") || strings.HasPrefix(strings.TrimSpace(h), "<?xml") {
			return &Refusal{"cover_picture_svg", "An SVG is not accepted as a cover: it can carry script, and a printer wants a raster. Export it as a PNG or a JPEG."}
		}
		return &Refusal{"cover_picture_type", "A cover is a PNG or a JPEG picture. This file is neither."}
	}
	w, h, format, cmyk, err := Header(b)
	if err != nil {
		return &Refusal{"cover_picture_damaged", "The picture is damaged."}
	}
	if cmyk {
		return &Refusal{"cover_picture_cmyk", "This JPEG is CMYK. Save it as RGB: the cover is drawn in RGB."}
	}
	px := int64(w) * int64(h)
	if px > MaxCoverPixels {
		return &Refusal{"cover_picture_large", fmt.Sprintf("The picture has %d million pixels, over the %d million a cover may have.", px/1_000_000, MaxCoverPixels/1_000_000)}
	}
	if format == "jpeg" {
		est, ok := imagesafe.DecodeBytes(b)
		if !ok {
			return &Refusal{"cover_picture_damaged", "The picture is damaged."}
		}
		if est > MaxDecodeBytes {
			return &Refusal{"cover_picture_progressive", fmt.Sprintf("This JPEG is progressive and takes %d MB to prepare here (%d million pixels at this many bytes a pixel), over the %d MB the server allows for a cover. Save it again as a baseline (not progressive) JPEG, or at under %d million pixels.",
				est>>20, px/1_000_000, MaxDecodeBytes>>20, px*MaxDecodeBytes/est/1_000_000)}
		}
	}
	if format == "png" && px > MaxPNGPixels {
		return &Refusal{"cover_picture_png_large", fmt.Sprintf("A PNG of %d million pixels is too large for the print cover, which takes a PNG up to %d million. Send it as a JPEG.", px/1_000_000, MaxPNGPixels/1_000_000)}
	}
	return nil
}

// ---- check -----------------------------------------------------------------------------------------

// Profile is a printer's figures for a paperback cover (docs/COVERS.md). They are the printer's, and the printer's
// own template stays authoritative.
type Profile struct {
	Name               string  `json:"name" enum:"kdp,ingram,generic"`
	TextMarginIn       float64 `json:"text_margin_in" desc:"Text and important art stay this far inside the trim."`
	BarcodeWidthIn     float64 `json:"barcode_width_in"`
	BarcodeHeightIn    float64 `json:"barcode_height_in"`
	SpineTextFromPages int     `json:"spine_text_from_pages" desc:"A book of fewer pages has no spine text."`
	SpineTextEdgeIn    float64 `json:"spine_text_edge_in" desc:"Spine text stays this far from the spine's edges."`
}

// Profiles are the named profiles.
var Profiles = map[string]Profile{
	"kdp":     {"kdp", 0.125, 2, 1.2, 79, 0.0625},
	"ingram":  {"ingram", 0.25, 1.75, 1, 48, 0.0625},
	"generic": {"generic", 0.25, 2, 1.2, 48, 0.0625},
}

// CheckIn asks for the layout to be checked against a print trim and profile.
type CheckIn struct {
	ID              string   `path:"id" json:"-" desc:"Project id."`
	Preset          string   `json:"preset,omitempty" desc:"A print preset (print-6x9, print-5.5x8.5, print-5x8, print-a5) or a trim name; default the layout's trim."`
	Profile         string   `json:"profile,omitempty" enum:"kdp,ingram,generic" desc:"The printer's profile. Default generic."`
	PageCount       int      `json:"page_count,omitempty" desc:"With page_thickness_in, the spine to compare a wrap's measured spine with (pages times thickness)."`
	PageThicknessIn float64  `json:"page_thickness_in,omitempty" desc:"The paper's thickness per page in inches, from the printer's guide."`
	BleedIn         *float64 `json:"bleed_in,omitempty" desc:"The print cover's bleed, 0 to 0.5 inches. Default 0.125 (a wrap's own bleed when it has one)."`
}

// ProjectRef names the project.
func (in CheckIn) ProjectRef() string { return in.ID }

// CheckOut is the result of a check.
type CheckOut struct {
	Layout          bool          `json:"layout" desc:"False when the project uses the generated cover: there is nothing to check."`
	Trim            string        `json:"trim"`
	Profile         Profile       `json:"profile"`
	BleedIn         float64       `json:"bleed_in"`
	Pictures        []PictureInfo `json:"pictures"`
	Measured        *Measured     `json:"measured,omitempty"`
	SpineComputedIn float64       `json:"spine_computed_in,omitempty" desc:"Pages times the paper's thickness, when both were given."`
	Warnings        []Warning     `json:"warnings"`
	NotChecked      []string      `json:"not_checked" desc:"What the profile asks for that this version does not check yet: the print cover's text margin and barcode box (the print cover phase)."`
}

func check(c *ops.Ctx, in CheckIn) (CheckOut, error) {
	q := c.DB().Read()
	prof := Profiles["generic"]
	if in.Profile != "" {
		p, ok := Profiles[in.Profile]
		if !ok {
			return CheckOut{}, ops.Invalid("profile", "profile is kdp, ingram or generic.")
		}
		prof = p
	}
	out := CheckOut{Profile: prof, Pictures: []PictureInfo{}, Warnings: []Warning{}, NotChecked: []string{"text_margin", "barcode_box", "spine_text"}}
	l, err := Load(c, q, in.ID)
	if err != nil {
		return out, err
	}
	if l == nil {
		return out, nil
	}
	out.Layout = true
	trimName := l.Trim
	if in.Preset != "" {
		trimName = strings.TrimPrefix(in.Preset, "print-")
		if _, ok := LookupTrim(trimName); !ok {
			return out, ops.Invalid("preset", "preset is a print preset (print-6x9, print-5.5x8.5, print-5x8, print-a5) or one of the trims: "+strings.Join(Trims, ", ")+".")
		}
	}
	out.Trim = trimName
	bleed := DefaultBleedIn
	if l.Wrap != nil {
		bleed = l.Wrap.Bleed()
	}
	if in.BleedIn != nil {
		if math.IsNaN(*in.BleedIn) || *in.BleedIn < 0 || *in.BleedIn > MaxBleedIn {
			return out, ops.Invalid("bleed_in", fmt.Sprintf("bleed_in is 0 to %g inches.", MaxBleedIn))
		}
		bleed = *in.BleedIn
	}
	out.BleedIn = bleed
	pr, err := loadProject(c, q, in.ID)
	if err != nil {
		return out, ops.NotFound("project")
	}
	pics := newPictureSet(c, q, in.ID)
	// A layout is measured at the trim it was made for; one checked against another trim is a mismatch, never stretched.
	if trimName != l.Trim {
		out.Warnings = append(out.Warnings, Warning{Code: "cover_trim_mismatch",
			Message: fmt.Sprintf("The cover is made for %s but the print preset is %s. Make the layout again for the new trim: it is never stretched.", l.Trim, trimName)})
	}
	an, err := analyze(l, pics.look, pr.rtl(), false)
	if err != nil {
		return out, refuse(err)
	}
	out.Pictures, out.Measured = an.Pictures, an.Wrap
	if out.Pictures == nil {
		out.Pictures = []PictureInfo{}
	}
	out.Warnings = append(out.Warnings, an.Warnings...)
	if in.PageCount != 0 || in.PageThicknessIn != 0 {
		if in.PageCount < 2 || in.PageCount > 1500 || math.IsNaN(in.PageThicknessIn) || in.PageThicknessIn < 0.0005 || in.PageThicknessIn > 0.01 {
			return out, ops.Invalid("page_count", "Give both page_count (2 to 1,500) and page_thickness_in (0.0005 to 0.01 inches) to compare the spine.")
		}
		out.SpineComputedIn = round3(float64(in.PageCount) * in.PageThicknessIn)
		if out.Measured != nil && math.Abs(out.Measured.SpineIn-out.SpineComputedIn) > 1.0/32 {
			out.Warnings = append(out.Warnings, Warning{Code: "cover_wrap_stale", Part: "wrap",
				Message: fmt.Sprintf("The wrap's spine measures %.3f in, but %d pages of this paper make %.3f in. The picture is placed as it is, never stretched: get a new wrap from your designer or the printer's template.",
					out.Measured.SpineIn, in.PageCount, out.SpineComputedIn)})
		}
	}
	return out, nil
}
