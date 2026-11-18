//! The window, the menus and the life of the server, put together.
//!
//! Nothing in this file is reachable from a page. The shell registers no command, grants no capability, adds no
//! script to a window and never evaluates one; the webview is told where it may go and the rest is refused.

use std::path::{Path, PathBuf};
use std::sync::atomic::{AtomicBool, AtomicU32, Ordering};
use std::sync::{Arc, Mutex, MutexGuard};
use std::thread;
use std::time::Duration;

use tauri::webview::{DownloadEvent, NewWindowResponse};
use tauri::{AppHandle, Manager, RunEvent, Url, WebviewUrl, WebviewWindow, WebviewWindowBuilder};

use crate::deeplink::{self, Open};
use crate::downloads::{self, Entry, Gate, Table};
use crate::keychain::{self, Notice};
use crate::launch::{self, LaunchError, Running, SpawnConfig, StartRefused};
use crate::nav::{self, Decision};
use crate::{datadir, dialogs, filename, menu, updater};

const DOCS_URL: &str = "https://github.com/crueber/scriobh/tree/main/docs";
const MAIN: &str = "main-";
const ERROR: &str = "error-";
const MAX_TAIL_BYTES: usize = 4000;

/// What a development build may be told on its command line. A release build takes none of these: its server is
/// the one inside the app, its data directory is the system's, and its keychain entry is the app's.
#[derive(Default)]
struct Options {
    sidecar: Option<PathBuf>,
    data_dir: Option<PathBuf>,
    keychain_service: Option<String>,
    /// A second launch with `--press-restart` does what the error window's Restart does, `--press-quit` what the
    /// Quit menu item does, and `--close-window` closes the app's window, for the smoke script.
    press_restart: bool,
    press_quit: bool,
    close_window: bool,
    /// The switches that start an update unattended (`--update-now`, `--update-restart`, `--restart-now`).
    update: updater::Debug,
}

impl Options {
    #[cfg(debug_assertions)]
    fn from_args(args: &[String]) -> (Self, Vec<String>) {
        let mut o = Options::default();
        let mut rest = Vec::new();
        let mut it = args.iter();
        while let Some(a) = it.next() {
            match a.as_str() {
                "--sidecar" => o.sidecar = it.next().map(PathBuf::from),
                "--data-dir" => o.data_dir = it.next().map(PathBuf::from),
                "--keychain-service" => o.keychain_service = it.next().cloned(),
                "--press-restart" => o.press_restart = true,
                "--press-quit" => o.press_quit = true,
                "--close-window" => o.close_window = true,
                // Make one check now, whatever was answered; restart to a ready update unasked; restart the app unasked.
                "--update-now" => o.update.check_now = true,
                "--update-restart" => o.update.restart_when_ready = true,
                "--restart-now" => o.update.restart_now = true,
                "--auto-dialogs" => {
                    if let Some(dir) = it.next() {
                        dialogs::answer_automatically(PathBuf::from(dir));
                    }
                }
                _ => rest.push(a.clone()),
            }
        }
        // A development build never touches the real data directory or the real keychain entry by accident.
        if o.data_dir.is_none() {
            o.data_dir = Some(std::env::temp_dir().join("scriobh-desktop-dev"));
        }
        (o, rest)
    }

    #[cfg(not(debug_assertions))]
    fn from_args(args: &[String]) -> (Self, Vec<String>) {
        let o = Options {
            sidecar: None,
            data_dir: None,
            keychain_service: Some(keychain::SERVICE.to_string()),
            press_restart: false,
            press_quit: false,
            close_window: false,
            update: updater::Debug::default(),
        };
        (o, args.to_vec())
    }
}

#[derive(Clone, Copy, PartialEq, Eq, Debug)]
enum Phase {
    Starting,
    Running,
    Failed,
    Quitting,
}

struct Inner {
    phase: Phase,
    server: Option<Running>,
    port: Option<u16>,
    /// Something to open that arrived before the window existed.
    pending: Option<Open>,
    zoom: f64,
    /// Downloads the webview is still writing.
    downloads: Table,
}

struct Shell {
    opts: Options,
    inner: Mutex<Inner>,
    /// A question about an outside link is on screen; further ones are dropped until it is answered.
    asking: AtomicBool,
    /// A notice about downloads is on screen; further ones are dropped until it is dismissed.
    telling: AtomicBool,
    /// Downloads from the webview's go-ahead until their file is placed or thrown away.
    gate: Gate,
    windows: AtomicU32,
}

impl Shell {
    fn lock(&self) -> MutexGuard<'_, Inner> {
        self.inner.lock().unwrap_or_else(|p| p.into_inner())
    }
}

fn shell(app: &AppHandle) -> Arc<Shell> {
    app.state::<Arc<Shell>>().inner().clone()
}

fn windows_with(app: &AppHandle, prefix: &str) -> Vec<WebviewWindow> {
    app.webview_windows().into_iter().filter(|(label, _)| label.starts_with(prefix)).map(|(_, w)| w).collect()
}

fn main_window(app: &AppHandle) -> Option<WebviewWindow> {
    windows_with(app, MAIN).into_iter().next()
}

