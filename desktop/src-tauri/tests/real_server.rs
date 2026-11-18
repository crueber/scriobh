//! The launch contract against the real server, not the stand-in. `desktop/scripts/test.sh` (which `make
//! desktop-test` and the desktop workflow run) sets `SCRIOBH_REAL_SERVER` to the production server it built from
//! this tree (`go build -tags prod ./cmd/scriobh`, see `desktop/scripts/sidecar.sh`) and refuses to run without
//! it. Run by hand with `SCRIOBH_REAL_SERVER=/path/to/scriobh cargo test --locked --test real_server`; without the
//! variable each test says it was skipped and passes.
//!
//! The stand-in (`desktop/testdata/standin`) follows the same contract (no lockout, Host check, no spending of the
//! token by a cross-site request, stop on end of input, the port kept across restarts, the proof on `/healthz`), and
//! `tests/launch.rs` asks it the same questions; these tests ask the real binary, which is what keeps the stand-in
//! honest. They pin the contract as the Go side built it (docs/p8-notes/desktop-contract.md, changes 7, 10, 23, 24, 30).

use std::ffi::OsString;
use std::io::{BufRead, BufReader};
use std::path::{Path, PathBuf};
use std::process::{Command, Stdio};
use std::time::{Duration, Instant};

use scriobh_desktop::launch::{self, LaunchError, Shutdown, SpawnConfig};
use zeroize::Zeroizing;

fn real() -> Option<PathBuf> {
    let p = std::env::var_os("SCRIOBH_REAL_SERVER").map(PathBuf::from);
    if p.is_none() {
        eprintln!("SKIPPED: SCRIOBH_REAL_SERVER is not set");
    }
    p
}

