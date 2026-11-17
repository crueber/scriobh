// Package site is the op layer of static site publishing for blog mode: the
// project's site options, the per-document settings (post or page, address,
// date, excerpt, tags), a preview of what would be published, a build that
// downloads the site as a zip, and a publish that writes it to a folder on the
// server that a web server serves. The site itself is made by internal/site,
// which is pure; this package chooses what goes in and does the writing.
//
// Nothing is published automatically and nothing in the site is a second writer
// to the database: it is generated from the manuscript and the settings here,
// and the folder is a projection the next publish replaces.
package site

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/crueber/scriobh/internal/cover"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/export"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
	sitegen "github.com/crueber/scriobh/internal/site"
)

// ServiceName is the key the server registers the Service under.
const ServiceName = "site"

// Config is what the instance decides about site publishing. The folder is never
// taken from a request: only the administrator sets it.
type Config struct {
	// Enabled is the instance switch (SCRIOBH_SITE=off turns the whole feature
	// off: no preview, no build, no publish).
	Enabled bool
	// Root is the folder sites are published under, each in <slug>--<id6>/public.
	// Empty means publishing to a folder is off; building a zip still works.
	Root string
}

// Service holds the configuration and the per-project locks of running publishes.
type Service struct {
	cfg   Config
	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// NewService makes the service the server registers.
func NewService(cfg Config) *Service { return &Service{cfg: cfg, locks: map[string]*sync.Mutex{}} }

func (s *Service) lock(projectID string) *sync.Mutex {
	s.mu.Lock()
	defer s.mu.Unlock()
	l := s.locks[projectID]
	if l == nil {
		l = &sync.Mutex{}
		s.locks[projectID] = l
	}
	return l
}

// Hold takes a project's publish lock until the function it returns is called:
// what a publish does, for a test (or a caller) that must act while one runs.
func (s *Service) Hold(projectID string) (release func()) {
	l := s.lock(projectID)
	l.Lock()
	return l.Unlock
}

func configOf(env *ops.Env) (*Service, Config) {
	if s, ok := ops.Service[*Service](env, ServiceName); ok && s != nil {
		return s, s.cfg
	}
	return nil, Config{Enabled: true} // no server around (a test, the schema): build is on, publish is off
}

func enabled(c *ops.Ctx) error {
	if _, cfg := configOf(c.Env); !cfg.Enabled {
		return ops.Conflict("Static site publishing is switched off on this server (SCRIOBH_SITE=off).")
	}
	return nil
}

// Register adds the site ops.
func Register(r *ops.Registry) {
	ed := ops.RequireProject(ops.ProjectEditor)
	// What becomes public, and the act of making it so, is a person's decision, as a share link is: an
	// API key of any scope (and so the MCP endpoint) is refused for the ops that change the site's
	// options or documents or publish it. Reading, previewing and building the zip stay open to a key
	// that may read or write the project, because none of them makes anything public.
	edSession := ed.ForSessions()
	ops.Register(r, ops.Op{Name: "site.options.get", Method: "GET", Path: "/api/v1/projects/{id}/site/options", Scope: ops.ScopeRead,
		Desc: "The project's site options (title, tagline, base address, language, posts per page, feed, navigation, accent, ...), with the defaults and the accents on offer. Editor and above.",
		Auth: ed}, optionsGet)
	ops.Register(r, ops.Op{Name: "site.options.set", Method: "PATCH", Path: "/api/v1/projects/{id}/site/options",
		Desc: "Change some of the site's options. Send only what changes in options; name fields in options.clear to return them to their default. Nothing is published by this. Editor and above, browser session only: an API key cannot change what the site will make public.",
		Auth: edSession}, optionsSet)
	ops.Register(r, ops.Op{Name: "site.doc.list", Method: "GET", Path: "/api/v1/projects/{id}/site/documents", Scope: ops.ScopeRead,
		Desc: "Every document that has site settings of its own: role (auto, post, page, skip), address, publication date, excerpt, tags. Documents with none are not listed; site.preview shows what every document becomes.",
		Auth: ed}, docList)
	ops.Register(r, ops.Op{Name: "site.doc.set", Method: "PATCH", Path: "/api/v1/projects/{id}/site/documents/{document_id}",
		Desc: "Set how one document appears on the site: role (auto, post, page or skip), an address (slug), a publication date, an excerpt and up to 10 tags. Send only what changes; name fields in clear to remove them. Addresses are unique in the project. Editor and above, browser session only: an API key cannot change what the site will make public.",
		Auth: edSession}, docSet)
	ops.Register(r, ops.Op{Name: "site.preview", Method: "POST", Path: "/api/v1/projects/{id}/site/preview", Scope: ops.ScopeRead,
		Desc: "What would be published, without writing anything: the posts (newest first) and pages with their addresses, dates, reading time and excerpts, the documents left out and why, warnings (no excerpt, no date, an image with no description, a duplicate address, no base address), counts, and the input hash a build or publish of the same state would have. The response holds the start of each post's text, so it is never kept for idempotent replay.",
		Auth: ed, SecretOutput: true}, heavy(preview))
	ops.Register(r, ops.Op{Name: "site.build", Method: "POST", Path: "/api/v1/projects/{id}/site/build", Status: 200,
		Desc: "Build the site and download it as a zip: HTML pages, listings, tag and category pages, RSS and Atom feeds and a sitemap (with a base address), a stylesheet and the images the posts use, with no script and nothing loaded from elsewhere. Private notes, ghosted text, alternatives, front matter, documents marked do not compile, notes, research, comments and the Compendium are never in it. Deterministic and recorded with a manifest of the documents and hashes; run site.preview first.",
		Auth: ed}, heavy(build))
	ops.Register(r, ops.Op{Name: "site.publish", Method: "POST", Path: "/api/v1/projects/{id}/site/publish", Status: 200, NoDryRun: true,
		Desc: "Publish the site to the folder the server administrator configured (SCRIOBH_SITE_DIR, or SCRIOBH_SITE_PUBLISH=1 for the data directory's sites folder), for a web server to serve. The folder is never chosen by the request. It replaces the previous publish as a whole, touches only a folder it made (anything else there is refused, never deleted) and refuses links. Explicit: nothing is ever published automatically. Not previewable; use site.preview. Editor and above, browser session only: publishing makes text public, as a share link does, so an API key of any scope cannot do it.",
		Auth: edSession}, heavy(publishOp))
	ops.Register(r, ops.Op{Name: "site.status", Method: "GET", Path: "/api/v1/projects/{id}/site", Scope: ops.ScopeRead,
		Desc: "The site's status: whether building and publishing to a folder are available on this server, the last build and the last publish (time, counts, hashes), and where it was published (relative to the server's sites folder). Compare input_hash with site.preview's to see whether anything changed since.",
		Auth: ed}, status)
	r.Env().AddPurgeHook(func(ctx context.Context, projectID string) {
		_, cfg := configOf(r.Env())
		if cfg.Root == "" {
			return
		}
		if svc, _ := configOf(r.Env()); svc != nil {
			l := svc.lock(projectID)
			l.Lock() // a publish that is writing finishes first, and none starts under the removal
			defer l.Unlock()
		}
		name, err := sitegen.FindDir(cfg.Root, projectID)
		if err != nil || name == "" {
			return
		}
		if err := sitegen.RemoveDir(cfg.Root, name, projectID); err != nil && r.Env().Log != nil {
			r.Env().Log.Warn("site: could not remove a purged project's published site", "error", err.Error())
		}
	})
}

// ---- options ------------------------------------------------------------------------------

// OptionsGetIn names the project.
type OptionsGetIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef names the project.
func (in OptionsGetIn) ProjectRef() string { return in.ProjectID }

// OptionsOut is a project's site options.
type OptionsOut struct {
	Options   sitegen.Options `json:"options"`
	Defaults  sitegen.Options `json:"defaults"`
	Accents   []string        `json:"accents"`
	UpdatedAt string          `json:"updated_at,omitempty"`
}

func optionsGet(c *ops.Ctx, in OptionsGetIn) (OptionsOut, error) {
	o, at, err := loadOptions(c, c.DB(), in.ProjectID)
	if err != nil {
		return OptionsOut{}, ops.Internal(err)
	}
	return OptionsOut{Options: o, Defaults: sitegen.Defaults(), Accents: sitegen.AccentNames, UpdatedAt: at}, nil
}

// OptionsSetIn changes some options.
type OptionsSetIn struct {
	ProjectID string        `path:"id" json:"-" desc:"Project id."`
	Options   sitegen.Patch `json:"options" desc:"The options to change; options.clear names the ones to return to their default."`
}

// ProjectRef names the project.
func (in OptionsSetIn) ProjectRef() string { return in.ProjectID }

func fieldErr(err error) error {
	var fe *sitegen.FieldError
	if errors.As(err, &fe) {
		return ops.Invalid(fe.Field, fe.Message)
	}
	return err
}

func optionsSet(c *ops.Ctx, in OptionsSetIn) (OptionsOut, error) {
	cur, _, err := loadOptions(c, c.DB(), in.ProjectID)
	if err != nil {
		return OptionsOut{}, ops.Internal(err)
	}
	next, err := cur.Apply(in.Options)
	if err != nil {
		return OptionsOut{}, fieldErr(err)
	}
	if err := next.Validate(); err != nil {
		return OptionsOut{}, fieldErr(err)
	}
	raw, _ := json.Marshal(next)
	now := db.Now()
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		if _, err := tx.Exec(`INSERT INTO site_options(project_id, options_json, updated_at) VALUES (?,?,?)
			ON CONFLICT(project_id) DO UPDATE SET options_json = excluded.options_json, updated_at = excluded.updated_at`, in.ProjectID, string(raw), now); err != nil {
			return err
		}
		c.Audit(ops.AuditEntry{Action: "site.options.set", TargetType: "project", TargetID: in.ProjectID})
		return c.FlushTo(tx)
	})
	if err != nil {
		return OptionsOut{}, ops.Internal(err)
	}
	return OptionsOut{Options: next, Defaults: sitegen.Defaults(), Accents: sitegen.AccentNames, UpdatedAt: now}, nil
}

