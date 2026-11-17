package compile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/crueber/scriobh/internal/cover"
	"github.com/crueber/scriobh/internal/domain/export"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
)

func registerCover(r *ops.Registry) {
	ed := ops.RequireProject(ops.ProjectEditor)
	ops.Register(r, ops.Op{Name: "compile.cover_spec", Method: "POST", Path: "/api/v1/projects/{id}/compile/cover-spec", Scope: ops.ScopeRead,
		Desc: "The measurements of a paperback cover for a print preset: the page count (exact, from the same layout the PDF export runs, or one you give), the spine width from the paper's thickness per page (the printer's figure: it is required, because a guess is a cover that does not wrap), the bleed and the full wrap width and height, and where the back, spine and front panels sit. Writes nothing.",
		Auth: ed}, heavy(coverSpec))
	ops.Register(r, ops.Op{Name: "export.cover", Method: "POST", Path: "/api/v1/projects/{id}/export/cover", Status: 200,
		Desc: "A print cover as a PDF of two pages: the full wrap (back, spine and front with bleed; a flat colour from the project's cover colour with white type, no gradient and no image) and the front on its own. The spine is set only when it is wide enough for type. Optional guides (trim, spine and safe-area lines) and a white box for the barcode. Deterministic and recorded. This is a typographic cover, not a designed one; check it against your printer's template.",
		Auth: ed}, heavy(exportCover))
}

// CoverIn names the book and the paper.
type CoverIn struct {
	ProjectID       string          `path:"id" json:"-" desc:"Project id."`
	Preset          string          `json:"preset,omitempty" desc:"A print preset (print-6x9, print-5x8, ...); its trim size and layout give the page count. Default print-6x9."`
	Options         publish.Options `json:"options,omitempty" desc:"Options that override the preset, as for export.compile."`
	PageCount       int             `json:"page_count,omitempty" desc:"Use this page count instead of the layout's (2 to 1,500). Printers print leaves, so give an even number."`
	PageThicknessIn float64         `json:"page_thickness_in" desc:"The paper's thickness per page in inches (0.0005 to 0.01), from your printer's cover template or guide. Required: the spine is the page count times this."`
	BleedIn         *float64        `json:"bleed_in,omitempty" desc:"Bleed in inches, 0 to 0.5 (default 0.125)."`
	Guides          bool            `json:"guides,omitempty" desc:"Draw dashed trim, spine and safe-area lines on the art (for checking; turn off for the file you send)."`
	Barcode         bool            `json:"barcode,omitempty" desc:"A white box for the barcode on the back (2 x 1.2 in)."`
}

// ProjectRef names the project.
func (in CoverIn) ProjectRef() string { return in.ProjectID }

// Validate bounds the numbers.
func (in *CoverIn) Validate() error {
	if err := optionsError(in.Options.Validate()); err != nil {
		return err
	}
	if in.PageCount != 0 && (in.PageCount < 2 || in.PageCount > 1500) {
		return ops.Invalid("page_count", "page_count is 2 to 1,500.")
	}
	t := in.PageThicknessIn
	if math.IsNaN(t) || t < 0.0005 || t > 0.01 {
		return ops.Invalid("page_thickness_in", "page_thickness_in is the paper's thickness per page in inches, 0.0005 to 0.01: take it from your printer's cover guide.")
	}
	if in.BleedIn != nil && (math.IsNaN(*in.BleedIn) || *in.BleedIn < 0 || *in.BleedIn > 0.5) {
		return ops.Invalid("bleed_in", "bleed_in is 0 to 0.5 inches.")
	}
	return nil
}

// Panel is a horizontal span of the wrap, in points from its left edge.
type Panel struct {
	X0 float64 `json:"x0"`
	X1 float64 `json:"x1"`
}

// CoverSpecOut is the geometry of the cover.
type CoverSpecOut struct {
	Preset       string    `json:"preset"`
	PageCount    int       `json:"page_count"`
	PageSource   string    `json:"page_count_source" enum:"layout,given"`
	TrimWidthIn  float64   `json:"trim_width_in"`
	TrimHeightIn float64   `json:"trim_height_in"`
	BleedIn      float64   `json:"bleed_in"`
	SpineIn      float64   `json:"spine_in"`
	SpineHasText bool      `json:"spine_has_text" desc:"The spine is wide enough for type (a quarter inch or more)."`
	WrapWidthIn  float64   `json:"wrap_width_in" desc:"Back, spine and front with bleed on both outer edges."`
	WrapHeightIn float64   `json:"wrap_height_in"`
	Back         Panel     `json:"back" desc:"In points from the left edge of the wrap (bleed included)."`
	Spine        Panel     `json:"spine"`
	Front        Panel     `json:"front"`
	Warnings     []Warning `json:"warnings"`
}

type coverGeo struct {
	spec  publish.CoverSpec
	out   CoverSpecOut
	r     *resolved
	pages int
}

