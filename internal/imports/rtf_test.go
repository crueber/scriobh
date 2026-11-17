package imports

import (
	"strings"
	"testing"
)

func rtfText(d *rtfDoc) string {
	var out []string
	for _, p := range d.paras {
		var b strings.Builder
		for _, s := range p.segs {
			switch {
			case s.image:
				b.WriteString("[IMG]")
			case s.note != 0:
				b.WriteString("[^" + itoa(s.note) + "]")
			default:
				if s.priv {
					b.WriteString("{{" + s.text + "}}")
				} else {
					b.WriteString(s.text)
				}
			}
		}
		out = append(out, b.String())
	}
	return strings.Join(out, "|")
}

// rtfSrc writes a backslash as the section sign, so the source of a test can say \uNNNN without a tool
// reading it as an escape.
func rtfSrc(s string) []byte { return []byte(strings.ReplaceAll(s, "§", "\\")) }

func TestRTFBasics(t *testing.T) {
	src := `{§rtf1§ansi§ansicpg1252§cocoartf2639{§fonttbl§f0§fswiss Helvetica;}{§colortbl;§red255§green0§blue0;}` +
		`§pard§f0 Hello §b bold§b0  and §i it§i0 .§par ` +
		`§pard Caf§'e9 §u8220 "quoted§u8221 "? and §uc0§u8217 s §u-10179 §u-8704  emoji§par ` +
		`Line one§line two§tab tabbed§par}`
	d, err := parseRTF(rtfSrc(src))
	if err != nil {
		t.Fatal(err)
	}
	got := rtfText(d)
	want := "Hello bold and it.|Café “quoted”? and ’s 😀 emoji|Line one\ntwo tabbed"
	if got != want {
		t.Errorf("got %q\nwant %q", got, want)
	}
	if !d.paras[0].segs[1].f.b || !d.paras[0].segs[3].f.i {
		t.Errorf("formatting lost: %+v", d.paras[0].segs)
	}
}
