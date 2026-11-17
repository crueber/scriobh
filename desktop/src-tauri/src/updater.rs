//! Updates, only when asked.
//!
//! The shell never looks for an update by itself. Help, Check for Updates… is the only way a check happens, and
//! it asks first, naming the host it will contact. The request is one HTTPS GET for a small JSON file; what is
//! downloaded is installed only if its minisign signature matches the public key built into the app.

use tauri::{AppHandle, Manager};
use tauri_plugin_updater::UpdaterExt;

use crate::dialogs;

/// The host updates come from, or `None` when this build has no real update source (the placeholder key or
/// address is still in the configuration, as it is in the repository).
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

/// Text from the update server, made safe to show: printable, one line unless `lines`, and short.
fn shown(text: &str, max: usize, lines: bool) -> String {
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

pub fn check(app: &AppHandle) {
    let Some(host) = update_source(app.config().plugins.0.get("updater")) else {
        dialogs::info(
            app,
            "Updates are not set up in this build",
            "This copy of Scríobh was built without an update source, so it cannot look for a newer version. Nothing was contacted.",
        );
        return;
    };
    let handle = app.clone();
    dialogs::ask(
        app,
        "Check for updates?",
        &format!(
            "Scríobh will ask {host} over HTTPS whether a newer version exists. Nothing about you or your writing is sent. Scríobh never checks by itself."
        ),
        "Check Now",
        "Cancel",
        move |yes| {
            if yes {
                tauri::async_runtime::spawn(run(handle));
            }
        },
    );
}

async fn run(app: AppHandle) {
    let failed = |app: &AppHandle| {
        dialogs::info(
            app,
            "Could not check for updates",
            "The update server did not answer, or its answer could not be read. Nothing was changed.",
        );
    };
    let Ok(updater) = app.updater() else { return failed(&app) };
    let update = match updater.check().await {
        Ok(Some(update)) => update,
        Ok(None) => return dialogs::info(&app, "Scríobh is up to date", "There is no newer version."),
        Err(_) => return failed(&app),
    };
    let version = shown(&update.version, 40, false);
    let notes = update.body.as_deref().map(|b| shown(b, 600, true)).unwrap_or_default();
    let body = format!(
        "You have {}. Scríobh will stop its server cleanly, install the new version and start again.{}",
        shown(&update.current_version, 40, false),
        if notes.is_empty() { String::new() } else { format!("\n\n{notes}") }
    );
    let handle = app.clone();
    dialogs::ask(
        &app,
        &format!("Version {version} is available"),
        &body,
        "Install and Restart",
        "Not Now",
        move |yes| {
            if yes {
                tauri::async_runtime::spawn(install(handle, update));
            }
        },
    );
}

async fn install(app: AppHandle, update: tauri_plugin_updater::Update) {
    // The download is checked against the public key before anything is touched.
    let bytes = match update.download(|_, _| {}, || {}).await {
        Ok(bytes) => bytes,
        Err(_) => {
            return dialogs::info(
                &app,
                "The update was not installed",
                "The download failed or its signature did not match. Nothing was changed.",
            );
        }
    };
    // Stop the server before the installer replaces anything, so the last write is on disk and no server is left
    // behind on a platform where the installer ends this process.
    let stopper = app.clone();
    let _ = tauri::async_runtime::spawn_blocking(move || crate::app::stop_server(&stopper)).await;
    // Hidden, not closed: closing the last window would end the app in the middle of the install.
    for (_, w) in app.webview_windows() {
        let _ = w.hide();
    }
    if update.install(bytes).is_err() {
        // Nothing sensible is running now; starting again brings back the version that was here.
        eprintln!("scriobh-desktop: the update could not be installed; restarting the installed version");
    }
    app.restart();
}

#[cfg(test)]
mod tests {
    use super::*;
    use serde_json::json;

    #[test]
    fn the_committed_configuration_has_no_update_source() {
        let conf: serde_json::Value = serde_json::from_str(include_str!("../tauri.conf.json")).unwrap();
        assert_eq!(update_source(conf["plugins"].get("updater")), None);
    }

    #[test]
    fn a_real_key_and_an_https_address_name_the_host() {
        let ok = json!({"pubkey": "dW50cnVzdGVkIGNvbW1lbnQ6IG1pbmlzaWdu", "endpoints": ["https://releases.example.org/latest.json"]});
        assert_eq!(update_source(Some(&ok)).as_deref(), Some("releases.example.org"));
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
    fn text_from_the_update_server_is_cut_and_cleaned() {
        assert_eq!(shown("2.1.0\n\u{1b}[31m", 40, false), "2.1.0[31m");
        assert_eq!(shown("a\nb\u{202E}c", 40, true), "a\nbc");
        assert_eq!(shown(&"x".repeat(100), 10, false).chars().count(), 11);
    }
}
