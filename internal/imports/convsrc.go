package imports

import (
	"context"
	"errors"

	"github.com/crueber/scriobh/internal/ops"
)

// convertIntake reads a Scrivener project, a WordPress export or a Ghost export
// into the files the folder importer reads, and the report of what was left
// out. A source that cannot be read is refused with the reason; nothing is
// guessed.
func (s source) convertIntake(layout string) (in *intake, err error) {
	// The command line does not recover from a panic; a source that breaks a
	// converter is refused there as it is over HTTP.
	defer func() {
		if r := recover(); r != nil {
			in, err = nil, ops.Invalid("files", "The file could not be read.")
		}
	}()
	var res *cresult
	o := s.conv
	o.ctx = s.ctx
	switch layout {
	case LayoutScrivener:
		var fs []File
		if fs, err = listingFor(s.files, s.zip, maxScrivEntries, maxScrivTotal, scrivWant); err == nil {
			res, err = scrivenerConvert(fs, s.name, o)
		}
	case LayoutWordPress:
		var fs []File
		if fs, err = listingFor(s.files, s.zip, maxExportZipEntries, maxExportTotal, exportWant); err == nil {
			res, err = wordpressConvert(fs, s.name, o)
		}
	case LayoutGhost:
		var fs []File
		if fs, err = listingFor(s.files, s.zip, maxExportZipEntries, maxExportTotal, exportWant); err == nil {
			res, err = ghostConvert(fs, s.name, o)
		}
	default:
		err = errors.New("unknown layout")
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return nil, err // the caller has gone: not a fault of the files
		}
		field := "files"
		if len(s.zip) > 0 {
			field = "zip"
		}
		return nil, ops.Invalid(field, err.Error())
	}
	in, err = intakeFiles(res.toFiles(), false)
	if err != nil {
		return nil, ops.Invalid("files", err.Error())
	}
	res.pics.report(res)
	in.apply(res.asPrepass(newExtras(res.research, res.pics)))
	return in, nil
}

const maxExportZipEntries = 10_000 // an export and its images folder (Ghost keeps several sizes of each picture)
