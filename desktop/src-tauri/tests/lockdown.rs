//! The shell's promises about what a page can reach, pinned as tests on the files that would have to change to
//! break them. These read the repository; they need no display and start nothing.

use std::fs;
use std::path::{Path, PathBuf};

use serde_json::Value;

fn root() -> PathBuf {
    PathBuf::from(env!("CARGO_MANIFEST_DIR"))
}

fn json(path: &Path) -> Value {
    serde_json::from_str(&fs::read_to_string(path).unwrap()).unwrap_or_else(|e| panic!("{}: {e}", path.display()))
}

fn files(dir: &Path, ext: &str) -> Vec<PathBuf> {
    let mut out: Vec<PathBuf> = fs::read_dir(dir)
        .unwrap_or_else(|e| panic!("{}: {e}", dir.display()))
        .map(|e| e.unwrap().path())
        .filter(|p| p.extension().is_some_and(|x| x == ext))
        .collect();
    out.sort();
    out
}

#[test]
fn no_capability_grants_anything_or_names_a_remote_origin() {
    let caps = files(&root().join("capabilities"), "json");
    assert_eq!(caps.len(), 1, "one capability file, and it is empty: {caps:?}");
    for path in caps {
        let cap = json(&path);
        assert_eq!(cap["permissions"], Value::Array(vec![]), "{} grants a permission", path.display());
        assert!(cap.get("remote").is_none(), "{} lets a remote origin reach the shell", path.display());
        assert!(cap.get("webviews").is_none());
        for w in cap["windows"].as_array().unwrap() {
            let w = w.as_str().unwrap();
            assert!(w == "main-*" || w == "error-*", "unexpected window {w}");
        }
    }
    assert!(files(&root().join("capabilities"), "toml").is_empty());
    assert!(!root().join("permissions").exists(), "the shell defines no permissions of its own");
}

#[test]
fn the_configuration_exposes_no_api_to_pages_and_declares_no_window() {
    let conf = json(&root().join("tauri.conf.json"));
    let app = &conf["app"];
    assert_eq!(app["withGlobalTauri"], Value::Bool(false));
    assert_eq!(app["windows"], Value::Array(vec![]), "windows are made in code, with their navigation rules attached");
    let sec = &app["security"];
    assert_eq!(sec["capabilities"], serde_json::json!(["none"]));
    for key in [
        "dangerousDisableAssetCspModification",
        "dangerousRemoteDomainIpcAccess",
        "assetProtocol",
        "pattern",
        "headers",
    ] {
        assert!(sec.get(key).is_none(), "app.security.{key} must stay absent");
    }
    let csp = sec["csp"].as_str().unwrap();
    assert!(csp.starts_with("default-src 'none'"), "{csp}");
    for forbidden in ["unsafe-inline", "unsafe-eval", "*", "http:", "https:", "data:", "blob:"] {
        assert!(!csp.contains(forbidden), "the shell page's CSP has {forbidden}: {csp}");
    }
    let text = fs::read_to_string(root().join("tauri.conf.json")).unwrap();
    for forbidden in ["devUrl", "beforeDevCommand", "beforeBuildCommand", "\"remote\"", "dangerous"] {
        assert!(!text.contains(forbidden), "tauri.conf.json has {forbidden}");
    }
    let plugins = conf["plugins"].as_object().unwrap();
    let mut names: Vec<&str> = plugins.keys().map(String::as_str).collect();
    names.sort();
    assert_eq!(names, ["deep-link", "updater"]);
    assert_eq!(conf["plugins"]["deep-link"]["desktop"]["schemes"], serde_json::json!(["scriobh"]));
}

#[test]
fn the_updater_is_configured_for_https_a_signature_and_no_downgrade() {
    let conf = json(&root().join("tauri.conf.json"));
    let up = &conf["plugins"]["updater"];
    for e in up["endpoints"].as_array().unwrap() {
        assert!(e.as_str().unwrap().starts_with("https://"));
        assert!(!e.as_str().unwrap().contains("current_version"), "the running version is not sent");
    }
    assert_eq!(up["requireSignedVersion"], Value::Bool(true));
    assert!(up.get("allowDowngrades").is_none());
    assert!(!up["pubkey"].as_str().unwrap().is_empty());
    assert_eq!(
        conf["bundle"]["createUpdaterArtifacts"],
        Value::Bool(false),
        "release builds turn this on with their own key"
    );
}

