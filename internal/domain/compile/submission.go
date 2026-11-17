package compile

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/crueber/scriobh/internal/domain/export"
	"github.com/crueber/scriobh/internal/domain/projects"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
)

// SubmissionIn asks for a submission bundle.
type SubmissionIn struct {
	ProjectID string          `path:"id" json:"-" desc:"Project id."`
	Preset    string          `json:"preset,omitempty" desc:"A DOCX preset for the manuscript in the bundle (default shunn-us)."`
	Options   publish.Options `json:"options,omitempty" desc:"Options that override the preset."`
}

// ProjectRef names the project.
func (in SubmissionIn) ProjectRef() string { return in.ProjectID }

type bundleFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int    `json:"bytes"`
}

type bundleManifest struct {
	SchemaVersion int          `json:"schema_version"`
	Format        string       `json:"format"`
	Project       string       `json:"project"`
	Preset        string       `json:"preset"`
	InputHash     string       `json:"input_hash"`
	Words         int          `json:"words"`
	Files         []bundleFile `json:"files"`
	Documents     []struct {
		ID     string `json:"id"`
		SHA256 string `json:"sha256"`
	} `json:"documents"`
}

func exportSubmission(c *ops.Ctx, in SubmissionIn) (Out, error) {
	r, err := resolve(c, in.ProjectID, in.Preset, in.Options, true)
	if err != nil {
		return Out{}, err
	}
	if r.opts.Format != "docx" {
		return Out{}, ops.Invalid("preset", "A submission bundle holds a DOCX: choose a manuscript preset (shunn-us, uk-manuscript) or a styled DOCX.")
	}
	if r.b.empty {
		return Out{}, ops.Invalid("", "There is nothing to compile: every document is excluded or empty. Check which documents are marked do not compile.")
	}
	docx, ext, _, wwarns, err := r.write(c)
	if err != nil {
		return Out{}, ops.Internal(err)
	}
	warnings := append(append([]Warning{}, r.b.warnings...), wwarns...)
	slug := projects.Slugify(r.src.Project.Title)
	synopsis, hasSynopsis := r.synopsis(c)
	if !hasSynopsis {
		warnings = append(warnings, Warning{Code: "no_synopsis", Message: "The project has no description and no chapter has a synopsis, so synopsis.md is a placeholder. Write a synopsis in the project's details or in the outline."})
	}
	letter := r.coverLetter()
	// The bundle holds more than the manuscript: its hash covers the synopsis and
	// the letter as well, so a changed description or genre is a changed hash.
	hh := sha256.New()
	fmt.Fprintf(hh, "scriobh-submission/1\x00%s\x00%s\x00%s", r.hash, synopsis, letter)
	r.hash = hex.EncodeToString(hh.Sum(nil))
	files := []publish.File{
		{Name: slug + "-manuscript." + ext, Data: docx},
		{Name: "synopsis.md", Data: []byte(synopsis)},
		{Name: "cover-letter.md", Data: []byte(letter)},
	}
	m := bundleManifest{SchemaVersion: 1, Format: "scriobh-submission-bundle", Project: r.src.Project.Title, Preset: r.preset.ID, InputHash: r.hash, Words: r.b.words}
	for _, f := range files {
		sum := sha256.Sum256(f.Data)
		m.Files = append(m.Files, bundleFile{Path: f.Name, SHA256: hex.EncodeToString(sum[:]), Bytes: len(f.Data)})
	}
	for _, h := range r.b.held {
		m.Documents = append(m.Documents, struct {
			ID     string `json:"id"`
			SHA256 string `json:"sha256"`
		}{h.ID, h.SHA256})
	}
	mj, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return Out{}, ops.Internal(err)
	}
	files = append(files, publish.File{Name: "manifest.json", Data: append(mj, '\n')})
	body, err := publish.ZipStored(files, r.b.book.Modified)
	if err != nil {
		return Out{}, ops.Internal(err)
	}
	file := export.NewFile(slug+"-submission.zip", "application/zip", body, len(r.b.held))
	file.Skipped = r.b.skipped
	man := r.manifest(warnings, true)
	if err := export.Record(c, in.ProjectID, "submission/"+r.preset.ID, "docx", file, r.b.held, man, r.audit(warnings)); err != nil {
		return Out{}, err
	}
	out := r.out(file, "zip", warnings)
	return out, nil
}