pub fn run() {
    // `std::env::args()` panics on an argument that is not valid Unicode (a file handed over by the system can be
    // named anything on Linux, and with a lone surrogate on Windows). Such an argument is left out: the shell
    // reads text only, and what it cannot read it cannot open.
    let args: Vec<String> = std::env::args_os()
        .skip(1)
        .filter_map(|a| {
            let text = a.into_string().ok();
            if text.is_none() {
                dialogs::note("a launch argument that is not valid Unicode was ignored");
            }
            text
        })
        .collect();
    let (opts, rest) = Options::from_args(&args);
    let first = deeplink::first_open(&rest);
    let update_state = updater::State::new(opts.update);
    let state = Arc::new(Shell {
        opts,
        inner: Mutex::new(Inner {
            phase: Phase::Starting,
            server: None,
            port: None,
            pending: Some(first).filter(|o| *o != Open::Ignore),
            zoom: 1.0,
            downloads: Table::default(),
        }),
        asking: AtomicBool::new(false),
        telling: AtomicBool::new(false),
        gate: Gate::new(downloads::MAX_IN_FLIGHT),
        windows: AtomicU32::new(0),
    });

    let app = tauri::Builder::default()
        // First, so a second launch stops here: it hands its arguments to the running app and exits.
        .plugin(tauri_plugin_single_instance::init(|app, argv, _cwd| {
            focus(app);
            let (second, rest) = Options::from_args(argv.get(1..).unwrap_or(&[]));
            if second.press_restart {
                restart(app);
            }
            if second.press_quit {
                return app.exit(0);
            }
            if second.close_window {
                for w in windows_with(app, MAIN) {
                    let _ = w.close();
                }
                return;
            }
            open(app, deeplink::first_open(&rest));
        }))
        // Registered for what it does at install time (the scheme) and for nothing at run time: links arrive as
        // launch arguments or as `RunEvent::Opened` and are read by `deeplink`.
        .plugin(tauri_plugin_deep_link::init())
        // Registered so that the update feature can use it. It does nothing by itself: a check is made only by the
        // shell's own schedule after a yes, or by Check for Updates… (`updater.rs`).
        .plugin(tauri_plugin_updater::Builder::new().build())
        .manage(state)
        .manage(update_state)
        .menu(menu::build)
        .on_menu_event(on_menu)
        .setup(|app| {
            let handle = app.handle().clone();
            updater::init(&handle);
            clear_downloads(&handle);
            let starter = handle.clone();
            thread::spawn(move || start_server(&starter));
            listen_for_signals();
            thread::spawn(move || watch(&handle));
            Ok(())
        })
        .build(tauri::generate_context!())
        .expect("the shell's own configuration is valid");

    app.run(|app, event| match event {
        // Every way out ends here: the menu, the last window closing, the Dock, a restart for an update.
        RunEvent::Exit => {
            stop_server(app);
            clear_downloads(app);
        }
        #[cfg(target_os = "macos")]
        RunEvent::Opened { urls } => {
            focus(app);
            let urls: Vec<String> = urls.iter().map(|u| u.to_string()).collect();
            open(app, deeplink::first_open(&urls));
        }
        _ => {}
    });
}

// ---- the server ---------------------------------------------------------------------------------------------

/// The server program inside the app: next to the shell's own executable, where the bundler puts it.
fn sidecar_path() -> PathBuf {
    let name = if cfg!(windows) { "scriobh-desktop-server.exe" } else { "scriobh-desktop-server" };
    std::env::current_exe().ok().and_then(|p| p.parent().map(|d| d.join(name))).unwrap_or_else(|| PathBuf::from(name))
}

