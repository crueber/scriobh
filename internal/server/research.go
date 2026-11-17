package server

import (
	"html/template"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/domain/attachments"
	"github.com/crueber/scriobh/internal/domain/compendiums"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/domain/research"
	"github.com/crueber/scriobh/internal/domain/search"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/text"
)

// Research pages: the notes, clippings and folders a person keeps beside a book, for a project
// (/p/{slug}/research) and for a Compendium (/c/{slug}/research). One set of handlers serves both: the
// owner abstraction below says which ops to call and which addresses to write. Everything on a page is read
// and changed through the ops, which authorize; being on a project grants nothing in its Compendium, so a
// Compendium's research is only ever reached through /c/ pages and a 404 is the same for "missing" and
// "not yours". The pages work without script (every action is a form); research.js and pad.js only smooth
// them: focus after a change, the board by arrow keys, live refresh, autosave with a local draft.

// resOwner is the project or Compendium a research page belongs to.
type resOwner struct {
	Kind       string // "p" or "c"
	Scope      string // "project" or "compendium", for the live marker
	ID, Slug   string
	Name       string
	Base       string // /p/{slug} or /c/{slug}
	UI         string // /ui/projects/{id} or /ui/compendiums/{id}
	API        string // /api/v1/projects/{id} or /api/v1/compendiums/{id}
	Prefix     string // the op prefix: "project." or "compendium."
	CanEdit    bool
	Project    projects.Project
	Compendium compendiums.Compendium
}

func resProjectOwner(proj projects.Project, canEdit bool) resOwner {
	return resOwner{Kind: "p", Scope: "project", ID: proj.ID, Slug: proj.Slug, Name: proj.Title, Base: "/p/" + proj.Slug,
		UI: "/ui/projects/" + proj.ID, API: "/api/v1/projects/" + proj.ID, Prefix: "project.", CanEdit: canEdit, Project: proj}
}

func resCompendiumOwner(c compendiums.Compendium) resOwner {
	return resOwner{Kind: "c", Scope: "compendium", ID: c.ID, Slug: c.Slug, Name: c.Name, Base: "/c/" + c.Slug,
		UI: "/ui/compendiums/" + c.ID, API: "/api/v1/compendiums/" + c.ID, Prefix: "compendium.", CanEdit: compEditable(c), Compendium: c}
}

func (s *Server) resBySlug(w http.ResponseWriter, r *http.Request, kind string) (resOwner, bool) {
	if kind == "c" {
		c, ok := s.compendiumBySlug(w, r)
		if !ok {
			return resOwner{}, false
		}
		return resCompendiumOwner(c), true
	}
	proj, ok := s.goalsProject(w, r)
	if !ok {
		return resOwner{}, false
	}
	return resProjectOwner(proj, s.gitOwner(r, proj) || proj.Role == ops.ProjectEditor), true
}

func (s *Server) resByID(w http.ResponseWriter, r *http.Request, kind string) (resOwner, bool) {
	if kind == "c" {
		c, ok := s.resCompendiumByID(w, r, r.PathValue("id"))
		if !ok {
			return resOwner{}, false
		}
		return resCompendiumOwner(c), true
	}
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok {
		return resOwner{}, false
	}
	return resProjectOwner(proj, s.gitOwner(r, proj) || proj.Role == ops.ProjectEditor), true
}

// ---- words ----------------------------------------------------------------------------------------------

var (
	resKindLabel   = map[string]string{"note": "Note", "clipping": "Clipping", "folder": "Folder"}
	resStatusLabel = map[string]string{"inbox": "Inbox", "reading": "Reading", "useful": "Useful", "archived": "Archived"}
	resStatusOrder = []string{"inbox", "reading", "useful", "archived"}
	// A tag's colour token is drawn as a swatch of one of the cover colours the app already uses in every theme;
	// the tag's name always sits beside it.
	resColorClass = map[string]string{"slate": "cover-7", "clay": "cover-1", "ochre": "cover-6", "moss": "cover-4", "teal": "cover-0", "blue": "cover-2", "plum": "cover-5", "rose": "cover-3"}
	resColorWord  = map[string]string{"slate": "Slate", "clay": "Clay", "ochre": "Ochre", "moss": "Moss", "teal": "Teal", "blue": "Blue", "plum": "Plum", "rose": "Rose"}
)

var resSaved = map[string]string{
	"created":    "Added.",
	"moved":      "Moved.",
	"restored":   "Restored.",
	"saved":      "Saved.",
	"tag":        "Tag saved.",
	"tagdeleted": "Tag deleted. The notes keep everything else.",
	"tagmerged":  "Tags merged.",
}

// ---- view models ------------------------------------------------------------------------------------------

type resTagView struct {
	ID, Name, Color, Class, Word string
	Notes                        int
}

type resRow struct {
	research.Entry
	Level                         int
	Folder                        bool
	Kids                          int
	CanUp, CanDown, CanIn, CanOut bool
	TagViews                      []resTagView
	KindLabel, StatusLabel, When  string
}

