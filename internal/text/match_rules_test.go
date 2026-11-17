package text

import (
	"reflect"
	"testing"
)

func TestMatcherLanguageRules(t *testing.T) {
	for _, tc := range []struct {
		name    string
		phrases []string
		text    string
		want    []string
	}{
		{"french and italian elision", []string{"Arthur", "Alba"}, "Le fils d'Arthur et l’Arthur, la luce dell'Alba, Arthur's.",
			[]string{"Arthur=1", "Arthur=1", "Alba=2", "Arthur=1"}},
		{"elision only for the listed prefixes", []string{"Arthur"}, "x'Arthur and z’Arthur and Arthur", []string{"Arthur=1"}},
		{"elision with a longer prefix and a possessive", []string{"Arthur"}, "jusqu'Arthur, qu'Arthur's", []string{"Arthur=1", "Arthur=1"}},
		{"a name that starts with a prefix still matches whole", []string{"d'Arcy"}, "Monsieur d'Arcy came", []string{"d'Arcy=1"}},
		{"hangul particles after a name of two syllables", []string{"민수"}, "민수가 왔다. 민수는 갔다. 민수에서 민수로 민수.", []string{"민수=1", "민수=1", "민수=1", "민수=1", "민수=1"}},
		{"a one-syllable stem is not stripped", []string{"나", "민"}, "나이가 많다. 민가에서", nil},
		{"a hangul particle is dropped only from the last word of a name", []string{"민수 철수"}, "민수가 철수를 and 민수 철수를", []string{"민수 철수=1"}},
		{"a zero-width non-joiner does not split a word", []string{"ab‌cd"}, "ab‌cd and abcd and ab cd", []string{"ab‌cd=1", "abcd=1"}},
		{"turkish dotted and dotless i", []string{"Işık"}, "IŞIK and ışık and Işık and İŞİK and isik", []string{"IŞIK=1", "Işık=1", "İŞİK=1"}},
		{"a single Han character alone", []string{"李"}, "李说 and 说李 and 李，说 and (李) and 李", []string{"李=1", "李=1", "李=1"}},
		{"a two-character Han name inside a compound", []string{"李明"}, "老李明天", []string{"李明=1"}},
		{"bare web addresses are not prose", []string{"Elara"}, "see http://Elara.com/Elara and https://x.org/Elara, www.Elara.org then Elara", []string{"Elara=1"}},
		{"a name in a sentence about a site is still a name", []string{"Elara"}, "Elara posted at http://x.example/ today", []string{"Elara=1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := surfaces(NewMatcher(ph(tc.phrases...)), tc.text)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

// The elided prefix is not part of the match: highlighting starts after the apostrophe.
func TestElisionMatchStartsAfterTheApostrophe(t *testing.T) {
	m := NewMatcher(ph("Arthur"))
	s := "le fils d’Arthur"
	var got string
	m.Find(s, func(h Match) { got = s[h.Start:h.End] })
	if got != "Arthur" {
		t.Errorf("the match covers %q", got)
	}
}

// Links name an entity by its spelling, with none of the guards.
func TestLookupTextIgnoresTheLoneCharacterRule(t *testing.T) {
	m := NewMatcher(ph("李"))
	if c := m.LookupText("李"); len(c) != 1 {
		t.Errorf("%+v", c)
	}
}
