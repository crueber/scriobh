# Phase 7 screens: mail

Branch `ui-7-mail`. The screens for the mail backend (`docs/p7-notes/mail.md`, section AV): the administrator's relay page, the Kindle address and the digest on the account page, Send to Kindle on the Compile page with its own page for the preview, the confirmation and the send log. FEATURES section AX, F-947 to F-951.

No real mail server, Amazon account or Kindle was used. The browser script talks to a small SMTP relay written for it (`scripts/fake-smtp.mjs`); what Amazon does with a message is still the second-hand assumption listed in `mail.md`.

## What is built

| Route | Who | What |
| --- | --- | --- |
| `GET /admin/mail`, `POST /ui/admin/mail/{save,enable,disable,test,domains}` | administrators (404 for everyone else, every form too) | `admin-mail.html`, with the shared `admin-tabs` partial |
| `POST /ui/settings/kindle/{save,delete}`, `POST /ui/settings/digest` | the signed-in person | the `settings-mail` partial on `/settings` |
| `GET /p/{slug}/kindle`, `POST /ui/projects/{id}/kindle/{preview,send}` | project owner and editors (a reviewer or a stranger: 404) | `kindle.html` |
| the `compile-kindle` partial on `/p/{slug}/compile` | owner and editors | one section, one field on `compileData` |

Files: `internal/server/mailui.go` (and `mailui_test.go`, `mailui_fixtures_test.go`), `web/templates/{admin-mail,kindle}.html`, `web/templates/partials/{settings-mail,compile-kindle}.html`, `web/static/css/mail.css` (1.2 KB gzip), `scripts/e2e-mail.mjs`, `scripts/fake-smtp.mjs`, the `mail.*` keys in `en.json`. No script file: every screen is a plain form that works with JavaScript off, so there is no `lazyJS` row.

## Decisions

**Send to Kindle has its own page; the Compile page holds a section that leads to it.** The Compile page is built by a script from the ops' schema, and its preview and download are fetches. Send to Kindle is the opposite: a preview that is a document you read, and a token that is spent once. Making it the answer to a form, rendered by the server, means it survives a reload (the preview is simply made again), works without script, and keeps the token out of every place a script could put it. The section on the Compile page says what is missing (mail off, no address, with a link to settings) or shows the stored address and a Preview button, and says that the compile options above it do not apply: the edition is fixed (EPUB, no notes, ghosted text, alternatives or contact block).

**The token.** A GET never calls `kindle.preview`. The preview form's POST answers with the page (`Cache-Control: no-store`) and the token in one hidden field of the confirm form. It is not in the URL, the flash, a `data-` attribute or storage (the script checks each). An error page after a refused send does not carry it back. The send answers with a redirect to the page, whose flash says it is queued.

**`mail.config.set` is tri-state, so the form never says "remove" by being empty.** An empty password field, certificate box or allow-list keeps what is stored; each has its own box ("Remove the stored password", "Remove the stored certificate authority", "Go back to the default"). `mail.config.get` returns only `has_ca` and `password_set`, so the page says "stored" and never prefills. `enabled` is not in the Save form: Save changes settings only, and the page says that an incomplete relay while mail is on switches mail off and drops the queue. A refused save is shown with what was typed except the password, and says the password must be entered again.

**The restore pause.** The banner holds the page's only switch-on form; the separate "Mail on or off" section is not shown while the pause stands. A save, a test and a new password leave the pause where it is (tested through the page).

**Widening the Kindle domains is decided in the handler.** The ops take any valid list. The handler requires the tick when a domain is not already allowed; taking one away or restoring the default needs none. The warning says what widening lets an editor do and that taking a domain away cancels what waits for it.

**Words.** The ops return English sentences (`ClassMessage`, `disabled_reason`, `sender_note`, `notice`, `contents`). The screens use catalogue keys chosen by class or reason instead, so the pseudo-locale test passes; the op's own sentence is shown only for a refusal the catalogue has no word for (a malformed address, a refused setting), as on the other screens. `mail.class.internal` and `mail.class.rate_limited` were missing from the backend's block and are added; an unknown class falls back to the internal one.

**The old Settings page is literal English.** Only the two sections added to it (Send to Kindle, Email digest) are in the catalogue; the rest is as it was. The same for the Compile page: the new section is catalogued, the page's script words were already.

