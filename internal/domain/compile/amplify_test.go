package compile_test

import (
	"fmt"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/publish"
)

// One note referenced over and over is copied for each reference, and the copies
// of the whole book are bounded: five chapters of 16 KB do not become 50 MB.
func TestRepeatedNoteReferencesCannotAmplifyABook(t *testing.T) {
	f := newFx(t, "Amp")
	def := strings.Repeat("x ", 5000)
	content := "Para " + strings.Repeat("[^a]", 1600) + "\n\n[^a]: " + def + "\n"
	for i := 0; i < 8; i++ {
		f.doc(fmt.Sprintf("C%d", i), "chapter", "", content)
	}
	in := 8 * len(content)
	out := f.compile("plain-markdown", publish.Options{})
	t.Logf("%d bytes in, %d bytes out (%.0fx)", in, len(out.Body), float64(len(out.Body))/float64(in))
	if len(out.Body) > 24<<20 {
		t.Errorf("%d bytes of input became %d", in, len(out.Body))
	}
	// the DOCX is bounded the same way
	d := f.compile("shunn-us", publish.Options{})
	for n, b := range unzip(t, d.Body) {
		if len(b) > 40<<20 {
			t.Errorf("%s is %d bytes", n, len(b))
		}
	}
}
