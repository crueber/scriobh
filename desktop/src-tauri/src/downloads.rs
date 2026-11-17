//! The bookkeeping for downloads that are on their way to a save dialog: how many may be in flight, and which
//! temporary file belongs to which download. No window, no dialog and no webview here, so it is tested on its own.
//!
//! A download is in flight from the moment the webview is told it may start until its temporary file has been
//! placed where the person chose, or thrown away. Without a bound a page could queue any number of save dialogs
//! (a blob that finishes at once, in a loop), each holding a copy of its file.

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicUsize, Ordering};
use std::sync::Arc;

/// How many downloads may be in flight at once. A person saves a book or two; a page that wants more is refused.
pub const MAX_IN_FLIGHT: usize = 4;

/// Counts the downloads in flight.
pub struct Gate {
    count: Arc<AtomicUsize>,
    max: usize,
}

/// One place in the gate. Giving it back is dropping it, on whatever path the download ends.
pub struct Slot(Arc<AtomicUsize>);

impl Drop for Slot {
    fn drop(&mut self) {
        self.0.fetch_sub(1, Ordering::SeqCst);
    }
}

impl Gate {
    pub fn new(max: usize) -> Self {
        Gate { count: Arc::new(AtomicUsize::new(0)), max }
    }

    /// A place, or `None` when `max` downloads are already in flight.
    pub fn enter(&self) -> Option<Slot> {
        let mut n = self.count.load(Ordering::SeqCst);
        loop {
            if n >= self.max {
                return None;
            }
            match self.count.compare_exchange(n, n + 1, Ordering::SeqCst, Ordering::SeqCst) {
                Ok(_) => return Some(Slot(self.count.clone())),
                Err(now) => n = now,
            }
        }
    }

    #[cfg(test)]
    pub fn in_flight(&self) -> usize {
        self.count.load(Ordering::SeqCst)
    }
}

/// A download the webview is writing.
pub struct Entry {
    pub url: String,
    pub temp: PathBuf,
    pub name: String,
    pub slot: Slot,
}

/// The downloads the webview is still writing, until each reports that it finished.
#[derive(Default)]
pub struct Table {
    entries: Vec<Entry>,
}

impl Table {
    /// True when this address is being written already. A second download of the same address is refused,
    /// because when it finishes the webview names the address and, on some platforms, nothing else: two of them
    /// could not be told apart.
    pub fn has_url(&self, url: &str) -> bool {
        self.entries.iter().any(|e| e.url == url)
    }

    pub fn add(&mut self, entry: Entry) {
        self.entries.push(entry);
    }

    /// The entry a finished download belongs to: by the file's path when the webview says it (Windows, Linux),
    /// otherwise by its address (macOS), which is unique because of [`Table::has_url`].
    pub fn take(&mut self, url: &str, path: Option<&Path>) -> Option<Entry> {
        let at = path
            .and_then(|p| self.entries.iter().position(|e| e.temp == p))
            .or_else(|| self.entries.iter().position(|e| e.url == url))?;
        Some(self.entries.remove(at))
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn entry(gate: &Gate, url: &str, temp: &str) -> Option<Entry> {
        Some(Entry { url: url.into(), temp: PathBuf::from(temp), name: "n".into(), slot: gate.enter()? })
    }

    #[test]
    fn only_a_few_downloads_are_in_flight_and_a_finished_one_makes_room() {
        let gate = Gate::new(MAX_IN_FLIGHT);
        let mut held: Vec<Slot> = (0..MAX_IN_FLIGHT).map(|_| gate.enter().expect("room")).collect();
        assert_eq!(gate.in_flight(), MAX_IN_FLIGHT);
        for _ in 0..50 {
            assert!(gate.enter().is_none(), "a page cannot queue more");
        }
        assert_eq!(gate.in_flight(), MAX_IN_FLIGHT, "refusals take nothing");
        held.pop();
        assert_eq!(gate.in_flight(), MAX_IN_FLIGHT - 1);
        let again = gate.enter();
        assert!(again.is_some());
        drop(again);
        drop(held);
        assert_eq!(gate.in_flight(), 0);
    }

    #[test]
    fn a_place_is_given_back_on_every_path_that_drops_the_entry() {
        let gate = Gate::new(2);
        let mut table = Table::default();
        table.add(entry(&gate, "http://127.0.0.1:1/a", "/t/a").unwrap());
        table.add(entry(&gate, "http://127.0.0.1:1/b", "/t/b").unwrap());
        assert!(entry(&gate, "http://127.0.0.1:1/c", "/t/c").is_none());
        // Finished, then the dialog is cancelled or the file cannot be placed: the entry is dropped.
        drop(table.take("http://127.0.0.1:1/a", None));
        assert_eq!(gate.in_flight(), 1);
        // Finished and handed to the dialog: the place stays until the callback has dropped it.
        let kept = table.take("http://127.0.0.1:1/b", None).unwrap();
        assert_eq!(gate.in_flight(), 1);
        drop(kept);
        assert_eq!(gate.in_flight(), 0);
    }

    #[test]
    fn a_finished_download_is_matched_by_its_path_or_else_by_its_address() {
        let gate = Gate::new(4);
        let mut table = Table::default();
        table.add(entry(&gate, "blob:http://127.0.0.1:1/x", "/t/1/x.md").unwrap());
        table.add(entry(&gate, "http://127.0.0.1:1/y", "/t/2/y.epub").unwrap());
        assert!(table.has_url("http://127.0.0.1:1/y") && !table.has_url("http://127.0.0.1:1/z"));
        // The path wins over a wrong address.
        let e = table.take("http://127.0.0.1:1/y", Some(Path::new("/t/1/x.md"))).unwrap();
        assert_eq!(e.temp, PathBuf::from("/t/1/x.md"));
        // No path (macOS): the address finds it.
        let e = table.take("http://127.0.0.1:1/y", None).unwrap();
        assert_eq!(e.temp, PathBuf::from("/t/2/y.epub"));
        // Nothing left, and an address that was never started finds nothing.
        assert!(table.take("http://127.0.0.1:1/y", None).is_none());
        assert!(table.take("http://127.0.0.1:1/q", Some(Path::new("/t/9/q"))).is_none());
    }
}
