package server

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"unicode/utf8"

	aicore "github.com/crueber/scriobh/internal/domain/ai"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/semantic"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
)

// Search by meaning (F-170, F-205): the owner's page that switches the index on after a preview, the "By meaning" mode of the
// search page, and the "Scenes like this one" list of the writing view's inspector. The words say plainly when text is
// sent and where; a page never implies that anything was sent when it was not. Everything goes through the
// search.semantic.* ops, and the owner's switches are reachable only here (browser session only).

func (s *Server) registerSemantic(m *http.ServeMux) {
	m.HandleFunc("GET /p/{slug}/semantic", s.semanticPage)
	s.uiRoute("POST /ui/projects/{id}/semantic/{action}", s.uiSemantic)
	m.HandleFunc("GET /ui/docs/{id}/similar", s.uiSimilar)
	s.uiRoute("POST /ui/projects/{id}/search/meaning", s.uiSearchMeaning)
}

// uiSearchMeaning runs a search by meaning. It is a POST with the session's token because it sends the question to the embedding
// endpoint (a remote, paid one if the owner chose that): a GET could be made by a link in another page.
func (s *Server) uiSearchMeaning(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	q := strings.TrimSpace(r.PostFormValue("q"))
	d := searchData{Q: q, Project: &proj, CSRF: authFrom(r.Context()).CSRF, Sem: s.semSearchFor(r, proj, q, true)}
	s.render(w, r, http.StatusOK, "search", s.page(r, "Search · "+proj.Title, d))
}

type semProvider struct {
	ID, Name, Model, Where string
	Models                 []string
}

type semData struct {
	Project   projects.Project
	Slug      string
	CSRF      string
	St        semantic.StatusOut
	State     string // the state in words
	Reason    string // why it is paused or waiting, in words
	LastErr   string
	Providers []semProvider
	Pick      string // the provider chosen for the preview
	Model     string
	Preview   *semantic.PreviewOut
	Cost      string
	Never     []string
	HasIndex  bool // embeddings exist, switched on or not
	Where     string
	Error     string
	Saved     string
}

func (s *Server) semOwner(w http.ResponseWriter, r *http.Request) (projects.Project, bool) {
	proj, ok := s.goalsProject(w, r)
	if !ok {
		return proj, false
	}
	if proj.Role != ops.ProjectOwner {
		s.fail(w, r, ops.Forbidden(s.tr(r, "sem.only_owner")))
		return proj, false
	}
	return proj, true
}

// semKey says words for a catalogue key when there is one, and the fallback when there is not.
func (s *Server) semWord(r *http.Request, key, fallback string, args ...any) string {
	if i18n.Default.Has(key) {
		return s.tr(r, key, args...)
	}
	return s.tr(r, fallback, args...)
}

func (s *Server) semView(r *http.Request, proj projects.Project) (semData, error) {
	ctx, p := r.Context(), s.principal(r)
	d := semData{Project: proj, Slug: proj.Slug, CSRF: authFrom(ctx).CSRF}
	out, err := s.Reg.Call(ctx, p, "search.semantic.status", semantic.StatusIn{ProjectID: proj.ID})
	if err != nil {
		if e := ops.AsError(err); e.Code == ops.CodeConflict || e.Code == ops.CodeForbidden {
			d.St.State = "off"
			d.State = s.tr(r, "search.semantic.state.off")
			d.Reason = e.Message
			return d, nil
		}
		return d, err
	}
	d.St = out.(semantic.StatusOut)
	if d.St.State == "indexing" {
		d.State = s.trN(r, "sem.state_indexing", d.St.QueueDepth)
	} else {
		d.State = s.tr(r, "search.semantic.state."+d.St.State)
	}
	if d.St.Reason != "" {
		d.Reason = s.semWord(r, "search.semantic.reason."+d.St.Reason, "sem.reason_other")
	}
	if d.St.LastError != "" {
		d.LastErr = s.semWord(r, "sem.err."+d.St.LastError, "sem.err_other", "class", d.St.LastError)
	}
	d.HasIndex = d.St.Chunks > 0
	if e := d.St.Endpoint; e != nil {
		d.Where = s.semWhere(r, e.Local, e.Remote, e.Host)
	}
	if lo, err := s.Reg.Call(ctx, p, "ai.provider.list", struct{}{}); err == nil {
		for _, pr := range lo.(aicore.ProviderListOut).Items {
			if !pr.Enabled || (pr.Kind != "ollama" && pr.Kind != "openai_compatible" && pr.Kind != "openrouter") {
				continue
			}
			sp := semProvider{ID: pr.ID, Name: pr.Name, Model: pr.DefaultModel, Where: s.semWhere(r, pr.Local, !pr.Local, "")}
			for _, m := range pr.Models {
				sp.Models = append(sp.Models, m.ID)
			}
			d.Providers = append(d.Providers, sp)
		}
	}
	return d, nil
}

