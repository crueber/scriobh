//! The network side of an update, in one place that can be audited: what the update client may do, and for how long.
//!
//! The plugin builds its own HTTP client. Three things are added to it here and nowhere else: every request has a deadline (the
//! check, the connection, a stalled read, and the whole download), a redirect is followed only to another `https` address (no
//! hop through plain `http`), and a download is abandoned when it is too big, when it takes too long, or when the person turns
//! the switch off while it runs. None of this chooses WHERE the app looks: the address and the key are the build's configuration
//! and nothing else (`tests/lockdown.rs`).

use std::future::{poll_fn, Future};
use std::pin::Pin;
use std::sync::atomic::{AtomicU64, AtomicU8, Ordering};
use std::sync::{Arc, Mutex};
use std::task::{Poll, Waker};
use std::time::Duration;

use tauri_plugin_updater::{Update, UpdaterBuilder};

/// The largest package the app will take: the biggest one now (the AppImage) is about 100 MB.
pub const MAX_PACKAGE: u64 = 300 * 1024 * 1024;
/// The check is one small file.
pub const CHECK_TIMEOUT: Duration = Duration::from_secs(30);
pub const CONNECT_TIMEOUT: Duration = Duration::from_secs(15);
/// No byte for this long ends a transfer.
pub const IDLE_TIMEOUT: Duration = Duration::from_secs(60);
/// The whole download, from the first byte to the last.
pub const DOWNLOAD_TIMEOUT: Duration = Duration::from_secs(20 * 60);
const WATCH_EVERY: Duration = Duration::from_millis(500);
/// A redirect is followed while fewer than this many have been: at most five are followed, the sixth is refused.
const MAX_REDIRECTS: usize = 5;

/// The update client with its deadlines and its redirect policy.
pub fn harden(builder: UpdaterBuilder) -> UpdaterBuilder {
    builder.timeout(CHECK_TIMEOUT).configure_client(|client| {
        client.connect_timeout(CONNECT_TIMEOUT).read_timeout(IDLE_TIMEOUT).redirect(https_only())
    })
}

/// Redirects are followed (GitHub sends a release asset to its own storage host) but never to an address that is not `https`,
/// and never more than a few times.
fn https_only() -> reqwest::redirect::Policy {
    reqwest::redirect::Policy::custom(|attempt| {
        match // previous() starts with the address first asked for, so the redirects already followed are one fewer
        redirect_decision(attempt.previous().len().saturating_sub(1), attempt.url().scheme()) {
            Ok(()) => attempt.follow(),
            Err(why) => attempt.error(why),
        }
    })
}

/// Whether to follow a redirect to an address with this scheme, after this many earlier hops.
fn redirect_decision(previous_hops: usize, scheme: &str) -> Result<(), &'static str> {
    if previous_hops >= MAX_REDIRECTS {
        Err("too many redirects")
    } else if scheme != "https" {
        Err("a redirect to an address that is not https")
    } else {
        Ok(())
    }
}

#[derive(Debug, PartialEq, Eq)]
pub enum DownloadFailure {
    TooBig,
    /// The person turned the switch off while it ran.
    Cancelled,
    Failed,
}

/// Why the watcher ended a download.
const RUNNING: u8 = 0;
const BIG: u8 = 1;
const CANCELLED: u8 = 2;

struct Shared {
    stop: AtomicU8,
    done: AtomicU8,
    waker: Mutex<Option<Waker>>,
}

impl Shared {
    fn stop_with(&self, why: u8) {
        let _ = self.stop.compare_exchange(RUNNING, why, Ordering::SeqCst, Ordering::SeqCst);
        if let Some(w) = self.waker.lock().ok().and_then(|mut w| w.take()) {
            w.wake();
        }
    }
}

/// Downloads a package (the plugin checks the signature against the key built into the app, and the version it was made for),
/// within [`MAX_PACKAGE`] and [`DOWNLOAD_TIMEOUT`], and gives up at once when `cancel()` says so. The download is a future that
/// is dropped, so its connection closes, the moment a limit is reached.
pub async fn bounded_download(
    update: &Update,
    cancel: impl Fn() -> bool + Send + 'static,
) -> Result<Vec<u8>, DownloadFailure> {
    let mut update = update.clone();
    update.timeout = Some(DOWNLOAD_TIMEOUT);
    run_bounded(|on_chunk| update.download(on_chunk, || {}), cancel).await
}

