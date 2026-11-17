package server

import (
	"net/http"
	"testing"

	"github.com/crueber/scriobh/internal/pandoc/pandoctest"
)

// GUARD (passes today; TestReviewGateAppliesToEveryPandocExportFormat): with the bridge on (against the stand-in), the owner who has unlocked a protected project
// gets the file for every Pandoc format, and a session that has not unlocked it gets the gate's 403 and Pandoc is
// never started for it.
func TestReviewGateAppliesToEveryPandocExportFormat(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	dir, bin := pandoctest.Install(t)
	rec := s.do(w.admin, "PUT", "/api/v1/admin/pandoc", `{"enabled":true,"path":`+jsonString(bin)+`}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("switching the bridge on: %d %.300s", rec.Code, rec.Body)
	}
	base := "/api/v1/projects/" + w.pid + "/export/compile"
	for _, format := range []string{"odt", "rtf", "latex", "rst", "mediawiki"} {
		body := `{"preset":"plain-markdown","options":{"format":"` + format + `"}}`
		ranBefore := pandoctest.Log(t, dir, "args.log")
		for _, c := range []caller{w.editor, w.owner2, w.admin} {
			rec := s.do(c, "POST", base, body)
			if rec.Code != http.StatusForbidden || !lockedBody(rec.Body.String()) {
				t.Errorf("%s as %s while locked: %d %.200s", format, c.name, rec.Code, rec.Body)
			}
		}
		if got := pandoctest.Log(t, dir, "args.log"); got != ranBefore {
			t.Errorf("%s: Pandoc was started for a locked caller", format)
		}
		rec := s.do(w.owner, "POST", base, body)
		if rec.Code != http.StatusOK || rec.Body.String() != "EXPORTED-FILE" {
			t.Errorf("%s as the unlocked owner: %d %.300s", format, rec.Code, rec.Body)
		}
		if pandoctest.Log(t, dir, "args.log") == ranBefore {
			t.Errorf("%s: the owner's export did not reach Pandoc", format)
		}
		if rec := s.do(w.outsider, "POST", base, body); rec.Code != http.StatusNotFound {
			t.Errorf("%s as a stranger: %d", format, rec.Code)
		}
	}
}
