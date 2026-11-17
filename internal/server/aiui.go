package server

import (
	"context"
	"net/http"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// The AI screens. AI is off until an administrator adds a provider and a project's owner turns it on, and every affordance
// follows: a page asks aiStateOf what is the case and draws nothing for the writing partner unless it is "on". The pages
// here are shells: they carry what the script needs to know (who may change what, where the ops are) and the script reads
// and changes everything through the ai.* ops, so the page and the CLI are the same thing.

// aiState is what the writing view and the project pages know about AI for one project and one person.
type aiState struct {
	State string // "on", "off" (this project's policy), "unavailable" (the server or an administrator switched it off), "none" (no right to use it)
	Mode  string // the policy mode: off, ask, local_only
}

// On reports whether the writing partner is available here.
func (a aiState) On() bool { return a.State == "on" }

// aiStateOf asks the AI ops, as the person, whether AI can be used on the project. It never errors: anything it cannot
// establish is "off", because an affordance for something that may not run is worse than none.
func (s *Server) aiStateOf(ctx context.Context, p ops.Principal, projectID string) aiState {
	if so, err := s.Reg.Call(ctx, p, "ai.status", struct{}{}); err == nil {
		if st, ok := so.(aicore.StatusOut); ok && !st.Enabled {
			return aiState{State: "unavailable"}
		}
	}
	out, err := s.Reg.Call(ctx, p, "ai.policy.get", aicore.PolicyGetIn{ProjectID: projectID})
	if err != nil {
		// A reader has no right to ask; everything else that fails leaves AI off.
		if ops.AsError(err).Code == ops.CodeForbidden {
			return aiState{State: "none"}
		}
		return aiState{State: "off"}
	}
	po, ok := out.(aicore.PolicyOut)
	switch {
	case !ok:
		return aiState{State: "off"}
	case !po.AIEnabled:
		return aiState{State: "unavailable"}
	case po.Item.Mode == "off" || po.Item.Mode == "":
		return aiState{State: "off", Mode: "off"}
	}
	return aiState{State: "on", Mode: po.Item.Mode}
}

// aiProjectData is the AI page of one project.
type aiProjectData struct {
	Project projects.Project
	Slug    string
	CSRF    string
	IsOwner bool // may change the policy
	CanEdit bool // may read it, use the exclusion list and see the person's own use
	State   aiState
}

// aiSettingsData is the account-level page: the providers a person may use and the instance's AI settings.
type aiSettingsData struct {
	CSRF    string
	IsAdmin bool
	Writer  bool
}

func (s *Server) aiProjectPage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProject(w, r)
	if !ok {
		return
	}
	p := s.principal(r)
	d := aiProjectData{Project: proj, Slug: proj.Slug, CSRF: authFrom(r.Context()).CSRF, IsOwner: proj.Role == ops.ProjectOwner || p.Role == ops.RoleAdmin,
		CanEdit: proj.Role == ops.ProjectOwner || proj.Role == ops.ProjectEditor || p.Role == ops.RoleAdmin}
	d.State = s.aiStateOf(r.Context(), p, proj.ID)
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, http.StatusOK, "project-ai", s.page(r, "AI · "+proj.Title, d))
}

func (s *Server) aiSettingsPage(w http.ResponseWriter, r *http.Request) {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	d := aiSettingsData{CSRF: authFrom(r.Context()).CSRF, IsAdmin: p.Role == ops.RoleAdmin, Writer: p.Role == ops.RoleAdmin || p.Role == ops.RoleWriter}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, http.StatusOK, "ai-settings", s.page(r, "AI", d))
}

func (s *Server) registerAIUI(m *http.ServeMux) {
	m.HandleFunc("GET /p/{slug}/ai", s.aiProjectPage)
	m.HandleFunc("GET /settings/ai", s.aiSettingsPage)
}
