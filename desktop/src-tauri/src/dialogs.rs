//! Native dialogs, shown from the main loop and never from inside a webview callback (see `later`).
//!
//! The Tauri dialog plugin is not used on purpose: registering it puts a script into every page that replaces
//! `window.alert` and `window.confirm`. These are the operating system's own dialogs, through `rfd`, and each
//! call returns at once; the answer arrives in the callback.
//!
//! **On Linux a dialog is never run by calling rfd's blocking `show()` on the main thread.** rfd's GTK 3 backend
//! posts the dialog as an idle source on GLib's default main context and then waits for the result, and the only
//! thread that can run that source is the main thread, because tao's event loop owns the default context for as
//! long as it runs (glib-rs `with_thread_default` acquires it). A blocking call on the main thread therefore waits
//! for itself for ever: the whole window stops, and so does a quit (found in a headless Arch run: the main thread
//! on a futex in our dialog call, and rfd's own thread in `g_main_context_iteration`, waiting for a context the
//! main thread owned). Where rfd uses GTK the dialog is shown through its asynchronous API from the async
//! runtime instead: the main thread dispatches the dialog's source as an ordinary event and keeps running, the
//! answer comes back to a future, and `then` runs on a runtime thread. macOS and Windows keep the blocking call
//! on the main thread, as before.
//!
//! The blocking main thread used to queue dialogs for free: the second waited for the first. The asynchronous ones
//! would stack, so on Linux every dialog goes through one queue (`Serial`): requests are pushed in the order they
//! are made, one task takes them one at a time, and the next dialog is not shown until the previous one has been
//! answered and its `then` has run. `then` must therefore be short and must not wait for another dialog.

use std::future::Future;
use std::path::PathBuf;
use std::pin::Pin;
use std::sync::OnceLock;

use tokio::sync::mpsc::{unbounded_channel, UnboundedSender};

use tauri::AppHandle;

pub const APP_NAME: &str = "Scríobh";

/// True where rfd draws its dialogs with GTK 3: Linux and the BSDs (see the module comment).
const GTK_BACKEND: bool = cfg!(all(unix, not(target_os = "macos")));

type Job = Pin<Box<dyn Future<Output = ()> + Send + 'static>>;

/// A queue that runs one job at a time, in the order the jobs were pushed. `push` does not wait and may be called
/// from any thread, including the main one; the order is the order in which `push` returned, because the channel
/// is written before `push` does. Each job runs as a task of its own, so a panic in a callback ends that job and
/// not the queue.
struct Serial {
    jobs: UnboundedSender<Job>,
}

impl Serial {
    /// Starts the single task that takes the jobs, on the async runtime.
    fn new() -> Self {
        let (jobs, mut waiting) = unbounded_channel::<Job>();
        tauri::async_runtime::spawn(async move {
            while let Some(job) = waiting.recv().await {
                let _ = tauri::async_runtime::spawn(job).await;
            }
        });
        Serial { jobs }
    }

    /// Shows a dialog by awaiting `show`, then calls `then` with the answer, both when it is this request's turn.
    fn run<R: Send + 'static>(
        &self,
        show: impl Future<Output = R> + Send + 'static,
        then: impl FnOnce(R) + Send + 'static,
    ) {
        let _ = self.jobs.send(Box::pin(async move { then(show.await) }));
    }
}

/// The one queue for every dialog on a GTK backend.
fn dialog_queue() -> &'static Serial {
    static QUEUE: OnceLock<Serial> = OnceLock::new();
    QUEUE.get_or_init(Serial::new)
}

/// A line on standard error in a development build, so a run can be followed without a debugger. A release
/// build prints nothing. Callers never pass an address with a query, a token or a key.
pub fn note(text: &str) {
    if cfg!(debug_assertions) {
        eprintln!("shell: {text}");
    }
}

