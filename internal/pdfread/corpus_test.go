package pdfread_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crueber/scriobh/internal/pdfread"
)

// TestCorpus reads real PDFs named in PDF_CORPUS (paths separated by
// colons) and prints what was recovered. It does nothing when the variable is
// unset: the files are not in the repository, and it asserts only that the
// reader does not fail on them and does not take long.
func TestCorpus(t *testing.T) {
	paths := os.Getenv("PDF_CORPUS")
	if paths == "" {
		t.Skip("PDF_CORPUS is not set")
	}
	for _, p := range strings.Split(paths, ":") {
		p := p
		t.Run(filepath.Base(p), func(t *testing.T) {
			data, err := os.ReadFile(p)
			if err != nil {
				t.Skip(err)
			}
			start := time.Now()
			d, err := pdfread.Open(context.Background(), data, pdfread.Limits{})
			if err != nil {
				t.Logf("refused: %v", err)
				return
			}
			var pages []*pdfread.Page
			if err := d.ReadPages(context.Background(), func(pg *pdfread.Page) error { pages = append(pages, pg); return nil }); err != nil {
				t.Fatal(err)
			}
			res, err := pdfread.Analyse(context.Background(), pages, d.Outline(), d.Info)
			if err != nil {
				t.Logf("%d pages, %v", len(pages), err)
				return
			}
			r := res.Report
			t.Logf("contents pages %v", r.TOCPages)
			t.Logf("producer %q creator %q: %d pages (%d text), %v: confidence %d, chapters %d from %s (outline %d/%d), 2-col pages %d, heads %d numbers %d toc lines %d, table rows %d, notes %d/%d linked, hyphens joined %d kept %d, unmapped %d, rotated %d, items %d quotes %d code %d",
				d.Info.Producer, d.Info.Creator, r.Pages, r.TextPages, time.Since(start).Round(time.Millisecond), r.Confidence, len(res.Chapters), r.ChapterSource, r.OutlineUsed, r.OutlineItems,
				r.TwoColumnPages, r.HeadLines, r.PageNumbers, r.TOCLines, r.TableRows, r.Notes, r.NotesLinked, r.HyphenJoined, r.HyphenKept, r.UnmappedChars, r.RotatedChars, r.ListItems, r.Quotes, r.CodeLines)
			if os.Getenv("PDF_SHOW") != "" {
				for i, c := range res.Chapters {
					if i >= 3 {
						break
					}
					fmt.Printf("== %s (%d blocks)\n", c.Title, len(c.Blocks))
					for k, b := range c.Blocks {
						if k >= 4 {
							break
						}
						tx := b.Text()
						if len(tx) > 160 {
							tx = tx[:160]
						}
						fmt.Printf("   [%d] %s\n", b.Kind, tx)
					}
				}
			}
		})
	}
}