fn data(name: &str) -> PathBuf {
    let d = Path::new(env!("CARGO_TARGET_TMPDIR")).join(format!("real-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&d);
    d
}

fn config(bin: &Path, dir: &Path) -> (SpawnConfig, Zeroizing<String>) {
    let token = launch::new_token().unwrap();
    let mut cfg = SpawnConfig::new(bin.to_path_buf(), token.clone());
    cfg.data_dir = Some(dir.to_path_buf());
    cfg.startup_timeout = Duration::from_secs(60);
    (cfg, token)
}

fn status_of(response: &str) -> &str {
    response.split(' ').nth(1).unwrap_or("")
}

#[test]
fn real_server_follows_the_current_contract() {
    let Some(bin) = real() else { return };
    let dir = data("contract");
    let (mut cfg, token) = config(&bin, &dir);
    cfg.secret_key = Some(Zeroizing::new("7c".repeat(32)));
    let mut server = launch::spawn(cfg).expect("the real server starts");
    let port = server.port;

    // Change 10: only the bound address is a Host. `localhost` is refused, the shell's own address works.
    let localhost = launch::loopback_get(port, "/healthz", "", 256).unwrap();
    assert!(localhost.starts_with("HTTP/1.1 200"), "{localhost}");
    let mut raw = std::net::TcpStream::connect(("127.0.0.1", port)).unwrap();
    use std::io::{Read, Write};
    write!(raw, "GET /healthz HTTP/1.1\r\nHost: localhost:{port}\r\nConnection: close\r\n\r\n").unwrap();
    let mut out = String::new();
    raw.read_to_string(&mut out).unwrap();
    assert_eq!(status_of(&out), "421", "{out}");

    // Change 30: /healthz proves, to a fresh challenge, that the server holds the launch token. `spawn` already
    // required it. A shell that held another token would not be given the server's address, and the right one is.
    assert!(server.start_url(&token).unwrap().contains("/desktop/start?t="));
    assert_eq!(server.start_url(&"1".repeat(64)), Err(launch::StartRefused::NotTheServer));
    let plain = launch::loopback_get(port, "/healthz", "", 4096).unwrap();
    assert!(plain.starts_with("HTTP/1.1 200") && !plain.contains("X-Scriobh-Proof"), "{plain}");
    let nonce = "ab".repeat(32);
    let asked = launch::loopback_get(port, "/healthz", &format!("X-Scriobh-Challenge: {nonce}\r\n"), 4096).unwrap();
    assert!(asked.contains(&format!("X-Scriobh-Proof: {}", launch::expected_proof(&token, &nonce))), "{asked}");

    // Change 7: a request from another site does not spend the token; wrong guesses do not lock anything.
    let path = format!("/desktop/start?t={}", token.as_str());
    let cross = launch::loopback_get(port, &path, "Sec-Fetch-Site: cross-site\r\n", 512).unwrap();
    assert_eq!(status_of(&cross), "404", "{cross}");
    for i in 0..12 {
        let wrong =
            launch::loopback_get(port, &format!("/desktop/start?t={}", "0".repeat(60) + &format!("{i:04}")), "", 256)
                .unwrap();
        assert_eq!(status_of(&wrong), "404");
    }
    let ok = launch::loopback_get(port, &path, "", 4096).unwrap();
    assert!(ok.starts_with("HTTP/1.1 303"), "the right token must still work after twelve wrong ones: {ok}");
    assert!(ok.contains("scriobh_desktop_launch="));
    assert_eq!(status_of(&launch::loopback_get(port, &path, "", 256).unwrap()), "404", "the token is accepted once");

    // The key travelled in the environment only: no key file was made in the data folder.
    assert!(!dir.join("secret.key").exists(), "a key file was made although the shell passed a key");

    assert_eq!(server.shutdown(Duration::from_secs(15)), Shutdown::Clean(Some(0)));

    // Without a key the server makes its own file: this is why a failed keychain on existing data matters.
    let (cfg, _) = config(&bin, &dir);
    let mut again = launch::spawn(cfg).unwrap();
    assert!(dir.join("secret.key").exists());
    again.shutdown(Duration::from_secs(15));
}

#[test]
fn real_server_exits_by_itself_when_its_standard_input_closes() {
    let Some(bin) = real() else { return };
    let dir = data("eof");
    let (cfg, _) = config(&bin, &dir);
    let mut child = Command::new(&bin)
        .env_clear()
        .envs(launch::child_env(&cfg))
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .unwrap();
    let stdin = child.stdin.take().unwrap();
    let mut lines = BufReader::new(child.stdout.take().unwrap());
    let mut line = String::new();
    lines.read_line(&mut line).unwrap();
    assert!(line.starts_with("LISTEN http://127.0.0.1:"), "{line}");
    drop(stdin); // what the shell's death does
    let began = Instant::now();
    let status = loop {
        if let Some(s) = child.try_wait().unwrap() {
            break s;
        }
        if began.elapsed() > Duration::from_secs(10) {
            let _ = child.kill();
            panic!("the server outlived its shell");
        }
        std::thread::sleep(Duration::from_millis(50));
    };
    assert!(status.success(), "{status:?}");
}

/// What `kill -9` of the shell does: standard input, output and error lose their other end at once. The server's log
/// line about it must not end the process on SIGPIPE (it did, with status 13, before the clean stop and the checkpoint).
#[test]
fn real_server_stops_cleanly_when_its_input_output_and_error_all_lose_their_reader() {
    let Some(bin) = real() else { return };
    let dir = data("sigpipe");
    let (cfg, _) = config(&bin, &dir);
    let mut child = Command::new(&bin)
        .env_clear()
        .envs(launch::child_env(&cfg))
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::piped())
        .spawn()
        .unwrap();
    let stdin = child.stdin.take().unwrap();
    let stderr = child.stderr.take().unwrap();
    let mut out = BufReader::new(child.stdout.take().unwrap());
    // The first line is read on a thread, so that a server that never prints it fails the test instead of hanging it.
    let (tx, rx) = std::sync::mpsc::channel();
    let reader = std::thread::spawn(move || {
        let mut line = String::new();
        let _ = out.read_line(&mut line);
        let _ = tx.send(line);
        out // handed back so that it is dropped (closed) by the test, after the line was seen
    });
    let Ok(line) = rx.recv_timeout(Duration::from_secs(60)) else {
        let _ = child.kill();
        panic!("no LISTEN line within 60 s");
    };
    assert!(line.starts_with("LISTEN http://127.0.0.1:"), "{line}");
    drop(reader.join().unwrap());
    drop(stderr);
    drop(stdin);
    let began = Instant::now();
    let status = loop {
        if let Some(s) = child.try_wait().unwrap() {
            break s;
        }
        if began.elapsed() > Duration::from_secs(10) {
            let _ = child.kill();
            panic!("the server outlived its shell");
        }
        std::thread::sleep(Duration::from_millis(50));
    };
    // The exit status carries this test. That the log was checkpointed is proved with writes in the Go twin,
    // `TestDesktopSurvivesClosedOutput`: here nothing was written, so an empty log would prove nothing.
    assert!(status.success(), "{status:?}: a closed output ended the server before its clean stop");
}

#[test]
fn real_server_comes_back_on_the_same_port_and_a_second_one_is_already_running() {
    let Some(bin) = real() else { return };
    let dir = data("port");
    let (cfg, _) = config(&bin, &dir);
    let mut first = launch::spawn(cfg).unwrap();
    let port = first.port;

    let (cfg2, _) = config(&bin, &dir);
    assert!(matches!(launch::spawn(cfg2), Err(LaunchError::AlreadyRunning)));
    assert_eq!(first.shutdown(Duration::from_secs(15)), Shutdown::Clean(Some(0)));

    let (cfg3, _) = config(&bin, &dir);
    let mut second = launch::spawn(cfg3).unwrap();
    if second.port != port {
        // Tolerated only when something else holds the old port now (the tests of this file start servers of their
        // own in parallel), as the Go twin of this test does: the port is then in use, and taking a free one is
        // the contract.
        assert!(
            std::net::TcpListener::bind(("127.0.0.1", port)).is_err(),
            "the origin (and what the webview keeps for it) changed across a restart although the port {port} was free"
        );
        eprintln!("the old port {port} was taken by another program; a free one ({}) was used", second.port);
    }
    second.shutdown(Duration::from_secs(15));
}

#[test]
fn the_shell_environment_names_are_the_ones_the_real_server_accepts() {
    let Some(bin) = real() else { return };
    // A profile that sets one of the refused variables must not stop the app: the shell drops it.
    let dir = data("env");
    let (mut cfg, _) = config(&bin, &dir);
    cfg.base_env.push((OsString::from("SCRIOBH_BASE_URL"), OsString::from("https://elsewhere.example")));
    cfg.base_env.push((OsString::from("SCRIOBH_ADMIN_GROUP"), OsString::from("admins")));
    cfg.base_env.push((OsString::from("SCRIOBH_TRUST_PROXY"), OsString::from("true")));
    let mut s = launch::spawn(cfg).expect("variables from a login profile must not reach the server");
    s.shutdown(Duration::from_secs(15));
}

/// "Save now", before an update: the key the shell derives from its launch token is the one the real server derives,
/// so the shell can ask, and a request without it, or with the healthz proof's key, is turned away as an unknown address.
#[test]
fn real_server_answers_save_now_to_the_shells_key_and_to_nothing_else() {
    let Some(bin) = real() else { return };
    let dir = data("savenow");
    let (cfg, token) = config(&bin, &dir);
    let mut server = launch::spawn(cfg).expect("the real server starts");
    let (port, key) = server.shell_access();
    assert_eq!(key.as_str(), launch::shell_key(&token).as_str());

    // No writing page has a live stream: nobody was asked, which is what the shell is told.
    let round = launch::save_now(port, &key).expect("the shell's key is accepted");
    assert_eq!(round.pages, 0, "{round:?}");
    assert_eq!(
        launch::save_status(port, &key, round.n).unwrap(),
        launch::SaveStatus { pending: 0, unsaved: 0, gone: 0 }
    );
    // A second round is the next number; asking for a round that is over is refused.
    let next = launch::save_now(port, &key).unwrap();
    assert_eq!(next.n, round.n + 1);
    assert!(launch::save_status(port, &key, round.n).is_err(), "an old round is over");

    // Another key, the token itself, and no key at all get the page an unknown address gets.
    for wrong in
        [&token.as_str(), &"0".repeat(64).as_str(), &launch::expected_proof(&token, &"ab".repeat(32)).as_str(), &""]
    {
        assert!(launch::save_now(port, wrong).is_err(), "save-now accepted {wrong:?}");
    }
    let bare = launch::loopback_get(port, "/desktop/save-status?n=1", "", 512).unwrap();
    assert_eq!(status_of(&bare), "404", "{bare}");
    server.shutdown(Duration::from_secs(15));
}
