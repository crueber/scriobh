package server

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/goals"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/revisions"
	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
)

// TestGenerateReleaseFixture writes a database holding what a release's ops
// produce: documents, revisions, wiki links, goal and session rows, a Git
// remote and an import source. It runs only when GEN_FIXTURE names the output
// file; each release commits one so the next release's migrations are tested
// against data the last one wrote:
//
//	GEN_FIXTURE=$PWD/internal/db/testdata/v0.2.db go test ./internal/server -run GenerateReleaseFixture
func TestGenerateReleaseFixture(t *testing.T) {
	out := os.Getenv("GEN_FIXTURE")
	if out == "" {
		t.Skip("set GEN_FIXTURE to write a release fixture")
	}
	base := time.Date(2026, 11, 10, 9, 0, 0, 0, time.UTC)
	now := base
	restore := db.SetClock(func() time.Time { return now })
	defer restore()
	adv := func(d time.Duration) { now = now.Add(d) }

	s := newAuthzServer(t)
	owner := s.newCaller(t, "writer", ops.RoleWriter)
	p := ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}
	ctx := context.Background()
	call := func(op string, in any) any {
		t.Helper()
		v, err := s.Reg.Call(ctx, p, op, in)
		if err != nil {
			t.Fatalf("%s: %v", op, err)
		}
		return v
	}

	pid := call("project.create", projects.CreateIn{Title: "The Salt Road", Kind: "novel"}).(projects.Item).Item.ID
	ch1 := call("doc.create", docs.CreateIn{ProjectID: pid, Title: "The Last Ferry", Content: "The ferry left at four. See [[Harbour Notes]] and [[Nowhere]]."}).(docs.Item).Item
	call("doc.create", docs.CreateIn{ProjectID: pid, Title: "Harbour Notes", Kind: "note", Content: "Tides and gulls."})
	call("goal.set", goals.SetIn{ProjectID: pid, Kind: "daily", TargetWords: 300})

	// A few saves ten minutes apart, so revisions and a writing session exist.
	cur := ch1
	for i, text := range []string{
		"The ferry left at four, and Máire waited. See [[Harbour Notes]] and [[Nowhere]].",
		"The ferry left at five, and Máire waited on the pier with the letter. See [[Harbour Notes]].",
		"The ferry left at five, and Máire waited on the pier with the letter in her coat. See [[Harbour Notes]] twice: [[Harbour Notes]].",
	} {
		adv(10 * time.Minute)
		text := text
		cur = call("doc.update", docs.UpdateIn{ID: ch1.ID, BaseRevision: cur.Revision, Content: &text}).(docs.Item).Item
		_ = i
	}
	call("revision.snapshot", revisions.SnapshotIn{ID: ch1.ID, Label: "First draft", Note: "kept for the fixture"})

	// An import source and its documents.
	files := []imports.File{{Path: "01-opening.md", Content: "# Opening\n\nSalt wind.\n"}, {Path: "02-closing.md", Content: "# Closing\n\nThe last light.\n"}}
	pv := call("import.preview", imports.PreviewIn{Name: "Imported Notes", Files: files}).(imports.PreviewOut)
	call("import.apply", imports.ApplyIn{Token: pv.Token, Files: files})

	// A Git remote row, as the setup form leaves it before any push.
	if _, err := s.DB.Writer().Exec(`INSERT INTO git_remotes(id, project_id, name, url, branch, auth_kind, created_at, updated_at)
		VALUES ('grm_fixture', ?, 'Backup', 'https://git.example.test/writer/salt-road.git', 'main', 'https_token', ?, ?)`, pid, db.Now(), db.Now()); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(out)
	if _, err := s.DB.Writer().Exec("VACUUM INTO ?", out); err != nil {
		t.Fatal(err)
	}
}
