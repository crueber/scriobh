package publish

import (
	"archive/zip"
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/crueber/scriobh/internal/publish/check"
)

func unzipTo(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		rc, _ := f.Open()
		b, _ := io.ReadAll(rc)
		rc.Close()
		out[f.Name] = string(b)
	}
	return out
}

func TestShunnDOCXIsValid(t *testing.T) {
	data, err := WriteDOCX(sampleBook(), shunnOptions())
	if err != nil {
		t.Fatal(err)
	}
	if probs := check.DOCX(data); len(probs) > 0 {
		for _, p := range probs {
			t.Error(p)
		}
	}
	if dir := os.Getenv("SCRIOBH_KEEP"); dir != "" {
		os.WriteFile(filepath.Join(dir, "shunn.docx"), data, 0o644)
	}
}
