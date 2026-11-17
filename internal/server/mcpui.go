package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strings"

	"github.com/crueber/scriobh/internal/domain/keys"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// The "connect an agent" page, /settings/mcp: whether this server's /mcp endpoint is on, its address (from the request
// the page answers, and in the browser from the page's own origin: nothing here knows a hard-coded origin), the
// configuration a client takes (a JSON block for the desktop clients, the scriobh-cli stdio proxy command, the remote
// address with its header), which keys work and what each sees, the tool groups the catalogue holds, and what is
// never offered. It reads the tool catalogue and the person's keys (key.list) and writes nothing; a key is made on the
// settings page, where it is shown once.

func (s *Server) registerMCPUI(m *http.ServeMux) {
	m.HandleFunc("GET /settings/mcp", s.mcpPage)
}

type mcpGroupRow struct {
	Name          string
	Read, Write   int
	Core, Bible   bool
	OnlyAdminTier bool
}

type mcpKeyRow struct {
	Name, Prefix, Scope, Pin string
}

type mcpSnippet struct {
	ID, Title, Note, Text string
}

type mcpData struct {
	Enabled        bool
	Origin         string // what the request says this server is; the page's script puts its own origin in its place
	URL            string // the endpoint with the chosen query
	Set            string // core, bible, both, all
	ReadOnly       bool
	Groups         []mcpGroupRow
	Tools          int
	Snippets       []mcpSnippet
	CanKeys        bool
	Keys           []mcpKeyRow
	AdminTools     bool
	PermanentTools bool
	Origins        int
}

// mcpSets are the choices the page offers, in order: what goes in ?groups= for each.
var mcpSets = []struct{ ID, Groups string }{{"core", "core"}, {"bible", "bible"}, {"both", "core,bible"}, {"all", ""}}

// requestOrigin is this server's origin as the request names it. It is a starting point for the page's text; the
// page's script replaces it with the browser's own location.origin, which is what the person actually reached.
func (s *Server) requestOrigin(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil || (s.Cfg.TrustProxy && strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")) {
		scheme = "https"
	}
	return scheme + "://" + r.Host
}

func jsonIndent(v any) string {
	b, _ := json.MarshalIndent(v, "", "  ")
	return string(b)
}

func (s *Server) mcpPage(w http.ResponseWriter, r *http.Request) {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	pg := s.page(r, "", nil)
	d := mcpData{Enabled: s.mcp != nil, Origin: s.requestOrigin(r), Set: "core", AdminTools: s.Cfg.MCP.AdminTools, PermanentTools: s.Cfg.MCP.PermanentTools,
		Origins: len(s.Cfg.MCP.AllowedOrigins), CanKeys: p.Role == ops.RoleWriter || p.Role == ops.RoleAdmin}
	q := r.URL.Query()
	for _, c := range mcpSets {
		if q.Get("set") == c.ID {
			d.Set = c.ID
		}
	}
	d.ReadOnly = q.Get("readonly") == "1" || q.Get("set") == "" // read only until the person asks for writes
	if d.Enabled {
		cat := s.mcp.catalog
		core, bible := map[string]bool{}, map[string]bool{}
		for _, g := range mcpPresets["core"] {
			core[g] = true
		}
		for _, g := range mcpPresets["bible"] {
			bible[g] = true
		}
		rows := map[string]*mcpGroupRow{}
		for i := range cat.Tools {
			t := &cat.Tools[i]
			row := rows[t.Group()]
			if row == nil {
				row = &mcpGroupRow{Name: t.Group(), Core: core[t.Group()], Bible: bible[t.Group()], OnlyAdminTier: true}
				rows[t.Group()] = row
			}
			if !t.AdminTier() {
				row.OnlyAdminTier = false
			}
			if t.AdminTier() || t.Permanent {
				continue // offered only on request, and counted there
			}
			if t.Mutating() {
				row.Write++
			} else {
				row.Read++
			}
			d.Tools++
		}
		for _, row := range rows {
			if row.OnlyAdminTier {
				continue
			}
			d.Groups = append(d.Groups, *row)
		}
		sort.Slice(d.Groups, func(i, j int) bool { return d.Groups[i].Name < d.Groups[j].Name })
	}
	groups := ""
	for _, c := range mcpSets {
		if c.ID == d.Set {
			groups = c.Groups
		}
	}
	v := url.Values{}
	if groups != "" {
		v.Set("groups", groups)
	}
	if d.ReadOnly {
		v.Set("readonly", "1")
	}
	d.URL = d.Origin + mcpPath
	if len(v) > 0 {
		d.URL += "?" + strings.ReplaceAll(v.Encode(), "%2C", ",")
	}
	args := []string{"mcp"}
	cli := "scriobh-cli mcp"
	if groups != "" {
		args = append(args, "--groups", groups)
		cli += " --groups " + groups
	}
	if d.ReadOnly {
		args = append(args, "--read-only")
		cli += " --read-only"
	}
	key := "scr_…"
	env := map[string]string{"SCRIOBH_URL": d.Origin, "SCRIOBH_API_KEY": key}
	d.Snippets = []mcpSnippet{
		{ID: "mcp-desktop", Title: pg.T("mcp.sn_desktop"), Note: pg.T("mcp.sn_desktop_note"),
			Text: jsonIndent(map[string]any{"mcpServers": map[string]any{"scriobh": map[string]any{"command": "scriobh-cli", "args": args, "env": env}}})},
		{ID: "mcp-cli", Title: pg.T("mcp.sn_cli"), Note: pg.T("mcp.sn_cli_note"),
			Text: "SCRIOBH_URL=" + d.Origin + " SCRIOBH_API_KEY=" + key + " " + cli},
		{ID: "mcp-remote", Title: pg.T("mcp.sn_remote"), Note: pg.T("mcp.sn_remote_note"),
			Text: d.URL + "\nAuthorization: Bearer " + key},
		{ID: "mcp-opencode", Title: pg.T("mcp.sn_opencode"), Note: pg.T("mcp.sn_opencode_note"),
			Text: jsonIndent(map[string]any{"mcp": map[string]any{"scriobh": map[string]any{"type": "remote", "url": d.URL, "headers": map[string]string{"Authorization": "Bearer " + key}}}})},
	}
	if d.CanKeys {
		if out, err := s.Reg.Call(r.Context(), p, "key.list", keys.ListIn{}); err == nil {
			titles := map[string]string{}
			if po, err := s.Reg.Call(r.Context(), p, "project.list", projects.ListIn{State: projects.StateAll}); err == nil {
				for _, x := range addressedProjects(po.(projects.ListOut).Items) {
					titles[x.ID] = x.Title
				}
			}
			for _, k := range out.(keys.ListOut).Items {
				if !k.Live {
					continue
				}
				row := mcpKeyRow{Name: k.Name, Prefix: k.Prefix, Scope: k.Scope, Pin: pg.T("mcp.k_everything")}
				switch {
				case k.ProjectID != "":
					row.Pin = pg.T("mcp.k_project", "title", firstNonEmptyStr(titles[k.ProjectID], pg.T("mcp.k_one_project")))
				case k.CompendiumID != "":
					row.Pin = pg.T("mcp.k_compendium")
				}
				d.Keys = append(d.Keys, row)
			}
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, http.StatusOK, "mcp", s.page(r, pg.T("mcp.title"), d))
}