fn start_server(app: &AppHandle) {
    let shell = shell(app);
    {
        let mut inner = shell.lock();
        if inner.phase == Phase::Quitting || inner.server.is_some() {
            return;
        }
        inner.phase = Phase::Starting;
        inner.port = None;
    }
    let program = shell.opts.sidecar.clone().unwrap_or_else(sidecar_path);
    if !program.is_file() {
        return fail(app, "missing", String::new());
    }
    let Ok(token) = launch::new_token() else {
        return fail(app, "start", "The operating system gave no random numbers for the launch token.".into());
    };

    // What is in the data folder, looked at before anything is started or the keychain is asked. A folder that
    // cannot be looked at is an error, not a first run. Without a known data directory nothing can be said about
    // what is already there, so the keychain is left alone and the server uses its key file.
    let data_dir = shell.opts.data_dir.clone().or_else(datadir::system_data_dir);
    let state = data_dir.as_deref().map(datadir::state);
    let resolved = match (&shell.opts.keychain_service, state) {
        (Some(service), Some(state)) => {
            Some(keychain::resolve(&keychain::OsKeychain::new(service), state, keychain::generate_key))
        }
        _ if state == Some(datadir::DataState::Unreadable) => {
            Some(keychain::Resolved::stop(Notice::DataFolderUnreadable))
        }
        _ => None,
    };
    let notice = resolved.as_ref().and_then(|r| r.notice);
    // The server makes a key file of its own when it is given no key and finds none. Where that would put a second
    // key over an install that has data, nothing is started: the window says why, and Restart tries again.
    match notice {
        Some(Notice::KeychainBlocksStart) => return fail(app, "keychain", String::new()),
        Some(Notice::DataFolderUnreadable) => {
            let dir = data_dir.as_deref().map(|d| d.display().to_string()).unwrap_or_default();
            return fail(app, "datafolder", dir);
        }
        _ => {}
    }

    let mut cfg = SpawnConfig::new(program, token.clone());
    cfg.secret_key = resolved.and_then(|r| r.key);
    cfg.data_dir = shell.opts.data_dir.clone();
    cfg.echo_stderr = cfg!(debug_assertions);
    // The key and the token are held in buffers that are wiped when dropped. One copy of each stays with the
    // running server's handle for as long as the server runs, so that its last lines can be scrubbed of them
    // before they are shown. The plain copies made to build the child's environment, and the child's environment
    // itself, are not wiped.
    match launch::spawn(cfg) {
        Ok(mut running) => {
            let port = running.port;
            // The last look at the server before the window is sent its launch token: it has to be alive, and what
            // answers at the port has to prove, to a fresh challenge, that it holds the token. This is a few
            // requests to a socket on this computer, so it is made before the shell's state is locked.
            let looked = running.start_url(&token);
            let start = {
                let mut inner = shell.lock();
                if inner.phase == Phase::Quitting {
                    drop(inner);
                    // Asked to quit while it was starting: it is asked to stop, not killed in the middle of its
                    // migrations.
                    running.shutdown(launch::SHUTDOWN_GRACE);
                    return;
                }
                match looked {
                    Ok(start) => {
                        inner.server = Some(running);
                        inner.port = Some(port);
                        inner.phase = Phase::Running;
                        dialogs::note(&format!("server ready on port {port}"));
                        inner.zoom = 1.0;
                        start
                    }
                    Err(refused) => {
                        // Nothing was sent to the port. A server that is still there is stopped before the error is
                        // shown, and not while the shell's state is locked.
                        drop(inner);
                        let tail = running.tail_text();
                        let (kind, text) = refusal_text(&refused, &tail);
                        if refused != StartRefused::Gone {
                            running.shutdown(launch::SHUTDOWN_GRACE);
                        }
                        return fail(app, kind, text);
                    }
                }
            };
            if !Url::parse(&start).is_ok_and(|url| open_main_window(app, port, url).is_ok()) {
                return fail(app, "start", "The window could not be opened.".into());
            }
            for w in windows_with(app, ERROR) {
                let _ = w.destroy();
            }
            if let Some(n) = notice {
                tell_once(app, n);
            }
            let pending = shell.lock().pending.take();
            if let Some(o) = pending {
                open(app, o);
            }
            // The window is up: the update feature may now ask its one question and start its schedule (off this thread).
            updater::window_is_up(app);
        }
        Err(LaunchError::AlreadyRunning) => {
            // Another Scríobh server holds the data folder and it is not one this app started (a second copy of
            // the app is stopped earlier, by the single-instance check, and brings the first to the front).
            shell.lock().phase = Phase::Quitting;
            let handle = app.clone();
            dialogs::info_then(
                app,
                "Scríobh is already running",
                "Another Scríobh is using the data folder on this computer. Use that one, or quit it and open Scríobh again.",
                move || handle.exit(0),
            );
        }
        Err(LaunchError::Spawn(_)) => fail(app, "missing", String::new()),
        Err(LaunchError::Timeout { tail }) => fail(app, "timeout", tail),
        Err(LaunchError::NotTheServer { tail }) => fail(app, "notserver", tail),
        Err(LaunchError::Exited { code, tail }) => {
            let status = code
                .map(|c| format!("The server exited with status {c}."))
                .unwrap_or_else(|| "The server was stopped by a signal.".into());
            fail(app, "start", if tail.is_empty() { status } else { format!("{tail}\n{status}") })
        }
    }
}

/// The error window's kind and text for a server whose address was not given to the window. A server that had
/// exited keeps its last lines and says so; one that could not prove itself is said to be that and nothing else; one
/// that merely did not answer in time is not called another program.
pub(crate) fn refusal_text(refused: &StartRefused, tail: &str) -> (&'static str, String) {
    let with_tail = |said: &str| if tail.is_empty() { said.to_string() } else { format!("{tail}\n{said}") };
    match refused {
        StartRefused::Gone => ("start", with_tail("The server stopped as it started.")),
        StartRefused::NoAnswer => {
            ("start", with_tail("The server did not answer in time. Choose Restart to try again."))
        }
        StartRefused::NotTheServer => ("notserver", tail.to_string()),
    }
}

/// The path of the page every main window shows (there is normally one; every `main-*` window counts).
pub(crate) fn main_paths(app: &AppHandle) -> Vec<String> {
    windows_with(app, MAIN).iter().filter_map(|w| w.url().ok()).map(|u| u.path().to_string()).collect()
}

/// The AppImage file this process runs from: the runtime sets `APPIMAGE` to it. Read here and nowhere in the update code, which
/// reads no variable (an update source is the build's configuration and nothing else).
pub(crate) fn appimage_path() -> Option<PathBuf> {
    std::env::var_os("APPIMAGE").filter(|v| !v.is_empty()).map(PathBuf::from)
}

/// Whether the app is on its way out.
pub(crate) fn quitting(app: &AppHandle) -> bool {
    shell(app).lock().phase == Phase::Quitting
}

/// What the shell needs to make its own requests of the running server (the update's "save now"): its port and its key.
pub(crate) fn server_access(app: &AppHandle) -> Option<(u16, zeroize::Zeroizing<String>)> {
    shell(app).lock().server.as_ref().map(|s| s.shell_access())
}

