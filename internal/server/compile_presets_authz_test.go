package server

import (
	"net/http"
	"strings"
	"testing"
)

// A custom preset is reached through its own project only: its id in another
// project's path is a 404 for update, delete and use, for an owner of both.
func TestCustomPresetOfAnotherProjectIs404(t *testing.T) {
	s := newAuthzServer(t)
	f := newDryFixture(t, s)
	rec := s.do(f.owner, "POST", "/api/v1/projects", `{"title":"Another project"}`)
	if rec.Code >= 300 {
		t.Fatalf("project: %d %s", rec.Code, rec.Body)
	}
	other := idOf(t, rec.Body.Bytes())
	rec = s.do(f.owner, "POST", "/api/v1/projects/"+other+"/compile/presets", `{"name":"Theirs","base":"epub3"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("preset: %d %s", rec.Code, rec.Body)
	}
	preset := idOf(t, rec.Body.Bytes())
	if !strings.HasPrefix(preset, "cpr_") {
		t.Fatalf("preset id %q", preset)
	}
	mine := "/api/v1/projects/" + f.project
	for _, c := range []struct{ method, path, body string }{
		{"PATCH", mine + "/compile/presets/" + preset, `{"name":"Taken"}`},
		{"DELETE", mine + "/compile/presets/" + preset, ""},
		{"POST", mine + "/export/compile", `{"preset":"` + preset + `"}`},
		{"POST", mine + "/compile/preview", `{"preset":"` + preset + `"}`},
		{"POST", mine + "/export/submission", `{"preset":"` + preset + `"}`},
		{"POST", mine + "/compile/cover-spec", `{"preset":"` + preset + `","page_count":100,"page_thickness_in":0.0025}`},
		{"POST", mine + "/export/cover", `{"preset":"` + preset + `","page_count":100,"page_thickness_in":0.0025}`},
	} {
		if r := s.do(f.owner, c.method, c.path, c.body); r.Code != http.StatusNotFound {
			t.Errorf("%s %s: %d, want 404 (%s)", c.method, c.path, r.Code, r.Body)
		}
	}
	// and it is untouched in its own project
	if r := s.do(f.owner, "PATCH", "/api/v1/projects/"+other+"/compile/presets/"+preset, `{"name":"Mine now"}`); r.Code != 200 {
		t.Errorf("its own project: %d", r.Code)
	}
}
