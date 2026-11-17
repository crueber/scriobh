package font

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"
)

// The files are the originals: the licences ask for that, and SOURCES.txt says
// which they are.
func TestEmbeddedFontsAreTheOriginals(t *testing.T) {
	want := map[string]string{
		"LiberationSerif-Regular.ttf":    "058ea80864aef09a23f45cbec2bb5400bc3dfbdea01c3f10538a21fcb497fb74",
		"LiberationSerif-Italic.ttf":     "0e3dea9f8d613e006ccfa62201f33e265d19167bd0907725c3e145368b04fc2e",
		"LiberationSerif-Bold.ttf":       "d754ba427cfe0bca54ae052384baa8f842da5bd6550ad4da024ac441e7a7d5ce",
		"LiberationSerif-BoldItalic.ttf": "f17db8af71e24d2066b587546021d4f0b296be389512b658dec3c09affeb11a7",
		"LiberationMono-Regular.ttf":     "f2b83c763e8afd21709333370bed4774337fae82267937e2b5aea7e2fbd922c1",
		"DejaVuSerif.ttf":                "42d1edeb7952f31b1f96d767ed7030b08a39e0c372b0071641518864e2bffb51",
		"DejaVuSans.ttf":                 "7da195a74c55bef988d0d48f9508bd5d849425c1770dba5d7bfc6ce9ed848954",
	}
	src, err := Raw("SOURCES.txt")
	if err != nil {
		t.Fatal(err)
	}
	for file, sum := range want {
		raw, err := Raw(file)
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(raw)
		if got := hex.EncodeToString(h[:]); got != sum {
			t.Errorf("%s has hash %s, want %s", file, got, sum)
		}
		if !strings.Contains(string(src), sum) {
			t.Errorf("SOURCES.txt does not record %s", file)
		}
	}
	lic := Licenses()
	if !strings.Contains(lic["OFL-liberation.txt"], "SIL OPEN FONT LICENSE") || !strings.Contains(lic["LICENSE-dejavu.txt"], "Bitstream Vera") {
		t.Error("a licence text is missing from the binary")
	}
}