// ---- documents ----------------------------------------------------------------------------

// DocListIn names the project.
type DocListIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef names the project.
func (in DocListIn) ProjectRef() string { return in.ProjectID }

// DocListOut lists site settings.
type DocListOut struct {
	Items []DocRow `json:"items" desc:"By document id."`
}

func docList(c *ops.Ctx, in DocListIn) (DocListOut, error) {
	rows, err := loadRows(c, c.DB(), in.ProjectID)
	if err != nil {
		return DocListOut{}, ops.Internal(err)
	}
	return DocListOut{Items: sortedRows(rows)}, nil
}

// DocSetIn changes one document's site settings.
type DocSetIn struct {
	ProjectID   string    `path:"id" json:"-" desc:"Project id."`
	DocumentID  string    `path:"document_id" json:"-" ref:"document" desc:"The document."`
	Role        *string   `json:"role,omitempty" enum:"auto,post,page,skip"`
	Slug        *string   `json:"slug,omitempty" desc:"An address: letters and digits of any script and hyphens (it is made so if it is not), up to 60 bytes, unique in the project."`
	PublishedAt *string   `json:"published_at,omitempty" desc:"A date (2026-03-01) or a time (RFC 3339)."`
	Excerpt     *string   `json:"excerpt,omitempty" desc:"Up to 300 characters, plain text."`
	Tags        *[]string `json:"tags,omitempty" desc:"Up to 10, each up to 40 characters. Replaces the list."`
	Clear       []string  `json:"clear,omitempty" desc:"Fields to remove: role, slug, published_at, excerpt, tags, pinned_slug."`
}

