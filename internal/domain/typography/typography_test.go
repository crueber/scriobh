package typography

import (
	"math/rand"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/account"
	"github.com/crueber/scriobh/internal/ops"
)

// Every field's bounds, one row at a time: what is accepted, what is refused, and that the refusal names the field.
func TestValidationTable(t *testing.T) {
	type row struct {
		name  string
		s     Settings
		field string // "" means accepted
	}
	var rows []row
	add := func(name string, s Settings, field string) { rows = append(rows, row{name, s, field}) }
	add("nothing set", Settings{}, "")
	for _, f := range Fonts {
		add("font "+f, Settings{Font: f}, "")
	}
	for _, f := range []string{"Serif", "serif ", "Georgia", "system", "url(x)", "serif;}", "newsreader", "inherit", "\x00"} {
		add("font "+strconv.Quote(f), Settings{Font: f}, "font")
	}
	for n := -10; n <= 200; n++ {
		ok := n >= 100 && n <= 140 && n%5 == 0
		if n == 0 {
			ok = true // zero is "not set"
		}
		field := "size"
		if ok {
			field = ""
		}
		add("size "+strconv.Itoa(n), Settings{Size: n}, field)
	}
	for n := 100; n <= 220; n++ {
		s := strconv.FormatFloat(float64(n)/100, 'f', -1, 64)
		ok := n >= 130 && n <= 200 && n%5 == 0
		field := "line_height"
		if ok {
			field = ""
		}
		add("line height "+s, Settings{LineHeight: s}, field)
	}
	for _, s := range []string{"1.30", "1.3 ", " 1.3", "1,3", "2.0", "02", "1.3e0", "1.325", "calc(1)", "1.5;}", "NaN", "+1.5", "1.50"} {
		add("line height "+strconv.Quote(s), Settings{LineHeight: s}, "line_height")
	}
	for n := -1; n <= 100; n++ {
		ok := n == 0
		for _, m := range Measures {
			ok = ok || m == n
		}
		field := "measure"
		if ok {
			field = ""
		}
		add("measure "+strconv.Itoa(n), Settings{Measure: n}, field)
	}
	for _, p := range []string{"spacing", "indent", ""} {
		add("paragraphs "+p, Settings{Paragraphs: p}, "")
	}
	for _, p := range []string{"Indent", "hanging", "indent;", "block"} {
		add("paragraphs "+p, Settings{Paragraphs: p}, "paragraphs")
	}
	for _, p := range []string{"ornament", "rule", ""} {
		add("scene break "+p, Settings{SceneBreak: p}, "")
	}
	for _, p := range []string{"Rule", "fleuron", "dinkus", "* * *"} {
		add("scene break "+p, Settings{SceneBreak: p}, "scene_break")
	}
	for _, r := range rows {
		err := r.s.Validate()
		switch {
		case r.field == "" && err != nil:
			t.Errorf("%s: refused: %v", r.name, err)
		case r.field != "" && err == nil:
			t.Errorf("%s: accepted", r.name)
		case r.field != "" && ops.AsError(err).Field != r.field:
			t.Errorf("%s: refused on %q, want %q", r.name, ops.AsError(err).Field, r.field)
		}
	}
	// the readable minimums: no size below the reader's own, no line height below 1.3, no measure above 80
	if (Settings{Size: 95}).Validate() == nil || (Settings{LineHeight: "1.25"}).Validate() == nil || (Settings{Measure: 90}).Validate() == nil {
		t.Fatal("a value below a readable minimum was accepted")
	}
	if len(Sizes()) != 9 || len(LineHeights()) != 15 || len(Measures) != 9 {
		t.Fatalf("the lists are %d, %d and %d long", len(Sizes()), len(LineHeights()), len(Measures))
	}
}

var allowedAttrs = regexp.MustCompile(`^(?: data-(?:tf="sys-(?:serif|sans|mono)"|ts="1[0-4][05]"|tlh="(?:1[3-9][05]|200)"|tm="(?:40|45|50|55|60|66|70|75|80)"|tp="indent"|tb="rule"))*$`)

func randomString(r *rand.Rand) string {
	pool := []string{"}", "{", "url(", "expression(", ";", "\"", "'", "<", ">", "\\", "/*", "*/", "@import", "data-x=", " ", "\n", "serif", "system-mono", "1.5", "110", "indent", "rule", "javascript:", "\x00", "ü"}
	var b strings.Builder
	for i, n := 0, r.Intn(6); i < n; i++ {
		b.WriteString(pool[r.Intn(len(pool))])
	}
	return b.String()
}

