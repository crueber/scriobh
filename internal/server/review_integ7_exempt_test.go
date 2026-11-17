package server

import "testing"

// The exempt pages answer a member who has not unlocked: what do they carry? (The page sweep skips them.)
func TestReviewInteg7ExemptPagesCarryNoCanary(t *testing.T) {
	s := newAuthzServer(t)
	w := newGateWorld(t, s)
	for _, c := range []caller{w.editor, w.owner2, w.admin} {
		for _, p := range []string{"/p/" + w.slug + "/passphrase", "/p/" + w.slug + "/unlock", "/api/v1/projects/" + w.pid + "/passphrase"} {
			rec := s.do(c, "GET", p, "")
			t.Logf("%s GET %s: %d %d bytes", c.name, p, rec.Code, rec.Body.Len())
			if l := w.leaks(rec.Body.String()); len(l) > 0 {
				t.Errorf("%s GET %s while locked leaks %v: %.300s", c.name, p, l, rec.Body)
			}
			if c.name == "editor" {
				t.Logf("%s", rec.Body.String()[:min(rec.Body.Len(), 600)])
			}
		}
	}
}
