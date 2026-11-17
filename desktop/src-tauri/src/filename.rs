//! File names that came from somewhere else: the server's suggested download name and the name of a file the
//! operating system asked the app to open. Both are shown to the person and one becomes the default in a save
//! dialog, so neither may carry a path, a control or direction-changing character, or a name Windows reserves.

/// The longest name kept, in bytes. Long enough for any book title, short enough for every filesystem.
pub const MAX_NAME_BYTES: usize = 150;

const FALLBACK: &str = "download";

/// Characters that change the direction or the joining of the text around them, or are invisible. A name that
/// reads "annexe-fdp.exe" on screen must not be "annexe-exe.pdf" on disk.
pub fn is_deceptive(c: char) -> bool {
    matches!(
        c,
        '\u{00AD}'
            | '\u{061C}'
            | '\u{180E}'
            | '\u{200B}'..='\u{200F}'
            | '\u{202A}'..='\u{202E}'
            | '\u{2028}'
            | '\u{2029}'
            | '\u{2060}'..='\u{2069}'
            | '\u{FEFF}'
            | '\u{FFF9}'..='\u{FFFB}'
            | '\u{E0000}'..='\u{E007F}'
    )
}

fn is_reserved_windows(stem: &str) -> bool {
    let s = stem.trim_end_matches([' ', '.']).to_ascii_uppercase();
    if matches!(s.as_str(), "CON" | "PRN" | "AUX" | "NUL" | "CONIN$" | "CONOUT$") {
        return true;
    }
    // `COM1` to `COM9` and `LPT1` to `LPT9`, and the superscript digits Windows reserves as well (`COM¹`, `LPT²`).
    (s.starts_with("COM") || s.starts_with("LPT"))
        && s.chars().count() == 4
        && s.chars().nth(3).is_some_and(|d| d.is_ascii_digit() || matches!(d, '\u{b9}' | '\u{b2}' | '\u{b3}'))
}

/// Makes a suggested file name safe to show and to offer in a save dialog.
///
/// Only the last path component is kept (after `/`, `\` and `:`), control and deceptive characters are removed,
/// characters Windows forbids become `_`, leading dots and trailing dots and spaces are dropped, a reserved
/// Windows device name gets a `_` prefix, and the result is cut to [`MAX_NAME_BYTES`] keeping the extension. An
/// empty result becomes `download`.
pub fn sanitise(raw: &str) -> String {
    let last = raw.rsplit(['/', '\\', ':']).next().unwrap_or("");
    let mut out = String::with_capacity(last.len());
    for c in last.chars() {
        if c.is_control() || is_deceptive(c) {
            continue;
        }
        if matches!(c, '<' | '>' | '"' | '|' | '?' | '*') {
            out.push('_');
        } else {
            out.push(c);
        }
    }
    let trimmed = out.trim().trim_start_matches('.').trim_end_matches([' ', '.']).trim();
    if trimmed.is_empty() {
        return FALLBACK.to_string();
    }
    let mut name = trimmed.to_string();
    let stem = name.split('.').next().unwrap_or("");
    if is_reserved_windows(stem) {
        name.insert(0, '_');
    }
    truncate(&name)
}

fn truncate(name: &str) -> String {
    if name.len() <= MAX_NAME_BYTES {
        return name.to_string();
    }
    // Keep a short extension; cut the stem on a character boundary.
    let (stem, ext) = match name.rfind('.') {
        Some(i) if i > 0 && name.len() - i <= 16 => (&name[..i], &name[i..]),
        _ => (name, ""),
    };
    let mut cut = MAX_NAME_BYTES.saturating_sub(ext.len()).min(stem.len());
    while cut > 0 && !stem.is_char_boundary(cut) {
        cut -= 1;
    }
    let stem = stem[..cut].trim_end_matches([' ', '.']);
    if stem.is_empty() {
        return FALLBACK.to_string();
    }
    format!("{stem}{ext}")
}

#[cfg(test)]
mod tests {
    use super::*;

    /// Review. Windows reserves `COM¹`, `COM²`, `COM³`, `LPT¹`, `LPT²` and `LPT³` as well as the ASCII digits.
    #[test]
    fn the_superscript_device_names_are_reserved_too() {
        for raw in ["COM\u{b9}.txt", "lpt\u{b2}", "COM\u{b3}.tar.gz"] {
            assert!(sanitise(raw).starts_with('_'), "{raw:?} became {:?}", sanitise(raw));
        }
    }

    #[test]
    fn keeps_an_ordinary_name() {
        assert_eq!(sanitise("An Leabhar Mór.epub"), "An Leabhar Mór.epub");
        assert_eq!(sanitise("draft-3.docx"), "draft-3.docx");
    }

