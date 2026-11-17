package server

import (
	"context"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/account"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
)

const arabicChapter = "# الطريق الملحي\n\nوصلت *مارا* إلى الميناء في **الليل** مع Scríobh وعادت في 2026.\n\nשלום עולם and הסירות.\n"

// A person's language is a preference; the screens built on the catalogue follow it, English fills any gap, and
// the pseudo-locales (which show a string that is not in the catalogue, or that is cut off) are for a development build.
func TestTheInterfaceFollowsThePersonsLanguage(t *testing.T) {
	w := newShareWorld(t, map[string]string{"SCRIOBH_OIDC_STUB": "1"})
	set := func(c caller, lang string) int {
		return w.s.do(c, "PUT", "/api/v1/me/prefs", `{"items":{"language":"`+lang+`"}}`).Code
	}
	if code := set(w.owner, "xx"); code != 200 {
		t.Fatalf("setting a language: %d", code)
	}
	if code := set(w.owner, "klingon"); code != 422 {
		t.Errorf("a language nobody has: %d", code)
	}
	for _, p := range []string{"/p/shared-book/review", "/p/shared-book/share", "/p/shared-book/people", "/p/shared-book/read/" + w.doc1} {
		rec := w.s.do(w.owner, "GET", p, "")
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, `<html lang="xx"`) || !strings.Contains(body, "⟦") {
			t.Errorf("%s in the pseudo-locale: %d lang=%v pseudo=%v", p, rec.Code, strings.Contains(body, `lang="xx"`), strings.Contains(body, "⟦"))
		}
	}
	// the strings a script is handed are the person's language too, so no English hides in a button
	if body := w.s.do(w.owner, "GET", "/p/shared-book/read/"+w.doc1, "").Body.String(); !strings.Contains(body, "data-msg=") || !strings.Contains(body, "⟦") {
		t.Error("a script's strings are not in the language")
	}
	// including the line the export dialog's script shows after a download that left documents out
	exp := w.s.do(w.owner, "GET", "/ui/projects/"+w.pid+"/export", "").Body.String()
	if i := strings.Index(exp, `data-skipped="`); i < 0 || !strings.Contains(exp[i:i+120], "⟦") {
		t.Error("the export dialog's skipped line is English in another language")
	}
	if code := set(w.owner, "xx-rtl"); code != 200 {
		t.Fatal(code)
	}
	if body := w.s.do(w.owner, "GET", "/p/shared-book/review", "").Body.String(); !strings.Contains(body, `<html lang="xx-rtl" dir="rtl"`) {
		t.Error("the right-to-left pseudo-locale does not mirror the page")
	}
	// English again, and a person who set nothing gets English
	set(w.owner, "en")
	if body := w.s.do(w.owner, "GET", "/p/shared-book/review", "").Body.String(); !strings.Contains(body, `<html lang="en">`) || strings.Contains(body, "⟦") {
		t.Error("English was not restored")
	}
	if body := w.s.do(w.editor, "GET", "/p/shared-book/review", "").Body.String(); !strings.Contains(body, `<html lang="en">`) {
		t.Error("another person's language leaked")
	}
	// a stranger's page follows Accept-Language, in a development build the pseudo-locales too, and stores nothing
	_, tok := w.link(t, `{}`)
	rec := w.pub("GET", "/s/"+tok, "", "Accept", htmlAccept, "Accept-Language", "xx-rtl, en;q=0.5")
	if !strings.Contains(rec.Body.String(), `lang="xx-rtl" dir="rtl"`) || rec.Header().Get("Set-Cookie") != "" {
		t.Errorf("the page of a link in another language: %v", rec.Header())
	}
	if rec := w.pub("GET", "/s/"+tok, "", "Accept", htmlAccept, "Accept-Language", "fr-CA, fr;q=0.8"); !strings.Contains(rec.Body.String(), `<html lang="en">`) {
		t.Error("a language with no catalogue falls back to English")
	}
	// ... and in a production setting the pseudo-locales are not offered to a stranger
	req := httptest.NewRequest("GET", "/s/x", nil)
	req.Header.Set("Accept-Language", "xx, de;q=0.5")
	if lang, _ := sharePageLang(req, false); lang != "en" {
		t.Errorf("a stranger chose a test locale: %q", lang)
	}
	if lang, _ := sharePageLang(req, true); lang != "xx" {
		t.Errorf("a development build does not offer it: %q", lang)
	}
}

