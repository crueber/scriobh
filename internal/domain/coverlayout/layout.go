// Package coverlayout is a project's own cover: one layout per project, of two pictures placed on the front and
// the back, or one picture for the whole wrap. When a project has a layout it takes the place of the generated cover
// (internal/cover) wherever a cover is shown; when it has none, nothing changes. The design is docs/COVERS.md.
//
// The pictures are ordinary project attachments that the cover upload made (it cleans them: no EXIF, no GPS), the
// layout is a row in cover_layouts, and the pictures the screens and the ebooks show are derivatives made on first
// use and kept under files/projects/<id>/covers/.
package coverlayout

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/domain/projects"
)

// Version changes whenever the derivatives would come out differently, so a cached picture is never served for
// newer rules. It is part of every derivative's cache key.
const Version = 1

// Shapes of a layout.
const (
	ShapeSides = "sides"
	ShapeWrap  = "wrap"
)

// Modes of a side.
const (
	ModeGenerated = "generated"
	ModeImage     = "image"
	ModePage      = "page"
	ModeNone      = "none" // a back that is left out
)

// Bounds of a page side.
const (
	MinScale    = 10
	MaxScale    = 100
	MaxMarginIn = 1.5
	MaxAlt      = 500
)

// Side is one side of a cover. Image and page use Attachment (the id of a cover-clean picture of the project); the
// rest of the fields are a page side's.
type Side struct {
	Mode       string  `json:"mode" enum:"generated,image,page,none" desc:"generated: the typographic cover for this side; image: one picture filling the side (fill and crop from the centre); page: a picture placed on a page of the trim; none: no back (the back only)."`
	Attachment string  `json:"attachment,omitempty" desc:"The picture: the id project.cover_layout.upload returned. Image and page only."`
	Fit        string  `json:"fit,omitempty" enum:"contain,cover,actual" desc:"Page only. contain fits the picture inside the margin, cover fills it and crops, actual puts it on the page at 300 dpi. Default contain."`
	Scale      int     `json:"scale,omitempty" desc:"Page only. 10 to 100 percent of the fit. Default 100."`
	Position   string  `json:"position,omitempty" enum:"top-left,top,top-right,left,center,right,bottom-left,bottom,bottom-right" desc:"Page only. Where the picture sits on the page. Default center."`
	Background string  `json:"background,omitempty" desc:"Page only. One of the cover palette's colours (#RRGGBB). Default the project's cover colour."`
	MarginIn   float64 `json:"margin_in,omitempty" desc:"Page only. The margin around the picture in inches, 0 to 1.5. Default 0."`
}

// Wrap is the picture that spans back, spine and front. The spine is measured from the picture, never stored.
type Wrap struct {
	Attachment string   `json:"attachment" desc:"The picture: the id project.cover_layout.upload returned."`
	BleedIn    *float64 `json:"bleed_in,omitempty" desc:"The bleed the picture was made with, on its outer edges, 0 to 0.5 inches. Default 0.125; 0 is a wrap made without bleed."`
}

// Bleed is the wrap's bleed in inches.
func (w *Wrap) Bleed() float64 {
	if w == nil || w.BleedIn == nil {
		return DefaultBleedIn
	}
	return *w.BleedIn
}

