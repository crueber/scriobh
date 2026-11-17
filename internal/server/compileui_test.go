package server

import (
	"encoding/json"
	"net/url"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
)

func TestCompilePageIsForEditorsAndTheOwnerAndNobodyElseFindsIt(t *testing.T) {
	w := newShareWorld(t, nil)
	reader := w.s.newCaller(t, "reader", ops.RoleReader)
	w.s.addMember(t, w.pid, reader.userID, ops.ProjectReader)
	page := "/p/shared-book/compile"
	for _, c := range []struct {
		who  caller
		want int
	}{{w.owner, 200}, {w.editor, 200}, {w.reviewer, 404}, {reader, 404}, {w.stranger, 404}, {caller{name: "anonymous"}, 303}} {
		if rec := w.s.do(c.who, "GET", page, ""); rec.Code != c.want {
			t.Errorf("%s opens the compile page: %d, want %d", c.who.name, rec.Code, c.want)
		}
	}
	form := url.Values{"preset": {"shunn-us"}}
	base := "/ui/projects/" + w.pid + "/compile/"
	for _, a := range []string{"download", "submission", "contact", "cover"} {
		if rec := w.s.postForm(t, w.reviewer, base+a, form); rec.Code != 403 {
			t.Errorf("a reviewer posts %s: %d", a, rec.Code)
		}
		if rec := w.s.postForm(t, w.stranger, base+a, form); rec.Code != 404 {
			t.Errorf("a stranger posts %s: %d", a, rec.Code)
		}
		if rec := w.s.postForm(t, w.owner, base+a, url.Values{"preset": {"shunn-us"}, "_csrf": {"nope"}}); rec.Code != 403 {
			t.Errorf("no token to %s: %d", a, rec.Code)
		}
	}
	if rec := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/trash", ""); rec.Code != 200 {
		t.Fatal(rec.Body)
	}
	if rec := w.s.do(w.owner, "GET", page, ""); rec.Code != 404 {
		t.Errorf("a trashed project's compile page: %d", rec.Code)
	}
}

// The page is built from what the ops say: the presets with their options, and each option's type and allowed values
// from the schema of compile.preview, so nothing about an option is written in the page.
func TestCompilePageHandsTheScriptWhatTheOpsSay(t *testing.T) {
	w := newShareWorld(t, nil)
	body := w.s.do(w.owner, "GET", "/p/shared-book/compile", "").Body.String()
	for _, want := range []string{"Every editor of this project can see and change this block", `id="cmp"`, `name="surname"`, "shunn-us", "epub3", "plain-markdown", "uk-manuscript-courier", "styled-docx"} {
		if !strings.Contains(body, want) {
			t.Errorf("the page lacks %q", want)
		}
	}
	descs := w.s.compileOptionDescs()
	by := map[string]optionDesc{}
	for _, d := range descs {
		by[d.Key] = d
	}
	if d := by["page_size"]; d.Type != "string" || !strings.Contains(strings.Join(d.Enum, ","), "letter,a4") || d.Group != "page" {
		t.Errorf("page_size: %+v", d)
	}
	if d := by["chapter_heading"]; len(d.Enum) != 4 {
		t.Errorf("chapter_heading: %+v", d)
	}
	if d := by["justify"]; d.Type != "boolean" {
		t.Errorf("justify: %+v", d)
	}
	if _, ok := by["clear"]; ok {
		t.Error("clear is a list of option names, not an option")
	}
	// every option the schema has is one the page's labels and groups know, or the page would show a bare key
	for k, d := range by {
		if d.Group == "more" {
			t.Errorf("option %s has no group: it is shown under More with its key as a label", k)
		}
	}
	var presets struct{ Presets []struct{ ID string } }
	raw := w.s.do(w.owner, "GET", "/api/v1/projects/"+w.pid+"/compile/presets", "").Body.Bytes()
	if err := json.Unmarshal(raw, &presets); err != nil || len(presets.Presets) < 7 {
		t.Errorf("presets: %v %s", err, raw)
	}
}

func TestTheContactBlockIsSharedAndSavedFromThePage(t *testing.T) {
	w := newShareWorld(t, nil)
	rec := w.s.postForm(t, w.editor, "/ui/projects/"+w.pid+"/compile/contact", url.Values{"name": {"Máire Nolan"}, "surname": {"Nolan"}, "address": {"1 Quay Road\nGalway"}, "email": {"m@example.org"}, "phone": {"0123"}})
	if rec.Code != 303 {
		t.Fatalf("saving: %d", rec.Code)
	}
	for _, c := range []caller{w.owner, w.editor} {
		body := w.s.do(c, "GET", "/p/shared-book/compile", "").Body.String()
		if !strings.Contains(body, "Máire Nolan") || !strings.Contains(body, "1 Quay Road") || !strings.Contains(body, "The contact block was saved") && c.name == "editor" {
			t.Errorf("%s does not see the block the editor saved", c.name)
		}
	}
	if strings.Contains(w.logs.String(), "m@example.org") || strings.Contains(w.logs.String(), "Quay Road") {
		t.Error("the contact block reached a log line")
	}
	// the page that reviewers and strangers cannot open is not where the contact leaks to
	if body := w.s.do(w.owner, "GET", "/p/shared-book/review", "").Body.String(); strings.Contains(body, "Máire") {
		t.Error("the contact block is on the review page")
	}
}