/// Code only: comments may name what the shell does not do.
fn code_of(path: &Path) -> String {
    fs::read_to_string(path)
        .unwrap()
        .lines()
        .filter(|l| !l.trim_start().starts_with("//"))
        .collect::<Vec<_>>()
        .join("\n")
}

#[test]
fn the_shell_registers_no_command_injects_no_script_and_serves_no_protocol() {
    let forbidden = [
        "invoke_handler",
        "generate_handler",
        "tauri::command",
        "initialization_script",
        "js_init_script",
        ".eval(",
        "register_uri_scheme_protocol",
        "register_asynchronous_uri_scheme_protocol",
        "on_web_resource_request",
        "ipc_capability",
        "add_capability",
        "CapabilityBuilder",
        "tauri_plugin_dialog",
        "tauri_plugin_shell",
        "tauri_plugin_fs",
        "tauri_plugin_http",
        "tauri_plugin_opener::init",
        "tauri_plugin_opener::Builder",
        "devtools(",
        "open_devtools",
        "open_path",
    ];
    // Dialogs go through one module, so that none can be put up from inside a webview callback by accident.
    for path in files(&root().join("src"), "rs") {
        if path.file_name().is_some_and(|n| n != "dialogs.rs") {
            let code = code_of(&path);
            assert!(!code.contains("rfd::"), "{} opens a dialog directly", path.display());
            // `run_on_main_thread` runs at once when called from the main thread; `dialogs::later` does not.
            assert!(
                !code.contains("run_on_main_thread"),
                "{} may run a task inside a webview callback",
                path.display()
            );
        }
    }
    let sources = files(&root().join("src"), "rs");
    assert!(sources.len() >= 8);
    for path in sources {
        let code = code_of(&path);
        for f in forbidden {
            assert!(!code.contains(f), "{} uses {f}", path.display());
        }
    }
}

#[test]
fn the_shell_depends_on_no_plugin_that_talks_to_pages() {
    let manifest = fs::read_to_string(root().join("Cargo.toml")).unwrap();
    let deps: Vec<&str> = manifest
        .lines()
        .filter(|l| !l.trim_start().starts_with('#'))
        .filter_map(|l| l.split('=').next())
        .map(str::trim)
        .filter(|n| n.starts_with("tauri-plugin-"))
        .collect();
    assert_eq!(
        deps,
        ["tauri-plugin-single-instance", "tauri-plugin-deep-link", "tauri-plugin-opener", "tauri-plugin-updater"]
    );
    let tauri = manifest.lines().find(|l| l.starts_with("tauri = ")).unwrap();
    for feature in ["devtools", "unstable", "protocol-asset", "isolation", "macos-private-api"] {
        assert!(!tauri.contains(feature), "the tauri dependency enables {feature}");
    }
    // What is linked, not only what is asked for: these would add page-facing commands or scripts.
    let lock = fs::read_to_string(root().join("Cargo.lock")).unwrap();
    for name in ["tauri-plugin-dialog", "tauri-plugin-shell", "tauri-plugin-fs", "tauri-plugin-http"] {
        assert!(!lock.contains(&format!("name = \"{name}\"")), "{name} is in Cargo.lock");
    }
}

#[test]
fn the_shells_own_page_has_no_inline_script_or_style_and_loads_nothing_from_elsewhere() {
    let ui = root().join("..").join("ui");
    let mut names: Vec<String> =
        fs::read_dir(&ui).unwrap().map(|e| e.unwrap().file_name().to_string_lossy().into_owned()).collect();
    names.sort();
    assert_eq!(names, ["error.css", "error.html", "error.js"]);
    let html = fs::read_to_string(ui.join("error.html")).unwrap();
    let lower = html.to_ascii_lowercase();
    assert_eq!(lower.matches("<script").count(), 1);
    assert!(lower.contains("<script src=\"error.js\" defer></script>"));
    for forbidden in
        ["<style", " style=", "javascript:", "http://", "https://", "//", "<iframe", "<form", "<img", "srcdoc", "<base"]
    {
        assert!(!lower.contains(forbidden), "error.html has {forbidden}");
    }
    assert!(!regex_on_attr(&lower), "error.html has an inline event handler");
    let js = fs::read_to_string(ui.join("error.js")).unwrap();
    let js_code: String = js.lines().filter(|l| !l.trim_start().starts_with("//")).collect::<Vec<_>>().join("\n");
    for forbidden in [
        "innerHTML",
        "outerHTML",
        "insertAdjacentHTML",
        "document.write",
        "eval(",
        "Function(",
        "__TAURI",
        "fetch(",
        "XMLHttpRequest",
        "import(",
        "location.href =",
        "location.assign",
    ] {
        assert!(!js_code.contains(forbidden), "error.js uses {forbidden}");
    }
    let css = fs::read_to_string(ui.join("error.css")).unwrap();
    for forbidden in ["gradient", "url(", "@import", "http"] {
        assert!(!css.contains(forbidden), "error.css has {forbidden}");
    }
}

