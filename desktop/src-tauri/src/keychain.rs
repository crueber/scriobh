//! The instance secret key and the operating system's keychain.
//!
//! The key seals the AI and Git credentials. In the desktop app it lives in the keychain (macOS Keychain,
//! Windows Credential Manager, the Secret Service on Linux) and is handed to the server in its environment at
//! launch. The rules here exist so that a key is never replaced by one that cannot open what the old one sealed.
//! The server makes its own `secret.key` whenever it is given no key and finds no file, so "pass no key" is only
//! ever answered when that is safe:
//!
//! - a key in the keychain is used, unless the data folder also has a `secret.key`: then the file wins, and the
//!   person is told once that there are two;
//! - a data folder that has a key file keeps using the file (no keychain key is made);
//! - a keychain that cannot be read, or holds something that is not a key, while the data folder has a database
//!   and no key file, **stops the launch**: starting would make a second key and strand what the first sealed;
//! - a data folder that cannot be looked at stops the launch;
//! - a keychain that cannot be read is never written to;
//! - a new key is made only on a first run, and only used if it reads back.

use zeroize::Zeroizing;

use crate::datadir::DataState;

/// Hex characters in a key the shell makes (32 random bytes).
pub const KEY_HEX_LEN: usize = 64;
/// The shortest key the server accepts.
pub const MIN_KEY_LEN: usize = 32;

#[derive(Debug, PartialEq, Eq)]
pub enum StoreError {
    /// No keychain on this system, or it is locked, or access was refused.
    Unavailable(String),
}

/// One secret in one place. The real one is the OS keychain; tests use a map.
pub trait KeyStore {
    /// `Ok(None)` means the keychain works and holds no key for the app.
    fn get(&self) -> Result<Option<Zeroizing<String>>, StoreError>;
    fn set(&self, key: &str) -> Result<(), StoreError>;
}

/// Something the person should be told, once.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Notice {
    /// The keychain could not be used; the key file in the data folder is used instead.
    KeychainUnavailable,
    /// There is data but no key anywhere: saved credentials will have to be entered again.
    KeyMissing,
    /// The keychain holds a key and the data folder has a `secret.key` too. The file is used.
    KeyFileOverKeychain,
    /// The launch is stopped: the keychain cannot be read (or holds something that is not a key) and the data
    /// folder has data but no key file, so the server would make a second key.
    KeychainBlocksStart,
    /// The launch is stopped: the data folder could not be looked at.
    DataFolderUnreadable,
}

impl Notice {
    /// True when the server must not be started at all.
    pub fn stops_launch(self) -> bool {
        matches!(self, Notice::KeychainBlocksStart | Notice::DataFolderUnreadable)
    }
}

#[derive(Debug)]
pub struct Resolved {
    /// The key to pass as `SCRIOBH_SECRET_KEY`, or `None` to let the server use its key file. Always `None` when
    /// the notice stops the launch.
    pub key: Option<Zeroizing<String>>,
    pub notice: Option<Notice>,
    /// True when this call made the key and stored it.
    pub created: bool,
}

impl Resolved {
    fn file(notice: Option<Notice>) -> Self {
        Resolved { key: None, notice, created: false }
    }

    /// No key and no launch: the notice says why.
    pub fn stop(notice: Notice) -> Self {
        debug_assert!(notice.stops_launch());
        Resolved::file(Some(notice))
    }
}

fn usable(key: &str) -> bool {
    key.len() >= MIN_KEY_LEN && key.bytes().all(|b| b.is_ascii_graphic())
}

/// Decides which key the server gets. `generate` is called at most once, and only on a first run.
pub fn resolve(
    store: &dyn KeyStore,
    state: DataState,
    generate: impl FnOnce() -> Option<Zeroizing<String>>,
) -> Resolved {
    if state == DataState::Unreadable {
        // Nothing is read from the keychain and nothing is made: the folder's contents are not known.
        return Resolved::file(Some(Notice::DataFolderUnreadable));
    }
    // What the keychain cannot give is not a reason to start a server that makes its own key over existing data.
    let cannot_use = || {
        if state == DataState::DataWithoutKeyFile {
            Resolved::file(Some(Notice::KeychainBlocksStart))
        } else {
            Resolved::file(Some(Notice::KeychainUnavailable))
        }
    };
    match store.get() {
        Err(_) => cannot_use(),
        // Something is stored under our name that is not a key. Leave it alone and do not use it.
        Ok(Some(key)) if !usable(&key) => cannot_use(),
        // Both exist: the file is what the server would use on its own, and what a browser-mode install had, so it
        // wins, and the person is told. The keychain is not touched.
        Ok(Some(_)) if state == DataState::HasKeyFile => Resolved::file(Some(Notice::KeyFileOverKeychain)),
        Ok(Some(key)) => Resolved { key: Some(key), notice: None, created: false },
        Ok(None) => match state {
            DataState::HasKeyFile => Resolved::file(None),
            DataState::DataWithoutKeyFile => Resolved::file(Some(Notice::KeyMissing)),
            DataState::Unreadable => Resolved::file(Some(Notice::DataFolderUnreadable)),
            DataState::Fresh => {
                let Some(key) = generate().filter(|k| usable(k)) else {
                    return Resolved::file(Some(Notice::KeychainUnavailable));
                };
                if store.set(&key).is_err() {
                    return Resolved::file(Some(Notice::KeychainUnavailable));
                }
                // Only trust a keychain that gives back what it was given: a store that accepts and forgets
                // would give the server a different key at every launch.
                match store.get() {
                    Ok(Some(back)) if *back == *key => Resolved { key: Some(key), notice: None, created: true },
                    _ => Resolved::file(Some(Notice::KeychainUnavailable)),
                }
            }
        },
    }
}

