package server

import "net/http"

// registerImportMirrorBackup adds the import pages, the mirror rebuild and the backup button.
func (s *Server) registerImportMirrorBackup(m *http.ServeMux) {
	m.HandleFunc("GET /import", s.importPage)
	m.HandleFunc("GET /p/{slug}/import", s.importIntoPage)
	s.uiRoute("POST /ui/projects/{id}/mirror/rebuild", s.uiMirrorRebuild)
	s.uiRoute("POST /ui/settings/backups", s.uiBackupCreate)
}
