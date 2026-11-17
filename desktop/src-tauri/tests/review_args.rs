//! A launch argument that is not valid UTF-8 must not stop the app: a file handed over by the system can be
//! named anything on Linux, and a lone surrogate on Windows. `run` reads `std::env::args()`, which panics.
//! Development build only (it uses the debug-only options). If the fix lets the app start, a window may flash
//! for a moment; the test closes it.
#![cfg(all(unix, debug_assertions))]

use std::ffi::OsString;
use std::io::Read;
use std::os::unix::ffi::OsStringExt;
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

#[test]
fn a_launch_argument_that_is_not_utf8_does_not_panic() {
    let tmp = std::path::Path::new(env!("CARGO_TARGET_TMPDIR")).join(format!("review-args-{}", std::process::id()));
    std::fs::create_dir_all(&tmp).unwrap();
    let mut child = Command::new(env!("CARGO_BIN_EXE_scriobh-desktop"))
        .arg("--data-dir")
        .arg(tmp.join("data"))
        .arg("--sidecar")
        .arg("/nonexistent/scriobh")
        .arg("--auto-dialogs")
        .arg(&tmp)
        .arg(OsString::from_vec(b"/tmp/caf\xe9.md".to_vec()))
        .stdout(Stdio::null())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    let began = Instant::now();
    let mut exited = None;
    while began.elapsed() < Duration::from_secs(3) {
        if let Some(s) = child.try_wait().unwrap() {
            exited = Some(s);
            break;
        }
        std::thread::sleep(Duration::from_millis(50));
    }
    if exited.is_none() {
        let _ = child.kill();
    }
    let _ = child.wait();
    let mut err = String::new();
    let _ = child.stderr.take().unwrap().read_to_string(&mut err);
    // The panic of `std::env::args()` quotes the argument; any other failure (no display on a headless runner,
    // for instance) does not, so only this one counts.
    assert!(!err.contains("caf\\xE9"), "the shell panicked on a non-UTF-8 argument (exit {exited:?}): {err}");
}
