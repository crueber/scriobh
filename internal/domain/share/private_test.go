package share

import (
	"context"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/testutil"
)

// What a guest of a link is shown is the threads written through that link. A private ("Only me") row never is, even one that carries the
// link's id (which nothing writes: this is the second line of defence), and neither are its replies.
func TestAGuestIsNeverShownAPrivateRow(t *testing.T) {
	d := testutil.DB(t)
	uid := testutil.User(t, d, "writer", "w@t.local")
	now := db.Now()
	for _, q := range []string{
		`INSERT INTO projects(id, owner_id, title, slug, created_at, updated_at) VALUES ('prj_1','` + uid + `','P','p','` + now + `','` + now + `')`,
		`INSERT INTO documents(id, project_id, created_at, updated_at) VALUES ('doc_1','prj_1','` + now + `','` + now + `')`,
		`INSERT INTO comments(id, project_id, document_id, share_id, guest_name, body, visibility, created_at, updated_at) VALUES ('cmt_g','prj_1','doc_1','shl_1','Gus','GUEST WORDS','shared','` + now + `','` + now + `')`,
		`INSERT INTO comments(id, project_id, document_id, share_id, guest_name, body, visibility, created_at, updated_at) VALUES ('cmt_p','prj_1','doc_1','shl_1','Gus','PRIVATE WORDS','private','` + now + `','` + now + `')`,
		`INSERT INTO comments(id, project_id, document_id, parent_id, author_id, body, visibility, created_at, updated_at) VALUES ('cmt_pr','prj_1','doc_1','cmt_p','` + uid + `','PRIVATE REPLY','private','` + now + `','` + now + `')`,
	} {
		if _, err := d.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	// Each of the two filters is tried alone: the trigger that keeps a reply in its thread's audience is taken away, so that a private reply under
	// a guest's shared thread, and a shared reply under a private row, can be made; neither may show.
	if _, err := d.Writer().Exec(`DROP TRIGGER comments_reply_visibility`); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO comments(id, project_id, document_id, parent_id, author_id, body, visibility, created_at, updated_at) VALUES ('cmt_gp','prj_1','doc_1','cmt_g','` + uid + `','PRIVATE UNDER SHARED','private','` + now + `','` + now + `')`,
		`INSERT INTO comments(id, project_id, document_id, parent_id, share_id, guest_name, body, visibility, created_at, updated_at) VALUES ('cmt_ps','prj_1','doc_1','cmt_p','shl_1','Gus','SHARED UNDER PRIVATE','shared','` + now + `','` + now + `')`,
	} {
		if _, err := d.Writer().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Threads(context.Background(), d, Link{ID: "shl_1"}, "doc_1", "Some text.")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != "cmt_g" {
		t.Fatalf("threads: %+v", got)
	}
	for _, th := range got {
		for _, c := range th.Comments {
			if c.Body == "PRIVATE WORDS" || c.Body == "PRIVATE REPLY" || c.Body == "PRIVATE UNDER SHARED" || c.Body == "SHARED UNDER PRIVATE" {
				t.Errorf("a private row is on the link: %+v", c)
			}
		}
	}
}
