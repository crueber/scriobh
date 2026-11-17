# Phase 7 screens: the Git page's new parts and the publish flow

Branch `ui-7-publish`. Section AX of FEATURES (F-940 to F-946). Words under `git.` in the catalogue. The backend is `docs/p7-notes/gitpub.md`; this note is what the screens do, what they decided, and what was not verified.

## Where things are

| What | Where |
| --- | --- |
| The Git page, several remotes, audience, signing, trigger | `internal/server/gitui.go`, `web/templates/git.html`, `partials/git-{parts,live,force}.html`, `git-push.html`, `git-force.html`, `web/static/css/git.css`, `js/git.js` |
| The publish flow and the release panel | `internal/server/publishui.go`, `web/templates/{publish,release}.html`, `partials/release-live.html`, `css/publish.css`, `js/publish.js` |
| A development-only trust seam for the browser tests | `internal/gitmirror/devtrust.go` (`//go:build !prod`), `internal/server/dev_trust.go` (`SCRIOBH_DEV_TRUST_PEM`) |
| The fake code host | `scripts/fakehost.mjs` |
| Browser scripts | `scripts/e2e-publish.mjs`; the Git page's older `scripts/e2e-git.mjs` was changed; `scripts/e2e-a11y.mjs` walks the new pages |
| Go tests | `internal/server/gitui_p7_test.go`, `publishui_test.go`, `internal/gitmirror/devtrust_test.go`; `gitui_test.go` was changed |

