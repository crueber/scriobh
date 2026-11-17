package aiwork_test

import (
	"testing"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/aiwork"
	"github.com/crueber/scriobh/internal/ops"
)

// With AI switched off by an administrator, what sends nothing still works: a
// person can read and delete their kept chats, set and lift never-send flags,
// and see their use and the run log. Everything that could send stays refused.
func TestManagementWorksWithTheKillSwitchOn(t *testing.T) {
	e := newEnv(t)
	run := e.mustRun(e.editor, e.rewrite("The harbour was quiet.", "shorter"))
	ch := e.call(e.editor, "ai.chat.create", aiwork.ChatCreateIn{ProjectID: e.project, Title: "Kept"}).(aiwork.ChatItem).Item
	send := aiwork.ChatSendIn{ProjectID: e.project, ChatID: ch.ID, Message: "A question?"}
	pv := e.call(e.editor, "ai.chat.preview", send).(aicore.PreviewOut).Item
	send.Nonce, send.PayloadHash = pv.Nonce, pv.PayloadHash
	ask := e.rewrite("The harbour was quiet.", "shorter")

	e.call(e.admin, "ai.settings.set", aicore.SettingsSetIn{Enabled: ptr(false)})
	before := len(e.fake.requests())

	// refused: everything that could send, and starting or adding to a chat
	refused := map[string]func() *ops.Error{
		"ai.chat.create": func() *ops.Error {
			_, err := e.try(e.editor, "ai.chat.create", aiwork.ChatCreateIn{ProjectID: e.project})
			return err
		},
		"ai.chat.append": func() *ops.Error {
			_, err := e.try(e.editor, "ai.chat.append", aiwork.ChatAppendIn{ProjectID: e.project, ChatID: ch.ID, RunID: run.RunID, Message: "hi", Answer: "An answer."})
			return err
		},
		"ai.chat.preview": func() *ops.Error {
			_, err := e.try(e.editor, "ai.chat.preview", aiwork.ChatSendIn{ProjectID: e.project, ChatID: ch.ID, Message: "Another?"})
			return err
		},
		"ai.chat.send": func() *ops.Error {
			_, err := e.try(e.editor, "ai.chat.send", send)
			return err
		},
		"ai.context.preview": func() *ops.Error {
			_, err := e.try(e.editor, "ai.context.preview", ask)
			return err
		},
		"ai.run": func() *ops.Error {
			_, err := e.try(e.editor, "ai.run", ask)
			return err
		},
		"ai.policy.set": func() *ops.Error {
			mode := "off"
			_, err := e.try(e.owner, "ai.policy.set", aicore.PolicySetIn{ProjectID: e.project, Mode: &mode})
			return err
		},
	}
	for name, f := range refused {
		if err := f(); err == nil || err.Code != ops.CodeConflict {
			t.Errorf("%s with AI off: want a conflict, got %v", name, err)
		}
	}

	// allowed: reading and deleting what is kept, flags, use
	if l := e.call(e.editor, "ai.chat.list", aiwork.ChatListIn{ProjectID: e.project}).(aiwork.ChatListOut); len(l.Items) != 1 || l.Items[0].ID != ch.ID {
		t.Fatalf("chat list with AI off: %+v", l)
	}
	if g := e.call(e.editor, "ai.chat.get", aiwork.ChatRefIn{ProjectID: e.project, ChatID: ch.ID}).(aiwork.ChatItem); g.Item.Title != "Kept" {
		t.Fatalf("chat get with AI off: %+v", g)
	}
	e.call(e.editor, "ai.exclude.set", aicore.ExcludeSetIn{ProjectID: e.project, Kind: "document", ID: e.doc, Exclude: true})
	if l := e.call(e.editor, "ai.exclude.list", aicore.ExcludeListIn{ProjectID: e.project}).(aicore.ExcludeListOut); len(l.Items) != 1 || l.Items[0].ID != e.doc {
		t.Fatalf("exclude list with AI off: %+v", l)
	}
	e.call(e.editor, "ai.exclude.set", aicore.ExcludeSetIn{ProjectID: e.project, Kind: "document", ID: e.doc, Exclude: false})
	if l := e.call(e.editor, "ai.exclude.list", aicore.ExcludeListIn{ProjectID: e.project}).(aicore.ExcludeListOut); len(l.Items) != 0 {
		t.Fatalf("the flag was not lifted with AI off: %+v", l)
	}
	if u := e.call(e.editor, "ai.usage.get", aicore.UsageIn{}).(aicore.UsageOut); u.Totals.Runs < 1 {
		t.Fatalf("usage with AI off: %+v", u.Totals)
	}
	if u := e.call(e.owner, "ai.usage.project", aicore.UsageProjectIn{ProjectID: e.project}).(aicore.UsageProjectOut); u.Totals.Runs < 1 {
		t.Fatalf("project usage with AI off: %+v", u.Totals)
	}
	e.call(e.editor, "ai.budget.get", aicore.BudgetGetIn{ProjectID: e.project})
	if l := e.call(e.editor, "ai.run.list", aicore.RunListIn{ProjectID: e.project}).(aicore.RunListOut); len(l.Items) < 1 {
		t.Fatalf("run log with AI off: %+v", l)
	}
	e.call(e.editor, "ai.chat.delete", aiwork.ChatRefIn{ProjectID: e.project, ChatID: ch.ID})
	if n := e.count(`SELECT count(*) FROM ai_chats`) + e.count(`SELECT count(*) FROM ai_chat_turns`); n != 0 {
		t.Fatalf("the chat was not deleted with AI off: %d rows", n)
	}

	// the owner-only and agents rules still hold with AI off
	_, err := e.try(e.editor, "ai.usage.project", aicore.UsageProjectIn{ProjectID: e.project})
	if err == nil {
		t.Error("an editor read the project's usage")
	}
	_, err = e.try(e.key(e.editor), "ai.chat.list", aiwork.ChatListIn{ProjectID: e.project})
	wantCode(t, err, ops.CodeForbidden)

	if n := len(e.fake.requests()); n != before {
		t.Fatalf("a provider was contacted with AI off: %d requests", n-before)
	}
}
