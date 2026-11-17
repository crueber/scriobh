package server

import (
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/i18n"
)

// Every key the Writing Studio's handlers build is in the catalogue (the templates' keys are checked by the catalogue's
// own test): a key written out, and each family a key is completed from (a period, a measure, a range, a state).
func TestEveryStudioScreenKeyIsInTheCatalogue(t *testing.T) {
	re := regexp.MustCompile(`"(studio\.(?:ui|js)\.[a-z0-9_.]+)"`)
	seen := 0
	for _, f := range []string{"studioui.go", "card.go", "goals.go", "write.go", "../../web/templates/studio.html", "../../web/templates/studio-sprints.html", "../../web/templates/studio-data.html"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range re.FindAllStringSubmatch(string(b), -1) {
			k := m[1]
			seen++
			if strings.HasSuffix(k, ".") || strings.HasSuffix(k, "_") { // a family
				n := 0
				for _, have := range i18n.Default.Keys() {
					if strings.HasPrefix(have, k) {
						n++
					}
				}
				if n < 2 {
					t.Errorf("%s builds keys from %q, and the catalogue has %d of them", f, k, n)
				}
				continue
			}
			if !i18n.Default.Has(k) && !i18n.Default.Has(k+".other") && !i18n.Default.Has(k+".one") {
				t.Errorf("%s uses %s, which the catalogue lacks", f, k)
			}
		}
	}
	if seen < 80 {
		t.Errorf("only %d keys found: has the way they are written changed?", seen)
	}
	// each family the handlers complete, by its members
	for prefix, words := range map[string][]string{
		"studio.ui.card.":           {"today", "7d", "30d", "365d"},
		"studio.ui.range.":          {"30d", "90d", "365d", "all"},
		"studio.ui.goal.p.":         {"day", "week", "month", "total"},
		"studio.ui.goal.m.":         {"words", "sessions", "chapters", "minutes"},
		"studio.ui.goal.value.":     {"words", "sessions", "chapters", "minutes"},
		"studio.ui.goal.met.":       {"day", "week", "month", "total"},
		"studio.ui.chart.h_":        {"day", "week", "month"},
		"studio.ui.compare.p_":      {"this_week", "this_month", "last_30"},
		"studio.ui.export.t_":       {"daily", "weekly", "goals", "sprints", "projects"},
		"studio.ui.sprints.s_":      {"completed", "stopped", "running", "abandoned"},
		"studio.ui.err.f_":          {"target", "deadline", "project_id", "minutes", "expires_days", "label", "period", "measure"},
		"studio.ui.goal.left.":      {"words.one", "words.other", "sessions.one", "chapters.other", "minutes.other"},
		"studio.ui.delta.today.":    {"more.one", "more.other", "same"},
		"studio.ui.delta.7d.":       {"more.one", "more.other", "fewer.one", "fewer.other", "same"},
		"studio.ui.delta.30d.":      {"more.one", "more.other", "fewer.one", "fewer.other", "same"},
		"studio.ui.delta.365d.":     {"more.one", "more.other", "fewer.one", "fewer.other", "same"},
		"studio.ui.goal.t.day.":     {"words", "sessions", "chapters", "minutes"},
		"studio.ui.goal.t.week.":    {"words", "sessions", "chapters", "minutes"},
		"studio.ui.goal.t.month.":   {"words", "sessions", "chapters", "minutes"},
		"studio.ui.goal.t.total.":   {"words", "sessions", "chapters", "minutes"},
		"studio.js.":                {"idle", "left.one", "left.other", "open.one", "open.other", "over", "done", "stopped", "abandoned", "started.one", "started.other", "started_open", "running", "failed", "offline", "bad"},
		"studio.ui.mail.saved_":     {"on", "off"},
		"studio.ui.nudges.saved_":   {"on", "off"},
		"studio.ui.spark.":          {"days", "weeks"},
		"studio.ui.cal.cell.":       {"one", "other"},
		"studio.ui.cal.cell_hit.":   {"one", "other"},
		"studio.ui.sprints.best.":   {"one", "other"},
		"studio.ui.sprints.part.":   {"one", "other"},
		"studio.ui.sprint.left.":    {"one", "other"},
		"studio.ui.sprint.open_for": {".one", ".other"},
	} {
		for _, w := range words {
			if !i18n.Default.Has(prefix + w) {
				t.Errorf("the catalogue lacks %s%s", prefix, w)
			}
		}
	}
}

