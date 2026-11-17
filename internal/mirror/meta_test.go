package mirror_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/domain/docs"
)

func sp(s string) *string { return &s }
func ip(n int) *int       { return &n }
func bp(b bool) *bool     { return &b }

// outlineDoc is a scene with every piece of outline metadata set.
func (e *env) outlineDoc(pid string) docs.Doc {
	e.t.Helper()
	d := e.doc(pid, "Arrival", "scene", "She came ashore.\n")
	return e.call(e.owner, "doc.update", docs.UpdateIn{ID: d.ID, BaseRevision: d.Revision,
		Status: sp("revise"), SynopsisShort: sp("She comes ashore."), SynopsisLong: sp("Line one.\nLine two with <angle> & amp \"quotes\"."),
		Pov: sp("Máire"), Location: sp("The dock"), StoryDate: sp("1847-03-02"), StoryDateEnd: sp("1847-03-03"),
		TargetWords: ip(1500), IncludeInCompile: bp(false)}).(docs.Item).Item
}

// The manifest entry of a document with outline metadata, byte for byte: the
// field order, the names, and no HTML escaping. A change here is a change to
// the schema that the Git push and the importer read, and needs a version bump.
func TestManifestGolden(t *testing.T) {
	e := newEnv(t)
	pid := e.project("Golden")
	d := e.outlineDoc(pid)
	if _, err := e.m.Rebuild(e.ctx, pid); err != nil {
		t.Fatal(err)
	}
	raw := read(t, filepath.Join(e.root(pid), "project.json"))
	m := manifestOf(t, e.root(pid))
	var sha string
	var words int
	for _, x := range m.Documents {
		if x.ID == d.ID {
			sha = x.SHA256
			words = 3
		}
	}
	if sha == "" {
		t.Fatalf("document missing: %s", raw)
	}
	golden := fmt.Sprintf(`    {
      "id": %q,
      "path": "manuscript/02-arrival.md",
      "kind": "scene",
      "title": "Arrival",
      "status": "revise",
      "synopsis_short": "She comes ashore.",
      "synopsis_long": "Line one.\nLine two with <angle> & amp \"quotes\".",
      "pov": "Máire",
      "location": "The dock",
      "story_date": "1847-03-02",
      "story_date_end": "1847-03-03",
      "target_words": 1500,
      "include_in_compile": false,
      "word_count": %d,
      "sha256": %q
    }`, d.ID, words, sha)
	if !strings.Contains(raw, golden) {
		t.Errorf("the manifest entry is not the golden one.\nwant:\n%s\n\ngot:\n%s", golden, raw)
	}
	if !strings.Contains(raw, `"schema_version": 3,`) || !strings.HasSuffix(raw, "}\n") {
		t.Errorf("manifest head or tail: %s", raw)
	}
	// Plain documents carry the defaults, explicitly, so a reader never has to guess.
	if !strings.Contains(raw, `"include_in_compile": true`) || !strings.Contains(raw, `"synopsis_short": ""`) {
		t.Errorf("defaults are missing: %s", raw)
	}
}

// Each outline field is in the fingerprint: changing one while the mirror is
// not running makes the next start rewrite project.json, once, and nothing else.
func TestOutlineMetadataChangesTriggerOneRewriteAtStart(t *testing.T) {
	e := newEnv(t) // the mirror is not running while the data is made
	pid := e.project("Outline")
	d := e.doc(pid, "Scene", "scene", "text\n")
	boot := func() *countFS {
		fs := &countFS{}
		_, stop := e.bootMirror(fs)
		stop()
		return fs
	}
	boot() // the first write
	if fs := boot(); fs.writes() != 0 {
		t.Fatalf("a current mirror was rewritten: %v", fs.renames)
	}
	cur := d
	for name, in := range map[string]docs.UpdateIn{
		"synopsis_short":     {SynopsisShort: sp("a line")},
		"synopsis_long":      {SynopsisLong: sp("a longer one")},
		"pov":                {Pov: sp("Máire")},
		"location":           {Location: sp("Dock")},
		"story_date":         {StoryDate: sp("1847-03-02")},
		"story_date_end":     {StoryDateEnd: sp("1847-03-04")},
		"target_words":       {TargetWords: ip(900)},
		"include_in_compile": {IncludeInCompile: bp(false)},
	} {
		in.ID, in.BaseRevision = cur.ID, cur.Revision
		cur = e.call(e.owner, "doc.update", in).(docs.Item).Item
		fs := boot()
		if got := fs.wrote("project.json"); got != 1 || fs.writes() != 1 {
			t.Errorf("%s: want exactly project.json rewritten, got %v", name, fs.renames)
		}
		if fs := boot(); fs.writes() != 0 {
			t.Errorf("%s: a second start rewrote %v", name, fs.renames)
		}
	}
}

// A mirror written by the previous version (project.json version 1, without
// outline fields) is rebuilt once when the new version starts, and only its
// manifest changes.
func TestVersionOneMirrorIsRebuiltOnceOnUpgrade(t *testing.T) {
	e := newEnv(t)
	pid := e.project("Upgrade")
	e.outlineDoc(pid)
	run := func() *countFS {
		fs := &countFS{}
		_, stop := e.bootMirror(fs)
		stop()
		return fs
	}
	run()
	// Put back what version 1 wrote: the old manifest and the old record.
	matches, _ := filepath.Glob(filepath.Join(e.dir, "mirror", "upgrade--*"))
	if len(matches) != 1 {
		t.Fatalf("mirror folders: %v", matches)
	}
	root := matches[0]
	v1 := `{
  "schema_version": 1,
  "format": "scriobh-mirror",
  "project": {"id": "` + pid + `", "slug": "upgrade", "title": "Upgrade", "subtitle": "", "author": "", "kind": "novel", "language": "en", "genre": "", "status": "idea"},
  "state": "active",
  "includes_notes": true,
  "documents": []
}
`
	if err := os.WriteFile(filepath.Join(root, "project.json"), []byte(v1), 0o600); err != nil {
		t.Fatal(err)
	}
	statePath := filepath.Join(e.dir, "mirror", ".state", pid+".json")
	var st map[string]any
	if err := json.Unmarshal([]byte(read(t, statePath)), &st); err != nil {
		t.Fatal(err)
	}
	st["fingerprint"] = "written by version 1"
	st["files"].(map[string]any)["project.json"] = hashOf(v1)
	b, _ := json.Marshal(st)
	if err := os.WriteFile(statePath, b, 0o600); err != nil {
		t.Fatal(err)
	}
	before := mtimes(t, root)
	time.Sleep(20 * time.Millisecond)

	fs := run()
	if fs.wrote("project.json") != 1 || fs.writes() != 1 {
		t.Errorf("the upgrade should rewrite only project.json, wrote %v", fs.renames)
	}
	raw := read(t, filepath.Join(root, "project.json"))
	if !strings.Contains(raw, `"schema_version": 3`) || !strings.Contains(raw, `"synopsis_long": "Line one.\nLine two with <angle> & amp \"quotes\"."`) {
		t.Errorf("the manifest was not upgraded: %s", raw)
	}
	for f, mt := range mtimes(t, root) {
		if f != "project.json" && !mt.Equal(before[f]) {
			t.Errorf("%s was rewritten by the upgrade", f)
		}
	}
	if fs := run(); fs.writes() != 0 {
		t.Errorf("a second start after the upgrade rewrote %v", fs.renames)
	}
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}
