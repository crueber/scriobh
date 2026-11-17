package site

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/text/unicode/norm"

	"github.com/crueber/scriobh/internal/site/check"
)

// Any two files of a site must stay two files on a disk that ignores case and
// normalization: under full case folding of the composed and of the decomposed form.
func TestNoTwoPathsCollideOnACaseInsensitiveDisk(t *testing.T) {
	titles := []string{"ΟΔΟΣ", "οδος", "Οδος", "ὈΔΟΣ", "İstanbul", "istanbul", "ISTANBUL", "ıstanbul", "STRASSE", "Straße", "strasse", "ẞ", "ss",
		"ǅ", "ǆ", "Ǆ", "ﬁsh", "fish", "FISH", "Kelvin K", "Kelvin k", "Å", "Å", "Å", "é", "é", "É", "ᏣᎳᎩ", "ꮳꭲꮃ", "Ⅷ", "ⅷ", "viii"}
	in := sample()
	var es []Entry
	for i, ti := range titles {
		es = append(es, Entry{ID: fmt.Sprintf("n%d", i), Title: ti, Role: RolePost, Published: day(i), Content: "text " + ti, Tags: []string{ti}, Category: ti})
	}
	in.Entries = es
	r := mustBuild(t, in)
	seen := map[string]string{}
	for _, f := range r.Files {
		for _, k := range []string{check.FoldKey(norm.NFC.String(f.Path)), check.FoldKey(norm.NFD.String(f.Path))} {
			if o, ok := seen[k]; ok && o != f.Path {
				t.Errorf("%q and %q are one file on APFS or NTFS", o, f.Path)
			}
			seen[k] = f.Path
		}
	}
	mustCheck(t, r, in.Options)
	root := t.TempDir()
	if _, err := WriteDir(root, "g--abcdef", "prj_g", r.Hash, r.Files); err != nil {
		t.Errorf("WriteDir: %v", err)
	}
}

func TestTheCheckerSeesGreekCaseCollisions(t *testing.T) {
	in := sample()
	r := mustBuild(t, in)
	m := files(r)
	m["posts/οδοσ.html"] = []byte("x")
	m["posts/ΟΔΟΣ.html"] = []byte("x")
	rep := check.Site(m, check.Options{Base: baseOf(in.Options)})
	var got bool
	for _, p := range rep.Problems {
		got = got || strings.Contains(p, "differ only by case")
	}
	if !got {
		t.Errorf("not named: %v", rep.Problems)
	}
	m = files(r)
	m["posts/é.html"] = []byte("x")
	m["posts/é.html"] = []byte("x")
	rep = check.Site(m, check.Options{Base: baseOf(in.Options)})
	got = false
	for _, p := range rep.Problems {
		got = got || strings.Contains(p, "normalization")
	}
	if !got {
		t.Errorf("a normalization collision is not named: %v", rep.Problems)
	}
}

func TestEveryAddressIsEscapedInFeedBodiesWhateverTheBase(t *testing.T) {
	for _, base := range []string{"https://example.org/a&b'c/", `https://example.org/a"b/`, "https://example.org/a<b>/"} {
		in := sample()
		in.Options.BaseURL = base
		in.Options.FeedContent = "full"
		r, err := Build(in)
		if err != nil {
			continue // a base address the options refuse is also fine
		}
		mustCheck(t, r, in.Options)
	}
	in := sample()
	in.Options.BaseURL = "https://example.org/a&b'c/"
	r := mustBuild(t, in)
	feed := string(files(r)["feed.xml"])
	if !strings.Contains(feed, "a&amp;amp;b") && !strings.Contains(feed, "a&amp;b") {
		t.Errorf("the ampersand in the base is not escaped in the feed:\n%s", feed[:min(1500, len(feed))])
	}
}

func TestCodeAndArrowsFollowTheDirection(t *testing.T) {
	in := sample()
	in.Project.Language = "ar"
	in.Options.PostsPerPage = 1
	in.Entries = []Entry{
		{ID: "a", Title: "أول", Role: RolePost, Published: day(0), Content: "نص `x <y>` ثم\n\n```\ncode()\n```\n"},
		{ID: "b", Title: "ثان", Role: RolePost, Published: day(1), Content: "نص"},
		{ID: "c", Title: "ثالث", Role: RolePost, Published: day(2), Content: "نص"},
	}
	r := mustBuild(t, in)
	mustCheck(t, r, in.Options)
	m := files(r)
	post := string(m["posts/أول.html"])
	if !strings.Contains(post, `<pre dir="ltr"><code>`) || !strings.Contains(post, `<code dir="ltr">`) {
		t.Errorf("code is not left to right in a right-to-left page:\n%s", post)
	}
	mid := string(m["posts/ثان.html"])
	prev := mid[strings.Index(mid, `rel="prev"`):]
	if !strings.Contains(prev[:strings.Index(prev, "</a>")], "→") {
		t.Errorf("on a right-to-left site the link back points right:\n%s", prev)
	}
	next := mid[strings.Index(mid, `rel="next"`):]
	if !strings.Contains(next[:strings.Index(next, "</a>")], "←") {
		t.Errorf("on a right-to-left site the link forward points left:\n%s", next)
	}
	// And on a left-to-right site they are the usual way round.
	in.Project.Language = "en"
	m = files(mustBuild(t, in))
	mid = string(m["posts/ثان.html"])
	prev = mid[strings.Index(mid, `rel="prev"`):]
	if !strings.Contains(prev[:strings.Index(prev, "</a>")], "←") {
		t.Errorf("left-to-right: the link back points left:\n%s", prev)
	}
	pager := string(m["index.html"])
	if !strings.Contains(pager, `<span dir="auto">Older posts</span>`) {
		t.Errorf("the pager label is not its own span:\n%s", pager)
	}
}

