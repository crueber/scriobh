//! What the update feature decides, as functions of plain values, so that every branch is tested on one machine
//! whatever it runs on (`docs/UPDATES.md`): which packages install and which only notify, whether the person has said
//! yes, when a check is due, whether the app is somewhere it can be replaced, how long to wait for the pages to save,
//! and the words. Nothing here touches the network, the window or the plugin.

use std::fs;
use std::io;
use std::path::{Path, PathBuf};
use std::time::{Duration, SystemTime};

// ---- which package updates how ------------------------------------------------------------------------------

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Os {
    Macos,
    Windows,
    Linux,
    Other,
}

impl Os {
    pub fn current() -> Os {
        if cfg!(target_os = "macos") {
            Os::Macos
        } else if cfg!(target_os = "windows") {
            Os::Windows
        } else if cfg!(target_os = "linux") {
            Os::Linux
        } else {
            Os::Other
        }
    }
}

/// How this binary was packaged, as Tauri's bundler recorded it in the binary (`tauri::utils::platform::bundle_type`).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Bundle {
    App,
    Nsis,
    Msi,
    AppImage,
    Deb,
    Rpm,
    /// Not recorded (a development build, a build from source, a package the bundler did not make), or a kind this
    /// shell has no name for.
    Unknown,
}

impl Bundle {
    pub fn current() -> Bundle {
        use tauri::utils::config::BundleType;
        match tauri::utils::platform::bundle_type() {
            Some(BundleType::App) => Bundle::App,
            Some(BundleType::Nsis) => Bundle::Nsis,
            Some(BundleType::Msi) => Bundle::Msi,
            Some(BundleType::AppImage) => Bundle::AppImage,
            Some(BundleType::Deb) => Bundle::Deb,
            Some(BundleType::Rpm) => Bundle::Rpm,
            _ => Bundle::Unknown,
        }
    }
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Kind {
    /// The app replaces itself.
    Install,
    /// The app says that a newer version exists and never installs it: a `.deb` is the package manager's to install
    /// (the plugin's own `.deb` path runs `dpkg` as root and asks for a password), an `.rpm` likewise, and a binary
    /// whose package is not known has no way to be replaced that this shell can vouch for.
    Notify,
}

/// Which kind of update this install gets. A pure function of the system and the bundle type.
pub fn kind(os: Os, bundle: Bundle) -> Kind {
    match (os, bundle) {
        (Os::Macos, Bundle::App) | (Os::Windows, Bundle::Nsis) | (Os::Linux, Bundle::AppImage) => Kind::Install,
        _ => Kind::Notify,
    }
}

/// Proof that this install is one that replaces itself. The call that installs a package takes one, and there is no
/// other way to make one than [`install_permit`], so a `.deb`, an `.rpm` or an unknown bundle cannot reach it.
pub struct InstallPermit(());

pub fn install_permit(os: Os, bundle: Bundle) -> Option<InstallPermit> {
    (kind(os, bundle) == Kind::Install).then_some(InstallPermit(()))
}

// ---- the person's answer, and the clock ---------------------------------------------------------------------

/// What the person has said about checking by itself. Kept in the shell, as markers in its own configuration folder
/// (never in the server: it takes no part in an update, and a page must have no say over a program that installs code).
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Consent {
    /// Never asked, or an answer that could not be kept. Nothing is checked by itself.
    Unasked,
    Yes,
    /// Asked and answered no (or the question was closed). Final until the person turns it on.
    No,
}

#[derive(Clone)]
pub struct Markers {
    dir: PathBuf,
}

const ASKED: &str = "updates-asked";
const AUTOMATIC: &str = "updates-automatic";
const LAST_CHECK: &str = "updates-last-check";
const FAILED: &str = "updates-failed";

impl Markers {
    pub fn new(dir: PathBuf) -> Self {
        Markers { dir }
    }

    pub fn consent(&self) -> Consent {
        match (self.dir.join(ASKED).is_file(), self.dir.join(AUTOMATIC).is_file()) {
            (false, _) => Consent::Unasked,
            (true, true) => Consent::Yes,
            (true, false) => Consent::No,
        }
    }

    /// Keeps the answer. `Err` means it could not be kept; then nothing is checked by itself. A "no" that could not be
    /// carried out (the marker could not be removed) is an error too, never a silent success: the caller reads
    /// [`Markers::consent`] again and says what is true.
    pub fn answer(&self, yes: bool) -> io::Result<()> {
        fs::create_dir_all(&self.dir)?;
        if yes {
            // The automatic marker first: a crash between the two files must not leave "asked, no" for a yes.
            fs::write(self.dir.join(AUTOMATIC), b"")?;
            fs::write(self.dir.join(ASKED), b"")
        } else {
            match fs::remove_file(self.dir.join(AUTOMATIC)) {
                Ok(()) => {}
                Err(e) if e.kind() == io::ErrorKind::NotFound => {}
                Err(e) => return Err(e),
            }
            fs::write(self.dir.join(ASKED), b"")
        }
    }

    /// When a check was last tried: the modification time of a marker. `None` if never.
    pub fn last_check(&self) -> Option<SystemTime> {
        fs::metadata(self.dir.join(LAST_CHECK)).and_then(|m| m.modified()).ok()
    }

    pub fn record_attempt(&self, at: SystemTime) -> io::Result<()> {
        fs::create_dir_all(&self.dir)?;
        let f = fs::OpenOptions::new().write(true).create(true).truncate(true).open(self.dir.join(LAST_CHECK))?;
        f.set_modified(at)
    }

    /// An install that failed after it was started: the version, to be said once at the next start.
    pub fn note_failure(&self, version: &str) {
        let _ = fs::create_dir_all(&self.dir);
        let _ = fs::write(self.dir.join(FAILED), version);
    }

    pub fn failure_pending(&self) -> bool {
        self.dir.join(FAILED).is_file()
    }

