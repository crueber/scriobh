package server

import (
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/testutil"
)

// "Back to writing" on a Compendium page, and the Compendium tab that goes to the Compendium (F-1264, F-1265).

// backEnv is a Compendium, its owner and two of the owner's books that both use it, each with a chapter.
type backEnv struct {
	entEnv
	other, otherSlug string
	docA, docB       string
}

func newBackEnv(t *testing.T) backEnv {
	t.Helper()
	b := backEnv{entEnv: newEntEnv(t)}
	b.docA = b.call(t, "doc.create", docs.CreateIn{ProjectID: b.pid, Title: "The arrival", Kind: "chapter"}).(docs.Item).Item.ID
	po := b.call(t, "project.create", projects.CreateIn{Title: "The Second Road"}).(projects.Item).Item
	b.other, b.otherSlug = po.ID, po.Slug
	b.call(t, "project.compendium.set", compendiums.SetIn{ID: b.other, CompendiumID: b.cid})
	b.docB = b.call(t, "doc.create", docs.CreateIn{ProjectID: b.other, Title: "The crossing", Kind: "chapter"}).(docs.Item).Item.ID
	return b
}

func (b backEnv) open(t *testing.T, pid, doc string) {
	t.Helper()
	b.call(t, "project.open", projects.OpenIn{ID: pid, DocumentID: doc})
	time.Sleep(2 * time.Millisecond) // two opens are told apart by the time of the later
}

func (b backEnv) page(c caller, path string) (int, string) {
	rec := b.s.do(c, "GET", path, "")
	return rec.Code, rec.Body.String()
}

func backLine(body string) string { return between(body, `<p class="comp-back">`, "</p>") }

func TestCompendiumPageGoesBackToTheBookMostRecentlyWrittenAndItsLastChapter(t *testing.T) {
	b := newBackEnv(t)
	if _, body := b.page(b.owner, "/c/"+b.slug); strings.Contains(body, "comp-back") {
		t.Errorf("a person who has opened no book is shown a way back: %s", backLine(body))
	}
	b.open(t, b.pid, b.docA)
	b.open(t, b.other, b.docB)
	code, body := b.page(b.owner, "/c/"+b.slug)
	line := backLine(body)
	if code != 200 || !strings.Contains(line, "Back to The Second Road, The crossing") || !strings.Contains(line, `href="/p/`+b.otherSlug+`/d/`+b.docB+`#resume"`) {
		t.Fatalf("the book opened last: %d %q", code, line)
	}
	b.open(t, b.pid, b.docA)
	for _, path := range []string{"/c/" + b.slug, "/c/" + b.slug + "/timeline", "/c/" + b.slug + "/e/" + b.maire} {
		code, body = b.page(b.owner, path)
		line = backLine(body)
		if code != 200 || !strings.Contains(line, "Back to The Salt Road, The arrival") || !strings.Contains(line, `href="/p/`+b.pslug+`/d/`+b.docA+`#resume"`) {
			t.Errorf("%s after the other book was opened: %d %q", path, code, line)
		}
	}
}

func TestCompendiumPageBackSkipsATrashedBookAndNamesAnUntitledChapter(t *testing.T) {
	b := newBackEnv(t)
	untitled := b.call(t, "doc.create", docs.CreateIn{ProjectID: b.other, Title: "", Kind: "scene"}).(docs.Item).Item.ID
	b.open(t, b.pid, b.docA)
	b.open(t, b.other, untitled)
	if _, body := b.page(b.owner, "/c/"+b.slug); !strings.Contains(backLine(body), "Back to The Second Road, Untitled") {
		t.Errorf("an untitled chapter: %q", backLine(body))
	}
	b.call(t, "project.trash", projects.StateIn{ID: b.other})
	if _, body := b.page(b.owner, "/c/"+b.slug); !strings.Contains(backLine(body), "Back to The Salt Road, The arrival") || strings.Contains(backLine(body), "The Second Road") {
		t.Errorf("a trashed book is skipped: %q", backLine(body))
	}
	b.call(t, "project.trash", projects.StateIn{ID: b.pid})
	if _, body := b.page(b.owner, "/c/"+b.slug); strings.Contains(body, "comp-back") {
		t.Errorf("both books in the trash: %q", backLine(body))
	}
}

func TestCompendiumPageBackIsNotOnFragmentsNorMadeFromTheRequest(t *testing.T) {
	b := newBackEnv(t)
	b.open(t, b.pid, b.docA)
	// What the request says never reaches the link: not a query, not the page it came from, not a header.
	req := httptest.NewRequest("GET", "/c/"+b.slug+"?from="+b.otherSlug+"&project="+b.other+"&back=https://example.com/x", nil)
	req.AddCookie(b.owner.cookie)
	req.Header.Set("Referer", "http://example.com/p/"+b.otherSlug+"/d/"+b.docB)
	req.Header.Set("X-Forwarded-Host", "example.com")
	rec := httptest.NewRecorder()
	b.s.Handler().ServeHTTP(rec, req)
	line := backLine(rec.Body.String())
	if !strings.Contains(line, `href="/p/`+b.pslug+`/d/`+b.docA+`#resume"`) || strings.Contains(line, "example.com") || strings.Contains(line, b.otherSlug) {
		t.Errorf("the link follows the request: %q", line)
	}
}