// The one place a value becomes HTML: whatever Settings holds (valid, invalid, hostile), what comes out is only the
// documented attributes with values from the fixed lists. There is no way to get a quote, a brace, a URL or a
// function into the page, because there is no path from a string to the output except through a list.
func TestHTMLAttrsOnlyEverCarryAllowListedValues(t *testing.T) {
	r := rand.New(rand.NewSource(85))
	check := func(s Settings) {
		t.Helper()
		out := s.HTMLAttrs()
		if !allowedAttrs.MatchString(out) {
			t.Fatalf("%+v produced %q", s, out)
		}
		for _, bad := range []string{"}", "url(", "expression(", "<", ">", ";", "javascript", "@import"} {
			if strings.Contains(out, bad) {
				t.Fatalf("%+v produced %q containing %q", s, out, bad)
			}
		}
		// Clean and Over keep that property
		if o := (Settings{}).Over(s).Clean().HTMLAttrs(); !allowedAttrs.MatchString(o) {
			t.Fatalf("clean: %q", o)
		}
	}
	for i := 0; i < 20000; i++ {
		check(Settings{Font: randomString(r), Size: r.Intn(400) - 100, LineHeight: randomString(r), Measure: r.Intn(200) - 20, Paragraphs: randomString(r), SceneBreak: randomString(r)})
	}
	// and every valid combination of each field alone produces exactly its attribute
	for _, f := range Fonts {
		check(Settings{Font: f})
	}
	for _, n := range Sizes() {
		check(Settings{Size: n})
	}
	for _, s := range LineHeights() {
		check(Settings{LineHeight: s})
	}
	for _, m := range Measures {
		check(Settings{Measure: m})
	}
	if (Settings{Font: "serif", Size: 100, Paragraphs: "spacing", SceneBreak: "ornament"}).HTMLAttrs() != "" {
		t.Fatal("the shipped defaults wrote an attribute")
	}
	if (Settings{}).HTMLAttrs() != "" {
		t.Fatal("nothing set wrote an attribute")
	}
}

// Every value on a list has a rule in the stylesheet, and the stylesheet has no rule for a value that is not on one.
func TestTheStylesheetHasARuleForEveryValueAndNoOther(t *testing.T) {
	b, err := os.ReadFile("../../../web/static/css/typography.css")
	if err != nil {
		t.Fatal(err)
	}
	css := string(b)
	for _, bad := range []string{"url(", "@import", "@font-face", "gradient", "expression(", "!important"} {
		if strings.Contains(css, bad) {
			t.Errorf("typography.css contains %q", bad)
		}
	}
	if !strings.Contains(css, "@media screen") {
		t.Error("the rules are not screen-only")
	}
	has := func(sel string) {
		t.Helper()
		if !strings.Contains(css, sel) {
			t.Errorf("no rule for %s", sel)
		}
	}
	for _, f := range []string{"serif", "sans", "mono"} {
		has(`[data-tf="sys-` + f + `"]`)
	}
	for _, n := range Sizes() {
		if n > DefaultSize {
			has(`[data-ts="` + strconv.Itoa(n) + `"]{--ts:` + strconv.FormatFloat(float64(n)/100, 'f', -1, 64) + `}`)
		}
	}
	for n := MinLineHeight; n <= MaxLineHeight; n += LineStep {
		has(`[data-tlh="` + strconv.Itoa(n) + `"]{--lh-prose:calc(var(--prose)*` + FormatLH(n) + `)}`)
	}
	for _, m := range Measures {
		has(`[data-tm="` + strconv.Itoa(m) + `"]{--measure:` + strconv.Itoa(m) + `ch}`)
	}
	// the selectors name elements the templates really draw: the reading view's scene separator is an hr that is a sibling of
	// .r-prose, and the writing view's plain-text stand-in is .doc-plain
	read, err := os.ReadFile("../../../web/templates/partials/read.html")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(read), `<hr class="scene-break">`) || !strings.Contains(css, ".r-sec > .scene-break") {
		t.Error("the reading view's scene separator is not reached by the rule")
	}
	write, err := os.ReadFile("../../../web/templates/write.html")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(write), `id="doc-plain"`) && !strings.Contains(css, ".doc-plain") {
		t.Error("the size rule does not reach the writing view's plain-text stand-in")
	}
	has(`[data-tp="indent"]`)
	has(`[data-tb="rule"]`)
	// no attribute value in the sheet that the server cannot write
	for _, m := range regexp.MustCompile(`\[data-(t[a-z]+)="([^"]+)"\]`).FindAllStringSubmatch(css, -1) {
		out := ""
		switch m[1] {
		case "tf":
			out = (Settings{Font: strings.Replace(m[2], "sys-", "system-", 1)}).HTMLAttrs()
		case "ts":
			n, _ := strconv.Atoi(m[2])
			out = (Settings{Size: n}).HTMLAttrs()
		case "tlh":
			n, _ := strconv.Atoi(m[2])
			out = (Settings{LineHeight: FormatLH(n)}).HTMLAttrs()
		case "tm":
			n, _ := strconv.Atoi(m[2])
			out = (Settings{Measure: n}).HTMLAttrs()
		case "tp":
			out = (Settings{Paragraphs: m[2]}).HTMLAttrs()
		case "tb":
			out = (Settings{SceneBreak: m[2]}).HTMLAttrs()
		}
		if !strings.Contains(out, ` data-`+m[1]+`="`+m[2]+`"`) {
			t.Errorf("the stylesheet reads [data-%s=%q], which the server never writes (it writes %q)", m[1], m[2], out)
		}
	}
}

