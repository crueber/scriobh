package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// compendium.get names the projects that use a Compendium, to a person who can
// open them. A pinned key reaches one project at most, and a Compendium-pinned
// key none: it gets counts, never names (nor the names of other Compendiums).
func TestPinnedKeysGetCountsNotNames(t *testing.T) {
	pw := newPinWorld(t)
	s := pw.s
	if rec := s.do(pw.w, "POST", "/api/v1/compendiums/"+pw.oneID+"/refs", `{"target_id":"`+pw.twoID+`"}`); rec.Code >= 300 {
		t.Fatalf("%d %s", rec.Code, rec.Body)
	}
	type detail struct {
		Item struct {
			Projects       []map[string]any
			HiddenProjects int `json:"hidden_projects"`
			References     []map[string]any
			HiddenRefs     int `json:"hidden_references"`
		}
	}
	read := func(key string) (detail, string) {
		t.Helper()
		var rec = s.doKey(key, "GET", "/api/v1/compendiums/"+pw.oneID, "")
		if rec.Code != 200 {
			t.Fatalf("compendium.get: %d %s", rec.Code, rec.Body)
		}
		var d detail
		_ = json.Unmarshal(rec.Body.Bytes(), &d)
		return d, rec.Body.String()
	}
	// The person sees both projects by name.
	var mine detail
	_ = json.Unmarshal(s.do(pw.w, "GET", "/api/v1/compendiums/"+pw.oneID, "").Body.Bytes(), &mine)
	if len(mine.Item.Projects) != 2 || len(mine.Item.References) != 1 {
		t.Fatalf("the person's own view: %+v", mine)
	}
	for who, key := range map[string]string{"compendium key": pw.readC, "compendium write key": pw.writeC, "project key": pw.readP, "project write key": pw.writeP} {
		d, body := read(key)
		if len(d.Item.Projects) != 0 || d.Item.HiddenProjects != 2 {
			t.Errorf("%s: projects %v hidden %d: %s", who, d.Item.Projects, d.Item.HiddenProjects, body)
		}
		if len(d.Item.References) != 0 || d.Item.HiddenRefs != 1 {
			t.Errorf("%s: references %v hidden %d", who, d.Item.References, d.Item.HiddenRefs)
		}
		for _, name := range []string{"Pinned Book", "Sibling Book", "Two", pw.proj, pw.other, pw.twoID} {
			if strings.Contains(body, name) {
				t.Errorf("%s: compendium.get names %q: %s", who, name, body)
			}
		}
	}
}
