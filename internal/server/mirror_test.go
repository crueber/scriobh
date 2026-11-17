package server

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// A server that is only built (handler tests) writes nothing in the
// background; one that serves writes the mirror, and finishes what is pending
// when it stops, even with a long debounce.
func TestServeStartsAndDrainsTheMirror(t *testing.T) {
	d := testutil.DB(t)
	cfg := testutil.Config(t, map[string]string{"SCRIOBH_MIRROR_DEBOUNCE_MS": "60000", "SCRIOBH_BACKUP_INTERVAL_HOURS": "0"})
	s, err := New(Deps{Cfg: cfg, DB: d})
	if err != nil {
		t.Fatal(err)
	}
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	p := ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}

	// Built but not serving: an event is not followed by files.
	if _, err := s.Reg.Call(context.Background(), p, "project.create", projects.CreateIn{Title: "Before Serve"}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	if _, err := os.Stat(filepath.Join(cfg.DataDir, "mirror")); !os.IsNotExist(err) {
		t.Fatalf("a server that is not serving touched the mirror: %v", err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()

	// The start-up check (which finds the project that predates Serve) writes
	// whatever it finds at once: a project created while it runs would be written
	// too, so create the one that must wait out the debounce only afterwards.
	select {
	case <-s.Mirror.Started():
	case <-time.After(10 * time.Second):
		t.Fatal("the mirror's start-up check never finished")
	}
	out, err := s.Reg.Call(context.Background(), p, "project.create", projects.CreateIn{Title: "While Serving"})
	if err != nil {
		t.Fatal(err)
	}
	pid := out.(projects.Item).Item.ID
	deadline := time.Now().Add(5 * time.Second)
	for !s.Mirror.Status(pid).Pending {
		if time.Now().After(deadline) {
			t.Fatal("the serving mirror never noticed the project")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// The debounce is a minute, so nothing has been written yet.
	if st := s.Mirror.Status(pid); st.Files != 0 {
		t.Fatalf("written before the debounce ran out: %+v", st)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("Serve did not return")
	}
	m, _ := filepath.Glob(filepath.Join(cfg.DataDir, "mirror", "while-serving--*", "project.json"))
	if len(m) != 1 {
		t.Fatalf("the pending project was not written at shutdown: %v", m)
	}
	// The project that existed before Serve was found by the start-up check.
	m, _ = filepath.Glob(filepath.Join(cfg.DataDir, "mirror", "before-serve--*", "project.json"))
	if len(m) != 1 {
		t.Errorf("the project that predates Serve was not written: %v", m)
	}
}

// The ops are reachable over REST with the right authority.
func TestMirrorAndBackupRoutes(t *testing.T) {
	s := newAuthzServer(t)
	owner := s.newCaller(t, "owner", ops.RoleWriter)
	reader := s.newCaller(t, "reader", ops.RoleReader)
	admin := s.newCaller(t, "admin", ops.RoleAdmin)
	out, err := s.Reg.Call(context.Background(), ops.Principal{UserID: owner.userID, Role: ops.RoleWriter}, "project.create", projects.CreateIn{Title: "Routes"})
	if err != nil {
		t.Fatal(err)
	}
	pid := out.(projects.Item).Item.ID
	s.addMember(t, pid, reader.userID, ops.ProjectReader)

	if rec := s.do(reader, "GET", "/api/v1/projects/"+pid+"/mirror", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"enabled":true`) {
		t.Errorf("mirror status as a reader: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(reader, "POST", "/api/v1/projects/"+pid+"/mirror/rebuild", ""); rec.Code != 403 {
		t.Errorf("rebuild as a reader: %d", rec.Code)
	}
	if rec := s.do(owner, "POST", "/api/v1/projects/"+pid+"/mirror/rebuild", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"in_sync":true`) {
		t.Errorf("rebuild as the owner: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(owner, "GET", "/api/v1/admin/backups", ""); rec.Code != 403 {
		t.Errorf("backup list as a writer: %d", rec.Code)
	}
	rec := s.do(admin, "POST", "/api/v1/admin/backups", "")
	if rec.Code != 201 {
		t.Fatalf("backup create: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(admin, "GET", "/api/v1/admin/backups", ""); rec.Code != 200 || !strings.Contains(rec.Body.String(), `"kind":"manual"`) {
		t.Errorf("backup list: %d %s", rec.Code, rec.Body)
	}
	if rec := s.do(admin, "GET", "/api/v1/admin/backups/..%2F..%2Fetc%2Fpasswd", ""); rec.Code == 200 {
		t.Errorf("a path was accepted as a backup id: %d", rec.Code)
	}
}
