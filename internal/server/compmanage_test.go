package server

import (
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"
)

// The merge, split and record screens (F-478): the preview is the op's plan, an unanswered clash disables the confirm,
// the confirmation is the name typed, a changed plan is a fresh preview and not a surprise, and the record says there is
// no undo. Nobody but the Compendium's owner reaches any of it.

type cmWorld struct {
	e                       compEnv
	src, dst                string
	srcSlug                 string
	elara, torin, wren, otr string // entity ids: a clash pair, and two that move freely
}

func newCMWorld(t *testing.T) cmWorld {
	t.Helper()
	e := newCompEnv(t)
	w := cmWorld{e: e, src: e.cid, srcSlug: e.slug}
	w.dst = send(t, e.s, e.owner, "POST", "/api/v1/compendiums", `{"name":"The Wide World"}`, 201)["item"].(map[string]any)["id"].(string)
	mk := func(comp, kind, name string) string {
		return send(t, e.s, e.owner, "POST", "/api/v1/compendiums/"+comp+"/entities", `{"kind":"`+kind+`","name":"`+name+`"}`, 201)["item"].(map[string]any)["id"].(string)
	}
	w.elara = mk(w.src, "character", "Elara")
	w.torin = mk(w.src, "character", "Torin")
	w.wren = mk(w.src, "place", "Wrenhold")
	mk(w.dst, "character", "Elara") // the name the first one clashes with
	return w
}

func (w cmWorld) form(t *testing.T, c caller, action string, v url.Values) (int, string) {
	t.Helper()
	rec := w.e.s.post(t, c, "/ui/compendiums/"+w.src+"/manage/"+action, v, "", false)
	return rec.Code, rec.Body.String()
}

