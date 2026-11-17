package server

import (
	"errors"
	"html/template"
	"net/http"

	"github.com/crueber/scriobh/internal/auth"
	"github.com/crueber/scriobh/internal/domain/studio"
	"github.com/crueber/scriobh/internal/i18n"
	"github.com/crueber/scriobh/internal/ops"
)

// The private progress card (F-516): /card/{token}, the page; /card/{token}/card.svg, the same as a picture; and
// /card/{token}/card.json, the numbers, for the browser to draw a PNG from. The routes are public and are not registry ops
// (a caller here is a link, not a person), and they take the share links' care: no session or key is read and no cookie
// is set, the token is never logged (the request log records the route pattern), noindex and no-referrer, the same strict
// policy as every page, one rate limit per client address before the token is looked at, and one answer for every token
// that opens nothing (the share links' "gone" page). What a card holds is worked out at every request from numbers only:
// internal/domain/studio.Cards.Open (cached for a minute, at most four at a time, the token checked live every time, views counted in memory).

func (s *Server) registerCard(m *http.ServeMux) {
	m.HandleFunc("GET /card/{token}", s.cardHandler("page"))
	m.HandleFunc("GET /card/{token}/card.svg", s.cardHandler("svg"))
	m.HandleFunc("GET /card/{token}/card.json", s.cardHandler("json"))
	m.HandleFunc("/card/", s.shareGone) // every other path and method under /card/: the one answer
}

// cardPage is what the card's template reads.
type cardPage struct {
	View    studio.CardView
	SVG     template.HTML // made by studio.CardSVG, every word of it escaped there
	SVGPath string
	Streak  string
	Goals   []string // each goal on the card as a line of text, the same words and figures as the picture
}

func (s *Server) cardHandler(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := shareClient(auth.ClientIP(r, s.Cfg.TrustProxy))
		if per := s.Cfg.ShareRateLimit; per > 0 {
			if ok, wait := s.limiter.allow("shareip:"+ip, per); !ok {
				shareError(w, ops.RateLimited(wait))
				return
			}
		}
		v, err := s.cards.Open(r.Context(), r.PathValue("token"), kind == "page")
		if err != nil {
			if errors.Is(err, studio.ErrNoCard) {
				s.shareGone(w, r)
				return
			}
			s.Log.Error("progress card", "err", err)
			shareError(w, ops.Internal(err))
			return
		}
		lang, _ := sharePageLang(r, s.Cfg.OIDCStub)
		t := func(key string, args ...any) string { return i18n.Default.T(lang, key, args...) }
		switch kind {
		case "json":
			shareJSON(w, http.StatusOK, v)
		case "svg":
			h := w.Header()
			h.Set("Content-Type", "image/svg+xml; charset=utf-8")
			_, _ = w.Write(studio.CardSVG(v, t))
		default:
			p := Page{Lang: lang, UIDir: i18n.Default.Dir(lang), Bare: true, Version: s.Cfg.Version}
			p.Title = t("studio.card.title")
			cp := cardPage{View: v, SVG: template.HTML(studio.CardSVG(v, t)), SVGPath: r.URL.Path + "/card.svg"} //nolint:gosec // built by studio.CardSVG, which escapes every word
			for _, g := range v.Goals {
				line := t("studio.card.goal", "period", t("studio.period."+g.Period), "measure", t("studio.measure."+g.Measure))
				if g.Project != "" {
					line = t("studio.ui.card.goal_project", "goal", line, "project", isolate(g.Project))
				}
				cp.Goals = append(cp.Goals, t("studio.ui.card.goal_line", "goal", line, "value", commas(g.Value), "target", commas(g.Target)))
			}
			p.Data = cp
			s.render(w, r, http.StatusOK, "progress-card", p)
		}
	}
}