/// True when the text has an attribute that starts with `on` (onclick, onload, …).
fn regex_on_attr(lower: &str) -> bool {
    let bytes = lower.as_bytes();
    lower.match_indices(" on").any(|(i, _)| {
        let rest = &bytes[i + 3..];
        let end = rest.iter().position(|b| !b.is_ascii_lowercase()).unwrap_or(rest.len());
        end > 0 && rest.get(end) == Some(&b'=')
    })
}

#[test]
fn the_server_binary_and_keys_are_not_in_the_repository() {
    let ignore = fs::read_to_string(root().join("..").join(".gitignore")).unwrap();
    for needed in ["src-tauri/binaries/", "node_modules/", "target/", "*.key", "*.key.pub"] {
        assert!(ignore.lines().any(|l| l.trim() == needed), "desktop/.gitignore must list {needed}");
    }
}

/// Every kind of error the shell opens its error window with has its words in `error.js`; a kind it does not know
/// falls back to "could not start", which would be the wrong thing to tell the person.
#[test]
fn every_error_kind_the_shell_asks_for_is_in_the_error_page() {
    let src = root().join("src");
    let js = fs::read_to_string(root().join("..").join("ui").join("error.js")).unwrap();
    let mut kinds = std::collections::BTreeSet::new();
    for file in ["app.rs", "launch.rs"] {
        let text = fs::read_to_string(src.join(file)).unwrap();
        // fail(app, "kind", ...) and the kinds refusal_text returns: ("kind", ...)
        for marker in ["fail(app, \"", "(\"notserver\"", "(\"start\""] {
            let mut rest = text.as_str();
            while let Some(i) = rest.find(marker) {
                rest = &rest[i + marker.len()..];
                if marker.starts_with("fail") {
                    kinds.insert(rest.split('"').next().unwrap().to_string());
                } else {
                    kinds.insert(marker[2..marker.len() - 1].to_string());
                }
            }
        }
    }
    assert!(kinds.contains("notserver") && kinds.contains("timeout") && kinds.len() >= 6, "{kinds:?}");
    for kind in kinds {
        assert!(js.contains(&format!("    {kind}: [")), "error.js has no text for the error kind {kind:?}");
    }
}

// ---- the update feature (docs/UPDATES.md) ----------------------------------------------------------------------------

/// The text of a top-level item from its start (a `fn` line, an `impl` line) to the line that closes it, comments out.
fn item_of(path: &Path, start: &str) -> String {
    let code = code_of(path);
    let from = code.find(start).unwrap_or_else(|| panic!("{} has no {start:?}", path.display()));
    let rest = &code[from..];
    let end = rest.find("\n}").map(|i| i + 2).unwrap_or(rest.len());
    rest[..end].to_string()
}

#[test]
fn a_package_is_installed_by_one_call_that_needs_the_proof_and_by_nothing_else() {
    let src = root().join("src");
    let mut calls = Vec::new();
    for path in files(&src, "rs") {
        let code = code_of(&path);
        for pattern in [".install(", "download_and_install", "install_inner", "installer_arg", "installer_args"] {
            for _ in code.matches(pattern) {
                calls.push((path.file_name().unwrap().to_string_lossy().into_owned(), pattern));
            }
        }
    }
    assert_eq!(calls, [("updater.rs".to_string(), ".install(")], "the install call must be one, in updater.rs");
    let updater = item_of(&src.join("updater.rs"), "fn install_package(");
    assert!(updater.contains("_permit: &InstallPermit") && updater.contains(".install(bytes)"), "{updater}");
    // The proof is made in one place, from the pure decision, and the restart takes it before it stops anything.
    let policy = code_of(&src.join("update_policy.rs"));
    assert_eq!(
        policy.matches("InstallPermit(())").count(),
        2,
        "the proof is made in one place (and named in its own type)"
    );
    assert!(policy.contains("(kind(os, bundle) == Kind::Install).then_some(InstallPermit(()))"));
    let restart = item_of(&src.join("updater.rs"), "fn do_restart(");
    let permit = restart.find("install_permit(").expect("do_restart asks for the permit");
    assert!(permit < restart.find("stop_server").unwrap() && permit < restart.find("install_package(").unwrap());
    // The save, the clean stop and the hiding of the windows come before the install, in that order.
    let order: Vec<usize> = ["save_pages(app)", "app::stop_server(app)", "w.hide()", "install_package(&permit"]
        .iter()
        .map(|m| restart.find(m).unwrap_or_else(|| panic!("{m}")))
        .collect();
    assert!(order.windows(2).all(|w| w[0] < w[1]), "the restart's order changed: {order:?}");
}

