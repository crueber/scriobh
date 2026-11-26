package compile

import (
	"testing"

	"github.com/crueber/scriobh/internal/publish"
)

// A custom preset is changed when its options differ from the built-in it starts from. The stored options of a saved
// preset are every option resolved (an update sends them all), so equal values must not count as a change.
func TestChangedIsJudgedAgainstTheBuiltInItStartsFrom(t *testing.T) {
	base, ok := Builtin(ShunnUS)
	if !ok {
		t.Fatal("shunn-us is not built in")
	}
	cases := []struct {
		name string
		p    Preset
		want bool
	}{
		{"a built-in is never a changed preset", base, false},
		{"a custom preset with the base's options", Preset{ID: "cpr_a", Base: ShunnUS, Options: publish.Merge(base.Options, publish.Options{})}, false},
		{"a custom preset that stores every option, one of them changed", Preset{ID: "cpr_b", Base: ShunnUS, Options: publish.Merge(base.Options, publish.Options{FontSize: 13})}, true},
		{"a value set to the base's own value is not a change", Preset{ID: "cpr_c", Base: ShunnUS, Options: publish.Merge(base.Options, publish.Options{FontSize: 12})}, false},
		{"a switch changed from the base", Preset{ID: "cpr_d", Base: ShunnUS, Options: publish.Merge(base.Options, publish.Options{TOC: publish.True()})}, true},
		{"a different size of font is a change", Preset{ID: "cpr_e", Base: ShunnUS, Options: publish.Merge(base.Options, publish.Options{Font: "Courier New"})}, true},
		{"a base that is not built in is not judged", Preset{ID: "cpr_f", Base: "nope", Options: base.Options}, false},
	}
	for _, c := range cases {
		if got := Changed(c.p); got != c.want {
			t.Errorf("%s: Changed = %v, want %v", c.name, got, c.want)
		}
	}
}