func coverGeometry(c *ops.Ctx, in CoverIn) (*coverGeo, error) {
	preset := in.Preset
	if preset == "" {
		preset = Print6x9
	}
	r, err := resolve(c, in.ProjectID, preset, in.Options, false)
	if err != nil {
		return nil, err
	}
	if r.opts.Format != "pdf" {
		return nil, ops.Invalid("preset", "A cover needs a print preset (print-6x9, print-5.5x8.5, print-5x8, print-a5) for the trim size and the page count.")
	}
	w, h, ok := publish.PageSize(r.opts.PageSize)
	if !ok {
		return nil, ops.Invalid("options.page_size", "The preset has no trim size.")
	}
	g := &coverGeo{r: r}
	pages, source := in.PageCount, "given"
	if pages == 0 {
		if r.b.empty {
			return nil, ops.Invalid("", "There is nothing to compile, so there is no page count: give page_count.")
		}
		if err := tooBigForPDF(gateOf(c), r.b.words); err != nil {
			return nil, err
		}
		l, err := publish.LayoutPDFContext(c, r.b.book, r.opts)
		if errors.Is(err, publish.ErrRTL) {
			return nil, ops.Invalid("", err.Error())
		}
		if err != nil {
			return nil, ops.Internal(err)
		}
		if t := l.Truncated(); len(t) > 0 {
			return nil, ops.Invalid("", "The book is larger than the layout will set ("+strings.Join(t, "; ")+"): give page_count.")
		}
		pages, source = l.PageCount(), "layout"
	}
	g.pages = pages
	bleed := 0.125
	if in.BleedIn != nil {
		bleed = *in.BleedIn
	}
	bleedM := mpt(math.Round(bleed * 72000))
	thick := mpt(in.PageThicknessIn*72000*1000 + 0.5) // thousandths of a milli-point per page: keep the precision
	spine := mpt(pages) * thick / 1000
	p := r.src.Project
	cs := cover.Spec{Title: p.Title, Subtitle: p.Subtitle, Author: p.Author, Kind: p.Kind, Color: p.CoverColor}.Clean()
	g.spec = publish.CoverSpec{Title: cs.Title, Subtitle: cs.Subtitle, Author: firstNonEmpty(r.b.book.Byline, cs.Author), Kind: cs.Kind, Color: cs.Color,
		TrimW: w, TrimH: h, Spine: spine, Bleed: bleedM, Guides: in.Guides, Barcode: in.Barcode,
		Language: r.b.book.Language, Identifier: r.b.book.Identifier, Modified: r.b.book.Modified}
	in2 := func(v mpt) float64 { return float64(v) / 72000 }
	pt := func(v mpt) float64 { return float64(v) / 1000 }
	o := CoverSpecOut{Preset: r.preset.ID, PageCount: pages, PageSource: source, TrimWidthIn: in2(w), TrimHeightIn: in2(h), BleedIn: in2(bleedM), SpineIn: in2(spine),
		SpineHasText: spine >= publish.MinSpineText, WrapWidthIn: in2(2*bleedM + 2*w + spine), WrapHeightIn: in2(h + 2*bleedM),
		Back: Panel{pt(bleedM), pt(bleedM + w)}, Spine: Panel{pt(bleedM + w), pt(bleedM + w + spine)}, Front: Panel{pt(bleedM + w + spine), pt(bleedM + 2*w + spine)}, Warnings: []Warning{}}
	if pages%2 == 1 {
		o.Warnings = append(o.Warnings, Warning{Code: "odd_page_count", Message: "The page count is odd; printers print leaves of two pages, so add a blank page."})
	}
	if !o.SpineHasText {
		o.Warnings = append(o.Warnings, Warning{Code: "spine_narrow", Message: fmt.Sprintf("The spine is %.3f in wide, too narrow for type (about a quarter of an inch is needed).", o.SpineIn)})
	}
	if pages < 24 {
		o.Warnings = append(o.Warnings, Warning{Code: "few_pages", Message: fmt.Sprintf("%d pages is under the 24 that print-on-demand printers commonly ask for.", pages)})
	}
	g.out = o
	return g, nil
}

type mpt = int64

func coverSpec(c *ops.Ctx, in CoverIn) (CoverSpecOut, error) {
	g, err := coverGeometry(c, in)
	if err != nil {
		return CoverSpecOut{}, err
	}
	return g.out, nil
}

func exportCover(c *ops.Ctx, in CoverIn) (Out, error) {
	g, err := coverGeometry(c, in)
	if err != nil {
		return Out{}, err
	}
	sum := sha256.New()
	fmt.Fprintf(sum, "scriobh-cover/1\x00%s\x00%d\x00%d\x00%d\x00%s\x00%v\x00%v\x00%s\x00%s\x00%s\x00%s\x00%s", g.r.hash, g.spec.Spine, g.spec.Bleed, g.pages, g.spec.Color, g.spec.Guides, g.spec.Barcode, g.spec.Title, g.spec.Subtitle, g.spec.Author, g.spec.Kind, g.r.src.Project.UpdatedAt)
	g.spec.InputHash = hex.EncodeToString(sum.Sum(nil))
	body, ws, err := publish.WriteCoverPDF(g.spec)
	if err != nil {
		return Out{}, ops.Internal(err)
	}
	warnings := append([]Warning{}, g.out.Warnings...)
	for _, w := range ws {
		warnings = append(warnings, Warning{Code: w.Code, Message: w.Message})
	}
	name := projects.Slugify(g.r.src.Project.Title) + "-cover.pdf"
	file := export.NewFile(name, "application/pdf", body, len(g.r.b.held))
	manifest := map[string]any{"preset_id": "cover/" + g.r.preset.ID, "input_hash": g.spec.InputHash, "pages": g.pages, "spine_in": g.out.SpineIn, "bleed_in": g.out.BleedIn,
		"page_thickness_in": in.PageThicknessIn, "guides": in.Guides, "barcode": in.Barcode, "warnings": len(warnings)}
	if err := export.Record(c, in.ProjectID, "cover/"+g.r.preset.ID, "pdf", file, g.r.b.held, manifest, map[string]any{"preset_id": "cover/" + g.r.preset.ID, "pages": g.pages}); err != nil {
		return Out{}, err
	}
	return Out{ExportID: file.ExportID, Filename: file.Filename, ContentType: file.ContentType, Bytes: file.Bytes, SHA256: file.SHA256, Documents: file.Documents,
		Preset: "cover/" + g.r.preset.ID, Format: "pdf", Words: g.r.b.words, InputHash: g.spec.InputHash, Warnings: warnings, Body: file.Body}, nil
}
