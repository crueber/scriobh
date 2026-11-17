package server

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/crueber/scriobh/internal/domain/keys"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// API keys on the settings page. Every change goes through the key ops; the
// forms post normally. A new key's secret is shown once, on the page that the
// create form answers with (never in a redirect or a URL), and is not kept.

type keyRow struct {
	ID, Name, Prefix, Scope, Restricted, LastUsed, Expires string
	Live                                                   bool
	// Protected says, for each project with a passphrase this key has reached or been refused at, what happened (F-252).
	Protected []string
}

// keysView fills the settings page with the caller's keys and the choices the
// create form offers. People who may not hold keys (instance readers) see no
// section.
func (s *Server) keysView(r *http.Request, d *settingsData) {
	p := s.principal(r)
	if p.Role != ops.RoleWriter && p.Role != ops.RoleAdmin {
		return
	}
	d.CanKeys = true
	loc := time.UTC
	out, err := s.Reg.Call(r.Context(), p, "key.list", keys.ListIn{})
	if err != nil {
		return
	}
	titles := map[string]string{}
	if po, err := s.Reg.Call(r.Context(), p, "project.list", projects.ListIn{State: projects.StateAll}); err == nil {
		for _, x := range addressedProjects(po.(projects.ListOut).Items) {
			titles[x.ID] = x.Title
			if !x.Trashed {
				d.KeyProjects = append(d.KeyProjects, opt{x.ID, x.Title})
			}
		}
	}
	if tz := d.Profile.Timezone; tz != "" {
		if l, err := time.LoadLocation(tz); err == nil {
			loc = l
		}
	}
	day := func(ts string) string {
		t, err := time.Parse(time.RFC3339Nano, ts)
		if err != nil {
			return ts
		}
		return t.In(loc).Format("2 Jan 2006")
	}
	for _, k := range out.(keys.ListOut).Items {
		if k.RevokedAt != "" {
			continue // revoked keys are history, not settings
		}
		row := keyRow{ID: k.ID, Name: k.Name, Prefix: k.Prefix, Scope: k.Scope, Live: k.Live, Restricted: "Everything you can open"}
		switch {
		case k.ProjectID != "":
			row.Restricted = "Only " + firstNonEmptyStr(titles[k.ProjectID], "one project")
		case k.CompendiumID != "":
			row.Restricted = "Only one compendium"
		}
		row.LastUsed = "Never used"
		if k.LastUsedAt != "" {
			row.LastUsed = "Last used " + day(k.LastUsedAt)
		}
		row.Expires = "Never expires"
		if k.ExpiresAt != "" {
			row.Expires = "Expires " + day(k.ExpiresAt)
			if !k.Live {
				row.Expires = "Expired " + day(k.ExpiresAt)
			}
		}
		for _, u := range k.Protected {
			row.Protected = append(row.Protected, s.tr(r, "keys.prot_row", "title", u.Title, "uses", commas(u.Uses), "refused", commas(u.Refused), "last", day(u.LastAt)))
		}
		d.Keys = append(d.Keys, row)
	}
}

func firstNonEmptyStr(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func (s *Server) uiKeyCreate(w http.ResponseWriter, r *http.Request) {
	in := keys.CreateIn{
		Name:      strings.TrimSpace(r.PostFormValue("name")),
		Scope:     r.PostFormValue("scope"),
		ProjectID: r.PostFormValue("project_id"),
	}
	if v := strings.TrimSpace(r.PostFormValue("expires_in_days")); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			s.settingsFail(w, r, ops.Invalid("expires_in_days", "Days must be a whole number."))
			return
		}
		in.ExpiresInDays = &n
	}
	res, err := s.Reg.Call(r.Context(), s.principal(r), "key.create", in)
	if err != nil {
		s.settingsFail(w, r, err)
		return
	}
	out := res.(keys.CreateOut)
	d, err := s.settingsView(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.NewSecret, d.NewKeyName = out.Secret, out.Item.Name
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, http.StatusOK, "settings", s.page(r, "Settings", d))
}

func (s *Server) uiKeyRevoke(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Reg.Call(r.Context(), s.principal(r), "key.revoke", keys.RevokeIn{ID: r.PathValue("id")}); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings?saved=keys#keys", http.StatusSeeOther)
}
