//! Updates, by the person's leave (`docs/UPDATES.md`).
//!
//! Nothing here looks for an update until the person has said yes, once, to one question (or clicks Check for Updates…,
//! which asks first), and then about once a day, quietly: one HTTPS request for a small JSON file, with nothing in it
//! about the person, the computer or the version. A newer package is downloaded in the background, checked against the
//! key built into the app, and kept. The menu then says Restart to Update; nothing is installed until the person
//! chooses it, and then in this order: the open pages are asked to save, the server is stopped cleanly, the package is
//! installed, the app starts again. This module takes no focus, shows no dialog and moves nothing on screen on its own
//! except the first question, once (invariant 8: the writing surface is sacred).
//!
//! Which packages install and which only say that a newer version exists (a `.deb`) is decided by `update_policy`, as
//! pure functions; the call that installs takes a proof that they decided "install". The update source is the build's
//! configuration and nothing else: no switch, variable or file can point a release build somewhere else.

use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{mpsc, Arc, Mutex, MutexGuard};
use std::thread;
use std::time::{Duration, Instant, SystemTime};

use tauri::{AppHandle, Manager};
use tauri_plugin_updater::{Update, UpdaterExt};

use crate::update_cache::{Cache, Cached};
use crate::update_net::{self, DownloadFailure};
use crate::update_policy::{
    self as policy, Bundle, Consent, InstallPermit, Kind, Markers, Os, Phase, SaveOutcome, ScheduleEnv, Why,
};
use crate::{app, dialogs, launch, menu};

/// Who asked for a check: the schedule (which a switch turned off must stop, even in flight), the person's click (which asked for
/// this one check and download whatever the switch says), or a development build's switch.
#[derive(Clone, Copy, PartialEq, Eq)]
enum Origin {
    Background,
    Manual,
    Debug,
}

/// A result that comes later than this after the person's last act is not put in a dialog: it would land on whatever they have
/// started doing since (typing, perhaps). The menu says where an update stands; this is for answers to a click, made at once.
const SOON: Duration = Duration::from_secs(3);

/// Shows the dialog if it is soon enough, and says whether it did.
fn say_if_soon(app: &AppHandle, since: Instant, (title, body): (String, String)) -> bool {
    if since.elapsed() <= SOON {
        dialogs::info(app, &title, &body);
        true
    } else {
        dialogs::note(&format!("not shown, it came too late after the click: {title}"));
        false
    }
}

/// What a development build may be told on its command line (a release build takes none of these).
#[derive(Clone, Copy, Default)]
pub struct Debug {
    /// Make one check now, whatever the person has answered (the unattended test of an update).
    pub check_now: bool,
    /// When an update is ready, restart to it without asking.
    pub restart_when_ready: bool,
    /// Restart the app a few seconds after its window is up, to see that a restart is not refused by the single-instance check.
    pub restart_now: bool,
}

pub struct State {
    phase: Mutex<Phase>,
    /// A check or a download is under way.
    busy: AtomicBool,
    /// Restart to Update has been chosen and is under way.
    restarting: AtomicBool,
    /// `window_is_up` has run.
    started: AtomicBool,
    /// The version whose download failed and was said, so that it is said once and not at every check.
    said_failed: Mutex<Option<String>>,
    debug: Debug,
}

impl State {
    pub fn new(debug: Debug) -> Self {
        State {
            phase: Mutex::new(Phase::Idle),
            busy: AtomicBool::new(false),
            restarting: AtomicBool::new(false),
            started: AtomicBool::new(false),
            said_failed: Mutex::new(None),
            debug,
        }
    }

    fn phase(&self) -> MutexGuard<'_, Phase> {
        self.phase.lock().unwrap_or_else(|p| p.into_inner())
    }
}

fn state(app: &AppHandle) -> tauri::State<'_, State> {
    app.state::<State>()
}

/// Gives the busy flag back when a check or a download ends, however it ends.
struct Busy<'a>(&'a AtomicBool);

impl Drop for Busy<'_> {
    fn drop(&mut self) {
        self.0.store(false, Ordering::SeqCst);
    }
}

/// The host this build checks, or `None`: only a release build for a tag has an update source.
fn source(app: &AppHandle) -> Option<String> {
    policy::update_source(app.config().plugins.0.get("updater"))
}

fn first_endpoint(app: &AppHandle) -> Option<String> {
    app.config().plugins.0.get("updater")?.get("endpoints")?.as_array()?.first()?.as_str().map(str::to_string)
}

