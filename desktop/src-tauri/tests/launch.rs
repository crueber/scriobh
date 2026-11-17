//! The launch protocol, end to end, against the stand-in server in `desktop/testdata/standin`: a real child
//! process, real pipes, a real loopback socket. No window and no display.

use std::ffi::OsString;
use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::OnceLock;
use std::time::{Duration, Instant};

use scriobh_desktop::launch::{self, LaunchError, Shutdown, SpawnConfig};
use zeroize::Zeroizing;

/// Builds the stand-in once. Go is needed; under CI its absence is a failure, elsewhere the tests say they
/// were skipped.
fn standin() -> Option<&'static Path> {
    static BIN: OnceLock<Option<PathBuf>> = OnceLock::new();
    BIN.get_or_init(|| {
        let src = Path::new(env!("CARGO_MANIFEST_DIR")).join("..").join("testdata").join("standin");
        let out = Path::new(env!("CARGO_TARGET_TMPDIR")).join(if cfg!(windows) { "standin.exe" } else { "standin" });
        let built = Command::new("go").arg("build").arg("-o").arg(&out).arg(".").current_dir(&src).status();
        match built {
            Ok(s) if s.success() => Some(out),
            other => {
                assert!(std::env::var_os("CI").is_none(), "the stand-in server could not be built with go: {other:?}");
                eprintln!("SKIPPED: go is not available, so the stand-in server could not be built");
                None
            }
        }
    })
    .as_deref()
}