// ---- stable addresses ---------------------------------------------------------------------

func slugOf(r *Result, id string) string {
	for _, p := range r.Posts {
		if p.DocumentID == id {
			return p.Path
		}
	}
	return ""
}

func TestPinnedAddressesStayWhateverArrives(t *testing.T) {
	in := sample()
	in.Entries = []Entry{{ID: "doc_new", Title: "Weekly update", Role: RolePost, Published: day(10), Content: "New."}}
	r1 := mustBuild(t, in)
	pinned := strings.TrimSuffix(strings.TrimPrefix(slugOf(r1, "doc_new"), "posts/"), ".html")

	// An older post with the same title is finished later: without the pin it would take the plain address.
	withOld := sample()
	withOld.Entries = []Entry{{ID: "doc_new", Title: "Weekly update", Role: RolePost, Published: day(10), Content: "New.", PinnedSlug: pinned},
		{ID: "doc_old", Title: "Weekly update", Role: RolePost, Published: day(1), Content: "Older."}}
	r2 := mustBuild(t, withOld)
	if a, b := slugOf(r1, "doc_new"), slugOf(r2, "doc_new"); a != b {
		t.Errorf("a published address moved: %s -> %s", a, b)
	}
	if slugOf(r2, "doc_old") == slugOf(r2, "doc_new") {
		t.Error("two posts share an address")
	}
	unpinned := withOld
	unpinned.Entries = []Entry{{ID: "doc_new", Title: "Weekly update", Role: RolePost, Published: day(10), Content: "New."}, withOld.Entries[1]}
	if slugOf(mustBuild(t, unpinned), "doc_new") == slugOf(r1, "doc_new") {
		t.Error("(the test is not testing anything: without the pin the address moves)")
	}

	// An address chosen by hand does not take a pinned one: the explicit address comes first, so the pin is what the owner must be refused (the op does); here the pin still wins its own document.
	hello := sample()
	hello.Entries = []Entry{{ID: "a1", Title: "Hello", Role: RolePost, Published: day(1), Content: "A.", PinnedSlug: "hello"},
		{ID: "b1", Title: "Other", Slug: "hello", Role: RolePost, Published: day(5), Content: "B."}}
	r3 := mustBuild(t, hello)
	if slugOf(r3, "b1") != "posts/hello.html" {
		t.Errorf("an explicit address is the owner's: %s", slugOf(r3, "b1"))
	}
	var warned bool
	for _, w := range r3.Warnings {
		warned = warned || (w.Code == "slug_suffixed" && w.DocumentID == "a1")
	}
	if !warned {
		t.Error("moving a pinned address is not said")
	}

	// A reserved address (a post that is a draft for now) is not taken by a newcomer.
	res := sample()
	res.Reserved = []string{"weekly-update"}
	res.Entries = []Entry{{ID: "x", Title: "Weekly update", Role: RolePost, Published: day(1), Content: "x"}}
	if slugOf(mustBuild(t, res), "x") == "posts/weekly-update.html" {
		t.Error("a newcomer took a reserved address")
	}
	for _, p := range r2.Posts {
		if p.DocumentID == "doc_new" && p.SlugFrom != "pinned" {
			t.Errorf("slug_from %q", p.SlugFrom)
		}
	}
}

// ---- the folder ---------------------------------------------------------------------------------

func TestEditedPublishedFilesAreNotReplacedByAccident(t *testing.T) {
	root := t.TempDir()
	r := mustBuild(t, sample())
	if _, err := WriteDir(root, "p--abcdef", "prj_x", r.Hash, r.Files); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(root, "p--abcdef", "public", "index.html")
	if err := os.WriteFile(p, []byte("HAND EDITED"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := WriteDir(root, "p--abcdef", "prj_x", r.Hash, r.Files)
	var ed *ErrEdited
	if !errors.As(err, &ed) || ed.Paths[0] != "index.html" {
		t.Fatalf("a hand-edited file: %v", err)
	}
	if b, _ := os.ReadFile(p); string(b) != "HAND EDITED" {
		t.Fatal("a refused publish changed the file")
	}
	if _, err := WriteDirWith(root, "p--abcdef", "prj_x", r.Hash, r.Files, DirOptions{OverwriteEdited: true}); err != nil {
		t.Fatal(err)
	}
	if b, _ := os.ReadFile(p); bytes.Contains(b, []byte("HAND EDITED")) {
		t.Error("overwrite_edited did not replace it")
	}
	// An untouched site is replaced without a word, and a removal takes edited files too (the project is gone).
	if _, err := WriteDir(root, "p--abcdef", "prj_x", r.Hash, r.Files); err != nil {
		t.Fatalf("an untouched publish: %v", err)
	}
	_ = os.WriteFile(p, []byte("EDITED AGAIN"), 0o644)
	if err := RemoveDir(root, "p--abcdef", "prj_x"); err != nil {
		t.Errorf("removal of an edited site: %v", err)
	}
}

func TestZipDatesStayInsideWhatAZipCanHold(t *testing.T) {
	r := mustBuild(t, sample())
	for _, when := range []time.Time{time.Date(9999, 12, 31, 0, 0, 0, 0, time.UTC), time.Date(2108, 1, 1, 0, 0, 0, 0, time.UTC), time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC), {}} {
		a, err := Zip(r.Files, when)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := Zip(r.Files, when)
		if !bytes.Equal(a, b) {
			t.Errorf("not deterministic for %v", when)
		}
	}
}
