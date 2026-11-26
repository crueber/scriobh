package storydate

import (
	"sort"
	"testing"
)

func TestParseAndCanonical(t *testing.T) {
	for in, want := range map[string]string{
		"1998":             "1998",
		" 1998 ":           "1998",
		"1998-04":          "1998-04",
		"1998-04-12":       "1998-04-12",
		"1998-04-12T19:30": "1998-04-12T19:30",
		"1998-04-12 07:05": "1998-04-12T07:05",
		"-0044":            "-0044",
		"-0044-03-15":      "-0044-03-15",
		"687-09-14":        "0687-09-14", // a year under a thousand may be written without its zeros
		"98":               "0098",
		"-44":              "-0044",
		"7-01":             "0007-01",
		"0000":             "0000",
		"2024-02-29":       "2024-02-29",
		"1900-02-28":       "1900-02-28",
	} {
		got, ok := Canonical(in)
		if !ok || got != want {
			t.Errorf("Canonical(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "x", "1998-4", "1998-13", "1998-00", "1998-04-31", "1900-02-29", "2023-02-29", "1998-04-12T25:00", "-0000", "-0000-05", "1998-04-12T", "1998-04-12T9:30",
		"1998-04T10:00", "1998 04 12", "12345", "1998-04-12-01", "c. 1998", "April 1998", "1998/04/12", "--1998"} {
		if got, ok := Canonical(bad); ok {
			t.Errorf("%q should not be a date, read as %q", bad, got)
		}
	}
}

func TestPartialDatesOrderCoarseBeforeFine(t *testing.T) {
	want := []string{"-0044", "-0001-12-31", "0000", "1997-12-31T23:59", "1998", "1998-01", "1998-01-01", "1998-01-01T00:00", "1998-01-01T00:01", "1998-02", "1998-02-03", "1999"}
	shuffled := []string{"1999", "1998-01-01T00:01", "-0001-12-31", "1998", "1998-02-03", "-0044", "1998-01", "1998-02", "0000", "1998-01-01T00:00", "1997-12-31T23:59", "1998-01-01"}
	sort.Slice(shuffled, func(i, j int) bool { return SortKey(shuffled[i]) < SortKey(shuffled[j]) })
	for i := range want {
		if shuffled[i] != want[i] {
			t.Fatalf("order %v, want %v", shuffled, want)
		}
	}
	if SortKey("sometime in spring") <= SortKey("9999-12-31T23:59") {
		t.Errorf("undated items must sort after every date")
	}
}

func TestIntervals(t *testing.T) {
	iv := func(s, e string) Interval { return Span(s, e) }
	for _, tc := range []struct {
		name     string
		a, b     Interval
		overlaps bool
		before   bool
	}{
		{"a year contains its days", iv("1998", ""), iv("1998-04-12", ""), true, false},
		{"the same day", iv("1998-04-12", ""), iv("1998-04-12", ""), true, false},
		{"next day", iv("1998-04-12", ""), iv("1998-04-13", ""), false, true},
		{"an instant inside a day", iv("1998-04-12", ""), iv("1998-04-12T19:30", ""), true, false},
		{"two different minutes", iv("1998-04-12T19:30", ""), iv("1998-04-12T19:31", ""), false, true},
		{"the same minute", iv("1998-04-12T19:30", ""), iv("1998-04-12T19:30", ""), true, false},
		{"a range across a date", iv("1998-04-10", "1998-04-14"), iv("1998-04-12", ""), true, false},
		{"a range ends before", iv("1998-04-10", "1998-04-11"), iv("1998-04-12", ""), false, true},
		{"months", iv("1998-04", ""), iv("1998-05", ""), false, true},
		{"undated overlaps nothing", iv("", ""), iv("1998", ""), false, false},
	} {
		if got := tc.a.Overlaps(tc.b); got != tc.overlaps || tc.b.Overlaps(tc.a) != tc.overlaps {
			t.Errorf("%s: overlaps %v, want %v", tc.name, got, tc.overlaps)
		}
		if got := tc.a.Before(tc.b); got != tc.before {
			t.Errorf("%s: before %v, want %v", tc.name, got, tc.before)
		}
	}
	if iv("1998", "").Definite || iv("1998-04", "").Definite {
		t.Errorf("a bare year or month is not a duration")
	}
	if !iv("1998-04-12", "").Definite || !iv("1998", "1999").Definite || !iv("1998-04-12T10:00", "").Definite {
		t.Errorf("a day, a range and a time are definite")
	}
	if bad := iv("1998-04-12", "1998-04-01"); bad.Valid {
		t.Errorf("an end before the start is not valid")
	}
	if !iv("", "1999").Valid || iv("", "1999").Dated() {
		t.Errorf("an end without a start is undated, not invalid")
	}
}
