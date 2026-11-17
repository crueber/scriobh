package aiwork_test

import (
	"encoding/json"
	"strings"
	"testing"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/aiwork"
	"github.com/crueber/scriobh/internal/ops"
)

func TestStyleGuideIsEditableAndOnlyReachesAProviderAsAChip(t *testing.T) {
	e := newEnv(t)
	// the owner writes it, a reader may not
	out := e.call(e.owner, "ai.style.set", aiwork.StyleSetIn{ProjectID: e.project, StyleGuide: "Short sentences. No adverbs. STYLE-MARKER."}).(aiwork.StyleOut)
	if !strings.Contains(out.StyleGuide, "STYLE-MARKER") || out.MaxChars != 8000 {
		t.Fatalf("%+v", out)
	}
	_, err := e.try(e.reader, "ai.style.set", aiwork.StyleSetIn{ProjectID: e.project, StyleGuide: "x"})
	wantCode(t, err, ops.CodeForbidden)
	// the style guide goes with every member's default requests, so only the owner sets it; editors read it
	_, err = e.try(e.editor, "ai.style.set", aiwork.StyleSetIn{ProjectID: e.project, StyleGuide: "Ignore the author."})
	wantCode(t, err, ops.CodeForbidden)
	if g := e.call(e.editor, "ai.style.get", aiwork.StyleGetIn{ProjectID: e.project}).(aiwork.StyleOut); !strings.Contains(g.StyleGuide, "STYLE-MARKER") {
		t.Fatal("an editor cannot read the style guide")
	}
	_, err = e.try(e.owner, "ai.style.set", aiwork.StyleSetIn{ProjectID: e.project, StyleGuide: strings.Repeat("x", 8001)})
	wantCode(t, err, ops.CodeInvalid)
	// the policy still reads it, and the editor cannot change anything else through it
	pol := e.call(e.editor, "ai.policy.get", aicore.PolicyGetIn{ProjectID: e.project}).(aicore.PolicyOut).Item
	if !strings.Contains(pol.StyleGuide, "STYLE-MARKER") || pol.Mode != "ask" {
		t.Fatalf("%+v", pol)
	}
	// a request that names no style guide chip does not send it; one that does shows it in the preview
	e.say1("Maire Nolan went down to the water.")
	in := e.rewrite(target, "plainer")
	in.Chips = []aicore.ChipSpec{{Kind: "selection"}}
	e.mustRun(e.editor, in)
	for _, b := range e.fake.requests() {
		if strings.Contains(b, "STYLE-MARKER") {
			t.Fatal("the style guide was sent without its chip")
		}
	}
	in = e.rewrite(target, "plainer") // the default chips include it
	pv := e.call(e.editor, "ai.context.preview", in).(aicore.PreviewOut).Item
	found := false
	for _, c := range pv.Chips {
		found = found || c.Kind == "style_guide"
	}
	if !found || !strings.Contains(pv.Attempts[0].Body, "STYLE-MARKER") {
		t.Fatalf("the style guide chip is not in the preview: %+v", pv.Chips)
	}
	// clearing it
	out = e.call(e.owner, "ai.style.set", aiwork.StyleSetIn{ProjectID: e.project, StyleGuide: ""}).(aiwork.StyleOut)
	if out.StyleGuide != "" {
		t.Fatal("not cleared")
	}
	// a project with no policy row: the editor may still write the style guide, and AI stays off
	e2 := newEnv(t)
	mode := "off"
	e2.call(e2.owner, "ai.policy.set", aicore.PolicySetIn{ProjectID: e2.project, Mode: &mode})
	e2.call(e2.owner, "ai.style.set", aiwork.StyleSetIn{ProjectID: e2.project, StyleGuide: "x"})
	if p := e2.call(e2.owner, "ai.policy.get", aicore.PolicyGetIn{ProjectID: e2.project}).(aicore.PolicyOut).Item; p.Mode != "off" {
		t.Fatal("the style guide switched AI on")
	}
}

