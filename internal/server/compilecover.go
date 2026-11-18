package server

import (
	"fmt"
	"html"
	"html/template"
	"math"
	"net/http"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/cover"
	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/coverlayout"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
)

// The paperback cover section of the Compile page, measured on the server. The page's script sends the form (htmx) with
// the preset and the options it holds, and swaps the answer in: a drawing of the cover laid flat (the layout's own
// pictures when the project has one, the folds, the text margin and the barcode box from compile.cover_spec), the facts,
// and every warning and refusal as a sentence of the catalogue by its code. The English the ops carry is not shown here.
// The same sentences answer the Cover page's print check (coverui.go), so a code has one set of words.

// printFacts are the figures a print sentence may use. A zero is "not known": the sentence without the figure is used.
type printFacts struct {
	Part                     string // front, back, wrap or "" (the warning did not say)
	DPI, W, H                float64
	Margin                   float64
	Profile                  string
	BarcodeW, BarcodeH, Inch float64
	Pages, FromPages         int
	Spine, Measured          float64
}

// thousands writes a count with its separators: 1,200,000.
func thousands(v any) string {
	n, ok := v.(int)
	if f, isF := v.(float64); isF {
		n, ok = int(f), true
	}
	if !ok {
		return fmt.Sprint(v)
	}
	s := strconv.Itoa(n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}

func num3(v float64) string {
	return strconv.FormatFloat(math.Round(v*1000)/1000, 'f', -1, 64)
}

// printWhat is the picture a sentence is about, as its first words.
func (s *Server) printWhat(r *http.Request, part string) string {
	switch part {
	case "front", "back", "wrap":
		return s.tr(r, "cmp.cv.what."+part)
	}
	return s.tr(r, "cmp.cv.what.cover")
}

// printWarning is the sentence for a code of the print cover (export.cover, compile.cover_spec, project.cover_layout.check),
// or "" for a code it has no words for.
func (s *Server) printWarning(r *http.Request, code string, f printFacts) string {
	key := "cmp.cv.w." + code
	switch {
	case code == "image_low_resolution" && f.DPI <= 0:
		key += "_plain"
	case code == "image_low_resolution" && f.W <= 0:
		key += "_dpi"
	case code == "cover_wrap_stale" && (f.Measured <= 0 || f.Pages <= 0):
		key += "_plain"
	}
	if !hasKey(key) {
		if key = "cmp.cv.r." + code; !hasKey(key) { // a refusal the check gives as a warning
			return ""
		}
	}
	return s.tr(r, key, "layout", "", "page", "", "what", s.printWhat(r, f.Part), "dpi", strconv.Itoa(int(math.Round(f.DPI))), "w", num3(f.W), "h", num3(f.H),
		"margin", num3(f.Margin), "profile", s.printProfile(r, f.Profile), "bw", num3(f.BarcodeW), "bh", num3(f.BarcodeH), "inset", num3(f.Inch),
		"pages", strconv.Itoa(f.Pages), "from", strconv.Itoa(f.FromPages), "spine", num3(f.Spine), "measured", num3(f.Measured))
}

func hasKey(k string) bool { return i18n.Default.Has(k) }

func (s *Server) printProfile(r *http.Request, name string) string {
	if k := "cmp.cv.profile." + name; hasKey(k) {
		return s.tr(r, k)
	}
	return name
}

// layoutReasons are the refusals that are about the cover layout, for which a link to the Cover page is the way to mend it.
var layoutReasons = map[string]bool{"cover_trim_mismatch": true, "cover_no_proof": true, "cover_rtl_text": true, "cover_plan": true, "cover_file_missing": true,
	"cover_picture_unusable": true, "cover_picture_missing": true, "cover_picture_damaged": true, "cover_picture_extreme": true, "cover_margin_large": true,
	"cover_wrap_missing": true, "cover_wrap_empty": true, "cover_wrap_low_dpi": true, "cover_wrap_spine_negative": true, "cover_wrap_spine_wide": true, "cover_wrap_bleed": true}

// printRefusal is the sentence for a refusal of the print cover by its stable reason (or, for a setting in the options form,
// by the control's name), "" for one it has none for. A reason the Cover page has words for and this page has none of (an
// upload's or a save's refusal, which the print cover cannot meet) uses the Cover page's sentence.
func (s *Server) printRefusal(r *http.Request, e *ops.Error) string {
	reason, _ := e.Data["reason"].(string)
	if reason == "" {
		if key, ok := strings.CutPrefix(e.Field, "options."); ok && e.Code == ops.CodeInvalid {
			label := key
			if k := "cmp.js.o_" + key; hasKey(k) {
				label = s.tr(r, k)
			}
			return s.tr(r, "cmp.cv.r.option", "label", label)
		}
		return ""
	}
	if !hasKey("cmp.cv.r." + reason) {
		if k := "cover.err." + strings.TrimPrefix(reason, "cover_"); strings.HasPrefix(reason, "cover_") && hasKey(k) {
			return s.tr(r, k, "part", s.coverPart(r, ""), "trim", "", "bleed", "", "dpi", "", "spine", "", "alt", "")
		}
		return ""
	}
	lt, _ := e.Data["layout_trim"].(string)
	ps, _ := e.Data["page_size"].(string)
	return s.tr(r, "cmp.cv.r."+reason, "layout", s.coverTrimWord(r, lt), "page", s.coverTrimWord(r, ps), "words", thousands(e.Data["words"]), "max_words", thousands(e.Data["max_words"]))
}

// coverPrintError turns what export.cover or compile.cover_spec refused with into an error whose message is the
// catalogue's sentence, so a script that shows an error's message shows the right words. A picture gone from the server's
// files is a not-found, which would otherwise be a bare 404.
func (s *Server) coverPrintError(r *http.Request, err error) error {
	e := ops.AsError(err)
	c := *e
	switch {
	case e.Code == ops.CodeNotFound:
		c.Message = s.tr(r, "cmp.cv.r.cover_file_missing")
	case s.printRefusal(r, e) != "":
		c.Message = s.printRefusal(r, e)
	default:
		return err
	}
	return &c
}

// coverFileNotes are the sentences of a downloaded file's warnings that the measuring could not give, because they are
// found only when the pictures are read: a colour profile that is not sRGB or was left out. Each names its picture, once.
func (s *Server) coverFileNotes(r *http.Request, ws []compile.Warning) []string {
	var out []string
	seen := map[string]bool{}
	for _, w := range ws {
		switch w.Code {
		case "cover_profile_not_srgb", "cover_profile_dropped":
			if t := s.printWarning(r, w.Code, printFacts{Part: w.Part}); t != "" && !seen[t] {
				seen[t] = true
				out = append(out, t)
			}
		}
	}
	return out
}

// ---- the drawing -----------------------------------------------------------------------------------------

// coverGeometry is the first page of the cover in points, y downward. For a wrap it is the picture's own size and
// panels, whatever the book's spine is.
type coverGeo struct {
	W, H, B, BY, TW, TH float64
	Back, Spine, Front  [2]float64
}

func geoOf(o compile.CoverSpecOut) coverGeo {
	g := coverGeo{W: o.WrapWidthIn * 72, H: o.WrapHeightIn * 72, B: o.BleedIn * 72, TW: o.TrimWidthIn * 72, TH: o.TrimHeightIn * 72,
		Back: [2]float64{o.Back.X0, o.Back.X1}, Spine: [2]float64{o.Spine.X0, o.Spine.X1}, Front: [2]float64{o.Front.X0, o.Front.X1}}
	if l := o.Layout; l != nil && l.Shape == coverlayout.ShapeWrap && l.MeasuredSpineIn != nil {
		g.W, g.H = l.WrapWidthIn*72, l.WrapHeightIn*72
		sp := *l.MeasuredSpineIn * 72
		g.B = (g.W - 2*g.TW - sp) / 2
		left, right := [2]float64{g.B, g.B + g.TW}, [2]float64{g.B + g.TW + sp, g.B + 2*g.TW + sp}
		g.Spine = [2]float64{left[1], right[0]}
		if o.Back.X0 < o.Front.X0 {
			g.Back, g.Front = left, right
		} else {
			g.Back, g.Front = right, left
		}
	}
	g.BY = (g.H - g.TH) / 2
	return g
}

type svgb struct{ strings.Builder }

func f1(v float64) string { return strconv.FormatFloat(math.Round(v*100)/100, 'f', -1, 64) }

func (b *svgb) tag(name string, at ...string) {
	b.WriteString("<" + name)
	for i := 0; i+1 < len(at); i += 2 {
		b.WriteString(" " + at[i] + `="` + html.EscapeString(at[i+1]) + `"`)
	}
	b.WriteString("/>")
}

func (b *svgb) text(txt string, at ...string) {
	b.WriteString("<text")
	for i := 0; i+1 < len(at); i += 2 {
		b.WriteString(" " + at[i] + `="` + html.EscapeString(at[i+1]) + `"`)
	}
	b.WriteString(">" + html.EscapeString(txt) + "</text>")
}

// wrapWords breaks text into lines of about n characters.
func wrapWords(text string, n int) []string {
	var out []string
	line := ""
	for _, w := range strings.Fields(text) {
		if line != "" && len([]rune(line+" "+w)) > n {
			out = append(out, line)
			line = w
		} else if line == "" {
			line = w
		} else {
			line += " " + w
		}
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}

func (b *svgb) lines(words []string, x, y, sz float64, cls string) float64 {
	for i, w := range words {
		b.text(w, "class", cls, "x", f1(x), "y", f1(y+float64(i)*sz*1.2), "font-size", f1(sz), "text-anchor", "middle")
	}
	return y + float64(len(words))*sz*1.2
}

// generated draws a side's typographic cover: the title in a third of the panel, the subtitle, the author near the foot.
func (b *svgb) generated(g coverGeo, x0 float64, look cover.Spec, untitled string) {
	tw, th, m := g.TW, g.TH, g.TW*0.12
	fx, sz := x0+tw/2, tw*0.08
	title := look.Title
	if title == "" {
		title = untitled
	}
	var tl []string
	for {
		tl = wrapWords(title, max(6, int(math.Floor((tw-2*m)/(sz*0.52)))))
		if float64(len(tl))*sz*1.2 <= th*0.38 || sz <= tw*0.035 {
			break
		}
		sz *= 0.9
	}
	y := b.lines(tl, fx, g.BY+th*0.16+sz, sz, "cv-t")
	if look.Subtitle != "" {
		b.lines(wrapWords(look.Subtitle, int(math.Floor((tw-2*m)/(tw*0.02)))), fx, y+tw*0.04, tw*0.0375, "cv-s")
	}
	if look.Author != "" {
		b.text(look.Author, "class", "cv-a", "x", f1(fx), "y", f1(g.BY+th*0.88), "font-size", f1(tw*0.045), "text-anchor", "middle")
	}
}

// coverSVG draws the cover laid flat.
func (s *Server) coverSVG(r *http.Request, proj projects.Project, o compile.CoverSpecOut, v *coverlayout.View, barcode bool) template.HTML {
	g := geoOf(o)
	look := cover.Spec{Title: proj.Title, Subtitle: proj.Subtitle, Author: proj.AuthorName, Kind: proj.Kind, Color: proj.CoverColor}.Clean()
	untitled := s.tr(r, "cmp.js.untitled")
	var b svgb
	title := s.tr(r, "cmp.cv_alt", "w", num3(g.W/72), "h", num3(g.H/72))
	b.WriteString(`<svg viewBox="0 0 ` + f1(g.W) + ` ` + f1(g.H) + `" class="cmp-wrapart" role="img" aria-labelledby="cv-art-t cv-art-d">`)
	b.WriteString(`<title id="cv-art-t">` + html.EscapeString(title) + `</title><desc id="cv-art-d">` + html.EscapeString(s.tr(r, "cmp.cv_art_desc")) + `</desc>`)
	b.tag("rect", "x", "0", "y", "0", "width", f1(g.W), "height", f1(g.H), "fill", look.Color)
	region := func(p [2]float64) (float64, float64) { // the panel with the bleed on its outer edge, and none on the spine's
		if p[0] < g.Spine[0] {
			return 0, p[1]
		}
		return p[0], g.W
	}
	// the pictures
	plain := true // the cover's type is drawn: there is no picture over the whole of it
	if v != nil && o.Layout != nil && v.Shape == coverlayout.ShapeWrap && v.Wrap != nil {
		plain = false
		b.tag("image", "href", "/files/"+v.Wrap.Attachment, "x", "0", "y", "0", "width", f1(g.W), "height", f1(g.H), "preserveAspectRatio", "none")
	}
	sideOf := func(part string, p [2]float64, sd *coverlayout.Side, href string) {
		if sd == nil || href == "" || (sd.Mode != coverlayout.ModeImage && sd.Mode != coverlayout.ModePage) {
			return
		}
		x0, x1 := region(p)
		id := "cv-clip-" + part
		b.WriteString(`<clipPath id="` + id + `">`)
		b.tag("rect", "x", f1(x0), "y", "0", "width", f1(x1-x0), "height", f1(g.H))
		b.WriteString(`</clipPath><g clip-path="url(#` + id + `)">`)
		if sd.Mode == coverlayout.ModePage {
			bg := sd.Background
			if bg == "" {
				bg = look.Color
			}
			b.tag("rect", "x", f1(x0), "y", "0", "width", f1(x1-x0), "height", f1(g.H), "fill", bg)
			b.tag("image", "href", href, "x", f1(p[0]), "y", f1(g.BY), "width", f1(g.TW), "height", f1(g.TH), "preserveAspectRatio", "none")
		} else { // the picture fills the trim and the bleed on every side, from the centre
			b.tag("image", "href", href, "x", f1(p[0]-g.B), "y", "0", "width", f1(g.TW+2*g.B), "height", f1(g.H), "preserveAspectRatio", "xMidYMid slice")
		}
		b.WriteString(`</g>`)
	}
	isGen := func(sd *coverlayout.Side) bool {
		return v == nil || o.Layout == nil || sd == nil || sd.Mode == coverlayout.ModeGenerated
	}
	if plain && v != nil && o.Layout != nil {
		sideOf("front", g.Front, v.Front, v.FrontURL)
		sideOf("back", g.Back, v.Back, v.BackURL)
	}
	b.WriteString(`<g fill="#FFFFFF">`) // white type on the colour in either theme, as in the file
	if plain {
		var fs, bs *coverlayout.Side
		if v != nil && o.Layout != nil {
			fs, bs = v.Front, v.Back
		}
		if isGen(fs) {
			b.generated(g, g.Front[0], look, untitled)
		}
		if look.Kind != "" && (v == nil || o.Layout == nil || (bs != nil && bs.Mode == coverlayout.ModeGenerated)) {
			b.text(strings.ToUpper(s.kindLabel(r, look.Kind)), "class", "cv-k", "x", f1(g.Back[0]+g.TW/2), "y", f1(g.BY+g.TH*0.18), "font-size", f1(g.TW*0.023), "text-anchor", "middle")
		}
		if o.SpineHasText {
			sw := g.Spine[1] - g.Spine[0]
			sx := g.Spine[0] + sw/2
			t := look.Title
			if t == "" {
				t = untitled
			}
			if look.Author != "" {
				t += "   " + look.Author
			}
			b.text(t, "class", "cv-p", "x", f1(sx), "y", f1(g.H/2), "font-size", f1(math.Min(sw*0.5, 11)), "text-anchor", "middle", "dominant-baseline", "central", "transform", "rotate(90 "+f1(sx)+" "+f1(g.H/2)+")")
		}
	}
	b.WriteString(`</g>`)
	// the barcode box: white when the file has it, an outline to keep clear when it has not
	bx0, bx1, by0, by1 := o.BarcodeBox.X0, o.BarcodeBox.X1, g.H-o.BarcodeBox.Y1, g.H-o.BarcodeBox.Y0
	if barcode {
		b.tag("rect", "x", f1(bx0), "y", f1(by0), "width", f1(bx1-bx0), "height", f1(by1-by0), "fill", "#FFFFFF")
	}
	// the cut, the folds, the margins: two strokes, so they show on any picture
	for _, cls := range []string{"cv-halo", "cv-line"} {
		b.WriteString(`<g class="` + cls + `">`)
		lx, rx := math.Min(g.Back[0], g.Front[0]), math.Max(g.Back[1], g.Front[1])
		b.tag("rect", "class", "cv-g-cut", "x", f1(lx), "y", f1(g.BY), "width", f1(rx-lx), "height", f1(g.TH))
		if g.Spine[1] > g.Spine[0] {
			b.tag("path", "class", "cv-g-fold", "d", "M"+f1(g.Spine[0])+" 0V"+f1(g.H)+"M"+f1(g.Spine[1])+" 0V"+f1(g.H))
		}
		if o.Layout != nil && o.Layout.Stale {
			c, h := g.W/2, o.SpineIn*72/2
			b.tag("path", "class", "cv-g-book", "d", "M"+f1(c-h)+" 0V"+f1(g.H)+"M"+f1(c+h)+" 0V"+f1(g.H))
		}
		m := o.TextMarginIn * 72
		for _, p := range [][2]float64{g.Back, g.Front} {
			b.tag("rect", "class", "cv-g-margin", "x", f1(p[0]+m), "y", f1(g.BY+m), "width", f1(math.Max(g.TW-2*m, 0)), "height", f1(math.Max(g.TH-2*m, 0)))
		}
		b.tag("rect", "class", "cv-g-bar", "x", f1(bx0), "y", f1(by0), "width", f1(bx1-bx0), "height", f1(by1-by0))
		b.WriteString(`</g>`)
	}
	// the panels' names; the spine's only when it is wide enough to hold one
	fs := math.Max(g.H/28, 8)
	name := func(p [2]float64, key string) {
		b.text(s.tr(r, key), "class", "cv-lbl", "x", f1((p[0]+p[1])/2), "y", f1(g.BY+2*fs), "font-size", f1(fs), "text-anchor", "middle")
	}
	name(g.Back, "cover.lbl_back")
	name(g.Front, "cover.lbl_front")
	if g.Spine[1]-g.Spine[0] >= 4*fs {
		name(g.Spine, "cover.lbl_spine")
	}
	b.WriteString(`</svg>`)
	return template.HTML(b.String()) //nolint:gosec // every attribute and text above went through html.EscapeString, every number is formatted here
}

// ---- the answer ------------------------------------------------------------------------------------------

type cvFact struct{ K, V string }
type cvKey struct {
	Class, Text string
	Box         bool // the swatch is a small box, not a line
}

type compileCoverOut struct {
	Pg    Page
	Slug  string
	Say   string
	Bad   bool
	Focus string
	Proof bool // the answer is a drawing of a layout, so a proof can be made
	Err   string
	Link  bool   // the refusal is about the cover layout: a link to the Cover page mends it
	Note  string // what the drawing is and how it differs from the file
	SVG   template.HTML
	Key   []cvKey
	Facts []cvFact
	Warns []string
}

// coverNotes are the warnings of a measured cover as sentences. The ops give a warning's code and no picture to say it
// about, so a low-resolution warning is said once for each picture the layout prints under 300 dpi.
func (s *Server) coverNotes(r *http.Request, o compile.CoverSpecOut) []string {
	pf, _ := publish.LookupCoverProfile(o.Profile)
	base := printFacts{Margin: o.TextMarginIn, Profile: o.Profile, BarcodeW: pf.BarcodeWIn, BarcodeH: pf.BarcodeHIn, Inch: pf.BarcodeInsetIn,
		Pages: o.PageCount, FromPages: pf.SpineTextFromPages, Spine: o.SpineIn}
	if o.Layout != nil && o.Layout.MeasuredSpineIn != nil {
		base.Measured = *o.Layout.MeasuredSpineIn
	}
	var out []string
	add := func(code string, f printFacts) {
		t := s.printWarning(r, code, f)
		if t == "" {
			t = s.tr(r, "cmp.cv.w.unknown", "code", code)
		}
		out = append(out, t)
	}
	for _, w := range o.Warnings {
		wf := base
		wf.Part = w.Part
		if w.Code != "image_low_resolution" || o.Layout == nil {
			add(w.Code, wf)
			continue
		}
		if w.Part != "" { // the warning says which picture
			for _, p := range o.Layout.Pictures {
				if p.Part == w.Part {
					wf.DPI, wf.W, wf.H = p.DPI, p.PrintWIn, p.PrintHIn
				}
			}
			add(w.Code, wf)
			continue
		}
		n := 0
		for _, p := range o.Layout.Pictures {
			if p.DPI > 0 && p.DPI < publish.LowDPI {
				f := base
				f.Part, f.DPI, f.W, f.H = p.Part, p.DPI, p.PrintWIn, p.PrintHIn
				add(w.Code, f)
				n++
			}
		}
		if n == 0 {
			add(w.Code, base)
		}
	}
	return out
}

func (s *Server) coverFacts(r *http.Request, o compile.CoverSpecOut, v *coverlayout.View) []cvFact {
	t := func(k string, a ...any) string { return s.tr(r, k, a...) }
	var f []cvFact
	add := func(k, v string) { f = append(f, cvFact{t(k), v}) }
	in := func(n float64) string { return t("cmp.js.p_inch", "n", num3(n)) }
	size := func(w, h float64) string { return t("cmp.js.p_in", "w", num3(w), "h", num3(h)) }
	switch {
	case o.Layout == nil:
		add("cmp.cv.f_made", t("cmp.cv.made_generated"))
	case o.Layout.Shape == coverlayout.ShapeWrap:
		add("cmp.cv.f_made", t("cmp.cv.made_wrap"))
	default:
		add("cmp.cv.f_made", t("cmp.cv.made_sides"))
	}
	add("cmp.cv.f_profile", s.printProfile(r, o.Profile))
	if o.PageSource == "given" {
		add("cmp.cv.f_pages", t("cmp.cv.pages_given", "n", strconv.Itoa(o.PageCount)))
	} else {
		add("cmp.cv.f_pages", t("cmp.cv.pages_layout", "n", strconv.Itoa(o.PageCount)))
	}
	spine := in(o.SpineIn)
	if o.Layout != nil && o.Layout.MeasuredSpineIn != nil {
		spine = t("cmp.cv.spine_wrap", "book", in(o.SpineIn), "wrap", in(*o.Layout.MeasuredSpineIn))
	}
	if !o.SpineHasText && o.SpineTextWhy != "wrap" {
		spine += " · " + t("cmp.cv.notype."+o.SpineTextWhy)
	}
	add("cmp.cv.f_spine", spine)
	if o.Layout != nil {
		add("cmp.cv.f_page1", size(o.Layout.WrapWidthIn, o.Layout.WrapHeightIn))
	} else {
		add("cmp.cv.f_page1", size(o.WrapWidthIn, o.WrapHeightIn))
	}
	add("cmp.cv.f_trim", size(o.TrimWidthIn, o.TrimHeightIn))
	add("cmp.cv.f_bleed", in(o.BleedIn))
	add("cmp.cv.f_margin", in(o.TextMarginIn))
	if o.Layout != nil {
		for _, p := range o.Layout.Pictures {
			add("cmp.cv.pic."+p.Part, t("cmp.cv.pic_v", "dpi", strconv.Itoa(int(math.Round(p.DPI))), "w", num3(p.PrintWIn), "h", num3(p.PrintHIn)))
		}
	}
	return f
}

func (s *Server) coverKey(r *http.Request, o compile.CoverSpecOut) []cvKey {
	k := []cvKey{{"cv-g-cut", s.tr(r, "cmp.cv.key_cut"), false}, {"cv-g-fold", s.tr(r, "cmp.cv.key_fold"), false}, {"cv-g-margin", s.tr(r, "cmp.cv.key_margin", "m", num3(o.TextMarginIn)), false}, {"cv-g-bar", s.tr(r, "cmp.cv.key_bar"), true}}
	if o.Layout != nil && o.Layout.Stale {
		k = append(k, cvKey{"cv-g-book", s.tr(r, "cmp.cv.key_book"), false})
	}
	return k
}

// compileCoverSpec answers the cover form: measures the cover and draws it, or says in words why it cannot.
func (s *Server) compileCoverSpec(w http.ResponseWriter, r *http.Request, proj projects.Project, in compile.CoverIn, thickGiven bool, formErr error) {
	pg := s.page(r, "", nil)
	d := compileCoverOut{Pg: pg, Slug: proj.Slug}
	status := http.StatusOK
	fail := func(code int, text, focus string) {
		d.Err, d.Say, d.Bad, d.Focus, status = text, text, true, focus, code
	}
	linkFor := func(e *ops.Error) {
		r, _ := e.Data["reason"].(string)
		d.Link = layoutReasons[r] || e.Code == ops.CodeNotFound
	}
	if formErr != nil {
		fe := ops.AsError(formErr)
		text := s.printRefusal(r, fe)
		if text == "" {
			text = fe.Message
		}
		fail(http.StatusUnprocessableEntity, text, map[string]string{"page_thickness_in": "cv-thick", "page_count": "cv-pages", "bleed_in": "cv-bleed"}[fe.Field])
	} else if !thickGiven {
		fail(http.StatusUnprocessableEntity, s.tr(r, "cmp.cv.need"), "cv-thick")
	} else {
		out, err := s.Reg.Call(r.Context(), s.principal(r), "compile.cover_spec", in)
		switch {
		case err != nil:
			e := ops.AsError(err)
			linkFor(e)
			text := s.printRefusal(r, e)
			if e.Code == ops.CodeNotFound {
				text = s.tr(r, "cmp.cv.r.cover_file_missing")
			}
			focus := map[string]string{"page_thickness_in": "cv-thick", "page_count": "cv-pages", "bleed_in": "cv-bleed"}[e.Field]
			switch {
			case text != "":
			case e.Code == ops.CodeInvalid:
				text = e.Message // a refusal with no reason code and no setting named: not met in the tests
			default:
				s.fail(w, r, err)
				return
			}
			fail(http.StatusUnprocessableEntity, text, focus)
		default:
			o := out.(compile.CoverSpecOut)
			var v *coverlayout.View
			if o.Layout != nil {
				if lo, err := s.Reg.Call(r.Context(), s.principal(r), "project.cover_layout.get", coverlayout.ProjectIn{ID: proj.ID}); err == nil {
					v = lo.(coverlayout.Item).Layout
				}
			}
			d.SVG = s.coverSVG(r, proj, o, v, in.Barcode)
			d.Key, d.Facts, d.Warns = s.coverKey(r, o), s.coverFacts(r, o, v), s.coverNotes(r, o)
			d.Proof = o.Layout != nil && o.Layout.Proof
			d.Note = s.coverNote(r, o, v)
			d.Say = s.tr(r, "cmp.cv.ready", "s", num3(o.SpineIn))
			if o.Layout != nil && o.Layout.Stale {
				d.Say += " " + s.tr(r, "cmp.cv.ready_stale")
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.fragment(w, r, status, "compile-cover", "compile-cover", d)
}

// coverNote says what the drawing is, by what it draws: a wrap as the picture has it, pictures as the screen shows them
// (the file crops a little tighter), the file's own type for a generated side.
func (s *Server) coverNote(r *http.Request, o compile.CoverSpecOut, v *coverlayout.View) string {
	switch {
	case o.Layout != nil && o.Layout.Shape == coverlayout.ShapeWrap:
		return s.tr(r, "cmp.cv_art_note_wrap")
	case o.Layout == nil || v == nil:
		return s.tr(r, "cmp.cv_art_note_plain")
	}
	n := s.tr(r, "cmp.cv_art_note_sides")
	for _, sd := range []*coverlayout.Side{v.Front, v.Back} {
		if sd != nil && sd.Mode == coverlayout.ModeGenerated {
			return n + " " + s.tr(r, "cmp.cv_art_note_type")
		}
	}
	return n
}
