// Package storydate reads and orders the dates of a story world: the story
// date of a scene and of a timeline event. They are ISO-like and may be
// partial, because a writer often knows the year and not the day.
//
// Grammar (nothing else is a date, and anything else sorts as undated):
//
//	[-]YYYY                year (one to four digits: a year under a thousand may
//	                       be written without its zeros, so 687 is 0687)
//	[-]YYYY-MM             month
//	[-]YYYY-MM-DD          day
//	[-]YYYY-MM-DDTHH:MM    minute (a space may stand for the T)
//
// The month and the day always take two digits, so "1998-4" is never guessed
// at. The year is the astronomical year: a leading minus is before year 0,
// year 0 is written 0000 and "-0000" is refused. There is no calendar of eras
// here: a world with its own calendar numbers its years in this range and shows
// its own words in the event's label. The calendar is proleptic Gregorian, so month lengths and
// leap years are the ordinary ones.
//
// Ordering. A date has a sort key of fixed width, so two keys compare as
// strings. An unknown part counts as before every known one, so
//
//	1998 < 1998-01 < 1998-01-01 < 1998-01-01T00:00 < 1998-01-01T00:01 < 1998-02
//
// and a coarser date sorts at or before every finer date it contains. That is
// the order lists use; ties are broken by the caller (the end, then the title,
// then the id), and undated items sort after every dated one.
//
// Intervals. An item occupies the whole period its precision names: "1998" is
// the year, "1998-04" the month, "1998-04-12" the day, and a time is the
// minute (an instant). With an end date it runs from the start's first moment
// to the end's last. Two intervals overlap when they share a moment. An
// interval is definite when it has an explicit end or names a day or finer:
// a bare "1998" says "sometime that year", not "all year", and contradiction
// checks do not treat it as a duration.
package storydate

import (
	"fmt"
	"strconv"
	"strings"
)

// Precision says how much of a date is known.
type Precision int

// Precisions, coarse to fine.
const (
	Year Precision = iota + 1
	Month
	Day
	Minute
)

// Date is a parsed story date.
type Date struct {
	Year, Month, Day int // Month and Day are 0 when not known
	Min              int // minute of the day, 0..1439, when Prec is Minute
	Prec             Precision
}

// Parse reads a story date. ok is false for anything outside the grammar.
func Parse(s string) (Date, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return Date{}, false
	}
	var d Date
	neg := false
	if s[0] == '-' {
		neg, s = true, s[1:]
	}
	date, clock := s, ""
	if i := strings.IndexAny(s, "T "); i >= 0 {
		date, clock = s[:i], strings.TrimSpace(s[i+1:])
		if clock == "" {
			return Date{}, false // "1998-04-12T" names no time
		}
	}
	parts := strings.Split(date, "-")
	if len(parts) > 3 {
		return Date{}, false
	}
	num := func(p string, min, max, width int) (int, bool) {
		if p == "" || len(p) > width || strings.TrimLeft(p, "0123456789") != "" {
			return 0, false
		}
		n, err := strconv.Atoi(p)
		return n, err == nil && n >= min && n <= max
	}
	y, ok := num(parts[0], 0, 9999, 4)
	if !ok || (neg && y == 0) {
		return Date{}, false
	}
	d.Year, d.Prec = y, Year
	if neg {
		d.Year = -y
	}
	if len(parts) > 1 {
		if len(parts[1]) != 2 {
			return Date{}, false
		}
		m, ok := num(parts[1], 1, 12, 2)
		if !ok {
			return Date{}, false
		}
		d.Month, d.Prec = m, Month
	}
	if len(parts) > 2 {
		if len(parts[2]) != 2 {
			return Date{}, false
		}
		dd, ok := num(parts[2], 1, 31, 2)
		if !ok || dd > DaysIn(d.Year, d.Month) {
			return Date{}, false
		}
		d.Day, d.Prec = dd, Day
	}
	if clock != "" {
		if d.Prec != Day || len(clock) != 5 || clock[2] != ':' {
			return Date{}, false
		}
		h, ok1 := num(clock[:2], 0, 23, 2)
		m, ok2 := num(clock[3:], 0, 59, 2)
		if !ok1 || !ok2 {
			return Date{}, false
		}
		d.Min, d.Prec = h*60+m, Minute
	}
	return d, true
}

