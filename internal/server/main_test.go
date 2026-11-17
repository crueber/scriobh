package server

import (
	"os"
	"testing"

	"github.com/crueber/scriobh/internal/imports"
	"github.com/crueber/scriobh/internal/pandoc"
)

// This test binary is its own helper for two features that start the server's own executable again: the PDF reader
// (the importer runs it with the argument pdf-extract) and the resource-limit helper for Pandoc
// (--scriobh-pandoc-helper, see internal/pandoc). Both must be checked before anything else runs.
func TestMain(m *testing.M) {
	if pandoc.RunHelperIfRequested() {
		return
	}
	imports.MaybeRunPDFChild()
	os.Exit(m.Run())
}
