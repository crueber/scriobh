# API reference

The REST API of Scríobh is not written by hand. Every capability is one **op** in the registry (`internal/ops`, registered by each domain's `Register`), and the same registration is rendered into the HTML pages, the REST routes under `/api/v1`, the agent CLI (`scriobh-cli`), the MCP tools and this table. So there is one set of rules for all of them: the same authorization, the same validation, the same errors.

This page has four kinds of content:

- **How the API behaves** (authentication, envelope, errors, concurrency, idempotency, dry runs, pagination, limits): written by hand and checked against the handlers.
- **The op table** (every op, with its method, path, access, scope and flags): generated from the registry by `make skill`, between the marker comments below. CI fails (`make skill-check`, and `go test ./cmd/scriobh-skillgen`) when it drifts. Do not edit it by hand.
- **Worked examples** you can paste into a shell against a development server.
- **Webhooks and MCP**, which are separate surfaces with their own pages in [OPERATIONS.md](OPERATIONS.md).

For one op's exact input and output fields, ask the server: `GET /api/v1/_ops` returns every op with a JSON-schema-like description of its input (each field says whether it is a path, query or body field, and whether it is required) and its output; `scriobh-cli ops` and `scriobh ops` print the same document. The schema is the contract for field names; this page does not copy it.

## Contents

- [Try it in five minutes](#try-it-in-five-minutes)
- [Authentication](#authentication)
- [Requests and responses](#requests-and-responses)
- [Errors](#errors)
- [Concurrent edits: `base_revision`](#concurrent-edits-base_revision)
- [Idempotency keys](#idempotency-keys)
- [Dry runs](#dry-runs)
- [Pagination](#pagination)
- [Limits and rate limits](#limits-and-rate-limits)
- [Routes that are not ops](#routes-that-are-not-ops)
- [Worked examples](#worked-examples)
- [Webhooks](#webhooks)
- [MCP](#mcp)
- [The op table](#the-op-table)

## Try it in five minutes

The examples in this page run against a development build, which has a built-in identity provider with three fixed test users. A production build has none of that: there you sign in with a browser and make a key under **Settings, API keys**, and the rest is the same.

```
make run                       # builds ./scriobh and prints  LISTEN http://127.0.0.1:<port>
```

In another terminal (the port is whatever `LISTEN` printed; `./data` is where this build keeps its data):

```
export BASE=http://127.0.0.1:PORT
scripts/dev-login.sh $BASE jar.txt dev-writer         # a session cookie, through the real OIDC flow
curl -s -b jar.txt $BASE/api/v1/_whoami

# make an API key (shown once) and keep it in a variable
export KEY=$(curl -s -b jar.txt -H 'Content-Type: application/json' \
  -d '{"name":"docs example","scope":"write","expires_in_days":1}' \
  $BASE/api/v1/keys | python3 -c 'import json,sys; print(json.load(sys.stdin)["secret"])')

curl -s -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"title":"The Salt Road","kind":"novel"}' $BASE/api/v1/projects
```

`scripts/dev-login.sh` drives the sign-in page, the picker's form and the callback with `curl` and leaves the session in the cookie jar. It works only against a server started with `SCRIOBH_OIDC_STUB=1` (`make run` does that). The three users are `dev-admin` (administrator), `dev-writer` and `dev-reader`.

## Authentication

There are two ways in, and a request that offers a key is judged by the key alone.

### A browser session

Signing in goes through OpenID Connect (`/auth/login`, `/auth/oidc/callback`; see [OPERATIONS.md](OPERATIONS.md#registering-scríobh-with-an-identity-provider)). The result is the `scriobh_session` cookie (`HttpOnly`, `SameSite=Lax`, and `Secure` when `SCRIOBH_BASE_URL` is `https://`). Every `/api/v1` route accepts it, which is how the pages themselves call the API.

**CSRF.** Mutating requests with a session are guarded by Go's cross-origin check on every mutation: a request that the browser marks `Sec-Fetch-Site: cross-site` (or `same-site`), or whose `Origin` does not match the request's host (or `SCRIOBH_BASE_URL`), is refused with `403 forbidden` ("Cross-origin requests are not allowed."). A request with neither header (curl, a script) passes. The `X-CSRF-Token` header and `_csrf` form field you see on the pages belong to the `/ui/...` form routes, not to `/api/v1`. There are no CORS headers on `/api`: a page on another origin cannot read the answers either. (`/mcp` has its own origin rules; see [MCP](#mcp).)

Some ops are for a person at a browser only (the AI policy, share links, the notification feed, provider setup, administrator user changes). They are marked **browser session only** in the table and refuse an API key at every scope with `403`.

### An API key

Make one under **Settings, API keys** (or `key.create`, or `scriobh-cli key create`). Send it as `Authorization: Bearer scr_...` (or the `X-API-Key` header). The secret is shown once; the server keeps only its SHA-256. Keys are honoured on `/api/` and `/mcp` only: a key sent to a page is ignored, and a session cookie is never read on `/mcp`.

A key **acts as the person who made it, narrowed**:

| Narrowing | Meaning |
| --- | --- |
| `scope` `read` | Every op whose scope is `read` (most GETs). |
| `scope` `write` (the default) | Also every op whose scope is `write`: what its owner may write. |
| `scope` `admin` | Also ops whose scope is `admin`: key management, project and Compendium membership, webhooks, backups and the console, when its owner is allowed them. |
| `project_id` pin | Only that project and its primary Compendium; nothing instance-wide (`403` "this key is restricted to one project or compendium"). Another project answers `404`. |
| `compendium_id` pin | Only that Compendium, and no project. |
| `expires_in_days` | Default 90, `0` for never, at most 3650. |

A key can never be stronger than its owner and can never make a key stronger than itself. A new key's `agent` flag (default true) marks its writes in the audit log. A key that is not recognised (mistyped, revoked, expired, belonging to a removed person) is `401` with `WWW-Authenticate: Bearer realm="scriobh"`, and never falls back to a cookie; one client address that offers 30 unrecognised keys in a minute is then answered `429`.

Scope is **not** the same as the HTTP method. It is declared per op, defaults from the method (`GET` is `read`, anything else `write`, admin-only ops `admin`), and is overridden where that is wrong. The table's **Scope** column is the truth: `compile.preview` and `site.preview` are `POST` but need only `read`, and `project.member.add`, `webhook.create` and `key.create` need `admin` even for a project owner.

### Roles and what a refusal looks like

Access has two layers, both checked before the body is read:

- **Instance role** from the identity provider's groups: `admin`, `writer`, `reader`. Ops marked *instance writer* or *instance admin* need it (a reader cannot create a project: `403`).
- **Project role** (and **Compendium role**, which is separate: being on a project never grants the Compendium): `owner` over `editor` over `reviewer` over `reader`. The table says which an op needs. An instance administrator acts as `owner` of every project through the API (the pages treat an administrator as a stranger to what was not shared with them).

Refusals are chosen so that existence does not leak:

| Situation | Answer |
| --- | --- |
| Not signed in, or a bad key | `401 unauthenticated` |
| A project or Compendium you cannot see, or that does not exist | `404 not_found` ("project not found"): the two are indistinguishable |
| A member whose role is too low for the op | `403 forbidden` ("your role on this project cannot do this") |
| A key whose scope is too low | `403 forbidden` ("this key cannot do that; it needs the write scope") |
| A key pinned elsewhere | `404` for another project, `403` for an instance-wide op |
| An op for browsers only, called with a key | `403 forbidden` |
| An administrator-console op, called by anyone else | `404` ("page not found"), marked *404 to others* in the table |

`GET /api/v1/_whoami` says who the caller is and what it may do: the user, `via` (`session` or `apikey`), the key's scope, pin and expiry, the project or Compendium a pinned key works in, the schema hash and the server version. It answers for a pinned key, which instance-wide ops would refuse.

## Requests and responses

- **Base path** `/api/v1`. Dates and times are RFC 3339 UTC with milliseconds (`2026-11-14T09:28:49.770Z`). Ids are a short prefix and a sortable token: `prj_` project, `doc_` document, `key_` API key, `usr_` person, `cmt_` comment, `att_` attachment, `aip_` AI provider, `air_` AI run, `whk_` webhook, `whd_` delivery, `exp_` export.
- **Where a field goes** is in the schema: `path` fields are in the URL (`{id}`), `query` fields are in the query string (a boolean query value is true for `true` or `1`), everything else is a JSON object in the body. `GET` and `HEAD` take no body. A mutating request with body fields takes `Content-Type: application/json` (the server does not insist on the header, but send it).
- **Strict decoding.** An unknown field is `422 invalid` ("Malformed JSON: json: unknown field ..."), and so is trailing data after the JSON object. Send only the fields you mean; for `PATCH` ops send only the fields to change.
- **Success is the op's own output**, bare: `{"item": {...}}` for one thing, `{"items": [...], ...}` for a list, with the status the table lists (`200`, or `201` for a create, `202` for a push that was queued). There is **no `{"ok", "data"}` wrapper** on the wire; that shape is what `scriobh-cli` prints around the server's answer.
- **Errors** are `{"error": {...}}` with a non-2xx status (below).
- **Files.** Exports, covers, downloads and diagnostics return the file itself, not JSON: the right `Content-Type`, `Content-Disposition: attachment; filename="..."` and `X-Content-Type-Options: nosniff`. (The schema shows the op's output as a JSON object with a `body`; that is how the CLI and MCP see it, not what REST sends.) A file response is not stored for idempotent replay. Uploads go the other way: for an op marked *body is the file* the request body is the raw bytes, with the name and alt text in the query string, and the server decides the type from the bytes, never from the header.
- **Headers on every response:** `X-Request-Id` (quote it when you report a problem; the log has the same id), `Cache-Control: no-store` for everything but `/static` and a few cache-validated files, and the security headers. JSON answers are `Content-Type: application/json`.

## Errors

Every error is `{"error": {"code", "message", "field"?, "data"?, "retry_after"?}}`. The `message` is for people; branch on `code` and the status.

| `code` | Status | Meaning |
| --- | --- | --- |
| `unauthenticated` | 401 | No session or key, or a key that is not valid. |
| `forbidden` | 403 | Signed in, but the role, scope, pin or the browser-only rule refuses it; also a cross-origin mutation. |
| `not_found` | 404 | Nothing by that id, or nothing you may see. Also an unknown `/api/` path (`endpoint not found`). |
| `method_not_allowed` | 405 | A known path with the wrong method; `Allow` lists the right ones. |
| `conflict` | 409 | A stale `base_revision`, a write to a project or a Compendium that is in the trash (`data.reason` is `project_trashed` or `compendium_trashed`), a repeat of a request whose response held a secret, any other state that forbids the change. May carry `data` (below). |
| `invalid` | 422 | The input is wrong: malformed JSON, an unknown field, a missing or out-of-range value. `field` names it when one field is to blame. |
| `dry_run_unsupported` | 422 | A dry run of an op that has effects a rollback cannot undo (see [Dry runs](#dry-runs)). |
| `idempotency_key_reuse` | 422 | An `Idempotency-Key` was already used for a different request. |
| `too_large` | 413 | The request body is over the limit. |
| `quota_exceeded` | 413 | The write would take a person, a project or the instance over a storage quota. |
| `unsupported_type` | 415 | An upload is not a kind of file the server keeps. |
| `rate_limited` | 429 | Too many requests; `retry_after` (seconds) in the body and a `Retry-After` header. Also a busy compile or import slot (one person holds at most one import slot at a time, so a second import started while the first is still being read is refused this way), the wait after wrong passphrases on `project.unlock` (the right passphrase waits too, until the time given), and too many open live streams. |
| `provider_error` | 502 | An AI provider failed; the message is one plain sentence and never carries the provider's own words. |
| `internal` | 500 | Something went wrong here; the log has the cause, under the request id. |

**403 `forbidden` with `data.reason`.** Two refusals share the status and are told apart by `data.reason`, so a client can branch on them:

- `project_locked`: the project has a passphrase (`project.passphrase.set`) and this browser session has not unlocked it (or its unlock ended: idle, locked, a new passphrase). `data` carries `project_id`, `title` and `slug` and nothing else of the project. Unlock it with `project.unlock` (browser session only) and repeat.
- `project_keys_refused`: an API key reached a project that has a passphrase and whose owner does not accept keys (the default), or one the owner accepts keys for but did not approve this key. `data` is the same. A key cannot unlock a project; the owner approves keys in the passphrase settings (`allow_keys`, `approve_keys`).

Either can come back from every op that names a project (every op whose `{id}` is a project, and the ones that reach a project by a document, comment, attachment or edition id), over REST, MCP and the event stream; the only project ops that answer a locked caller are `project.passphrase.get`, `.set` and `.clear` (which check the passphrase themselves), `project.unlock` and `project.lock`; `project.list` shows a locked project by its title with `protected` and `locked` set. A page answers the same with the unlock page.

One exception to the table: a dry-run header the server cannot read is `400 invalid` with `field` set to the header's name (see below), so that a preview is never silently a real write.

`data` carries structured detail where a client can act on it. The one to know is the conflict on a stale save:

```
HTTP/1.1 409 Conflict
{"error":{"code":"conflict","message":"This document changed since you opened it.","data":{"current_revision":2,"reason":"revision"}}}
```

## Concurrent edits: `base_revision`

Documents carry an integer `revision` that goes up by one with every saved change. A write that depends on what you read sends the revision it read as `base_revision`: `doc.update` requires it, and so do anything addressed by character offsets (`comment.create` with `start`/`end`, `suggestion.create`, the AI selection). If the document has moved on, the answer is `409 conflict` with `data.current_revision`, and nothing is written. Fetch the document, merge, and send again with the new revision.

This is the whole of optimistic concurrency here. **There are no `ETag` or `If-Match` headers on documents**; `ETag` appears only where a cached copy is validated: `GET /api/v1/_ops` (the schema hash, `If-None-Match` gives `304`), the inline images and covers (`project.cover`, `GET /files/{id}`). The in-browser single-writer lease (`doc.lease`, the `tab_id` field) is for the editor; leave `tab_id` empty from a script.

## Idempotency keys

Any mutating call may carry `Idempotency-Key` (1 to 200 printable ASCII characters). The first request runs and, if it succeeded, its status and body are stored for 24 hours under the caller (one API key, or one person's browser session). Then:

- the same key with the same request (op, method, path, query and exact body) returns the stored answer with `Idempotent-Replay: true`;
- the same key with a different request is `422 idempotency_key_reuse`, and nothing runs;
- two requests with one key at once run once; the second waits and replays;
- a failed request stores nothing, so you can correct it and send it again with the same key;
- a response that holds a secret (a new API key, a webhook's signing secret), or generated text, or is over 1 MiB, is not stored; a repeat is `409`, not a second execution. File responses and uploads ignore the header;
- one caller's stored responses are capped (`SCRIOBH_IDEMPOTENCY_MAX_ENTRIES`, `_MAX_BYTES`), oldest dropped first.

```
curl -s -i -H "Authorization: Bearer $KEY" -H 'Idempotency-Key: demo-1' -H 'Content-Type: application/json' \
  -d '{"title":"The Salt Road","kind":"novel"}' $BASE/api/v1/projects      # 201, runs
# the same command again:                                                   # 201 + Idempotent-Replay: true, no second project
```

`scriobh-cli` sends a fresh key with every real write and retries network failures with the same one.

## Dry runs

Ask for a preview with `Prefer: dry-run` (RFC 7240: comma-separated tokens, any case; `dry-run` with any value but `false`, `0`, `no` or `off` asks) or `X-Scriobh-Dry-Run: 1` (`1`, `true`, `yes`, `on`; `0`, `false`, `no`, `off` or empty mean no). Either one asking is enough. Any other value is `400 invalid` and nothing runs.

The server authorizes and validates exactly as for a real call, runs the op inside the writer transaction, and **rolls it back**: nothing is committed, no event or audit row is written, nothing reaches the mirror or a remote. The answer is what the op would have returned, plus `dry_run: true`, `would_change` and `changes` (rows it would have inserted, updated or deleted), with `Preference-Applied: dry-run`:

```
curl -s -X PATCH -H "Authorization: Bearer $KEY" -H 'Prefer: dry-run' -H 'Content-Type: application/json' \
  -d '{"base_revision":2,"content":"Preview only."}' $BASE/api/v1/docs/$DOC
# {"changes":14,"dry_run":true,"item":{... "content":"Preview only." ...},"would_change":true}
```

A file-producing op answers with a description of the file (`"file": {"filename", "content_type", "bytes"}`), not the bytes. Reads (`GET`) run as they are. An op that has effects outside the database (a backup, a mirror rebuild, a Git push, an upload, a provider call that costs money, key creation) answers `422 dry_run_unsupported`; the table's **no dry run** note marks them.

## Pagination

There is no single pagination scheme: each list op uses the one its data suits, and the schema says which. Read the op's input and output.

| Style | Ops (examples) | How |
| --- | --- | --- |
| `limit` and `offset` | `search.query`, `comment.list`, `stash.list`, `timeline.event.list`, `doc.backlinks`, `admin.user.list` | Send `limit` (search: 1 to 50, default 20; comments: default 50, at most 200) and `offset`. The answer has `total` and, for most, `next_offset` (absent on the last page). |
| `cursor` | `ai.run.list`, `entity.list`, `project.attachment.list`, `webhook.deliveries` | Send the answer's `next_cursor` as `cursor` (webhook deliveries: `before`). Absent on the last page. |
| `before` | `revision.list`, `git.push_history` | The id of the last item you have (`revision.list` returns it as `next`); the next page is older. |
| `after` | `review.queue` | A thread id: the threads after it in queue order. |
| Not paged | `project.list` (at most 500), `compendium.list`, `doc.list`, `outline.get` | The whole answer. Where a cap exists (`project.list`, `compendium.list`) `truncated: true` says it cut the list short, and the field is absent otherwise; narrow with `q` or `state`. |

## Limits and rate limits

- **Rate limit.** `SCRIOBH_RATE_LIMIT` (default 600) requests a minute, per **principal**: one API key (which also spends its owner's allowance, so holding many keys does not multiply it), one signed-in person, or, for an anonymous request, one client address. The bucket allows a burst of a quarter of the rate (at least 10), then refills steadily. Over it the answer is `429` with `Retry-After` and `retry_after`, and nothing ran. Sign-in routes under `/auth/` have their own fixed limit of 30 a minute per address; share links (`/s/`) have separate limits. AI runs add `SCRIOBH_AI_RATE_PER_MIN` per person. Behind a reverse proxy set `SCRIOBH_TRUST_PROXY` so addresses are real ones (see [OPERATIONS.md](OPERATIONS.md#reverse-proxy)).
- **Body size.** 2 MiB for JSON requests; imports 48 MiB; an upload up to `SCRIOBH_UPLOAD_MAX_MB` (default 25), checked before the body is read. Over it: `413 too_large`. A quota (`SCRIOBH_QUOTA_*`) is `413 quota_exceeded`.
- **Time.** The server's write timeout is 60 seconds; a non-streamed AI run must finish in 50 (`ai/run/stream` is for longer answers); a MCP tool call in 55.
- **Compiles and site builds** share a gate (`SCRIOBH_COMPILE_CONCURRENCY`); one that cannot get a slot within two seconds is `429` "busy".

## Routes that are not ops

These are served by hand, outside the registry, so they are not in the generated table.

| Route | What it is |
| --- | --- |
| `GET /api/v1/_ops` | The op schema (every op, its input and output). `ETag` is the schema hash; `If-None-Match` gives `304`. Any signed-in caller. |
| `GET /api/v1/_whoami` | Who the caller is and what it may do (above). |
| `GET /api/v1/events?project=ID` or `?compendium=ID` | Live updates as server-sent events. Authorized like `project.get`/`compendium.get` and re-checked at every heartbeat (25 seconds). Frames carry an `id`; reconnect with `Last-Event-ID` (or `last_event_id`) to be caught up; `event: bye` ends it. At most `SCRIOBH_SSE_PER_USER` (10) and `SCRIOBH_SSE_TOTAL` (200) streams; a proxy must not buffer it. See ARCHITECTURE.md, "Live updates". |
| `POST /api/v1/projects/{id}/ai/run/stream` | `ai.run` as server-sent events: `event: start` (model, provider, run id), `delta` (`{"text"}`), then `done` (the same `item` as `ai.run`) or `error`. Closing the connection cancels the request at the provider. |
| `GET /files/{id}` | An attachment's bytes, for someone who can read its owner; `ETag` is the content hash. |
| `GET /healthz`, `GET /readyz` | Liveness (`{"status":"ok","version"}`) and readiness (database up, every migration applied). No sign-in. |
| `POST /mcp` | The MCP endpoint ([below](#mcp)). |

## Worked examples

Every example below was run against a development server (`make run`) with the variables of [the first section](#try-it-in-five-minutes): `$BASE`, `$KEY` (a `write` key), and the ids the earlier steps printed. Answers are shortened with `...`.

### Projects

```
# create (writer or admin); the creator is the owner
curl -s -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"title":"The Salt Road","kind":"novel","author_name":"Wren Writer"}' $BASE/api/v1/projects
# 201 {"item":{"id":"prj_01M7...","slug":"the-salt-road","title":"The Salt Road","kind":"novel","role":"owner","word_count":0,"document_count":1,...}}

export PROJECT=prj_01M7...        # the id from that answer

curl -s -H "Authorization: Bearer $KEY" "$BASE/api/v1/projects?q=salt"        # list: pinned first, then most recently opened; state=active|archived|trashed|all
curl -s -H "Authorization: Bearer $KEY" $BASE/api/v1/projects/$PROJECT        # get one
curl -s -X PATCH -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"status":"drafting","word_goal":90000}' $BASE/api/v1/projects/$PROJECT # owner only; send only what changes
```

A new project starts with one empty chapter. Trash and restore with `project.trash` and `project.restore` (owner); `project.purge` deletes for good and is marked *permanent*.

### Documents

```
# the tree, flat, with reading order
curl -s -H "Authorization: Bearer $KEY" $BASE/api/v1/projects/$PROJECT/docs
# {"items":[{"id":"doc_...","kind":"chapter","title":"Chapter 1","revision":1,"word_count":0,...}],"order":["doc_..."]}

# create a chapter with text (editor)
curl -s -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"kind":"chapter","title":"Chapter 2","content":"The road to the salt flats began at dawn."}' \
  $BASE/api/v1/projects/$PROJECT/docs
# 201 {"item":{"id":"doc_01M7...","revision":1,"word_count":9,"content":"The road to ...","content_hash":"...",...}}

export DOC=doc_01M7...

# save: send the revision you last saw
curl -s -X PATCH -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"base_revision":1,"content":"The road to the salt flats began at dawn, and it was cold."}' $BASE/api/v1/docs/$DOC
# 200 {"item":{... "revision":2,"word_count":13 ...}}

# the same request again is now stale
# 409 {"error":{"code":"conflict","message":"This document changed since you opened it.","data":{"current_revision":2,"reason":"revision"}}}
```

`doc.get` returns the body, neighbours and breadcrumb; `doc.move`, `doc.trash`, `doc.restore` and `doc.split` reorganise; `revision.list`, `revision.diff` and `revision.restore` are the history. A document's text is Markdown; the app keeps its own annotations (alternatives, ghosts, private notes) in it, as the manual describes.

### Search

```
curl -s -H "Authorization: Bearer $KEY" "$BASE/api/v1/projects/$PROJECT/search?q=salt&limit=5"
# {"items":[{"document_id":"doc_...","title":"Chapter 2","matched_in":"body","snippet":"The road to the salt flats ...",
#   "snippet_html":"The road to the <mark>salt</mark> flats ...","offset":16,"word_count":13,...}],"total":1}
```

Every word must match, the last may be a prefix, case and diacritics are ignored. `snippet_html` is escaped apart from `<mark>`. Page with `offset` and `next_offset`. Research notes, the Compendium and its research have their own search ops (`search.research`, `search.compendium`, `search.compendium_research`).

### Export and compile

Exports return the file, not JSON.

```
# Markdown: one file, or mode "bundle" for a zip that can be imported again
curl -s -o salt-road.md -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"mode":"single"}' $BASE/api/v1/projects/$PROJECT/export
# 200  Content-Type: text/markdown; charset=utf-8   Content-Disposition: attachment; filename="the-salt-road.md"

# what a compile would do, writing nothing (scope read, role editor): pages, outline, warnings
curl -s -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"preset":"shunn-us"}' $BASE/api/v1/projects/$PROJECT/compile/preview
# {"preset":"shunn-us","words":13,"page_count":3,"warnings":[{"code":"no_author","message":"..."},{"code":"no_contact",...}],...}

# compile with a preset: a manuscript DOCX, an EPUB, or a print PDF
curl -s -o manuscript.docx -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"preset":"shunn-us"}' $BASE/api/v1/projects/$PROJECT/export/compile
curl -s -o proof.pdf -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"preset":"print-6x9"}' $BASE/api/v1/projects/$PROJECT/export/compile
```

The presets and their options are in [OPERATIONS.md](OPERATIONS.md#compile-docx-epub-markdown-and-the-submission-bundle) and in `compile.preset.list`. Exports are deterministic and recorded with a manifest; a finished one fires the `export.completed` webhook. With a dry-run header the answer is `{"dry_run":true,"file":{"filename","content_type","bytes"},...}`.

### Comments and review

Comments need the **reviewer** role (a reader cannot see them). A comment is a thread anchored to a range of the document's Markdown source, by `quote` with some `before`/`after` context (or by offsets at a `base_revision`); the anchor follows the text and goes *orphaned*, never guessed onto a lookalike.

```
# first, the owner (jar.txt) adds the dev-reader user, who must have signed in once, as a reviewer, and the reviewer gets a session.
# An API key would need an instance writer, which a reviewer need not be.
curl -s -b jar.txt -H 'Content-Type: application/json' -d '{"email":"reader@dev.scriobh","role":"reviewer"}' $BASE/api/v1/projects/$PROJECT/members
scripts/dev-login.sh $BASE reviewer.txt dev-reader

# a reviewer asks a question about a phrase
curl -s -b reviewer.txt -H 'Content-Type: application/json' \
  -d "{\"document_id\":\"$DOC\",\"quote\":\"salt flats\",\"body\":\"Which flats? Name them.\",\"kind\":\"question\"}" \
  $BASE/api/v1/projects/$PROJECT/comments
# 201 {"item":{"id":"cmt_...","thread_id":"cmt_...","kind":"question","status":"open",
#   "anchor":{"state":"active","start":16,"end":26,"quote":"salt flats","revision":2},...}}

# the writer's review queue: open threads in reading order, with counts
curl -s -H "Authorization: Bearer $KEY" "$BASE/api/v1/projects/$PROJECT/review/queue"      # editor role

curl -s -H "Authorization: Bearer $KEY" "$BASE/api/v1/projects/$PROJECT/comments?status=open"
curl -s -X POST -H "Authorization: Bearer $KEY" $BASE/api/v1/projects/$PROJECT/comments/$COMMENT/resolve    # $COMMENT: the comment's id, from the answers above
```

A reply is `comment.create` with `parent_id` (the thread's first comment). Suggested text changes are `suggestion.create` (reviewer; up to 50 changes at a `base_revision`), accepted or rejected by an editor with `suggestion.accept` and `suggestion.reject`. Both fire webhooks (`comment.created`, `suggestion.created`).

### AI

AI is off until the server allows it, a provider exists, and the project's **owner** turns it on in a browser. Everything that decides where text may go is browser-only: providers, the AI policy, never-send flags and budgets refuse a key (`403`). An API key can use AI only on a project whose owner enabled `agents_may_use_ai`; otherwise the preview says so and nothing is sent. The quick path to a working provider is in [OPERATIONS.md](OPERATIONS.md#ai-providers-quick-path).

Running is two steps: **preview** (builds the exact request, calls no provider, returns a `payload_hash` and `nonce`), then **run** with the same fields plus those two (a `409` if the request would now be different, so what is sent is what was shown). Nothing is written to the manuscript: the result is a suggestion with a diff that a person accepts.

```
# 0. Setup, once (the quick path is in OPERATIONS.md). An administrator adds a local provider; a stand-in for Ollama
#    on 127.0.0.1:11434 will do. Then the project's owner turns AI on for the project, in a browser session.
scripts/dev-login.sh $BASE admin.txt dev-admin
curl -s -b admin.txt -H 'Content-Type: application/json' -d '{"name":"Local Ollama","kind":"ollama",
  "base_url":"http://127.0.0.1:11434","scope":"instance","local":true,"default_model":"llama3.2:3b"}' $BASE/api/v1/ai/providers
export PROVIDER=aip_...                     # the id in that answer
curl -s -b admin.txt -X POST $BASE/api/v1/ai/providers/$PROVIDER/test
curl -s -b jar.txt -X PUT -H 'Content-Type: application/json' \
  -d '{"mode":"local_only","actions":["selection"]}' $BASE/api/v1/projects/$PROJECT/ai/policy

# 1. preview (any editor; a selection needs the revision its offsets were read at)
curl -s -b jar.txt -H 'Content-Type: application/json' -d '{
  "recipe":"rewrite", "document_id":"'$DOC'",
  "selection":{"start":0,"end":41,"base_revision":2},
  "params":{"instruction":"Make it livelier"}}' $BASE/api/v1/projects/$PROJECT/ai/preview
# {"item":{"decision":{"allowed":true},"policy_mode":"local_only","payload_hash":"ba54...","nonce":"35f4...",
#   "input_tokens":575,"cost":{"known":true,"local":true},"chips":[...],"attempts":[{"provider_name":"Local Ollama","body":"{...}"}],...}}

# 2. run: the same fields, plus payload_hash and nonce from the preview
curl -s -b jar.txt -H 'Content-Type: application/json' -d '{
  "recipe":"rewrite", "document_id":"'$DOC'", "selection":{"start":0,"end":41,"base_revision":2},
  "params":{"instruction":"Make it livelier"},
  "payload_hash":"ba54...", "nonce":"35f4..."}' $BASE/api/v1/projects/$PROJECT/ai/run
# {"item":{"run_id":"air_...","status":"ok","text":"...","data":{"kind":"edit","suggestions":[{"op":"replace","start":0,"end":41,
#   "original":"...","text":"...","diff":[{"op":"equal",...},{"op":"delete",...},{"op":"insert",...}]}],"expires_at":"..."},...}}
```

Then `ai.accept.edit` applies the suggestion (editor, explicit accept) and `ai.accept.reject` discards it. `ai.status` says whether AI is available and why not; `ai.recipe.list` lists the recipes and their parameters; `ai.run.list` is the run log (no prompt and no answer are kept). `ai.run` cannot be dry-run (it spends money) and its answer is never replayed. `ai/run/stream` is the same run as server-sent events. In the example the session cookie is used because the key is refused unless the owner allowed agents.

## Webhooks

A project's owner can have the project call an address when something happens. Make one with `webhook.create` (project owner; with a key, the `admin` scope), see the screen at `/p/<slug>/webhooks`, or use `scriobh-cli webhook create`. Setup, limits and the network rules (an `https://` public address unless `SCRIOBH_WEBHOOK_ALLOW_PRIVATE=1`) are in [OPERATIONS.md](OPERATIONS.md#webhooks).

**Events** (the `events` field of `webhook.create`), with the `data` each carries: `document.updated` (`document_id`, `revision`, `words`, `content`: true when the text changed), `comment.created` (`comment_id`, `thread_id`, `document_id`, `kind`; the comment's words only if `include_text` was set, up to 2,000 characters), `suggestion.created` (`suggestion_id`, `document_id`), `export.completed` (`export_id`, `format`, `documents`, `bytes`). `webhook.test` sends a `ping` (`{"webhook_id"}`) at once. No manuscript text is ever in a payload.

**A delivery** is a `POST` of JSON:

```
{"id":"whd_...","type":"export.completed","created_at":"2026-11-14T09:31:14.621Z","project_id":"prj_...",
 "data":{"bytes":27,"documents":1,"export_id":"exp_...","format":"md"}}
```

with these headers: `Content-Type: application/json`, `User-Agent: Scriobh-Webhook/<version>`, and

| Header | Value |
| --- | --- |
| `Scriobh-Webhook-Id` | The delivery id; the same on every attempt of one notification. Store the ones you have seen. |
| `Scriobh-Webhook-Event` | The event name (or `ping`). |
| `Scriobh-Webhook-Attempt` | 1 for the first try. |
| `Scriobh-Webhook-Timestamp` | Unix seconds. |
| `Scriobh-Webhook-Signature` | `v1=<hex>`: the lowercase hex HMAC-SHA256, keyed with the webhook's secret (the whole `whsec_...` string), of the timestamp, a full stop and the body exactly as received. While a rotated secret is still honoured there are two, `v1=<new>, v1=<old>`. |

To verify (this is what the repository's tests do, and it was run against a real delivery):

```python
import hmac, hashlib, time

def verify(secret: str, body: bytes, timestamp: str, signature_header: str, tolerance=300) -> bool:
    if abs(time.time() - int(timestamp)) > tolerance:          # reject a stale or replayed timestamp
        return False
    want = "v1=" + hmac.new(secret.encode(), timestamp.encode() + b"." + body, hashlib.sha256).hexdigest()
    return any(hmac.compare_digest(p.strip(), want) for p in signature_header.split(","))
```

**Retries.** Delivery is at least once. Answer `2xx` quickly. A `5xx`, `408`, `429`, a timeout or a refused connection is retried: eight attempts in all (`SCRIOBH_WEBHOOK_MAX_ATTEMPTS`), so seven waits of about 10 seconds, 1 minute, 5 minutes, 30 minutes, 2 hours, 6 hours and 12 hours, each plus or minus 20 percent (a 24-hour wait follows if the limit is raised above 8); a `Retry-After` from the receiver is honoured up to an hour. A `410`, any other `4xx` and any redirect end it, and the delivery is dead (`webhook.redeliver` queues it again). Ten dead deliveries in a row switch the webhook off. Saves of one document within five seconds (`SCRIOBH_WEBHOOK_DEBOUNCE_MS`) are one `document.updated`. Order is not promised; the `revision` orders document updates.

`webhook.rotate_secret` makes a new secret and lets the old one keep signing for 24 hours so a receiver can change over without a gap.

## MCP

`POST /mcp` speaks the Model Context Protocol (Streamable HTTP, stateless: one JSON answer per request, `GET` is `405`) with `Authorization: Bearer scr_...`; `scriobh-cli mcp` serves the same tools over stdio. A tool is an op, called as the key that made the call, so scope, pin, role and the trash apply exactly as on `/api/v1`. It does not implement OAuth, and it never reads a session cookie (`401`).

```
curl -s -H "Authorization: Bearer $KEY" -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}' "$BASE/mcp?groups=core&readonly=1"
```

`?groups=` (and the CLI's `--groups`) narrows the tool set, `readonly=1` offers only tools that cannot change anything, `permanent=1` asks for the tools that destroy data (only when the server runs with `SCRIOBH_MCP_PERMANENT_TOOLS=1`). Setup for desktop clients, the safety rules, the origin allow-list and what is never offered are in [OPERATIONS.md](OPERATIONS.md#mcp-the-model-context-protocol-endpoint); the generated tool reference is `skills/scriobh/mcp.md`. Agents that use the CLI or the skill, rather than MCP, are in [AGENT-GUIDE.md](AGENT-GUIDE.md).

## The op table

How to read it. **Request** is the method and path. **Access** is who may call the op: an *instance writer* or *instance admin*, or the lowest *project* or *Compendium* role (`reader`, `reviewer`, `editor`, `owner`). **Scope** is the lowest API-key scope that may call it (a browser session is not limited by scope). **Notes** lists what is unusual: *browser session only* (an API key is refused), *404 to others* (an operation of the administrator console answers `404` rather than `403`), *no dry run*, *permanent* (destroys data beyond the trash; MCP offers it only on request), *body is the file* (the request body is the file's bytes), and a success status other than 200. Groups are the first part of the op name.

<!-- BEGIN GENERATED: op table (make skill) -->

453 operations, schema `sha256:e7191bde55bb`. Generated from the op registry by `make skill`; do not edit between the markers.

#### `account` (3)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `account.typography.get` | `GET /api/v1/me/typography` | any signed-in caller | read |  | The caller's own typography override, which wins over every project's in their own browser, and the fixed schema. |
| `account.typography.reset` | `POST /api/v1/me/typography/reset` | any signed-in caller | write |  | Remove the caller's typography override, so each project's own typography applies. |
| `account.typography.set` | `PUT /api/v1/me/typography` | any signed-in caller | write |  | Set the caller's typography override, replacing what it had. |

#### `admin` (8)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `admin.diagnostics` | `GET /api/v1/admin/diagnostics` | instance admin | admin | 404 to others | A bundle for a bug report with nothing private in it: version and build, system, the configuration's shape (an allow-list of settings; no secret… |
| `admin.diagnostics.download` | `GET /api/v1/admin/diagnostics/download` | instance admin | admin | 404 to others | The same bundle as a zip (diagnostics.json and a README), the same bytes for the same state. |
| `admin.quota.list` | `GET /api/v1/admin/quotas` | instance admin | admin | 404 to others | What the instance and each person use against the limits that exist: attachment storage (per person, per project, whole instance, largest file) and… |
| `admin.user.disable` | `POST /api/v1/admin/users/{id}/disable` | instance admin | admin | browser session only, 404 to others | Switch a person off. They cannot sign in, every browser session of theirs ends now, their API keys and open streams stop working, and everything they… |
| `admin.user.enable` | `POST /api/v1/admin/users/{id}/enable` | instance admin | admin | browser session only, 404 to others | Switch a person back on. Their keys work again; they sign in as before. |
| `admin.user.get` | `GET /api/v1/admin/users/{id}` | instance admin | admin | 404 to others | One person by id, as admin.user.list shows them. Administrators only. |
| `admin.user.list` | `GET /api/v1/admin/users` | instance admin | admin | 404 to others | The people on this instance, for an administrator: name, email (an administrator sees it; nobody else does here), instance role, last seen, whether… |
| `admin.user.set_role` | `PATCH /api/v1/admin/users/{id}/role` | instance admin | admin | browser session only, 404 to others | Give or take away administration (role admin, or writer or reader for someone who is not one). |

#### `ai` (62)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `ai.accept.alternative` | `POST /api/v1/projects/{id}/ai/accept/alternative` | project editor | write |  | Add candidates of an alternatives run to an alternative (an existing one, or a new one over the text they were made for), as wordings with source… |
| `ai.accept.edit` | `POST /api/v1/projects/{id}/ai/accept/edit` | project editor | write |  | Accept one suggestion of a selection, chat or continue run: the server applies the text it holds for that run (never text you send) to the document… |
| `ai.accept.reject` | `POST /api/v1/projects/{id}/ai/accept/reject` | project editor | write |  | Dismiss a suggestion or candidate of a run (or every open one). |
| `ai.budget.get` | `GET /api/v1/ai/budget` | instance writer | read |  | The monthly caps that apply to you (the instance's, yours, and with project_id that project's) and how much of each is used or held for requests in… |
| `ai.budget.set` | `PUT /api/v1/ai/budget` | instance admin | admin | browser session only | Administrators, browser sessions only. Set a monthly cap for the instance, a person or a project, in tokens and in money (micro-units of the instance… |
| `ai.chat.append` | `POST /api/v1/projects/{id}/ai/chat/append` | project editor | write |  | Add a finished exchange to a kept chat (after a streamed chat run): the answer must be the one the run produced; its hash is checked against the run… |
| `ai.chat.create` | `POST /api/v1/projects/{id}/ai/chats` | project editor | write | 201 on success | Keep a conversation: nothing is stored on the server unless you create a chat and add to it. |
| `ai.chat.delete` | `DELETE /api/v1/projects/{id}/ai/chat` | project editor | write | permanent | Delete a kept chat and its messages. Works while AI is switched off: it sends nothing. |
| `ai.chat.get` | `GET /api/v1/projects/{id}/ai/chat` | project editor | read |  | One of your kept chats with its messages. Works while AI is switched off: it sends nothing. |
| `ai.chat.list` | `GET /api/v1/projects/{id}/ai/chats` | project editor | read |  | Your kept chats on the project. Works while AI is switched off: it sends nothing. |
| `ai.chat.preview` | `POST /api/v1/projects/{id}/ai/chat/preview` | project editor | write |  | Preview one chat message exactly as ai.context.preview does, with the earlier messages taken from a kept chat. |
| `ai.chat.send` | `POST /api/v1/projects/{id}/ai/chat/send` | project editor | write | no dry run | Send a previewed chat message (the preview's payload_hash and nonce are required, as for ai.run) and, for a kept chat, add the exchange to it. |
| `ai.context.preview` | `POST /api/v1/projects/{id}/ai/preview` | project editor | write |  | Build an AI request without sending it: the chips with their sizes, the exact request body for every provider that may receive it, the token and cost… |
| `ai.defaults.get` | `GET /api/v1/projects/{id}/ai/defaults` | project editor | read |  | The default provider chains per model class (cheap, strong, fast, local): the project's, yours and the instance's, and the one each class resolves to. |
| `ai.defaults.set` | `PUT /api/v1/projects/{id}/ai/defaults` | project editor | write | browser session only | From a browser session only (it is configuration, like the policy). |
| `ai.exclude.list` | `GET /api/v1/projects/{id}/ai/exclude` | project editor | read |  | What is flagged never-send in the project (entities as far as you may read them). |
| `ai.exclude.set` | `PUT /api/v1/projects/{id}/ai/exclude` | project editor | write | browser session only | Browser sessions only: an API key cannot lift a never-send flag. |
| `ai.extract.accept` | `POST /api/v1/projects/{id}/ai/extract/accept` | project editor | write |  | Create the entity an extraction proposed, in a Compendium the project draws on and you may edit. |
| `ai.extract.dismiss` | `POST /api/v1/projects/{id}/ai/extract/dismiss` | project editor | write |  | Dismiss a proposed entity. |
| `ai.lab.accept` | `POST /api/v1/projects/{id}/ai/lab/accept` | project editor | write |  | Accept one mark. A mark with a fix applies exactly that edit (a trim deletes the marked words and one neighbouring space), as one history entry with… |
| `ai.lab.accept_all` | `POST /api/v1/projects/{id}/ai/lab/accept_all` | project editor | write |  | Accept a chosen set of marks of one document as one change: one transaction, one history entry (ai-accepted). |
| `ai.lab.analyse` | `POST /api/v1/projects/{id}/ai/lab/analyse` | project editor | write |  | Run the Lab's local analysers on a document: long sentences, hedge words (English) and small punctuation fixes (double spaces, repeated marks, space… |
| `ai.lab.dismiss` | `POST /api/v1/projects/{id}/ai/lab/dismiss` | project editor | write |  | Dismiss marks. Dismissed marks stay dismissed when the analysers run again. |
| `ai.lab.marks` | `GET /api/v1/projects/{id}/ai/lab/marks` | project editor | read |  | Your Lab marks on a document, local and model-made together, each looked up in the text as it is now (placed, or stale). |
| `ai.lab.preview` | `POST /api/v1/projects/{id}/ai/lab/preview` | project editor | write |  | What accepting a chosen set of marks would do, worked out on the server from the text as it is now: which marks apply, which are skipped and why, and… |
| `ai.policy.get` | `GET /api/v1/projects/{id}/ai/policy` | project editor | read |  | The project's AI policy: mode (off by default), the actions allowed, providers and models, chains, redaction, whether agents may use AI, the owner's… |
| `ai.policy.set` | `PUT /api/v1/projects/{id}/ai/policy` | project owner | write | browser session only | Owner only, browser sessions only: an API key cannot change what leaves the server. |
| `ai.price.delete` | `DELETE /api/v1/ai/prices` | instance admin | admin | browser session only, permanent | Administrators, browser sessions only. Remove a model's price. |
| `ai.price.list` | `GET /api/v1/ai/prices` | instance writer | read |  | The model prices an administrator set. A model with no price anywhere has an unknown cost, which is never shown or counted as zero. |
| `ai.price.set` | `PUT /api/v1/ai/prices` | instance admin | admin | browser session only | Administrators, browser sessions only. Set a model's price in micro-units of the instance currency per million input and output tokens (1,000,000… |
| `ai.provider.create` | `POST /api/v1/ai/providers` | instance writer | write | browser session only, no dry run, 201 on success | Add a provider: OpenRouter, an OpenAI-compatible endpoint, Ollama or OpenCode. |
| `ai.provider.delete` | `DELETE /api/v1/ai/providers/{id}` | instance writer | write | browser session only, permanent | Remove a provider and its sealed key. Browser sessions only. |
| `ai.provider.get` | `GET /api/v1/ai/providers/{id}` | instance writer | read |  | One provider you can use. |
| `ai.provider.list` | `GET /api/v1/ai/providers` | instance writer | read |  | The providers you can use: the instance's and your own. Keys are never returned, only whether one is stored and its last four characters. |
| `ai.provider.model.remove` | `DELETE /api/v1/ai/providers/{id}/models` | instance writer | write | browser session only, permanent | Remove a model from a provider's list. Browser sessions only. |
| `ai.provider.model.set` | `PUT /api/v1/ai/providers/{id}/models` | instance writer | write | browser session only | Add or change a model by hand (for a provider with no model list): its id, and optionally its context window and longest answer. |
| `ai.provider.models` | `GET /api/v1/ai/providers/{id}/models` | instance writer | read |  | A provider's cached model list (context size and price where the provider gave them), with models entered by hand. |
| `ai.provider.models.refresh` | `POST /api/v1/ai/providers/{id}/models/refresh` | instance writer | write | browser session only, no dry run | Ask the provider for its model list and cache it. Models entered by hand are kept. |
| `ai.provider.test` | `POST /api/v1/ai/providers/{id}/test` | instance writer | write | browser session only, no dry run | Test a provider with a short request (its model list, or a tiny completion when it has none). |
| `ai.provider.update` | `PATCH /api/v1/ai/providers/{id}` | instance writer | write | browser session only, no dry run | Change a provider. Changing its address drops the stored key (a key belongs to one address): send api_key to store one for the new address. |
| `ai.recipe.list` | `GET /api/v1/ai/recipes` | instance writer | read |  | The recipes (actions) the server offers: id, what each does, the policy action it needs, the result kind, the parameters, and the context it uses by… |
| `ai.review.delete` | `DELETE /api/v1/projects/{id}/ai/review` | project editor | write | permanent | Delete one of your reviews and its notes. |
| `ai.review.get` | `GET /api/v1/projects/{id}/ai/review` | project editor | read |  | One of your reviews with its notes. Each note's quote is looked up in the text as it is now: placed (with its place), unplaced (never found), or… |
| `ai.review.list` | `GET /api/v1/projects/{id}/ai/reviews` | project editor | read |  | Your reviews on the project: the notes of critiques, the findings of continuity audits and the entities extractions proposed, newest first, with how… |
| `ai.review.note.dismiss` | `POST /api/v1/projects/{id}/ai/review/note/dismiss` | project editor | write |  | Dismiss a note: you disagree, or it does not apply. It changes nothing in the text. |
| `ai.review.note.resolve` | `POST /api/v1/projects/{id}/ai/review/note/resolve` | project editor | write |  | Mark a note as dealt with. It changes nothing in the text. |
| `ai.run` | `POST /api/v1/projects/{id}/ai/run` | project editor | write | no dry run | Run a previewed request. Send the same fields as ai.context.preview plus its payload_hash and nonce: if building the request now gives anything else… |
| `ai.run.cancel` | `POST /api/v1/projects/{id}/ai/run/cancel` | project editor | write |  | Cancel a running request of yours (or any on a project you own). |
| `ai.run.list` | `GET /api/v1/projects/{id}/ai/runs` | project editor | read |  | The project's AI run log, newest first: who, which recipe, provider and model, tokens, cost, status, and a hash of what was sent, with which chips by… |
| `ai.settings.get` | `GET /api/v1/ai/settings` | instance admin | admin |  | Administrators. The instance's AI settings: the on/off switch, the default provider chains per action, the currency. |
| `ai.settings.set` | `PUT /api/v1/ai/settings` | instance admin | admin | browser session only | Administrators, browser sessions only. Switch AI on or off for the whole server (an environment kill switch, SCRIOBH_AI=off, overrides it), set the… |
| `ai.status` | `GET /api/v1/ai/status` | any signed-in caller | read |  | Whether AI is available on this server and why not when it is not: the server's kill switch (SCRIOBH_AI=off), the administrator's setting, no secret… |
| `ai.style.get` | `GET /api/v1/projects/{id}/ai/style` | project editor | read |  | The project's style guide: the author's voice rules, sent only as the style guide chip of a request. |
| `ai.style.set` | `PUT /api/v1/projects/{id}/ai/style` | project owner | write | browser session only | Set the project's style guide (up to 8,000 characters), from a browser session only (it is configuration, like the policy). |
| `ai.summary.accept` | `POST /api/v1/projects/{id}/ai/summary/accept` | project editor | write |  | Write an accepted summary into the document's synopsis (short, long or both), through the document's own save: revision checked, no word credit. |
| `ai.summary.rolling.cancel` | `POST /api/v1/projects/{id}/ai/summary/rolling/cancel` | project editor | write |  | Cancel a rolling summary. Summaries already accepted stay. |
| `ai.summary.rolling.get` | `GET /api/v1/projects/{id}/ai/summary/rolling` | project editor | read |  | A rolling summary job and how far it has got. Without job_id, your open job on the project, to resume it. |
| `ai.summary.rolling.next` | `POST /api/v1/projects/{id}/ai/summary/rolling/next` | project editor | write |  | The next step of a rolling summary: the chapter and the request fields to preview and run (summary.chapter). |
| `ai.summary.rolling.skip` | `POST /api/v1/projects/{id}/ai/summary/rolling/skip` | project editor | write |  | Skip the current step of a rolling summary. |
| `ai.summary.rolling.start` | `POST /api/v1/projects/{id}/ai/summary/rolling` | project editor | write |  | Plan a rolling summary of the manuscript, chapter by chapter in reading order, each using the previous chapter's summary as context. |
| `ai.usage.get` | `GET /api/v1/ai/usage` | instance writer | read |  | Your own AI usage for a month, by day, with your caps. Works while AI is switched off: it sends nothing. |
| `ai.usage.project` | `GET /api/v1/projects/{id}/ai/usage` | project owner | read |  | Owner only. The project's AI usage for a month, by day and by person, with the caps that apply. |

#### `alternative` (9)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `alternative.add_variant` | `POST /api/v1/docs/{id}/alternatives/variants` | project editor | write | 201 on success | Add a wording to an alternative. |
| `alternative.choose` | `POST /api/v1/docs/{id}/alternatives/choose` | project editor | write |  | Cycle: write a wording into the text in place of the chosen one, in one step that records a history entry (reason alternative). |
| `alternative.create` | `POST /api/v1/docs/{id}/alternatives` | project editor | write | 201 on success | Make an alternative over a word, a sentence or a paragraph of the text: the text there becomes its original wording, kept chosen, and other wordings… |
| `alternative.delete` | `POST /api/v1/docs/{id}/alternatives/delete` | project editor | write |  | Resolve an alternative: keep the chosen wording in the text and drop the other wordings. |
| `alternative.edit_variant` | `PATCH /api/v1/docs/{id}/alternatives/variants` | project editor | write |  | Change the words of a wording (never the original). Editing the chosen wording rewrites the text of the document too. |
| `alternative.list` | `GET /api/v1/docs/{id}/alternatives` | project reader | read |  | List a document's alternatives: each anchored range with its wordings, which one is in the text, and the lost-anchors list (status orphaned). |
| `alternative.reanchor` | `POST /api/v1/docs/{id}/alternatives/reanchor` | project editor | write |  | Attach an alternative (usually an orphaned one) to a range of the text. |
| `alternative.remove_variant` | `POST /api/v1/docs/{id}/alternatives/variants/remove` | project editor | write |  | Remove a wording (never the original). Removing the chosen one puts the original back in the text. |
| `alternative.reorder_variant` | `POST /api/v1/docs/{id}/alternatives/variants/reorder` | project editor | write |  | Move a wording to another place in the cycle. |

#### `attachment` (1)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `attachment.usage` | `GET /api/v1/files/usage` | any signed-in caller | read |  | What you have uploaded in all, the largest file allowed, and (for administrators) the instance's total. |

#### `backup` (3)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `backup.create` | `POST /api/v1/admin/backups` | instance admin | admin | no dry run, 201 on success | Take a backup now: a consistent archive of the database, files, mirror and conflict copies, written to data/backups. |
| `backup.download` | `GET /api/v1/admin/backups/{id}` | instance admin | admin |  | Download one backup by the id backup.list gave. Never takes a path. |
| `backup.list` | `GET /api/v1/admin/backups` | instance admin | admin |  | List the backups in the data directory, newest first: manual, scheduled and pre-restore. |

#### `comment` (9)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `comment.create` | `POST /api/v1/projects/{id}/comments` | project reviewer | write | 201 on success | Write a comment: a new thread anchored to a range of a document (give document_id and a quote with the words around it, or offsets with the… |
| `comment.delete` | `DELETE /api/v1/projects/{id}/comments/{comment_id}` | project reviewer | write | permanent | Delete a comment: its author or the project's owner. The row stays as a tombstone with no text so a thread keeps its shape (a thread with no live… |
| `comment.get` | `GET /api/v1/projects/{id}/comments/{comment_id}` | project reviewer | read |  | One thread: the comment you name (or the thread a reply belongs to), with its replies. |
| `comment.list` | `GET /api/v1/projects/{id}/comments` | project reviewer | read |  | List comment threads (each root with its replies), newest last, for the whole project or one document (in document order, anchored ones first, then… |
| `comment.people` | `GET /api/v1/projects/{id}/comment-people` | project reviewer | read |  | The people on this project who can read comments (owner, editors, reviewers), with the display names to mention. |
| `comment.reanchor` | `POST /api/v1/projects/{id}/comments/{comment_id}/reanchor` | project reviewer | write |  | Give a thread a new place: for one that lost its text (orphaned), or to move it (to another range, another document of the project, or with detach… |
| `comment.reopen` | `POST /api/v1/projects/{id}/comments/{comment_id}/reopen` | project reviewer | write |  | Reopen a resolved thread. Same permission as resolving. |
| `comment.resolve` | `POST /api/v1/projects/{id}/comments/{comment_id}/resolve` | project reviewer | write |  | Resolve a thread. An editor or the owner may resolve any thread; a reviewer only their own. |
| `comment.update` | `PATCH /api/v1/projects/{id}/comments/{comment_id}` | project reviewer | write |  | Reword a comment. Only its author, and only within 15 minutes of writing it; after that, reply instead. |

#### `compendium` (47)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `compendium.archive` | `POST /api/v1/compendiums/{id}/archive` | compendium owner | write |  | Archive a Compendium: hidden from the default list, still fully readable and still used by its projects. |
| `compendium.attachment.delete` | `DELETE /api/v1/compendiums/{id}/attachments/{att_id}` | compendium editor | write | permanent | Delete an attachment and its file. Refused while documents or notes still link to it, unless force is set. |
| `compendium.attachment.download` | `GET /api/v1/compendiums/{id}/attachments/{att_id}/download` | compendium reader | read |  | Download an attachment's bytes (the CLI: --output FILE). |
| `compendium.attachment.get` | `GET /api/v1/compendiums/{id}/attachments/{att_id}` | compendium reader | read |  | Get one attachment's details (not its bytes: see download, or the url). |
| `compendium.attachment.list` | `GET /api/v1/compendiums/{id}/attachments` | compendium reader | read |  | List a Compendium's attachments, newest first. |
| `compendium.attachment.refs` | `GET /api/v1/compendiums/{id}/attachments/{att_id}/refs` | compendium reader | read |  | Where an attachment is used: the documents and notes whose Markdown links to it. |
| `compendium.attachment.update` | `PATCH /api/v1/compendiums/{id}/attachments/{att_id}` | compendium editor | write |  | Change an attachment's alt text or display name. |
| `compendium.attachment.upload` | `POST /api/v1/compendiums/{id}/attachments` | compendium editor | write | no dry run, body is the file, 201 on success | Upload a file to a Compendium. The request body is the file itself (the CLI: --file @path); the name and alt text are query parameters. |
| `compendium.attachment.usage` | `GET /api/v1/compendiums/{id}/attachment-usage` | compendium reader | read |  | Storage in use by a Compendium, by you, and its limits. |
| `compendium.change.get` | `GET /api/v1/compendiums/{id}/changes/{change_id}` | compendium owner | read | browser session only | One recorded merge or split with its before-state: every entity that moved (its name and revision as they were), every entity merged away with its… |
| `compendium.change.list` | `GET /api/v1/compendiums/{id}/changes` | compendium owner | read | browser session only | The merges and splits that moved entities into or out of this Compendium, newest first: when, by whom, the counts. |
| `compendium.create` | `POST /api/v1/compendiums` | instance writer | write | 201 on success | Create a Compendium: the shared bible (entities, and later research and a timeline) for one story world or subject. |
| `compendium.export` | `POST /api/v1/compendiums/{id}/export` | compendium editor | write |  | Export a Compendium as a Markdown bundle (a zip): one file per entity with front matter (name, kind, aliases, summary, custom fields, relationships)… |
| `compendium.get` | `GET /api/v1/compendiums/{id}` | compendium reader | read |  | Get one Compendium with what depends on it: the projects that use it (named only when the caller can open them), the Compendiums it references and… |
| `compendium.list` | `GET /api/v1/compendiums` | any signed-in caller | read |  | List the Compendiums the caller is a member of, most recently changed first. |
| `compendium.member.add` | `POST /api/v1/compendiums/{id}/members` | compendium owner | admin | no dry run, 201 on success | Give a person access to a Compendium, as an editor or a reader. |
| `compendium.member.list` | `GET /api/v1/compendiums/{id}/members` | compendium owner | read |  | List who can open a Compendium and their roles. Owner only. |
| `compendium.member.remove` | `DELETE /api/v1/compendiums/{id}/members` | compendium owner | admin |  | Take a person off a Compendium. Their project memberships are untouched, and so is anything they wrote. |
| `compendium.member.update` | `PATCH /api/v1/compendiums/{id}/members` | compendium owner | admin |  | Change a member's role (editor or reader). The owner's role cannot change. |
| `compendium.merge` | `POST /api/v1/compendiums/{id}/merge` | compendium owner | write | browser session only, permanent | Move this Compendium's entities (all of them, or entity_ids) into another, in one transaction, keeping their ids: aliases, mentions, links, overrides… |
| `compendium.merge.preview` | `POST /api/v1/compendiums/{id}/merge/preview` | compendium owner | read | browser session only | Preview merging this Compendium (the source) into another (into_id), or moving some of its entities there (entity_ids): which entities move, which… |
| `compendium.primary.preview` | `POST /api/v1/projects/{id}/compendium/primary/preview` | project owner | read | browser session only | Preview changing a project's primary Compendium: the entities that enter and leave the project, its overrides that become dormant, the mention index… |
| `compendium.primary.set` | `PUT /api/v1/projects/{id}/compendium/primary` | project owner | write | browser session only | Set or clear a project's primary Compendium (compendium_id empty clears it) with the preview's fingerprint. |
| `compendium.purge` | `DELETE /api/v1/compendiums/{id}` | compendium owner | write | permanent | Permanently delete a Compendium with its entities, aliases, relationships, field definitions, overrides, links, references and recorded exports. |
| `compendium.ref.add` | `POST /api/v1/compendiums/{id}/refs` | compendium owner | write |  | Reference another Compendium from this one, read-only (a shared cosmology, a common history, a house style). |
| `compendium.ref.remove` | `DELETE /api/v1/compendiums/{id}/refs` | compendium owner | write |  | Stop referencing another Compendium. Owner of this Compendium. |
| `compendium.research.board` | `GET /api/v1/compendiums/{id}/research-board` | compendium reader | read |  | The research board as data: columns by status or by tag, and the notes in each. |
| `compendium.research.create` | `POST /api/v1/compendiums/{id}/research` | compendium editor | write | 201 on success | Create a research note, a clipping or a folder in a Compendium. |
| `compendium.research.get` | `GET /api/v1/compendiums/{id}/research/{note_id}` | compendium reader | read |  | Get one research note with its body. |
| `compendium.research.list` | `GET /api/v1/compendiums/{id}/research` | compendium reader | read |  | List a Compendium's research notes, clippings and folders as a flat tree with reading order. |
| `compendium.research.move` | `POST /api/v1/compendiums/{id}/research/{note_id}/move` | compendium editor | write |  | Move a research note to another folder or position. |
| `compendium.research.restore` | `POST /api/v1/compendiums/{id}/research/{note_id}/restore` | compendium editor | write |  | Restore a trashed note and what was trashed with it. |
| `compendium.research.trash` | `POST /api/v1/compendiums/{id}/research/{note_id}/trash` | compendium editor | write |  | Move a note, and a folder's contents, to the trash. |
| `compendium.research.update` | `PATCH /api/v1/compendiums/{id}/research/{note_id}` | compendium editor | write |  | Save a research note. Send the revision you last saw; a stale revision is a 409. |
| `compendium.restore` | `POST /api/v1/compendiums/{id}/restore` | compendium owner | write |  | Take a Compendium out of the trash. Owner only; restoring a live Compendium does nothing. |
| `compendium.scratch.get` | `GET /api/v1/compendiums/{id}/scratchpad` | compendium reader | read | browser session only | Read your private scratchpad for this Compendium. It is yours alone: nobody else, an administrator included, can read it through Scríobh, and an API… |
| `compendium.scratch.set` | `PUT /api/v1/compendiums/{id}/scratchpad` | compendium reader | write | browser session only | Save your private scratchpad for this Compendium. Send the revision you last saw; a stale revision is a 409. |
| `compendium.split` | `POST /api/v1/compendiums/{id}/split` | compendium owner | write | browser session only, permanent, 201 on success | Move the chosen entities (entity_ids) of this Compendium into a new Compendium named name, owned by the caller, in one transaction, keeping their ids. |
| `compendium.split.preview` | `POST /api/v1/compendiums/{id}/split/preview` | compendium owner | read | browser session only | Preview moving the chosen entities (entity_ids) of this Compendium into a new one named name: the plan compendium.merge.preview gives for a merge… |
| `compendium.tag.create` | `POST /api/v1/compendiums/{id}/tags` | compendium editor | write | 201 on success | Create a tag. Tags are also created when a note is saved with a new name. |
| `compendium.tag.delete` | `DELETE /api/v1/compendiums/{id}/tags/{tag_id}` | compendium editor | write | permanent | Delete a tag. Notes lose it and are otherwise unchanged. |
| `compendium.tag.list` | `GET /api/v1/compendiums/{id}/tags` | compendium reader | read |  | List a Compendium's tags with how many research notes use each. |
| `compendium.tag.merge` | `POST /api/v1/compendiums/{id}/tags/{tag_id}/merge` | compendium editor | write | permanent | Merge a tag into another: its notes carry the other tag, and it is deleted. |
| `compendium.tag.update` | `PATCH /api/v1/compendiums/{id}/tags/{tag_id}` | compendium editor | write |  | Rename a tag or change its colour. Notes keep it. |
| `compendium.trash` | `POST /api/v1/compendiums/{id}/trash` | compendium owner | write |  | Move a Compendium to the trash. Owner only; nothing is deleted, but projects stop showing its entities until it is restored. |
| `compendium.unarchive` | `POST /api/v1/compendiums/{id}/unarchive` | compendium owner | write |  | Bring an archived Compendium back to the list. Owner only; does nothing when it is not archived. |
| `compendium.update` | `PATCH /api/v1/compendiums/{id}` | compendium owner | write |  | Change a Compendium's name, kind or description. Send only the fields to change. |

#### `compile` (8)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `compile.contact.get` | `GET /api/v1/projects/{id}/compile/contact` | project editor | read |  | The contact block a manuscript title page shows (name, surname for the running header, address, email, phone). |
| `compile.contact.set` | `PATCH /api/v1/projects/{id}/compile/contact` | project editor | write |  | Set the manuscript contact block. Send only the fields to change; an empty string clears one. |
| `compile.cover_spec` | `POST /api/v1/projects/{id}/compile/cover-spec` | project editor | read |  | The measurements of a paperback cover for a print preset: the page count (exact, from the same layout the PDF export runs, or one you give), the… |
| `compile.preset.create` | `POST /api/v1/projects/{id}/compile/presets` | project editor | write | 201 on success | Save a custom compile preset: a name, the built-in preset it starts from, and the options that differ. |
| `compile.preset.delete` | `DELETE /api/v1/projects/{id}/compile/presets/{preset_id}` | project editor | write | permanent | Delete a custom preset. Built-in presets cannot be deleted. Exports already made keep their record. |
| `compile.preset.list` | `GET /api/v1/projects/{id}/compile/presets` | project editor | read |  | List the compile presets: the built-in ones (US Standard Manuscript, UK manuscript, plain Markdown, styled DOCX, EPUB 3, the print and proof PDFs and… |
| `compile.preset.update` | `PATCH /api/v1/projects/{id}/compile/presets/{preset_id}` | project editor | write |  | Rename a custom preset, or change some of its options. Send only what changes. |
| `compile.preview` | `POST /api/v1/projects/{id}/compile/preview` | project editor | read |  | Preview a compile without writing a file: the title page, the running header, the chapters with their words and estimated pages, the first pages as… |

#### `desktop` (7)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `desktop.idp.cancel` | `DELETE /api/v1/desktop/idp/switch` | instance admin | admin | browser session only, 404 to others | Desktop mode only. Cancel a switch of identity provider that is waiting for a restart. |
| `desktop.idp.get` | `GET /api/v1/desktop/idp` | instance admin | admin | browser session only, 404 to others | Desktop mode only. Which identity provider this instance signs in through: the embedded issuer (the local profile) or an external one, the external… |
| `desktop.idp.set` | `PUT /api/v1/desktop/idp` | instance admin | admin | browser session only, 404 to others, no dry run | Desktop mode only, while the embedded issuer is in use. Store the external provider to switch to: issuer, client ID, client secret (sealed, never… |
| `desktop.idp.switch` | `POST /api/v1/desktop/idp/switch` | instance admin | admin | browser session only, 404 to others | Desktop mode only. Ask for the switch of identity provider; it is made when the application next starts. |
| `desktop.idp.verify` | `POST /api/v1/desktop/idp/verify` | instance admin | admin | browser session only, 404 to others, no dry run | Desktop mode only. Record that you signed in at the external provider with the stored settings. |
| `desktop.info` | `GET /api/v1/desktop/info` | instance admin | admin | browser session only, 404 to others, no dry run | Desktop mode only (SCRIOBH_DESKTOP=1; not found otherwise). Where this instance keeps its data, its version, how it signs people in, and how many… |
| `desktop.localmodel.detect` | `POST /api/v1/desktop/local-model/detect` | instance admin | admin | browser session only, 404 to others, no dry run | Desktop mode only. Look for an Ollama model server on this computer: one request to 127.0.0.1:11434 (the address is fixed; nothing you send chooses… |

#### `doc` (13)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `doc.backlinks` | `GET /api/v1/docs/{id}/backlinks` | project reader | read |  | List the documents that link to this one, in reading order, with a short context snippet and breadcrumb. |
| `doc.conflict` | `POST /api/v1/docs/{id}/conflict` | project editor | write | 201 on success | Keep a copy of local text that lost a save conflict, in the data directory. |
| `doc.create` | `POST /api/v1/projects/{id}/docs` | project editor | write | 201 on success | Create a document (part, chapter, scene or note) in a project. |
| `doc.get` | `GET /api/v1/docs/{id}` | project reader | read |  | Get one document with its body, neighbours and breadcrumb. |
| `doc.lease` | `POST /api/v1/docs/{id}/lease` | project editor | write | no dry run | Ask for the single-writer lease on a document for one browser tab. |
| `doc.links` | `GET /api/v1/docs/{id}/links` | project reader | read |  | List the wiki links in a document, each with whether it resolves to a document and where. |
| `doc.list` | `GET /api/v1/projects/{id}/docs` | project reader | read |  | List a project's documents as a flat tree, with reading order. |
| `doc.move` | `POST /api/v1/docs/{id}/move` | project editor | write |  | Move a document to another folder or position. |
| `doc.resolve_link` | `POST /api/v1/projects/{id}/links/resolve` | project editor | write |  | Find the document a wiki link title points at. With create, a missing target is created as a document (default a note, placed at the end of the… |
| `doc.restore` | `POST /api/v1/docs/{id}/restore` | project editor | write |  | Restore a trashed document and what was trashed with it. |
| `doc.split` | `POST /api/v1/docs/{id}/split` | project editor | write | 201 on success | Cut a document in two at a text position. The text after the cut becomes the next document at the same level. |
| `doc.trash` | `POST /api/v1/docs/{id}/trash` | project editor | write |  | Move a document and everything inside it to the trash. |
| `doc.update` | `PATCH /api/v1/docs/{id}` | project editor | write |  | Save a document. Send the revision you last saw; a stale revision is a 409. |

#### `edition` (7)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `edition.compare` | `GET /api/v1/projects/{id}/editions/{edition_id}/compare` | project editor | write |  | Compare two editions of a book ("what changed in the second edition"): two editions of this project, or this project's edition with one of another… |
| `edition.diff` | `GET /api/v1/projects/{id}/editions/{edition_id}/diff` | project reader | read |  | Compare the project as it stands with one of its editions, chapter by chapter, so a reviewer can step through what changed in a revised book. |
| `edition.download` | `GET /api/v1/projects/{id}/editions/{edition_id}/download` | project editor | write |  | Download the source file of an edition, byte for byte as it was imported (checked against its recorded SHA-256). |
| `edition.get` | `GET /api/v1/projects/{id}/editions/{edition_id}` | project reader | read |  | One edition with its chapters: where each came from in the book (source_path), the document it became, and its size. |
| `edition.list` | `GET /api/v1/projects/{id}/editions` | project reader | read |  | The editions of a project: the books it was imported from, with each one's label, identifier, source file hash and size, and when it was imported. |
| `edition.suggest` | `POST /api/v1/projects/{id}/editions/{edition_id}/suggest` | project editor | write | 201 on success | Turn differences between a chapter and its edition into suggestions on the document, one suggestion for each difference, to be accepted or rejected… |
| `edition.take` | `POST /api/v1/projects/{id}/editions/{edition_id}/take` | project editor | write |  | Take the edition's text for a whole chapter ("keep mine" is not calling this): the document's text becomes what the edition had, as a new version of… |

#### `entity` (18)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `entity.alias.add` | `POST /api/v1/entities/{id}/aliases` | compendium editor | write | 201 on success | Add an alias (a nickname, a title, a former name) to an entity. |
| `entity.alias.remove` | `DELETE /api/v1/entities/{id}/aliases` | compendium editor | write | permanent | Remove one alias from an entity, by the alias id entity.get lists. |
| `entity.appears_in` | `GET /api/v1/entities/{id}/appears-in` | compendium reader | read |  | Which books mention this entity: for each project the caller can open, how many documents and mentions, and the first appearance. |
| `entity.create` | `POST /api/v1/compendiums/{id}/entities` | compendium editor | write | 201 on success | Create an entity (character, place, item, org or concept) in a Compendium, with a summary, a Markdown body, typed custom fields and aliases. |
| `entity.field.list` | `GET /api/v1/compendiums/{id}/entity-fields` | compendium reader | read |  | List a Compendium's custom entity fields, by kind: key, label, type (text, number, date or choice) and the choices of a choice field. |
| `entity.field.remove` | `DELETE /api/v1/compendiums/{id}/entity-fields` | compendium editor | write | permanent | Remove a custom field definition. Refused while entities or project overrides hold a value for it, unless drop_values is set, which deletes those… |
| `entity.field.set` | `PUT /api/v1/compendiums/{id}/entity-fields` | compendium editor | write |  | Define a custom field for one kind of entity, or change an existing one's label and choices (its type is fixed once defined). |
| `entity.first_appearance` | `GET /api/v1/projects/{id}/first-appearance` | project reader | read |  | The first document, in reading order, that mentions an entity, per project (a Compendium is shared, so first appearance is a fact about one book). |
| `entity.get` | `GET /api/v1/entities/{id}` | compendium reader | read |  | Get one entity sheet with its aliases, custom fields and relationships. |
| `entity.list` | `GET /api/v1/compendiums/{id}/entities` | compendium reader | read |  | List a Compendium's entities in sort order, a page at a time, without bodies. |
| `entity.merge` | `POST /api/v1/entities/{id}/merge` | compendium editor | write | permanent | Merge this entity (the source) into another of the same kind in the same Compendium (into_id). |
| `entity.referenced.list` | `GET /api/v1/compendiums/{id}/referenced-entities` | compendium reader | read |  | The entities of the Compendiums this one references, read-only, in sort order, a page at a time. |
| `entity.relation.add` | `POST /api/v1/entities/{id}/relations` | compendium editor | write | 201 on success | Relate this entity to another in the same Compendium with a typed edge (family, ally, rival, mentor, romantic, member or custom) and a label that… |
| `entity.relation.list` | `GET /api/v1/entities/{id}/relations` | compendium reader | read |  | List an entity's relationships with live entities, as they read from this entity: direction out, in or both, and the label for that side. |
| `entity.relation.remove` | `DELETE /api/v1/entities/{id}/relations` | compendium editor | write | permanent | Remove a relationship that touches this entity, by the relation id entity.relation.list gives. |
| `entity.restore` | `POST /api/v1/entities/{id}/restore` | compendium editor | write |  | Take an entity out of the trash. Refused with a 409 naming the other entity if its name or an alias has been used since. |
| `entity.trash` | `POST /api/v1/entities/{id}/trash` | compendium editor | write |  | Move an entity to the trash: it leaves lists, search, projects and the mirror, and its name and aliases are free to reuse. |
| `entity.update` | `PATCH /api/v1/entities/{id}` | compendium editor | write |  | Change an entity's name, sort name, summary, body, colour or custom field values. |

#### `export` (5)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `export.compile` | `POST /api/v1/projects/{id}/export/compile` | project editor | write |  | Compile the manuscript with a preset into a DOCX (US Standard Manuscript, UK manuscript, styled), an EPUB 3 (also, in a custom preset's format… |
| `export.cover` | `POST /api/v1/projects/{id}/export/cover` | project editor | write |  | A print cover as a PDF of two pages: the full wrap (back, spine and front with bleed; a flat colour from the project's cover colour with white type… |
| `export.html` | `POST /api/v1/projects/{id}/export/html` | project editor | write |  | Export the manuscript as HTML to read or share: one self-contained file, or a zip of pages with an index. |
| `export.markdown` | `POST /api/v1/projects/{id}/export` | project editor | write |  | Export the manuscript as Markdown: one file, or a zip that mirrors the tree. |
| `export.submission` | `POST /api/v1/projects/{id}/export/submission` | project editor | write |  | A submission bundle: the manuscript as a DOCX (a manuscript preset), a synopsis, a cover letter template and a manifest, in one zip. |

#### `ghost` (3)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `ghost.create` | `POST /api/v1/docs/{id}/ghosts` | project editor | write | 201 on success | Ghost ranges of the text: they stay in the document and in exports, and are left out of the word count, search and mentions. |
| `ghost.list` | `GET /api/v1/docs/{id}/ghosts` | project reader | read |  | List a document's ghosted ranges, with the word count, the words ghosted and the raw count with them. |
| `ghost.revive` | `POST /api/v1/docs/{id}/ghosts/revive` | project editor | write |  | Revive ghosted text: remove the ghost tags of the named ghosts, of the ghosts overlapping a range, or of all of them. |

#### `git` (22)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `git.presets` | `GET /api/v1/git/presets` | any signed-in caller | read |  | Presets that prefill the Git remote form: the address shape, default branch, where the deploy key is added, and where the host publishes its host key… |
| `git.push` | `POST /api/v1/projects/{id}/git/push` | project editor | write | no dry run, 202 on success | Push now. Queues a push of the project's Markdown mirror to the remote and returns at once with the running push; read git.push_get or git.remote.get… |
| `git.push_cancel` | `POST /api/v1/projects/{id}/git/push/cancel` | project editor | write | no dry run | Cancel the remote's queued or running push. |
| `git.push_force` | `POST /api/v1/projects/{id}/git/push/force` | project owner | write | no dry run, 202 on success | Force push: replace the remote branch's history with this project's. |
| `git.push_get` | `GET /api/v1/projects/{id}/git/push` | project editor | read |  | One push in full, with the manifest of the exact tree that was sent (path to SHA-256). |
| `git.push_history` | `GET /api/v1/projects/{id}/git/pushes` | project editor | read |  | Pushes of a remote, newest first: status, commit, files changed, what changed, and for a refused push the remote's commits that are not ours. |
| `git.remote.create` | `POST /api/v1/projects/{id}/git/remotes` | project owner | write | no dry run, 201 on success | Owner only. Add a push-only Git remote to a project (up to five per project; a private remote at an address that was ever public needs confirm_name… |
| `git.remote.delete` | `DELETE /api/v1/projects/{id}/git/remote` | project owner | write | no dry run, permanent | Owner only. Remove a remote, its stored credential and its local repository. |
| `git.remote.get` | `GET /api/v1/projects/{id}/git/remote` | project editor | read |  | One Git remote with its status. remote_id is optional while the project has one remote. |
| `git.remote.hostkey` | `POST /api/v1/projects/{id}/git/remote/hostkey` | project editor | write | no dry run | Scan an ssh remote's host keys and return their fingerprints, so a person can compare them with the host's published ones before accepting. |
| `git.remote.hostkey_accept` | `POST /api/v1/projects/{id}/git/remote/hostkey/accept` | project owner | write | no dry run | Owner only. Accept (pin) one scanned host key by its fingerprint. |
| `git.remote.keygen` | `POST /api/v1/projects/{id}/git/remote/keygen` | project owner | write | no dry run | Owner only. Generate an ed25519 deploy key for an ssh remote. |
| `git.remote.list` | `GET /api/v1/projects/{id}/git/remotes` | project editor | read |  | A project's push-only Git remotes, with status: state, last push, what a retry waits for, what is still to set up. |
| `git.remote.secret_ack` | `POST /api/v1/projects/{id}/git/remote/secret-ack` | project owner | write | no dry run | Owner only. Acknowledge that a file the pre-push check refused is not a secret (a key quoted in a story, an example). |
| `git.remote.test` | `POST /api/v1/projects/{id}/git/remote/test` | project editor | write | no dry run | Test the connection: a read-only ls-remote with the stored credentials and the accepted host key. |
| `git.remote.update` | `PATCH /api/v1/projects/{id}/git/remote` | project owner | write | no dry run | Owner only. Change a remote: name, address, branch, folder, push mode, whether notes are pushed, the commit identity, a new token, or switch it off. |
| `git.signing.generate` | `POST /api/v1/projects/{id}/git/signing` | project owner | write | no dry run | Owner only. Generate the project's ed25519 signing key (the private half is stored encrypted and never returned), or replace it with replace=true. |
| `git.signing.get` | `GET /api/v1/projects/{id}/git/signing` | project owner | read |  | The project's commit-signing key: its public half, fingerprint and an allowed-signers line. |
| `git.trigger.delete` | `DELETE /api/v1/projects/{id}/git/trigger` | project owner | write | no dry run, permanent | Owner only. Remove a remote's trigger and its waiting deliveries. |
| `git.trigger.get` | `GET /api/v1/projects/{id}/git/trigger` | project owner | read |  | A remote's push trigger: where it calls (scheme, host and port), whether it is on, the last delivery's short status. |
| `git.trigger.set` | `PUT /api/v1/projects/{id}/git/trigger` | project owner | write | no dry run | Owner only. Set a remote's trigger: after every successful push one signed POST goes to the address, with the repository, the branch, the commit id… |
| `git.trigger.test` | `POST /api/v1/projects/{id}/git/trigger/test` | project owner | write | no dry run | Owner only. Send one test delivery (event git.ping, no commit) to the trigger's address, signed as a real one, and say how it went in one short line. |

#### `goal` (3)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `goal.delete` | `DELETE /api/v1/projects/{id}/goals` | project editor | write | permanent | Remove one of the caller's own goals (kind=daily or kind=project). |
| `goal.list` | `GET /api/v1/projects/{id}/goals` | project reader | read |  | List the caller's own goals on a project, with the project's shared word goal and deadline. |
| `goal.set` | `PUT /api/v1/projects/{id}/goals` | project editor | write |  | Set one of the caller's own goals on a project: a daily words-added target, or a total-word target with an optional deadline. |

#### `import` (9)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `import.append` | `POST /api/v1/projects/{id}/import` | project editor | write | 201 on success | Add a previewed folder, document, book or export to an existing project as new documents after what is already there (and research notes after the… |
| `import.append_preview` | `POST /api/v1/projects/{id}/import/preview` | project editor | write |  | Preview adding a folder of Markdown, a Word document, an Obsidian vault, a Scrivener project, a WordPress export, a Ghost export or an EPUB book to… |
| `import.apply` | `POST /api/v1/imports` | instance writer | write | 201 on success | Create a new project from a previewed folder, document, book or export, with the writer's edits, in one transaction: all of it or none (documents… |
| `import.git_apply` | `POST /api/v1/imports/git` | instance writer | write | no dry run, 201 on success | Import a Git repository as a new project, final step: create the project from a ready job in one transaction, record the address, the commit and the… |
| `import.git_discard` | `DELETE /api/v1/imports/git` | instance writer | write | no dry run, permanent | Throw away an import job and its clone. A job that is still cloning cannot be discarded yet. |
| `import.git_job` | `GET /api/v1/imports/git` | instance writer | read |  | One repository import job: running (cloning), preview (ready), done or failed, with a summary when ready. |
| `import.git_jobs` | `GET /api/v1/imports/git/jobs` | instance writer | read |  | Your repository import jobs, newest first. Jobs that were never applied are removed after a day. |
| `import.git_preview` | `POST /api/v1/imports/git/preview` | instance writer | write | no dry run | Import a Git repository as a new project, step one. With url (https) or remote_id (a remote of a project you own) it starts a job: a shallow clone… |
| `import.preview` | `POST /api/v1/imports/preview` | instance writer | write |  | Preview importing a folder of Markdown (or a Scríobh mirror or export), a Word document, an Obsidian vault, a Scrivener project, a WordPress export… |

#### `key` (3)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `key.create` | `POST /api/v1/keys` | instance writer | admin | no dry run, 201 on success | Create an API key for yourself, with a scope and an optional restriction to one project (and that project's primary compendium) or to one compendium. |
| `key.list` | `GET /api/v1/keys` | instance writer | admin |  | List your API keys, newest first, with their scope, restriction, last use and expiry. |
| `key.revoke` | `POST /api/v1/keys/{id}/revoke` | instance writer | admin |  | Revoke one of your API keys now, and every key it created. Revoking twice is fine. |

#### `kindle` (6)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `kindle.address.delete` | `DELETE /api/v1/me/kindle` | any signed-in caller | write | browser session only | Delete your stored Send-to-Kindle address and any delivery still waiting for it. |
| `kindle.address.get` | `GET /api/v1/me/kindle` | any signed-in caller | read | browser session only | Your stored Send-to-Kindle address, whether mail is available, the sender address to approve at Amazon, and what to do there. |
| `kindle.address.set` | `PUT /api/v1/me/kindle` | any signed-in caller | write | browser session only | Store your own Send-to-Kindle address (one plain address). Deliveries only ever go to it. |
| `kindle.log` | `GET /api/v1/projects/{id}/kindle/log` | project editor | read | browser session only | Who sent which edition of this project to a Kindle, when, its hash and size, the recipient's domain and how it went. |
| `kindle.preview` | `POST /api/v1/projects/{id}/kindle/preview` | project editor | write | browser session only, no dry run | Say exactly what Send to Kindle would send: the EPUB's file name and size, your stored address as the only recipient, the sender to approve at… |
| `kindle.send` | `POST /api/v1/projects/{id}/kindle/send` | project editor | write | browser session only, no dry run | Mail the previewed EPUB edition of the project to your own stored Send-to-Kindle address. |

#### `mail` (8)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `mail.config.get` | `GET /api/v1/admin/mail` | instance admin | admin | browser session only, 404 to others | The mail relay's settings: host, port, security, the user name, whether a password is stored (never the password), the sender, the size limit… |
| `mail.config.set` | `PUT /api/v1/admin/mail` | instance admin | admin | browser session only, 404 to others, no dry run | Set the mail relay and switch mail on or off. The password is sealed and never returned; changing the relay's address or user name drops it unless a… |
| `mail.config.test` | `POST /api/v1/admin/mail/test` | instance admin | admin | browser session only, 404 to others, no dry run | Send a test message through the stored relay to the administrator's own account address, and say what happened as a class and a sentence (never what… |
| `mail.digest.get` | `GET /api/v1/me/mail/digest` | any signed-in caller | read | browser session only | Your email digest setting, the address it would go to (your account address) and what it says. |
| `mail.digest.set` | `PUT /api/v1/me/mail/digest` | any signed-in caller | write | browser session only | Turn your email digest on (daily or weekly) or off. It goes to your account address only, says only counts and project titles and a link to this… |
| `mail.outbox.status` | `GET /api/v1/admin/mail/outbox` | instance admin | admin | browser session only, 404 to others | How many messages wait, were sent or failed, and the newest 30 with their kind, state, attempts, failure class and the recipient's domain. |
| `mail.progress.get` | `GET /api/v1/me/mail/progress` | any signed-in caller | read | browser session only | Your progress email setting: off by default, the address it would go to (your account address) and what it says. |
| `mail.progress.set` | `PUT /api/v1/me/mail/progress` | any signed-in caller | write | browser session only | Turn your weekly or monthly progress email on or off. It goes to your account address only and says only numbers: the words you added, your writing… |

#### `mention` (3)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `mention.by_entity` | `GET /api/v1/projects/{id}/mentions` | project reader | read |  | Where an entity appears in this book: the documents that mention it, in reading order, with the count in each and a snippet around the first. |
| `mention.list` | `GET /api/v1/docs/{id}/mentions` | project reader | read |  | The entities a document mentions, with how many times each occurs (as a name, as an alias, as an explicit [[link]]), a snippet, and with spans=true… |
| `mention.rebuild` | `POST /api/v1/projects/{id}/mentions/rebuild` | project owner | write |  | Rebuild this project's mention index from its text. The index is derived and always safe to rebuild; it is kept current by every save and by the… |

#### `mirror` (4)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `mirror.compendium.rebuild` | `POST /api/v1/compendiums/{id}/mirror/rebuild` | compendium editor | write | no dry run | Rewrite a standalone Compendium's Markdown mirror now, comparing every file with what is on disk and repairing any that differ. |
| `mirror.compendium.status` | `GET /api/v1/compendiums/{id}/mirror` | compendium reader | read |  | How a Compendium's own Markdown mirror stands (see mirror.status). |
| `mirror.rebuild` | `POST /api/v1/projects/{id}/mirror/rebuild` | project editor | write | no dry run | Rewrite the project's Markdown mirror now, comparing every file with what is on disk and repairing any that differ. |
| `mirror.status` | `GET /api/v1/projects/{id}/mirror` | project reader | read |  | How the project's Markdown mirror stands: whether a write is pending, when a file last changed, the last error, the file count, and a hash of the… |

#### `notification` (4)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `notification.count` | `GET /api/v1/notifications/count` | any signed-in caller | read | browser session only | How many notifications you have not read, in all and per project: the badge. |
| `notification.list` | `GET /api/v1/notifications` | any signed-in caller | read | browser session only | Your notifications, newest first: a mention, a reply to your comment, a thread of yours resolved, a comment on a project you own, a suggestion… |
| `notification.mark_read` | `POST /api/v1/notifications/read` | any signed-in caller | write | browser session only | Mark notifications read: the ids you send (ones that are not yours are ignored). |
| `notification.read_all` | `POST /api/v1/notifications/read-all` | any signed-in caller | write | browser session only | Mark all your notifications read, or only those of one project. |

#### `outline` (1)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `outline.get` | `GET /api/v1/projects/{id}/outline` | project reader | read |  | The tree flattened in reading order for an outline: synopses, status, targets, word counts, scene metadata, and roll-up totals (words, target… |

#### `pandoc` (3)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `pandoc.config.get` | `GET /api/v1/admin/pandoc` | instance admin | admin | browser session only | Administrators, browser sessions only. The Pandoc bridge: whether it is switched on, the path set (or found on PATH), the version, whether the binary… |
| `pandoc.config.set` | `PUT /api/v1/admin/pandoc` | instance admin | admin | browser session only, no dry run | Administrators, browser sessions only. Switch the Pandoc bridge on or off and set the absolute path of the binary (empty: find pandoc on PATH) and… |
| `pandoc.config.test` | `POST /api/v1/admin/pandoc/test` | instance admin | admin | browser session only, no dry run | Administrators, browser sessions only. Run a tiny conversion in each direction with exactly the arguments, environment and limits a real one gets… |

#### `prefs` (2)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `prefs.get` | `GET /api/v1/me/prefs` | any signed-in caller | read |  | Get the caller's preferences (theme, editor font, size, measure and line height, motion, autosave, density) with defaults filled in. |
| `prefs.set` | `PUT /api/v1/me/prefs` | any signed-in caller | write |  | Set one or more preferences; keys not sent are left as they are. |

#### `presence` (3)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `presence.beat` | `POST /api/v1/projects/{id}/presence` | project reader | write | browser session only, no dry run | Say that you have this project open (and optionally which document) and learn who else is here. |
| `presence.leave` | `POST /api/v1/projects/{id}/presence/leave` | project reader | write | browser session only, no dry run | Say that you have closed the project's page: you are no longer shown. |
| `presence.list` | `GET /api/v1/projects/{id}/presence` | project reader | read | browser session only | Who else is on this project right now, as you may be told: the same answer as presence.beat without saying you are here. |

#### `profile` (2)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `profile.get` | `GET /api/v1/me` | any signed-in caller | read |  | Get the caller's profile: name, email, instance role, time zone, locale, avatar colour and initials. |
| `profile.update` | `PATCH /api/v1/me` | any signed-in caller | write |  | Change the caller's display name, time zone or locale. Send only the fields to change. |

#### `project` (64)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `project.archive` | `POST /api/v1/projects/{id}/archive` | project owner | write |  | Archive a project: hidden from the default library, still fully readable. |
| `project.attachment.delete` | `DELETE /api/v1/projects/{id}/attachments/{att_id}` | project editor | write | permanent | Delete an attachment and its file. Refused while documents or notes still link to it, unless force is set. |
| `project.attachment.download` | `GET /api/v1/projects/{id}/attachments/{att_id}/download` | project reader | read |  | Download an attachment's bytes (the CLI: --output FILE). |
| `project.attachment.get` | `GET /api/v1/projects/{id}/attachments/{att_id}` | project reader | read |  | Get one attachment's details (not its bytes: see download, or the url). |
| `project.attachment.list` | `GET /api/v1/projects/{id}/attachments` | project reader | read |  | List a project's attachments, newest first. |
| `project.attachment.refs` | `GET /api/v1/projects/{id}/attachments/{att_id}/refs` | project reader | read |  | Where an attachment is used: the documents and notes whose Markdown links to it. |
| `project.attachment.update` | `PATCH /api/v1/projects/{id}/attachments/{att_id}` | project editor | write |  | Change an attachment's alt text or display name. |
| `project.attachment.upload` | `POST /api/v1/projects/{id}/attachments` | project editor | write | no dry run, body is the file, 201 on success | Upload a file to a project. The request body is the file itself (the CLI: --file @path); the name and alt text are query parameters. |
| `project.attachment.usage` | `GET /api/v1/projects/{id}/attachment-usage` | project reader | read |  | Storage in use by a project, by you, and its limits. |
| `project.compendium.create` | `POST /api/v1/projects/{id}/compendium` | project owner | write | 201 on success | Create a Compendium for a project and make it the project's primary, in one step. |
| `project.compendium.get` | `GET /api/v1/projects/{id}/compendium` | project reader | read |  | A project's Compendium wiring: its primary Compendium and the whole Compendiums it references. |
| `project.compendium.ref.add` | `POST /api/v1/projects/{id}/compendium/refs` | project owner | write |  | Reference a whole Compendium from a project, read-only: its entities appear in the project's resolved entities and in the mirror's compendium/… |
| `project.compendium.ref.remove` | `DELETE /api/v1/projects/{id}/compendium/refs` | project owner | write |  | Stop referencing a Compendium from a project. Project owner. |
| `project.compendium.set` | `PUT /api/v1/projects/{id}/compendium` | project owner | write |  | Set or clear a project's primary Compendium (compendium_id empty clears it). |
| `project.cover` | `GET /api/v1/projects/{id}/cover` | project reader | read |  | The project's cover as an image. With a cover layout of its own (project.cover_layout.get) it is the front as a JPEG, at most 2560 px on the long… |
| `project.cover_layout.check` | `POST /api/v1/projects/{id}/cover-layout/check` | project reader | read |  | Check the layout against a print trim and a printer's profile (kdp, ingram or generic): each picture's resolution, a wrap's measured spine against… |
| `project.cover_layout.clear` | `DELETE /api/v1/projects/{id}/cover-layout` | project editor | write |  | Go back to the generated cover. The pictures stay as attachments until you delete them. |
| `project.cover_layout.get` | `GET /api/v1/projects/{id}/cover-layout` | project reader | read |  | The project's own cover layout: its shape (sides or wrap), trim, front and back, alt text, revision, each picture's resolution at the trim, the… |
| `project.cover_layout.set` | `PUT /api/v1/projects/{id}/cover-layout` | project editor | write |  | Set the project's cover layout: the whole layout at once, with the revision you read (0 when there is none yet; a stale revision is a conflict). |
| `project.cover_layout.upload` | `POST /api/v1/projects/{id}/cover-layout/images` | project editor | write | no dry run, body is the file, 201 on success | Upload a cover picture. The request body is the picture itself (the CLI: --file @path); part says which part of the cover it is for: front, back or… |
| `project.create` | `POST /api/v1/projects` | instance writer | write | 201 on success | Create a project. The caller becomes its owner. |
| `project.entity.get` | `GET /api/v1/projects/{id}/entity` | project reader | read |  | One entity as the project sees it: the effective name, summary and fields, the Compendium's body, the override (with the Compendium's own values in… |
| `project.entity.link.add` | `POST /api/v1/projects/{id}/entity-links` | project editor | write |  | Link one entity from a Compendium into this project, read-only (a cameo): it appears among the project's entities and in the mirror's compendium/… |
| `project.entity.link.list` | `GET /api/v1/projects/{id}/entity-links` | project reader | read |  | List the single entities this project links from Compendiums it does not otherwise use (cameos). |
| `project.entity.link.remove` | `DELETE /api/v1/projects/{id}/entity-links` | project editor | write |  | Remove a linked entity from the project. Its override, if any, is kept, dormant. |
| `project.entity.list` | `GET /api/v1/projects/{id}/entities` | project reader | read |  | The entities a project shows, as the project sees them: its primary Compendium's, those of Compendiums it references, and single linked entities… |
| `project.entity.override.clear` | `DELETE /api/v1/projects/{id}/entity-overrides` | project editor | write | permanent | Remove this book's override of an entity, so it shows the Compendium's record again. |
| `project.entity.override.set` | `PUT /api/v1/projects/{id}/entity-overrides` | project editor | write |  | Override a shared entity for this book only: a different name or summary, different values for custom fields, private notes. |
| `project.get` | `GET /api/v1/projects/{id}` | project reader | read |  | Get one project the caller can open, with word and document counts and the caller's pin and last-opened state. |
| `project.list` | `GET /api/v1/projects` | any signed-in caller | read |  | List the projects the caller can open: pinned first, then most recently opened by the caller, then most recently edited. |
| `project.lock` | `POST /api/v1/projects/{id}/lock` | project reader | write | browser session only, no dry run | Lock the project again in this browser session, now. Any member, browser session only. |
| `project.member.add` | `POST /api/v1/projects/{id}/members` | project owner | admin | no dry run, 201 on success | Add a person to a project as an editor, a reviewer or a reader. |
| `project.member.list` | `GET /api/v1/projects/{id}/members` | project owner | read |  | List who is on a project and their roles. Owner only. When the project has a primary Compendium and the caller is a member of it, each person's role… |
| `project.member.remove` | `DELETE /api/v1/projects/{id}/members` | project owner | admin |  | Take a person off a project. Their Compendium access is separate and stays: the result says whether they remain on the project's primary Compendium… |
| `project.member.update` | `PATCH /api/v1/projects/{id}/members` | project owner | admin |  | Change a member's role on the project (editor, reviewer or reader). |
| `project.open` | `POST /api/v1/projects/{id}/open` | project reader | write |  | Record that the caller opened the project, and optionally which document they are in, so the library can offer Continue where I left off. |
| `project.passphrase.clear` | `DELETE /api/v1/projects/{id}/passphrase` | project owner | write | browser session only, no dry run | Remove the project's passphrase. Project owner, browser session only; current must be given. |
| `project.passphrase.get` | `GET /api/v1/projects/{id}/passphrase` | project reader | read |  | Whether a project has a passphrase and whether this session has unlocked it. |
| `project.passphrase.set` | `PUT /api/v1/projects/{id}/passphrase` | project owner | write | browser session only, no dry run | Set, change or adjust the project's passphrase. Project owner, browser session only (a key cannot). |
| `project.pin` | `POST /api/v1/projects/{id}/pin` | project reader | write |  | Pin or unpin a project on the caller's own library shelf. Per person: other members are not affected. |
| `project.purge` | `DELETE /api/v1/projects/{id}` | project owner | write | permanent | Permanently delete a project and everything in it. This is the only hard delete, and it cannot be undone. |
| `project.research.board` | `GET /api/v1/projects/{id}/research-board` | project reader | read |  | The research board as data: columns by status or by tag, and the notes in each. |
| `project.research.create` | `POST /api/v1/projects/{id}/research` | project editor | write | 201 on success | Create a research note, a clipping or a folder in a project. |
| `project.research.get` | `GET /api/v1/projects/{id}/research/{note_id}` | project reader | read |  | Get one research note with its body. |
| `project.research.list` | `GET /api/v1/projects/{id}/research` | project reader | read |  | List a project's research notes, clippings and folders as a flat tree with reading order. |
| `project.research.move` | `POST /api/v1/projects/{id}/research/{note_id}/move` | project editor | write |  | Move a research note to another folder or position. |
| `project.research.restore` | `POST /api/v1/projects/{id}/research/{note_id}/restore` | project editor | write |  | Restore a trashed note and what was trashed with it. |
| `project.research.trash` | `POST /api/v1/projects/{id}/research/{note_id}/trash` | project editor | write |  | Move a note, and a folder's contents, to the trash. |
| `project.research.update` | `PATCH /api/v1/projects/{id}/research/{note_id}` | project editor | write |  | Save a research note. Send the revision you last saw; a stale revision is a 409. |
| `project.restore` | `POST /api/v1/projects/{id}/restore` | project owner | write |  | Take a project out of the trash. Owner only; restoring a live project does nothing. |
| `project.scratch.get` | `GET /api/v1/projects/{id}/scratchpad` | project reader | read | browser session only | Read your private scratchpad for this project. It is yours alone: nobody else, an administrator included, can read it through Scríobh, and an API key… |
| `project.scratch.set` | `PUT /api/v1/projects/{id}/scratchpad` | project reader | write | browser session only | Save your private scratchpad for this project. Send the revision you last saw; a stale revision is a 409. |
| `project.tag.create` | `POST /api/v1/projects/{id}/tags` | project editor | write | 201 on success | Create a tag. Tags are also created when a note is saved with a new name. |
| `project.tag.delete` | `DELETE /api/v1/projects/{id}/tags/{tag_id}` | project editor | write | permanent | Delete a tag. Notes lose it and are otherwise unchanged. |
| `project.tag.list` | `GET /api/v1/projects/{id}/tags` | project reader | read |  | List a project's tags with how many research notes use each. |
| `project.tag.merge` | `POST /api/v1/projects/{id}/tags/{tag_id}/merge` | project editor | write | permanent | Merge a tag into another: its notes carry the other tag, and it is deleted. |
| `project.tag.update` | `PATCH /api/v1/projects/{id}/tags/{tag_id}` | project editor | write |  | Rename a tag or change its colour. Notes keep it. |
| `project.trash` | `POST /api/v1/projects/{id}/trash` | project owner | write |  | Move a project to the trash. Owner only; nothing is deleted. |
| `project.typography.get` | `GET /api/v1/projects/{id}/typography` | project reader | read |  | How the project's writing and reading views are set: what the project sets, the caller's own override, the effective result, and the fixed schema of… |
| `project.typography.reset` | `POST /api/v1/projects/{id}/typography/reset` | project editor | write |  | Remove the project's typography, so its views are set as the application ships them. |
| `project.typography.set` | `PUT /api/v1/projects/{id}/typography` | project editor | write |  | Set the project's typography, replacing what it had: typeface, size (a percentage of the reader's own, 100 to 140), line height (1.3 to 2), measure… |
| `project.unarchive` | `POST /api/v1/projects/{id}/unarchive` | project owner | write |  | Bring an archived project back to the library. Owner only; does nothing when it is not archived. |
| `project.unlock` | `POST /api/v1/projects/{id}/unlock` | project reader | write | browser session only, no dry run | Unlock a protected project in this browser session. Any member, browser session only. |
| `project.update` | `PATCH /api/v1/projects/{id}` | project owner | write |  | Change project details. Send only the fields to change; an empty body changes nothing. |

#### `repo` (9)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `repo.account.connect` | `POST /api/v1/git/accounts` | any signed-in caller | write | browser session only, no dry run, 201 on success | Connect your account at GitHub or a Forgejo or Gitea host with a personal access token. |
| `repo.account.disconnect` | `DELETE /api/v1/git/accounts` | any signed-in caller | write | browser session only, no dry run, permanent | Forget a connected account and its stored token. Remotes made with it keep their own copy of the token and are not touched. |
| `repo.account.list` | `GET /api/v1/git/accounts` | any signed-in caller | read | browser session only | Your connected accounts at code hosts (GitHub, Forgejo, Gitea): host and login, never the token. |
| `repo.list` | `POST /api/v1/projects/{id}/repo/list` | project owner | write | browser session only, no dry run | The repositories your connected account can write to, one page of up to 50 from the host. |
| `repo.plan` | `POST /api/v1/projects/{id}/repo/plan` | project owner | write | browser session only, no dry run | A dry run of publishing the project to a repository: what will be created, the visibility, which files the first push holds (count, size, paths)… |
| `repo.publish` | `POST /api/v1/projects/{id}/repo/publish` | project owner | write | browser session only, no dry run, 201 on success | Publish the project to a repository: create it (or take an empty one), register it as a remote of the project and queue the first push. |
| `repo.release` | `POST /api/v1/projects/{id}/repo/release` | project owner | write | browser session only, no dry run, 202 on success | Publish a compiled edition (EPUB, DOCX, PDF or a Markdown bundle) as a release asset at a GitHub or Forgejo remote, after a successful push: a… |
| `repo.release_get` | `GET /api/v1/projects/{id}/repo/release` | project owner | read | browser session only | One release attempt: its status, the asset, the hashes, and why it failed. |
| `repo.release_list` | `GET /api/v1/projects/{id}/repo/releases` | project owner | read | browser session only | A remote's release attempts, newest first (the last 30). Owner only. |

#### `review` (2)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `review.queue` | `GET /api/v1/projects/{id}/review/queue` | project editor | read |  | The writer's review queue: every open thread of the project in reading order (comments on the project first; then each document in the order of the… |
| `review.resolve_all` | `POST /api/v1/projects/{id}/review/resolve` | project editor | write |  | Resolve every open thread that matches a filter (document_id, author, kind, state, or an explicit thread_ids list) in one transaction. |

#### `revision` (9)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `revision.days` | `GET /api/v1/docs/{id}/revisions/days` | project reader | read |  | List the days a document has versions, newest first, with each day's last version: three clicks to restore yesterday's chapter. |
| `revision.delete` | `DELETE /api/v1/docs/{id}/revision` | project editor | write | permanent | Delete a named snapshot. Automatic versions cannot be deleted; retention thins them. |
| `revision.diff` | `GET /api/v1/docs/{id}/revisions/diff` | project reader | read |  | Compare two versions, or a version with the current text, word by word or paragraph by paragraph. |
| `revision.get` | `GET /api/v1/docs/{id}/revision` | project reader | read |  | Get one version of a document with its text. |
| `revision.label` | `PATCH /api/v1/docs/{id}/revision` | project editor | write |  | Rename or annotate a version. A labelled version is kept forever. |
| `revision.list` | `GET /api/v1/docs/{id}/revisions` | project reader | read |  | List a document's versions, newest first, with reason, label, words and word delta. |
| `revision.prune` | `POST /api/v1/projects/{id}/revisions/prune` | project owner | write | permanent | Apply the retention policy to a project's history now (it also runs on a schedule). |
| `revision.restore` | `POST /api/v1/docs/{id}/revisions/restore` | project editor | write |  | Restore a version as the document's text. History is append-only: the current text is kept first if no version holds it, and the restore is written… |
| `revision.snapshot` | `POST /api/v1/docs/{id}/revisions` | project editor | write | 201 on success | Take a named snapshot of a document as it stands. Snapshots are kept forever. |

#### `scene` (4)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `scene.entities` | `GET /api/v1/docs/{id}/scene-entities` | project reader | read |  | The "in this scene" sidebar: the entities a document mentions, plus its point of view and location, each with kind, summary (the book's override… |
| `scene.link_entities` | `POST /api/v1/projects/{id}/scenes/link-entities` | project owner | write |  | Turn the free-text point of view and location of this project's scenes into entity references: each distinct value is matched, exactly and ignoring… |
| `scene.set_location` | `PUT /api/v1/docs/{id}/location` | project editor | write |  | Set where a scene happens to an entity of the bible (usually a place), by id, name or alias among the entities the caller may read (empty clears the… |
| `scene.set_pov` | `PUT /api/v1/docs/{id}/pov` | project editor | write |  | Set a scene's point of view to a character of the bible, by id, name or alias among the entities the caller may read (empty clears the link and keeps… |

#### `search` (12)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `search.compendium` | `GET /api/v1/compendiums/{id}/search` | compendium reader | read |  | Search a Compendium's entities: names, aliases, summaries and bodies, best matches first (a name or alias match outranks a body match). |
| `search.compendium_research` | `GET /api/v1/compendiums/{id}/search/research` | compendium reader | read |  | Search a Compendium's research notes and clippings, paged. Needs membership of the Compendium. |
| `search.query` | `GET /api/v1/projects/{id}/search` | project reader | read |  | Search a project's documents: titles, synopses and text, best matches first. |
| `search.research` | `GET /api/v1/projects/{id}/search/research` | project reader | read |  | Search a project's own research notes and clippings (not the manuscript, and never a Compendium's research). |
| `search.semantic` | `GET /api/v1/projects/{id}/search/semantic` | project editor | read | no dry run | Search a project by meaning: the chunks of the manuscript's chosen wording whose embedding is closest to the question, with document, position, score… |
| `search.semantic.disable` | `POST /api/v1/projects/{id}/search/semantic/disable` | project owner | write | browser session only | Owner, browser sessions only. Stop indexing and refuse semantic searches. |
| `search.semantic.enable` | `POST /api/v1/projects/{id}/search/semantic/enable` | project owner | write | browser session only | Owner, browser sessions only: an API key cannot change what leaves the server. |
| `search.semantic.preview` | `POST /api/v1/projects/{id}/search/semantic/preview` | project owner | read | browser session only | Owner, browser sessions only. What switching the semantic index on would do, before anything is sent: how many documents, chunks, words and bytes… |
| `search.semantic.purge` | `POST /api/v1/projects/{id}/search/semantic/purge` | project owner | write | browser session only, permanent | Owner, browser sessions only. Delete every embedding of the project and forget the endpoint choice and consent: the index is gone and off. |
| `search.semantic.rebuild` | `POST /api/v1/projects/{id}/search/semantic/rebuild` | project owner | write | browser session only | Owner, browser sessions only. Delete every embedding and index the project again from the start, to the same confirmed endpoint. |
| `search.semantic.status` | `GET /api/v1/projects/{id}/search/semantic/status` | project editor | read |  | The state of the project's semantic index: off, paused (and why), waiting, indexing, in back-off after a failure, or ready; the counts of documents… |
| `search.similar` | `GET /api/v1/projects/{id}/search/similar` | project reader | read |  | Scenes like this one: the documents whose text is closest in meaning to a document (or to one of its chunks), best first, leaving out the document… |

#### `session` (3)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `session.list` | `GET /api/v1/me/sessions` | any signed-in caller | read |  | List the caller's live browser sessions, newest first, marking the current one. |
| `session.revoke` | `POST /api/v1/me/sessions/{sid}/revoke` | any signed-in caller | write |  | Sign out one of the caller's own sessions. Revoking the current session is allowed and signs the caller out. |
| `session.revoke_others` | `POST /api/v1/me/sessions/revoke-others` | any signed-in caller | write |  | Sign out every live session of the caller except the current one; returns how many were ended. |

#### `share` (5)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `share.create` | `POST /api/v1/projects/{id}/shares` | project owner | write | browser session only, no dry run, 201 on success | Make a share link: read-only or comment-only access for a person with no account. |
| `share.list` | `GET /api/v1/projects/{id}/shares` | project owner | read | browser session only | The project's share links, newest first, with their state (active, expired or revoked) and counts. |
| `share.revoke` | `POST /api/v1/projects/{id}/shares/{share_id}/revoke` | project owner | write | browser session only | Revoke a link: from then on its token is answered exactly as a token that never existed. |
| `share.update` | `PATCH /api/v1/projects/{id}/shares/{share_id}` | project owner | write | browser session only, no dry run | Change a link: its label, scope, documents, passphrase (a new one, or an empty string to remove it), expiry (an empty string clears it), download… |
| `share.usage` | `GET /api/v1/projects/{id}/shares/{share_id}/usage` | project owner | read | browser session only | How a link has been used: views, when last, comments written through it and how many of those threads are open, wrong passphrases tried and whether… |

#### `site` (8)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `site.build` | `POST /api/v1/projects/{id}/site/build` | project editor | write |  | Build the site and download it as a zip: HTML pages, listings, tag and category pages, RSS and Atom feeds and a sitemap (with a base address), a… |
| `site.doc.list` | `GET /api/v1/projects/{id}/site/documents` | project editor | read |  | Every document that has site settings of its own: role (auto, post, page, skip), address, publication date, excerpt, tags. |
| `site.doc.set` | `PATCH /api/v1/projects/{id}/site/documents/{document_id}` | project editor | write | browser session only | Set how one document appears on the site: role (auto, post, page or skip), an address (slug), a publication date, an excerpt and up to 10 tags. |
| `site.options.get` | `GET /api/v1/projects/{id}/site/options` | project editor | read |  | The project's site options (title, tagline, base address, language, posts per page, feed, navigation, accent, ...), with the defaults and the accents… |
| `site.options.set` | `PATCH /api/v1/projects/{id}/site/options` | project editor | write | browser session only | Change some of the site's options. Send only what changes in options; name fields in options.clear to return them to their default. |
| `site.preview` | `POST /api/v1/projects/{id}/site/preview` | project editor | read |  | What would be published, without writing anything: the posts (newest first) and pages with their addresses, dates, reading time and excerpts, the… |
| `site.publish` | `POST /api/v1/projects/{id}/site/publish` | project editor | write | browser session only, no dry run | Publish the site to the folder the server administrator configured (SCRIOBH_SITE_DIR, or SCRIOBH_SITE_PUBLISH=1 for the data directory's sites… |
| `site.status` | `GET /api/v1/projects/{id}/site` | project editor | read |  | The site's status: whether building and publishing to a folder are available on this server, the last build and the last publish (time, counts… |

#### `stash` (9)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `stash.create` | `POST /api/v1/projects/{id}/stash` | project editor | write | 201 on success | Put a paragraph, note or outline in the project's stash: overflow that is not in the manuscript, not counted and not exported. |
| `stash.delete` | `POST /api/v1/projects/{id}/stash/delete` | project editor | write | permanent | Delete a trashed stash item for good. |
| `stash.drag_back` | `POST /api/v1/projects/{id}/stash/drag-back` | project editor | write |  | Put a stash item into a document, as its own paragraph (block) or inside one (inline), at a position or at the end. |
| `stash.get` | `GET /api/v1/projects/{id}/stash/item` | project editor | read |  | Get one stash item. |
| `stash.list` | `GET /api/v1/projects/{id}/stash` | project editor | read |  | List the stash in order, or search it (q: title, tags and text, best match first). |
| `stash.reorder` | `POST /api/v1/projects/{id}/stash/reorder` | project editor | write |  | Move a stash item to another place in the stash. |
| `stash.restore` | `POST /api/v1/projects/{id}/stash/restore` | project editor | write |  | Bring a trashed stash item back, at the end. |
| `stash.trash` | `POST /api/v1/projects/{id}/stash/trash` | project editor | write |  | Move a stash item to the stash trash. |
| `stash.update` | `PATCH /api/v1/projects/{id}/stash/item` | project editor | write |  | Edit a stash item. Send the revision you last saw; a stale one is a 409. |

#### `stats` (3)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `stats.history` | `GET /api/v1/projects/{id}/stats/history` | project reader | read |  | The caller's last N days on a project, oldest first, one entry per day including quiet days, for a calendar or heatmap. |
| `stats.project` | `GET /api/v1/projects/{id}/stats/project` | project reader | read |  | Progress toward the project's total words: the target, the words per day pace, the words per day needed for a deadline, and a projected finish date… |
| `stats.today` | `GET /api/v1/projects/{id}/stats/today` | project reader | read |  | The caller's day so far on a project: words added and net, minutes, the daily target, whether it is met, and the streak. |

#### `studio` (20)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `studio.calendar` | `GET /api/v1/me/studio/calendar` | any signed-in caller | read |  | The caller's activity calendar: columns of seven days, Monday first, with month labels, Monday-Wednesday-Friday row labels and five intensity steps… |
| `studio.card.create` | `POST /api/v1/me/studio/cards` | any signed-in caller | write | browser session only, no dry run, 201 on success | Make a private progress card: an unguessable link to a small page (and an SVG) that shows the streak and a goal summary and nothing of any project. |
| `studio.card.list` | `GET /api/v1/me/studio/cards` | any signed-in caller | read | browser session only | The caller's progress cards with their views and expiry. The secret is never listed. |
| `studio.card.revoke` | `POST /api/v1/me/studio/cards/{id}/revoke` | any signed-in caller | write | browser session only | Revoke a progress card: its link stops working at once. Browser session only. |
| `studio.compare` | `GET /api/v1/me/studio/compare` | any signed-in caller | read |  | Compare two periods (this week against last, this month against last, or any two ranges), or two projects over the same range (draft against draft)… |
| `studio.dashboard` | `GET /api/v1/me/studio` | any signed-in caller | read |  | The writing dashboard in one read, for the caller only: today's words against the daily target, the habit-streak card, the goals with counts, the… |
| `studio.export` | `GET /api/v1/me/studio/export` | any signed-in caller | read |  | The caller's analytics as CSV (daily, weekly, goals, sprints or projects) or Markdown, over a range of at most ten years. |
| `studio.goal.archive` | `POST /api/v1/me/studio/goals/{id}/archive` | any signed-in caller | write |  | End a goal. Its record stays, and the days it was met stay met. |
| `studio.goal.create` | `POST /api/v1/me/studio/goals` | any signed-in caller | write | 201 on success | Make a goal. Goals count words added, never net words, and a day without writing is never counted against one. |
| `studio.goal.history` | `GET /api/v1/me/studio/goals/{id}/history` | any signed-in caller | read |  | A goal's versions and its past periods, each judged against the target that was in force then. |
| `studio.goal.list` | `GET /api/v1/me/studio/goals` | any signed-in caller | read |  | The caller's goals with progress: studio goals (day, week, month or total; words, sessions, chapters or minutes; over every project or one) and the… |
| `studio.goal.update` | `PATCH /api/v1/me/studio/goals/{id}` | any signed-in caller | write |  | Change a goal's target (or a total goal's deadline). The change is a new version: periods already over keep the target they had, and a period that… |
| `studio.nudge.seen` | `POST /api/v1/me/studio/nudges/seen` | any signed-in caller | write |  | Record that nudges were shown, so they are not offered again today. |
| `studio.project.totals` | `GET /api/v1/projects/{id}/studio` | project reader | read |  | The caller's own daily and weekly words on one project (never other members'), for the last N days. |
| `studio.series` | `GET /api/v1/me/studio/series` | any signed-in caller | read |  | The caller's words-written series over 30 days, 90 days, 365 days, all time or a custom range (days, weeks or months by length), with pace and the… |
| `studio.sprint.abandon` | `POST /api/v1/me/studio/sprint/abandon` | any signed-in caller | write |  | Abandon the running sprint: it is left out of the sprint statistics. |
| `studio.sprint.current` | `GET /api/v1/me/studio/sprint` | any signed-in caller | read |  | The running sprint or session timer, if any, with the seconds left. |
| `studio.sprint.start` | `POST /api/v1/projects/{id}/studio/sprints` | project editor | write | 201 on success | Start a sprint (15, 25, 45 or any 1 to 240 minutes) or an open session timer (0 minutes) on a project. |
| `studio.sprint.stats` | `GET /api/v1/me/studio/sprints` | any signed-in caller | read |  | Sprint statistics: how many, words per sprint, the best sprint, and by time of day. |
| `studio.sprint.stop` | `POST /api/v1/me/studio/sprint/stop` | any signed-in caller | write |  | Stop the running sprint. Its words stay. |

#### `suggestion` (7)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `suggestion.accept` | `POST /api/v1/projects/{id}/suggestions/{suggestion_id}/accept` | project editor | write |  | Accept a suggestion: apply all of its changes to the document in one transaction, as one history entry with reason suggestion-accepted (restoring the… |
| `suggestion.accept_all` | `POST /api/v1/projects/{id}/suggestions/accept_all` | project editor | write |  | Accept a chosen set of open suggestions of one document as one change: one transaction, one history entry. |
| `suggestion.create` | `POST /api/v1/projects/{id}/suggestions` | project reviewer | write | 201 on success | Propose a change to a document's text: up to 50 changes, each a range (offsets at base_revision, or a quote with the words around it) and the text to… |
| `suggestion.get` | `GET /api/v1/projects/{id}/suggestions/{suggestion_id}` | project reviewer | read |  | One suggestion with its changes. |
| `suggestion.list` | `GET /api/v1/projects/{id}/suggestions` | project reviewer | read |  | List suggestions, newest first: all of the project's, or one document's. |
| `suggestion.reject` | `POST /api/v1/projects/{id}/suggestions/{suggestion_id}/reject` | project editor | write |  | Reject an open suggestion. The text is not touched. The author is notified. |
| `suggestion.withdraw` | `POST /api/v1/projects/{id}/suggestions/{suggestion_id}/withdraw` | project reviewer | write |  | Take back your own open suggestion. |

#### `timeline` (13)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `timeline.check` | `GET /api/v1/projects/{id}/timeline/check` | project reader | read |  | Find contradictions in a book's timeline by fixed rules: one person at two places at once, an event before a person's `born` date or after their… |
| `timeline.compendium.check` | `GET /api/v1/compendiums/{id}/timeline/check` | compendium reader | read |  | The event-only checks for a Compendium's own timeline (a place conflict, an event before a birth or after a death), without any book's scenes. |
| `timeline.event.create` | `POST /api/v1/compendiums/{id}/timeline/events` | compendium editor | write | 201 on success | Add an event to a Compendium's timeline: a title, an optional story date (YYYY, YYYY-MM, YYYY-MM-DD or with a time; partial dates are fine) and end… |
| `timeline.event.delete` | `DELETE /api/v1/timeline/events/{id}` | compendium editor | write |  | Move an event to the trash. Its people, place and scene links are kept, so timeline.event.restore brings it back exactly. |
| `timeline.event.get` | `GET /api/v1/timeline/events/{id}` | compendium reader | read |  | One event with its people, place and the scenes that tell it (only those in projects the caller can open). |
| `timeline.event.list` | `GET /api/v1/compendiums/{id}/timeline/events` | compendium reader | read |  | A Compendium's timeline in story order (dated events by start then end, undated last, ties by title), paged. |
| `timeline.event.restore` | `POST /api/v1/timeline/events/{id}/restore` | compendium editor | write |  | Bring a trashed event back. Does nothing if it is not in the trash. |
| `timeline.event.update` | `PATCH /api/v1/timeline/events/{id}` | compendium editor | write |  | Change an event. Only what is sent changes; participants, when sent, replace the whole list. |
| `timeline.order.get` | `GET /api/v1/projects/{id}/timeline/order` | project reader | read |  | The order this book tells its events in: derived from where its scenes sit unless the book set its own. |
| `timeline.order.set` | `PUT /api/v1/projects/{id}/timeline/order` | project editor | write |  | Set this book's own order of events (event_ids, first told first; events not named follow in their derived order), or reset it to the order the… |
| `timeline.project.view` | `GET /api/v1/projects/{id}/timeline` | project reader | read |  | A book's timeline: the events of the Compendiums the caller can read that the book draws on, and the dated scenes no event covers, merged in story… |
| `timeline.scene.link` | `POST /api/v1/docs/{id}/timeline-events` | project editor | write |  | Say that this scene tells an event. The event must be readable by the caller and belong to a Compendium the scene's book uses. |
| `timeline.scene.unlink` | `DELETE /api/v1/docs/{id}/timeline-events` | project editor | write |  | Say that this scene no longer tells an event. Does nothing if it did not. |

#### `webhook` (9)

| Op | Request | Access | Scope | Notes | What it does |
| --- | --- | --- | --- | --- | --- |
| `webhook.create` | `POST /api/v1/projects/{id}/webhooks` | project owner | admin | no dry run, 201 on success | Make a webhook: a signed notification sent to an address when a document is saved (ids and word counts only), a comment or suggestion is made (ids… |
| `webhook.delete` | `DELETE /api/v1/projects/{id}/webhooks/{webhook_id}` | project owner | admin | permanent | Delete a webhook and the list of its deliveries. Deliveries still waiting are dropped. |
| `webhook.deliveries` | `GET /api/v1/projects/{id}/webhooks/{webhook_id}/deliveries` | project owner | admin |  | A webhook's deliveries, newest first, with state, attempts, status and failure kind. |
| `webhook.get` | `GET /api/v1/projects/{id}/webhooks/{webhook_id}` | project owner | admin |  | One webhook. Never includes the secret. Project owner. |
| `webhook.list` | `GET /api/v1/projects/{id}/webhooks` | project owner | admin |  | List the project's webhooks with their state: enabled, last status, deliveries waiting and dead. |
| `webhook.redeliver` | `POST /api/v1/projects/{id}/webhooks/{webhook_id}/deliveries/{delivery_id}/redeliver` | project owner | admin |  | Queue a dead or delivered notification again, with the same delivery id. |
| `webhook.rotate_secret` | `POST /api/v1/projects/{id}/webhooks/{webhook_id}/rotate` | project owner | admin | no dry run | Replace the signing secret. The new secret is in this response and nowhere else; the old one keeps signing beside it for keep_old_hours so a receiver… |
| `webhook.test` | `POST /api/v1/projects/{id}/webhooks/{webhook_id}/test` | project owner | admin | no dry run | Send a signed ping to the webhook's address now and report what came back (status and failure kind). |
| `webhook.update` | `PATCH /api/v1/projects/{id}/webhooks/{webhook_id}` | project owner | admin |  | Change a webhook's name, address, events, whether comments carry their words, or switch it on or off. |
<!-- END GENERATED: op table -->
