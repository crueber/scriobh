//! Starting, watching and stopping the server.
//!
//! The server is the ordinary `scriobh` binary, started as a child of the shell. The shell talks to it over the
//! child's standard streams and over loopback HTTP, and in no other way. See `docs/p8-notes/desktop-contract.md`
//! for the protocol; the steps the shell owns are here.

use std::collections::VecDeque;
use std::ffi::{OsStr, OsString};
use std::io::{BufRead, BufReader, Read, Write};
use std::net::{Ipv4Addr, SocketAddr, TcpStream};
use std::path::PathBuf;
use std::process::{Child, ChildStdin, Command, ExitStatus, Stdio};
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::mpsc::{self, RecvTimeoutError};
use std::sync::{Arc, Mutex};
use std::thread;
use std::time::{Duration, Instant};

use hmac::{Hmac, KeyInit, Mac};
use sha2::Sha256;
use zeroize::Zeroizing;

/// How long the server has to print its address and answer `/healthz`.
pub const STARTUP_TIMEOUT: Duration = Duration::from_secs(30);
/// How long the server has to exit after it is asked to.
pub const SHUTDOWN_GRACE: Duration = Duration::from_secs(10);
/// The exit status of a server that found another one holding the data directory.
pub const EXIT_ALREADY_RUNNING: i32 = 75;

const TAIL_LINES: usize = 60;
const TAIL_LINE_BYTES: usize = 600;
const MAX_LINE_BYTES: usize = 8192;
const REDACTED: &str = "[removed]";
/// How long the server is given, after its first good `/healthz`, to show that it is still there.
const SETTLE: Duration = Duration::from_millis(100);

/// Lower-case hex, in a buffer that is wiped when dropped.
pub fn hex(bytes: &[u8]) -> Zeroizing<String> {
    const DIGITS: &[u8; 16] = b"0123456789abcdef";
    let mut s = Zeroizing::new(String::with_capacity(bytes.len() * 2));
    for b in bytes {
        s.push(DIGITS[(b >> 4) as usize] as char);
        s.push(DIGITS[(b & 15) as usize] as char);
    }
    s
}

/// A launch token: 32 bytes from the operating system, as 64 hex characters. It exists in this process's memory,
/// in the child's environment and, once, in the address the webview first loads.
pub fn new_token() -> std::io::Result<Zeroizing<String>> {
    let mut raw = Zeroizing::new([0u8; 32]);
    getrandom::fill(&mut raw[..]).map_err(|e| std::io::Error::other(e.to_string()))?;
    Ok(hex(&raw[..]))
}

/// Reads the server's `LISTEN http://127.0.0.1:<port>` line. Nothing else is accepted: not another host, not
/// `https`, not a path, not a port of zero.
pub fn parse_listen(line: &str) -> Option<u16> {
    let rest = line.strip_suffix('\r').unwrap_or(line).strip_prefix("LISTEN http://127.0.0.1:")?;
    if rest.is_empty() || rest.len() > 5 || rest.starts_with('0') || !rest.bytes().all(|b| b.is_ascii_digit()) {
        return None;
    }
    rest.parse::<u16>().ok().filter(|p| *p != 0)
}

/// What the shell needs to start the server.
pub struct SpawnConfig {
    pub program: PathBuf,
    pub token: Zeroizing<String>,
    /// From the keychain. `None` lets the server use its key file.
    pub secret_key: Option<Zeroizing<String>>,
    /// Only for development and tests: the contract leaves the data directory to the server.
    pub data_dir: Option<PathBuf>,
    /// The environment the child starts from. Every `SCRIOBH_*` name in it is dropped.
    pub base_env: Vec<(OsString, OsString)>,
    /// Extra variables for tests (the stand-in's failure modes). Never `SCRIOBH_*`.
    pub extra_env: Vec<(OsString, OsString)>,
    pub startup_timeout: Duration,
    /// Development builds: also copy the server's standard error, scrubbed, to the shell's own.
    pub echo_stderr: bool,
}

impl SpawnConfig {
    pub fn new(program: PathBuf, token: Zeroizing<String>) -> Self {
        SpawnConfig {
            program,
            token,
            secret_key: None,
            data_dir: None,
            base_env: std::env::vars_os().collect(),
            extra_env: Vec::new(),
            startup_timeout: STARTUP_TIMEOUT,
            echo_stderr: false,
        }
    }
}

fn is_scriobh_name(name: &OsStr) -> bool {
    name.to_string_lossy().to_ascii_uppercase().starts_with("SCRIOBH_")
}

