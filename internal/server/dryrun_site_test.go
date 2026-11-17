package server

import "testing"

// siteFixtures drives the site ops through the dry-run sweep: the settings
// writes are rolled back, a build records nothing, and publish (which writes a
// folder) refuses. The project needs a post (a chapter whose status is done) for
// the preview and the build to have something to make.
func (f *dryFixture) siteFixtures(t *testing.T) map[string]drive {
	t.Helper()
	base := "/api/v1/projects/" + f.project
	rec := f.srv.do(f.owner, "POST", base+"/docs", `{"title":"Site post","kind":"chapter","content":"A post for the site.\n\nWith two paragraphs."}`)
	if rec.Code >= 300 {
		t.Fatalf("site fixture post: %d %s", rec.Code, rec.Body)
	}
	id := idOf(t, rec.Body.Bytes())
	if rec := f.srv.do(f.owner, "PATCH", "/api/v1/docs/"+id, `{"base_revision":1,"status":"done"}`); rec.Code >= 300 {
		t.Fatalf("site fixture status: %d %s", rec.Code, rec.Body)
	}
	return map[string]drive{
		"site.options.set": {base + "/site/options", `{"options":{"tagline":"previewed","posts_per_page":5}}`, true},
		"site.doc.set":     {base + "/site/documents/" + f.doc, `{"tags":["previewed"],"published_at":"2026-01-02"}`, true},
		"site.preview":     {base + "/site/preview", ``, false},
		"site.build":       {base + "/site/build", ``, true},
	}
}
