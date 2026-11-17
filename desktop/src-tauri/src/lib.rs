//! The desktop shell for Scríobh.
//!
//! A window, native menus and a process supervisor around the unchanged server binary. The shell starts the
//! server on a loopback port, shows it, and stops it. It adds nothing to the pages, exposes nothing to them, and
//! reads no file on the app's behalf. `docs/DESKTOP.md` says what it is; `docs/p8-notes/desktop-shell.md` says
//! why each piece is the way it is.

pub mod datadir;
pub mod deeplink;
pub mod filename;
pub mod keychain;
pub mod launch;
pub mod menu;
pub mod nav;

mod app;
mod dialogs;
mod downloads;
mod updater;

pub use app::run;