/// The child's whole environment: what the shell was started with, minus every `SCRIOBH_*` name (so a variable
/// left in a login profile cannot move the data directory, set a base URL or switch a mode), plus the contract's.
pub fn child_env(cfg: &SpawnConfig) -> Vec<(OsString, OsString)> {
    let mut env: Vec<(OsString, OsString)> =
        cfg.base_env.iter().filter(|(k, _)| !is_scriobh_name(k)).cloned().collect();
    env.extend(cfg.extra_env.iter().filter(|(k, _)| !is_scriobh_name(k)).cloned());
    let mut set = |k: &str, v: &OsStr| env.push((OsString::from(k), v.to_os_string()));
    set("SCRIOBH_DESKTOP", OsStr::new("1"));
    set("SCRIOBH_LISTEN", OsStr::new("127.0.0.1:0"));
    set("SCRIOBH_LAUNCH_TOKEN", OsStr::new(cfg.token.as_str()));
    if let Some(key) = &cfg.secret_key {
        set("SCRIOBH_SECRET_KEY", OsStr::new(key.as_str()));
    }
    if let Some(dir) = &cfg.data_dir {
        set("SCRIOBH_DATA_DIR", dir.as_os_str());
    }
    env
}

/// The last lines of the server's standard error, for the error window.
#[derive(Default)]
struct Tail {
    lines: VecDeque<String>,
}

impl Tail {
    fn push(&mut self, line: &str) {
        let mut cut = line.len().min(TAIL_LINE_BYTES);
        while !line.is_char_boundary(cut) {
            cut -= 1;
        }
        if self.lines.len() == TAIL_LINES {
            self.lines.pop_front();
        }
        self.lines.push_back(line[..cut].to_string());
    }
}

/// Removes anything that must not be shown from text that came from the server: the launch token and the key,
/// should the server ever print them, and every control character except the line break.
fn scrub(text: &str, secrets: &[Zeroizing<String>]) -> String {
    let mut out = text.to_string();
    for s in secrets {
        if !s.is_empty() {
            out = out.replace(s.as_str(), REDACTED);
        }
    }
    out.chars().filter(|c| *c == '\n' || !c.is_control()).collect()
}

/// A line that was cut at the length limit may end in the first part of a secret whose rest was dropped. That part
/// is not a whole secret, so [`scrub`] would not find it; it is removed here. Call it on text that has been
/// scrubbed already: on a whole secret at the very end it would remove only a shorter start of it.
fn without_partial_secret<'a>(text: &'a str, secrets: &[Zeroizing<String>]) -> &'a str {
    let mut keep = text.len();
    for s in secrets {
        let s = s.as_str();
        for k in (1..s.len()).rev() {
            if s.is_char_boundary(k) && text.ends_with(&s[..k]) {
                keep = keep.min(text.len() - k);
                break;
            }
        }
    }
    &text[..keep]
}

#[derive(Debug)]
pub enum LaunchError {
    /// The program could not be started at all.
    Spawn(String),
    /// Another server holds the data directory (exit status 75).
    AlreadyRunning,
    /// The server exited before it was ready.
    Exited { code: Option<i32>, tail: String },
    /// No `LISTEN` line, or `/healthz` never answered 200, within the time allowed.
    Timeout { tail: String },
    /// Something answered `/healthz` with 200 at the server's port but could not prove that it holds the launch
    /// token (no proof, or the wrong one): it is not the server this shell started, or not a server that can be
    /// told from another program. The token was not sent to it, and the server that was started has been stopped.
    NotTheServer { tail: String },
}

/// Why the window was not given its address.
#[derive(Debug, PartialEq, Eq)]
pub enum StartRefused {
    /// The server has exited: the token is not offered to a port nobody is serving from any more.
    Gone,
    /// What answers at the port could not prove that it holds the launch token.
    NotTheServer,
    /// The server has not exited but did not answer `/healthz` in time, over several tries (a stalled machine, or
    /// a server that is stuck): it is not said to be another program.
    NoAnswer,
}

#[derive(Debug, PartialEq, Eq)]
pub enum Shutdown {
    /// The server exited by itself after it was asked to.
    Clean(Option<i32>),
    /// The grace period ran out and the server was killed.
    Killed,
    /// It had already exited.
    AlreadyGone(Option<i32>),
}

/// A running server.
pub struct Running {
    pub port: u16,
    child: Child,
    stdin: Option<ChildStdin>,
    tail: Arc<Mutex<Tail>>,
    secrets: Vec<Zeroizing<String>>,
}

