//! Deep links and files the operating system asks the app to open.
//!
//! Both can only move the webview to an ordinary page of the app. A deep link names one of a few routes and is
//! checked character by character; a file is never read by the shell: the webview goes to the import page and the
//! person picks the file there, through the page's own file input, after signing in like anyone else.

use crate::filename;

/// The longest deep link read.
pub const MAX_LINK_LEN: usize = 512;

const PREFIX: &str = "scriobh://open";

#[derive(Debug, PartialEq, Eq)]
pub enum Open {
    /// Go to this path on the app's origin.
    Route(String),
    /// Go to the import page and tell the person to choose this file there. The name is for display only.
    ImportNotice(String),
    /// A `scriobh:` address that names nothing the app opens.
    BadLink,
    /// Not for us (a flag, an unknown file type).
    Ignore,
}

/// A project slug as the server makes them: lower-case ASCII letters, digits and single hyphens.
pub fn is_slug(s: &str) -> bool {
    !s.is_empty()
        && s.len() <= 80
        && s.bytes().all(|b| b.is_ascii_lowercase() || b.is_ascii_digit() || b == b'-')
        && !s.starts_with('-')
        && !s.ends_with('-')
}

/// A document id as the server makes them: a short prefix, an underscore and a ULID.
fn is_id(s: &str) -> bool {
    !s.is_empty() && s.len() <= 64 && s.bytes().all(|b| b.is_ascii_alphanumeric() || b == b'_')
}

/// Reads `scriobh://open/<path>` and returns the app path it names, or `None`.
///
/// The link is compared as written: nothing is decoded, normalised or resolved, so `..`, `%2f`, a backslash, a
/// query, a fragment, a space or a line break can only fail to match. The routes are `/`, `/import`,
/// `/settings`, `/p/<slug>` and `/p/<slug>/d/<id>`.
pub fn parse_deep_link(raw: &str) -> Option<String> {
    if raw.len() > MAX_LINK_LEN || !raw.bytes().all(|b| b.is_ascii_graphic()) {
        return None;
    }
    if raw.len() < PREFIX.len() || !raw[..PREFIX.len()].eq_ignore_ascii_case(PREFIX) {
        return None;
    }
    let path = &raw[PREFIX.len()..];
    match path {
        "" | "/" => return Some("/".to_string()),
        "/import" => return Some("/import".to_string()),
        "/settings" => return Some("/settings".to_string()),
        _ => {}
    }
    let rest = path.strip_prefix("/p/")?;
    let mut parts = rest.split('/');
    let slug = parts.next()?;
    if !is_slug(slug) {
        return None;
    }
    match (parts.next(), parts.next(), parts.next()) {
        (None, _, _) => Some(format!("/p/{slug}")),
        (Some("d"), Some(id), None) if is_id(id) => Some(format!("/p/{slug}/d/{id}")),
        _ => None,
    }
}

fn has_import_extension(name: &str) -> bool {
    let lower = name.to_ascii_lowercase();
    [".md", ".markdown", ".epub"].iter().any(|e| lower.ends_with(e) && lower.len() > e.len())
}

/// Classifies one argument of a launch, or one address the operating system handed over.
///
/// A file is recognised by its name alone: the shell does not look at the disk.
pub fn classify(arg: &str) -> Open {
    if arg.get(..8).is_some_and(|p| p.eq_ignore_ascii_case("scriobh:")) {
        return match parse_deep_link(arg) {
            Some(path) => Open::Route(path),
            None => Open::BadLink,
        };
    }
    if arg.starts_with('-') || arg.len() > 4096 {
        return Open::Ignore;
    }
    let name = if arg.get(..7).is_some_and(|p| p.eq_ignore_ascii_case("file://")) {
        match tauri::Url::parse(arg).ok().and_then(|u| u.to_file_path().ok()) {
            Some(p) => p.file_name().map(|n| n.to_string_lossy().into_owned()).unwrap_or_default(),
            None => return Open::Ignore,
        }
    } else if arg.contains("://") {
        return Open::Ignore;
    } else {
        arg.rsplit(['/', '\\']).next().unwrap_or("").to_string()
    };
    if !has_import_extension(&name) {
        return Open::Ignore;
    }
    let mut shown = filename::sanitise(&name);
    if shown.chars().count() > 80 {
        shown = shown.chars().take(79).chain(std::iter::once('…')).collect();
    }
    Open::ImportNotice(shown)
}

