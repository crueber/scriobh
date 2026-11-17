package studio

import (
	"context"
	"sort"
)

// ViewerFor reads a person's numbers over exactly the projects the caller names. It is for work that has no session (the
// progress email), where the caller has already decided which projects may be spoken of, and a project that is not in
// that list is not there.
func ViewerFor(ctx context.Context, q querier, userID string, projectIDs []string) (*Viewer, error) {
	v := &Viewer{Ctx: ctx, Q: q, UserID: userID}
	if err := v.zone(); err != nil {
		return nil, err
	}
	ids, err := memberIDs(ctx, q, userID)
	if err != nil {
		return nil, err
	}
	allow := make(map[string]bool, len(projectIDs))
	for _, id := range projectIDs {
		allow[id] = true
	}
	for _, id := range ids {
		if allow[id] {
			v.IDs = append(v.IDs, id)
		}
	}
	sort.Strings(v.IDs)
	if err := v.settle(); err != nil {
		return nil, err
	}
	return v, nil
}

// Report is the figures of the progress email for one period: numbers, never a name or a word of any text.
type Report struct {
	From          string
	To            string
	Days          int
	Words         int // words added in the period
	PreviousWords int // words added in the period of the same length before
	Minutes       int
	Sessions      int
	WritingDays   int
	Streak        int // as of today
	Longest       int
	HasDailyGoal  bool
	GoalDaysMet   int // days of the period on which a daily goal was met
	GoalsActive   int
	GoalsDone     int
}

// Empty says there was nothing to report: no words, no time, no session in the period.
func (r Report) Empty() bool { return r.Words == 0 && r.Minutes == 0 && r.Sessions == 0 }

// Report works out the figures for from..to (local dates) as of today.
func (v *Viewer) Report(from, to string) (Report, error) {
	s, err := v.snapshot()
	if err != nil {
		return Report{}, err
	}
	f := figuresOf(s.all.Days, from, to)
	n := daysBetween(from, to) + 1
	r := Report{From: from, To: to, Days: n, Words: f.Words, Minutes: f.Minutes, Sessions: f.Sessions, WritingDays: f.WritingDays,
		PreviousWords: figuresOf(s.all.Days, addDays(from, -n), addDays(from, -1)).Words,
		Streak:        s.streak, Longest: s.longest, HasDailyGoal: s.hasDaily}
	for d := from; d <= to; d = addDays(d, 1) {
		if s.hits[d] {
			r.GoalDaysMet++
		}
	}
	gs, err := s.goalsOut(false)
	if err != nil {
		return Report{}, err
	}
	r.GoalsActive, r.GoalsDone = gs.Active, gs.Completed
	return r, nil
}