/// Calls `each(line, truncated)` for every line the stream gives. A line is kept up to `MAX_LINE_BYTES` and the
/// rest of a longer one is read and dropped (`truncated` is then true), so nothing grows without bound and a line
/// is never handed over in pieces: a secret cannot straddle a cut that the caller does not know about.
fn read_lines(stream: impl Read + Send + 'static, mut each: impl FnMut(&str, bool) + Send + 'static) {
    thread::spawn(move || {
        let mut reader = BufReader::new(stream);
        let mut line: Vec<u8> = Vec::with_capacity(256);
        let mut truncated = false;
        loop {
            let chunk = match reader.fill_buf() {
                Ok(chunk) if !chunk.is_empty() => chunk,
                // The end of the stream, or an error: what was read last still counts as a line.
                _ => {
                    if !line.is_empty() {
                        each(String::from_utf8_lossy(&line).trim_end_matches(['\n', '\r']), truncated);
                    }
                    return;
                }
            };
            let (used, end) = match chunk.iter().position(|b| *b == b'\n') {
                Some(i) => (i + 1, true),
                None => (chunk.len(), false),
            };
            let room = MAX_LINE_BYTES.saturating_sub(line.len());
            line.extend_from_slice(&chunk[..used.min(room)]);
            truncated |= used > room;
            reader.consume(used);
            if end {
                each(String::from_utf8_lossy(&line).trim_end_matches(['\n', '\r']), truncated);
                line.clear();
                truncated = false;
            }
        }
    });
}

/// Starts the server and waits until it is ready: the `LISTEN` line, then `GET /healthz` answering 200.
pub fn spawn(cfg: SpawnConfig) -> Result<Running, LaunchError> {
    let deadline = Instant::now() + cfg.startup_timeout;
    let mut cmd = Command::new(&cfg.program);
    // No arguments: the token and the key travel in the environment only, where another user's `ps` cannot see them.
    cmd.env_clear().envs(child_env(&cfg)).stdin(Stdio::piped()).stdout(Stdio::piped()).stderr(Stdio::piped());
    #[cfg(windows)]
    {
        use std::os::windows::process::CommandExt;
        const CREATE_NO_WINDOW: u32 = 0x0800_0000;
        cmd.creation_flags(CREATE_NO_WINDOW);
    }
    let mut child = cmd.spawn().map_err(|e| LaunchError::Spawn(e.to_string()))?;
    drop(cmd);

    let mut secrets = vec![cfg.token.clone()];
    if let Some(k) = &cfg.secret_key {
        secrets.push(k.clone());
    }
    let tail = Arc::new(Mutex::new(Tail::default()));
    let already = Arc::new(AtomicBool::new(false));
    let (port_tx, port_rx) = mpsc::channel::<u16>();

    let stdout = child.stdout.take().expect("stdout was piped");
    let stderr = child.stderr.take().expect("stderr was piped");
    {
        let already = already.clone();
        let mut sent = false;
        read_lines(stdout, move |line, truncated| {
            // A line that was cut is never one of the two this reads.
            if truncated {
                return;
            }
            if line == "ALREADY_RUNNING" {
                already.store(true, Ordering::SeqCst);
            }
            if !sent {
                if let Some(port) = parse_listen(line) {
                    sent = port_tx.send(port).is_ok();
                }
            }
        });
    }
    {
        let tail = tail.clone();
        let already = already.clone();
        let secrets = secrets.clone();
        let echo = cfg.echo_stderr;
        read_lines(stderr, move |line, truncated| {
            if line == "ALREADY_RUNNING" && !truncated {
                already.store(true, Ordering::SeqCst);
            }
            // The whole line is scrubbed before anything is cut from it (the tail keeps 600 bytes of it), so a
            // cut can never leave part of a secret behind.
            let clean = scrub(line, &secrets);
            let clean = if truncated { without_partial_secret(&clean, &secrets) } else { &clean };
            if echo {
                eprintln!("server: {clean}");
            }
            if let Ok(mut t) = tail.lock() {
                t.push(clean);
            }
        });
    }

    let mut running = Running { port: 0, stdin: child.stdin.take(), child, tail, secrets };

    // Step 1: the address.
    let port = loop {
        match port_rx.recv_timeout(Duration::from_millis(50)) {
            Ok(port) => break port,
            Err(RecvTimeoutError::Timeout) => {}
            // Standard output was closed without a LISTEN line; the exit or the deadline below decides.
            Err(RecvTimeoutError::Disconnected) => thread::sleep(Duration::from_millis(50)),
        }
        if let Some(status) = running.try_exit() {
            // Let the readers drain what the child wrote last.
            thread::sleep(Duration::from_millis(100));
            return Err(exited(status, &already, &running));
        }
        if Instant::now() >= deadline {
            let tail = running.tail_text();
            running.shutdown(Duration::from_secs(2));
            return Err(LaunchError::Timeout { tail });
        }
    };
    running.port = port;

    // Step 2: ready. `/healthz` is answered by whoever holds the port, and the window will be sent the launch
    // token there, so the child has to be the one that is alive and answering: it is looked at before the first
    // request, after the first 200 (once more after a short settle, because a server that prints its address and
    // dies at once may not have been reaped yet), and again just before the window is opened (`start_url`).
    loop {
        if let Some(status) = running.try_exit() {
            thread::sleep(Duration::from_millis(100));
            return Err(exited(status, &already, &running));
        }
        match healthz(port, &cfg.token) {
            Health::Proven => {
                thread::sleep(SETTLE.min(deadline.saturating_duration_since(Instant::now())));
                match running.try_exit() {
                    Some(status) => {
                        thread::sleep(Duration::from_millis(100));
                        return Err(exited(status, &already, &running));
                    }
                    None => return Ok(running),
                }
            }
            // Something answers, and it is not the server that was started (or it cannot show that it is). Nothing
            // is sent to it, and waiting longer will not change what it is.
            Health::Unproven => {
                // A child that died just now, with another program answering in its place, is a server that
                // exited (with its status, and the already-running classification), not one that was swapped.
                thread::sleep(Duration::from_millis(100));
                if let Some(status) = running.try_exit() {
                    thread::sleep(Duration::from_millis(100));
                    return Err(exited(status, &already, &running));
                }
                let tail = running.tail_text();
                running.shutdown(Duration::from_secs(2));
                return Err(LaunchError::NotTheServer { tail });
            }
            Health::Down => {}
        }
        if Instant::now() >= deadline {
            let tail = running.tail_text();
            running.shutdown(Duration::from_secs(2));
            return Err(LaunchError::Timeout { tail });
        }
        thread::sleep(Duration::from_millis(100));
    }
}