fn markers(app: &AppHandle) -> Option<Markers> {
    app::own_dir(app, false).map(Markers::new)
}

fn cache(app: &AppHandle) -> Option<Cache> {
    app::own_dir(app, true).map(|d| Cache::new(d.join("update")))
}

fn consent(app: &AppHandle) -> Consent {
    markers(app).map(|m| m.consent()).unwrap_or(Consent::Unasked)
}

fn set_phase(app: &AppHandle, phase: Phase) {
    let (text, enabled) = policy::label(&phase);
    *state(app).phase() = phase;
    menu::set_update_item(&text, enabled);
}

/// Called once when the app is set up: the menu says what this build can do.
pub fn init(app: &AppHandle) {
    let has_source = source(app).is_some();
    menu::set_automatic_item(has_source && consent(app) == Consent::Yes, has_source);
    menu::set_update_item(&policy::label(&Phase::Idle).0, true);
}

/// Called when the main window is up (and only the first time). Off the start-up path: the server and the window come
/// first. It picks up a package kept from an earlier run and starts the schedule (nothing there is a request to the network
/// until the person has said yes), and, on a thread of its own, says what must be said and asks the one question, but only
/// when the person is on a page where nobody is typing (`wait_until_calm`).
pub fn window_is_up(app: &AppHandle) {
    if state(app).started.swap(true, Ordering::SeqCst) {
        return;
    }
    let handle = app.clone();
    thread::spawn(move || {
        let app = handle;
        let debug = state(&app).debug;
        if debug.restart_now {
            thread::sleep(Duration::from_secs(4));
            dialogs::note("restarting the app, as asked");
            app.restart();
        }
        let dialogs_app = app.clone();
        thread::spawn(move || say_what_is_due(&dialogs_app));
        if source(&app).is_none() {
            return;
        }
        // A package kept from an earlier run: checked again against the key before it is called ready.
        restore_kept(&app);
        if debug.check_now {
            tauri::async_runtime::block_on(background(&app, Origin::Debug));
            if debug.restart_when_ready && matches!(*state(&app).phase(), Phase::Ready(_)) {
                restart_to_update(&app, true);
            }
        }
        schedule(&app);
    });
}

/// The notice of an install that failed last time, and the first question, once the person is on a calm page.
fn say_what_is_due(app: &AppHandle) {
    let failure_due = markers(app).is_some_and(|m| m.failure_pending());
    let host = source(app);
    let question_due = host.is_some() && consent(app) == Consent::Unasked && markers(app).is_some();
    if !failure_due && !question_due {
        return;
    }
    if !wait_until_calm(app) {
        return;
    }
    if let Some(version) = markers(app).and_then(|m| m.take_failure()) {
        let page = first_endpoint(app).and_then(|e| policy::releases_page(&e));
        let (title, body) = policy::install_failed(&version, page.as_deref());
        dialogs::info(app, &title, &body);
    }
    let Some(host) = host else { return };
    if consent(app) != Consent::Unasked {
        return;
    }
    let (title, body) = policy::first_run_question(&host);
    let handle = app.clone();
    // "Only When I Ask" is the first button, the one a stray Return presses, and closing the dialog is that answer too.
    dialogs::ask_safe_first(app, &title, &body, "Only When I Ask", "Check Automatically", move |yes| {
        let kept = markers(&handle).map(|m| m.answer(yes).is_ok());
        // What is reported is what was kept, read again.
        menu::set_automatic_item(consent(&handle) == Consent::Yes, true);
        if kept != Some(true) {
            dialogs::note("the answer about updates could not be kept; nothing is checked");
        }
    });
}

/// Waits until every main window shows a page with no place to type, and has for two looks in a row, a second apart. A stray key
/// pressed while a chapter is open cannot answer a question that was not there a moment ago. False when the app is leaving.
fn wait_until_calm(app: &AppHandle) -> bool {
    let mut calm_looks = 0;
    loop {
        thread::sleep(Duration::from_secs(1));
        if app::quitting(app) {
            return false;
        }
        if policy::all_calm(&app::main_paths(app)) {
            calm_looks += 1;
            if calm_looks >= 2 {
                return true;
            }
        } else {
            calm_looks = 0;
        }
    }
}

fn restore_kept(app: &AppHandle) {
    let Some(cache) = cache(app) else { return };
    match cache.load().and_then(|c| restore(app, &c)) {
        Some(update) if policy::kind(Os::current(), Bundle::current()) == Kind::Install => {
            set_phase(app, Phase::Ready(update.version.clone()));
        }
        _ => cache.clear(),
    }
}

