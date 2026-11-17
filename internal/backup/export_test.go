package backup

import "github.com/crueber/scriobh/internal/db"

// SetOpenDB replaces how Restore reopens the restored database, and returns the way back.
func SetOpenDB(f func(string) (*db.DB, error)) func() {
	old := openDB
	openDB = f
	return func() { openDB = old }
}
