package server

import (
	"context"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/pandoc/pandoctest"
)

// The Pandoc screens, against the stand-in program: who sees the settings, what a refused path says, what the test
// shows, and that the Compile and import pages offer the extra formats only while the bridge is on and usable.

func TestThePandocPageIsTheAdministratorsAndTheFormatsFollowTheBridge(t *testing.T) {
	w := newShareWorld(t, map[string]string{"SCRIOBH_OIDC_STUB": "1"})
	admin := w.s.newCaller(t, "admin", ops.RoleAdmin)
	dir, bin := pandoctest.Install(t)
	pandoctest.Put(t, dir, "md.out", []byte("# Title\n\nA short test paragraph.\n"))
	pandoctest.Put(t, dir, "export.out", []byte("A short test paragraph.\n"))

	// a person who is not an administrator finds nothing
	for _, c := range []caller{w.owner, w.reviewer} {
		if code, _ := w.s.getPage(t, c, "/admin/pandoc"); code != http.StatusNotFound {
			t.Errorf("%s: the page answered %d", c.name, code)
		}
		if rec := w.s.postForm(t, c, "/ui/admin/pandoc/save", url.Values{"enabled": {"1"}, "path": {bin}}); rec.Code != http.StatusNotFound {
			t.Errorf("%s: saving answered %d", c.name, rec.Code)
		}
		if rec := w.s.postForm(t, c, "/ui/admin/pandoc/test", url.Values{}); rec.Code != http.StatusNotFound {
			t.Errorf("%s: testing answered %d", c.name, rec.Code)
		}
	}
	if code, _ := w.s.getPage(t, w.s.newCaller(t, "ghost", ops.RoleWriter), "/admin/pandoc"); code != http.StatusNotFound {
		t.Errorf("a writer: %d", code)
	}

	// off: the page says so, the Compile page lacks the formats, the import page lacks the radio
	code, body := w.s.getPage(t, admin, "/admin/pandoc")
	if code != 200 || !strings.Contains(body, `id="pd-state"`) || !strings.Contains(body, "Switched off") || strings.Contains(body, `id="pd-restored"`) {
		t.Fatalf("the off page: %d", code)
	}
	if strings.Contains(body, `action="/ui/admin/pandoc/test`) {
		t.Error("a test button is offered while the bridge is off")
	}
	_, cmp := w.s.getPage(t, w.owner, "/p/shared-book/compile")
	if !strings.Contains(cmp, `data-pandoc="false"`) {
		t.Error("the Compile page does not say the bridge is off")
	}
	if _, imp := w.s.getPage(t, w.owner, "/import"); strings.Contains(imp, "imp-f-pandoc") || strings.Contains(imp, `data-fmt="pandoc"`) {
		t.Error("the import page offers Pandoc while it is off")
	}

	// a refused path is shown in the op's words on the field, and nothing changes
	r := w.s.postForm(t, admin, "/ui/admin/pandoc/save", url.Values{"enabled": {"1"}, "path": {"pandoc-relative"}, "memory_mb": {"512"}})
	if r.Code != http.StatusSeeOther {
		t.Fatalf("a relative path: %d", r.Code)
	}
	b := followAs(t, w.s, admin, r).Body.String()
	if !strings.Contains(b, `id="pd-error"`) || !strings.Contains(b, `id="pd-path"`) || !strings.Contains(b, `aria-invalid="true"`) || !strings.Contains(b, `value="pandoc-relative"`) {
		t.Errorf("the refusal is not tied to the field with the typed value kept: %.600s", b)
	}
	if _, imp := w.s.getPage(t, w.owner, "/import"); strings.Contains(imp, "imp-f-pandoc") {
		t.Error("a refused save switched the bridge on")
	}

	// a version that is too old is refused with its reason
	pandoctest.SetMode(t, dir, "old")
	r = w.s.postForm(t, admin, "/ui/admin/pandoc/save", url.Values{"enabled": {"1"}, "path": {bin}, "memory_mb": {"512"}})
	if r.Code != http.StatusSeeOther || !strings.Contains(followAs(t, w.s, admin, r).Body.String(), "pd-error") {
		t.Errorf("an old Pandoc: %d", r.Code)
	}
	pandoctest.SetMode(t, dir, "ok")

	// on
	r = w.s.postForm(t, admin, "/ui/admin/pandoc/save", url.Values{"enabled": {"1"}, "path": {bin}, "memory_mb": {"512"}})
	if follow(t, r) != "/admin/pandoc?saved=1" {
		t.Fatalf("saving: %s", r.Header().Get("Location"))
	}
	_, body = w.s.getPage(t, admin, "/admin/pandoc?saved=1")
	for _, want := range []string{"Switched on", "Version 3.1.9", `role="status"`, "The settings are saved.", `action="/ui/admin/pandoc/test`} {
		if !strings.Contains(body, want) {
			t.Errorf("the on page lacks %q", want)
		}
	}
	_, cmp = w.s.getPage(t, w.owner, "/p/shared-book/compile")
	if !strings.Contains(cmp, `data-pandoc="true"`) {
		t.Error("the Compile page does not say the bridge is on")
	}
	_, imp := w.s.getPage(t, w.owner, "/import")
	for _, want := range []string{`id="imp-f-pandoc"`, `data-fmt="pandoc"`, `data-opt="pandoc_format"`, `value="typst"`} {
		if !strings.Contains(imp, want) {
			t.Errorf("the import page lacks %q", want)
		}
	}
	if _, into := w.s.getPage(t, w.owner, "/p/shared-book/import"); !strings.Contains(into, `id="imp-f-pandoc"`) {
		t.Error("the add-files page does not offer Pandoc while it is on")
	}

	// the test shows both directions and only sentences
	r = w.s.postForm(t, admin, "/ui/admin/pandoc/test", url.Values{})
	b = followAs(t, w.s, admin, r).Body.String()
	if r.Code != 303 || !strings.Contains(b, "Both directions worked.") || !strings.Contains(b, "Reading rst into Markdown worked") || !strings.Contains(b, "Writing rst from Markdown worked") {
		t.Errorf("the test result: %d %.800s", r.Code, b)
	}
	// a stand-in that fails shows a bounded sentence and never the program's own output
	pandoctest.SetMode(t, dir, "fail")
	r = w.s.postForm(t, admin, "/ui/admin/pandoc/test", url.Values{})
	b = followAs(t, w.s, admin, r).Body.String()
	if !strings.Contains(b, "The test did not pass.") || !strings.Contains(b, "did not work:") || strings.Contains(b, "SECRET_DOCUMENT_TEXT") {
		t.Errorf("the failing test result: %.800s", b)
	}
	pandoctest.SetMode(t, dir, "ok")

	// switched off by hand with the path kept: no restore note, and the formats go
	r = w.s.postForm(t, admin, "/ui/admin/pandoc/save", url.Values{"path": {bin}, "memory_mb": {"512"}})
	if follow(t, r) != "/admin/pandoc?saved=1" {
		t.Fatal("switching off")
	}
	_, body = w.s.getPage(t, admin, "/admin/pandoc")
	if strings.Contains(body, `id="pd-restored"`) || !strings.Contains(body, "Switched off") {
		t.Error("an administrator who switched it off is told about a restore")
	}
	if _, cmp = w.s.getPage(t, w.owner, "/p/shared-book/compile"); !strings.Contains(cmp, `data-pandoc="false"`) {
		t.Error("the Compile page still offers the formats")
	}
	if _, imp = w.s.getPage(t, w.owner, "/import"); strings.Contains(imp, "imp-f-pandoc") {
		t.Error("the import page still offers Pandoc")
	}

	// a restore: the note, whatever the path, until it is switched on again
	r = w.s.postForm(t, admin, "/ui/admin/pandoc/save", url.Values{"enabled": {"1"}, "path": {bin}, "memory_mb": {"512"}})
	follow(t, r)
	for _, step := range backup.PostRestore {
		if _, err := step(context.Background(), w.s.DB, nil); err != nil {
			t.Fatal(err)
		}
	}
	_, body = w.s.getPage(t, admin, "/admin/pandoc")
	if !strings.Contains(body, `id="pd-restored"`) || !strings.Contains(body, "Switched off") {
		t.Error("after a restore the page does not say so")
	}
	if r = w.s.postForm(t, admin, "/ui/admin/pandoc/save", url.Values{"path": {""}, "memory_mb": {"512"}}); r.Code != 303 {
		t.Fatalf("saving while off: %d", r.Code)
	}
	if _, body = w.s.getPage(t, admin, "/admin/pandoc"); !strings.Contains(body, `id="pd-restored"`) {
		t.Error("saving the settings while off cleared the restore note")
	}
	r = w.s.postForm(t, admin, "/ui/admin/pandoc/save", url.Values{"enabled": {"1"}, "path": {bin}, "memory_mb": {"512"}})
	follow(t, r)
	if _, body = w.s.getPage(t, admin, "/admin/pandoc"); strings.Contains(body, `id="pd-restored"`) {
		t.Error("switching on left the restore note")
	}
}