/// An update rebuilt from the kept answer and checked, with the kept bytes, against the key built into the app and the
/// version the signature records (the plugin requires a signed version for a restored update).
fn restore(app: &AppHandle, kept: &Cached) -> Option<Update> {
    let update = app.updater().ok()?.restore_update(kept.answer.clone()).ok()??;
    update.verify(&kept.bytes).ok()?;
    Some(update)
}

// ---- the schedule -------------------------------------------------------------------------------------------

struct Real {
    app: AppHandle,
    markers: Markers,
}

impl ScheduleEnv for Real {
    fn consent(&self) -> Consent {
        self.markers.consent()
    }
    fn last_check(&self) -> Option<SystemTime> {
        self.markers.last_check()
    }
    fn record_attempt(&mut self, at: SystemTime) {
        let _ = self.markers.record_attempt(at);
    }
    fn now(&self) -> SystemTime {
        SystemTime::now()
    }
    fn wait(&mut self, d: Duration) -> bool {
        thread::sleep(d);
        !app::quitting(&self.app)
    }
    fn check(&mut self) {
        tauri::async_runtime::block_on(background(&self.app, Origin::Background));
    }
}

fn schedule(app: &AppHandle) {
    let Some(markers) = markers(app) else { return };
    let mut env = Real { app: app.clone(), markers };
    policy::run_schedule(&mut env);
}

// ---- asking the server for an update ----------------------------------------------------------------------

enum Fetch {
    Update(Box<Update>),
    /// The manifest names a newer version and has no package for this computer.
    NoPackage(String),
    UpToDate,
    Failed,
}

/// One request for the manifest. The version the manifest names is noted as the plugin compares it, so that a manifest with no
/// package for this computer still tells which version exists (the plugin's error does not).
async fn fetch(app: &AppHandle) -> Fetch {
    let seen = Arc::new(Mutex::new(None::<String>));
    let note = seen.clone();
    let built = update_net::harden(app.updater_builder().version_comparator(move |current, remote| {
        if let Ok(mut s) = note.lock() {
            *s = Some(remote.version.to_string());
        }
        remote.version > current
    }))
    .build();
    let Ok(updater) = built else { return Fetch::Failed };
    match updater.check().await {
        Ok(Some(update)) => Fetch::Update(Box::new(update)),
        Ok(None) => Fetch::UpToDate,
        Err(tauri_plugin_updater::Error::TargetsNotFound(_) | tauri_plugin_updater::Error::TargetNotFound(_)) => {
            match seen.lock().ok().and_then(|s| s.clone()) {
                Some(version) => Fetch::NoPackage(version),
                None => Fetch::Failed,
            }
        }
        Err(_) => Fetch::Failed,
    }
}

/// The check made by the schedule or a development switch: silent whatever happens. Never a dialog, never focus. A switch turned
/// off while it runs ends it: the answer is not acted on, and a download is abandoned.
async fn background(app: &AppHandle, origin: Origin) {
    if state(app).busy.swap(true, Ordering::SeqCst) {
        return;
    }
    let _free = Busy(&state(app).busy);
    let found = fetch(app).await;
    if origin == Origin::Background && consent(app) != Consent::Yes {
        return;
    }
    match found {
        Fetch::Update(update) => {
            keep_or_note(app, *update, origin).await;
        }
        Fetch::NoPackage(version) => note_available(app, version, Why::NoPackage),
        Fetch::UpToDate | Fetch::Failed => {}
    }
}

/// What a newer version leads to on this install: a package that is downloaded and kept, or a note that it exists.
async fn keep_or_note(app: &AppHandle, update: Update, origin: Origin) -> bool {
    match policy::kind(Os::current(), Bundle::current()) {
        Kind::Notify => {
            note_available(app, update.version.clone(), Why::NotByItself);
            false
        }
        Kind::Install => download_and_keep(app, &update, origin).await,
    }
}

/// An available version is shown in the menu, unless a package is already waiting (that is the better news).
fn note_available(app: &AppHandle, version: String, why: Why) {
    if !matches!(*state(app).phase(), Phase::Ready(_) | Phase::Downloading(_)) {
        set_phase(app, Phase::Available(version, why));
    }
}

