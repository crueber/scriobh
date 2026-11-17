# Phase 8: desktop mode, the Go side (section BD)

Branch `p8-desktop-go`, cut from `phase-7`. The Go binary and the web layer only; the Tauri shell is another branch. Rows: [FEATURES § BD](../FEATURES.md#bd-implementation-status-phase-8-desktop-mode-the-go-side). The protocol between the two, and every place this side differs from it: [desktop-contract.md](desktop-contract.md). Who can reach the port and what stops them: [desktop-threat-model.md](desktop-threat-model.md). Operating it: [OPERATIONS, "Desktop mode"](../OPERATIONS.md#desktop-mode).

Nothing here was run with the shell. See "Not verified" at the end.

## What it is

`SCRIOBH_DESKTOP=1` turns the same binary into the sidecar of a desktop shell. With the variable unset nothing below exists: no route, no check in the middleware chain, no change to configuration validation, and the seven `desktop.*` ops answer "not found" to everyone (`TestDesktopOffChangesNothing`, `TestWithoutDesktopModeNothingChanges`, `TestWithoutDesktopModeASecondInstanceIsAsBefore`).

| Piece | Where | What |
| --- | --- | --- |
| Configuration | `internal/config/desktop.go` | `SCRIOBH_DESKTOP`, `SCRIOBH_LAUNCH_TOKEN`; the refusals (an administrators' group among them); the data directory in the system's place; the administrators' group cleared so the first-user rule applies |
| The issuer | `internal/desktopidp` | discovery, key set, authorization endpoint, token endpoint; the launch gate; the Host and own-navigation checks; the signing key file; profile validation. No dependency on the server; compiled into `-tags prod` builds |
| State and ops | `internal/domain/desktop` | the profile and the identity-provider state (two `settings` rows), the switch, the look for a local model, the seven ops |
| Wiring | `internal/server/desktop.go` | mounts the issuer at `/desktop-idp/`, `/desktop/start`, `/desktop/profile`, the Host check, the pages, the callback of the proving sign-in |
| Start | `cmd/scriobh/desktop.go`, `main.go` | exit 75 and `ALREADY_RUNNING`, a pending switch made before the server is built, the fixed port, the recovery command |
| Relying party | `internal/auth/oidc.go`, `users.go` | the issuer and client resolved for desktop mode, the pinned client, the relying party for a named provider, the one sign-in exception for a profile with no address |
| Outbound observer | `internal/netguard/observe.go` | every dial site of the program tells the observers; a source test lists the sites |

## Routes and ops

Routes, registered in desktop mode only:

| Route | Who | What |
| --- | --- | --- |
| `GET /desktop/start?t=` | the shell's window | the launch token for the launch cookie, once; then 303 to `/` |
| `GET`, `POST /desktop/profile` | the window (launch cookie), while no profile exists | the first-run form: name, optional email; makes the profile; 303 to `/auth/login` |
| `/desktop-idp/.well-known/openid-configuration`, `/jwks`, `/authorize`, `/token` | see the threat model | the embedded issuer; not mounted while the instance signs in through an external provider |
| `GET /desktop/identity` | administrator | which provider is in use, the external provider's settings, the switch, the way back |
| `GET /desktop/identity/verify` | administrator, or `?ticket=` in a web browser with no session; not from another site | begins the sign-in at the external provider that proves its settings |
| `POST /ui/desktop/identity/{save,ticket,switch,back,cancel}` | administrator, session token | the forms of that page (`ticket` makes the address for a web browser) |
| `GET /desktop/local-model` | administrator | the page for a model on this computer; asks nothing by itself |
| `POST /ui/desktop/local-model/{detect,use}` | administrator, session token | the look, and adding the provider |

Ops (group `desktop`; every one is for an administrator's browser session, hidden from everyone else, refused to API keys, and excluded from MCP on purpose in `internal/mcp/catalog.go`):

| Op | Route | What |
| --- | --- | --- |
| `desktop.info` | `GET /api/v1/desktop/info` | `data_dir` (absolute), `version`, `mode`, `outbound_connections`, `outbound_elsewhere` |
| `desktop.idp.get` | `GET /api/v1/desktop/idp` | the running and stored mode, a pending switch, the provider without its secret, who was proved |
| `desktop.idp.set` | `PUT /api/v1/desktop/idp` | store the provider (secret sealed); discards an earlier proof |
| `desktop.idp.verify` | `POST /api/v1/desktop/idp/verify` | record a proof of sign-in the server sealed |
| `desktop.idp.switch` | `POST /api/v1/desktop/idp/switch` | ask for the switch, with the typed confirmation; made at the next start |
| `desktop.idp.cancel` | `DELETE /api/v1/desktop/idp/switch` | drop a switch that is waiting |
| `desktop.localmodel.detect` | `POST /api/v1/desktop/local-model/detect` | one request to `127.0.0.1:11434/api/tags`; the models it lists |

The local profile is made by the handler through `desktop.CreateProfile`, not by an op: nobody is signed in yet, as with `Users.SignIn` and `Users.ClaimAdmin`, which are not ops either. It is create-only and audited (`desktop.profile.created`).

**The snapshots are stale on this branch on purpose.** The ops are registered, so `internal/cli/snapshot.json`, `skills/`, `internal/mcp/tools.snapshot.json` and the op table of `docs/API.md` need `make skill`; the coordinator runs it at the merge. Until then `TestCommittedToolsAreCurrent` (in `internal/mcp`) and `make skill-check` fail.

## Decisions

**No migration.** The profile and the identity-provider state are two JSON rows of the existing `settings` table (`desktop.profile`, `desktop.idp`). A backup carries the database and nothing else from the data directory, so the profile's subject has to be in the database or a restore would bring back the manuscripts and lose the person. The signing key is a file (`desktop/issuer-key.json`) because it can be made again; it is not in a backup and does not need to be. The migration range 1970 to 1989 is unused.

**The first user is the administrator by the existing rule, applied without its page.** The existing rule: with no administrators' group, the first account may claim administration once. Desktop mode clears the group (unless it is set explicitly) so that rule applies, and when the sign-in through the embedded issuer returns "this person may claim", the claim is made at once with the same function. The alternative was to send a first-time desktop user through the "Claim administration" page, which names an issuer and a group they have never heard of.

**A profile with no email address.** Sign-in keeps refusing an identity with no verified address, except for the fixed issuer string `urn:scriobh:desktop-profile`. That string is produced only by the relying party when desktop mode's embedded issuer is in use; no token from an external provider can carry it. The address is stored NULL, not empty (the unique index on addresses is for addresses that exist).

**No insecure-issuer check existed to make an exception to.** The brief expected one; the relying party never checked the issuer's scheme (the development issuer has always run over http). No general allowance was added. What desktop mode adds instead is narrower than an exception: the client that talks to the embedded issuer dials the address this process bound, whatever the URL says, with no proxy. An external provider entered in the desktop settings must be https, or http on loopback.

**The switch to an external provider is proved before it is made.** See the contract's change 20 for the rule and why its order differs. The safest alternative considered was "the external identity becomes a new account": it leaves the manuscripts with an account nobody can sign in to, makes an external sign-in with the profile's own address fail (the existing rule refuses an address that belongs to a bound account), and leaves no administrator unless a group is mapped, so there would be no session to switch back from. Proving first avoids all three and never matches by address.

**A recovery from the command line.** `scriobh desktop use-local-profile` is outside the contract ("switching back is possible only from a signed-in admin session"). Without it, a provider that goes away while nobody holds a session leaves the manuscripts unreachable through the application. It takes the data directory's lock (so it refuses while the application runs) and only records the request; the start makes it.

**A restored server database.** A data directory with accounts and no local-profile account refuses the profile (no one is promoted, nothing is linked by email). The refusal page says in desktop terms what happened, and `scriobh desktop use-local-profile --adopt-admin <email-or-id>` is the way out: stopped application, data directory lock, one named administrator, applied at once in one transaction, audited. Contract change 28. It does not infer anything: no email match, no "oldest administrator" guess, and it refuses when the profile already has an account.

**The Host check is global and answers 421.** It costs one string comparison a request and covers the unauthenticated surface too.

**Signing out is respected.** Without the marker cookie a person who signs out would be signed in again by the next page load, because the window still holds the launch cookie.

**Sessions are unchanged** (24 hours, 8 idle by default). When one ends, the next page load signs the person in again through the issuer without a page.

**The outbound count is always on in desktop mode.** `netguard.Observe` was added for the offline test; the same observer feeds `desktop.info`, so the claim "nothing left this computer" can be checked on a running production build, by the shell or by a person, and not only in a test.

**Pseudo-locales.** They were offered only with the development issuer, which desktop mode refuses. A development build in desktop mode now offers them too, so the profile page can be checked right to left; a production build never does.

**What the shell found in WKWebView, and what was done.** The shell engineer ran the application in the real macOS webview against this branch at `436239c`. Each finding is the Go side's or the web layer's, and each fix applies to every deployment unless it says desktop mode:

- *Sign-in broke once the service worker was active.* WebKit marks a navigation the worker re-sends as `same-origin`, `navigate`, `empty`; the authorization endpoint required `document`. The endpoint now judges by `Sec-Fetch-Site` and `Origin` (contract change 11), and the worker no longer answers navigations to `/auth/`, `/desktop/` and `/desktop-idp/` (`web/static/sw/worker.js`; still under its 1536-byte budget). Chromium sends `document`, so no Chromium test can fail the way WebKit did: the header shape is pinned in Go (`TestAuthorizeIsOnlyForThisInstancesOwnNavigation`, `TestDesktopOnlyTheShellsWindowCanSignIn`), and `e2e-desktop` signs in with the worker active.
- *`window.confirm()` returns false with no panel.* The four call sites (sign-out with unsaved text, resolve an alternative, delete a compile preset, delete a stash item for good) now ask in the page through `scriobh.confirm` (`js/shell.js`), which uses the existing `js/arm.js` panel: asked where the button was, focus on the "no" button, the "yes" button off for half a second, Escape and "no" put the focus back. `arm.js` is fetched the first time it is needed (the layout names its address in a `meta`), so the shell's script budget grew by the few lines of the loader only. The sign-out's words are in the catalogue (`confirm.*`) and the compile page's in its own (`cmp.js.*`); the stash's and the alternatives panel's are English in the script, like every other word of those two panels. `TestNoBrowserDialogs` (`internal/repocheck`) fails if `confirm(`, `alert(` or `prompt(` appears in the application's scripts or the editor's sources again, or `hx-confirm` in a template.
- *`window.print()` opens no panel.* There is one call site, the share page's Print button (`js/share.js`); the compile page's "print" is the print proof and the PDF download, which are server-side and call nothing in the browser. The button still asks the browser, and when no print event follows the page says where printing is (the application's or browser's own Print command, and the Download button when the link allows downloads).
- *Links that should save showed the file instead.* The three attachment links of the file page and the two edition download links carry `download` now; links scripts build from a blob always named the file. `TestEveryLinkThatSavesAFileSaysSo` keeps it so. **The attribute is not free in a browser** (found by the release gate, in Chromium): a link with `download` saves whatever answers it, so the edition download's old answer to a missing file, a redirect to the edition's page with a one-time sentence, was saved as a file named for the book. The Editions pages now look at the disk (`editions.Present`: a regular file of the recorded size, no hashing) and put the sentence where the link was, and the download itself answers 404 with the sentence as plain text (`TestTheEditionsPagesFollowTheRoles`, `e2e-editions`, which clicks a link on a page drawn before the file went and fails if a file is saved). Not covered: a session that ends between drawing the page and pressing the edition's link is answered with a redirect to sign-in (a Go test pins that), which a browser saves under the `download` attribute by the same mechanism (not driven in a browser); the file page's links answer a 401 JSON error for it (read in `serveFile`, not driven); a project that locks in between was not checked. Nothing was changed for these.
- *A killed shell left its server running.* Contract change 23: standard input closing stops the server.
- *Browser storage was lost at every quit.* Contract change 24: the port is kept. **What is lost when the port does change** (it was taken by something else): what the browser keeps per origin, which is the writing view's local copy of text that had not reached the server (`scriobh:draft:*`), fields held while a save was failing (`scriobh:held:*`, scratchpad and research drafts), the reading position, and the service worker with its cache of static files. Everything the server saved is untouched, and the server-side copy is the truth; only text typed while saves were failing, in a window that was then closed, is at stake. The log names the change.
- *`localhost`.* Contract change 10.
- *An external provider's page cannot be shown in the window.* Contract change 25: the window is not the way in for such an instance, and says so. The same is true of the sign-in that proves the provider *before* the switch, which the first version began with a link in the window: contract change 26 adds the address for a web browser (a one-time ticket). The route still answers an administrator's own session (the tests use it); the page no longer links to it, since nobody on the embedded issuer is reading it in a browser.

**The sign-in bucket.** Found while testing the above: the per-address limit on `/auth/` (ten at once) refused a person who signed out and in a few times on a desktop instance, where every request has the same address. It does not apply on the embedded issuer (contract change 27).

## Limits

| What | Limit |
| --- | --- |
| Launch token | hexadecimal, 32 to 256 characters; once; a wrong one is a 404 and counts for nothing (no lockout: contract change 7) |
| Authorization code | 60 s, one attempt, at most 32 waiting |
| ID token | 2 minutes |
| Authorization parameter | 512 bytes each; a repeated parameter is refused |
| Token request body | 16 KiB |
| Profile | name 80 characters, address 254 |
| Signing key file | read up to 16 KiB; anything else is replaced |
| Proof of sign-in at a provider | 5 minutes, one administrator, one set of settings |
| Ticket for a web browser | 5 minutes, one administrator, one use |
| Remembered port | `desktop/port`: read up to 16 bytes, a number from 1024 to 65535, anything else ignored |
| The look for a model | one GET, 3 s, 1 MiB, 200 models, 200 characters a name, no redirect, loopback only |
| Provider settings | issuer 512, client id 256, secret 1024, group 128; port 1024 to 65535 or none |

The limits on what another party sends (the token, the codes, the parameters, the token body, the key file, the proof, the model server's answer, the profile fields) each have a test that passes them (`internal/desktopidp`, `TestDetectTreatsTheAnswerAsHostile`, `TestDesktopProfileFormIsProtected`, `TestDesktopSwitchCannotLockTheAdministratorOut`). Of the provider settings, the issuer's shape, an empty client id and the port's range are tested; the length limits of the secret and the group names are not.

## Measured

On the development machine (Apple silicon, macOS), production build, desktop mode, loopback:

| What | Time |
| --- | --- |
| First start of an empty data directory to `/healthz` (all migrations) | 0.8 s |
| A later start to `/healthz` | 0.2 s (median of 5; maximum 0.2 s) |
| The start address to the library, signed in (five redirects, discovery, token exchange, verification) | 5 ms (median of 5; maximum 7 ms) |
| The profile form's POST to the library, signed in | 3 ms |
| `SIGTERM` to exit 0 | 6 ms |

The budget for the shell is the contract's: `/healthz` within 30 s, exit within 10 s. No performance gate of `make bench` is touched; with desktop mode off the request path gains nothing (the Host check is not in the chain), and with it on it gains one comparison.

## Tests

| Where | What |
| --- | --- |
| `internal/desktopidp` | the issuer against go-oidc and oauth2 (three client auth styles); the launch cookie; cross-site, frame, fetch and same-site requests; a foreign Host on all four endpoints; exact client and redirect; PKCE; a code's life and single use; the bounded store; no CORS; refusals reported without the request; the launch gate with an injected clock; the key file |
| `internal/config` | desktop defaults and every refusal; the data directory rule of each platform; nothing changes with the variable off |
| `internal/auth` | the one sign-in exception; how the relying party resolves; `Owns`; the pinned client |
| `internal/domain/desktop` | the profile made once; the switch moves only the proved account, and each way it is refused; the way back; the recovery; the look for a model against hostile answers |
| `internal/netguard` | the observers; `TestEveryDialSiteIsObserved` (a source scan: a new HTTP client, transport or dialer anywhere in the program fails until it is listed with how it is observed) |
| `internal/server` (`desktop_test.go`, `desktop_idp_test.go`) | the first run and the next launch end to end in process; the token; strangers; the form; the Host check; `desktop.info`; the ops' authorization in desktop mode; the local model page; secrets sealed with the environment's key and with the key file; **a whole session with every outbound connection observed** (`TestDesktopWithAIOffNothingLeavesTheMachine`); the switch to a provider and back, its refusals, the recovery; desktop off |
| `cmd/scriobh` (`desktop_e2e_test.go`) | the production binary: refusals, the data directory and its modes, exit 75, the first run, `desktop.info`, no secret in arguments, log or data, `SIGTERM` and `SIGINT`, the write-ahead log checkpointed, the next launch, the recovery command |
| `scripts/desktop-smoke.sh` | the same production binary driven with `curl` |
| `scripts/e2e-desktop.mjs` | Chromium against the production build (in `e2e-fast`): eleven steps, among them sign-in with the service worker active and a draft found again after a restart on the same port |
| `internal/repocheck`, `internal/server/savelinks_test.go` | no browser dialogs in the scripts; every link that saves a file says so |
| `scripts/e2e-library.mjs`, `e2e-gate.mjs`, `e2e-alts2.mjs`, `e2e-compile.mjs`, `e2e-share.mjs`, `e2e-write.mjs`, `e2e-review3.mjs` | changed to answer the in-page question instead of a browser dialog, or to check it; all seven were run on the final code and pass |
| `scripts/e2e-a11y.mjs` | the profile form, the identity page and the local model page, each in two states, on a second server in desktop mode: 0 violations |

`TestDesktopSwitchToAnExternalProviderAndBack` and its neighbours use the development issuer as the external provider (a separate HTTP server in the test), so that file has the `!prod` build tag.

## Not verified

- **Anything with the Tauri shell**: the spawn, the handshake, the webview's cookies and `Sec-Fetch-*` headers, menus, quit and relaunch. The Go side was driven by Go tests, `curl` and Chromium only.
- **WKWebView itself.** The fixes for what the shell found there were tested with WebKit's header shape in Go and with Chromium; they were not run again in the webview from this branch.
- **Sign-out from the command palette with unsaved text** (the question then opens the account menu while the palette closes) is not driven by a browser test; sign-out from the menu, and from a script with the menu closed, are.
- **The stop on standard input was never run on Windows**, where it is the only clean stop.
- **The in-page question for a stash item and for an alternative** uses the same call as sign-out and the compile preset, which are driven in Chromium; those two buttons themselves are not pressed by any browser test.
- **A real keychain.** The tests pass the key by environment, which is what the shell does after reading the keychain. No prompt, no keychain failure path.
- **Windows.** `GOOS=windows go build -tags prod ./cmd/scriobh` succeeds; the exclusive-open lock, the `%AppData%` rule beyond its unit test, and file modes there were never run.
- **Linux at run time on this branch.** `GOOS=linux go build -tags prod` succeeds and the tests avoid macOS-only tools (the data directory rule is chosen by `runtime.GOOS`, `ps` and `lsof` are optional, `stat` has both spellings in the smoke test), but the suite was run on macOS only. The CI runner is where Linux is proved.
- **Signed and notarised builds, installers, the auto-updater**: not the Go side's.
- **A real external provider.** The switch was tested against the development issuer standing in as one. Not tested: a provider that requires an exact redirect port (the fixed-port setting exists for it; the bind and the fallback are not covered by a test), providers that send `email_verified` as a string, and providers that rotate keys during the proof.
- **A real Ollama.** The look was tested against stand-in servers; on the development machine nothing was listening on 11434, so the page's "found" state was seen only with the stand-in.
- **WebKit and WebView2.** The browser tests are Chromium.
- **Restoring a server's backup into a desktop instance, or the reverse.** Tried after review for the first direction, with test databases and the production binary (`TestDesktopAdoptAdminCommand`); not tried with a real server backup, and not the reverse (a desktop database run as a server).
- **The whole `make ci` and the whole `make e2e`** were not run on this branch (by instruction).
