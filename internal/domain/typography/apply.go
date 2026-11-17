package typography

import (
	"context"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/db"
	"github.com/crueber/scriobh/internal/domain/account"
)

// Effective is the typography a person gets on a project. The rule, lowest to highest:
//
//	(c) the project's typography applies only where the person has chosen neither of the next two;
//	(a) a person's explicit editor settings (Settings, Editor: typeface, line height, measure) that are not the application's
//	    defaults always win over the project's, because they may be an accessibility choice;
//	(b) the person's own typography override wins over both.
//
// A person who wants a default value in the face of a project pins it in the override. Size composes instead of competing:
// the project's percentage is of the person's own size and can only keep or enlarge it. So a project can never make
// text smaller, tighter or narrower than a reader chose. editor is the person's preferences (account.Prefs, defaults filled
// in): a value is "explicit" when it differs from account.Defaults(), which is also what a Settings form that saves every key at its
// default does not trip.
func Effective(project, person Settings, editor map[string]string) Settings {
	def := account.Defaults()
	if v := editor["editor_font"]; v != "" && v != def["editor_font"] {
		project.Font = ""
	}
	if v := editor["editor_line_height"]; v != "" && v != def["editor_line_height"] {
		project.LineHeight = ""
	}
	if v := editor["editor_measure"]; v != "" && v != def["editor_measure"] {
		project.Measure = 0
	}
	return project.Over(person)
}

// EditorFont is the value for the page's existing data-font attribute when the effective typeface is one of the three
// bundled faces, or "" when it is not set or is a system face (see HTMLAttrs).
func (s Settings) EditorFont() string {
	switch s.Font {
	case "serif", "sans", "mono":
		return s.Font
	}
	return ""
}

// HTMLAttrs is what the page adds to the paper element, as a string that starts with a space and is empty when
// nothing differs from what the application ships. It is built only from the fixed lists above (a value that is not
// on them is skipped), so nothing a client sent can reach the markup: it is the one place a typography value becomes
// HTML, and a fuzz test drives it.
//
//	data-tf  system-serif | system-sans | system-mono
//	data-ts  size percentage, 105 to 140 (100 is the default and is not written)
//	data-tlh line height in hundredths, 130 to 200
//	data-tm  measure in characters
//	data-tp  indent (spacing is the default)
//	data-tb  rule (ornament is the default)
func (s Settings) HTMLAttrs() string {
	var b strings.Builder
	if s.Font != "" && s.EditorFont() == "" && (Settings{Font: s.Font}).Validate() == nil {
		b.WriteString(` data-tf="sys-` + strings.TrimPrefix(s.Font, "system-") + `"`)
	}
	if s.Size > DefaultSize && (Settings{Size: s.Size}).Validate() == nil {
		b.WriteString(` data-ts="` + strconv.Itoa(s.Size) + `"`)
	}
	if n, ok := ParseLH(s.LineHeight); ok {
		b.WriteString(` data-tlh="` + strconv.Itoa(n) + `"`)
	}
	if s.Measure != 0 && (Settings{Measure: s.Measure}).Validate() == nil {
		b.WriteString(` data-tm="` + strconv.Itoa(s.Measure) + `"`)
	}
	if s.Paragraphs == "indent" {
		b.WriteString(` data-tp="indent"`)
	}
	if s.SceneBreak == "rule" {
		b.WriteString(` data-tb="rule"`)
	}
	return b.String()
}

// Load reads the project's typography and the person's override. Anything unreadable is "nothing set", so a page
// always renders.
func Load(ctx context.Context, d *db.DB, userID, projectID string) (project, person Settings) {
	var raw string
	if projectID != "" {
		if d.Read().QueryRowContext(ctx, `SELECT settings_json FROM project_typography WHERE project_id = ?`, projectID).Scan(&raw) == nil {
			project = Decode(raw)
		}
	}
	if userID != "" {
		raw = ""
		if d.Read().QueryRowContext(ctx, `SELECT settings_json FROM user_typography WHERE user_id = ?`, userID).Scan(&raw) == nil {
			person = Decode(raw)
		}
	}
	return
}
