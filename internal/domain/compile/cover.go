package compile

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"strings"

	"github.com/crueber/scriobh/internal/cover"
	"github.com/crueber/scriobh/internal/domain/coverlayout"
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
		Desc: "A print cover as a PDF. The default file is exactly one page, the full wrap with bleed, which is what KDP and IngramSpark take (part front is the front on its own, part proof the proof). With no cover layout (or own_cover false) it is the generated cover: a flat colour from the project's cover colour with white type, no gradient and no image, in any language. With the project's own layout (project.cover_layout.get) it is made from the layout: an image side fills its panel to the bleed, cropped from the centre, a page side lays its picture on a page of the trim by its fit, scale, position, background and margin, a generated side keeps the typographic drawing, a back left out is the cover colour, and a wrap picture is the whole first page at its measured size with nothing drawn over it; part proof is a separate file, the proof (the wrap at half size with the trim, bleed, spine folds, text-safe margin and barcode box over it, and a legend: never send it to a printer), and part front the front alone. The layout's trim must be the preset's (cover_trim_mismatch refuses otherwise). Pictures go in at full resolution (a JPEG as it is), in RGB: a picture's own RGB colour profile goes with it, anything else is DeviceRGB, and a printer that wants CMYK converts. profile (kdp, ingram, generic) sets the text-safe margin, barcode box and the page count from which the spine carries type. The spine is set only when it is wide enough for type and the book has the profile's pages. Warnings: image_low_resolution (under 285 dpi where the picture prints; print asks for 300, and a picture made at 300 for the trim lands at about 288 once it covers the bleed), cover_print_cropped (an image side loses over 5 per cent of its picture to fill the panel and bleed), cover_wrap_stale (the wrap's spine is over 1/32 in from the book's), cover_margin_crossed, cover_bleed_missing, cover_barcode_area, spine_narrow, spine_few_pages, spine_rtl, cover_profile_not_srgb. Optional guides (trim, spine and safe-area lines) and a white box for the barcode. A right-to-left book needs page_count; its layout may not have a generated side (cover_rtl_text refuses the file, compile.cover_spec still measures it). Deterministic and recorded. Check it against your printer's template.",
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
	Barcode         bool            `json:"barcode,omitempty" desc:"A white box for the barcode on the lower right of the back, the profile's size (2 x 1.2 in for kdp and generic, 1.75 x 1 in for ingram)."`
	Part            string          `json:"part,omitempty" enum:"cover,front,proof" desc:"Which file: cover (the default) is the one page a printer takes, the whole wrap, named <title>-cover.pdf; front is the front alone at trim plus bleed (<title>-cover-front.pdf); proof is the proof page, the wrap at half size with the cut lines over it (<title>-cover-proof.pdf), for a cover with a layout, never for a printer. Each is recorded as its own export."`
	Profile         string          `json:"profile,omitempty" enum:"kdp,ingram,generic" desc:"The printer's figures for the text-safe margin, the barcode box and the page count from which the spine has type. Default generic. With no layout the cover keeps its own margin and barcode place unless a profile is named."`
}

// ProjectRef names the project.
func (in CoverIn) ProjectRef() string { return in.ProjectID }

// Validate bounds the numbers.
func (in *CoverIn) Validate() error {
	if err := optionsError(in.Options.Validate()); err != nil {
		return err
	}
	if in.PageCount != 0 && (in.PageCount < 2 || in.PageCount > 1500) {
		return invalidWith("page_count", "page_count is 2 to 1,500.", map[string]any{"reason": "cover_page_count_range"})
	}
	t := in.PageThicknessIn
	if math.IsNaN(t) || t < 0.0005 || t > 0.01 {
		return invalidWith("page_thickness_in", "page_thickness_in is the paper's thickness per page in inches, 0.0005 to 0.01: take it from your printer's cover guide.", map[string]any{"reason": "cover_thickness_range"})
	}
	if in.BleedIn != nil && (math.IsNaN(*in.BleedIn) || *in.BleedIn < 0 || *in.BleedIn > 0.5) {
		return invalidWith("bleed_in", "bleed_in is 0 to 0.5 inches.", map[string]any{"reason": "cover_bleed_range"})
	}
	switch in.Part {
	case "", publish.PartCover, publish.PartFront, publish.PartProof:
	default:
		return invalidWith("part", "part is cover, front or proof.", map[string]any{"reason": "cover_part"})
	}
	if _, ok := publish.LookupCoverProfile(in.Profile); !ok {
		return invalidWith("profile", "profile is kdp, ingram or generic.", map[string]any{"reason": "cover_profile"})
	}
	return nil
}

