//! Review findings about the launch code, as tests that fail today. Unix only: the "server" is a small `sh` script.
#![cfg(unix)]

use std::io::{Read, Write};
use std::net::TcpListener;
use std::os::unix::fs::PermissionsExt;
use std::path::PathBuf;
use std::time::Duration;

use scriobh_desktop::launch::{self, LaunchError, SpawnConfig};

fn script(name: &str, body: &str) -> PathBuf {
    let path = PathBuf::from(env!("CARGO_TARGET_TMPDIR")).join(format!("review-{name}-{}.sh", std::process::id()));
    std::fs::write(&path, format!("#!/bin/sh\n{body}\n")).unwrap();
    std::fs::set_permissions(&path, std::fs::Permissions::from_mode(0o755)).unwrap();
    path
}

/// `/healthz` is asked before the child's exit is looked at (launch.rs `spawn`, step 2), so a server that prints
/// `LISTEN` and dies at once is reported ready if anything else answers 200 on that port. The shell then opens
/// the window at `/desktop/start?t=<token>` on that port: the launch token goes to the other program.
#[test]
fn a_child_that_has_exited_is_not_ready_because_something_else_answers_healthz() {
    let listener = TcpListener::bind("127.0.0.1:0").unwrap();
    let port = listener.local_addr().unwrap().port();
    std::thread::spawn(move || {
        for stream in listener.incoming() {
            let Ok(mut s) = stream else { return };
            let mut buf = [0u8; 512];
            let _ = s.read(&mut buf);
            let _ = s.write_all(b"HTTP/1.1 200 OK\r\nContent-Length: 0\r\nConnection: close\r\n\r\n");
        }
    });
    let program = script("dies-after-listen", &format!("echo 'LISTEN http://127.0.0.1:{port}'\nexit 3"));
    let mut cfg = SpawnConfig::new(program, launch::new_token().unwrap());
    cfg.startup_timeout = Duration::from_secs(10);
    match launch::spawn(cfg) {
        Err(_) => {}
        Ok(mut running) => {
            std::thread::sleep(Duration::from_millis(300));
            let port = running.port;
            let exited = running.try_exit();
            panic!(
                "spawn returned a ready server on port {port} whose process had exited ({exited:?}); the token URL would go to whatever holds the port"
            );
        }
    }
}

/// `read_lines` cuts a line at 8192 bytes and hands the pieces to `scrub` one by one, so a token that straddles
/// the cut is in neither piece whole and is shown (the second piece is a line of its own in the tail).
#[test]
fn a_secret_that_straddles_the_line_limit_is_still_removed_from_the_tail() {
    let program = script("straddle", "printf '%8190s%s\\n' '' \"$SCRIOBH_LAUNCH_TOKEN\" >&2\nexit 3");
    let token = launch::new_token().unwrap();
    let mut cfg = SpawnConfig::new(program, token.clone());
    cfg.startup_timeout = Duration::from_secs(10);
    match launch::spawn(cfg) {
        Err(LaunchError::Exited { tail, .. }) => {
            let rest = &token[2..];
            assert!(!tail.contains(rest), "the last 62 of the token's 64 characters reached the error text");
        }
        other => panic!("expected Exited, got {:?}", other.map(|r| r.port)),
    }
}
