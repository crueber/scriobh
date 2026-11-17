// Package skillgen generates the agent skill and the CLI snapshot from the op
// registry. `make skill` writes the files; a test regenerates them in memory
// and fails when the committed copies differ, so an op change without a skill
// update breaks CI.
//
// The skill text itself is rendered by internal/cli from a schema, the same
// code the CLI runs against a live server: the committed SKILL.md is exactly
// what `scriobh-cli --skill` prints for a server whose schema is the
// committed snapshot.
package skillgen

import (
	"encoding/json"

	"github.com/crueber/scriobh/internal/cli"
	"github.com/crueber/scriobh/internal/mcp"
	"github.com/crueber/scriobh/internal/ops"
)

// The generated files, relative to the repository root. All of them are made
// from one document, the registry's schema: the CLI reads the snapshot, the
// skill is rendered from it, and the MCP tool list is built from it by the same
// function the server answers tools/list with.
const (
	SnapshotPath     = "internal/cli/snapshot.json"
	SkillPath        = "skills/scriobh/SKILL.md"
	MCPToolsPath     = "internal/mcp/tools.snapshot.json"
	MCPReferencePath = "skills/scriobh/mcp.md"
)

// Generate returns every generated file, keyed by repo-relative path.
func Generate(reg *ops.Registry) (map[string][]byte, error) {
	snap, err := json.MarshalIndent(reg.Schema(), "", "  ")
	if err != nil {
		return nil, err
	}
	// Render from what the CLI will read back, not from the registry types, so
	// the two cannot drift.
	schema, err := cli.ParseSchema(snap)
	if err != nil {
		return nil, err
	}
	skill := cli.RenderSkill(schema, cli.SkillOptions{})
	cat, err := mcp.BuildCatalog(reg.Schema())
	if err != nil {
		return nil, err
	}
	tools, err := cat.Snapshot()
	if err != nil {
		return nil, err
	}
	return map[string][]byte{
		SnapshotPath: append(snap, '\n'), SkillPath: []byte(skill),
		MCPToolsPath: tools, MCPReferencePath: []byte(cat.Reference()),
	}, nil
}