Routes: `GET /p/{slug}/git?remote=ID` (the page, one remote chosen), `GET /p/{slug}/git/publish`, `GET /p/{slug}/git/release?remote=ID`, `GET /ui/projects/{id}/release/{rid}?remote=ID` (the release job's fragment), `POST /ui/projects/{id}/git/{action}` (new actions: `audience`, `signing-generate`, `signing-remote`, `trigger-set`, `trigger-rotate`, `trigger-switch`, `trigger-test`, `trigger-delete`), `POST /ui/projects/{id}/publish/{connect,disconnect,plan,publish}`, `POST /ui/projects/{id}/release/{preview,start}`.

## Decisions

**The publish flow is a page, not a dialog.** It has states that have to survive a reload and the back button (an account, a repository, a plan, a refused plan), a plan that can run to hundreds of files, a typed confirmation, and a phone. Every step is a server-rendered form that works without script; `shell.js`'s typed-name gate is the only enhancement on the plan, and the server refuses a wrong name whatever the script does. They survive a reload and Back because every answer is a redirect (see "Fix round" below): the first version answered the plan, a refusal and the secret on the POST address, which a reload re-sent.

**Audience is never defaulted, in the browser or on the server.** The add form and the publish form render both choices with neither checked and `required`; the handlers refuse a form that does not choose (`audience` is not passed to the op as empty, so the op's strict default and the old "notes ticked means private" shortcut are gone from this screen). What each choice sends is said in words on the choice itself ("Everything in the project except secrets"; "Only what a reader sees") and again where the choice is shown afterwards. A remote's audience is changed in its own form, not in Save, so a routine save cannot widen or narrow a remote.

**Widening asks for the name.** The backend asks a person at a browser (a session) when a change touches a remote that is exposed. The screen adds one more step for the same cases: when a remote is public or was made by the publish flow, a change that lets more reach it (public made private, the done-only filter taken off, notes switched on) needs the remote's name typed. Every change of audience or done-only needs a box ticked saying the history starts over. The backend does not check either of those two; the screen does.

**Several remotes by selection.** The page shows the list of remotes and one remote in full, chosen by `?remote=`. Everything that used to assume one remote (the status poll, the force page and its dialog, the redirects after a change, a failed form coming back) carries the choice. The command palette's "Push to Git now" names no remote and the op refuses to guess with several, so the handler pushes each remote that is on and not paused.

**A secret is shown only in the answer that made it.** The trigger's signing secret is rendered in the POST's own response, not in a redirect, a flash or the address, and the response is `no-store`. A trigger address's path is stored sealed by the backend and the page shows only its origin. The account token, the trigger address and the typed confirmations are not kept in the values a refused form comes back with. The connect form's token field is a write-only password field; after a refusal it is empty and the token is not in the page (tested by a canary). The token is not in the address, `localStorage`, `sessionStorage`, cookies or the DOM after the account is connected (`e2e-publish.mjs`).

**Words are in the catalogue, the old ones too.** The Git page, its force page, its push page, the status banners, the hints for each error code, the "saved" notes and the form errors were literal English in templates and in Go; they are now `git.` keys with the same English (tests that looked for sentences were changed only where HTML escaping of an apostrophe made them miss). The presets (`Forgejo`, `GitHub`, `Any Git server` and their notes) are static English data from an op; the page translates them by id on a copy. What is still English: the Markdown copy's status line (`mirrorSummary`, shared with another page), the sentences of an op's own refusal shown beside a form (they are the server's words), the names of things people typed, and a remote's last error text.

**The search in the repository list looks at one page.** `repo.list` filters by name inside the page the host returned, so a name on page 2 is not found by a search made on page 1. The page says so (`git.pub.find_note`) and keeps the page number in the search form. This is a backend limit, listed below.

## Backend gaps found

1. `repo.list`'s `q` filters within one host page of 50 (see above). A search over every repository would need the backend to page through them.
2. The created-but-incomplete state cannot be provoked from outside the package (the seam `afterCreateHook` is in `internal/gitmirror`), so no browser step makes it. The page for it is tested from the error the op gives (`TestACreatedButIncompleteRepositoryIsShownAsSuch`), and the e2e fake cannot make registration fail.
3. Nothing else needed a change in `internal/gitmirror`, apart from the development trust seam (`devtrust.go`, not in a `prod` build; `SCRIOBH_DEV_TRUST_PEM` is listed among the tooling variables in `internal/config/env.go`).

## Known limits of the screens

- A reload with the network cut shows the offline notice and does not keep what was typed in a form (as every server-rendered form of the application).
- Both "only done" and "notes" boxes are shown on the audience form; the one that does not belong to the chosen audience is dropped by the handler without a word (the label says which audience it is for).
- On a remote's audience form the current audience is shown checked, as the remote's state; only the add and publish forms start with nothing chosen.
- The fake code host (`scripts/fakehost.mjs`) is a second copy of the Go fake's behaviour, written from the same documentation; it has already needed two fixes to agree (a repository size, a multipart asset). It proves the screens against what the documentation says, not against a real host.
- `scripts/e2e-a11y.mjs` now needs git and openssl, because it starts the fake host.

## What was run

- `go vet ./...`, `gofmt -l`, `go build -tags prod ./...`.
- `go test ./... -count=1 -p 2` on the merged tip: every package passes except `cmd/scriobh-skillgen` (`TestCommittedSkillIsCurrent`, the snapshots are stale until the coordinator runs `make skill`; the Pandoc schema field and the mail ops are what changed).
- `cd scripts && node e2e-publish.mjs` (22 of 22 on the last run before the merge), `node e2e-git.mjs` (18 of 18), `node e2e-a11y.mjs --only git` (0 violations on 11 pages, before the merge), and on the merged tip the chain recorded in the hand-back.
- Screenshots looked at (Chromium, `--shots`): the Git page with five remotes, the publish form and plan, the right-to-left publish page; the phone Git page is a very tall capture and was checked by the script's overflow and touch-target assertions rather than by eye.

## What was not verified

- Nothing was run against a real GitHub, GitHub Enterprise Server, Forgejo or Gitea.
- Dark themes and Safari, Firefox, real phones and screen readers were not looked at.
- The desktop smoke test (`scripts/desktop-smoke.sh`) was not run.
- The created-but-incomplete state is not reachable from a browser script.

## Fix round after the independent review

What the review found, and what was done. Ops whose schema changed are listed at the end.

- **C1, a credential echoed back: fixed.** An address with a user name or password (`https://user:secret@host`) is refused in plain words before any op is asked (`internal/server/addrguard.go`: the Git forms, the publish forms, the trigger address, the SMTP host, the Kindle address, the release tag, the Pandoc path), never put in a value a refused form shows again, and taken out of any sentence an op wrote (`redactPosted`). `ssh://git@host/...` and `git@host:path` are not refused: `git` is the account Git logs in as. `TestACredentialPastedIntoAnyFieldIsNeverEchoed` posts a canary into every text field of every form of the new pages (scraped from the pages, plus the forms an account or a host must exist to show), all at once and one at a time, and looks for it in the answer, the redirect and the page the redirect leads to; the review's two tests are kept (`review_ui7pub_test.go`, adapted to the redirect).
- **C2, confirmations only on the screens: fixed in the ops.**
  - `git.remote.update` takes `confirm_name`: needed when a change lets more reach a repository that is or may be public (public made private, `only_done` taken off, notes switched on, the address changed), on a remote that is public, was made by publish, or sits at an address that was ever public; also when a private remote is pointed at an address that was ever public (ssh addresses included). It is checked from the row as it is, before anything is written. Without it, or with another name: 409 with `data.git_code = confirmation_required`.
  - `git.remote.create` takes `confirm_name` for a private remote at an address that was ever public.
  - `mail.config.set` takes `confirm_domains`: exactly the domains a new `kindle_domains` list adds to the one in force (case and order do not matter); taking domains away, or going back to the default, needs none. 409 with `data.mail_code = confirmation_required` and `data.adding`.
  - Already in the ops and unchanged: the typed name of `git.push_force`, `repo.publish` for a public repository, `replace` for a new signing key or deploy key, the host-key fingerprint of `git.remote.hostkey_accept`, the Kindle preview token.
  - Left to the screens (a tick or an Arm box, not a widening of what reaches a public place): "I compared this fingerprint", "the old key will stop working", "the saved key will be removed", "the history starts over" (the op's own result is the same whether the box is ticked), the Arm boxes of the trigger and of Disconnect, and the typed name that deletes a remote (the op's delete is `Permanent` and audited; the screen's name is a guard against a slip, not against a client).
  - Tests through REST: `TestWideningARemoteOverRESTNeedsItsNameTypedInTheOp`, `TestAddressesThatWerePublicNeedTheNameWhateverTheRemoteIsNow` (an ssh address; delete and recreate; re-pointing), `TestWideningTheKindleDomainsOverRESTNeedsThemNamedInTheOp`, each without, with a wrong value and with the right one; clients that do not widen are unaffected.
- **H1, the result out of view: fixed.** The result of an answer (the refusal, the saved line, the plan, the preview, a test, the secret) carries `data-result` and `tabindex="-1"`, and `git.js` and `publish.js` focus the first one on load. The plan and the preview are drawn above their forms, and the once-only secret is the first block after the saved line. `e2e-publish.mjs` checks at 390 px that each is in the first screen and has the focus.
- **M1, reload and Back: fixed with post, redirect, get.**
  - Every form of the Git, publish, release, admin mail and admin Pandoc pages is answered with a redirect, so the address in the browser can be reloaded. A refusal, with what was typed (never a token, a password or anything with a credential in it), a host key scan, a connection test, a trigger test and the trigger's secret are kept by the server against the session for two minutes and shown once by the page after the redirect (`flash.go`).
  - The trigger's secret is never in an address: reloading the page after it shows "the secret was already shown" and makes no new secret (the rotation is the POST, which is not repeated). The Kindle preview is unchanged: it is a POST answer carrying a single-use token (a reload makes a new token and sends nothing); the mail fork's page keeps it so.
  - A reviewed plan has an address of its own, `?plan=pln_...`, holding only an id; what it was made from is kept for the session for half an hour (`planstore.go`) and the page asks the op for a fresh plan and token each time it is shown, so a reload makes nothing and an expired plan says so. The release preview is a GET page (`?preview=1&edition=&tag=`; it only reads).
  - A refusal of the dialog's force push still answers with the dialog's own fragment, which has no address of its own.
- **H2, the release preview: fixed.** "Size" says it is known once built and the cap is labelled as a cap; the preview says that anyone who can read the repository at the host can download the file, and that if it is public that is everyone.
- **M2, page size: fixed by changing the approach.** One select, "What to publish", lists the formats and each print PDF page size by name; there is no separate field to hide.
- **M3, boxes for the other audience: fixed.** Each box sits under its own choice with "for this choice only"; a box ticked for the other audience is refused in words, not dropped.
- **M4, a link preselecting public: fixed.** The form page reads only `account_id`, `mode`, `owner`, `name` and `q` from the address (test).
- **L1 (200% text on a phone), L2 (sender address clipped), L3 (the intro said mail was off), L4 (two ellipses), L5 (list links under 44 px), L6 (no disclosure marker), L7 (spacing, the memory field's fill, the facts grid), L8 (the trigger word and the page sizes under xx-rtl), L9 (the POST address after a refusal), L10 (the offline sentence): fixed.** The offline page now says that what was typed into a form on another page is not kept.
- **Stale docs: fixed** (FEATURES sections AS, AV, AW and F-860, F-912, F-934; OPERATIONS; the generated API line for `git.remote.create`; this note).
- **Not done:** the radio gaps in "When to push" were not measured again; the mail fork's Kindle preview and the settings Kindle form still answer on their own addresses by design (see above).

Ops whose schema changed: `git.remote.update` (`confirm_name`), `git.remote.create` (`confirm_name`), `mail.config.set` (`confirm_domains`). `make skill` was run; the snapshots are committed.