/// 32 bytes from the operating system, as 64 hex characters.
pub fn generate_key() -> Option<Zeroizing<String>> {
    let mut raw = Zeroizing::new([0u8; 32]);
    getrandom::fill(&mut raw[..]).ok()?;
    Some(crate::launch::hex(&raw[..]))
}

/// The OS keychain, through the `keyring` crate.
pub struct OsKeychain {
    service: String,
}

/// The account name of the one entry.
pub const ACCOUNT: &str = "instance-secret-key";
/// The service name in a release build.
pub const SERVICE: &str = "Scriobh";

impl OsKeychain {
    pub fn new(service: &str) -> Self {
        OsKeychain { service: service.to_string() }
    }

    fn entry(&self) -> Result<keyring::Entry, StoreError> {
        keyring::Entry::new(&self.service, ACCOUNT).map_err(describe)
    }

    /// Removes the entry. Used by the keychain test only.
    pub fn delete(&self) -> Result<(), StoreError> {
        match self.entry()?.delete_credential() {
            Ok(()) | Err(keyring::Error::NoEntry) => Ok(()),
            Err(e) => Err(describe(e)),
        }
    }
}

// The text of a keychain error names the failure, never the secret; it is kept for the notes of a test run and
// is not shown or logged by the app.
fn describe(e: keyring::Error) -> StoreError {
    StoreError::Unavailable(match e {
        keyring::Error::NoEntry => "no entry".into(),
        keyring::Error::NoStorageAccess(_) => "no access to the keychain".into(),
        keyring::Error::PlatformFailure(_) => "the keychain failed".into(),
        keyring::Error::NoDefaultStore => "no keychain on this system".into(),
        _ => "the keychain refused".into(),
    })
}

impl KeyStore for OsKeychain {
    fn get(&self) -> Result<Option<Zeroizing<String>>, StoreError> {
        match self.entry()?.get_password() {
            Ok(p) => Ok(Some(Zeroizing::new(p))),
            Err(keyring::Error::NoEntry) => Ok(None),
            Err(e) => Err(describe(e)),
        }
    }