type OnChunk = Box<dyn FnMut(usize, Option<u64>) + Send>;

/// The limits, around any download: `make` is given the callback to report each chunk to, and returns the download. Tested with
/// stand-in downloads (`tests` below); `bounded_download` is this with the plugin's.
async fn run_bounded<E, F>(
    make: impl FnOnce(OnChunk) -> F,
    cancel: impl Fn() -> bool + Send + 'static,
) -> Result<Vec<u8>, DownloadFailure>
where
    F: Future<Output = Result<Vec<u8>, E>> + Send,
{
    let shared = Arc::new(Shared { stop: AtomicU8::new(RUNNING), done: AtomicU8::new(0), waker: Mutex::new(None) });
    let received = Arc::new(AtomicU64::new(0));
    // The watcher must end however this future ends: finished, or DROPPED by its caller before it finished.
    struct DoneOnDrop(Arc<Shared>);
    impl Drop for DoneOnDrop {
        fn drop(&mut self) {
            self.0.done.store(1, Ordering::SeqCst);
        }
    }
    let _end_watch = DoneOnDrop(shared.clone());
    // A thread looks every half second whether the person has said stop, so that a download that is waiting for bytes ends too.
    let watcher = {
        let shared = shared.clone();
        std::thread::spawn(move || {
            while shared.done.load(Ordering::SeqCst) == 0 {
                std::thread::sleep(WATCH_EVERY);
                if cancel() {
                    shared.stop_with(CANCELLED);
                }
            }
        })
    };
    let on_chunk: OnChunk = {
        let (shared, received) = (shared.clone(), received.clone());
        Box::new(move |chunk: usize, total: Option<u64>| {
            let so_far = received.fetch_add(chunk as u64, Ordering::SeqCst) + chunk as u64;
            if so_far > MAX_PACKAGE || total.is_some_and(|t| t > MAX_PACKAGE) {
                shared.stop_with(BIG);
            }
        })
    };
    let result = {
        let mut download: Pin<Box<dyn Future<Output = Result<Vec<u8>, E>> + Send + '_>> = Box::pin(make(on_chunk));
        let shared = shared.clone();
        poll_fn(move |cx| {
            if let Ok(mut w) = shared.waker.lock() {
                *w = Some(cx.waker().clone());
            }
            let ended = |s: &Shared| match s.stop.load(Ordering::SeqCst) {
                BIG => Some(Err(DownloadFailure::TooBig)),
                CANCELLED => Some(Err(DownloadFailure::Cancelled)),
                _ => None,
            };
            if let Some(r) = ended(&shared) {
                return Poll::Ready(r);
            }
            match download.as_mut().poll(cx) {
                // Whatever the download made, a limit reached while it ran is what is reported
                Poll::Ready(Ok(bytes)) => Poll::Ready(ended(&shared).unwrap_or(Ok(bytes))),
                Poll::Ready(Err(_)) => Poll::Ready(ended(&shared).unwrap_or(Err(DownloadFailure::Failed))),
                // a limit reached while this poll ran ends the download now, not at the next byte
                Poll::Pending => ended(&shared).map(Poll::Ready).unwrap_or(Poll::Pending),
            }
        })
        .await
    };
    // The watcher ends within half a second by itself (`_end_watch` tells it); nothing waits for it.
    drop(watcher);
    result
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::AtomicBool;
    use std::time::Instant;

    fn run<T>(f: impl Future<Output = T>) -> T {
        tauri::async_runtime::block_on(f)
    }

    #[test]
    fn a_download_within_the_limits_is_returned() {
        let got = run(run_bounded(
            |mut on_chunk| async move {
                on_chunk(5, Some(10));
                on_chunk(5, Some(10));
                Ok::<_, ()>(b"0123456789".to_vec())
            },
            || false,
        ));
        assert_eq!(got, Ok(b"0123456789".to_vec()));
    }

    #[test]
    fn a_package_that_says_it_is_too_big_is_refused_at_once() {
        let began = Instant::now();
        let got = run(run_bounded(
            |mut on_chunk| async move {
                on_chunk(1, Some(MAX_PACKAGE + 1));
                std::future::pending::<Result<Vec<u8>, ()>>().await
            },
            || false,
        ));
        assert_eq!(got, Err(DownloadFailure::TooBig));
        assert!(began.elapsed() < Duration::from_secs(2), "it waited for a download that was refused");
    }

    #[test]
    fn a_package_that_grows_past_the_limit_is_abandoned() {
        let got = run(run_bounded(
            |mut on_chunk| async move {
                let mut sent = 0u64;
                while sent <= MAX_PACKAGE {
                    on_chunk(64 * 1024 * 1024, None); // no length announced: the bytes are counted
                    sent += 64 * 1024 * 1024;
                }
                std::future::pending::<Result<Vec<u8>, ()>>().await
            },
            || false,
        ));
        assert_eq!(got, Err(DownloadFailure::TooBig));
    }

    #[test]
    fn a_download_that_stalls_ends_when_the_person_turns_the_switch_off() {
        let off = Arc::new(AtomicBool::new(false));
        let flip = off.clone();
        std::thread::spawn(move || {
            std::thread::sleep(Duration::from_millis(300));
            flip.store(true, Ordering::SeqCst);
        });
        let began = Instant::now();
        let got =
            run(run_bounded(|_| std::future::pending::<Result<Vec<u8>, ()>>(), move || off.load(Ordering::SeqCst)));
        assert_eq!(got, Err(DownloadFailure::Cancelled));
        assert!(began.elapsed() < Duration::from_secs(3), "{:?}", began.elapsed());
    }

    #[test]
    fn the_watcher_ends_when_the_download_is_dropped_before_it_finishes() {
        use std::sync::atomic::AtomicUsize;
        use std::task::{Context, Waker};
        let looks = Arc::new(AtomicUsize::new(0));
        let counted = looks.clone();
        {
            let mut fut = Box::pin(run_bounded(
                |_| std::future::pending::<Result<Vec<u8>, ()>>(),
                move || {
                    counted.fetch_add(1, Ordering::SeqCst);
                    false
                },
            ));
            assert!(fut.as_mut().poll(&mut Context::from_waker(Waker::noop())).is_pending());
            std::thread::sleep(Duration::from_millis(700)); // the watcher looks at least once
        } // dropped here, unfinished
        std::thread::sleep(Duration::from_millis(700)); // let a look that was under way end
        let after_drop = looks.load(Ordering::SeqCst);
        assert!(after_drop >= 1, "the watcher never looked");
        std::thread::sleep(Duration::from_millis(1600));
        assert_eq!(looks.load(Ordering::SeqCst), after_drop, "the watcher kept looking after its download was dropped");
    }

    #[test]
    fn a_failed_download_is_a_failure_and_a_limit_wins_over_it() {
        assert_eq!(
            run(run_bounded(|_| async { Err::<Vec<u8>, _>("network") }, || false)),
            Err(DownloadFailure::Failed)
        );
        let got = run(run_bounded(
            |mut on_chunk| async move {
                on_chunk(1, Some(MAX_PACKAGE + 1));
                Err::<Vec<u8>, _>("cut off")
            },
            || false,
        ));
        assert_eq!(got, Err(DownloadFailure::TooBig));
    }

    #[test]
    fn a_redirect_is_followed_only_over_https_and_only_a_few_times() {
        assert_eq!(redirect_decision(0, "https"), Ok(()));
        assert_eq!(redirect_decision(MAX_REDIRECTS - 1, "https"), Ok(()));
        assert!(redirect_decision(MAX_REDIRECTS, "https").is_err(), "a long chain");
        for scheme in ["http", "ftp", "file", "data", ""] {
            assert!(redirect_decision(0, scheme).is_err(), "{scheme}");
        }
    }

    // The numbers the notes state, held at compile time.
    const _: () = assert!(MAX_PACKAGE == 300 * 1024 * 1024);
    const _: () = assert!(
        CHECK_TIMEOUT.as_secs() <= 60 && IDLE_TIMEOUT.as_secs() <= 120 && DOWNLOAD_TIMEOUT.as_secs() <= 30 * 60
    );
    const _: () = assert!(MAX_REDIRECTS <= 10);
}
