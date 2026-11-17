package server

import (
	"github.com/crueber/scriobh/internal/domain/studio"
	"github.com/crueber/scriobh/internal/mail"
)

// The Writing Studio's pages as fixtures: they join the template lint, the structure tests and the CSS budget per page.

func stFixtureDashboard(empty bool) studio.DashboardOut {
	pct := 53
	best, hour := 2, 10
	o := studio.DashboardOut{Day: "2026-03-13", Timezone: "America/New_York", NudgesEnabled: true}
	for _, k := range []struct {
		key  string
		days int
	}{{"today", 1}, {"7d", 7}, {"30d", 30}, {"365d", 365}} {
		c := studio.Card{Key: k.key, Days: k.days, From: "2026-03-07", To: "2026-03-13", Spark: []int{0, 0, 0, 0, 0, 0, 0}}
		if !empty {
			c.Figures = studio.Figures{Words: 505, Net: 340, Minutes: 6, Sessions: 7, WritingDays: 6}
			c.Previous = studio.Figures{Words: 329, Net: 119, WritingDays: 4}
			c.Delta = studio.Delta{Change: 176, Percent: &pct}
			c.Spark = []int{100, 100, 5, 0, 100, 100, 100}
		}
		o.Cards = append(o.Cards, c)
	}
	o.Calendar = studio.Calendar{From: "2025-09-29", To: "2026-03-15", LabelRows: []int{0, 2, 4}, Levels: 5}
	for w := 0; w < 24; w++ {
		wk := studio.Week{Start: "2026-01-05"}
		if w%4 == 0 {
			wk.Month = w/4 + 1
		}
		for d := 0; d < 7; d++ {
			c := studio.Cell{Day: "2026-03-13"}
			if !empty {
				c.Level, c.Words, c.Hit = (w+d)%5, (w+d)%5*100, (w+d)%5 == 4
			}
			c.Future = w == 23 && d > 4
			wk.Cells = append(wk.Cells, c)
		}
		o.Calendar.Weeks = append(o.Calendar.Weeks, wk)
	}
	o.Series = studio.SeriesOut{Range: "30d", From: "2026-02-12", To: "2026-03-13", Bucket: "day"}
	for i := 0; i < 30; i++ {
		p := studio.Point{Start: "2026-03-01"}
		if !empty {
			p.Words, p.Net, p.Minutes = i*20, i*15, i
		}
		o.Series.Points = append(o.Series.Points, p)
	}
	o.Series.Weekdays = make([]studio.WeekdayStat, 7)
	o.Series.Hours = make([]studio.HourStat, 24)
	o.Series.Parts = []studio.PartStat{{Part: "night"}, {Part: "morning"}, {Part: "afternoon"}, {Part: "evening"}}
	o.Trend = studio.Trend{Direction: "none", MessageKey: "studio.trend.none"}
	o.Streak = studio.StreakOut{Message: studio.Msg{Key: "studio.streak.set_goal"}}
	o.Goals.Items = []studio.GoalOut{}
	if empty {
		return o
	}
	o.Calendar.WritingDays, o.Calendar.Words, o.Calendar.AllTimeDays = 10, 834, 10
	o.Calendar.Thresholds = studio.Thresholds{Cuts: [3]int{99, 100, 100}, Days: 10}
	o.Series.Figures = studio.Figures{Words: 834, Net: 459, Minutes: 6, WritingDays: 10}
	o.Series.AvgPerDay, o.Series.AvgPerWritingDay = 64, 83
	o.Series.Weekdays[2] = studio.WeekdayStat{Weekday: 2, Words: 200, WritingDays: 2, AvgPerWritingDay: 100}
	o.Series.Parts[1].Words = 529
	o.Series.BestWeekday, o.Series.BestHour = &best, &hour
	o.Today = studio.TodayOut{Words: 60, Net: 60, HasGoal: true, GoalID: "sgl_1", Target: 100, Remaining: 40, Percent: 60, Message: &studio.Msg{Key: "studio.today.to_go", Params: map[string]any{"n": 40}}}
	o.Streak = studio.StreakOut{HasGoal: true, Current: 2, Longest: 3, TodayWords: 60, TodayTarget: 100, Measure: "words", TodayValue: 60,
		Message: studio.Msg{Key: "studio.streak.keep", Params: map[string]any{"days": 2, "n": 40}}, Note: &studio.Msg{Key: "studio.streak.note"}}
	margin, net := -3, 20
	o.Goals = studio.GoalsOut{Active: 2, Completed: 1, Items: []studio.GoalOut{
		{ID: "sgl_1", Source: "studio", Period: "day", Measure: "words", Target: 100, Value: 60, Remaining: 40, Percent: 60, Status: "active", Version: 2, Changes: 1, Net: &net,
			Message: studio.Msg{Key: "studio.goal.remaining", Params: map[string]any{"n": 40}}},
		{ID: "sgl_2", Source: "studio", ProjectID: "prj_a", ProjectTitle: "The Salt Road", ProjectSlug: "the-salt-road", Period: "total", Measure: "words", Target: 80000, Value: 31000, Remaining: 49000, Percent: 38, Status: "active", Deadline: "2026-12-01",
			Forecast: &studio.Forecast{Estimate: true, Remaining: 49000, Deadline: "2026-12-01", DaysLeft: 263, RequiredPerDay: 187, ProjectedDate: "2026-12-04", MarginDays: &margin,
				Basis: studio.Basis{WindowDays: 28, WritingDays: 10, PerDay: 64, Measure: "net"},
				Notes: []studio.Msg{{Key: "studio.forecast.required_words", Params: map[string]any{"n": 187, "measure": "words", "date": "2026-12-01"}}, {Key: "studio.forecast.projected", Params: map[string]any{"date": "2026-12-04"}},
					{Key: "studio.forecast.basis_words", Params: map[string]any{"days": 28, "writing": 10, "n": 64, "measure": "words"}}}}},
		{ID: "project:prj_a:daily", Source: "project", ProjectID: "prj_a", ProjectTitle: "The Salt Road", ProjectSlug: "the-salt-road", Period: "day", Measure: "words", Target: 50, Value: 60, Percent: 120, Met: true, Status: "active"},
		{ID: "sgl_3", Source: "studio", Period: "total", Measure: "chapters", Target: 3, Value: 3, Percent: 100, Met: true, Status: "completed", CompletedOn: "2026-03-01"},
	}}
	o.Projects = []studio.ProjectRow{{ProjectID: "prj_a", Title: "The Salt Road", Slug: "the-salt-road", Today: 60, Week: 505, PreviousWeek: 329, WeekNet: 340}}
	o.Trend = studio.Trend{RecentWords: 300, PreviousWords: 534, Direction: "down", MessageKey: "studio.trend.down",
		Resources: []studio.Action{{Key: "studio.resource.sprint", Action: "sprint"}, {Key: "studio.resource.goal", Action: "goal"}, {Key: "studio.resource.calendar", Action: "calendar"}}}
	o.Nudges = []studio.Nudge{{ID: "half:2026-03-13", Key: "studio.nudge.half", Params: map[string]any{"percent": 50, "n": 40}, Kind: "milestone"},
		{ID: "keep:2026-03-13", Key: "studio.nudge.keep_streak", Params: map[string]any{"days": 2, "n": 40}, Kind: "streak"}}
	o.Resume = &studio.ResumeOut{ProjectID: "prj_a", Slug: "the-salt-road", Title: "The Salt Road"}
	o.Hidden = 1
	return o
}

