//go:build !unix

package pandoc

const (
	helperName = "scriobh-pandoc-helper"
	helperFlag = "--scriobh-pandoc-helper"
)

// RunHelperIfRequested does nothing off Unix.
func RunHelperIfRequested() bool { return false }
