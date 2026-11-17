package studio_test

import (
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/projectgate"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/review"
	"github.com/crueber/scriobh/internal/domain/studio"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
)

func tr(key string, args ...any) string { return i18n.Default.T("en", key, args...) }

func TestAProgressCardShowsNumbersAndNothingOfAnyProject(t *testing.T) {
	w := newWorld(t)
	u := w.person("u", "UTC")
	s := w.session(u, "ses_card")
	p := w.project(s, "CANARYPROJECT secret title")
	d := w.doc(s, p)
	w.at("2026-03-01T09:00:00Z")
	mkGoal(t, w, s, "day", "words", 50)
	w.call(s, "studio.goal.create", studio.GoalCreateIn{Period: "week", Measure: "words", Target: 1000, ProjectID: p})
	for _, day := range []string{"2026-03-02", "2026-03-03"} {
		w.at(day + "T09:00:00Z")
		w.save(s, d, w.text(s, d)+"CANARYWORD "+lorem(60, "x"+day[8:]))
	}
	w.call(s, "comment.create", review.CreateIn{ProjectID: p, DocumentID: d, Body: "CANARYCOMMENT in the margin"})
	w.at("2026-03-03T12:00:00Z")

	made := w.call(s, "studio.card.create", studio.CardIn{Label: "for my sister", ExpiresDays: 10}).(studio.CardItem).Item
	if !regexp.MustCompile(`^crd_[0-9A-HJKMNP-TV-Z]{26}\.[A-Za-z0-9_-]{43}$`).MatchString(made.Token) || made.Path != "/card/"+made.Token || made.ExpiresAt != "2026-03-13T12:00:00.000Z" {
		t.Fatalf("card %+v", made)
	}
	// The secret is shown once: the list never has it, and only a hash of it is stored.
	list := w.call(s, "studio.card.list", studio.CardListIn{}).(studio.CardListOut)
	if len(list.Items) != 1 || list.Items[0].Token != "" || list.Items[0].Path != "" || list.Items[0].Label != "for my sister" {
		t.Errorf("list %+v", list)
	}
	var stored string
	_ = w.d.Read().QueryRow(`SELECT token_hash FROM studio_cards WHERE id = ?`, made.ID).Scan(&stored)
	if strings.Contains(stored, strings.SplitN(made.Token, ".", 2)[1]) || len(stored) != 64 {
		t.Errorf("stored %q", stored)
	}

	v, err := w.cards.Open(t.Context(), made.Token, true)
	if err != nil {
		t.Fatal(err)
	}
	if v.Streak != 2 || v.Longest != 2 || !v.HasGoal || v.GoalsActive != 2 || v.WritingDays30 != 2 || len(v.Goals) != 2 {
		t.Errorf("card %+v", v)
	}
	svg := string(studio.CardSVG(v, tr))
	page := marshal(t, v) + svg
	for _, bad := range []string{"CANARYPROJECT", "CANARYWORD", "CANARYCOMMENT", "Chapter 1", "secret title", "for my sister", p, d} {
		if strings.Contains(page, bad) {
			t.Errorf("the card holds %q", bad)
		}
	}
	for _, g := range v.Goals {
		if g.Project != "" {
			t.Errorf("a project named without being asked: %+v", g)
		}
	}
	// Flat and plain: no gradient, no script, no address.
	for _, bad := range []string{"Gradient", "gradient", "<script", "http://", "https://", "href", "<image", "<style", "onload"} {
		if strings.Contains(strings.ReplaceAll(svg, `xmlns="http://www.w3.org/2000/svg"`, ""), bad) {
			t.Errorf("the svg holds %q", bad)
		}
	}
	if !strings.Contains(svg, ">2<") {
		t.Errorf("the streak is not in the svg: %s", svg)
	}

	// Views are counted for the owner to see; a read for the picture or the data is not a view.
	if _, err := w.cards.Open(t.Context(), made.Token, true); err != nil {
		t.Fatal(err)
	}
	if _, err := w.cards.Open(t.Context(), made.Token, false); err != nil {
		t.Fatal(err)
	}
	if got := w.call(s, "studio.card.list", studio.CardListIn{}).(studio.CardListOut).Items[0]; got.Views != 2 || got.LastViewedAt == "" {
		t.Errorf("views %+v", got)
	}

	// A card that was asked to name the project does; one that was not does not.
	named := w.call(s, "studio.card.create", studio.CardIn{ShowProjects: true}).(studio.CardItem).Item
	nv, _ := w.cards.Open(t.Context(), named.Token, false)
	found := false
	for _, g := range nv.Goals {
		found = found || g.Project == "CANARYPROJECT secret title"
	}
	if !found {
		t.Errorf("the card that shows projects does not: %+v", nv.Goals)
	}

	// Every way a token can fail opens nothing, the same way.
	secret := strings.SplitN(made.Token, ".", 2)[1]
	for name, tok := range map[string]string{
		"empty": "", "no dot": made.ID, "wrong secret": made.ID + "." + strings.Repeat("A", 43), "wrong id": "crd_00000000000000000000000000." + secret,
		"a share token's shape": "shl_" + made.ID[4:] + "." + secret, "long": strings.Repeat("a", 5000), "path": "../" + made.Token,
	} {
		if _, err := w.cards.Open(t.Context(), tok, true); err != studio.ErrNoCard {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Revoked: at once. Expired: at its time. A locked project: off the card at once.
	rev := w.call(s, "studio.card.revoke", studio.CardRevokeIn{ID: made.ID}).(studio.CardItem).Item
	if !rev.Revoked {
		t.Errorf("revoked %+v", rev)
	}
	if _, err := w.cards.Open(t.Context(), made.Token, true); err != studio.ErrNoCard {
		t.Errorf("a revoked card opened: %v", err)
	}
	if again := w.call(s, "studio.card.revoke", studio.CardRevokeIn{ID: made.ID}).(studio.CardItem).Item; !again.Revoked {
		t.Error("revoking twice")
	}
	if _, err := w.cards.Open(t.Context(), named.Token, false); err != nil {
		t.Fatal(err)
	}
	if _, err := w.try(s, "project.passphrase.set", projectgate.SetIn{ID: p, Passphrase: "a long enough one"}); err != nil {
		t.Fatal(err)
	}
	nv, _ = w.cards.Open(t.Context(), named.Token, false)
	if marshal(t, nv) == "" || strings.Contains(marshal(t, nv), "CANARYPROJECT") || nv.WritingDays30 != 0 || nv.Streak != 0 && nv.Streak != 1 {
		t.Errorf("a protected project is on the card: %+v", nv)
	}
	if nv.WritingDays30 != 0 || len(nv.Goals) != 0 && nv.Goals[0].Scope == "project" {
		t.Errorf("a protected project is on the card: %+v", nv)
	}
	w.at("2026-04-03T12:00:00Z") // the card was made on 3 March for the default 30 days
	if _, err := w.cards.Open(t.Context(), named.Token, false); err != studio.ErrNoCard {
		t.Errorf("an expired card opened: %v", err)
	}
	// A key cannot make, list or revoke one, and another person's card is not there.
	other := w.person("o", "UTC")
	key := other
	key.p.KeyID, key.p.Via, key.p.Scope = "key_x", "apikey", "write"
	for _, op := range []string{"studio.card.create", "studio.card.list"} {
		var in any = studio.CardIn{}
		if op == "studio.card.list" {
			in = studio.CardListIn{}
		}
		if _, err := w.try(key, op, in); err == nil {
			t.Errorf("%s through a key", op)
		}
	}
	if _, err := w.try(other, "studio.card.revoke", studio.CardRevokeIn{ID: named.ID}); !isNotFound(err) {
		t.Errorf("another person's card: %v", err)
	}
	// Five at a time, an expiry of at most a year, a label that is a line.
	if _, err := w.try(other, "studio.card.create", studio.CardIn{ExpiresDays: 366}); err == nil {
		t.Error("a card for 366 days")
	}
	if _, err := w.try(other, "studio.card.create", studio.CardIn{Label: "two\nlines"}); err == nil {
		t.Error("a label with a line break")
	}
	for i := 0; i < 5; i++ {
		w.call(other, "studio.card.create", studio.CardIn{})
	}
	if _, err := w.try(other, "studio.card.create", studio.CardIn{}); !isConflict(err) {
		t.Errorf("the sixth card: %v", err)
	}
	_ = ops.CodeConflict
	_ = projects.StateIn{}
}

func TestTheCardSVGEscapesAProjectTitle(t *testing.T) {
	v := studio.CardView{Day: "2026-03-03", Goals: []studio.CardGoal{{Period: "day", Measure: "words", Scope: "project", Project: `</text><script>alert(1)</script>"&`, Target: 5, Value: 9, Percent: 180, Met: true}}}
	svg := string(studio.CardSVG(v, tr))
	if strings.Contains(svg, "<script") || strings.Contains(svg, `</text><script`) || !strings.Contains(svg, "&lt;script&gt;") {
		t.Errorf("not escaped: %s", svg)
	}
	// A goal past 100 percent does not draw outside the card.
	if strings.Contains(svg, `width="950"`) || !strings.Contains(svg, `width="528"`) {
		t.Errorf("bar: %s", svg)
	}
}

// ---- the export --------------------------------------------------------------------------------------------

func TestTheExportIsDeterministicSafeAndTheCallersOwn(t *testing.T) {
	w := newWorld(t)
	a, b := w.person("a", "UTC"), w.person("b", "UTC")
	titles := []string{"=HYPERLINK(\"http://x\",\"c\")", "+SUM(1)", "-2+3", "@cmd", "\t=1", "plain, with \"quotes\" and, commas"}
	var docsOf []string
	var pids []string
	for _, ti := range titles {
		p := w.project(a, ti)
		pids = append(pids, p)
		docsOf = append(docsOf, w.doc(a, p))
	}
	pb := w.project(b, "BOTHERS-CANARY")
	db := w.doc(b, pb)
	w.at("2026-03-02T09:00:00Z")
	for i, d := range docsOf {
		w.save(a, d, lorem(10+i, "t"))
	}
	w.save(a, docsOf[0], "") // a day of cutting: a negative net figure
	w.save(b, db, lorem(77, "bb"))
	mkGoal(t, w, a, "day", "words", 20)

	exp := func(p person, in studio.ExportIn) studio.ExportOut {
		return w.call(p, "studio.export", in).(studio.ExportOut)
	}
	one := exp(a, studio.ExportIn{Table: "daily", Range: "30d"})
	two := exp(a, studio.ExportIn{Table: "daily", Range: "30d"})
	if string(one.Body) != string(two.Body) {
		t.Error("the same export gave different bytes")
	}
	if !regexp.MustCompile(`^scriobh-writing-daily-2026-02-0\d-2026-03-02\.csv$`).MatchString(one.Filename) || one.ContentType != "text/csv; charset=utf-8" {
		t.Errorf("name %q type %q", one.Filename, one.ContentType)
	}
	cd := ops.ContentDisposition(one.Filename)
	if strings.ContainsAny(cd, "\r\n") || !strings.HasPrefix(cd, "attachment; filename=\"") {
		t.Errorf("disposition %q", cd)
	}
	lines := strings.Split(strings.TrimSuffix(string(one.Body), "\r\n"), "\r\n")
	if lines[0] != "day,words_added,net_words,minutes,sessions,goal_met" || len(lines) != 31 {
		t.Fatalf("header %q, %d lines", lines[0], len(lines))
	}
	// 10+11+12+13+14+15 = 75 words typed; the first chapter (10 words) emptied: net 65, and the goal day is marked with a 1.
	if last := lines[len(lines)-1]; !strings.HasPrefix(last, "2026-03-02,75,65,") || !strings.HasSuffix(last, ",1") {
		t.Errorf("the day: %q", last)
	}
	if strings.Contains(string(one.Body), "BOTHERS-CANARY") || strings.Contains(string(one.Body), ",77,") {
		t.Error("another person's numbers are in the export")
	}

	// The projects table: text cells that begin like a formula are made text; numbers, even negative ones, are left alone.
	proj := string(exp(a, studio.ExportIn{Table: "projects"}).Body)
	for _, want := range []string{"\"'=HYPERLINK(\"\"http://x\"\",\"\"c\"\")\"", "'+SUM(1)", "'-2+3", "'@cmd", "\"plain, with \"\"quotes\"\" and, commas\""} {
		if !strings.Contains(proj, want) {
			t.Errorf("the projects table lacks %s:\n%s", want, proj)
		}
	}
	for _, line := range strings.Split(proj, "\r\n") {
		for _, cell := range strings.Split(line, ",") {
			if cell != "" && strings.ContainsAny(cell[:1], "=+@") {
				t.Errorf("a cell begins like a formula: %q", cell)
			}
		}
	}
	weekly := string(exp(a, studio.ExportIn{Table: "weekly", From: "2026-02-23", To: "2026-03-08"}).Body)
	if !strings.Contains(weekly, "2026-03-02,75,65,") {
		t.Errorf("weekly:\n%s", weekly)
	}
	if got := string(exp(a, studio.ExportIn{Table: "goals"}).Body); !strings.HasPrefix(got, "period,measure,target,value,percent,status,start_date,deadline,project\r\nday,words,20,75,375,active,2026-03-02,,\r\n") {
		t.Errorf("goals:\n%s", got)
	}
	md := string(exp(a, studio.ExportIn{Format: "md", Range: "30d"}).Body)
	if !strings.HasPrefix(md, "# Writing, ") || !strings.Contains(md, "| 2026-03-02 | 75 | 65 | 0 |") || strings.Contains(md, "BOTHERS-CANARY") {
		t.Errorf("markdown:\n%s", md)
	}
	// A title that is markup is text in the Markdown table.
	w.call(a, "studio.goal.create", studio.GoalCreateIn{Period: "total", Measure: "words", Target: 99, ProjectID: pids[5]})
	md = string(exp(a, studio.ExportIn{Format: "md"}).Body)
	if strings.Contains(md, "plain, with \"quotes\" and, commas |") && !strings.Contains(md, "plain, with") {
		t.Error("markdown title")
	}

	// Bounds: ten years of days at most, a valid range, a known table and format.
	for name, in := range map[string]studio.ExportIn{
		"11 years": {From: "2015-01-01", To: "2026-03-02"}, "backwards": {From: "2026-03-02", To: "2026-03-01"}, "half a range": {From: "2026-03-02"},
		"unknown table": {Table: "everything"}, "unknown format": {Format: "xlsx"}, "unknown range": {Range: "forever"},
	} {
		if _, err := w.try(a, "studio.export", in); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	long := exp(a, studio.ExportIn{Table: "daily", From: "2016-03-03", To: "2026-03-02"})
	if len(long.Body) > 2<<20 || strings.Count(string(long.Body), "\r\n") != 3653 { // a header and ten years less a day: 2016-03-03 to 2026-03-02 is 3652 days, two of them leap days
		t.Errorf("ten years: %d bytes, %d lines", len(long.Body), strings.Count(string(long.Body), "\r\n"))
	}
	// Someone with nothing gets headers and nothing else.
	c := w.person("c", "UTC")
	if got := string(exp(c, studio.ExportIn{Table: "daily", Range: "30d"}).Body); strings.Count(got, "\r\n") != 31 {
		t.Errorf("an empty person: %d lines", strings.Count(got, "\r\n"))
	}
	_ = projectgate.ServiceName
}
