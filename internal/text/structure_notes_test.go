package text

import (
	"strings"
	"testing"
)

// A note referenced twice becomes two notes, one for each reference: a page and
// a Word footnote cannot point at one note from two places.
func TestStructureCopiesANoteForEachReference(t *testing.T) {
	d := Structure("One[^a] and two[^a] and three[^b].\n\n[^a]: Shared.\n[^b]: Own.\n[^c]: Orphan.\n", StructOptions{})
	var nums []int
	for _, n := range d.Notes {
		nums = append(nums, n.Num)
	}
	if len(d.Notes) != 4 || nums[0] != 1 || nums[1] != 2 || nums[2] != 3 || nums[3] != 4 {
		t.Fatalf("notes %v", nums)
	}
	if got := PlainText(d.Notes[0].Nodes) + PlainText(d.Notes[1].Nodes); got != "Shared.Shared." {
		t.Errorf("copies: %q", got)
	}
	refs := 0
	for _, in := range d.Nodes[0].Inl {
		if in.Kind == INote {
			refs++
			if in.Num != refs {
				t.Errorf("reference %d numbered %d", refs, in.Num)
			}
		}
	}
	if refs != 3 {
		t.Errorf("references: %d", refs)
	}
}

func TestStructureBoundsNoteCopies(t *testing.T) {
	big := strings.Repeat("word ", 200_000)
	md := strings.Repeat("x[^a]", 50_000) + "\n\n[^a]: " + big + "\n"
	d := Structure(md, StructOptions{})
	total := 0
	for _, n := range d.Notes {
		total += len(PlainText(n.Nodes))
	}
	if total > 40<<20 {
		t.Errorf("%d bytes of note copies", total)
	}
	// A note that refers to itself does not recurse.
	d = Structure("a[^a]\n\n[^a]: loops back to [^a] and [^b].\n[^b]: other.\n", StructOptions{})
	if len(d.Notes) < 1 {
		t.Error("no notes")
	}
}
