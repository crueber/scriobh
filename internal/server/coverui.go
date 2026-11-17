package server

import (
	"context"
	"errors"
	"io"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
)

// The cover page: /p/{slug}/cover, where a project's own cover is chosen (docs/COVERS.md, "The cover page").
//
// It is one ordinary form. The pictures are chosen in the same form that holds the rest, so a person without script
// uploads and saves with one button, and with script a chosen or dropped file submits the form at once and the server
// answers with the draft (the picture added, nothing saved). Every change is an op: the file goes to
// project.cover_layout.upload, the layout to project.cover_layout.set, the way back to project.cover_layout.clear.
// The words of every refusal and warning come from the catalogue by the op's stable code; the English sentences the
// ops carry are never shown.

// registerCoverUI adds the cover page and its routes.
func (s *Server) registerCoverUI(m *http.ServeMux) {
	m.HandleFunc("GET /p/{slug}/cover", s.coverPage)
	s.uiRouteStream("POST /ui/projects/{id}/cover", s.coverAuthorize, s.coverBusy, s.uiCover)
	s.uiRoute("POST /ui/projects/{id}/cover/clear", s.uiCoverClear)
}

// isCoverForm says whether a path is the cover page's form, which may carry pictures (see bodyLimit).
func isCoverForm(p string) bool {
	rest, ok := strings.CutPrefix(p, "/ui/projects/")
	if !ok {
		return false
	}
	id, tail, ok := strings.Cut(rest, "/")
	return ok && id != "" && tail == "cover"
}

// coverPaperInPerPage is the thickness of the paper the spine is stated in pages for: KDP's white paper. It only
// turns a measured width into a page count a writer can picture; the printer's own template stays authoritative.
const coverPaperInPerPage = 0.002252

var coverColours = []string{"Moss", "Walnut", "Harbour", "Plum", "Fern", "Heather", "Ochre", "Peat"}

// coverPic is a cover picture as the page needs it: the file to show and its size.
type coverPic struct {
	ID, URL string
	W, H    int
	Bytes   int64
	DPI     float64
	CropPct int // the share an image side cuts away, whole percent
}

// coverSide is a side of the draft, as the form shows it.
type coverSide struct {
	Part   string // front or back
	Mode   string
	Att    string
	Fit    string
	Scale  string
	Pos    string
	Bg     string
	Margin string
	Pic    *coverPic
}

// coverSvg is the front as a picture on a page, drawn by the same arithmetic as the server's own derivative
// (coverlayout.PlacePage): what Save will make, before it is made.
type coverSvg struct {
	CW, CH         int
	X, Y, W, H     int
	BX, BY, BW, BH int
	Page           bool
	Href           string
	Class          string
}

type coverTrim struct {
	Value, Label string
	W, H         string
	Selected     bool
}

type coverNote struct {
	Part, Text string
}

type coverPos struct {
	Value, Label string
	Checked      bool
}

type coverSwatch struct {
	Hex, Class, Name string
	Checked          bool
}

// coverVersion is a version of the cover in a few facts, to set two side by side when a save meets another's.
type coverVersion struct {
	Shape, Trim, Alt, Thumb string
	Shown                   bool
}

// coverBox is a rectangle in a picture's pixels.
type coverBox struct{ X, Y, W, H int }

func boxOf(x0, y0, x1, y1 int) coverBox { return coverBox{x0, y0, x1 - x0, y1 - y0} }

// inset is the box drawn m pixels inside itself.
func (b coverBox) inset(m int) coverBox {
	return coverBox{b.X + m, b.Y + m, max(b.W-2*m, 0), max(b.H-2*m, 0)}
}

// coverWrapView is a wrap as the drawing needs it: the picture, its measured figures and the guides, all in the
// picture's own pixels.
type coverWrapView struct {
	Att, URL     string
	PW, PH       int
	DPI          float64
	WidthIn      string
	SpineIn      string
	SpinePages   int
	BleedIn      string
	FrontOn      string // left or right
	Measured     bool
	FrontPreview string // the viewBox of the front cut from the picture
	Cut          coverBox
	MarginBack   coverBox
	MarginFront  coverBox
	Fold0, Fold1 int
	FS           int // the drawing's label size
	LabelY       int
	Labels       [3]coverLabel
}

// coverLabel names a panel of the wrap on the drawing: its centre and whether it is the spine.
type coverLabel struct {
	Part string // back, spine or front
	X    int
	Show bool
}

type coverPageData struct {
	Project                     projects.Project
	Slug                        string
	CSRF                        string
	CanEdit                     bool
	Has                         bool // a layout is saved
	Revision                    int
	Shape                       string // generated, sides or wrap
	Trim                        string
	TrimW                       string
	TrimH                       string
	TrimWord                    string // the trim in words, for a sentence
	Trims                       []coverTrim
	Front                       coverSide
	Back                        coverSide
	Wrap                        coverWrapView
	Bleed                       string
	Alt                         string
	Dirty                       bool // what the form shows is not what is saved
	Flash                       string
	Error                       string
	ErrorFor                    string // the id of the control the error is about
	Conflict                    bool
	Yours, Saved                *coverVersion // on a conflict: the writer's draft and the layout someone else saved
	Warnings                    []coverNote
	Notes                       []coverNote
	Palette                     []coverSwatch
	Positions                   []coverPos
	FrontSvg                    *coverSvg
	FrontURL, ThumbURL, BackURL string
	CoverClass                  string
	SavedAlt                    string
	Shown                       bool // the saved layout has a picture on its front
	Kind                        string
	KindLabel                   string // the kind of book in words, from the catalogue
	MaxPicture                  string
	Messages                    map[string]string
	Paper                       string
}

// PicShown says whether the front's preview is a picture (the saved derivative, or a draft drawn on its page).
func (d coverPageData) PicShown() bool {
	return d.FrontSvg != nil || (d.Dirty && d.Shape == coverlayout.ShapeWrap && d.Wrap.Measured) || (d.Shown && !d.Dirty)
}

