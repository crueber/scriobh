package comanchor_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/comanchor"
	"github.com/crueber/scriobh/internal/testutil"
)

// A comment that a write has just orphaned on purpose (a split does that to the comments on the text it moves)
// is not looked for again by the re-anchoring of the same transaction; an ordinary save may bring a lost
// comment back when its words and their surroundings return.
func TestAfterWriteKeepingLostDoesNotReattach(t *testing.T) {
	for _, keep := range []bool{false, true} {
		d := testutil.DB(t)
		now := db.Now()
		for _, q := range []string{
			`INSERT INTO users(id, oidc_issuer, oidc_sub, email, display_name, role, created_at, updated_at) VALUES ('usr_1','t','s','a@t','A','writer','` + now + `','` + now + `')`,
			`INSERT INTO projects(id, owner_id, title, slug, created_at, updated_at) VALUES ('prj_1','usr_1','P','p','` + now + `','` + now + `')`,
			`INSERT INTO documents(id, project_id, created_at, updated_at) VALUES ('doc_1','prj_1','` + now + `','` + now + `')`,
		} {
			if _, err := d.Writer().Exec(q); err != nil {
				t.Fatal(err)
			}
		}
		oldContent := "Intro.\n\nThe lighthouse keeper counted every ship that passed.\n"
		newContent := oldContent + "\nA new closing paragraph.\n"
		if _, err := d.Writer().Exec(`INSERT INTO comments(id, project_id, document_id, kind, body, status, anchor_state, anchor_start, anchor_end, anchor_quote, anchor_original, anchor_prefix, anchor_suffix, anchor_revision, created_at, updated_at)
			VALUES ('cmt_1','prj_1','doc_1','comment','b','open','orphaned',0,0,'The lighthouse keeper counted every ship that passed','The lighthouse keeper counted every ship that passed',?,?,1,?,?)`, "\n\n", ".\n", now, now); err != nil {
			t.Fatal(err)
		}
		err := d.WithTx(context.Background(), func(tx *sql.Tx) error {
			var err error
			if keep {
				_, err = comanchor.AfterWriteKeepingLost(tx, "doc_1", oldContent, newContent, 2, now)
			} else {
				_, err = comanchor.AfterWrite(tx, "doc_1", oldContent, newContent, 2, now)
			}
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
		var state string
		if err := d.Read().QueryRow(`SELECT anchor_state FROM comments WHERE id = 'cmt_1'`).Scan(&state); err != nil {
			t.Fatal(err)
		}
		want := "active"
		if keep {
			want = "orphaned"
		}
		if state != want {
			t.Errorf("keep lost %v: state %q, want %q", keep, state, want)
		}
	}
}