/// Downloads the package and keeps it. Before a byte is fetched the address the manifest named is checked: only the project's own
/// release at the announced tag. The plugin checks the signature against the key built into the app and the version it was made for;
/// `update_net` bounds the size and the time and ends the download if the person turns the switch off (for a background one).
/// True when a verified package is waiting.
async fn download_and_keep(app: &AppHandle, update: &Update, origin: Origin) -> bool {
    let version = update.version.clone();
    if matches!(&*state(app).phase(), Phase::Ready(v) if *v == version) {
        return true;
    }
    let from_here =
        first_endpoint(app).is_some_and(|e| policy::package_source_ok(&e, &update.download_url, &update.version));
    if !from_here {
        dialogs::note("a package address that is not the project's release was refused");
        return false;
    }
    // What stands now, to go back to if the download fails: a package that is already waiting is still waiting.
    let before = state(app).phase().clone();
    set_phase(app, Phase::Downloading(version.clone()));
    let watched = markers(app);
    let cancel = move || origin == Origin::Background && watched.as_ref().is_none_or(|m| m.consent() != Consent::Yes);
    let mut cancelled = false;
    let kept = match update_net::bounded_download(update, cancel).await {
        Ok(bytes) => cache(app).is_some_and(|c| c.store(&version, &update.raw_json, &bytes).is_ok()),
        Err(DownloadFailure::TooBig) => {
            dialogs::note("a package larger than the limit was refused");
            false
        }
        Err(DownloadFailure::Cancelled) => {
            dialogs::note("the download was abandoned: automatic checking was turned off");
            cancelled = true;
            false
        }
        Err(DownloadFailure::Failed) => false,
    };
    if kept {
        set_phase(app, Phase::Ready(version));
    } else {
        // Nothing new is kept and nothing was changed; the next check tries again, a day from now.
        let failed = if matches!(before, Phase::Ready(_)) { before } else { Phase::DownloadFailed(version) };
        // (a download the person cancelled by turning the switch off is not a failure to report)
        set_phase(app, if cancelled { Phase::Idle } else { failed });
    }
    kept
}

// ---- the menu -----------------------------------------------------------------------------------------------

/// The update item was chosen.
pub fn on_updates_item(app: &AppHandle) {
    let phase = state(app).phase().clone();
    match phase {
        Phase::Ready(_) => restart_to_update(app, false),
        Phase::Available(version, why) => tell_available(app, &version, why),
        Phase::Downloading(version) => {
            let (title, body) = policy::downloading_notice(&version);
            dialogs::info(app, &title, &body);
        }
        Phase::Idle | Phase::DownloadFailed(_) => manual_check(app),
    }
}

/// The switch was chosen. What is shown afterwards is what is true: the answer is read again from where it is kept, and a "no"
/// that could not be carried out is said.
pub fn on_automatic_item(app: &AppHandle) {
    let was = consent(app) == Consent::Yes;
    if source(app).is_none() {
        menu::set_automatic_item(false, false);
        let (title, body) = not_set_up();
        dialogs::info(app, &title, &body);
        return;
    }
    let want = !was;
    let written = markers(app).map(|m| m.answer(want));
    let now = consent(app) == Consent::Yes;
    menu::set_automatic_item(now, true);
    if now != want || !matches!(written, Some(Ok(()))) {
        let body = if now && !want {
            "Scríobh could not change its own settings folder, so automatic checking is still ON. Nothing was changed."
        } else {
            "Scríobh could not write to its own settings folder, so the setting is unchanged."
        };
        dialogs::info(
            app,
            if now && !want { "Automatic checking could not be turned off" } else { "The choice could not be kept" },
            body,
        );
    }
}

fn not_set_up() -> (String, String) {
    (
        "Updates are not set up in this build".into(),
        "This copy of Scríobh was built without an update source, so it cannot look for a newer version. Nothing was contacted.".into(),
    )
}

/// Check for Updates…, chosen by hand. Until the person has said yes to checking by itself it asks first and names the
/// host, as it always has.
fn manual_check(app: &AppHandle) {
    let Some(host) = source(app) else {
        let (title, body) = not_set_up();
        return dialogs::info(app, &title, &body);
    };
    let handle = app.clone();
    if consent(app) == Consent::Yes {
        tauri::async_runtime::spawn(manual_run(handle));
        return;
    }
    let (title, body) = policy::manual_question(&host);
    dialogs::ask(app, &title, &body, "Check Now", "Cancel", move |yes| {
        if yes {
            tauri::async_runtime::spawn(manual_run(handle));
        }
    });
}

