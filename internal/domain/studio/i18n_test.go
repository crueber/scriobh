package studio_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/i18n"
)

// Every message key the server can hand a page is in the catalogue: the keys written out in this package and in the card's
// page and handler, and the families it builds from a word (the period, the measure, the weekday, the month, the part of
// the day, the direction of the trend). A key with a count has .one and .other forms.
func TestEveryMessageKeyTheStudioCanReturnIsInTheCatalogue(t *testing.T) {
	has := func(k string) bool { return i18n.Default.Has(k) || i18n.Default.Has(k+".other") }
	// a key is written as Key: "...", t("..."), countT(t, "...") or {{$.T "..."}} (or $.N)
	re := regexp.MustCompile(`(?:Key:\s*|\bt\(|\bcountT\(t, |\$\.[TN] )"(studio\.[a-z_0-9.]+)"`)
	files, _ := filepath.Glob("*.go")
	files = append(files, "../../server/card.go", "../../../web/templates/progress-card.html")
	seen := 0
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		src := string(b)
		for _, m := range re.FindAllStringSubmatch(src, -1) {
			k := m[1]
			if strings.HasSuffix(k, ".") { // a family: checked below
				continue
			}
			seen++
			if !has(k) {
				t.Errorf("%s uses the key %s, which the catalogue does not have", f, k)
			}
		}
	}
	if seen < 30 {
		t.Errorf("found only %d keys: has the way they are written changed?", seen)
	}
	families := map[string][]string{
		"studio.trend.":         {"none", "new", "up", "steady", "down"},
		"studio.period.":        {"day", "week", "month", "total"},
		"studio.measure.":       {"words", "sessions", "chapters", "minutes"},
		"studio.part.":          {"night", "morning", "afternoon", "evening"},
		"studio.weekday.":       {"0", "1", "2", "3", "4", "5", "6"},
		"studio.weekday_short.": {"0", "1", "2", "3", "4", "5", "6"},
		"studio.month.":         {"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12"},
		"studio.month_short.":   {"1", "2", "3", "4", "5", "6", "7", "8", "9", "10", "11", "12"},
	}
	for prefix, words := range families {
		for _, w := range words {
			if !has(prefix + w) {
				t.Errorf("the catalogue lacks %s%s", prefix, w)
			}
		}
	}
	// No message frames a day that was not written as a shortfall: never "2 of 7", never "of the last 30".
	denominator := regexp.MustCompile(`(?i)\b(\d+|\{[a-z]+\}) of (\d+|\{[a-z]+\}|the last|them)\b|\bout of\b|\bof (7|30|the last \d+) days\b`)
	for _, k := range i18n.Default.Keys() {
		if strings.HasPrefix(k, "studio.") && denominator.MatchString(i18n.Default.Raw("en", k)) {
			t.Errorf("%s frames a count of days against the days there were: %s", k, i18n.Default.Raw("en", k))
		}
	}
	// Whatever English a message says, it has the parameters the server sends, and says no word of shame.
	for _, k := range i18n.Default.Keys() {
		if !strings.HasPrefix(k, "studio.") {
			continue
		}
		low := strings.ToLower(i18n.Default.Raw("en", k))
		for _, bad := range []string{"missed", "failed", "behind schedule", "warning", "lost your", "broke your", "broken"} {
			if strings.Contains(low, bad) {
				t.Errorf("%s says %q: %s", k, bad, low)
			}
		}
	}
}
