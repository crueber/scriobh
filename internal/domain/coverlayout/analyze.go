package coverlayout

import (
	"fmt"
	"image"
)

// Rect is a rectangle in a picture's pixels.
type Rect struct {
	X0 int `json:"x0"`
	Y0 int `json:"y0"`
	X1 int `json:"x1"`
	Y1 int `json:"y1"`
}

func rect(r image.Rectangle) Rect { return Rect{r.Min.X, r.Min.Y, r.Max.X, r.Max.Y} }

// Measured is a wrap picture measured against the layout's trim and bleed.
type Measured struct {
	WidthPx    int     `json:"width_px"`
	HeightPx   int     `json:"height_px"`
	DPI        float64 `json:"dpi" desc:"The picture's resolution: its height over the trim height plus two bleeds."`
	WidthIn    float64 `json:"width_in"`
	HeightIn   float64 `json:"height_in"`
	BleedIn    float64 `json:"bleed_in"`
	SpineIn    float64 `json:"spine_in" desc:"Measured from the picture's width: the width less two trim widths and two bleeds."`
	FrontOn    string  `json:"front_on" enum:"left,right" desc:"Which side the front is on: the right for a book read left to right, the left for right to left."`
	Front      Rect    `json:"front" desc:"The front cut at the trim, without bleed, in the picture's pixels."`
	Back       Rect    `json:"back" desc:"The back cut at the trim, without bleed."`
	Spine      Rect    `json:"spine"`
	FrontBleed Rect    `json:"front_bleed" desc:"The front with the outer bleed, as a print cover places it."`
	BackBleed  Rect    `json:"back_bleed"`
}

func measured(g WrapGeometry, bleedIn float64) *Measured {
	on := "right"
	if !g.FrontOnRight {
		on = "left"
	}
	return &Measured{WidthPx: g.Width, HeightPx: g.Height, DPI: round2(g.DPI), WidthIn: round3(g.WidthIn), HeightIn: round3(g.HeightIn), BleedIn: bleedIn,
		SpineIn: round3(g.SpineIn), FrontOn: on, Front: rect(g.Front), Back: rect(g.Back), Spine: rect(g.Spine),
		FrontBleed: rect(g.FrontBleed), BackBleed: rect(g.BackBleed)}
}

func round2(v float64) float64 { return float64(roundPx(v*100)) / 100 }
func round3(v float64) float64 { return float64(roundPx(v*1000)) / 1000 }

// PictureInfo is one placed picture: what it is and how it comes out at the trim.
type PictureInfo struct {
	Part       string  `json:"part" enum:"front,back,wrap"`
	Attachment string  `json:"attachment"`
	WidthPx    int     `json:"width_px"`
	HeightPx   int     `json:"height_px"`
	Bytes      int64   `json:"bytes"`
	DPI        float64 `json:"dpi" desc:"The picture's resolution at the size it is placed on the trim's page: for a wrap, its measured resolution."`
	Crop       *Rect   `json:"crop,omitempty" desc:"Image side only: the part of the picture that is shown, cut from its centre."`
	CropLoss   float64 `json:"crop_loss,omitempty" desc:"Image side only: the share of the picture cut away, 0 to 1."`
}

// Analysis is what a layout comes to against its pictures.
type Analysis struct {
	Pictures []PictureInfo
	Wrap     *Measured
	Warnings []Warning
}

