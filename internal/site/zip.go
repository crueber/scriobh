package site

import (
	"archive/zip"
	"bytes"
	"time"
)

// Zip writes files as a zip: in the order given (Build's are sorted), deflated,
// with one fixed time, no extra fields and no owner. The same files are the
// same bytes within a build of the program.
func Zip(files []File, modified time.Time) ([]byte, error) {
	if modified.IsZero() || modified.Year() < 1980 {
		modified = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	if modified.Year() > 2107 { // a zip's date field ends at 2107
		modified = time.Date(2107, 12, 31, 0, 0, 0, 0, time.UTC)
	}
	modified = modified.UTC()
	date := uint16(modified.Year()-1980)<<9 | uint16(modified.Month())<<5 | uint16(modified.Day())
	tm := uint16(modified.Hour())<<11 | uint16(modified.Minute())<<5 | uint16(modified.Second()/2)
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, f := range files {
		method := zip.Deflate
		if len(f.Data) < 64 {
			method = zip.Store
		}
		h := &zip.FileHeader{Name: f.Path, Method: method, ModifiedDate: date, ModifiedTime: tm}
		h.SetMode(0o644)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return nil, err
		}
		if _, err := w.Write(f.Data); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