// Panel is a horizontal span of the wrap, in points from its left edge.
type Panel struct {
	X0 float64 `json:"x0"`
	X1 float64 `json:"x1"`
}

// Box is a rectangle in points from the left and bottom edges of the wrap.
type Box struct {
	X0 float64 `json:"x0"`
	Y0 float64 `json:"y0"`
	X1 float64 `json:"x1"`
	Y1 float64 `json:"y1"`
}

// CoverSpecOut is the geometry of the cover.
type CoverSpecOut struct {
	Preset       string          `json:"preset"`
	PageCount    int             `json:"page_count"`
	PageSource   string          `json:"page_count_source" enum:"layout,given"`
	TrimWidthIn  float64         `json:"trim_width_in"`
	TrimHeightIn float64         `json:"trim_height_in"`
	BleedIn      float64         `json:"bleed_in"`
	SpineIn      float64         `json:"spine_in"`
	SpineHasText bool            `json:"spine_has_text" desc:"The spine carries type: it is a quarter inch or more, the book has the profile's pages, and the cover is not a wrap or right to left."`
	SpineTextWhy string          `json:"spine_text_why,omitempty" enum:"narrow,few_pages,wrap,rtl" desc:"When spine_has_text is false: why."`
	Profile      string          `json:"profile" enum:"kdp,ingram,generic" desc:"The profile in force (generic by default)."`
	TextMarginIn float64         `json:"text_margin_in" desc:"Text and important art stay this far inside the trim (the profile's; a generated cover with no profile named keeps 0.125 in in its guides)."`
	BarcodeBox   Box             `json:"barcode_box" desc:"The barcode box on the lower right of the back, in points from the left and bottom edges of the wrap."`
	Layout       *CoverLayoutOut `json:"layout,omitempty" desc:"Present when the cover is made from the project's own layout."`
	WrapWidthIn  float64         `json:"wrap_width_in" desc:"Back, spine and front with bleed on both outer edges."`
	WrapHeightIn float64         `json:"wrap_height_in"`
	Back         Panel           `json:"back" desc:"In points from the left edge of the wrap (bleed included)."`
	Spine        Panel           `json:"spine"`
	Front        Panel           `json:"front"`
	Warnings     []Warning       `json:"warnings"`
}

// CoverLayoutOut says what the layout comes to as a print cover.
type CoverLayoutOut struct {
	Shape           string            `json:"shape" enum:"sides,wrap"`
	Trim            string            `json:"trim"`
	Proof           bool              `json:"proof" desc:"The PDF has a third page, the proof."`
	MeasuredSpineIn *float64          `json:"measured_spine_in,omitempty" desc:"A wrap's spine as measured from its picture."`
	Stale           bool              `json:"stale,omitempty" desc:"A wrap whose measured spine is over 1/32 in from the book's: cover_wrap_stale."`
	WrapWidthIn     float64           `json:"wrap_width_in" desc:"The first page's width: for a wrap, the picture's measured size, whatever the book's spine."`
	WrapHeightIn    float64           `json:"wrap_height_in"`
	Pictures        []CoverPictureOut `json:"pictures" desc:"Each placed picture and its resolution where it prints."`
}

// CoverPictureOut is one placed picture.
type CoverPictureOut struct {
	Part     string  `json:"part" enum:"front,back,wrap"`
	DPI      float64 `json:"dpi"`
	PrintWIn float64 `json:"print_width_in"`
	PrintHIn float64 `json:"print_height_in"`
}

