package server

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/pandoc"
)

// The Pandoc screens: the administrator's /admin/pandoc, and the formats the Compile and import pages offer while the
// bridge is on. The page reads the bridge through pandoc.config.get and changes it through pandoc.config.set and
// .config.test; it never sees a program's error output, only the bounded sentences the ops return. Nothing here runs
// Pandoc itself.

func (s *Server) registerPandocUI(m *http.ServeMux) {
	m.HandleFunc("GET /admin/pandoc", s.adminPandocPage)
	s.uiRoute("POST /ui/admin/pandoc/{action}", s.uiAdminPandoc)
}

// pandocUsable reports whether the bridge is switched on and its program passes the checks, for the pages that offer
// the extra formats to people who cannot read the administrator's settings. The service caches the check.
func (s *Server) pandocUsable(ctx context.Context) bool {
	svc, ok := pandoc.ServiceOf(s.Reg.Env())
	return ok && svc.Status(ctx).Err == nil
}

type pandocTestView struct {
	Version string
	OK      bool
	In, Out pandoc.TestResult
}

type pandocPage struct {
	Tab, CSRF    string
	Cfg          pandoc.ConfigOut
	Enabled      bool   // the checkbox as the form shows it
	Path, Memory string // the fields as the form shows them
	Saved        bool
	Error, Field string
	Test         *pandocTestView
	ImportList   string // the format names, for the words
	ExportList   string
}

func (s *Server) pandocView(r *http.Request, p ops.Principal) (pandocPage, error) {
	out, err := s.Reg.Call(r.Context(), p, "pandoc.config.get", struct{}{})
	if err != nil {
		return pandocPage{}, err
	}
	c := out.(pandoc.ConfigOut)
	return pandocPage{Tab: "pandoc", CSRF: authFrom(r.Context()).CSRF, Cfg: c, Enabled: c.Enabled, Path: c.Path, Memory: strconv.Itoa(c.MemoryMB),
		ImportList: strings.Join(c.ImportFormats, ", "), ExportList: strings.Join(c.ExportFormats, ", ")}, nil
}

func (s *Server) adminPandocPage(w http.ResponseWriter, r *http.Request) {
	p, ok := s.adminOnly(w, r)
	if !ok {
		return
	}
	d, err := s.pandocView(r, p)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.Saved = r.URL.Query().Get("saved") == "1"
	// what a form was answered with (a refusal with what was typed, or a test) comes by redirect, so this address can be reloaded
	if fl := s.flashTake(r); fl != nil {
		if fl["err"] != "" {
			d.Enabled, d.Path, d.Memory, d.Error, d.Field = fl["enabled"] == "1", fl["path"], fl["memory"], fl["err"], fl["field"]
		}
		if fl["test"] != "" {
			var t pandocTestView
			if json.Unmarshal([]byte(fl["test"]), &t) == nil {
				d.Test = &t
			}
		}
	}
	s.pandocRender(w, r, http.StatusOK, d)
}

func (s *Server) pandocRender(w http.ResponseWriter, r *http.Request, status int, d pandocPage) {
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, status, "admin-pandoc", s.page(r, s.tr(r, "pandoc.title"), d))
}

func (s *Server) uiAdminPandoc(w http.ResponseWriter, r *http.Request) {
	p, ok := s.adminOnly(w, r)
	if !ok {
		return
	}
	switch r.PathValue("action") {
	case "save":
		if err := s.refuseAnyCredentials(r); err != nil {
			e := ops.AsError(err)
			s.flashSet(r, map[string]string{"err": e.Message, "field": e.Field})
			http.Redirect(w, r, "/admin/pandoc", http.StatusSeeOther)
			return
		}
		en, path, mem := formBool(r, "enabled"), strings.TrimSpace(r.PostFormValue("path")), strings.TrimSpace(r.PostFormValue("memory_mb"))
		in := pandoc.SetIn{Enabled: &en, Path: &path}
		if mem != "" {
			n, err := strconv.Atoi(mem)
			if err != nil {
				n = -1
			}
			in.MemoryMB = &n
		}
		if _, err := s.Reg.Call(r.Context(), p, "pandoc.config.set", in); err != nil {
			e := ops.AsError(err)
			if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict {
				s.fail(w, r, err)
				return
			}
			field := e.Field
			if field == "" {
				field = "path"
			}
			fl := map[string]string{"err": redactPosted(r, e.Message), "field": field, "path": scrubValue(path), "memory": mem}
			if en {
				fl["enabled"] = "1"
			}
			s.flashSet(r, fl)
			http.Redirect(w, r, "/admin/pandoc", http.StatusSeeOther)
			return
		}
		http.Redirect(w, r, "/admin/pandoc?saved=1", http.StatusSeeOther)
	case "test":
		out, err := s.Reg.Call(r.Context(), p, "pandoc.config.test", struct{}{})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		t := out.(pandoc.TestOut)
		if b, err := json.Marshal(pandocTestView{Version: t.Version, OK: t.OK, In: t.Import, Out: t.Export}); err == nil {
			s.flashSet(r, map[string]string{"test": string(b)})
		}
		http.Redirect(w, r, "/admin/pandoc#test", http.StatusSeeOther)
	default:
		s.fail(w, r, ops.NotFound("page"))
	}
}
