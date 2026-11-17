package server

import (
	"errors"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/domain/docs"
	"github.com/crueber/scriobh/internal/domain/editiondiff"
	"github.com/crueber/scriobh/internal/domain/editions"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
)

// The edition diff screens: the project as it stands against one of its editions, and one edition against another,
// chapter by chapter and difference by difference. They are server-rendered GET pages computed from parameters that
// name things (an edition, a chapter's key) and carry no text and no choice; the two actions, offering a difference as
// a suggestion and putting a chapter back, are POSTs that answer with a 303 to a GET page and a single-use flash, so a
// reload repeats nothing. Everything the text of an edition says is data: it is only ever written by the template's
// escaping, each run in a block that isolates its direction. The text of an edition is for editors; the page shows
// readers the list of chapters and says so.

func (s *Server) registerEditionDiffUI(m *http.ServeMux) {
	m.HandleFunc("GET /p/{slug}/editions/compare", s.compareEditionsPage)
	m.HandleFunc("GET /p/{slug}/editions/{edn}/diff", s.editionDiffPage)
	s.uiRoute("POST /ui/projects/{id}/editions/{edn}/suggest", s.uiEditionSuggest)
	s.uiRoute("POST /ui/projects/{id}/editions/{edn}/take", s.uiEditionTake)
}

// diffStep is one stop of the review as a page draws it.
type diffStep struct {
	editiondiff.Step
	BaseTitle, HeadTitle string
	Link                 string // the page of its differences, for an editor, when there is something to look at
	Reviewable           bool
	GuessScore           string
	Position             int // 1-based among the steps with differences
}

type diffHunk struct {
	editiondiff.Hunk
	Total     int
	Next      string // where the next difference is (on this page, or the page after it)
	Offerable bool
	BaseWords int // the words of the edition's text and of the project's, for the folds of a long paragraph
	HeadWords int
	Fold      bool // the two full texts are folded: the word-by-word line is the view
	Status    bool // the sentence of the action just made is shown here
}

type diffPicker struct {
	Projects []projects.Project
	With     string // the project chosen for the second edition
	Editions []editionRow
}

type diffData struct {
	Project     projects.Project
	Slug        string
	CSRF        string
	EditionID   string
	EditionName string
	Compare     bool
	CanText     bool
	Res         *editiondiff.Result
	Steps       []diffStep // the steps with something to review, then the rest
	Quiet       []diffStep // unchanged steps
	Hidden      int        // steps beyond the ones drawn
	Step        *diffStep
	Hunks       []diffHunk
	Page, Pages int    // the page of differences shown, of how many
	From, To    int    // the numbers of the first and the last difference on it
	Total       int    // differences in the chapter
	PrevPage    string // the page before and after, when there is one
	NextPage    string
	OfferN      int // the differences on this page that can be offered as suggestions
	OfferIDs    []string
	Coarse      int // chapters counted by whole paragraph
	FlashAt     string
	FlashSoft   bool
	CanTake     bool
	Prev, Next  string
	Back        string
	Revision    int // the head document's revision as this page read it
	Notice      string
	Error       string
	Lines       []string
	DocLink     string
	Retry       int
	RetryURL    string
	Picker      *diffPicker
	Query       url.Values
	FirstLabel  string
}

const diffStepsShown = 1500

// diffPageSize is how many differences a page of a chapter shows: every difference is on some page, and the button that
// offers suggestions acts on the ones on its own page.
const diffPageSize = 50

// foldWords is the length of a paragraph from which the two full texts of a reworded difference are folded away.
const foldWords = 300