type coverGeo struct {
	spec  publish.CoverSpec
	out   CoverSpecOut
	r     *resolved
	pages int
	lay   *coverlayout.Layout
	art   *coverlayout.Art
	rep   *publish.CoverReport
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
		return nil, invalidWith("preset", "A cover needs a print preset (print-6x9, print-5.5x8.5, print-5x8, print-a5) for the trim size and the page count.", map[string]any{"reason": "cover_preset_not_print"})
	}
	w, h, ok := publish.PageSize(r.opts.PageSize)
	if !ok {
		return nil, invalidWith("options.page_size", "The preset has no trim size.", map[string]any{"reason": "cover_no_trim"})
	}
	g := &coverGeo{r: r}
	pages, source := in.PageCount, "given"
	if pages == 0 {
		if r.b.empty {
			return nil, invalidWith("page_count", "There is nothing to compile, so there is no page count: give page_count.", map[string]any{"reason": "cover_page_count_needed"})
		}
		if err := tooBigForPDF(gateOf(c), r.b.words); err != nil {
			return nil, err
		}
		l, err := publish.LayoutPDFContext(c, r.b.book, r.opts)
		if errors.Is(err, publish.ErrRTL) {
			return nil, invalidWith("page_count", "The page count of a right-to-left book cannot be taken from this program's PDF layout, which sets text left to right: give page_count, from the printer's count or the book's own layout.", map[string]any{"reason": "cover_page_count_rtl"})
		}
		if err != nil {
			return nil, ops.Internal(err)
		}
		if t := l.Truncated(); len(t) > 0 {
			return nil, invalidWith("page_count", "The book is larger than the layout will set ("+strings.Join(t, "; ")+"): give page_count.", map[string]any{"reason": "cover_book_too_large"})
		}
		pages, source = l.PageCount(), "layout"
	}
	g.pages = pages
	// The project's own cover, unless own_cover is off (the generated cover then, as for the EPUB).
	if publish.On(r.opts.OwnCover) {
		lay, art, err := coverlayout.ForPrint(c, c.DB().Read(), in.ProjectID, r.opts.Language)
		if err != nil {
			return nil, err
		}
		if lay != nil {
			if lay.Trim != r.opts.PageSize {
				return nil, coverTrimMismatch(lay.Trim, r.opts.PageSize)
			}
			g.lay, g.art = lay, art
		}
	}
	bleed := 0.125
	if in.BleedIn != nil {
		bleed = *in.BleedIn
	} else if g.lay != nil && g.lay.Shape == coverlayout.ShapeWrap {
		bleed = g.lay.Wrap.Bleed() // a wrap has the bleed it was made with
	}
	bleedM := mpt(math.Round(bleed * 72000))
	thick := mpt(in.PageThicknessIn*72000*1000 + 0.5) // thousandths of a milli-point per page: keep the precision
	spine := mpt(pages) * thick / 1000
	p := r.src.Project
	cs := cover.Spec{Title: p.Title, Subtitle: p.Subtitle, Author: p.Author, Kind: p.Kind, Color: p.CoverColor}.Clean()
	prof, _ := publish.LookupCoverProfile(in.Profile)
	rtl := publish.IsRTL(firstNonEmpty(r.opts.Language, r.b.book.Language, "en"))
	g.spec = publish.CoverSpec{Title: cs.Title, Subtitle: cs.Subtitle, Author: firstNonEmpty(r.b.book.Byline, cs.Author), Kind: cs.Kind, Color: cs.Color,
		TrimW: w, TrimH: h, Spine: spine, Bleed: bleedM, Guides: in.Guides, Barcode: in.Barcode,
		Language: r.b.book.Language, Identifier: r.b.book.Identifier, Modified: r.b.book.Modified,
		Profile: prof, ProfileGiven: in.Profile != "", Pages: pages, RTL: rtl}
	if g.art != nil {
		g.art.ApplyTo(&g.spec)
	}
	rep, err := publish.PlanCover(g.spec)
	if err != nil {
		return nil, invalidWith("", err.Error(), map[string]any{"reason": "cover_plan"})
	}
	g.rep = rep
	in2 := func(v mpt) float64 { return float64(v) / 72000 }
	pt := func(v mpt) float64 { return float64(v) / 1000 }
	textMargin := prof.TextMarginIn
	if g.lay == nil && in.Profile == "" {
		textMargin = 0.125 // the generated cover's own guides, until a profile is named
	}
	back, front := Panel{pt(bleedM), pt(bleedM + w)}, Panel{pt(bleedM + w + spine), pt(bleedM + 2*w + spine)}
	if rtl && g.lay != nil { // a right-to-left book has its front on the left of the wrap (a generated cover has never turned)
		back, front = front, back
	}
	o := CoverSpecOut{Preset: r.preset.ID, PageCount: pages, PageSource: source, TrimWidthIn: in2(w), TrimHeightIn: in2(h), BleedIn: in2(bleedM), SpineIn: in2(spine),
		SpineHasText: rep.SpineType, SpineTextWhy: rep.SpineTypeWhy, Profile: prof.Name, TextMarginIn: textMargin,
		BarcodeBox:  Box{rep.BarcodeBox[0], rep.BarcodeBox[1], rep.BarcodeBox[2], rep.BarcodeBox[3]},
		WrapWidthIn: in2(2*bleedM + 2*w + spine), WrapHeightIn: in2(h + 2*bleedM),
		Back: back, Spine: Panel{pt(bleedM + w), pt(bleedM + w + spine)}, Front: front, Warnings: []Warning{}}
	if pages%2 == 1 {
		o.Warnings = append(o.Warnings, Warning{Code: "odd_page_count", Message: "The page count is odd; printers print leaves of two pages, so add a blank page."})
	}
	if pages < 24 {
		o.Warnings = append(o.Warnings, Warning{Code: "few_pages", Message: fmt.Sprintf("%d pages is under the 24 that print-on-demand printers commonly ask for.", pages)})
	}
	if g.lay != nil {
		lo := &CoverLayoutOut{Shape: g.lay.Shape, Trim: g.lay.Trim, Proof: true, WrapWidthIn: rep.WrapWIn, WrapHeightIn: rep.WrapHIn, Pictures: []CoverPictureOut{}}
		if g.lay.Shape == coverlayout.ShapeWrap {
			m := rep.SpineIn
			lo.MeasuredSpineIn = &m
		}
		for _, pc := range rep.Pictures {
			lo.Pictures = append(lo.Pictures, CoverPictureOut{Part: pc.Part, DPI: math.Round(pc.DPI*100) / 100, PrintWIn: math.Round(pc.PrintWIn*1000) / 1000, PrintHIn: math.Round(pc.PrintHIn*1000) / 1000})
		}
		for _, w := range rep.Warnings {
			if w.Code == "cover_wrap_stale" {
				lo.Stale = true
			}
		}
		o.Layout = lo
	}
	for _, w := range rep.Warnings {
		o.Warnings = append(o.Warnings, Warning{Code: w.Code, Message: w.Message, Part: w.Part})
	}
	g.out = o
	return g, nil
}