// ProjectRef names the project.
func (in DocSetIn) ProjectRef() string { return in.ProjectID }

// DocItem wraps one document's settings.
type DocItem struct {
	Item DocRow `json:"item"`
}

func docSet(c *ops.Ctx, in DocSetIn) (DocItem, error) {
	var kind string
	err := c.DB().Read().QueryRowContext(c, `SELECT kind FROM documents WHERE id = ? AND project_id = ? AND deleted_at IS NULL AND compendium_id IS NULL`, in.DocumentID, in.ProjectID).Scan(&kind)
	if errors.Is(err, sql.ErrNoRows) {
		return DocItem{}, ops.NotFound("document")
	}
	if err != nil {
		return DocItem{}, ops.Internal(err)
	}
	if kind == "research" {
		return DocItem{}, ops.Invalid("document_id", "Research documents are never published.")
	}
	var out DocItem
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		var r docRow
		var tags string
		err := tx.QueryRow(`SELECT role, slug, published_at, excerpt, tags_json, pinned_slug FROM site_docs WHERE document_id = ? AND project_id = ?`, in.DocumentID, in.ProjectID).
			Scan(&r.Role, &r.Slug, &r.Published, &r.Excerpt, &tags, &r.Pinned)
		if errors.Is(err, sql.ErrNoRows) {
			r = docRow{Role: "auto"}
			tags = "[]"
		} else if err != nil {
			return err
		}
		_ = json.Unmarshal([]byte(tags), &r.Tags)
		for _, name := range in.Clear {
			switch name {
			case "role":
				r.Role = "auto"
			case "slug":
				r.Slug = ""
			case "published_at":
				r.Published = ""
			case "excerpt":
				r.Excerpt = ""
			case "tags":
				r.Tags = nil
			case "pinned_slug":
				r.Pinned = ""
			default:
				return ops.Invalid("clear", fmt.Sprintf("%q is not a field to clear.", name))
			}
		}
		if in.Role != nil {
			switch *in.Role {
			case "auto", "post", "page", "skip":
				r.Role = *in.Role
			default:
				return ops.Invalid("role", "Use auto, post, page or skip.")
			}
		}
		if in.Slug != nil {
			if strings.TrimSpace(*in.Slug) == "" {
				r.Slug = ""
			} else {
				s := sitegen.Slugify(*in.Slug)
				if s == "" {
					return ops.Invalid("slug", "That address has no letters or digits to make a slug of.")
				}
				r.Slug = s
			}
		}
		if in.PublishedAt != nil {
			p, err := ParsePublished(*in.PublishedAt)
			if err != nil {
				return ops.Invalid("published_at", err.Error())
			}
			r.Published = p
		}
		if in.Excerpt != nil {
			ex := publish.CleanLine(*in.Excerpt)
			if len([]rune(ex)) > 300 {
				return ops.Invalid("excerpt", "The excerpt is limited to 300 characters.")
			}
			r.Excerpt = ex
		}
		if in.Tags != nil {
			t, err := cleanTags(*in.Tags)
			if err != nil {
				return ops.Invalid("tags", err.Error())
			}
			r.Tags = t
		}
		key := ""
		if r.Slug != "" {
			key = strings.ToLower(r.Slug)
			var other string
			err := tx.QueryRow(`SELECT document_id FROM site_docs WHERE project_id = ? AND slug_key = ? AND document_id <> ?`, in.ProjectID, key, in.DocumentID).Scan(&other)
			if err == nil {
				return ops.Conflict("Another document already has that address.")
			}
			if !errors.Is(err, sql.ErrNoRows) {
				return err
			}
			// An address that has been out belongs to its document, even if the
			// document is a draft again.
			prows, err := tx.Query(`SELECT pinned_slug FROM site_docs WHERE project_id = ? AND document_id <> ? AND pinned_slug <> ''`, in.ProjectID, in.DocumentID)
			if err != nil {
				return err
			}
			defer prows.Close()
			for prows.Next() {
				var ps string
				if err := prows.Scan(&ps); err != nil {
					return err
				}
				if strings.EqualFold(ps, r.Slug) || sitegen.FoldName(ps) == sitegen.FoldName(r.Slug) {
					return ops.Conflict("Another document was given that address when the site was built or published, and keeps it.")
				}
			}
			if err := prows.Err(); err != nil {
				return err
			}
		}
		tj, _ := json.Marshal(append([]string{}, r.Tags...))
		now := db.Now()
		if _, err := tx.Exec(`INSERT INTO site_docs(document_id, project_id, role, slug, slug_key, published_at, excerpt, tags_json, updated_at, pinned_slug) VALUES (?,?,?,?,?,?,?,?,?,?)
			ON CONFLICT(document_id) DO UPDATE SET role=excluded.role, slug=excluded.slug, slug_key=excluded.slug_key, published_at=excluded.published_at,
			excerpt=excluded.excerpt, tags_json=excluded.tags_json, updated_at=excluded.updated_at, pinned_slug=excluded.pinned_slug`,
			in.DocumentID, in.ProjectID, r.Role, r.Slug, key, r.Published, r.Excerpt, string(tj), now, r.Pinned); err != nil {
			return err
		}
		r.UpdatedAt = now
		out.Item = toRow(in.DocumentID, r)
		c.Audit(ops.AuditEntry{Action: "site.doc.set", TargetType: "document", TargetID: in.DocumentID})
		return c.FlushTo(tx)
	})
	var oe *ops.Error
	if errors.As(err, &oe) {
		return DocItem{}, oe
	}
	if err != nil {
		return DocItem{}, ops.Internal(err)
	}
	return out, nil
}

