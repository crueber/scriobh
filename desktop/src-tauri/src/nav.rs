//! Where the app's webview may go. It is a window onto one loopback server and nothing else: every other address
//! is refused in the webview, and only an ordinary web or mail link is even offered to the system browser.

use tauri::Url;

/// The longest outside address the shell will offer to open.
pub const MAX_EXTERNAL_LEN: usize = 2048;

#[derive(Debug, PartialEq, Eq)]
pub enum Decision {
    /// The app's own origin: load it.
    Allow,
    /// Not the app: do not load it here, but it is an `http`, `https` or `mailto` address the person may be
    /// asked about.
    OfferExternal,
    /// Not the app and not something to hand to the system: drop it.
    Block,
}

/// True for `http://127.0.0.1:<port>/…` with no user or password part. Not `localhost`: the server answers it
/// with 421 (it can resolve to `::1`, where the server does not listen and something else may), so a link to it
/// is an ordinary outside link.
pub fn is_app_url(url: &Url, port: u16) -> bool {
    url.scheme() == "http"
        && url.host_str() == Some("127.0.0.1")
        && url.port_or_known_default() == Some(port)
        && url.username().is_empty()
        && url.password().is_none()
}

/// The decision for a navigation, a new-window request or a redirect in the app's webview.
pub fn decide(url: &Url, port: u16) -> Decision {
    if is_app_url(url, port) {
        return Decision::Allow;
    }
    if is_offerable(url) {
        Decision::OfferExternal
    } else {
        Decision::Block
    }
}

fn is_offerable(url: &Url) -> bool {
    if url.as_str().len() > MAX_EXTERNAL_LEN {
        return false;
    }
    match url.scheme() {
        "http" | "https" => {
            url.host_str().is_some_and(|h| !h.is_empty()) && url.username().is_empty() && url.password().is_none()
        }
        "mailto" => !url.path().is_empty(),
        _ => false,
    }
}

/// True when a download may start: the app's own origin, or a `blob:` the app's own page made.
pub fn is_app_download(url: &Url, port: u16) -> bool {
    if is_app_url(url, port) {
        return true;
    }
    if url.scheme() == "blob" {
        return Url::parse(url.path()).is_ok_and(|inner| is_app_url(&inner, port));
    }
    false
}

/// The text shown when the person is asked whether to open an outside address. The serialised form of a URL is
/// ASCII (an international host name is in its `xn--` form, which is what should be shown), so nothing here can
/// change direction or hide; it is only shortened.
pub fn display(url: &Url) -> String {
    let s = url.as_str();
    let clean: String = s.chars().filter(|c| c.is_ascii_graphic()).collect();
    if clean.len() <= 200 {
        clean
    } else {
        format!("{}…", &clean[..200])
    }
}

/// Some mail programs honour `attach=` and `attachment=` in a `mailto:` address and would attach a file from this
/// computer to the message. A link in a manuscript can ask for that, so those two parameters are taken out (names
/// are compared after percent-decoding, any case) before the address is shown or opened. Anything else is as it
/// was. Other schemes are returned as they are.
pub fn without_mail_attachments(url: &Url) -> Url {
    if url.scheme() != "mailto" {
        return url.clone();
    }
    let Some(query) = url.query() else { return url.clone() };
    let kept: Vec<&str> = query
        .split('&')
        .filter(|pair| {
            // Decoded whole before it is split, so `attach%3Dx` is read as a mail program may read it.
            let decoded = percent_decode(pair).to_ascii_lowercase();
            let key = decoded.split('=').next().unwrap_or("");
            key != "attach" && key != "attachment"
        })
        .collect();
    let mut out = url.clone();
    if kept.iter().all(|p| p.is_empty()) {
        out.set_query(None);
    } else {
        out.set_query(Some(&kept.join("&")));
    }
    out
}

fn percent_decode(text: &str) -> String {
    let bytes = text.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        let hex = |b: u8| (b as char).to_digit(16);
        if bytes[i] == b'%' && i + 2 < bytes.len() && hex(bytes[i + 1]).is_some() && hex(bytes[i + 2]).is_some() {
            out.push((hex(bytes[i + 1]).unwrap() * 16 + hex(bytes[i + 2]).unwrap()) as u8);
            i += 3;
        } else {
            out.push(bytes[i]);
            i += 1;
        }
    }
    String::from_utf8_lossy(&out).into_owned()
}