// ---- reading ---------------------------------------------------------------------------------------------

func (s *Server) coverLayout(r *http.Request, id string) (*coverlayout.View, error) {
	out, err := s.Reg.Call(r.Context(), s.principal(r), "project.cover_layout.get", coverlayout.ProjectIn{ID: id})
	if err != nil {
		return nil, err
	}
	return out.(coverlayout.Item).Layout, nil
}

func fnum(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func trimLabel(r *http.Request, s *Server, name string, w, h float64) string {
	switch name {
	case "a5", "a4":
		return strings.ToUpper(name)
	case "letter":
		return s.tr(r, "cover.trim_letter")
	}
	return s.tr(r, "cover.trim_in", "w", fnum(w), "h", fnum(h))
}

// coverDefaults is a project that has no layout, or the side of a draft that has not been given one.
func defaultSide(part string, proj projects.Project) coverSide {
	mode := "generated"
	if part == "back" {
		mode = "none"
	}
	return coverSide{Part: part, Mode: mode, Fit: coverlayout.FitContain, Scale: "100", Pos: "center", Bg: strings.ToUpper(proj.CoverColor), Margin: "0"}
}

func sideFrom(part string, sd *coverlayout.Side, proj projects.Project) coverSide {
	out := defaultSide(part, proj)
	if sd == nil || sd.Mode == "" {
		return out
	}
	out.Mode, out.Att = sd.Mode, sd.Attachment
	if sd.Mode == coverlayout.ModePage {
		out.Fit, out.Scale, out.Pos, out.Bg, out.Margin = sd.Fit, strconv.Itoa(sd.Scale), sd.Position, sd.Background, fnum(sd.MarginIn)
	}
	return out
}

// draftOf is the form's starting point: the saved layout, or the generated cover.
func draftOf(v *coverlayout.View, proj projects.Project) coverPageData {
	d := coverPageData{Project: proj, Slug: proj.Slug, Shape: "generated", Trim: coverlayout.DefaultTrim, Bleed: fnum(coverlayout.DefaultBleedIn),
		Front: defaultSide("front", proj), Back: defaultSide("back", proj)}
	if v == nil {
		return d
	}
	d.Has, d.Revision, d.Shape, d.Trim, d.Alt = true, v.Revision, v.Shape, v.Trim, v.Alt
	if v.Shape == coverlayout.ShapeWrap && v.Wrap != nil {
		d.Wrap.Att = v.Wrap.Attachment
		d.Bleed = fnum(v.Wrap.Bleed())
	} else {
		d.Front, d.Back = sideFrom("front", v.Front, proj), sideFrom("back", v.Back, proj)
	}
	return d
}

// ---- the sentences -----------------------------------------------------------------------------------------

// kindLabel is a kind of book in words (novel, short story), from the catalogue; a kind the catalogue does not know
// keeps the name the projects package gives it.
func (s *Server) kindLabel(r *http.Request, kind string) string {
	if k := "kind." + kind; i18n.Default.Has(k) {
		return s.tr(r, k)
	}
	for _, o := range projects.KindOptions() {
		if o.Value == kind {
			return o.Label
		}
	}
	return kind
}

// part is a part of the cover in words.
func (s *Server) coverPart(r *http.Request, part string) string {
	if k := "cover.part." + part; i18n.Default.Has(k) {
		return s.tr(r, k)
	}
	return s.tr(r, "cover.part.cover")
}

// coverWarning is the sentence for a warning of the layout or of an upload, by its code, with the figures the picture
// information carries. A code the page does not know gets a general sentence that names it.
func (s *Server) coverWarning(r *http.Request, w coverlayout.Warning, pics []coverlayout.PictureInfo, trim string) string {
	var info *coverlayout.PictureInfo
	for i := range pics {
		if pics[i].Part == w.Part {
			info = &pics[i]
		}
	}
	part := s.coverPart(r, w.Part)
	tl := s.coverTrimWord(r, trim)
	switch w.Code {
	case "cover_low_resolution":
		if w.Part == "wrap" { // the wrap's figure is measured from its height with the bleed, not at the size a picture is placed
			if info != nil && info.DPI > 0 {
				return s.tr(r, "cover.warn.low_resolution_wrap", "dpi", strconv.Itoa(int(math.Round(info.DPI))), "trim", tl)
			}
			return s.tr(r, "cover.warn.low_resolution_wrap_plain", "trim", tl)
		}
		if info != nil && info.DPI > 0 {
			return s.tr(r, "cover.warn.low_resolution", "part", part, "dpi", strconv.Itoa(int(math.Round(info.DPI))), "trim", tl)
		}
		return s.tr(r, "cover.warn.low_resolution_plain", "part", part, "trim", tl)
	case "cover_cropped":
		if info != nil && info.CropLoss > 0 {
			return s.tr(r, "cover.warn.cropped", "part", part, "pct", strconv.Itoa(int(math.Round(info.CropLoss*100))), "trim", tl)
		}
		return s.tr(r, "cover.warn.cropped_plain", "part", part, "trim", tl)
	case "cover_picture_small":
		if info != nil && info.WidthPx > 0 {
			return s.tr(r, "cover.warn.small", "part", part, "w", strconv.Itoa(info.WidthPx), "h", strconv.Itoa(info.HeightPx), "trim", tl)
		}
		return s.tr(r, "cover.warn.small_plain", "part", part, "trim", tl)
	case "cover_profile_not_srgb":
		return s.tr(r, "cover.warn.profile", "part", part)
	case "cover_profile_dropped":
		return s.tr(r, "cover.warn.profile_dropped", "part", part)
	case "cover_picture_missing":
		return s.tr(r, "cover.warn.missing", "part", part)
	case "cover_wrap_low_dpi", "cover_wrap_spine_negative", "cover_wrap_spine_wide", "cover_wrap_empty", "cover_wrap_bleed":
		return s.tr(r, "cover.err."+strings.TrimPrefix(w.Code, "cover_"), "trim", tl, "bleed", "", "dpi", strconv.Itoa(coverlayout.MinWrapDPI), "spine", fnum(coverlayout.MaxSpineIn))
	case "cover_wrap_invalid":
		return s.tr(r, "cover.warn.wrap_invalid", "trim", tl)
	case "cover_wrap_stale":
		return s.tr(r, "cover.warn.stale")
	case "cover_trim_mismatch":
		return s.tr(r, "cover.warn.trim_mismatch")
	}
	return s.tr(r, "cover.warn.unknown", "code", w.Code)
}

func (s *Server) coverTrimWord(r *http.Request, name string) string {
	t, ok := coverlayout.LookupTrim(name)
	if !ok {
		return name
	}
	return trimLabel(r, s, name, t.W, t.H)
}

// coverRefusal is the sentence and the control for an error an op gave, by its stable reason, never its English.
func (s *Server) coverRefusal(r *http.Request, e *ops.Error, trim, bleed, part string) (text, target string) {
	reason, _ := e.Data["reason"].(string)
	args := []any{"part", s.coverPart(r, part), "trim", s.coverTrimWord(r, trim), "bleed", bleed,
		"px", strconv.Itoa(coverlayout.MaxCoverPixels / 1_000_000), "pngpx", strconv.Itoa(coverlayout.MaxPNGPixels / 1_000_000),
		"dpi", strconv.Itoa(coverlayout.MinWrapDPI), "spine", fnum(coverlayout.MaxSpineIn), "alt", strconv.Itoa(coverlayout.MaxAlt)}
	pic := part + "_file"
	switch {
	case e.Code == ops.CodeConflict && reason == ops.ReasonProjectTrashed:
		return s.tr(r, "cover.err.trashed"), ""
	case e.Code == ops.CodeConflict:
		return s.tr(r, "cover.err.conflict"), ""
	case e.Code == ops.CodeTooLarge:
		mb := 25
		if b, ok := e.Data["max_file_bytes"].(int64); ok && b > 0 {
			mb = int(b >> 20)
		}
		return s.tr(r, "cover.err.too_large", "mb", strconv.Itoa(mb)), pic
	case e.Code == ops.CodeQuota:
		return s.tr(r, "cover.err.quota"), pic
	case e.Code == ops.CodeRateLimited:
		return s.tr(r, "cover.err.busy"), pic
	case reason != "":
		key := "cover.err." + strings.TrimPrefix(reason, "cover_")
		if i18n.Default.Has(key) {
			switch {
			case reason == "cover_alt_required" || reason == "cover_alt_long":
				target = "cv-alt"
			case reason == "cover_wrap_bleed":
				target = "cv-bleed"
			case reason == "cover_trim":
				target = "cv-trim"
			case reason == "cover_scale", reason == "cover_margin", reason == "cover_margin_large", reason == "cover_fit", reason == "cover_position", reason == "cover_background":
				target = "cv-" + part + "-mode"
			case strings.HasPrefix(reason, "cover_picture") || strings.HasPrefix(reason, "cover_wrap"):
				target = pic
			}
			return s.tr(r, key, args...), target
		}
	case e.Field == "bleed_in":
		return s.tr(r, "cover.err.wrap_bleed", args...), "cv-bleed"
	case e.Field == "trim":
		return s.tr(r, "cover.err.trim", args...), "cv-trim"
	}
	return s.tr(r, "cover.err.other"), ""
}

// ---- the page --------------------------------------------------------------------------------------------

func (s *Server) coverCanEdit(proj projects.Project) bool {
	return proj.Role == ops.ProjectOwner || proj.Role == ops.ProjectEditor
}

// coverFill completes a draft with everything the page shows that is not a field: the trims, the swatches, the
// pictures' sizes, the measured wrap and the previews. v is the saved layout (nil when generated); up holds the
// pictures this request has just uploaded.
func (s *Server) coverFill(r *http.Request, d *coverPageData, v *coverlayout.View, up map[string]*coverlayout.UploadOut) {
	proj := d.Project
	d.CSRF = authFrom(r.Context()).CSRF
	d.CanEdit = s.coverCanEdit(proj)
	d.Kind, d.KindLabel = proj.Kind, s.kindLabel(r, proj.Kind)
	d.CoverClass = coverClass(proj.CoverColor)
	d.Paper = fnum(coverPaperInPerPage)
	d.MaxPicture = strconv.Itoa(s.Cfg.UploadMaxMB)
	trims := []string{}
	trims = append(trims, coverlayout.Trims...)
	for _, name := range trims {
		t, _ := coverlayout.LookupTrim(name)
		d.Trims = append(d.Trims, coverTrim{Value: name, Label: trimLabel(r, s, name, t.W, t.H), W: fnum(t.W), H: fnum(t.H), Selected: name == d.Trim})
	}
	tm, ok := coverlayout.LookupTrim(d.Trim)
	if !ok {
		tm, _ = coverlayout.LookupTrim(coverlayout.DefaultTrim)
	}
	d.TrimW, d.TrimH = fnum(tm.W), fnum(tm.H)
	d.TrimWord = trimLabel(r, s, tm.Name, tm.W, tm.H)
	for i, sw := range projects.Swatches() {
		d.Palette = append(d.Palette, coverSwatch{Hex: sw.Hex, Class: sw.Class, Name: s.tr(r, "cover.color."+strconv.Itoa(i)), Checked: strings.EqualFold(sw.Hex, d.Front.Bg)})
	}
	d.Positions = coverPositions(r, s, "")
	if v != nil {
		d.SavedAlt = v.Alt
		d.Shown = v.FrontURL != ""
		d.FrontURL, d.ThumbURL, d.BackURL = v.FrontURL, v.ThumbURL, v.BackURL
	}

	// what the pictures are: from the saved layout's measurements, from this request's uploads, else from the attachment
	known := map[string]*coverPic{}
	if v != nil {
		for _, p := range v.Pictures {
			cp := &coverPic{ID: p.Attachment, URL: "/files/" + p.Attachment, W: p.WidthPx, H: p.HeightPx, Bytes: p.Bytes, DPI: p.DPI}
			if p.CropLoss > 0 {
				cp.CropPct = int(math.Round(p.CropLoss * 100))
			}
			known[p.Attachment] = cp
		}
	}
	for part, u := range up {
		if u == nil {
			continue
		}
		cp := &coverPic{ID: u.AttachmentID, URL: "/files/" + u.AttachmentID, W: u.Width, H: u.Height, Bytes: u.Bytes, DPI: u.DPI}
		if part != "wrap" {
			cp.CropPct = int(math.Round(coverlayout.CropLoss(u.Width, u.Height, tm) * 100))
			if cp.CropPct < 1 {
				cp.CropPct = 0
			}
		}
		known[u.AttachmentID] = cp
	}
	find := func(id string) *coverPic {
		if id == "" {
			return nil
		}
		if p, ok := known[id]; ok {
			return p
		}
		out, err := s.Reg.Call(r.Context(), s.principal(r), "project.attachment.get", attachments.AttIn{ID: proj.ID, AttID: id})
		if err != nil {
			return nil
		}
		a := out.(attachments.Item).Item
		if a.Kind != "image" || a.Width <= 0 || a.Height <= 0 {
			return nil
		}
		cp := &coverPic{ID: a.ID, URL: "/files/" + a.ID, W: a.Width, H: a.Height, Bytes: a.Bytes}
		known[id] = cp
		return cp
	}
	d.Front.Pic, d.Back.Pic = find(d.Front.Att), find(d.Back.Att)

	if d.Shape == coverlayout.ShapeWrap && d.Wrap.Att != "" {
		d.Wrap.fill(r, s, d, v, find(d.Wrap.Att), up["wrap"])
	}

	// the front as a picture on a page, for a draft (a saved layout shows its own derivative)
	if d.Dirty {
		switch d.Shape {
		case coverlayout.ShapeSides:
			d.FrontSvg = frontSvg(d, tm)
		case coverlayout.ShapeWrap:
			// the front is cut from the wrap picture: the drawing's own preview
		}
	}
}

// fill measures a wrap: the saved layout's figures when it has them for this picture, else the same arithmetic the
// upload uses (coverlayout.MeasureWrap), so a picture that has only just been chosen is drawn too.
func (w *coverWrapView) fill(r *http.Request, s *Server, d *coverPageData, v *coverlayout.View, pic *coverPic, up *coverlayout.UploadOut) {
	if pic == nil {
		return
	}
	w.URL, w.PW, w.PH = pic.URL, pic.W, pic.H
	tm, ok := coverlayout.LookupTrim(d.Trim)
	if !ok {
		return
	}
	var m *coverlayout.Measured
	if v != nil && v.Measured != nil && v.Wrap != nil && v.Wrap.Attachment == d.Wrap.Att && v.Trim == d.Trim && fnum(v.Wrap.Bleed()) == d.Bleed {
		m = v.Measured
	} else if up != nil && up.Measured != nil {
		m = up.Measured
	} else {
		bleed, err := strconv.ParseFloat(d.Bleed, 64)
		if err != nil {
			return
		}
		g, err := coverlayout.MeasureWrap(pic.W, pic.H, tm, bleed, publish.IsRTL(d.Project.Language))
		if err != nil {
			return
		}
		m = &coverlayout.Measured{WidthPx: g.Width, HeightPx: g.Height, DPI: math.Round(g.DPI*100) / 100, WidthIn: g.WidthIn, SpineIn: g.SpineIn, BleedIn: bleed,
			FrontOn: map[bool]string{true: "right", false: "left"}[g.FrontOnRight],
			Front:   coverlayout.Rect{X0: g.Front.Min.X, Y0: g.Front.Min.Y, X1: g.Front.Max.X, Y1: g.Front.Max.Y},
			Back:    coverlayout.Rect{X0: g.Back.Min.X, Y0: g.Back.Min.Y, X1: g.Back.Max.X, Y1: g.Back.Max.Y},
			Spine:   coverlayout.Rect{X0: g.Spine.Min.X, Y0: g.Spine.Min.Y, X1: g.Spine.Max.X, Y1: g.Spine.Max.Y}}
	}
	w.Measured = true
	w.DPI, w.SpineIn, w.WidthIn, w.BleedIn, w.FrontOn = m.DPI, fnum(math.Round(m.SpineIn*1000)/1000), fnum(math.Round(m.WidthIn*1000)/1000), fnum(m.BleedIn), m.FrontOn
	w.SpinePages = int(math.Round(m.SpineIn / coverPaperInPerPage))
	margin := int(math.Round(0.25 * m.DPI)) // the generic profile's text margin; the printer's own figure is for the print cover
	w.Cut = boxOf(min(m.Back.X0, m.Front.X0), m.Front.Y0, max(m.Back.X1, m.Front.X1), m.Front.Y1)
	w.MarginBack, w.MarginFront = boxOf(m.Back.X0, m.Back.Y0, m.Back.X1, m.Back.Y1).inset(margin), boxOf(m.Front.X0, m.Front.Y0, m.Front.X1, m.Front.Y1).inset(margin)
	w.Fold0, w.Fold1 = m.Spine.X0, m.Spine.X1
	w.FS = max(pic.H/28, 12)
	w.LabelY = m.Front.Y0 + 2*w.FS
	spineW := m.Spine.X1 - m.Spine.X0
	w.Labels = [3]coverLabel{{"back", (m.Back.X0 + m.Back.X1) / 2, true}, {"spine", (m.Spine.X0 + m.Spine.X1) / 2, spineW >= 4*w.FS}, {"front", (m.Front.X0 + m.Front.X1) / 2, true}}
	w.FrontPreview = strconv.Itoa(m.Front.X0) + " " + strconv.Itoa(m.Front.Y0) + " " + strconv.Itoa(m.Front.X1-m.Front.X0) + " " + strconv.Itoa(m.Front.Y1-m.Front.Y0)
}

func coverPositions(r *http.Request, s *Server, checked string) []coverPos {
	out := make([]coverPos, 0, len(coverlayout.Positions))
	for _, p := range coverlayout.Positions {
		out = append(out, coverPos{Value: p, Label: s.tr(r, "cover.pos."+strings.ReplaceAll(p, "-", "_")), Checked: p == checked})
	}
	return out
}

// frontSvg places the draft's front picture, or returns nil when it has none to place.
func frontSvg(d *coverPageData, tm coverlayout.TrimIn) *coverSvg {
	sd := d.Front
	if sd.Pic == nil || (sd.Mode != coverlayout.ModeImage && sd.Mode != coverlayout.ModePage) {
		return nil
	}
	cw, ch := coverlayout.CanvasSize(tm)
	sv := &coverSvg{CW: cw, CH: ch, Href: sd.Pic.URL, Class: coverClass(sd.Bg)}
	if sd.Mode == coverlayout.ModeImage {
		sv.Class = coverClass(d.Project.CoverColor)
		return sv
	}
	scale, _ := strconv.Atoi(sd.Scale)
	margin, _ := strconv.ParseFloat(sd.Margin, 64)
	pl, err := coverlayout.PlacePage(sd.Pic.W, sd.Pic.H, tm, coverlayout.Side{Mode: coverlayout.ModePage, Fit: sd.Fit, Scale: scale, Position: sd.Pos, Background: sd.Bg, MarginIn: margin})
	if err != nil {
		return nil
	}
	sv.Page = true
	sv.X, sv.Y, sv.W, sv.H = pl.Dst.Min.X, pl.Dst.Min.Y, pl.Dst.Dx(), pl.Dst.Dy()
	sv.BX, sv.BY, sv.BW, sv.BH = pl.Box.Min.X, pl.Box.Min.Y, pl.Box.Dx(), pl.Box.Dy()
	return sv
}

func (s *Server) coverRender(w http.ResponseWriter, r *http.Request, status int, d coverPageData) {
	d.Messages = s.page(r, "", nil).Msgs("cover.js.")
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, status, "cover", s.page(r, s.tr(r, "cover.page_title", "title", d.Project.Title), d))
}