/// Stops the server: asked first, killed after ten seconds. Safe to call more than once.
pub(crate) fn stop_server(app: &AppHandle) {
    let shell = shell(app);
    let server = {
        let mut inner = shell.lock();
        inner.phase = Phase::Quitting;
        inner.port = None;
        inner.server.take()
    };
    if let Some(mut server) = server {
        let outcome = server.shutdown(launch::SHUTDOWN_GRACE);
        dialogs::note(&format!("server stopped: {outcome:?}"));
    }
}

/// Set by SIGTERM, SIGINT or SIGHUP. The shell then leaves the ordinary way, which stops the server; without
/// this a `kill` of the shell would leave the server running with nothing to show it.
static ASKED_TO_STOP: AtomicBool = AtomicBool::new(false);

#[cfg(unix)]
fn listen_for_signals() {
    extern "C" fn on_signal(_: libc::c_int) {
        // Only this: storing to an atomic is safe inside a signal handler.
        ASKED_TO_STOP.store(true, Ordering::SeqCst);
    }
    let handler = on_signal as extern "C" fn(libc::c_int) as libc::sighandler_t;
    for sig in [libc::SIGTERM, libc::SIGINT, libc::SIGHUP] {
        // SAFETY: the handler is async-signal-safe and stays valid for the life of the process.
        unsafe {
            libc::signal(sig, handler);
        }
    }
}

#[cfg(not(unix))]
fn listen_for_signals() {}

/// Notices a server that stopped by itself and says so, instead of leaving a window that loads nothing; and
/// notices a request to stop.
fn watch(app: &AppHandle) {
    let shell = shell(app);
    loop {
        thread::sleep(Duration::from_millis(200));
        if ASKED_TO_STOP.load(Ordering::SeqCst) {
            dialogs::note("asked to stop by a signal");
            app.exit(0);
            return;
        }
        let died = {
            let mut inner = shell.lock();
            match inner.phase {
                Phase::Quitting => return,
                Phase::Running => match inner.server.as_mut().and_then(|s| s.try_exit()) {
                    Some(status) => {
                        // Give the reader threads a moment to collect the last lines.
                        thread::sleep(Duration::from_millis(100));
                        let tail = inner.server.as_ref().map(|s| s.tail_text()).unwrap_or_default();
                        inner.server = None;
                        inner.port = None;
                        inner.phase = Phase::Failed;
                        let status = status
                            .code()
                            .map(|c| format!("The server exited with status {c}."))
                            .unwrap_or_else(|| "The server was stopped by a signal.".into());
                        Some(if tail.is_empty() { status } else { format!("{tail}\n{status}") })
                    }
                    None => None,
                },
                _ => None,
            }
        };
        if let Some(tail) = died {
            dialogs::note("the server stopped by itself");
            show_error(app, "stopped", &tail);
        }
    }
}

fn fail(app: &AppHandle, kind: &str, tail: String) {
    let server = {
        let shell = shell(app);
        let mut inner = shell.lock();
        if inner.phase == Phase::Quitting {
            return;
        }
        inner.phase = Phase::Failed;
        inner.port = None;
        inner.server.take()
    };
    // A server that is still up (the window could not be opened) is asked to stop, not killed, and not while the
    // lock is held: the webview's callbacks take it on the main thread.
    if let Some(mut server) = server {
        server.shutdown(launch::SHUTDOWN_GRACE);
    }
    dialogs::note(&format!("the server did not start: {kind}"));
    show_error(app, kind, &tail);
}

// ---- the windows --------------------------------------------------------------------------------------------

fn open_main_window(app: &AppHandle, port: u16, start: Url) -> tauri::Result<()> {
    let label = format!("{MAIN}{}", shell(app).windows.fetch_add(1, Ordering::SeqCst));
    let (nav_app, new_app, dl_app) = (app.clone(), app.clone(), app.clone());
    WebviewWindowBuilder::new(app, label, WebviewUrl::External(start))
        .title(dialogs::APP_NAME)
        .inner_size(1180.0, 820.0)
        .min_inner_size(390.0, 480.0)
        // The page's own drag and drop: with the shell's handler on, a dropped file never reaches the page.
        .disable_drag_drop_handler()
        // Zoom is the View menu's; the webview's own hot keys would change it behind the menu's back.
        .zoom_hotkeys_enabled(false)
        .on_navigation(move |url| match nav::decide(url, port) {
            Decision::Allow => true,
            Decision::OfferExternal => {
                offer_outside(&nav_app, url.clone());
                // Written after the offer returned: the question itself is put later, from the main loop.
                dialogs::note(&format!(
                    "navigation refused, offered outside: {} {}",
                    url.scheme(),
                    nav::display_host(url)
                ));
                false
            }
            Decision::Block => {
                dialogs::note(&format!("navigation refused: scheme {}", url.scheme()));
                false
            }
        })
        .on_new_window(move |url, _features| {
            // No second window, ever. A link that wanted one is treated as a link.
            dialogs::note(&format!("new window refused: scheme {}", url.scheme()));
            match nav::decide(&url, port) {
                Decision::Allow => {
                    let app = new_app.clone();
                    dialogs::later(&new_app, move || {
                        if let Some(w) = main_window(&app) {
                            let _ = w.navigate(url);
                        }
                    });
                }
                Decision::OfferExternal => offer_outside(&new_app, url),
                Decision::Block => {}
            }
            NewWindowResponse::Deny
        })
        .on_download(move |_webview, event| on_download(&dl_app, port, event))
        .on_document_title_changed(follow_title)
        .build()?;
    Ok(())
}

