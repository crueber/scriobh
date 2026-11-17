//! The data directory's place on this system, by the same rule the server uses when it is started in desktop
//! mode without one. The shell computes it for two things only: to show the folder in the file manager, and to
//! see whether a key file or a database is already there before it decides about the keychain. It never opens a
//! file in it.

use std::ffi::OsString;
use std::path::{Path, PathBuf};

/// What is already in the data directory, by the presence of two files.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum DataState {
    /// No key file and no database: a first run.
    Fresh,
    /// `secret.key` is there: an install that keeps its key in the file.
    HasKeyFile,
    /// A database without a key file.
    DataWithoutKeyFile,
    /// The folder could not be looked at (permission, I/O). Not a first run: nothing may be decided from it.
    Unreadable,
}

/// macOS `~/Library/Application Support/Scriobh`, Linux `${XDG_DATA_HOME:-~/.local/share}/scriobh`, Windows
/// `%AppData%\Scriobh`. `None` when the variables it needs are not set.
pub fn default_data_dir(env: impl Fn(&str) -> Option<OsString>) -> Option<PathBuf> {
    let abs = |name: &str| env(name).map(PathBuf::from).filter(|p| p.is_absolute());
    if cfg!(target_os = "macos") {
        abs("HOME").map(|h| h.join("Library").join("Application Support").join("Scriobh"))
    } else if cfg!(windows) {
        abs("APPDATA").map(|a| a.join("Scriobh"))
    } else {
        abs("XDG_DATA_HOME").or_else(|| abs("HOME").map(|h| h.join(".local").join("share"))).map(|d| d.join("scriobh"))
    }
}

pub fn system_data_dir() -> Option<PathBuf> {
    default_data_dir(|k| std::env::var_os(k))
}

/// `Path::exists` answers `false` for "could not look", which would make an unreadable folder a first run, and a
/// first run is when the shell makes a new key. `try_exists` tells the two apart.
pub fn state(dir: &Path) -> DataState {
    match dir.join("secret.key").try_exists() {
        Ok(true) => return DataState::HasKeyFile,
        Ok(false) => {}
        Err(_) => return DataState::Unreadable,
    }
    match dir.join("scriobh.db").try_exists() {
        Ok(true) => DataState::DataWithoutKeyFile,
        Ok(false) => DataState::Fresh,
        Err(_) => DataState::Unreadable,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn env<'a>(pairs: &'a [(&'a str, &'a str)]) -> impl Fn(&str) -> Option<OsString> + 'a {
        move |k| pairs.iter().find(|(n, _)| *n == k).map(|(_, v)| OsString::from(v))
    }

    #[cfg(target_os = "macos")]
    #[test]
    fn macos_location() {
        let d = default_data_dir(env(&[("HOME", "/Users/a")])).unwrap();
        assert_eq!(d, PathBuf::from("/Users/a/Library/Application Support/Scriobh"));
        assert_eq!(default_data_dir(env(&[])), None);
        assert_eq!(default_data_dir(env(&[("HOME", "relative")])), None);
    }

    #[cfg(all(unix, not(target_os = "macos")))]
    #[test]
    fn linux_location() {
        assert_eq!(
            default_data_dir(env(&[("HOME", "/home/a")])).unwrap(),
            PathBuf::from("/home/a/.local/share/scriobh")
        );
        assert_eq!(
            default_data_dir(env(&[("HOME", "/home/a"), ("XDG_DATA_HOME", "/data")])).unwrap(),
            PathBuf::from("/data/scriobh")
        );
        // A relative XDG_DATA_HOME is invalid by the specification and is ignored.
        assert_eq!(
            default_data_dir(env(&[("HOME", "/home/a"), ("XDG_DATA_HOME", "rel")])).unwrap(),
            PathBuf::from("/home/a/.local/share/scriobh")
        );
        assert_eq!(default_data_dir(env(&[])), None);
    }

    #[cfg(windows)]
    #[test]
    fn windows_location() {
        let d = default_data_dir(env(&[("APPDATA", r"C:\Users\a\AppData\Roaming")])).unwrap();
        assert_eq!(d, PathBuf::from(r"C:\Users\a\AppData\Roaming\Scriobh"));
    }

    #[test]
    fn state_is_read_from_two_file_names() {
        let dir = std::env::temp_dir().join(format!("scriobh-shell-state-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        assert_eq!(state(&dir), DataState::Fresh);
        std::fs::create_dir_all(&dir).unwrap();
        assert_eq!(state(&dir), DataState::Fresh);
        std::fs::write(dir.join("scriobh.db"), b"").unwrap();
        assert_eq!(state(&dir), DataState::DataWithoutKeyFile);
        std::fs::write(dir.join("secret.key"), b"").unwrap();
        assert_eq!(state(&dir), DataState::HasKeyFile);
        std::fs::remove_dir_all(&dir).unwrap();
    }

    /// Review. `Path::exists` answers `false` for "could not look", so a data folder that cannot be read is
    /// reported as a first run, and a first run is when the shell makes a new key and puts it in the keychain.
    #[cfg(unix)]
    #[test]
    fn a_data_folder_that_cannot_be_read_is_not_a_first_run() {
        use std::os::unix::fs::PermissionsExt;
        if unsafe { libc::geteuid() } == 0 {
            return; // root reads everything
        }
        let dir = std::env::temp_dir().join(format!("scriobh-shell-unreadable-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("scriobh.db"), b"x").unwrap();
        std::fs::set_permissions(&dir, std::fs::Permissions::from_mode(0o000)).unwrap();
        let got = state(&dir);
        std::fs::set_permissions(&dir, std::fs::Permissions::from_mode(0o700)).unwrap();
        std::fs::remove_dir_all(&dir).unwrap();
        assert_eq!(got, DataState::Unreadable, "an unreadable folder holding a database was called {got:?}");
    }
}