/// The first thing to open among a launch's arguments (the program name excluded by the caller).
pub fn first_open<S: AsRef<str>>(args: &[S]) -> Open {
    for a in args {
        match classify(a.as_ref()) {
            Open::Ignore => continue,
            other => return other,
        }
    }
    Open::Ignore
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn the_listed_routes_parse() {
        let ok = [
            ("scriobh://open", "/"),
            ("scriobh://open/", "/"),
            ("scriobh://open/import", "/import"),
            ("scriobh://open/settings", "/settings"),
            ("scriobh://open/p/an-leabhar-mor", "/p/an-leabhar-mor"),
            ("scriobh://open/p/b/d/doc_01JABCDEFGHJKMNPQRSTVWXYZ0", "/p/b/d/doc_01JABCDEFGHJKMNPQRSTVWXYZ0"),
            ("SCRIOBH://OPEN/import", "/import"),
            ("Scriobh://Open/p/book", "/p/book"),
        ];
        for (raw, want) in ok {
            assert_eq!(parse_deep_link(raw).as_deref(), Some(want), "{raw}");
        }
    }

    #[test]
    fn hostile_and_malformed_links_name_nothing() {
        let long = format!("scriobh://open/p/{}", "a".repeat(MAX_LINK_LEN));
        let bad = [
            "scriobh://open/../../x",
            "scriobh://open/p/../settings",
            "scriobh://open/p/book/../../settings",
            "scriobh://open/p/book/d/..",
            "scriobh://open/%2e%2e/%2e%2e/x",
            "scriobh://open/p%2Fbook",
            "scriobh://open/p/book%2Fd%2Fx",
            "scriobh://open/p/book/d/doc%00",
            "scriobh://open//import",
            "scriobh://open/import/",
            "scriobh://open/import?next=https://example.com",
            "scriobh://open/import#x",
            "scriobh://open/settings/ai",
            "scriobh://open/auth/logout",
            "scriobh://open/desktop/start?t=x",
            "scriobh://open/api/v1/projects",
            "scriobh://open/p/",
            "scriobh://open/p//d/x",
            "scriobh://open/p/-book",
            "scriobh://open/p/book-",
            "scriobh://open/p/Book",
            "scriobh://open/p/bo ok",
            "scriobh://open/p/book/d/",
            "scriobh://open/p/book/d/a/b",
            "scriobh://open/p/book/x/doc_1",
            "scriobh://open/p/book/d/doc-1",
            "scriobh://open\\import",
            "scriobh://open/p\\book",
            "scriobh://open/import\n",
            "scriobh://open/import\r\nHost: x",
            "scriobh://open/im\tport",
            "scriobh://open/import\u{0000}",
            "scriobh://open/p/b\u{202E}ook",
            "scriobh://open/p/leabhar-mór",
            "scriobh://opener/import",
            "scriobh://open.example.com/import",
            "scriobh://user@open/import",
            "scriobh://open:80/import",
            "scriobh:/open/import",
            "scriobh:open/import",
            "scriobh:///import",
            "scriobh://127.0.0.1/import",
            "scriobh://import",
            "javascript:alert(1)",
            "javascript://open/import",
            "file:///etc/passwd",
            "http://127.0.0.1:1/import",
            "https://example.com/scriobh://open/import",
            " scriobh://open/import",
            "scriobh://open/import ",
            "",
            long.as_str(),
        ];
        for raw in bad {
            assert_eq!(parse_deep_link(raw), None, "{raw:?}");
        }
    }

    #[test]
    fn a_parsed_route_is_always_one_of_the_shapes() {
        // Whatever parses can be appended to the origin as it stands.
        for raw in ["scriobh://open/p/a-b-c/d/doc_9", "scriobh://open/p/x", "scriobh://open"] {
            let p = parse_deep_link(raw).unwrap();
            assert!(p.starts_with('/'));
            assert!(p.bytes().all(|b| b.is_ascii_alphanumeric() || matches!(b, b'/' | b'-' | b'_')));
            assert!(!p.contains("//") && !p.contains(".."));
        }
    }

    #[test]
    fn arguments_are_classified_by_shape_alone() {
        assert_eq!(classify("scriobh://open/import"), Open::Route("/import".into()));
        assert_eq!(classify("scriobh://open/../x"), Open::BadLink);
        assert_eq!(classify("SCRIOBH:whatever"), Open::BadLink);
        assert_eq!(classify("/Users/a/Books/An Leabhar.epub"), Open::ImportNotice("An Leabhar.epub".into()));
        assert_eq!(classify("notes.MD"), Open::ImportNotice("notes.MD".into()));
        assert_eq!(classify(r"C:\Users\a\draft.markdown"), Open::ImportNotice("draft.markdown".into()));
        assert_eq!(classify("file:///Users/a/My%20Book.epub"), Open::ImportNotice("My Book.epub".into()));
        for ignored in [
            "--flag",
            "-psn_0_12345",
            "/Users/a/photo.png",
            "/etc/passwd",
            ".md",
            "https://example.com/book.epub",
            "javascript:alert(1)//.md",
            "ftp://x/y.md",
            "éé.txt",
            "ééééééé",
            "",
        ] {
            assert_eq!(classify(ignored), Open::Ignore, "{ignored:?}");
        }
    }

    #[test]
    fn a_file_name_shown_in_a_notice_is_sanitised_and_short() {
        match classify("/tmp/evil\u{202E}bupe\u{0007}.md") {
            Open::ImportNotice(n) => {
                assert!(!n.chars().any(|c| c.is_control() || c == '\u{202E}'), "{n:?}");
            }
            other => panic!("{other:?}"),
        }
        let long = format!("/tmp/{}.epub", "x".repeat(140));
        match classify(&long) {
            Open::ImportNotice(n) => assert!(n.chars().count() <= 80),
            other => panic!("{other:?}"),
        }
    }

    #[test]
    fn the_first_meaningful_argument_wins() {
        assert_eq!(first_open(&["--x", "a.txt", "scriobh://open/settings", "b.md"]), Open::Route("/settings".into()));
        assert_eq!(first_open::<&str>(&[]), Open::Ignore);
        assert_eq!(first_open(&["--data-dir", "/tmp/x"]), Open::Ignore);
    }
}
