package server

import (
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/ops"
)

// Review events (a comment was written, a thread resolved, a suggestion made) go to the people who can
// read comments. A project reader is on the stream (they hear the text change) and hears nothing of review.
// What a reviewer hears is ids only: never the text of a comment, a name or a quote.
func TestReviewEventsReachReviewersAndNotReaders(t *testing.T) {
	f := newSSEFixture(t, nil)
	reviewer := f.s.newCaller(t, "rev", ops.RoleReader)
	f.s.addMember(t, f.pid, reviewer.userID, ops.ProjectReviewer)
	f.save(t, "Some text for a comment to hold.", 1)
	rc, code := openStream(t, f.ts.URL, reviewer, "", "project="+f.pid, "")
	if code != 200 {
		t.Fatal(code)
	}
	rd, code := openStream(t, f.ts.URL, f.reader, "", "project="+f.pid, "")
	if code != 200 {
		t.Fatal(code)
	}
	rc.nextEvent() // hello
	rd.nextEvent()
	rec := f.s.do(f.owner, "POST", "/api/v1/projects/"+f.pid+"/comments", `{"document_id":"`+f.did+`","quote":"text for a comment","body":"STREAMSECRET remark"}`)
	if rec.Code != 201 {
		t.Fatalf("comment: %d %s", rec.Code, rec.Body)
	}
	e := rc.nextEvent()
	if e.Event != "comment.created" || strings.Contains(e.Data, "STREAMSECRET") || strings.Contains(e.Data, "text for a comment") {
		t.Fatalf("what a reviewer hears: %+v", e)
	}
	if !strings.Contains(e.Data, `"comment_id"`) || !strings.Contains(e.Data, `"thread_id"`) {
		t.Errorf("the event names no comment: %s", e.Data)
	}
	// the reader hears the next text change, and nothing of the comment before it
	f.save(t, "Some text for a comment to hold, changed.", 2)
	time.Sleep(20 * time.Millisecond)
	got := rd.nextEvent()
	if got.Event != "doc.updated" {
		t.Errorf("a reader heard %q (%s) where the first thing should be the text change", got.Event, got.Data)
	}
}