func invalidWith(field, msg string, data map[string]any) *ops.Error {
	return &ops.Error{Code: ops.CodeInvalid, Field: field, Message: msg, Data: data}
}

// coverTrimMismatch refuses a print cover (or a PDF's cover page) made for another trim than the layout's: the layout
// is made again for the new trim, never stretched.
func coverTrimMismatch(layoutTrim, pageSize string) error {
	return invalidWith("", fmt.Sprintf("The cover layout is made for %s but this print preset is %s. Make the layout again for %s on the Cover page (a picture is never stretched), or turn own_cover off to use the generated cover.", layoutTrim, pageSize, pageSize),
		map[string]any{"reason": "cover_trim_mismatch", "layout_trim": layoutTrim, "page_size": pageSize})
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
	if g.rep.Refuse != nil {
		return Out{}, invalidWith("", "A generated side sets its type left to right, which a right-to-left book cannot have. Use a picture for the front and the back (or a wrap) on the Cover page.", map[string]any{"reason": "cover_rtl_text"})
	}
	part := in.Part
	if part == "" {
		part = publish.PartCover
	}
	if part == publish.PartProof && g.lay == nil {
		return Out{}, invalidWith("part", "A cover with no layout has no proof: the proof is of the project's own cover layout.", map[string]any{"reason": "cover_no_proof"})
	}
	g.spec.Part = part
	sum := sha256.New()
	fmt.Fprintf(sum, "scriobh-cover/1\x00%s\x00%d\x00%d\x00%d\x00%s\x00%v\x00%v\x00%s\x00%s\x00%s\x00%s\x00%s", g.r.hash, g.spec.Spine, g.spec.Bleed, g.pages, g.spec.Color, g.spec.Guides, g.spec.Barcode, g.spec.Title, g.spec.Subtitle, g.spec.Author, g.spec.Kind, g.r.src.Project.UpdatedAt)
	// What the layout and the profile add to the file is in the hash only when there is a layout or a profile was named:
	// a cover with neither is the file, and the hash, it always was. The layout's own hash holds its pictures' hashes.
	if g.lay != nil {
		fmt.Fprintf(sum, "\x00layout\x00%s", g.lay.PrintHash(g.spec.RTL))
	}
	if g.lay != nil || in.Profile != "" {
		fmt.Fprintf(sum, "\x00profile\x00%s\x00%d", g.out.Profile, g.spec.Pages)
	}
	if part != publish.PartCover { // the one page a printer takes keeps the hash it has; the others are other files
		fmt.Fprintf(sum, "\x00part\x00%s", part)
	}
	g.spec.InputHash = hex.EncodeToString(sum.Sum(nil))
	var more []Warning
	if g.art != nil && c.DryRun() {
		g.spec.NoPictures = true // a dry run has no data directory: the pictures are not read, and grey boxes show where they go
	} else if g.art != nil {
		cws, err := g.art.Load(c, c.Env, in.ProjectID)
		if err != nil {
			return Out{}, coverlayout.Refused(err)
		}
		for _, w := range cws {
			more = append(more, Warning{Code: w.Code, Message: w.Message, Part: w.Part})
		}
		// A PNG the writer has to decode (an alpha channel, 16 bits, interlacing) takes its share of the memory every cover
		// decode shares; a JPEG and a plain PNG are passed through and take none.
		release, err := g.art.HoldDecode(c)
		if err != nil {
			return Out{}, err
		}
		defer release()
	}
	body, ws, err := publish.WriteCoverPDF(g.spec)
	if errors.Is(err, publish.ErrCoverPicture) {
		return Out{}, invalidWith("", err.Error(), map[string]any{"reason": "cover_picture_unusable"})
	}
	if err != nil {
		return Out{}, ops.Internal(err)
	}
	warnings := append([]Warning{}, g.out.Warnings...)
	warnings = append(warnings, more...)
	for _, w := range ws { // the writer's are the plan's again (already here, with their parts), with the typesetter's (a missing glyph)
		dup := false
		for _, x := range warnings {
			dup = dup || x.Code == w.Code && x.Message == w.Message
		}
		if !dup {
			warnings = append(warnings, Warning{Code: w.Code, Message: w.Message})
		}
	}
	name := projects.Slugify(g.r.src.Project.Title) + "-cover.pdf"
	if part != publish.PartCover {
		name = projects.Slugify(g.r.src.Project.Title) + "-cover-" + part + ".pdf"
	}
	file := export.NewFile(name, "application/pdf", body, len(g.r.b.held))
	manifest := map[string]any{"preset_id": "cover/" + g.r.preset.ID, "input_hash": g.spec.InputHash, "pages": g.pages, "spine_in": g.out.SpineIn, "bleed_in": g.out.BleedIn,
		"page_thickness_in": in.PageThicknessIn, "guides": in.Guides, "barcode": in.Barcode, "warnings": len(warnings), "part": part}
	if g.lay != nil {
		manifest["layout"], manifest["layout_shape"], manifest["profile"] = g.lay.Hash, g.lay.Shape, g.out.Profile
	}
	if err := export.Record(c, in.ProjectID, "cover/"+g.r.preset.ID, "pdf", file, g.r.b.held, manifest, map[string]any{"preset_id": "cover/" + g.r.preset.ID, "pages": g.pages}); err != nil {
		return Out{}, err
	}
	return Out{ExportID: file.ExportID, Filename: file.Filename, ContentType: file.ContentType, Bytes: file.Bytes, SHA256: file.SHA256, Documents: file.Documents,
		Preset: "cover/" + g.r.preset.ID, Format: "pdf", Words: g.r.b.words, InputHash: g.spec.InputHash, Warnings: warnings, Body: file.Body}, nil
}

