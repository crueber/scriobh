package coverlayout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"runtime/debug"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
)

// The print cover (docs/COVERS.md, "As built in C2"): a layout turned into what the PDF writer places. The plan is
// publish's (publish.PlanCover): this file only translates a layout and its pictures' sizes into it, so the cover check
// (no picture is read) and the export (the pictures are read) give the same warnings.

// Art is a layout as the print cover places it, with the pictures it needs read when Load has run.
type Art struct {
	Art *publish.CoverArt
	// Layout is the layout it was made from.
	Layout *Layout
	// RTL is the book's direction: the front is on the left of a right-to-left book's wrap.
	RTL bool

	refs []artRef
}

type artRef struct {
	img  *publish.CoverImage
	id   string
	part string
}

// PrintArt turns a layout into the print cover's art, from the pictures' sizes (the database's), reading no file. A wrap
// that is not a wrap for the layout's trim, or a placement that cannot be drawn, is a refusal.
func PrintArt(l *Layout, look func(string) (Picture, bool), rtl bool) (*Art, error) {
	t, ok := LookupTrim(l.Trim)
	if !ok {
		return nil, &Refusal{"cover_trim", "The trim is not one this version knows."}
	}
	a := &Art{Layout: l, RTL: rtl, Art: &publish.CoverArt{Shape: l.Shape}}
	img := func(p Picture, part string) *publish.CoverImage {
		ext := p.Ext
		if ext == "jpeg" {
			ext = "jpg"
		}
		ci := &publish.CoverImage{Ext: ext, Hash: p.SHA256, W: p.Width, H: p.Height}
		a.refs = append(a.refs, artRef{img: ci, id: p.ID, part: part})
		return ci
	}
	if l.Shape == ShapeWrap {
		if l.Wrap == nil {
			return nil, &Refusal{"cover_wrap_missing", "The layout has no wrap picture."}
		}
		p, ok := look(l.Wrap.Attachment)
		if !ok {
			return nil, &Refusal{"cover_picture_missing", "The wrap picture is no longer there."}
		}
		g, err := MeasureWrap(p.Width, p.Height, t, l.Wrap.Bleed(), rtl)
		if err != nil {
			return nil, err
		}
		a.Art.Wrap = &publish.CoverWrap{Image: *img(p, "wrap"), BleedIn: l.Wrap.Bleed(), DPI: g.DPI, SpineIn: g.SpineIn,
			Front: g.Front, Back: g.Back, FrontBleed: g.FrontBleed, BackBleed: g.BackBleed, Spine: g.Spine, FrontOnRight: g.FrontOnRight}
		// the copy in the art is the one the reference must fill
		a.refs[len(a.refs)-1].img = &a.Art.Wrap.Image
		return a, nil
	}
	panel := func(s Side, part string) (publish.CoverPanel, error) {
		switch s.Mode {
		case ModeImage, ModePage:
			p, ok := look(s.Attachment)
			if !ok {
				return publish.CoverPanel{}, &Refusal{"cover_picture_missing", fmt.Sprintf("The %s picture is no longer there.", part)}
			}
			cp := publish.CoverPanel{Mode: s.Mode, Image: img(p, part)}
			if s.Mode == ModePage {
				pl, err := PlacePage(p.Width, p.Height, t, s)
				if err != nil {
					return cp, err
				}
				cp.Place = &publish.CoverPlacement{CanvasW: pl.CanvasW, CanvasH: pl.CanvasH, Box: pl.Box, Dst: pl.Dst}
				cp.Background = s.Background
			}
			return cp, nil
		case ModeGenerated:
			return publish.CoverPanel{Mode: publish.PanelGenerated}, nil
		}
		return publish.CoverPanel{Mode: publish.PanelNone}, nil
	}
	var err error
	if a.Art.Front, err = panel(l.Front, "front"); err != nil {
		return nil, err
	}
	if a.Art.Back, err = panel(l.Back, "back"); err != nil {
		return nil, err
	}
	return a, nil
}

// Load reads the pictures' bytes (through the checks every file read goes through), takes each one's colour profile
// (carried only when it is an RGB profile of at most a megabyte, as the derivatives do) and checks the file is the
// picture the database says. It returns the colour warnings: a profile that is not sRGB goes into the PDF as the
// picture's own colour space, and a profile left out says so.
func (a *Art) Load(ctx context.Context, env *ops.Env, projectID string) ([]Warning, error) {
	var ws []Warning
	for _, r := range a.refs {
		raw, err := readPicture(ctx, env, projectID, r.id)
		if err != nil {
			return nil, err
		}
		w, h, format, cmyk, herr := Header(raw)
		if herr != nil || cmyk || w != r.img.W || h != r.img.H {
			return nil, &Refusal{"cover_picture_damaged", fmt.Sprintf("The %s picture is not the one that was uploaded (damaged or changed on disk).", r.part)}
		}
		switch format {
		case "jpeg":
			r.img.Ext = "jpg"
		case "png":
			r.img.Ext = "png"
		}
		r.img.Data = raw
		icc, had := profileOf(raw)
		r.img.ICC = icc
		part := r.part
		if had && icc == nil {
			ws = append(ws, Warning{Code: "cover_profile_dropped", Part: part,
				Message: fmt.Sprintf("The %s picture carries a colour profile the print cover leaves out (it is over 1 MB, or not an RGB profile): its numbers go into the PDF as DeviceRGB, which a printer reads as sRGB.", part)})
		}
		if icc != nil && !isSRGB(raw, icc) {
			ws = append(ws, Warning{Code: "cover_profile_not_srgb", Part: part,
				Message: fmt.Sprintf("The %s picture is not in sRGB. Its own profile goes into the PDF, so a printer that honours profiles converts it correctly; one that ignores them shows it duller or different. Converting it to sRGB yourself is the surest.", part)})
		}
	}
	return ws, nil
}