#[test]
fn no_code_path_reads_an_update_source_from_anywhere_but_the_configuration() {
    let src = root().join("src");
    for file in ["updater.rs", "update_policy.rs", "update_cache.rs"] {
        let code = code_of(&src.join(file));
        for forbidden in [
            ".endpoints(",
            ".pubkey(",
            ".target(",
            ".header(",
            ".headers(",
            ".proxy(",
            ".timeout(",
            ".connect_timeout(",
            ".read_timeout(",
            ".redirect(",
            ".configure_client(",
            "dangerous",
            "env::var",
            "env::args",
            "UpdaterBuilder",
            "set_endpoints",
        ] {
            assert!(!code.contains(forbidden), "{file} uses {forbidden}");
        }
    }
    // The deadlines and the redirect policy live in ONE audited place, `update_net.rs`: no other file sets a timeout or a redirect.
    // That file sets no address, key, target, header or proxy either.
    let net = code_of(&src.join("update_net.rs"));
    for forbidden in [
        ".endpoints(",
        ".pubkey(",
        ".target(",
        ".header(",
        ".headers(",
        ".proxy(",
        "dangerous",
        "env::var",
        "set_endpoints",
    ] {
        assert!(!net.contains(forbidden), "update_net.rs uses {forbidden}");
    }
    assert!(net.contains(".timeout(") && net.contains(".redirect(https_only())") && net.contains("MAX_PACKAGE"));
    for path in files(&src, "rs") {
        if path.file_name().is_some_and(|n| n != "update_net.rs") {
            let code = code_of(&path);
            for pattern in [".timeout(", ".connect_timeout(", ".read_timeout(", ".redirect(", ".configure_client("] {
                assert!(
                    !code.contains(pattern),
                    "{} sets {pattern}; the update client's deadlines are in update_net.rs only",
                    path.display()
                );
            }
        }
    }
    // The plugin is registered as it was: no builder call that sets an address, a key or a target.
    let app = code_of(&src.join("app.rs"));
    assert!(app.contains(".plugin(tauri_plugin_updater::Builder::new().build())"));
    // The one use of the plugin's builder only notes the version it compares.
    let updater = code_of(&src.join("updater.rs"));
    assert_eq!(updater.matches("updater_builder()").count(), 1);
    let chain = updater.split("updater_builder()").nth(1).unwrap().split(".build()").next().unwrap();
    assert!(chain.trim_start().starts_with(".version_comparator("), "{chain}");
    assert!(
        updater.contains("update_net::harden(app.updater_builder()"),
        "the check does not go through the deadlines"
    );
    assert!(
        updater.contains("update_net::bounded_download("),
        "the download does not go through the size and time limits"
    );
    assert!(!updater.contains(".download("), "a download that bypasses update_net");
    assert!(!chain.contains(".endpoints") && !chain.contains(".pubkey"));
}

#[test]
fn a_release_build_has_no_switch_that_starts_or_answers_an_update() {
    let app = fs::read_to_string(root().join("src").join("app.rs")).unwrap();
    let release = app.split("#[cfg(not(debug_assertions))]\n    fn from_args").nth(1).expect("the release from_args");
    let release = release.split("\n    }\n").next().unwrap();
    for flag in ["--update-now", "--update-restart", "--restart-now", "--auto-dialogs", "--sidecar"] {
        assert!(!release.contains(flag), "a release build reads {flag}");
    }
    assert!(release.contains("updater::Debug::default()"));
    assert!(app.contains("#[cfg(debug_assertions)]\n    fn from_args") && app.contains("\"--update-now\""));
}