/// A question answered by the person, awaited without holding a thread of the runtime.
async fn ask_async(app: &AppHandle, title: &str, body: &str, yes: &str, no: &str) -> bool {
    let (tx, rx) = tokio::sync::oneshot::channel();
    dialogs::ask(app, title, body, yes, no, move |answer| {
        let _ = tx.send(answer);
    });
    rx.await.unwrap_or(false)
}

async fn manual_run(app: AppHandle) {
    if state(&app).busy.swap(true, Ordering::SeqCst) {
        return dialogs::info(&app, "Scríobh is already looking", "A check or a download is already under way.");
    }
    let _free = Busy(&state(&app).busy);
    // When the person last did something: a result that comes much later is left to the menu, not put over their typing.
    let mut since = Instant::now();
    match fetch(&app).await {
        Fetch::UpToDate => {
            say_if_soon(&app, since, policy::up_to_date());
        }
        Fetch::Failed => {
            say_if_soon(&app, since, policy::check_failed());
        }
        Fetch::NoPackage(version) => {
            note_available(&app, version.clone(), Why::NoPackage);
            say_if_soon(&app, since, policy::no_package_for_this_computer(&version));
        }
        Fetch::Update(update) => {
            let update = *update;
            if policy::kind(Os::current(), Bundle::current()) == Kind::Notify {
                note_available(&app, update.version.clone(), Why::NotByItself);
                if since.elapsed() <= SOON {
                    tell_available(&app, &update.version, Why::NotByItself);
                }
                return;
            }
            // A package is a second request to the same hosts: after a yes to checking by itself it is simply fetched; a
            // person who is checking by hand is asked.
            let wanted = if consent(&app) == Consent::Yes {
                true
            } else {
                let (title, body) = policy::download_question(&update.current_version, &update.version);
                let answer = ask_async(&app, &title, &body, "Download", "Not Now").await;
                since = Instant::now();
                answer
            };
            if !wanted {
                return;
            }
            say_if_soon(&app, since, policy::downloading_notice(&update.version));
            if download_and_keep(&app, &update, Origin::Manual).await {
                // The menu now says Restart to Update; this is shown only if the download was quick.
                say_if_soon(&app, since, policy::downloaded_notice(&update.version));
            } else {
                // Said once for a version, not at every attempt.
                // A failure is remembered as said only when it was SHOWN: one that came too late for a dialog is in the menu
                // (the item says the download failed), and the next failure is still told.
                let repeat = state(&app)
                    .said_failed
                    .lock()
                    .map(|said| said.as_deref() == Some(update.version.as_str()))
                    .unwrap_or(false);
                if !repeat && say_if_soon(&app, since, policy::download_failed()) {
                    if let Ok(mut said) = state(&app).said_failed.lock() {
                        *said = Some(update.version.clone());
                    }
                }
            }
        }
    }
}

/// A newer version that this install does not install by itself, or that has no package here.
fn tell_available(app: &AppHandle, version: &str, why: Why) {
    let page = first_endpoint(app).and_then(|e| policy::releases_page(&e));
    let (title, body) = match why {
        Why::NoPackage => policy::no_package_for_this_computer(version),
        Why::NotByItself => policy::newer_exists_but_not_by_itself(version, page.is_some()),
    };
    let handle = app.clone();
    dialogs::info_then(app, &title, &body, move || {
        // The release page is offered through the shell's ordinary question about an outside address.
        if let Some(Ok(url)) = page.map(|p| tauri::Url::parse(&p)) {
            app::offer_outside(&handle, url);
        }
    });
}

/// A question answered by the person, waited for on a thread of our own (never the main one).
fn ask_blocking(app: &AppHandle, title: &str, body: &str, yes: &str, no: &str) -> bool {
    let (tx, rx) = mpsc::channel();
    dialogs::ask(app, title, body, yes, no, move |answer| {
        let _ = tx.send(answer);
    });
    rx.recv().unwrap_or(false)
}

/// As [`ask_blocking`], with the safe button first (the one a stray Return presses). True only when the OTHER button was chosen.
fn ask_safe_blocking(app: &AppHandle, title: &str, body: &str, safe: &str, other: &str) -> bool {
    let (tx, rx) = mpsc::channel();
    dialogs::ask_safe_first(app, title, body, safe, other, move |chose_other| {
        let _ = tx.send(chose_other);
    });
    rx.recv().unwrap_or(false)
}

// ---- Restart to Update --------------------------------------------------------------------------------------

/// The one call that installs a package. It takes the proof that this install replaces itself, so a `.deb`, an `.rpm` or
/// a package of unknown kind cannot get here (`update_policy::install_permit`, and a test that this is the only call).
fn install_package(_permit: &InstallPermit, update: &Update, bytes: &[u8]) -> tauri_plugin_updater::Result<()> {
    update.install(bytes)
}

