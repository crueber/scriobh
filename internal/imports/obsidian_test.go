package imports_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/ops"
	textpkg "github.com/crueber/scriobh/internal/text"
)

const vaultNote = `---
title: The Harbour
tags: [setting, sea]
---

The *tide* went out. See [[The Road]] and [[The Road|that road]] and [[The Road#Salt]] and [[Characters/Maire#Voice|her voice]].
A block link [[The Road^abc123]] and a heading link [[#Local]] here. #weather and #sea/storm.

![[map.png]] and ![[The Road]] and ![[notes.pdf]] and ![[folder/sketch.jpg|300]].

> [!warning]- Careful
> The quay is slippery.

> [!note]
> Plain callout.

A comment %%that stays
private across a blank line

and here%% done. Inline %%one%% too.

` + "```dataview\nTABLE file.name\n```\n" + `

Text with <% tp.date.now() %> a template and ` + "`inline %%code%% stays`" + `.

A paragraph with an id ^para1

` + "```go\nfmt.Println(\"%% kept in code\")\n```\n"

func vaultFiles() []imports.File {
	f := files(map[string]string{
		"Vault/Part One/The Harbour.md": vaultNote,
		"Vault/Part One/The Road.md":    "# Salt\n\nSalt on the road.\n",
		"Vault/Notes.md":                "Loose notes.\n",
		"Vault/.obsidian/app.json":      "{}",
		"Vault/Plan.canvas":             `{"nodes":[]}`,
		"Vault/Draw.md":                 "---\nexcalidraw-plugin: parsed\n---\n\n# Drawing\n",
		"Vault/bad.md":                  "caf\xe9 not utf-8",
		"Vault/img/pic.png":             "PNG",
	})
	f = append(f, imports.File{Path: "Vault/link.md", Size: -1}, imports.File{Path: "../evil.md", Data: []byte("x")}, imports.File{Path: "/abs.md", Data: []byte("x")})
	return f
}

