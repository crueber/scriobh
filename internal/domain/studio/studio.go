package studio

import (
	"context"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/ops"
)

func init() {
	// A backup brings back the sprints that were running when it was taken. They ended with that moment: they are
	// closed, and the words they hold stay.
	backup.PostRestore = append(backup.PostRestore, func(ctx context.Context, d *db.DB, key []byte) (string, error) {
		n, err := CloseOpenSprints(ctx, d)
		if err != nil || n == 0 {
			return "", err
		}
		return "Writing studio: a sprint that was running when the backup was made was closed.", nil
	})
}

// Register adds the writing studio's ops.
func Register(r *ops.Registry) {
	me := ops.Authenticated()
	mine := ops.Authenticated().ForSessions()
	reader := ops.RequireProject(ops.ProjectReader)
	editor := ops.RequireProject(ops.ProjectEditor)

	ops.Register(r, ops.Op{Name: "studio.dashboard", Method: "GET", Path: "/api/v1/me/studio", Auth: me,
		Desc: "The writing dashboard in one read, for the caller only: today's words against the daily target, the habit-streak card, the goals with counts, the four period cards with deltas and sparklines, the words-written series with the busiest weekdays and hours, the 24-week activity calendar, the person's own words per project, the three-week trend, a few gentle nudges as message keys, and the running sprint. Every figure counts words added (net words are beside them, never against a day), is summed from the projects the caller may read and has unlocked, and is computed on this server. It names the projects its goals and totals are about (title and address), holds no manuscript text, and days written in projects the caller cannot open now are not counted."}, dashboard)
	ops.Register(r, ops.Op{Name: "studio.series", Method: "GET", Path: "/api/v1/me/studio/series", Auth: me,
		Desc: "The caller's words-written series over 30 days, 90 days, 365 days, all time or a custom range (days, weeks or months by length), with pace and the most productive weekdays and times of day."}, seriesOp)
	ops.Register(r, ops.Op{Name: "studio.calendar", Method: "GET", Path: "/api/v1/me/studio/calendar", Auth: me,
		Desc: "The caller's activity calendar: columns of seven days, Monday first, with month labels, Monday-Wednesday-Friday row labels and five intensity steps read from the caller's own writing days. An empty day is empty; nothing is marked missed. Use all=true for all of the caller's writing."}, calendarOp)
	ops.Register(r, ops.Op{Name: "studio.compare", Method: "GET", Path: "/api/v1/me/studio/compare", Auth: me,
		Desc: "Compare two periods (this week against last, this month against last, or any two ranges), or two projects over the same range (draft against draft): words added, net words, minutes, sessions and writing days, with the change."}, compare)
	ops.Register(r, ops.Op{Name: "studio.project.totals", Method: "GET", Path: "/api/v1/projects/{id}/studio", Auth: reader,
		Desc: "The caller's own daily and weekly words on one project (never other members'), for the last N days."}, projectTotals)

	ops.Register(r, ops.Op{Name: "studio.goal.list", Method: "GET", Path: "/api/v1/me/studio/goals", Auth: me,
		Desc: "The caller's goals with progress: studio goals (day, week, month or total; words, sessions, chapters or minutes; over every project or one) and the daily and total goals set on projects' Goals pages, with active and completed counts and, for total goals, a forecast stated as an estimate with its basis."}, goalList)
	ops.Register(r, ops.Op{Name: "studio.goal.create", Method: "POST", Path: "/api/v1/me/studio/goals", Status: 201, Auth: me,
		Desc: "Make a goal. Goals count words added, never net words, and a day without writing is never counted against one. At most 20 active goals; one per scope, period and measure."}, goalCreate)
	ops.Register(r, ops.Op{Name: "studio.goal.update", Method: "PATCH", Path: "/api/v1/me/studio/goals/{id}", Auth: me,
		Desc: "Change a goal's target (or a total goal's deadline). The change is a new version: periods already over keep the target they had, and a period that was already met stays met."}, goalUpdate)
	ops.Register(r, ops.Op{Name: "studio.goal.archive", Method: "POST", Path: "/api/v1/me/studio/goals/{id}/archive", Auth: me,
		Desc: "End a goal. Its record stays, and the days it was met stay met. To go on, make a new goal."}, goalArchive)
	ops.Register(r, ops.Op{Name: "studio.goal.history", Method: "GET", Path: "/api/v1/me/studio/goals/{id}/history", Auth: me,
		Desc: "A goal's versions and its past periods, each judged against the target that was in force then."}, goalHistory)

	ops.Register(r, ops.Op{Name: "studio.sprint.start", Method: "POST", Path: "/api/v1/projects/{id}/studio/sprints", Status: 201, Auth: editor,
		Desc: "Start a sprint (15, 25, 45 or any 1 to 240 minutes) or an open session timer (0 minutes) on a project. The words added while it runs are the save loop's own additions, so each word is counted once in its day and once in the sprint (a sprint across local midnight holds words of two days). One at a time."}, sprintStart)
	ops.Register(r, ops.Op{Name: "studio.sprint.stop", Method: "POST", Path: "/api/v1/me/studio/sprint/stop", Auth: me,
		Desc: "Stop the running sprint. Its words stay."}, sprintStop)
	ops.Register(r, ops.Op{Name: "studio.sprint.abandon", Method: "POST", Path: "/api/v1/me/studio/sprint/abandon", Auth: me,
		Desc: "Abandon the running sprint: it is left out of the sprint statistics. The words written stay in the day."}, sprintAbandon)
	ops.Register(r, ops.Op{Name: "studio.sprint.current", Method: "GET", Path: "/api/v1/me/studio/sprint", Auth: me,
		Desc: "The running sprint or session timer, if any, with the seconds left. A sprint left running past its planned end and two minutes is read as completed."}, sprintCurrent)
	ops.Register(r, ops.Op{Name: "studio.sprint.stats", Method: "GET", Path: "/api/v1/me/studio/sprints", Auth: me,
		Desc: "Sprint statistics: how many, words per sprint, the best sprint, and by time of day."}, sprintStats)

	ops.Register(r, ops.Op{Name: "studio.nudge.seen", Method: "POST", Path: "/api/v1/me/studio/nudges/seen", Auth: me,
		Desc: "Record that nudges were shown, so they are not offered again today. At most three are offered a day."}, nudgeSeen)
	ops.Register(r, ops.Op{Name: "studio.export", Method: "GET", Path: "/api/v1/me/studio/export", Auth: me,
		Desc: "The caller's analytics as CSV (daily, weekly, goals, sprints or projects) or Markdown, over a range of at most ten years. Deterministic; text cells are safe against spreadsheet formulas."}, export)

	ops.Register(r, ops.Op{Name: "studio.card.create", Method: "POST", Path: "/api/v1/me/studio/cards", Status: 201, Auth: mine, NoDryRun: true, SecretOutput: true,
		Desc: "Make a private progress card: an unguessable link to a small page (and an SVG) that shows the streak and a goal summary and nothing of any project. It expires (up to a year), can be revoked and counts its views. The secret is in this answer only. Browser session only."}, cardCreate)
	ops.Register(r, ops.Op{Name: "studio.card.list", Method: "GET", Path: "/api/v1/me/studio/cards", Auth: mine,
		Desc: "The caller's progress cards with their views and expiry. The secret is never listed. Browser session only."}, cardList)
	ops.Register(r, ops.Op{Name: "studio.card.revoke", Method: "POST", Path: "/api/v1/me/studio/cards/{id}/revoke", Auth: mine,
		Desc: "Revoke a progress card: its link stops working at once. Browser session only."}, cardRevoke)
}