// What the file holds is what the preview said: the same input hash, and the header the page shows as the manifest
// summary; documents marked do not compile are counted as left out, in the preview and the download and the dialog.
func TestDownloadsCarryTheManifestAndAgreeWithThePreview(t *testing.T) {
	w := newShareWorld(t, nil)
	no := false
	d := w.callAs(t, w.owner, "doc.get", docs.DocIn{ID: w.doc2}).(docs.Item).Item
	w.callAs(t, w.owner, "doc.update", docs.UpdateIn{ID: w.doc2, BaseRevision: d.Revision, IncludeInCompile: &no})
	pv := w.s.do(w.owner, "POST", "/api/v1/projects/"+w.pid+"/compile/preview", `{"preset":"shunn-us"}`)
	var prev struct {
		InputHash     string `json:"input_hash"`
		ExcludedTotal int    `json:"excluded_total"`
		Excluded      []struct{ Title, Reason string }
	}
	if err := json.Unmarshal(pv.Body.Bytes(), &prev); err != nil || prev.InputHash == "" || prev.ExcludedTotal != 1 || !strings.Contains(prev.Excluded[0].Reason, "do not compile") {
		t.Fatalf("preview: %v %s", err, pv.Body)
	}
	dl := w.s.postForm(t, w.owner, "/ui/projects/"+w.pid+"/compile/download", url.Values{"preset": {"shunn-us"}, "options": {`{"font_size":13}`}})
	if dl.Code != 200 || !strings.Contains(dl.Header().Get("Content-Type"), "wordprocessingml") {
		t.Fatalf("download: %d %v", dl.Code, dl.Header())
	}
	h := dl.Header()
	if h.Get("X-Export-Skipped") != "1" || h.Get("X-Export-Sha256") == "" || h.Get("X-Export-Words") == "" || h.Get("X-Export-Id") == "" {
		t.Errorf("manifest headers: %v", h)
	}
	if h.Get("X-Export-Input-Hash") == prev.InputHash {
		t.Error("the input hash did not change with an option")
	}
	same := w.s.postForm(t, w.owner, "/ui/projects/"+w.pid+"/compile/download", url.Values{"preset": {"shunn-us"}})
	if same.Header().Get("X-Export-Input-Hash") != prev.InputHash {
		t.Errorf("the download's input hash %q is not the preview's %q", same.Header().Get("X-Export-Input-Hash"), prev.InputHash)
	}
	if !strings.Contains(same.Header().Get("Access-Control-Expose-Headers"), "X-Export-Input-Hash") {
		t.Error("the headers are not exposed to the page's script")
	}
	sub := w.s.postForm(t, w.owner, "/ui/projects/"+w.pid+"/compile/submission", url.Values{})
	if sub.Code != 200 || sub.Header().Get("Content-Type") != "application/zip" || sub.Header().Get("X-Export-Input-Hash") == "" {
		t.Errorf("submission: %d %v", sub.Code, sub.Header())
	}
	if bad := w.s.postForm(t, w.owner, "/ui/projects/"+w.pid+"/compile/download", url.Values{"options": {"{not json"}}); bad.Code != 422 {
		t.Errorf("unreadable options: %d", bad.Code)
	}
	// the export dialog: the notice header, and the choice
	ex := func(vals url.Values) string {
		vals.Set("mode", "single")
		rec := w.s.postForm(t, w.owner, "/ui/projects/"+w.pid+"/export", vals)
		if rec.Code != 200 {
			t.Fatalf("export: %d %s", rec.Code, rec.Body)
		}
		return rec.Header().Get("X-Export-Skipped")
	}
	if got := ex(url.Values{}); got != "1" {
		t.Errorf("a single file left out %q, want 1", got)
	}
	if got := ex(url.Values{"include_excluded": {"yes"}}); got != "" {
		t.Errorf("a single file asked to keep them left out %q", got)
	}
	bundle := w.s.postForm(t, w.owner, "/ui/projects/"+w.pid+"/export", url.Values{"mode": {"bundle"}, "include_excluded": {"no"}})
	if bundle.Header().Get("X-Export-Skipped") != "1" {
		t.Errorf("a zip told to leave them out: %q", bundle.Header().Get("X-Export-Skipped"))
	}
	if form := w.s.do(w.owner, "GET", "/ui/projects/"+w.pid+"/export", "").Body.String(); !strings.Contains(form, `name="include_excluded"`) || !strings.Contains(form, "data-skipped=") {
		t.Error("the export dialog has no choice about documents marked do not compile")
	}
	_ = compile.PresetListIn{}
}

