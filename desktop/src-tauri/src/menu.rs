//! The native menus. Every item that opens something opens an ordinary page of the app in the same webview;
//! nothing here does what the web app cannot do in a browser.

use std::sync::OnceLock;

use tauri::menu::{
    AboutMetadataBuilder, CheckMenuItem, CheckMenuItemBuilder, Menu, MenuBuilder, MenuItem, MenuItemBuilder,
    PredefinedMenuItem, SubmenuBuilder,
};
use tauri::{AppHandle, Wry};

pub const IMPORT: &str = "file.import";
pub const EXPORT: &str = "file.export";
pub const PRINT: &str = "file.print";
pub const QUIT: &str = "app.quit";
pub const LIBRARY: &str = "go.library";
pub const SETTINGS: &str = "go.settings";
pub const RELOAD: &str = "view.reload";
pub const ZOOM_IN: &str = "view.zoom_in";
pub const ZOOM_OUT: &str = "view.zoom_out";
pub const ZOOM_RESET: &str = "view.zoom_reset";
pub const FULLSCREEN: &str = "view.fullscreen";
pub const DOCS: &str = "help.docs";
pub const DATA_FOLDER: &str = "help.data_folder";
pub const UPDATES: &str = "help.updates";
/// The same two items in the macOS application menu, where the notice is also seen (docs/UPDATES.md, decision 8).
pub const UPDATES_APP: &str = "app.updates";
pub const AUTO_UPDATES: &str = "help.auto_updates";
pub const AUTO_UPDATES_APP: &str = "app.auto_updates";

/// The menu items the update feature changes while the app runs: the one that checks, and says Restart to Update when
/// one is ready, and the switch for checking by itself. On macOS each is there twice (the application menu and Help).
pub struct UpdateItems {
    updates: Vec<MenuItem<Wry>>,
    automatic: Vec<CheckMenuItem<Wry>>,
}

static UPDATE_ITEMS: OnceLock<UpdateItems> = OnceLock::new();

/// Sets the text of the update item, and whether it can be chosen.
pub fn set_update_item(text: &str, enabled: bool) {
    if let Some(items) = UPDATE_ITEMS.get() {
        for item in &items.updates {
            let _ = item.set_text(text);
            let _ = item.set_enabled(enabled);
        }
    }
}

/// Sets the switch for checking by itself: whether it is on, and whether it can be used at all (a build with no update
/// source cannot check, so its switch is there and off).
pub fn set_automatic_item(checked: bool, enabled: bool) {
    if let Some(items) = UPDATE_ITEMS.get() {
        for item in &items.automatic {
            let _ = item.set_checked(checked);
            let _ = item.set_enabled(enabled);
        }
    }
}

/// The accelerators the shell claims, for the collision test and the notes. `CmdOrCtrl` is Command on macOS and
/// Control elsewhere. The Edit menu's are the platform's own (undo, redo, cut, copy, paste, select all).
pub const ACCELERATORS: &[(&str, &str)] = &[
    (IMPORT, "CmdOrCtrl+Shift+I"),
    (EXPORT, "CmdOrCtrl+Shift+E"),
    (PRINT, "CmdOrCtrl+P"),
    (QUIT, "CmdOrCtrl+Q"),
    (SETTINGS, "CmdOrCtrl+,"),
    (RELOAD, "CmdOrCtrl+R"),
    (ZOOM_IN, "CmdOrCtrl+="),
    (ZOOM_OUT, "CmdOrCtrl+-"),
    (ZOOM_RESET, "CmdOrCtrl+0"),
];

/// The chords the web app itself listens for with the same modifier (`docs/FEATURES.md` F-063, F-164, F-411, F-1224,
/// `web/editor/src/rules.js`, `web/static/js/palette.js`, `web/static/js/tree.js`). A native accelerator wins over the
/// page, so none of the shell's may be one of these. The list is the registry's Mod and Alt+Shift chords
/// (`internal/shortcuts`, F-1251): a Go test reads it and fails, naming the chord, when the two differ, so a chord is
/// added to the registry and here together.
pub const APP_CHORDS: &[&str] = &[
    "CmdOrCtrl+Z",
    "CmdOrCtrl+Shift+Z",
    "CmdOrCtrl+Y",
    "CmdOrCtrl+B",
    "CmdOrCtrl+I",
    "CmdOrCtrl+Shift+X",
    "CmdOrCtrl+K",
    "CmdOrCtrl+E",
    "CmdOrCtrl+S",
    "CmdOrCtrl+Enter",
    "CmdOrCtrl+Alt+0",
    "CmdOrCtrl+Alt+1",
    "CmdOrCtrl+Alt+2",
    "CmdOrCtrl+Alt+3",
    "CmdOrCtrl+O",
    "CmdOrCtrl+/",
    "CmdOrCtrl+\\",
    "CmdOrCtrl+Shift+P",
    "Alt+Shift+A",
    "Alt+Shift+N",
    "Alt+Shift+P",
    "Alt+Shift+O",
    "Alt+Shift+G",
    "Alt+Shift+S",
    "Alt+Shift+I",
    "Alt+Shift+J",
    "Alt+Shift+B",
    "Alt+Shift+C",
    "Alt+Shift+M",
];