// DaysIn is the number of days in a month of a year (proleptic Gregorian).
func DaysIn(year, month int) int {
	switch month {
	case 4, 6, 9, 11:
		return 30
	case 2:
		if year%4 == 0 && (year%100 != 0 || year%400 == 0) {
			return 29
		}
		return 28
	}
	return 31
}

// String is the canonical form: what Parse reads back to the same Date.
func (d Date) String() string {
	y := fmt.Sprintf("%04d", abs(d.Year))
	if d.Year < 0 {
		y = "-" + y
	}
	switch d.Prec {
	case Month:
		return fmt.Sprintf("%s-%02d", y, d.Month)
	case Day:
		return fmt.Sprintf("%s-%02d-%02d", y, d.Month, d.Day)
	case Minute:
		return fmt.Sprintf("%s-%02d-%02dT%02d:%02d", y, d.Month, d.Day, d.Min/60, d.Min%60)
	}
	return y
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// Canonical parses s and returns its canonical form.
func Canonical(s string) (string, bool) {
	d, ok := Parse(s)
	if !ok {
		return "", false
	}
	return d.String(), true
}

// key builds a fixed-width key. minute is 0 for "before the day began",
// 1 to 1440 for the minutes of the day, and 1441 for "after the last minute".
func key(year, month, day, minute int) string {
	return fmt.Sprintf("%05d%02d%02d%04d", year+10000, month, day, minute)
}

// StartKey is the key of the first moment the date can mean.
func (d Date) StartKey() string {
	m := 0
	if d.Prec == Minute {
		m = d.Min + 1
	}
	return key(d.Year, d.Month, d.Day, m)
}

// EndKey is the key of the last moment the date can mean.
func (d Date) EndKey() string {
	switch d.Prec {
	case Year:
		return key(d.Year, 12, 31, 1441)
	case Month:
		return key(d.Year, d.Month, DaysIn(d.Year, d.Month), 1441)
	case Day:
		return key(d.Year, d.Month, d.Day, 1441)
	}
	return key(d.Year, d.Month, d.Day, d.Min+1)
}

// Interval is the span of time an item occupies.
type Interval struct {
	Start, End string // keys; both empty when undated
	Definite   bool   // an explicit end, or a day or finer
	Valid      bool   // false when the end is before the start
}

// Dated reports whether the interval has a start.
func (i Interval) Dated() bool { return i.Start != "" }

// Span builds the interval of a story date and an optional end date. A date
// that does not parse is undated (and so is an end without a start).
func Span(start, end string) Interval {
	s, ok := Parse(start)
	if !ok {
		return Interval{Valid: true}
	}
	iv := Interval{Start: s.StartKey(), End: s.EndKey(), Definite: s.Prec >= Day, Valid: true}
	if e, ok := Parse(end); ok {
		iv.End, iv.Definite = e.EndKey(), true
		if iv.End < iv.Start {
			iv.Valid = false
		}
	}
	return iv
}

// Overlaps reports whether two dated intervals share a moment.
func (a Interval) Overlaps(b Interval) bool {
	return a.Dated() && b.Dated() && a.Start <= b.End && b.Start <= a.End
}

// Before reports whether a ends strictly before b starts.
func (a Interval) Before(b Interval) bool {
	return a.Dated() && b.Dated() && a.End < b.Start
}

// SortKey orders dated items first by start; undated items get a key that
// sorts after every date.
func SortKey(start string) string {
	if d, ok := Parse(start); ok {
		return d.StartKey()
	}
	return "~"
}
