package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/crueber/scriobh/internal/backup"
	"github.com/crueber/scriobh/internal/db"
)

const backupUsage = `usage: scriobh backup [--out FILE] [--no-files] [--no-mirror] [--no-conflicts]

  Writes a consistent archive of the data directory: the database, files/,
  mirror/ and conflicts/, with a manifest of hashes. It works while the server
  runs and when it does not. Without --out the archive goes to
  <data dir>/backups/<timestamp>.zip. The secret key is never included: back up
  secret.key (or SCRIOBH_SECRET_KEY) separately.
`

const restoreUsage = `usage: scriobh restore ARCHIVE [--force]
       scriobh restore --finish

  Restores an archive made by 'scriobh backup' into the data directory. Stop
  the server first: the command refuses while one is running. The directory
  must be empty unless --force is given; with --force a backup of what is there
  is taken first. secret.key is never touched.

  --finish makes the pauses of a restore that did not complete (it was
  interrupted, or a step failed and left restore.pending in the data
  directory): every remote, trigger, webhook, the Pandoc bridge, mail and
  semantic index is switched off. It exits non-zero and says what is NOT
  paused, with the statement that pauses it by hand, when it cannot.
`

// backupCmd runs `scriobh backup`.
func backupCmd(args []string, stdout, stderr io.Writer) int {
	restrictUmask()
	var o backup.Options
	for i := 0; i < len(args); i++ {
		switch a := args[i]; a {
		case "--out", "-out":
			if i+1 >= len(args) {
				fmt.Fprint(stderr, backupUsage)
				return 2
			}
			i++
			o.Out = args[i]
		case "--no-files", "-no-files":
			o.NoFiles = true
		case "--no-mirror", "-no-mirror":
			o.NoMirror = true
		case "--no-conflicts", "-no-conflicts":
			o.NoConflicts = true
		case "-h", "--help", "help":
			fmt.Fprint(stdout, backupUsage)
			return 0
		default:
			if len(a) > 6 && a[:6] == "--out=" {
				o.Out = a[6:]
				continue
			}
			fmt.Fprint(stderr, backupUsage)
			return 2
		}
	}
	cfg, ok := loadConfig(stderr)
	if !ok {
		return 1
	}
	o.Version = version
	if o.Out != "" {
		abs, err := filepath.Abs(o.Out)
		if err != nil {
			fmt.Fprintln(stderr, "scriobh:", err)
			return 1
		}
		o.Out = abs
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	// No data-directory lock and no migration here: a backup must work beside a
	// running server, and it must record the database exactly as it is.
	r, err := backup.Create(ctx, filepath.Join(cfg.DataDir, "scriobh.db"), cfg.DataDir, o)
	if err != nil {
		fmt.Fprintln(stderr, "scriobh: backup failed:", err)
		return 1
	}
	fmt.Fprintf(stdout, "backup written: %s\n", r.Path)
	fmt.Fprintf(stdout, "  %d projects, %d documents, %d files, %d bytes\n", r.Manifest.Counts.Projects, r.Manifest.Counts.Documents, len(r.Manifest.Entries), r.Bytes)
	fmt.Fprintf(stdout, "  sha256 %s\n", r.SHA256)
	if r.Skipped > 0 {
		fmt.Fprintf(stdout, "  %d entries that were not regular files were left out\n", r.Skipped)
	}
	fmt.Fprintln(stdout, "  the secret key is NOT in this archive: keep secret.key (or SCRIOBH_SECRET_KEY) somewhere safe, separately")
	return 0
}

// restoreCmd runs `scriobh restore`.
func restoreCmd(args []string, stdout, stderr io.Writer) int {
	restrictUmask()
	var o backup.RestoreOptions
	var archive string
	finish := false
	for _, a := range args {
		switch a {
		case "--finish", "-finish":
			finish = true
		case "--force", "-force":
			o.Force = true
		case "-h", "--help", "help":
			fmt.Fprint(stdout, restoreUsage)
			return 0
		default:
			if archive != "" || (len(a) > 0 && a[0] == '-') {
				fmt.Fprint(stderr, restoreUsage)
				return 2
			}
			archive = a
		}
	}
	if finish {
		if archive != "" || o.Force {
			fmt.Fprint(stderr, restoreUsage)
			return 2
		}
		return restoreFinish(stdout, stderr)
	}
	if archive == "" {
		fmt.Fprint(stderr, restoreUsage)
		return 2
	}
	cfg, ok := loadConfig(stderr)
	if !ok {
		return 1
	}
	o.Version = version
	o.SecretKey = cfg.SecretKey
	if _, err := os.Stat(archive); err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		return 1
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	r, err := backup.Restore(ctx, archive, cfg.DataDir, o)
	if r != nil && err != nil {
		fmt.Fprintln(stdout, "the data was restored, but:")
	}
	if err != nil {
		fmt.Fprintln(stderr, "scriobh: restore failed:", err)
		if r == nil {
			fmt.Fprintln(stderr, "nothing was changed")
		}
		return 1
	}
	fmt.Fprintf(stdout, "restored %d projects and %d documents from a backup made %s\n", r.Counts.Projects, r.Counts.Documents, r.Manifest.CreatedAt)
	if len(r.Migrated) > 0 {
		fmt.Fprintf(stdout, "  the database was brought up to date: migrations %v\n", r.Migrated)
	}
	if r.PreRestore != "" {
		fmt.Fprintf(stdout, "  what was here before is in %s\n", r.PreRestore)
	}
	if r.PreRestoreError != "" {
		fmt.Fprintf(stdout, "  the current data could not be archived first (%s), so it was moved aside whole instead\n", r.PreRestoreError)
	}
	if r.KeptAside != "" {
		fmt.Fprintf(stdout, "  what was here before is kept as it was in %s\n", r.KeptAside)
	}
	if r.SecretKeyPresent || len(cfg.SecretKey) > 0 {
		fmt.Fprintln(stdout, "  secret key: the key of this installation is used. If it is not the key the backup was made with, stored credentials will not decrypt and must be entered again; sessions are not affected")
	} else {
		fmt.Fprintln(stdout, "  secret key: none found. A new key is generated at the next start; stored credentials from the backup will not decrypt and must be entered again. To keep them, put the original secret.key back before starting")
	}
	for _, n := range r.Notes {
		fmt.Fprintln(stdout, "  "+n)
	}
	fmt.Fprintln(stdout, "start the server; the Markdown mirror is rewritten as needed")
	return 0
}

// restoreFinish runs the pauses of a restore that left its marker, on the data directory's own database.
func restoreFinish(stdout, stderr io.Writer) int {
	cfg, ok := loadConfig(stderr)
	if !ok {
		return 1
	}
	release, err := db.Lock(cfg.DataDir)
	if err != nil {
		fmt.Fprintf(stderr, "scriobh: %v: stop the server that is using this data directory first\n", err)
		return 1
	}
	defer release()
	d, err := db.Open(filepath.Join(cfg.DataDir, backup.DatabaseName))
	if err != nil {
		fmt.Fprintln(stderr, "scriobh:", err)
		return 1
	}
	defer d.Close()
	pending, notes, err := backup.FinishPending(context.Background(), d, cfg.DataDir)
	if !pending {
		fmt.Fprintln(stdout, "no restore is waiting to be finished: there is no "+backup.MarkerName+" in the data directory")
		return 0
	}
	for _, n := range notes {
		fmt.Fprintln(stdout, "  "+n)
	}
	if err != nil {
		fmt.Fprintln(stderr, "scriobh: the pauses could not all be made:", err)
		return 1
	}
	fmt.Fprintln(stdout, "every pause was made and the marker removed; start the server")
	return 0
}
