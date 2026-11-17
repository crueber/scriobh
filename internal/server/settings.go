package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/domain/account"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
)

// Settings: the person's profile, their preferences, and their sessions. Every
// change goes through the account ops; the forms post normally and the server
// redirects back, so the page needs no script (a choice of theme or density
// previews at once when script is available).

type opt struct{ Value, Label string }

// selectField is one <select> of the preferences form.
type selectField struct {
	Name, Label, Hint, Live string
	Options                 []opt
	Current                 string
}

type checkField struct {
	Name, Label, Hint string
	On                bool
}

type sessionRow struct {
	ID, Agent, When string
	Current         bool
}

// themeOpt is one theme of the picker.
type themeOpt struct {
	ID, Label string
	Checked   bool
}

func themeOpts(scheme, current string) []themeOpt {
	var out []themeOpt
	for _, t := range themesOf(scheme) {
		out = append(out, themeOpt{t.ID, t.Label, t.ID == current})
	}
	return out
}

type settingsData struct {
	LightThemes, DarkThemes []themeOpt
	Profile                 account.Profile
	Locales                 []opt
	Look                    []selectField
	Editor                  []selectField
	Checks                  []checkField
	Zones                   []string // suggestions for the time zone box; the op accepts any IANA name
	Sessions                []sessionRow
	Backups                 *backupsView // administrators only
	Prefs                   editorPrefs
	CSRF                    string
	Saved                   string // which section was just saved
	Error                   string // a message from an op, with Field naming the control
	Field                   string
	Others                  int
	// API keys (keys_ui.go)
	CanKeys     bool
	Keys        []keyRow
	KeyProjects []opt
	NewSecret   string // shown once, on the page that answers the create form
	NewKeyName  string
	// the Kindle address and the email digest (mailui.go)
	Mail *mailAccount
	// the progress email and the nudges of the Writing Studio (studioui.go)
	Studio *stSettings
}

func opts(pairs ...string) []opt {
	out := make([]opt, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		out = append(out, opt{pairs[i], pairs[i+1]})
	}
	return out
}

func rangeOpts(lo, hi, step int, suffix string) []opt {
	var out []opt
	for n := lo; n <= hi; n += step {
		out = append(out, opt{strconv.Itoa(n), strconv.Itoa(n) + suffix})
	}
	return out
}

func lineHeightOpts() []opt {
	var out []opt
	for h := 140; h <= 190; h += 5 {
		v := strconv.Itoa(h/100) + "." + strconv.Itoa(h%100/10) + strconv.Itoa(h%10)
		v = strings.TrimSuffix(v, "0")
		out = append(out, opt{v, v})
	}
	return out
}

