package publish

import "testing"

// A chapter whose title only says its number is headed with the number once, not "Chapter 1: Chapter 1".
func TestAChapterTitledByItsNumberIsNotNamedTwice(t *testing.T) {
	o := Options{ChapterHeading: "number-title"}
	for _, c := range []struct {
		title string
		n     int
		want  string
	}{{"Chapter 1", 1, "Chapter 1"}, {"chapter 2", 2, "Chapter 2"}, {"The Quay", 3, "Chapter 3: The Quay"}, {"", 4, "Chapter 4"}, {"Chapter 1", 2, "Chapter 2: Chapter 1"}} {
		if got := HeadingText(Section{Kind: SecChapter, Title: c.title, Number: c.n}, o); got != c.want {
			t.Errorf("%q numbered %d: %q, want %q", c.title, c.n, got, c.want)
		}
	}
}
