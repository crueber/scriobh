package imports

import (
	"bytes"
	"math/rand"
	"os"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/alts"
)

// Fixtures with bytes flipped, cut and repeated are refused or read, never a panic
// (a converter recovers from one, so a recovered panic shows as its own message).
func TestConvertersSurviveMutation(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	mutate := func(b []byte) []byte {
		out := append([]byte(nil), b...)
		if len(out) == 0 {
			return out
		}
		switch rng.Intn(5) {
		case 0: // flip some bytes
			for i := 0; i < 1+rng.Intn(8); i++ {
				out[rng.Intn(len(out))] = byte(rng.Intn(256))
			}
		case 1: // cut
			out = out[:rng.Intn(len(out))]
		case 2: // delete a span
			i := rng.Intn(len(out))
			j := min(len(out), i+rng.Intn(200))
			out = append(out[:i], out[j:]...)
		case 3: // repeat a span
			i := rng.Intn(len(out))
			j := min(len(out), i+1+rng.Intn(100))
			rep := bytes.Repeat(out[i:j], 1+rng.Intn(50))
			out = append(append(append([]byte(nil), out[:j]...), rep...), out[j:]...)
		case 4: // brace and tag noise
			for i := 0; i < 1+rng.Intn(6); i++ {
				out[rng.Intn(len(out))] = "{}\\<>&\"[]"[rng.Intn(9)]
			}
		}
		return out
	}
	check := func(what string, s source) {
		_, err := preview(s, nil, nil, alts.DefaultLimits)
		if err != nil && strings.Contains(err.Error(), "could not be read") && !strings.Contains(err.Error(), "binder") {
			t.Errorf("%s: a converter panicked: %v", what, err)
		}
	}
	scriv := readTree(t, "testdata/Salt Road.scriv")
	wp, _ := os.ReadFile("testdata/wordpress-1.2.xml")
	g4, _ := os.ReadFile("testdata/ghost-4.json")
	g5, _ := os.ReadFile("testdata/ghost-5.json")
	for i := 0; i < 400; i++ {
		fs := make([]File, len(scriv))
		copy(fs, scriv)
		for k := 0; k < 1+rng.Intn(3); k++ {
			j := rng.Intn(len(fs))
			fs[j].Data = mutate(fs[j].Data)
		}
		check("scrivener", source{name: "x", files: fs, layout: LayoutScrivener})
		check("wordpress", source{name: "x", files: []File{{Path: "e.xml", Data: mutate(wp)}}, layout: LayoutWordPress})
		g := g4
		if i%2 == 1 {
			g = g5
		}
		check("ghost", source{name: "x", files: []File{{Path: "e.json", Data: mutate(g)}}, layout: LayoutGhost})
	}
}