fn accel(id: &str) -> &'static str {
    ACCELERATORS.iter().find(|(i, _)| *i == id).map(|(_, a)| *a).expect("every id used here is in the table")
}

pub fn build(app: &AppHandle) -> tauri::Result<Menu<tauri::Wry>> {
    let item = |id: &str, text: &str| MenuItemBuilder::with_id(id, text);
    let with = |id: &str, text: &str| MenuItemBuilder::with_id(id, text).accelerator(accel(id));

    let quit = with(QUIT, "Quit Scríobh").build(app)?;
    let settings = with(SETTINGS, "Settings…").build(app)?;
    let about = PredefinedMenuItem::about(
        app,
        Some("About Scríobh"),
        Some(
            AboutMetadataBuilder::new()
                .name(Some(crate::dialogs::APP_NAME))
                .version(Some(app.package_info().version.to_string()))
                .comments(Some("A private place to write long things. This window shows the Scríobh server running on this computer."))
                .build(),
        ),
    )?;
    let updates = item(UPDATES, "Check for Updates…").build(app)?;
    let automatic =
        CheckMenuItemBuilder::with_id(AUTO_UPDATES, "Check for Updates Automatically").checked(false).build(app)?;
    #[cfg(target_os = "macos")]
    let updates_app = item(UPDATES_APP, "Check for Updates…").build(app)?;
    #[cfg(target_os = "macos")]
    let automatic_app =
        CheckMenuItemBuilder::with_id(AUTO_UPDATES_APP, "Check for Updates Automatically").checked(false).build(app)?;
    let data_folder = item(DATA_FOLDER, "Show the Data Folder").build(app)?;

    let mut menu = MenuBuilder::new(app);

    // macOS puts About, Settings and Quit in the application menu; elsewhere they go where they are expected.
    #[cfg(target_os = "macos")]
    {
        let app_menu = SubmenuBuilder::new(app, crate::dialogs::APP_NAME)
            .item(&about)
            .item(&updates_app)
            .item(&automatic_app)
            .separator()
            .item(&settings)
            .separator()
            .services()
            .separator()
            .hide()
            .hide_others()
            .show_all()
            .separator()
            .item(&quit)
            .build()?;
        menu = menu.item(&app_menu);
    }

    let mut file = SubmenuBuilder::new(app, "File")
        .item(&item(LIBRARY, "Library").build(app)?)
        .separator()
        .item(&with(IMPORT, "Import…").build(app)?)
        .item(&with(EXPORT, "Export…").build(app)?)
        .separator()
        .item(&with(PRINT, "Print…").build(app)?)
        .separator()
        .close_window();
    if cfg!(not(target_os = "macos")) {
        file = file.item(&settings).separator().item(&quit);
    }
    menu = menu.item(&file.build()?);

    // Native roles: the operating system does the editing, so the clipboard, the input method and the editor's
    // own undo history all behave as they do in a browser.
    let edit = SubmenuBuilder::new(app, "Edit").undo().redo().separator().cut().copy().paste().select_all().build()?;
    menu = menu.item(&edit);

    let fullscreen = MenuItemBuilder::with_id(FULLSCREEN, "Toggle Full Screen")
        .accelerator(if cfg!(target_os = "macos") { "Ctrl+Cmd+F" } else { "F11" })
        .build(app)?;
    let view = SubmenuBuilder::new(app, "View")
        .item(&with(RELOAD, "Reload").build(app)?)
        .separator()
        .item(&with(ZOOM_IN, "Zoom In").build(app)?)
        .item(&with(ZOOM_OUT, "Zoom Out").build(app)?)
        .item(&with(ZOOM_RESET, "Actual Size").build(app)?)
        .separator()
        .item(&fullscreen)
        .build()?;
    menu = menu.item(&view);

    let window = SubmenuBuilder::new(app, "Window").minimize().maximize().build()?;
    menu = menu.item(&window);

    // The pair is in Help on every system, so the switch is always where the person looks for it.
    let mut help = SubmenuBuilder::new(app, "Help")
        .item(&item(DOCS, "Documentation").build(app)?)
        .item(&data_folder)
        .separator()
        .item(&updates)
        .item(&automatic);
    if cfg!(not(target_os = "macos")) {
        help = help.separator().item(&about);
    }
    menu = menu.item(&help.build()?);

    #[cfg(target_os = "macos")]
    let (all_updates, all_automatic) = (vec![updates, updates_app], vec![automatic, automatic_app]);
    #[cfg(not(target_os = "macos"))]
    let (all_updates, all_automatic) = (vec![updates], vec![automatic]);
    let _ = UPDATE_ITEMS.set(UpdateItems { updates: all_updates, automatic: all_automatic });

    menu.build()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn no_accelerator_is_a_chord_the_app_uses() {
        for (id, a) in ACCELERATORS {
            assert!(!APP_CHORDS.iter().any(|c| c.eq_ignore_ascii_case(a)), "{id} uses {a}, which the app uses");
        }
        for fs in ["Ctrl+Cmd+F", "F11"] {
            assert!(!APP_CHORDS.contains(&fs));
        }
    }

    #[test]
    fn accelerators_are_unique() {
        let mut seen = std::collections::HashSet::new();
        for (id, a) in ACCELERATORS {
            assert!(seen.insert(a.to_ascii_lowercase()), "{id} repeats {a}");
        }
    }
}