**The log.** `kindle.log` says what each reader may see (the full address for the project's owner and administrators, the domain for an editor); the page says so when the address is withheld. An administrator who is a member as an editor still sees the full address, because the op treats an administrator as the owner.

## What was run

- `go test ./internal/server -run 'TestTheMailAdmin|TestSavingTheRelay|TestSwitchingMail|TestWidening|TestTheTestMessage|TestTheKindleAddress|TestSendToKindle|TestTheOutbox|TestThePseudoLocaleLeavesNoEnglishOnTheMail'`: the page tests (who finds what, what an empty field does, the password in no page, the single switch-on form, the allow-list tick, the test's words, the address and digest states, the preview and its token, the log's two views, no content in the queue) and the pseudo-locale sweep over the admin page, the Compile section, the Kindle page, a preview, the log and the settings sections.
- The page fixtures (`mailui_fixtures_test.go`) join the template lint, structure and CSS-budget tests.

## Browser tests

`scripts/e2e-mail.mjs` (16 steps, about 45 s) against `scripts/fake-smtp.mjs`, a relay written for it (TLS from the first byte, and STARTTLS with the upgrade; a self-signed certificate made with the `openssl` command for 127.0.0.1 and pasted into the page's authority box; a password). Steps: the 404s for a non-administrator; the relay saved and the password never in the page or the message, a test that arrives over TLS signed in, a save with an empty password that keeps it; a wrong password reported by class, the hold, the fix; a second relay that upgrades with STARTTLS; mail on and off and a refused save that keeps what was typed but not the password; the allow-list tick; the address and the digest on settings; Send to Kindle from the Compile page (the EPUB arrives, one attachment, `application/epub+zip`, a plain ASCII name, to the stored address only; the token in no URL, storage or data attribute; a spent preview is refused in words); an editor who is not the owner sees the domain, the owner the address; mail off; the restore pause with one switch-on button (needs `sqlite3`; skipped with a note when missing); the queue with no content; the keyboard path (Enter saves, Tab reaches Save with a focus ring, a collapsed box opens by Enter); 390 px (no sideways scroll, every button, link, field and summary of the new sections at least 44 px, touch emulation); right to left on four pages; the dark scheme (screenshots only); and an offline reload.

Run: `cd scripts && node e2e-mail.mjs [--shots DIR]`. Also run after the final change: `e2e-compile.mjs` (12 of 12 on the second run; see below), `e2e-admin.mjs` (5 of 5), `e2e-themes.mjs` (6 of 6, for the Settings page), and `node e2e-a11y.mjs --only admin-mail`, `--only kindle`, `--only settings`, `--only compile` (0 violations in each, both themes at 1280 and 390 px, forced colours, zoom, reduced motion). The walk's seed now switches mail on (a loopback relay nothing connects to) and stores a Kindle address for the writer, so the Send to Kindle pages have their real states for every page the walk visits; it also walks `admin-mail`, `kindle` and `kindle-preview` (a `pre` step that submits the preview form).

The a11y audit found, and this change fixed: a button row that did not wrap at 320 px (`Go back to the default`), and a text link in the Compile section under the 24 px target size (now a quiet small button).

## Screenshots looked at (real Chromium, not committed)

Admin page at 1280 light, at 390 px and in right to left (`xx-rtl`); the Kindle preview at 1280 light; the Settings page in right to left; the Compile page in the dark scheme. Taken but not looked at: the paused banner, the Kindle page and the Compile page in right to left, the 390 px Settings, Compile and Kindle pages, the dark admin and Kindle pages (the audit, which passed, checks their contrast and reflow). What changed because of them: the certificate-authority summary had no marker and looked like a label (a disclosure triangle was added); "It worked.." had a doubled full stop; times in a right-to-left list ran together (`<bdi dir="ltr">` around times, addresses, hosts and file names); "1 tries" (now "tries: 1"); and the first version of `mail.css` restyled `.hint`, `.more` and `.radios` on every Settings page, so the rules are now scoped to the mail sections.

## Budgets

`mail.css` is 1.2 KB gzip, inside the 25 KB per page; no script, so no `lazyJS` row and no `prefix` row. The page fixtures join `TestCSSBudgetPerPage`.

## Backend gaps found, nothing changed in the backend

- The ops answer in English sentences (see Decisions); the screens map by class and reason. A new `reason` the screens do not know falls back to "It could not be sent: <the op's sentence>".
- `mail.class.internal` and `mail.class.rate_limited` were not in the backend's `en.json` block (added here).
- `mail.config.test` has a five-second gap shared by every administrator; the page says so and handles the 429.

## Not verified

- A real relay, Amazon or a Kindle (see `mail.md`); other browsers than Chromium; a screen reader; a real phone; the desktop smoke test (nothing here is origin- or proxy-specific, but it was not run).
- Offline: a reload with the network cut shows the shared offline notice, and the plain forms do not keep what was typed across it (a form sent while offline does not arrive and nothing is saved; going back to the page restores what was typed in Chromium, which the script checks). There is no script on these pages, so there is no draft kept in storage: that is the one place this falls short of "keep what the person typed" across an offline reload.
- The digest's "needs confirming" state is covered by the Go test (the stored address is changed in the database); the browser script does not change an account's address.
- Two failures seen under load while this was built, reported as failures: `TestStreamAuthorization` timed out once in a full run of `internal/server` while other builds were running (it passes alone, and the package passed in full on the next run: 414 s), and `e2e-compile.mjs` once failed the step that waits for the inspector's do-not-compile switch (a writing-view step this change does not touch; it passed on the next run).

## Files touched outside the mail area

`internal/server/settings.go` (one field and one line), `internal/server/compileui.go` (one field and one line), `web/templates/settings.html` (a stylesheet link and one include), `web/templates/compile.html` (one include), `internal/server/gate_sweep_test.go` (four `notProject` entries), `internal/server/timeline_fixtures_test.go` (the two fixture lists), `internal/i18n/locales/en.json` (one block after `mail.kindle.mail_off`, and `adm.t_*` came with the shared groundwork), `Makefile` (`e2e-mail.mjs` before the audit), `scripts/e2e-a11y.mjs` (seed, three pages), `docs/ARCHITECTURE.md` (one row), `docs/FEATURES.md` (F-947 to F-951). `mailui_fixtures_test.go` also adds a fragment fixture for the shared `admin-tabs` partial (the template lint needs one); if the Git screens add the same, keep one.

## Final gate on this branch

`go test ./... -count=1 -p 2` once (load average about 20 from other builds): every package passed except two timing tests in packages this change does not touch, `internal/webhook` `TestSavePathCostWithWebhooksAndAStalledReceiver` (median 1.48 ms over a 1 ms budget; failed again alone at the same load) and `internal/text` `TestHostileInputsStayBounded` (43 s; passed on a second run). `internal/server` passed in full (489 s). `go test -race` of the mail screen tests passed. `check-contrast.py` was started with the full run and printed nothing, so it is not claimed.
