package publish

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/publish/check"
)

func sum256(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

// A cover with no layout draws what it drew before phase C2. Two things are pinned, taken from the code at the commit before
// that phase and never updated to follow a change (a change to the generated cover is a decision to make, not a number to
// refresh): the digest of the whole file the old two-page writer made (Part "all" still writes it, and it must be the same
// bytes), and the digest of each page's content stream. What changed in C2 is only how many pages a cover has by default:
// the file for the printer is page 1, and the front alone is a file of its own (part "front"), each with the content stream
// it had as a page of the two.
func TestGeneratedCoverIsWhatItWas(t *testing.T) {
	base := CoverSpec{Title: "The Salt Road", Subtitle: "A sub title", Author: "Máire Ní Bhriain", Kind: "novel", Color: "#2F5D50",
		TrimW: 6 * inch, TrimH: 9 * inch, Spine: 750 * 72, Bleed: inch / 8, Language: "en", Identifier: "id-1", InputHash: "h", Modified: time.Date(2026, 11, 1, 9, 0, 0, 0, time.UTC)}
	guides := base
	guides.Guides, guides.Barcode = true, true
	thin := base
	thin.Spine = 150 * 72
	a5 := base
	a5.TrimW, a5.TrimH, a5.Spine = 419528, 595276, 400*72
	spine := func(sp mpt) CoverSpec {
		c := base
		c.Spine = sp
		// what compile always gives: the page count (40, under the generic floor), the generic profile unnamed
		c.Pages = 40
		c.Profile, _ = LookupCoverProfile("")
		return c
	}
	for _, c := range []struct {
		name         string
		cs           CoverSpec
		file         string // the two-page file
		page1, page2 string // the content streams
	}{
		{"plain", base, "cea43e4add3ef646e27ffea226b3698977e164bbe479c06c4808c67ae47a6520", "b2cff689da274dc79d2bce982d57c6d1643262a38d10d800646b7a02dbba4758", "05519e80378a04c5d8ad5499014402050bb709fdf49db395e8ce8543f6dcd5fe"},
		{"guides and barcode", guides, "9ed3cc156e32d2c92d7cb1da598b9d2f24075e16dcff09c779a54d3a1a17fa83", "740a554d81a064944dfb8f29b4a406ed975bfb2292fe25284f6b3d2433d30345", "faf497074dfaebfef570d24c96a6fa6c8e1c9b549867dbff2940829bbefaad3a"},
		{"thin spine", thin, "d5dbdc7c7a8d983c4258bb642087e88e6b19de02ae72438a28d277f674f41dd3", "dcb7470962a0e2423aaad0a3006418cc0fb2fbcc12ad0f3c2525dba1e55b9ede", "05519e80378a04c5d8ad5499014402050bb709fdf49db395e8ce8543f6dcd5fe"},
		{"a5", a5, "a526d41deb8f38063fb51d968a6da19aaef097696bc513a4deaad16c4aa2bb65", "981a0bfe7e3e3416c32a27055aa48dc0dbc82bb20ffb9a4f6636ef414bb53b28", "db4e85f0ea00e8b2fa20cf7e679d5970e827ddab8bb3193bcf34c2c7b96f1085"},
		// spines from a quarter inch to a third (about 100 to 145 pages): where a profile's edge distance would shrink the type
		// and, with 40 pages, its floor would take it off; neither applies to a cover that names no profile
		{"0.25 in", spine(250 * 72), "88c926ba6fc9b34e17d6690fe93782e98db6490a13b4eaf84a2199b8dce18ed3", "81fbf1b0f7baa0f6bf90e4d5645d48090becf926ff011ccaf654541538ba9b12", "05519e80378a04c5d8ad5499014402050bb709fdf49db395e8ce8543f6dcd5fe"},
		{"0.26 in", spine(260 * 72), "28e54cafd778714e09c9f15952b1ee30f8b2b7ee4aa9ca7766390eb1d94549b8", "90fd65e5ccbea1cf4b08ae31b9ba5128231a9b0569dc46d6930f092c88fe3b93", "05519e80378a04c5d8ad5499014402050bb709fdf49db395e8ce8543f6dcd5fe"},
		{"0.30 in", spine(300 * 72), "49fc3851195add7a7a7d7dd67dcfdd584046b2e5d621c352476ed6a123255b2c", "037be1350d12c6a4781b864896a6c4db07b17dc4212c74ac8474e08421ea590c", "05519e80378a04c5d8ad5499014402050bb709fdf49db395e8ce8543f6dcd5fe"},
		{"0.36 in", spine(360 * 72), "8d75433ce7b565bc4e4af825327cc53ed49631d44ac695bbff4dbd75e776635e", "9abe8b8ecd8e83ddf97ec725ea40edea7195838b693c3e67bcaec06d6e53bef1", "05519e80378a04c5d8ad5499014402050bb709fdf49db395e8ce8543f6dcd5fe"},
	} {
		pages := map[int]string{}
		coverPageHook = func(i int, b []byte) { pages[i] = sum256(b) }
		both := c.cs
		both.Part = "all"
		b, _, err := WriteCoverPDF(both)
		if err != nil {
			t.Fatal(err)
		}
		if got := sum256(b); got != c.file {
			t.Errorf("%s: the two-page file changed: %s", c.name, got)
		}
		if pages[1] != c.page1 || pages[2] != c.page2 {
			t.Errorf("%s: a page's content changed: %s %s", c.name, pages[1], pages[2])
		}
		// by default one page, the first, with the content it had
		for _, part := range []string{"", PartCover} {
			pages = map[int]string{}
			one := c.cs
			one.Part = part
			out, _, err := WriteCoverPDF(one)
			if err != nil {
				t.Fatal(err)
			}
			if n := countPages(t, out); n != 1 {
				t.Errorf("%s part %q: %d pages", c.name, part, n)
			}
			if pages[1] != c.page1 {
				t.Errorf("%s part %q: page 1's content is not what it was", c.name, part)
			}
		}
		// the front alone is the old second page: its size, trim box and every line of type, in the places they were (its
		// content stream names its fonts and glyph codes in the order it first uses them, so those differ when it stands
		// alone, and the digest of the two-page file above is what pins the pair)
		front := c.cs
		front.Part = PartFront
		fb, _, err := WriteCoverPDF(front)
		if err != nil {
			t.Fatal(err)
		}
		fd, _ := check.ParsePDF(fb)
		ad, _ := check.ParsePDF(b)
		if len(fd.Pages) != 1 || fd.Pages[0].W != ad.Pages[1].W || fd.Pages[0].H != ad.Pages[1].H || fmt.Sprint(fd.Pages[0].Trim) != fmt.Sprint(ad.Pages[1].Trim) ||
			fmt.Sprint(fd.Pages[0].Lines) != fmt.Sprint(ad.Pages[1].Lines) || len(fd.Pages[0].Fills) != len(ad.Pages[1].Fills) || fd.Pages[0].Strokes != ad.Pages[1].Strokes {
			t.Errorf("%s: the front alone is not the old second page", c.name)
		}
		if _, _, err := WriteCoverPDF(func() CoverSpec { x := c.cs; x.Part = PartProof; return x }()); err != ErrNoProof {
			t.Errorf("%s: a proof of a cover with no layout: %v", c.name, err)
		}
	}
	coverPageHook = nil
}
