package server

import (
	"context"
	"errors"

	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/gitmirror"
	"github.com/crueber/scriobh/internal/ops"
	"github.com/crueber/scriobh/internal/publish"
)

// gitCompiler is the edition builder a release uses: the registry's own export ops, run as the person who asked,
// so the authorization, the limits, the gate on heavy work and the record of the export are the ones of an export
// made by hand (the Markdown bundle is built by the Git service itself, from the reader's view of the project: export.markdown keeps private notes and alternatives byte for byte). The options are held to what a reader sees whatever a preset says: no notes, no ghosted text, no
// alternatives, and (by the preset list gitmirror allows) no contact block.
func (s *Server) gitCompiler() gitmirror.Compiler {
	return func(ctx context.Context, p ops.Principal, req gitmirror.EditionRequest) (*gitmirror.Edition, error) {
		if req.Format == "md_bundle" {
			return nil, errors.New("the Markdown bundle is built by the Git service from the reader's view, not by an export")
		}
		out, err := s.Reg.Call(ctx, p, "export.compile", compile.CompileIn{ProjectID: req.ProjectID, Preset: req.Preset, Options: publish.Options{Notes: "none", Ghosts: "omit"}})
		if err != nil {
			return nil, err
		}
		o, ok := out.(compile.Out)
		if !ok {
			return nil, errors.New("export.compile answered with an unexpected type")
		}
		return &gitmirror.Edition{Filename: o.Filename, ContentType: o.ContentType, Body: o.Body, SHA256: o.SHA256, InputHash: o.InputHash, Documents: o.Documents}, nil
	}
}