type resFolder struct {
	ID, Label string
}

type resHit struct {
	ID, Title, Kind, MatchedIn string
	Parts                      []snipPart
}

type resData struct {
	resOwner
	CSRF       string
	View       string // tree or trash
	Rows       []resRow
	Folders    []resFolder
	Tags       []resTagView
	Tag        string
	Total      int // entries before paging
	Page       int
	Pages      int
	From, To   int
	PrevURL    string
	NextURL    string
	TreeURL    string // what the live refresh fetches
	Q          string
	Searched   bool
	Hits       []resHit
	HitTotal   int
	HitPrev    string
	HitNext    string
	Degraded   bool
	Message    string
	Saved      string
	SavedText  string
	Error      string
	Field      string
	Values     map[string]string
	TrashCount int
	Zone       string
}

const resPageSize = 100
const resHitSize = 20

func resTagViews(items []research.Tag) []resTagView {
	out := make([]resTagView, 0, len(items))
	for _, t := range items {
		cls := resColorClass[t.Color]
		if cls == "" {
			cls = resColorClass["slate"]
		}
		out = append(out, resTagView{ID: t.ID, Name: t.Name, Color: t.Color, Class: cls, Word: resColorWord[t.Color], Notes: t.Notes})
	}
	return out
}

func resTagsFor(names []string, byName map[string]resTagView) []resTagView {
	var out []resTagView
	for _, n := range names {
		if t, ok := byName[strings.ToLower(n)]; ok {
			out = append(out, t)
		} else {
			out = append(out, resTagView{Name: n, Class: resColorClass["slate"], Word: "Slate"})
		}
	}
	return out
}

func (s *Server) resTags(r *http.Request, o resOwner) ([]resTagView, map[string]resTagView, []string, error) {
	out, err := s.Reg.Call(r.Context(), s.principal(r), o.Prefix+"tag.list", research.TagListIn{ID: o.ID})
	if err != nil {
		return nil, nil, nil, err
	}
	l := out.(research.TagListOut)
	views := resTagViews(l.Items)
	by := map[string]resTagView{}
	for _, v := range views {
		by[strings.ToLower(v.Name)] = v
	}
	return views, by, l.Colors, nil
}

// resSiblings groups entries by parent in position order.
func resSiblings(items []research.Entry) map[string][]research.Entry {
	kids := map[string][]research.Entry{}
	for _, e := range items {
		kids[e.ParentID] = append(kids[e.ParentID], e)
	}
	for k := range kids {
		ks := kids[k]
		sort.SliceStable(ks, func(i, j int) bool {
			if ks[i].Position != ks[j].Position {
				return ks[i].Position < ks[j].Position
			}
			return ks[i].ID < ks[j].ID
		})
	}
	return kids
}

type resQuery struct {
	View, Tag, Q  string
	Page, HitPage int
}

func resParseQuery(r *http.Request) resQuery {
	q := r.URL.Query()
	v := resQuery{View: q.Get("view"), Tag: strings.TrimSpace(q.Get("tag")), Q: strings.TrimSpace(q.Get("q"))}
	if v.View != "trash" {
		v.View = "tree"
	}
	v.Page, _ = strconv.Atoi(q.Get("p"))
	if v.Page < 1 {
		v.Page = 1
	}
	v.HitPage, _ = strconv.Atoi(q.Get("hp"))
	if v.HitPage < 1 {
		v.HitPage = 1
	}
	return v
}

func (o resOwner) link(path string, kv ...string) string {
	u := o.Base + path
	vals := url.Values{}
	for i := 0; i+1 < len(kv); i += 2 {
		if kv[i+1] != "" {
			vals.Set(kv[i], kv[i+1])
		}
	}
	if len(vals) > 0 {
		u += "?" + vals.Encode()
	}
	return u
}