func (s *Server) coverPage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProject(w, r)
	if !ok {
		return
	}
	v, err := s.coverLayout(r, proj.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := draftOf(v, proj)
	s.coverFill(r, &d, v, nil)
	if v != nil {
		for _, wn := range v.Warnings {
			d.Warnings = append(d.Warnings, coverNote{Part: wn.Part, Text: s.coverWarning(r, wn, v.Pictures, v.Trim)})
		}
	}
	if fl := s.flashTake(r); fl != nil {
		d.Flash = fl["msg"]
	}
	s.coverRender(w, r, http.StatusOK, d)
}

// ---- changing --------------------------------------------------------------------------------------------

func formSide(r *http.Request, part string, def coverSide) coverSide {
	f := func(k string) string { return strings.TrimSpace(r.PostFormValue(part + "_" + k)) }
	sd := def
	sd.Part = part
	if v := f("mode"); v != "" {
		sd.Mode = v
	}
	sd.Att = f("att")
	if v := f("fit"); v != "" {
		sd.Fit = v
	}
	if r.PostForm.Has(part + "_scale") {
		sd.Scale = f("scale")
	}
	if v := f("pos"); v != "" {
		sd.Pos = v
	}
	if v := strings.ToUpper(f("bg")); v != "" {
		sd.Bg = v
	}
	if r.PostForm.Has(part + "_margin") {
		sd.Margin = f("margin")
	}
	return sd
}