    #[test]
    fn keeps_only_the_last_component() {
        assert_eq!(sanitise("../../etc/passwd"), "passwd");
        assert_eq!(sanitise("/Users/x/Library/Preferences/a.plist"), "a.plist");
        assert_eq!(sanitise(r"C:\Windows\System32\drivers\etc\hosts"), "hosts");
        assert_eq!(sanitise(r"..\..\boot.ini"), "boot.ini");
        assert_eq!(sanitise("book.epub:Zone.Identifier"), "Zone.Identifier");
    }

    #[test]
    fn a_name_that_is_only_dots_or_separators_becomes_the_fallback() {
        for raw in ["", ".", "..", "...", "/", "\\", "../", "..\\..\\", " ", ". .", "\u{202E}", "\n\r\t"] {
            assert_eq!(sanitise(raw), "download", "{raw:?}");
        }
    }

    #[test]
    fn drops_leading_dots_so_nothing_is_saved_hidden() {
        assert_eq!(sanitise(".bashrc"), "bashrc");
        assert_eq!(sanitise("..hidden.txt"), "hidden.txt");
    }

    #[test]
    fn removes_control_and_direction_characters() {
        assert_eq!(sanitise("a\u{0000}b\u{0007}c\nd\re\tf.txt"), "abcdef.txt");
        // Right-to-left override: shown as "annexe-fdp.exe", really ending in ".pdf" reversed.
        assert_eq!(sanitise("annexe-\u{202E}fdp.exe"), "annexe-fdp.exe");
        assert_eq!(sanitise("a\u{2066}b\u{2069}c\u{200F}d\u{FEFF}e\u{200B}.md"), "abcde.md");
        assert_eq!(sanitise("x\u{0085}y\u{009F}z"), "xyz");
        for c in sanitise("q\u{202A}\u{202B}\u{202C}\u{202D}\u{202E}\u{2067}\u{2068}\u{061C}\u{2028}\u{2029}w").chars()
        {
            assert!(c == 'q' || c == 'w');
        }
    }

    #[test]
    fn replaces_characters_windows_forbids() {
        assert_eq!(sanitise("what?<is>\"this\"|*.txt"), "what__is__this___.txt");
    }

    #[test]
    fn defuses_reserved_windows_names() {
        for raw in ["CON", "con", "NUL.txt", "aux.epub", "COM1", "com9.md", "LPT3.tar.gz", "PRN .txt", "CONIN$"] {
            let got = sanitise(raw);
            assert!(got.starts_with('_'), "{raw:?} gave {got:?}");
        }
        assert_eq!(sanitise("console.txt"), "console.txt");
        assert_eq!(sanitise("COM10.txt"), "COM10.txt");
        assert_eq!(sanitise("comet.md"), "comet.md");
    }

    #[test]
    fn drops_trailing_dots_and_spaces() {
        assert_eq!(sanitise("report. . "), "report");
        assert_eq!(sanitise("  spaced.txt  "), "spaced.txt");
    }

    #[test]
    fn cuts_an_overlong_name_and_keeps_the_extension() {
        let long = format!("{}.epub", "é".repeat(400));
        let got = sanitise(&long);
        assert!(got.len() <= MAX_NAME_BYTES, "{}", got.len());
        assert!(got.ends_with(".epub"));
        assert!(got.starts_with('é'));
        let no_ext = "x".repeat(1000);
        assert_eq!(sanitise(&no_ext).len(), MAX_NAME_BYTES);
        // An "extension" that is itself enormous is not kept.
        let silly = format!("a.{}", "b".repeat(500));
        assert!(sanitise(&silly).len() <= MAX_NAME_BYTES);
    }

    #[test]
    fn the_result_is_always_a_plain_name() {
        let hostile = [
            "..",
            "a/..",
            "a/../b",
            "\\\\server\\share\\x",
            "x\u{0000}/y",
            "%2e%2e%2f",
            "~",
            "-rf",
            "a\u{202E}b/c",
            "CON/..",
            "nul",
            " . ",
            "ok.txt\u{0000}.exe",
        ];
        for raw in hostile {
            let got = sanitise(raw);
            assert!(!got.is_empty());
            assert!(!got.contains(['/', '\\', ':']), "{raw:?} gave {got:?}");
            assert!(got != "." && got != "..", "{raw:?} gave {got:?}");
            assert!(!got.starts_with('.'), "{raw:?} gave {got:?}");
            assert!(!got.chars().any(|c| c.is_control() || is_deceptive(c)), "{raw:?} gave {got:?}");
            assert!(got.len() <= MAX_NAME_BYTES);
        }
    }
}
