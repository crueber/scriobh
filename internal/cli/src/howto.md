## Setup

Create a key in Scríobh under Settings, API keys (or ask the person who runs the server), then:

```sh
scriobh-cli login --url https://write.example.com --key scr_...
scriobh-cli whoami
```

`login` saves the server and key in the user's config directory with mode 0600 and never prints the key. In a container or CI, set `SCRIOBH_URL` and `SCRIOBH_API_KEY` instead; `--server` and `--key` override both. `whoami` says who the key acts as, its scope, its expiry and any project or Compendium it is restricted to.

## How commands work

Every operation is a command: `scriobh-cli <group> <verb>`, from the operation's name (`doc.update` is `doc update`; an underscore becomes a hyphen, so `doc.resolve_link` is `doc resolve-link`). `scriobh-cli ops` lists them, `scriobh-cli ops doc` lists one group, and `scriobh-cli <group> <verb> --help` shows a command's flags. The reference at the end of this skill is generated from the same schema.

- **Flags come from the operation's inputs**, spelled with hyphens: `base_revision` is `--base-revision`. Required ones are marked. A bare argument fills the next path parameter, then the first required text flag, so `scriobh-cli doc get "The Salt Road/Chapter 1"` and `scriobh-cli doc get --id "The Salt Road/Chapter 1"` mean the same.
- **Long text** comes from a file or standard input: `--content @chapter.md` reads a file, `--content -` reads stdin, `@@` is a literal at-sign. `--content-file chapter.md` also works. To send a whole JSON body, use `--data '{"title":"..."}'`, `--data @body.json` or `--data -`; flags override its fields.
- **Lists and objects** take JSON text (`--items '{"theme":"dark"}'`). A list of strings is a repeated flag. A field that takes uploaded files takes a directory: `--files @./manuscript` reads every non-hidden file in it, and `--zip @book.zip` reads an archive.

## Names instead of ids

Wherever a command takes the id of a project, document, Compendium or entity, give its name instead. The CLI looks it up through the list commands, on your side, and tells you what it resolved:

- `"The Salt Road"` is a project; `"The Salt Road/Part One/Chapter 3"` is a document by its path in the tree (the first part is the project). Inside a project you may write `--project "The Salt Road"` and then the document by its own path.
- An exact match wins, then a match ignoring case and spacing. If more than one thing matches, the command fails with exit code 2 and code `ambiguous`, listing the candidates with their ids; it never guesses. If nothing matches the error is `not_found` (exit 3) with the nearest names.
- Ids always work and cost no lookup. `id:xyz` forces an id and `name:xyz` forces a name.

## Output

On a pipe, or with `--json`, stdout is one JSON object:

```json
{"ok": true, "op": "doc.get", "status": 200, "data": {"item": {"id": "doc_...", "revision": 3}}, "meta": {"resolved": [{"field": "id", "kind": "doc", "input": "The Salt Road/Chapter 1", "id": "doc_..."}]}}
```

`data` is the server's response unchanged; `meta` appears when there is something to say (`dry_run`, `would_change`, `idempotent_replay`, `resolved`, `pages`). Errors are one JSON object on stderr and a non-zero exit code, never mixed into stdout:

```json
{"ok": false, "error": {"code": "conflict", "message": "This document changed since you opened it.", "status": 409, "data": {"reason": "revision", "current_revision": 4}}, "exit": 4}
```

A command that returns a file (an export) needs `--output FILE`; `--output -` writes the bytes to stdout. A list that continues (`next_cursor`) is followed with `--all`.

## Previewing a write

`--dry-run` runs the command on the server inside its write transaction and rolls it back. You get the result the command would have returned, plus `dry_run`, `would_change` and `changes` (rows it would have touched). No event is sent, no audit entry kept, no file written, no revision made. A few commands cannot be previewed (they write files outside the database or return a secret) and refuse with `dry_run_unsupported`; the command's `--help` says so. Add `--fail-on-change` to exit 9 when a preview would change something.

## Retrying safely

Every write sends a fresh `Idempotency-Key`, and a network failure or a 502, 503 or 504 is retried with the same key, so a write is applied once. To make your own retry loop safe, pass the same `--idempotency-key` each time: the server replays the stored answer for 24 hours (`idempotent_replay` in `meta`), and refuses the key with `idempotency_key_reuse` (exit 6) if the request differs. Only successful answers are kept; a failed write can simply be corrected and sent again. Commands whose answer holds a secret (`key create`) are never replayed.
