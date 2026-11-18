//! The downloaded update, kept between runs so that a quit does not lose a package that took a download to get.
//!
//! The folder is the shell's own, in its cache folder, and not the downloads folder, which is emptied at start and at exit
//! (`app.rs`, `clear_downloads`). What is kept is the package's bytes and the answer of the update server that announced it
//! (the plugin can rebuild the update from that without a request, `Updater::restore_update`). Neither is trusted when it is
//! read back: the plugin verifies the bytes against the key built into the app before anything is installed, and the shell
//! does it again at the start and again when the person chooses Restart to Update. Files are private (mode 0600 in a 0700
//! folder) and the folder is replaced as a whole, so a newer version supersedes an older one.

use std::fs;
use std::io::{self, Write};
use std::path::{Path, PathBuf};

const PACKAGE: &str = "update.bin";
const ANSWER: &str = "update.json";

pub struct Cached {
    pub version: String,
    /// The update server's answer, as the plugin read it.
    pub answer: serde_json::Value,
    pub bytes: Vec<u8>,
}

pub struct Cache {
    dir: PathBuf,
}

fn private_file(path: &Path, data: &[u8]) -> io::Result<()> {
    let mut o = fs::OpenOptions::new();
    o.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        o.mode(0o600);
    }
    let mut f = o.open(path)?;
    f.write_all(data)?;
    f.sync_all()
}

impl Cache {
    pub fn new(dir: PathBuf) -> Self {
        Cache { dir }
    }

    fn sibling(&self, suffix: &str) -> PathBuf {
        let mut name = self.dir.file_name().map(|n| n.to_os_string()).unwrap_or_default();
        name.push(".");
        name.push(suffix);
        self.dir.with_file_name(name)
    }

    /// Keeps `bytes` as the package of `version`, replacing whatever was kept. The new package is written whole in a folder
    /// beside the old one and only then swapped in, so a download that cannot be kept (the disk is full, a write fails) leaves
    /// the package that was already waiting where it was. The answer file is written last, so a package is never read
    /// without the answer that goes with it, nor half of either.
    pub fn store(&self, version: &str, answer: &serde_json::Value, bytes: &[u8]) -> io::Result<()> {
        let (new, old) = (self.sibling("new"), self.sibling("old"));
        // What an interrupted store left. If it stopped between its two renames the package that was waiting is in `old` and
        // `dir` is missing: put it back first (it is the only copy), and only then clear what is left over.
        if !self.dir.exists() && old.exists() {
            let _ = fs::rename(&old, &self.dir);
        }
        let _ = fs::remove_dir_all(&new);
        let _ = fs::remove_dir_all(&old);
        let mut b = fs::DirBuilder::new();
        b.recursive(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::DirBuilderExt;
            b.mode(0o700);
        }
        let written = b.create(&new).and_then(|_| {
            private_file(&new.join(PACKAGE), bytes)?;
            let record = serde_json::json!({"version": version, "length": bytes.len(), "answer": answer});
            private_file(&new.join(ANSWER), record.to_string().as_bytes())
        });
        if let Err(e) = written {
            let _ = fs::remove_dir_all(&new);
            return Err(e);
        }
        let had_old = self.dir.exists();
        if had_old {
            if let Err(e) = fs::rename(&self.dir, &old) {
                let _ = fs::remove_dir_all(&new);
                return Err(e);
            }
        }
        if let Err(e) = fs::rename(&new, &self.dir) {
            if had_old {
                let _ = fs::rename(&old, &self.dir);
            }
            let _ = fs::remove_dir_all(&new);
            return Err(e);
        }
        let _ = fs::remove_dir_all(&old);
        Ok(())
    }

    /// What was kept, if it is whole: both files, and a package of the length the record says. If a store was interrupted between
    /// its two renames the folder is missing and the package that was waiting is beside it, in `old`: that one is read.
    pub fn load(&self) -> Option<Cached> {
        let dir = if self.dir.exists() { self.dir.clone() } else { self.sibling("old") };
        let record: serde_json::Value = serde_json::from_slice(&fs::read(dir.join(ANSWER)).ok()?).ok()?;
        let bytes = fs::read(dir.join(PACKAGE)).ok()?;
        if record.get("length")?.as_u64()? != bytes.len() as u64 {
            return None;
        }
        Some(Cached {
            version: record.get("version")?.as_str()?.to_string(),
            answer: record.get("answer")?.clone(),
            bytes,
        })
    }

