package compile

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/cover"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/export"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/pandoc"
	"github.com/crueber/scriobh/internal/publish"
)

// Register adds the compile ops: presets, the contact block, the preview and
// the exports.
func Register(r *ops.Registry) {
	ed := ops.RequireProject(ops.ProjectEditor)
	ops.Register(r, ops.Op{Name: "compile.preset.list", Method: "GET", Path: "/api/v1/projects/{id}/compile/presets", Scope: ops.ScopeRead,
		Desc: "List the compile presets: the built-in ones (US Standard Manuscript, UK manuscript, plain Markdown, styled DOCX, EPUB 3, the print and proof PDFs and the paged HTML) and the project's own, each with every option resolved. Editor and above.",
		Auth: ed}, presetList)
	ops.Register(r, ops.Op{Name: "compile.preset.create", Method: "POST", Path: "/api/v1/projects/{id}/compile/presets", Status: 201,
		Desc: "Save a custom compile preset: a name, the built-in preset it starts from, and the options that differ. The options are stored in full, so a later change to the built-in never alters the preset. Up to 50 per project.",
		Auth: ed}, presetCreate)
	ops.Register(r, ops.Op{Name: "compile.preset.update", Method: "PATCH", Path: "/api/v1/projects/{id}/compile/presets/{preset_id}",
		Desc: "Rename a custom preset, or change some of its options. Send only what changes.",
		Auth: ed}, presetUpdate)
	ops.Register(r, ops.Op{Name: "compile.preset.delete", Permanent: true, Method: "DELETE", Path: "/api/v1/projects/{id}/compile/presets/{preset_id}",
		Desc: "Delete a custom preset. Built-in presets cannot be deleted. Exports already made keep their record.",
		Auth: ed}, presetDelete)
	ops.Register(r, ops.Op{Name: "compile.contact.get", Method: "GET", Path: "/api/v1/projects/{id}/compile/contact", Scope: ops.ScopeRead,
		Desc: "The contact block a manuscript title page shows (name, surname for the running header, address, email, phone). Personal data: it is written into manuscript files and nowhere else.",
		Auth: ed}, contactGet)
	ops.Register(r, ops.Op{Name: "compile.contact.set", Method: "PATCH", Path: "/api/v1/projects/{id}/compile/contact", SecretOutput: true,
		Desc: "Set the manuscript contact block. Send only the fields to change; an empty string clears one. The response holds the block, so it is never kept for idempotent replay.",
		Auth: ed}, contactSet)
	ops.Register(r, ops.Op{Name: "compile.preview", Method: "POST", Path: "/api/v1/projects/{id}/compile/preview", Scope: ops.ScopeRead,
		Desc: "Preview a compile without writing a file: the title page, the running header, the chapters with their words and estimated pages, the first pages as blocks, and warnings (an untitled chapter, a scene break at a chapter's end, documents skipped because they are marked do not compile, words in notes left out). For a DOCX the page numbers are an estimate (Word's own layout decides); for a PDF preset (print-6x9 and the others) the preview runs the same layout as the export, so the page count, the page each part starts on and the warnings are the file's, and it returns the page geometry and the first pages as placed lines. Writes nothing and records nothing; the response holds your contact block and the first pages of the text, so it is never kept for idempotent replay.",
		Auth: ed, SecretOutput: true}, heavy(preview))
	ops.Register(r, ops.Op{Name: "export.compile", Method: "POST", Path: "/api/v1/projects/{id}/export/compile", Status: 200,
		Desc: "Compile the manuscript with a preset into a DOCX (US Standard Manuscript, UK manuscript, styled), an EPUB 3 (also, in a custom preset's format option, an odt, rtf, latex, rst or mediawiki file written by the Pandoc program from the compile Markdown, only while an administrator has the Pandoc bridge on; the Pandoc version is in the export's record and input hash, and the same text gives the same file only with the same version), plain Markdown, a print-ready PDF (print-6x9 and the other print and proof presets: fonts embedded, mirrored margins, running heads, roman and arabic page numbers, contents, footnotes at the foot of the page, a deterministic file that is the same for the same input hash) or one paged HTML file to print from a browser. Documents marked do not compile, notes, private notes, ghosted text and alternatives comments are left out. Deterministic, recorded with a manifest of the documents and hashes and the preset; run compile.preview first.",
		Auth: ed}, exportCompile)
	registerCover(r)
	ops.Register(r, ops.Op{Name: "export.submission", Method: "POST", Path: "/api/v1/projects/{id}/export/submission", Status: 200,
		Desc: "A submission bundle: the manuscript as a DOCX (a manuscript preset), a synopsis, a cover letter template and a manifest, in one zip. Deterministic and recorded.",
		Auth: ed}, heavy(exportSubmission))
}