func TestCompendiumTabOpensTheCompendiumOrTheWiringPage(t *testing.T) {
	b := newBackEnv(t)
	where := func(c caller, slug string) (int, string) {
		rec := b.s.do(c, "GET", "/p/"+slug+"/compendium/open", "")
		return rec.Code, rec.Header().Get("Location")
	}
	if code, loc := where(b.owner, b.pslug); code != http.StatusSeeOther || loc != "/c/"+b.slug+"/entities" {
		t.Errorf("an owner of the book and the Compendium: %d %q", code, loc)
	}
	// The wiring page keeps its address and is reached from the project menu.
	if code, body := b.page(b.owner, "/p/"+b.pslug+"/compendium"); code != 200 || !strings.Contains(body, "Compendium") {
		t.Errorf("the wiring page: %d", code)
	}
	_, body := b.page(b.owner, "/p/"+b.pslug+"/outline")
	if n := strings.Count(body, `href="/p/`+b.pslug+`/compendium/open"`); n < 2 {
		t.Errorf("the strip's tab and the phone's Bible go to the Compendium: %d links", n)
	}
	if !strings.Contains(body, `href="/p/`+b.pslug+`/compendium"`) || !strings.Contains(body, "Compendium settings") {
		t.Errorf("the project menu keeps the wiring page: %.200s", between(body, "Compendium settings", "</li>"))
	}
	// A book with a primary the caller is not on: the wiring page, which says why.
	if code, loc := where(b.projectOnly, b.pslug); code != http.StatusSeeOther || loc != "/p/"+b.pslug+"/compendium" {
		t.Errorf("a member of the book who is not on the Compendium: %d %q", code, loc)
	}
	// A book with no Compendium: the wiring page.
	b.call(t, "project.compendium.set", compendiums.SetIn{ID: b.other, CompendiumID: ""})
	if code, loc := where(b.owner, b.otherSlug); code != http.StatusSeeOther || loc != "/p/"+b.otherSlug+"/compendium" {
		t.Errorf("a book with no Compendium: %d %q", code, loc)
	}
	// A reader of the Compendium who reads the book is taken in; a stranger to the book is told nothing.
	b.s.addMember(t, b.pid, b.reader.userID, ops.ProjectReader)
	if code, loc := where(b.reader, b.pslug); code != http.StatusSeeOther || loc != "/c/"+b.slug+"/entities" {
		t.Errorf("a reader of both: %d %q", code, loc)
	}
	if code, loc := where(b.stranger, b.pslug); code != http.StatusNotFound || loc != "" {
		t.Errorf("a stranger to the book: %d %q", code, loc)
	}
	// The overview of the Compendium links each book to its wiring page.
	if _, body = b.page(b.owner, "/c/"+b.slug); !strings.Contains(body, `href="/p/`+b.pslug+`/compendium">Settings`) {
		t.Errorf("Settings beside each project of the overview: %.300s", between(body, "Projects that use it", "</ul>"))
	}
}

// With 50 books that use the Compendium, the way back (compendium.get, project.list, one read of the caller's own state and one chapter's
// title) adds at most 10 ms (p95) to a Compendium page. It is timed where a page calls it, on a request that carries the session.
func TestCompendiumPageBackCostsLittleWithFiftyBooks(t *testing.T) {
	testutil.Timing(t)
	b := newBackEnv(t)
	for i := 0; i < 48; i++ {
		po := b.call(t, "project.create", projects.CreateIn{Title: "Book " + strconv.Itoa(i)}).(projects.Item).Item
		b.call(t, "project.compendium.set", compendiums.SetIn{ID: po.ID, CompendiumID: b.cid})
		d := b.call(t, "doc.create", docs.CreateIn{ProjectID: po.ID, Title: "Chapter", Kind: "chapter"}).(docs.Item).Item.ID
		b.call(t, "project.open", projects.OpenIn{ID: po.ID, DocumentID: d})
	}
	var took []time.Duration
	var got *compBack
	b.s.mux.HandleFunc("GET /ui/zzback", func(w http.ResponseWriter, r *http.Request) {
		t0 := time.Now()
		got = b.s.compBackTo(r, b.cid)
		took = append(took, time.Since(t0))
	})
	for i := 0; i < 30; i++ {
		b.s.do(b.owner, "GET", "/ui/zzback", "")
	}
	if got == nil {
		t.Fatal("no way back was made")
	}
	sort.Slice(took, func(i, j int) bool { return took[i] < took[j] })
	p95 := took[len(took)*95/100-1]
	t.Logf("compBackTo p95 with 50 books: %v", p95)
	if p95 > 10*time.Millisecond {
		t.Errorf("the way back costs %v (p95), want at most 10 ms", p95)
	}
}