// resView reads everything the list page and its fragment show.
func (s *Server) resView(r *http.Request, o resOwner, q resQuery) (resData, error) {
	ctx := r.Context()
	p := s.principal(r)
	d := resData{resOwner: o, CSRF: authFrom(ctx).CSRF, View: q.View, Tag: q.Tag, Q: q.Q, Values: map[string]string{}}
	tags, byName, _, err := s.resTags(r, o)
	if err != nil {
		return d, err
	}
	d.Tags = tags
	in := research.ListIn{ID: o.ID, Trash: q.View == "trash", Tag: q.Tag}
	lo, err := s.Reg.Call(ctx, p, o.Prefix+"research.list", in)
	if err != nil {
		return d, err
	}
	list := lo.(research.ListOut)
	loc := s.userZone(r)
	d.Zone = loc.String()
	byID := map[string]research.Entry{}
	for _, e := range list.Items {
		byID[e.ID] = e
	}
	kids := resSiblings(list.Items)
	kidCount := map[string]int{}
	for _, e := range list.Items {
		if e.ParentID != "" {
			kidCount[e.ParentID]++
		}
	}
	ordered := list.Order
	if len(ordered) == 0 { // a filter or the trash: a flat list
		ordered = make([]string, 0, len(list.Items))
		for _, e := range list.Items {
			ordered = append(ordered, e.ID)
		}
	}
	flat := len(list.Order) == 0
	depth := func(e research.Entry) int {
		n := 0
		for a, hops := e.ParentID, 0; a != "" && hops < 16; hops++ {
			pe, ok := byID[a]
			if !ok {
				break
			}
			n++
			a = pe.ParentID
		}
		return n
	}
	for _, e := range list.Items {
		if e.Kind == "folder" && !e.Trashed {
			d.Folders = append(d.Folders, resFolder{ID: e.ID, Label: strings.Repeat(" ", depth(e)) + displayTitle(e.Title)})
		}
	}
	// The list is ordered by position, folders are shown before their contents; resFolder order follows it.
	if !flat {
		idx := map[string]int{}
		for i, id := range ordered {
			idx[id] = i
		}
		sort.SliceStable(d.Folders, func(i, j int) bool { return idx[d.Folders[i].ID] < idx[d.Folders[j].ID] })
	}
	rows := make([]resRow, 0, len(ordered))
	for _, id := range ordered {
		e, ok := byID[id]
		if !ok {
			continue
		}
		row := resRow{Entry: e, Folder: e.Kind == "folder", Kids: kidCount[e.ID], KindLabel: resKindLabel[e.Kind], StatusLabel: resStatusLabel[e.Status],
			TagViews: resTagsFor(e.Tags, byName), When: whenIn(e.UpdatedAt, loc)}
		if !flat {
			row.Level = depth(e)
			sibs := kids[e.ParentID]
			i := 0
			for k, sb := range sibs {
				if sb.ID == e.ID {
					i = k
				}
			}
			row.CanUp, row.CanDown = i > 0, i < len(sibs)-1
			row.CanIn = i > 0 && sibs[i-1].Kind == "folder" && row.Level < 11
			row.CanOut = e.ParentID != ""
		}
		if row.Level > 8 {
			row.Level = 8
		}
		rows = append(rows, row)
	}
	d.Total = len(rows)
	d.Pages = (len(rows) + resPageSize - 1) / resPageSize
	if d.Pages < 1 {
		d.Pages = 1
	}
	d.Page = q.Page
	if d.Page > d.Pages {
		d.Page = d.Pages
	}
	lo2, hi := (d.Page-1)*resPageSize, d.Page*resPageSize
	if hi > len(rows) {
		hi = len(rows)
	}
	if lo2 > hi {
		lo2 = hi
	}
	d.Rows = rows[lo2:hi]
	d.From, d.To = lo2+1, hi
	if len(rows) == 0 {
		d.From = 0
	}
	if d.Page > 1 {
		d.PrevURL = o.link("/research", "view", viewParam(q.View), "tag", q.Tag, "p", strconv.Itoa(d.Page-1))
	}
	if d.Page < d.Pages {
		d.NextURL = o.link("/research", "view", viewParam(q.View), "tag", q.Tag, "p", strconv.Itoa(d.Page+1))
	}
	d.TreeURL = o.UI + "/research/tree?" + url.Values{"view": {viewParam(q.View)}, "tag": {q.Tag}, "p": {strconv.Itoa(d.Page)}}.Encode()
	return d, nil
}

func viewParam(v string) string {
	if v == "trash" {
		return "trash"
	}
	return ""
}

// resSearch fills the search part of the page.
func (s *Server) resSearch(r *http.Request, o resOwner, d *resData, q resQuery) {
	if q.Q == "" {
		return
	}
	d.Searched = true
	op := "search.research"
	if o.Kind == "c" {
		op = "search.compendium_research"
	}
	off := (q.HitPage - 1) * resHitSize
	out, err := s.Reg.Call(r.Context(), s.principal(r), op, search.ResearchQueryIn{ID: o.ID, Q: q.Q, Limit: resHitSize, Offset: off})
	if err != nil {
		e := ops.AsError(err)
		if e.Code == ops.CodeInvalid {
			d.Message = e.Message
			return
		}
		d.Message = "Search could not be run just now."
		return
	}
	res := out.(search.ResearchQueryOut)
	d.HitTotal, d.Degraded = res.Total, res.Degraded
	for _, h := range res.Items {
		d.Hits = append(d.Hits, resHit{ID: h.NoteID, Title: displayTitle(h.Title), Kind: resKindLabel[h.Kind], MatchedIn: h.MatchedIn, Parts: snippetParts(h.SnippetHTML)})
	}
	if q.HitPage > 1 {
		d.HitPrev = o.link("/research", "q", q.Q, "hp", strconv.Itoa(q.HitPage-1))
	}
	if res.NextOffset > 0 {
		d.HitNext = o.link("/research", "q", q.Q, "hp", strconv.Itoa(q.HitPage+1))
	}
}

// ---- pages --------------------------------------------------------------------------------------------------