func stFixtureBase(tab string) stBase {
	return stBase{Tab: tab, CSRF: "tok_writer", W: stT{"en"}, Values: map[string]string{}}
}

func stFixtureProjects() []stProjectOpt {
	return []stProjectOpt{{ID: "prj_a", Title: "The Salt Road", CanWrite: true}, {ID: "prj_b", Title: "A reader's project"}}
}

func stFixtureSprint() *studio.SprintOut {
	return &studio.SprintOut{ID: "spr_1", ProjectID: "prj_a", PlannedMinutes: 25, State: "running", Day: "2026-03-13", StartedAt: "2026-03-13T10:00:00Z", EndsAt: "2026-03-13T10:25:00Z", ClosesAt: "2026-03-13T10:27:00Z", WordsAdded: 120, RemainingSecs: 700}
}

func studioPageFixtures() []fixture {
	t := stT{"en"}
	dash := func(empty bool) studioData {
		d := studioData{stBase: stFixtureBase("studio")}
		d.fill(stFixtureDashboard(empty), stFixtureProjects(), nil)
		return d
	}
	full, fresh := dash(false), dash(true)
	bad := dash(false)
	bad.Error, bad.Field, bad.Form, bad.Values = "Give both dates.", "from", "range", map[string]string{"from": "2026-03-01"}
	running := dash(false)
	running.Sprint = stSprintOf(t, stFixtureSprint(), stFixtureProjects())
	running.Flash = "Sprint stopped: 120 words"
	allCal := studioData{stBase: stFixtureBase("studio")}
	cal := stFixtureDashboard(false).Calendar
	allCal.fill(stFixtureDashboard(false), stFixtureProjects(), &cal)

	goals := studioGoalsData{stBase: stFixtureBase("goals"), Counts: "2 active, 1 completed", Projects: stFixtureProjects()}
	for _, g := range stFixtureDashboard(false).Goals.Items {
		v := stGoalOf(t, g)
		if g.Status == "completed" {
			goals.Done = append(goals.Done, v)
		} else {
			goals.Active = append(goals.Active, v)
		}
	}
	arch := stFixtureDashboard(false).Goals.Items[0]
	arch.ID, arch.Status, arch.EndedOn = "sgl_9", "archived", "2026-02-01"
	goals.Archived = []stGoal{stGoalOf(t, arch)}
	goals.Periods, goals.Measures = (&Server{}).stGoalWords(t)
	goals.Values = map[string]string{"period": "day", "measure": "words"}
	goalsBad := goals
	goalsBad.Error, goalsBad.Field, goalsBad.Form, goalsBad.Link = "You already have that goal.", "period", "new", "/studio/goals/sgl_1"
	goalsNone := studioGoalsData{stBase: stFixtureBase("goals"), Counts: "0 active, 0 completed", Periods: goals.Periods, Measures: goals.Measures}
	goalsNone.Values = goals.Values

	one := studioGoalData{stBase: stFixtureBase("goals"), Goal: stGoalOf(t, stFixtureDashboard(false).Goals.Items[1]), Total: true,
		Versions: []stVersionRow{{N: "2", Target: "80,000", Deadline: "1 Dec 2026", From: "1 Mar 2026"}, {N: "1", Target: "60,000", From: "1 Jan 2026"}},
		Periods:  []stPeriodRow{{Range: "1 Mar 2026 to 13 Mar 2026", Target: "80,000", Value: "31,000"}, {Range: "1 Feb 2026", Target: "60,000", Value: "61,000", Net: "400", Met: true}}}
	oneBad := one
	oneBad.Error, oneBad.Field, oneBad.Values = "Enter a whole number.", "target", map[string]string{"target": "x"}
	day := studioGoalData{stBase: stFixtureBase("goals"), Goal: stGoalOf(t, arch)}

	sprints := studioSprintsData{stBase: stFixtureBase("sprints"), Writable: stWritable(stFixtureProjects()), Resume: "prj_a", Range: "90d", Ranges: stRanges,
		Facts: []stLink{{Text: "Sprints", URL: "4"}, {Text: "Words", URL: "1,200"}}, Best: "Most words in one sprint: 500",
		Parts:  []stMeter{{Label: "Morning", Text: "3 sprints, 900 words", Value: 900, Max: 900}},
		Recent: []stSprintRow{{Day: "13 Mar 2026", Length: "25 minutes", Words: "500", State: "Ran to its end"}}}
	sprintsNow := sprints
	sprintsNow.Sprint = stSprintOf(t, stFixtureSprint(), stFixtureProjects())
	sprintsNone := studioSprintsData{stBase: stFixtureBase("sprints"), Range: "90d", Ranges: stRanges, None: true}
	sprintsBad := sprints
	sprintsBad.Error, sprintsBad.Field, sprintsBad.Form, sprintsBad.Values = "Choose a length.", "minutes", "sprint", map[string]string{"minutes": "custom", "custom": "999"}

	data := studioDataData{stBase: stFixtureBase("data"), Projects: stFixtureProjects(), Preset: "this_week", Has: true, Compared: true, HeadA: "9 Mar 2026 to 13 Mar 2026", HeadB: "2 Mar 2026 to 8 Mar 2026",
		Presets: []opt{{"this_week", "This week and last"}}, Ranges: []opt{{"all", "All time"}}, Tables: []opt{{"daily", "Days"}},
		Rows: []stCompareRow{{"Words added", "505", "329", "+176"}}}
	dataBad := data
	dataBad.Has, dataBad.Preset, dataBad.Error, dataBad.Field, dataBad.Form = false, "", "Give all four dates.", "a_from", "compare"

	card := studioCardData{stBase: stFixtureBase("card"), Live: 1, Cards: []stCardRow{
		{ID: "crd_1", Label: "For Sam", Made: "1 Mar 2026", Expires: "31 Mar 2026", Views: "Opened 3 times", Last: "Last opened 12 Mar 2026", State: "Live", Live: true, Projects: true},
		{ID: "crd_2", Made: "1 Jan 2026", Expires: "31 Jan 2026", Views: "Opened 0 times", State: "Expired"}}}
	cardOnce := card
	cardOnce.Once, cardOnce.OnceLabel = "/card/crd_0000000000000000000000000A.AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "For Sam"
	cardBad := studioCardData{stBase: stFixtureBase("card")}
	cardBad.Error, cardBad.Field, cardBad.Values = "Enter a number of days from 1 to 365.", "expires_days", map[string]string{"expires_days": "900", "label": "x"}

	set := settingsFixture("")
	set.Studio = &stSettings{NudgesOn: true, Zone: "Europe/Dublin", Progress: mail.ProgressOut{MailAvailable: true, Enabled: true, Frequency: "weekly", Email: "wren@example.org"}}

	return []fixture{
		{"studio", p5With("Writing studio", "/studio", full)}, {"studio", p5With("Writing studio", "/studio", fresh)}, {"studio", p5With("Writing studio", "/studio", bad)},
		{"studio", p5With("Writing studio", "/studio", running)}, {"studio", p5With("Writing studio", "/studio", allCal)},
		{"studio-goals", p5With("Goals", "/studio/goals", goals)}, {"studio-goals", p5With("Goals", "/studio/goals", goalsBad)}, {"studio-goals", p5With("Goals", "/studio/goals", goalsNone)},
		{"studio-goal", p5With("Words in total", "/studio/goals/sgl_2", one)}, {"studio-goal", p5With("Words in total", "/studio/goals/sgl_2", oneBad)}, {"studio-goal", p5With("Words a day", "/studio/goals/sgl_1", day)},
		{"studio-sprints", p5With("Sprints", "/studio/sprints", sprints)}, {"studio-sprints", p5With("Sprints", "/studio/sprints", sprintsNow)},
		{"studio-sprints", p5With("Sprints", "/studio/sprints", sprintsNone)}, {"studio-sprints", p5With("Sprints", "/studio/sprints", sprintsBad)},
		{"studio-data", p5With("Compare and export", "/studio/data", data)}, {"studio-data", p5With("Compare and export", "/studio/data", dataBad)},
		{"studio-card", p5With("Progress card", "/studio/card", card)}, {"studio-card", p5With("Progress card", "/studio/card", cardOnce)}, {"studio-card", p5With("Progress card", "/studio/card", cardBad)},
		{"settings", p5With("Settings", "/settings", set)},
	}
}