// coverSideIn builds one side of the layout strictly by its mode: a generated or left-out side carries nothing, an image
// only its picture, a page its placement (the op refuses anything else).
func coverSideIn(sd coverSide) *coverlayout.Side {
	switch sd.Mode {
	case coverlayout.ModeImage:
		return &coverlayout.Side{Mode: sd.Mode, Attachment: sd.Att}
	case coverlayout.ModePage:
		scale, err := strconv.Atoi(sd.Scale)
		if err != nil {
			scale = -1
		}
		margin, err := strconv.ParseFloat(sd.Margin, 64)
		if err != nil || math.IsNaN(margin) {
			margin = -1
		}
		return &coverlayout.Side{Mode: sd.Mode, Attachment: sd.Att, Fit: sd.Fit, Scale: scale, Position: sd.Pos, Background: sd.Bg, MarginIn: margin}
	}
	return &coverlayout.Side{Mode: sd.Mode}
}

// coverProjKey carries the project that coverAuthorize found, so the handler does not look it up again.
type coverProjKey struct{}

// coverAuthorize is the cover form's check before any of its body is read: the caller reads the project (a non-member is
// told 404, as everywhere) and is an editor of it.
func (s *Server) coverAuthorize(w http.ResponseWriter, r *http.Request) (*http.Request, bool) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok {
		return r, false
	}
	if !s.coverCanEdit(proj) {
		s.fail(w, r, ops.Forbidden("Only editors can change the cover."))
		return r, false
	}
	return r.WithContext(context.WithValue(r.Context(), coverProjKey{}, proj)), true
}