/// A page's title as a window title: printable, one line, not long.
fn window_title(page: &str) -> String {
    let clean: String = page.chars().filter(|c| !c.is_control() && !filename::is_deceptive(*c)).take(120).collect();
    let clean = clean.trim();
    if clean.is_empty() || clean == dialogs::APP_NAME {
        dialogs::APP_NAME.to_string()
    } else if clean.ends_with(dialogs::APP_NAME) {
        clean.to_string()
    } else {
        format!("{clean} · {}", dialogs::APP_NAME)
    }
}

/// The window is named after the page it shows, as a browser tab is.
fn follow_title(window: WebviewWindow, title: String) {
    dialogs::note(&format!("page title: {}", window_title(&title)));
    let _ = window.set_title(&window_title(&title));
}

/// Asks whether to open an outside address in the system browser. The navigation that led here was already
/// refused; this returns at once and the question is put from the main loop. One question at a time: a page that
/// fires a hundred gets one.
pub(crate) fn offer_outside(app: &AppHandle, url: Url) {
    let shell = shell(app);
    if shell.asking.swap(true, Ordering::SeqCst) {
        return;
    }
    // What the question shows is what is opened: a mail link loses the parts that ask a mail app to attach a file.
    let url = nav::without_mail_attachments(&url);
    let what = if url.scheme() == "mailto" {
        "Write to this address in your mail app?"
    } else {
        "Open this link in your browser?"
    };
    let body = format!("{}\n\nIt opens outside Scríobh, at {}.", nav::display(&url), nav::display_host(&url));
    dialogs::ask(app, what, &body, "Open", "Cancel", move |yes| {
        shell.asking.store(false, Ordering::SeqCst);
        if yes && nav::decide(&url, 0) == Decision::OfferExternal {
            let _ = tauri_plugin_opener::open_url(url.as_str(), None::<&str>);
        }
    });
}

fn percent_encode(text: &str) -> String {
    let mut out = String::with_capacity(text.len() * 3 / 2);
    for b in text.bytes() {
        if b.is_ascii_alphanumeric() || matches!(b, b'-' | b'.' | b'_' | b'~') {
            out.push(b as char);
        } else {
            out.push_str(&format!("%{b:02X}"));
        }
    }
    out
}

fn last_bytes(text: &str, max: usize) -> &str {
    if text.len() <= max {
        return text;
    }
    let mut start = text.len() - max;
    while !text.is_char_boundary(start) {
        start += 1;
    }
    &text[start..]
}

/// True for the shell's own page: `tauri://localhost/…`, or `http(s)://tauri.localhost/…` where the platform
/// serves it that way.
fn is_shell_page(url: &Url) -> bool {
    match url.scheme() {
        "tauri" => url.host_str() == Some("localhost"),
        "http" | "https" => url.host_str() == Some("tauri.localhost"),
        _ => false,
    }
}

/// Shows the error window and closes the app's. The page is the shell's own bundled file; the reason and the
/// server's last lines travel in the address fragment and are put on the page as text.
fn show_error(app: &AppHandle, kind: &str, tail: &str) {
    let label = format!("{ERROR}{}", shell(app).windows.fetch_add(1, Ordering::SeqCst));
    let page =
        format!("error.html#kind={}&tail={}", percent_encode(kind), percent_encode(last_bytes(tail, MAX_TAIL_BYTES)));
    let nav_app = app.clone();
    let built = WebviewWindowBuilder::new(app, label, WebviewUrl::App(page.into()))
        .title(dialogs::APP_NAME)
        .inner_size(640.0, 560.0)
        .min_inner_size(390.0, 420.0)
        .zoom_hotkeys_enabled(false)
        .on_navigation(move |url| {
            if !is_shell_page(url) {
                return false;
            }
            match url.path() {
                "/error.html" => true,
                "/action/restart" => {
                    restart(&nav_app);
                    false
                }
                "/action/quit" => {
                    nav_app.exit(0);
                    false
                }
                _ => false,
            }
        })
        .on_new_window(|_, _| NewWindowResponse::Deny)
        .on_document_title_changed(|window, title| {
            dialogs::note(&format!("error page says: {}", window_title(&title)));
            let _ = window.set_title(&window_title(&title));
        })
        .build();
    if built.is_err() {
        // No window can be shown at all; say it the plainest way there is and leave.
        dialogs::info(
            app,
            "Scríobh has stopped",
            "The server is not running and the window could not be opened. Open Scríobh again.",
        );
    }
    for w in windows_with(app, MAIN) {
        let _ = w.destroy();
    }
}

fn restart(app: &AppHandle) {
    {
        let shell = shell(app);
        let mut inner = shell.lock();
        if inner.phase != Phase::Failed {
            return; // a second click, or a restart already under way
        }
        inner.phase = Phase::Starting;
    }
    let app = app.clone();
    thread::spawn(move || start_server(&app));
}

fn focus(app: &AppHandle) {
    let w = main_window(app).or_else(|| windows_with(app, ERROR).into_iter().next());
    if let Some(w) = w {
        let _ = w.unminimize();
        let _ = w.show();
        let _ = w.set_focus();
    }
}

// ---- opening things -----------------------------------------------------------------------------------------

