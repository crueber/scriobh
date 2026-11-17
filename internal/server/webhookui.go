package server

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/webhook"

	"github.com/crueber/scriobh/internal/domain/projects"
)

// The project owner's Webhooks page, /p/{slug}/webhooks: list the project's webhooks with their state and queue, make
// one (name, address, events, whether a comment carries its words), see the signing secret once, send a test, switch one
// off or on, change it, rotate its secret, delete it, and read its delivery log with a replay for what failed. Every
// change is one of the webhook.* ops, which are the owner's; the address the server refuses is refused with its own words.
// The secret is shown once, on the page the form that made it redirects to (kept in the session's one-shot flash for at
// most two minutes, never in a URL or a log), and nowhere again.

func (s *Server) registerWebhookUI(m *http.ServeMux) {
	m.HandleFunc("GET /p/{slug}/webhooks", s.webhooksPage)
	s.uiRoute("POST /ui/projects/{id}/webhooks/{action}", s.uiWebhook)
}

type whView struct {
	webhook.Webhook
	Created, LastAt string
	Events          map[string]bool
}

type whOnce struct{ Name, Secret string }

type whLog struct {
	Hook          whView
	State         string
	Items         []whDelivery
	Next, Before  string
	Pending, Dead int
	Error         string
}

type whDelivery struct {
	webhook.Delivery
	Created, Next, Delivered string
	Replay                   bool
}

type webhooksData struct {
	Project         projects.Project
	Slug, ProjectID string
	CSRF            string
	List            webhook.List
	Hooks           []whView
	Pending, Dead   int
	Once            *whOnce
	Test            map[string]string
	Log             *whLog
	Flash, Error    string
	Form            map[string]string
	FormEvents      map[string]bool
}

// whProject is the page's door: the owner only; a member who is not the owner is told it is the owner's, a stranger finds nothing.
func (s *Server) whProject(w http.ResponseWriter, r *http.Request, proj projects.Project) bool {
	if proj.Role != ops.ProjectOwner {
		s.fail(w, r, ops.Forbidden(i18n.Default.T(s.page(r, "", nil).Lang, "wh.owner_only")))
		return false
	}
	return true
}

func (s *Server) webhooksView(r *http.Request, proj projects.Project) (webhooksData, error) {
	ctx, p := r.Context(), s.principal(r)
	d := webhooksData{Project: proj, Slug: proj.Slug, ProjectID: proj.ID, CSRF: authFrom(ctx).CSRF, Form: map[string]string{}, FormEvents: map[string]bool{}}
	lo, err := s.Reg.Call(ctx, p, "webhook.list", webhook.ProjectIn{ProjectID: proj.ID})
	if err != nil {
		return d, err
	}
	d.List = lo.(webhook.List)
	for _, h := range d.List.Items {
		v := whView{Webhook: h, Created: utcWhen(h.CreatedAt), LastAt: utcWhen(h.LastDeliveryAt), Events: map[string]bool{}}
		for _, e := range h.Events {
			v.Events[e] = true
		}
		d.Pending += h.Pending
		d.Dead += h.Dead
		d.Hooks = append(d.Hooks, v)
	}
	if id := r.URL.Query().Get("log"); id != "" {
		for _, h := range d.Hooks {
			if h.ID != id {
				continue
			}
			lg := &whLog{Hook: h, State: r.URL.Query().Get("state"), Before: r.URL.Query().Get("before"), Pending: h.Pending, Dead: h.Dead}
			out, err := s.Reg.Call(ctx, p, "webhook.deliveries", webhook.DeliveriesIn{ProjectID: proj.ID, WebhookID: id, State: lg.State, Before: lg.Before, Limit: 25})
			if err != nil {
				lg.Error = ops.AsError(err).Message
			} else {
				dl := out.(webhook.DeliveriesOut)
				lg.Next = dl.NextCursor
				for _, it := range dl.Items {
					lg.Items = append(lg.Items, whDelivery{Delivery: it, Created: utcWhen(it.CreatedAt), Next: utcWhen(it.NextAttemptAt), Delivered: utcWhen(it.DeliveredAt),
						Replay: it.Event != "ping" && it.State != "pending"})
				}
			}
			d.Log = lg
		}
	}
	return d, nil
}

func (s *Server) webhooksPage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProject(w, r)
	if !ok || !s.whProject(w, r, proj) {
		return
	}
	d, err := s.webhooksView(r, proj)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if fl := s.flashTake(r); fl != nil {
		d.Flash, d.Error = fl["msg"], fl["err"]
		if fl["secret"] != "" {
			d.Once = &whOnce{Name: fl["secret_name"], Secret: fl["secret"]}
		}
		if fl["test_id"] != "" {
			d.Test = fl
		}
	}
	w.Header().Set("Cache-Control", "no-store")
	pg := s.page(r, "", nil)
	s.render(w, r, http.StatusOK, "webhooks", s.page(r, pg.T("wh.title_of", "title", proj.Title), d))
}