func (s *Server) settingsView(r *http.Request) (settingsData, error) {
	p := s.principal(r)
	po, err := s.Reg.Call(r.Context(), p, "profile.get", account.EmptyIn{})
	if err != nil {
		return settingsData{}, err
	}
	pr, err := s.Reg.Call(r.Context(), p, "prefs.get", account.EmptyIn{})
	if err != nil {
		return settingsData{}, err
	}
	so, err := s.Reg.Call(r.Context(), p, "session.list", account.EmptyIn{})
	if err != nil {
		return settingsData{}, err
	}
	pv := pr.(account.PrefsOut).Items
	d := settingsData{Profile: po.(account.ProfileItem).Item, CSRF: authFrom(r.Context()).CSRF, Prefs: editorPrefsFrom(pv)}
	for _, l := range account.Locales {
		d.Locales = append(d.Locales, opt{l, localeName(l)})
	}
	d.LightThemes, d.DarkThemes = themeOpts("light", pv["theme_light"]), themeOpts("dark", pv["theme_dark"])
	d.Look = []selectField{
		{Name: "theme", Label: "Colour mode", Live: "theme", Current: pv["theme"], Hint: "Follow the system, or always light or dark.",
			Options: opts("system", "Follow the system", "light", "Light", "dark", "Dark")},
		{Name: "density", Label: "Density", Live: "density", Current: pv["density"], Hint: "Spacing only; type size does not change.",
			Options: opts("comfortable", "Comfortable", "compact", "Compact", "spacious", "Spacious")},
		{Name: "reduced_motion", Label: "Reduced motion", Live: "motion", Current: pv["reduced_motion"], Hint: "Follow the system, or turn motion off or on here.",
			Options: opts("system", "Follow the system", "on", "Reduce motion", "off", "Allow motion")},
	}
	lang := selectField{Name: "language", Label: "Interface language", Current: pv["language"], Hint: i18n.Default.T(pv["language"], "lang.note")}
	for _, l := range i18n.Default.Locales() {
		if l.Test && !s.testLocales() { // the pseudo-locales are for developers
			continue
		}
		lang.Options = append(lang.Options, opt{l.Tag, l.Name})
	}
	d.Look = append(d.Look, lang)
	d.Editor = []selectField{
		{Name: "editor_font", Label: "Font", Current: pv["editor_font"], Options: opts("serif", "Serif (Newsreader)", "sans", "Sans (Instrument Sans)", "mono", "Mono (IBM Plex Mono)")},
		{Name: "editor_size", Label: "Size", Current: pv["editor_size"], Options: rangeOpts(16, 24, 1, " px")},
		{Name: "editor_measure", Label: "Line length", Current: pv["editor_measure"], Hint: "Characters per line.",
			Options: append(rangeOpts(52, 82, 2, " characters"), opt{"full", "Full width"})},
		{Name: "editor_line_height", Label: "Line height", Current: pv["editor_line_height"], Options: lineHeightOpts()},
		{Name: "autosave_ms", Label: "Save after", Current: pv["autosave_ms"], Hint: "How long to wait after you stop typing.",
			Options: opts("300", "0.3 seconds", "500", "0.5 seconds", "700", "0.7 seconds", "1000", "1 second", "2000", "2 seconds", "5000", "5 seconds")},
	}
	d.Checks = []checkField{
		{Name: "show_word_count", Label: "Show word and character counts", Hint: "In the status bar under the page.", On: pv["show_word_count"] == "true"},
		{Name: "typewriter", Label: "Typewriter scrolling", Hint: "Keep the line you are writing in the middle of the screen.", On: pv["typewriter"] == "true"},
		{Name: "focus_dim", Label: "Dim the other paragraphs", Hint: "Only the paragraph you are in stays at full strength.", On: pv["focus_dim"] == "true"},
	}
	d.Zones = commonZones
	d.Backups = s.backupsFor(r)
	loc, err := time.LoadLocation(d.Profile.Timezone)
	if err != nil || d.Profile.Timezone == "" {
		loc = time.UTC
	}
	for _, x := range so.(account.SessionsOut).Items {
		d.Sessions = append(d.Sessions, sessionRow{ID: x.ID, Agent: friendlyAgent(x.UserAgent), Current: x.Current, When: sessionWhen(x.LastSeenAt, loc)})
		if !x.Current {
			d.Others++
		}
	}
	s.keysView(r, &d)
	d.Mail = s.mailAccountView(r)
	d.Studio = s.stSettingsView(r)
	return d, nil
}

func localeName(l string) string {
	switch l {
	case "en":
		return "English"
	case "ga":
		return "Gaeilge"
	case "fr":
		return "Français"
	case "de":
		return "Deutsch"
	case "es":
		return "Español"
	case "it":
		return "Italiano"
	case "pt":
		return "Português"
	case "nl":
		return "Nederlands"
	}
	return l
}

// friendlyAgent names the browser and system from a User-Agent string, roughly.
func friendlyAgent(ua string) string {
	br := "Browser"
	switch {
	case strings.Contains(ua, "Firefox/"):
		br = "Firefox"
	case strings.Contains(ua, "Edg/"):
		br = "Edge"
	case strings.Contains(ua, "Chrome/"), strings.Contains(ua, "Chromium/"):
		br = "Chrome"
	case strings.Contains(ua, "Safari/"):
		br = "Safari"
	}
	os := ""
	switch {
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		os = "iOS"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "Mac OS X"), strings.Contains(ua, "Macintosh"):
		os = "macOS"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	if ua == "" {
		return "Unknown device"
	}
	if os != "" {
		return br + " on " + os
	}
	return br
}

// sessionWhen writes a time in the person's own zone (their profile's), not the server's.
func sessionWhen(ts string, loc *time.Location) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil {
		return ts
	}
	return "Last used " + t.In(loc).Format("2 Jan 2006, 15:04")
}

