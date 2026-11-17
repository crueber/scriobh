package server

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"

	"github.com/crueber/scriobh/web"
)

// A link that is meant to save a file says so with the download attribute. The server also answers such an address
// with Content-Disposition: attachment, which is enough for most browsers; the desktop shell's webview shows a PDF, a
// picture or a text file in the window instead when the link does not say it is a download.

var (
	anchorRE   = regexp.MustCompile(`<a\b[^>]*>`)
	saveHrefRE = regexp.MustCompile(`href="[^"]*(\?download=1|[?&]download=1|/download)"`)
)

func TestEveryLinkThatSavesAFileSaysSo(t *testing.T) {
	found := 0
	err := fs.WalkDir(web.FS, "templates", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw, err := fs.ReadFile(web.FS, path)
		if err != nil {
			return err
		}
		for _, a := range anchorRE.FindAllString(string(raw), -1) {
			if !saveHrefRE.MatchString(a) {
				continue
			}
			found++
			if !regexp.MustCompile(`\sdownload(\s|>|=)`).MatchString(a) {
				t.Errorf("%s: a link to a download has no download attribute: %s", path, a)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found < 5 {
		t.Errorf("only %d download links were found in the templates; has the scan stopped seeing them?", found)
	}
	// A script that makes a link to a file it built (a blob) names the file, which is what makes it a download.
	blobs := 0
	err = fs.WalkDir(web.FS, "static/js", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(path, "editor.js") || strings.HasSuffix(path, ".min.js") || !strings.HasSuffix(path, ".js") {
			return err
		}
		raw, err := fs.ReadFile(web.FS, path)
		if err != nil {
			return err
		}
		lines := strings.Split(string(raw), "\n")
		for i, line := range lines {
			if !strings.Contains(line, "createObjectURL") || !regexp.MustCompile(`\bhref\b`).MatchString(line) {
				continue
			}
			blobs++
			// on the same line, or one of the next two (the link is built in a few statements)
			if near := strings.Join(lines[i:min(i+3, len(lines))], "\n"); !regexp.MustCompile(`\.download\s*=|download:`).MatchString(near) {
				t.Errorf("%s: a link to a built file is not marked as a download: %.160s", path, strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if blobs < 3 {
		t.Errorf("only %d links to built files were found in the scripts", blobs)
	}
}
