package server

import (
	"context"
	"errors"

	"github.com/crueber/scriobh/internal/domain/compile"
	"github.com/crueber/scriobh/internal/mail"
	"github.com/crueber/scriobh/internal/ops"
)

// mailCompiler is the edition builder Send to Kindle uses: the registry's own export.compile, run as the person
// who asked, so the authorization, the limits, the gate on heavy work and the record of the export are those of
// an export made by hand. The preset and the options come from the mail package (the EPUB preset with no notes,
// no ghosted text and no author contact block), never from the caller.
func (s *Server) mailCompiler() mail.Compiler {
	return func(ctx context.Context, p ops.Principal, req mail.EditionRequest) (*mail.Edition, error) {
		out, err := s.Reg.Call(ctx, p, "export.compile", compile.CompileIn{ProjectID: req.ProjectID, Preset: req.Preset, Options: req.Options})
		if err != nil {
			return nil, err
		}
		o, ok := out.(compile.Out)
		if !ok {
			return nil, errors.New("export.compile answered with an unexpected type")
		}
		return &mail.Edition{Filename: o.Filename, ContentType: o.ContentType, Body: o.Body, SHA256: o.SHA256, InputHash: o.InputHash, Documents: o.Documents}, nil
	}
}
