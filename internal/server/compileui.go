package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
)

// The Compile page, /p/{slug}/compile, for the owner and the editors (anyone else finds nothing). It does not know
// what a preset is made of: the presets and every option, with its type and its allowed values, come from the ops
// (compile.preset.list, and the schema of compile.preview's options), so a preset or an option an op adds later appears
// here without a change. The page's script builds the options form from that, asks compile.preview, draws what it
// answers (the title page, the running header, the outline, the warnings, what was left out, the pages as blocks, and
// for a print preset the pages as the layout placed them), and downloads through UI routes that return the file with its
// manifest summary in headers: the compile, the submission bundle and the print cover (export.cover; its measurements
// come from compile.cover_spec, which the script asks itself). The manuscript contact block is a form on the page: an
// editor sees and changes it, by design.

func (s *Server) registerCompileUI(m *http.ServeMux) {
	m.HandleFunc("GET /p/{slug}/compile", s.compilePage)
	s.uiRoute("POST /ui/projects/{id}/compile/{action}", s.uiCompile)
}

// optionDesc describes one option of a compile as the op schema gives it, with where the page shows it: its group (in
// reading order), the formats it means something for (none: every format), whether it can be turned on, and whether its
// text runs to several lines.
type optionDesc struct {
	Key     string   `json:"key"`
	Type    string   `json:"type"`
	Enum    []string `json:"enum,omitempty"`
	Group   string   `json:"group"`
	Formats []string `json:"formats,omitempty"`
	Off     bool     `json:"off,omitempty"`
	Long    bool     `json:"long,omitempty"`
	On      bool     `json:"on,omitempty"` // a switch that is on when a preset leaves it unset (publish.On)
}

// optionLayout places the known options under plain headings, in reading order; an option this table does not know goes
// under "more", after them, with its key as its label (a test fails when that happens).
var optionLayout = []struct{ group, keys string }{
	{"output", "format crop_marks"},
	{"page", "page_size font font_size line_spacing justify hyphenate indent_inches space_after_pt"},
	{"margins", "margin_inches margin_top margin_bottom margin_inside margin_outside gutter_inches"},
	{"title", "title_page title_layout byline contact word_count cover own_cover back_cover language"},
	{"front", "copyright_page imprint toc cover_page"},
	{"header", "header header_title header_upper running_heads page_numbers"},
	{"chapters", "chapter_heading chapter_label chapter_start chapter_drop chapter_ornament drop_cap part_pages the_end"},
	{"scenes", "scene_break ornament scene_titles"},
	{"include", "front_matter back_matter notes footnotes ghosts link_urls"},
}

// printOnly are the options only the print formats read; the form hides them for a Word file, an EPUB or Markdown.
// Nothing else is hidden by format: an option a writer ignores is harmless, an option hidden by mistake is not.
var printOnly = map[string][]string{"crop_marks": {"pdf"}}

func init() {
	printOnly["cover_page"] = []string{"pdf"}
	printOnly["back_cover"] = []string{"epub"}
	for _, k := range strings.Fields("margin_top margin_bottom margin_inside margin_outside gutter_inches footnotes running_heads drop_cap chapter_ornament toc hyphenate copyright_page imprint") {
		printOnly[k] = []string{"pdf", "html-paged"}
	}
}

// optionOff are options in the schema that the ops refuse to turn on (hyphenation is not built): shown, and switched off.
var optionOff = map[string]bool{"hyphenate": true}

// optionOn are the switches the writers read with publish.On: unset is on. A Word preset leaves the print switches
// unset, so when its kind of file is changed to a PDF the form must show them on, as the file will have them.
var optionOn = map[string]bool{"front_matter": true, "back_matter": true, "title_page": true, "part_pages": true, "cover": true, "own_cover": true, "contact": true, "running_heads": true, "toc": true, "copyright_page": true}