func TestMergePreviewConfirmAndRecord(t *testing.T) {
	w := newCMWorld(t)
	owner := w.e.owner
	page := w.e.s.do(owner, "GET", "/c/"+w.srcSlug+"/manage", "")
	if page.Code != 200 || !strings.Contains(page.Body.String(), "There is no undo button") || !strings.Contains(page.Body.String(), "The Wide World") {
		t.Fatalf("the manage page: %d", page.Code)
	}
	// the preview names the clash and refuses to be confirmed until it is answered
	code, b := w.form(t, owner, "merge-preview", url.Values{"into_id": {w.dst}})
	b = strings.NewReplacer("\u2068", "", "\u2069", "").Replace(b) // names are isolated so that a right-to-left name cannot reorder the sentence
	if code != 200 || !strings.Contains(b, "Names already used in the target") || !strings.Contains(b, "&#34;Elara&#34;") && !strings.Contains(b, "“Elara”") {
		t.Fatalf("preview: %d\n%s", code, regexp.MustCompile(`(?s)<main.*</main>`).FindString(b))
	}
	if !strings.Contains(b, "1 entity needs an answer before the move.") {
		t.Error("the unanswered clash is not counted")
	}
	if !regexp.MustCompile(`(?s)<form class="form cmg-confirm".*?<button[^>]*disabled[^>]*>`).MatchString(b) || strings.Contains(b, `data-confirm-submit`) {
		t.Error("the confirm is not disabled while a clash is unanswered")
	}
	// a confirm sent anyway is refused by the op, not applied
	fp := regexp.MustCompile(`name="fingerprint" value="([0-9a-f]+)"`).FindStringSubmatch(b)[1]
	if code, b2 := w.form(t, owner, "merge", url.Values{"into_id": {w.dst}, "fingerprint": {fp}, "confirm_name": {"The Salt Road Bible"}}); code != http.StatusUnprocessableEntity && !strings.Contains(b2, "need an answer") {
		t.Errorf("a confirm with an unanswered clash: %d", code)
	}
	if got := w.e.s.do(owner, "GET", "/api/v1/compendiums/"+w.src+"/entities", "").Body.String(); !strings.Contains(got, "Torin") {
		t.Fatal("something moved before it was confirmed")
	}
	// answer: rename it; preview again
	ans := url.Values{"into_id": {w.dst}, "res." + w.elara: {"rename"}, "new." + w.elara: {"Elara of the Salt"}}
	code, b = w.form(t, owner, "merge-preview", ans)
	if code != 200 || strings.Contains(b, "need an answer") {
		t.Fatalf("answered preview: %d", code)
	}
	if !strings.Contains(b, "data-confirm-submit") || !strings.Contains(b, `data-confirm="The Salt Road Bible"`) {
		t.Error("the typed confirmation is not offered once the preview is complete")
	}
	fp = regexp.MustCompile(`name="fingerprint" value="([0-9a-f]+)"`).FindStringSubmatch(b)[1]
	confirm := func(typed string) (int, string, http.Header) {
		v := url.Values{"into_id": {w.dst}, "fingerprint": {fp}, "confirm_name": {typed}, "res." + w.elara: {"rename"}, "new." + w.elara: {"Elara of the Salt"}}
		rec := w.e.s.post(t, owner, "/ui/compendiums/"+w.src+"/manage/merge", v, "", false)
		return rec.Code, rec.Body.String(), rec.Header()
	}
	// the wrong name typed: nothing happens
	if code, b, _ := confirm("the wrong name"); code != 200 || !strings.Contains(b, "not the name of the Compendium") {
		t.Fatalf("a wrong typed name: %d", code)
	}
	if got := w.e.s.do(owner, "GET", "/api/v1/compendiums/"+w.src+"/entities", "").Body.String(); !strings.Contains(got, "Torin") {
		t.Fatal("a wrong typed name moved entities")
	}
	// a change between the preview and the confirm: a fresh preview, not a merge
	send(t, w.e.s, owner, "POST", "/api/v1/compendiums/"+w.src+"/entities", `{"kind":"item","name":"Latecomer"}`, 201)
	if code, b, _ := confirm("The Salt Road Bible"); code != 200 || !strings.Contains(b, "Something changed since the preview") {
		t.Fatalf("a stale confirm: %d\n%s", code, alertText(b))
	}
	if got := w.e.s.do(owner, "GET", "/api/v1/compendiums/"+w.src+"/entities", "").Body.String(); !strings.Contains(got, "Torin") {
		t.Fatal("a stale confirm moved entities")
	}
	// preview again (the page did) and confirm
	_, b = w.form(t, owner, "merge-preview", ans)
	fp = regexp.MustCompile(`name="fingerprint" value="([0-9a-f]+)"`).FindStringSubmatch(b)[1]
	code, _, h := confirm("The Salt Road Bible")
	if code != http.StatusSeeOther || !strings.Contains(h.Get("Location"), "/changes/") {
		t.Fatalf("the merge: %d %s", code, h.Get("Location"))
	}
	if got := w.e.s.do(owner, "GET", "/api/v1/compendiums/"+w.dst+"/entities", "").Body.String(); !strings.Contains(got, "Torin") || !strings.Contains(got, "Elara of the Salt") {
		t.Errorf("the entities are not in the target: %s", got)
	}
	// the record page
	rec := w.e.s.do(owner, "GET", h.Get("Location"), "")
	rb := rec.Body.String()
	for _, want := range []string{"There is no undo button", "Elara of the Salt", "Torin", "Record of a change", "Merge"} {
		if !strings.Contains(rb, want) {
			t.Errorf("the record page lacks %q", want)
		}
	}
	if list := w.e.s.do(owner, "GET", "/c/"+w.srcSlug+"/changes", "").Body.String(); !strings.Contains(list, "/changes/") {
		t.Error("the record is not listed")
	}
}