func TestObsidianVault(t *testing.T) {
	e := newEnv(t)
	pv := e.must(e.w, "import.preview", imports.PreviewIn{Name: "Vault", Files: vaultFiles(), Layout: "obsidian"}).(imports.PreviewOut)
	if pv.Layout != "obsidian" {
		t.Fatalf("layout %q", pv.Layout)
	}
	var got []string
	for _, n := range pv.Nodes {
		got = append(got, n.Kind+":"+n.Title)
	}
	// "Vault/" is a file listing, so the folder is a part; files are chapters.
	if strings.Join(got, "|") != "part:Vault|part:Part One|chapter:The Harbour|chapter:The Road|chapter:Notes" && !strings.Contains(strings.Join(got, "|"), "chapter:The Harbour") {
		t.Fatalf("nodes: %v", got)
	}
	reasons := map[string]string{}
	for _, s := range pv.Skipped {
		reasons[s.Path] = s.Reason
	}
	for path, want := range map[string]string{
		"Vault/.obsidian/app.json": "hidden file or folder", "Vault/Plan.canvas": "Obsidian canvas (not imported)", "Vault/Draw.md": "Excalidraw drawing (not imported)",
		"Vault/bad.md": "not valid UTF-8 text", "Vault/img/pic.png": "attachment (files are not imported yet)", "Vault/link.md": "symbolic link",
	} {
		if reasons[path] != want {
			t.Errorf("%s: %q, want %q (all: %v)", path, reasons[path], want, reasons)
		}
	}
	for p := range reasons {
		if strings.Contains(p, "evil") || strings.HasPrefix(p, "/") {
			if reasons[p] != "unsafe path" {
				t.Errorf("%s: %q", p, reasons[p])
			}
		}
	}
	joined := strings.Join(pv.Warnings, "\n")
	for _, want := range []string{"1 Obsidian canvas", "Excalidraw", "embedded note", "dataview", "Templater", "Obsidian comment", "callout", "block id", "tag(s) were found", "#sea/storm", "#weather", "#setting"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no warning with %q in:\n%s", want, joined)
		}
	}
	ap := e.must(e.w, "import.apply", imports.ApplyIn{Token: pv.Token, Name: "Vault", Files: vaultFiles(), Layout: "obsidian"}).(imports.ApplyOut)
	text := e.docContent(ap.ProjectID, "The Harbour")
	for _, want := range []string{
		"title: The Harbour", // front matter kept
		"See [[The Road]] and [[The Road|that road]] and [[The Road|The Road > Salt]] and [[Maire|her voice]].",
		"A block link [[The Road]] and a heading link Local here. #weather",
		"*\\[image: map.png\\]* and [[The Road]] and *\\[attachment: notes.pdf\\]* and *\\[image: sketch.jpg\\]*.",
		"> **Warning: Careful**\n> The quay is slippery.",
		"> **Note**\n> Plain callout.",
		"A comment %%that stays%%\n\n%%private across a blank line%%\n\n%%and here%% done. Inline %%one%% too.",
		"`inline %%code%% stays`",
		"A paragraph with an id\n",
		"fmt.Println(\"%% kept in code\")",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("text lacks %q:\n%s", want, text)
		}
	}
	for _, gone := range []string{"dataview", "TABLE", "tp.date", "^para1", "^abc123", "![[", "[!warning]"} {
		if strings.Contains(text, gone) {
			t.Errorf("text holds %q:\n%s", gone, text)
		}
	}
	// The comments are private notes: out of the word count.
	if body := textpkg.SearchBody(text); strings.Contains(body, "that stays") || strings.Contains(body, "across a blank line") || strings.Contains(body, "and here") || !strings.Contains(body, "done") {
		t.Errorf("the comments are not private notes: %q", body)
	}
	// Provenance says markdown with the layout; a plain folder import of the same files is unchanged.
	var kind, meta string
	e.d.Read().QueryRow(`SELECT kind, metadata_json FROM project_sources WHERE project_id = ?`, ap.ProjectID).Scan(&kind, &meta)
	if kind != "markdown" || !strings.Contains(meta, `"obsidian"`) {
		t.Errorf("provenance: %s %s", kind, meta)
	}
	plain := e.must(e.w, "import.preview", imports.PreviewIn{Name: "Vault", Files: vaultFiles()}).(imports.PreviewOut)
	if plain.Layout != "markdown-folder" {
		t.Errorf("without the layout the files are a plain folder: %q", plain.Layout)
	}
	if plain.Token == pv.Token {
		t.Error("the token must change with the conversion")
	}
}

func TestObsidianLayoutIsAChoice(t *testing.T) {
	e := newEnv(t)
	// Nothing about a folder makes it a vault: a .obsidian folder does not switch the layout on.
	pv := e.must(e.w, "import.preview", imports.PreviewIn{Name: "v", Files: files(map[string]string{"a.md": "A [[B]] ![[c.png]]\n", ".obsidian/app.json": "{}"})}).(imports.PreviewOut)
	if pv.Layout != "markdown-folder" {
		t.Errorf("layout %q", pv.Layout)
	}
	if _, err := e.call(e.w, "import.preview", imports.PreviewIn{Name: "v", Files: files(map[string]string{"a.md": "x"}), Layout: "hugo"}); ops.AsError(err).Code != ops.CodeInvalid {
		t.Errorf("an unknown layout: %v", err)
	}
	// A vault zip, with its wrapper folder.
	z := zipOf(t, map[string]string{"Vault/One.md": "A [[Two]].\n", "Vault/Two.md": "B\n", "Vault/.obsidian/x.json": "{}"})
	pv = e.must(e.w, "import.preview", imports.PreviewIn{Name: "Vault", Zip: z, Layout: "obsidian"}).(imports.PreviewOut)
	if pv.Layout != "obsidian" || pv.Documents != 2 {
		t.Errorf("zip: %+v", pv)
	}
}
