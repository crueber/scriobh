package mcp

import "testing"

// ExposeGroupForTest classifies a made-up op group as exposed for the length of one test, so a
// hand-built schema can have tools. The registry's own groups are checked by TestEveryRegisteredGroupIsClassified.
func ExposeGroupForTest(t testing.TB, group string) {
	t.Helper()
	if _, ok := exposedGroups[group]; ok {
		return
	}
	exposedGroups[group] = "a test group"
	t.Cleanup(func() { delete(exposedGroups, group) })
}
