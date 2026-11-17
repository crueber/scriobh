package imports

import (
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/text"
)

var reDef = regexp.MustCompile(`(?m)^\[\^[0-9]+\]: `)

// Footnote ids are numbers this program writes; what a note says cannot be a
// definition, a reference, a heading or a private note of its own.
func TestRTFNotesCannotInject(t *testing.T) {
	src := "{§rtf1§ansi Text{§footnote [^7]: injected §par # heading§par %% hidden %% [[Wiki]] §par - item}" +
		" and more{§footnote second}§par [^1]: not a definition§par}"
	md := rtfMD(t, src)
	if n := len(reDef.FindAllString(md, -1)); n != 2 {
		t.Errorf("%d definitions in %q", n, md)
	}
	d := text.Structure(md, text.StructOptions{})
	for _, n := range d.Nodes {
		if n.Kind != text.NPara {
			t.Errorf("block kind %v in %q", n.Kind, md)
		}
	}
	if len(text.WikiLinks(md)) != 0 {
		t.Errorf("a wiki link in %q", md)
	}
	if !strings.Contains(md, `\[^1\]: not a definition`) {
		t.Errorf("a literal definition was not escaped: %q", md)
	}
}

// A binder title cannot make a link to anything but the document it names, and
// a title that could end or change a link makes no link.
func TestScrivenerLinksUseOnlyTitlesThatAreSafe(t *testing.T) {
	scrivx := `<ScrivenerProject Version="2.0"><Binder><BinderItem UUID="D" Type="DraftFolder"><Title>Draft</Title><Children>` +
		`<BinderItem UUID="A" Type="Text"><Title>Good Title</Title></BinderItem>` +
		`<BinderItem UUID="B" Type="Text"><Title>Evil ]] %%hide%% [[x</Title></BinderItem>` +
		`<BinderItem UUID="C" Type="Text"><Title>Twin</Title></BinderItem>` +
		`<BinderItem UUID="E" Type="Text"><Title>Twin</Title></BinderItem>` +
		`<BinderItem UUID="F" Type="Text"><Title>Source</Title></BinderItem>` +
		`</Children></BinderItem></Binder></ScrivenerProject>`
	link := func(id, label string) string {
		return `{§field{§*§fldinst{HYPERLINK "scrivlnk://` + id + `"}}{§fldrslt ` + label + `}}`
	}
	body := "{§rtf1 " + link("A", "good") + " " + link("B", "evil") + " " + link("C", "twin") + " " + link("MISSING", "gone") + " " + link("A?x=1#y", "param") + "§par}"
	res, err := scrivenerConvert([]File{
		{Path: "p.scrivx", Data: []byte(scrivx)},
		{Path: "Files/Data/F/content.rtf", Data: rtfSrc(body)},
	}, "p", convOpts{})
	if err != nil {
		t.Fatal(err)
	}
	var src string
	for _, d := range res.docs {
		if d.title == "Source" {
			src = d.body
		}
	}
	if !strings.Contains(src, "[[Good Title|good]]") || !strings.Contains(src, "[[Good Title|param]]") {
		t.Errorf("a good link was not made: %q", src)
	}
	for _, bad := range []string{"[[Evil", "hide", "[[Twin"} {
		if strings.Contains(src, bad) {
			t.Errorf("%q in %q", bad, src)
		}
	}
	for _, want := range []string{"evil", "twin", "gone"} {
		if !strings.Contains(src, want) {
			t.Errorf("the text %q was lost from %q", want, src)
		}
	}
	links := text.WikiLinks(src)
	if len(links) != 1 || links[0].Target != "Good Title" {
		t.Errorf("links: %+v", links)
	}
}