/// Sends the app's webview to one of the app's own pages.
fn go(app: &AppHandle, path: &str) {
    let port = shell(app).lock().port;
    if let (Some(port), Some(w)) = (port, main_window(app)) {
        let _ = w.navigate(nav::app_url(port, path));
        let _ = w.set_focus();
    }
}

fn open(app: &AppHandle, what: Open) {
    let shell = shell(app);
    {
        let mut inner = shell.lock();
        if inner.phase != Phase::Running || main_window(app).is_none() {
            if what != Open::Ignore && inner.phase == Phase::Starting {
                inner.pending = Some(what);
            }
            return;
        }
    }
    match what {
        Open::Route(path) => {
            dialogs::note(&format!("opening {path}"));
            go(app, &path)
        }
        Open::ImportNotice(name) => {
            go(app, "/import");
            dialogs::info(
                app,
                &format!("Choose {name} on the import page"),
                "Scríobh does not read a file the system hands it. The import page is open: choose the file there, and it is imported the same way as in a browser.",
            );
        }
        Open::BadLink => dialogs::info(
            app,
            "That link does not open anything",
            "The link is not one Scríobh understands, so nothing was opened.",
        ),
        Open::Ignore => {}
    }
}

fn tell_once(app: &AppHandle, notice: Notice) {
    let name = match notice {
        Notice::KeychainUnavailable => "notice-keychain-unavailable",
        Notice::KeyMissing => "notice-key-missing",
        Notice::KeyFileOverKeychain => "notice-key-file-over-keychain",
        // These stop the launch; they are shown in the error window, every time, and never get here.
        Notice::KeychainBlocksStart | Notice::DataFolderUnreadable => return,
    };
    // An empty marker file in the shell's own configuration folder remembers that this was said.
    if let Some(dir) = own_dir(app, false) {
        let marker = dir.join(name);
        if marker.exists() {
            return;
        }
        let _ = std::fs::create_dir_all(&dir);
        let _ = std::fs::write(&marker, b"");
    }
    match notice {
        Notice::KeychainUnavailable => dialogs::info(
            app,
            "The system keychain could not be used",
            "Scríobh keeps the key that protects saved AI and Git credentials in a file in its data folder instead. Everything works; keep that folder private. This is said once.",
        ),
        Notice::KeyFileOverKeychain => dialogs::info(
            app,
            "Two keys were found",
            "The data folder has its own key file (secret.key) and the system keychain holds a key too. Scríobh uses the key file. Credentials saved under the keychain's key cannot be opened until you enter them again in Settings. Nothing was deleted. This is said once.",
        ),
        Notice::KeychainBlocksStart | Notice::DataFolderUnreadable => {}
        Notice::KeyMissing => dialogs::info(
            app,
            "Saved credentials need to be entered again",
            "The data folder holds your writing but the key that protected saved AI and Git credentials was not found, in the keychain or in the folder. Your writing is not affected. Enter those credentials again in Settings when you need them.",
        ),
    }
}

// ---- downloads ----------------------------------------------------------------------------------------------

/// The shell's own folder for the little it keeps: the "said once" markers (configuration) and downloads on
/// their way to where the person wants them (cache). A development build keeps both in the temporary folder so
/// that it leaves nothing in the places the installed app uses.
pub(crate) fn own_dir(app: &AppHandle, cache: bool) -> Option<PathBuf> {
    if cfg!(debug_assertions) {
        return Some(std::env::temp_dir().join("scriobh-desktop-dev-shell").join(if cache {
            "cache"
        } else {
            "config"
        }));
    }
    if cache {
        app.path().app_cache_dir().ok()
    } else if cfg!(windows) {
        // Not the roaming profile: the markers (what was said once, the answer about updates, when a check was last tried) belong to
        // this computer, and a roaming profile would carry a "yes" to another one.
        app.path().app_local_data_dir().ok()
    } else {
        app.path().app_config_dir().ok()
    }
}

fn downloads_dir(app: &AppHandle) -> Option<PathBuf> {
    own_dir(app, true).map(|d| d.join("downloads"))
}

fn clear_downloads(app: &AppHandle) {
    if let Some(dir) = downloads_dir(app) {
        let _ = std::fs::remove_dir_all(dir);
    }
}

fn private_dir(path: &Path) -> std::io::Result<()> {
    let mut b = std::fs::DirBuilder::new();
    b.recursive(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        b.mode(0o700);
    }
    b.create(path)
}

