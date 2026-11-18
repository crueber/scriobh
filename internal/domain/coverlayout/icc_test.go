package coverlayout

import (
	"encoding/binary"
	"testing"
)

func header(class, space string, size int) []byte {
	p := make([]byte, 200)
	copy(p[12:], class)
	copy(p[16:], space)
	copy(p[36:], "acsp")
	binary.BigEndian.PutUint32(p, uint32(size))
	return p
}

// A profile is carried only if its header is a profile's: a class a PDF may name for an image's colour space, RGB data, the
// signature, and the size it says it has.
func TestValidProfileHeader(t *testing.T) {
	for _, c := range []struct {
		name  string
		p     []byte
		valid bool
	}{
		{"display", header("mntr", "RGB ", 200), true},
		{"input device", header("scnr", "RGB ", 200), true},
		{"colour space", header("spac", "RGB ", 200), true},
		{"output (a printer's)", header("prtr", "RGB ", 200), false},
		{"abstract", header("abst", "RGB ", 200), false},
		{"link", header("link", "RGB ", 200), false},
		{"CMYK data", header("mntr", "CMYK", 200), false},
		{"size says more", header("mntr", "RGB ", 201), false},
		{"size says less", header("mntr", "RGB ", 199), false},
		{"too short", header("mntr", "RGB ", 200)[:100], false},
	} {
		if got := validProfile(c.p); got != c.valid {
			t.Errorf("%s: valid = %v, want %v", c.name, got, c.valid)
		}
	}
}
