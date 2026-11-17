package semantic_test

import (
	"strings"
	"testing"

	"github.com/crueber/scriobh/internal/domain/semantic"
)

// A footnote that only a private note or ghosted text refers to is as private as the note: its definition never
// reaches an embedding endpoint (the same cut as a share link and the exports).
func TestEmbedBodyDropsAFootnoteOnlyAHiddenSpanReferences(t *testing.T) {
	got := semantic.EmbedBody("Seen[^1] and%%[^2]%% and <scriobh-ghost>[^3]</scriobh-ghost>.\n\n[^1]: KEPTFN\n\n[^2]: PRIVFN\n\n[^3]: GHOSTFN\n")
	for _, gone := range []string{"PRIVFN", "GHOSTFN"} {
		if strings.Contains(got, gone) {
			t.Errorf("%s is in the text that would be embedded: %q", gone, got)
		}
	}
	if !strings.Contains(got, "KEPTFN") {
		t.Errorf("the visible footnote was lost: %q", got)
	}
}

// An index built under the older rules (version 1: footnotes of hidden text, HTML comments and front matter were not
// cut) is rebuilt: each document is queued and embedded again under the new version.
func TestAnIndexFromTheOlderRulesIsRebuilt(t *testing.T) {
	e := newEnv(t, semantic.Config{}, nil)
	f := newFakeEmbed(t, "ollama")
	pid := e.setup(f)
	book(e)
	e.turnOn(pid)
	e.settle()
	if n := e.count(`SELECT count(*) FROM embed_docs WHERE index_version = ?`, semantic.IndexVersion); n == 0 {
		t.Fatalf("nothing was indexed under version %d", semantic.IndexVersion)
	}
	if _, err := e.d.Writer().Exec(`UPDATE embed_docs SET index_version = 1`); err != nil {
		t.Fatal(err)
	}
	if st := e.status(e.owner); st.QueueDepth == 0 {
		t.Fatalf("documents held under the old version are not queued: %+v", st)
	}
	base := f.textCount()
	e.sem.Wake()
	e.settle()
	if f.textCount() == base {
		t.Fatal("nothing was embedded again")
	}
	if n := e.count(`SELECT count(*) FROM embed_docs WHERE index_version <> ?`, semantic.IndexVersion); n != 0 {
		t.Fatalf("%d documents are still held under the old version", n)
	}
}