func TestSplitPreviewAndConfirm(t *testing.T) {
	w := newCMWorld(t)
	owner := w.e.owner
	code, b := w.form(t, owner, "split-preview", url.Values{"entity_id": {w.torin, w.wren}, "name": {"Torin's Hold"}})
	if code != 200 || !strings.Contains(b, "Torin&#39;s Hold") || !strings.Contains(b, `data-confirm="Torin&#39;s Hold"`) {
		t.Fatalf("split preview: %d %s", code, alertText(b))
	}
	// nothing chosen is a message on the form, not an empty plan
	if code, b := w.form(t, owner, "split-preview", url.Values{"name": {"Nothing"}}); code != http.StatusUnprocessableEntity || !strings.Contains(b, "Choose the entities") {
		t.Errorf("an empty split: %d %s", code, alertText(b))
	}
	fp := regexp.MustCompile(`name="fingerprint" value="([0-9a-f]+)"`).FindStringSubmatch(b)
	_ = fp
	_, b = w.form(t, owner, "split-preview", url.Values{"entity_id": {w.torin, w.wren}, "name": {"Torin's Hold"}})
	f := regexp.MustCompile(`name="fingerprint" value="([0-9a-f]+)"`).FindStringSubmatch(b)[1]
	rec := w.e.s.post(t, owner, "/ui/compendiums/"+w.src+"/manage/split", url.Values{"entity_id": {w.torin, w.wren}, "name": {"Torin's Hold"}, "fingerprint": {f}, "confirm_name": {"Torin's Hold"}}, "", false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("split: %d %s", rec.Code, alertText(rec.Body.String()))
	}
	if got := w.e.s.do(owner, "GET", "/api/v1/compendiums/"+w.src+"/entities", "").Body.String(); strings.Contains(got, "Torin") || !strings.Contains(got, "Elara") {
		t.Errorf("the source after a split: %s", got)
	}
}

func TestManagePagesAreTheOwnersAlone(t *testing.T) {
	w := newCMWorld(t)
	e := w.e
	for _, p := range []string{"/c/" + w.srcSlug + "/manage", "/c/" + w.srcSlug + "/changes"} {
		for _, c := range []caller{e.editor, e.reader} {
			if rec := e.s.do(c, "GET", p, ""); rec.Code != http.StatusForbidden {
				t.Errorf("%s as %s: %d", p, c.name, rec.Code)
			}
		}
		if rec := e.s.do(e.stranger, "GET", p, ""); rec.Code != http.StatusNotFound {
			t.Errorf("%s as a stranger: %d", p, rec.Code)
		}
	}
	for _, c := range []caller{e.editor, e.reader} {
		if rec := e.s.post(t, c, "/ui/compendiums/"+w.src+"/manage/merge-preview", url.Values{"into_id": {w.dst}}, "", false); rec.Code != http.StatusForbidden {
			t.Errorf("a preview as %s: %d", c.name, rec.Code)
		}
	}
	if rec := e.s.post(t, e.stranger, "/ui/compendiums/"+w.src+"/manage/merge-preview", url.Values{"into_id": {w.dst}}, "", false); rec.Code != http.StatusNotFound {
		t.Errorf("a preview as a stranger: %d", rec.Code)
	}
	if rec := e.s.post(t, e.owner, "/ui/compendiums/"+w.src+"/manage/merge-preview", url.Values{"into_id": {w.dst}}, "-", false); rec.Code != http.StatusForbidden {
		t.Errorf("no token: %d", rec.Code)
	}
	// the owner's overview offers the link, the editor's does not
	if !strings.Contains(e.s.do(e.owner, "GET", "/c/"+w.srcSlug, "").Body.String(), "/manage") || strings.Contains(e.s.do(e.editor, "GET", "/c/"+w.srcSlug, "").Body.String(), "/manage") {
		t.Error("the link to merge and split is not the owner's alone")
	}
}