// ---- resolving a site ---------------------------------------------------------------------

type resolved struct {
	src  *export.Source
	opts sitegen.Options
	sel  selection
	res  *sitegen.Result
	rows map[string]docRow
}

func resolve(c *ops.Ctx, projectID string) (*resolved, error) {
	if err := enabled(c); err != nil {
		return nil, err
	}
	src, err := export.LoadSource(c, c.DB(), projectID)
	if err != nil {
		return nil, err
	}
	opts, _, err := loadOptions(c, c.DB(), projectID)
	if err != nil {
		return nil, ops.Internal(err)
	}
	rows, err := loadRows(c, c.DB(), projectID)
	if err != nil {
		return nil, ops.Internal(err)
	}
	created, err := loadCreated(c, c.DB(), projectID)
	if err != nil {
		return nil, ops.Internal(err)
	}
	sel := choose(src, opts, rows, created)
	var imageNotes []sitegen.Warning
	p := src.Project
	updated := time.Time{}
	if t, err := db.Parse(p.UpdatedAt); err == nil {
		updated = t.UTC()
	}
	in := sitegen.Input{
		Project: sitegen.Project{ID: p.ID, Title: p.Title, Subtitle: p.Subtitle, Author: p.Author, Language: p.Language, Updated: updated},
		Options: opts, Entries: sel.entries,
		Resolve: func(dests []string) map[string]sitegen.Image {
			out := map[string]sitegen.Image{}
			got, ws := compile.ResolveImages(c, projectID, dests)
			for k, v := range got {
				out[k] = sitegen.Image{Data: v.Data, Mime: v.Mime, Ext: v.Ext, Width: v.Width, Height: v.Height, Rotated: v.Rotated}
			}
			for _, w := range ws {
				if w.Code == "image_skipped" { // too large or too many: the generator says only "could not be published"
					imageNotes = append(imageNotes, sitegen.Warning{Code: w.Code, Message: w.Message})
				}
			}
			return out
		},
	}
	// Addresses already given stay: they are pinned to their documents, and a
	// pinned address of a document that is not in this build is kept free.
	present := map[string]bool{}
	for i := range in.Entries {
		present[in.Entries[i].ID] = true
		in.Entries[i].PinnedSlug = rows[in.Entries[i].ID].Pinned
	}
	for id, r := range rows {
		if r.Pinned != "" && !present[id] {
			in.Reserved = append(in.Reserved, r.Pinned)
		}
	}
	sort.Strings(in.Reserved)
	if opts.ShowCover {
		_, in.CoverSVG = cover.Cached(cover.Spec{Title: p.Title, Subtitle: p.Subtitle, Author: p.Author, Kind: p.Kind, Color: p.CoverColor})
	}
	res, err := sitegen.Build(in)
	switch {
	case errors.Is(err, sitegen.ErrNoPosts):
		return nil, ops.Invalid("", "There is nothing to publish: no post is selected. Chapters are posts; check their status (only done is published unless include_drafts is on), whether they are marked do not compile, and the project's selection rule (options.posts).")
	case errors.Is(err, sitegen.ErrTooMany), errors.Is(err, sitegen.ErrTooLarge):
		return nil, ops.TooLarge()
	case err != nil:
		var fe *sitegen.FieldError
		if errors.As(err, &fe) {
			return nil, ops.Invalid(fe.Field, fe.Message)
		}
		return nil, ops.Internal(err)
	}
	res.Warnings = append(res.Warnings, imageNotes...)
	if sel.drafts > 0 {
		res.Warnings = append(res.Warnings, sitegen.Warning{Code: "drafts_included", Message: fmt.Sprintf("%d document(s) still marked draft or revise are published because include_drafts is on.", sel.drafts)})
	}
	return &resolved{src: src, opts: opts, sel: sel, res: res, rows: rows}, nil
}

