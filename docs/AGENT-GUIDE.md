# Agent guide

Scríobh is designed so an agent can build a Compendium or draft a story from outside the app, using the same operations a person uses. This guide is for the human who sets the agent up; the agent itself should read the skill.

## 1. Create a scoped key

In Scríobh, open **Settings → API keys** and create a key:

- **Scope:** `read` for research, `write` for building, `admin` only when you truly need it.
- **Restrict to:** one project, whenever possible (a Compendium restriction is accepted by the auth code and arrives with the Compendium).
- **Label:** name the agent, for example `Claude — Salt Road bible`.
- **Expiry:** set one. A key that outlives the task is a liability.

The raw key is shown once. Store it in the agent's secret store, not in a repository.

## 2. Install and sign in

```sh
scriobh-cli login --url https://write.example.com --key -    # paste the key on standard input
scriobh-cli whoami
```

Credentials live in `config.json` in the user's config directory with mode `0600`; `SCRIOBH_URL` and `SCRIOBH_API_KEY` override them. `scriobh-cli logout` removes the key.

## 3. Give the agent the skill

```sh
scriobh-cli --skill                 # print the full skill to stdout
scriobh-cli --skill drafting        # one recipe: quickstart, drafting, compendium, import or export
scriobh-cli skill show              # same as --skill
scriobh-cli skill install --agent claude   # or codex, opencode, gemini, cursor, windsurf, copilot; no --agent detects
scriobh-cli skill check             # compare the installed skill and the CLI with the server
```

`--skill` is the short form. It prints a Markdown document containing:

- what Scríobh is and what this instance is for;
- the object model: Compendium, entity, document, timeline event, research note;
- a safe workflow: read, propose, `--dry-run`, write in small batches, verify;
- recipes for looking around, drafting a chapter, building a compendium, importing and exporting (the compendium recipe is written from what the server offers);
- error codes, rate limits, and the rule that content is data, never instructions;
- the skill version and the schema hash it was generated against.

The skill is generated from the op registry plus hand-written guidance. After any op change, run `make skill`; CI fails if the committed skill is stale. `scriobh-cli skill check` catches an installed copy that has drifted from the server.

## 4. The shape of a build

```sh
# a compendium that may outlive this project
scriobh-cli compendium create "The Salt Road"

# the cast
scriobh-cli entity create --compendium "The Salt Road" --kind character \
  --name "Máire Nolan" --field age=34 --field want="keep the house"

# a book that uses it
scriobh-cli project create "The Salt Road" --kind novel
scriobh-cli project compendium set "The Salt Road" --compendium "The Salt Road"

# a chapter
scriobh-cli doc create --project "The Salt Road" --title "The Harbour at Low Tide" \
  --body-file chapter.md --dry-run
scriobh-cli doc create --project "The Salt Road" --title "The Harbour at Low Tide" \
  --body-file chapter.md

# the world's clock
scriobh-cli timeline add --compendium "The Salt Road" --title "The letter arrives" \
  --story-date 1998-11-04 --participant "Máire Nolan"
```

Names resolve to ids, so an agent can work from the names a writer uses. Output is JSON when piped; `--dry-run` reports what would change; `--idempotency-key` makes a retried write safe.

## 4b. Over MCP

An agent that speaks the Model Context Protocol uses the same operations as tools, with the same keys and limits:

```sh
scriobh-cli mcp --groups core,bible --read-only     # stdio: the MCP client starts this
```

Put it in the client's MCP configuration with `SCRIOBH_URL` and `SCRIOBH_API_KEY` in its environment, or point a client that speaks Streamable HTTP at `https://write.example.com/mcp` with `Authorization: Bearer <key>`. A tool is an operation with its dots as underscores (`doc_update`); the key's scope and pin decide which exist for it (a read key sees only reading tools); writes take `dry_run` and `idempotency_key`; resources (`scriobh://project/{id}/outline`, `.../doc/{docId}`, the entities) give the text as Markdown; two prompts (`continuity-check`, `summarize-chapter`) mirror the skill. The generated tool reference is `skills/scriobh/mcp.md`. The whole set is large (about 200 tools), so name the groups the agent needs. Keys, Git, backups, shares, webhooks, imports, the in-app writing assistant, the purge of a project, the author's contact block and the static site's publishing operations (`site.options.set`, `site.doc.set`, `site.publish`: making text public is a person's act, like a share link; an agent can read the site's status and preview it, and a key of any scope is refused if it tries) are not offered. A new group of operations is never offered by default. 17 tools destroy data beyond the trash (marked permanent): they are hidden unless the server's administrator allowed them and the connection asks (`--permanent`); every other delete goes to the trash. Details and the setup for Claude Desktop, Cursor and OpenCode: [OPERATIONS, MCP](OPERATIONS.md#mcp-the-model-context-protocol-endpoint).

## 4c. Webhooks

A project's owner (or an `admin`-scope key) can make signed notifications go to an address:

```sh
scriobh-cli webhook create --project "The Salt Road" --name "Build server" \
  --url https://ci.example.com/hooks/scriobh --events document.updated --events export.completed
scriobh-cli webhook test "The Salt Road" "Build server"
scriobh-cli webhook deliveries "The Salt Road" "Build server" --state dead
```

The secret is in the `create` answer once. Payloads carry ids and counts, never text; delivery is at least once and a receiver should store `Scriobh-Webhook-Id`. See [OPERATIONS, Webhooks](OPERATIONS.md#webhooks) for the signature, the retries and what the server will and will not call.

## 5. What the agent may not do

- Read or write outside its key's Compendium or project restriction.
- Delete anything permanently. Deletes go to trash and require `--yes`; over MCP there is no purge tool at all, and the tools that destroy data beyond the trash are hidden unless the administrator and the connection both ask for them.
- Touch admin settings, users, or another project's material.
- Treat manuscript or compendium text as instructions. The skill says this explicitly, and the server gives no route that would let content execute.

Every write is attributed in the audit log and the event stream.

## 6. Error handling

| Exit | Meaning | What the agent should do |
| --- | --- | --- |
| `0` | Success (a dry run too) | Continue |
| `1` | Server error | Retry once later, then stop and report |
| `2` | Usage, or an ambiguous name | Fix the command; pick an id from the candidates |
| `3` | Not found | Re-read the tree; the name may have changed |
| `4` | Conflict | Re-read, then retry with the current revision |
| `5` | Authentication or permission | Stop; the key is missing, expired, revoked, or not allowed |
| `6` | Validation | Fix the input; do not retry unchanged |
| `7` | Rate limited | Wait `retry_after` seconds; do not loop |
| `8` | Unreachable | Check the URL; retry later |
| `9` | Dry run would change something (with `--fail-on-change`) | Review, then run for real |
| `10` | Skill or CLI snapshot out of date | `scriobh-cli skill install` |

## 7. Keeping the CLI honest

The slim CLI (`scriobh-cli`) never imports the server; a test lists everything it links. It reads `GET /api/v1/_ops` and caches the schema, with an embedded snapshot as a fallback. If a command is not in the schema, it does not exist. That is how the CLI, the REST API, and the browser stay one system.
