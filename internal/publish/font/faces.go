package font

import (
	"embed"
	"fmt"
	"sync"
)

//go:embed data/*.ttf data/*.txt
var files embed.FS

// Style is one of the four faces of a family.
type Style uint8

// The styles, in the order a Family holds them.
const (
	Regular Style = iota
	Italic
	Bold
	BoldItalic
)

// Face is one embedded font file, parsed on first use.
type Face struct {
	// Name is the base of the PDF font name (no spaces): LiberationSerif-Italic.
	Name string
	File string

	once sync.Once
	font *Font
	err  error
}

// Font returns the parsed face. A failure here means a damaged binary (the
// files are embedded and their hashes are tested), so it is reported once and
// the face is unusable.
func (f *Face) Font() (*Font, error) {
	f.once.Do(func() {
		raw, err := files.ReadFile("data/" + f.File)
		if err != nil {
			f.err = err
			return
		}
		f.font, f.err = Parse(raw)
		if f.err != nil {
			f.err = fmt.Errorf("%s: %w", f.File, f.err)
		}
	})
	return f.font, f.err
}

// Family is a typeface in up to four styles. A missing style falls back to the
// nearest the family has (a mono family has only a regular).
type Family struct {
	// Name is what the font option says.
	Name  string
	Faces [4]*Face
	Mono  bool
}

// Face returns the face for a style, falling back from bold italic to italic
// or bold to regular when the family lacks it, and the third result says
// whether the face is the style asked for.
func (fam *Family) Face(s Style) (*Face, bool) {
	if f := fam.Faces[s]; f != nil {
		return f, true
	}
	switch s {
	case BoldItalic:
		if f := fam.Faces[Bold]; f != nil {
			return f, false
		}
		if f := fam.Faces[Italic]; f != nil {
			return f, false
		}
	}
	return fam.Faces[Regular], false
}

func face(name, file string) *Face { return &Face{Name: name, File: file} }

var (
	liberationSerif = &Family{Name: "Liberation Serif", Faces: [4]*Face{
		face("LiberationSerif", "LiberationSerif-Regular.ttf"),
		face("LiberationSerif-Italic", "LiberationSerif-Italic.ttf"),
		face("LiberationSerif-Bold", "LiberationSerif-Bold.ttf"),
		face("LiberationSerif-BoldItalic", "LiberationSerif-BoldItalic.ttf"),
	}}
	liberationMono = &Family{Name: "Liberation Mono", Mono: true, Faces: [4]*Face{
		face("LiberationMono", "LiberationMono-Regular.ttf"), nil, nil, nil,
	}}
	// The fallbacks are for characters the book face does not have; they are
	// not offered as body faces.
	dejaVuSerif = &Family{Name: "DejaVu Serif", Faces: [4]*Face{face("DejaVuSerif", "DejaVuSerif.ttf"), nil, nil, nil}}
	dejaVuSans  = &Family{Name: "DejaVu Sans", Faces: [4]*Face{face("DejaVuSans", "DejaVuSans.ttf"), nil, nil, nil}}
)

// Body lists the families a book can be set in.
func Body() []*Family { return []*Family{liberationSerif, liberationMono} }

// ByName finds a body family by the name the font option uses.
func ByName(name string) (*Family, bool) {
	for _, f := range Body() {
		if f.Name == name {
			return f, true
		}
	}
	return nil, false
}

// Serif, Mono and the fallbacks for glyphs a face lacks.
func Serif() *Family { return liberationSerif }
func Mono() *Family  { return liberationMono }

// Fallbacks are tried in order for a character the chosen face does not have.
func Fallbacks() []*Family { return []*Family{dejaVuSerif, dejaVuSans} }

// Licenses returns the licence texts of the embedded fonts, by file name.
func Licenses() map[string]string {
	out := map[string]string{}
	for _, n := range []string{"OFL-liberation.txt", "LICENSE-dejavu.txt", "SOURCES.txt"} {
		if b, err := files.ReadFile("data/" + n); err == nil {
			out[n] = string(b)
		}
	}
	return out
}

// Raw returns an embedded file's bytes (for the hash test).
func Raw(file string) ([]byte, error) { return files.ReadFile("data/" + file) }