// The pseudo-locales are for a development build: in production a person cannot choose one, and one already stored
// (from a stub run, say) is not shown.
func TestTheTestLocalesAreNotOfferedInProduction(t *testing.T) {
	w := newShareWorld(t, nil)
	// the test harness runs on the stub issuer; production is the same server without it
	w.s.Cfg.OIDCStub = false
	w.s.Reg.Env().SetService(account.TestLocalesService, false)
	rec := w.s.do(w.owner, "PUT", "/api/v1/me/prefs", `{"items":{"language":"xx-rtl"}}`)
	if rec.Code != 422 {
		t.Errorf("choosing a test locale in production: %d", rec.Code)
	}
	if _, err := w.s.DB.Writer().Exec(`INSERT OR REPLACE INTO user_prefs(user_id, key, value) VALUES (?, 'language', 'xx-rtl')`, w.owner.userID); err != nil {
		t.Fatal(err)
	}
	if body := w.s.do(w.owner, "GET", "/p/shared-book/review", "").Body.String(); !strings.Contains(body, `<html lang="en">`) || strings.Contains(body, "⟦") {
		t.Error("a stored test locale was applied in production")
	}
}

// A manuscript's direction comes from its language, by the rule the DOCX and EPUB writers use, and every page that
// shows the text honours it; each paragraph then takes its own direction from its first strong character.
func TestManuscriptDirectionIsHonouredWhereTheTextIsShown(t *testing.T) {
	w := newShareWorld(t, nil)
	ar := "ar"
	if _, err := w.s.Reg.Call(context.Background(), w.ownerP, "project.update", projects.UpdateIn{ID: w.pid, Language: &ar}); err != nil {
		t.Fatal(err)
	}
	d := w.callAs(t, w.owner, "doc.get", docs.DocIn{ID: w.doc2}).(docs.Item).Item
	c := arabicChapter
	w.callAs(t, w.owner, "doc.update", docs.UpdateIn{ID: w.doc2, BaseRevision: d.Revision, Content: &c})
	read := w.s.do(w.owner, "GET", "/p/shared-book/read/"+w.doc2, "").Body.String()
	for _, want := range []string{`lang="ar" dir="rtl"`, "وصلت", "<em>مارا</em>", "שלום"} {
		if !strings.Contains(read, want) {
			t.Errorf("the reading view lacks %q", want)
		}
	}
	if write := w.s.do(w.owner, "GET", "/p/shared-book/d/"+w.doc2, "").Body.String(); !strings.Contains(write, `lang="ar" dir="rtl"`) {
		t.Error("the writing view does not take the book's direction")
	}
	// a link's text is the same text: the shown HTML keeps the Arabic and Hebrew whole
	_, tok := w.link(t, `{}`)
	doc := w.pub("GET", "/s/"+tok+"/api/docs/"+w.doc2, "").Body.String()
	if !strings.Contains(doc, "\\u0648\\u0635\\u0644\\u062a") && !strings.Contains(doc, "وصلت") {
		t.Errorf("the shared text lost the Arabic: %.200s", doc)
	}
	// a left-to-right book stays left to right
	en := "en"
	if _, err := w.s.Reg.Call(context.Background(), w.ownerP, "project.update", projects.UpdateIn{ID: w.pid, Language: &en}); err != nil {
		t.Fatal(err)
	}
	if read := w.s.do(w.owner, "GET", "/p/shared-book/read/"+w.doc2, "").Body.String(); !strings.Contains(read, `lang="en" dir="ltr"`) {
		t.Error("an English book is not left to right")
	}
	// the stylesheets let each paragraph choose, and use logical properties
	for _, f := range []string{"../../web/static/css/review.css", "../../web/static/css/share.css", "../../web/static/css/editor.css"} {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(b), "unicode-bidi: plaintext") {
			t.Errorf("%s does not let a paragraph choose its direction", f)
		}
	}
}