func (s *Server) uiWebhook(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok || !s.whProject(w, r, proj) {
		return
	}
	ctx, p := r.Context(), s.principal(r)
	T := func(k string, args ...any) string { return i18n.Default.T(s.page(r, "", nil).Lang, k, args...) }
	id := r.PostFormValue("webhook_id")
	page := "/p/" + proj.Slug + "/webhooks"
	back := func(vals map[string]string, frag string) {
		s.flashSet(r, vals)
		http.Redirect(w, r, page+frag, http.StatusSeeOther)
	}
	refuse := func(err error, frag string) {
		e := ops.AsError(err)
		if e.Code == ops.CodeInternal || e.Code == ops.CodeUnauthenticated || e.Code == ops.CodeNotFound {
			s.fail(w, r, err)
			return
		}
		back(map[string]string{"err": e.Message}, frag)
	}
	events := func() []string {
		var out []string
		for _, e := range r.PostForm["event"] {
			out = append(out, strings.TrimSpace(e))
		}
		return out
	}
	switch r.PathValue("action") {
	case "create":
		text := r.PostFormValue("include_text") == "on"
		in := webhook.CreateIn{ProjectID: proj.ID, Name: r.PostFormValue("name"), URL: strings.TrimSpace(r.PostFormValue("url")), Events: events(), IncludeText: &text}
		out, err := s.Reg.Call(ctx, p, "webhook.create", in)
		if err != nil {
			s.webhookFail(w, r, proj, err)
			return
		}
		res := out.(webhook.CreateOut)
		back(map[string]string{"secret": res.Secret, "secret_name": res.Item.Name}, "") // no fragment: scrolling to one takes the focus off the secret
	case "rotate":
		keep, err := strconv.Atoi(strings.TrimSpace(r.PostFormValue("keep_old_hours")))
		if err != nil {
			back(map[string]string{"err": T("wh.keep_number")}, "#hook-"+id)
			return
		}
		out, err := s.Reg.Call(ctx, p, "webhook.rotate_secret", webhook.RotateIn{ProjectID: proj.ID, WebhookID: id, KeepOldHours: &keep})
		if err != nil {
			refuse(err, "#hook-"+id)
			return
		}
		res := out.(webhook.CreateOut)
		back(map[string]string{"secret": res.Secret, "secret_name": res.Item.Name}, "") // no fragment: scrolling to one takes the focus off the secret
	case "test":
		out, err := s.Reg.Call(ctx, p, "webhook.test", webhook.HookIn{ProjectID: proj.ID, WebhookID: id})
		if err != nil {
			e := ops.AsError(err)
			if e.Code == ops.CodeRateLimited {
				back(map[string]string{"err": T("wh.test_soon")}, "#hook-"+id)
				return
			}
			refuse(err, "#hook-"+id)
			return
		}
		t := out.(webhook.TestOut)
		vals := map[string]string{"test_id": id, "test_status": strconv.Itoa(t.Delivery.Status), "test_error": t.Delivery.Error, "test_excerpt": t.Delivery.Excerpt}
		if t.OK {
			vals["test_ok"] = "1"
		}
		back(vals, "#hook-"+id)
	case "enable", "disable":
		on := r.PathValue("action") == "enable"
		if _, err := s.Reg.Call(ctx, p, "webhook.update", webhook.UpdateIn{ProjectID: proj.ID, WebhookID: id, Enabled: &on}); err != nil {
			refuse(err, "#hook-"+id)
			return
		}
		msg := T("wh.now_off")
		if on {
			msg = T("wh.now_on")
		}
		back(map[string]string{"msg": msg}, "#hook-"+id)
	case "update":
		name := r.PostFormValue("name")
		ev := events()
		text := r.PostFormValue("include_text") == "on"
		in := webhook.UpdateIn{ProjectID: proj.ID, WebhookID: id, Name: &name, Events: &ev, IncludeText: &text}
		if u := strings.TrimSpace(r.PostFormValue("url")); u != "" {
			in.URL = &u
		}
		if _, err := s.Reg.Call(ctx, p, "webhook.update", in); err != nil {
			refuse(err, "#hook-"+id)
			return
		}
		back(map[string]string{"msg": T("wh.saved")}, "#hook-"+id)
	case "delete":
		if _, err := s.Reg.Call(ctx, p, "webhook.delete", webhook.HookIn{ProjectID: proj.ID, WebhookID: id}); err != nil {
			refuse(err, "#wh-list")
			return
		}
		back(map[string]string{"msg": T("wh.deleted")}, "#wh-list")
	case "redeliver":
		frag := "?log=" + id + "#wh-log"
		if _, err := s.Reg.Call(ctx, p, "webhook.redeliver", webhook.RedeliverIn{ProjectID: proj.ID, WebhookID: id, DeliveryID: r.PostFormValue("delivery_id")}); err != nil {
			refuse(err, frag)
			return
		}
		back(map[string]string{"msg": T("wh.requeued")}, frag)
	default:
		s.fail(w, r, ops.NotFound("page"))
	}
}

// webhookFail shows a refused create on the page with what was typed and the server's own words, untouched.
func (s *Server) webhookFail(w http.ResponseWriter, r *http.Request, proj projects.Project, err error) {
	e := ops.AsError(err)
	if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict {
		s.fail(w, r, err)
		return
	}
	d, derr := s.webhooksView(r, proj)
	if derr != nil {
		s.fail(w, r, derr)
		return
	}
	d.Form["name"], d.Form["url"], d.Form["include_text"], d.Form["field"] = r.PostFormValue("name"), r.PostFormValue("url"), r.PostFormValue("include_text"), e.Field
	for _, ev := range r.PostForm["event"] {
		d.FormEvents[ev] = true
	}
	d.Error = e.Message
	pg := s.page(r, "", nil)
	s.render(w, r, http.StatusUnprocessableEntity, "webhooks", s.page(r, pg.T("wh.title_of", "title", proj.Title), d))
}
