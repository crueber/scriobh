package mentions_test

import (
	"reflect"
	"testing"
)

// The longest name wins: when "the Guild" and "the Guild of Lamplighters" are both names, a text that names the
// Guild of Lamplighters links only the longer one, and the shorter one counts only where it stands alone.
func TestTheLongestNameWinsOverAShorterOne(t *testing.T) {
	for _, tc := range []struct {
		name, guild, longer, body string
		want                      map[string]int
	}{
		{"lower case", "the Guild", "the Guild of Lamplighters",
			"They met the Guild of Lamplighters at dawn. Later the Guild rode out.",
			map[string]int{"the Guild of Lamplighters": 1, "the Guild": 1}},
		{"capitalised", "The Guild", "The Guild of Lamplighters",
			"They met The Guild of Lamplighters at dawn. The Guild of Lamplighters wept.",
			map[string]int{"The Guild of Lamplighters": 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			b := newBook(t)
			cid := b.compendium(b.owner, "Guilds")
			b.ent(b.owner, cid, "org", tc.guild)
			b.ent(b.owner, cid, "org", tc.longer)
			pid := b.project(b.owner, "Lamps", cid)
			d := b.doc(b.owner, pid, "One", tc.body)
			if got := b.counts(b.owner, d.ID); !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("counts %v, want %v", got, tc.want)
			}
			// The highlighting agrees with the counts: no span is drawn for the shorter name where the longer one stands.
			for _, m := range b.list(b.owner, d.ID, true).Items {
				if m.Entity.Name == tc.guild && m.Count > 0 && len(m.Spans) != tc.want[tc.guild] {
					t.Errorf("%s has %d spans, want %d", tc.guild, len(m.Spans), tc.want[tc.guild])
				}
			}
		})
	}
}
