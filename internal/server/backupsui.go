package server

import (
	"fmt"
	"net/http"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/ops"
)

// The Backups section of Settings, for administrators. It lists what is in the data
// directory's backups folder, takes a backup now, and links each one to the download
// route of the REST API (backup.download). Restoring is a command, not a button: it needs
// the server stopped, so the page says how.

type backupRow struct {
	ID, Kind, When, Size string
}

type backupsView struct {
	Rows    []backupRow
	Problem string // why the list is not shown, in plain words
}

func backupKind(k string) string {
	switch k {
	case "scheduled":
		return "Scheduled"
	case "pre-restore":
		return "Before a restore"
	}
	return "Manual"
}

func humanBytes(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/float64(1<<10))
	}
	return fmt.Sprintf("%d bytes", n)
}

// backupsFor loads the section for an administrator; anyone else gets nil and no section.
func (s *Server) backupsFor(r *http.Request) *backupsView {
	if s.principal(r).Role != ops.RoleAdmin {
		return nil
	}
	v := &backupsView{}
	out, err := s.Reg.Call(r.Context(), s.principal(r), "backup.list", backup.ListIn{})
	if err != nil {
		v.Problem = ops.AsError(err).Message
		return v
	}
	loc := s.userZone(r)
	for _, it := range out.(backup.ListOut).Items {
		v.Rows = append(v.Rows, backupRow{ID: it.ID, Kind: backupKind(it.Kind), When: whenIn(it.CreatedAt, loc), Size: humanBytes(it.Bytes)})
	}
	return v
}

// uiBackupCreate is POST /ui/settings/backups: take a backup now.
func (s *Server) uiBackupCreate(w http.ResponseWriter, r *http.Request) {
	if _, err := s.Reg.Call(r.Context(), s.principal(r), "backup.create", backup.CreateIn{}); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/settings?saved=backup#backups", http.StatusSeeOther)
}