// Pictures lists the cover pictures the art holds.
func (a *Art) Pictures() []*publish.CoverImage {
	var out []*publish.CoverImage
	for _, r := range a.refs {
		out = append(out, r.img)
	}
	return out
}

// HoldDecode reserves, from the memory every cover decode shares (decodeMem), what the PDF writer will need to decode
// the art's pictures: a PNG with an alpha channel, transparency, interlacing or 16 bits is decoded to samples, a JPEG
// and a plain PNG are passed through as they are and need nothing. It returns what to call when the file is written;
// the server is busy (429) when the memory is not to be had.
func (a *Art) HoldDecode(ctx context.Context) (release func(), err error) {
	// What a decode holds is the picture's samples (four bytes a pixel, eight at 16 bits a channel) and the three colours and
	// the alpha the writer keeps until the file is written; the collector lets a heap run to about twice what is live, and
	// measured peaks (docs/COVERS.md) are 1.8 times the live figure, so twice it is reserved, and room for the rest.
	var need int64
	for _, r := range a.refs {
		if r.img.Ext != "png" || len(r.img.Data) == 0 || !publish.PNGNeedsDecode(r.img.Data) {
			continue
		}
		px := int64(r.img.W) * int64(r.img.H)
		dec := int64(4)
		if len(r.img.Data) > 24 && r.img.Data[24] == 16 { // the bit depth, in the header
			dec = 8
		}
		need += 2 * (dec + 4) * px
	}
	if need == 0 {
		return func() {}, nil
	}
	rel, err := decodeMem.acquire(ctx, need+decodeRoom)
	if err != nil {
		return nil, err
	}
	return func() {
		rel()
		if need > 160<<20 { // a big picture: give what it took back to the system now, not at the collector's leisure
			debug.FreeOSMemory()
		}
	}, nil
}

// ProfileOf is the colour profile of a PNG or JPEG that a cover carries: an RGB profile of at most MaxProfileBytes.
func ProfileOf(b []byte) []byte {
	icc, _ := profileOf(b)
	return icc
}

// ApplyTo puts the art on a cover spec.
func (a *Art) ApplyTo(cs *publish.CoverSpec) { cs.Art = a.Art; cs.RTL = a.RTL }

// PrintHash is what the print cover's input hash holds of the layout: the shape, the trim, the sides or the wrap with
// the pictures' own hashes (the whole layout: a print cover shows both sides and the alt text it does not), and the
// direction the book reads. The layout's own hash already covers all of that except the direction.
func (l *Layout) PrintHash(rtl bool) string {
	d := "ltr"
	if rtl {
		d = "rtl"
	}
	return l.Hash + "/" + d
}

// TrimOf is the layout's trim.
func (l *Layout) TrimOf() (TrimIn, bool) { return LookupTrim(l.Trim) }

// ForPrint loads a project's layout and turns it into the print cover's art (no picture is read: Load does that). It
// returns nil, nil, nil when the project has no layout. language "" is the project's own; a compile's language option
// may turn a wrap round. A layout that cannot be placed (a wrap that is not a wrap for its trim, a placement that cannot
// be drawn) is a refusal with the sentence that says why.
func ForPrint(ctx context.Context, q Queryer, projectID, language string) (*Layout, *Art, error) {
	l, err := Load(ctx, q, projectID)
	if err != nil || l == nil {
		return nil, nil, err
	}
	pr, err := loadProject(ctx, q, projectID)
	if err != nil {
		return nil, nil, ops.NotFound("project")
	}
	if language != "" {
		pr.Language = language
	}
	a, err := PrintArt(l, newPictureSet(ctx, q, projectID).look, pr.rtl())
	if err != nil {
		return l, nil, refuse(err)
	}
	return l, a, nil
}

// BackHash is FrontHash for the back: the hash of what shows the back of the cover and nothing else (the shape, the trim,
// the back side, or the wrap with the side of it the book's direction puts the back on, and the pictures' own hashes).
// It is what an export that shows the back (the EPUB's back_cover) puts in its input hash.
func (l *Layout) BackHash(look func(id string) (Picture, bool), rtl bool) string {
	sha := func(id string) string {
		if p, ok := look(id); ok {
			return p.SHA256
		}
		return "missing:" + id
	}
	h := sha256.New()
	fmt.Fprintf(h, "cover-back/%d\x00%s\x00%s\x00", Version, l.Shape, l.Trim)
	if l.Shape == ShapeWrap && l.Wrap != nil {
		fmt.Fprintf(h, "wrap\x00%s\x00%g\x00%v\x00", sha(l.Wrap.Attachment), l.Wrap.Bleed(), rtl)
	} else {
		s := l.Back
		fmt.Fprintf(h, "side\x00%s\x00%s\x00%s\x00%d\x00%s\x00%s\x00%g\x00", s.Mode, sha(s.Attachment), s.Fit, s.Scale, s.Position, s.Background, s.MarginIn)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// BackHashOf loads a project's layout and gives its BackHash for a book in the language ("" is the project's own); the
// hash is "" when the project has no layout or the layout gives the back no picture.
func BackHashOf(ctx context.Context, q Queryer, projectID, language string) (hash string, lay *Layout, err error) {
	lay, err = Load(ctx, q, projectID)
	if err != nil || !lay.HasBack() {
		return "", lay, err
	}
	pr, err := loadProject(ctx, q, projectID)
	if err != nil {
		return "", lay, err
	}
	if language != "" {
		pr.Language = language
	}
	return lay.BackHash(newPictureSet(ctx, q, projectID).look, pr.rtl()), lay, nil
}