fn exited(status: ExitStatus, already: &AtomicBool, running: &Running) -> LaunchError {
    if status.code() == Some(EXIT_ALREADY_RUNNING) || already.load(Ordering::SeqCst) {
        LaunchError::AlreadyRunning
    } else {
        LaunchError::Exited { code: status.code(), tail: running.tail_text() }
    }
}

/// The most one look at the server may take, from connecting to the last byte: a program that sends a byte every
/// second or so would otherwise keep one request going for as long as the byte limit allows.
const LOOK_DEADLINE: Duration = Duration::from_secs(4);

/// One plain HTTP/1.1 request to the server on loopback, with short timeouts. No proxy setting applies: this is
/// a socket to 127.0.0.1 and nothing else. Returns the head and body as read, at most `limit` bytes. The whole
/// exchange is over within [`LOOK_DEADLINE`]: an answer still arriving then is a `TimedOut` error.
pub fn loopback_get(port: u16, path: &str, extra_headers: &str, limit: usize) -> std::io::Result<String> {
    request(port, path, extra_headers, limit, false)
}

/// As [`loopback_get`], but reading stops at the end of the response head (the blank line), which is all a
/// check of headers needs. `limit` bounds the head.
fn request(port: u16, path: &str, extra_headers: &str, limit: usize, head_only: bool) -> std::io::Result<String> {
    let began = Instant::now();
    let timed_out = || std::io::Error::new(std::io::ErrorKind::TimedOut, "the answer took too long");
    let addr = SocketAddr::from((Ipv4Addr::LOCALHOST, port));
    let mut stream = TcpStream::connect_timeout(&addr, Duration::from_millis(1000))?;
    stream.set_write_timeout(Some(Duration::from_millis(2000)))?;
    write!(stream, "GET {path} HTTP/1.1\r\nHost: 127.0.0.1:{port}\r\nConnection: close\r\n{extra_headers}\r\n")?;
    let mut out: Vec<u8> = Vec::new();
    let mut chunk = [0u8; 1024];
    while out.len() < limit {
        let left = LOOK_DEADLINE.saturating_sub(began.elapsed());
        if left.is_zero() {
            return Err(timed_out());
        }
        stream.set_read_timeout(Some(left.min(Duration::from_millis(2000))))?;
        let want = chunk.len().min(limit - out.len());
        match stream.read(&mut chunk[..want]) {
            Ok(0) => break,
            Ok(n) => {
                // Only the part that can complete the blank line needs looking at.
                let from = out.len().saturating_sub(3);
                out.extend_from_slice(&chunk[..n]);
                if head_only && out[from..].windows(4).any(|w| w == b"\r\n\r\n") {
                    break;
                }
            }
            Err(e) if e.kind() == std::io::ErrorKind::Interrupted => {}
            Err(e) if matches!(e.kind(), std::io::ErrorKind::WouldBlock | std::io::ErrorKind::TimedOut) => {
                return Err(timed_out());
            }
            Err(e) => return Err(e),
        }
    }
    Ok(String::from_utf8_lossy(&out).into_owned())
}

/// What `/healthz` said.
#[derive(Debug, PartialEq, Eq)]
pub enum Health {
    /// 200, with the proof that only a process holding the launch token can make.
    Proven,
    /// 200, without the proof or with a wrong one.
    Unproven,
    /// No answer, or one that is not 200 (the server may still be starting).
    Down,
}

/// The header the shell puts a fresh nonce in, and the one the server answers in (Go change 30 in
/// `docs/p8-notes/desktop-contract.md`).
const CHALLENGE_HEADER: &str = "X-Scriobh-Challenge";
const PROOF_HEADER: &str = "x-scriobh-proof";

