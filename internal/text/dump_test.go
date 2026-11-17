package text

import (
	"os"
	"testing"
)

// TestDump prints the parse tree of the file named by DUMP_FILE. A debugging
// aid; it does nothing otherwise.
func TestDump(t *testing.T) {
	path := os.Getenv("DUMP_FILE")
	if path == "" {
		t.Skip("set DUMP_FILE to dump a parse tree")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b := body(normalize(string(raw)))
	parseDoc(b).Dump(b, 0)
	walkBlocks(b, func(toks []tok) { t.Logf("%+v", toks) })
}
