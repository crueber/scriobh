package imports

import (
	"github.com/crueber/scriobh/internal/testutil"
	"strings"
	"testing"
	"time"
)

// A text file that is there and is not read is said, by the document's title;
// a text with a big picture in it is read, picture skipped.
func TestScrivenerTextFilesNotReadAreSaid(t *testing.T) {
	scrivx := `<ScrivenerProject Version="2.0"><Binder><BinderItem UUID="D" Type="DraftFolder"><Title>Draft</Title><Children>` +
		`<BinderItem UUID="A" Type="Text"><Title>Too Big</Title></BinderItem>` +
		`<BinderItem UUID="B" Type="Text"><Title>Has A Picture</Title></BinderItem>` +
		`<BinderItem UUID="C" Type="Text"><Title>Fine</Title></BinderItem>` +
		`</Children></BinderItem></Binder></ScrivenerProject>`
	pic := "{\\rtf1 before {\\pict\\pngblip " + strings.Repeat("0123456789abcdef", 700_000) + "} after}"
	var res *cresult
	var err error
	d, a := measure(func() {
		res, err = scrivenerConvert([]File{
			{Path: "p.scrivx", Data: []byte(scrivx)},
			{Path: "Files/Data/A/content.rtf", Size: 40 << 20}, // listed, not sent: over the limit
			{Path: "Files/Data/B/content.rtf", Data: []byte(pic)},
			{Path: "Files/Data/C/content.rtf", Data: []byte("{\\rtf1 fine}")},
			{Path: "Files/Data/A/content.comments", Data: []byte("<c/>")},
		}, "p", convOpts{})
	})
	if err != nil {
		t.Fatal(err)
	}
	w := strings.Join(res.warnings, "\n")
	if !strings.Contains(w, "1 document(s) have a text file that was not read") || !strings.Contains(w, "Too Big") {
		t.Errorf("the unread file was not said:\n%s", w)
	}
	if !strings.Contains(w, ".comments") || !strings.Contains(w, "comments kept apart") {
		t.Errorf("separate comment files were not said:\n%s", w)
	}
	var byTitle = map[string]string{}
	for _, d := range res.docs {
		byTitle[d.title] = d.body
	}
	if byTitle["Has A Picture"] != "before *\\[image\\]* after\n" && !strings.Contains(byTitle["Has A Picture"], "before") {
		t.Errorf("the text with a picture: %q", byTitle["Has A Picture"])
	}
	if !strings.Contains(byTitle["Has A Picture"], "after") || byTitle["Fine"] != "fine\n" {
		t.Errorf("bodies: %q", byTitle)
	}
	within(t, "a text with an 11 MB picture", d, a, 3*time.Second, 120)
}

// A project of many items is not quadratic in the number of its files.
func TestScrivenerManyItemsIsLinear(t *testing.T) {
	testutil.Timing(t)
	build := func(n int) []File {
		var b strings.Builder
		b.WriteString(`<ScrivenerProject Version="2.0"><Binder><BinderItem UUID="D" Type="DraftFolder"><Title>Draft</Title><Children>`)
		var fs []File
		for i := 0; i < n; i++ {
			id := "U" + itoa(i)
			b.WriteString(`<BinderItem UUID="` + id + `" Type="Text"><Title>Doc ` + itoa(i) + `</Title></BinderItem>`)
			fs = append(fs, File{Path: "Files/Data/" + id + "/content.png", Data: []byte("x")}) // an item with files and no text
		}
		b.WriteString(`</Children></BinderItem></Binder></ScrivenerProject>`)
		return append(fs, File{Path: "p.scrivx", Data: []byte(b.String())})
	}
	small, big := build(10_000), build(40_000)
	run := func(fs []File) func() {
		return func() {
			if _, err := scrivenerConvert(fs, "p", convOpts{}); err != nil {
				t.Fatal(err)
			}
		}
	}
	ds, _ := best(run(small))
	db, _ := best(run(big))
	t.Logf("10,000 items %v, 40,000 items %v (%.1f times)", ds, db, float64(db)/float64(ds))
	if float64(db)/float64(ds) > 8 {
		t.Errorf("four times the items took %.1f times as long", float64(db)/float64(ds))
	}
}