/// What the server must answer to `nonce` (64 lower-case hex characters, as sent) when it holds `token`:
/// HMAC-SHA-256 under k of the nonce's characters, where k is HMAC-SHA-256 under the token's characters of the
/// fixed message `scriobh desktop healthz key v1`. The proof is never the token, and a proof made for one nonce is
/// no use for another, so a program that only listened to an earlier answer cannot give this one.
fn healthz_mac(token: &str, nonce: &str) -> Hmac<Sha256> {
    let mut key = Hmac::<Sha256>::new_from_slice(token.as_bytes()).expect("HMAC takes a key of any length");
    key.update(b"scriobh desktop healthz key v1");
    let mut derived = Zeroizing::new([0u8; 32]);
    derived.copy_from_slice(&key.finalize().into_bytes());
    let mut mac = Hmac::<Sha256>::new_from_slice(&derived[..]).expect("HMAC takes a key of any length");
    mac.update(nonce.as_bytes());
    mac
}

/// The proof the server should give for `nonce`, as lower-case hex. Public so that a test (and a stand-in server)
/// can say what the right answer is; the check itself is [`proof_matches`].
pub fn expected_proof(token: &str, nonce: &str) -> String {
    hex(&healthz_mac(token, nonce).finalize().into_bytes()[..]).to_string()
}

/// Whether the response head carries exactly one proof header, and it is the right one for `nonce`. The comparison
/// is the library's constant-time one, on the decoded bytes.
fn proof_matches(head: &str, token: &str, nonce: &str) -> bool {
    let head = head.split("\r\n\r\n").next().unwrap_or("");
    let mut values = head.split("\r\n").skip(1).filter_map(|line| {
        let (name, value) = line.split_once(':')?;
        name.eq_ignore_ascii_case(PROOF_HEADER).then(|| value.trim())
    });
    let (Some(value), None) = (values.next(), values.next()) else { return false };
    if value.len() != 64 || !value.bytes().all(|b| b.is_ascii_digit() || (b'a'..=b'f').contains(&b)) {
        return false;
    }
    let mut bytes = [0u8; 32];
    for (i, pair) in value.as_bytes().chunks(2).enumerate() {
        let digit = |b: u8| if b <= b'9' { b - b'0' } else { b - b'a' + 10 };
        bytes[i] = digit(pair[0]) << 4 | digit(pair[1]);
    }
    healthz_mac(token, nonce).verify_slice(&bytes).is_ok()
}

/// One `GET /healthz` with a fresh challenge. The response head fits well inside the limit; the body is not read for
/// anything.
fn healthz(port: u16, token: &str) -> Health {
    let Ok(raw) = new_token() else { return Health::Down };
    let nonce = raw.as_str();
    let request_headers = format!("{CHALLENGE_HEADER}: {nonce}\r\n");
    match request(port, "/healthz", &request_headers, 8192, true) {
        Ok(head) if head.starts_with("HTTP/1.1 200") || head.starts_with("HTTP/1.0 200") => {
            if proof_matches(&head, token, nonce) {
                Health::Proven
            } else {
                Health::Unproven
            }
        }
        _ => Health::Down,
    }
}

impl Running {
    /// The address the webview loads first. It carries the launch token, so it is never logged or shown. It is
    /// given only after two looks: the server has not exited (the token is not offered to a port nobody is serving
    /// from any more), and `/healthz` at the port, asked a fresh challenge now, answers with the proof that needs the
    /// token (the program that answers is the server that was started, not another one that took its port). What
    /// remains is the instant between this check and the window's own request, which nothing in the shell can close.
    pub fn start_url(&mut self, token: &str) -> Result<String, StartRefused> {
        if self.try_exit().is_some() {
            return Err(StartRefused::Gone);
        }
        // A server that is alive answers at once; a few tries cover a busy moment, not a program that is not there.
        let mut health = healthz(self.port, token);
        for _ in 0..3 {
            if health != Health::Down {
                break;
            }
            thread::sleep(Duration::from_millis(100));
            health = healthz(self.port, token);
        }
        if health != Health::Proven {
            // Gone in the meantime (another program may have taken its port): that is what is reported.
            thread::sleep(Duration::from_millis(100));
            if self.try_exit().is_some() {
                return Err(StartRefused::Gone);
            }
            return Err(if health == Health::Down { StartRefused::NoAnswer } else { StartRefused::NotTheServer });
        }
        if self.try_exit().is_some() {
            return Err(StartRefused::Gone);
        }
        Ok(format!("http://127.0.0.1:{}/desktop/start?t={}", self.port, token))
    }

    /// The exit status, if the server has exited.
    pub fn try_exit(&mut self) -> Option<ExitStatus> {
        self.child.try_wait().ok().flatten()
    }