func (s *Server) resListPage(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		o, ok := s.resBySlug(w, r, kind)
		if !ok {
			return
		}
		q := resParseQuery(r)
		d, err := s.resView(r, o, q)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		s.resSearch(r, o, &d, q)
		d.Saved = r.URL.Query().Get("saved")
		d.SavedText = resSavedText(d.Saved, r.URL.Query().Get("n"))
		s.render(w, r, http.StatusOK, "research", s.page(r, "Research · "+o.Name, d))
	}
}

var resDigits = regexp.MustCompile(`^[0-9]{1,6}$`)

func resSavedText(code, n string) string {
	switch code {
	case "trashed":
		c, _ := strconv.Atoi(n)
		if !resDigits.MatchString(n) || c < 1 {
			return "Moved to the trash."
		}
		if c == 1 {
			return "Moved 1 note to the trash."
		}
		return "Moved " + strconv.Itoa(c) + " notes to the trash: a folder takes what it holds, and restoring it brings them back."
	case "restored":
		c, _ := strconv.Atoi(n)
		if resDigits.MatchString(n) && c > 1 {
			return "Restored " + strconv.Itoa(c) + " notes."
		}
	}
	return resSaved[code]
}

func (s *Server) resTreeFragment(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		o, ok := s.resByID(w, r, kind)
		if !ok {
			return
		}
		d, err := s.resView(r, o, resParseQuery(r))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "private, no-cache")
		s.fragment(w, r, http.StatusOK, "research-tree", "research-tree", d)
	}
}

// ---- the board ----------------------------------------------------------------------------------------------------

type resCard struct {
	research.Entry
	TagViews  []resTagView
	KindLabel string
}

type resColumn struct {
	Key, Label, Class string
	Count             int
	Cards             []resCard
	Hidden            int
	More              string
}

type resBoardData struct {
	resOwner
	CSRF    string
	By      string
	Columns []resColumn
	Error   string
	Saved   string
	Total   int
}

const resColumnShown = 40

func (s *Server) resBoardView(r *http.Request, o resOwner) (resBoardData, error) {
	by := r.URL.Query().Get("by")
	if by != "tag" {
		by = "status"
	}
	all := r.URL.Query().Get("all")
	d := resBoardData{resOwner: o, CSRF: authFrom(r.Context()).CSRF, By: by}
	_, byName, _, err := s.resTags(r, o)
	if err != nil {
		return d, err
	}
	out, err := s.Reg.Call(r.Context(), s.principal(r), o.Prefix+"research.board", research.BoardIn{ID: o.ID, By: by})
	if err != nil {
		return d, err
	}
	for _, c := range out.(research.BoardOut).Columns {
		col := resColumn{Key: c.Key, Label: c.Label, Count: c.Count, Class: resColorClass[c.Color]}
		shown := c.Items
		if all != c.Key && len(shown) > resColumnShown {
			col.Hidden = len(shown) - resColumnShown
			shown = shown[:resColumnShown]
			col.More = o.link("/research/board", "by", by, "all", c.Key)
		}
		for _, e := range shown {
			if e.Kind == "folder" {
				continue
			}
			col.Cards = append(col.Cards, resCard{Entry: e, TagViews: resTagsFor(e.Tags, byName), KindLabel: resKindLabel[e.Kind]})
		}
		d.Total += c.Count
		d.Columns = append(d.Columns, col)
	}
	return d, nil
}

func (s *Server) resBoardPage(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		o, ok := s.resBySlug(w, r, kind)
		if !ok {
			return
		}
		d, err := s.resBoardView(r, o)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Error = resBoardMsg(r.URL.Query().Get("e"))
		s.render(w, r, http.StatusOK, "research-board", s.page(r, "Research board · "+o.Name, d))
	}
}

func resBoardMsg(code string) string {
	if code == "stale" {
		return "That note changed somewhere else, so it was not moved. The board shows what it is now."
	}
	return ""
}

func (s *Server) resBoardFragment(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		o, ok := s.resByID(w, r, kind)
		if !ok {
			return
		}
		d, err := s.resBoardView(r, o)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		w.Header().Set("Cache-Control", "private, no-cache")
		s.fragment(w, r, http.StatusOK, "research-board", "research-board", d)
	}
}

// ---- tags page -------------------------------------------------------------------------------------------------

type resTagsData struct {
	resOwner
	CSRF      string
	Tags      []resTagView
	Colors    []resColor
	Error     string
	Field     string
	Values    map[string]string
	Saved     string
	SavedText string
}

type resColor struct{ Token, Word, Class string }

func (s *Server) resTagsView(r *http.Request, o resOwner) (resTagsData, error) {
	views, _, colors, err := s.resTags(r, o)
	d := resTagsData{resOwner: o, CSRF: authFrom(r.Context()).CSRF, Tags: views, Values: map[string]string{}}
	if err != nil {
		return d, err
	}
	for _, c := range colors {
		d.Colors = append(d.Colors, resColor{Token: c, Word: resColorWord[c], Class: resColorClass[c]})
	}
	return d, nil
}