func (s *Server) semWhere(r *http.Request, local, remote bool, host string) string {
	if local {
		return s.tr(r, "search.semantic.local")
	}
	if host == "" {
		return s.tr(r, "sem.remote_short")
	}
	return s.tr(r, "sem.remote_host", "host", host)
}

func (s *Server) semanticPage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.semOwner(w, r)
	if !ok {
		return
	}
	d, err := s.semView(r, proj)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Saved = r.URL.Query().Get("saved")
	s.render(w, r, http.StatusOK, "semantic", s.page(r, s.tr(r, "search.semantic.title"), d))
}

// semPreview asks what switching on would do, and draws the page with the answer.
func (s *Server) semPreview(w http.ResponseWriter, r *http.Request, proj projects.Project, errMsg string) {
	d, err := s.semView(r, proj)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Pick, d.Model, d.Error = r.PostFormValue("provider_id"), strings.TrimSpace(r.PostFormValue("model")), errMsg
	out, err := s.Reg.Call(r.Context(), s.principal(r), "search.semantic.preview", semantic.PreviewIn{ProjectID: proj.ID, ProviderID: d.Pick, Model: d.Model})
	status := http.StatusOK
	if err != nil {
		e := ops.AsError(err)
		if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict && e.Code != ops.CodeForbidden && e.Code != ops.CodeNotFound {
			s.fail(w, r, err)
			return
		}
		d.Error, status = e.Message, http.StatusUnprocessableEntity
		s.render(w, r, status, "semantic", s.page(r, s.tr(r, "search.semantic.title"), d))
		return
	}
	pv := out.(semantic.PreviewOut)
	d.Preview = &pv
	d.Cost = s.semCost(r, pv)
	d.Where = s.semWhere(r, pv.Endpoint.Local, pv.Remote, pv.ConfirmHost)
	s.render(w, r, status, "semantic", s.page(r, s.tr(r, "search.semantic.title"), d))
}

func (s *Server) semCost(r *http.Request, pv semantic.PreviewOut) string {
	switch {
	case pv.Endpoint.Local:
		return s.tr(r, "sem.cost_local")
	case pv.CostKnown && pv.EstimatedMicros > 0:
		return s.tr(r, "sem.cost_about", "amount", fmt.Sprintf("%.2f", float64(pv.EstimatedMicros)/1e6), "currency", pv.Currency)
	case pv.CostKnown:
		return s.tr(r, "sem.cost_none")
	}
	return s.tr(r, "sem.cost_unknown")
}

