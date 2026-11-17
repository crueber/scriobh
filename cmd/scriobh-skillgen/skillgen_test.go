package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/crueber/scriobh/internal/registry"
	"github.com/crueber/scriobh/internal/skillgen"
)

// TestCommittedSkillIsCurrent regenerates the skill, the CLI snapshot and the op table of docs/API.md in
// memory and fails when the committed files differ from the registry. Any new
// or changed op requires `make skill`.
func TestCommittedSkillIsCurrent(t *testing.T) {
	files, err := skillgen.GenerateAll(registry.Build(nil, nil, ""), filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range files {
		have, err := os.ReadFile(filepath.Join("..", "..", rel))
		if err != nil {
			t.Fatalf("%s is missing: run `make skill`", rel)
		}
		if string(have) != string(want) {
			t.Errorf("%s is stale: run `make skill` and commit the result", rel)
		}
	}
}