// coverBusy answers a form that found no place among the bodies being read: the page as saved, with the words, and a
// Retry-After. Nothing of the body was read.
func (s *Server) coverBusy(w http.ResponseWriter, r *http.Request, own bool) {
	proj, _ := r.Context().Value(coverProjKey{}).(projects.Project)
	cur, err := s.coverLayout(r, proj.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := draftOf(cur, proj)
	d.Error = s.tr(r, "cover.err.busy_form")
	if own {
		d.Error = s.tr(r, "cover.err.busy_you")
	}
	s.coverFill(r, &d, cur, nil)
	w.Header().Set("Retry-After", "5")
	s.coverRender(w, r, http.StatusTooManyRequests, d)
}

// countingBody is a part as the upload op reads it: it counts what was read and keeps the first error that was not the
// end, so a body that ran out of its limit mid-picture is told from a picture that was damaged.
type countingBody struct {
	r   io.Reader
	n   int64
	err error
}

func (c *countingBody) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	if err != nil && err != io.EOF && c.err == nil {
		c.err = err
	}
	return n, err
}

// coverIntake is the cover form as it was read: its fields, and what became of each picture as it went by.
type coverIntake struct {
	vals     url.Values
	picked   string                            // the shape a first picture chose, for a form that said "generated"
	up       map[string]*coverlayout.UploadOut // the pictures made, by part
	names    map[string]string                 // and the names they came with
	ignored  []string                          // parts whose picture the shape does not use
	err      error                             // the first picture that was refused
	errPart  string
	tooBig   bool   // the body ran past its limit: what was not read yet is not known
	lastFile string // the part whose picture was being read last
	late     bool   // a field that decides how a picture is read came after the picture
}