// pin keeps the address each published document has now: the first time a site
// is built or published its addresses are written down, and from then on the
// generator uses them before it makes any new one.
func pin(tx *sql.Tx, projectID string, r *resolved) error {
	now := db.Now()
	for _, p := range r.res.Posts {
		row := r.rows[p.DocumentID]
		if p.SlugFrom == "set" || p.Slug == row.Pinned {
			continue
		}
		if _, err := tx.Exec(`INSERT INTO site_docs(document_id, project_id, role, tags_json, updated_at, pinned_slug) VALUES (?,?, 'auto', '[]', ?, ?)
			ON CONFLICT(document_id) DO UPDATE SET pinned_slug = excluded.pinned_slug, updated_at = excluded.updated_at`, p.DocumentID, projectID, now, p.Slug); err != nil {
			return err
		}
	}
	return nil
}

// ---- preview ------------------------------------------------------------------------------

// PreviewIn names the project.
type PreviewIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef names the project.
func (in PreviewIn) ProjectRef() string { return in.ProjectID }

// PreviewOut is what a build or publish of the same state would produce.
type PreviewOut struct {
	Posts      []sitegen.PostInfo `json:"posts" desc:"Newest first."`
	Pages      []sitegen.PostInfo `json:"pages" desc:"In reading order."`
	Excluded   []Excluded         `json:"excluded" desc:"Documents left out, with the reason. Notes are not listed (they are never published); parts are containers."`
	Warnings   []sitegen.Warning  `json:"warnings"`
	Tags       int                `json:"tags"`
	Categories int                `json:"categories"`
	Files      int                `json:"files"`
	Bytes      int                `json:"bytes"`
	Words      int                `json:"words"`
	Feeds      bool               `json:"feeds" desc:"Whether RSS, Atom and a sitemap would be written (they need a base address)."`
	BaseURL    string             `json:"base_url"`
	InputHash  string             `json:"input_hash"`
	SiteHash   string             `json:"site_hash"`
	Notes      int                `json:"notes_skipped" desc:"Documents of kind note: never published."`
}