// commonZones are offered as suggestions; any name time.LoadLocation knows is accepted.
var commonZones = []string{"UTC", "Europe/Dublin", "Europe/London", "Europe/Lisbon", "Europe/Paris", "Europe/Berlin", "Europe/Madrid", "Europe/Rome", "Europe/Amsterdam",
	"Europe/Brussels", "Europe/Zurich", "Europe/Vienna", "Europe/Stockholm", "Europe/Oslo", "Europe/Copenhagen", "Europe/Helsinki", "Europe/Warsaw", "Europe/Prague",
	"Europe/Athens", "Europe/Istanbul", "Europe/Kyiv", "Europe/Moscow", "Africa/Cairo", "Africa/Lagos", "Africa/Nairobi", "Africa/Johannesburg", "Asia/Dubai",
	"Asia/Tehran", "Asia/Karachi", "Asia/Kolkata", "Asia/Dhaka", "Asia/Bangkok", "Asia/Jakarta", "Asia/Singapore", "Asia/Hong_Kong", "Asia/Shanghai", "Asia/Manila",
	"Asia/Seoul", "Asia/Tokyo", "Australia/Perth", "Australia/Adelaide", "Australia/Sydney", "Australia/Melbourne", "Pacific/Auckland", "Pacific/Honolulu",
	"America/Anchorage", "America/Los_Angeles", "America/Vancouver", "America/Denver", "America/Phoenix", "America/Chicago", "America/Mexico_City", "America/Toronto",
	"America/New_York", "America/Halifax", "America/St_Johns", "America/Bogota", "America/Lima", "America/Santiago", "America/Argentina/Buenos_Aires", "America/Sao_Paulo"}

func (s *Server) settingsPage(w http.ResponseWriter, r *http.Request) {
	if !s.principal(r).Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	d, err := s.settingsView(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Saved = r.URL.Query().Get("saved")
	s.render(w, r, http.StatusOK, "settings", s.page(r, "Settings", d))
}

func (s *Server) settingsFail(w http.ResponseWriter, r *http.Request, err error) {
	e := ops.AsError(err)
	if e.Code != ops.CodeInvalid {
		s.fail(w, r, err)
		return
	}
	d, verr := s.settingsView(r)
	if verr != nil {
		s.fail(w, r, verr)
		return
	}
	d.Error, d.Field = e.Message, e.Field
	s.render(w, r, http.StatusUnprocessableEntity, "settings", s.page(r, "Settings", d))
}

func (s *Server) uiSettingsPrefs(w http.ResponseWriter, r *http.Request) {
	items := map[string]string{}
	for _, k := range []string{"theme", "theme_light", "theme_dark", "density", "reduced_motion", "editor_font", "editor_size", "editor_measure", "editor_line_height", "autosave_ms", "language"} {
		if v := r.PostFormValue(k); v != "" {
			items[k] = v
		}
	}
	// A checkbox is absent when off, so each posts a hidden "false" first.
	for _, k := range []string{"show_word_count", "typewriter", "focus_dim"} {
		if v := r.PostForm[k]; len(v) > 0 {
			items[k] = v[len(v)-1]
		}
	}
	if len(items) == 0 {
		http.Redirect(w, r, "/settings", http.StatusSeeOther)
		return
	}
	if _, err := s.Reg.Call(r.Context(), s.principal(r), "prefs.set", account.SetPrefsIn{Items: items}); err != nil {
		s.settingsFail(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings?saved=prefs#editor", http.StatusSeeOther)
}

func (s *Server) uiSettingsProfile(w http.ResponseWriter, r *http.Request) {
	name, loc, tz := r.PostFormValue("display_name"), r.PostFormValue("locale"), r.PostFormValue("timezone")
	in := account.UpdateIn{DisplayName: &name}
	if loc != "" {
		in.Locale = &loc
	}
	if tz != "" {
		in.Timezone = &tz
	}
	if _, err := s.Reg.Call(r.Context(), s.principal(r), "profile.update", in); err != nil {
		s.settingsFail(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings?saved=profile#profile", http.StatusSeeOther)
}

func (s *Server) uiSessionRevoke(w http.ResponseWriter, r *http.Request) {
	sid := r.PathValue("sid")
	if _, err := s.Reg.Call(r.Context(), s.principal(r), "session.revoke", account.RevokeIn{SID: sid}); err != nil {
		s.fail(w, r, err)
		return
	}
	if authFrom(r.Context()).SessionID == sid {
		s.Sess.ClearCookie(w)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/settings?saved=sessions#sessions", http.StatusSeeOther)
}

func (s *Server) uiSessionsRevokeOthers(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Reg.Call(r.Context(), s.principal(r), "session.revoke_others", account.EmptyIn{}); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings?saved=sessions#sessions", http.StatusSeeOther)
}