// No word of the studio's screens frames a day that was not written, or a smaller number, as a fault. The backend's test
// holds its own keys to this; the screens' keys are held to a longer list ("only" among them).
func TestTheStudioScreensSayNoWordOfShame(t *testing.T) {
	bad := regexp.MustCompile(`(?i)\b(missed|failed|failure|behind|only|lost|broke|broken|warning|lazy|slipp\w+|falling|fell|shame|guilt|should have|you have not|you haven't|did not write|didn't write|no excuse|catch up|overdue)\b`)
	denominator := regexp.MustCompile(`(?i)\b(\d+|\{[a-z]+\}) of (\d+|\{[a-z]+\}|the last|them)\b|\bout of\b|\bof (7|30|the last \d+) days\b`)
	n := 0
	for _, k := range i18n.Default.Keys() {
		if !strings.HasPrefix(k, "studio.ui.") && !strings.HasPrefix(k, "studio.js.") {
			continue
		}
		n++
		s := i18n.Default.Raw("en", k)
		if m := bad.FindString(s); m != "" {
			t.Errorf("%s says %q: %s", k, m, s)
		}
		if denominator.MatchString(s) {
			t.Errorf("%s sets a count against what there could have been: %s", k, s)
		}
		if strings.Contains(s, "!") {
			t.Errorf("%s has an exclamation mark: %s", k, s)
		}
	}
	if n < 300 {
		t.Errorf("only %d screen keys found", n)
	}
}

func stLocation(t *testing.T, s *Server, c caller, path string, vals url.Values) string {
	t.Helper()
	rec := s.postForm(t, c, path, vals)
	if rec.Code != 303 {
		t.Fatalf("POST %s: %d, want a redirect: %s", path, rec.Code, rec.Body.String())
	}
	return rec.Header().Get("Location")
}