func (s *Server) resTagsPage(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		o, ok := s.resBySlug(w, r, kind)
		if !ok {
			return
		}
		d, err := s.resTagsView(r, o)
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Saved = r.URL.Query().Get("saved")
		d.SavedText = resSaved[d.Saved]
		s.render(w, r, http.StatusOK, "research-tags", s.page(r, "Tags · "+o.Name, d))
	}
}

// ---- the note page -----------------------------------------------------------------------------------------------

type resConflict struct {
	Body, Title, Quote string
	Revision           int
}

type resAttachment struct {
	ID, Filename, Kind, Size, URL string
}

type resNoteData struct {
	DraftKey string // where this browser keeps the person's unsaved text of the note: their id is in it
	resOwner
	CSRF        string
	Note        research.Note
	TagsText    string
	TagNames    []string
	AllTags     []resTagView
	KindLabel   string
	Folder      string
	Statuses    []resOption
	Preview     template.HTML
	HasPreview  bool
	Conflict    *resConflict
	Error       string
	Field       string
	Saved       string
	SavedText   string
	Attachments []resAttachment
	Words       int
	Updated     string
	Source      string // the clipping's address, as text
	FilesURL    string
	Zone        string
	Values      map[string]string
}

type resOption struct {
	Value, Label string
	Selected     bool
}

var resAttRef = regexp.MustCompile(`/files/(att_[0-9A-Za-z]+)`)

func resPreview(md string) template.HTML {
	if strings.TrimSpace(md) == "" {
		return ""
	}
	// RenderHTML is a whitelist renderer: raw HTML is shown as text, unknown schemes lose their link, images are
	// not fetched, so the fragment is safe to place in the page.
	return template.HTML(text.RenderHTML(md, text.HTMLOptions{Private: true})) //nolint:gosec
}

func (s *Server) resNoteView(r *http.Request, o resOwner, id string) (resNoteData, error) {
	ctx := r.Context()
	p := s.principal(r)
	out, err := s.Reg.Call(ctx, p, o.Prefix+"research.get", research.NoteIn{ID: o.ID, NoteID: id})
	if err != nil {
		return resNoteData{}, err
	}
	n := out.(research.Item).Item
	d := resNoteData{DraftKey: "scriobh:research:" + authFrom(ctx).P.UserID + ":" + o.Kind + ":" + o.ID + ":" + id, resOwner: o, CSRF: authFrom(ctx).CSRF, Note: n, KindLabel: resKindLabel[n.Kind], Values: map[string]string{}, Zone: s.userZone(r).String(),
		TagNames: n.Tags, TagsText: strings.Join(n.Tags, ", "), Words: n.WordCount, Source: n.SourceURL, FilesURL: o.Base + "/files"}
	d.Updated = whenIn(n.UpdatedAt, s.userZone(r))
	views, _, _, err := s.resTags(r, o)
	if err == nil {
		d.AllTags = views
	}
	d.Statuses = resStatusOptions(n.Status)
	d.Preview = resPreview(n.Body)
	d.HasPreview = d.Preview != ""
	// Attachments the note links to: read from its text, looked up among the owner's attachments.
	ids := map[string]bool{}
	for _, m := range resAttRef.FindAllStringSubmatch(n.Body, -1) {
		ids[m[1]] = true
	}
	if len(ids) > 0 {
		if ao, aerr := s.Reg.Call(ctx, p, o.Prefix+"attachment.list", attachments.ListIn{ID: o.ID, Limit: 500}); aerr == nil {
			for _, a := range ao.(attachments.ListOut).Items {
				if ids[a.ID] {
					d.Attachments = append(d.Attachments, resAttachment{ID: a.ID, Filename: a.Filename, Kind: a.Kind, Size: resBytes(a.Bytes), URL: a.URL})
				}
			}
		}
	}
	if n.ParentID != "" {
		if po, perr := s.Reg.Call(ctx, p, o.Prefix+"research.get", research.NoteIn{ID: o.ID, NoteID: n.ParentID}); perr == nil {
			d.Folder = displayTitle(po.(research.Item).Item.Title)
		}
	}
	return d, nil
}

func resStatusOptions(cur string) []resOption {
	var out []resOption
	for _, st := range resStatusOrder {
		out = append(out, resOption{Value: st, Label: resStatusLabel[st], Selected: st == cur})
	}
	return out
}

func resBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
	case n >= 1<<10:
		return strconv.FormatFloat(float64(n)/(1<<10), 'f', 0, 64) + " KB"
	}
	return strconv.FormatInt(n, 10) + " bytes"
}

func (s *Server) resNotePage(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		o, ok := s.resBySlug(w, r, kind)
		if !ok {
			return
		}
		d, err := s.resNoteView(r, o, r.PathValue("note"))
		if err != nil {
			s.fail(w, r, err)
			return
		}
		d.Saved = r.URL.Query().Get("saved")
		d.SavedText = resSaved[d.Saved]
		s.render(w, r, http.StatusOK, "research-note", s.page(r, displayTitle(d.Note.Title)+" · Research", d))
	}
}

