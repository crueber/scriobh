package studio_test

import (
	"encoding/xml"
	"io"
	"math/rand"
	"regexp"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/crueber/scriobh/internal/domain/studio"
)

func wellFormed(t testing.TB, svg string) {
	t.Helper()
	d := xml.NewDecoder(strings.NewReader(svg))
	d.Strict = true
	for {
		_, err := d.Token()
		if err == io.EOF {
			return
		}
		if err != nil {
			t.Fatalf("the card's SVG is not well-formed XML: %v\n%.300s", err, svg)
		}
	}
}

func cardOf(title string) studio.CardView {
	return studio.CardView{Day: "2026-03-03", Streak: 3, Longest: 5, HasGoal: true, WritingDays30: 9, GoalsActive: 2, GoalsDone: 1,
		Goals: []studio.CardGoal{{Period: "day", Measure: "words", Scope: "project", Project: title, Target: 5, Value: 9, Percent: 180, Met: true},
			{Period: "total", Measure: "words", Scope: "project", Project: title, Target: 80000, Value: 31000, Percent: 38}}}
}

// Whatever a title holds, the picture is well-formed XML under a strict parser and says what it should: the characters XML
// forbids, bytes that are not UTF-8 (a lone surrogate among them), bidirectional and zero-width marks and markup are all
// made plain, and a long title is cut to fit before the figures on its line.
func TestTheCardSVGIsAlwaysWellFormed(t *testing.T) {
	alphabet := []rune{'a', 'Z', ' ', '<', '>', '&', '"', '\'', ']', '-', '\x00', '\x01', '\x08', '\x0b', '\x0c', '\x1f', '\t', '\n', '\r', '\x7f', '\u0085',
		'\u202e', '\u2066', '\u200b', '\ufeff', '\ufffe', '\uffff', '\ufffd', 'é', '日', '😀', '\U0010ffff'}
	raw := []string{"\xff\xfe", "\xed\xa0\x80", "\xc0\xaf", "\xf4\x90\x80\x80", "<![CDATA[", "]]>", "<!--", "&#x0;", "&#1;"}
	r := rand.New(rand.NewSource(75))
	var titles []string
	for i := 0; i < 400; i++ {
		var b strings.Builder
		for n := r.Intn(120); n > 0; n-- {
			if r.Intn(8) == 0 {
				b.WriteString(raw[r.Intn(len(raw))])
			} else {
				b.WriteRune(alphabet[r.Intn(len(alphabet))])
			}
		}
		titles = append(titles, b.String())
	}
	titles = append(titles, "</text><script>alert(1)</script>", strings.Repeat("W", 5000), strings.Repeat("日本", 400), "")
	for _, title := range titles {
		svg := string(studio.CardSVG(cardOf(title), tr))
		wellFormed(t, svg)
		if strings.Contains(svg, "<script") || !utf8.ValidString(svg) {
			t.Fatalf("title %q: live markup or invalid UTF-8 in the picture", title)
		}
	}
	// The label of a goal ends before the figures at the right: at most 52 characters of words and title.
	svg := string(studio.CardSVG(cardOf(strings.Repeat("W", 300)), tr))
	tags := regexp.MustCompile(`<[^>]*>`)
	for _, line := range strings.Split(svg, "<text") {
		i, j := strings.Index(line, ">"), strings.Index(line, "</text>")
		if i < 0 || j < i {
			continue
		}
		text := tags.ReplaceAllString(line[i+1:j], "") // the characters drawn, not the tspan around the title
		if n := utf8.RuneCountInString(text); n > 80 {
			t.Errorf("a line of %d characters: %.100s", n, text)
		}
	}
	if !strings.Contains(svg, "…") {
		t.Error("a long title was not cut with an ellipsis")
	}
}

func FuzzCardSVG(f *testing.F) {
	for _, s := range []string{"", "plain", "</text><script>", "a\x00b", "\xff", "\u202e", strings.Repeat("x", 500)} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, title string) {
		wellFormed(t, string(studio.CardSVG(cardOf(title), tr)))
	})
}