/// A download the webview is about to make, or has finished.
///
/// The webview writes the bytes itself, into a private folder of the shell's cache, under the server's suggested
/// name made safe. When it has finished, the save dialog asks where the file should go and the shell moves that
/// one file there. The handler never waits for a person: it answers the webview at once.
fn on_download(app: &AppHandle, port: u16, event: DownloadEvent<'_>) -> bool {
    let shell = shell(app);
    match event {
        DownloadEvent::Requested { url, destination } => {
            if !nav::is_app_download(&url, port) {
                dialogs::note(&format!("download refused: scheme {}", url.scheme()));
                return false;
            }
            let suggested = destination.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default();
            let name = filename::sanitise(&suggested);
            let Some(base) = downloads_dir(app) else { return false };
            let mut id = [0u8; 8];
            if getrandom::fill(&mut id).is_err() {
                return false;
            }
            let dir = base.join(launch::hex(&id).as_str());
            if private_dir(&dir).is_err() {
                return false;
            }
            let temp = dir.join(&name);
            let key = url.to_string();
            let mut inner = shell.lock();
            if inner.downloads.has_url(&key) {
                // Two downloads of one address cannot be told apart when they finish.
                drop(inner);
                let _ = std::fs::remove_dir_all(&dir);
                dialogs::note("download refused: the same address is already being downloaded");
                return false;
            }
            let Some(slot) = shell.gate.enter() else {
                drop(inner);
                let _ = std::fs::remove_dir_all(&dir);
                dialogs::note("download refused: too many in flight");
                tell_one_at_a_time(
                    app,
                    &shell,
                    "Too many downloads at once",
                    "Scríobh is still saving earlier downloads. Answer or cancel those save dialogs, then download again.",
                );
                return false;
            };
            dialogs::note(&format!("download started: suggested {suggested:?}, kept as {name:?}"));
            inner.downloads.add(Entry { url: key, temp: temp.clone(), name, slot });
            *destination = temp;
            true
        }
        DownloadEvent::Finished { url, path, success } => {
            let pending = shell.lock().downloads.take(url.as_str(), path.as_deref());
            let Some(Entry { temp, name, slot, .. }) = pending else { return true };
            let discard = move |temp: &Path| {
                if let Some(dir) = temp.parent() {
                    let _ = std::fs::remove_dir_all(dir);
                }
            };
            // The place in the gate is given back when `slot` is dropped: here, or after the dialog's answer.
            if !success || !temp.is_file() {
                discard(&temp);
                // Through the same one-at-a-time notice as a refusal: a page that starts downloads that fail must not
                // queue a dialog for each.
                tell_one_at_a_time(
                    app,
                    &shell,
                    "The download did not finish",
                    "Nothing was saved. Try again from the page.",
                );
                return true;
            }
            let handle = app.clone();
            dialogs::save(app, app.path().download_dir().ok(), &name, move |chosen| {
                thread::spawn(move || {
                    if let Some(dest) = chosen {
                        let moved = move_file(&temp, &dest);
                        dialogs::note(&format!("download placed: {}", moved.is_ok()));
                        if moved.is_err() {
                            dialogs::info(
                                &handle,
                                "The file could not be saved there",
                                "Nothing was saved. Try again and choose another folder.",
                            );
                        }
                    }
                    discard(&temp);
                    drop(slot);
                });
            });
            true
        }
        _ => true,
    }
}

/// A notice about downloads, one at a time: while one is on screen, further ones are dropped, however many are
/// asked for.
fn tell_one_at_a_time(app: &AppHandle, shell: &Arc<Shell>, title: &str, body: &str) {
    if shell.telling.swap(true, Ordering::SeqCst) {
        return;
    }
    let shell = shell.clone();
    dialogs::info_then(app, title, body, move || shell.telling.store(false, Ordering::SeqCst));
}

fn move_file(from: &Path, to: &Path) -> std::io::Result<()> {
    if std::fs::rename(from, to).is_ok() {
        return Ok(());
    }
    // Another volume: copy, then remove the original.
    std::fs::copy(from, to)?;
    std::fs::remove_file(from)
}

// ---- the menus ----------------------------------------------------------------------------------------------

fn on_menu(app: &AppHandle, event: tauri::menu::MenuEvent) {
    let id = event.id().as_ref();
    match id {
        menu::QUIT => app.exit(0),
        menu::LIBRARY => go(app, "/"),
        menu::IMPORT => go(app, "/import"),
        menu::SETTINGS => go(app, "/settings"),
        menu::EXPORT => {
            let port = shell(app).lock().port;
            if let (Some(port), Some(w)) = (port, main_window(app)) {
                let path = w.url().map(|u| nav::export_path(&u, port)).unwrap_or_else(|_| "/".into());
                go(app, &path);
            }
        }
        menu::PRINT => {
            if let Some(w) = main_window(app) {
                let _ = w.print();
            }
        }
        menu::RELOAD => {
            if let Some(w) = main_window(app) {
                let _ = w.reload();
            }
        }
        menu::ZOOM_IN | menu::ZOOM_OUT | menu::ZOOM_RESET => {
            let shell = shell(app);
            let zoom = {
                let mut inner = shell.lock();
                inner.zoom = next_zoom(inner.zoom, id);
                inner.zoom
            };
            if let Some(w) = main_window(app) {
                let _ = w.set_zoom(zoom);
            }
        }
        menu::FULLSCREEN => {
            if let Some(w) = main_window(app) {
                let _ = w.set_fullscreen(!w.is_fullscreen().unwrap_or(false));
            }
        }
        menu::DOCS => {
            if let Ok(url) = Url::parse(DOCS_URL) {
                offer_outside(app, url);
            }
        }
        menu::DATA_FOLDER => show_data_folder(app),
        menu::UPDATES | menu::UPDATES_APP => updater::on_updates_item(app),
        menu::AUTO_UPDATES | menu::AUTO_UPDATES_APP => updater::on_automatic_item(app),
        _ => {}
    }
}

fn next_zoom(now: f64, id: &str) -> f64 {
    let z = match id {
        menu::ZOOM_IN => now + 0.1,
        menu::ZOOM_OUT => now - 0.1,
        _ => 1.0,
    };
    ((z * 10.0).round() / 10.0).clamp(0.5, 3.0)
}