var reImportPanel = regexp.MustCompile(`(?s)<div class="rg-block" data-fmt="pandoc".*?id="imp-pdp".*?</select>`)

// Every word on the Pandoc settings page, and on the Pandoc panel of the import page, comes from the catalogue. The
// format names and the program's path are identifiers, and let through.
func TestThePseudoLocaleLeavesNoEnglishOnThePandocScreens(t *testing.T) {
	w := newShareWorld(t, map[string]string{"SCRIOBH_OIDC_STUB": "1"})
	admin := w.s.newCaller(t, "admin", ops.RoleAdmin)
	dir, bin := pandoctest.Install(t)
	pandoctest.Put(t, dir, "md.out", []byte("# Title\n\nA short test paragraph.\n"))
	pandoctest.Put(t, dir, "export.out", []byte("A short test paragraph.\n"))
	for _, c := range []caller{admin, w.owner} {
		if rec := w.s.do(c, "PUT", "/api/v1/me/prefs", `{"items":{"language":"xx"}}`); rec.Code != 200 {
			t.Fatalf("language: %d", rec.Code)
		}
	}
	pages := map[string]string{}
	_, pages["off"] = w.s.getPage(t, admin, "/admin/pandoc")
	if rec := w.s.postForm(t, admin, "/ui/admin/pandoc/save", url.Values{"enabled": {"1"}, "path": {bin}, "memory_mb": {"512"}}); rec.Code != 303 {
		t.Fatalf("on: %d", rec.Code)
	}
	_, pages["on"] = w.s.getPage(t, admin, "/admin/pandoc?saved=1")
	pages["tested"] = followAs(t, w.s, admin, w.s.postForm(t, admin, "/ui/admin/pandoc/test", url.Values{})).Body.String()
	_, imp := w.s.getPage(t, w.owner, "/import")
	pages["import panel"] = reImportPanel.FindString(imp)
	if pages["import panel"] == "" {
		t.Fatal("no Pandoc panel on the import page")
	}
	for _, step := range backup.PostRestore {
		if _, err := step(context.Background(), w.s.DB, nil); err != nil {
			t.Fatal(err)
		}
	}
	_, pages["restored"] = w.s.getPage(t, admin, "/admin/pandoc")
	if !strings.Contains(pages["restored"], `id="pd-restored"`) {
		t.Fatal("no restore note")
	}
	allowed := map[string]bool{"odt": true, "rtf": true, "latex": true, "mediawiki": true, "org": true, "textile": true, "rst": true, "typst": true, "docbook": true, "markdown": true}
	for name, body := range pages {
		if !strings.Contains(body, "⟦") {
			t.Errorf("%s: nothing in the pseudo-locale", name)
			continue
		}
		var bad []string
		for _, word := range englishIn(reCodeOrOp.ReplaceAllString(body, " ")) {
			if !allowed[strings.ToLower(word)] {
				bad = append(bad, word)
			}
		}
		if len(bad) > 0 {
			t.Errorf("%s has English that did not come from the catalogue: %s", name, strings.Join(bad, " "))
		}
	}
}