    /// The last lines of standard error with the token, the key and control characters removed.
    pub fn tail_text(&self) -> String {
        let joined = match self.tail.lock() {
            Ok(t) => t.lines.iter().map(String::as_str).collect::<Vec<_>>().join("\n"),
            Err(_) => String::new(),
        };
        scrub(&joined, &self.secrets)
    }

    /// Asks the server to stop and waits. Closing its standard input is the request that works on every platform,
    /// and the server honours it (contract change 23: it stops as on `SIGTERM`, status 0). On Unix a `SIGTERM`
    /// follows at once; it is redundant with the real server and only matters for one that does not read its
    /// standard input. After `grace` the process is killed.
    pub fn shutdown(&mut self, grace: Duration) -> Shutdown {
        if let Some(status) = self.try_exit() {
            return Shutdown::AlreadyGone(status.code());
        }
        drop(self.stdin.take());
        #[cfg(unix)]
        {
            // SAFETY: the pid is this process's own child, which has not been waited for, so it cannot have
            // been reused.
            unsafe {
                libc::kill(self.child.id() as libc::pid_t, libc::SIGTERM);
            }
        }
        let deadline = Instant::now() + grace;
        loop {
            if let Some(status) = self.try_exit() {
                return Shutdown::Clean(status.code());
            }
            if Instant::now() >= deadline {
                break;
            }
            thread::sleep(Duration::from_millis(25));
        }
        let _ = self.child.kill();
        let _ = self.child.wait();
        Shutdown::Killed
    }
}

impl Drop for Running {
    fn drop(&mut self) {
        // A server must not outlive the shell that started it, whatever path led here.
        if self.try_exit().is_none() {
            let _ = self.child.kill();
            let _ = self.child.wait();
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_listen_line_is_read_strictly() {
        assert_eq!(parse_listen("LISTEN http://127.0.0.1:54321"), Some(54321));
        assert_eq!(parse_listen("LISTEN http://127.0.0.1:1\r"), Some(1));
        assert_eq!(parse_listen("LISTEN http://127.0.0.1:65535"), Some(65535));
        for bad in [
            "",
            "LISTEN",
            "LISTEN ",
            "LISTEN http://127.0.0.1:",
            "LISTEN http://127.0.0.1:0",
            "LISTEN http://127.0.0.1:65536",
            "LISTEN http://127.0.0.1:080",
            "LISTEN http://127.0.0.1:123456",
            "LISTEN http://127.0.0.1:80/",
            "LISTEN http://127.0.0.1:80 ",
            "LISTEN http://127.0.0.1:80\n",
            "LISTEN http://127.0.0.1:+80",
            "LISTEN http://127.0.0.1:-1",
            "LISTEN http://127.0.0.1:8a",
            "LISTEN http://127.0.0.1:80@example.com",
            "LISTEN https://127.0.0.1:443",
            "LISTEN http://localhost:8080",
            "LISTEN http://0.0.0.0:8080",
            "LISTEN http://example.com:80",
            "LISTEN http://[::1]:8080",
            "LISTEN  http://127.0.0.1:80",
            " LISTEN http://127.0.0.1:80",
            "listen http://127.0.0.1:80",
            "time=… msg=\"LISTEN http://127.0.0.1:80\"",
            "LISTEN http://127.0.0.1:８０",
        ] {
            assert_eq!(parse_listen(bad), None, "{bad:?}");
        }
    }

    #[test]
    fn tokens_are_64_hex_characters_and_differ() {
        let a = new_token().unwrap();
        let b = new_token().unwrap();
        assert_eq!(a.len(), 64);
        assert!(a.bytes().all(|c| matches!(c, b'0'..=b'9' | b'a'..=b'f')));
        assert_ne!(*a, *b);
        assert_eq!(hex(&[0x00, 0x0f, 0xa5, 0xff]).as_str(), "000fa5ff");
    }

    // The same token, nonce and answer as `TestTheHealthzProofMatchesTheShellsVector` in internal/desktopidp.
    const TOKEN: &str = "5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e5ca1ab1e";
    const NONCE: &str = "00112233445566778899aabbccddeeff00112233445566778899aabbccddeeff";
    const PROOF: &str = "68976f01ca10fc56efcfd44364fc8a604e14f46723d71f4d717517802c04c899";

    fn head(lines: &[&str]) -> String {
        format!("HTTP/1.1 200 OK\r\n{}\r\n\r\nok\n", lines.join("\r\n"))
    }

    #[test]
    fn the_proof_is_the_one_the_server_makes() {
        assert_eq!(expected_proof(TOKEN, NONCE), PROOF);
        assert_ne!(expected_proof(&TOKEN.replace('5', "6"), NONCE), PROOF, "another token, another answer");
        assert_ne!(expected_proof(TOKEN, &NONCE.replace('0', "1")), PROOF, "another challenge, another answer");
    }

    #[test]
    fn only_exactly_one_right_proof_is_accepted() {
        let right = format!("X-Scriobh-Proof: {PROOF}");
        let ok = |lines: &[&str]| proof_matches(&head(lines), TOKEN, NONCE);
        assert!(ok(&["Content-Type: text/plain", &right]));
        assert!(ok(&[&format!("X-SCRIOBH-PROOF: {PROOF}")]), "header names are not case sensitive");
        assert!(ok(&[&format!("x-scriobh-proof:   {PROOF}  ")]), "spaces around the value are not part of it");
        assert!(!ok(&["Content-Type: text/plain"]), "no proof");
        assert!(
            !ok(&[&format!("X-Scriobh-Proof: {}", expected_proof("another token", NONCE))]),
            "a proof without the token"
        );
        assert!(!ok(&[&right, &right]), "two proof headers are not a proof");
        assert!(!ok(&[&right, &format!("X-Scriobh-Proof: {}", "0".repeat(64))]), "one right and one wrong");
        assert!(!ok(&[&format!("X-Scriobh-Proof: {}", PROOF.to_uppercase())]), "the answer is lower case");
        assert!(!ok(&[&format!("X-Scriobh-Proof: {}", &PROOF[..62])]), "too short");
        assert!(!ok(&[&format!("X-Scriobh-Proof: {PROOF}00")]), "too long");
        assert!(!ok(&[&format!("X-Scriobh-Proof: {}g", &PROOF[..63])]), "not hex");
        assert!(!ok(&[&format!("X-Scriobh-Proof-Extra: {PROOF}")]), "another header");
        assert!(
            !proof_matches(&format!("HTTP/1.1 200 OK\r\n\r\nX-Scriobh-Proof: {PROOF}\r\n"), TOKEN, NONCE),
            "in the body"
        );
        assert!(!proof_matches(&head(&[&right]), TOKEN, &NONCE.replace('0', "1")), "a proof for another challenge");
    }

    #[test]
    fn a_look_at_the_head_stops_at_the_end_of_the_head() {
        use std::net::TcpListener;
        let l = TcpListener::bind("127.0.0.1:0").unwrap();
        let port = l.local_addr().unwrap().port();
        thread::spawn(move || {
            for s in l.incoming() {
                let mut s = s.unwrap();
                thread::spawn(move || {
                    let _ = s.write_all(b"HTTP/1.1 200 OK\r\nX-A: b\r\n\r\nbody");
                    // The connection stays open and the body never ends.
                    loop {
                        thread::sleep(Duration::from_millis(500));
                        if s.write_all(b"x").is_err() {
                            break;
                        }
                    }
                });
            }
        });
        let began = Instant::now();
        let head = request(port, "/", "", 8192, true).unwrap();
        assert!(head.starts_with("HTTP/1.1 200 OK\r\nX-A: b\r\n\r\n"), "{head:?}");
        assert!(began.elapsed() < Duration::from_secs(2), "it waited for a body it does not need");
        // A head split across reads is still found, and a body-reading call keeps reading until the deadline.
        assert!(request(port, "/", "", 8192, false).is_err());
    }

    fn os(k: &str, v: &str) -> (OsString, OsString) {
        (OsString::from(k), OsString::from(v))
    }

    fn get<'a>(env: &'a [(OsString, OsString)], k: &str) -> Vec<&'a OsStr> {
        env.iter().filter(|(n, _)| n == k).map(|(_, v)| v.as_os_str()).collect()
    }

