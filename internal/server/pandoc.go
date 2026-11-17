package server

import (
	"log/slog"
	"os"
	"path/filepath"

	"github.com/crueber/scriobh/internal/config"
	"github.com/crueber/scriobh/internal/pandoc"
)

// pandocOptions gives the Pandoc bridge a working folder under the data
// directory (a read-only system folder is no obstacle), and this program as the
// helper that puts resource limits on Pandoc just before it starts. The folder is
// swept of what a stopped process left.
func pandocOptions(cfg *config.Config, log *slog.Logger) pandoc.Options {
	root := filepath.Join(cfg.DataDir, "pandoc-tmp")
	_ = os.MkdirAll(root, 0o700)
	pandoc.SweepTemp(root)
	self, err := os.Executable()
	if err != nil {
		self = ""
	}
	return pandoc.Options{Helper: self, TempRoot: root, Log: log}
}