// The merge, split and record pages carry no plain English in the pseudo-locale: every word is the catalogue's.
func TestThePseudoLocaleLeavesNoEnglishOnTheCompendiumManagementPages(t *testing.T) {
	w := newCMWorld(t)
	w.e.s.Cfg.OIDCStub = true
	if rec := w.e.s.do(w.e.owner, "PUT", "/api/v1/me/prefs", `{"items":{"language":"xx"}}`); rec.Code != 200 {
		t.Fatalf("language: %d", rec.Code)
	}
	s, owner := w.e.s, w.e.owner
	ans := url.Values{"into_id": {w.dst}, "res." + w.elara: {"rename"}, "new." + w.elara: {"Elara of the Salt"}}
	_, prev := w.form(t, owner, "merge-preview", url.Values{"into_id": {w.dst}})
	_, answered := w.form(t, owner, "merge-preview", ans)
	_, split := w.form(t, owner, "split-preview", url.Values{"entity_id": {w.torin, w.wren}, "name": {"Torin's Hold"}})
	fp := regexp.MustCompile(`name="fingerprint" value="([0-9a-f]+)"`).FindStringSubmatch(answered)[1]
	ans.Set("fingerprint", fp)
	ans.Set("confirm_name", "The Salt Road Bible")
	rec := s.post(t, owner, "/ui/compendiums/"+w.src+"/manage/merge", ans, "", false)
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("merge: %d", rec.Code)
	}
	record := s.do(owner, "GET", rec.Header().Get("Location"), "").Body.String()
	pages := map[string]string{
		"manage": s.do(owner, "GET", "/c/"+w.srcSlug+"/manage", "").Body.String(), "preview with a clash": prev, "answered preview": answered, "split preview": split,
		"the record": record, "the list": s.do(owner, "GET", "/c/"+w.srcSlug+"/changes", "").Body.String(),
	}
	names := strings.NewReplacer("The Salt Road Bible", " ", "The Wide World", " ", "Elara of the Salt", " ", "Elara", " ", "Torin's Hold", " ", "Torin&#39;s Hold", " ", "Torin", " ", "Wrenhold", " ")
	for name, body := range pages {
		for _, word := range englishIn(names.Replace(body)) {
			t.Errorf("%s: %q is plain English in the pseudo-locale", name, word)
		}
	}
}

