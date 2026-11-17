package publish

import "strings"

// LayoutLine is a line of text as placed on a page, for a screen that draws a
// proof: the baseline's position from the top left of the trim box, in points,
// the type size and the text.
type LayoutLine struct {
	X    float64 `json:"x"`
	Y    float64 `json:"y"`
	Size float64 `json:"size"`
	Text string  `json:"text"`
	Kind string  `json:"kind" enum:"text,heading,note,head,folio,image,table"`
}

// LayoutPage is a page of the layout.
type LayoutPage struct {
	Number int          `json:"number" desc:"The physical page, from 1."`
	Label  string       `json:"label" desc:"The number a reader sees: roman in the front matter, then arabic from 1."`
	Recto  bool         `json:"recto" desc:"A right-hand page."`
	Blank  bool         `json:"blank,omitempty"`
	Lines  []LayoutLine `json:"lines"`
}

const maxPreviewText = 160

func clipText(s string) string {
	r := []rune(s)
	if len(r) > maxPreviewText {
		return string(r[:maxPreviewText]) + "…"
	}
	return s
}

// lineString is the text of a broken line.
func lineString(p *para, ln lineRec) string {
	var b strings.Builder
	for i := ln.from; i < ln.to && int(i) < len(p.items); i++ {
		it := p.items[i]
		switch it.kind {
		case iBox:
			for r := it.r0; r < it.r1; r++ {
				b.WriteString(string(p.runs[r].runes))
			}
		case iGlue:
			if it.w > 0 {
				b.WriteByte(' ')
			}
		}
	}
	return strings.TrimSpace(b.String())
}

func pointsOf(v mpt) float64 { return float64(v) / 1000 }

// Page describes page i (0-based), up to maxLines lines.
func (l *PDFLayout) Page(i, maxLines int) LayoutPage {
	sh := l.sheets[i]
	pr := l.pr
	recto := i%2 == 0
	x0 := pr.outside
	if recto {
		x0 = pr.inside
	}
	pg := LayoutPage{Number: i + 1, Label: l.PageLabel(i), Recto: recto, Blank: sh.kind == pageBlank, Lines: []LayoutLine{}}
	add := func(ln LayoutLine) {
		if len(pg.Lines) < maxLines {
			pg.Lines = append(pg.Lines, ln)
		}
	}
	emit := func(it *vitem, y mpt, kind string) {
		switch it.kind {
		case vLine:
			if it.par == nil {
				return
			}
			txt := lineString(it.par, it.ln)
			if it.first && it.par.dropCap != nil {
				txt = string(it.par.dropCap.run.runes) + txt
			}
			size := mpt(0)
			if len(it.par.runs) > 0 {
				size = it.par.runs[0].size
			}
			// where the line starts as drawn: a centred or right-aligned line starts inside its measure (drawLine)
			x := it.x + it.ln.indent
			switch it.align {
			case alignCenter:
				x = it.x + (it.w-it.ln.nat)/2
			case alignRight:
				x = it.x + it.w - it.ln.nat
			}
			add(LayoutLine{X: pointsOf(x0 + x), Y: pointsOf(pr.top + y + it.base), Size: pointsOf(size), Text: clipText(txt), Kind: kind})
		case vTOC:
			label := ""
			if n := l.SectionPage(it.toc.sec); n > 0 {
				label = l.PageLabel(n - 1)
			}
			var b strings.Builder
			for _, r := range it.toc.runs {
				b.WriteString(string(r.runes))
			}
			add(LayoutLine{X: pointsOf(x0 + it.x), Y: pointsOf(pr.top + y + it.base), Size: pointsOf(l.t.size), Text: clipText(b.String() + "\t" + label), Kind: "text"})
		case vImage:
			add(LayoutLine{X: pointsOf(x0 + it.img.x), Y: pointsOf(pr.top + y + it.h), Size: pointsOf(it.img.h), Text: "[image]", Kind: "image"})
		case vRow:
			var cells []string
			for _, c := range it.row.cells {
				if it.row.from < len(c.lines) {
					cells = append(cells, lineString(c.par, c.lines[it.row.from]))
				} else {
					cells = append(cells, "")
				}
			}
			add(LayoutLine{X: pointsOf(x0 + it.row.x), Y: pointsOf(pr.top + y + it.h), Size: pointsOf(it.row.size), Text: clipText(strings.Join(cells, " | ")), Kind: "table"})
		}
	}
	for _, p := range sh.items {
		kind := "text"
		if p.it.sec > 0 || p.it.opener {
			kind = "heading"
		}
		emit(p.it, p.y, kind)
	}
	if nl := sh.noteLines(); len(nl) > 0 {
		y := pr.textH - sh.noteH + l.t.lead*2
		for _, it := range nl {
			emit(it, y, "note")
			y += it.h
		}
	}
	if sh.kind == pageBody && !sh.noHead && pr.runningHeads {
		txt := RunningTitle(l.b, l.o)
		if !recto {
			txt = CleanLine(l.b.Byline)
		}
		if txt != "" {
			add(LayoutLine{X: pointsOf(x0 + pr.textW/2), Y: pointsOf(pr.top / 2), Size: pointsOf(l.t.size * 80 / 100), Text: clipText(txt), Kind: "head"})
		}
	}
	if !sh.noFolio && pr.folio != "none" {
		add(LayoutLine{X: pointsOf(x0 + pr.textW/2), Y: pointsOf(pr.top + pr.textH + pr.bottom/2), Size: pointsOf(l.t.size * 90 / 100), Text: l.PageLabel(i), Kind: "folio"})
	}
	return pg
}