func (s *Server) uiSemantic(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	if proj.Role != ops.ProjectOwner {
		s.fail(w, r, ops.Forbidden(s.tr(r, "sem.only_owner")))
		return
	}
	p, ctx := s.principal(r), r.Context()
	var err error
	saved := ""
	switch action := r.PathValue("action"); action {
	case "preview":
		s.semPreview(w, r, proj, "")
		return
	case "enable":
		_, err = s.Reg.Call(ctx, p, "search.semantic.enable", semantic.EnableIn{ProjectID: proj.ID, ProviderID: r.PostFormValue("provider_id"), Model: strings.TrimSpace(r.PostFormValue("model")),
			ConsentToken: r.PostFormValue("consent_token"), ConfirmRemote: strings.TrimSpace(r.PostFormValue("confirm_remote"))})
		saved = "enabled"
	case "disable":
		_, err = s.Reg.Call(ctx, p, "search.semantic.disable", semantic.ProjectIn{ProjectID: proj.ID})
		saved = "disabled"
	case "rebuild":
		_, err = s.Reg.Call(ctx, p, "search.semantic.rebuild", semantic.ProjectIn{ProjectID: proj.ID})
		saved = "rebuilt"
	case "purge":
		if strings.TrimSpace(r.PostFormValue("confirm_title")) != strings.TrimSpace(proj.Title) {
			d, derr := s.semView(r, proj)
			if derr != nil {
				s.fail(w, r, derr)
				return
			}
			d.Error = s.tr(r, "sem.purge_mismatch")
			s.render(w, r, http.StatusUnprocessableEntity, "semantic", s.page(r, s.tr(r, "search.semantic.title"), d))
			return
		}
		_, err = s.Reg.Call(ctx, p, "search.semantic.purge", semantic.ProjectIn{ProjectID: proj.ID})
		saved = "purged"
	default:
		err = ops.Invalid("", "That form was not recognised.")
	}
	if err != nil {
		e := ops.AsError(err)
		if e.Code == ops.CodeInvalid || e.Code == ops.CodeConflict || e.Code == ops.CodeForbidden {
			if r.PathValue("action") == "enable" { // the token or the typed host was refused: draw the preview again, with its fresh token
				s.semPreview(w, r, proj, e.Message)
				return
			}
			d, derr := s.semView(r, proj)
			if derr != nil {
				s.fail(w, r, derr)
				return
			}
			d.Error = e.Message
			s.render(w, r, http.StatusUnprocessableEntity, "semantic", s.page(r, s.tr(r, "search.semantic.title"), d))
			return
		}
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/p/"+proj.Slug+"/semantic?saved="+saved, http.StatusSeeOther)
}

// ---- the search page's mode and the inspector's list ------------------------------------------------------------

type semHit struct {
	Title, URL, Meta, Snippet string
}

type semSearch struct {
	Offered bool // owner or editor, and the index is switched on
	Mode    string
	Hits    []semHit
	Pending int
	Message string // why there is nothing to show, in words (paused, off, failed)
	Sent    string // where the question went, said before the result
	Count   int
	Ran     bool // a question was sent and answered
}

// findURL opens a document at a passage: the first words of the snippet travel in the address and js/jump.js scrolls to them.
func findURL(slug, docID, snippet string) string {
	f := strings.Join(strings.Fields(snippet), " ")
	if utf8.RuneCountInString(f) > 48 {
		f = string([]rune(f)[:48])
	}
	u := "/p/" + slug + "/d/" + docID
	if f != "" {
		u += "?find=" + url.QueryEscape(f)
	}
	return u
}

func (s *Server) semHits(r *http.Request, slug string, hits []semantic.Hit) []semHit {
	out := make([]semHit, 0, len(hits))
	for _, h := range hits {
		out = append(out, semHit{Title: displayTitle(h.Title), URL: findURL(slug, h.DocumentID, h.Snippet), Meta: docKindLabel(h.Kind) + " · " + statusLabel(h.Status), Snippet: h.Snippet})
	}
	return out
}

// semSearchFor reads the mode's data for the search page. It asks nothing of the endpoint unless the person ran a search.
func (s *Server) semSearchFor(r *http.Request, proj projects.Project, q string, send bool) semSearch {
	out := semSearch{Mode: "words"}
	if proj.Role != ops.ProjectOwner && proj.Role != ops.ProjectEditor {
		return out
	}
	ctx, p := r.Context(), s.principal(r)
	so, err := s.Reg.Call(ctx, p, "search.semantic.status", semantic.StatusIn{ProjectID: proj.ID})
	if err != nil {
		return out
	}
	st := so.(semantic.StatusOut)
	if !st.Enabled && st.Reason == "" {
		if send { // a question was posted to a project whose index is off (paused by its owner, say): it is said, and nothing is sent
			out.Offered, out.Mode, out.Message = true, "meaning", s.tr(r, "sem.not_on")
		}
		return out // otherwise nothing is offered and nothing is said
	}
	out.Offered = true
	if !send && r.URL.Query().Get("mode") != "meaning" {
		return out
	}
	out.Mode = "meaning"
	if st.Reason != "" || st.State == "paused" || st.State == "waiting" {
		out.Message = s.semWord(r, "search.semantic.reason."+st.Reason, "sem.reason_other")
		return out
	}
	// A question is sent only by a form posted with this session's token (uiSearchMeaning): an address, a link or a prefetch never
	// makes the server call the embedding endpoint.
	if !send || q == "" {
		return out
	}
	if e := st.Endpoint; e != nil {
		if e.Local {
			out.Sent = s.tr(r, "sem.sent_local", "name", e.Name)
		} else {
			out.Sent = s.tr(r, "sem.sent_remote", "name", e.Name)
		}
	}
	res, err := s.Reg.Call(ctx, p, "search.semantic", semantic.SearchIn{ProjectID: proj.ID, Q: q, Limit: 20})
	if err != nil {
		e := ops.AsError(err)
		switch {
		case e.Code == ops.CodeInvalid:
			out.Message = e.Message
		case e.Code == ops.CodeRateLimited:
			out.Message = s.tr(r, "sem.slow_down")
		case e.Code == ops.CodeConflict && e.Data["semantic"] != nil:
			out.Message = s.semWord(r, "search.semantic.reason."+fmt.Sprint(e.Data["reason"]), "sem.reason_other")
		default:
			out.Message = s.tr(r, "sem.failed")
		}
		return out
	}
	so2 := res.(semantic.SearchOut)
	out.Hits, out.Pending, out.Count, out.Ran = s.semHits(r, proj.Slug, so2.Items), so2.Pending, len(so2.Items), true
	return out
}

// uiSimilar is the fragment of the inspector's "Scenes like this one": the documents closest in meaning to this one,
// from the vectors already stored. It sends nothing, so a reader may ask.
func (s *Server) uiSimilar(w http.ResponseWriter, r *http.Request) {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	docID := r.PathValue("id")
	pid := s.lookup(r.Context(), `SELECT project_id FROM documents WHERE id = ?`, docID)
	slug := s.gateAddress(r, pid, s.lookup(r.Context(), `SELECT slug FROM projects WHERE id = ?`, pid))
	out, err := s.Reg.Call(r.Context(), p, "search.similar", semantic.SimilarIn{ProjectID: pid, DocumentID: docID, Limit: 8})
	d := similarData{}
	if err != nil {
		e := ops.AsError(err)
		switch {
		case e.Code == ops.CodeNotFound || e.Code == ops.CodeForbidden:
			s.fail(w, r, err)
			return
		case e.Code == ops.CodeConflict && e.Data["semantic"] == "paused":
			d.Message = s.semWord(r, "search.semantic.reason."+fmt.Sprint(e.Data["reason"]), "sem.reason_other")
		default:
			d.Message = s.tr(r, "sem.like_off")
		}
	} else {
		so := out.(semantic.SimilarOut)
		for _, h := range so.Items {
			d.Hits = append(d.Hits, semHit{Title: displayTitle(h.Title), URL: findURL(slug, h.DocumentID, h.Snippet), Meta: docKindLabel(h.Kind) + " · " + statusLabel(h.Status), Snippet: h.Snippet})
		}
		if len(d.Hits) == 0 {
			d.Message = s.tr(r, "search.semantic.similar_none")
		}
		if so.Pending > 0 {
			d.Partial = s.trN(r, "sem.partial", so.Pending)
		}
	}
	s.fragment(w, r, http.StatusOK, "semantic-parts", "sem-similar", d)
}

// similarData is the inspector list: the words are written here, as a fragment has no page to ask for them.
type similarData struct {
	Hits    []semHit
	Message string
	Partial string
}