    #[test]
    fn the_child_environment_is_the_contracts_and_drops_inherited_settings() {
        let mut cfg = SpawnConfig::new(PathBuf::from("x"), Zeroizing::new("t".repeat(64)));
        cfg.base_env = vec![
            os("PATH", "/usr/bin"),
            os("HOME", "/home/a"),
            os("SCRIOBH_BASE_URL", "https://evil.example"),
            os("SCRIOBH_DATA_DIR", "/elsewhere"),
            os("SCRIOBH_LISTEN", "0.0.0.0:80"),
            os("SCRIOBH_DESKTOP", "0"),
            os("SCRIOBH_OIDC_STUB", "1"),
            os("SCRIOBH_SECRET_KEY", "from-a-profile-0123456789abcdef0123"),
            os("SCRIOBH_LAUNCH_TOKEN", "stale"),
            os("scriobh_trust_proxy", "true"),
        ];
        cfg.extra_env = vec![os("STANDIN_MODE", "x"), os("SCRIOBH_OIDC_ISSUER", "https://idp.example")];
        let env = child_env(&cfg);
        assert_eq!(get(&env, "PATH"), ["/usr/bin"]);
        assert_eq!(get(&env, "HOME"), ["/home/a"]);
        assert_eq!(get(&env, "STANDIN_MODE"), ["x"]);
        assert_eq!(get(&env, "SCRIOBH_DESKTOP"), ["1"]);
        assert_eq!(get(&env, "SCRIOBH_LISTEN"), ["127.0.0.1:0"]);
        assert_eq!(get(&env, "SCRIOBH_LAUNCH_TOKEN"), ["t".repeat(64).as_str()]);
        for gone in [
            "SCRIOBH_BASE_URL",
            "SCRIOBH_DATA_DIR",
            "SCRIOBH_OIDC_STUB",
            "SCRIOBH_SECRET_KEY",
            "SCRIOBH_OIDC_ISSUER",
            "scriobh_trust_proxy",
        ] {
            assert!(get(&env, gone).is_empty(), "{gone} must not reach the server");
        }
        let names: Vec<String> =
            env.iter().map(|(k, _)| k.to_string_lossy().into_owned()).filter(|k| k.starts_with("SCRIOBH_")).collect();
        assert_eq!(names, ["SCRIOBH_DESKTOP", "SCRIOBH_LISTEN", "SCRIOBH_LAUNCH_TOKEN"]);

        cfg.secret_key = Some(Zeroizing::new("k".repeat(64)));
        cfg.data_dir = Some(PathBuf::from("/tmp/dev"));
        let env = child_env(&cfg);
        assert_eq!(get(&env, "SCRIOBH_SECRET_KEY"), ["k".repeat(64).as_str()]);
        assert_eq!(get(&env, "SCRIOBH_DATA_DIR"), ["/tmp/dev"]);
    }

