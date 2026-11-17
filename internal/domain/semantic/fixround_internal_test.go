package semantic

import (
	"fmt"
	"strings"
	"testing"
)

// The flag applies down the whole tree, however deep, and a chain that loops is not embedded: every doubt closes.
func TestSkipReasonGoesToTheRootAndFailsClosed(t *testing.T) {
	docs := map[string]*docRow{}
	add := func(id, parent string, mut func(*docRow)) {
		d := &docRow{ID: id, Parent: parent, Kind: "scene", InCompile: true}
		if mut != nil {
			mut(d)
		}
		docs[id] = d
	}
	add("root", "", func(d *docRow) { d.AIExclude = true })
	prev := "root"
	for i := 0; i < 20_000; i++ {
		id := fmt.Sprintf("n%d", i)
		add(id, prev, nil)
		prev = id
	}
	if got := skipReason(docs, prev); got != skipNeverSend {
		t.Fatalf("a document 20,000 levels below a never-send document: %q", got)
	}
	docs["root"].AIExclude = false
	docs["root"].InCompile = false
	if got := skipReason(docs, prev); got != skipNoCompile {
		t.Fatalf("do-not-compile at the top of a deep chain: %q", got)
	}
	docs["root"].InCompile = true
	docs["root"].Trashed = true
	if got := skipReason(docs, prev); got != skipTrashed {
		t.Fatalf("trashed at the top: %q", got)
	}
	docs["root"].Trashed = false
	if got := skipReason(docs, prev); got != "" {
		t.Fatalf("a clean chain: %q", got)
	}
	// A loop is a doubt.
	docs["root"].Parent = prev
	if got := skipReason(docs, prev); got != skipTree {
		t.Fatalf("a chain that loops: %q", got)
	}
	_ = strings.ToLower
}