fn data_dir(name: &str) -> PathBuf {
    let d = Path::new(env!("CARGO_TARGET_TMPDIR")).join(format!("data-{name}-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&d);
    d
}

fn config(bin: &Path, name: &str, mode: &str) -> (SpawnConfig, Zeroizing<String>) {
    let token = launch::new_token().unwrap();
    let mut cfg = SpawnConfig::new(bin.to_path_buf(), token.clone());
    cfg.data_dir = Some(data_dir(name));
    cfg.startup_timeout = Duration::from_secs(20);
    if !mode.is_empty() {
        cfg.extra_env.push((OsString::from("STANDIN_MODE"), OsString::from(mode)));
    }
    (cfg, token)
}

fn get(port: u16, path: &str) -> String {
    launch::loopback_get(port, path, "", 1 << 16).unwrap()
}

#[test]
fn a_launch_follows_the_contract_and_a_quit_is_clean() {
    let Some(bin) = standin() else { return };
    let (mut cfg, token) = config(bin, "happy", "");
    let key = Zeroizing::new("5e".repeat(32));
    cfg.secret_key = Some(key.clone());
    // What a login profile might leave lying around must not reach the server.
    cfg.base_env.push((OsString::from("SCRIOBH_BASE_URL"), OsString::from("https://elsewhere.example")));
    cfg.base_env.push((OsString::from("SCRIOBH_OIDC_STUB"), OsString::from("1")));
    let mut server = launch::spawn(cfg).expect("the stand-in starts");
    assert!(server.port > 0);

    let report = get(server.port, "/standin/report");
    let body = report.split("\r\n\r\n").nth(1).unwrap();
    let r: serde_json::Value = serde_json::from_str(body).unwrap();
    assert_eq!(r["argc"], 0, "the server is started with no arguments");
    assert_eq!(r["token_in_argv"], false);
    assert_eq!(r["key_in_argv"], false);
    assert_eq!(r["key_len"], 64);
    assert_eq!(r["stdin_is_a_pipe"], true);
    assert_eq!(
        r["scriobh_names"],
        serde_json::json!([
            "SCRIOBH_DATA_DIR",
            "SCRIOBH_DESKTOP",
            "SCRIOBH_LAUNCH_TOKEN",
            "SCRIOBH_LISTEN",
            "SCRIOBH_SECRET_KEY"
        ])
    );
    assert!(!report.contains(token.as_str()) && !report.contains(key.as_str()));

    // The address the webview is given works once and sets the launch cookie.
    let start = server.start_url(&token).expect("the server is alive");
    let path = start
        .strip_prefix(&format!("http://127.0.0.1:{}", server.port))
        .expect("a loopback address on the printed port");
    assert_eq!(get(server.port, "/desktop/start?t=wrong").split(' ').nth(1), Some("404"));
    let first = get(server.port, path);
    assert!(first.starts_with("HTTP/1.1 303"), "{first}");
    assert!(first.contains("Set-Cookie: scriobh_desktop_launch="));
    assert!(first.contains("Location: /\r\n"));
    assert_eq!(get(server.port, path).split(' ').nth(1), Some("404"), "the token is accepted once");

    let began = Instant::now();
    assert_eq!(server.shutdown(Duration::from_secs(10)), Shutdown::Clean(Some(0)));
    assert!(began.elapsed() < Duration::from_secs(5));
    assert!(launch::loopback_get(server.port, "/healthz", "", 64).is_err(), "nothing listens after the quit");
    assert!(matches!(server.shutdown(Duration::from_secs(1)), Shutdown::AlreadyGone(_)));
}

#[test]
fn without_a_key_the_variable_is_absent_not_empty() {
    let Some(bin) = standin() else { return };
    let (cfg, _) = config(bin, "nokey", "");
    let mut server = launch::spawn(cfg).unwrap();
    let report = get(server.port, "/standin/report");
    assert!(!report.contains("SCRIOBH_SECRET_KEY"), "{report}");
    server.shutdown(Duration::from_secs(5));
}

#[test]
fn a_second_server_on_the_same_data_reports_already_running() {
    let Some(bin) = standin() else { return };
    let (cfg, _) = config(bin, "twice", "");
    let dir = cfg.data_dir.clone();
    let mut first = launch::spawn(cfg).unwrap();
    let (mut again, _) = config(bin, "unused", "");
    again.data_dir = dir;
    match launch::spawn(again) {
        Err(LaunchError::AlreadyRunning) => {}
        other => panic!("expected AlreadyRunning, got {:?}", other.map(|r| r.port)),
    }
    // The first is untouched by the second's attempt.
    assert!(get(first.port, "/healthz").starts_with("HTTP/1.1 200"));
    assert_eq!(first.shutdown(Duration::from_secs(5)), Shutdown::Clean(Some(0)));
}

#[test]
fn a_server_that_never_prints_its_address_times_out_with_its_last_words() {
    let Some(bin) = standin() else { return };
    let (mut cfg, _) = config(bin, "nolisten", "no-listen");
    cfg.startup_timeout = Duration::from_millis(1200);
    let began = Instant::now();
    match launch::spawn(cfg) {
        Err(LaunchError::Timeout { tail }) => assert!(tail.contains("never comes"), "{tail:?}"),
        other => panic!("expected Timeout, got {:?}", other.map(|r| r.port)),
    }
    assert!(began.elapsed() < Duration::from_secs(8), "the wait is bounded and the child is not left behind");
}

#[test]
fn a_server_that_listens_but_is_never_healthy_times_out() {
    let Some(bin) = standin() else { return };
    let (mut cfg, _) = config(bin, "unhealthy", "unhealthy");
    cfg.startup_timeout = Duration::from_millis(1500);
    assert!(matches!(launch::spawn(cfg), Err(LaunchError::Timeout { .. })));
}

#[test]
fn a_server_that_dies_at_once_gives_its_status_and_its_last_lines() {
    let Some(bin) = standin() else { return };
    let (cfg, _) = config(bin, "early", "die-early");
    match launch::spawn(cfg) {
        Err(LaunchError::Exited { code, tail }) => {
            assert_eq!(code, Some(3));
            assert!(tail.contains("migration 0042 failed") && tail.contains("newer version"), "{tail:?}");
        }
        other => panic!("expected Exited, got {:?}", other.map(|r| r.port)),
    }
}

#[test]
fn a_server_that_prints_the_token_or_the_key_does_not_get_them_shown() {
    let Some(bin) = standin() else { return };
    let (mut cfg, token) = config(bin, "leak", "leak");
    let key = Zeroizing::new("c4".repeat(32));
    cfg.secret_key = Some(key.clone());
    match launch::spawn(cfg) {
        Err(LaunchError::Exited { tail, .. }) => {
            assert!(tail.contains("starting with token"), "{tail:?}");
            assert!(!tail.contains(token.as_str()), "the launch token reached the error text");
            assert!(!tail.contains(key.as_str()), "the key reached the error text");
            assert!(!tail.chars().any(|c| c.is_control() && c != '\n'), "{tail:?}");
            assert!(tail.contains("coloured"));
        }
        other => panic!("expected Exited, got {:?}", other.map(|r| r.port)),
    }
}

#[cfg(unix)]
#[test]
fn a_server_that_ignores_the_request_is_killed_after_the_grace_period() {
    let Some(bin) = standin() else { return };
    let (cfg, _) = config(bin, "stubborn", "ignore-term");
    let mut server = launch::spawn(cfg).unwrap();
    let port = server.port;
    let began = Instant::now();
    assert_eq!(server.shutdown(Duration::from_millis(800)), Shutdown::Killed);
    let took = began.elapsed();
    assert!(took >= Duration::from_millis(800) && took < Duration::from_secs(5), "{took:?}");
    assert!(launch::loopback_get(port, "/healthz", "", 64).is_err());
}

#[test]
fn a_server_is_not_left_behind_when_its_handle_is_dropped() {
    let Some(bin) = standin() else { return };
    let (cfg, _) = config(bin, "dropped", "");
    let server = launch::spawn(cfg).unwrap();
    let port = server.port;
    drop(server);
    std::thread::sleep(Duration::from_millis(200));
    assert!(launch::loopback_get(port, "/healthz", "", 64).is_err());
}

#[test]
fn a_program_that_is_not_there_is_a_spawn_error() {
    let token = launch::new_token().unwrap();
    let cfg = SpawnConfig::new(PathBuf::from("/nonexistent/scriobh-not-here"), token);
    assert!(matches!(launch::spawn(cfg), Err(LaunchError::Spawn(_))));
}

#[test]
fn a_crash_after_start_is_seen_with_its_last_line() {
    let Some(bin) = standin() else { return };
    let (cfg, _) = config(bin, "later", "die-after");
    let mut server = launch::spawn(cfg).unwrap();
    let deadline = Instant::now() + Duration::from_secs(10);
    let status = loop {
        if let Some(s) = server.try_exit() {
            break s;
        }
        assert!(Instant::now() < deadline, "the stand-in was meant to exit");
        std::thread::sleep(Duration::from_millis(50));
    };
    assert_eq!(status.code(), Some(1));
    std::thread::sleep(Duration::from_millis(150));
    assert!(server.tail_text().contains("out of disk space"));
}

/// A request with a Host of the test's choosing, which `launch::loopback_get` does not allow.
fn get_as(port: u16, host: &str, path: &str, headers: &str) -> String {
    use std::io::{Read, Write};
    let mut raw = std::net::TcpStream::connect(("127.0.0.1", port)).unwrap();
    raw.set_read_timeout(Some(Duration::from_secs(5))).unwrap();
    write!(raw, "GET {path} HTTP/1.1\r\nHost: {host}\r\nConnection: close\r\n{headers}\r\n").unwrap();
    let mut out = String::new();
    raw.read_to_string(&mut out).unwrap();
    out
}

fn status(response: &str) -> &str {
    response.split(' ').nth(1).unwrap_or("")
}

/// The stand-in keeps to the contract as the Go side has it, so that what these tests and the smoke prove is the
/// current one (contract changes 7 and 10). `tests/real_server.rs` asks the same of the real binary.
#[test]
fn the_stand_in_answers_only_to_its_own_address_and_has_no_lockout() {
    let Some(bin) = standin() else { return };
    let (cfg, token) = config(bin, "contract", "");
    let mut server = launch::spawn(cfg).unwrap();
    let port = server.port;

    // Host: only 127.0.0.1:<port>, on every route.
    for path in ["/healthz", "/", "/desktop/start?t=x"] {
        assert_eq!(status(&get_as(port, &format!("localhost:{port}"), path, "")), "421", "{path}");
        assert_eq!(status(&get_as(port, &format!("127.0.0.1:{}", port.wrapping_add(1)), path, "")), "421");
        assert_eq!(status(&get_as(port, "example.com", path, "")), "421");
    }
    assert_eq!(status(&get_as(port, &format!("127.0.0.1:{port}"), "/healthz", "")), "200");

    // A request from another site does not spend the token; twelve wrong ones lock nothing.
    let path = format!("/desktop/start?t={}", token.as_str());
    assert_eq!(status(&launch::loopback_get(port, &path, "Sec-Fetch-Site: cross-site\r\n", 256).unwrap()), "404");
    for i in 0..12 {
        let wrong = format!("/desktop/start?t={}{i:04}", "0".repeat(60));
        assert_eq!(status(&get(port, &wrong)), "404");
    }
    let ok = get(port, &path);
    assert!(ok.starts_with("HTTP/1.1 303"), "the right token still works after twelve wrong ones: {ok}");
    assert_eq!(status(&get(port, &path)), "404", "and only once");
    server.shutdown(Duration::from_secs(5));
}

/// Contract change 23: the shell holds the server's standard input, and the end of it is the clean stop. This is
/// what stops a server whose shell was killed.
#[test]
fn the_stand_in_stops_by_itself_when_its_standard_input_closes() {
    use std::io::{BufRead, BufReader};
    use std::process::Stdio;
    let Some(bin) = standin() else { return };
    let (cfg, _) = config(bin, "eof", "");
    let mut child = Command::new(bin)
        .env_clear()
        .envs(launch::child_env(&cfg))
        .stdin(Stdio::piped())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .unwrap();
    let stdin = child.stdin.take().unwrap();
    let mut line = String::new();
    BufReader::new(child.stdout.take().unwrap()).read_line(&mut line).unwrap();
    assert!(line.starts_with("LISTEN http://127.0.0.1:"), "{line}");
    std::thread::sleep(Duration::from_millis(300));
    assert!(child.try_wait().unwrap().is_none(), "it must not stop while its standard input is open");
    drop(stdin);
    let began = Instant::now();
    let status = loop {
        if let Some(s) = child.try_wait().unwrap() {
            break s;
        }
        if began.elapsed() > Duration::from_secs(10) {
            let _ = child.kill();
            panic!("the stand-in outlived its shell");
        }
        std::thread::sleep(Duration::from_millis(50));
    };
    assert!(status.success(), "{status:?}");
}

/// A server that was started with no pipe on its standard input (by hand, from a service manager) is not a shell's
/// child and is never stopped by the end of it.
#[cfg(unix)]
#[test]
fn the_stand_in_ignores_a_standard_input_that_is_not_a_pipe() {
    use std::io::{BufRead, BufReader};
    use std::process::Stdio;
    let Some(bin) = standin() else { return };
    let (cfg, _) = config(bin, "devnull", "");
    let mut child = Command::new(bin)
        .env_clear()
        .envs(launch::child_env(&cfg))
        .stdin(Stdio::null())
        .stdout(Stdio::piped())
        .stderr(Stdio::null())
        .spawn()
        .unwrap();
    let mut line = String::new();
    BufReader::new(child.stdout.take().unwrap()).read_line(&mut line).unwrap();
    std::thread::sleep(Duration::from_millis(500));
    assert!(child.try_wait().unwrap().is_none(), "/dev/null reaches its end at once and must not stop the server");
    let _ = child.kill();
    let _ = child.wait();
}

/// Contract change 24: the port bound last time is bound again when it is free, so the origin survives a restart.
#[test]
fn the_stand_in_comes_back_on_the_port_it_had() {
    let Some(bin) = standin() else { return };
    let (cfg, _) = config(bin, "port", "");
    let dir = cfg.data_dir.clone();
    let mut first = launch::spawn(cfg).unwrap();
    let port = first.port;
    assert_eq!(first.shutdown(Duration::from_secs(5)), Shutdown::Clean(Some(0)));
    let (mut again, _) = config(bin, "unused", "");
    again.data_dir = dir;
    let mut second = launch::spawn(again).unwrap();
    assert_eq!(second.port, port, "the origin changed across a restart");
    // Taken by someone else: any free port, and that one is remembered instead.
    assert_eq!(second.shutdown(Duration::from_secs(5)), Shutdown::Clean(Some(0)));
    let held = std::net::TcpListener::bind(("127.0.0.1", port));
    let (mut third, _) = config(bin, "unused2", "");
    third.data_dir = Some(Path::new(env!("CARGO_TARGET_TMPDIR")).join(format!("data-port-{}", std::process::id())));
    let mut third = launch::spawn(third).unwrap();
    if held.is_ok() {
        assert_ne!(third.port, port, "a port in use cannot be bound again");
    }
    third.shutdown(Duration::from_secs(5));
}

/// Any program can answer `/healthz`, and the window is sent the launch token at the port. What answers has to show
/// it holds the token, to a challenge that is new every time (Go change 30).
#[test]
fn a_program_that_answers_healthz_without_the_proof_is_not_sent_the_token() {
    let Some(bin) = standin() else { return };
    for (mode, name) in [("no-proof", "noproof"), ("wrong-proof", "wrongproof")] {
        let (cfg, _) = config(bin, name, mode);
        let began = Instant::now();
        match launch::spawn(cfg) {
            Err(LaunchError::NotTheServer { .. }) => {}
            other => panic!("{mode}: expected NotTheServer, got {:?}", other.map(|r| r.port)),
        }
        assert!(began.elapsed() < Duration::from_secs(10), "{mode}: refused at once, not after the timeout");
    }
}

#[test]
fn the_token_is_not_sent_to_a_program_that_took_the_port_after_the_first_look() {
    let Some(bin) = standin() else { return };
    // Right for the first challenge, wrong after: the first check is spawn's, the second is the last look before the
    // window is given its address.
    let (cfg, token) = config(bin, "proofonce", "proof-once");
    let mut server = launch::spawn(cfg).expect("the first challenge was answered");
    assert_eq!(server.start_url(&token), Err(launch::StartRefused::NotTheServer));
    server.shutdown(Duration::from_secs(10));
}

#[test]
fn the_right_proof_gets_the_address_and_each_challenge_is_new() {
    let Some(bin) = standin() else { return };
    let (cfg, token) = config(bin, "proof", "");
    let mut server = launch::spawn(cfg).unwrap();
    // Two requests with the same nonce get the same proof; the shell never sends one twice.
    let nonce = "ab".repeat(32);
    let ask = |n: &str| {
        launch::loopback_get(server.port, "/healthz", &format!("X-Scriobh-Challenge: {n}\r\n"), 4096).unwrap()
    };
    let (a, b) = (ask(&nonce), ask(&"cd".repeat(32)));
    let proof = |r: &str| {
        r.lines().find_map(|l| l.strip_prefix("X-Scriobh-Proof: ")).map(str::to_owned).expect("a proof header")
    };
    assert_eq!(proof(&a), launch::expected_proof(&token, &nonce));
    assert_ne!(proof(&a), proof(&b));
    // No challenge, no proof; a malformed challenge, no proof.
    assert!(!launch::loopback_get(server.port, "/healthz", "", 4096).unwrap().contains("X-Scriobh-Proof"));
    assert!(!ask("not a nonce").contains("X-Scriobh-Proof"));
    assert!(server.start_url(&token).unwrap().contains("/desktop/start?t="));
    server.shutdown(Duration::from_secs(10));
}
