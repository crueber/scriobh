# Phase 7: presence (F-244)

Backend and ops only; the screens come later. Section letter AZ in FEATURES, ids F-244 and F-970 to F-975, no migration, i18n prefix `presence.`.

## What it is, and what it is not

Who has the project open, and which chapter. A page sends a small heartbeat; the server keeps a table of "this person, this project, this document id, last beat" in memory and tells each person who else is here.

It is **not** co-editing, and the non-goals row says so. There are no cursors, no selection positions, no typing indicators, no text and no document content, anywhere in the code, the wire or the table. The beat carries a project id and optionally one document id, and the answer carries display names and (for some roles) a document id and title. Nothing a client types is ever stored or shown: the document id is checked and a title is read by the server when the list is made.

## The rules, and where each is enforced

| Rule | How | Test |
| --- | --- | --- |
| Memory only. No database write, nothing in a backup, export, mirror, event or Git | `internal/domain/presence` holds a map and a list; the one file that reads the database only reads | `TestBeatsWriteNothing` compares sessions, users, projects, events and audit rows before and after 800 beats, lists and leaves |
| Expires 60 s after the last beat; a refresh moves it | `TTL`; entries are swept in order of last beat | `TestEntriesExpireSixtySecondsAfterTheLastBeat` (injected clock, 59 s and 61 s); `TestEntriesStopBeingShown` |
| Expires on leave, sign-out, lock, disable, removal, switch off | `presence.leave`; a page's sign-out calls `ForgetSession`; **every list asks the database and the gate again**: is the person still a member, not disabled, switched on, with a session that is not revoked, expired or idle, and (for a protected project) does the gate still let that session in | `TestEntriesStopBeingShown`, `TestLockedProjectShowsNothingAndRefusesBeats` |
| Bounded: 64 entries a project, 20,000 in all, oldest evicted | `MaxPerProject`, `MaxTotal`, a list ordered by last beat | `TestPerProjectCapEvictsTheOldest`, `TestTotalCapEvictsTheOldestOverall` |
| Per-viewer filter | one function, `others`, builds the list for one viewer; the policy is `seesNames`, `seesChapters`, `nameVisible` | `TestWhatEachRoleIsToldAboutTheOthers`, `TestCanaryNoChapterTheViewerCannotReadEverAppears` |
| Not for guests, anonymous readers, non-members | a share link has no session and no project route; the ops need a member's browser session; the **membership row itself** is read, not the role function (an instance administrator who is not a member is "owner" there) | `TestNonMembersAreNeitherShownNorTold` |
| Never for a locked project; a locked session's beats are refused | the registry asks the gate for every op that names a project (these are not exempt); the page layer asks it too | `TestLockedProjectShowsNothingAndRefusesBeats`, `TestLockedEditorGetsNoPresenceList`, and the gate and authz sweeps, which walk the new ops with no change |
| Never for a trashed project | beat and leave are writes, so the registry refuses them (409); the list answers `reason: trashed`; entries of a trashed project are dropped | `TestTrashedProjectShowsNothing` |
| A key never sees or sends it | `ForSessions`, plus a non-empty `SessionID` in the service (the operator's shell passes the session-only check and has none); not an MCP tool (`presence` is an excluded group) | `TestKeysNeverSendOrSeePresence`, the key sweep |
| A beat is not activity | see below | `TestPresenceBeatsAreNotActivity` |
| Never stored, never replayed | the three ops are `Op.Ephemeral`: an `Idempotency-Key` on them is ignored, nothing goes to `idempotency_keys`, a repeat is answered afresh (a stored answer would put names and chapter titles in the database and replay them after a role or the switch changed) | `TestPresenceOverHTTPWritesNothingEvenWithAnIdempotencyKey`, `TestReviewBeatWithAnIdempotencyKeyIsNeitherStoredNorReplayed`, `TestBeatsWriteNothing` (now counts `idempotency_keys`) |

## The decision: sharing is symmetric

`presence.share` is a per-person preference (an ordinary `user_prefs` row, set through `prefs.set`), **off for a new account**. When it is off the person neither sends nor sees: their beat is answered `enabled: false, reason: off` and nothing is recorded; they are not in anyone's list; their list is empty; their stream carries no presence frame.

Why symmetric: a setting called "share" that let a person look without being looked at would be a way to watch a colleague, which is the one thing it must not be. And a solo writer who never opens the setting has nothing broadcast and the page makes no extra request. Turning it off through `prefs.set` removes the person from every list at once (`Forget`); the lists also check the stored value each time, so the two cannot drift.

**A key cannot switch it on or off** (`prefs.set` refuses `presence.share` for a key, 403): whether a person is shown to others is theirs to decide at a browser, and an unpinned key could otherwise start broadcasting which projects they have open (`TestAKeyCannotSwitchPresenceOn`; other preferences are unchanged for a key).

The key is spelled `presence.share`, as the brief gives it (the other preferences use underscores; a dot is a legal key and the form field name is plain).

One entry is kept **per person per project**, not per tab: if a person has two windows open on different chapters, the last beat wins and the chapter shown alternates between them with each beat (and each change wakes the other people's streams). A page that is hidden should not beat.

## What each role is told

The rule is the least that never shows a name the viewer could not already see in the application. `project.member.list` is the owner's; `comment.people` gives owner, editors and reviewers the names of owner, editors and reviewers, "not the readers". Presence follows both.

| Viewer | Names | Documents |
| --- | --- | --- |
| owner | every member who shares | yes |
| editor | owner, editors, reviewers who share; **a reader is not listed** | yes |
| reviewer | owner, editors, reviewers who share; a reader is not listed | **no**: "who is here, not where" |
| reader | **no one** (`sees_people: false`): a reader sees no member names anywhere else | no |
| instance administrator who is not a member, a stranger | nothing (`not_member`, or the 404 a missing project gives) | no |

A reader who shares is still shown to the owner, and to no one else. A reader therefore gets less than a reviewer; that is a consequence of the rule and is easy to change in one place (`nameVisible`). A name is the display name only, never an email address. A document is shown only if it belongs to this project, is not in the trash, and the viewer is an owner or an editor (there is no per-document restriction in this codebase today: `doc.get` is reader-level for every document of a project it can see, so "cannot read a chapter" means another project's, a trashed one, or a role that is not told; the canary covers all three).

## The ops

| Op | Route | Who | Notes |
| --- | --- | --- | --- |
| `presence.beat` | `POST /api/v1/projects/{id}/presence` | any member, browser session | body `{"document_id": "doc_..."}` optional; answers the list for the caller; not a dry run |
| `presence.list` | `GET /api/v1/projects/{id}/presence` | same | the same answer without saying you are here |
| `presence.leave` | `POST /api/v1/projects/{id}/presence/leave` | same | a POST so a page can send it on unload (`sendBeacon`) |

The preference goes through the existing `prefs.set`. The answer:

```json
{"enabled": true, "sees_people": true, "sees_documents": true,
 "items": [{"user_id": "usr_...", "name": "Edgar", "document": {"id": "doc_...", "title": "Chapter one"}}],
 "heartbeat_seconds": 20, "expires_seconds": 60}
```

`enabled: false` carries `reason`: `off`, `not_member` or `trashed`, and an empty list. A document id that is malformed is a 422; one that is well formed but not a live document of the project is accepted and recorded as "no document", and nothing says why, so a probing client learns nothing.

### A beat is not activity

A page that beats every 20 seconds would otherwise keep a session alive past its idle limit and keep a protected project unlocked past its own. Three places had to be told:

1. the session middleware reads the session with `Sessions.Peek` (no refresh of `last_seen_at` or the idle time) for these routes;
2. the page layer's gate check (`pageGate`) asks passively for these routes, as it does for a stream;
3. the registry asks the gate passively for an op declared `Auth.AsPassive()` (a new flag in `internal/ops/auth.go`).

`TestPresenceBeatsAreNotActivity` has a control (an ordinary request does refresh the session, so the check proves something) and drives the gate with an injected clock: unlocked with a one-minute limit, beats at 20 and 40 seconds, then a beat at 70 seconds is refused as locked. The user's rate-limit bucket still counts the request (three a minute from one open page).

The passive treatment is for a **session**. A key is refused on these routes anyway, and its refusal on a protected project is counted exactly as on any other route (`project_gate_key_use.refused`): the gate is asked passively only when the caller is not a key (`TestReviewKeyRefusedOnPresenceIsCountedLikeAnyOtherRefusal`).

## The stream

`GET /api/v1/events?project=…` carries `event: presence` for a person with a browser session and `presence.share` on. The frame is the same JSON as `presence.list`, built for that person in the stream's goroutine; it has **no `id:` line** (an id would move the client's resume cursor), and it does not pass through the hub or the events table, so it is never replayed.

- **A frame is authorized like an event.** Before a frame is written the stream asks the registry whether this session may still read the project (the gate included, passively), and a refusal ends the stream with `bye` and writes no name or title first (`TestStreamPresenceEndsWhenTheUnlockIdlesOut`, with the gate's clock). A project that locks wakes the stream's feed too (the existing `relock` signal), so it is noticed within 5 seconds.
- **Per-recipient filtering.** The hub broadcasts events per project and the stream filters each event per recipient (role, review, export, bible). Presence does not use it: each stream calls `ListFor` with its own principal, so the filter above is the only path. A canary test reads the raw frames of a reviewer's stream.
- **Coalescing.** The service signals a project's watchers (a buffer of one, never blocking) when a person arrives, leaves or moves to another document; a plain refresh signals no one. A stream looks at most every 5 seconds, builds a list only when signalled, when the service's version changed, or every 30 seconds (which is how an expiry, a sign-out or a lock is noticed), and sends only when the bytes differ from the last frame.
- **A person who does not share is sent nothing**, and told once if they stop. A key's stream gets none. A Compendium stream gets none.
- **A slow client never blocks the hub or the table.** The service never writes to a client. A stalled stream is its own goroutine blocked on its own write (10 seconds, then dropped, as before); the hub drops a subscriber whose buffer fills, as before. `TestAStalledStreamDoesNotSlowPresence` holds a connection that never reads and checks that 300 beats stay fast and another stream still gets both project events and presence. It does not fill the socket far enough to block the server's own write: that path is the existing one and is not retested. `TestAWatcherNobodyReadsNeverBlocksTheTable` makes 10,000 changes beside a watcher no one reads.

## Load

The numbers below are for projects **without a passphrase**. A protected project asks the gate for each person shown; that answer is kept per session and project for **5 seconds** (`GateTTL`) and dropped at once when the project relocks (the gate calls `SetOnRelock`: `project.lock`, a new passphrase, the passphrase removed), so a lock or a changed passphrase is seen immediately; a person who unlocks, or an unlock that idles out, is noticed within 5 seconds. Measured through the registry with 64 people shown in a protected project (an unlock that idles out is noticed after the 5 seconds, tested with separate clocks in `TestTheGateAnswerIsKeptForFiveSecondsOnly`): **221 µs a beat with the cache, 1.38 ms without it** (the review measured 2.4 ms); `TestGatedProjectBeatCost` states a 1.5 ms budget and also checks that a lock is seen at once. Membership, the person's switch, the account and the session are still asked of the database on every list (they are not cached).

Budget: 1,000 projects with 10 viewers each, every viewer beating every 20 seconds, is 500 beats a second. Through the service (a heartbeat and the list that answers it) a beat must average under **600 microseconds** (30 percent of one core at that rate); through the registry as a client makes it (the registry adds the caller's role, the gate and the trash, three more reads) under **1 ms** (50 percent of one core), 99th percentile under 8 ms at the service, and the table of 10,000 entries under 8 MB of heap. `TestLoadThousandProjectsTenViewersEach` builds that database (10,000 people, sessions, memberships, documents) and fails at five times the averages.

Measured on a 16-core Mac **while another full gate or test run was going on** (so these are not best cases, and they moved by a third between runs):

| Path | Average | p99 | One core at 500 a second |
| --- | --- | --- | --- |
| the service, one at a time | 373 to 513 µs (two runs) | 1.2 to 3.4 ms | 19 to 26 percent |
| the registry (`presence.beat`), one at a time | 541 µs | 4.9 ms | 27 percent |
| the registry, eight goroutines at once | 263 µs of wall time a beat | 17 ms for one beat's latency | 13 percent of the machine's wall time |

The table of 10,000 entries holds 2.1 MB of heap. Each service beat is four reads of the database (the caller and the document in one, the project's members, the entries' sessions, the documents' titles), so the cost is the database's, not the table's; the table itself (touch, watchers, eviction) is microseconds. If a deployment ever needs more, the next step is a short cache of the validated member list per project; it was not built because the budget is met and a cache would make a sign-out or a lock visible later. The stream adds one list build per stream when something changed or every 30 seconds, and an instance allows 200 streams by default.

Race: `go test -race` over the package and over the server's presence tests. Restore: presence is memory, so a restore has nothing to pause; the first beat after a restart rebuilds the table.

## What a screen needs

**Routes** (all session-only; the page calls them with `fetch`, `credentials: same-origin`, `Content-Type: application/json`):

- `POST /api/v1/projects/{id}/presence` every 20 seconds while the page is open and visible, with `{"document_id": ...}` when a document is open. Send one at once on load. Stop when `document.hidden` (and send one when it becomes visible again). There is no CSRF token to send: these are `/api/v1` calls, which the server protects by checking the browser's fetch metadata (`Sec-Fetch-Site`, `Origin`) on every mutation, not with the `X-CSRF-Token` the `/ui` form routes use. A same-origin `fetch` and `sendBeacon` pass it.
- `POST /api/v1/projects/{id}/presence/leave` from `pagehide` via `navigator.sendBeacon`.
- `GET /api/v1/projects/{id}/presence` for a first paint that does not announce the person.
- The same JSON arrives as `event: presence` on the project's existing stream (`live.js`); render it, do not poll. A stream that is not delivering (offline) leaves the last list on screen: say so (`presence.offline`) after two missed beats.
- The setting: `PUT /api/v1/me/prefs` with `{"items": {"presence.share": "true"}}`; it appears in `GET /api/v1/me/prefs`.

**Fragments:** none. Render the list from the JSON with `textContent` (names are the one thing here a person typed). A person's name is empty only if they never set one: show `presence.someone`.

**Heartbeat protocol:** beat, read the answer, render `items`. `enabled: false` means stop beating (the setting is off, the project is trashed, or you are not on it); show `presence.off` with a link to Settings for `off`. `403` with `data.reason: project_locked` means the project is locked: stop, show nothing (the unlock page takes over). `404` means access ended. Any other failure: keep the last list, try again at the next interval. `sees_people: false` (a reader) means show `presence.reader` or nothing; `sees_documents: false` (a reviewer) means show names without chapters (`presence.reviewer_note`).

**Offline:** nothing is queued or retried faster; a beat that fails is dropped. The list on screen is not cleared by a failed beat; after 60 seconds without an answer, grey it and say it is not updating. The page must render without any of this (the writing surface never waits on it, never moves because of it, and a list update must not take focus or reflow the text).

**Words to translate** (`en.json`, prefix `presence.`): `here`, `setting`, `setting_hint`, `none`, `count_one`, `count_many`, `in`, `untitled`, `someone`, `off`, `reader`, `reviewer_note`, `offline`, `locked`.

## Not built, and what was not verified

- No screen: no setting row, no list in the header or the outline, no script. The routes, answers and words are above.
- No typing indicator, cursor, selection, document content or "last seen" by design.
- No expiry hook for **project.lock**, a session revoke or an administrator's disable: they are noticed by the next list (a beat, a list, or a stream's check: within 5 seconds for a lock, up to 30 for the others), not pushed. Sign-out through the page, leaving, switching off and trashing are immediate.
- The list is built from the database for each beat. The only thing kept is the gate's answer for one session on one project, for 5 seconds (see Load); membership, the switch, the account and the session are asked every time. The answers are dropped by age as the table grows and by project when it relocks.
- Not driven: a real browser's `sendBeacon` on unload, a page left open for hours, an instance with 20,000 simultaneous sharers (the cap is tested with a synthetic table, not with load), and the stream's own socket write blocking.