    pub fn take_failure(&self) -> Option<String> {
        let path = self.dir.join(FAILED);
        let v = fs::read_to_string(&path).ok()?;
        let _ = fs::remove_file(path);
        Some(v.trim().to_string()).filter(|v| !v.is_empty())
    }
}

/// How soon after the window is up the first check may be made, and how long between checks.
pub const START_DELAY: Duration = Duration::from_secs(30);
pub const INTERVAL: Duration = Duration::from_secs(24 * 60 * 60);
/// How often the schedule looks again at the person's answer, so that the Help switch takes effect within this.
pub const POLL: Duration = Duration::from_secs(30);

/// How long until a check by itself is due, or `None` when none may be made: only after a yes, not sooner than
/// [`START_DELAY`] after the start, and not sooner than [`INTERVAL`] after the last attempt (made or failed). A last
/// attempt that is in the future (the clock was set back) counts as now, so one wrong clock cannot silence checks for
/// longer than a day.
pub fn wait_before_check(
    consent: Consent,
    last: Option<SystemTime>,
    now: SystemTime,
    since_start: Duration,
) -> Option<Duration> {
    if consent != Consent::Yes {
        return None;
    }
    let from_start = START_DELAY.saturating_sub(since_start);
    let from_last =
        last.map(|l| (l + INTERVAL).duration_since(now).unwrap_or_default().min(INTERVAL)).unwrap_or_default();
    Some(from_start.max(from_last))
}

/// What the schedule runs on: the real one is the shell's markers, the system clock and the plugin; a test's is a
/// clock that moves when it waits.
pub trait ScheduleEnv {
    fn consent(&self) -> Consent;
    fn last_check(&self) -> Option<SystemTime>;
    fn record_attempt(&mut self, at: SystemTime);
    fn now(&self) -> SystemTime;
    /// Waits, and says whether to go on (false ends the schedule: the app is quitting).
    fn wait(&mut self, d: Duration) -> bool;
    fn check(&mut self);
}

/// The loop that makes the daily check. An attempt is recorded before it is made, so one that never returns still
/// counts, and it is also kept in memory, so a marker that cannot be written does not turn into a check every half
/// minute.
pub fn run_schedule(env: &mut impl ScheduleEnv) {
    let started = env.now();
    let mut attempted: Option<SystemTime> = None;
    loop {
        let now = env.now();
        let last = match (env.last_check(), attempted) {
            (Some(a), Some(b)) => Some(a.max(b)),
            (a, b) => a.or(b),
        };
        let since = now.duration_since(started).unwrap_or_default();
        match wait_before_check(env.consent(), last, now, since) {
            None => {
                if !env.wait(POLL) {
                    return;
                }
            }
            Some(d) if d.is_zero() => {
                attempted = Some(now);
                env.record_attempt(now);
                env.check();
            }
            Some(d) => {
                if !env.wait(d.min(POLL)) {
                    return;
                }
            }
        }
    }
}

// ---- the menu's item ----------------------------------------------------------------------------------------

/// Where an update stands, as the menu says it.
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Phase {
    Idle,
    /// A newer version is being downloaded in the background.
    Downloading(String),
    /// Downloaded and checked against the key: Restart to Update installs it.
    Ready(String),
    /// A newer version exists that this install does not install by itself, or that has no download for this computer.
    Available(String, Why),
    /// The download of this version failed. A failure that came too late to be put in a dialog is told here, in the menu, and the
    /// item tries again.
    DownloadFailed(String),
}

/// Why an available version is not downloaded and installed by the app.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Why {
    /// This install is a `.deb`, an Arch package or something whose package is not known: it is told, never changed.
    NotByItself,
    /// The manifest names the version but has no package for this computer.
    NoPackage,
}

/// The text of the menu's update item and whether it can be chosen. A version is shown cleaned (see [`shown`]).
pub fn label(phase: &Phase) -> (String, bool) {
    match phase {
        Phase::Idle => ("Check for Updates…".into(), true),
        Phase::Downloading(v) => (format!("Downloading Scríobh {}…", shown(v, 40, false)), false),
        Phase::Ready(v) => (format!("Restart to Update to {}…", shown(v, 40, false)), true),
        Phase::Available(v, _) => (format!("Scríobh {} Is Available…", shown(v, 40, false)), true),
        Phase::DownloadFailed(v) => (format!("Download of Scríobh {} Failed. Try Again…", shown(v, 40, false)), true),
    }
}

/// Text from outside, made safe to show: printable, one line unless `lines`, and short.
pub fn shown(text: &str, max: usize, lines: bool) -> String {
    let clean: String = text
        .chars()
        .filter(|c| (lines && *c == '\n') || !c.is_control())
        .filter(|c| !matches!(c, '\u{202A}'..='\u{202E}' | '\u{2066}'..='\u{2069}'))
        .collect();
    if clean.chars().count() > max {
        clean.chars().take(max).chain(std::iter::once('…')).collect()
    } else {
        clean
    }
}

/// The host updates come from, or `None` when this build has no real update source (the placeholder key or address is
/// still in the configuration, as it is in the repository: only a release build for a tag has one).
pub fn update_source(plugin: Option<&serde_json::Value>) -> Option<String> {
    let plugin = plugin?;
    let key = plugin.get("pubkey")?.as_str()?;
    if key.trim().is_empty() || key.contains("REPLACE") {
        return None;
    }
    let first = plugin.get("endpoints")?.as_array()?.first()?.as_str()?;
    let url = tauri::Url::parse(first).ok()?;
    let host = url.host_str()?;
    if url.scheme() != "https" || host.ends_with(".invalid") || host.ends_with(".example") {
        return None;
    }
    Some(host.to_string())
}

/// The owner and repository named by the update address `https://github.com/<owner>/<repo>/releases/latest/download/latest.json`.
/// `None` for any other address.
pub fn github_repo_of(endpoint: &str) -> Option<(String, String)> {
    let url = tauri::Url::parse(endpoint).ok()?;
    let parts: Vec<&str> = url.path_segments()?.collect();
    match parts.as_slice() {
        [owner, repo, "releases", "latest", "download", "latest.json"]
            if url.scheme() == "https"
                && url.host_str() == Some("github.com")
                && plain_name(owner)
                && plain_name(repo) =>
        {
            Some((owner.to_string(), repo.to_string()))
        }
        _ => None,
    }
}

fn plain_name(s: &str) -> bool {
    !s.is_empty() && s.bytes().all(|b| b.is_ascii_alphanumeric() || matches!(b, b'-' | b'_' | b'.'))
}

/// The page of the project's releases that the notice for a `.deb` offers, from the update address. `None` for any other address.
pub fn releases_page(endpoint: &str) -> Option<String> {
    github_repo_of(endpoint).map(|(owner, repo)| format!("https://github.com/{owner}/{repo}/releases/latest"))
}

/// Whether a package may be downloaded from `url`: only `https://github.com/<the update address's owner and repository>/releases/
/// download/v<version>/<one file name>`, with no port, credentials, query or fragment. The manifest is read from the update
/// address, but what it names is checked again here, before one byte is fetched (the signature protects what is installed; this
/// protects where the app connects, and what it is made to fetch).
pub fn package_source_ok(endpoint: &str, url: &tauri::Url, version: &str) -> bool {
    let Some((owner, repo)) = github_repo_of(endpoint) else { return false };
    let Some(segments) = url.path_segments() else { return false };
    let segments: Vec<&str> = segments.collect();
    let tag = format!("v{version}");
    url.scheme() == "https"
        && url.host_str() == Some("github.com")
        && url.port().is_none()
        && url.username().is_empty()
        && url.password().is_none()
        && url.query().is_none()
        && url.fragment().is_none()
        && matches!(segments.as_slice(), [o, r, "releases", "download", t, asset]
            if *o == owner && *r == repo && *t == tag && plain_name(asset))
}

// ---- where the app is (macOS) -------------------------------------------------------------------------------