func studioFragmentFixtures() []fragmentFixture {
	pg := p5Base()
	t := stT{"en"}
	b := stFixtureBase("studio")
	bad := stFixtureBase("studio")
	bad.Error, bad.Field, bad.Form, bad.Values = "Choose a length.", "minutes", "sprint", map[string]string{"minutes": "custom"}
	set := func(p mail.ProgressOut, where, flash, errMsg string) map[string]any {
		return map[string]any{"P": pg, "S": &stSettings{NudgesOn: where != "off", Zone: "Europe/Dublin", Progress: p, Where: where, Flash: flash, Error: errMsg}, "CSRF": "tok"}
	}
	var out []fragmentFixture
	for _, tab := range []string{"studio", "goals", "sprints", "data", "card"} {
		out = append(out, fragmentFixture{"studio-parts", "studio-tabs", map[string]any{"P": pg, "Tab": tab}})
	}
	for _, g := range stFixtureDashboard(false).Goals.Items {
		out = append(out, fragmentFixture{"studio-parts", "studio-goal", map[string]any{"P": pg, "G": stGoalOf(t, g), "W": t}})
	}
	return append(out,
		fragmentFixture{"studio-parts", "studio-sprint-form", map[string]any{"P": pg, "B": b, "Projects": stWritable(stFixtureProjects()), "Pick": "prj_a", "Back": "studio"}},
		fragmentFixture{"studio-parts", "studio-sprint-form", map[string]any{"P": pg, "B": bad, "Projects": stWritable(stFixtureProjects()), "Pick": "", "Back": "sprints"}},
		fragmentFixture{"studio-parts", "studio-sprint-now", map[string]any{"P": pg, "S": stSprintOf(t, stFixtureSprint(), stFixtureProjects()), "CSRF": "tok", "Back": "studio"}},
		fragmentFixture{"settings-studio", "settings-studio", set(mail.ProgressOut{MailAvailable: true, Enabled: true, Frequency: "monthly", Email: "wren@example.org"}, "progress", "The progress email is on.", "")},
		fragmentFixture{"settings-studio", "settings-studio", set(mail.ProgressOut{MailAvailable: true, Email: "wren@example.org"}, "progress", "", "Choose weekly or monthly.")},
		fragmentFixture{"settings-studio", "settings-studio", set(mail.ProgressOut{MailAvailable: true, NeedsConfirm: true, Frequency: "weekly", Email: "wren@example.org"}, "nudges", "Notes are off.", "")},
		fragmentFixture{"settings-studio", "settings-studio", set(mail.ProgressOut{MailAvailable: true}, "off", "", "")},
		fragmentFixture{"settings-studio", "settings-studio", set(mail.ProgressOut{}, "", "", "")},
	)
}