var reEditionID = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}$`)

func diffURL(slug, edn, chapter string) string { return diffURLAt(slug, edn, chapter, 0, "") }

// diffURLAt is the address of the diff page: every part of the path is one escaped segment, so a value that holds a slash, a
// question mark or a hash cannot change where it goes.
func diffURLAt(slug, edn, chapter string, page int, frag string) string {
	u := "/p/" + url.PathEscape(slug) + "/editions/" + url.PathEscape(edn) + "/diff"
	q := url.Values{}
	if chapter != "" {
		q.Set("chapter", chapter)
	}
	if page > 1 {
		q.Set("page", strconv.Itoa(page))
	}
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	if frag != "" {
		u += "#" + url.PathEscape(frag)
	}
	return u
}

func compareURL(slug string, q url.Values) string {
	return "/p/" + slug + "/editions/compare?" + q.Encode()
}

func joinTitles(refs []editiondiff.Ref) string {
	var t []string
	for _, r := range refs {
		if r.Title != "" {
			t = append(t, r.Title)
		}
	}
	return strings.Join(t, " + ")
}

// diffErrorText is a refused or failed op in words, and how long to wait when it is a busy answer.
// compare says the refusal is of a comparison with a second project: one sentence for a project that is not there, one that is
// not the person's, and one that is locked, so none of them is told from the others.
func (s *Server) diffErrorText(r *http.Request, err error, compare bool) (string, int) {
	T := s.page(r, "", nil).T
	var oe *ops.Error
	if !errors.As(err, &oe) {
		return T("editions.diff.failed"), 0
	}
	switch oe.Code {
	case ops.CodeRateLimited:
		return oe.Message, oe.RetryAfterSeconds
	case ops.CodeForbidden, ops.CodeNotFound:
		if compare {
			return T("editions.diff.cannot_compare"), 0
		}
		if oe.Code == ops.CodeForbidden {
			return T("editions.diff.text_for_editors"), 0
		}
		return T("editions.diff.gone"), 0
	}
	if oe.Data != nil {
		if rs, _ := oe.Data["reason"].(string); rs == "revision" {
			return T("editions.diff.stale"), 0
		}
	}
	return oe.Message, 0
}

// decorate draws the steps of a result: the ones with something to review first, in order, and the unchanged ones apart.
func (d *diffData) decorate(res *editiondiff.Result, edn string) {
	pos := 0
	var review, quiet []diffStep
	for _, st := range res.Steps {
		ds := diffStep{Step: st, BaseTitle: joinTitles(st.Base), HeadTitle: joinTitles(st.Head)}
		if st.Match != nil && st.Match.Guess {
			ds.GuessScore = strconv.Itoa(int(st.Match.Score*100+0.5)) + "%"
		}
		if st.Compared == editiondiff.ComparedCoarse {
			d.Coarse++
		}
		if st.Status == editiondiff.StatusUnchanged {
			quiet = append(quiet, ds)
			continue
		}
		ds.Reviewable = true
		pos++
		ds.Position = pos
		if d.CanText && st.Status != editiondiff.StatusAdded && st.Status != editiondiff.StatusRemoved {
			ds.Link = d.stepLink(st.Key)
		}
		review = append(review, ds)
	}
	if len(review) > diffStepsShown {
		d.Hidden = len(review) - diffStepsShown
		review = review[:diffStepsShown]
	}
	if len(quiet) > diffStepsShown {
		d.Hidden += len(quiet) - diffStepsShown
		quiet = quiet[:diffStepsShown]
	}
	d.Steps, d.Quiet = review, quiet
}

func (d *diffData) stepLink(key string) string {
	if d.Compare {
		q := url.Values{}
		for k, v := range d.Query {
			q[k] = v
		}
		q.Set("chapter", key)
		return compareURL(d.Slug, q)
	}
	return diffURL(d.Slug, d.EditionID, key)
}

func (d *diffData) listURL() string {
	if d.Compare {
		q := url.Values{}
		for k, v := range d.Query {
			if k != "chapter" {
				q[k] = v
			}
		}
		return compareURL(d.Slug, q)
	}
	return diffURL(d.Slug, d.EditionID, "")
}

// pageHref is the address of a page of the open chapter's differences, at a place on it.
func (d *diffData) pageHref(key string, page int, frag string) string {
	if d.Compare {
		q := url.Values{}
		for k, v := range d.Query {
			q[k] = v
		}
		q.Set("chapter", key)
		if page > 1 {
			q.Set("page", strconv.Itoa(page))
		}
		u := compareURL(d.Slug, q)
		if frag != "" {
			u += "#" + frag
		}
		return u
	}
	return diffURLAt(d.Slug, d.EditionID, key, page, frag)
}

// words is how many words a text has.
func words(s string) int { return len(strings.Fields(s)) }

// selectStep opens one step: the page of its differences asked for (diffPageSize at a time, so every difference is on some
// page), and the chapters before and after it among those with something to review.
func (d *diffData) selectStep(res *editiondiff.Result, key string, page int) {
	var order []string
	for _, st := range res.Steps {
		if st.Status != editiondiff.StatusUnchanged && st.Status != editiondiff.StatusAdded && st.Status != editiondiff.StatusRemoved {
			order = append(order, st.Key)
		}
	}
	for _, st := range res.Steps {
		if st.Key != key {
			continue
		}
		ds := diffStep{Step: st, BaseTitle: joinTitles(st.Base), HeadTitle: joinTitles(st.Head)}
		if st.Match != nil && st.Match.Guess {
			ds.GuessScore = strconv.Itoa(int(st.Match.Score*100+0.5)) + "%"
		}
		d.Step = &ds
		for j, k := range order {
			if k == key {
				if j > 0 {
					d.Prev = d.stepLink(order[j-1])
				}
				if j+1 < len(order) {
					d.Next = d.stepLink(order[j+1])
				}
			}
		}
		all := st.Hunks
		d.Total = len(all)
		d.Pages = (len(all) + diffPageSize - 1) / diffPageSize
		if d.Pages < 1 {
			d.Pages = 1
		}
		if page < 1 {
			page = 1
		}
		if page > d.Pages {
			page = d.Pages
		}
		d.Page = page
		lo, hi := (page-1)*diffPageSize, page*diffPageSize
		if hi > len(all) {
			hi = len(all)
		}
		if lo > hi {
			lo = hi
		}
		hunks := all[lo:hi]
		if len(hunks) > 0 {
			d.From, d.To = hunks[0].Index, hunks[len(hunks)-1].Index
		}
		if page > 1 {
			d.PrevPage = d.pageHref(key, page-1, "dfx-hunks")
		}
		if page < d.Pages {
			d.NextPage = d.pageHref(key, page+1, "dfx-hunks")
		}
		d.CanTake = !d.Compare && d.CanText && st.Offer != nil && st.Offer.Take
		for n, h := range hunks {
			dh := diffHunk{Hunk: h, Total: len(all), BaseWords: words(h.Base), HeadWords: words(h.Head)}
			dh.Fold = len(h.Segments) > 0 && (dh.BaseWords > foldWords || dh.HeadWords > foldWords)
			// the next difference, on this page or on the page after it (a difference with a button of its own has Tab for that)
			switch {
			case n+1 < len(hunks):
				dh.Next = "#diff-" + strconv.Itoa(hunks[n+1].Index)
			case hi < len(all):
				dh.Next = d.pageHref(key, page+1, "diff-"+strconv.Itoa(all[hi].Index))
			}
			dh.Offerable = !d.Compare && d.CanText && h.Offer == "" && st.Offer != nil && st.Offer.Suggest
			if dh.Offerable {
				d.OfferN++
				d.OfferIDs = append(d.OfferIDs, h.ID)
			}
			d.Hunks = append(d.Hunks, dh)
		}
	}
}

// takeDiffFlash takes the sentence an action left for this page: for this project and this edition, and nothing left for
// another page. The place it belongs on the page is checked here: a difference that is on the page, the list of differences,
// the take section, or else the top.
func (s *Server) takeDiffFlash(r *http.Request, d *diffData) {
	mine := func(m map[string]string) bool {
		return !d.Compare && m["pid"] == d.Project.ID && m["edn"] == d.EditionID
	}
	if fl := s.flashTakeFor(r, "editiondiff", mine); fl != nil {
		d.Notice, d.Error, d.DocLink = fl["ok"], fl["bad"], fl["doc"]
		d.FlashAt, d.FlashSoft = fl["at"], fl["soft"] == "1"
		if l := fl["lines"]; l != "" {
			d.Lines = strings.Split(l, "\n")
		}
	}
}

// flashPlace says where on the chapter's page the sentence is drawn: beside the difference it is about, above the list of
// differences, in the take section, or at the top when what it names is not on the page.
func (d *diffData) flashPlace() {
	if d.Notice == "" && d.Error == "" {
		return
	}
	switch {
	case d.Step == nil:
		d.FlashAt = ""
	case d.FlashAt == "dfx-hunks", d.FlashAt == "dfx-take" && d.CanTake:
	case strings.HasPrefix(d.FlashAt, "diff-"):
		found := false
		for i := range d.Hunks {
			if "diff-"+strconv.Itoa(d.Hunks[i].Index) == d.FlashAt {
				d.Hunks[i].Status, found = true, true
			}
		}
		if !found {
			d.FlashAt = ""
		}
	default:
		d.FlashAt = ""
	}
}

// editionDiffPage is GET /p/{slug}/editions/{edn}/diff[?chapter=KEY].
func (s *Server) editionDiffPage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProject(w, r)
	if !ok {
		return
	}
	edn := r.PathValue("edn")
	T := s.page(r, "", nil).T
	d := diffData{Project: proj, Slug: proj.Slug, CSRF: authFrom(r.Context()).CSRF, EditionID: edn,
		CanText: proj.Role == ops.ProjectOwner || proj.Role == ops.ProjectEditor}
	s.takeDiffFlash(r, &d)
	p := s.principal(r)
	eo, err := s.Reg.Call(r.Context(), p, "edition.get", editions.EditionIn{ProjectID: proj.ID, EditionID: edn})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	d.EditionName = eo.(editions.GetOut).Item.Label
	chapter := strings.TrimSpace(r.URL.Query().Get("chapter"))
	if chapter != "" && !d.CanText {
		d.Error, chapter = T("editions.diff.text_for_editors"), ""
	}
	out, err := s.Reg.Call(r.Context(), p, "edition.diff", editiondiff.DiffIn{ProjectID: proj.ID, EditionID: edn, Chapter: chapter})
	if err != nil {
		var oe *ops.Error
		if chapter != "" && errors.As(err, &oe) && oe.Code == ops.CodeNotFound {
			// the key is not in the comparison any more (a text changed): the list is the way back
			d.Error = T("editions.diff.gone")
			d.FlashAt = ""
			out, err = s.Reg.Call(r.Context(), p, "edition.diff", editiondiff.DiffIn{ProjectID: proj.ID, EditionID: edn})
			chapter = ""
		}
		if err != nil {
			s.fail(w, r, err)
			return
		}
	}
	res := out.(editiondiff.Result)
	d.Res = &res
	if chapter != "" {
		d.selectStep(&res, chapter, atoi(r.URL.Query().Get("page")))
		if d.Step != nil && d.Step.Offer != nil && len(d.Step.Head) == 1 && d.Step.Head[0].DocumentID != "" {
			if do, err := s.Reg.Call(r.Context(), p, "doc.get", docs.DocIn{ID: d.Step.Head[0].DocumentID}); err == nil {
				d.Revision = do.(docs.Item).Item.Revision
			}
		}
		d.Back = d.listURL()
		d.flashPlace()
	} else {
		d.FlashAt = ""
		d.decorate(&res, edn)
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, http.StatusOK, "editiondiff", s.page(r, T("editions.diff.title"), d))
}

// compareEditionsPage is GET /p/{slug}/editions/compare?edition=ID&with_project=ID&with_edition=ID[&chapter=KEY].
func (s *Server) compareEditionsPage(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProject(w, r)
	if !ok {
		return
	}
	T := s.page(r, "", nil).T
	q := r.URL.Query()
	edn := q.Get("edition")
	d := diffData{Project: proj, Slug: proj.Slug, CSRF: authFrom(r.Context()).CSRF, EditionID: edn, Compare: true,
		CanText: proj.Role == ops.ProjectOwner || proj.Role == ops.ProjectEditor, Query: url.Values{}}
	s.takeDiffFlash(r, &d)
	w.Header().Set("Cache-Control", "no-store")
	render := func() { s.render(w, r, http.StatusOK, "editiondiff", s.page(r, T("editions.diff.compare_title"), d)) }
	if !d.CanText {
		d.Error = T("editions.diff.text_for_editors")
		render()
		return
	}
	p := s.principal(r)
	// the first edition: of this project
	var first *editions.GetOut
	if edn != "" {
		if eo, err := s.Reg.Call(r.Context(), p, "edition.get", editions.EditionIn{ProjectID: proj.ID, EditionID: edn}); err == nil {
			g := eo.(editions.GetOut)
			first = &g
			d.FirstLabel = g.Item.Label
		}
	}
	if first == nil {
		// no edition chosen (or not this project's): choose the first from this project's list
		lo, err := s.Reg.Call(r.Context(), p, "edition.list", editions.ProjectIn{ProjectID: proj.ID})
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Picker = &diffPicker{With: proj.ID, Editions: s.editionRows(r, proj, lo.(editions.ListOut).Items)}
		if edn != "" {
			d.Error = T("editions.diff.gone")
		}
		render()
		return
	}
	with := q.Get("with_project")
	if with == "" {
		with = proj.ID
	}
	wEdn := q.Get("with_edition")
	// the picker: the projects this person edits, and the chosen one's editions
	if wEdn == "" || q.Get("pick") != "" {
		pk := &diffPicker{With: with}
		if po, err := s.Reg.Call(r.Context(), p, "project.list", projects.ListIn{}); err == nil {
			for _, x := range po.(projects.ListOut).Items {
				if x.Role == ops.ProjectOwner || x.Role == ops.ProjectEditor {
					pk.Projects = append(pk.Projects, x)
				}
			}
		}
		if lo, err := s.Reg.Call(r.Context(), p, "edition.list", editions.ProjectIn{ProjectID: with}); err == nil {
			var rows []editions.Edition
			for _, e := range lo.(editions.ListOut).Items {
				if !(with == proj.ID && e.ID == edn) {
					rows = append(rows, e)
				}
			}
			wp := proj
			if with != proj.ID {
				for _, x := range pk.Projects {
					if x.ID == with {
						wp = x
					}
				}
			}
			pk.Editions = s.editionRows(r, wp, rows)
		} else {
			d.Error, _ = s.diffErrorText(r, err, true)
		}
		d.Query.Set("edition", edn)
		d.Picker = pk
		render()
		return
	}
	d.Query.Set("edition", edn)
	d.Query.Set("with_project", with)
	d.Query.Set("with_edition", wEdn)
	chapter := strings.TrimSpace(q.Get("chapter"))
	out, err := s.Reg.Call(r.Context(), p, "edition.compare", editiondiff.CompareIn{ProjectID: proj.ID, EditionID: edn, WithProject: with, WithEdition: wEdn, Chapter: chapter})
	if err != nil {
		var oe *ops.Error
		if chapter != "" && errors.As(err, &oe) && oe.Code == ops.CodeNotFound {
			http.Redirect(w, r, d.listURL(), http.StatusSeeOther)
			return
		}
		d.Error, d.Retry = s.diffErrorText(r, err, true)
		d.RetryURL = r.URL.RequestURI()
		render()
		return
	}
	res := out.(editiondiff.Result)
	d.Res = &res
	if chapter != "" {
		d.selectStep(&res, chapter, atoi(q.Get("page")))
		d.Back = d.listURL()
	} else {
		d.decorate(&res, edn)
	}
	render()
}

// diffRedirect answers an action with a 303 to a GET page, carrying one flash that is for this project and this edition and
// says where on the page it belongs (the difference, the list of differences, the take section).
func (s *Server) diffRedirect(w http.ResponseWriter, r *http.Request, proj projects.Project, edn, chapter string, page int, at string, fl map[string]string) {
	fl["_scope"], fl["pid"], fl["edn"], fl["at"] = "editiondiff", proj.ID, edn, at
	s.flashSet(r, fl)
	// No fragment: the sentence is drawn beside what it is about and takes the focus, which brings it into view (a browser does not
	// apply autofocus to a page opened at a fragment).
	http.Redirect(w, r, diffURLAt(proj.Slug, edn, chapter, page, ""), http.StatusSeeOther)
}

// editionOf is the edition id of an action's address, or a not-found when it is not shaped like one (it is written into
// the address of the page the answer goes to).
func (s *Server) editionOf(w http.ResponseWriter, r *http.Request) (string, bool) {
	edn := r.PathValue("edn")
	if !reEditionID.MatchString(edn) {
		s.fail(w, r, ops.NotFound("edition"))
		return "", false
	}
	return edn, true
}

// uiEditionSuggest is POST /ui/projects/{id}/editions/{edn}/suggest: the one difference named, or the differences on the
// page the person was looking at (and no others), become suggestions. The op makes at most 20 at a call, so a page of 50 is
// three calls; what was made, and what was not and why, is said beside the difference or above the list.
func (s *Server) uiEditionSuggest(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	edn, ok := s.editionOf(w, r)
	if !ok {
		return
	}
	T := s.page(r, "", nil).T
	chapter, page := strings.TrimSpace(r.PostFormValue("chapter")), atoi(r.PostFormValue("page"))
	ids := r.PostForm["hunk"]
	single := len(ids) == 1
	at := "dfx-hunks"
	if single {
		if n := atoi(r.PostFormValue("at")); n > 0 {
			at = "diff-" + strconv.Itoa(n)
		}
	}
	if len(ids) > diffPageSize {
		s.diffRedirect(w, r, proj, edn, chapter, page, at, map[string]string{"bad": T("editions.diff.too_many")})
		return
	}
	if len(ids) == 0 { // the op would offer everything in the chapter: this page offers what is on it, and there is nothing
		s.diffRedirect(w, r, proj, edn, chapter, page, at, map[string]string{"bad": T("editions.diff.none_made"), "soft": "1"})
		return
	}
	rev := atoi(r.PostFormValue("base_revision"))
	made := 0
	skipped := map[string]int{}
	var order []string
	var stop string
	soft := false
	for lo := 0; lo < len(ids); lo += editiondiff.MaxSuggestHunks {
		hi := lo + editiondiff.MaxSuggestHunks
		if hi > len(ids) {
			hi = len(ids)
		}
		out, err := s.Reg.Call(r.Context(), s.principal(r), "edition.suggest", editiondiff.SuggestIn{ProjectID: proj.ID, EditionID: edn, Chapter: chapter, BaseRevision: rev, Hunks: ids[lo:hi]})
		if err != nil {
			text, wait := s.diffErrorText(r, err, false)
			if wait > 0 {
				text, soft = T("editions.diff.too_many_calls", "n", (wait+59)/60), true
			}
			stop = text
			break
		}
		so := out.(editiondiff.SuggestOut)
		made += len(so.Created)
		for _, sk := range so.Skipped {
			if skipped[sk.Reason] == 0 {
				order = append(order, sk.Reason)
			}
			skipped[sk.Reason]++
		}
		if so.Stopped != "" {
			stop = T("editions.diff.stopped", "reason", so.Stopped)
			break
		}
	}
	var lines []string
	for _, reason := range order {
		if n := skipped[reason]; n > 1 {
			lines = append(lines, T("editions.diff.skipped_line_n", "reason", reason, "n", n))
		} else {
			lines = append(lines, T("editions.diff.skipped_line", "reason", reason))
		}
	}
	fl := map[string]string{"lines": strings.Join(lines, "\n")}
	switch {
	case made == 0 && stop != "":
		fl["bad"], fl["soft"] = stop, "1"
		if !soft {
			delete(fl, "soft")
		}
	case made == 0:
		fl["bad"], fl["soft"] = T("editions.diff.none_made"), "1"
	case single:
		fl["ok"] = T("editions.diff.made", "n", made)
	default:
		fl["ok"] = T("editions.diff.made_page", "n", made, "total", len(ids))
		if stop != "" {
			fl["lines"] = strings.TrimPrefix(fl["lines"]+"\n"+T("editions.diff.page_partial", "reason", stop), "\n")
		}
	}
	s.diffRedirect(w, r, proj, edn, chapter, page, at, fl)
}

// uiEditionTake is POST /ui/projects/{id}/editions/{edn}/take: a whole chapter put back as the edition had it. The op needs the
// confirmation when the pairing was a guess; the page asks for it, and the op enforces it. A refusal is said in the take
// section; the answer to a success is the list again, since the keys of the steps are not the same after a write.
func (s *Server) uiEditionTake(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	edn, ok := s.editionOf(w, r)
	if !ok {
		return
	}
	T := s.page(r, "", nil).T
	chapter, page := strings.TrimSpace(r.PostFormValue("chapter")), atoi(r.PostFormValue("page"))
	in := editiondiff.TakeIn{ProjectID: proj.ID, EditionID: edn, Chapter: chapter, BaseRevision: atoi(r.PostFormValue("base_revision")), ConfirmGuess: r.PostFormValue("confirm_guess") == "1"}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "edition.take", in)
	if err != nil {
		var oe *ops.Error
		if errors.As(err, &oe) && oe.Data != nil {
			if rs, _ := oe.Data["reason"].(string); rs == "guess_unconfirmed" {
				s.diffRedirect(w, r, proj, edn, chapter, page, "dfx-take", map[string]string{"bad": T("editions.diff.need_confirm")})
				return
			}
		}
		text, _ := s.diffErrorText(r, err, false)
		s.diffRedirect(w, r, proj, edn, chapter, page, "dfx-take", map[string]string{"bad": text})
		return
	}
	to := out.(editiondiff.TakeOut)
	msg := T("editions.diff.taken")
	if to.Unchanged {
		msg = T("editions.diff.taken_already")
	}
	s.diffRedirect(w, r, proj, edn, "", 0, "", map[string]string{"ok": msg, "doc": to.DocumentID})
}
