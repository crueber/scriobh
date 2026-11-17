package server

import (
	"io/fs"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/account"
)

// The theme table, the account allowlists and the stylesheets describe the same five themes.
func TestThemeTableMatchesAllowlistsAndStylesheets(t *testing.T) {
	light, dark := map[string]bool{}, map[string]bool{}
	for _, th := range themeTable {
		if th.Scheme == "light" {
			light[th.ID] = true
		} else {
			dark[th.ID] = true
		}
	}
	for _, id := range account.LightThemes {
		if !light[id] {
			t.Errorf("light theme %q is allowed but has no row in themeTable", id)
		}
		delete(light, id)
	}
	for _, id := range account.DarkThemes {
		if !dark[id] {
			t.Errorf("dark theme %q is allowed but has no row in themeTable", id)
		}
		delete(dark, id)
	}
	if len(light)+len(dark) > 0 {
		t.Errorf("themeTable has themes the allowlists refuse: %v %v", light, dark)
	}
	if themeTable[0].ID != account.LightThemes[0] || themesOf("dark")[0].ID != account.DarkThemes[0] {
		t.Error("the first theme of each scheme is the default and is drawn by app.css")
	}
	// The browser chrome colour is the theme's page background in the stylesheet.
	static := staticFS(t)
	read := func(f string) string {
		b, err := fs.ReadFile(static, f)
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}
	app, themes := read("css/app.css"), read("css/themes.css")
	bg := regexp.MustCompile(`--bg:\s*(#[0-9A-Fa-f]{6})`)
	for _, th := range themeTable {
		var block string
		switch th.ID {
		case "paper":
			block = app[strings.Index(app, `.tsw[data-sw="paper"]`):]
		case "ink":
			block = app[strings.Index(app, `.tsw[data-sw="ink"]`):]
		default:
			i := strings.Index(themes, `.tsw[data-sw="`+th.ID+`"]`)
			if i < 0 {
				t.Errorf("themes.css has no block for %s", th.ID)
				continue
			}
			block = themes[i:]
		}
		m := bg.FindStringSubmatch(block)
		if m == nil || !strings.EqualFold(m[1], th.Color) {
			t.Errorf("%s: theme-color %s in the table, --bg in the stylesheet %v", th.ID, th.Color, m)
		}
		if th.ID != "paper" && th.ID != "ink" {
			attr := "lt"
			if th.Scheme == "dark" {
				attr = "dk"
			}
			if strings.Count(themes, `[data-`+attr+`="`+th.ID+`"]`) != 2 {
				t.Errorf("%s: themes.css must hold the forced and the system-following block", th.ID)
			}
		}
	}
}

func TestThemePreferencesRenderOnTheHTMLElement(t *testing.T) {
	e := newWriteEnv(t)
	s := e.s
	// Defaults: nothing on <html>, two theme-color metas for Paper and Ink.
	body := s.do(e.owner, "GET", "/", "").Body.String()
	if strings.Contains(body, "data-lt=") || strings.Contains(body, "data-dk=") || strings.Contains(body, "data-theme=") {
		t.Error("the defaults put theme attributes on <html>")
	}
	if !strings.Contains(body, `content="#FBFAF7" media="(prefers-color-scheme: light)"`) || !strings.Contains(body, `content="#131311" media="(prefers-color-scheme: dark)"`) {
		t.Error("the default theme-color metas are missing")
	}
	// Refused values change nothing.
	for _, bad := range []url.Values{{"theme_light": {"ink"}}, {"theme_dark": {"sepia"}}, {"theme_light": {"neon"}}} {
		rec := s.post(t, e.owner, "/ui/settings/prefs", bad, "", false)
		if rec.Code == http.StatusSeeOther {
			t.Errorf("%v was accepted", bad)
		}
	}
	// Following the system, with a chosen light and dark theme.
	if rec := s.post(t, e.owner, "/ui/settings/prefs", url.Values{"theme_light": {"sepia"}, "theme_dark": {"moss"}}, "", false); rec.Code != http.StatusSeeOther {
		t.Fatalf("save themes: %d", rec.Code)
	}
	body = s.do(e.owner, "GET", "/", "").Body.String()
	for _, want := range []string{`<html lang="en" data-lt="sepia" data-dk="moss">`, `content="#F1E7D0" media="(prefers-color-scheme: light)"`, `content="#161E19" media="(prefers-color-scheme: dark)"`} {
		if !strings.Contains(body, want) {
			t.Errorf("following the system: missing %s", want)
		}
	}
	// Forced dark: one meta, the dark theme's colour; the light theme is still remembered.
	s.post(t, e.owner, "/ui/settings/prefs", url.Values{"theme": {"dark"}}, "", false)
	body = s.do(e.owner, "GET", "/", "").Body.String()
	if !strings.Contains(body, `<html lang="en" data-theme="dark" data-lt="sepia" data-dk="moss">`) || strings.Count(body, `name="theme-color"`) != 1 || !strings.Contains(body, `content="#161E19">`) {
		t.Errorf("forced dark: %s", body[:300])
	}
	s.post(t, e.owner, "/ui/settings/prefs", url.Values{"theme": {"light"}}, "", false)
	body = s.do(e.owner, "GET", "/", "").Body.String()
	if strings.Count(body, `name="theme-color"`) != 1 || !strings.Contains(body, `content="#F1E7D0">`) {
		t.Error("forced light: the meta is not Sepia's colour")
	}
	// Settings shows the choice as a radio group per scheme, the saved one checked.
	page := s.do(e.owner, "GET", "/settings", "").Body.String()
	lint(t, "settings", page)
	for _, want := range []string{`name="theme_light" value="sepia" data-theme-pick="theme_light" checked`, `name="theme_dark" value="moss" data-theme-pick="theme_dark" checked`, `<legend>Light theme</legend>`, `<legend>Dark theme</legend>`, `data-sw="slate"`} {
		if !strings.Contains(page, want) {
			t.Errorf("settings lacks %s", want)
		}
	}
	// Back to Paper and Ink: the attributes go away again.
	s.post(t, e.owner, "/ui/settings/prefs", url.Values{"theme": {"system"}, "theme_light": {"paper"}, "theme_dark": {"ink"}}, "", false)
	body = s.do(e.owner, "GET", "/", "").Body.String()
	if strings.Contains(body, "data-lt=") || strings.Contains(body, "data-dk=") {
		t.Error("Paper and Ink are the defaults and render no attribute")
	}
}