/// The host to name in the question, or the address for `mailto`.
pub fn display_host(url: &Url) -> String {
    match url.scheme() {
        "mailto" => {
            let addr = url.path().split('?').next().unwrap_or("");
            let clean: String = addr.chars().filter(|c| c.is_ascii_graphic()).take(120).collect();
            clean
        }
        _ => url.host_str().unwrap_or("").chars().take(120).collect(),
    }
}

/// Where File, Export… goes: the compile page of the project the webview is showing, or the library.
pub fn export_path(current: &Url, port: u16) -> String {
    if !is_app_url(current, port) {
        return "/".to_string();
    }
    let mut parts = current.path().split('/');
    if (parts.next(), parts.next()) == (Some(""), Some("p")) {
        if let Some(slug) = parts.next() {
            if crate::deeplink::is_slug(slug) {
                return format!("/p/{slug}/compile");
            }
        }
    }
    "/".to_string()
}

/// A path on the app's origin.
pub fn app_url(port: u16, path: &str) -> Url {
    debug_assert!(path.starts_with('/'));
    Url::parse(&format!("http://127.0.0.1:{port}{path}")).expect("a loopback URL with a fixed shape")
}

#[cfg(test)]
mod tests {
    use super::*;

    fn u(s: &str) -> Url {
        Url::parse(s).unwrap()
    }

    #[test]
    fn a_mail_link_loses_the_parameters_that_attach_files() {
        let clean = |s: &str| without_mail_attachments(&u(s)).to_string();
        assert_eq!(clean("mailto:a@example.com"), "mailto:a@example.com");
        assert_eq!(
            clean("mailto:a@example.com?subject=Hi%20there&body=x"),
            "mailto:a@example.com?subject=Hi%20there&body=x"
        );
        assert_eq!(clean("mailto:a@example.com?attach=/etc/passwd"), "mailto:a@example.com");
        assert_eq!(
            clean("mailto:a@example.com?subject=s&attachment=~/.ssh/id_rsa&body=b"),
            "mailto:a@example.com?subject=s&body=b"
        );
        assert_eq!(clean("mailto:a@example.com?ATTACH=x&Attachment=y&subject=s"), "mailto:a@example.com?subject=s");
        assert_eq!(clean("mailto:a@example.com?att%61ch=x&%41ttachment%3d=y"), "mailto:a@example.com");
        // Only the parameter's name is looked at, not text that merely contains it.
        assert_eq!(clean("mailto:a@example.com?subject=attach=me"), "mailto:a@example.com?subject=attach=me");
        assert_eq!(clean("mailto:a@example.com?attachments=1"), "mailto:a@example.com?attachments=1");
        assert_eq!(clean("https://example.com/?attach=1"), "https://example.com/?attach=1");
        // Still an offerable mail address after the cleaning.
        assert_eq!(
            decide(&without_mail_attachments(&u("mailto:a@example.com?attach=x")), 4321),
            Decision::OfferExternal
        );
    }

    #[test]
    fn the_app_origin_is_127_0_0_1_only() {
        for s in [
            "http://127.0.0.1:4321/",
            "http://127.0.0.1:4321/p/book/d/doc_01J?x=1#frag",
            "http://127.0.0.1:4321/desktop/start?t=abc",
        ] {
            assert_eq!(decide(&u(s), 4321), Decision::Allow, "{s}");
        }
    }

    #[test]
    fn another_port_host_or_scheme_is_not_the_app() {
        for s in [
            "http://127.0.0.1:4322/",
            "http://127.0.0.1/",
            "https://127.0.0.1:4321/",
            "http://127.0.0.2:4321/",
            "http://[::1]:4321/",
            "http://0.0.0.0:4321/",
            "http://127.0.0.1.example.com:4321/",
            "http://localhost.example.com:4321/",
            // Review: the Go side answers `localhost` with 421 and the contract copy says it is never the app
            // (`localhost` may resolve to ::1, where the server does not listen, and something else may).
            "http://localhost:4321/",
            "http://localhost:4321/import",
            "http://LOCALHOST:4321/",
            "http://localhost./",
            "http://example.com:4321/",
            "http://user@127.0.0.1:4321/",
            "http://user:pw@127.0.0.1:4321/",
            "http://127.0.0.1:4321@example.com/",
            "http://2130706433:4322/",
        ] {
            assert_ne!(decide(&u(s), 4321), Decision::Allow, "{s}");
        }
        // Port 80 is only the app when the app is on port 80.
        assert_eq!(decide(&u("http://127.0.0.1/"), 80), Decision::Allow);
    }