// maxFormFields bounds the fields of the form; the cover form has about forty.
const maxFormFields = 200

// readCoverForm reads the form a part at a time: a field is kept (up to 64 KB), a picture is handed on as a stream to
// project.cover_layout.upload as soon as it comes, so no more than one picture is held and nothing is parsed whole. The
// fields that decide how a picture is read (the shape and the trim before any picture, the bleed before the wrap) come
// before it in the form; a form that sends them after is refused. A picture the shape does not use is not read into the project. After a
// picture is refused the rest of the form is still read, so the answer shows the draft whole.
func (s *Server) readCoverForm(r *http.Request, proj projects.Project, base coverPageData, mr *multipart.Reader) *coverIntake {
	in := &coverIntake{vals: r.PostForm, up: map[string]*coverlayout.UploadOut{}, names: map[string]string{}}
	if mr == nil {
		return in
	}
	in.vals = url.Values{}
	shape := func() string {
		if in.picked != "" {
			return in.picked
		}
		if v := strings.TrimSpace(in.vals.Get("shape")); v == "generated" || v == "sides" || v == "wrap" {
			return v
		}
		return base.Shape
	}
	for n := 0; ; n++ {
		part, err := mr.NextPart()
		if err != nil {
			var tooBig *http.MaxBytesError
			in.tooBig = errors.As(err, &tooBig)
			if err != io.EOF && !in.tooBig {
				in.err, in.errPart = ops.Invalid("", "The form could not be read."), "front"
			}
			return in
		}
		name := part.FormName()
		pn, isFile := strings.CutSuffix(name, "_file")
		if !isFile || (pn != "front" && pn != "back" && pn != "wrap") {
			if n > maxFormFields {
				continue
			}
			b, _ := io.ReadAll(io.LimitReader(part, 64<<10))
			if (len(in.names) > 0 && (name == "shape" || name == "trim")) || (in.names["wrap"] != "" && name == "bleed") {
				in.late = true
			}
			in.vals.Add(name, string(b))
			continue
		}
		if part.FileName() == "" || in.err != nil || in.tooBig {
			continue // no file was chosen, or a picture was refused already: the rest of the form is read for its fields
		}
		if in.up[pn] != nil || in.names[pn] != "" {
			continue // a second file for a part: the first is the one
		}
		ok := true
		switch shape() {
		case "sides":
			ok = pn != "wrap"
		case "wrap":
			ok = pn == "wrap"
		default:
			in.picked = map[bool]string{true: "wrap", false: "sides"}[pn == "wrap"]
		}
		if !ok {
			in.ignored = append(in.ignored, pn)
			continue
		}
		trim, bleed := strings.TrimSpace(in.vals.Get("trim")), ""
		if trim == "" {
			trim = base.Trim
		}
		if in.vals.Has("bleed") {
			bleed = strings.TrimSpace(in.vals.Get("bleed"))
		} else {
			bleed = base.Bleed
		}
		in.names[pn], in.lastFile = part.FileName(), pn
		cb := &countingBody{r: part}
		u, err := s.coverUpload(r, proj, pn, trim, bleed, part.FileName(), part.Header.Get("Content-Type"), cb)
		var tooBig *http.MaxBytesError
		switch {
		case errors.As(cb.err, &tooBig):
			in.tooBig = true
		case err != nil && cb.n == 0 && ops.AsError(err).Code == ops.CodeInvalid && ops.AsError(err).Data["reason"] == nil:
			in.err, in.errPart = errEmptyPicture, pn
		case err != nil:
			in.err, in.errPart = err, pn
		default:
			in.up[pn] = u
		}
	}
}

// errEmptyPicture marks a chosen file that had nothing in it.
var errEmptyPicture = errors.New("empty picture")

func (s *Server) coverUpload(r *http.Request, proj projects.Project, part, trim, bleed, filename, contentType string, body io.Reader) (*coverlayout.UploadOut, error) {
	ctx := ops.WithRawBody(r.Context(), ops.RawBody{R: body, ContentType: contentType, Length: -1})
	in := coverlayout.UploadIn{ID: proj.ID, Part: part, Trim: trim, Filename: filename}
	if part == "wrap" {
		in.BleedIn = bleed
	}
	out, err := s.Reg.Call(ctx, s.principal(r), "project.cover_layout.upload", in)
	if err != nil {
		return nil, err
	}
	u := out.(coverlayout.UploadOut)
	return &u, nil
}

