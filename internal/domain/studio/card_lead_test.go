package studio_test

import (
	"regexp"
	"strconv"
	"testing"

	"github.com/crueber/scriobh/internal/domain/studio"
	"github.com/crueber/scriobh/internal/i18n"
)

// The card's lead line (a lapsed run says the past month) is sized to the card: a longer sentence is drawn smaller and the
// larger count never reaches the right margin.
func TestTheLeadLineOfALapsedCardFitsTheCard(t *testing.T) {
	re := regexp.MustCompile(`font-size="(\d+)" font-weight="700" fill="#2F5D50">([^<]*)</text>`)
	for _, lang := range []string{"en", "xx-rtl"} {
		tr := func(key string, args ...any) string { return i18n.Default.T(lang, key, args...) }
		for _, n := range []int{1, 6, 17, 30} {
			svg := string(studio.CardSVG(studio.CardView{Day: "2026-11-16", Streak: 0, Longest: 4, WritingDays30: n, HasGoal: true, GoalsActive: 1}, tr))
			m := re.FindStringSubmatch(svg)
			if m == nil {
				t.Fatalf("%s %d: no lead line", lang, n)
			}
			size, _ := strconv.Atoi(m[1])
			if width := float64(size) * 0.6 * float64(len([]rune(m[2]))); width > 528 {
				t.Errorf("%s %d: %q at %d is about %.0f wide, the room is 528", lang, n, m[2], size, width)
			}
		}
	}
}