// coverPage prepares the PDF's cover_page option: the book's PrintCover, which is the layout's front when the project has
// one with a front picture and own_cover is on, and the generated front otherwise. It returns what the cover page adds
// to the input hash ("" when the front is the generated one, which the hash holds already through the title, author,
// kind and colour) and, when the pictures were read, what to call once the file is written. The layout's trim must be the
// PDF's page size: a front made for another page is refused, never stretched.
func coverPage(c *ops.Ctx, projectID string, src *export.Source, opts publish.Options, b *built, readPictures bool) (hash string, release func(), err error) {
	p := src.Project
	cs := cover.Spec{Title: p.Title, Subtitle: p.Subtitle, Author: p.Author, Kind: p.Kind, Color: p.CoverColor}.Clean()
	spec := &publish.CoverSpec{Title: cs.Title, Subtitle: cs.Subtitle, Author: firstNonEmpty(b.book.Byline, cs.Author), Kind: cs.Kind, Color: cs.Color,
		Language: b.book.Language, Identifier: b.book.Identifier, Modified: b.book.Modified}
	if publish.On(opts.OwnCover) {
		fh, lay, err := coverlayout.FrontHashOf(c, c.DB().Read(), projectID, opts.Language)
		if err != nil {
			return "", nil, err
		}
		if fh != "" {
			if lay.Trim != opts.PageSize {
				return "", nil, coverTrimMismatch(lay.Trim, opts.PageSize)
			}
			_, art, err := coverlayout.ForPrint(c, c.DB().Read(), projectID, opts.Language)
			if err != nil {
				return "", nil, err
			}
			hash = fh
			art.ApplyTo(spec)
			if readPictures {
				cws, err := art.Load(c, c.Env, projectID)
				if err != nil {
					return "", nil, coverlayout.Refused(err)
				}
				for _, w := range cws { // a profile that is not sRGB, or one that was left out
					b.warnings = append(b.warnings, Warning{Code: w.Code, Message: w.Message, Part: w.Part})
				}
				if release, err = art.HoldDecode(c); err != nil {
					return "", nil, err
				}
			}
		}
	}
	b.book.PrintCover = spec
	return hash, release, nil
}