    fn set(&self, key: &str) -> Result<(), StoreError> {
        self.entry()?.set_password(key).map_err(describe)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::cell::{Cell, RefCell};

    #[derive(Default)]
    struct Fake {
        held: RefCell<Option<String>>,
        get_fails: Cell<bool>,
        set_fails: Cell<bool>,
        forgets: Cell<bool>,
        sets: Cell<u32>,
    }

    impl Fake {
        fn holding(k: &str) -> Self {
            let f = Fake::default();
            *f.held.borrow_mut() = Some(k.to_string());
            f
        }
    }

    impl KeyStore for Fake {
        fn get(&self) -> Result<Option<Zeroizing<String>>, StoreError> {
            if self.get_fails.get() {
                return Err(StoreError::Unavailable("denied".into()));
            }
            Ok(self.held.borrow().clone().map(Zeroizing::new))
        }
        fn set(&self, key: &str) -> Result<(), StoreError> {
            self.sets.set(self.sets.get() + 1);
            if self.set_fails.get() {
                return Err(StoreError::Unavailable("denied".into()));
            }
            if !self.forgets.get() {
                *self.held.borrow_mut() = Some(key.to_string());
            }
            Ok(())
        }
    }

    const K1: &str = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef";
    const K2: &str = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210";

    fn never() -> Option<Zeroizing<String>> {
        panic!("a key must not be generated here")
    }
    fn k2() -> Option<Zeroizing<String>> {
        Some(Zeroizing::new(K2.to_string()))
    }

    #[test]
    fn a_first_run_makes_a_key_stores_it_and_passes_it() {
        let s = Fake::default();
        let r = resolve(&s, DataState::Fresh, k2);
        assert_eq!(r.key.as_deref().map(|k| k.as_str()), Some(K2));
        assert!(r.created);
        assert_eq!(r.notice, None);
        assert_eq!(s.held.borrow().as_deref(), Some(K2));
        // The second launch uses the same key and makes nothing.
        let again = resolve(&s, DataState::DataWithoutKeyFile, never);
        assert_eq!(again.key.as_deref().map(|k| k.as_str()), Some(K2));
        assert!(!again.created);
        assert_eq!(s.sets.get(), 1);
    }

    #[test]
    fn a_stored_key_is_used_when_there_is_no_key_file() {
        for state in [DataState::Fresh, DataState::DataWithoutKeyFile] {
            let s = Fake::holding(K1);
            let r = resolve(&s, state, never);
            assert_eq!(r.key.as_deref().map(|k| k.as_str()), Some(K1), "{state:?}");
            assert_eq!(r.notice, None);
            assert_eq!(s.sets.get(), 0);
        }
    }

    /// Both a keychain key and a `secret.key` exist (a restored backup that carried its key file, or a desktop
    /// install that was also run without the shell). The file wins, the person is told, nothing is written and
    /// nothing is deleted.
    #[test]
    fn a_key_file_beats_the_keychain_and_the_person_is_told() {
        let s = Fake::holding(K1);
        let r = resolve(&s, DataState::HasKeyFile, never);
        assert!(r.key.is_none(), "the keychain's key must not be passed over the key file");
        assert_eq!(r.notice, Some(Notice::KeyFileOverKeychain));
        assert!(!r.notice.unwrap().stops_launch());
        assert_eq!(s.sets.get(), 0);
        assert_eq!(s.held.borrow().as_deref(), Some(K1), "the keychain is left as it was");
    }

    #[test]
    fn an_existing_key_file_stays_the_key_and_nothing_is_written() {
        let s = Fake::default();
        let r = resolve(&s, DataState::HasKeyFile, never);
        assert!(r.key.is_none());
        assert_eq!(r.notice, None);
        assert_eq!(s.sets.get(), 0);
        assert!(s.held.borrow().is_none());
    }

    #[test]
    fn data_without_any_key_is_not_given_a_new_keychain_key_silently() {
        let s = Fake::default();
        let r = resolve(&s, DataState::DataWithoutKeyFile, never);
        assert!(r.key.is_none());
        assert_eq!(r.notice, Some(Notice::KeyMissing));
        assert_eq!(s.sets.get(), 0);
    }

    /// Review. A keychain that fails on an install whose data is already sealed with the keychain's key is not a
    /// first run: with no key and no key file the server makes a new `secret.key` (seen with the real server),
    /// the saved credentials cannot be read this session, webhooks are switched off for good, and the next
    /// launch (keychain open again) uses the old key and ignores the new file. The caller has to be able to tell
    /// this from "no keychain on this system", so the person can retry instead of being given a second key.
    #[test]
    fn a_failing_keychain_on_existing_data_is_not_the_same_answer_as_no_keychain() {
        let broken = Fake::holding(K1);
        broken.get_fails.set(true);
        let existing = resolve(&broken, DataState::DataWithoutKeyFile, never);
        let first_run = resolve(&broken, DataState::Fresh, never);
        assert_ne!(
            existing.notice, first_run.notice,
            "a locked or refused keychain on existing data must not look like a first run's fallback to a key file"
        );
    }

    /// Review. A key in the keychain wins over a `secret.key` that is also there, silently. Both exist after a
    /// restore of a backup that carried its key file, or after the case above. Whichever is right, the person
    /// must be told that there are two.
    #[test]
    fn a_keychain_key_and_a_key_file_together_are_not_silent() {
        let s = Fake::holding(K1);
        let r = resolve(&s, DataState::HasKeyFile, never);
        assert!(r.notice.is_some(), "two different keys may now exist and nothing was said");
    }

    /// The key is in a keychain that cannot be read right now and the data folder has a database but no key file:
    /// the server would make a second key. The launch is stopped, whatever the reason the keychain failed.
    #[test]
    fn a_keychain_that_cannot_be_read_stops_the_launch_when_data_has_no_key_file() {
        let s = Fake::holding(K1);
        s.get_fails.set(true);
        let r = resolve(&s, DataState::DataWithoutKeyFile, never);
        assert!(r.key.is_none());
        assert_eq!(r.notice, Some(Notice::KeychainBlocksStart));
        assert!(r.notice.unwrap().stops_launch());
        assert_eq!(s.sets.get(), 0);
        assert_eq!(s.held.borrow().as_deref(), Some(K1), "the stored key is untouched");
    }

    /// A first run, and an install whose key is in a file, can go on without the keychain: the server makes or
    /// keeps its own file. The keychain is never written.
    #[test]
    fn a_keychain_that_cannot_be_read_is_never_written_and_only_blocks_where_it_must() {
        for state in [DataState::Fresh, DataState::HasKeyFile] {
            let s = Fake::holding(K1);
            s.get_fails.set(true);
            let r = resolve(&s, state, never);
            assert!(r.key.is_none(), "{state:?}");
            assert_eq!(r.notice, Some(Notice::KeychainUnavailable));
            assert!(!r.notice.unwrap().stops_launch());
            assert_eq!(s.sets.get(), 0);
            assert_eq!(s.held.borrow().as_deref(), Some(K1), "the stored key is untouched");
        }
    }

    #[test]
    fn a_data_folder_that_could_not_be_looked_at_stops_the_launch_before_the_keychain_is_asked() {
        struct Untouchable;
        impl KeyStore for Untouchable {
            fn get(&self) -> Result<Option<Zeroizing<String>>, StoreError> {
                panic!("the keychain must not be read")
            }
            fn set(&self, _: &str) -> Result<(), StoreError> {
                panic!("the keychain must not be written")
            }
        }
        let r = resolve(&Untouchable, DataState::Unreadable, never);
        assert!(r.key.is_none() && !r.created);
        assert_eq!(r.notice, Some(Notice::DataFolderUnreadable));
        assert!(r.notice.unwrap().stops_launch());
    }

    #[test]
    fn a_keychain_that_refuses_the_write_leaves_the_key_file_in_use() {
        let s = Fake::default();
        s.set_fails.set(true);
        let r = resolve(&s, DataState::Fresh, k2);
        assert!(r.key.is_none());
        assert!(!r.created);
        assert_eq!(r.notice, Some(Notice::KeychainUnavailable));
    }

    #[test]
    fn a_keychain_that_forgets_is_not_trusted() {
        let s = Fake::default();
        s.forgets.set(true);
        let r = resolve(&s, DataState::Fresh, k2);
        assert!(r.key.is_none(), "a key that did not read back must not reach the server");
        assert_eq!(r.notice, Some(Notice::KeychainUnavailable));
    }

    #[test]
    fn a_stored_value_that_is_not_a_key_is_neither_used_nor_replaced() {
        for junk in [
            "",
            "short",
            "has a space in it and is long enough to pass the length",
            "line\nbreak-0123456789abcdef0123456789abcdef",
        ] {
            let s = Fake::holding(junk);
            let r = resolve(&s, DataState::Fresh, never);
            assert!(r.key.is_none(), "{junk:?}");
            assert_eq!(r.notice, Some(Notice::KeychainUnavailable));
            assert_eq!(s.held.borrow().as_deref(), Some(junk));
            // With data and no key file the server would make a key over it: that stops the launch instead.
            let r = resolve(&s, DataState::DataWithoutKeyFile, never);
            assert!(r.key.is_none(), "{junk:?}");
            assert_eq!(r.notice, Some(Notice::KeychainBlocksStart));
            assert_eq!(s.sets.get(), 0);
            assert_eq!(s.held.borrow().as_deref(), Some(junk));
        }
    }

    #[test]
    fn a_failed_or_weak_generator_gives_no_key() {
        let s = Fake::default();
        let r = resolve(&s, DataState::Fresh, || None);
        assert!(r.key.is_none());
        assert_eq!(s.sets.get(), 0);
        let r = resolve(&s, DataState::Fresh, || Some(Zeroizing::new("tooshort".into())));
        assert!(r.key.is_none());
        assert_eq!(s.sets.get(), 0);
    }

    #[test]
    fn generated_keys_are_64_hex_characters_and_differ() {
        let a = generate_key().unwrap();
        let b = generate_key().unwrap();
        assert_eq!(a.len(), KEY_HEX_LEN);
        assert!(a.bytes().all(|c| c.is_ascii_hexdigit() && !c.is_ascii_uppercase()));
        assert_ne!(*a, *b);
        assert!(usable(&a));
    }

    /// The real keychain of this machine, under a throwaway service name. Not run by default: it writes to the
    /// login keychain. `cargo test -- --ignored real_keychain`.
    #[test]
    #[ignore]
    fn real_keychain_round_trip() {
        let service = format!("Scriobh shell test {}", std::process::id());
        let s = OsKeychain::new(&service);
        assert_eq!(s.get().map(|k| k.is_some()), Ok(false));
        let r = resolve(&s, DataState::Fresh, generate_key);
        assert!(r.created, "{:?}", r.notice);
        let key = r.key.unwrap();
        let again = resolve(&OsKeychain::new(&service), DataState::Fresh, never);
        assert_eq!(again.key.as_deref().map(|k| k.as_str()), Some(key.as_str()));
        s.delete().unwrap();
        assert_eq!(s.get().map(|k| k.is_some()), Ok(false));
    }
}