// resNoteFail shows the note page again with a refused value, or with the two versions of a note that changed.
func (s *Server) resNoteFail(w http.ResponseWriter, r *http.Request, o resOwner, id string, err error, mine map[string]string) {
	e := ops.AsError(err)
	if e.Code != ops.CodeInvalid && e.Code != ops.CodeConflict && e.Code != ops.CodeTooLarge {
		s.fail(w, r, err)
		return
	}
	d, verr := s.resNoteView(r, o, id)
	if verr != nil {
		s.fail(w, r, verr)
		return
	}
	status := http.StatusUnprocessableEntity
	if e.Code == ops.CodeConflict {
		status = http.StatusConflict
		d.Conflict = &resConflict{Body: d.Note.Body, Title: d.Note.Title, Quote: d.Note.Quote, Revision: d.Note.Revision}
		d.Error = "This note was changed somewhere else after you opened it. Your text is kept below, and nothing was lost. Choose which to keep."
	} else {
		d.Error, d.Field = e.Message, e.Field
	}
	// What the person typed stays in the form.
	d.Values = mine
	if v, ok := mine["title"]; ok {
		d.Note.Title = v
	}
	if v, ok := mine["body"]; ok {
		d.Note.Body = v
	}
	if v, ok := mine["quote"]; ok {
		d.Note.Quote = v
	}
	if v, ok := mine["source_url"]; ok {
		d.Note.SourceURL = v
	}
	if v, ok := mine["retrieved_on"]; ok {
		d.Note.RetrievedOn = v
	}
	if v, ok := mine["tags"]; ok {
		d.TagsText = v
	}
	if v, ok := mine["status"]; ok {
		d.Statuses = resStatusOptions(v)
	}
	s.render(w, r, status, "research-note", s.page(r, displayTitle(d.Note.Title)+" · Research", d))
}

// ---- changes ----------------------------------------------------------------------------------------------------

func formTags(v string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range strings.Split(v, ",") {
		t = strings.TrimSpace(t)
		k := strings.ToLower(t)
		if t != "" && !seen[k] {
			seen[k] = true
			out = append(out, t)
		}
	}
	return out
}

func resForm(r *http.Request) map[string]string {
	m := map[string]string{}
	for k, v := range r.PostForm {
		if len(v) > 0 && k != "_csrf" {
			m[k] = v[0]
		}
	}
	return m
}

func (s *Server) resRedirect(w http.ResponseWriter, r *http.Request, to string) {
	http.Redirect(w, r, to, http.StatusSeeOther)
}