    pub fn clear(&self) {
        let _ = fs::remove_dir_all(&self.dir);
        let _ = fs::remove_dir_all(self.sibling("new"));
        let _ = fs::remove_dir_all(self.sibling("old"));
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    fn scratch() -> PathBuf {
        let mut b = [0u8; 8];
        getrandom::fill(&mut b).unwrap();
        std::env::temp_dir().join(format!("scriobh-cache-test-{}", crate::launch::hex(&b).as_str())).join("update")
    }

    fn done(dir: &Path) {
        let _ = fs::remove_dir_all(dir.parent().unwrap());
    }

    #[test]
    fn a_package_and_its_answer_are_kept_and_read_back_whole() {
        let dir = scratch();
        let c = Cache::new(dir.clone());
        assert!(c.load().is_none());
        let answer = json!({"version": "2.3.0", "platforms": {"darwin-aarch64-app": {"url": "https://github.com/o/r/releases/download/v2.3.0/x", "signature": "c2ln"}}});
        c.store("2.3.0", &answer, b"package bytes").unwrap();
        let got = c.load().unwrap();
        assert_eq!(
            (got.version.as_str(), got.bytes.as_slice(), &got.answer),
            ("2.3.0", &b"package bytes"[..], &answer)
        );
        // A newer version replaces it; nothing of the old one is left.
        c.store("2.3.1", &json!({}), b"newer").unwrap();
        let got = c.load().unwrap();
        assert_eq!((got.version.as_str(), got.bytes.as_slice()), ("2.3.1", &b"newer"[..]));
        assert_eq!(fs::read_dir(&dir).unwrap().count(), 2, "only the package and its answer remain");
        assert!(!c.sibling("new").exists() && !c.sibling("old").exists(), "no folder of the swap is left");
        c.clear();
        assert!(c.load().is_none() && !dir.exists());
        done(&dir);
    }

    #[test]
    fn what_is_cut_short_or_missing_is_not_read() {
        let dir = scratch();
        let c = Cache::new(dir.clone());
        c.store("2.3.0", &json!({}), b"0123456789").unwrap();
        fs::write(dir.join(PACKAGE), b"01234").unwrap();
        assert!(c.load().is_none(), "a package shorter than its record");
        c.store("2.3.0", &json!({}), b"0123456789").unwrap();
        fs::remove_file(dir.join(PACKAGE)).unwrap();
        assert!(c.load().is_none(), "no package");
        c.store("2.3.0", &json!({}), b"0123456789").unwrap();
        fs::write(dir.join(ANSWER), b"{not json").unwrap();
        assert!(c.load().is_none(), "a record that is not JSON");
        fs::write(dir.join(ANSWER), b"{\"version\":\"1\"}").unwrap();
        assert!(c.load().is_none(), "a record with no length");
        done(&dir);
    }

    #[test]
    fn a_package_that_cannot_be_kept_leaves_the_one_that_was_waiting() {
        let dir = scratch();
        let c = Cache::new(dir.clone());
        c.store("2.3.0", &json!({"v": 1}), b"the package that was waiting").unwrap();
        // The new package's folder cannot be made: a file is in its place, and the store cannot remove a file as a folder.
        fs::write(c.sibling("new"), b"in the way").unwrap();
        assert!(c.store("2.3.1", &json!({}), b"newer").is_err());
        let got = c.load().expect("the waiting package is still there");
        assert_eq!((got.version.as_str(), got.bytes.as_slice()), ("2.3.0", &b"the package that was waiting"[..]));
        done(&dir);
    }

    #[test]
    fn a_store_interrupted_between_its_renames_loses_nothing() {
        let dir = scratch();
        let c = Cache::new(dir.clone());
        c.store("2.3.0", &json!({"v": 1}), b"the package that was waiting").unwrap();
        // the crash: the waiting folder was moved aside and the new one never moved in
        fs::rename(&dir, c.sibling("old")).unwrap();
        fs::create_dir_all(c.sibling("new")).unwrap();
        fs::write(c.sibling("new").join(PACKAGE), b"half").unwrap();
        let got = c.load().expect("the package that was waiting is found beside the missing folder");
        assert_eq!((got.version.as_str(), got.bytes.as_slice()), ("2.3.0", &b"the package that was waiting"[..]));
        // the next store puts it back before it clears anything, then replaces it
        c.store("2.3.1", &json!({}), b"newer").unwrap();
        let got = c.load().unwrap();
        assert_eq!((got.version.as_str(), got.bytes.as_slice()), ("2.3.1", &b"newer"[..]));
        assert!(!c.sibling("new").exists() && !c.sibling("old").exists());
        done(&dir);
    }

    #[cfg(unix)]
    #[test]
    fn a_failed_store_changes_nothing() {
        use std::os::unix::fs::PermissionsExt;
        let dir = scratch();
        let c = Cache::new(dir.clone());
        c.store("2.3.0", &json!({}), b"waiting").unwrap();
        let parent = dir.parent().unwrap().to_path_buf();
        fs::set_permissions(&parent, fs::Permissions::from_mode(0o500)).unwrap();
        let outcome = c.store("2.3.1", &json!({}), b"newer");
        fs::set_permissions(&parent, fs::Permissions::from_mode(0o700)).unwrap();
        // (running as root ignores the mode; then the store simply worked)
        if outcome.is_err() {
            let got = c.load().expect("the old package is still there");
            assert_eq!((got.version.as_str(), got.bytes.as_slice()), ("2.3.0", &b"waiting"[..]));
        }
        done(&dir);
    }

    #[cfg(unix)]
    #[test]
    fn the_files_and_the_folder_are_private() {
        use std::os::unix::fs::PermissionsExt;
        let dir = scratch();
        let c = Cache::new(dir.clone());
        c.store("2.3.0", &json!({}), b"x").unwrap();
        let mode = |p: &Path| fs::metadata(p).unwrap().permissions().mode() & 0o777;
        assert_eq!(mode(&dir), 0o700);
        assert_eq!(mode(&dir.join(PACKAGE)), 0o600);
        assert_eq!(mode(&dir.join(ANSWER)), 0o600);
        done(&dir);
    }
}