#[test]
fn the_daily_check_and_the_background_download_show_nothing_and_take_no_focus() {
    let updater = root().join("src").join("updater.rs");
    let mut background = String::new();
    for start in [
        "async fn background(",
        "async fn keep_or_note(",
        "fn note_available(",
        "async fn download_and_keep(",
        "fn schedule(",
        "impl ScheduleEnv for Real",
        "async fn fetch(",
        "fn restore_kept(",
        "fn restore(",
    ] {
        background.push_str(&item_of(&updater, start));
    }
    // (`dialogs::note` writes a line in a development build; it is not a dialog.)
    let background = background.replace("dialogs::note(", "note(");
    for forbidden in
        ["dialogs::", "focus(", "set_focus", "show(", "navigate", "go(", "offer_outside", "restart(", "stop_server"]
    {
        assert!(!background.contains(forbidden), "the background path uses {forbidden}");
    }
    // The only dialogs that come unasked are the notice of an install that failed and the first question, in `say_what_is_due`:
    // only on a page where nobody is typing, only when the question was never answered, and with the safe button first. The page
    // is never touched: the shell injects nothing and evaluates nothing (the test above), so the notice is the menu.
    let due = item_of(&updater, "fn say_what_is_due(");
    assert!(
        due.contains("Consent::Unasked")
            && due.contains("policy::first_run_question")
            && due.contains("wait_until_calm(app)")
    );
    assert!(
        due.contains("dialogs::ask_safe_first(") && !due.contains("dialogs::ask("),
        "the first question must have the safe button first"
    );
    assert!(
        due.find("wait_until_calm(app)").unwrap() < due.find("dialogs::").unwrap(),
        "a dialog before the wait for a calm page"
    );
    let up = item_of(&updater, "pub fn window_is_up(");
    assert!(
        !up.contains("dialogs::ask") && !up.contains("first_run_question"),
        "the question must not be asked from the start-up path"
    );
}

#[test]
fn the_public_update_key_is_committed_and_is_a_public_key() {
    let key = fs::read_to_string(root().join("..").join("updater.pub")).unwrap();
    let key = key.trim();
    assert!(
        key.bytes().all(|b| b.is_ascii_alphanumeric() || matches!(b, b'+' | b'/' | b'=')),
        "not one line of base64"
    );
    // base64 of "untrusted comment: minisign public key: "; a private key's comment is another text
    assert!(
        key.starts_with("dW50cnVzdGVkIGNvbW1lbnQ6IG1pbmlzaWduIHB1YmxpYyBrZXk6"),
        "desktop/updater.pub is not a minisign public key"
    );
    assert!(key.len() < 200, "a public key is short; this is not one");
    // The committed configuration still carries no key and no address that works: only a release build for a tag has them.
    let conf = fs::read_to_string(root().join("tauri.conf.json")).unwrap();
    assert!(!conf.contains(key), "the public key is in the committed configuration; it is injected at build time");
}

/// rfd decides what a closed dialog reports, and each backend does it its own way. Only the `gtk3` backend (and Windows' common
/// controls) was reviewed: a new feature (`xdg-portal`, `zenity`, ...) is another backend whose answer for a closed window is
/// unknown, so it fails here until someone has looked, and the first question can only be answered yes by the explicit label.
#[test]
fn a_new_dialog_backend_is_reviewed_before_it_can_answer_the_first_question() {
    const REVIEWED_RFD_FEATURES: [&str; 2] = ["common-controls-v6", "gtk3"];
    let manifest = fs::read_to_string(root().join("Cargo.toml")).unwrap();
    let line = manifest.lines().find(|l| l.starts_with("rfd = ")).expect("the rfd dependency");
    assert!(line.contains("default-features = false"), "rfd's default features choose a backend: {line}");
    let features: Vec<&str> = line
        .split("features = [")
        .nth(1)
        .and_then(|f| f.split(']').next())
        .map(|f| f.split(',').map(|x| x.trim().trim_matches('"')).filter(|x| !x.is_empty()).collect())
        .unwrap_or_default();
    for f in &features {
        assert!(
            REVIEWED_RFD_FEATURES.contains(f),
            "rfd feature {f:?} enables a dialog backend that was not reviewed. Check what it reports for a closed dialog, extend \
             `dialogs::is_explicit_other`'s tests, then add it to REVIEWED_RFD_FEATURES here"
        );
    }
    let dialogs = code_of(&root().join("src").join("dialogs.rs"));
    let safe_first = item_of(&root().join("src").join("dialogs.rs"), "pub fn ask_safe_first(");
    assert!(
        safe_first.contains("is_explicit_other(&answer"),
        "ask_safe_first does not use the explicit mapping: {safe_first}"
    );
    assert!(dialogs
        .contains("other != safe && matches!(answer, rfd::MessageDialogResult::Custom(label) if label == other)"));
}
