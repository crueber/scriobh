package gitmirror

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Disk accounting for what Git sync keeps. Three things take space: the local
// repositories (data/git), repository imports and the clones kept beside the
// projects they made (data/imports), and the bundles written before a force
// push (data/backups/git). Each person has a quota for the clones they have
// started or kept, the instance has a quota for all three, and the rest is kept
// small by pruning: only the newest few bundles of a remote, and kept clones
// that expire. A quota of zero is no quota.

// Defaults for Options.
const (
	DefaultUserQuotaMB   = 2048
	DefaultTotalQuotaMB  = 20480
	DefaultBundlesKeep   = 5
	DefaultKeepImportDay = 30
)

func (s *Service) gitDirs() []string {
	return []string{
		filepath.Join(s.o.DataDir, "git"),
		filepath.Join(s.o.DataDir, importsFolder),
		filepath.Join(s.o.DataDir, "backups", "git"),
	}
}

// totalUsed is the space Git sync holds on disk.
func (s *Service) totalUsed() int64 {
	var n int64
	for _, d := range s.gitDirs() {
		n += dirSize(d)
	}
	return n
}

// checkTotalQuota refuses when adding extra bytes would pass the instance quota.
func (s *Service) checkTotalQuota(extra int64) *Error {
	if s.o.TotalQuotaBytes <= 0 {
		return nil
	}
	if s.totalUsed()+extra > s.o.TotalQuotaBytes {
		return gerr(CodeTooLarge, "The space this server keeps for Git copies is full (%d MB, SCRIOBH_GIT_TOTAL_QUOTA_MB). Nothing was started. Discard imports you no longer need, or ask the administrator.", s.o.TotalQuotaBytes>>20)
	}
	return nil
}

// userImportUsage is the disk a person's imports hold: clones waiting to be
// applied and clones kept beside the projects they own. A clone that is still
// being made is not on this list: it is counted as the full cap by the caller.
func (s *Service) userImportUsage(ctx context.Context, userID string) int64 {
	rows, err := s.o.DB.Read().QueryContext(ctx, `SELECT clone_path FROM import_jobs WHERE owner_id = ? AND status IN ('preview','done') AND clone_path <> ''
		UNION SELECT ps.kept_copy_path FROM project_sources ps JOIN projects p ON p.id = ps.project_id WHERE p.owner_id = ? AND ps.kept_copy_path <> ''`, userID, userID)
	if err != nil {
		return 0
	}
	defer rows.Close()
	seen := map[string]bool{}
	var n int64
	for rows.Next() {
		var rel string
		if rows.Scan(&rel) != nil || seen[rel] {
			continue
		}
		seen[rel] = true
		if clean := filepath.Clean(filepath.FromSlash(rel)); strings.HasPrefix(clean, importsFolder+string(filepath.Separator)) && !strings.Contains(clean, "..") {
			n += dirSize(filepath.Join(s.o.DataDir, clean))
		}
	}
	return n
}

// pruneBundles keeps the newest BundlesKeep bundles of one remote. The time is
// in the file name, so the order needs no stat.
func (s *Service) pruneBundles(projectID, remoteID string) {
	keep := s.o.BundlesKeep
	if keep <= 0 {
		return
	}
	dir := s.bundleDir(projectID)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), remoteID+"-") && strings.HasSuffix(e.Name(), ".bundle") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for len(names) > keep {
		_ = os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
}

// expireKeptImports removes the clones kept beside imported projects once they
// are older than KeepImportDays, and says so in the project's source record. It
// goes by the source records (which outlive the import job a person may have
// discarded), not by the jobs.
func (s *Service) expireKeptImports(ctx context.Context) {
	if s.o.KeepImportDays <= 0 {
		return
	}
	cutoff := stamp(s.o.Clock.Now().Add(-time.Duration(s.o.KeepImportDays) * 24 * time.Hour))
	rows, err := s.o.DB.Read().QueryContext(ctx, `SELECT DISTINCT kept_copy_path FROM project_sources WHERE kind = 'git' AND kept_copy_path <> '' AND imported_at < ?`, cutoff)
	if err != nil {
		return
	}
	var rels []string
	for rows.Next() {
		var rel string
		if rows.Scan(&rel) == nil {
			rels = append(rels, rel)
		}
	}
	rows.Close()
	for _, rel := range rels {
		clean := filepath.Clean(filepath.FromSlash(rel))
		if strings.HasPrefix(clean, importsFolder+string(filepath.Separator)) && !strings.Contains(clean, "..") {
			_ = os.RemoveAll(filepath.Join(s.o.DataDir, clean))
		}
		_, _ = s.o.DB.Writer().ExecContext(ctx, `UPDATE project_sources SET kept_copy_path = '' WHERE kept_copy_path = ?`, rel)
		_, _ = s.o.DB.Writer().ExecContext(ctx, `UPDATE import_jobs SET clone_path = '' WHERE clone_path = ?`, rel)
	}
}
