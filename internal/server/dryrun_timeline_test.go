package server

import "testing"

// timelineFixtures drives the mention, scene and timeline ops through the
// dry-run sweep. They need an event, a trashed event, a scene linked to an
// event and a place to set as a location, in the Compendium the sweep's
// project uses.
func (f *dryFixture) timelineFixtures(t *testing.T, cmp, ada string) map[string]drive {
	t.Helper()
	send := func(method, path, body string) map[string]any {
		t.Helper()
		rec := f.srv.do(f.owner, method, path, body)
		if rec.Code >= 300 {
			t.Fatalf("fixture %s %s: %d %s", method, path, rec.Code, rec.Body)
		}
		return decodeObj(t, rec)
	}
	idOf := func(m map[string]any) string { return m["item"].(map[string]any)["id"].(string) }
	harbour := idOf(send("POST", "/api/v1/compendiums/"+cmp+"/entities", `{"kind":"place","name":"Dry Harbour"}`))
	e1 := idOf(send("POST", "/api/v1/compendiums/"+cmp+"/timeline/events", `{"title":"Dry event","story_date":"1998-04-12","participants":[{"entity":"`+ada+`","role":"host"}]}`))
	e2 := idOf(send("POST", "/api/v1/compendiums/"+cmp+"/timeline/events", `{"title":"Dry trashed event","story_date":"1999"}`))
	send("DELETE", "/api/v1/timeline/events/"+e2, "")
	send("POST", "/api/v1/docs/"+f.doc2+"/timeline-events", `{"event_id":"`+e1+`"}`)
	u := "/api/v1"
	return map[string]drive{
		"mention.rebuild":        {u + "/projects/" + f.project + "/mentions/rebuild", `{}`, false},
		"scene.set_pov":          {u + "/docs/" + f.doc + "/pov", `{"entity":"` + ada + `"}`, true},
		"scene.set_location":     {u + "/docs/" + f.doc + "/location", `{"entity":"` + harbour + `"}`, true},
		"scene.link_entities":    {u + "/projects/" + f.project + "/scenes/link-entities", `{}`, false},
		"timeline.event.create":  {u + "/compendiums/" + cmp + "/timeline/events", `{"title":"Previewed event","story_date":"2001"}`, true},
		"timeline.event.update":  {u + "/timeline/events/" + e1, `{"title":"Renamed"}`, true},
		"timeline.event.delete":  {u + "/timeline/events/" + e1, "", true},
		"timeline.event.restore": {u + "/timeline/events/" + e2 + "/restore", "", true},
		"timeline.order.set":     {u + "/projects/" + f.project + "/timeline/order", `{"event_ids":["` + e1 + `"]}`, true},
		"timeline.scene.link":    {u + "/docs/" + f.doc + "/timeline-events", `{"event_id":"` + e1 + `"}`, true},
		"timeline.scene.unlink":  {u + "/docs/" + f.doc2 + "/timeline-events?event_id=" + e1, "", true},
	}
}