// FrontPages is how many pages are numbered in roman before the body.
func (l *PDFLayout) FrontPages() int { return l.bodyStart }

// BodyStart is the physical page (from 1) that is numbered 1.
func (l *PDFLayout) BodyStart() int { return l.bodyStart + 1 }

// Metrics describes the layout for a preview.
type LayoutMetrics struct {
	PageWidthPt   float64 `json:"page_width_pt"`
	PageHeightPt  float64 `json:"page_height_pt"`
	TextWidthPt   float64 `json:"text_width_pt"`
	TextHeightPt  float64 `json:"text_height_pt"`
	InsidePt      float64 `json:"inside_margin_pt" desc:"The inside margin with the gutter."`
	OutsidePt     float64 `json:"outside_margin_pt"`
	TopPt         float64 `json:"top_margin_pt"`
	BottomPt      float64 `json:"bottom_margin_pt"`
	FontSizePt    float64 `json:"font_size_pt"`
	LeadingPt     float64 `json:"leading_pt"`
	LinesPerPage  int     `json:"lines_per_page"`
	MediaWidthPt  float64 `json:"media_width_pt" desc:"The page with bleed and crop-mark slug when crop marks are on; otherwise the trim size."`
	MediaHeightPt float64 `json:"media_height_pt"`
}

// Metrics returns the geometry of the layout.
func (l *PDFLayout) Metrics() LayoutMetrics {
	pr := l.pr
	m := LayoutMetrics{PageWidthPt: pointsOf(pr.pageW), PageHeightPt: pointsOf(pr.pageH), TextWidthPt: pointsOf(pr.textW), TextHeightPt: pointsOf(pr.textH),
		InsidePt: pointsOf(pr.inside), OutsidePt: pointsOf(pr.outside), TopPt: pointsOf(pr.top), BottomPt: pointsOf(pr.bottom),
		FontSizePt: pointsOf(pr.size), LeadingPt: pointsOf(l.t.lead), LinesPerPage: l.LinesPerPage()}
	m.MediaWidthPt, m.MediaHeightPt = m.PageWidthPt, m.PageHeightPt
	if pr.cropMarks {
		m.MediaWidthPt += 2 * pointsOf(pr.bleed+pr.slug)
		m.MediaHeightPt += 2 * pointsOf(pr.bleed+pr.slug)
	}
	return m
}
