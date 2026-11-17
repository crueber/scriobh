package publish

import "math"

// Print layout is done in integers: lengths are milli-points (thousandths of a
// point, 72 to the inch), so the same input gives the same lines, pages and
// bytes on any processor. (Go may fuse a floating-point multiply and add on
// some CPUs and not on others, which can move a line break.)

// mpt is a length in thousandths of a point.
type mpt = int64

const (
	pt   mpt = 1000
	inch mpt = 72000
)

// PageSize returns a named page's width and height in milli-points.
func PageSize(name string) (w, h mpt, ok bool) {
	switch name {
	case "letter":
		return 612000, 792000, true
	case "a4":
		return 595276, 841890, true
	case "a5":
		return 419528, 595276, true
	case "5x8":
		return 5 * inch, 8 * inch, true
	case "5.25x8":
		return 378000, 8 * inch, true
	case "5.5x8.5":
		return 396000, 612000, true
	case "6x9":
		return 6 * inch, 9 * inch, true
	}
	return 0, 0, false
}

// inches converts an option in inches to milli-points, rounding to the nearest.
func inches(v float64) mpt {
	if v < 0 {
		return 0
	}
	return mpt(math.Round(v * 72000))
}

// params are the resolved measures of a print layout.
type params struct {
	pageW, pageH mpt
	// Margins as the book is bound: inside is at the spine, with the gutter.
	top, bottom, inside, outside mpt
	textW, textH                 mpt
	size                         mpt // body type size
	leading                      mpt // distance between baselines
	indent                       mpt
	spaceAfter                   mpt
	justify                      bool
	dropCap                      bool
	runningHeads                 bool
	ornamentChapters             bool
	toc                          bool
	cropMarks                    bool
	copyright                    bool
	recto                        bool // chapters start on a right-hand page
	continuous                   bool // chapters run on
	sink                         mpt  // how far down a chapter title starts
	folio                        string
	notes                        string // page, chapter, book
	bleed, slug                  mpt
	upperHead                    bool
	gutter                       mpt
}

// resolveParamsRaw is the margins and text block of a page of this size,
// before resolveParams repairs margins that leave too little room.
func resolveParamsRaw(o Options, w, h mpt) params {
	o.PageSize = ""
	p := resolveParamsAt(o, w, h, false)
	return p
}

// resolveParams reads the options (defaults for anything unset) into measures.
func resolveParams(o Options) params {
	w, h := 6*inch, 9*inch
	if pw, ph, ok := PageSize(o.PageSize); ok {
		w, h = pw, ph
	}
	return resolveParamsAt(o, w, h, true)
}

func resolveParamsAt(o Options, w, h mpt, repair bool) params {
	var p params
	p.pageW, p.pageH = w, h
	base := 0.75
	if o.MarginInches > 0 {
		base = o.MarginInches
	}
	pick := func(v *float64) mpt {
		if v != nil {
			return inches(*v)
		}
		return inches(base)
	}
	p.top, p.bottom, p.outside = pick(o.MarginTop), pick(o.MarginBottom), pick(o.MarginOutside)
	p.gutter = 0
	if o.Gutter != nil {
		p.gutter = inches(*o.Gutter)
	}
	p.inside = pick(o.MarginInside) + p.gutter
	// Margins never leave less than an inch of page to set type in (the
	// validation asks for more; this only keeps a hostile value from breaking the layout).
	if repair {
		for _, m := range []*mpt{&p.top, &p.bottom} {
			if p.pageH-*m*2 < inch {
				*m = (p.pageH - inch) / 2
			}
		}
		if p.pageW-p.inside-p.outside < inch {
			p.inside = (p.pageW - inch) / 2
			p.outside = p.inside
		}
	}
	p.textW = p.pageW - p.inside - p.outside
	p.textH = p.pageH - p.top - p.bottom
	size := o.FontSize
	if size <= 0 {
		size = 11
	}
	p.size = mpt(math.Round(size * 1000))
	p.justify = On(o.Justify) // a book is justified unless the preset says not
	if o.IndentInches != nil {
		p.indent = inches(*o.IndentInches)
	} else {
		p.indent = p.size * 3 / 2 // one and a half em
	}
	if o.SpaceAfterPt != nil {
		p.spaceAfter = mpt(math.Round(*o.SpaceAfterPt * 1000))
	}
	if p.indent > p.textW/3 { // an indent is never more than a third of the line
		p.indent = p.textW / 3
	}
	p.dropCap = Set(o.DropCap)
	p.runningHeads = On(o.RunningHeads)
	p.ornamentChapters = Set(o.ChapterOrnament)
	p.toc = On(o.TOC)
	p.cropMarks = Set(o.CropMarks)
	p.copyright = On(o.CopyrightPage)
	p.recto = o.ChapterStart == "recto"
	p.continuous = o.ChapterStart == "continuous"
	if o.ChapterDrop != nil {
		p.sink = inches(*o.ChapterDrop)
	} else {
		p.sink = inch + inch/2
	}
	p.folio = o.PageNumbers
	if p.folio == "" {
		p.folio = "footer"
	}
	p.notes = o.Footnotes
	if p.notes == "" {
		p.notes = "page"
	}
	p.upperHead = Set(o.HeaderUpper)
	if p.cropMarks {
		p.bleed, p.slug = inch/8, inch/4
	}
	return p
}