func split(r *resolved) (posts, pages []sitegen.PostInfo, words int) {
	for _, p := range r.res.Posts {
		words += p.Words
		if p.Role == sitegen.RolePage {
			pages = append(pages, p)
		} else {
			posts = append(posts, p)
		}
	}
	return
}

func preview(c *ops.Ctx, in PreviewIn) (PreviewOut, error) {
	r, err := resolve(c, in.ProjectID)
	if err != nil {
		return PreviewOut{}, err
	}
	posts, pages, words := split(r)
	return PreviewOut{Posts: nonNilP(posts), Pages: nonNilP(pages), Excluded: nonNil(r.sel.excluded), Warnings: nonNilW(r.res.Warnings), Tags: r.res.Tags, Categories: r.res.Cats,
		Files: len(r.res.Files), Bytes: r.res.Bytes, Words: words, Feeds: r.opts.BaseURL != "", BaseURL: r.opts.BaseURL, InputHash: r.res.InputHash,
		SiteHash: r.res.Hash, Notes: r.sel.notes}, nil
}

func nonNilP(p []sitegen.PostInfo) []sitegen.PostInfo {
	if p == nil {
		return []sitegen.PostInfo{}
	}
	return p
}

func nonNil(e []Excluded) []Excluded {
	if e == nil {
		return []Excluded{}
	}
	return e
}

func nonNilW(w []sitegen.Warning) []sitegen.Warning {
	if w == nil {
		return []sitegen.Warning{}
	}
	return w
}

// ---- build --------------------------------------------------------------------------------

// BuildIn names the project.
type BuildIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef names the project.
func (in BuildIn) ProjectRef() string { return in.ProjectID }

// BuildOut is the built site. Over REST it is the zip itself; elsewhere it is
// this JSON, with the zip base64-encoded in body.
type BuildOut struct {
	ExportID    string            `json:"export_id"`
	Filename    string            `json:"filename"`
	ContentType string            `json:"content_type"`
	Bytes       int               `json:"bytes" desc:"The size of the zip."`
	SHA256      string            `json:"sha256" desc:"The zip's checksum."`
	Posts       int               `json:"posts"`
	Pages       int               `json:"pages"`
	Files       int               `json:"files"`
	SiteHash    string            `json:"site_hash" desc:"A hash of every file and its path."`
	InputHash   string            `json:"input_hash" desc:"The same value site.preview gives for the same state."`
	Warnings    []sitegen.Warning `json:"warnings"`
	Body        []byte            `json:"body" desc:"The zip. The REST route returns it as the response body instead of JSON."`
}

// Download makes BuildOut an ops.Download.
func (o BuildOut) Download() (string, string, []byte) { return o.Filename, o.ContentType, o.Body }

func heldOf(r *resolved) []export.Held {
	out := make([]export.Held, len(r.res.Docs))
	for i, d := range r.res.Docs {
		out[i] = export.Held{ID: d.ID, SHA256: d.SHA256}
	}
	return out
}

func counts(r *resolved) (posts, pages int) {
	for _, p := range r.res.Posts {
		if p.Role == sitegen.RolePage {
			pages++
		} else {
			posts++
		}
	}
	return
}