// uiResearch applies one action. The path names it; the forms carry the note, the revision it was drawn from and
// where to go back to.
func (s *Server) uiResearch(kind string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		o, ok := s.resByID(w, r, kind)
		if !ok {
			return
		}
		action := r.PathValue("action")
		if action == "preview" { // any member may preview text; nothing is stored
			w.Header().Set("Cache-Control", "no-store")
			prev := resPreview(r.PostFormValue("body"))
			if prev == "" {
				prev = template.HTML("<p class=\"fine\">Nothing to preview yet.</p>")
			}
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write([]byte(prev))
			return
		}
		if !o.CanEdit {
			s.fail(w, r, ops.Forbidden("You can read this research but not change it."))
			return
		}
		p := s.principal(r)
		ctx := r.Context()
		noteID := r.PostFormValue("note_id")
		pre := o.Prefix + "research."
		var err error
		switch action {
		case "create":
			kindV := r.PostFormValue("kind")
			in := research.CreateIn{ID: o.ID, Title: strings.TrimSpace(r.PostFormValue("title")), Kind: kindV, ParentID: r.PostFormValue("parent_id")}
			var out any
			if out, err = s.Reg.Call(ctx, p, pre+"create", in); err == nil {
				n := out.(research.Item).Item
				if n.Kind == "folder" {
					s.resRedirect(w, r, o.link("/research", "saved", "created"))
				} else {
					s.resRedirect(w, r, o.link("/research/"+n.ID, "saved", "created"))
				}
				return
			}
		case "move":
			err = s.resMove(r, o, noteID, r.PostFormValue("act"))
			if err == nil {
				s.resRedirect(w, r, o.link("/research", "view", "", "p", r.PostFormValue("p"), "saved", "moved")+"#n-"+noteID)
				return
			}
		case "trash":
			var out any
			if out, err = s.Reg.Call(ctx, p, pre+"trash", research.NoteIn{ID: o.ID, NoteID: noteID}); err == nil {
				s.resRedirect(w, r, o.link("/research", "saved", "trashed", "n", strconv.Itoa(out.(research.TrashOut).Count)))
				return
			}
		case "restore":
			var out any
			if out, err = s.Reg.Call(ctx, p, pre+"restore", research.NoteIn{ID: o.ID, NoteID: noteID}); err == nil {
				s.resRedirect(w, r, o.link("/research", "view", "trash", "saved", "restored", "n", strconv.Itoa(out.(research.TrashOut).Count)))
				return
			}
		case "save":
			mine := resForm(r)
			rev := atoi(r.PostFormValue("rev"))
			in := research.UpdateIn{ID: o.ID, NoteID: noteID, BaseRevision: rev}
			t := strings.TrimSpace(r.PostFormValue("title"))
			in.Title = &t
			st := r.PostFormValue("status")
			if st != "" {
				in.Status = &st
			}
			tags := formTags(r.PostFormValue("tags"))
			if tags == nil {
				tags = []string{}
			}
			in.Tags = &tags
			if _, has := r.PostForm["body"]; has {
				b := r.PostFormValue("body")
				in.Body = &b
			}
			if _, has := r.PostForm["source_url"]; has {
				u, ro, qt := strings.TrimSpace(r.PostFormValue("source_url")), strings.TrimSpace(r.PostFormValue("retrieved_on")), r.PostFormValue("quote")
				in.SourceURL, in.RetrievedOn, in.Quote = &u, &ro, &qt
			}
			if _, err = s.Reg.Call(ctx, p, pre+"update", in); err == nil {
				s.resRedirect(w, r, o.link("/research/"+noteID, "saved", "saved"))
				return
			}
			s.resNoteFail(w, r, o, noteID, err, mine)
			return
		case "board-move":
			err = s.resBoardMove(r, o)
			if err == nil {
				s.resRedirect(w, r, o.link("/research/board", "by", r.PostFormValue("by"))+"#c-"+noteID)
				return
			}
			if ops.AsError(err).Code == ops.CodeConflict {
				s.resRedirect(w, r, o.link("/research/board", "by", r.PostFormValue("by"), "e", "stale"))
				return
			}
		case "tag-create":
			_, err = s.Reg.Call(ctx, p, o.Prefix+"tag.create", research.TagCreateIn{ID: o.ID, Name: strings.TrimSpace(r.PostFormValue("name")), Color: r.PostFormValue("color")})
			if err == nil {
				s.resRedirect(w, r, o.link("/research/tags", "saved", "tag"))
				return
			}
		case "tag-update":
			n, c := strings.TrimSpace(r.PostFormValue("name")), r.PostFormValue("color")
			_, err = s.Reg.Call(ctx, p, o.Prefix+"tag.update", research.TagUpdateIn{ID: o.ID, TagID: r.PostFormValue("tag_id"), Name: &n, Color: &c})
			if err == nil {
				s.resRedirect(w, r, o.link("/research/tags", "saved", "tag"))
				return
			}
		case "tag-merge":
			_, err = s.Reg.Call(ctx, p, o.Prefix+"tag.merge", research.TagMergeIn{ID: o.ID, TagID: r.PostFormValue("tag_id"), IntoID: r.PostFormValue("into_id")})
			if err == nil {
				s.resRedirect(w, r, o.link("/research/tags", "saved", "tagmerged"))
				return
			}
		case "tag-delete":
			_, err = s.Reg.Call(ctx, p, o.Prefix+"tag.delete", research.TagIn{ID: o.ID, TagID: r.PostFormValue("tag_id")})
			if err == nil {
				s.resRedirect(w, r, o.link("/research/tags", "saved", "tagdeleted"))
				return
			}
		default:
			err = ops.Invalid("", "That form was not recognised.")
		}
		s.resFail(w, r, o, action, err)
	}
}

// resFail shows a refusal beside the page the action came from, in plain words.
func (s *Server) resFail(w http.ResponseWriter, r *http.Request, o resOwner, action string, err error) {
	e := ops.AsError(err)
	switch e.Code {
	case ops.CodeInvalid, ops.CodeConflict, ops.CodeTooLarge:
	default:
		s.fail(w, r, err)
		return
	}
	if isFetch(r) {
		s.fail(w, r, err)
		return
	}
	if strings.HasPrefix(action, "tag-") {
		d, verr := s.resTagsView(r, o)
		if verr != nil {
			s.fail(w, r, verr)
			return
		}
		d.Error, d.Field, d.Values = e.Message, e.Field, resForm(r)
		s.render(w, r, http.StatusUnprocessableEntity, "research-tags", s.page(r, "Tags · "+o.Name, d))
		return
	}
	d, verr := s.resView(r, o, resQuery{View: "tree", Page: 1, HitPage: 1})
	if verr != nil {
		s.fail(w, r, verr)
		return
	}
	d.Error, d.Field, d.Values = e.Message, e.Field, resForm(r)
	s.render(w, r, http.StatusUnprocessableEntity, "research", s.page(r, "Research · "+o.Name, d))
}

