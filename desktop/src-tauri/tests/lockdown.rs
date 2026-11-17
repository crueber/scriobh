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