    #[test]
    fn the_url_parser_normalises_odd_loopback_spellings_to_the_same_host() {
        // These are the same address after parsing, so they are the app; what matters is that nothing else is.
        assert!(is_app_url(&u("http://0x7f.1:4321/"), 4321));
        assert!(is_app_url(&u("http://2130706433:4321/"), 4321));
    }

    #[test]
    fn web_and_mail_links_are_offered_and_never_loaded() {
        for s in [
            "https://example.com/a?b#c",
            "http://example.com/",
            "mailto:someone@example.com",
            "https://xn--fsq.example/",
        ] {
            assert_eq!(decide(&u(s), 4321), Decision::OfferExternal, "{s}");
        }
    }

    #[test]
    fn everything_else_is_dropped() {
        for s in [
            "file:///etc/passwd",
            "file://localhost/etc/passwd",
            "javascript:alert(1)",
            "data:text/html,<script>alert(1)</script>",
            "blob:http://127.0.0.1:4321/0b1c",
            "about:blank",
            "tauri://localhost/error.html",
            "ipc://localhost/x",
            "scriobh://open/import",
            "ftp://example.com/x",
            "ssh://example.com",
            "smb://server/share",
            "vbscript:x",
            "x-apple.systempreferences:com.apple.preference.security",
            "tel:+1",
            "sms:+1",
            "mailto:",
            "https://user:pw@example.com/",
            "https://user@example.com/",
        ] {
            assert_eq!(decide(&u(s), 4321), Decision::Block, "{s}");
        }
        let long = format!("https://example.com/{}", "a".repeat(MAX_EXTERNAL_LEN));
        assert_eq!(decide(&u(&long), 4321), Decision::Block);
    }

    #[test]
    fn downloads_come_only_from_the_app_or_its_own_blobs() {
        assert!(is_app_download(&u("http://127.0.0.1:4321/p/b/editions/1/download"), 4321));
        assert!(is_app_download(&u("blob:http://127.0.0.1:4321/5f0c-11"), 4321));
        for s in [
            "https://example.com/x.dmg",
            "blob:https://example.com/5f0c",
            "blob:http://127.0.0.1:9/5f0c",
            "blob:http://localhost:4321/5f0c-11",
            "blob:null/5f0c",
            "data:application/octet-stream;base64,AAAA",
            "file:///etc/passwd",
        ] {
            assert!(!is_app_download(&u(s), 4321), "{s}");
        }
    }

    #[test]
    fn the_question_shows_the_real_host() {
        // An international name is shown in its encoded form, which cannot imitate another.
        let idn = u("https://аpple.com/login");
        assert!(display_host(&idn).starts_with("xn--"), "{}", display_host(&idn));
        assert!(display(&idn).is_ascii());
        assert_eq!(display_host(&u("mailto:a@example.com?subject=x")), "a@example.com");
        let long = u(&format!("https://example.com/{}", "a".repeat(900)));
        assert!(display(&long).chars().count() <= 201);
        // Direction marks in a path arrive percent-encoded.
        assert!(!display(&u("https://example.com/\u{202E}gpj.exe")).contains('\u{202E}'));
    }

    #[test]
    fn export_goes_to_the_project_being_shown() {
        let p = 4321;
        assert_eq!(export_path(&u("http://127.0.0.1:4321/p/an-leabhar/d/doc_01J"), p), "/p/an-leabhar/compile");
        assert_eq!(export_path(&u("http://127.0.0.1:4321/p/an-leabhar"), p), "/p/an-leabhar/compile");
        assert_eq!(export_path(&u("http://127.0.0.1:4321/p/b/compile?x=1"), p), "/p/b/compile");
        assert_eq!(export_path(&u("http://localhost:4321/p/b/compile?x=1"), p), "/");
        assert_eq!(export_path(&u("http://127.0.0.1:4321/"), p), "/");
        assert_eq!(export_path(&u("http://127.0.0.1:4321/settings"), p), "/");
        assert_eq!(export_path(&u("http://127.0.0.1:4321/p/"), p), "/");
        assert_eq!(export_path(&u("http://127.0.0.1:4321/p/%2e%2e/x"), p), "/");
        assert_eq!(export_path(&u("http://127.0.0.1:4321/p/UPPER/x"), p), "/");
        assert_eq!(export_path(&u("https://example.com/p/b"), p), "/");
    }
}