// Every action of the studio's pages answers with a redirect to a GET page, says what happened once, keeps what was typed
// beside a refusal, and repeats nothing on a reload.
func TestTheStudioFormsAnswerWithARedirectAndSayItOnce(t *testing.T) {
	w := newShareWorld(t, nil)
	s := w.s
	get := func(c caller, path string) string {
		t.Helper()
		rec := s.do(c, "GET", path, "")
		if rec.Code != 200 {
			t.Fatalf("GET %s: %d", path, rec.Code)
		}
		return rec.Body.String()
	}
	goal := url.Values{"period": {"week"}, "measure": {"words"}, "target": {"2000"}}
	if loc := stLocation(t, s, w.owner, "/ui/studio/goals", goal); loc != "/studio/goals" {
		t.Fatalf("a new goal goes to %q", loc)
	}
	if body := get(w.owner, "/studio/goals"); !strings.Contains(body, "Goal added.") || !strings.Contains(body, "Words a week") {
		t.Error("the goal's page does not say the goal was added, or does not list it")
	}
	if strings.Contains(get(w.owner, "/studio/goals"), "Goal added.") {
		t.Error("a reload says it again")
	}
	// the same goal again: refused beside the form, what was typed kept, a link to the one that exists; and no second goal
	goal.Set("target", "900")
	stLocation(t, s, w.owner, "/ui/studio/goals", goal)
	body := get(w.owner, "/studio/goals")
	for _, want := range []string{"You already have that goal", `value="900"`, `<option value="week" selected>`, `href="/studio/goals/sgl_`, `aria-invalid="true" aria-describedby="st-error"`} {
		if !strings.Contains(body, want) {
			t.Errorf("after a duplicate the page lacks %q", want)
		}
	}
	if n := strings.Count(get(w.owner, "/studio/goals"), `class="st-goal"`); n != 1 {
		t.Errorf("%d goals after a refused duplicate, want 1", n)
	}
	// a flash for one page is not taken by another
	stLocation(t, s, w.owner, "/ui/studio/goals", url.Values{"period": {"day"}, "measure": {"words"}, "target": {"0"}})
	if strings.Contains(get(w.owner, "/studio"), "whole number") || strings.Contains(get(w.owner, "/settings"), "whole number") {
		t.Error("another page took the goals page's message")
	}
	if !strings.Contains(get(w.owner, "/studio/goals"), "Enter a whole number of at least 1") {
		t.Error("the goals page lost its message to another page")
	}
	// another person's goal is not found, whoever asks
	id := regexp.MustCompile(`/studio/goals/(sgl_[0-9A-Z]+)`).FindStringSubmatch(body)
	if id == nil {
		t.Fatal("no goal id on the page")
	}
	if rec := s.do(w.editor, "GET", "/studio/goals/"+id[1], ""); rec.Code != 404 {
		t.Errorf("another person's goal: %d", rec.Code)
	}
	if rec := s.postForm(t, w.editor, "/ui/studio/goals/"+id[1]+"/archive", nil); rec.Code != 404 {
		t.Errorf("archiving another person's goal: %d", rec.Code)
	}
	// a form without its token changes nothing
	if rec := s.postForm(t, w.owner, "/ui/studio/goals", url.Values{"_csrf": {"wrong"}, "period": {"month"}, "measure": {"words"}, "target": {"5"}}); rec.Code != 403 {
		t.Errorf("a goal form with a wrong token: %d", rec.Code)
	}
	// a sprint: a reviewer may not start one (said in words, nothing started); an editor's start goes to the writing view
	stLocation(t, s, w.reviewer, "/ui/studio/sprint/start", url.Values{"project_id": {w.pid}, "minutes": {"15"}})
	if b := get(w.reviewer, "/studio/sprints"); !strings.Contains(b, "You can read that project but not write in it.") {
		t.Error("a reviewer's sprint is not refused in words")
	}
	if loc := stLocation(t, s, w.editor, "/ui/studio/sprint/start", url.Values{"project_id": {w.pid}, "minutes": {"custom"}, "custom": {"12"}}); loc != "/p/shared-book" {
		t.Errorf("a started sprint goes to %q", loc)
	}
	if b := get(w.editor, "/p/shared-book/d/"+w.doc1); !strings.Contains(b, "Sprint: 12 minutes left") || !strings.Contains(b, "js/sprint.") {
		t.Error("the writing view does not show the running sprint, or does not load its script")
	}
	if b := get(w.owner, "/p/shared-book/d/"+w.doc1); !strings.Contains(b, `id="sprint-open"`) || !strings.Contains(b, `title="Start or stop a sprint" hidden><span class="sp-now" data-sprint-now>Sprint</span>`) || strings.Contains(regexp.MustCompile(`data-ext="[^"]*"`).FindString(b), "sprint.") {
		t.Error("a person with no sprint is shown one, or is sent the sprint script with the page")
	}
	if b := get(w.reviewer, "/p/shared-book/d/"+w.doc1); strings.Contains(b, `id="st-sprint"`) || strings.Contains(b, `id="sprint-open"`) || strings.Contains(b, `id="sprint-menu"`) {
		t.Error("a reviewer is offered a sprint in the writing view")
	}
	stLocation(t, s, w.editor, "/ui/studio/sprint/start", url.Values{"project_id": {w.pid}, "minutes": {"15"}, "back": {"studio"}})
	if b := get(w.editor, "/studio"); !strings.Contains(b, "A sprint is already running.") {
		t.Error("a second sprint is not refused in words on the dashboard")
	}
	if loc := stLocation(t, s, w.editor, "/ui/studio/sprint/stop", nil); loc != "/studio/sprints" {
		t.Errorf("a stopped sprint goes to %q", loc)
	}
	if b := get(w.editor, "/studio/sprints"); !strings.Contains(b, "Sprint stopped: 0 words") {
		t.Error("the stop is not said")
	}
	stLocation(t, s, w.editor, "/ui/studio/sprint/stop", nil)
	if b := get(w.editor, "/studio/sprints"); !strings.Contains(b, "No sprint is running now.") {
		t.Error("stopping nothing is not said plainly")
	}
	// the card: the address is in no redirect, on the next page once, and never again
	loc := stLocation(t, s, w.owner, "/ui/studio/cards", url.Values{"label": {"For Sam"}, "expires_days": {"7"}})
	if loc != "/studio/card" {
		t.Fatalf("a new card goes to %q", loc)
	}
	once := get(w.owner, "/studio/card")
	tok := regexp.MustCompile(`value="(/card/crd_[^"]+)"`).FindStringSubmatch(once)
	if tok == nil {
		t.Fatal("the card's address is not on the page that follows")
	}
	if again := get(w.owner, "/studio/card"); strings.Contains(again, tok[1]) || strings.Contains(again, strings.SplitN(tok[1], ".", 2)[1]) {
		t.Error("the card's address is shown a second time")
	}
	if strings.Contains(once, "show_projects\" checked") || strings.Contains(get(w.owner, "/studio/card?show_projects=on"), `name="show_projects" checked`) {
		t.Error("an address can tick the box that names projects")
	}
	if rec := s.do(caller{}, "GET", tok[1], ""); rec.Code != 200 || strings.Contains(rec.Body.String(), "<script") || len(rec.Result().Cookies()) != 0 {
		t.Errorf("the card's page: %d, with a script or a cookie", rec.Code)
	}
	// the settings switches
	if loc := stLocation(t, s, w.owner, "/ui/settings/nudges", url.Values{}); loc != "/settings" {
		t.Errorf("the nudge switch goes to %q", loc)
	}
	if b := get(w.owner, "/settings"); !strings.Contains(b, "Notes are off.") || strings.Contains(b, `id="st-nudges" type="checkbox" name="nudges" value="true" checked`) {
		t.Error("the nudge switch did not turn off")
	}
	stLocation(t, s, w.owner, "/ui/settings/progress", url.Values{"do": {"on"}, "frequency": {"weekly"}})
	if b := get(w.owner, "/settings"); !strings.Contains(b, "Mail is not switched on for this server") {
		t.Error("turning the progress email on with mail off is not refused in words")
	}
	// signed out: every page asks for a sign-in, every form is refused
	for _, p := range []string{"/studio", "/studio/goals", "/studio/sprints", "/studio/data", "/studio/card"} {
		if rec := s.do(caller{}, "GET", p, ""); rec.Code != 303 {
			t.Errorf("signed out, %s: %d", p, rec.Code)
		}
	}
}