// compileOptionDescs reads the options out of compile.preview's input schema.
func (s *Server) compileOptionDescs() []optionDesc {
	place, group := map[string]int{}, map[string]string{}
	for gi, g := range optionLayout {
		for ki, k := range strings.Fields(g.keys) {
			place[k], group[k] = gi*100+ki, g.group
		}
	}
	var out []optionDesc
	for _, o := range s.schema.Ops {
		if o.Name != "compile.preview" {
			continue
		}
		props, _ := o.Input["properties"].(map[string]any)
		opts, _ := props["options"].(map[string]any)
		ps, _ := opts["properties"].(map[string]any)
		for k, v := range ps {
			if k == "clear" {
				continue
			}
			m, _ := v.(map[string]any)
			d := optionDesc{Key: k, Type: strings.TrimSpace(asString(m["type"])), Group: group[k], Formats: printOnly[k], Off: optionOff[k], Long: k == "imprint", On: optionOn[k]}
			if d.Group == "" {
				d.Group, place[k] = "more", 1<<20
			}
			if en, ok := m["enum"].([]any); ok {
				for _, e := range en {
					d.Enum = append(d.Enum, asString(e))
				}
			}
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if place[out[i].Key] != place[out[j].Key] {
			return place[out[i].Key] < place[out[j].Key]
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func asString(v any) string { s, _ := v.(string); return s }

type compileData struct {
	Project         projects.Project
	Slug, ProjectID string
	CSRF            string
	Dir, Lang       string
	Presets         string // JSON: compile.preset.list
	Options         string // JSON: the option descriptors
	Contact         compile.ContactOut
	Kindle          kindleCompile // the Send to Kindle section (mailui.go)
	Flash, Error    string
	Pandoc          bool // the bridge is on and usable: the Pandoc formats are offered
	Profiles        []coverProfileView
}

// coverProfileView is a printer's profile as the cover form's one line about it needs the figures: the table the PDF is drawn from.
type coverProfileView struct {
	Name           string
	Margin, BW, BH string
	From           int
}

func coverProfileViews() []coverProfileView {
	var out []coverProfileView
	for _, n := range []string{"kdp", "ingram", "generic"} {
		p := publish.CoverProfiles[n]
		out = append(out, coverProfileView{Name: n, Margin: num3(p.TextMarginIn), BW: num3(p.BarcodeWIn), BH: num3(p.BarcodeHIn), From: p.SpineTextFromPages})
	}
	return out
}

func (s *Server) compilePage(w http.ResponseWriter, r *http.Request) {
	p := s.principal(r)
	if !p.Authenticated() {
		s.fail(w, r, ops.Unauthenticated())
		return
	}
	proj, ok := s.goalsProject(w, r)
	if !ok {
		return
	}
	if proj.Role != ops.ProjectOwner && proj.Role != ops.ProjectEditor {
		s.fail(w, r, ops.NotFound("page")) // the page is for people who can compile; to anyone else it is not there
		return
	}
	ctx := r.Context()
	po, err := s.Reg.Call(ctx, p, "compile.preset.list", compile.PresetListIn{ProjectID: proj.ID})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	pj, _ := json.Marshal(po)
	oj, _ := json.Marshal(s.compileOptionDescs())
	d := compileData{Project: proj, Slug: proj.Slug, ProjectID: proj.ID, CSRF: authFrom(ctx).CSRF, Dir: textDir(proj.Language), Lang: proj.Language, Presets: string(pj), Options: string(oj), Pandoc: s.pandocUsable(ctx), Profiles: coverProfileViews()}
	d.Kindle = s.kindleCompileState(r)
	if co, err := s.Reg.Call(ctx, p, "compile.contact.get", compile.ContactIn{ProjectID: proj.ID}); err == nil {
		d.Contact = co.(compile.ContactOut)
	}
	if fl := s.flashTake(r); fl != nil {
		d.Flash, d.Error = fl["msg"], fl["err"]
	}
	w.Header().Set("Cache-Control", "no-store")
	s.render(w, r, http.StatusOK, "compile", s.page(r, "Compile · "+proj.Title, d))
}

func (s *Server) uiCompile(w http.ResponseWriter, r *http.Request) {
	proj, ok := s.goalsProjectByID(w, r, r.PathValue("id"))
	if !ok {
		return
	}
	p, ctx := s.principal(r), r.Context()
	switch r.PathValue("action") {
	case "contact":
		in := compile.ContactSetIn{ProjectID: proj.ID}
		f := func(k string) *string { v := strings.TrimSpace(r.PostFormValue(k)); return &v }
		in.Name, in.Surname, in.Address, in.Email, in.Phone = f("name"), f("surname"), f("address"), f("email"), f("phone")
		if _, err := s.Reg.Call(ctx, p, "compile.contact.set", in); err != nil {
			e := ops.AsError(err)
			if e.Code == ops.CodeInvalid {
				s.flashSet(r, map[string]string{"err": e.Message})
				http.Redirect(w, r, "/p/"+proj.Slug+"/compile#contact", http.StatusSeeOther)
				return
			}
			s.fail(w, r, err)
			return
		}
		s.flashSet(r, map[string]string{"msg": s.page(r, "", nil).T("cmp.contact_saved")})
		http.Redirect(w, r, "/p/"+proj.Slug+"/compile#contact", http.StatusSeeOther)
	case "cover-spec":
		var opts publish.Options
		if raw := r.PostFormValue("options"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &opts); err != nil {
				s.fail(w, r, ops.Invalid("options", "The options could not be read."))
				return
			}
		}
		in, bad := coverForm(r, proj.ID, r.PostFormValue("preset"), opts)
		s.compileCoverSpec(w, r, proj, in, strings.TrimSpace(r.PostFormValue("page_thickness_in")) != "", bad)
	case "download", "submission", "cover":
		var opts publish.Options
		if raw := r.PostFormValue("options"); raw != "" {
			if err := json.Unmarshal([]byte(raw), &opts); err != nil {
				s.fail(w, r, ops.Invalid("options", "The options could not be read."))
				return
			}
		}
		preset := r.PostFormValue("preset")
		var out any
		var err error
		switch r.PathValue("action") {
		case "submission":
			out, err = s.Reg.Call(ctx, p, "export.submission", compile.SubmissionIn{ProjectID: proj.ID, Preset: preset, Options: opts})
		case "cover":
			in, bad := coverForm(r, proj.ID, preset, opts)
			if bad != nil {
				s.fail(w, r, bad)
				return
			}
			out, err = s.Reg.Call(ctx, p, "export.cover", in)
		default:
			out, err = s.Reg.Call(ctx, p, "export.compile", compile.CompileIn{ProjectID: proj.ID, Preset: preset, Options: opts})
		}
		if err != nil {
			s.fail(w, r, s.coverPrintError(r, err))
			return
		}
		f := out.(compile.Out)
		h := w.Header()
		h.Set("X-Export-Id", f.ExportID)
		h.Set("X-Export-Documents", strconv.Itoa(f.Documents))
		h.Set("X-Export-Words", strconv.Itoa(f.Words))
		h.Set("X-Export-Skipped", strconv.Itoa(f.Skipped))
		h.Set("X-Export-Sha256", f.SHA256)
		h.Set("X-Export-Input-Hash", f.InputHash)
		h.Set("X-Export-Format", f.Format)
		h.Set("X-Export-Warnings", strconv.Itoa(len(f.Warnings)))
		if notes := s.coverFileNotes(r, f.Warnings); len(notes) > 0 && r.PathValue("action") == "cover" {
			h.Set("X-Export-Notes", url.PathEscape(strings.Join(notes, "\n")))
		}
		h.Set("Access-Control-Expose-Headers", "X-Export-Notes, X-Export-Id, X-Export-Documents, X-Export-Words, X-Export-Skipped, X-Export-Sha256, X-Export-Input-Hash, X-Export-Format, X-Export-Warnings, Content-Disposition")
		h.Set("Cache-Control", "no-store")
		writeDownload(w, f)
	default:
		s.fail(w, r, ops.NotFound("page"))
	}
}

// coverForm reads the cover's numbers from the form; the op bounds them. An empty field is "not given".
func coverForm(r *http.Request, projectID, preset string, opts publish.Options) (compile.CoverIn, error) {
	in := compile.CoverIn{ProjectID: projectID, Preset: preset, Options: opts, Guides: r.PostFormValue("guides") == "1", Barcode: r.PostFormValue("barcode") == "1", Profile: strings.TrimSpace(r.PostFormValue("profile")), Part: strings.TrimSpace(r.PostFormValue("part"))}
	num := func(k string) (float64, bool, error) {
		v := strings.TrimSpace(r.PostFormValue(k))
		if v == "" {
			return 0, false, nil
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil {
			return 0, false, &ops.Error{Code: ops.CodeInvalid, Field: k, Message: k + " is a number.", Data: map[string]any{"reason": "form_number"}}
		}
		return f, true, nil
	}
	var err error
	if in.PageThicknessIn, _, err = num("page_thickness_in"); err != nil {
		return in, err
	}
	pc, set, err := num("page_count")
	if err != nil {
		return in, err
	}
	if set {
		if pc != float64(int(pc)) {
			return in, &ops.Error{Code: ops.CodeInvalid, Field: "page_count", Message: "page_count is a whole number.", Data: map[string]any{"reason": "form_whole"}}
		}
		in.PageCount = int(pc)
	}
	b, set, err := num("bleed_in")
	if err != nil {
		return in, err
	}
	if set {
		in.BleedIn = &b
	}
	return in, nil
}