/// The `.app` that holds the running program, from its path (`…/Scriobh.app/Contents/MacOS/scriobh-desktop`).
pub fn app_bundle_of(exe: &Path) -> Option<&Path> {
    exe.ancestors().find(|p| p.extension().is_some_and(|e| e == "app"))
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Location {
    /// Somewhere the update can replace it.
    Fine,
    /// Run from a mounted disk image: it cannot be replaced, and it is not where it will live.
    OnADiskImage,
    /// macOS runs a quarantined app opened from Downloads from a randomised read-only copy.
    Translocated,
    /// The folder that holds it is not one this person can change.
    NotWritable,
}

/// Whether the swap of the running `.app` can work where the app is (docs/UPDATES.md, "macOS"). `parent_writable` is
/// whether the folder that holds the `.app` can be changed by this person; `/Applications` is always fine, because the
/// plugin asks for administrator rights itself there.
pub fn mac_location(bundle: &Path, parent_writable: bool) -> Location {
    let text = bundle.to_string_lossy();
    if text.starts_with("/Volumes/") {
        Location::OnADiskImage
    } else if text.contains("/AppTranslocation/") {
        Location::Translocated
    } else if parent_writable || text.starts_with("/Applications/") {
        Location::Fine
    } else {
        Location::NotWritable
    }
}

/// Whether this person can create files in `dir`.
pub fn can_write(dir: &Path) -> bool {
    #[cfg(unix)]
    {
        use std::os::unix::ffi::OsStrExt;
        std::ffi::CString::new(dir.as_os_str().as_bytes())
            // SAFETY: a valid, NUL-terminated path and a plain flag.
            .map(|c| unsafe { libc::access(c.as_ptr(), libc::W_OK) } == 0)
            .unwrap_or(false)
    }
    #[cfg(not(unix))]
    {
        dir.is_dir()
    }
}

// ---- where the AppImage is (Linux), and which page the person is on -------------------------------------------------

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum AppImageProblem {
    /// `$APPIMAGE`, which the AppImage runtime sets, is not set: this is not a run of an AppImage, so there is nothing to replace.
    NotRunAsAnAppImage,
    /// The file, or the folder that holds it, cannot be changed by this person.
    NotWritable,
}

/// Whether the AppImage can replace itself (the plugin writes the new file beside the old one and swaps them): checked
/// before the server is stopped, as the macOS location is.
pub fn appimage_problem(path: Option<&Path>, file_writable: bool, folder_writable: bool) -> Option<AppImageProblem> {
    match path {
        None => Some(AppImageProblem::NotRunAsAnAppImage),
        Some(_) if !file_writable || !folder_writable => Some(AppImageProblem::NotWritable),
        Some(_) => None,
    }
}

/// A writing page: `/p/<slug>/d/<id>`.
pub fn is_writing_path(path: &str) -> bool {
    let parts: Vec<&str> = path.trim_matches('/').split('/').collect();
    matches!(parts.as_slice(), ["p", slug, "d", id, ..] if !slug.is_empty() && !id.is_empty())
}

/// Pages with NO place to type, so that a stray key cannot answer a question put over them: the library (its search box is not
/// focused) and the Compendium list. Every page with a form or a text field is left out on purpose, whatever it is for: settings,
/// import (including `/import/git`), search (its box is focused), the identity page (it has a password field), and every page of a
/// chapter, a project or a Compendium. The first question and the notice of a failed install wait for one of these.
pub fn calm_page(path: &str) -> bool {
    matches!(path, "/" | "/compendiums")
}

/// Whether every window of the app shows a calm page (and there is at least one): a question is not put while any window is on a
/// page where someone could be typing.
pub fn all_calm(paths: &[String]) -> bool {
    !paths.is_empty() && paths.iter().all(|p| calm_page(p))
}

/// Whether any window shows a writing page.
pub fn any_writing(paths: &[String]) -> bool {
    paths.iter().any(|p| is_writing_path(p))
}

// ---- save now, and how long to wait ---------------------------------------------------------------------------

/// How the wait for the open pages to save ended.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SaveOutcome {
    /// Every page that was asked has saved (and none was asked, if `pages` is 0: no writing page is open, or its
    /// window is hidden and the page saves itself as it is left).
    Saved { pages: u32 },
    /// A page answered that it still holds text it could not save.
    TextLeft { unsaved: u32 },
    /// A page's connection closed before it answered: nothing confirmed that its text was saved.
    PageGone { gone: u32 },
    /// The wait ended with pages that had not answered.
    TimedOut { pending: u32 },
    /// The server could not be asked at all: it is gone, or does not know the request.
    NotAsked,
}

/// Asks, then looks every `step` until nobody is pending or `limit` has passed. `ask` and `status` are the shell's
/// calls to the server; `sleep` is what waits.
pub fn wait_for_pages(
    ask: impl FnOnce() -> io::Result<crate::launch::SaveRound>,
    mut status: impl FnMut(i64) -> io::Result<crate::launch::SaveStatus>,
    mut sleep: impl FnMut(Duration),
    limit: Duration,
    step: Duration,
) -> SaveOutcome {
    let Ok(round) = ask() else { return SaveOutcome::NotAsked };
    if round.pages == 0 {
        return SaveOutcome::Saved { pages: 0 };
    }
    let mut waited = Duration::ZERO;
    let mut last_pending = round.pages;
    loop {
        if let Ok(s) = status(round.n) {
            last_pending = s.pending;
            if s.pending == 0 {
                return if s.unsaved > 0 {
                    SaveOutcome::TextLeft { unsaved: s.unsaved }
                } else if s.gone > 0 {
                    SaveOutcome::PageGone { gone: s.gone }
                } else {
                    SaveOutcome::Saved { pages: round.pages }
                };
            }
        }
        if waited >= limit {
            return SaveOutcome::TimedOut { pending: last_pending };
        }
        sleep(step);
        waited += step;
    }
}

/// Why the app cannot say that everything written is saved.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Unconfirmed {
    TextLeft,
    TimedOut,
    PageGone,
    /// Nobody was asked, and a writing page is open: its window is hidden or its connection is gone.
    NobodyListening,
    NotAsked,
}

/// What the wait for the pages means for the person: `None` only when every page that was asked saved (or no page that holds
/// text is open). Everything else is "not confirmed saved", and the person chooses whether to go on.
pub fn unconfirmed(outcome: SaveOutcome, writing_window_open: bool) -> Option<Unconfirmed> {
    match outcome {
        SaveOutcome::Saved { pages: 0 } if writing_window_open => Some(Unconfirmed::NobodyListening),
        SaveOutcome::Saved { .. } => None,
        SaveOutcome::TextLeft { .. } => Some(Unconfirmed::TextLeft),
        SaveOutcome::TimedOut { .. } => Some(Unconfirmed::TimedOut),
        SaveOutcome::PageGone { .. } => Some(Unconfirmed::PageGone),
        SaveOutcome::NotAsked => Some(Unconfirmed::NotAsked),
    }
}

// ---- the words ----------------------------------------------------------------------------------------------

