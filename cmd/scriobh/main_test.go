package main

import (
	"os"
	"testing"

	"github.com/crueber/scriobh/internal/imports"
)

// This test binary is its own PDF reader: the importer starts it again with the argument pdf-extract.
func TestMain(m *testing.M) {
	imports.MaybeRunPDFChild()
	os.Exit(m.Run())
}
