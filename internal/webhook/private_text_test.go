package webhook

import (
	"context"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/testutil"
)

// A webhook that asked for comment text gets the words of a shared comment and never those of a private ("Only me") one, whatever event names it:
// the text is read from the table at send time, and the read refuses a private row.
func TestWithCommentTextRefusesAPrivateRow(t *testing.T) {
	d := testutil.DB(t)
	uid := testutil.User(t, d, "writer", "w@t.local")
	now := db.Now()
	for _, q := range []string{
		`INSERT INTO projects(id, owner_id, title, slug, created_at, updated_at) VALUES ('prj_1','` + uid + `','P','p','` + now + `','` + now + `')`,
		`INSERT INTO comments(id, project_id, author_id, body, visibility, created_at, updated_at) VALUES ('cmt_shared','prj_1','` + uid + `','SHARED WORDS','shared','` + now + `','` + now + `')`,
		`INSERT INTO comments(id, project_id, author_id, body, visibility, created_at, updated_at) VALUES ('cmt_private','prj_1','` + uid + `','PRIVATE WORDS','private','` + now + `','` + now + `')`,
	} {
		if _, err := d.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	s := &Service{o: Options{DB: d}}
	body := func(id string) string {
		return `{"id":"x","type":"comment.created","project_id":"prj_1","data":{"comment_id":"` + id + `"}}`
	}
	if got := string(s.withCommentText(context.Background(), []byte(body("cmt_shared")))); !strings.Contains(got, "SHARED WORDS") {
		t.Errorf("a shared comment's words are not sent: %s", got)
	}
	if got := string(s.withCommentText(context.Background(), []byte(body("cmt_private")))); strings.Contains(got, "PRIVATE WORDS") {
		t.Errorf("a private comment's words were put in a webhook body: %s", got)
	}
}