/// Development builds only (`--auto-dialogs <dir>`): no dialog is shown; a question is answered "no", a save
/// goes to `<dir>/<name>`, and each is noted. It lets `desktop/scripts/smoke.sh` drive the app with nobody at
/// the keyboard. A release build has no such switch.
#[cfg(debug_assertions)]
static AUTO: std::sync::OnceLock<PathBuf> = std::sync::OnceLock::new();

#[cfg(debug_assertions)]
pub fn answer_automatically(save_dir: PathBuf) {
    let _ = AUTO.set(save_dir);
}

#[cfg(debug_assertions)]
fn auto() -> Option<&'static PathBuf> {
    AUTO.get()
}

#[cfg(not(debug_assertions))]
fn auto() -> Option<&'static PathBuf> {
    None
}

/// Runs `f` on the main loop, later: never inside the caller. `run_on_main_thread` runs its task at once when
/// it is called from the main thread, and the webview's navigation, new-window and download handlers are called
/// there, from inside WebKit or WebView2. A modal dialog put up at that point would run a nested loop inside the
/// webview's callback. Going through another thread makes the task an ordinary event of the loop.
pub fn later(app: &AppHandle, f: impl FnOnce() + Send + 'static) {
    let app = app.clone();
    std::thread::spawn(move || {
        let _ = app.run_on_main_thread(f);
    });
}

pub fn info(app: &AppHandle, title: &str, body: &str) {
    info_then(app, title, body, || {});
}

/// Says something and runs `then` when the person has dismissed it.
pub fn info_then(app: &AppHandle, title: &str, body: &str, then: impl FnOnce() + Send + 'static) {
    let (title, body) = (title.to_string(), body.to_string());
    if GTK_BACKEND && auto().is_none() {
        dialog_queue().run(
            rfd::AsyncMessageDialog::new()
                .set_level(rfd::MessageLevel::Info)
                .set_title(title)
                .set_description(body)
                .set_buttons(rfd::MessageButtons::Ok)
                .show(),
            move |_| then(),
        );
        return;
    }
    later(app, move || {
        if auto().is_some() {
            note(&format!("dialog (not shown): {title}"));
            return then();
        }
        rfd::MessageDialog::new()
            .set_level(rfd::MessageLevel::Info)
            .set_title(title)
            .set_description(body)
            .set_buttons(rfd::MessageButtons::Ok)
            .show();
        then();
    });
}

/// Whether a two-button answer means the first button.
fn is_yes(answer: rfd::MessageDialogResult, yes: &str) -> bool {
    match answer {
        rfd::MessageDialogResult::Custom(label) => label == yes,
        rfd::MessageDialogResult::Ok | rfd::MessageDialogResult::Yes => true,
        _ => false,
    }
}

/// Asks a question with two named buttons. `then` gets `true` only for the first.
pub fn ask(app: &AppHandle, title: &str, body: &str, yes: &str, no: &str, then: impl FnOnce(bool) + Send + 'static) {
    let (title, body, yes, no) = (title.to_string(), body.to_string(), yes.to_string(), no.to_string());
    if GTK_BACKEND && auto().is_none() {
        let buttons = rfd::MessageButtons::OkCancelCustom(yes.clone(), no);
        dialog_queue().run(
            rfd::AsyncMessageDialog::new()
                .set_level(rfd::MessageLevel::Info)
                .set_title(title)
                .set_description(body)
                .set_buttons(buttons)
                .show(),
            move |answer| then(is_yes(answer, &yes)),
        );
        return;
    }
    later(app, move || {
        if auto().is_some() {
            note(&format!("question (not shown, answered {no}): {title}"));
            return then(false);
        }
        let answer = rfd::MessageDialog::new()
            .set_level(rfd::MessageLevel::Info)
            .set_title(title)
            .set_description(body)
            .set_buttons(rfd::MessageButtons::OkCancelCustom(yes.clone(), no))
            .show();
        then(is_yes(answer, &yes));
    });
}