    #[test]
    fn the_tail_keeps_the_last_lines_and_cuts_long_ones() {
        let mut t = Tail::default();
        for i in 0..(TAIL_LINES + 25) {
            t.push(&format!("line {i}"));
        }
        assert_eq!(t.lines.len(), TAIL_LINES);
        assert_eq!(t.lines.front().unwrap(), "line 25");
        t.push(&"é".repeat(TAIL_LINE_BYTES));
        assert!(t.lines.back().unwrap().len() <= TAIL_LINE_BYTES);
    }

    #[test]
    fn secrets_and_control_characters_are_removed_from_what_is_shown() {
        let token = Zeroizing::new("a1".repeat(32));
        let key = Zeroizing::new("b2".repeat(32));
        let text = format!("start t={}\nkey {} again {}\n\u{1b}[31mred\u{1b}[0m\u{7}\r", *token, *key, *token);
        let got = scrub(&text, &[token.clone(), key.clone(), Zeroizing::new(String::new())]);
        assert!(!got.contains(token.as_str()) && !got.contains(key.as_str()), "{got}");
        assert_eq!(got.matches(REDACTED).count(), 3);
        assert!(!got.chars().any(|c| c.is_control() && c != '\n'));
        assert!(got.contains("red"));
    }

    fn lines_of(bytes: Vec<u8>) -> Vec<(String, bool)> {
        let (tx, rx) = mpsc::channel();
        read_lines(std::io::Cursor::new(bytes), move |line, truncated| {
            let _ = tx.send((line.to_string(), truncated));
        });
        rx.iter().collect()
    }

    #[test]
    fn a_line_is_handed_over_whole_and_a_long_one_is_cut_once_and_marked() {
        let got = lines_of(b"one\r\ntwo\n\nlast without a break".to_vec());
        let plain: Vec<(&str, bool)> = got.iter().map(|(l, t)| (l.as_str(), *t)).collect();
        assert_eq!(plain, [("one", false), ("two", false), ("", false), ("last without a break", false)]);

        let mut long = vec![b'x'; MAX_LINE_BYTES * 3 + 5];
        long.push(b'\n');
        long.extend_from_slice(b"after\n");
        let got = lines_of(long);
        assert_eq!(got.len(), 2, "one logical line, not one per piece");
        assert_eq!((got[0].0.len(), got[0].1), (MAX_LINE_BYTES, true));
        assert_eq!(got[1], ("after".to_string(), false));
    }

    #[test]
    fn the_start_of_a_secret_left_at_a_cut_is_removed() {
        let token = Zeroizing::new("ab12cd34".repeat(8));
        let key = Zeroizing::new("ef56".repeat(16));
        let secrets = [token.clone(), key.clone()];
        let at_cut = format!("some text {}", &token[..40]);
        assert_eq!(without_partial_secret(&at_cut, &secrets), "some text ");
        let at_cut = format!("{}{}", "x".repeat(10), &key[..1]);
        assert_eq!(without_partial_secret(&at_cut, &secrets), "xxxxxxxxxx");
        // Nothing to remove, and a whole secret is the scrubber's job, not this one's.
        assert_eq!(without_partial_secret("plain", &secrets), "plain");
        // Scrubbed first, as the reader does, a whole secret at the end of a cut line is not left half there.
        let whole = format!("t={}", *token);
        let scrubbed = scrub(&whole, &secrets);
        assert_eq!(without_partial_secret(&scrubbed, &secrets), format!("t={REDACTED}"));
    }
}
