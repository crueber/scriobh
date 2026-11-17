package publish

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/publish/pdfw"
	"github.com/crueber/scriobh/internal/text"
)

// CoverSpec describes a print cover: the flat-colour wrap for a paperback
// (back, spine, front, with bleed) and the front alone.
type CoverSpec struct {
	Title, Subtitle, Author, Kind string
	Color                         string // "#RRGGBB", one flat fill
	TrimW, TrimH                  mpt
	Spine                         mpt
	Bleed                         mpt
	Guides                        bool // trim, spine and safe-area lines in the bleed and on the art
	Barcode                       bool // a white box for the barcode on the back
	Language, Identifier          string
	InputHash                     string
	Modified                      time.Time
}

// SpineWidth is the width of a spine: the number of pages times the paper's
// thickness per page (the caliper of a leaf is two pages' worth), both in the
// units of the result. The thickness is the printer's figure, never a guess.
func SpineWidth(pages int, thicknessPerPage mpt) mpt { return mpt(pages) * thicknessPerPage }

// MinSpineText is the narrowest spine that can carry type (printers commonly
// ask for 0.125 in of space each side, so about a quarter of an inch).
const MinSpineText = inch / 4

func parseHex(c string) (r, g, b uint8, ok bool) {
	c = strings.TrimPrefix(c, "#")
	if len(c) != 6 {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(c, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return uint8(v >> 16), uint8(v >> 8), uint8(v), true
}

// WriteCoverPDF writes a two-page PDF: the full wrap (back, spine and front,
// with bleed) and the front cover alone at trim size with bleed. Both are a
// flat colour with white type, no gradient and no image.
func WriteCoverPDF(cs CoverSpec) ([]byte, []Warn, error) {
	if cs.TrimW < inch || cs.TrimH < inch || cs.Bleed < 0 || cs.Spine < 0 {
		return nil, nil, fmt.Errorf("publish: a cover needs a trim size and a non-negative spine and bleed")
	}
	cr, cg, cb, ok := parseHex(cs.Color)
	if !ok {
		cr, cg, cb = 0x2F, 0x5D, 0x50
	}
	ts, err := newTypeface("")
	if err != nil {
		return nil, nil, err
	}
	o := printDefaults(Options{FontSize: 12})
	pr := resolveParams(o)
	t := newTypesetter(&Book{}, o, ts, pr)
	d := pdfw.New()
	d.Title = CleanLine(cs.Title)
	d.Author = CleanLine(cs.Author)
	d.Subject = "Cover"
	d.Language = firstNonEmpty(cs.Language, "en")
	if !validLang(d.Language) {
		d.Language = "en"
	}
	if !cs.Modified.IsZero() {
		m := cs.Modified.UTC()
		d.Created = [6]int{m.Year(), int(m.Month()), m.Day(), m.Hour(), m.Minute(), m.Second()}
	}
	d.ID = pdfw.DocumentID("scriobh-cover", cs.InputHash, cs.Title, cs.Identifier)
	var warns []Warn
	r := &renderer{l: &PDFLayout{ts: ts, t: t, pr: pr}, d: d, fonts: map[*faceRT]*pdfw.Font{}}

	// A panel is a trim-sized area with its own origin; items are placed from
	// its top left.
	drawPanel := func(c *pdfw.Content, left, top mpt, front bool) {
		w, h := cs.TrimW, cs.TrimH
		margin := w * 12 / 100
		textW := w - 2*margin
		c.FillGray(1000)
		if front {
			size := mpt(math.Round(float64(t.size) * 3.2))
			lh := t.lines(size * 12 / 10)
			title := CleanLine(cs.Title)
			if title == "" {
				title = "Untitled"
			}
			// shrink the title until it fits in a third of the panel
			var ls []*vitem
			for {
				ls, _ = t.paragraph([]text.Inline{{Kind: text.IText, Text: title}}, paraOpt{size: size, style: stBold, lead: lh, x: 0, w: textW, align: alignCenter, ragged: true, noNotes: true, fitWords: true})
				if heightOf(ls) <= h*38/100 || size <= t.size*14/10 {
					break
				}
				size = size * 9 / 10
				lh = t.lines(size * 12 / 10)
			}
			y := h * 16 / 100
			g := geom{x0: left + margin, yTop: top}
			for _, it := range ls {
				r.item(c, g, it, y)
				y += it.h
			}
			if sub := CleanLine(cs.Subtitle); sub != "" {
				ss := t.size * 15 / 10
				sl := t.lines(ss * 12 / 10)
				sls, _ := t.paragraph([]text.Inline{{Kind: text.IText, Text: sub}}, paraOpt{size: ss, style: stItalic, lead: sl, x: 0, w: textW, align: alignCenter, ragged: true, noNotes: true, fitWords: true})
				y += t.lead
				for _, it := range sls {
					r.item(c, g, it, y)
					y += it.h
				}
			}
			if a := CleanLine(cs.Author); a != "" {
				as := t.size * 18 / 10
				al := t.lines(as * 12 / 10)
				als, _ := t.paragraph([]text.Inline{{Kind: text.IText, Text: a}}, paraOpt{size: as, lead: al, x: 0, w: textW, align: alignCenter, ragged: true, noNotes: true, fitWords: true})
				yy := h*88/100 - heightOf(als)
				for _, it := range als {
					r.item(c, g, it, yy)
					yy += it.h
				}
			}
			return
		}
		// back: the kind of book and the author, a place for the barcode
		g := geom{x0: left + margin, yTop: top}
		y := h * 18 / 100
		if k := CleanLine(cs.Kind); k != "" {
			ks, _ := t.paragraph([]text.Inline{{Kind: text.IText, Text: strings.ToUpper(k)}}, paraOpt{size: t.size * 9 / 10, style: stBold, x: 0, w: textW, align: alignCenter, ragged: true, noNotes: true, fitWords: true})
			for _, it := range ks {
				r.item(c, g, it, y)
				y += it.h
			}
		}
		if cs.Barcode {
			bw, bh := inch*2, inch*6/5
			c.Save()
			c.FillGray(1000)
			c.FillRect(left+w-margin/2-bw, r.mediaH-(top+h-margin/2), bw, bh)
			c.Restore()
		}
	}

	line := func(c *pdfw.Content, x0, y0, x1, y1 mpt) { c.Line(x0, y0, x1, y1) }
	guides := func(c *pdfw.Content, left, bottom, w, h mpt, spine mpt) {
		if !cs.Guides {
			return
		}
		c.Save()
		c.StrokeGray(1000)
		c.LineWidth(400)
		c.Dash(3*pt, 3*pt)
		c.StrokeRect(left, bottom, w, h) // trim
		if spine > 0 {
			line(c, left+cs.TrimW, bottom, left+cs.TrimW, bottom+h)
			line(c, left+cs.TrimW+spine, bottom, left+cs.TrimW+spine, bottom+h)
		}
		safe := inch / 8
		c.StrokeRect(left+safe, bottom+safe, w-2*safe, h-2*safe)
		c.Restore()
	}

	// Page 1: the wrap.
	wrapW := 2*cs.Bleed + 2*cs.TrimW + cs.Spine
	wrapH := cs.TrimH + 2*cs.Bleed
	pg := d.NewPage(wrapW, wrapH)
	pg.Trim = [4]int64{cs.Bleed, cs.Bleed, wrapW - cs.Bleed, wrapH - cs.Bleed}
	pg.Bleed = [4]int64{0, 0, wrapW, wrapH}
	if cs.Bleed == 0 {
		pg.Trim, pg.Bleed = [4]int64{}, [4]int64{}
	}
	r.mediaH = wrapH
	c := pg.Content
	c.FillRGB(cr, cg, cb)
	c.FillRect(0, 0, wrapW, wrapH)
	drawPanel(c, cs.Bleed, cs.Bleed, false)
	drawPanel(c, cs.Bleed+cs.TrimW+cs.Spine, cs.Bleed, true)
	if cs.Spine >= MinSpineText {
		var b strings.Builder
		b.WriteString(CleanLine(cs.Title))
		if a := CleanLine(cs.Author); a != "" {
			b.WriteString("   ·   " + a)
		}
		size := t.size * 12 / 10
		txt := t.fit(b.String(), stBold, size, cs.TrimH*80/100)
		runs := ts.shape(cleanText(txt), stBold, size, 0)
		c.FillGray(1000)
		e := &emitter{r: r, c: c}
		c.BeginText()
		// reads from the top down, centred on the spine and on the height
		var w mpt
		for _, rn := range runs {
			w += rn.width
		}
		c.TextAtRotated(cs.Bleed+cs.TrimW+cs.Spine/2-size*7/20, wrapH-cs.Bleed-(cs.TrimH-w)/2, false)
		for i := range runs {
			e.run(&runs[i])
		}
		e.flush()
		e.reset()
		c.EndText()
	} else if cs.Spine > 0 {
		warns = append(warns, Warn{"spine_narrow", fmt.Sprintf("The spine is %.3f in wide, too narrow for type (printers ask for about a quarter of an inch); the spine is left blank.", float64(cs.Spine)/72000)})
	}
	guides(c, cs.Bleed, cs.Bleed, wrapW-2*cs.Bleed, cs.TrimH, cs.Spine)

	// Page 2: the front on its own.
	fw, fh := cs.TrimW+2*cs.Bleed, cs.TrimH+2*cs.Bleed
	pg2 := d.NewPage(fw, fh)
	if cs.Bleed > 0 {
		pg2.Trim = [4]int64{cs.Bleed, cs.Bleed, fw - cs.Bleed, fh - cs.Bleed}
		pg2.Bleed = [4]int64{0, 0, fw, fh}
	}
	r.mediaH = fh
	c2 := pg2.Content
	c2.FillRGB(cr, cg, cb)
	c2.FillRect(0, 0, fw, fh)
	drawPanel(c2, cs.Bleed, cs.Bleed, true)
	if cs.Guides {
		c2.Save()
		c2.StrokeGray(1000)
		c2.LineWidth(400)
		c2.Dash(3*pt, 3*pt)
		c2.StrokeRect(cs.Bleed, cs.Bleed, cs.TrimW, cs.TrimH)
		c2.Restore()
	}
	out, err := d.Finish()
	return out, warns, err
}
