package gitmirror_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/mirror"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/server"
	"github.com/crueber/scriobh/internal/testutil"
)

// oldWaitMirrorCurrent is the helper as it was: it returns as soon as Current is true.
func oldWaitMirrorCurrent(t *testing.T, m *mirror.Mirror, projectID string) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if cur, err := m.Current(context.Background(), projectID); err != nil {
			t.Fatal(err)
		} else if cur {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the mirror did not become current")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// The early return, made to happen: an event is in the log that the mirror has not read yet (it reads on a wake-up, or every
// 30 seconds), and Current is true all the same, because the mirror is as the database was when it last looked. The old helper
// returns in that state; a push that followed would then meet "the mirror is behind" the moment the event is read. The helper
// now waits for the event to be read and for what it said to be written.
func TestWaitMirrorCurrentDoesNotReturnBeforeTheMirrorHasReadTheEvents(t *testing.T) {
	d := testutil.DB(t)
	cfg := testutil.Config(t, map[string]string{"SCRIOBH_MIRROR_DEBOUNCE_MS": "20"})
	srv, err := server.New(server.Deps{Cfg: cfg, DB: d})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	srv.Mirror.Start(ctx)
	t.Cleanup(func() { cancel(); srv.Mirror.Wait() })
	owner := ops.Principal{UserID: testutil.User(t, d, "writer", "w@test.local"), Role: "writer"}
	out, err := srv.Reg.Call(context.Background(), owner, "project.create", projects.CreateIn{Title: "Waiting"})
	if err != nil {
		t.Fatal(err)
	}
	pid := out.(projects.Item).Item.ID
	if _, err := srv.Reg.Call(context.Background(), owner, "doc.create", docs.CreateIn{ProjectID: pid, Title: "One", Kind: "chapter", Content: "x\n"}); err != nil {
		t.Fatal(err)
	}
	waitMirrorCurrent(t, srv.Mirror, d, pid)

	// An event nobody woke the mirror for: the state the race passes through.
	if _, err := d.Writer().Exec(`INSERT INTO events(type, project_id, payload_json, created_at) VALUES ('doc.updated', ?, '{}', ?)`, pid, db.Now()); err != nil {
		t.Fatal(err)
	}
	var last int64
	_ = d.Read().QueryRow(`SELECT max(seq) FROM events`).Scan(&last)
	if cur, _ := srv.Mirror.Current(context.Background(), pid); !cur || srv.Mirror.Cursor() >= last {
		t.Fatalf("the state to reproduce is not there: current=%v cursor=%d last=%d", cur, srv.Mirror.Cursor(), last)
	}

	// The old helper returns at once, with the event unread: this is the flake.
	var woke atomic.Bool
	oldWaitMirrorCurrent(t, srv.Mirror, pid)
	if woke.Load() || srv.Mirror.Cursor() >= last {
		t.Fatal("the old helper was expected to return before the event was read")
	}

	// The new one waits for the read: the wake-up comes 300 ms later, and the helper returns only after it, with the cursor
	// at the event and the mirror settled.
	go func() {
		time.Sleep(300 * time.Millisecond)
		woke.Store(true)
		srv.Events.Wake()
	}()
	start := time.Now()
	waitMirrorCurrent(t, srv.Mirror, d, pid)
	if !woke.Load() || time.Since(start) < 250*time.Millisecond {
		t.Errorf("the helper returned after %v, before the mirror was told of the event", time.Since(start))
	}
	if srv.Mirror.Cursor() < last {
		t.Errorf("the cursor is %d, the last event %d", srv.Mirror.Cursor(), last)
	}
	if cur, _ := srv.Mirror.Current(context.Background(), pid); !cur || srv.Mirror.Status(pid).Pending {
		t.Errorf("the mirror is not settled when the helper returns: %+v", srv.Mirror.Status(pid))
	}
}