// resMove works out where a note goes from the tree as it is now (never from what the page drew), then moves it.
func (s *Server) resMove(r *http.Request, o resOwner, id, act string) error {
	lo, err := s.Reg.Call(r.Context(), s.principal(r), o.Prefix+"research.list", research.ListIn{ID: o.ID})
	if err != nil {
		return err
	}
	items := lo.(research.ListOut).Items
	byID := map[string]research.Entry{}
	for _, e := range items {
		byID[e.ID] = e
	}
	e, ok := byID[id]
	if !ok {
		return ops.NotFound("note")
	}
	kids := resSiblings(items)
	sibs := kids[e.ParentID]
	i := -1
	for k, sb := range sibs {
		if sb.ID == id {
			i = k
		}
	}
	in := research.MoveIn{ID: o.ID, NoteID: id}
	parent := e.ParentID
	switch act {
	case "up":
		if i <= 0 {
			return ops.Invalid("", "It is already first.")
		}
		after := ""
		if i > 1 {
			after = sibs[i-2].ID
		}
		in.ParentID, in.After = &parent, &after
	case "down":
		if i < 0 || i >= len(sibs)-1 {
			return ops.Invalid("", "It is already last.")
		}
		after := sibs[i+1].ID
		in.ParentID, in.After = &parent, &after
	case "in":
		if i <= 0 || sibs[i-1].Kind != "folder" {
			return ops.Invalid("", "Only a folder directly above can hold it.")
		}
		np := sibs[i-1].ID
		in.ParentID = &np
	case "out":
		if e.ParentID == "" {
			return ops.Invalid("", "It is already at the top level.")
		}
		pp := byID[e.ParentID].ParentID
		after := e.ParentID
		in.ParentID, in.After = &pp, &after
	default:
		return ops.Invalid("", "Move it up, down, in or out.")
	}
	_, err = s.Reg.Call(r.Context(), s.principal(r), o.Prefix+"research.move", in)
	return err
}

// resBoardMove changes a note's board column: its status, or (by tag) one tag for another.
func (s *Server) resBoardMove(r *http.Request, o resOwner) error {
	id, to, from := r.PostFormValue("note_id"), r.PostFormValue("to"), r.PostFormValue("from")
	rev := atoi(r.PostFormValue("rev"))
	in := research.UpdateIn{ID: o.ID, NoteID: id, BaseRevision: rev}
	if r.PostFormValue("by") == "tag" {
		// The columns are tag ids; the names come from the owner's own tag list, never from the form.
		names := map[string]string{}
		if views, _, _, terr := s.resTags(r, o); terr == nil {
			for _, v := range views {
				names[v.ID] = v.Name
			}
		}
		toL, fromL := names[to], names[from]
		out, err := s.Reg.Call(r.Context(), s.principal(r), o.Prefix+"research.get", research.NoteIn{ID: o.ID, NoteID: id})
		if err != nil {
			return err
		}
		tags := []string{}
		for _, t := range out.(research.Item).Item.Tags {
			if from != "untagged" && strings.EqualFold(t, fromL) {
				continue
			}
			tags = append(tags, t)
		}
		if to != "untagged" && toL != "" {
			dup := false
			for _, t := range tags {
				dup = dup || strings.EqualFold(t, toL)
			}
			if !dup {
				tags = append(tags, toL)
			}
		}
		in.Tags = &tags
	} else {
		if !contains(resStatusOrder, to) {
			return ops.Invalid("", "Choose a column to move it to.")
		}
		in.Status = &to
	}
	_, err := s.Reg.Call(r.Context(), s.principal(r), o.Prefix+"research.update", in)
	return err
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// registerResearch adds the research pages, the scratchpad and the routes behind their buttons, for projects
// (/p/{slug}/...) and for Compendiums (/c/{slug}/...).
func (s *Server) registerResearch(m *http.ServeMux) {
	for _, kind := range []string{"p", "c"} {
		page, ui := "/p/{slug}", "/ui/projects/{id}"
		if kind == "c" {
			page, ui = "/c/{slug}", "/ui/compendiums/{id}"
		}
		m.HandleFunc("GET "+page+"/research", s.resListPage(kind))
		m.HandleFunc("GET "+page+"/research/board", s.resBoardPage(kind))
		m.HandleFunc("GET "+page+"/research/tags", s.resTagsPage(kind))
		m.HandleFunc("GET "+page+"/research/{note}", s.resNotePage(kind))
		m.HandleFunc("GET "+ui+"/research/tree", s.resTreeFragment(kind))
		m.HandleFunc("GET "+ui+"/research/board", s.resBoardFragment(kind))
		s.uiRoute("POST "+ui+"/research/{action}", s.uiResearch(kind))
		m.HandleFunc("GET "+page+"/scratch", s.scratchPage(kind))
		s.uiRoute("POST "+ui+"/scratch", s.uiScratch(kind))
	}
}

// resCompendiumByID is the one by-id Compendium reader (compendiumByID in compui.go).
func (s *Server) resCompendiumByID(w http.ResponseWriter, r *http.Request, id string) (compendiums.Compendium, bool) {
	return s.compendiumByID(w, r, id)
}