// Every word the options form shows comes from the catalogue: each option's label, each group's heading, each allowed
// value of a list (the typefaces are their own names) and each built-in preset's name. The print options are for the print
// formats only, hyphenation is shown and cannot be turned on, and the copyright text has room for its lines.
func TestEveryOptionThePageShowsHasWordsInTheCatalogue(t *testing.T) {
	w := newShareWorld(t, nil)
	has := func(k string) bool { return i18n.Default.Has("cmp.js." + k) }
	groups := map[string]bool{}
	for _, d := range w.s.compileOptionDescs() {
		groups[d.Group] = true
		if !has("o_" + d.Key) {
			t.Errorf("option %s has no label", d.Key)
		}
		if d.Key == "font" {
			continue
		}
		for _, e := range d.Enum {
			if !has("v_" + d.Key + "_" + e) {
				t.Errorf("option %s: the value %q has no words", d.Key, e)
			}
		}
	}
	for g := range groups {
		if !has("g_" + g) {
			t.Errorf("group %s has no heading", g)
		}
	}
	for _, p := range compile.Builtins() {
		if !has("preset_" + p.ID) {
			t.Errorf("preset %s has no name in the catalogue", p.ID)
		}
	}
	by := map[string]optionDesc{}
	for _, d := range w.s.compileOptionDescs() {
		by[d.Key] = d
	}
	for _, k := range []string{"gutter_inches", "margin_inside", "running_heads", "footnotes", "drop_cap", "chapter_ornament", "toc", "copyright_page", "imprint", "hyphenate"} {
		if f := strings.Join(by[k].Formats, ","); f != "pdf,html-paged" {
			t.Errorf("%s is shown for %q, want the print formats", k, f)
		}
	}
	if f := strings.Join(by["crop_marks"].Formats, ","); f != "pdf" {
		t.Errorf("crop_marks is for %q", f)
	}
	if len(by["font"].Formats) != 0 || len(by["page_size"].Formats) != 0 {
		t.Error("an option every format reads is hidden for some")
	}
	if !by["toc"].On || !by["running_heads"].On || !by["copyright_page"].On || by["drop_cap"].On || by["crop_marks"].On {
		t.Error("a switch that is on when unset is not shown on, or one that is off is shown on")
	}
	if !by["hyphenate"].Off || by["justify"].Off || !by["imprint"].Long {
		t.Errorf("hyphenate %+v, imprint %+v", by["hyphenate"], by["imprint"])
	}
	// the margins read top, bottom, inside, outside, gutter: the table's order, not the alphabet's
	var order []string
	for _, d := range w.s.compileOptionDescs() {
		if d.Group == "margins" {
			order = append(order, d.Key)
		}
	}
	if got := strings.Join(order, " "); got != "margin_inches margin_top margin_bottom margin_inside margin_outside gutter_inches" {
		t.Errorf("the margins are in the order %s", got)
	}
}

// The cover is downloaded through the page with its manifest summary, from a print preset and the paper's thickness;
// without the thickness, or from a Word preset, the op's own refusal comes back.
func TestTheCoverIsDownloadedFromThePage(t *testing.T) {
	w := newShareWorld(t, nil)
	base := "/ui/projects/" + w.pid + "/compile/cover"
	rec := w.s.postForm(t, w.editor, base, url.Values{"preset": {"print-6x9"}, "page_thickness_in": {"0.0025"}, "page_count": {"200"}, "bleed_in": {"0.125"}, "barcode": {"1"}})
	if rec.Code != 200 || rec.Header().Get("Content-Type") != "application/pdf" || !strings.HasPrefix(rec.Body.String(), "%PDF-") {
		t.Fatalf("cover: %d %v", rec.Code, rec.Header())
	}
	if rec.Header().Get("X-Export-Id") == "" || rec.Header().Get("X-Export-Input-Hash") == "" || !strings.Contains(rec.Header().Get("Content-Disposition"), "-cover.pdf") {
		t.Errorf("the manifest headers: %v", rec.Header())
	}
	for _, c := range []struct {
		name string
		form url.Values
	}{
		{"no thickness", url.Values{"preset": {"print-6x9"}, "page_count": {"200"}}},
		{"a Word preset", url.Values{"preset": {"shunn-us"}, "page_thickness_in": {"0.0025"}, "page_count": {"200"}}},
		{"a page count that is not whole", url.Values{"preset": {"print-6x9"}, "page_thickness_in": {"0.0025"}, "page_count": {"20.5"}}},
		{"a bleed that is not a number", url.Values{"preset": {"print-6x9"}, "page_thickness_in": {"0.0025"}, "bleed_in": {"lots"}}},
	} {
		if rec := w.s.postForm(t, w.editor, base, c.form); rec.Code != 422 {
			t.Errorf("%s: %d %s", c.name, rec.Code, rec.Body)
		}
	}
}
