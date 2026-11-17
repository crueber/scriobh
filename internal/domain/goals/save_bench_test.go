package goals_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/testutil"
)

// BenchmarkDocUpdate is the save path as a person's autosave drives it: an
// edit near the end of a 5,000-word chapter, each save a new revision. The
// statistics hook (RecordSave and what it feeds) is part of what is timed, so
// the number moves if the hook gets slower.
func BenchmarkDocUpdate(b *testing.B) {
	d := testutil.DB(b)
	reg := registry.Build(d, nil, b.TempDir())
	now := time.Date(2026, 3, 2, 10, 0, 0, 0, time.UTC)
	restore := db.SetClock(func() time.Time { return now })
	defer restore()
	id := testutil.User(b, d, "writer", "bench@test.local")
	p := ops.Principal{UserID: id, Role: ops.RoleWriter}
	ctx := context.Background()
	call := func(op string, in any) any {
		out, err := reg.Call(ctx, p, op, in)
		if err != nil {
			b.Fatalf("%s: %v", op, err)
		}
		return out
	}
	pid := call("project.create", projects.CreateIn{Title: "Book"}).(projects.Item).Item.ID
	did := call("doc.create", docs.CreateIn{ProjectID: pid, Title: "Ch"}).(docs.Item).Item.ID
	var base strings.Builder
	for i := 0; i < 5000; i++ {
		fmt.Fprintf(&base, "word%d ", i)
	}
	rev := call("doc.get", docs.DocIn{ID: did}).(docs.Item).Item.Revision
	first := base.String()
	rev = call("doc.update", docs.UpdateIn{ID: did, BaseRevision: rev, Content: &first}).(docs.Item).Item.Revision
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		now = now.Add(4 * time.Second)
		c := first + fmt.Sprintf("tail%d tail%d", i, i+1)
		rev = call("doc.update", docs.UpdateIn{ID: did, BaseRevision: rev, Content: &c}).(docs.Item).Item.Revision
	}
}