// analyze measures a layout. With strict, a wrap that is not a wrap for the trim is an error (a layout may not be
// saved so); without it the same finding is a warning (a stored layout is always shown, however it came to be). Strict
// also runs PlacePage for each page side, so a placement that cannot be drawn (a picture over 8 times the page, under
// a pixel, or a margin with no room) is refused with the code the cover form shows, instead of failing when the
// derivative is made.
func analyze(l *Layout, look func(string) (Picture, bool), rtl bool, strict bool) (Analysis, error) {
	var a Analysis
	t, ok := LookupTrim(l.Trim)
	if !ok {
		return a, &Refusal{"cover_trim", "The trim is not one this version knows."}
	}
	lowRes := func(part string, dpi float64) {
		if dpi < PrintDPI {
			a.Warnings = append(a.Warnings, Warning{Code: "cover_low_resolution", Part: part,
				Message: fmt.Sprintf("At the size it is placed on a %s page the %s picture is %.0f dpi; 300 is advised for print. It is never enlarged to fill the gap.", trimLabel(t), part, dpi)})
		}
	}
	if l.Shape == ShapeWrap && l.Wrap != nil {
		p, ok := look(l.Wrap.Attachment)
		if !ok {
			a.Warnings = append(a.Warnings, Warning{Code: "cover_picture_missing", Part: "wrap", Message: "The wrap picture is no longer there."})
			return a, nil
		}
		g, err := MeasureWrap(p.Width, p.Height, t, l.Wrap.Bleed(), rtl)
		if err != nil {
			if strict {
				return a, err
			}
			var r *Refusal
			msg := err.Error()
			code := "cover_wrap_invalid"
			if ok := asRefusal(err, &r); ok {
				code = r.Code
			}
			a.Warnings = append(a.Warnings, Warning{Code: code, Part: "wrap", Message: msg})
			a.Pictures = append(a.Pictures, PictureInfo{Part: "wrap", Attachment: p.ID, WidthPx: p.Width, HeightPx: p.Height, Bytes: p.Bytes})
			return a, nil
		}
		a.Wrap = measured(g, l.Wrap.Bleed())
		a.Pictures = append(a.Pictures, PictureInfo{Part: "wrap", Attachment: p.ID, WidthPx: p.Width, HeightPx: p.Height, Bytes: p.Bytes, DPI: round2(g.DPI)})
		lowRes("wrap", g.DPI)
		return a, nil
	}
	var sideErr error
	side := func(part string, s Side) {
		if s.Mode != ModeImage && s.Mode != ModePage {
			return
		}
		p, ok := look(s.Attachment)
		if !ok {
			a.Warnings = append(a.Warnings, Warning{Code: "cover_picture_missing", Part: part, Message: fmt.Sprintf("The %s picture is no longer there.", part)})
			return
		}
		info := PictureInfo{Part: part, Attachment: p.ID, WidthPx: p.Width, HeightPx: p.Height, Bytes: p.Bytes}
		switch s.Mode {
		case ModeImage:
			cr := CropToAspect(p.Width, p.Height, t)
			info.DPI = round2(float64(cr.Dy()) / t.H)
			if loss := CropLoss(p.Width, p.Height, t); loss > 0.005 {
				c := rect(cr)
				info.Crop, info.CropLoss = &c, round3(loss)
				a.Warnings = append(a.Warnings, Warning{Code: "cover_cropped", Part: part,
					Message: fmt.Sprintf("The %s picture is not in the proportions of a %s page, so %.0f%% of it is cut away, evenly from the centre. Use a page side with fit inside to keep all of it.", part, trimLabel(t), loss*100)})
			}
		case ModePage:
			pl, err := PlacePage(p.Width, p.Height, t, s)
			if err != nil && strict && sideErr == nil { // a placement that cannot be drawn is not saved
				sideErr = err
			}
			if err == nil && pl.Dst.Dx() > 0 {
				ppi := float64(pl.CanvasW) / t.W
				info.DPI = round2(ppi * float64(p.Width) / float64(pl.Dst.Dx()))
				if pl.Clamped {
					a.Warnings = append(a.Warnings, Warning{Code: "cover_picture_small", Part: part,
						Message: fmt.Sprintf("The %s picture is %d by %d pixels, too small to fill its place on a %s page, and a cover never enlarges a picture: it sits at its own size, %.0f%% of the page's width. Use a larger picture to fill the page.",
							part, p.Width, p.Height, trimLabel(t), 100*float64(pl.Dst.Dx())/float64(pl.CanvasW))})
				}
			}
		}
		a.Pictures = append(a.Pictures, info)
		if info.DPI > 0 {
			lowRes(part, info.DPI)
		}
	}
	side("front", l.Front)
	side("back", l.Back)
	if sideErr != nil {
		return a, sideErr
	}
	return a, nil
}

func asRefusal(err error, target **Refusal) bool {
	r, ok := err.(*Refusal)
	if ok {
		*target = r
	}
	return ok
}
