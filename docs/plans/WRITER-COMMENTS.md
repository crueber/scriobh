# Writer comments: comment on your own writing from the inspector, for yourself first

Status: phases 0 and 1 built, and phase 2's marks (F-1286, `cmarks.js`); section 14.2 says what is not built of phase 2 (section 14 says where main differed from this text); phases 3 and 4 are not built. Lives at `docs/plans/WRITER-COMMENTS.md`.
The compendium sidebar's first build (`COMPENDIUM-NAV.md`, build A) and `split-chapter` have merged; `S.alts.sync` exists on main.
Rows are numbered from F-1280. Nothing here is built, run or measured except where a number is marked "measured".

## 1. The request, and the rule it serves

The owner: "The writer would like to be able to make comments. So in the 'Comments' section of the info bar that they can add comments to their writing for themselves."

The standing goal: the writer never leaves the writing view for frequent work. Anything done regularly belongs in the inspector.

So the feature is: **in the writing view's Comments tab, a writer can start a comment on selected words or on the whole chapter, it is "Only me" unless they choose otherwise, they can see where it sits, and nothing about it reaches anyone else.** Typing never stops, nothing in the manuscript changes, focus goes back to the caret.

## 2. What exists today (each point checked in the tree at 81f5b6a9)

| Thing | What is there | Where |
| --- | --- | --- |
| Comment ops (F-135, F-136) | `comment.create` (by quote, by offsets at a revision, or `plain: true` for a reader's selection), `list`, `get`, `update` (author, 15 minutes), `delete` (author or owner, tombstone), `resolve`, `reopen`, `reanchor` (re-attach, move, detach), `people`. All need the reviewer role. Threads are one level deep. Kinds: comment, question, suggestion-note. Whole-document and project comments exist (`document_id` empty means the project). | `internal/domain/review/comments.go`, `core.go` |
| Table | `comments` has **no visibility or audience column**. Every comment is visible to every member who can call `comment.list` (reviewer and up). The migration says comments never reach an export, the mirror, Git, search, a word count or an AI context; There is no reader of `comments` in `export`, `mirror`, `gitmirror`, `search`, `semantic` or `ai`. | `internal/db/migrations/0430_comments.sql`, `0470_review_hardening.sql` |
| Anchors | Range of the Markdown body in UTF-16 units plus quote, original quote, 24 characters of context each side. `comanchor.AfterWrite` re-anchors in the save transaction; words that are gone make the thread `orphaned`; it is never moved onto a lookalike. A range must sit inside one block, and may not touch a private note, ghosted text or an HTML comment. Cap: 1,000 open anchored threads per document. | `internal/domain/comanchor/comanchor.go` |
| F-700 reading view | Comment margin (sheet at 390 px), `C` to comment, `plain: true`. | `web/static/js/review.js` (reading half) |
| F-703 review page | `/p/{slug}/review` through `review.queue` and `review.resolve_all` (preview, `expect_count`). | `internal/server/reviewui.go`, `internal/domain/review/queue.go` |
| F-704 Comments tab | Lists the chapter's threads (`comment.list?document_id&plain=true`), filters, reply, resolve, delete, a link to the reading view. **Cannot start a comment.** Only drawn for `CanEdit`. Its note says "not in the text as you write". | `web/static/js/review.js` (panel half, lines 580-600), `web/templates/write.html:56,87` |
| F-139/F-243 share links | Guests see only threads with their own `share_id`; replies to those threads are included. | `internal/domain/share/public.go:255-300` |
| F-032 dashboard count | **Not found.** No template or Go code shows an unresolved-comment count on a project home; the Review menu item has no number, and there is no "what's next" card either. The only counts are `review.queue`'s per-chapter counts (review page) and the reading view's `loadCounts` over `comment.list`. No filter is planned for code that is not there; section 5 says how a future count must be made. | grep of `internal/`, `web/templates/` |
| F-248 notifications | One row per mention, reply, owner "comment", resolved, etc.; the list joins `comments`. No email or push. | `internal/domain/review/notify.go`, `core.go:208-240` |
| Webhooks | `comment.created` event; with `include_text` the body is read from `comments` when sent. | `internal/webhook/service.go:869,906` |
| Live events | `comment.*` events go to every session that passes `comment.list`; the stream does no per-comment check. | `internal/server/sse.go:165,282` |
| Private notes | `%%text%%`, an inline mark (`private_note`) **inside the stored text**. See section 3. | `web/editor/src/schema.js`, `rules.js:107,143` |

### 2.1 Three points where the code differs from what was assumed

1. **`arm.js` is not a Comments-tab file.** It is the double-tap-proof confirmation helper (1,511 B of 1,792 gzip). The tab's code is `review.js` (shared with the reading view: 13,365 B of 14,336, so 971 B left) and its sheet is `review.css`. New code goes in new lazy files (section 8).
2. **The "single decoration slot" reason in F-704 is stale.** `scriobh.decorate` (`web/static/js/shell.js:97-117`) is a registry: `alts`, `lab`, `mentions`, `wiki`, `ai` and `focus` all register in it today. The decision in OPEN-QUESTIONS ("Comment marks stay out of the editor", line 1360) was about typing cost and stale answers, not about room. Both are answerable with the Lab's route (section 7). The first build step amends that note and the F-704 row.
3. **Mention marks and Lab marks are drawn by two different routes.** `mentions.js` asks `mention.list?spans=true` after a save, finds each name by text in a flattened copy of the prose, moves marks through edits by diffing the old and new document, and draws nothing if any place cannot be matched. `lab.js` takes exact offsets from the server, turns them into editor positions with `ed.alts.rangesOf(bodyOf(W.serverMd), …)` and a `toLive` correction, keeps them in one `DecorationSet`, carries that through every edit with `ed.alts.mapSet`, and draws only when the editor holds exactly what is stored (`view.state.doc` unchanged, `W.rev` unchanged, nothing unsaved). Comment anchors are the Lab's shape, so comments use the Lab's route.

## 3. Private notes versus comments

### 3.1 What a private note is

`%%like this%%`, an inline mark in the Markdown. It is part of `documents.content`. So it is in revisions and versions, in the Git mirror and a private remote, in the Markdown export (kept byte for byte), in backups, and in the writing view of every editor of the chapter. It is cut from word counts, compile, share links, AI requests, search and a public repository (`text.CleanMarkdown`). It is added by typing `%%words%%` (input rule, `rules.js:107`) or in Source view. The command `private` exists in `commandTable` (`rules.js:268`) but has **no toolbar button, no key and no palette entry**, so most writers will never find it.

### 3.2 Side by side

| | Private note | Comment (this feature) |
| --- | --- | --- |
| Lives | In the manuscript text, in the flow of the paragraph | In its own table, beside the text |
| Needs an edit of the chapter | Yes (it is text; it takes a revision) | No (the only write to the chapter is the flush of what is already typed) |
| Anchored to words | It is at its place, not on words | Yes, on a stretch of one paragraph, or on the chapter |
| Survives edits | Moves with the text by construction | Follows by `AfterWrite`, goes `orphaned` when the words go |
| Thread, reply, resolve, filter | No | Yes |
| Who sees it | Everyone who can open the chapter in the writing view | "Only me" (new), or the people on the project |
| In revisions, mirror, Git, Markdown export | Yes | Never |
| Counted as a to-do | No | Open or resolved |
| Reflows the page | It lengthens the paragraph while it shows | Nothing is drawn in the flow |

Other places a writer keeps notes today, so nothing is missed: Details tab "Notes on this chapter" (`synopsis_long`, shared document metadata, not private, not anchored) and the per-person scratchpad (F-098, private, one pad per person and project, not anchored).

### 3.3 Is the request partly met?

Partly. A private note lets a writer write to themselves inside the text. It does not give them: a place in the Comments tab, a note on chosen words that is kept out of the manuscript and its history, a list of open notes to work through, resolve and reopen, or a note on the chapter as a whole. The owner asked for the Comments section of the info bar, and that is what is missing. The two do different jobs, so both stay.

### 3.4 Recommendation: coexist, with plain names

- **Keep "private note" for the `%%` construct only.** Do not use "private" for comments, so no one has to ask "is a private comment a private note?".
- **Comments have two states, shown in words: "Only you" and "Shared".** The switch is labelled "Who can see this" with "Only me" and "People on this project".
- One sentence under the composer settles it: "Stays beside your text, out of the manuscript and its history. For a note inside the text itself, use a private note (%%like this%%)."
- **Not built, said in the plan so no one expects it:** converting a comment into a private note, or the reverse. That would edit the manuscript from the sidebar and needs an explicit accept; it is its own row if the owner wants it.
- **Adjacent gap, offered as a small separate row (F-1291):** give the `private` command a palette entry ("Make the selection a private note") so private notes can be found. It is not part of this build and not needed for it.

## 4. Visibility: the model

### 4.1 Decision

One column, two values, set when the comment is written, one-way to shared.

```sql
-- 0480_comment_visibility.sql (range 0400, review; 0470 is the last file there)
ALTER TABLE comments ADD COLUMN visibility TEXT NOT NULL DEFAULT 'shared' CHECK (visibility IN ('shared','private'));
ALTER TABLE comments ADD COLUMN shared_at TEXT;
UPDATE comments SET shared_at = created_at;               -- every comment written so far was visible to its readers
CREATE INDEX comments_private ON comments(project_id, author_id, document_id) WHERE visibility = 'private';
-- a reply has its thread's visibility (defence in depth; the op already enforces it)
CREATE TRIGGER comments_reply_visibility BEFORE INSERT ON comments WHEN NEW.parent_id IS NOT NULL
  BEGIN SELECT RAISE(ABORT, 'a reply takes the visibility of its thread')
  WHERE NEW.visibility <> (SELECT visibility FROM comments WHERE id = NEW.parent_id); END;
```

Precedent for an `ADD COLUMN ... NOT NULL DEFAULT ... CHECK` is `0470` (`share_links.whole`). The runner runs unapplied files once, in filename order, with a pre-flight backup (`internal/db/migrate.go`); an upgraded database picks `0480` up like `0470`. Update the `0400` row of the migration table in `docs/DATA-MODEL.md` and the `comments` description there.

- "Only me" means **the author alone**. Not the owner, not other editors, not an instance administrator who is not the author, not a reviewer. Visibility is `visibility = 'shared' OR author_id = <viewer>`; nothing else grants access.
- Replies carry the row's visibility too, so a single-row read needs no join. A reply to a private thread is by its author and private. A reply to a shared thread is shared (a `visibility` value that disagrees with the thread is a 422 naming the rule).
- **The op default stays `shared`.** Existing clients (the CLI, the reading view, `e2e-review`, guests, Word imports in `imported.go`) are unchanged. Only the writing view's composer sends `private`, and it sends it explicitly.
- A person who is not the author gets **404** for a private thread, whatever op, so existence is not told (invariant: authz returns 404 for missing access). The owner's right to delete any comment does not reach a thread they cannot see.

### 4.2 What "private" means for the write paths

| Op | Rule |
| --- | --- |
| `comment.create` | New input `visibility` (`shared` default, `private`). A private comment: **no mentions** (a `mentions` list is a 422; `@name` in the body stays plain text and is not resolved); **no notification** to anyone; **no event** (`comment.created` is not emitted, so no live-stream line and no webhook); no `comment_mentions` rows. The parent lookup (`core.go:122`) treats a private thread that is not the actor's as `NotFound`. |
| `comment.update` | Author only, as now. For a private comment there is **no 15-minute limit** (nobody else has read it); for a shared one the window runs from `shared_at`. No mention work for private. `visibility` is **not** accepted here. |
| `comment.share` (new) | Author only, thread roots only, `Auth: reviewer`; anyone else gets 404. Sets `visibility = 'shared'` and `shared_at` on the root and its replies, resolves `@name` and picks in the body, then does exactly what a new shared comment does: notifications (mentioned people, the owner), the `comment.created` event once. Calling it on a shared thread changes nothing and sends nothing. |
| Making a shared comment private again | **Not offered.** Notifications, the live stream and webhooks have already told people, and replies by others may exist; hiding it would pretend they did not. A mistaken share is undone by `comment.delete` (tombstone). The UI says this next to the switch. |
| `comment.resolve`, `reopen`, `reanchor`, `delete` | Same checks as now, after the 404 for a thread the caller cannot see. A private thread emits no event and no notification on any of them. |
| Caps | Shared comments count against the existing project cap (20,000), the per-document 1,000 open anchored cap and the 60-per-hour limit, **counted over shared rows only**, so one person's notes can never block someone else or reveal how many exist. Private rows have their own limits: 5,000 live per author per project, 100 open anchored per author per document, and no hourly limit (a private note harms no one). |
| `AfterWrite` | Re-anchors every row of the document, private included (it is maintenance, not a read). Its `maxProcessed` guard counts shared and private separately (2,000 shared, 1,000 private) so private volume can never orphan shared threads; the cut-off is unreachable without ten authors at their private cap. |
| Lifecycle | Accounts are soft-deleted (`users.deleted_at`; `commentCols` reads it as `AuthorGone`), so `author_id` stays set. `review.Sweep` deletes private rows whose author has `users.deleted_at` set, and any with a NULL `author_id`. A member who is removed from the project keeps their private rows, unreachable to everyone else, and gets them back if re-added; this is stated in the op text. The project purge removes them (cascade). |

### 4.3 Who counts as the same person: agent keys

An agent key acts as the person who owns it, so a key (CLI, MCP) sees its owner's "Only me" comments and nobody else's. The recommendation is to leave it that way and say so in the `comment.list` description and the skill text: the person's agent is the person's tool. This is a question for the owner (section 13); it changes nothing in the build either way except one filter.

## 5. Every read path honours it

### 5.1 One rule, one helper, one scan

All of `review` reads through two functions, `fetch` and `threadFilter`, plus six raw SQL strings. The plan:

1. A single helper `visible(alias, viewerID) (sql string, args []any)` returning `(<alias>.visibility = 'shared' OR <alias>.author_id = ?)`.
2. `fetch` takes the viewer and appends the helper, so every caller that scans rows (`loadOne`, `thread`, `repliesOf`, the `after` lookup in `queue`, the `ThreadIDs` branch of `resolveAll`) is covered by a compile error until it passes one.
3. `threadFilter` includes it, so `total`, `open` and `resolved` in `comment.list` and every filter (`q`, `author`, `mentions_me`, `kind`, `state`) are counted over what the caller can see only. A search term cannot confirm a hidden body.
4. The raw SQL gets the clause by hand: `queue.go:237` (per-chapter counts), `notify.go:94-95` (the notification list's join; defence in depth, since a private comment creates no notification), `webhook/service.go:906` (`withCommentText`), `share/public.go:255` and `:293` (guest threads and their replies, `AND c.visibility = 'shared'`).
5. **A source scan test** (precedent: `internal/gitmirror/secrets_canary_test.go`, `internal/repocheck`) walks the non-test Go files for `FROM comments` and `JOIN comments` and fails on any statement that does not use `visible(` or `visibility`, except a short named list with a reason each: `comanchor.AfterWrite` and its helpers (maintenance), `docs.go:1175` (orphaning on split), `notify.go:282-283` (the sweep). The limit counts in `core.go` and `imported.go` and the target-document cap in `reanchor` (`comments.go:848`) are **not** exempt: they must count by audience as 4.2 says (shared rows for shared threads, the author's own rows for private ones), so a person's notes cannot block another person or reveal how many exist. A new reader of `comments` cannot be added without choosing.

### 5.2 Path by path, each with a second-user test

Fixture: owner Ada, editor Ben, reviewer Cy, reader Di, a stranger Eve with no membership, and a guest of a comment-scope share link. Both directions are tested: Ada's private comment against Ben, and **Ben's private comment against Ada** (the owner must not see it).

| Path | Expected | Test |
| --- | --- | --- |
| `comment.list` | Ben sees shared threads and his own only. `total/open/resolved` ignore Ada's private rows. `q=CANARY` and `author=<Ada>` return nothing. Same through the CLI and the MCP tool. | `TestPrivateList` in `internal/domain/review`; MCP and CLI cases in `internal/server` with Ben's agent key; Ada's own key does see hers |
| `comment.get/update/delete/resolve/reopen/reanchor` by id | Ben gets 404, **byte-identical** to the answer for an id that does not exist. Ada as owner cannot delete Ben's private thread. | `TestPrivateByIdIsNotFound` compares response bodies |
| `comment.create` with `parent_id` of another's private thread | 404 (not 403, not 409) | same file |
| Replies | A shared reply to a private thread is a 422; a private reply to a shared thread is a 422 | same |
| Reading-view margin (F-700) | The margin reads `comment.list`, so Ben's margin lacks Ada's. Ada's own margin shows hers with the label "Only you". Its "Next unresolved" calls `review.queue`, whose default is `shared`; for an owner or editor it passes `visibility=all` (the shared threads plus the caller's own) so it steps through what the margin shows. `all` is never the default of `review.queue` or `review.resolve_all`, and `resolve_all` never accepts it, so "Resolve all" cannot touch the writer's own notes. | e2e in `e2e-writer-comments.mjs` (two browser contexts) |
| Review page (F-703) | The page, its per-chapter counts and "Resolve all" use `review.queue` and `review.resolve_all` **with the default `visibility=shared`** (the same set as before, so existing behaviour is unchanged). Ada's preview count excludes Ben's private rows. Ben creating a private comment between preview and confirm **does not** trip `expect_count` (no 409, no signal). `thread_ids` that name another person's private thread are skipped exactly as a missing id is. `after=` another's private id is 404. A chip "Only me" lists one's own (phase 3). | `TestPrivateQueueAndResolveAll`; `TestReviewPageDoesNotShowOthersPrivate` (HTML contains neither canary nor count) |
| Writing view tab badge and count | The server-rendered open count on `#write` comes from `comment.list` (limit 1) as the viewer, so Ben's page lacks Ada's. | `TestWritePageCommentCount` |
| Dashboard unresolved count (F-032) | Not built. When it is, it counts `visibility=shared` only (collaborator feedback waiting) and goes through `comment.list`/`review.queue`, never raw SQL. | The source scan test already fails on a raw read; add a case then |
| Share links (F-139/F-243) | `share_id` filters already exclude private rows; add the clause anyway. Guest comment list, the manifest's per-document counts (`public.go:137`), `share.usage` counts and the rendered document are unchanged by Ada's private comment. A guest thread's replies never include a private row. | `TestPrivateNeverOnAShareLink` (extends `share_round2_test.go` setup) |
| Notifications and @mentions (F-248) | A private create sends none to anyone, including the owner and the author. `@Ben` in a private body does not notify Ben. `comment.share` notifies mentioned people and the owner exactly once; a second call sends none. `notification.list` never names a private comment of someone else. | `TestPrivateSendsNoNotification`, `TestShareNotifiesOnce` |
| Webhooks | With `include_text` on, a private create, reply, edit, resolve and delete put nothing in the outbox; `comment.share` puts exactly one `comment.created`. `withCommentText` refuses a private row. | `internal/server/webhooks_test.go` cases |
| Live stream (SSE) | Ben's stream gets no line for Ada's private actions; it gets one when she shares. | extend `sse_review_test.go` |
| Events and audit tables | No `comment.*` row for private actions. Generic audit rows, if the registry writes any, hold ids only. | assert on the tables |
| Search, embeddings | Comments are not indexed (no FTS trigger names `comments`); assert the canary is not in `search.query` results, `semantic.EmbedBody` or the index tables. | `TestPrivateCommentNeverLeaves` |
| Exports, compile, site, md bundle | Canary absent from the Markdown, HTML, DOCX, EPUB exports, the static site zip and `md_bundle`. | same |
| Git mirror and push | Canary absent from the mirror tree and from every push to the fake host (add `PRIVATE-COMMENT-CANARY` to `canaries` in `gitmirror/audience_test.go`). | same |
| AI | Canary absent from `ai.context.preview`'s request body. | same |
| Mail digest | Counts come from notification rows, so unchanged; assert Ben's digest count with and without Ada's private comment. | `review_mail_test.go` case |
| CLI and MCP | Generated from the registry, so they carry `--visibility` and the new op; `make skill` regenerates the skill, the CLI snapshot and the op table in `docs/API.md`. | `make skill-check` |

## 6. Starting a comment from the writing view

### 6.1 The surface (one mechanism with the compendium plan)

The compendium plan (`compendium-nav-plan.md`) builds **no floating selection menu**. Its mechanism is: a chord handled in `inspector.js` that opens the sidebar tab with a hand-off of the editor selection (`S.lookupWant`, after the existing `S.labWant`), a palette entry, and buttons inside the panel; Alt+Shift+C is its chord. This plan uses the same mechanism and does not add a menu. A floating menu would sit over the page being written, and on a phone it would fight the system's own selection handles. If the owner ever wants one, it is one component calling the two hand-offs below.

**One function, three entrances.** `inspector.js` gets `S.inspector.want(panel)`: it records the editor selection now (`{from, to, empty, doc: view.state.doc}`, taken at the moment of the key or tap, after `S.alts.sync()`), stores it under `S[panel + "Want"]`, opens the tab and lets the tab's lazy file read it in `show()`. The compendium panel and the comments panel both call it.

| Entrance | Comments | Compendium (their plan) |
| --- | --- | --- |
| Chord | **Alt+Shift+M** (M for margin). Free: taken are A N P O G S (alts), I J (ai), B (split), C (compendium). Claimed in `S.chords` as `comments: panel`; `e2e-ai-edit` fails on any clash. Matched by `e.code`, ignored while composing and on key repeat, never types a character. | Alt+Shift+C |
| Palette | "Comment on the selected words" (hint Alt Shift M), "Comment on this chapter", "Show the comments of this chapter" | their entries |
| Button | A "Comment" button on the format bar (`data-cmd="comment"`, `data-keys="Alt+Shift+M"`, new `comment` icon in the sprite, `data-wait` like Split). The format bar is where the existing selection actions already live (alternative, ghost, stash, writing partner, split), and it is the one entrance a phone has. In the tab itself: the composer. | their panel's buttons |

Meaning of the chord by context, one press, one meaning:
- Focus in the editor, **words selected**: open Comments, composer for those words, focus in the text box.
- Focus in the editor, **caret inside a drawn comment mark** (phase 2): open Comments on that thread's card, focus on the card.
- Focus in the editor, **neither**: open Comments with focus in the "Comment on this chapter" box.
- Focus in the panel: return to the editor (same as Escape); focus lands with `view.focus()` (to confirm at build that it does not scroll); the selection and caret are exactly as they were.

The format bar button and the palette entry go through `S.inspector.want("comments")`, so there is one code path to test. The compendium plan could do the same: its "Use the selected words" could get a format bar button on the same grounds; that is that plan's decision.

### 6.2 What the tab looks like

Top to bottom, in the existing `.rv-` styles, flat fills, hairline rules, one accent:
1. **"Comment on this chapter"**: a labelled text box (two rows) and an "Add" button. Visible when the tab opens, so the frequent no-selection case is one box and one key.
2. The **composer for selected words**, shown only after the chord or button with a selection: the quoted words, the text box, the visibility fieldset, "Add comment", "Cancel". It sits at the top of the list and replaces the chapter box while open.
3. The filters (Open, Mine, Mentions me, Resolved, and "Only me" when other people are on the project), the cards, the empty state.

Visibility fieldset: legend "Who can see this", radios "Only me" (checked) and "People on this project". It is shown only when `comment.people` lists someone besides the writer; on a solo project the line is "Only you can see this." (no control, no confusion). Next to the second radio: "Once shared it cannot be made private again. You can delete it." A card carries a text chip "Only you" (words, never colour alone). No `kind` select in the writing composer (comment by default); it stays in the reading view.

While a comment is "Only me" the @ picker is off and no `mentions` are sent (the op refuses them); a Reply on a private thread has no picker either and sends no `visibility` (a reply with none takes its thread's). On a shared thread, and on a comment set to "People on this project", the picker works as it does today. Each card gets the existing Reply, Resolve, Delete, and now **Edit** for `can_edit` (the control the op already supports and the UI never offered; `form()` is reused with the text filled in), **Show in the text** (section 7.3), and, for a lost anchor, **Attach to the selected words** and **Keep on the chapter** (section 7.4).

The Comments tab label shows the open count, drawn by the server as `Comments (3)` and kept current by `S.cwrite` (the count changes by text, in a live region message, never by moving things).

### 6.3 From selection to anchor, step by step

The words are named in the stored Markdown by the editor's own serializer, as the alternatives and the writing partner do, not by guessing in the page. Reused as written: `ed.alts.sourceOf(from, to, ctx)` (it puts private-use markers in a scratch copy, serializes, finds them once each, checks that taking them out gives the same text, and grows the context until quote plus context is unique), `S.alts.exclusive`, `S.alts.ensurePlaced`, `W.flushAll`, and the retry of `alts.js create` on "occurs N times".

At **open** (chord or button): `S.alts.sync()` (read the browser selection first; it matters on a phone), then capture. Check the selection is non-empty, inside **one textblock** (`$from.sameParent($to)`), not in a code block; otherwise say in words, as the reading view does: "Select inside one paragraph. A comment goes on a stretch of one paragraph, or on the chapter." Trim spaces off both ends of the range. The held words are shown as the quote in the composer and **held as a decoration** (`cmt-pending`, through `S.decorate`, carried through edits with `ed.alts.mapSet`) so they stay visible while focus is in the sidebar. Nothing is dispatched to the editor.

While the comment is **typed**, the editor is not held. The writer may click back and keep typing; the pending range follows their edits. If the words are deleted, submit says so ("Those words are gone from the chapter. Select them again.") and keeps the typed comment.

At **submit** (Add, or Ctrl or Command with Enter):
1. `A.exclusive(...)` holds the editor for the few hundred milliseconds that follow (it queues behind any alternatives op and waits for an input-method composition to end).
2. `W.flushAll()`. If it fails: stop, keep the composer and its text, say "Your text is not saved yet, so the comment was not added. Try again in a moment." Unsaved typing is therefore saved first, never lost and never left unanchored.
3. `A.ensurePlaced()`. If the editor's spelling differs from the stored text, `flushAll(true)` stores the editor's and checks again; if it still does not match: "That place could not be matched exactly with the saved text. Reload the chapter and try again."
4. Read the pending range from the decoration set (current positions). If the document changed since step 1 in a way that removed it, stop as above.
5. `src = ed.alts.sourceOf(from, to)`. If null: "Those words could not be named exactly. Select a little more or less of them." There is **no balanced-formatting rule** for a comment: the server checks the range is non-empty, trimmed, at character edges (no cut through an emoji or combining sequence), inside one block, clear of ghost and alternatives markers and of hidden text (`alts.CheckPlacement` with `ScopeSpan`: "any range inside one block"), and `plain` hints are built for a range that starts or ends inside formatting (F-705). So a run that crosses the end of an emphasis is accepted and its quote may hold the closing asterisks; the card shows `quote_plain`. (The alternatives' client-side balance check is not copied.)
6. `POST comment.create {document_id, quote, before, after, body, visibility}`. **Quote with context, not offsets.** Offsets need `base_revision` equal to the current revision and fail (409) if a collaborator saves between the flush and the post; a quote is found at any revision. `sourceOf` has already made quote plus context unique in the editor's text. On "occurs N times" retry once with 160 characters of context (the `alts.js create` rule). The server's own refusals (one block, private note, ghost, HTML comment) pass through in its words.
7. On success: draw the new mark from the pending range (no round trip), add the card, announce "Comment added." in the live region (`#rv-live`), clear the pending decoration, release the hold, and **return focus to the editor** with `view.focus()`.
8. On any failure the composer, its text and the pending highlight stay.

**Selection and caret after.** The editor selection is not touched at any point (no dispatch), so after focus returns the same words are still selected and the caret is where it was. The next key replaces the selected words, as it would have anyway. Collapsing the selection to the end was rejected: it changes something the writer owns. The e2e asserts the selection anchor and head are unchanged.

**Source view.** `S.alts.exclusive` refuses in Source mode, so this path flushes directly with `W.flushAll()` as `split.js` does. The textarea's `selectionStart/End` are the Markdown offsets: quote = the slice, context 24 characters either side. No marks are drawn in Source view. If the slice crosses a line break the server's refusal is shown.
**A chapter too long to edit (read-only).** The tab and the chapter box work; marks are drawn; selection comments are offered where `ed.alts` is present. To confirm at build.
**Rejected:** comment on a selection that crosses paragraphs (the server rule is one block; the message points to the chapter box), and `plain: true` from the editor. Plain mode would need the editor's text to match the visible text a reader sees, which fails next to private notes and ghosted text (the editor shows them; the server's reading text does not). `sourceOf` is exact there. If the corpus test (section 11) finds `sourceOf` returning null often, plain mode becomes the fallback for that case; not planned.

### 6.4 Focus, keyboard, 390 px, both themes

- **Focus never moves unless the writer asked.** The tab opening from the cookie on a page load, a refresh, a live update: none takes focus. The chord, the button and the palette entry do, to the composer's text box. Return is Escape or the chord, to the editor, no scrolling.
- **Keyboard only:** Shift+Arrows select, Alt+Shift+M, type, Ctrl or Command+Enter to add (the existing `form()` rule), Escape to cancel (the typed text is kept for this chapter in memory and put back into the next composer), Tab order: text box, the two radios, Add, Cancel. The quote is the box's description (`aria-describedby`), so a screen reader hears the words before typing. Every action is also a button.
- **390 px.** The inspector is a bottom sheet (`inspector.css` `max-width: 639px`: `max-height: 72dvh`, and `html.kb` lifts it above the on-screen keyboard). When the composer opens at that width and the words would be under the sheet, scroll them into the free upper part exactly as the reading view's `reveal` does (the only scroll the feature ever causes, and not at all when they are already visible; at 1150 px and wider the panel is beside the page and nothing scrolls). After Add the sheet closes, then focus goes to the editor. Targets stay `var(--target)`. The format bar is the phone entrance; its horizontal scroll cue already exists.
- **Selection survives the tap on the button.** The plan relies on ProseMirror keeping `state.selection` when the editor is blurred, as the compendium plan does; `S.alts.sync()` first reads the browser's selection. To be proven on a real phone, because the Chromium run cannot show it.
- **Both themes.** Colours are the existing tokens `--comment` (wash) and `--comment-line`, with dark values already defined (`app.css:73,103,131`). `scripts/check-contrast.py` already pins prose on the comment wash at 7.0; add "comment line on page" at 3.0. Judge by real screenshots in both themes at 1280 and 390, not from CSS.

## 7. Showing where a comment sits

### 7.1 How mention marks are drawn despite "the single slot", and whether comments can use the route

As in section 2.1: the registry `scriobh.decorate` lets many users share the one prop. `mentions.js` and `lab.js` are the two precedents. Comments can use it safely **by the Lab's route** if all of these hold:

1. Built only from saved text: when `view.state.doc` is not the one asked about, or `W.rev` moved, or `W.unsaved()`, nothing is drawn and the ask is retried later (the Lab's `draw()` returns false and calls `later(900)`).
2. One `DecorationSet` kept from document to document and carried through each edit with `ed.alts.mapSet` (`findDiffStart`/`findDiffEnd` and a `StepMap`); rebuilt only after an ask or an op. A key never rebuilds the set.
3. Each mark is checked against the text: after `rangesOf`/`toLive`, `textBetween(from, to)` must equal the server's `quote_plain` (`comment.list?plain=true` already computes it). A mark that fails is not drawn and its card has no Show button. If the whole answer cannot be placed, nothing is drawn (the mention rule).
4. Only **open, active** threads are drawn. Resolved and lost ones are listed, not drawn.
5. Loaded only when it can matter (section 8): when the chapter has open comments, or the tab has been opened. A chapter with none loads nothing.
6. Cost is measured, not assumed. `shell.js` re-adds every decoration set after the first on each state update (`out.add(state.doc, sets[i].find())`), so a third set costs `find()` over its marks per keystroke. A document may hold 1,000 open threads. Gate: with 0, 200 and 1,000 marks in a 20,000-word chapter on a 390 x 844 phone at 4x CPU slowdown, typing p95 (keydown to input) stays under 16 ms, and with 0 marks it is within 0.5 ms of today. If 1,000 fails, marks above a cap (for example 300 nearest the caret) are not drawn and the tab says so. Same method as `e2e-altperf`.
7. Re-asking: after a save, 2.5 seconds still (the mention rule, not while keys are going down), one `comment.list?plain=true` for the document. If the response for a heavy chapter is large or slow, `comment.list` gets a `compact=true` form (id, status, anchor, plain quote, no replies); decide from the measurement, do not add it first.

Look: `background: var(--comment); border-block-end: 2px solid var(--comment-line)` on an inline decoration, the same fill and line as the reading view's `mark.rv-mark`. An inline box's border changes neither the line height nor the width, so nothing reflows; no animation. It is a fill with a thick line, which is not the mention underline (1 px, muted), the wiki dots, the Lab's coloured line or the alternatives' wavy marks, and it does not depend on colour alone. Printing: no mark (as `@media print` does for the Lab).

### 7.2 The minimum, phase 1, with no marks

Each card already shows its quote (`note()` in `review.js`). Phase 1 adds **Show in the text**. The tab loads `start` and `end` once, and they are valid only at `anchor.revision`, so the press does not trust the loaded copy:
1. If text is unsaved, `W.flushAll()` first; if that fails, say "Your text is not saved yet." and stop.
2. Re-read the thread (`comment.get`) so the anchor is current.
3. Require `anchor.state === "active"` and `anchor.revision === W.rev` and nothing unsaved. If the revision differs (another person saved), say "The chapter changed since this was loaded. Reload it, then try again." and stop; never place on an old range.
4. Place it: `ed.alts.rangesOf(bodyOf(W.serverMd), [{start, end}])` then the `toLive` correction (the block-edge spaces the stored text loses), then check `view.state.doc.textBetween(from, to, "", "\ufffc")` equals `quote_plain`. On a mismatch say "Those words are not in the text as it is now." and do not scroll.
5. Scroll to the middle with the DOM (`view.domAtPos(from).node` `scrollIntoView({block: "center"})`) and show a temporary decoration (`cmt-flash`, a static outline, no animation) that clears after two seconds or at the next key.
It does not set the selection and does not focus the editor, so the caret does not move. Do not copy `lab.js`'s `select`, which moves the caret and focuses.

The placement code (`rangesOf` call, `toLive`, `bodyOf`; about 450 B gzip, copied from `lab.js` because the bundle is not rebuilt for this) therefore belongs to phase 1 in `cwrite.js` and is exposed as `S.cwrite.place` for the marks of phase 2 to reuse.

### 7.3 Clicking and pressing

- A click or tap on a mark while the Comments tab is showing scrolls its card into view and gives it a current state (the Lab's rule: a tap in the text never opens a panel over it). The click also places the caret as any click does; nothing else moves.
- Alt+Shift+M with the caret inside a mark opens the tab on that card with focus on the card (phase 2).
- Palette: "Next comment" and "Previous comment" (from the drawn marks, wrapping, no new chord). They scroll and flash and move no caret.

### 7.4 Lost anchors, the way F-416 does it

F-416 keeps a wording whose text was edited away, lists it as lost, and lets the writer re-attach it to a range; it is never dropped and never moved by a guess. Comments already follow that (`anchor_state = 'orphaned'`, listed after the others, the original words kept, exact words returning re-adopt it in `AfterWrite`). The tab adds the writer's side:
- the card says "The words this was on are gone from the text." and shows the original words (already there);
- **Attach to the selected words** (enabled when the editor has a valid selection) runs the section 6.3 steps 1-5 and calls `comment.reanchor` with `quote/before/after`;
- **Keep on the chapter** calls `comment.reanchor {detach: true}`, so the comment lives on and is no longer lost;
- deleting stays available. Nothing is attached or moved without the writer's press.

## 8. Budget

All gzip, as `go test ./internal/server -run TestWritingViewBudgets` measures them today. Estimates are to be measured at each phase; a limit is raised only in the same commit as the `ARCHITECTURE.md` row.

| File | Now (measured) | Limit now | Change | After (estimate) | Limit after |
| --- | --- | --- | --- | --- | --- |
| `doc.js` + `tree.js` | 13,815 + 8,097 = 21,912 (about 16 B under after split) | 22,016 | **none, not touched** | same | same |
| `inspector.js` | 5,000 | 5,120 | `S.inspector.want`, the chord table entry (shared with the compendium plan, which raises the limit to 5,632 for its chord; this adds at most 200) | 5,200 to 5,500 | 5,632 |
| `review.js` | 13,365 | 14,336 | panel mode exports its helpers (`form`, `area`, `api`, `say`, `T`, `draw`, `replace`, `armed`), loads `cwrite.js` in `show()`, refreshes on `visibilitychange`, the tab count | about 13,600 | 14,336 (no change) |
| `cwrite.js` (new, lazy, tab only, `lazyJS` row) | n/a | n/a | the composer, chapter box, selection capture and submit, edit, Show, lost-anchor actions, share switch, the want hand-off | about 3,000 at the end of phase 3 (about 2,250 at the end of phase 1, with the placement code) | 3,584 (3,072 for phase 1) |
| `cmarks.js` (new, lazy, `lazyJS` row, `data-ext` only when the chapter has open comments, else loaded by `cwrite.js` after the first comment) | n/a | n/a | marks, `toLive`, mapping, re-ask, click, next and previous | about 900 (it reuses `S.cwrite.place`) | 1,536 |
| `format.js` | 2,835 | 3,072 | `run("comment")` and its `data-wait` line | about 2,930 | 3,072 |
| `palette.js` | about 7,200 | 12,288 | four commands | about 7,450 | 12,288 |
| `arm.js` | 1,511 | 1,792 | **none** | same | same |
| `review.css` | 2,513 | no per-file limit (loaded lazily, not counted in the page budget) | composer, fieldset, chip, `cmt-*` rules | about 2,900 | n/a |
| Chapter page HTML | n/a | the chapter page budget is 150 ms p95 | inline strings for the writer's side (about 30 keys, under 2 KB raw, 0.7 KB gzip), the tab count, the format bar button | measure with `scriobh bench` | unchanged gate |

A chapter with no comments and a tab that is never opened costs the writer: the inline strings, one button and one extra `comment.list` (limit 1, indexed) in the page handler, called through the op (`s.Reg.Call`, never SQL in the handler), measured against the chapter-page gate in `scriobh bench`. The marks file is not fetched. A chapter with open comments adds `cmarks.js` after `load` (the `data-ext` list), never in the way of typing.

`ARCHITECTURE.md` "Lazy script files" row gets `cwrite.js` and `cmarks.js`; `web_test.go`'s `lazyJS` table gets both; `TestWritingViewBudgets` and `TestCSSBudgetPerPage` stay green.

## 9. Ops, authorization, generated surfaces

| Op | Change | Auth | Registry follow-ups |
| --- | --- | --- | --- |
| `comment.create` | input `visibility`, rules in 4.2, `visibility` in `CommentOut` | reviewer (unchanged); author check inside for replies to private | `make skill`; description text; dry-run entry already exists (`dryrun_review_test.go`) |
| `comment.list`, `comment.get` | viewer-aware; `comment.list` gets a `visibility` filter (`shared`, `private`, `all`; default `all`, meaning every thread the caller can see) and the field on each item | reviewer | `make skill` |
| `review.queue`, `review.resolve_all` | viewer-aware; new `visibility` filter, **default `shared`** (existing behaviour; `queue` also accepts `all`, `resolve_all` accepts `shared` or `private` only) | unchanged | `make skill` |
| `comment.update`, `delete`, `resolve`, `reopen`, `reanchor` | 404 for a thread the caller cannot see; update rules in 4.2 | unchanged | none beyond text |
| `comment.share` (new) | `POST /api/v1/projects/{id}/comments/{comment_id}/share` | `ops.RequireProject(ops.ProjectReviewer)`, author-only check inside, 404 otherwise | an entry in the authz sweep (`internal/server/authz_test.go:241`), in the role table (`roles_test.go:65`, "reviewer"), in the dry-run table (`dryrun_review_test.go`), `make skill` and `make skill-check` |

A new op is chosen over a `visibility` field on `comment.update`: update's body is required and its 15-minute rule belongs to the words; sharing has different effects (notifications, an event) and a clear name for agents.

Webhook and event docs (`docs/API.md`, the webhook op text) say that a private comment sends nothing and that `comment.share` is what announces it.

## 10. FEATURES rows

Put in the section for the writing view's review screens, after F-707. Statuses are "Planned" until built.

| ID | Notes |
| --- | --- |
| F-1280 | Comments have a **visibility**: shared (all that exist today) or private ("Only me", the author alone). Migration `0480_comment_visibility`, `shared_at`, the reply trigger, the sweep of private rows whose author is gone. |
| F-1281 | `comment.create` takes `visibility`; a private comment has no mentions, no notification, no event, no webhook; replies take their thread's visibility; private rows have their own caps and are exempt from the hourly limit. |
| F-1282 | **Every read path honours it** (`comment.list/get/update/delete/resolve/reopen/reanchor`, `review.queue`, `review.resolve_all` and its preview count, notifications, share-link lists and counts, webhook text, live stream), 404 for a thread you cannot see, with the source scan test that fails on an unfiltered read of `comments`. |
| F-1283 | The Comments tab starts comments: **"Comment on this chapter"** box, "Only me" by default, the radio to create it shared when other people are on the project, the card chip "Only you", Edit for your own, the open count on the tab. |
| F-1284 | **Comment on the selected words** from the writing view: Alt+Shift+M, a format bar button and palette entries, the words named in the stored Markdown through the editor's serializer after the unsaved text is saved, focus back to the caret, the selection untouched, at 390 px with the keyboard alone. |
| F-1285 | **Show in the text**: scroll and flash without moving the caret; click a mark with the tab open to find its card; palette Next and Previous comment. |
| F-1286 | **Marks in the text**: open comments drawn through the decoration registry the Lab's way (saved text only, mapped through edits, checked against the words, none drawn when they cannot all be placed), with the typing-cost gate. |
| F-1287 | **Lost comments**, the F-416 way: listed with their original words, **Attach to the selected words** and **Keep on the chapter** (`comment.reanchor`), nothing moved by a guess. |
| F-1288 | `comment.share`: move an existing "Only me" comment to shared (author only, one way); notifications and the event fire then, once. |
| F-1289 | The review page and the reading view know the two states: the review page lists shared threads by default with an "Only me" chip for your own; the reading view marks your own private comments "Only you". |
| F-1290 | The writing view's Comments tab note and F-704 are amended: comment marks are drawn (replaces "not drawn"); OPEN-QUESTIONS line 1360 is superseded by the decision in section 7.1. |
| F-1291 | (Optional, separate) A palette entry that makes the selection a private note, so the `private` command can be found. |

Also: the changelog line when one exists; `docs/ACCESSIBILITY.md` writing-view line (the chord, Escape, the tab); `docs/DATA-MODEL.md` (migration row, `comments` description); `docs/API.md` (generated table plus the hand-written comment notes); `docs/OPEN-QUESTIONS.md` (the decisions: private one-way, new op over a field, quote over offsets, no floating menu, the Lab's route for marks).

## 11. Tests

### 11.1 Go

- `internal/domain/review/private_test.go` (uses `harness_test.go`): create private (row, no `comment_mentions`, no notification rows, no `events` rows, `mentions` refused, `@name` inert); reply rules; caps (private never uses the shared project, document or hourly budget; three authors cannot make `AfterWrite` orphan shared rows); list, counts and filters per viewer; every by-id op as another user is 404 and byte-identical to a missing id; `queue` and `resolve_all` (after=, `thread_ids`, the `expect_count` race); update window rules; `comment.share` (author only, one way, notifies once, event once, second call silent); sweep of a private row whose author account is soft-deleted.
- `TestEveryCommentReadFiltersVisibility`: the source scan of 5.1.
- `internal/server/writer_comments_test.go`: through the real handlers with sessions for Ada, Ben, Cy, Di, Eve and a share guest, every row of 5.2 that is not a domain test: the review page HTML, "Resolve all" through `POST /ui/projects/{id}/review/{action}`, the writing page count, share list and counts, SSE, webhooks with `include_text`, the mail digest count.
- `TestPrivateCommentNeverLeaves`: one canary comment (body and quoted words) made private, then absent from: Markdown, HTML, DOCX and EPUB exports, the static site, `md_bundle`, the Git mirror tree and each push (`gitmirror` canaries), `search.query`, the embedding text, `ai.context.preview`, the `events` and `audit_log` rows, and present in Ada's own `comment.list` and her agent key's.
- Registry tables: `authz_test.go`, `roles_test.go`, `dryrun_review_test.go`, `nodryrun_test.go` if needed, `make skill-check`; `web_test.go`'s `lazyJS`; i18n: the new strings live under a new prefix `cw.js.` in `en.json`, rendered into a hidden `#cw-msg` like `split-msg`, and `internal/i18n/i18n_test.go`'s `prefix` table gets `cwrite.js` and `cmarks.js` mapped to it (that test fails on a key a script uses that the catalogue lacks).
- Upgrade: an `internal/db` upgrade test that starts from a database at `0470` with shared comments, applies `0480`, and checks every old comment is `shared` with `shared_at = created_at` (precedent: `cover_layouts_upgrade_test.go`).
- Editor corpus: a Node test next to `web/editor/test/split.test.mjs` that, over `internal/editor/testdata/corpus`, selects the first word and a mid-paragraph run in each paragraph and checks `sourceOf` is non-null and its quote is found once in the serializer text. The null rate is the number that decides the plain-mode question in 6.3.

### 11.2 Browser

**New `scripts/e2e-writer-comments.mjs`** (added to `make e2e-fast`), in the style of `e2e-split.mjs`:
1. *Comment mid-paragraph with unsaved text.* Seed a chapter whose second paragraph has `*emphasised*` words and a repeated phrase. Open it. Type " QQ" before the target (unsaved: assert `scriobh.write.unsaved()`). Select a run that crosses the end of the emphasis. Record `markdown()`, the selection anchor and head, `window.scrollY`, the `view.state` object, `rev`. Press Alt+Shift+M. Assert: Comments tab open, composer shows the words, focus is the text box, `view.state` is still the recorded object while typing in the box (nothing dispatched), scroll and caret unchanged. Type, press Ctrl or Command+Enter. Then: a card appears; `comment.list` gives `visibility: "private"`, an anchor whose `quote` equals the **exact Markdown slice** `content.slice(start, end)` (asterisks included) and the expected string; the stored text contains " QQ" and the revision went up once from the flush only; the editor's `markdown()` equals the stored content; `document.activeElement` is inside the editor; selection anchor, head and `scrollY` equal the recorded ones; the Source view was never shown (the `__source` watcher from `e2e-split`).
2. *Second user.* Ben, an editor, opens the same chapter in another context: no card, no mark, tab count without it, `GET .../comments/{id}` is 404, byte-equal to an unknown id. Ben's own private comment is not visible to Ada.
3. *Chapter box.* No selection: open the tab, the box has focus when opened by the chord, type, Add, a card "On the chapter as a whole" with the "Only you" chip; focus returns to the caret.
4. *Keyboard only at 390 x 844.* No mouse: Shift+Arrows, the chord, type, Ctrl or Command+Enter, Escape paths (cancel keeps the draft), the sheet shows the words in the free part above it, closes after Add, focus is in the editor, no horizontal scroll, targets at least `--target`, announcements read from `#rv-live`.
5. *Share and one-way.* With Ben on the project, the fieldset shows; share a comment; Ben's tab and notification count show it once; there is no control to make it private again.
6. *Marks (phase 2).* The mark is drawn with the exact words (`textContent` equals `quote_plain`); typing before it moves it; deleting the words removes the mark and, after the save, the card says the words are gone and offers Attach and Keep; Attach to a new selection re-draws it; both themes at 1280 and 390 screenshots for the review of looks.
7. *a11y.* Add the Comments tab with the composer open to `e2e-a11y.mjs` in both themes at both widths.
8. *No focus theft.* Reload with the tab remembered open: nothing takes focus; a live `comment.shared` event does not clobber a half-typed reply or composer.

**New `scripts/e2e-commentperf.mjs`** (`e2e-altperf` method, 0, 200 and 1,000 marks, 4x CPU, 390 x 844; typing p95 under 16 ms, 0 marks within 0.5 ms of today). The seeding needs the hourly limit out of the way: seed through the domain layer from a Go helper, or through a test-only setting like `SCRIOBH_MAX_ALTS_PER_DOC`; decide at build.

**Existing scripts that must stay green:**
- `e2e-review.mjs`: line 347 asserts the panel says comments are "not in the text as you write"; that sentence changes and the assertion changes with it, in the same commit. Its other steps (reading view, review page, "Resolve all" plan, Next unresolved, scrolling marks) are the regression guard for the default `shared`.
- `e2e-review-ui7.mjs`, `e2e-review3.mjs`, `e2e-share.mjs` (guest comments and counts), `e2e-live.mjs`, `e2e-mail.mjs`.
- Editor and decorations: `e2e-write.mjs`, `e2e-write2.mjs`, `e2e-phase2.mjs`, `e2e-long.mjs` (20,000 words), `e2e-mentions.mjs`, `e2e-alts.mjs`, `e2e-alts2.mjs`, `e2e-altperf.mjs`, `e2e-ai-lab.mjs`, `e2e-ai-edit.mjs` (the chord table must report no clash), `e2e-scene.mjs`, `e2e-gate.mjs` (held drafts), `e2e-themes.mjs`, `e2e-i18n.mjs`, `e2e-split.mjs` once merged, `e2e-compendium.mjs`/`e2e-entities.mjs` and the compendium sidebar's own new script.
- `scripts/csp-smoke.mjs` (it opens the Comments tab at line 748; the new composer must obey the CSP: classes only, no `style=` and no inline script; any placement uses the adopted style sheet as `alts.js` does).
- `go vet ./...`, `go test ./...`, `go test ./internal/server -run Authz`, `go test ./internal/editor -run RoundTrip`, `./scripts/check-contrast.py`, `make skill-check`, and the desktop smoke at 390 px, keyboard alone, with the network cut after a reload.

## 12. Phasing, biggest win first

**Phase 0, a day, no code.** Land this plan as `docs/plans/WRITER-COMMENTS.md`. Amend F-704 and the OPEN-QUESTIONS note (the plan is the contract). Reserve Alt+Shift+M and the `S.inspector.want` name with the compendium plan's author.

**Phase 1, the win: write a comment to yourself from the sidebar** (F-1280 to F-1284, the Edit control of F-1283, and Show in the text from F-1285).
- Migration, `visible()` and `fetch`, every read path of 5.2, the source scan, `comment.create` private, the caps, no events. Visibility is built **before** any UI: no UI ships on an unfiltered table.
- The tab: chapter box, composer for selection, "Only me" default and, when other people are on the project, the radio that **creates the comment shared** (so phase 1 lets a writer write a shared comment from the writing view; only moving a private one to shared later waits for phase 3), the chip, Edit, the tab count, Show in the text (scroll and flash, no marks), the chord, the format bar button and the palette entries.
- Budget at the end: `cwrite.js` about 2,250 (limit 3,072 for this phase), `inspector.js`, `review.js`, `format.js` as in section 8.
- Tests: all of 11.1 except `comment.share`; `e2e-comments-write` steps 1 to 4 and 8; the existing scripts.

**Phase 2, see where it sits** (F-1286, F-1287, the click and Next/Previous of F-1285). Marks through the decoration registry, `cmarks.js`, the lost-anchor actions, `e2e-commentperf` as a gate. If the gate fails at 1,000 the cap in 7.1 ships instead and the number is written into the docs.

**Phase 3, share later, and the other screens** (F-1288, F-1289). `comment.share` (moving an existing "Only me" comment to shared, with a confirmation through `arm.js`'s `ask`, already loaded for the delete confirmation), the review page chip, the reading view label, the new e2e step 5, the sweep and role-table entries.

**Phase 4, optional.** F-1291; a compact list form of `comment.list` if the measurement asks for it; "Only me" in the reading view's composer for reviewers.

## 13. Risks, and what is needed from the owner

Risks to watch:
- Every raw `FROM comments` is a leak waiting; the scan test is the control. Reviewers should read that test first.
- The chord letter and the `want` function are shared with the compendium plan. If that plan changes its hand-off, section 6.1 follows it.
- `inspector.js` has 120 B left today; both plans depend on its limit being raised together.
- Selection kept while the editor is blurred on iOS and Android is assumed (as in the compendium plan) and is the one thing Chromium cannot show.
- Marks cost: unmeasured until `e2e-commentperf` runs; the plan has a cap ready.
- A private comment's quote is stored beside the body; backups hold it like everything else.

Questions, each with a recommendation (none blocks phase 1):
1. **Does "Only me" mean the author alone?** Assumed yes: not the owner, not editors, not an administrator.
2. **Should the author's own agent keys (CLI, MCP) see their private comments?** Assumed yes, since a key acts as its person; say no and it is one more filter and a note in the skill text.
3. Should a reviewer be able to keep "Only me" comments in the reading view? Recommended later (phase 4), not now.

## 14. Phase 0: where main differed from this plan (checked at f9f1e039), and what was decided

- **`S.inspector.want(panel)` did not exist.** The compendium build shipped `S.inspector.compendium(how)`, `S.lookupWant` and `S.inspector.toEditor()` instead, and `inspector.js` claims Alt+Shift+C itself. This build adds `S.inspector.want(panel)` for the Comments tab only (it records the editor selection and opens the tab); the Compendium's own hand-off is not changed.
- **Budget numbers.** `inspector.js` has a limit of 6,144 B gzip in `web_test.go`, not 5,632 (5,870 B at the start of this build); `review.js` 13,365 of 14,336; `format.js` 3,037 of 3,072; `lookup.js` 5,915 of 6,144. Each limit raised by this build is raised in the same commit as its row in `docs/ARCHITECTURE.md`, with the reason.
- **Lost-anchor handling is in phase 1**, by the owner's instruction (it was phase 2 here), so `cwrite.js` is budgeted for Attach to the selected words and Keep on the chapter. Marks in the text, `cmarks.js`, `comment.share`, the review page chip, the reading-view label and Next/Previous remain phases 2 and 3.
- **Phase 1 changes to the reading view and the review page are only these:** `review.queue` and `review.resolve_all` take `visibility` with the default `shared`, and the reading view's "Next unresolved" passes `all` so that it steps through what the margin shows.
- **Split orphaning** is at `internal/domain/docs/docs.go:1175` (the plan said 1161). It is an `UPDATE comments` with no `FROM` and no `RETURNING`: it reads nothing, so the scan test does not see it and it is not on the list of named exceptions (the plan's section 5.1 said it would be). If it ever reads (`RETURNING`, a subselect), the scan fails and it has to say why.
- **Reads of `comments` the first draft of section 5.1 did not list:** `share/ops.go:570` (`share.usage` counts) and `share/public.go:137, 418, 465, 470, 478` (guest counts, rate limit and double-submit checks). They are all keyed by `share_id`, which a private row never has; the scan test requires each to say so (`visibility`).
- **`comment.get` has no `plain` input;** Show in the text asks `comment.list?document_id&plain=true` for the fresh anchor.
- **Shortcuts.** Alt+Shift+M is a registry row (group `comments`, `Needs: "edit"`, `Claim: "KeyM"`), with its words, an entry in the desktop shell's `APP_CHORDS`, and a probe in `scripts/e2e-shortcuts-keys.mjs`.
- **Format bar.** The button is `data-cmd="comment"`, drawn only for people who can edit (as the Comments tab is), and goes through the same code as the chord. `format.js` has 35 B of headroom, so its limit is raised by the cost of the one line, with the reason in `ARCHITECTURE.md`.

### 14.1 What phase 1 turned out to be, where it differs from the text above

- **Show in the text compares content, not revisions.** A Details save moves `documents.revision` without `comanchor.AfterWrite` (only a change of the text calls it), so `anchor.revision === W.rev` would say "the chapter changed" after any metadata edit. The press re-reads the thread (`comment.list?plain=true`) and the chapter (`doc.get`) and places the words only when the stored text is the text the editor was loaded from or saved as (`W.serverMd`), and when the words found are the thread's `quote_plain`.
- **A new card is not focused or scrolled to.** `scrollIntoView` on a card in the tab moved the page (the e2e caught it); focus goes back to the text, and the page stays where it was. Putting focus back with `view.focus()` is also followed by a check of the page's scroll position, put back if it moved.
- **The scan test is per function,** not per statement: a function that has the SQL of a read of `comments` must call `visible`, `audience` or `threadFilter`, or name `visibility` in a literal; the three exceptions (maintenance and the one `FROM` that only `fetch` uses) are named with their reasons, and the test fails if one stops being a read. A statement that is unfiltered inside a function that also has a filtered one would pass: split such a function.
- **The Git push** is covered through the mirror (`TestReviewDataIsNeverMirrored` has a private comment now), since a push is built from the mirror's files and nothing of a comment is in them; the gitmirror tests use a fake source and could not carry a comment.
- **`review.queue` takes `shared`, `private` or `all`** (the plan said `all` only), `review.resolve_all` takes `shared` or `private`; private resolving tells no one and leaves no audit row or event, so the owner's audit log cannot show that notes exist.
- **Where the first press opens the sidebar,** the page is laid out again as for Alt+Shift+C (the editor column narrows); what the e2e asserts unchanged (selection, caret, scroll, the editor's state object) it asserts with the inspector already open.
- **Selections across a mark's edge are several spans.** A selection across the end of an emphasis is held and flashed as two inline spans (one per text node); the e2e joins their text.
- **A private comment is not kept for an Idempotency-Key's replay** (`ThreadItem.KeepOutOfReplay`, honoured in `internal/server/rest.go` as `SecretOutput` is): the stored copy of the answer was the one place outside `comments` that held the words. A repeated key is refused, so a retry cannot write a second comment.
- **Deleting words that end inside emphasis can leave the thread active on a stray `*`.** `comanchor`'s rebase shrinks an anchor to what is left of its words (here the closing asterisk), so the card is not offered Attach or Keep. This is how reading-view comments have always behaved; the e2e deletes the emphasis with the words. Not changed by this build.
- **The scan is per statement** (reviewer's finding, replacing the per-function rule above): an assignment, call, return or declaration, without the bodies of function literals in it. A read is `FROM` or `JOIN comments`, an `UPDATE`, `INSERT` or `DELETE` on comments with `RETURNING`, or a table name made at run time (a literal that ends in `FROM`) in a function that lists `sqlite_master`. A statement must call `visible`, `audience` or `onlyShared`, use `commentFrom` (fetch's), or name `visibility`; the exceptions are named by function (`exempt`) or by function and a piece of SQL (`exemptSQL`), each with its reason, and a stale one fails the test. It walks `internal/` and `cmd/`. The reply count in `InsertComment` and the subselects of `threadFilter` name `visibility` now (a reply is in its thread's audience).
- **Re-anchoring bounds private threads per author** (`comanchor.maxProcessedPrivate`, 1,000 per author per chapter, resolved ones included), so one person's notes cannot orphan anyone else's anchors or a shared one.
- **A comment does not call `ensurePlaced`** (review finding): that forces the editor's spelling of the Markdown into the chapter (a revision, a `doc.updated`, a mirror diff) just to find a place. The composer flushes only what the writer typed (`flushAll()`, never `flushAll(true)`), names the words with `sourceOf` when `S.alts.placed()` says the editor and the stored text spell alike, and otherwise sends them as a reader sees them (`plain: true`); if the server cannot find them it says so and nothing is written. The editor is held only while the words are named; the POST goes out after it is let go.
- **On a phone, the room under the last line stays** (`body.cw-pad`, set when the tab is first shown, 639 px and below): closing the sheet removes no padding, so the page can never be clamped or jump, the last paragraph included. Lifting happens only below 640 px (where the inspector is a sheet), the page is put back on Escape and after Add unless the writer has moved it, and the inspector's own close no longer scrolls to the button it focuses.

### 14.2 Phase 2 (U11.4): where the build differs from section 7 and 8

- **The mark code is its own file, `js/cmarks.js` (3.1 KB gzip, limit 3.25 KB), not `S.cwrite.place`.** `cwrite.js` needs the reading view's `review.js` and is fetched only with the Comments tab, so a chapter with comments would have to load 21 KB at mount to place a mark. The place code (`bodyOf`, `toLive`, about 0.5 KB gzip) is copied, as section 7.2 allowed for the first copy.
- **The file loads when there is something to mark.** `data-ext` names it only when the chapter has an open comment the person can read (`OpenComments`, which is counted for people who can edit). The Comments tab loads it the first time it has an open thread (`review.js`, one line in `draw`), so the first comment on a plain chapter is marked without a reload. A chapter with none loads nothing, even with the tab open.
- **A click opens the comment.** Section 7.3 said the click only scrolls a card that is already showing. The task for this build asked for the click to open the tab, so it does: the tab opens, the card is marked current for 1.5 s and scrolled to with the tab's own scroll, and focus and the caret stay where the click put them. The card is not focused (`R.focusCard` focuses and scrolls the page, which section 14.1 found to be a fault).
- **Not built, from section 12 phase 2:** Next comment and Previous comment (palette), Alt+Shift+M while the caret is inside a mark, and `e2e-commentperf` (the typing-cost gate at 0, 200 and 1,000 marks). The mark list is read as one page of 200 and up to five pages (1,000 threads), so a chapter with more than that shows its first 1,000 by the server's order.
- **Readers and reviewers get no marks** in the writing view, because the writing view's count of open comments is taken for people who can edit (as the tab's count already is). The reading view's margin is unchanged.
- **The Comments tab's note changed** to say the words "can be marked in the text", with its e2e assertion (`e2e-review.mjs`) changed in the same commit.