func build(c *ops.Ctx, in BuildIn) (BuildOut, error) {
	r, err := resolve(c, in.ProjectID)
	if err != nil {
		return BuildOut{}, err
	}
	body, err := sitegen.Zip(r.res.Files, r.res.Modified)
	if err != nil {
		return BuildOut{}, ops.Internal(err)
	}
	posts, pages := counts(r)
	name := projects.Slugify(r.src.Project.Title) + "-site.zip"
	file := export.NewFile(name, "application/zip", body, len(r.res.Docs))
	var excluded []string
	for _, e := range r.sel.excluded {
		excluded = append(excluded, e.ID)
	}
	manifest := map[string]any{"kind": "site", "input_hash": r.res.InputHash, "site_hash": r.res.Hash, "posts": posts, "pages": pages, "files": len(r.res.Files),
		"feeds": r.opts.BaseURL != "", "include_drafts": r.opts.IncludeDrafts, "posts_rule": r.opts.Posts, "warnings": len(r.res.Warnings), "not_published": excluded}
	audit := map[string]any{"kind": "site", "posts": posts, "pages": pages, "files": len(r.res.Files), "site_hash": r.res.Hash, "warnings": len(r.res.Warnings)}
	if err := export.Record(c, in.ProjectID, "site", "html", file, heldOf(r), manifest, audit); err != nil {
		return BuildOut{}, err
	}
	st := Stamp{At: db.Now(), SiteHash: r.res.Hash, InputHash: r.res.InputHash, Posts: posts, Files: len(r.res.Files), Bytes: r.res.Bytes}
	if err := c.DB().WithTx(c, func(tx *sql.Tx) error {
		if err := pin(tx, in.ProjectID, r); err != nil {
			return err
		}
		return saveStamp(tx, in.ProjectID, "build", st, "")
	}); err != nil {
		return BuildOut{}, ops.Internal(err)
	}
	return BuildOut{ExportID: file.ExportID, Filename: file.Filename, ContentType: file.ContentType, Bytes: file.Bytes, SHA256: file.SHA256, Posts: posts, Pages: pages,
		Files: len(r.res.Files), SiteHash: r.res.Hash, InputHash: r.res.InputHash, Warnings: nonNilW(r.res.Warnings), Body: body}, nil
}

// ---- publish ------------------------------------------------------------------------------

// PublishIn names the project.
type PublishIn struct {
	ProjectID       string `path:"id" json:"-" desc:"Project id."`
	OverwriteEdited bool   `json:"overwrite_edited,omitempty" desc:"Replace files that were changed by hand in the published folder since the last publish. Without it, such a file stops the publish (409, naming it)."`
}

// ProjectRef names the project.
func (in PublishIn) ProjectRef() string { return in.ProjectID }

// PublishOut says what was published.
type PublishOut struct {
	Location    string            `json:"location" desc:"Where it is, relative to the server's sites folder: <folder>/public. The administrator points the web server there."`
	PublishedAt string            `json:"published_at"`
	Posts       int               `json:"posts"`
	Pages       int               `json:"pages"`
	Files       int               `json:"files"`
	Bytes       int               `json:"bytes"`
	Replaced    bool              `json:"replaced" desc:"A previous publish was replaced."`
	SiteHash    string            `json:"site_hash"`
	InputHash   string            `json:"input_hash"`
	Warnings    []sitegen.Warning `json:"warnings"`
}

