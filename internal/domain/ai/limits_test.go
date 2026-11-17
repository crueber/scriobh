package ai_test

import (
	"fmt"
	"strings"
	"testing"

	lib "github.com/crueber/scriobh/internal/ai"
	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/ops"
)

// A request for many large pieces is refused as the total is passed, not after every one has
// been read and held in memory.
func TestManyLargeChipsAreRefusedEarly(t *testing.T) {
	e := newEnv(t, nil)
	e.ready()
	body := strings.Repeat("A line of research about the tides. ", 3000) // about 100 KB
	var ids []string
	for i := 0; i < 30; i++ {
		ids = append(ids, e.call(e.owner, "project.research.create", research.CreateIn{ID: e.project, Title: fmt.Sprintf("Note %d", i), Body: body}).(research.Item).Item.ID)
	}
	in := e.freeform("x")
	in.Chips = []aicore.ChipSpec{{Kind: lib.ChipResearch, IDs: ids}, {Kind: lib.ChipResearch, IDs: ids}}
	_, err := e.try(e.editor, "ai.context.preview", in)
	wantCode(t, err, ops.CodeTooLarge)
	if !strings.Contains(err.Message, "most one request sends") {
		t.Errorf("%q", err.Message)
	}
	// The same note asked for again and again is one chip, not many.
	var again []aicore.ChipSpec
	for i := 0; i < 30; i++ {
		again = append(again, aicore.ChipSpec{Kind: lib.ChipResearch, IDs: ids[:1]})
	}
	in.Chips = again
	if _, err := e.try(e.editor, "ai.context.preview", in); err != nil {
		t.Errorf("%v", err)
	}
}

// A preview builds a whole request from the project; it is limited, a little more generously than a run.
func TestPreviewsAreRateLimited(t *testing.T) {
	e := newEnv(t, func(o *aicore.Options) { o.Config.RatePerMinute = 3 })
	e.ready()
	limited := 0
	for i := 0; i < 60; i++ {
		if _, err := e.try(e.editor, "ai.context.preview", e.freeform("x")); err != nil {
			if err.Code != ops.CodeRateLimited || err.RetryAfterSeconds < 1 {
				t.Fatalf("%v", err)
			}
			limited++
		}
	}
	if limited < 40 {
		t.Fatalf("only %d of 60 previews were refused", limited)
	}
	// A malformed one does not spend the allowance, and someone else has their own.
	for i := 0; i < 20; i++ {
		_, err := e.try(e.owner, "ai.context.preview", aicore.RunIn{ProjectID: e.project})
		wantCode(t, err, ops.CodeInvalid)
	}
	if _, err := e.try(e.owner, "ai.context.preview", e.freeform("x")); err != nil {
		t.Errorf("another person's preview: %v", err)
	}
}