/// Where the person finds the switch: the macOS application menu has one beside Check for Updates…, and the Help menu on
/// every system has the pair.
pub const WHERE_THE_SWITCH_IS: &str = "the Help menu";

pub fn first_run_question(host: &str) -> (String, String) {
    (
        "Check for updates automatically?".into(),
        format!(
            "Scríobh can look at {host} about once a day to see whether a newer version is out. That check is the same request for everyone: it does not say who you are, what you have written, or which version you have. GitHub sees your internet address and the time, as any website does. If there is a newer version, Scríobh downloads the file for your kind of computer, which tells GitHub which kind that is and which version it is getting. Nothing is installed until you choose Restart to Update. You can change this in {WHERE_THE_SWITCH_IS}."
        ),
    )
}

pub fn manual_question(host: &str) -> (String, String) {
    (
        "Check for updates?".into(),
        format!(
            "Scríobh will ask {host} (and GitHub's release servers) whether a newer version is out. The request is the same for everyone: it does not say who you are, what you have written, or which version you have. To have it check by itself, turn on Check for Updates Automatically in {WHERE_THE_SWITCH_IS}."
        ),
    )
}

pub fn download_question(current: &str, version: &str) -> (String, String) {
    (
        format!("Version {} is available", shown(version, 40, false)),
        format!(
            "You have {}. Scríobh can download it now, in the background, and check it against the key built into the app. It is installed only when you choose Restart to Update.",
            shown(current, 40, false)
        ),
    )
}

pub fn downloaded_notice(version: &str) -> (String, String) {
    (
        format!("Scríobh {} has been downloaded", shown(version, 40, false)),
        "It is checked and waiting. Choose Restart to Update in the menu when you are ready; nothing is installed until you do.".into(),
    )
}

pub fn downloading_notice(version: &str) -> (String, String) {
    (
        format!("Scríobh {} is downloading", shown(version, 40, false)),
        "It is downloaded in the background. When it is ready the menu says Restart to Update, and nothing is installed until you choose it.".into(),
    )
}

pub fn up_to_date() -> (String, String) {
    ("Scríobh is up to date".into(), "There is no newer version.".into())
}

pub fn check_failed() -> (String, String) {
    (
        "Could not check for updates".into(),
        "The update server did not answer, or its answer could not be read. Nothing was changed.".into(),
    )
}

pub fn download_failed() -> (String, String) {
    (
        "The update was not installed".into(),
        "The download failed or its signature did not match. Nothing was changed.".into(),
    )
}

pub fn no_package_for_this_computer(version: &str) -> (String, String) {
    (
        format!("Version {} exists", shown(version, 40, false)),
        "A newer version exists, but it has no download for this computer yet. Nothing was changed.".into(),
    )
}

/// What a `.deb` install, an Arch package or an install of unknown kind is told.
pub fn newer_exists_but_not_by_itself(version: &str, how_to_get_it: bool) -> (String, String) {
    (
        format!("Scríobh {} is available", shown(version, 40, false)),
        format!(
            "This copy of Scríobh does not update itself: it was installed with a package manager (or in a way that has no safe way to replace itself), so install the new version the way you installed this one.{} Nothing was changed.",
            if how_to_get_it { " The release page has the files." } else { "" }
        ),
    )
}

pub fn cannot_replace(location: Location, bundle: &Path) -> (String, String) {
    let place = bundle.parent().map(|p| p.display().to_string()).unwrap_or_default();
    let why = match location {
        Location::OnADiskImage => "Scríobh is running from a disk image, which cannot be updated.".to_string(),
        Location::Translocated => {
            "macOS is running this copy from a temporary place (it was opened straight from Downloads), which cannot be updated.".to_string()
        }
        _ => format!("Scríobh is in a folder you cannot change ({place}), so it cannot replace itself there."),
    };
    (
        "Scríobh cannot update from here".into(),
        format!("{why} Move Scríobh to the Applications folder, open it from there, and update. Nothing was changed."),
    )
}

pub fn restart_question(version: &str, mac: bool) -> (String, String) {
    let keychain = if mac {
        "\n\nAfter the update macOS asks once for your login password, so Scríobh can open the key that protects saved AI and Git credentials. Choose Always Allow. (If you choose Deny, Scríobh says so; choose Restart in its window and allow it then. Your writing is not affected.)"
    } else {
        ""
    };
    (
        format!("Restart to update to {}?", shown(version, 40, false)),
        format!("Scríobh will save your writing, close, install the new version and open again.{keychain}"),
    )
}

pub fn unconfirmed_text(why: Unconfirmed) -> (String, String) {
    let reason = match why {
        Unconfirmed::TextLeft => "A page says it still holds text it could not save.",
        Unconfirmed::TimedOut => "A page did not answer within ten seconds.",
        Unconfirmed::PageGone => "A page closed before it answered.",
        Unconfirmed::NobodyListening => "Scríobh cannot reach the page you are writing in (its window may be hidden).",
        Unconfirmed::NotAsked => "Scríobh could not ask its pages to save.",
    };
    (
        "Scríobh could not confirm that your writing is saved".into(),
        format!(
            "{reason} If you go on, a copy of unsaved text is kept on this computer and Scríobh offers it back when you open the same chapter after the restart, but only if the app opens at the same local address as before, which it usually does and cannot promise. To be sure, keep writing, wait until the chapter says Saved, and update again."
        ),
    )
}

pub fn cannot_replace_appimage(problem: AppImageProblem) -> (String, String) {
    let why = match problem {
        AppImageProblem::NotRunAsAnAppImage => {
            "Scríobh was not started from its AppImage file, so there is no file for it to replace."
        }
        AppImageProblem::NotWritable => {
            "The AppImage file, or the folder it is in, is one you cannot change, so Scríobh cannot replace it."
        }
    };
    (
        "Scríobh cannot update from here".into(),
        format!("{why} Download the new AppImage yourself from the release page, or move this one to a folder you own. Nothing was changed."),
    )
}