// A project locked by its passphrase gives the studio's pages no word, no day and no title, and is offered for no goal and no
// sprint; the dashboard says one project is left out and nothing more.
func TestTheStudioPagesLeaveALockedProjectOut(t *testing.T) {
	w := newShareWorld(t, nil)
	s := w.s
	words := strings.Repeat("lantern ", 321)
	d := s.do(w.owner, "GET", "/api/v1/docs/"+w.doc2, "").Body.String()
	rev := regexp.MustCompile(`"revision":(\d+)`).FindStringSubmatch(d)
	if rec := s.do(w.owner, "PATCH", "/api/v1/docs/"+w.doc2, `{"base_revision":`+rev[1]+`,"content":"`+words+`"}`); rec.Code != 200 {
		t.Fatalf("writing: %d %s", rec.Code, rec.Body)
	}
	open := s.do(w.owner, "GET", "/studio", "").Body.String()
	if !strings.Contains(open, "Shared Book") || !regexp.MustCompile(`data-num>3\d\d<`).MatchString(open) {
		t.Fatal("the control: an open project's words and title are on the dashboard")
	}
	if rec := s.do(w.owner, "PUT", "/api/v1/projects/"+w.pid+"/passphrase", `{"passphrase":"correct horse battery staple","idle_minutes":30}`); rec.Code != 200 {
		t.Fatalf("passphrase: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/lock", ""); rec.Code != 200 {
		t.Fatalf("lock: %d %s", rec.Code, rec.Body)
	}
	for _, p := range []string{"/studio", "/studio?range=all&cal=all", "/studio/goals", "/studio/sprints", "/studio/data", "/studio/card"} {
		rec := s.do(w.owner, "GET", p, "")
		body := rec.Body.String()
		if i := strings.Index(body, "<main"); i >= 0 {
			body = body[i:]
		}
		if rec.Code != 200 || strings.Contains(body, "Shared Book") || strings.Contains(body, w.pid) || regexp.MustCompile(`\b3\d\d words`).MatchString(body) {
			t.Errorf("%s (%d) names the locked project or shows its words", p, rec.Code)
		}
	}
	if body := s.do(w.owner, "GET", "/studio", "").Body.String(); !strings.Contains(body, "1 project is locked and not counted.") || !strings.Contains(body, "data-num>0<") {
		t.Error("the dashboard does not say a project is left out, or still counts it")
	}
	stLocation(t, s, w.owner, "/ui/studio/sprint/start", url.Values{"project_id": {w.pid}, "minutes": {"15"}})
	if body := s.do(w.owner, "GET", "/studio/sprints", "").Body.String(); !strings.Contains(body, `id="st-error"`) || strings.Contains(body, "minutes left") {
		t.Error("a sprint on a locked project is not refused")
	}
	stLocation(t, s, w.owner, "/ui/studio/goals", url.Values{"period": {"day"}, "measure": {"words"}, "target": {"100"}, "project_id": {w.pid}})
	if body := s.do(w.owner, "GET", "/studio/goals", "").Body.String(); !strings.Contains(body, "Choose a project you can open.") || strings.Contains(body, `class="st-goal"`) {
		t.Error("a goal on a locked project is not refused")
	}
}

// The studio's pages, the settings sections and the project Goals page's studio section carry no plain English in the
// pseudo-locale: every word is the catalogue's.
func TestThePseudoLocaleLeavesNoEnglishOnTheStudioPages(t *testing.T) {
	w := newShareWorld(t, map[string]string{"SCRIOBH_OIDC_STUB": "1"})
	s := w.s
	stLocation(t, s, w.owner, "/ui/studio/goals", url.Values{"period": {"day"}, "measure": {"words"}, "target": {"100"}})
	stLocation(t, s, w.owner, "/ui/studio/goals", url.Values{"period": {"total"}, "measure": {"words"}, "target": {"90000"}, "project_id": {w.pid}, "deadline": {"2099-01-01"}})
	stLocation(t, s, w.owner, "/ui/studio/cards", url.Values{"expires_days": {"7"}})
	stLocation(t, s, w.owner, "/ui/studio/sprint/start", url.Values{"project_id": {w.pid}, "minutes": {"25"}})
	if rec := s.do(w.owner, "PUT", "/api/v1/me/prefs", `{"items":{"language":"xx"}}`); rec.Code != 200 {
		t.Fatalf("language: %d", rec.Code)
	}
	goals := s.do(w.owner, "GET", "/studio/goals", "").Body.String()
	id := regexp.MustCompile(`/studio/goals/(sgl_[0-9A-Z]+)`).FindStringSubmatch(goals)
	if id == nil {
		t.Fatal("no goal on the page")
	}
	for _, p := range []string{"/studio", "/studio?range=all&cal=all", "/studio?from=2026-02-01&to=2026-01-01", "/studio/goals", "/studio/goals/" + id[1], "/studio/sprints", "/studio/data", "/studio/data?preset=last_30",
		"/studio/data?a_from=x", "/studio/card", "/studio/card"} {
		rec := s.do(w.owner, "GET", p, "")
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, "⟦") {
			t.Errorf("%s: %d, not in the pseudo-locale", p, rec.Code)
			continue
		}
		if words := englishIn(body); len(words) > 0 {
			t.Errorf("%s has English that did not come from the catalogue: %s", p, strings.Join(words, " "))
		}
	}
	// the sections added to older pages: only they are looked at (the rest of those pages is older English)
	for p, ids := range map[string][]string{"/settings": {`id="progress"`, `id="studio"`}, "/p/shared-book/goals": {`id="studio"`}} {
		body := s.do(w.owner, "GET", p, "").Body.String()
		for _, sec := range ids {
			i := strings.Index(body, "<section "+sec)
			if i < 0 {
				t.Errorf("%s has no section %s", p, sec)
				continue
			}
			part := body[i:]
			part = part[:strings.Index(part, "</section>")]
			if words := englishIn("<main>" + part + "</main>"); len(words) > 0 {
				t.Errorf("%s %s has English that did not come from the catalogue: %s", p, sec, strings.Join(words, " "))
			}
		}
	}
	// the writing view's sprint line and menu
	body := s.do(w.owner, "GET", "/p/shared-book/d/"+w.doc1, "").Body.String()
	i := strings.Index(body, `<div id="sprint-menu"`)
	if i < 0 {
		t.Fatal("no sprint menu in the writing view")
	}
	menu := body[i:]
	if j := strings.Index(menu, "\n</div>\n"); j > 0 {
		menu = menu[:j]
	}
	if words := englishIn("<main>" + reAttrDataMsg.ReplaceAllString(menu, "") + "</main>"); len(words) > 0 {
		t.Errorf("the writing view's sprint menu has English that did not come from the catalogue: %s", strings.Join(words, " "))
	}
	if !strings.Contains(body, "⟦Śṕŕíńť") {
		t.Error("the sprint line is not in the pseudo-locale")
	}
}

var reAttrDataMsg = regexp.MustCompile(`data-(msg|sprint)="[^"]*"`)