// Layout is a project's cover layout as stored.
type Layout struct {
	ProjectID string `json:"-"`
	Shape     string `json:"shape" enum:"sides,wrap"`
	Trim      string `json:"trim" desc:"The page size the cover is made for: 6x9, 5.5x8.5, 5.25x8, 5x8, a5, letter or a4. It fixes the proportions of every side."`
	Front     Side   `json:"front,omitempty"`
	Back      Side   `json:"back,omitempty"`
	Wrap      *Wrap  `json:"wrap,omitempty"`
	Alt       string `json:"alt" desc:"The cover described in a sentence for screen readers. Required when a picture is used."`
	Revision  int    `json:"revision" desc:"The concurrency token: send it back with project.cover_layout.set."`
	Hash      string `json:"hash" desc:"Hash of the layout and the pictures' own hashes; part of every export's input hash."`
	CreatedBy string `json:"-"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

// Picture is what validation needs to know of an attachment.
type Picture struct {
	ID            string
	SHA256        string
	Mime, Ext     string
	Width, Height int
	Bytes         int64
}

// pictures lists the attachments a layout names, front first, with the part of the layout that names each.
func (l *Layout) attachments() []string {
	var out []string
	add := func(id string) {
		if id == "" {
			return
		}
		for _, x := range out {
			if x == id {
				return
			}
		}
		out = append(out, id)
	}
	if l.Shape == ShapeWrap {
		if l.Wrap != nil {
			add(l.Wrap.Attachment)
		}
		return out
	}
	add(l.Front.Attachment)
	add(l.Back.Attachment)
	return out
}

// HasFront says whether the layout gives the front a picture. A front that is generated is the generated cover.
func (l *Layout) HasFront() bool {
	if l == nil {
		return false
	}
	if l.Shape == ShapeWrap {
		return l.Wrap != nil && l.Wrap.Attachment != ""
	}
	return l.Front.Mode == ModeImage || l.Front.Mode == ModePage
}

// HasBack says whether the layout gives the back a picture.
func (l *Layout) HasBack() bool {
	if l == nil {
		return false
	}
	if l.Shape == ShapeWrap {
		return l.Wrap != nil && l.Wrap.Attachment != ""
	}
	return l.Back.Mode == ModeImage || l.Back.Mode == ModePage
}

// usesPicture says whether any side shows a picture, which is when the alt text is required.
func (l *Layout) usesPicture() bool { return l.HasFront() || l.HasBack() }

// Normalize fills the defaults of a layout and checks it against the pictures it names. look finds a picture of the
// project that the cover upload made (ok is false for one that is missing, foreign or not cover-clean); cover is the
// project's cover colour, the default background of a page side. The error is an *Refusal with a sentence.
func (l *Layout) Normalize(look func(id string) (Picture, bool), coverColor string) error {
	if l.Trim == "" {
		l.Trim = DefaultTrim
	}
	if _, ok := LookupTrim(l.Trim); !ok {
		return &Refusal{"cover_trim", "The trim is one of " + strings.Join(Trims, ", ") + "."}
	}
	l.Alt = tidyAlt(l.Alt)
	if utf8.RuneCountInString(l.Alt) > MaxAlt {
		return &Refusal{"cover_alt_long", fmt.Sprintf("The alt text is limited to %d characters.", MaxAlt)}
	}
	switch l.Shape {
	case ShapeSides:
		if l.Wrap != nil {
			return &Refusal{"cover_shape", "A cover of front and back sides has no wrap picture: send the wrap with shape wrap."}
		}
		if l.Front.Mode == "" {
			l.Front.Mode = ModeGenerated
		}
		if l.Back.Mode == "" {
			l.Back.Mode = ModeNone
		}
		if l.Front.Mode == ModeNone {
			return &Refusal{"cover_front_none", "The front is generated, an image or a page: only the back may be left out."}
		}
		if err := normalizeSide(&l.Front, "front", look, coverColor); err != nil {
			return err
		}
		if err := normalizeSide(&l.Back, "back", look, coverColor); err != nil {
			return err
		}
	case ShapeWrap:
		if l.Front.Mode != "" || l.Back.Mode != "" || l.Front.Attachment != "" || l.Back.Attachment != "" {
			return &Refusal{"cover_shape", "A cover made of one wrap picture has no front or back sides of its own."}
		}
		if l.Wrap == nil || l.Wrap.Attachment == "" {
			return &Refusal{"cover_wrap_missing", "Name the wrap picture: upload it with part wrap, then use its id."}
		}
		l.Front, l.Back = Side{}, Side{}
		bleed := l.Wrap.Bleed()
		if math.IsNaN(bleed) || bleed < 0 || bleed > MaxBleedIn {
			return &Refusal{"cover_wrap_bleed", fmt.Sprintf("The bleed is 0 to %g inches.", MaxBleedIn)}
		}
		bleed = math.Round(bleed*10000) / 10000
		l.Wrap.BleedIn = &bleed
		if _, ok := look(l.Wrap.Attachment); !ok {
			return &Refusal{"cover_picture_unknown", "The wrap picture is not one this project's cover upload made. Upload it again with project.cover_layout.upload."}
		}
	default:
		return &Refusal{"cover_shape", "The shape is sides or wrap."}
	}
	if l.usesPicture() && l.Alt == "" {
		return &Refusal{"cover_alt_required", "Describe the cover in a sentence (alt): a screen reader says it where the picture is."}
	}
	return nil
}

func normalizeSide(s *Side, name string, look func(string) (Picture, bool), coverColor string) error {
	switch s.Mode {
	case ModeGenerated, ModeNone:
		if s.Attachment != "" || s.Fit != "" || s.Scale != 0 || s.Position != "" || s.Background != "" || s.MarginIn != 0 {
			return &Refusal{"cover_side", fmt.Sprintf("The %s is %s: it takes no picture or placement.", name, s.Mode)}
		}
		return nil
	case ModeImage:
		if s.Fit != "" || s.Scale != 0 || s.Position != "" || s.Background != "" || s.MarginIn != 0 {
			return &Refusal{"cover_side", fmt.Sprintf("The %s is an image, which fills the side: fit, scale, position, background and margin belong to a page.", name)}
		}
	case ModePage:
		if s.Fit == "" {
			s.Fit = FitContain
		}
		if s.Fit != FitContain && s.Fit != FitCover && s.Fit != FitActual {
			return &Refusal{"cover_fit", "The fit is contain, cover or actual."}
		}
		if s.Scale == 0 {
			s.Scale = MaxScale
		}
		if s.Scale < MinScale || s.Scale > MaxScale {
			return &Refusal{"cover_scale", fmt.Sprintf("The scale is %d to %d percent.", MinScale, MaxScale)}
		}
		if s.Position == "" {
			s.Position = "center"
		}
		okPos := false
		for _, p := range Positions {
			okPos = okPos || p == s.Position
		}
		if !okPos {
			return &Refusal{"cover_position", "The position is one of " + strings.Join(Positions, ", ") + "."}
		}
		if math.IsNaN(s.MarginIn) || s.MarginIn < 0 || s.MarginIn > MaxMarginIn {
			return &Refusal{"cover_margin", fmt.Sprintf("The margin is 0 to %g inches.", MaxMarginIn)}
		}
		s.MarginIn = math.Round(s.MarginIn*1000) / 1000
		bg := strings.ToUpper(strings.TrimSpace(s.Background))
		if bg == "" {
			bg = strings.ToUpper(coverColor)
			if !inPalette(bg) {
				bg = projects.CoverPalette[0]
			}
		}
		if !inPalette(bg) {
			return &Refusal{"cover_background", "The background is one of the cover palette's colours: " + strings.Join(projects.CoverPalette, ", ") + "."}
		}
		s.Background = bg
	case "":
		return &Refusal{"cover_side", fmt.Sprintf("Give the %s a mode: generated, image or page.", name)}
	default:
		return &Refusal{"cover_side", fmt.Sprintf("The %s mode is generated, image or page (the back may also be none).", name)}
	}
	if s.Attachment == "" {
		return &Refusal{"cover_picture_missing", fmt.Sprintf("The %s needs a picture: upload it with project.cover_layout.upload, then use its id.", name)}
	}
	if _, ok := look(s.Attachment); !ok {
		return &Refusal{"cover_picture_unknown", fmt.Sprintf("The %s's picture is not one this project's cover upload made. Upload it again with project.cover_layout.upload.", name)}
	}
	return nil
}

func inPalette(c string) bool {
	for _, p := range projects.CoverPalette {
		if p == c {
			return true
		}
	}
	return false
}

func tidyAlt(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\r' && r != '\t' {
			return -1
		}
		return r
	}, s)), " "))
}

// ComputeHash is the layout's hash: the layout as written with each picture named by its own hash, not its id, so the
// same cover made twice is the same hash. look finds the pictures.
func (l *Layout) ComputeHash(look func(id string) (Picture, bool)) string {
	sha := func(id string) string {
		if id == "" {
			return ""
		}
		if p, ok := look(id); ok {
			return p.SHA256
		}
		return "missing:" + id
	}
	h := sha256.New()
	fmt.Fprintf(h, "cover-layout/%d\x00%s\x00%s\x00%s\x00", Version, l.Shape, l.Trim, l.Alt)
	side := func(s Side) {
		fmt.Fprintf(h, "side\x00%s\x00%s\x00%s\x00%d\x00%s\x00%s\x00%g\x00", s.Mode, sha(s.Attachment), s.Fit, s.Scale, s.Position, s.Background, s.MarginIn)
	}
	if l.Shape == ShapeWrap && l.Wrap != nil {
		fmt.Fprintf(h, "wrap\x00%s\x00%g\x00", sha(l.Wrap.Attachment), l.Wrap.Bleed())
	} else {
		side(l.Front)
		side(l.Back)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// FrontHash is the hash of what shows the front of the cover and nothing else: the shape, the trim, the alt text, the front
// side (or the wrap, and with it which side of it the front is, by the book's direction) and the pictures' own hashes.
// A change to the back alone does not change it. It is what an export that shows only the front (the EPUB) puts in its
// input hash. look finds the pictures; rtl is the book's direction.
func (l *Layout) FrontHash(look func(id string) (Picture, bool), rtl bool) string {
	sha := func(id string) string {
		if p, ok := look(id); ok {
			return p.SHA256
		}
		return "missing:" + id
	}
	h := sha256.New()
	fmt.Fprintf(h, "cover-front/%d\x00%s\x00%s\x00%s\x00", Version, l.Shape, l.Trim, l.Alt)
	if l.Shape == ShapeWrap && l.Wrap != nil {
		fmt.Fprintf(h, "wrap\x00%s\x00%g\x00%v\x00", sha(l.Wrap.Attachment), l.Wrap.Bleed(), rtl)
	} else {
		s := l.Front
		fmt.Fprintf(h, "side\x00%s\x00%s\x00%s\x00%d\x00%s\x00%s\x00%g\x00", s.Mode, sha(s.Attachment), s.Fit, s.Scale, s.Position, s.Background, s.MarginIn)
	}
	return hex.EncodeToString(h.Sum(nil))
}

// FrontHashOf loads a project's layout and gives its FrontHash for a book in the language ("" is the project's own);
// the hash is "" when the project has no layout or the layout has no front picture.
func FrontHashOf(ctx context.Context, q Queryer, projectID, language string) (hash string, lay *Layout, err error) {
	lay, err = Load(ctx, q, projectID)
	if err != nil || !lay.HasFront() {
		return "", lay, err
	}
	pr, err := loadProject(ctx, q, projectID)
	if err != nil {
		return "", lay, err
	}
	if language != "" {
		pr.Language = language
	}
	return lay.FrontHash(newPictureSet(ctx, q, projectID).look, pr.rtl()), lay, nil
}

// sideJSON and parse are the stored forms. A side that is not used is stored as the column's default.
func sideJSON(s Side) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func parseSide(raw string) Side {
	var s Side
	_ = json.Unmarshal([]byte(raw), &s)
	return s
}

func wrapJSON(w *Wrap) string {
	if w == nil {
		return "{}"
	}
	b, _ := json.Marshal(w)
	return string(b)
}

func parseWrap(raw string) *Wrap {
	var w Wrap
	if json.Unmarshal([]byte(raw), &w) != nil || w.Attachment == "" {
		return nil
	}
	return &w
}

// Warning is something a writer should know about a layout, in a sentence, with a stable code.
type Warning struct {
	Code    string `json:"code" desc:"On the layout: cover_low_resolution, cover_picture_small, cover_profile_not_srgb, cover_profile_dropped, cover_cropped, cover_wrap_low_resolution, cover_wrap_stale, cover_trim_mismatch. From the print check and export.cover: image_low_resolution (at the printed size, under 300 dpi), cover_margin_crossed, cover_bleed_missing, cover_wrap_stale, cover_wrap_bleed, cover_barcode_area, spine_narrow, spine_few_pages, spine_rtl, cover_rtl_text."`
	Message string `json:"message"`
	// Part says which part of the cover it is about: front, back, spine or wrap.
	Part string `json:"part,omitempty" enum:"front,back,spine,wrap"`
}