func TestLayersAndDecoding(t *testing.T) {
	project := Settings{Font: "mono", Size: 120, Measure: 55}
	person := Settings{Size: 105, Paragraphs: "indent"}
	got := Effective(project, person, account.Defaults())
	if got != (Settings{Font: "mono", Size: 105, Measure: 55, Paragraphs: "indent"}) {
		t.Fatalf("effective: %+v", got)
	}
	if got := Decode(`{"font":"mono","size":99,"measure":66,"extra":1}`); got != (Settings{Font: "mono", Measure: 66}) {
		t.Fatalf("a stored row with a value that is no longer valid keeps the rest: %+v", got)
	}
	for _, raw := range []string{"", "null", "[]", "{", `"x"`, "\x00"} {
		if !Decode(raw).IsZero() {
			t.Errorf("Decode(%q) is not empty", raw)
		}
	}
	if Effective(Settings{}, Settings{}, account.Defaults()).HTMLAttrs() != "" {
		t.Fatal("no layer, no attribute")
	}
	if Effective(Settings{Font: "serif"}, Settings{}, account.Defaults()).EditorFont() != "serif" || (Settings{Font: "system-sans"}).EditorFont() != "" {
		t.Fatal("editor font")
	}
}

// The precedence, every combination: for each of the three fields a person can also set in Settings (typeface, line height,
// measure), the project's typography set or not, the person's editor value at its default or not, and their own typography
// override set or not.
//
//	override set                          -> the override
//	else editor value is not the default  -> nothing from the project (the person's editor value is what is drawn)
//	else project set                      -> the project's
//	else                                  -> nothing
//
// Size is not part of it: it composes (the project's percentage of the person's own size) and is checked last.
func TestPrecedenceEveryCombination(t *testing.T) {
	type field struct {
		name, editorKey, nonDefault string
		project, override           Settings
		get                         func(Settings) any
	}
	fields := []field{
		{"font", "editor_font", "sans", Settings{Font: "mono"}, Settings{Font: "system-serif"}, func(s Settings) any { return s.Font }},
		{"line height", "editor_line_height", "1.9", Settings{LineHeight: "1.3"}, Settings{LineHeight: "1.5"}, func(s Settings) any { return s.LineHeight }},
		{"measure", "editor_measure", "full", Settings{Measure: 80}, Settings{Measure: 55}, func(s Settings) any { return s.Measure }},
	}
	for _, f := range fields {
		for _, projectSet := range []bool{false, true} {
			for _, nonDefault := range []bool{false, true} {
				for _, overrideSet := range []bool{false, true} {
					editor := account.Defaults()
					if nonDefault {
						editor[f.editorKey] = f.nonDefault
					}
					var project, override Settings
					if projectSet {
						project = f.project
					}
					if overrideSet {
						override = f.override
					}
					got := f.get(Effective(project, override, editor))
					var want any
					switch {
					case overrideSet:
						want = f.get(f.override)
					case nonDefault:
						want = f.get(Settings{})
					case projectSet:
						want = f.get(f.project)
					default:
						want = f.get(Settings{})
					}
					if got != want {
						t.Errorf("%s: project %v, editor non-default %v, override %v: got %v, want %v", f.name, projectSet, nonDefault, overrideSet, got, want)
					}
				}
			}
		}
	}
	// the other fields of the project are untouched by a person's editor settings, and size composes
	editor := account.Defaults()
	editor["editor_font"], editor["editor_line_height"], editor["editor_measure"] = "sans", "1.9", "full"
	got := Effective(Settings{Font: "mono", LineHeight: "1.3", Measure: 80, Size: 130, Paragraphs: "indent", SceneBreak: "rule"}, Settings{}, editor)
	if got != (Settings{Size: 130, Paragraphs: "indent", SceneBreak: "rule"}) {
		t.Fatalf("a person's three editor settings keep their own and the project still sets the rest: %+v", got)
	}
	// a value the Settings form saved at its default is not a choice
	editor = account.Defaults()
	if got := Effective(Settings{LineHeight: "1.3"}, Settings{}, editor); got.LineHeight != "1.3" {
		t.Fatal("a default editor value blocked the project")
	}
	// a project can never go below a reader's choice: nothing a project sets reaches line height, measure or typeface when the
	// reader chose them, and its size can only enlarge
	for _, n := range Sizes() {
		if n < 100 {
			t.Fatalf("a size below the reader's own: %d", n)
		}
	}
}
