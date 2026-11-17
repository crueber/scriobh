# Phase 8 notes: the desktop shell

What the shell is, why each piece is the way it is, what was run and seen, and what was only written. The contract it was built to is [desktop-contract.md](desktop-contract.md) (what the shell relies on from the Go side's later changes, and its own deviations, are at its top). How to build and use it: [../DESKTOP.md](../DESKTOP.md). How to release it: [release-runbook.md](release-runbook.md).

Everything here was built and run on one Mac (macOS, Apple silicon, the command-line tools only, no signing identity). **Nothing was compiled or run on Linux or Windows.**

## What is in the tree

| Path | What |
| --- | --- |
| `desktop/src-tauri/` | The shell: a Rust crate (`scriobh-desktop`, library plus a three-line `main`), `tauri.conf.json`, one capability file, the icons |
| `desktop/src-tauri/src/launch.rs` | The launch protocol: token, environment, `LISTEN`, `/healthz`, the stderr tail, stop |
| `…/nav.rs` | Where the webview may go; what is offered to the system browser; where Export… goes |
| `…/deeplink.rs` | `scriobh://open/…` and files handed over by the system |
| `…/filename.rs` | Suggested download names and displayed file names |
| `…/keychain.rs`, `datadir.rs` | The instance key and the keychain; the data folder's place and what is in it |
| `…/downloads.rs` | How many downloads may wait for a save dialog, and which temporary file belongs to which |
| `…/menu.rs`, `dialogs.rs`, `updater.rs`, `app.rs` | Menus; native dialogs; the on-demand update check; the window and the server's life |
| `desktop/ui/` | The shell's one page: the error window (`error.html`, `error.css`, `error.js`) |
| `desktop/testdata/standin/` | A stand-in server in Go for the tests and the smoke. It keeps to the contract as the Go side now has it (no lockout, the `Host` check, no spending of the token by a cross-site request, stop on end of standard input, the port kept); `tests/real_server.rs` asks the same of the real binary when the variable named in the header of `tests/real_server.rs` is set |
| `desktop/scripts/sidecar.sh`, `smoke.sh` | Build the server into the bundle's place; drive a development build with nobody at the keyboard |
| `desktop/package.json`, `package-lock.json` | The Tauri command line, pinned (2.12.5) |
| `desktop.yml` (the development repository's CI workflow) | Dispatch-only builds: Linux (can run here), macOS and Windows (no runners here) |
| `Makefile` | `desktop-deps`, `desktop-sidecar`, `desktop-test`, `desktop-dev`, `desktop-shell-smoke`, `desktop-build` |

**npm.** `@tauri-apps/cli` is a build-time tool, pinned in `desktop/package.json`. It is not in the server, not in the app, and the root rule "no npm at runtime" is unaffected: the shell is a separate build product. On this machine `npx tauri` and `npm run tauri` exit 193 without a word (something in this Node install); `node node_modules/@tauri-apps/cli/tauri.js` works, and that is what the Makefile and the workflow call.

**Versions.** Tauri 2.12.6, wry 0.57.2, tao 0.37.2, `tauri-plugin-single-instance` 2.5.5, `-deep-link` 2.6.3, `-updater` 2.14.1, `-opener` 2.7.3 (its functions only; the plugin is not registered), `keyring` 4.2.0, `rfd` 0.17.2. `Cargo.lock` is committed and the builds use `--locked`. Rust 1.100.0.

## Decisions

### Nothing is reachable from a page

- **No capability grants anything.** `capabilities/none.json` names the shell's windows and has an empty permission list and no `remote` entry. A page from `http://127.0.0.1:<port>` is therefore outside every capability, and so is the shell's own error page.
- **No command, no script, no protocol.** The shell registers no `invoke_handler`, calls no `initialization_script` or `eval`, registers no URI scheme, and `withGlobalTauri` is off. `tests/lockdown.rs` reads the configuration, the capability file, the sources, `Cargo.toml` and `Cargo.lock` and fails if any of that changes.
- **The Tauri dialog plugin is not used.** Registering it adds a script to every page that replaces `window.alert` and `window.confirm` with asynchronous versions. A replaced `confirm` returns a promise, which is truthy: every "are you sure?" in the app would be answered yes. Dialogs are the operating system's own through `rfd`, from one module, from the main loop.
- **The shell plugin is not used.** The server is started with `std::process::Command`.
- **What "injects nothing" means here, exactly.** The shell adds nothing. **Tauri itself adds its own bootstrap script to the main frame of every page it shows**, including the app's: it defines `window.isTauri` and `window.__TAURI_INTERNALS__` (an `invoke` function and helpers). Tauri 2.12 has no switch for this. A page script can therefore tell it is inside the shell and can call `invoke`; every call is refused by the access-control list because no capability applies. This was checked in the running app, not assumed: the stand-in's page script tries sixteen calls (app, window, webview, event, path, updater, deep-link, opener, shell, fs, dialog and a made-up command) and reports back; all sixteen are refused, and `window.confirm` is still the webview's own function. The smoke repeats this at every run. The only way to have no bootstrap at all is to stop using Tauri's webview and drive wry directly; that is a rewrite of `app.rs` with per-platform window code and was not done.
- **The shell's own page can be read by a page script, and that is accepted.** With no CSP on the page, `fetch("tauri://localhost/error.html")` from a `127.0.0.1` page returned the file (and `ipc://localhost/…` answered `500 missing Tauri-Invoke-Key header`; `invoke()` is refused by the ACL). What is served that way is `desktop/ui/`: three static files with nothing in them that is not public, pinned by `tests/lockdown.rs` (the folder must hold exactly `error.html`, `error.css`, `error.js`). The rule that follows: **nothing secret or per-user is ever put in `desktop/ui/`**; the error window's text arrives in the address fragment of the window's own address, which a page cannot read.
- **The server's CSP is untouched.** `app.security.csp` in `tauri.conf.json` applies to the shell's own page only and is stricter than the app's (`default-src 'none'; script-src 'self'; style-src 'self'; img-src 'self'`).

### Navigation

The webview may load `http://127.0.0.1:<port>/…` for the port the server printed, with no user or password part. Everything else is refused in the webview (links, scripts, redirects, `window.open`, frames: a subframe's navigation goes through the same handler, so an outside address in one is refused and offered like any other, which can put the question up without a click; the app's own pages have no outside frames and their CSP forbids them).

- `http`, `https` (with a host, without credentials, at most 2048 characters) and `mailto` are **offered**: a native question shows the address as serialised (ASCII; an international host name in its `xn--` form) and the host, with Open and Cancel. Only Open hands the address to the system. A `mailto:` address first loses its `attach` and `attachment` parameters (names compared after percent-decoding, any case), because some mail programs would attach a local file for them; what the question shows is what is opened. While one question is on screen further ones are dropped.
- **No dialog is ever put up inside a webview callback.** The navigation, new-window and download handlers run on the main thread inside WebKit or WebView2, and Tauri's `run_on_main_thread` runs its task at once when called from that thread (read in `tauri-runtime-wry` 2.12.4, `send_user_message`), which would start a modal loop inside the callback. Every dialog, and the navigation a refused new window turns into, goes through `dialogs::later`, which hands the task to the main loop from another thread. The lockdown test refuses `run_on_main_thread` and `rfd::` anywhere else, and the smoke checks that the refusal is noted before the question is put.
- **`localhost` is not the app.** The first version allowed it, because the contract listed it. The Go side then refused it as a `Host`, an `Origin` and a redirect address (change 10): on a system where `localhost` resolves to `::1` first it would reach whatever listens on the IPv6 loopback at that port (which is not the server, and the port is now stable across launches). `nav::is_app_url` accepts `127.0.0.1` only; a `localhost` link is an outside link, offered to the system browser, and tests pin `localhost`, `LOCALHOST` and `localhost.`. The smoke has a page that navigates to it. Not seen: whether `::1` is tried first by WebKitGTK or WebView2.
- Everything else (`file:`, `javascript:`, `data:`, `blob:`, `about:`, custom schemes, the shell's own `tauri:`) is dropped without a word.
- A new window is never opened. A link that asked for one is treated as a link: an app address loads in the one window, an outside one is offered.
- "Only after the person clicks" is implemented as that question. Neither wry nor Tauri passes a user-gesture flag to the navigation or new-window handler (read in the sources of wry 0.57.2), so the shell cannot tell a click from a script or a redirect; asking is the reading that holds in all three cases.

### Downloads

The webview's download handler has to answer synchronously, on the main thread, inside a WebKit or WebView2 callback, so a save dialog cannot be shown there. The handler accepts a download only from the app's origin or from a `blob:` the app's own page made, points it at a private folder in the shell's cache (`0700`, a random sub-folder per download) under the server's suggested name made safe, and returns. When the webview has finished writing, the save dialog asks where the file should go and the shell moves that one file. Cancelling deletes it. The folder is emptied at start and at exit.

**A bound on what a page can queue.** At most four downloads are in flight, from the webview's go-ahead until the file is placed where the person chose or thrown away (`downloads::Gate`: a place is given back when it is dropped, so every path gives it back, including failed, cancelled and a failed move). The fifth is refused and one calm notice says so; while it is on screen, further refusals add no dialog. A second download of an address that is still being written is refused too: when it finishes the webview names the address and, on macOS, nothing else, so two could not be told apart (a finished download is matched by its path where the webview gives one, otherwise by its address). Not bounded: the size of a file (four at a time can still be large), and a download whose `Finished` event never comes would keep its place until the app ends. Seen: the unit tests for the gate and the table; not seen with real dialogs stacked.

The suggested name: only the last path component (after `/`, `\` and `:`), no control characters, no direction or invisible characters (bidi marks, zero-width characters, the tag block), Windows-forbidden characters replaced, no leading dots, no trailing dots or spaces, reserved Windows device names prefixed, at most 150 bytes with the extension kept.

### Deep links and files

`scriobh://open/<path>` is compared as written, never decoded or normalised. The routes are `/`, `/import`, `/settings`, `/p/<slug>` and `/p/<slug>/d/<id>`; a slug is lower-case letters, digits and inner hyphens (what `projects.Slugify` makes), an id is letters, digits and underscores. A query, a fragment, `%`, `..`, a backslash, a space or a line break can only fail to match. A link that names nothing gets a calm message and no request.

**A file is never read by the shell.** Opening `book.epub` or `notes.md` with the app moves the webview to `/import` and shows a notice naming the file (sanitised, at most 80 characters): the person picks it in the page's own file input. Reading or uploading the file from the shell would be a second way into the app, one that skips sign-in and gives the web layer a filesystem bridge; the invariants rule both out. The file is recognised by its name; the shell does not look at the disk.

Links arrive as launch arguments (Linux, Windows, and a second launch anywhere) or as `RunEvent::Opened` (macOS); both go through the same classifier. The deep-link plugin is registered for what the bundler and installers read from its configuration; its run-time API is not used.

### The key and the keychain

One keychain entry: service `Scriobh`, account `instance-secret-key`, value 64 hex characters. `keychain::resolve` decides, from the keychain and from what `datadir::state` finds in the data folder (two file names, looked at with `try_exists`, so that "could not look" is not "nothing there"; state is looked at before the server is started, in development builds too). The server makes its own `secret.key` whenever it is given no key and finds no file, so the rule is: **never answer "pass no key" where that would put a second key over data that has one.**

| Keychain | Data folder | What happens | Notice |
| --- | --- | --- | --- |
| holds a key | no `secret.key` | that key is passed | none |
| holds a key | `secret.key` present | **nothing is passed; the file is used** | once: two keys were found, the file is used, nothing was deleted |
| empty | `secret.key` present | nothing (the key file is used) | none |
| empty | a database, no `secret.key` | nothing (the server makes a key file) | once: saved credentials need to be entered again |
| empty | neither (first run) | a new key, stored, then read back; used only if it reads back | none |
| cannot be read, or holds something that is not a key | first run, or `secret.key` present | nothing; the keychain is not written | once: the keychain could not be used |
| cannot be read, or holds something that is not a key | **a database, no `secret.key`** | **the server is not started**: the error window says the keychain could not be read, that nothing was started or changed, and to unlock it and choose Restart | every time, in the error window |
| not asked | the folder could not be looked at | **the server is not started**: the error window names the folder | every time, in the error window |

So a key file from a browser-mode install stays the source of truth and nothing sealed is stranded. **The server has no op to export its key, so an existing key file is not copied into the keychain**; the install keeps using the file.

- **Why the stop.** Found in review with the real server: with no key and no file it makes a `secret.key`; credentials saved meanwhile are sealed with that key, anything sealed with the old one cannot be opened, the server switches webhooks it cannot open off for good, and the next launch (keychain readable again) passes the old key and ignores the new file. A locked login keychain, a macOS prompt declined after the app's signature changes, and a Secret Service that is not unlocked yet at autostart all read as an error.
- **The way out is the person's, and it is Restart.** There is no "continue without the key" button; nothing was invented to override the stop. If the keychain stays unreadable for good the app stays at the error window: unlock or repair the keychain, or put the `secret.key` of that install in the data folder (the file is then the key and the keychain is not asked). That is a real limit and the one trade the rule makes: a person with a permanently broken keychain and a database but no key file is locked out until they act.
- **Not driven in a running app: the keychain stop.** A debug build never touches the keychain, so that half is covered by `resolve`'s unit tests (a fake store) and by reading `start_server`. The unreadable-folder half is driven in the smoke (a `chmod 000` data folder: the error window says so, its script ran, no server starts).
- **A keychain that answers "no entry" when it should have had one** (locked in a way that raises no error) has the shape of the stop above and is not caught: the empty-keychain rows apply. Not seen on macOS.
- **A backup of the data folder does not contain the key** when it lives in the keychain. `scriobh backup` already says the key is kept separately; for the desktop app that place is the keychain entry above.
- The key is never logged or written to disk by the shell and is never an argument. **It is not gone from the shell's memory after the spawn.** The buffers the shell reads it into are wiped when dropped, but one copy (and one of the launch token) stays with the running server's handle for as long as the server runs, so that the server's last lines can be scrubbed of them before the error window shows them; that copy is wiped when the server stops. The plain strings built for the child's environment are not wiped, and the child's environment itself is the operating system's: readable by the same user, as the key file would be. A shell that can read the keychain entry at will gains little from wiping its own copy; the brief's "zeroises the buffer after spawn" is met only in that narrower sense.
- "Said once" is an empty marker file in the shell's own configuration folder, one per notice.

### The server's life

- **The program.** The bundler puts the server next to the shell as `scriobh-desktop-server` (`externalBin`, `scripts/sidecar.sh`, `app::sidecar_path`), not `scriobh`: on a Linux `.deb` the old name was `/usr/bin/scriobh`, the path a server package would use. **The rename is unverified in a bundle** (no `tauri build --bundles app` was run after it; `cargo` builds and tests were, with a placeholder file under the new name).
- **Launch arguments** are read with `args_os`; one that is not valid Unicode is left out and one line is noted (it is not printed). `std::env::args()` panicked on it, which in a release build (`panic = "abort"`) ended the app at start. `tauri-plugin-single-instance` reads `args_os` too.
- **Environment.** The child starts from the shell's environment with **every `SCRIOBH_*` name removed**, then the contract's four are set. A variable left in a login profile cannot move the data folder, set a base URL or switch a mode.
- **`LISTEN`** is read strictly: `LISTEN http://127.0.0.1:<1..65535>`, no leading zero, nothing after. Lines are read with an 8 KiB limit.
- **`/healthz`** is one plain HTTP/1.1 request on a socket to 127.0.0.1 with one-second connect and two-second read limits; no HTTP client, no proxy settings. It is answered by whoever holds the port, and the window will be sent the launch token there, so the request carries a fresh challenge (`X-Scriobh-Challenge`, 32 random bytes as hex, new every time) and a 200 counts only with the proof that needs the token (`X-Scriobh-Proof`, Go change 30: an HMAC under a key derived from the token, compared in constant time, exactly one header). A 200 without the right proof ends the launch at once with the error window "Scríobh could not tell its own server apart" and the server that was started is stopped; nothing is sent to the port. The child is also looked at before the first request, 100 ms after the first good answer (a server that prints `LISTEN` and dies at once may not have been reaped yet), and once more immediately before the window is built: `Running::start_url` asks again with a new challenge and gives the address only to a living child whose answer is proven. The cost is 100 ms at every start. **What is left:** the instant between that last check and the window's own request, which the shell cannot close: the server would have to die and another program take its port inside it.
- **The stderr tail** keeps the last 60 lines of 600 bytes. A line is read whole (at most 8192 bytes of it are kept; the rest is read and dropped), scrubbed of the launch token, the key and control characters, and only then cut to 600 bytes; a line that was cut at 8192 also loses a trailing start of a secret. So neither a cut nor a join leaves half a secret. At most 4000 bytes reach the error page, percent-encoded in the address fragment and set as text.
- **Stop.** Standard input is closed (the server honours that on every platform: contract change 23), `SIGTERM` follows on Unix (redundant with this server), the shell waits up to ten seconds and then kills. A quit that arrives while the server is still starting, and a failure after it is up, stop it the same way instead of killing it in the middle of its migrations. Every way out ends in the same place, `RunEvent::Exit`. Seen on macOS, each with a clean server exit and no process left: the Quit item's handler, closing the window (closing the last window quits, on macOS too), the system's quit request (what the Dock's Quit and `Cmd+Q` send, sent here with `NSRunningApplication.terminate()`), and `SIGTERM`. Not seen: a restart for an update.
- **A signal to the shell** (`SIGTERM`, `SIGINT`, `SIGHUP`) is caught and turned into an ordinary exit. Found by test: without it, `kill` of the shell left the server running.
- **A shell that is killed outright no longer leaves its server behind.** The server stops when its standard input, which the shell holds, reaches its end (change 23). Seen with the real server: `kill -9` of the shell stopped it within three seconds; the smoke does the same against the stand-in and starts again on the same port. **One thing found on the way, which was the Go side's and is fixed (`TestDesktopSurvivesClosedOutput`, `real_server_stops_cleanly_when_its_input_output_and_error_all_lose_their_reader`):** a shell that dies closes the server's standard error as well, and the real server then ended with `SIGPIPE` (signal 13 to a parent that closes both pipes) at its next log write, before the clean stop on end of input and so without the final checkpoint. In desktop mode the server now asks to be told of `SIGPIPE`, and the stdin stop does the work.
- **A server that stops by itself** is noticed within 200 ms; the app window is replaced by the error window (Restart, Quit, the last lines). Restart makes a new token and starts again.

### Menus and keys

| Menu | Items |
| --- | --- |
| Scríobh (macOS) | About, Check for Updates…, Settings… (`Cmd+,`), Services, Hide, Quit (`Cmd+Q`) |
| File | Library, Import… (`Mod+Shift+I`), Export… (`Mod+Shift+E`), Print… (`Mod+P`), Close Window; Settings… and Quit here on Linux and Windows |
| Edit | Undo, Redo, Cut, Copy, Paste, Select All, as native roles |
| View | Reload (`Mod+R`), Zoom In (`Mod+=`), Zoom Out (`Mod+-`), Actual Size (`Mod+0`), Toggle Full Screen (`Ctrl+Cmd+F` or `F11`) |
| Window | Minimize, Zoom |
| Help | Documentation, Show the Data Folder; Check for Updates… and About here on Linux and Windows |

Items that open something navigate the one webview to an ordinary page: `/`, `/import`, `/settings`, and for Export… `/p/<slug>/compile` when the webview is on a project page (the shell reads the webview's address; otherwise the library).

**Collisions checked.** The app's own chords with the same modifier, from F-063, F-164, F-411, `web/editor/src/rules.js` and `web/static/js/palette.js`: `Mod+Z`, `Mod+Shift+Z`, `Mod+Y`, `Mod+B`, `Mod+I`, `Mod+Shift+X`, `Mod+K`, `Mod+E`, `Mod+S`, `Mod+Enter`, `Mod+Alt+0` to `3`, `Mod+O`, `Mod+/`, `Mod+Shift+P`, and `Alt+Shift+A/N/P/O/G/S`. None of the shell's accelerators is one of them (`menu::tests` holds the two lists and fails on an overlap). The Edit menu's are the system's own undo, redo, cut, copy, paste and select all.

**Print** is a File menu item that calls the webview's native print, because `window.print()` does nothing in this webview (below).

### Updates

Off, in the strongest sense: the updater plugin is registered and never called, except from Help, Check for Updates…, which first asks, naming the host. The request is one HTTPS GET of a static JSON file; the running version is not in the address. The download is checked against the public key in the app before anything is touched; `requireSignedVersion` is on, so an old, genuinely signed release cannot be passed off as a newer one. The server is stopped cleanly **before** the installer runs, and the windows are hidden, not closed, so the app does not end in the middle of the install. **None of the code after the first question has ever run**: there is no release to check against. Only "updates are not set up" and the tests of what counts as an update source were exercised. The committed configuration has a placeholder key and an `.invalid` host, and with those the menu item says updates are not set up and contacts nothing. The Windows installer is configured not to download WebView2 (`webviewInstallMode: skip`): no outbound request at install.

### Development builds

A build with debug assertions takes `--sidecar`, `--data-dir`, `--keychain-service`, `--auto-dialogs <dir>` and (on a second launch) `--press-restart`. Without `--data-dir` it uses the temporary folder, never the real data folder; without `--keychain-service` it does not touch the keychain; its markers and downloads are in the temporary folder. It writes one-line notes and the server's standard error, scrubbed, to its own standard error. A release build has none of these switches and prints nothing.

## What was run, and what was seen

Commands, on this Mac, at the first hand-back (`dcb4a88`); the review fix round that followed is below this table:

| Command | Result |
| --- | --- |
| `cargo fmt --check` | clean |
| `cargo clippy --locked --all-targets -- -D warnings` | clean (debug and `--release`) |
| `cargo test --locked` | 53 unit tests, 11 launch tests against the stand-in, 7 lockdown tests: all pass; 1 ignored (the real keychain) |
| `cargo test --lib -- --ignored real_keychain` | passes: an entry is made, read back and deleted in the login keychain under a throwaway service name |
| `cargo build` and `tauri build --debug --bundles app` | an unsigned `Scriobh.app` (82.9 MiB, debug) with the server inside, the `scriobh` URL scheme and both document types in `Info.plist` |
| `tauri build --debug --bundles app --config <release overlay>` with a throwaway key | `Scriobh.app.tar.gz` and its `.sig` are made |
| `desktop/scripts/smoke.sh` on the plain binary (46 checks at the tip) and, earlier, on the binary inside the `.app` (37 checks then) | all pass |
| `make desktop-test`, `make workflows-check` | pass |

**After the review fix round** (same Mac, Go side `15aee67`, the server built from it with `go build -tags prod` into a scratch folder, nothing written to that worktree):

| Command | Result |
| --- | --- |
| `cargo fmt --check`, `cargo clippy --locked --all-targets -- -D warnings`, and the same with `--release` | clean |
| `cargo test --locked` | 70 unit tests, 18 launch tests against the stand-in, 8 lockdown, 1 argument test, 2 review launch tests, 1 trickle test, 5 real-server tests (skipped unless that variable is set; `desktop/scripts/test.sh`, which `make desktop-test` and the Linux job run, sets it): all pass; 1 ignored (the real keychain) |
| the same with the variable named in the header of `tests/real_server.rs` set to that binary | the same, and the five real-server tests run and pass |
| `desktop/scripts/smoke.sh` | 54 checks, all pass |
| the review's race test (a child that prints `LISTEN` and exits, another program answering `/healthz`) run 30 times | 30 passes (before the fix it passed or failed with the timing) |
| `make workflows-check` | pass |
| not run | `tauri build --bundles app` after the sidecar rename; the Linux and Windows jobs; the keychain stop in a window; a real save dialog |

What each review finding came to:

- **M1** `localhost` is no longer an app address (`nav.rs`); tests changed to the contract and a smoke check added.
- **M6** the child is looked at before `/healthz`, after the first 200 (with a 100 ms settle) and just before the window is built; a gone server is never sent the token; the review's test passes. The window that remained after it is narrowed by the proof on `/healthz` (Go change 30), which `spawn` and `start_url` require (a program of the same user, which can read the server's environment, is outside it).
- **L1** lines are read whole, scrubbed whole and only then cut; a trailing start of a secret at the 8192-byte cut is removed.
- **H1** the keychain table above; the stop and the file-wins rule are tested; the stop is not driven in a window.
- **M2** `args_os`, a non-Unicode argument is left out with one line.
- **M3** `try_exists`, a new `Unreadable` state, an error window that names the folder; driven in the smoke.
- **M4** the stand-in follows the current contract and its tests and the smoke (kill -9, same port, `localhost`) check it; `tests/real_server.rs` asks the real binary the same questions. It is opt-in by variable, and since the merge `desktop/scripts/test.sh` sets the variable to the server binary that `desktop/scripts/sidecar.sh` built from this tree and refuses to run without it; `make desktop-test` and the three jobs of the development repository's `desktop.yml` workflow run it that way (the workflow has never been run, on any of its three jobs).
- **M5** and **L4** `downloads.rs`.
- **L2** the superscript device names. **L3** the server is asked to stop, with the lock released first, in the two paths that killed it. **L5** `mailto:` attachments. **L6** documented (frames). **L7** the sidecar is `scriobh-desktop-server`, unverified in a bundle. **L8** documented (the page can read `desktop/ui`, which holds nothing secret and is pinned).
- **Stale text** the contract copy's header, `DESKTOP.md`, this file, F-1160 and the neighbouring rows, the runbook (and which release configuration file is meant).

**Seen in the smoke** (a real window, the stand-in server, dialogs answered automatically): the handshake; every call from a page to the shell refused; a second launch exits 0 and leaves one server; a deep link moves the webview, a hostile one does not; a file handed over opens `/import` with the notice; a script, a redirect and `window.open` that leave the origin are refused and the question is put; `window.open` to an app page opens no window; a custom scheme is refused; an attachment, a blob download and a download with a hostile name (`../../../../tmp/.evil<RLO>gpj.command`) are saved under a safe name where the dialog said and nowhere else; `SIGTERM`, the Quit handler and closing the window each give a clean server exit (`Clean(Some(0))`) and no process is left; a server that fails shows the error page (its script ran: the title changed) and Restart starts it again with a new token; a server that dies while the app is open is noticed; a data folder held by another server gives the message and exit 0.

**Seen with the real server** (built from `p8-desktop-go` at 436239c into a scratch folder; nothing was written to that worktree): the shell started it, read `LISTEN`, `/healthz` answered, the webview loaded `/desktop/start` (303), `/` (303), `/auth/login` (302), `/desktop-idp/authorize` (303) and showed **"Your local profile"** with its styles, scripts and service worker; the page was up about 0.4 s after the server's first log line. A deep link to `/import` reached the server. `SIGTERM` to the shell stopped the server with status 0 in under a second. A server started by hand on the same folder made the shell see exit 75. The profile form could not be filled in (no keyboard access from here), so nothing past first run was seen.

**The keychain, in the running app:** two launches with fresh data folders were given the same key (the stand-in reports a fingerprint, never the key); a folder with a database and no key anywhere got no key, no new keychain entry and the notice.

### Found on the way (not the shell's to fix, or not fixable in the shell)

1. **(Fixed on the Go side, change 11; not seen again in a window.) The embedded issuer refused the app's own window once the service worker is active** (WebKit sends `Sec-Fetch-Dest: empty` for a navigation a worker fetched). Proposal 2 in the contract copy. Reproduce without the real server: `STANDIN_SW=1` with the stand-in, then read `/standin/seen`. **This very likely breaks first sign-in on macOS**, since the profile form's redirect back to the issuer happens after the worker has claimed the page; that last step was not seen.
2. **(The four call sites use the page's own `scriobh.confirm` on the Go branch, read in the source; not seen in a window.) `window.confirm()` returns `false` at once and shows nothing in WKWebView** (wry implements no confirm panel). The app called it in four places: signing out with unsaved text (`shell.js`), resolving an alternative (`altpanel.js`), deleting a compile preset (`compile.js`), deleting a stash item for good (`stash.js`). In the macOS app those actions cannot be confirmed. The fix belongs in the web layer (an in-page dialog, which is also better in a browser); the shell must not replace `confirm` by injection. Seen in the smoke at every run (`confirm()` returns `false`, no panel). Windows and Linux were not tried; their webviews are expected to have their own panels.
3. **`window.print()` opens no print panel in WKWebView** (called from a page; the app's window list did not change). The print buttons of the shared page (`share.js`) and compile (`compile-print.js`) are affected. File, Print… in the menu calls the webview's native print instead; that item was not clicked here.
4. **An attachment behind a plain link is shown, not saved, when the webview can display its type** (PDF, images, text): WebKit decides by type, and wry does not look at `Content-Disposition`. Links with the `download` attribute, and blobs, are saved correctly (both in the smoke). The app's plain links are the edition downloads (`/p/<slug>/editions/<id>/download`) and the file downloads (`/files/<id>?download=1`): adding the `download` attribute to those anchors (in `editions.html` and the file manager templates) fixes it and changes nothing in a browser.
5. **(Fixed on the Go side, change 24: the port is kept; seen across a restart, the origin's storage was not looked at again.) Per-origin state did not survive a relaunch.** The port was new at each launch, so the origin was new: `localStorage`, `sessionStorage`, IndexedDB, the Cache API and the service worker registration start empty every time, and the old ones stay on disk under the old origin. Cookies are per host and do survive. Whatever the app keeps only in `localStorage` (a theme choice, unsent text held for recovery) is lost on quit. Not verified which of these the app relies on.
6. **(Answered on the Go side, changes 25 and 26: the window shows a page with an address for a browser.) F-390 could not work inside the app** as the contract stood.

## Not verified

- **Everything on Linux and Windows**: compiling, the webviews, the keychain backends, signals, the Windows quit path, file associations, installers. The Linux job in the development repository's `desktop.yml` had never run when this was written; it has since (forge run 147: the `.deb` built, installed and launched headless), and the macOS and Windows jobs have not.
- **Signed and notarised builds**, the `.dmg`, and **the update round trip** against a release server. Making and signing an update artefact with a throwaway key was seen; checking and installing one was not.
- **Anything that needs a person at the screen**: `screencapture` was refused ("could not create image from display") and System Events timed out, so there are **no screenshots**, no menu item was clicked, no key was pressed and no file was dragged. Not seen: how the window, the menus, the dialogs and the error page look (both themes, 390 px); that each menu item does what its handler says; that the editor's chords (`Mod+Z`, `S`, `E`, `K`, `Alt+0`) reach the editor with the native Edit menu in place; that a dropped `.docx` reaches the import page (the shell's own drop handler is off, which is what lets the page see a drop); the real save dialog and the real questions (the smoke answers them itself, on the same queued path); the Restart and Quit links by a click (the smoke calls what Restart calls); zoom, full screen, print. **One real dialog was shown**, by accident, on the "already running" path before that message went through the dialogs module; the app was killed with it on screen, so even that one was not read.
- **The error page was looked at only in Chromium.** `desktop/ui/error.html` was rendered in the cached headless Chromium inside a 390 px frame, in the light and the dark scheme: the text wraps, nothing overflows, both themes read well, the script fills in the title and the lines. That is the page, not the app's window: WebKit's rendering of it was not seen.
- **Deep links and file associations as the system delivers them** (`open scriobh://…`, double-clicking a file): no bundle was registered with LaunchServices on this machine. The same handler was exercised through a second launch's arguments.
- **Sign-in past the profile form**, and so every page of the real app inside the shell.
- **Budgets.** None is set for the shell. Seen once: about 0.4 s from the real server's first log line to the first page, on a fresh data folder. The debug bundle is 82.9 MiB; no release build was made, so the shipped size is not known.