func load(t *testing.T, fam *Family, s Style) *Font {
	t.Helper()
	face, _ := fam.Face(s)
	f, err := face.Font()
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestParseMetrics(t *testing.T) {
	f := load(t, Serif(), Regular)
	if f.UnitsPerEm != 2048 {
		t.Errorf("units per em %d", f.UnitsPerEm)
	}
	g, ok := f.Glyph('A')
	if !ok || g == 0 {
		t.Fatal("no glyph for A")
	}
	if a := f.Advance(g); a != 1479 { // Times New Roman's A is 0.722 em
		t.Errorf("advance of A is %d", a)
	}
	sp, _ := f.Glyph(' ')
	if a := f.Advance(sp); a != 512 { // a quarter em
		t.Errorf("advance of space is %d", a)
	}
	if f.CapHeight < 1200 || f.CapHeight > 1500 || f.XHeight < 800 || f.XHeight > 1100 {
		t.Errorf("cap height %d, x height %d", f.CapHeight, f.XHeight)
	}
	if !f.HasKern() {
		t.Error("the kern table was not read")
	}
	av, _ := f.Glyph('A')
	vv, _ := f.Glyph('V')
	if f.Kern(av, vv) >= 0 {
		t.Errorf("AV is not kerned tighter: %d", f.Kern(av, vv))
	}
	if !load(t, Serif(), Bold).Bold || !load(t, Serif(), Italic).Italic {
		t.Error("style flags")
	}
	if !load(t, Mono(), Regular).Mono {
		t.Error("the mono face is not fixed pitch")
	}
	// Every face parses.
	for _, fam := range append(append([]*Family{}, Body()...), Fallbacks()...) {
		for _, fc := range fam.Faces {
			if fc != nil {
				if _, err := fc.Font(); err != nil {
					t.Error(err)
				}
			}
		}
	}
	// Greek, Cyrillic, dotted Irish letters and curly quotes are in the book face.
	for _, r := range "αβγЖяḃṁ“”’—…ŋéé" {
		if _, ok := f.Glyph(r); !ok {
			t.Errorf("Liberation Serif has no %q", r)
		}
	}
	if _, ok := f.Glyph('❦'); ok {
		t.Error("the face unexpectedly has a fleuron: the fallback chain is untested")
	}
	if _, ok := load(t, Fallbacks()[1], Regular).Glyph('❦'); !ok {
		t.Error("no fallback carries the fleuron")
	}
}

func TestScaleRoundsInIntegers(t *testing.T) {
	f := load(t, Serif(), Regular)
	if got := f.Scale(1024, 12000); got != 6000 {
		t.Errorf("half an em at 12 pt is %d", got)
	}
	if got := f.Scale(1, 12000); got != 6 { // 5.86 rounds to 6
		t.Errorf("got %d", got)
	}
	if got := f.Scale(-1, 12000); got != -6 {
		t.Errorf("got %d", got)
	}
}

func TestSubsetRenumbersAndKeepsOutlines(t *testing.T) {
	f := load(t, Serif(), Regular)
	var gids []uint16
	for _, r := range "Hello, Wörld é" { // é and ö are composites in many faces
		g, _ := f.Glyph(r)
		gids = append(gids, g)
	}
	s, err := f.Subset(gids)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Data) > 40_000 {
		t.Errorf("the subset is %d bytes", len(s.Data))
	}
	if s.Orig[0] != 0 || s.NewID[0] != 0 {
		t.Error("glyph 0 is not the .notdef")
	}
	// The subset parses as a font of its own (cmap aside): re-read its tables.
	if u32(s.Data, 0) != 0x00010000 {
		t.Fatal("not a TrueType file")
	}
	n := u16(s.Data, 4)
	tags := map[string][2]int{}
	var sum uint32
	for i := 0; i < n; i++ {
		rec := 12 + 16*i
		tags[string(s.Data[rec:rec+4])] = [2]int{u32(s.Data, rec+8), u32(s.Data, rec+12)}
	}
	for _, need := range []string{"head", "hhea", "maxp", "hmtx", "loca", "glyf", "name"} {
		if _, ok := tags[need]; !ok {
			t.Errorf("no %s table", need)
		}
	}
	if checksum(s.Data) != 0xB1B0AFBA {
		t.Errorf("the file checksum is %08x (sum %08x)", checksum(s.Data), sum)
	}
	num := u16(s.Data[tags["maxp"][0]:], 4)
	if num != len(s.Orig) {
		t.Errorf("maxp says %d glyphs, there are %d", num, len(s.Orig))
	}
	// loca is monotonic and ends inside glyf.
	loca, glyf := tags["loca"], tags["glyf"]
	prev := 0
	for i := 0; i <= num; i++ {
		v := u32(s.Data, loca[0]+4*i)
		if v < prev || v > glyf[1] {
			t.Fatalf("loca[%d]=%d", i, v)
		}
		prev = v
	}
	// A kept glyph has its outline (H has contours) and the advances are carried.
	hg, _ := f.Glyph('H')
	nh := s.NewID[hg]
	if u32(s.Data, loca[0]+4*int(nh+1))-u32(s.Data, loca[0]+4*int(nh)) < 10 {
		t.Error("H lost its outline")
	}
	if s.Advance[nh] != f.Advance(hg) {
		t.Error("advance")
	}
	// Deterministic.
	s2, _ := f.Subset(gids)
	if string(s2.Data) != string(s.Data) {
		t.Error("two subsets of the same glyphs differ")
	}
	// Components of a composite are kept: an accented capital is built from two glyphs in this face.
	var comp uint16
	for r := rune(0xC0); r < 0x180; r++ {
		g, ok := f.Glyph(r)
		if !ok {
			continue
		}
		if kids, isComp, _ := f.components(g); isComp && len(kids) >= 2 {
			comp = g
			break
		}
	}
	if comp == 0 {
		t.Skip("no composite glyph found")
	}
	sc, err := f.Subset([]uint16{comp})
	if err != nil {
		t.Fatal(err)
	}
	if len(sc.Orig) < 3 {
		t.Errorf("a composite was kept without its components: %v", sc.Orig)
	}
}

func TestParseRefusesDamage(t *testing.T) {
	raw, _ := Raw("LiberationSerif-Regular.ttf")
	for name, bad := range map[string][]byte{
		"empty":     nil,
		"magic":     append([]byte("OTTO"), raw[4:]...),
		"truncated": raw[:2000],
		"short":     raw[:40],
	} {
		if _, err := Parse(bad); err == nil {
			t.Errorf("%s: parsed", name)
		}
	}
}