// synopsis is the project's description, or an outline of the chapters'
// synopses; false when there is neither.
func (r *resolved) synopsis(c *ops.Ctx) (string, bool) {
	var desc string
	_ = c.DB().Read().QueryRowContext(c, `SELECT description FROM projects WHERE id = ?`, r.src.Project.ID).Scan(&desc)
	var b strings.Builder
	b.WriteString("# Synopsis: " + escapeHeading(r.src.Project.Title) + "\n\n")
	if d := strings.TrimSpace(desc); d != "" {
		b.WriteString(d + "\n")
		return b.String(), true
	}
	n, found := 0, false
	inCompile := map[string]bool{}
	for _, h := range r.b.held {
		inCompile[h.ID] = true
	}
	for _, d := range r.src.Docs {
		if d.Kind != "chapter" || !inCompile[d.ID] {
			continue
		}
		n++
		s := strings.TrimSpace(d.SynopsisLong)
		if s == "" {
			s = strings.TrimSpace(d.SynopsisShort)
		}
		if s == "" {
			continue
		}
		found = true
		title := strings.TrimSpace(d.Title)
		head := fmt.Sprintf("Chapter %d", n)
		if title != "" {
			head += ": " + escapeHeading(title)
		}
		b.WriteString("## " + head + "\n\n" + s + "\n\n")
	}
	if !found {
		return "# Synopsis: " + escapeHeading(r.src.Project.Title) + "\n\n(Write the synopsis here: one page, the whole story including the ending.)\n", false
	}
	return strings.TrimRight(b.String(), "\n") + "\n", true
}

func escapeHeading(s string) string {
	s = publish.CleanLine(s)
	return strings.NewReplacer("\\", "\\\\", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "<", "\\<", ">", "\\>", "`", "\\`").Replace(s)
}

// coverLetter is a template with the facts a letter needs filled in and the
// rest left in brackets for the writer.
func (r *resolved) coverLetter() string {
	p := r.src.Project
	byline := r.b.book.Byline
	name := firstNonEmpty(r.contact.Name, byline, "[Your name]")
	var b strings.Builder
	b.WriteString(name + "\n")
	for _, l := range r.b.book.Contact.Address {
		b.WriteString(l + "\n")
	}
	if e := strings.TrimSpace(r.contact.Email); e != "" {
		b.WriteString(e + "\n")
	}
	if ph := strings.TrimSpace(r.contact.Phone); ph != "" {
		b.WriteString(ph + "\n")
	}
	kind := map[string]string{"novel": "novel", "novella": "novella", "short": "short story", "collection": "story collection", "nonfiction": "work of nonfiction", "essay": "essay", "report": "report", "blog": "piece"}[p.Kind]
	if kind == "" {
		kind = "manuscript"
	}
	genre := strings.TrimSpace(p.Genre)
	desc := kind
	if genre != "" {
		desc = genre + " " + kind
	}
	b.WriteString("\n[Date]\n\nDear [Editor's name],\n\n")
	fmt.Fprintf(&b, "I am writing to submit %s, a %s of about %s words, for your consideration. [One sentence that says what the story is and why it is for this editor or agent.]\n\n",
		italicTitle(p.Title), desc, publish.Thousands(publish.RoundedWords(r.b.words)))
	b.WriteString("[One short paragraph about the book: the character, the problem, what is at stake.]\n\n")
	b.WriteString("[Two lines about you: what you have published, or what qualifies you to write this.]\n\n")
	b.WriteString("The manuscript and a synopsis are attached. Thank you for your time and consideration.\n\nSincerely,\n\n" + name + "\n")
	return b.String()
}

func italicTitle(t string) string { return "*" + escapeHeading(t) + "*" }