/// Restart to Update, chosen. Everything that waits happens off the main thread.
pub fn restart_to_update(app: &AppHandle, unattended: bool) {
    if state(app).restarting.swap(true, Ordering::SeqCst) {
        return;
    }
    let app = app.clone();
    thread::spawn(move || {
        if !do_restart(&app, unattended) {
            state(&app).restarting.store(false, Ordering::SeqCst);
        }
    });
}

/// The order of a restart, each step before the next (docs/UPDATES.md): the kept package is checked again, the person
/// confirms, the open pages are asked to save while the server still runs, the server is stopped cleanly, the windows
/// are hidden, the package is installed and the app starts again. Returns false when it stopped before changing anything.
fn do_restart(app: &AppHandle, unattended: bool) -> bool {
    let say = |(title, body): (String, String)| dialogs::info(app, &title, &body);
    let Some(permit) = policy::install_permit(Os::current(), Bundle::current()) else { return false };
    let (Some(markers), Some(cache)) = (markers(app), cache(app)) else { return false };
    let Some((kept, update)) = cache.load().and_then(|k| restore(app, &k).map(|u| (k, u))) else {
        // What was kept is gone, cut short or does not verify: it is thrown away and nothing is installed.
        cache.clear();
        set_phase(app, Phase::Idle);
        say(policy::download_failed());
        return false;
    };
    let from_here =
        first_endpoint(app).is_some_and(|e| policy::package_source_ok(&e, &update.download_url, &update.version));
    if !from_here {
        // The kept answer names an address that is not the project's release: nothing is installed.
        cache.clear();
        set_phase(app, Phase::Idle);
        say(policy::download_failed());
        return false;
    }
    if Os::current() == Os::Linux {
        let image = app::appimage_path();
        let file_ok = image.as_deref().is_some_and(policy::can_write);
        let folder_ok = image.as_deref().and_then(|p| p.parent()).is_some_and(policy::can_write);
        if let Some(problem) = policy::appimage_problem(image.as_deref(), file_ok, folder_ok) {
            say(policy::cannot_replace_appimage(problem));
            return false;
        }
    }
    if Os::current() == Os::Macos {
        if let Some(bundle) = std::env::current_exe().ok().as_deref().and_then(policy::app_bundle_of) {
            let parent_writable = bundle.parent().is_some_and(policy::can_write);
            let location = policy::mac_location(bundle, parent_writable);
            if location != policy::Location::Fine {
                say(policy::cannot_replace(location, bundle));
                return false;
            }
        }
    }
    if !unattended {
        let (title, body) = policy::restart_question(&update.version, Os::current() == Os::Macos);
        if !ask_blocking(app, &title, &body, "Restart Now", "Not Now") {
            return false;
        }
    }
    // The pages save while the server still runs. A page saves with the request it always uses; nothing is written here.
    let outcome = save_pages(app);
    dialogs::note(&format!("save now: {outcome:?}"));
    let writing_open = policy::any_writing(&app::main_paths(app));
    if let Some(why) = policy::unconfirmed(outcome, writing_open) {
        if !unattended {
            let (title, body) = policy::unconfirmed_text(why);
            // "Keep Writing" is the first button, the one a stray Return presses.
            if !ask_safe_blocking(app, &title, &body, "Keep Writing", "Update Anyway") {
                return false;
            }
        }
    }
    // From here the old version is going away. The server stops cleanly (the log is checkpointed), then the windows are
    // hidden, not closed: closing the last window would end the app in the middle of the install.
    app::stop_server(app);
    for (_, w) in app.webview_windows() {
        let _ = w.hide();
    }
    match install_package(&permit, &update, &kept.bytes) {
        Ok(()) => cache.clear(),
        Err(_) => {
            // Nothing sensible is running now; starting again brings back the version that was here, which says once
            // that the update could not be installed.
            cache.clear();
            markers.note_failure(&update.version);
            dialogs::note("the update could not be installed; restarting the installed version");
        }
    }
    app.restart()
}

fn save_pages(app: &AppHandle) -> SaveOutcome {
    let Some((port, key)) = app::server_access(app) else { return SaveOutcome::NotAsked };
    policy::wait_for_pages(
        || launch::save_now(port, &key),
        |n| launch::save_status(port, &key, n),
        thread::sleep,
        Duration::from_secs(10),
        Duration::from_millis(250),
    )
}