pub fn install_failed(version: &str, releases: Option<&str>) -> (String, String) {
    (
        "The update could not be installed".into(),
        format!(
            "Scríobh {} could not replace this version, so this version is still the one that is installed. Nothing was lost.{}",
            shown(version, 40, false),
            releases.map(|r| format!(" You can download it from {r}.")).unwrap_or_default()
        ),
    )
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::launch::{SaveRound, SaveStatus};
    use serde_json::json;
    use std::cell::{Cell, RefCell};

    fn t(secs: u64) -> SystemTime {
        SystemTime::UNIX_EPOCH + Duration::from_secs(1_800_000_000 + secs)
    }

    // ---- kind

    #[test]
    fn only_a_package_that_can_replace_itself_installs() {
        use Bundle::*;
        use Os::*;
        let all_bundles = [App, Nsis, Msi, AppImage, Deb, Rpm, Unknown];
        let all_os = [Macos, Windows, Linux, Other];
        let mut installing = Vec::new();
        for os in all_os {
            for b in all_bundles {
                if kind(os, b) == Kind::Install {
                    installing.push((os, b));
                    assert!(install_permit(os, b).is_some());
                } else {
                    assert!(install_permit(os, b).is_none(), "{os:?} {b:?} must never reach the installer");
                }
            }
        }
        assert_eq!(installing, [(Macos, App), (Windows, Nsis), (Linux, AppImage)]);
        // The three packages that must only notify: a .deb (and the Arch package made from it), an .rpm, an unknown
        // binary, whatever system they run on.
        for os in all_os {
            for b in [Deb, Rpm, Unknown] {
                assert_eq!(kind(os, b), Kind::Notify, "{os:?} {b:?}");
            }
        }
        // An AppImage on macOS or an NSIS installer on Linux is not something that exists; neither installs.
        assert_eq!(kind(Macos, AppImage), Kind::Notify);
        assert_eq!(kind(Linux, Nsis), Kind::Notify);
        assert_eq!(kind(Windows, Msi), Kind::Notify, "there is no .msi; one would be told, not installed");
    }

    // ---- consent

    fn markers() -> (tempfile_dir::Dir, Markers) {
        let d = tempfile_dir::Dir::new();
        let m = Markers::new(d.path().join("config"));
        (d, m)
    }

    /// A scratch folder that goes away (no extra crate: the standard library is enough here).
    mod tempfile_dir {
        use std::path::{Path, PathBuf};
        pub struct Dir(PathBuf);
        impl Dir {
            pub fn new() -> Dir {
                let mut b = [0u8; 8];
                getrandom::fill(&mut b).unwrap();
                let p = std::env::temp_dir().join(format!("scriobh-policy-test-{}", crate::launch::hex(&b).as_str()));
                std::fs::create_dir_all(&p).unwrap();
                Dir(p)
            }
            pub fn path(&self) -> &Path {
                &self.0
            }
        }
        impl Drop for Dir {
            fn drop(&mut self) {
                let _ = std::fs::remove_dir_all(&self.0);
            }
        }
    }

    #[test]
    fn consent_is_unasked_until_answered_and_an_answer_is_kept() {
        let (_d, m) = markers();
        assert_eq!(m.consent(), Consent::Unasked);
        m.answer(false).unwrap();
        assert_eq!(m.consent(), Consent::No);
        m.answer(true).unwrap();
        assert_eq!(m.consent(), Consent::Yes);
        // The Help switch turns it off again, and the answer stays an answer.
        m.answer(false).unwrap();
        assert_eq!(m.consent(), Consent::No);
        // Another process, another start: the markers are what is kept.
        assert_eq!(Markers::new(m.dir.clone()).consent(), Consent::No);
        // A marker for yes without the one that says it was asked is not a yes.
        let (_d2, m2) = markers();
        fs::create_dir_all(&m2.dir).unwrap();
        fs::write(m2.dir.join(AUTOMATIC), b"").unwrap();
        assert_eq!(m2.consent(), Consent::Unasked);
    }

    #[test]
    fn an_answer_that_cannot_be_kept_is_not_a_yes() {
        let (d, _) = markers();
        let blocked = d.path().join("a-file");
        fs::write(&blocked, b"x").unwrap();
        let m = Markers::new(blocked.join("config"));
        assert!(m.answer(true).is_err());
        assert_eq!(m.consent(), Consent::Unasked);
    }

    #[test]
    fn the_time_of_the_last_attempt_is_a_markers_modification_time() {
        let (_d, m) = markers();
        assert_eq!(m.last_check(), None);
        m.record_attempt(t(0)).unwrap();
        assert_eq!(m.last_check(), Some(t(0)));
        m.record_attempt(t(500)).unwrap();
        assert_eq!(m.last_check(), Some(t(500)));
    }

    #[test]
    fn a_failed_install_is_said_once() {
        let (_d, m) = markers();
        assert_eq!(m.take_failure(), None);
        m.note_failure("2.3.0");
        assert_eq!(m.take_failure().as_deref(), Some("2.3.0"));
        assert_eq!(m.take_failure(), None);
    }

    // ---- the schedule

    struct Fake {
        now: Cell<SystemTime>,
        consent: Cell<Consent>,
        last: Cell<Option<SystemTime>>,
        checks: RefCell<Vec<u64>>,
        origin: SystemTime,
        stop_after: Duration,
        record_works: bool,
        /// At this many seconds the person flips the switch to this.
        flip: Option<(u64, Consent)>,
    }

    impl Fake {
        fn new(consent: Consent, days: u64) -> Fake {
            Fake {
                now: Cell::new(t(0)),
                consent: Cell::new(consent),
                last: Cell::new(None),
                checks: RefCell::new(Vec::new()),
                origin: t(0),
                stop_after: Duration::from_secs(days * 86_400),
                record_works: true,
                flip: None,
            }
        }
        fn at(&self) -> u64 {
            self.now.get().duration_since(self.origin).unwrap().as_secs()
        }
    }

    impl ScheduleEnv for Fake {
        fn consent(&self) -> Consent {
            self.consent.get()
        }
        fn last_check(&self) -> Option<SystemTime> {
            self.last.get()
        }
        fn record_attempt(&mut self, at: SystemTime) {
            if self.record_works {
                self.last.set(Some(at));
            }
        }
        fn now(&self) -> SystemTime {
            self.now.get()
        }
        fn wait(&mut self, d: Duration) -> bool {
            self.now.set(self.now.get() + d);
            if let Some((secs, c)) = self.flip {
                if self.at() >= secs {
                    self.consent.set(c);
                    self.flip = None;
                }
            }
            self.now.get().duration_since(self.origin).unwrap() < self.stop_after
        }
        fn check(&mut self) {
            let at = self.at();
            self.checks.borrow_mut().push(at);
        }
    }

    #[test]
    fn nothing_is_checked_before_the_question_is_answered_or_after_a_no() {
        for c in [Consent::Unasked, Consent::No] {
            let mut env = Fake::new(c, 10);
            run_schedule(&mut env);
            assert!(env.checks.borrow().is_empty(), "{c:?} made a check");
            assert_eq!(env.last.get(), None, "{c:?} recorded an attempt");
        }
        assert_eq!(wait_before_check(Consent::Unasked, None, t(0), Duration::from_secs(10_000)), None);
        assert_eq!(wait_before_check(Consent::No, None, t(0), Duration::from_secs(10_000)), None);
    }

    #[test]
    fn after_a_yes_the_first_check_waits_for_the_window_and_the_next_for_a_day() {
        let mut env = Fake::new(Consent::Yes, 3);
        run_schedule(&mut env);
        let at = env.checks.borrow().clone();
        assert_eq!(at.len(), 3, "{at:?}");
        assert!((30..=60).contains(&at[0]), "the first check came at {}", at[0]);
        assert!(at.windows(2).all(|w| w[1] - w[0] >= 86_400), "two checks less than a day apart: {at:?}");
        assert!(at.windows(2).all(|w| w[1] - w[0] <= 86_400 + 60), "a check was late by more than a poll: {at:?}");
    }

    #[test]
    fn a_start_within_a_day_of_the_last_attempt_does_not_check() {
        let mut env = Fake::new(Consent::Yes, 1);
        env.last.set(Some(t(0) - Duration::from_secs(3_600)));
        run_schedule(&mut env);
        let at = env.checks.borrow().clone();
        // The last attempt (a failed one counts) was an hour before the start: the next is due 23 hours after it.
        assert_eq!(at.len(), 1, "{at:?}");
        assert!((82_800..=82_860).contains(&at[0]), "{at:?}");
    }

    #[test]
    fn a_wrong_clock_cannot_silence_checks_for_more_than_a_day() {
        // The last attempt is a year in the future.
        let w = wait_before_check(
            Consent::Yes,
            Some(t(0) + Duration::from_secs(365 * 86_400)),
            t(0),
            Duration::from_secs(99),
        );
        assert_eq!(w, Some(INTERVAL));
    }

    #[test]
    fn the_help_switch_takes_effect_within_a_poll_and_a_no_stops_it_again() {
        let mut env = Fake::new(Consent::No, 3);
        env.flip = Some((100_000, Consent::Yes));
        run_schedule(&mut env);
        let at = env.checks.borrow().clone();
        assert!(!at.is_empty() && at[0] >= 100_000 && at[0] <= 100_000 + 60, "{at:?}");
        let mut env = Fake::new(Consent::Yes, 3);
        env.flip = Some((100_000, Consent::No));
        run_schedule(&mut env);
        assert!(env.checks.borrow().iter().all(|&a| a < 100_000));
    }

    #[test]
    fn a_marker_that_cannot_be_written_does_not_make_a_check_every_poll() {
        let mut env = Fake::new(Consent::Yes, 2);
        env.record_works = false;
        run_schedule(&mut env);
        let at = env.checks.borrow().clone();
        assert!(at.len() <= 2, "{at:?}");
    }

    // ---- the menu and the words

    #[test]
    fn the_menu_item_says_where_an_update_stands() {
        assert_eq!(label(&Phase::Idle), ("Check for Updates…".to_string(), true));
        assert_eq!(label(&Phase::Downloading("2.3.0".into())), ("Downloading Scríobh 2.3.0…".to_string(), false));
        assert_eq!(label(&Phase::Ready("2.3.0".into())), ("Restart to Update to 2.3.0…".to_string(), true));
        assert_eq!(
            label(&Phase::DownloadFailed("2.3.0".into())),
            ("Download of Scríobh 2.3.0 Failed. Try Again…".to_string(), true)
        );
        assert_eq!(
            label(&Phase::Available("2.3.0".into(), Why::NotByItself)),
            ("Scríobh 2.3.0 Is Available…".to_string(), true)
        );
        // A version from outside is cleaned before it is put in a menu.
        assert_eq!(label(&Phase::Ready("9\n\u{202E}x".into())).0, "Restart to Update to 9x…");
    }

    #[test]
    fn text_from_the_update_server_is_cut_and_cleaned() {
        assert_eq!(shown("2.1.0\n\u{1b}[31m", 40, false), "2.1.0[31m");
        assert_eq!(shown("a\nb\u{202E}c", 40, true), "a\nbc");
        assert_eq!(shown(&"x".repeat(100), 10, false).chars().count(), 11);
    }

    #[test]
    fn the_committed_configuration_has_no_update_source() {
        let conf: serde_json::Value = serde_json::from_str(include_str!("../tauri.conf.json")).unwrap();
        assert_eq!(update_source(conf["plugins"].get("updater")), None);
    }

    #[test]
    fn a_real_key_and_an_https_address_name_the_host() {
        let ok = json!({"pubkey": "dW50cnVzdGVkIGNvbW1lbnQ6IG1pbmlzaWduIHB1YmxpYyBrZXk6IDMwMDQ5MEI2OTYzNDUyQjY=", "endpoints": ["https://github.com/someone/scriobh/releases/latest/download/latest.json"]});
        assert_eq!(update_source(Some(&ok)).as_deref(), Some("github.com"));
    }

    #[test]
    fn placeholders_plain_http_and_missing_parts_are_not_a_source() {
        for bad in [
            json!({"pubkey": "REPLACE-ME", "endpoints": ["https://releases.example.org/x"]}),
            json!({"pubkey": "", "endpoints": ["https://releases.example.org/x"]}),
            json!({"pubkey": "abc", "endpoints": ["http://releases.example.org/x"]}),
            json!({"pubkey": "abc", "endpoints": ["https://releases.scriobh.invalid/x"]}),
            json!({"pubkey": "abc", "endpoints": []}),
            json!({"pubkey": "abc"}),
            json!({"endpoints": ["https://releases.example.org/x"]}),
            json!({"pubkey": "abc", "endpoints": ["not a url"]}),
        ] {
            assert_eq!(update_source(Some(&bad)), None, "{bad}");
        }
        assert_eq!(update_source(None), None);
    }

    #[test]
    fn the_release_page_is_derived_from_the_update_address_and_nothing_else() {
        let page = |e: &str| releases_page(e);
        assert_eq!(
            page("https://github.com/someone/scriobh/releases/latest/download/latest.json").as_deref(),
            Some("https://github.com/someone/scriobh/releases/latest")
        );
        for bad in [
            "https://evil.example/someone/scriobh/releases/latest/download/latest.json",
            "http://github.com/someone/scriobh/releases/latest/download/latest.json",
            "https://github.com/someone/scriobh/releases/download/v1/latest.json",
            "https://github.com/a b/scriobh/releases/latest/download/latest.json",
            "https://github.com/someone/scriobh/releases/latest/download/other.json",
            "https://github.com.evil.example/o/r/releases/latest/download/latest.json",
            "not a url",
        ] {
            assert_eq!(page(bad), None, "{bad}");
        }
    }

    // ---- where the app is

    #[test]
    fn the_app_is_found_from_its_program_and_judged_by_where_it_is() {
        let exe = Path::new("/Applications/Scriobh.app/Contents/MacOS/scriobh-desktop");
        let app = app_bundle_of(exe).unwrap();
        assert_eq!(app, Path::new("/Applications/Scriobh.app"));
        assert_eq!(app_bundle_of(Path::new("/usr/bin/scriobh-desktop")), None);
        assert_eq!(mac_location(app, false), Location::Fine, "/Applications: the plugin asks for the rights itself");
        assert_eq!(mac_location(Path::new("/Users/me/Apps/Scriobh.app"), true), Location::Fine);
        assert_eq!(mac_location(Path::new("/Users/me/Apps/Scriobh.app"), false), Location::NotWritable);
        assert_eq!(mac_location(Path::new("/Volumes/Scriobh 2.3.0/Scriobh.app"), true), Location::OnADiskImage);
        assert_eq!(
            mac_location(Path::new("/private/var/folders/x/AppTranslocation/ABC/d/Scriobh.app"), true),
            Location::Translocated
        );
        // Each says what to do and where the app is.
        for loc in [Location::OnADiskImage, Location::Translocated, Location::NotWritable] {
            let (title, body) = cannot_replace(loc, Path::new("/Users/me/Apps/Scriobh.app"));
            assert!(title.contains("cannot update") && body.contains("Applications folder"), "{loc:?}: {body}");
        }
        assert!(cannot_replace(Location::NotWritable, Path::new("/Users/me/Apps/Scriobh.app"))
            .1
            .contains("/Users/me/Apps"));
        assert!(can_write(&std::env::temp_dir()));
        assert!(!can_write(Path::new("/definitely/not/a/folder")));
    }

    // ---- save now

    fn run_wait(
        pages: Option<u32>,
        statuses: Vec<io::Result<SaveStatus>>,
    ) -> (SaveOutcome, Vec<Duration>, Option<usize>) {
        let slept = RefCell::new(Vec::new());
        let mut statuses = statuses.into_iter();
        let polled = Cell::new(0usize);
        let outcome = wait_for_pages(
            || match pages {
                Some(p) => Ok(SaveRound { n: 7, pages: p }),
                None => Err(io::Error::other("no server")),
            },
            |n| {
                assert_eq!(n, 7);
                polled.set(polled.get() + 1);
                statuses.next().unwrap_or(Ok(SaveStatus { pending: 1, unsaved: 0, gone: 0 }))
            },
            |d| slept.borrow_mut().push(d),
            Duration::from_secs(10),
            Duration::from_millis(250),
        );
        (outcome, slept.into_inner(), Some(polled.get()))
    }

    fn st(pending: u32, unsaved: u32) -> io::Result<SaveStatus> {
        Ok(SaveStatus { pending, unsaved, gone: 0 })
    }

    #[test]
    fn the_wait_for_the_pages_ends_on_their_answers_or_on_the_limit() {
        // nobody is asked: nothing to wait for (and not "unsaved")
        assert_eq!(run_wait(Some(0), vec![]).0, SaveOutcome::Saved { pages: 0 });
        // the server cannot be asked
        assert_eq!(run_wait(None, vec![]).0, SaveOutcome::NotAsked);
        // answered at once
        let (o, slept, polled) = run_wait(Some(1), vec![st(0, 0)]);
        assert_eq!((o, slept.len(), polled), (SaveOutcome::Saved { pages: 1 }, 0, Some(1)));
        // answered on the third look
        let (o, slept, _) = run_wait(Some(2), vec![st(2, 0), st(1, 0), st(0, 0)]);
        assert_eq!((o, slept.len()), (SaveOutcome::Saved { pages: 2 }, 2));
        // a page that could not save
        assert_eq!(run_wait(Some(1), vec![st(1, 0), st(0, 1)]).0, SaveOutcome::TextLeft { unsaved: 1 });
        // a page whose connection closed before it answered is not "saved"
        assert_eq!(
            run_wait(
                Some(2),
                vec![
                    Ok(SaveStatus { pending: 1, unsaved: 0, gone: 1 }),
                    Ok(SaveStatus { pending: 0, unsaved: 0, gone: 1 })
                ]
            )
            .0,
            SaveOutcome::PageGone { gone: 1 }
        );
        // text left outranks a page that is gone
        assert_eq!(
            run_wait(Some(2), vec![Ok(SaveStatus { pending: 0, unsaved: 1, gone: 1 })]).0,
            SaveOutcome::TextLeft { unsaved: 1 }
        );
        // never answered: ten seconds, and no more
        let (o, slept, polled) = run_wait(Some(1), vec![]);
        assert_eq!(o, SaveOutcome::TimedOut { pending: 1 });
        assert_eq!(slept.iter().sum::<Duration>(), Duration::from_secs(10));
        assert_eq!(polled, Some(41));
        // a status that fails now and then is waited out, not taken for an answer
        let (o, _, _) = run_wait(Some(1), vec![Err(io::Error::other("busy")), st(0, 0)]);
        assert_eq!(o, SaveOutcome::Saved { pages: 1 });
    }

    // ---- the words that matter

    #[test]
    fn the_restart_question_explains_the_macos_keychain_prompt_only_on_a_mac() {
        let (_, mac) = restart_question("2.3.0", true);
        assert!(
            mac.contains("Always Allow") && mac.contains("login password") && mac.contains("save your writing"),
            "{mac}"
        );
        let (_, other) = restart_question("2.3.0", false);
        assert!(!other.contains("Always Allow") && other.contains("save your writing"), "{other}");
    }

    #[test]
    fn the_first_question_says_what_is_asked_of_whom_and_exactly_what_is_revealed() {
        let (title, body) = first_run_question("github.com");
        assert_eq!(title, "Check for updates automatically?");
        for word in [
            "github.com",
            "once a day",
            "the same request for everyone",
            "does not say who you are",
            "what you have written",
            "which version you have",
            "internet address",
            "which kind",
            "Restart to Update",
            "Help menu",
        ] {
            assert!(body.to_lowercase().contains(&word.to_lowercase()), "the question does not say {word:?}: {body}");
        }
        // It must not claim that a download reveals nothing.
        assert!(!body.contains("sends nothing"), "{body}");
    }

    #[test]
    fn a_package_may_only_come_from_the_projects_own_release_at_its_tag() {
        let ep = "https://github.com/someone/scriobh/releases/latest/download/latest.json";
        let ok = |u: &str, v: &str| package_source_ok(ep, &tauri::Url::parse(u).unwrap(), v);
        let base = "https://github.com/someone/scriobh/releases/download/v2.3.0/";
        assert!(ok(&format!("{base}Scriobh_2.3.0_aarch64.app.tar.gz"), "2.3.0"));
        assert!(ok(&format!("{base}scriobh-desktop-v2.3.0-windows-amd64-setup.exe"), "2.3.0"));
        for bad in [
            "http://github.com/someone/scriobh/releases/download/v2.3.0/x.tar.gz",
            "https://github.com.evil.example/someone/scriobh/releases/download/v2.3.0/x.tar.gz",
            "https://evil.example/someone/scriobh/releases/download/v2.3.0/x.tar.gz",
            "https://github.com:8443/someone/scriobh/releases/download/v2.3.0/x.tar.gz",
            "https://user:pw@github.com/someone/scriobh/releases/download/v2.3.0/x.tar.gz",
            "https://github.com/other/scriobh/releases/download/v2.3.0/x.tar.gz",
            "https://github.com/someone/other/releases/download/v2.3.0/x.tar.gz",
            "https://github.com/someone/scriobh/releases/latest/download/x.tar.gz",
            "https://github.com/someone/scriobh/releases/download/v9.9.9/x.tar.gz",
            "https://github.com/someone/scriobh/releases/download/v2.3.0/x.tar.gz?token=1",
            "https://github.com/someone/scriobh/releases/download/v2.3.0/x.tar.gz#frag",
            "https://github.com/someone/scriobh/releases/download/v2.3.0/a/x.tar.gz",
            "https://github.com/someone/scriobh/releases/download/v2.3.0/",
            "https://github.com/someone/scriobh/archive/v2.3.0.tar.gz",
            "file:///tmp/x",
            // encoded slashes and dots, a host that only looks like github.com, a trailing dot, user information, another case of the owner
            "https://github.com/someone/scriobh/releases/download/v2.3.0/a%2Fb",
            "https://github.com/someone/scriobh/releases/download/v2.3.0/%2e%2e",
            "https://github.com/someone/scriobh/releases/download/v2.3.0/..%2F..%2Fx",
            "https://github.com/someone/scriobh/releases/download/v2.3.0%2Fx/y",
            "https://g\u{131}thub.com/someone/scriobh/releases/download/v2.3.0/x",
            "https://github.com./someone/scriobh/releases/download/v2.3.0/x",
            "https://github.com@evil.example/someone/scriobh/releases/download/v2.3.0/x",
            "https://evil.example@github.com/someone/scriobh/releases/download/v2.3.0/x",
            "https://github.com:443@github.com/someone/scriobh/releases/download/v2.3.0/x",
            "https://github.com/Someone/scriobh/releases/download/v2.3.0/x",
            "https://github.com/someone/scriobh/releases/download/V2.3.0/x",
            "https://github.com//someone/scriobh/releases/download/v2.3.0/x",
        ] {
            assert!(!ok(bad, "2.3.0"), "{bad}");
        }
        // The host is compared as the URL parser normalises it: the same host in capitals is the same host.
        assert!(ok("https://GITHUB.COM/someone/scriobh/releases/download/v2.3.0/x", "2.3.0"));
        assert!(
            ok("https://github.com:443/someone/scriobh/releases/download/v2.3.0/x", "2.3.0"),
            "the default port is no port"
        );
        assert!(!ok(&format!("{base}x"), "2.3.1"), "the tag must be the announced version's");
        // An update address that is not the project's GitHub one names no source at all.
        assert!(!package_source_ok(
            "https://releases.example.org/latest.json",
            &tauri::Url::parse(&format!("{base}x")).unwrap(),
            "2.3.0"
        ));
    }

    #[test]
    fn switching_off_that_cannot_be_done_is_an_error_not_a_success() {
        let (_d, m) = markers();
        m.answer(true).unwrap();
        // the marker cannot be removed as a file: it is a folder
        fs::remove_file(m.dir.join(AUTOMATIC)).unwrap();
        fs::create_dir(m.dir.join(AUTOMATIC)).unwrap();
        fs::write(m.dir.join(AUTOMATIC).join("x"), b"").unwrap();
        assert!(m.answer(false).is_err());
    }

    #[test]
    fn the_appimage_must_be_a_file_this_person_can_replace() {
        let img = Path::new("/home/me/Apps/Scriobh.AppImage");
        assert_eq!(appimage_problem(Some(img), true, true), None);
        assert_eq!(appimage_problem(None, true, true), Some(AppImageProblem::NotRunAsAnAppImage));
        assert_eq!(appimage_problem(Some(img), false, true), Some(AppImageProblem::NotWritable));
        assert_eq!(appimage_problem(Some(img), true, false), Some(AppImageProblem::NotWritable));
        for p in [AppImageProblem::NotRunAsAnAppImage, AppImageProblem::NotWritable] {
            assert!(cannot_replace_appimage(p).1.contains("Nothing was changed"));
        }
    }

    #[test]
    fn the_question_waits_for_a_page_with_no_place_to_type() {
        // Only the library and the Compendium list: no form, no text field that could take a key.
        for calm in ["/", "/compendiums"] {
            assert!(calm_page(calm), "{calm}");
        }
        for busy in [
            "/settings",
            "/settings/ai",
            "/import",
            "/import/git",
            "/search",
            "/desktop/identity",
            "/desktop/local-model",
            "/compendiums/new",
            "/compendiums/cmp_1",
            "/p/the-salt-road/d/doc_1",
            "/p/the-salt-road",
            "/desktop/profile",
            "/desktop/start",
            "/auth/login",
            "/settingsx",
            "/importer",
            "/desktop-idp/authorize",
            "//",
            "",
        ] {
            assert!(!calm_page(busy), "{busy}");
        }
        // Every window counts: one on a chapter makes the app not calm, and a writing page in any window is a writing window.
        let v = |a: &[&str]| a.iter().map(|s| s.to_string()).collect::<Vec<_>>();
        assert!(all_calm(&v(&["/"])) && all_calm(&v(&["/", "/compendiums"])));
        assert!(!all_calm(&v(&[])), "no window is not a calm window");
        assert!(!all_calm(&v(&["/", "/p/a/d/doc_1"])) && !all_calm(&v(&["/settings", "/"])));
        assert!(
            any_writing(&v(&["/", "/p/a/d/doc_1"])) && !any_writing(&v(&["/", "/settings"])) && !any_writing(&v(&[]))
        );
        assert!(is_writing_path("/p/the-salt-road/d/doc_01ABC"));
        assert!(is_writing_path("/p/the-salt-road/d/doc_01ABC/"));
        for not in ["/", "/p/the-salt-road", "/p/the-salt-road/outline", "/p//d/x", "/p/a/d/"] {
            assert!(!is_writing_path(not), "{not}");
        }
    }

    #[test]
    fn what_is_not_confirmed_saved_is_told_to_the_person() {
        use Unconfirmed::*;
        let open = true;
        assert_eq!(unconfirmed(SaveOutcome::Saved { pages: 2 }, open), None);
        assert_eq!(unconfirmed(SaveOutcome::Saved { pages: 0 }, false), None, "no writing page open: nothing to lose");
        assert_eq!(
            unconfirmed(SaveOutcome::Saved { pages: 0 }, open),
            Some(NobodyListening),
            "a writing page is open and nobody was asked"
        );
        assert_eq!(unconfirmed(SaveOutcome::TextLeft { unsaved: 1 }, false), Some(TextLeft));
        assert_eq!(unconfirmed(SaveOutcome::TimedOut { pending: 1 }, false), Some(TimedOut));
        assert_eq!(unconfirmed(SaveOutcome::PageGone { gone: 1 }, false), Some(PageGone));
        assert_eq!(unconfirmed(SaveOutcome::NotAsked, false), Some(NotAsked));
        assert_eq!(unconfirmed(SaveOutcome::NotAsked, open), Some(NotAsked));
        // and each says only what is true: it is not promised that the text comes back
        for why in [TextLeft, TimedOut, PageGone, NobodyListening, NotAsked] {
            let (title, body) = unconfirmed_text(why);
            assert!(
                title.contains("could not confirm")
                    && body.contains("cannot promise")
                    && body.contains("same local address"),
                "{why:?}: {body}"
            );
            assert!(!body.contains("put back when Scríobh reopens"), "{body}");
        }
    }
}
