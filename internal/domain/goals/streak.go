package goals

import (
	"sort"
	"time"
)

// Streaks reports the current and the longest run of consecutive hit days.
// hitDays are local dates (YYYY-MM-DD) on which the daily goal was met, in any
// order; today is the person's local date now.
//
// Today does not break a streak until it is over: if today is not hit yet the
// run is counted from yesterday, and if today is hit it is part of the run. A
// day with no row is a day with no hit; it simply ends the run, and nothing
// else is made of it.
//
// Dates are stepped as calendar dates (midnight UTC plus one day), never as
// 24-hour intervals, so a 23- or 25-hour day on a daylight-saving change cannot
// skip or repeat a date.
func Streaks(hitDays []string, today string) (current, longest int) {
	set := make(map[string]bool, len(hitDays))
	days := make([]time.Time, 0, len(hitDays))
	for _, d := range hitDays {
		t, err := time.Parse(dayLayout, d)
		if err != nil || set[d] {
			continue
		}
		set[d] = true
		days = append(days, t)
	}
	sort.Slice(days, func(i, j int) bool { return days[i].Before(days[j]) })
	run := 0
	for i, t := range days {
		if i > 0 && days[i-1].AddDate(0, 0, 1).Equal(t) {
			run++
		} else {
			run = 1
		}
		longest = max(longest, run)
	}
	t, err := time.Parse(dayLayout, today)
	if err != nil {
		return 0, longest
	}
	if !set[today] {
		t = t.AddDate(0, 0, -1)
	}
	for set[t.Format(dayLayout)] {
		current++
		t = t.AddDate(0, 0, -1)
	}
	return current, longest
}