/// The save dialog. `then` gets the place the person chose, or `None` when they cancelled.
pub fn save(app: &AppHandle, dir: Option<PathBuf>, name: &str, then: impl FnOnce(Option<PathBuf>) + Send + 'static) {
    let name = name.to_string();
    if GTK_BACKEND && auto().is_none() {
        let mut d = rfd::AsyncFileDialog::new().set_file_name(name).set_can_create_directories(true);
        if let Some(dir) = dir {
            d = d.set_directory(dir);
        }
        dialog_queue().run(d.save_file(), move |chosen| then(chosen.map(|c| c.path().to_path_buf())));
        return;
    }
    later(app, move || {
        if let Some(auto_dir) = auto() {
            note(&format!("save dialog (not shown): {name}"));
            return then(Some(auto_dir.join(name)));
        }
        let mut d = rfd::FileDialog::new().set_file_name(name).set_can_create_directories(true);
        if let Some(dir) = dir {
            d = d.set_directory(dir);
        }
        then(d.save_file());
    });
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicUsize, Ordering};
    use std::sync::{mpsc, Arc, Mutex};
    use std::time::Duration;

    /// A stand-in for a dialog: `open` and `close` are noted around a pause on a blocking thread, and the answer
    /// is the number of the dialog.
    async fn fake_dialog(
        n: usize,
        events: Arc<Mutex<Vec<String>>>,
        open_now: Arc<AtomicUsize>,
        most: Arc<AtomicUsize>,
    ) -> usize {
        let now = open_now.fetch_add(1, Ordering::SeqCst) + 1;
        most.fetch_max(now, Ordering::SeqCst);
        events.lock().unwrap().push(format!("open {n}"));
        // Later requests are shorter, so any overlap or reordering would show.
        let pause = Duration::from_millis(40 - 8 * n as u64);
        let _ = tauri::async_runtime::spawn_blocking(move || std::thread::sleep(pause)).await;
        events.lock().unwrap().push(format!("close {n}"));
        open_now.fetch_sub(1, Ordering::SeqCst);
        n
    }

    #[test]
    fn dialogs_run_one_at_a_time_in_request_order() {
        let queue = Serial::new();
        let events = Arc::new(Mutex::new(Vec::new()));
        let (open_now, most) = (Arc::new(AtomicUsize::new(0)), Arc::new(AtomicUsize::new(0)));
        let (done, finished) = mpsc::channel();
        for n in 0..4 {
            let (e, o, m) = (events.clone(), open_now.clone(), most.clone());
            let (e2, done) = (events.clone(), done.clone());
            queue.run(fake_dialog(n, e, o, m), move |answer| {
                e2.lock().unwrap().push(format!("then {answer}"));
                done.send(answer).unwrap();
            });
        }
        let answers: Vec<usize> = (0..4).map(|_| finished.recv_timeout(Duration::from_secs(10)).unwrap()).collect();
        assert_eq!(answers, vec![0, 1, 2, 3]);
        assert_eq!(most.load(Ordering::SeqCst), 1, "two dialogs were open at once");
        let want: Vec<String> =
            (0..4).flat_map(|n| [format!("open {n}"), format!("close {n}"), format!("then {n}")]).collect();
        assert_eq!(*events.lock().unwrap(), want);
    }

    #[test]
    fn a_callback_that_panics_does_not_stop_the_queue() {
        let queue = Serial::new();
        let (done, finished) = mpsc::channel();
        queue.run(async { 1 }, |_| panic!("a callback went wrong"));
        queue.run(async { 2 }, move |n| done.send(n).unwrap());
        assert_eq!(finished.recv_timeout(Duration::from_secs(10)).unwrap(), 2);
    }

    #[test]
    fn a_callback_may_ask_for_another_dialog() {
        let queue = Arc::new(Serial::new());
        let (done, finished) = mpsc::channel();
        let again = queue.clone();
        queue.run(async { 1 }, move |_| again.run(async { 2 }, move |n| done.send(n).unwrap()));
        assert_eq!(finished.recv_timeout(Duration::from_secs(10)).unwrap(), 2);
    }
}