// coverVersion says a version of the cover in a few facts, for the page that shows two at once.
func (s *Server) coverVersion(r *http.Request, shape, trim, alt string) *coverVersion {
	v := &coverVersion{Trim: s.coverTrimWord(r, trim), Alt: alt}
	switch shape {
	case coverlayout.ShapeSides:
		v.Shape = s.tr(r, "cover.shape_sides")
	case coverlayout.ShapeWrap:
		v.Shape = s.tr(r, "cover.shape_wrap")
	default:
		v.Shape = s.tr(r, "cover.shape_generated")
	}
	return v
}

func (s *Server) uiCover(w http.ResponseWriter, r *http.Request, mr *multipart.Reader) {
	proj, _ := r.Context().Value(coverProjKey{}).(projects.Project)
	cur, err := s.coverLayout(r, proj.ID)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d := draftOf(cur, proj)
	d.Dirty = true
	in := s.readCoverForm(r, proj, d, mr)
	r.PostForm = in.vals
	f := func(k string) string { return strings.TrimSpace(in.vals.Get(k)) }
	if in.late {
		s.fail(w, r, ops.Invalid("", "The shape and the page size must come before the pictures in the form, and the bleed before the wrap."))
		return
	}
	if rev, err := strconv.Atoi(f("revision")); err == nil {
		d.Revision = rev
	} else {
		d.Revision = 0
	}
	if v := f("shape"); v == "generated" || v == "sides" || v == "wrap" {
		d.Shape = v
	}
	if in.picked != "" {
		d.Shape = in.picked // a picture chosen under "generated" means the writer wants it: the shape follows the picture
	}
	if v := f("trim"); v != "" {
		d.Trim = v
	}
	d.Alt = f("alt")
	d.Front, d.Back = formSide(r, "front", defaultSide("front", proj)), formSide(r, "back", defaultSide("back", proj))
	d.Wrap.Att = f("wrap_att")
	if in.vals.Has("bleed") {
		d.Bleed = f("bleed")
	}
	action := f("action")
	up := in.up
	for _, part := range in.ignored {
		key, shape := "cover.note.ignored_sides", "cover.shape_sides"
		if part == "wrap" {
			key, shape = "cover.note.ignored_wrap", "cover.shape_wrap"
		}
		d.Notes = append(d.Notes, coverNote{Part: part, Text: s.tr(r, key, "part", s.coverPart(r, part), "shape", s.tr(r, shape))})
	}

	fail := func(status int, err error, part string) {
		e := ops.AsError(err)
		if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict && e.Code != ops.CodeTooLarge && e.Code != ops.CodeQuota && e.Code != ops.CodeRateLimited {
			s.fail(w, r, err)
			return
		}
		if e.Code == ops.CodeTooLarge {
			status = http.StatusRequestEntityTooLarge
		}
		var target string
		if e.Code == ops.CodeConflict && e.Data["reason"] == nil {
			// someone else saved the cover while this page was open: the form keeps the writer's version, the other
			// version is described beside it, and a save now puts the writer's in its place
			yours := s.coverVersion(r, d.Shape, d.Trim, d.Alt)
			if cur2, err2 := s.coverLayout(r, proj.ID); err2 == nil {
				cur = cur2
			}
			d.Revision = 0
			saved := s.coverVersion(r, "generated", coverlayout.DefaultTrim, "")
			if cur != nil {
				d.Revision = cur.Revision
				saved = s.coverVersion(r, cur.Shape, cur.Trim, cur.Alt)
			}
			saved.Shown = cur != nil && cur.FrontURL != ""
			if cur != nil {
				saved.Thumb = cur.ThumbURL
			}
			d.Conflict, d.Yours, d.Saved = true, yours, saved
			d.Error, _ = s.coverRefusal(r, e, d.Trim, d.Bleed, part)
			s.coverFill(r, &d, cur, up)
			s.coverRender(w, r, http.StatusConflict, d)
			return
		}
		d.Error, target = s.coverRefusal(r, e, d.Trim, d.Bleed, part)
		d.ErrorFor = target
		s.coverFill(r, &d, cur, up)
		s.coverRender(w, r, status, d)
	}
	for _, part := range []string{"front", "back", "wrap"} {
		u, ok := up[part]
		if !ok {
			continue
		}
		switch part {
		case "wrap":
			d.Wrap.Att = u.AttachmentID
		case "front":
			d.Front.Att = u.AttachmentID
			if d.Front.Mode != coverlayout.ModePage {
				d.Front.Mode = coverlayout.ModeImage
			}
		case "back":
			d.Back.Att = u.AttachmentID
			if d.Back.Mode != coverlayout.ModePage {
				d.Back.Mode = coverlayout.ModeImage
			}
		}
	}
	switch {
	case in.tooBig:
		d.Error, d.ErrorFor = s.tr(r, "cover.err.body_large", "mb", strconv.FormatInt(s.bodyLimit(r)>>20, 10)), in.lastFile+"_file"
		s.coverFill(r, &d, cur, up)
		s.coverRender(w, r, http.StatusRequestEntityTooLarge, d)
		return
	case in.err == errEmptyPicture:
		d.Error, d.ErrorFor = s.tr(r, "cover.err.empty", "part", s.coverPart(r, in.errPart)), in.errPart+"_file"
		s.coverFill(r, &d, cur, up)
		s.coverRender(w, r, http.StatusUnprocessableEntity, d)
		return
	case in.err != nil:
		fail(http.StatusUnprocessableEntity, in.err, in.errPart)
		return
	}
	if action == "upload" {
		for part, u := range up {
			for _, wn := range u.Warnings {
				d.Warnings = append(d.Warnings, coverNote{Part: part, Text: s.coverWarning(r, wn, []coverlayout.PictureInfo{{Part: part, WidthPx: u.Width, HeightPx: u.Height, DPI: u.DPI, CropLoss: coverlayout.CropLoss(u.Width, u.Height, mustTrim(d.Trim))}}, d.Trim)})
			}
			d.Notes = append(d.Notes, coverNote{Part: part, Text: s.tr(r, "cover.note.added", "part", s.coverPart(r, part), "name", in.names[part])})
		}
		s.coverFill(r, &d, cur, up)
		s.coverRender(w, r, http.StatusOK, d)
		return
	}
	flash, saved := s.tr(r, "cover.saved"), s.tr(r, "cover.saved_generated")
	for _, n := range d.Notes { // only the pictures this save did not use are there
		flash, saved = flash+" "+n.Text, saved+" "+n.Text
	}

	// save
	if d.Shape == "generated" {
		if cur == nil {
			s.flashSet(r, map[string]string{"msg": saved})
			http.Redirect(w, r, "/p/"+url.PathEscape(proj.Slug)+"/cover", http.StatusSeeOther)
			return
		}
		s.coverClear(w, r, proj)
		return
	}
	sin := coverlayout.SetIn{ID: proj.ID, Revision: d.Revision, Shape: d.Shape, Trim: d.Trim, Alt: d.Alt}
	switch d.Shape {
	case coverlayout.ShapeSides:
		sin.Front, sin.Back = coverSideIn(d.Front), coverSideIn(d.Back)
		for _, sd := range []coverSide{d.Front, d.Back} {
			if (sd.Mode == coverlayout.ModeImage || sd.Mode == coverlayout.ModePage) && sd.Att == "" {
				d.Error, d.ErrorFor = s.tr(r, "cover.err.picture_missing", "part", s.coverPart(r, sd.Part)), sd.Part+"_file"
				s.coverFill(r, &d, cur, up)
				s.coverRender(w, r, http.StatusUnprocessableEntity, d)
				return
			}
		}
		// The layout op does not place the picture; the derivative does, later, and fails for a placement the page's own
		// preview refuses (a picture more than eight times the page, or under a pixel). Say so now, as the preview does.
		for _, sd := range []coverSide{d.Front, d.Back} {
			if sd.Mode != coverlayout.ModePage || sd.Att == "" {
				continue
			}
			side := coverSideIn(sd)
			if side.Scale < 10 || side.Scale > 100 || side.MarginIn < 0 || side.MarginIn > 1.5 {
				continue // the op refuses these in its own words
			}
			pw, ph, ok := s.coverPictureSize(r, proj, sd.Att, up)
			if !ok {
				continue
			}
			var ref *coverlayout.Refusal
			if _, err := coverlayout.PlacePage(pw, ph, mustTrim(d.Trim), *side); errors.As(err, &ref) {
				d.Error, d.ErrorFor = s.coverRefusal(r, &ops.Error{Code: ops.CodeInvalid, Data: map[string]any{"reason": ref.Code}}, d.Trim, d.Bleed, sd.Part)
				s.coverFill(r, &d, cur, up)
				s.coverRender(w, r, http.StatusUnprocessableEntity, d)
				return
			}
		}
	case coverlayout.ShapeWrap:
		wr := &coverlayout.Wrap{Attachment: d.Wrap.Att}
		if d.Bleed != "" {
			b, err := strconv.ParseFloat(d.Bleed, 64)
			if err != nil || math.IsNaN(b) {
				b = -1
			}
			wr.BleedIn = &b
		}
		sin.Wrap = wr
		if d.Wrap.Att == "" {
			d.Error, d.ErrorFor = s.tr(r, "cover.err.picture_missing", "part", s.coverPart(r, "wrap")), "wrap_file"
			s.coverFill(r, &d, cur, up)
			s.coverRender(w, r, http.StatusUnprocessableEntity, d)
			return
		}
	}
	if _, err := s.Reg.Call(r.Context(), s.principal(r), "project.cover_layout.set", sin); err != nil {
		fail(http.StatusUnprocessableEntity, err, "front")
		return
	}
	s.flashSet(r, map[string]string{"msg": flash})
	http.Redirect(w, r, "/p/"+url.PathEscape(proj.Slug)+"/cover", http.StatusSeeOther)
}