/// Shows the data folder in the file manager: the folder itself, selected in the folder that holds it. The path
/// is the one the shell worked out from the operating system's rule, not something a page or the server said.
fn show_data_folder(app: &AppHandle) {
    let dir = shell(app).opts.data_dir.clone().or_else(datadir::system_data_dir);
    match dir {
        Some(dir) if dir.is_dir() => {
            let _ = tauri_plugin_opener::reveal_item_in_dir(&dir);
        }
        _ => dialogs::info(app, "The data folder is not there yet", "Scríobh makes it the first time it starts."),
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn a_refused_address_is_explained_by_what_happened() {
        let (kind, text) = refusal_text(&StartRefused::NotTheServer, "last words");
        assert_eq!((kind, text.as_str()), ("notserver", "last words"));
        let (kind, text) = refusal_text(&StartRefused::Gone, "");
        assert_eq!((kind, text.as_str()), ("start", "The server stopped as it started."));
        let (kind, text) = refusal_text(&StartRefused::Gone, "migration failed");
        assert!(kind == "start" && text.starts_with("migration failed\n"), "{text}");
        // A server that was slow is not called another program, and is not shown as the "notserver" window.
        let (kind, text) = refusal_text(&StartRefused::NoAnswer, "");
        assert!(kind == "start" && text.contains("did not answer"), "{text}");
        // Every kind the shell can ask for has its words in the error page.
        let page = include_str!("../../ui/error.js");
        for refused in [StartRefused::Gone, StartRefused::NoAnswer, StartRefused::NotTheServer] {
            let (kind, _) = refusal_text(&refused, "");
            assert!(page.contains(&format!("    {kind}: [")), "error.js has no text for {kind}");
        }
    }

    #[test]
    fn zoom_moves_in_tenths_and_stays_in_range() {
        assert_eq!(next_zoom(1.0, menu::ZOOM_IN), 1.1);
        assert_eq!(next_zoom(1.1, menu::ZOOM_OUT), 1.0);
        assert_eq!(next_zoom(2.3, menu::ZOOM_RESET), 1.0);
        let mut z = 1.0;
        for _ in 0..100 {
            z = next_zoom(z, menu::ZOOM_IN);
        }
        assert_eq!(z, 3.0);
        for _ in 0..100 {
            z = next_zoom(z, menu::ZOOM_OUT);
        }
        assert_eq!(z, 0.5);
    }

    #[test]
    fn a_page_title_becomes_a_plain_window_title() {
        assert_eq!(window_title("Chapter 1"), "Chapter 1 · Scríobh");
        assert_eq!(window_title("Library · Scríobh"), "Library · Scríobh");
        assert_eq!(window_title(""), "Scríobh");
        assert_eq!(window_title("  \n\t "), "Scríobh");
        assert_eq!(window_title("a\u{202E}b\nc\u{7}"), "abc · Scríobh");
        assert!(window_title(&"x".repeat(1000)).chars().count() <= 130);
    }

    #[test]
    fn the_error_address_carries_text_only() {
        let hostile = "<script>alert(1)</script>&kind=x#y\n\"'%é";
        let enc = percent_encode(hostile);
        assert!(
            enc.bytes().all(|b| b.is_ascii_alphanumeric() || matches!(b, b'-' | b'.' | b'_' | b'~' | b'%')),
            "{enc}"
        );
        assert!(!enc.contains('&') && !enc.contains('#') && !enc.contains('<'));
    }

    #[test]
    fn the_tail_is_cut_from_the_front_on_a_character_boundary() {
        let text = format!("{}{}", "é".repeat(3000), "the end");
        let cut = last_bytes(&text, MAX_TAIL_BYTES);
        assert!(cut.len() <= MAX_TAIL_BYTES);
        assert!(cut.ends_with("the end"));
        assert_eq!(last_bytes("short", 100), "short");
    }

    #[test]
    fn only_the_shells_own_origin_counts_as_its_page() {
        let u = |s: &str| Url::parse(s).unwrap();
        assert!(is_shell_page(&u("tauri://localhost/error.html#kind=start")));
        assert!(is_shell_page(&u("http://tauri.localhost/action/restart")));
        for s in [
            "http://127.0.0.1:4000/action/quit",
            "https://example.com/action/restart",
            "tauri://example.com/action/quit",
            "http://localhost/action/quit",
            "file:///action/quit",
        ] {
            assert!(!is_shell_page(&u(s)), "{s}");
        }
    }

    #[cfg(debug_assertions)]
    #[test]
    fn a_development_build_never_defaults_to_the_real_data_folder_or_keychain() {
        let (o, rest) = Options::from_args(&["scriobh://open/import".to_string()]);
        assert!(o.data_dir.is_some_and(|d| d.starts_with(std::env::temp_dir())));
        assert!(o.keychain_service.is_none());
        assert_eq!(rest, ["scriobh://open/import"]);
        let args: Vec<String> =
            ["--data-dir", "/tmp/x", "--sidecar", "/tmp/s", "book.epub"].iter().map(|s| s.to_string()).collect();
        let (o, rest) = Options::from_args(&args);
        assert_eq!(o.data_dir, Some(PathBuf::from("/tmp/x")));
        assert_eq!(o.sidecar, Some(PathBuf::from("/tmp/s")));
        assert_eq!(rest, ["book.epub"]);
    }
}
