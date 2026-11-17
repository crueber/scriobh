package server

import (
	"testing"
)

// compileFixtures drives the compile ops through the dry-run sweep: the preview
// writes nothing, the preset and contact ops change rows that a preview rolls
// back, and the two exports record a row (and write no file) that a preview
// rolls back as well.
func (f *dryFixture) compileFixtures(t *testing.T) map[string]drive {
	t.Helper()
	base := "/api/v1/projects/" + f.project
	rec := f.srv.do(f.owner, "POST", base+"/compile/presets", `{"name":"Spare preset","base":"uk-manuscript","options":{"font_size":13}}`)
	if rec.Code >= 300 {
		t.Fatalf("fixture preset: %d %s", rec.Code, rec.Body)
	}
	preset := idOf(t, rec.Body.Bytes())
	return map[string]drive{
		"compile.preset.create": {base + "/compile/presets", `{"name":"Previewed preset","base":"epub3"}`, true},
		"compile.preset.update": {base + "/compile/presets/" + preset, `{"name":"Renamed","options":{"font_size":11}}`, true},
		"compile.preset.delete": {base + "/compile/presets/" + preset, "", true},
		"compile.contact.set":   {base + "/compile/contact", `{"email":"someone@example.com","address":"1 Road\nTown"}`, true},
		"compile.preview":       {base + "/compile/preview", `{"preset":"shunn-us","pages":2}`, false},
		"export.compile":        {base + "/export/compile", `{"preset":"epub3"}`, true},
		"export.submission":     {base + "/export/submission", `{"preset":"uk-manuscript"}`, true},
		"export.cover":          {base + "/export/cover", `{"page_count":200,"page_thickness_in":0.0025}`, true},
		"compile.cover_spec":    {base + "/compile/cover-spec", `{"page_count":200,"page_thickness_in":0.0025}`, false},
	}
}