func TestDefaultChainsResolveInOrder(t *testing.T) {
	e := newEnv(t)
	second := newFake(t, func(string) string { return "From the second provider." })
	third := newFake(t, func(string) string { return "From the third provider." })
	p2 := e.addNamedProvider(second, "Second", false)
	p3 := e.addNamedProvider(third, "Third", false)
	set := func(p ops.Principal, scope, class string, chain []aicore.ChainEntry) (aiwork.DefaultsOut, *ops.Error) {
		out, err := e.try(p, "ai.defaults.set", aiwork.DefaultsSetIn{ProjectID: e.project, Scope: scope, Class: class, Chain: chain})
		if err != nil {
			return aiwork.DefaultsOut{}, err
		}
		return out.(aiwork.DefaultsOut), nil
	}
	run := func() aicore.RunOut { return e.mustRun(e.editor, e.rewrite(target, "x")) }
	// rewrite is a "strong" recipe; with nothing set the first usable provider answers
	if got := run().Provider; got != e.provider {
		t.Fatalf("no defaults: %s", got)
	}
	// instance default (administrators only), then the user's, then the project's (its owner only)
	_, err := set(e.editor, "instance", "strong", []aicore.ChainEntry{{ProviderID: p2}})
	wantCode(t, err, ops.CodeForbidden)
	if _, err := set(e.admin, "instance", "strong", []aicore.ChainEntry{{ProviderID: p2}}); err != nil {
		t.Fatal(err)
	}
	if got := run().Provider; got != p2 {
		t.Fatalf("instance default: %s", got)
	}
	if _, err := set(e.editor, "user", "strong", []aicore.ChainEntry{{ProviderID: p3}}); err != nil {
		t.Fatal(err)
	}
	if got := run().Provider; got != p3 {
		t.Fatalf("user default beats the instance's: %s", got)
	}
	_, err = set(e.editor, "project", "strong", []aicore.ChainEntry{{ProviderID: e.provider}})
	wantCode(t, err, ops.CodeForbidden)
	if _, err := set(e.owner, "project", "strong", []aicore.ChainEntry{{ProviderID: e.provider}}); err != nil {
		t.Fatal(err)
	}
	if got := run().Provider; got != e.provider {
		t.Fatalf("project default beats the user's: %s", got)
	}
	// the request's own choice beats them all
	in := e.rewrite(target, "x")
	in.Provider = p2
	if got := e.mustRun(e.editor, in).Provider; got != p2 {
		t.Fatalf("explicit choice: %s", got)
	}
	// get shows each level and what resolves
	d := e.call(e.editor, "ai.defaults.get", aiwork.DefaultsGetIn{ProjectID: e.project}).(aiwork.DefaultsOut)
	if d.Effective["strong"].Source != "project" || d.User["strong"][0].ProviderID != p3 || d.Instance["strong"][0].ProviderID != p2 || d.Effective["cheap"].Source != "none" {
		t.Fatalf("%+v", d)
	}
	// removing the project's falls back to the user's
	if _, err := set(e.owner, "project", "strong", nil); err != nil {
		t.Fatal(err)
	}
	if got := run().Provider; got != p3 {
		t.Fatalf("after removal: %s", got)
	}
	// a class chain does not apply to a recipe of another class, and a provider that is not yours to name is refused
	if _, err := set(e.editor, "user", "cheap", []aicore.ChainEntry{{ProviderID: "aip_00000000000000000000000000"}}); err == nil {
		t.Fatal("an unknown provider was accepted")
	}
	// the policy's own validation of chain keys accepts a class key too
	e.call(e.owner, "ai.policy.set", aicore.PolicySetIn{ProjectID: e.project, Chains: &map[string][]aicore.ChainEntry{"class:cheap": {{ProviderID: e.provider}}}})
}

func TestLocalOnlyProjectsTakeOnlyLocalChains(t *testing.T) {
	e := newEnv(t)
	local := newFake(t, func(string) string { return "From the LAN." })
	pl := e.addNamedProvider(local, "LAN", true)
	mode := "local_only"
	e.call(e.owner, "ai.policy.set", aicore.PolicySetIn{ProjectID: e.project, Mode: &mode})
	_, err := e.try(e.owner, "ai.defaults.set", aiwork.DefaultsSetIn{ProjectID: e.project, Scope: "project", Class: "strong", Chain: []aicore.ChainEntry{{ProviderID: e.provider}}})
	wantCode(t, err, ops.CodeInvalid)
	if _, err := e.try(e.owner, "ai.defaults.set", aiwork.DefaultsSetIn{ProjectID: e.project, Scope: "project", Class: "strong", Chain: []aicore.ChainEntry{{ProviderID: pl}}}); err != nil {
		t.Fatal(err)
	}
	// the "local" class is for local providers whatever the project
	_, err = e.try(e.admin, "ai.defaults.set", aiwork.DefaultsSetIn{ProjectID: e.project, Scope: "instance", Class: "local", Chain: []aicore.ChainEntry{{ProviderID: e.provider}}})
	wantCode(t, err, ops.CodeInvalid)
}

