package compile

import "testing"

// A warning about one document says "document" and "was", about several "documents" and "were": no "(s)".
func TestCountWordChoosesTheSingularOnlyForOne(t *testing.T) {
	for n, want := range map[int]string{0: "documents", 1: "document", 2: "documents", 11: "documents"} {
		if got := countWord(n, "document", "documents"); got != want {
			t.Errorf("%d: %q, want %q", n, got, want)
		}
	}
}