// ---- presets ------------------------------------------------------------------------------

const maxPresets = 50

// PresetListIn names the project.
type PresetListIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef names the project.
func (in PresetListIn) ProjectRef() string { return in.ProjectID }

// PresetListOut lists the presets.
type PresetListOut struct {
	Presets []Preset `json:"presets" desc:"Built-in presets first, then the project's own by name."`
}

func presetList(c *ops.Ctx, in PresetListIn) (PresetListOut, error) {
	out := PresetListOut{Presets: Builtins()}
	custom, err := customPresets(c, c.DB(), in.ProjectID)
	if err != nil {
		return PresetListOut{}, err
	}
	out.Presets = append(out.Presets, custom...)
	return out, nil
}

func customPresets(ctx context.Context, d *db.DB, projectID string) ([]Preset, error) {
	rows, err := d.Read().QueryContext(ctx, `SELECT id, name, base, options_json, updated_at FROM compile_presets WHERE project_id = ? ORDER BY name_key, id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Preset
	for rows.Next() {
		var p Preset
		var oj string
		if err := rows.Scan(&p.ID, &p.Name, &p.Base, &oj, &p.UpdatedAt); err != nil {
			return nil, err
		}
		if err := json.Unmarshal([]byte(oj), &p.Options); err != nil {
			continue // a row nobody can read is not a preset
		}
		p.Format = p.Options.Format
		out = append(out, p)
	}
	return out, rows.Err()
}

func loadPreset(ctx context.Context, d *db.DB, projectID, id string) (Preset, error) {
	if id == "" {
		id = ShunnUS
	}
	if b, ok := Builtin(id); ok {
		return b, nil
	}
	if !strings.HasPrefix(id, "cpr_") {
		return Preset{}, ops.NotFound("preset")
	}
	var p Preset
	var oj string
	err := d.Read().QueryRowContext(ctx, `SELECT id, name, base, options_json, updated_at FROM compile_presets WHERE id = ? AND project_id = ?`, id, projectID).
		Scan(&p.ID, &p.Name, &p.Base, &oj, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Preset{}, ops.NotFound("preset")
	}
	if err != nil {
		return Preset{}, err
	}
	if err := json.Unmarshal([]byte(oj), &p.Options); err != nil {
		return Preset{}, ops.Internal(err)
	}
	p.Format = p.Options.Format
	return p, nil
}

// PresetItem wraps one preset.
type PresetItem struct {
	Item Preset `json:"item"`
}

// PresetCreateIn saves a custom preset.
type PresetCreateIn struct {
	ProjectID string          `path:"id" json:"-" desc:"Project id."`
	Name      string          `json:"name" desc:"1 to 80 characters, unique in the project."`
	Base      string          `json:"base,omitempty" desc:"The built-in preset to start from (default shunn-us)."`
	Options   publish.Options `json:"options,omitempty" desc:"The options that differ from the base."`
}

// ProjectRef names the project.
func (in PresetCreateIn) ProjectRef() string { return in.ProjectID }

// Validate checks the name and the options.
func (in *PresetCreateIn) Validate() error {
	n, err := presetName(in.Name)
	if err != nil {
		return err
	}
	in.Name = n
	return optionsError(in.Options.Validate())
}

func presetName(s string) (string, error) {
	s = publish.CleanLine(s)
	if s == "" || utf8.RuneCountInString(s) > 80 {
		return "", ops.Invalid("name", "A preset's name is 1 to 80 characters.")
	}
	return s, nil
}

func optionsError(err error) error {
	var fe *publish.FieldError
	if errors.As(err, &fe) {
		return ops.Invalid("options."+fe.Field, fe.Message)
	}
	return err
}

func foldName(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsSpace(r) {
			return ' '
		}
		return unicode.ToLower(r)
	}, s)
}

func presetCreate(c *ops.Ctx, in PresetCreateIn) (PresetItem, error) {
	base := in.Base
	if base == "" {
		base = ShunnUS
	}
	b, ok := Builtin(base)
	if !ok {
		return PresetItem{}, ops.Invalid("base", "base is the id of a built-in preset: shunn-us, uk-manuscript, plain-markdown, styled-docx, epub3, print-6x9, print-5x8, proof-letter... (see compile.preset.list).")
	}
	opts := publish.Merge(b.Options, in.Options)
	if err := opts.Validate(); err != nil {
		return PresetItem{}, optionsError(err)
	}
	oj, err := json.Marshal(opts)
	if err != nil {
		return PresetItem{}, ops.Internal(err)
	}
	p := Preset{ID: db.NewID("cpr"), Name: in.Name, Base: base, Format: opts.Format, Options: opts, UpdatedAt: db.Now()}
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRow(`SELECT count(*) FROM compile_presets WHERE project_id = ?`, in.ProjectID).Scan(&n); err != nil {
			return err
		}
		if n >= maxPresets {
			return ops.Invalid("name", fmt.Sprintf("A project can have %d custom presets; delete one first.", maxPresets))
		}
		var dup int
		if err := tx.QueryRow(`SELECT count(*) FROM compile_presets WHERE project_id = ? AND name_key = ?`, in.ProjectID, foldName(in.Name)).Scan(&dup); err != nil {
			return err
		}
		if dup > 0 {
			return ops.Conflict("This project already has a preset with that name.")
		}
		if _, err := tx.Exec(`INSERT INTO compile_presets(id, project_id, name, name_key, base, options_json, created_by, created_at, updated_at) VALUES (?,?,?,?,?,?,?,?,?)`,
			p.ID, in.ProjectID, in.Name, foldName(in.Name), base, string(oj), c.P.UserID, p.UpdatedAt, p.UpdatedAt); err != nil {
			return err
		}
		c.Audit(ops.AuditEntry{Action: "compile.preset.create", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"preset_id": p.ID}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return PresetItem{}, err
	}
	return PresetItem{Item: p}, nil
}

// PresetUpdateIn changes a custom preset.
type PresetUpdateIn struct {
	ProjectID string          `path:"id" json:"-" desc:"Project id."`
	PresetID  string          `path:"preset_id" json:"-" desc:"The custom preset's id (cpr_...)."`
	Name      *string         `json:"name,omitempty"`
	Options   publish.Options `json:"options,omitempty" desc:"Options to change; the rest keep their values."`
}

// ProjectRef names the project.
func (in PresetUpdateIn) ProjectRef() string { return in.ProjectID }

// Validate checks what was sent.
func (in *PresetUpdateIn) Validate() error {
	if in.Name != nil {
		n, err := presetName(*in.Name)
		if err != nil {
			return err
		}
		in.Name = &n
	}
	return optionsError(in.Options.Validate())
}

func presetUpdate(c *ops.Ctx, in PresetUpdateIn) (PresetItem, error) {
	if _, builtin := Builtin(in.PresetID); builtin {
		return PresetItem{}, ops.Invalid("preset_id", "A built-in preset cannot be changed; save a custom one.")
	}
	var out Preset
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		var oj string
		err := tx.QueryRow(`SELECT id, name, base, options_json FROM compile_presets WHERE id = ? AND project_id = ?`, in.PresetID, in.ProjectID).Scan(&out.ID, &out.Name, &out.Base, &oj)
		if errors.Is(err, sql.ErrNoRows) {
			return ops.NotFound("preset")
		}
		if err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(oj), &out.Options); err != nil {
			return ops.Internal(err)
		}
		out.Options = publish.Merge(out.Options, in.Options)
		if err := out.Options.Validate(); err != nil {
			return optionsError(err)
		}
		if in.Name != nil {
			out.Name = *in.Name
			var dup int
			if err := tx.QueryRow(`SELECT count(*) FROM compile_presets WHERE project_id = ? AND name_key = ? AND id <> ?`, in.ProjectID, foldName(out.Name), in.PresetID).Scan(&dup); err != nil {
				return err
			}
			if dup > 0 {
				return ops.Conflict("This project already has a preset with that name.")
			}
		}
		out.Format = out.Options.Format
		out.UpdatedAt = db.Now()
		nj, err := json.Marshal(out.Options)
		if err != nil {
			return ops.Internal(err)
		}
		if _, err := tx.Exec(`UPDATE compile_presets SET name = ?, name_key = ?, options_json = ?, updated_at = ? WHERE id = ? AND project_id = ?`,
			out.Name, foldName(out.Name), string(nj), out.UpdatedAt, in.PresetID, in.ProjectID); err != nil {
			return err
		}
		c.Audit(ops.AuditEntry{Action: "compile.preset.update", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"preset_id": in.PresetID}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return PresetItem{}, err
	}
	return PresetItem{Item: out}, nil
}

// PresetDeleteIn names the preset to delete.
type PresetDeleteIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
	PresetID  string `path:"preset_id" json:"-" desc:"The custom preset's id (cpr_...)."`
}

// ProjectRef names the project.
func (in PresetDeleteIn) ProjectRef() string { return in.ProjectID }

// DeleteOut says whether a preset was deleted.
type DeleteOut struct {
	Deleted bool `json:"deleted"`
}

func presetDelete(c *ops.Ctx, in PresetDeleteIn) (DeleteOut, error) {
	if _, builtin := Builtin(in.PresetID); builtin {
		return DeleteOut{}, ops.Invalid("preset_id", "A built-in preset cannot be deleted.")
	}
	var out DeleteOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		res, err := tx.Exec(`DELETE FROM compile_presets WHERE id = ? AND project_id = ?`, in.PresetID, in.ProjectID)
		if err != nil {
			return err
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			return ops.NotFound("preset")
		}
		out.Deleted = true
		c.Audit(ops.AuditEntry{Action: "compile.preset.delete", TargetType: "project", TargetID: in.ProjectID, Detail: map[string]any{"preset_id": in.PresetID}})
		return c.FlushTo(tx)
	})
	return out, err
}

// ---- contact ------------------------------------------------------------------------------

// ContactIn names the project.
type ContactIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef names the project.
func (in ContactIn) ProjectRef() string { return in.ProjectID }

// ContactOut is the contact block.
type ContactOut struct {
	Name      string `json:"name" desc:"The name for the contact block; empty: the byline."`
	Surname   string `json:"surname" desc:"For the running header; empty: the last word of the byline."`
	Address   string `json:"address" desc:"Lines separated by newlines."`
	Email     string `json:"email"`
	Phone     string `json:"phone"`
	UpdatedAt string `json:"updated_at,omitempty"`
}

func (c ContactOut) contact() Contact {
	return Contact{Name: c.Name, Surname: c.Surname, Address: c.Address, Email: c.Email, Phone: c.Phone}
}

func loadContact(ctx context.Context, d *db.DB, projectID string) (ContactOut, error) {
	var c ContactOut
	err := d.Read().QueryRowContext(ctx, `SELECT name, surname, address, email, phone, updated_at FROM manuscript_contact WHERE project_id = ?`, projectID).
		Scan(&c.Name, &c.Surname, &c.Address, &c.Email, &c.Phone, &c.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return ContactOut{}, nil
	}
	return c, err
}

func contactGet(c *ops.Ctx, in ContactIn) (ContactOut, error) {
	return loadContact(c, c.DB(), in.ProjectID)
}

// ContactSetIn changes the contact block.
type ContactSetIn struct {
	ProjectID string  `path:"id" json:"-" desc:"Project id."`
	Name      *string `json:"name,omitempty" desc:"Up to 200 characters."`
	Surname   *string `json:"surname,omitempty" desc:"Up to 100 characters; for the running header."`
	Address   *string `json:"address,omitempty" desc:"Up to 8 lines, 600 characters."`
	Email     *string `json:"email,omitempty" desc:"Up to 200 characters."`
	Phone     *string `json:"phone,omitempty" desc:"Up to 60 characters."`
}

// ProjectRef names the project.
func (in ContactSetIn) ProjectRef() string { return in.ProjectID }

// Validate trims and bounds each field that was sent.
func (in *ContactSetIn) Validate() error {
	one := func(field string, p **string, max int) error {
		if *p == nil {
			return nil
		}
		v := publish.CleanLine(**p)
		if utf8.RuneCountInString(v) > max {
			return ops.Invalid(field, fmt.Sprintf("%s is limited to %d characters.", field, max))
		}
		*p = &v
		return nil
	}
	if err := one("name", &in.Name, 200); err != nil {
		return err
	}
	if err := one("surname", &in.Surname, 100); err != nil {
		return err
	}
	if err := one("email", &in.Email, 200); err != nil {
		return err
	}
	if err := one("phone", &in.Phone, 60); err != nil {
		return err
	}
	if in.Address != nil {
		var lines []string
		for _, l := range strings.Split(publish.CleanXML(*in.Address), "\n") {
			if l = publish.CleanLine(l); l != "" {
				lines = append(lines, l)
			}
		}
		v := strings.Join(lines, "\n")
		if len(lines) > 8 || utf8.RuneCountInString(v) > 600 {
			return ops.Invalid("address", "The address is limited to 8 lines and 600 characters.")
		}
		in.Address = &v
	}
	return nil
}

func contactSet(c *ops.Ctx, in ContactSetIn) (ContactOut, error) {
	var out ContactOut
	err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		now := db.Now()
		if _, err := tx.Exec(`INSERT INTO manuscript_contact(project_id, updated_at) VALUES (?,?) ON CONFLICT(project_id) DO NOTHING`, in.ProjectID, now); err != nil {
			return err
		}
		set := func(col string, v *string) error {
			if v == nil {
				return nil
			}
			_, err := tx.Exec(`UPDATE manuscript_contact SET `+col+` = ?, updated_at = ? WHERE project_id = ?`, *v, now, in.ProjectID)
			return err
		}
		for _, s := range []struct {
			col string
			v   *string
		}{{"name", in.Name}, {"surname", in.Surname}, {"address", in.Address}, {"email", in.Email}, {"phone", in.Phone}} {
			if err := set(s.col, s.v); err != nil {
				return err
			}
		}
		if err := tx.QueryRow(`SELECT name, surname, address, email, phone, updated_at FROM manuscript_contact WHERE project_id = ?`, in.ProjectID).
			Scan(&out.Name, &out.Surname, &out.Address, &out.Email, &out.Phone, &out.UpdatedAt); err != nil {
			return err
		}
		// The audit entry says that the block changed, never what it holds.
		c.Audit(ops.AuditEntry{Action: "compile.contact.set", TargetType: "project", TargetID: in.ProjectID})
		return c.FlushTo(tx)
	})
	return out, err
}

// ---- resolving a compile ------------------------------------------------------------------

// resolved is a compile ready to preview or write.
type resolved struct {
	// pandoc is the bridge when the format is one Pandoc writes; pandocVersion the version
	// that wrote the file (known once it has), recorded in the manifest and in the input hash.
	pandoc        *pandoc.Service
	pandocVersion string
	preset        Preset
	opts          publish.Options
	src           *export.Source
	contact       Contact
	b             *built
	hash          string
}

// pandocHash finds the bridge for an export that needs it, and refuses (with the reason) when it is
// off or unusable, and folds the version of the Pandoc that would write the file into the hash.
func pandocHash(c *ops.Ctx, hash, format string) (*pandoc.Service, string, error) {
	svc, ok := pandoc.ServiceOf(c.Env)
	if !ok {
		return nil, "", ops.Conflict("Exporting through Pandoc is not available on this server.")
	}
	info := svc.Status(c)
	if info.Err != nil {
		return nil, "", pandocFailure(info.Err)
	}
	sum := sha256.Sum256([]byte(hash + "\x00pandoc\x00" + format + "\x00" + info.Binary.Version))
	return svc, hex.EncodeToString(sum[:]), nil
}

// pandocFailure turns a failure of the bridge into the error a caller sees; nothing Pandoc printed is in it.
func pandocFailure(err error) error {
	e, ok := pandoc.AsError(err)
	if !ok {
		return ops.Invalid("format", "Pandoc could not write that file.")
	}
	switch e.Kind {
	case pandoc.KindDisabled:
		return ops.ConflictWith(e.Message(), map[string]any{"reason": "pandoc_off"})
	case pandoc.KindBusy:
		x := ops.RateLimited(5)
		x.Message = e.Message()
		return x
	case pandoc.KindNotFound, pandoc.KindUnsafe, pandoc.KindVersion, pandoc.KindUnsupported:
		return ops.ConflictWith("The Pandoc bridge is not usable: ask an administrator to check the Pandoc settings.", map[string]any{"reason": "pandoc_unusable"})
	}
	return ops.Invalid("format", e.Message())
}

func resolve(c *ops.Ctx, projectID, presetID string, over publish.Options, readImages bool) (*resolved, error) {
	if err := over.Validate(); err != nil {
		return nil, optionsError(err)
	}
	preset, err := loadPreset(c, c.DB(), projectID, presetID)
	if err != nil {
		return nil, err
	}
	opts := publish.Merge(preset.Options, over)
	if err := opts.Validate(); err != nil {
		return nil, optionsError(err)
	}
	src, err := export.LoadSource(c, c.DB(), projectID)
	if err != nil {
		return nil, err
	}
	co, err := loadContact(c, c.DB(), projectID)
	if err != nil {
		return nil, err
	}
	b := build(src, opts, co.contact(), resolverFor(c, projectID, readImages && !c.DryRun()))
	if readImages && c.DryRun() && len(b.book.Images) > 0 {
		b.warn("image_skipped", fmt.Sprintf("%d image(s) are not read in a dry run and are left out of this file.", len(b.book.Images)), "", "")
	}
	hash := inputHash(preset.ID, opts, src, co.contact(), b)
	var bridge *pandoc.Service
	if pandoc.ValidExport(opts.Format) {
		// Pandoc's output is the same only for the same Pandoc: the version is part of what the hash stands for.
		var perr error
		if bridge, hash, perr = pandocHash(c, hash, opts.Format); perr != nil {
			return nil, perr
		}
	}
	b.book.InputHash = hash // a PDF's document id comes from it
	return &resolved{preset: preset, opts: opts, src: src, contact: co.contact(), b: b, hash: hash, pandoc: bridge}, nil
}

// ---- the export ---------------------------------------------------------------------------

// CompileIn asks for a compiled file.
type CompileIn struct {
	ProjectID string          `path:"id" json:"-" desc:"Project id."`
	Preset    string          `json:"preset,omitempty" desc:"A built-in preset id (shunn-us, shunn-us-courier, uk-manuscript, uk-manuscript-courier, styled-docx, epub3, plain-markdown, print-6x9, print-5.5x8.5, print-5x8, print-a5, proof-letter, proof-a4, paged-html) or a custom preset's id. Default shunn-us."`
	Options   publish.Options `json:"options,omitempty" desc:"Options that override the preset for this compile only."`
}

// ProjectRef names the project.
func (in CompileIn) ProjectRef() string { return in.ProjectID }

// Out is the compiled file. Over REST it is the file itself; elsewhere it is
// this JSON, with the file base64-encoded in body.
type Out struct {
	ExportID    string    `json:"export_id"`
	Filename    string    `json:"filename"`
	ContentType string    `json:"content_type"`
	Bytes       int       `json:"bytes"`
	SHA256      string    `json:"sha256"`
	Documents   int       `json:"documents" desc:"How many documents the file holds."`
	Skipped     int       `json:"skipped,omitempty" desc:"Documents left out because they are marked not to compile (or sit inside one that is)."`
	Preset      string    `json:"preset"`
	Format      string    `json:"format" enum:"docx,epub,md,pdf,html,zip,odt,rtf,latex,rst,mediawiki"`
	Words       int       `json:"words" desc:"Words in the compiled text."`
	InputHash   string    `json:"input_hash" desc:"A hash of the preset, the options and every included document's text: the same input hash is the same content."`
	Warnings    []Warning `json:"warnings"`
	Body        []byte    `json:"body" desc:"The file. The REST route returns it as the response body instead of JSON."`
}

// Download makes Out an ops.Download.
func (o Out) Download() (string, string, []byte) { return o.Filename, o.ContentType, o.Body }

const (
	docxType = "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	epubType = "application/epub+zip"
)

func (r *resolved) presetSlug() string {
	if r.preset.Builtin {
		return r.preset.ID
	}
	if s := projects.Slugify(r.preset.Name); s != "" {
		return s
	}
	return "custom"
}

// write builds the file for the resolved compile. The warnings are what the
// writer left out or could not fix, as the compile reports them.
func (r *resolved) write(ctx context.Context) (body []byte, ext, contentType string, warns []Warning, err error) {
	plain := func(ws []string) []Warning {
		var out []Warning
		for _, w := range ws {
			out = append(out, Warning{Code: "writer", Message: w})
		}
		return out
	}
	typed := func(ws []publish.Warn) []Warning {
		var out []Warning
		for _, w := range ws {
			out = append(out, Warning{Code: w.Code, Message: w.Message})
		}
		return out
	}
	switch r.opts.Format {
	case "epub":
		body, err = publish.WriteEPUB(r.b.book, r.coverOpts())
		return body, "epub", epubType, plain(r.b.book.Warnings), err
	case "md":
		return publish.WriteMarkdown(r.b.book, r.opts), "md", "text/markdown; charset=utf-8", plain(r.b.book.Warnings), nil
	case "docx":
		body, err = publish.WriteDOCX(r.b.book, r.opts)
		return body, "docx", docxType, plain(r.b.book.Warnings), err
	case "pdf":
		var ws []publish.Warn
		body, ws, err = publish.WritePDFContext(ctx, r.b.book, r.opts)
		return body, "pdf", "application/pdf", typed(ws), err
	case "html-paged":
		var ws []publish.Warn
		body, ws, err = publish.WriteHTMLPaged(r.b.book, r.opts)
		// recorded as html: the exports table has no value for it and the file is HTML
		return body, "html", "text/html; charset=utf-8", typed(ws), err
	case "odt", "rtf", "latex", "rst", "mediawiki":
		// The compile Markdown, converted by Pandoc. The file Pandoc writes is not read or interpreted here.
		ex, perr := r.pandoc.FromMarkdown(ctx, r.opts.Format, publish.WriteMarkdown(r.b.book, r.opts))
		if perr != nil {
			return nil, "", "", nil, perr
		}
		r.pandocVersion = ex.Version
		warns = append(plain(r.b.book.Warnings), Warning{Code: "pandoc", Message: "Written by Pandoc " + ex.Version + " from the compile Markdown. Pandoc's own rules decide the styling, and the same text gives the same file only with the same Pandoc version."})
		return ex.Body, ex.Ext, ex.ContentType, warns, nil
	}
	return nil, "", "", nil, fmt.Errorf("compile: the format %q is not one this program writes", r.opts.Format)
}

// coverOpts attaches the generated cover to the book when the EPUB wants one.
func (r *resolved) coverOpts() publish.Options {
	if publish.On(r.opts.Cover) && len(r.b.book.CoverSVG) == 0 {
		r.b.book.CoverSVG = coverSVG(r.src)
	}
	return r.opts
}

// exportCompile builds the file. The layout work (memory the gate exists to bound) runs inside the compile gate; a
// conversion by Pandoc, a separate process with a limit of its own (two at once, one per person), runs after the gate
// is let go, so a slow Pandoc never holds a slot every other compile and site build is waiting for.
func exportCompile(c *ops.Ctx, in CompileIn) (Out, error) {
	var (
		r    *resolved
		md   []byte
		done *Out
	)
	_, err := gated(c, func() (struct{}, error) {
		var err error
		r, err = resolve(c, in.ProjectID, in.Preset, in.Options, true)
		if err != nil {
			return struct{}{}, err
		}
		if r.b.empty {
			return struct{}{}, ops.Invalid("", "There is nothing to compile: every document is excluded or empty. Check which documents are marked do not compile.")
		}
		if r.opts.Format == "pdf" {
			if err := tooBigForPDF(gateOf(c), r.b.words); err != nil {
				return struct{}{}, err
			}
		}
		if r.pandoc != nil {
			if c.DryRun() {
				return struct{}{}, ops.DryRunUnsupported("export.compile")
			}
			md = publish.WriteMarkdown(r.b.book, r.opts)
			return struct{}{}, nil
		}
		out, err := r.finish(c, in.ProjectID)
		if err != nil {
			return struct{}{}, err
		}
		done = &out
		return struct{}{}, nil
	})
	if err != nil {
		return Out{}, err
	}
	if done != nil {
		return *done, nil
	}
	return r.finishPandoc(c, in.ProjectID, md)
}

// finish writes the file of a format this program writes itself and records the export.
func (r *resolved) finish(c *ops.Ctx, projectID string) (Out, error) {
	body, ext, ctype, wwarns, err := r.write(c)
	if errors.Is(err, publish.ErrRTL) || errors.Is(err, publish.ErrTruncated) {
		return Out{}, ops.Invalid("", err.Error())
	}
	if err != nil {
		return Out{}, ops.Internal(err)
	}
	return r.record(c, projectID, body, ext, ctype, wwarns, ext)
}

// finishPandoc has Pandoc write the file from the compile Markdown (outside the gate) and records the export.
func (r *resolved) finishPandoc(c *ops.Ctx, projectID string, md []byte) (Out, error) {
	ctx := pandoc.WithWho(c, c.P.UserID)
	ex, err := r.pandoc.FromMarkdown(ctx, r.opts.Format, md)
	if err != nil {
		return Out{}, pandocFailure(err)
	}
	r.pandocVersion = ex.Version
	warns := append(plainWarnings(r.b.book.Warnings), Warning{Code: "pandoc", Message: "Written by Pandoc " + ex.Version + " from the compile Markdown. Pandoc's own rules decide the styling, and the same text gives the same file only with the same Pandoc version."})
	// a file Pandoc wrote is recorded under its format's name, not its extension
	return r.record(c, projectID, ex.Body, ex.Ext, ex.ContentType, warns, r.opts.Format)
}

func plainWarnings(ws []string) []Warning {
	var out []Warning
	for _, w := range ws {
		out = append(out, Warning{Code: "writer", Message: w})
	}
	return out
}

func (r *resolved) record(c *ops.Ctx, projectID string, body []byte, ext, ctype string, wwarns []Warning, format string) (Out, error) {
	warnings := append(append([]Warning{}, r.b.warnings...), wwarns...)
	name := projects.Slugify(r.src.Project.Title) + "-" + r.presetSlug() + "." + ext
	file := export.NewFile(name, ctype, body, len(r.b.held))
	file.Skipped = r.b.skipped
	if err := export.Record(c, projectID, r.preset.ID, format, file, r.b.held, r.manifest(warnings, false), r.audit(warnings)); err != nil {
		return Out{}, err
	}
	return r.out(file, format, warnings), nil
}

func coverSVG(src *export.Source) []byte {
	p := src.Project
	_, svg := cover.Cached(cover.Spec{Title: p.Title, Subtitle: p.Subtitle, Author: p.Author, Kind: p.Kind, Color: p.CoverColor})
	return svg
}

func (r *resolved) out(f export.File, format string, warnings []Warning) Out {
	return Out{ExportID: f.ExportID, Filename: f.Filename, ContentType: f.ContentType, Bytes: f.Bytes, SHA256: f.SHA256, Documents: f.Documents,
		Skipped: f.Skipped, Preset: r.preset.ID, Format: format, Words: r.b.words, InputHash: r.hash, Warnings: warnings, Body: f.Body}
}

// manifest is what the export row records beyond the documents: ids, hashes,
// counts and option names. The contact block is never in it.
func (r *resolved) manifest(warnings []Warning, bundle bool) map[string]any {
	opts := r.opts
	m := map[string]any{"preset_id": r.preset.ID, "options": opts, "input_hash": r.hash, "words": r.b.words, "warnings": len(warnings),
		"includes_notes": opts.Notes == "appendix", "ghosts": opts.Ghosts, "images": len(r.b.book.Images)}
	if bundle {
		m["bundle"] = "submission"
	}
	if r.pandocVersion != "" {
		// Pandoc's output is deterministic only for one Pandoc version: the version is part of the record.
		m["pandoc"] = map[string]any{"version": r.pandocVersion, "format": r.opts.Format}
	}
	if len(r.b.excluded) > 0 || r.b.skipped > 0 {
		var ids []string
		for _, e := range r.b.excluded {
			ids = append(ids, e.ID)
		}
		m["not_compiled"] = ids
	}
	return m
}

func (r *resolved) audit(warnings []Warning) map[string]any {
	a := map[string]any{"preset_id": r.preset.ID, "words": r.b.words, "warnings": len(warnings), "ghosts": r.opts.Ghosts}
	if r.b.skipped > 0 {
		a["skipped"] = r.b.skipped
	}
	return a
}