// Each choice the preview offers reaches the confirm form and the op, and ends in a merge (303 to the record), not in a loop
// of "Something changed": a rename, a merge into the entity it clashes with, dropping the aliases that clash, leaving the entity
// where it is, dropping a relationship that would join the two, and what to do with a key pinned to the Compendium.
func TestEveryChoiceOnThePreviewReachesTheConfirmation(t *testing.T) {
	e := newCompEnv(t)
	s, owner := e.s, e.owner
	comp := func(name string) string {
		return send(t, s, owner, "POST", "/api/v1/compendiums", `{"name":"`+name+`"}`, 201)["item"].(map[string]any)["id"].(string)
	}
	slugOf := func(id string) string {
		return send(t, s, owner, "GET", "/api/v1/compendiums/"+id, "", 200)["item"].(map[string]any)["slug"].(string)
	}
	ent := func(c, kind, name string) string {
		return send(t, s, owner, "POST", "/api/v1/compendiums/"+c+"/entities", `{"kind":"`+kind+`","name":"`+name+`"}`, 201)["item"].(map[string]any)["id"].(string)
	}
	type scenario struct {
		name  string
		setup func(src, dst string) (preview url.Values, extra url.Values, kind string)
		check func(src, dst string)
	}
	listed := func(c string) string {
		return s.do(owner, "GET", "/api/v1/compendiums/"+c+"/entities?limit=100", "").Body.String()
	}
	scenarios := []scenario{
		{"merge into the entity it clashes with", func(src, dst string) (url.Values, url.Values, string) {
			a, b := ent(src, "character", "Elara"), ent(dst, "character", "Elara")
			return url.Values{"res." + a: {"merge"}, "into." + a: {b}}, nil, "merge"
		}, func(src, dst string) {
			if strings.Count(listed(dst), `"name":"Elara"`) != 1 {
				t.Errorf("the two Elaras were not made one: %s", listed(dst))
			}
		}},
		{"drop the aliases that clash", func(src, dst string) (url.Values, url.Values, string) {
			r := ent(src, "character", "Rowan")
			send(t, s, owner, "POST", "/api/v1/entities/"+r+"/aliases", `{"alias":"Elara"}`, 201)
			ent(dst, "character", "Elara")
			return url.Values{"res." + r: {"drop_aliases"}}, nil, "merge"
		}, func(src, dst string) {
			if !strings.Contains(listed(dst), "Rowan") {
				t.Error("Rowan did not move")
			}
		}},
		{"leave the clashing entity where it is", func(src, dst string) (url.Values, url.Values, string) {
			a := ent(src, "character", "Elara")
			ent(src, "character", "Quiet")
			ent(dst, "character", "Elara")
			return url.Values{"res." + a: {"skip"}}, nil, "merge"
		}, func(src, dst string) {
			if !strings.Contains(listed(src), "Elara") || !strings.Contains(listed(dst), "Quiet") {
				t.Errorf("skip: source %s", listed(src))
			}
		}},
		{"a relationship that would join the two is dropped", func(src, dst string) (url.Values, url.Values, string) {
			a, b := ent(src, "character", "Moves"), ent(src, "character", "Stays")
			send(t, s, owner, "POST", "/api/v1/entities/"+a+"/relations", `{"to_entity_id":"`+b+`","kind":"ally"}`, 201)
			return url.Values{"entity_id": {a}, "name": {"Pair"}, "crossing": {"drop"}}, nil, "split"
		}, func(src, dst string) {
			if strings.Contains(listed(src), "Moves") {
				t.Error("the entity did not move")
			}
		}},
		{"a key pinned to the Compendium is left pinned", func(src, dst string) (url.Values, url.Values, string) {
			ent(src, "character", "Keyed")
			s.mintKey(t, owner, `{"name":"pinned","scope":"read","compendium_id":"`+src+`"}`)
			return url.Values{"key_pins": {"leave"}}, nil, "merge"
		}, func(src, dst string) {
			if !strings.Contains(listed(dst), "Keyed") {
				t.Error("the merge was not made")
			}
		}},
		{"follow: the books the caller owns reference the target", func(src, dst string) (url.Values, url.Values, string) {
			ent(src, "character", "Followed")
			pj := send(t, s, owner, "POST", "/api/v1/projects", `{"title":"Follower"}`, 201)["item"].(map[string]any)["id"].(string)
			send(t, s, owner, "PUT", "/api/v1/projects/"+pj+"/compendium/primary", `{"compendium_id":"`+src+`","fingerprint":"x"}`, 409) // the old way needs the fingerprint now
			send(t, s, owner, "PUT", "/api/v1/projects/"+pj+"/compendium", `{"compendium_id":"`+src+`"}`, 200)
			return url.Values{"follow": {"1"}}, nil, "merge"
		}, func(src, dst string) {
			if !strings.Contains(listed(dst), "Followed") {
				t.Error("the merge was not made")
			}
		}},
	}
	for _, sc := range scenarios {
		src, dst := comp("Src "+sc.name), comp("Dst "+sc.name)
		pv, _, kind := sc.setup(src, dst)
		action := kind
		form := url.Values{}
		for k, v := range pv {
			form[k] = v
		}
		if kind == "merge" {
			form.Set("into_id", dst)
		}
		typed := "Src " + sc.name
		if kind == "split" {
			typed = form.Get("name")
		}
		rec := s.post(t, owner, "/ui/compendiums/"+src+"/manage/"+action+"-preview", form, "", false)
		b := rec.Body.String()
		m := regexp.MustCompile(`name="fingerprint" value="([0-9a-f]+)"`).FindStringSubmatch(b)
		if rec.Code != 200 || m == nil {
			t.Errorf("%s: the preview: %d %s", sc.name, rec.Code, alertText(b))
			continue
		}
		if !strings.Contains(b, "data-confirm-submit") {
			t.Errorf("%s: the confirmation is not offered\n%s", sc.name, regexp.MustCompile(`(?s)<main.*</main>`).FindString(b))
			continue
		}
		// the confirm form's own hidden fields are what is sent: read them back from the page, as a browser would
		cf := regexp.MustCompile(`(?s)<form class="form cmg-confirm".*?</form>`).FindString(b)
		conf := url.Values{"confirm_name": {typed}}
		for _, in := range regexp.MustCompile(`<input type="hidden" name="([^"]+)" value="([^"]*)"`).FindAllStringSubmatch(cf, -1) {
			if in[1] != "_csrf" {
				conf.Add(in[1], htmlUnescape(in[2]))
			}
		}
		rec = s.post(t, owner, "/ui/compendiums/"+src+"/manage/"+action, conf, "", false)
		if rec.Code != http.StatusSeeOther {
			t.Errorf("%s: the confirmation: %d %s %s", sc.name, rec.Code, alertText(rec.Body.String()), regexp.MustCompile(`notice-line[^>]*>([^<]*)`).FindString(rec.Body.String()))
			continue
		}
		sc.check(src, dst)
		_ = slugOf
	}
}

func htmlUnescape(s string) string {
	return strings.NewReplacer("&#34;", `"`, "&#39;", "'", "&amp;", "&", "&lt;", "<", "&gt;", ">").Replace(s)
}