func publishOp(c *ops.Ctx, in PublishIn) (PublishOut, error) {
	svc, cfg := configOf(c.Env)
	if !cfg.Enabled {
		return PublishOut{}, ops.Conflict("Static site publishing is switched off on this server (SCRIOBH_SITE=off).")
	}
	if cfg.Root == "" || svc == nil {
		return PublishOut{}, ops.Conflict("Publishing to a folder is not enabled on this server. The administrator sets SCRIOBH_SITE_DIR to a folder (or SCRIOBH_SITE_PUBLISH=1 to use the sites folder of the data directory); until then, build the site and download the zip.")
	}
	l := svc.lock(in.ProjectID)
	if !l.TryLock() {
		return PublishOut{}, ops.Conflict("A publish of this project is already running.")
	}
	defer l.Unlock()
	var trashed bool
	if err := c.DB().Read().QueryRowContext(c, `SELECT trashed_at IS NOT NULL FROM projects WHERE id = ?`, in.ProjectID).Scan(&trashed); err != nil {
		return PublishOut{}, ops.Internal(err)
	}
	if trashed {
		return PublishOut{}, ops.Conflict("A project in the trash cannot be published. Restore it first. (A site published earlier stays where it is until the project is deleted for good.)")
	}
	r, err := resolve(c, in.ProjectID)
	if err != nil {
		return PublishOut{}, err
	}
	st, err := loadState(c, c.DB(), in.ProjectID)
	if err != nil {
		return PublishOut{}, ops.Internal(err)
	}
	var slug string
	if err := c.DB().Read().QueryRowContext(c, `SELECT slug FROM projects WHERE id = ?`, in.ProjectID).Scan(&slug); err != nil {
		return PublishOut{}, ops.Internal(err)
	}
	name := st.Dir
	if name == "" {
		if name, err = sitegen.FindDir(cfg.Root, in.ProjectID); err != nil {
			return PublishOut{}, ops.Internal(err)
		}
	}
	if name == "" {
		name = sitegen.DirName(slug, in.ProjectID)
	}
	wr, err := sitegen.WriteDirWith(cfg.Root, name, in.ProjectID, r.res.Hash, r.res.Files, sitegen.DirOptions{OverwriteEdited: in.OverwriteEdited})
	var um *sitegen.ErrUnmanaged
	var ed *sitegen.ErrEdited
	switch {
	case errors.As(err, &um):
		return PublishOut{}, ops.Conflict(um.Error())
	case errors.As(err, &ed):
		return PublishOut{}, ops.Conflict(ed.Error())
	case errors.Is(err, sitegen.ErrUnsafe):
		return PublishOut{}, ops.Conflict("The publish folder is not safe to write to (it, or something in it, is a link or belongs to another project). Nothing was changed.")
	case err != nil:
		if c.Env.Log != nil {
			c.Env.Log.Error("site: publish failed", "project", in.ProjectID, "error", err.Error())
		}
		return PublishOut{}, ops.Conflict("The site could not be written: " + sitegen.Reason(err) + ". Nothing was changed.")
	}
	posts, pages := counts(r)
	now := db.Now()
	stamp := Stamp{At: now, SiteHash: r.res.Hash, InputHash: r.res.InputHash, Posts: posts, Files: wr.Files, Bytes: wr.Bytes}
	err = c.DB().WithTx(c, func(tx *sql.Tx) error {
		if err := pin(tx, in.ProjectID, r); err != nil {
			return err
		}
		if err := saveStamp(tx, in.ProjectID, "publish", stamp, wr.Name); err != nil {
			return err
		}
		c.Audit(ops.AuditEntry{Action: "site.publish", TargetType: "project", TargetID: in.ProjectID,
			Detail: map[string]any{"posts": posts, "pages": pages, "files": wr.Files, "bytes": wr.Bytes, "site_hash": r.res.Hash, "input_hash": r.res.InputHash, "replaced": wr.Replaced}})
		return c.FlushTo(tx)
	})
	if err != nil {
		return PublishOut{}, ops.Internal(err)
	}
	return PublishOut{Location: wr.Name + "/public", PublishedAt: now, Posts: posts, Pages: pages, Files: wr.Files, Bytes: wr.Bytes, Replaced: wr.Replaced,
		SiteHash: r.res.Hash, InputHash: r.res.InputHash, Warnings: nonNilW(r.res.Warnings)}, nil
}

// ---- status -------------------------------------------------------------------------------

// StatusIn names the project.
type StatusIn struct {
	ProjectID string `path:"id" json:"-" desc:"Project id."`
}

// ProjectRef names the project.
func (in StatusIn) ProjectRef() string { return in.ProjectID }

// StatusOut is the site's status.
type StatusOut struct {
	Enabled        bool   `json:"enabled" desc:"Building is available on this server (SCRIOBH_SITE is not off)."`
	PublishEnabled bool   `json:"publish_enabled" desc:"Publishing to a folder is available: the administrator set SCRIOBH_SITE_DIR or SCRIOBH_SITE_PUBLISH=1."`
	Location       string `json:"location,omitempty" desc:"Where it was published, relative to the sites folder."`
	BaseURL        string `json:"base_url"`
	OptionsSet     bool   `json:"options_set" desc:"The project has saved site options."`
	Build          Stamp  `json:"build"`
	Publish        Stamp  `json:"publish"`
}

func status(c *ops.Ctx, in StatusIn) (StatusOut, error) {
	_, cfg := configOf(c.Env)
	st, err := loadState(c, c.DB(), in.ProjectID)
	if err != nil {
		return StatusOut{}, ops.Internal(err)
	}
	o, at, err := loadOptions(c, c.DB(), in.ProjectID)
	if err != nil {
		return StatusOut{}, ops.Internal(err)
	}
	out := StatusOut{Enabled: cfg.Enabled, PublishEnabled: cfg.Enabled && cfg.Root != "", BaseURL: o.BaseURL, OptionsSet: at != "", Build: st.Build, Publish: st.Publish}
	if st.Dir != "" {
		out.Location = st.Dir + "/public"
	}
	return out, nil
}