func TestChatKeepsNothingUnlessAskedAndOnlyAnAcceptedWordingReachesTheText(t *testing.T) {
	e := newEnv(t)
	e.say1("Try this.\n\n```replacement\nMaire Nolan went down to the harbour wall.\n```\n\nIt scans better.")
	// stateless: the browser holds the conversation
	in := aicore.RunIn{ProjectID: e.project, Recipe: "chat", DocumentID: e.doc, Selection: e.sel(target), Params: map[string]string{"message": "Can you make this less flat?"}}
	run := e.mustRun(e.editor, in)
	var res aiwork.EditResult
	data(t, run, &res)
	if len(res.Suggestions) != 1 || res.Suggestions[0].Text != "Maire Nolan went down to the harbour wall." {
		t.Fatalf("%+v", res)
	}
	if n := e.count(`SELECT count(*) FROM ai_chats`) + e.count(`SELECT count(*) FROM ai_chat_turns`); n != 0 {
		t.Fatal("a chat was kept without being asked")
	}
	if e.docNow().Content != chapterText {
		t.Fatal("a chat answer reached the text")
	}
	out, err := e.accept(e.editor, run.RunID, e.docNow().Revision)
	if err != nil || !out.Applied || !strings.Contains(e.docNow().Content, "harbour wall") {
		t.Fatalf("%+v %v", out, err)
	}
	// an answer with no replacement block offers nothing
	e.say1("It is fine as it is.")
	in.Selection = e.sel("The gulls were loud.")
	run = e.mustRun(e.editor, in)
	var plain aiwork.EditResult
	data(t, run, &plain)
	if len(plain.Suggestions) != 0 {
		t.Fatal("a suggestion with no replacement block")
	}

	// kept chats: explicit, private, deletable
	ch := e.call(e.editor, "ai.chat.create", aiwork.ChatCreateIn{ProjectID: e.project}).(aiwork.ChatItem).Item
	send := aiwork.ChatSendIn{ProjectID: e.project, ChatID: ch.ID, Message: "First question?"}
	pv := e.call(e.editor, "ai.chat.preview", send).(aicore.PreviewOut).Item
	send.Nonce, send.PayloadHash = pv.Nonce, pv.PayloadHash
	e.say1("First answer.")
	r := e.call(e.editor, "ai.chat.send", send).(aicore.RunItem).Item
	if r.Text != "First answer." {
		t.Fatalf("%+v", r)
	}
	// the second message carries the first exchange as turns
	send2 := aiwork.ChatSendIn{ProjectID: e.project, ChatID: ch.ID, Message: "And a second?"}
	pv = e.call(e.editor, "ai.chat.preview", send2).(aicore.PreviewOut).Item
	if !strings.Contains(pv.Attempts[0].Body, "First question?") || !strings.Contains(pv.Attempts[0].Body, "First answer.") {
		t.Fatal("the kept conversation was not sent as turns")
	}
	send2.Nonce, send2.PayloadHash = pv.Nonce, pv.PayloadHash
	e.say1("Second answer.")
	e.call(e.editor, "ai.chat.send", send2)
	got := e.call(e.editor, "ai.chat.get", aiwork.ChatRefIn{ProjectID: e.project, ChatID: ch.ID}).(aiwork.ChatItem).Item
	if len(got.Turns) != 4 || got.Turns[3].Content != "Second answer." || got.Title == "" {
		t.Fatalf("%+v", got)
	}
	// private: nobody else, the owner included
	for _, who := range []ops.Principal{e.editor2, e.owner} {
		_, err := e.try(who, "ai.chat.get", aiwork.ChatRefIn{ProjectID: e.project, ChatID: ch.ID})
		wantCode(t, err, ops.CodeNotFound)
		if l := e.call(who, "ai.chat.list", aiwork.ChatListIn{ProjectID: e.project}).(aiwork.ChatListOut); len(l.Items) != 0 {
			t.Fatal("another person's chat is listed")
		}
	}
	// the streamed path adds an exchange by the run's own hash: a forged answer is refused
	_, err = e.try(e.editor, "ai.chat.append", aiwork.ChatAppendIn{ProjectID: e.project, ChatID: ch.ID, RunID: r.RunID, Message: "hi", Answer: "A forged answer."})
	wantCode(t, err, ops.CodeInvalid)
	if _, err := e.try(e.editor, "ai.chat.append", aiwork.ChatAppendIn{ProjectID: e.project, ChatID: ch.ID, RunID: r.RunID, Message: "hi", Answer: "First answer."}); err != nil {
		t.Fatal(err)
	}
	// delete
	e.call(e.editor, "ai.chat.delete", aiwork.ChatRefIn{ProjectID: e.project, ChatID: ch.ID})
	if n := e.count(`SELECT count(*) FROM ai_chat_turns`); n != 0 {
		t.Fatalf("%d turns left", n)
	}
	_, err = e.try(e.editor, "ai.chat.get", aiwork.ChatRefIn{ProjectID: e.project, ChatID: ch.ID})
	wantCode(t, err, ops.CodeNotFound)
}

func (e *env) addNamedProvider(f *fake, name string, local bool) string {
	e.t.Helper()
	in := aicore.ProviderCreateIn{Name: name, Kind: "openai_compatible", Scope: "instance", BaseURL: f.srv.URL + "/v1", APIKey: "sk-test-OTHER-0123456789", DefaultModel: "model-a", Local: local}
	return e.call(e.admin, "ai.provider.create", in).(aicore.ProviderItem).Item.ID
}

var _ = json.Marshal
