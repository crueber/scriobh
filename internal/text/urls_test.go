package text

import "testing"

// Bare addresses, autolinks and image alt text are not prose mentions: the
// index (the search text) and the spans (the Markdown) agree, and count only
// the two real uses.
func TestWebAddressesAndImageAltAreNotMentions(t *testing.T) {
	m := NewMatcher(ph("Elara"))
	md := "![Elara in the garden](http://x/Elara.png) and Elara and <http://Elara.com/Elara> and http://Elara.com/Elara and [Elara](http://z/)\n"
	n := 0
	m.Find(SearchBody(md), func(Match) { n++ })
	sp, complete := Spans(md, m)
	if n != 2 || len(sp) != 2 || !complete {
		t.Errorf("index %d, spans %d (complete %v), want 2 and 2", n, len(sp), complete)
	}
}
