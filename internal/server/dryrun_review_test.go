package server

import (
	"encoding/json"
	"strconv"
	"testing"
)

// reviewFixtures are the dry-run and event-audit requests for the review ops: a comment, three
// suggestions and a share link in the fixture project, which the sweeps then preview (and, for
// the event audit, really run) as the owner.
func (f *dryFixture) reviewFixtures(t *testing.T) map[string]drive {
	t.Helper()
	s := f.srv
	base := "/api/v1/projects/" + f.project
	post := func(path, body string) string {
		rec := s.do(f.owner, "POST", path, body)
		if rec.Code >= 300 {
			t.Fatalf("fixture POST %s: %d %s", path, rec.Code, rec.Body)
		}
		var out struct{ Item struct{ ID string } }
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out.Item.ID
	}
	doc := f.doc
	rev := strconv.Itoa(f.docRev)
	comment := post(base+"/comments", `{"document_id":"`+doc+`","quote":"draft text","body":"fixture comment"}`)
	sug := func(quote, text string) string {
		return post(base+"/suggestions", `{"document_id":"`+doc+`","base_revision":`+rev+`,"changes":[{"quote":"`+quote+`","text":"`+text+`"}]}`)
	}
	accept, reject, withdraw, many := sug("First", "Second"), sug("here", "there"), sug("text", "words"), sug("draft", "final")
	share := post(base+"/shares", `{"label":"fixture","scope":"comment"}`)
	return map[string]drive{
		"comment.create":         {base + "/comments", `{"document_id":"` + doc + `","quote":"First","body":"A new comment"}`, true},
		"comment.update":         {base + "/comments/" + comment, `{"body":"Reworded"}`, true},
		"comment.delete":         {base + "/comments/" + comment, "", true},
		"comment.resolve":        {base + "/comments/" + comment + "/resolve", "", true},
		"comment.reopen":         {base + "/comments/" + comment + "/reopen", "", false},
		"comment.reanchor":       {base + "/comments/" + comment + "/reanchor", `{"quote":"text here"}`, true},
		"review.resolve_all":     {base + "/review/resolve", `{"document_id":"` + doc + `"}`, true},
		"suggestion.create":      {base + "/suggestions", `{"document_id":"` + doc + `","base_revision":` + rev + `,"changes":[{"quote":"First","text":"Third"}]}`, true},
		"suggestion.accept":      {base + "/suggestions/" + accept + "/accept", `{}`, true},
		"suggestion.reject":      {base + "/suggestions/" + reject + "/reject", "", true},
		"suggestion.withdraw":    {base + "/suggestions/" + withdraw + "/withdraw", "", true},
		"suggestion.accept_all":  {base + "/suggestions/accept_all", `{"document_id":"` + doc + `","suggestion_ids":["` + many + `"]}`, true},
		"share.update":           {base + "/shares/" + share, `{"label":"renamed"}`, true},
		"share.revoke":           {base + "/shares/" + share + "/revoke", "", true},
		"notification.mark_read": {"/api/v1/notifications/read", `{"ids":["ntf_00000000000000000000000000"]}`, false},
		"notification.read_all":  {"/api/v1/notifications/read-all", `{}`, false},
	}
}
