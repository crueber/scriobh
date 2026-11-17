package server

import (
	"context"
	"testing"

	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// A preview runs to its end even if the request that asked for it is gone:
// database/sql must not tear the one shared connection down halfway.
func TestDryRunIgnoresCancellation(t *testing.T) {
	s := newAuthzServer(t)
	w := s.newCaller(t, "writer", ops.RoleWriter)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := s.Reg.CallDry(ctx, ops.Principal{UserID: w.userID, Role: ops.RoleWriter}, "project.create", projects.CreateIn{Title: "Cancelled"})
	if err != nil || res.Changes == 0 {
		t.Fatalf("a cancelled preview: %v %+v", err, res)
	}
	// The writer is free afterwards.
	if _, err := s.Reg.Call(context.Background(), ops.Principal{UserID: w.userID, Role: ops.RoleWriter}, "project.create", projects.CreateIn{Title: "After"}); err != nil {
		t.Fatalf("writer after a cancelled preview: %v", err)
	}
}