// coverPictureSize is the size of a picture the draft names: from this request's uploads, else from the attachment.
func (s *Server) coverPictureSize(r *http.Request, proj projects.Project, att string, up map[string]*coverlayout.UploadOut) (w, h int, ok bool) {
	for _, u := range up {
		if u != nil && u.AttachmentID == att {
			return u.Width, u.Height, u.Width > 0 && u.Height > 0
		}
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "project.attachment.get", attachments.AttIn{ID: proj.ID, AttID: att})
	if err != nil {
		return 0, 0, false
	}
	a := out.(attachments.Item).Item
	return a.Width, a.Height, a.Kind == "image" && a.Width > 0 && a.Height > 0
}

func mustTrim(name string) coverlayout.TrimIn {
	t, ok := coverlayout.LookupTrim(name)
	if !ok {
		t, _ = coverlayout.LookupTrim(coverlayout.DefaultTrim)
	}
	return t
}

func (s *Server) coverClear(w http.ResponseWriter, r *http.Request, proj projects.Project) {
	out, err := s.Reg.Call(r.Context(), s.principal(r), "project.cover_layout.clear", coverlayout.ProjectIn{ID: proj.ID})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	msg := s.tr(r, "cover.cleared")
	if !out.(coverlayout.ClearOut).Cleared {
		msg = s.tr(r, "cover.saved_generated")
	}
	s.flashSet(r, map[string]string{"msg": msg})
	http.Redirect(w, r, "/p/"+url.PathEscape(proj.Slug)+"/cover", http.StatusSeeOther)
}

func (s *Server) uiCoverClear(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if !s.coverCanEdit(proj) {
		s.fail(w, r, ops.Forbidden("Only editors can change the cover."))
		return
	}
	s.coverClear(w, r, proj)
}
