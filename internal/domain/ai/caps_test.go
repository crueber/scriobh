package ai_test

import (
	"testing"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/ops"
)

func hasWarning(st aicore.StatusOut, code string) bool {
	for _, w := range st.Warnings {
		if w.Code == code {
			return true
		}
	}
	return false
}

// No cap anywhere is a state an administrator is told about, and only an administrator.
func TestStatusWarnsWhenNothingIsCapped(t *testing.T) {
	e := newEnv(t, nil) // the test configuration sets no default caps
	e.addProvider(newFake(t, nil), "Main", false)
	st := e.call(e.admin, "ai.status", struct{}{}).(aicore.StatusOut)
	if !hasWarning(st, "no_caps") {
		t.Fatalf("no warning: %+v", st.Warnings)
	}
	if st := e.call(e.editor, "ai.status", struct{}{}).(aicore.StatusOut); hasWarning(st, "no_caps") {
		t.Error("a person who cannot do anything about it was warned")
	}
	e.setBudget("instance", "", 0, 1_000_000)
	if st := e.call(e.admin, "ai.status", struct{}{}).(aicore.StatusOut); hasWarning(st, "no_caps") {
		t.Errorf("an instance cap is set: %+v", st.Warnings)
	}
	e.setBudget("instance", "", 0, 0)
	e2 := newEnv(t, func(o *aicore.Options) { o.Config.DefaultUserTokens = 10_000 })
	e2.addProvider(newFake(t, nil), "Main", false)
	if st := e2.call(e2.admin, "ai.status", struct{}{}).(aicore.StatusOut); hasWarning(st, "no_caps") {
		t.Errorf("a default per-person cap is set: %+v", st.Warnings)
	}
}

// The default per-person cap applies to someone with no budget row of their own.
func TestTheDefaultCapAppliesToEveryone(t *testing.T) {
	e := newEnv(t, func(o *aicore.Options) { o.Config.DefaultUserTokens = 4000 })
	f := usageFake(t, 100, 50)
	e.addProvider(f, "Main", false)
	e.enable("ask", nil)
	in := e.freeform("Again.")
	pv := e.preview(e.editor, in)
	in.Nonce, in.PayloadHash = pv.Nonce, pv.PayloadHash
	ok := 0
	var last *ops.Error
	for i := 0; i < 100 && last == nil; i++ {
		if _, err := e.try(e.editor, "ai.run", in); err != nil {
			last = err
		} else {
			ok++
		}
	}
	if last == nil || last.Data["ai_code"] != "budget_exceeded" || ok == 0 || ok >= 100 {
		t.Fatalf("ok %d, %v", ok, last)
	}
	if tokens, _ := e.spent(); tokens > 4000 {
		t.Errorf("spent %d against a cap of 4000", tokens)
	}
}
