# Operations

How to run Scríobh and keep it running: install, configure sign-in, put it behind a proxy, back it up, restore it, upgrade it, and the setup of each feature that needs the operator (AI providers, Git sync, webhooks, MCP, static sites, compile). Scríobh is one process and one data directory. There is no database server, cache, or queue to run.

Start with the [Quick start](#quick-start). The sections after it are reference: read the one you need.

**What has and has not been run.** The binary, its commands (`backup`, `restore`, `migrate`, `config check`, `healthcheck`), the development issuer, the REST API and every example in [API.md](API.md) have been run. The container path (the `Dockerfile` and the two compose files) has been checked with `docker compose config` and by reading, but the image has **not been built or started** from this repository's documentation by anyone yet; a real identity provider (Passage, Authentik, Keycloak and the rest below) has **not** been registered against this build; and the exit criterion "two independent people complete this guide without asking a question" has **not** been met. Sections say so where it matters.

## Contents

- [Quick start](#quick-start)
- [Run it with Compose](#run-it-with-compose)
- [Configuration](#configuration)
- [Registering Scríobh with an identity provider](#registering-scríobh-with-an-identity-provider)
- [Roles and the first administrator](#roles-and-the-first-administrator)
- [When the identity provider is down](#when-the-identity-provider-is-down)
- [Reverse proxy](#reverse-proxy)
- [The data directory](#the-data-directory)
- [The Markdown mirror](#the-markdown-mirror)
- [Backups](#backups)
- [Git sync](#git-sync)
- [AI providers: quick path](#ai-providers-quick-path)
- [AI providers, policy and budgets](#ai-providers-policy-and-budgets)
- [The agent CLI, keys, dry runs and idempotency](#the-agent-cli-keys-dry-runs-and-idempotency)
- [MCP, the Model Context Protocol endpoint](#mcp-the-model-context-protocol-endpoint)
- [Webhooks](#webhooks)
- [Mail](#mail)
- [A project passphrase: what it is not, and a forgotten one](#a-project-passphrase-what-it-is-not-and-a-forgotten-one)
- [HTML export and covers](#html-export-and-covers)
- [Compile: DOCX, EPUB, Markdown and the submission bundle](#compile-docx-epub-markdown-and-the-submission-bundle)
- [Publishing a static site](#publishing-a-static-site)
- [The installable app and working offline](#the-installable-app-and-working-offline)
- [Performance: `scriobh bench`](#performance-scriobh-bench)
- [Upgrades](#upgrades)
- [Health checks and logs](#health-checks-and-logs)
- [Verifying a build](#verifying-a-build)
- [Releases](#releases)
- [Continuous integration](#continuous-integration)

The REST API is documented in [API.md](API.md).

## Quick start

From nothing to your first sign-in, with Docker Compose, a reverse proxy you already run, and an identity provider you already have (any OpenID Connect provider: Passage, Authentik, Keycloak, Pocket ID, Authelia). If you only want to look at the app first, do [step 0](#step-0-just-look-at-it) and stop.

You need: Docker with the Compose plugin, `git`, `openssl`, a DNS name that will point at this machine (`write.example.com` below), and a way to put TLS in front of an HTTP service (the proxy examples are further down). The steps below **build the image on your machine from this repository**; CI also publishes a production image you can run instead (see [The published image](#the-published-image)). The build needs network access to fetch the Go and distroless base images and the Go modules.

1. **Get the source.** `git clone <this repository's URL> scriobh && cd scriobh`
2. **Register Scríobh with your identity provider** (about two minutes, in the provider's admin screens). Callback URL `https://write.example.com/auth/oidc/callback`; confidential client; authorization-code flow; scopes `openid profile email`. Create three groups, `scriobh-admins`, `scriobh-writers` and `scriobh-readers`, and put yourself in `scriobh-admins`. Make sure `groups` is in the ID token and your email is marked verified. Copy the client id, the client secret and the issuer URL. The details and per-provider notes are [below](#registering-scríobh-with-an-identity-provider); the issuer must be copied **exactly** as the provider's `/.well-known/openid-configuration` reports it, trailing slash included.
3. **Write the configuration.** Create a file named `.env` in the repository folder (it is git-ignored). Put each comment on its own line, never after a value:

   ```
   SCRIOBH_BASE_URL=https://write.example.com
   SCRIOBH_OIDC_ISSUER=https://auth.example.com/application/o/scriobh/
   SCRIOBH_OIDC_CLIENT_ID=paste-the-client-id
   SCRIOBH_OIDC_CLIENT_SECRET=paste-the-client-secret
   SCRIOBH_SECRET_KEY=paste-the-output-of-the-command-below
   ```

   Make the key with `openssl rand -hex 32` and paste its output as `SCRIOBH_SECRET_KEY`. Keep a copy of it somewhere that is not this machine: it seals the credentials Scríobh stores and is **not** in backups. `SCRIOBH_BASE_URL` must be the address people type, with no trailing slash. Avoid a `$` in any value.
4. **Start it.** `cp compose.prod.example.yaml compose.prod.yaml && docker compose -f compose.prod.yaml up -d --build`. The first build takes a few minutes. Every later `docker compose` command for this stack needs the same `-f compose.prod.yaml`, or it addresses the development stack instead.
5. **Check it is up.** `docker compose -f compose.prod.yaml ps` shows the service `healthy` after about 30 seconds, and `curl -s http://127.0.0.1:8080/healthz` prints `{"status":"ok",...}`. If it exited, `docker compose -f compose.prod.yaml logs scriobh` names the problem in plain words (a missing variable is listed with every other one that is missing).
6. **Put TLS in front.** The container publishes `127.0.0.1:8080` only. Point your proxy at it (Caddy, Traefik and nginx examples are in [Reverse proxy](#reverse-proxy)); the proxy must keep the `Host` header and allow request bodies of at least 50 MB. Make `https://write.example.com` answer.
7. **Sign in.** Open `https://write.example.com`, choose Sign in, and sign in at your provider. You land in an empty library as an administrator. A refusal shows a plain message that says why (not in a group, email not verified, groups missing from the token).
8. **Make something.** New project, write a sentence, reload: it is still there. Then take a first backup: `docker compose -f compose.prod.yaml exec scriobh /scriobh backup`, and copy the archive and your `SCRIOBH_SECRET_KEY` off the machine (see [Backups](#backups)).

If sign-in fails: `docker compose -f compose.prod.yaml logs scriobh | tail` shows the real cause (an issuer that does not match is logged as `sign-in is misconfigured: the identity provider's issuer does not match SCRIOBH_OIDC_ISSUER` with both values, `configured_issuer` and `reported_issuer`, and the browser shows a "Sign-in is not set up correctly" page that names the setting and the issuer you configured; a client ID or secret the provider refuses is the same page, logged with the `client_id` and the provider's error code and never the secret), and `docker compose -f compose.prod.yaml run --rm scriobh config check` prints what the server read from the environment.

**Without Docker:** install Go 1.27.4 or newer, run `make build` (it writes `./scriobh` and `./scriobh-cli`), export the same variables as in step 3 plus `SCRIOBH_DATA_DIR=/var/lib/scriobh` (a directory the service user owns) and `SCRIOBH_LISTEN=127.0.0.1:8080`, check them with `./scriobh config check`, and start `./scriobh`. Steps 6 to 8 are the same. Keep it running with whatever you use for services (a systemd unit that runs it with that environment and `Restart=on-failure`; none ships here).

### Step 0: just look at it

No identity provider and nothing to register: the development stack has a built-in issuer with three fixed test users. It must not be exposed to a network.

```
docker compose up --build        # then open http://localhost:8080 and pick a test user
```

or, without Docker, `make run` (builds a development binary, prints `LISTEN http://127.0.0.1:<port>`, keeps its data in `./data`). Sign in as `admin@dev.scriobh`, `writer@dev.scriobh` or `reader@dev.scriobh`. See [the development stack](#development-stack-composeyaml) below.

## Run it with Compose

**Status of the container path:** the compose files parse (`docker compose config` was run on both, with a sample `.env`), the `Dockerfile` builds the same static binaries as `make build` (a `go build` of the same lines was run), and the Go version it names matches `go.mod`. The image itself has **not been built or started** from here: the statements below about restarts, volume ownership, and health describe how the stack is designed to behave, not something this repository has recorded passing yet. The binary itself is covered by `make desktop-smoke` and the Go end-to-end tests.

Two compose files ship in the repository root. Both build the image from the `Dockerfile`; nothing is pulled from a registry except the Go and distroless base images at build time.

### Development stack: `compose.yaml`

```
docker compose up --build
```

Then open <http://localhost:8080>, choose Sign in, and pick one of the three test users on the development issuer's page:

| User | Group | Role |
| --- | --- | --- |
| `admin@dev.scriobh` | `scriobh-admins` | administrator |
| `writer@dev.scriobh` | `scriobh-writers` | writer |
| `reader@dev.scriobh` | `scriobh-readers` | reader |

Sign in as the writer, create a project, then `docker compose restart` and reload: the project and your session are still there, because both live in the data volume.

What this stack is:

- It builds **without** the `prod` build tag, so the binary contains the development OIDC issuer. `SCRIOBH_OIDC_STUB=1` mounts it on the app's own origin at `/dev-idp/`. It is a real OIDC issuer (discovery, PKCE, nonce), not a bypass, and it signs anyone in as one of the three fixed users. Never expose it.
- The app refuses the stub unless `SCRIOBH_BASE_URL` is a loopback URL, and the compose file publishes the port on `127.0.0.1` only.
- State lives in the named volume `scriobh-dev_scriobh-data`. `docker compose down` keeps it; `docker compose down -v` deletes it.

### Production shape: `compose.prod.example.yaml`

```
cp compose.prod.example.yaml compose.prod.yaml
$EDITOR .env                                   # create it; see below
docker compose -f compose.prod.yaml up -d --build
```

Every later `docker compose` command for this stack (`ps`, `logs`, `exec`, `stop`, `run`) needs the same `-f compose.prod.yaml`. Without it Compose uses `compose.yaml`, which is the development stack and a different project.

`.env` (git-ignored; there is no `.env.example`, so you create it) holds the configuration and secrets, one `NAME=value` per line, comments on lines of their own:

```
SCRIOBH_BASE_URL=https://write.example.com
SCRIOBH_OIDC_ISSUER=https://auth.example.com/application/o/scriobh/
SCRIOBH_OIDC_CLIENT_ID=...
SCRIOBH_OIDC_CLIENT_SECRET=...
# the output of: openssl rand -hex 32
SCRIOBH_SECRET_KEY=...
SCRIOBH_ADMIN_GROUP=scriobh-admins
SCRIOBH_WRITER_GROUP=scriobh-writers
SCRIOBH_READER_GROUP=scriobh-readers
```

The three group lines are the defaults; list them only to rename them. Compose also reads `.env` for `${...}` interpolation in the compose file, so a literal `$` in a secret may be rewritten; avoid `$` in values. Do not write a comment after a value (`SCRIOBH_SECRET_KEY=abc  # note`): Compose strips it (checked with `docker compose config`), but as far as Docker's documentation says `docker run --env-file` does not (not run here), and the comment would then become part of the value.

`SCRIOBH_VERSION` (optional, in `.env` or the shell) is only the label the image's binary reports (`/healthz`, `scriobh version`); it defaults to `dev`.

The production image is built with `-tags prod`: the development issuer is not in the binary, and `SCRIOBH_OIDC_STUB=1` makes it exit at startup with `SCRIOBH_OIDC_STUB is set but this is a production build without the development issuer`.

### How the container is locked down

Both files set the same hardening. The image runs as uid/gid 65532 (distroless `nonroot`), with `read_only: true`, `/tmp` as tmpfs, `cap_drop: [ALL]`, and `no-new-privileges`. The app writes only under `SCRIOBH_DATA_DIR` (`/data` in the image), so the read-only root filesystem costs nothing.

`/data` is created in the image and owned by 65532, and a new named volume inherits that. If you use a **bind mount** instead, make the host directory writable by that user first:

```
mkdir -p /srv/scriobh && chown 65532:65532 /srv/scriobh && chmod 700 /srv/scriobh
```

Without Compose, the same image can be run directly. `scriobh:prod` is the tag the production compose file gives the image it builds (`make image` tags `scriobh:<version>` instead, so use that tag if you built it that way):

```
docker run --rm --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges:true \
  -p 127.0.0.1:8080:8080 -v scriobh-data:/data --env-file .env scriobh:prod
```

### The published image

CI pushes the production image to the project's registry, for example as `ghcr.io/crueber/scriobh:latest` (tags: the short commit SHA, `latest`, and the tag's own name for a `v*` tag; see [Continuous integration](#continuous-integration)). It is the same image the `Dockerfile` builds with `-tags prod`, so everything above about the user, the read-only root and `/data` applies. `latest` moves: pin a SHA or a `v*` tag. If the registry refuses an anonymous pull, `docker login ghcr.io` first.

```
docker run -d --name scriobh --restart unless-stopped --read-only --tmpfs /tmp --cap-drop ALL \
  --security-opt no-new-privileges:true -p 127.0.0.1:8080:8080 -v scriobh-data:/data --env-file .env \
  ghcr.io/crueber/scriobh:v2.0
```

Under Compose, replace the `build:` block and `image: scriobh:prod` of `compose.prod.yaml` with `image: ghcr.io/crueber/scriobh:<tag>`. **Not run:** like the rest of the container path, these lines follow the image's entrypoint and have not been run against a pulled image. To upgrade, back up first (see [Upgrades](#upgrades)), then start the new tag on the same volume.

### Without a container

```
make build                      # ./scriobh and ./scriobh-cli, production build (Go 1.27.4 or newer)
SCRIOBH_BASE_URL=https://write.example.com SCRIOBH_DATA_DIR=./data \
  SCRIOBH_OIDC_ISSUER=... SCRIOBH_OIDC_CLIENT_ID=... SCRIOBH_OIDC_CLIENT_SECRET=... ./scriobh
```

The binary defaults to `SCRIOBH_LISTEN=127.0.0.1:8080` (loopback). The container image sets `:8080`. `./scriobh config check` validates the environment and lists everything that is missing (and, once valid, prints `configuration is valid`). `./scriobh help` lists the commands. `make run` starts a development build with the stub issuer on an ephemeral loopback port, keeps its data in `./data`, and prints the address as `LISTEN http://127.0.0.1:<port>`.

## Configuration

Everything is configured by environment variables, read once at startup. Secrets (the OIDC client secret, `SCRIOBH_SECRET_KEY`) come only from the environment or the key file, never from the database or the UI. The one exception is a [desktop instance](#desktop-mode) that its administrator switched to an external provider: that provider's client secret is entered in the application and kept sealed with the instance key, as an AI key is.

This table was checked against `internal/config` (every variable the code reads is here, and the defaults are the code's). A variable that is not in the table is not read. A `SCRIOBH_` name this version does not know (a typo, or a setting from another version) is **warned about** and otherwise ignored: the server logs `unknown environment variable ignored` once for each, and `scriobh config check` prints `warning: <name> is not a setting this version reads`, each with the nearest setting as a hint (`did you mean SCRIOBH_TRUST_PROXY?`) and never the value. A number that is not a valid non-negative integer (and `SCRIOBH_IMPORT_CONCURRENCY`, which must be at least 1), or any yes/no setting whose value is not one of `1`, `true`, `yes`, `on`, `0`, `false`, `no`, `off` (any case), stops the server at start and `config check` with a message that names the variable and the accepted spellings. That is every switch: `SCRIOBH_TRUST_PROXY`, `SCRIOBH_OIDC_STUB`, `SCRIOBH_GIT_ALLOW_PRIVATE`, `SCRIOBH_SITE_PUBLISH`, `SCRIOBH_WEBHOOK_ALLOW_PRIVATE`, `SCRIOBH_MCP_ADMIN_TOOLS`, `SCRIOBH_MCP_PERMANENT_TOOLS`, `SCRIOBH_DEV_ALLOW_ANY_LISTEN`, and the ones that are on unless set off (`SCRIOBH_MIRROR`, `SCRIOBH_SITE`, `SCRIOBH_AI`, `SCRIOBH_AI_USER_PROVIDERS`, `SCRIOBH_MCP`, `SCRIOBH_WEBHOOKS`). An empty value is the default.

| Variable | Default | Meaning |
| --- | --- | --- |
| `SCRIOBH_BASE_URL` | derived | The public URL. **Set it in any real install**: when unset it is derived from the bound listener (`http://127.0.0.1:8080` for the default listen address; the image's `:8080` gives `http://localhost:8080`), which is wrong behind a proxy. Used for the OIDC callback (`<base>/auth/oidc/callback`, which you register at the identity provider), absolute links, the same-origin check, and whether cookies carry `Secure` (they do when it starts with `https://`). Set it to the URL people type; a trailing slash is trimmed. |
| `SCRIOBH_LISTEN` | `127.0.0.1:8080` (image: `:8080`) | Address to bind. `127.0.0.1:0` picks an ephemeral port and prints `LISTEN <base url>`. |
| `SCRIOBH_DESKTOP` | off | Desktop mode: the process is the sidecar of the desktop shell. See [Desktop mode](#desktop-mode). Off, nothing about it exists. |
| `SCRIOBH_LAUNCH_TOKEN` | empty | Desktop mode only, and required there: the shell's secret for one launch, at least 32 hexadecimal characters. Never logged, shown or stored. Ignored outside desktop mode. |
| `SCRIOBH_PORT_FILE` | empty | If set, the bound port is written here once the server listens. For wrappers and tests. |
| `SCRIOBH_DATA_DIR` | `data` (image: `/data`) | The data directory. See [below](#the-data-directory). |
| `SCRIOBH_IMPORT_CONCURRENCY` | `2` (1 to 16) | How many imports are read at once, for the whole process. Reading a source (a Scrivener project, a WordPress or Ghost export, a Word document, a vault, a folder) costs memory and CPU far out of proportion to the size of the request, and a preview and an apply each do it; one that cannot get a place within three seconds is answered `429 rate_limited` ("busy", with `Retry-After`) and the person tries again. A dry run of an apply is a reading too and waits for a place like any other. Writing to the database is not part of it. |
| `SCRIOBH_PDF_TIMEOUT_SECONDS` | `20` (1 or more; can only lower) | The wall clock a PDF import's reading child process may run before it is killed (the file is read by a child of the server binary run as `scriobh pdf-extract`, see [Importing a PDF](#print-pdf-paged-html-and-covers)). A larger value than 20 is ignored. |
| `SCRIOBH_PDF_MEMORY_MB` | `512` (1 or more; can only lower) | The resident memory a PDF import's reading child may reach before it is killed (checked every 75 ms; the child also runs with `GOMEMLIMIT` at three quarters of it). A larger value than 512 is ignored. |
| `SCRIOBH_OIDC_ISSUER` | none | Issuer URL of the identity provider. Required unless the stub is on. It must equal the `issuer` the provider's discovery document reports **exactly, trailing slash included** (Authentik's ends in `/`, Keycloak's does not); it is not normalised. A mismatch is logged with both values (`configured_issuer`, `reported_issuer`) and the person sees a "Sign-in is not set up correctly" page that names this setting and the configured value (never what the provider reported). |
| `SCRIOBH_OIDC_CLIENT_ID` | none | Required unless the stub is on. |
| `SCRIOBH_OIDC_CLIENT_SECRET` | none | Required unless the stub is on. |
| `SCRIOBH_OIDC_STUB` | off | `1`, `true`, `yes` or `on` enables the development issuer. Refused on a non-loopback base URL, and refused outright by a `prod` build. |
| `SCRIOBH_ADMIN_GROUP` | `scriobh-admins` | Group that maps to administrator. Set to an **empty value** (`SCRIOBH_ADMIN_GROUP=`) to use the first-run claim; leaving it unset keeps the default. |
| `SCRIOBH_WRITER_GROUP` | `scriobh-writers` | Group that maps to writer. |
| `SCRIOBH_READER_GROUP` | `scriobh-readers` | Group that maps to reader. |
| `SCRIOBH_SECRET_KEY` | generated | Key that signs the sign-in flow cookies, keys the CSRF tokens and the address hashes, and seals every credential Scríobh stores (Git deploy keys and tokens, AI provider keys, webhook secrets). At least 32 characters (`openssl rand -hex 32` gives 64). When unset, Scríobh creates `secret.key` (mode 0600) in the data directory on first start and reuses it, so restarts keep working; set the variable instead if you want the key outside the data directory. It is **not in backups**: keep a copy elsewhere. Changing it breaks sign-ins and open forms in progress, makes stored credentials unreadable (they must be entered again), and makes new address hashes differ from old ones; sessions themselves are stored hashed in the database and survive. |
| `SCRIOBH_SESSION_HOURS` | `24` | Absolute session lifetime. Minimum 1. |
| `SCRIOBH_SESSION_IDLE_MINUTES` | `480` | Idle timeout. `0` turns the idle cap off. |
| `SCRIOBH_DEV_ALLOW_ANY_LISTEN` | off | Development only. Lets the credential-free development issuer run on a non-loopback listen address; the dev compose stack sets it and publishes the port to 127.0.0.1 only. |
| `SCRIOBH_TRUST_PROXY` | off | Read the client address from `X-Forwarded-For`. See [Reverse proxy](#reverse-proxy) before enabling it. |
| `SCRIOBH_RATE_LIMIT` | `600` | Requests per minute per principal: one API key (which also spends its owner's allowance), one signed-in person, or, when not signed in, one client address; the burst is a quarter of it (at least 10). `0` disables the limit. Sign-in routes under `/auth/` have their own fixed limit of 30 per minute per address. See [API.md](API.md#limits-and-rate-limits). |
| `SCRIOBH_SHARE_RATE_LIMIT` | `120` | Requests per minute per client address to the public share routes (`/s/`), spent before the token is looked at, so guessing tokens spends it too. `0` disables it. |
| `SCRIOBH_SHARE_LINK_RATE` | `600` | Requests per minute one share link takes from everyone together. `0` disables it. |
| `SCRIOBH_SHARE_COMMENTS_PER_HOUR` | `60` | Comments the guests of one comment link may write in an hour (at least 1). A link also holds at most 500 guest comments in all, and a project at most 50 live links. |
| `SCRIOBH_MAX_KEYS_PER_USER` | `25` | Live API keys one person may hold. Revoked and expired keys do not count. |
| `SCRIOBH_MAX_ALTS_PER_DOC` | `200` | Alternatives one document may hold (1 to 5000). A document carries them in its text, so a very high limit makes every save of that document do more work: measured here, a 20,000-word chapter with 500 alternatives saves in about 30 ms. |
| `SCRIOBH_COMPILE_CONCURRENCY` | `2` | How many compiles (previews, exports, covers) and site builds, previews and publishes run at once on the whole server, in one shared gate (1 to 32); another waits two seconds and is then answered with a 429 "busy". |
| `SCRIOBH_COMPILE_PDF_MAX_WORDS` | `600000` | The longest book a PDF compile or preview will lay out (10,000 to 2,000,000); a longer one is refused with a message to compile a part at a time. The Markdown and DOCX exports keep their own 2,000,000-word cap. A PDF of 2,000,000 words needs about 2 GB of memory. |
| `SCRIOBH_IDEMPOTENCY_MAX_ENTRIES` | `200` | Stored idempotent responses one caller (an API key, or a person's browser session) may hold; the oldest are dropped first. |
| `SCRIOBH_IDEMPOTENCY_MAX_BYTES` | `8388608` | The most response bytes one caller's stored idempotent responses may add up to; the oldest are dropped first, and a response bigger than this is not stored. |
| `SCRIOBH_MIRROR` | `on` | `off` stops the Markdown mirror entirely (nothing is written to `mirror/`). See [The Markdown mirror](#the-markdown-mirror). |
| `SCRIOBH_MIRROR_DEBOUNCE_MS` | `2000` | How long after the last change a project's mirror is rewritten. A project is never left unwritten longer than 15 seconds while changes keep arriving. |
| `SCRIOBH_BACKUP_INTERVAL_HOURS` | `24` | Hours between scheduled backups. `0` turns scheduled backups off. |
| `SCRIOBH_BACKUP_KEEP` | `7` | How many scheduled backups to keep (minimum 1). Manual and pre-restore backups are never deleted by the application. |
| `SCRIOBH_GIT_MAX_REPO_MB` | `512` | The most a repository import's clone, or the scratch copy made when a push is refused or forced, may hold. Over it the import is refused (and a force push does not happen) with the limit named. See [Git sync](#git-sync). |
| `SCRIOBH_MCP` | `on` | `off` removes `/mcp` (it answers 404). See [MCP](#mcp-the-model-context-protocol-endpoint). |
| `SCRIOBH_MCP_ADMIN_TOOLS` | off | `1` offers the administration tools (project and Compendium membership) over MCP to a key with the `admin` scope. Never offered: key, session, Git, backup, AI, share, webhook and import operations, the purge of a whole project or Compendium, anything that takes a credential, and the author's contact block. |
| `SCRIOBH_MCP_PERMANENT_TOOLS` | off | `1` allows the tools that destroy data beyond the trash (a stash item deleted for good, a pruned history, a merged entity, a deleted comment, tag or attachment: 17) to be offered over MCP, to a key that can write, on a connection that asks with `?permanent=1` (`scriobh-cli mcp --permanent`). Everything else that deletes goes to the trash. |
| `SCRIOBH_MCP_ALLOWED_ORIGINS` | empty | Browser origins (comma separated, `https://app.example.org`) that may call `/mcp` besides the instance's own. A request without an `Origin` header always passes the origin check and still needs a key. |
| `SCRIOBH_MCP_MAX_RESULT_KB` / `_MAX_BATCH` / `_CALL_TIMEOUT_S` | `512` / `16` / `55` | The largest tool result (a bigger one is refused, never cut), the most messages in one legacy-era batch, and the longest one tool call. |
| `SCRIOBH_WEBHOOKS` | `on` | `off` stops webhook delivery and refuses to make new ones. See [Webhooks](#webhooks). |
| `SCRIOBH_WEBHOOK_ALLOW_PRIVATE` | off | `1` lets a webhook call loopback, private and carrier-grade NAT addresses, and allows plain `http://` to them. Link-local (the cloud metadata address) and reserved ranges are never reachable. Off by default: otherwise anyone who owns a project could aim the server at its own network. A receiver on your LAN needs this. |
| `SCRIOBH_WEBHOOK_MAX_PER_PROJECT` / `_MAX_TOTAL` | `10` / `200` | Webhooks a project may have, and the instance in all. |
| `SCRIOBH_WEBHOOK_RATE_PER_MIN` / `_PROJECT_RATE_PER_MIN` | `300` / `120` | Deliveries the instance sends in a minute, and the share of that one project may use (each with a burst of a sixth), so one busy project cannot starve the others. |
| `SCRIOBH_WEBHOOK_QUEUE_MAX` / `_QUEUE_PER_PROJECT` / `_QUEUE_PER_WEBHOOK` | `5000` / `1000` / `500` | Deliveries that may wait: in all (counting only those that could be sent: a webhook that is on, a project that is not in the trash), for one project, and for one webhook. An event over a cap is counted (`webhook_state.dropped`) and logged once a minute, not kept. |
| `SCRIOBH_WEBHOOK_CONCURRENCY` / `_TIMEOUT_S` / `_MAX_ATTEMPTS` | `4` / `10` / `8` | Sends in flight at once (one at a time to one webhook), the budget of one attempt, and attempts before a delivery is dead. |
| `SCRIOBH_WEBHOOK_KEEP_DAYS` | `14` | How long delivered deliveries are listed (dead ones twice as long). |
| `SCRIOBH_WEBHOOK_DEBOUNCE_MS` | `5000` | Holds a `document.updated` notification back so a burst of saves is one delivery. `0` sends at once. |
| `SCRIOBH_GIT_ALLOW_PRIVATE` | off | Set to `1` to let Git sync connect to loopback, private (RFC 1918, unique-local), carrier-grade NAT, link-local and other internal addresses. Off by default: otherwise anyone who can edit a project's remote could aim the server at its own network. A Forgejo on your LAN needs this. `scriobh config check` shows the setting. See [Git sync](#git-sync). |
| `SCRIOBH_SITE` | on | `off` turns static site publishing off: no preview, no build, no publish (the ops answer 409). On, building a zip to download works for every editor. See [Publishing a static site](#publishing-a-static-site). |
| `SCRIOBH_SITE_DIR` | empty | An **absolute** folder that static sites are published under, one `<slug>--<id6>/public` per project. Empty: publishing to a folder is off (building a zip still works). It must be **outside the data directory altogether**: not the data directory, not a folder that holds it, not one inside it (`files/` for one), not the root (symlinks are followed before the comparison). Only the administrator sets this; no request can name a folder. |
| `SCRIOBH_SITE_PUBLISH` | off | `1` publishes under `<data>/sites` when `SCRIOBH_SITE_DIR` is empty. The data directory is mode 0700, so a web server running as another user cannot read it: use `SCRIOBH_SITE_DIR` for that. |
| `SCRIOBH_GIT_USER_QUOTA_MB` | `2048` | The disk one person's repository imports may hold (clones waiting to be applied and clones kept beside their projects). A clone being made counts as `SCRIOBH_GIT_MAX_REPO_MB`. `0` turns the quota off. |
| `SCRIOBH_GIT_TOTAL_QUOTA_MB` | `20480` | The disk Git sync may hold in all: local repositories, imports and bundles. `0` turns it off. |
| `SCRIOBH_GIT_BUNDLES_KEEP` | `5` | How many bundles of overwritten remote history to keep per remote (minimum 1). |
| `SCRIOBH_GIT_KEEP_IMPORTS_DAYS` | `30` | How long the clone kept beside an imported project lives; the project is not touched. `0` keeps it forever. |
| `SCRIOBH_UPLOAD_MAX_MB` | `25` | The largest file an upload accepts (minimum 1). A larger one is refused with 413 while it is read, never stored. The request body limit of the upload routes follows it. |
| `SCRIOBH_QUOTA_USER_MB` | `2048` | What one person may have uploaded, counted after de-duplication. `0` turns it off. An upload over it is refused with `quota_exceeded`. |
| `SCRIOBH_QUOTA_PROJECT_MB` | `1024` | What one project, or one Compendium, may hold. `0` turns it off. |
| `SCRIOBH_QUOTA_INSTANCE_MB` | `0` | What the whole instance may hold in uploaded files. `0` (the default) means no cap; set it to the space you are willing to give. |
| `SCRIOBH_UPLOAD_CONCURRENCY` | `3` | How many uploads one person may have being received at once; a fourth is a 429. `0` turns the cap off. An upload that cannot fit the owner's, the person's or the instance's quota is refused before its body is read. |
| `SCRIOBH_SSE_PER_USER` | `10` | How many live-update streams one person may hold open (a browser tab holds one). `0` turns the cap off. Over it a new stream is refused with 429. |
| `SCRIOBH_SSE_TOTAL` | `200` | How many live-update streams the instance holds open in all. `0` turns the cap off. |
| `SCRIOBH_SSE_REPLAY_EVENTS` | `1000` | How many events a reconnecting stream can be caught up with (minimum 1). Past it the client is told to reload. |
| `SCRIOBH_SSE_REPLAY_HOURS` | `24` | How old the oldest replayed event may be (minimum 1). |
| `SCRIOBH_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`. Anything else is treated as `info`. |

**Planned, not implemented:** `SCRIOBH_GIT_BINARY` (Git sync uses the pure-Go go-git library and never runs a `git` program), `SCRIOBH_EMBEDDED_OIDC` (never built: `SCRIOBH_DESKTOP=1` always uses the embedded issuer), and `SCRIOBH_AI_ALLOW_PRIVATE` (the local flag is decided per provider, see [AI providers](#ai-providers-policy-and-budgets)). Setting them does nothing, and the server and `config check` warn that they are not read (a removed or planned name says so). The CLI's own variables (`SCRIOBH_URL`, `SCRIOBH_API_KEY` and `SCRIOBH_TOKEN`, `SCRIOBH_CONFIG_DIR`, `SCRIOBH_CACHE_DIR`) are described under [the agent CLI](#the-agent-cli-keys-dry-runs-and-idempotency); the server ignores them. `SCRIOBH_DEV_CLOCK_OFFSET_SECONDS` exists in development builds only, for tests.

Notes:

- `scriobh config check` loads the environment, prints the effective listen address, base URL, data directory and identity source (`not configured` when no issuer is set and the stub is off, instead of a blank), then a warning for each unknown `SCRIOBH_` name, and exits non-zero with every problem it found. It does not contact the identity provider.
- The binary's commands are `serve` (the default when no command is named), `migrate`, `healthcheck`, `config check`, `backup`, `restore`, `import`, `mentions`, `passphrase` (`passphrase clear --project ID`, with the server stopped: removes a forgotten project passphrase), `search`, `stats`, `bench`, `ops` (the op schema as JSON), `version` and `help`. `scriobh --help` and `scriobh --version` answer without starting the server. Everything but `ops`, `version`, `help` and `bench` reads the same environment as the server.
- A trailing slash on `SCRIOBH_BASE_URL` is trimmed. One on `SCRIOBH_OIDC_ISSUER` is **not**: see its row.

## Registering Scríobh with an identity provider

Scríobh signs people in only through OpenID Connect. There are no local passwords, no reset flow, and no second factor of its own; whatever your provider enforces is what you get.

What every provider needs to be told:

| Setting | Value |
| --- | --- |
| Client type | Confidential (client id and secret) |
| Grant | Authorization code |
| Redirect / callback URI | `${SCRIOBH_BASE_URL}/auth/oidc/callback`, for example `https://write.example.com/auth/oidc/callback` |
| Scopes the app requests | `openid profile email` (fixed in the code) |
| PKCE | S256. Scríobh always sends it; if your provider can require it, turn that on |
| Token endpoint auth | Client secret, as the provider's default |

What the ID token must contain:

- `sub`, `email`, and `email_verified` set to `true` (the string `"true"` is accepted too). A sign-in is refused with "your email address is not verified at the identity provider" otherwise.
- `name` (optional; the part of the email before `@` is used when it is missing).
- `groups`: a list of strings, **in the ID token itself**. Scríobh reads claims from the ID token only, not from the userinfo endpoint. Because Scríobh requests only `openid profile email`, a provider that releases `groups` only when a `groups` scope is requested will not send it; configure the provider to include groups for this client without that scope.

Groups map to roles on every sign-in:

| Group | Role |
| --- | --- |
| `scriobh-admins` | instance administrator |
| `scriobh-writers` | can create projects and write |
| `scriobh-readers` | can open projects they are invited to |

If a person belongs to none of the configured groups, the sign-in is refused with a message naming the writer and reader groups. If they belong to several, administrator wins over writer, and writer over reader. You can rename the groups with `SCRIOBH_ADMIN_GROUP`, `SCRIOBH_WRITER_GROUP` and `SCRIOBH_READER_GROUP`.

An identity is `issuer|sub`. The first time someone signs in, Scríobh links them by email only to an account that is not yet bound to an identity; an email that already belongs to a different identity is refused. Changing the issuer URL later therefore creates new accounts rather than reusing old ones.

### Passage (the maintainer's provider)

[Passage](https://github.com/crueber/passage) is the identity provider this project is developed against. In Passage, register Scríobh as an application:

1. Callback: `${SCRIOBH_BASE_URL}/auth/oidc/callback`.
2. Scopes: `openid profile email`.
3. App-scoped groups `scriobh-admins`, `scriobh-writers`, `scriobh-readers`, and put people in them. Passage emits app-scoped `groups` in the ID token.
4. Copy the client id and client secret into `SCRIOBH_OIDC_CLIENT_ID` and `SCRIOBH_OIDC_CLIENT_SECRET`, and set `SCRIOBH_OIDC_ISSUER` to Passage's issuer URL (the value its discovery document reports as `issuer`).

Check Passage's own documentation for the exact menu names and for the issuer URL of your instance.

### Any OIDC provider

The recipes below are short and written from memory of each product's general shape. Menu names and claim defaults change between versions, so **check your provider's documentation for the exact names**. The checklist that matters is the table above: callback URI, the three scopes, `email_verified` true, and `groups` in the ID token.

You can check what a provider publishes with `curl https://<issuer>/.well-known/openid-configuration`; the `issuer` value in that document is what `SCRIOBH_OIDC_ISSUER` must equal, **character for character, trailing slash included** (this was run against the development issuer: the same address with a slash added is refused). When they differ, the person sees a "Sign-in is not set up correctly" page that names `SCRIOBH_OIDC_ISSUER` and the value you set, and the server log says `sign-in is misconfigured: the identity provider's issuer does not match SCRIOBH_OIDC_ISSUER` with `configured_issuer` and `reported_issuer`. A wrong client secret is the same page (`SCRIOBH_OIDC_CLIENT_ID` and `_SECRET` named), logged with the client ID and the provider's error code.

**Authentik.** Create an OAuth2/OpenID provider and an application that uses it. Set the redirect URI to the callback as a strict match, choose a confidential client, and select the `openid`, `email` and `profile` scope mappings. Authentik's default profile mapping includes a `groups` claim; create groups named `scriobh-admins`, `scriobh-writers` and `scriobh-readers`, and add users to them. The issuer is usually `https://<authentik-host>/application/o/<application-slug>/`.

**Keycloak.** In a realm, create a client with client authentication on and the standard flow enabled, and add the callback as a valid redirect URI. Keycloak does not put groups in tokens by default: add a "Group Membership" mapper (on the client's dedicated scope or a shared client scope that is a default scope for the client), set the token claim name to `groups`, turn off full group paths, and enable "Add to ID token". Make sure users have "Email verified" set. In the client's advanced settings you can require PKCE with method S256. The issuer is `https://<keycloak-host>/realms/<realm>` (older versions prefix the path with `/auth`).

**Pocket ID.** Create an OIDC client with the callback URL, and create user groups named as above, allowing them for the client. Check whether your Pocket ID version includes a `groups` claim in the ID token for a client that does not ask for a `groups` scope; if it only does so for a `groups` scope, Scríobh cannot sign people in with the right role today.

**Authelia.** Define an entry under its OIDC clients with the client id, a hashed client secret, the callback in `redirect_uris`, and `openid`, `profile`, `email` in `scopes`. Turn on PKCE for the client (`require_pkce` with `pkce_challenge_method: S256`). Authelia groups come from your user backend. By default Authelia releases `groups` only for a requested `groups` scope; look in its documentation for how to release them in the ID token for a client that does not request that scope (a claims policy, in current versions). The issuer is the externally reachable Authelia URL.

After registering, set the three `SCRIOBH_OIDC_*` variables and the group names, restart, and sign in. A refused sign-in shows a plain message with the reason and is recorded as an `auth.denied` audit entry; an unreachable provider shows its own page and a log warning.

## Roles and the first administrator

If `SCRIOBH_ADMIN_GROUP` is its default (`scriobh-admins`), administrators come from that group and there is nothing else to do.

If you have no admin group in your provider, set `SCRIOBH_ADMIN_GROUP=` (present but empty). The first person ever to sign in is offered a one-time "claim administration" screen that names their identity. They do not need to be in any group for that first sign-in. Once they confirm, the claim is closed for good and they stay an administrator for as long as no admin group is configured. Everyone after the first person must be in the writer or reader group.

Roles are recomputed from groups at every sign-in, so removing someone from a group takes effect the next time they sign in. Existing sessions keep the role they had until then or until the session ends.

## When the identity provider is down

- The server does not contact the provider at startup, so Scríobh starts and serves normally while the provider is unreachable.
- People who are already signed in keep working: sessions live in the local database and do not call the provider again. They end at the absolute lifetime (`SCRIOBH_SESSION_HOURS`) or the idle limit (`SCRIOBH_SESSION_IDLE_MINUTES`).
- New sign-ins show a "Cannot reach the identity provider" page (HTTP 502) that names the issuer URL and offers a retry. The server logs `identity provider unreachable` with the underlying error. Discovery is retried on every sign-in attempt, so recovery needs no restart.
- There is no local bypass, by design. If the provider is gone for good, the data directory is still readable by you; see [Backups](#backups).

## Reverse proxy

Scríobh speaks plain HTTP and has no built-in TLS or certificate handling. Put a proxy in front of it, terminate TLS there, and set `SCRIOBH_BASE_URL` to the public `https://` URL.

**Share links and your proxy's logs.** A share link's token is in the path of every `/s/` request (`/s/<token>/…`). Scríobh never logs it, but a reverse proxy's access log will. Leave `/s/` out of the proxy's access log, or log it without the path, and do not send those logs anywhere that should not be able to open a link. Per-client limits and wait times on `/s/` are by the client address the proxy forwards (`SCRIOBH_TRUST_PROXY`); an IPv6 client counts as its /64.

What the app needs from the proxy:

- **Preserve the `Host` header.** State-changing requests are checked for same-origin, and the check compares the browser's `Origin` with the request's `Host`.
- **Do not strip or rewrite the path.** The app is served from the root of its origin; it has no sub-path support.
- **Send the real client address in `X-Forwarded-For`**, and only that. See the next section.
- **Allow request bodies of at least 50 MB.** The app's own limit is 2 MiB for ordinary requests, `SCRIOBH_UPLOAD_MAX_MB` (25 by default) for a file upload, and 48 MiB for an import; the proxy must not cut those first (nginx's `client_max_body_size` defaults to 1 MB, which refuses most uploads).
- `X-Forwarded-Proto` is sent by every proxy example below, but it matters in one place only: with `SCRIOBH_TRUST_PROXY` on, the Connect-an-agent page builds the `/mcp` address it shows from it. Whether cookies are `Secure`, and every other absolute link, follows the scheme of `SCRIOBH_BASE_URL`. Getting `SCRIOBH_BASE_URL` right is what matters.
- **Do not buffer `GET /api/v1/events`** (live updates, server-sent events) and keep its read timeout above 25 seconds, the heartbeat; the app sends `X-Accel-Buffering: no` for nginx, and Caddy and Traefik stream it by default. No WebSocket upgrade is needed anywhere.
- **`/mcp` needs nothing special.** It is plain `POST` with one JSON answer: no streaming, no `Upgrade`, no long-lived connection and no session affinity, so no buffering setting matters. Keep the `Authorization` header (some proxies drop it on unknown paths) and the `Origin` header (the origin check reads it), and let `MCP-Protocol-Version`, `Mcp-Method` and `Mcp-Name` through. A tool call can take up to `SCRIOBH_MCP_CALL_TIMEOUT_S` (55 s), so the proxy's read timeout must be longer than that; the server's own write timeout is 60 s.

### `SCRIOBH_TRUST_PROXY`

Off by default. When off, the client address is the TCP peer, which behind a proxy is the proxy itself: every user then shares one rate-limit bucket (600 requests per minute by default, and 30 per minute for sign-in routes) and audit lines record the proxy. So behind a proxy you normally want it **on**.

When on, Scríobh takes the **last (rightmost) entry** of `X-Forwarded-For` as the client address: the one your own proxy appended or wrote. That is only safe if nothing but the proxy can reach the app's port. Anything to the left of that entry is client-controlled and is ignored. The examples below set the header rather than append to it, which also works.

Today the flag affects only the client address (rate limiting and the hashed address stored with audit entries and sessions). It does not change cookie or link behaviour.

### Caddy

```
write.example.com {
	reverse_proxy 127.0.0.1:8080 {
		header_up X-Forwarded-For {remote_host}
	}
}
```

Caddy obtains certificates itself and preserves `Host` by default. Setting `X-Forwarded-For` explicitly with `header_up` replaces anything the client sent.

### Traefik (file provider)

```yaml
http:
  routers:
    scriobh:
      rule: Host(`write.example.com`)
      entryPoints: [websecure]
      tls:
        certResolver: letsencrypt
      service: scriobh
  services:
    scriobh:
      loadBalancer:
        servers:
          - url: http://127.0.0.1:8080
```

With Docker labels, put the equivalent `traefik.http.routers.*` and `traefik.http.services.scriobh.loadbalancer.server.port=8080` labels on the service, drop the published port, and put Traefik and Scríobh on one network. Traefik preserves `Host`. For `X-Forwarded-For`, Traefik's behaviour depends on the entry point's `forwardedHeaders` settings: as far as the Traefik documentation describes it, it replaces headers from clients it does not trust, and trusts forwarded headers only from the `trustedIPs` you list. Leave `trustedIPs` empty on an entry point that faces the internet, and confirm in the Traefik documentation for your version before turning on `SCRIOBH_TRUST_PROXY`.

### nginx

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name write.example.com;

    ssl_certificate     /etc/ssl/write.example.com/fullchain.pem;
    ssl_certificate_key /etc/ssl/write.example.com/privkey.pem;

    client_max_body_size 50m;

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_http_version 1.1;
        proxy_set_header Host $host;
        proxy_set_header X-Forwarded-For $remote_addr;
        proxy_set_header X-Forwarded-Proto $scheme;
    }
}
```

`X-Forwarded-For $remote_addr` overwrites the client's header. `$proxy_add_x_forwarded_for` also works, because the app reads the rightmost entry.

## The data directory

Everything Scríobh writes is under `SCRIOBH_DATA_DIR`. The directory is created with mode 0700 if it does not exist. It contains:

```
data/
  scriobh.db            SQLite database (the canonical store)
  scriobh.db-wal        write-ahead log; present while the server runs
  scriobh.db-shm        shared-memory index; present while the server runs
  scriobh.lock          advisory lock: one process per data directory
  secret.key            generated signing key, mode 0600; absent if SCRIOBH_SECRET_KEY is set
  mirror/               the generated Markdown copy of every project (see below)
    <slug>--<id suffix>/
    .state/  .tmp/      the mirror's own bookkeeping; not part of any project
  conflicts/<project id>/   copies of text that lost a save conflict
  mail/outbox/<message id>  the attachment of a message waiting to be sent (a Kindle edition), mode 0600; removed when the message finishes; not in backups
  sites/<slug>--<id6>/      published static sites, only with SCRIOBH_SITE_PUBLISH=1 (see Publishing a static site); not in backups
  git/<project id>/<remote id>/   the local repository Git commits are built in (see Git sync)
  git/.scratch/         throw-away fetches; empty when nothing is running
  imports/<job id>/     the clone of a repository import waiting to be applied
  imports/p-<project id>/   the clone kept as the provenance copy of an imported project
  files/projects/<project id>/editions/<sha256>.epub   the source file of an imported book, kept as received (see Importing an EPUB book)
  backups/
    git/<project id>/<remote id>-<time>-<sha>.bundle   a bundle of a remote branch, written before a force push
    <timestamp>.zip           manual backups
    auto-<timestamp>.zip      scheduled backups
    pre-restore-<timestamp>.zip   taken before a --force restore
    migrate/pre-migrate-<timestamp>.db   taken before migrations run on an existing database
```

`files/` is created when the first file is stored:

```
files/projects/<project id>/attachments/<sha256>.<ext>       uploads, named by their hash
files/compendiums/<compendium id>/attachments/<sha256>.<ext>
files/projects/<project id>/editions/<sha256>.epub            the source file of an imported book, kept as received (not an attachment: no text links to it, only owners and editors can download it, and no export, mirror or push carries it)
files/.tmp/                                                  uploads being received; swept at start
```

A file is kept once per project or Compendium, however many times it is uploaded, and is served only by `GET /files/{id}` to someone who can read its owner. Never put files in these folders by hand: a file with no row is an orphan, and one with a row but no file is a broken image. A backup holds `files/` (restore round trip is tested); `--no-files` leaves it out, and the rows then point at nothing. Purging a project or a Compendium removes its folder.

**Uploads.** Images (PNG, JPEG, GIF, WebP), PDF, plain text and Markdown are kept; what a file is comes from its bytes, not from its name or the type the client sent, and a name or type that contradicts the bytes is refused. **SVG is refused** (it can carry script), and so is anything executable. Text and PDF are served as downloads or, for a PDF and an image, inline with a restrictive Content-Security-Policy, `nosniff`, and `Cache-Control: private, no-cache`. Quotas and the size limit are in the table above; the usage is in `project.attachment.usage` and `attachment.usage` (an administrator also sees the instance total). Exports: a Markdown bundle, the mirror and a Git push carry the files the documents link to under `attachments/` (a push leaves out a file over 16 MB and says so in the manifest it publishes).

**Live updates.** `GET /api/v1/events` is a long-lived response per open page. Behind a reverse proxy it must not be buffered (`X-Accel-Buffering: no` is sent for nginx; Caddy and Traefik stream by default) and the read timeout must be longer than the 25-second heartbeat. The streams are counted by the two `SCRIOBH_SSE_*` limits above and are ended when the server shuts down.

The lock file is how Scríobh enforces one writer: a second process pointed at the same directory exits with `another Scríobh process is already using <dir>`. Do not run two containers on one volume, and do not mount the volume read-write anywhere else while the server runs.

The database file is mode 0644 inside the 0700 directory, so access is controlled by the directory. Do not loosen the directory's permissions.

In [desktop mode](#desktop-mode) the directory also holds `desktop/issuer-key.json` (the embedded issuer's signing key, mode 0600 in a 0700 folder; made again when it is missing, and not in backups).

## Desktop mode

`SCRIOBH_DESKTOP=1` is how the desktop application runs this same binary: as a sidecar on a loopback port, with one local profile and no identity provider to configure. It is meant to be started by the shell, not by hand; this section says what it does so that a person who runs it by hand, or reads its log, knows what they are looking at. The protocol between the shell and the binary is [p8-notes/desktop-contract.md](p8-notes/desktop-contract.md); who can reach the port and what stops them is [p8-notes/desktop-threat-model.md](p8-notes/desktop-threat-model.md).

**Starting.** Desktop mode needs `SCRIOBH_LAUNCH_TOKEN` (at least 32 hexadecimal characters) and listens on `127.0.0.1:0` unless `SCRIOBH_LISTEN` names another literal loopback address. It refuses to start with `SCRIOBH_BASE_URL`, `SCRIOBH_OIDC_STUB`, `SCRIOBH_OIDC_ISSUER`, `SCRIOBH_OIDC_CLIENT_ID`, `SCRIOBH_OIDC_CLIENT_SECRET`, `SCRIOBH_TRUST_PROXY` or `SCRIOBH_ADMIN_GROUP` (naming a group) set, and `config check` says which. It prints `LISTEN http://127.0.0.1:<port>` on stdout. A second process on the same data directory prints `ALREADY_RUNNING` and exits with status 75 (outside desktop mode it exits 1, as it always has). `SIGTERM` or `SIGINT` stops it cleanly, status 0, and so does the end of its standard input when that was a pipe at start (the shell holds the pipe, so the server never outlives it; started from a terminal this does not apply).

**The port.** With port `0` the server binds the port it used last time (remembered in `desktop/port` in the data directory) and picks a new one only when that port is taken, with a line in the log. The address matters to the browser: the unsaved text the writing view keeps in the browser while a save is failing, and the offline shell, are kept per address. If the port changes, those local copies are not offered at the new address; everything the server saved is unaffected. Only `127.0.0.1:<port>` is answered: a request for `localhost:<port>` or any other name gets 421.

**Where the data is.** With `SCRIOBH_DATA_DIR` unset: `~/Library/Application Support/Scriobh` on macOS, `$XDG_DATA_HOME/scriobh` or `~/.local/share/scriobh` on Linux, `%AppData%\Scriobh` on Windows; created with mode 0700. Everything in [The data directory](#the-data-directory) applies. Back it up as you would a server's (`scriobh backup` with `SCRIOBH_DESKTOP=1` set, or with `SCRIOBH_DATA_DIR` naming the folder); the application's administrator page shows the path.

**Signing in.** The window opens `/desktop/start?t=<token>`; the token works once and is exchanged for a cookie that only that window holds. The first run asks for a name and, optionally, an email address: the local profile. There is no password. From then on opening the application signs that person in. Sign-in is ordinary OpenID Connect against an issuer the binary serves to itself at `/desktop-idp/`; a browser tab pointed at the port is shown the sign-in page and cannot sign in. The profile is the instance's administrator.

**The secret key.** The shell passes `SCRIOBH_SECRET_KEY` from the system keychain when it has one. Without it the key file `secret.key` in the data directory is made and used, as on a server.

**Signing in through an organisation's provider instead.** *Account menu, This computer.* Enter the provider's issuer URL, client ID and client secret (kept sealed); register `http://127.0.0.1:<port>/auth/oidc/callback` at the provider as the redirect address (most providers accept any loopback port for a native application; for one that does not, set a fixed port on the same page). Then prove the provider: press *Make a browser address*, open the address it shows in your web browser (the application's window does not open another site's page), and sign in at the provider there; the address works once, for five minutes. Back in the application, reload the page, check the name it says you signed in as, type the issuer URL to confirm, and quit and reopen the application. Your account, with everything in it, is now the one you signed in to the provider with. Nothing is matched by email address. **From then on you sign in from a web browser, not from the application's window:** the provider's sign-in page is another site, which the window does not open. The window shows the address to use (`http://127.0.0.1:<port>`); keep the application open while you work. To go back: the same page, type the local profile's name, quit and reopen.

If the provider is gone and nobody can sign in to go back, stop the application and run, with the same data directory:

```
SCRIOBH_DESKTOP=1 scriobh desktop use-local-profile
```

The next start signs in through the local profile again, as the account that was moved.

**A data directory restored from a server.** A backup of a server instance, restored into a desktop data directory, has accounts that belong to the server's identity provider and none for the local profile, so the window is refused: the sign-in page says the data came from a server and names the command below. Nobody is promoted and nothing is matched by email address. With the application stopped, run, with the same data directory (`SCRIOBH_DESKTOP=1` or `SCRIOBH_DATA_DIR`):

```
SCRIOBH_DESKTOP=1 scriobh desktop use-local-profile --adopt-admin <email-or-id>
```

`<email-or-id>` is the exact address or id of one **administrator** account in the restored data (`sqlite3 scriobh.db "SELECT id, email FROM users WHERE role = 'admin'"` lists them). The local profile becomes that account: its projects, role and history are unchanged, its browser sessions from the server end, and `desktop.profile.adopt` is recorded in the audit log. The command refuses a name that matches no account or more than one, a writer or reader, an agent, a switched-off or removed account, a local profile that already has an account, and an instance with a switch waiting or on an external provider (the plain `use-local-profile` settles those). Run it only on a copy of data you mean to open in the application; a restore from a server leaves its mail, remotes and triggers paused (see Restore).

**A model on this computer.** *This computer, A model on this computer.* The button makes one request to Ollama on `127.0.0.1:11434` and lists its models; choosing one adds an AI provider marked local. Nothing is asked until the button is pressed, and nothing is ever downloaded. AI stays off until you switch it on ([AI providers: quick path](#ai-providers-quick-path)).

**What leaves the computer.** With AI off, no Git remote, no mail relay, no webhook and no external provider: nothing. `GET /api/v1/desktop/info` (the administrator's session) reports how many connections the process has opened and how many went anywhere but this computer.

**What was not verified** is listed in [p8-notes/desktop-go.md](p8-notes/desktop-go.md): everything with the shell, a real keychain, Windows, and a real external provider.

## The Markdown mirror

Scríobh keeps a generated, readable copy of every project under `data/mirror/`, so `grep`, `git`, `rsync` and a text editor always see the work. It is written a couple of seconds after each change, one file at a time (written beside the file and renamed into place, so you never see half a file), and only the files that changed are touched. The database stays canonical: **edits made in `mirror/` are never read back** and are overwritten by the next change to that project.

Each project has its own folder named `<slug>--<six characters of the project id>`, because two people may both have a project called "The Salt Road". Inside: `project.json` (the project's details and, for every document, its path, title, status, synopses, point of view, location, story dates, word target, whether it is compiled, and a hash of its text, which is what lets a clone from Git or a re-import rebuild the outline), `README.md`, `manuscript/` (numbered in reading order; a part or chapter with children becomes a folder, with its own text in the file that starts with `00`), and `notes/`. Documents in the trash are not mirrored. Text is written exactly as stored, including the comment blocks Scríobh keeps in it.

If a write fails (disk full, permissions), saving is not affected: the error is logged, the write is retried with a longer wait each time (5 seconds up to 5 minutes), and `mirror.status` for the project shows the error and when the next attempt runs. At every start Scríobh checks each project against what it last wrote and rewrites any mirror that is missing or out of date, so deleting `data/mirror/` is safe: it comes back. `mirror.rebuild` (project editors and administrators) compares every file with what is on disk and repairs the ones that differ. When a project is deleted permanently its folder goes with it. `SCRIOBH_MIRROR=off` turns the mirror off.

A project that uses a Compendium (its primary, one it references, or a single linked entity) has a read-only `compendium/` folder inside its mirror: one Markdown file per entity with the project's overrides applied, so the folder is readable on its own. **It includes everything the project uses, whether or not each collaborator may read it in Scríobh**: it is a projection of what the project's owner wired in, for whoever can read the mirror folder. It is not part of what a Git push sends today (the push sends the manuscript, the notes, the attachments and `scriobh.json`; see [The bible and Git sync](#the-bible-and-git-sync)). A Compendium that no project uses as its primary has a folder of its own, `compendium-<slug>--<id suffix>/`, and it is removed when a project takes the Compendium as its primary (the entities then appear in that project's `compendium/`) and when the Compendium is deleted. A first write of a 5,000-entity Compendium is 5,000 files, each synced before it is renamed into place, and takes about half a minute on a laptop disk; later changes rewrite only the files that changed. `mirror.compendium.rebuild` repairs a Compendium's folder.

The Compendium's `timeline.md` (in a standalone Compendium's folder and in every project's `compendium/`) is the timeline in story order, written like every other file: deterministic, and rewritten only when an event, a scene link or the book's own order changes. `project.json` is schema version 3: it adds `pov_entity` and `location_entity` (the id and the book's name of the entity a scene's point of view or location is linked to; absent when none is). The first start after the upgrade rewrites every project's `project.json` once, and nothing else.

**Mentions.** Which entities each document names (the "in this scene" sidebar, first appearance, "appears in") is an index kept in the database, derived from the stored text, and always safe to throw away. Every save keeps its own document's rows current inside the same transaction. Changes to the entities (a new character, an alias, a rename, an override, a merge, a Compendium trashed or restored) make the projects that can see them owe a rescan; a background worker does it: it reads the text outside the single writer, matches it against the project's names, and writes only the rows that changed, in chunks that hold the writer for about a tenth of a second at most, so saving is never starved. Mention reads say `indexing: true` while a scan is owed. After an upgrade the worker builds the index for every existing project once (recorded in `settings` as `mentions.index`). `scriobh mentions rebuild [--project ID]` rebuilds it offline (stop the server first); `mention.rebuild` does one project from the API (project owner, with `dry_run`). Measured on a laptop with a 60,000-word project and 500 entities, a full rebuild takes about 40 ms and the longest writer hold of a chunk was about 12 ms (the numbers are in OPEN-QUESTIONS).

## Backups

**Take one.** `scriobh backup` writes a consistent archive to `data/backups/<timestamp>.zip` (or to the file named by `--out`; it never overwrites). It works while the server runs and when it does not, and saving is not held up. `--no-files`, `--no-mirror` and `--no-conflicts` leave a tree out (the mirror can always be regenerated, so `--no-mirror` is a reasonable choice for a small archive). Administrators can do the same from the API with `backup.create`, list archives with `backup.list`, and download one by id with `backup.download` (archives over 256 MB are not served that way; copy them from `data/backups/`).

```
docker compose -f compose.prod.yaml exec scriobh /scriobh backup    # in the container: /data/backups/<timestamp>.zip
scriobh backup --out /mnt/offsite/scriobh-2026-11-12.zip            # a binary install (reads the same environment as the server)
```

To get an archive out of the container's volume: `docker compose -f compose.prod.yaml cp scriobh:/data/backups/<file>.zip .` (the image has no shell, but `cp` works on a stopped or running container), or mount a host directory for backups. `--out` never overwrites an existing file, and creates the folder if it is missing.

An archive holds: `scriobh.db` (a consistent copy of the database), `files/`, `mirror/`, `conflicts/`, and `manifest.json`, which lists every other entry with its size and SHA-256, the migrations applied, and how many projects and documents the database holds.

**Scheduled backups** run inside the server: every `SCRIOBH_BACKUP_INTERVAL_HOURS` (default 24) it writes `data/backups/auto-<timestamp>.zip` and keeps the newest `SCRIOBH_BACKUP_KEEP` (default 7) of those. The first runs a minute after the server starts, unless one was made less than an interval ago. Set the interval to `0` to turn them off. Scheduled backups live on the same disk as the data: they protect you from a bad edit or a bad upgrade, not from losing the disk. **Copy archives somewhere else** (another machine, object storage, an external drive) with whatever you already use. Manual backups and the pre-migration copies in `backups/migrate/` are never deleted by Scríobh; clear them out yourself.

**The secret key is not in the archive, on purpose.** `secret.key` (or the value of `SCRIOBH_SECRET_KEY`) signs the sign-in flow and seals the credentials Scríobh keeps: Git deploy keys and tokens, AI provider keys, and each webhook's signing secret and the path of its address. An archive that held the key would put every credential and the means to read it in one file. Back the key up separately, somewhere other than where the archives go. Restore never creates, copies or replaces a key. If there is none, the server makes a new one at the next start.

What a restore with a different key costs. The writing, history, files, comments, share links, API keys and sessions are not affected: none of them depends on the key. The sealed credentials cannot be opened, and `scriobh restore` says which, one line each:

- **Git remotes**: the credentials are cleared at restore and the remote is switched off with the reason. Putting the right key back afterwards does not bring them back; enter them again, or restore the archive once more with the right key in place.
- **AI providers**: the keys are kept. A test or a run says "The stored key for this provider cannot be read with this installation's secret key. Enter the key again." Putting the right key back before the next start makes them work again.
- **Webhooks**: kept, and paused by the restore whatever the key (see [Webhooks](#webhooks)). Switched on again, each is switched off at its next delivery with the reason ("The signing secret cannot be read ..."). Rotate the secret (and set the address again) and switch it on to use it, or put the right key back first and just switch it on.

A restore into a directory with **no key at all** (no `secret.key` and no `SCRIOBH_SECRET_KEY`) clears nothing: `scriobh restore` says the credentials could not be checked. Put the original `secret.key` in the data directory (or set `SCRIOBH_SECRET_KEY`) before the first start and everything works. If the server starts first, it creates a new key, and the sealed credentials cannot be read with it; they are kept as they are. Putting the original key back and restarting makes them readable again but does **not** switch anything on: a restored Git remote, trigger, webhook, mail, Pandoc and semantic index are paused by the restore itself, and stay off until an owner or administrator switches each on. (The restore rehearsal covers the same key, a different key, no key, and the original key put back at the start; it checks that a remote stays off.)

**Sessions, API keys and share links come back as they were when the archive was made.** They are stored as hashes, so a restore keeps people signed in. It also means that a session, key or share link revoked *after* the archive was made is live again after restoring it: when you restore an older archive, revoke again what you revoked since: sessions and API keys on the Settings page (or `session.revoke` and `key.revoke`), share links on each project's sharing page (`share.revoke`).

**Backups are not encrypted, and neither is anything else.** Scríobh does not encrypt manuscripts, the database, exports, or backups (`docs/OPEN-QUESTIONS.md` §15). Whoever can read the data directory or an archive can read every manuscript. Protect the volume and the archives with whatever you already use: full-disk encryption, LUKS, ZFS native encryption, or an encrypted backup tool. The interface will not imply otherwise.

> The `docker compose` lines here follow the image (entrypoint `/scriobh`, data in `/data`) but, like the rest of the container path, have not been run. The same commands against a binary install were run for this guide (backup beside a running server, restore into a new directory, restore refused over existing data and while a server runs) and are covered by the Go tests.

### Restoring

`scriobh restore <archive>` puts an archive back. Stop the server first: the command takes the data-directory lock and refuses with `another Scríobh process is already using <dir>` while a server (or another restore) holds it.

```
docker compose -f compose.prod.yaml stop scriobh
docker compose -f compose.prod.yaml run --rm scriobh restore /data/backups/auto-2026-11-12T03-00-00.zip --force
docker compose -f compose.prod.yaml start scriobh
```

What it does, in order, and what it refuses:

1. Reads `manifest.json` and checks it against the archive in both directions: every listed file must be there, and every file must be listed. It refuses names that are absolute, contain `..` or a backslash, sit outside `scriobh.db`, `files/`, `mirror/` and `conflicts/`, or are links or devices.
2. Extracts into a staging folder next to the data, checking each file's size and SHA-256 as it is read and stopping if the archive expands past 64 GiB. Nothing live is touched yet.
3. Opens the staged database, applies any migrations an older archive needs, checks its integrity, and compares its project and document counts with the manifest. An archive made by a **newer** build than the one you are running is refused (`... that this binary does not know`): upgrade first.
4. If the data directory already holds data it refuses unless you pass `--force`; with `--force` it first writes `data/backups/pre-restore-<timestamp>.zip` of what is there. If the current database is damaged and cannot be archived (the usual reason to restore), it goes on anyway: what is there is moved aside whole into `data/.restore-old-<timestamp>/` and the output says so. An archive made with `--no-files`, `--no-mirror` or `--no-conflicts` replaces only what it carries: the trees it left out stay as they are.
5. Swaps the database, `files/`, `mirror/` and `conflicts/` in, undoing the swap if a step fails, and records an audit line. `secret.key` is never touched.

**What a restore pauses.** Everything that can send something to an outside party comes back switched off and with its queue emptied, so a restored copy, on this machine or a second one, contacts nobody until a person switches each thing on again: **Git remotes** (a public mirror included) and their **triggers**, **webhooks** (the feeder parked at the newest event), **mail** (the relay off, the outbox and Kindle previews emptied; digest opt-ins and Kindle addresses kept), the **semantic index** of every project (off with the reason "restore"; embeddings whose document text differs from the backup's, or made under older rules, are dropped, the rest kept), and the **Pandoc bridge** (off, its path kept; the settings page does not run the program until it is switched on again). A project passphrase, the editions of imported books with their stored source files, the presence preference and typography come back as they were. These pauses are made **in the restored database before any file is installed**, so the files reach the data directory already paused: pressing Ctrl-C, killing the process or losing power at any later moment cannot leave a sender on. The steps are then run once more on the installed database, which is also where the markers below are removed.

If a pause could not be made, or the restore was interrupted after the install, the command prints what happened, lists **every** sender under `NOT paused` with the statement that pauses it by hand (`sqlite3 scriobh.db "..."`, with the server stopped), exits non-zero, and leaves a marker, `restore.pending`, in the data directory (mail and the semantic index also leave their own, `mail.restore-pause` and `semantic.restore-pause`). **While the marker is there the server refuses to start**: it runs every pause first and, if any fails, prints the same list and exits, so nothing sends. `scriobh restore --finish` (server stopped) makes the pauses now, removes the marker once every pause succeeded, and exits non-zero with the list when one still fails. A second restore over an interrupted one (`--force`) ends clean. If the restored database cannot be opened to finish (it is tried twice), the same marker is left and the message says so.

When it finishes it prints how many projects and documents came back, the pre-restore archive, a note about the secret key and a line for each of the pauses above that changed something (see also [Webhooks](#webhooks), [Mail](#mail) and the sections on Git sync, semantic search and Pandoc: each is switched on again by its owner or an administrator from its own page). Start the server: the mirror is checked and rewritten where needed.

### Restore drill

Do this once on a scratch directory before you need it, and again after any change to how you store archives. It reads the archive and writes only to the scratch directory.

```
export SCRIOBH_DATA_DIR=/tmp/drill && rm -rf "$SCRIOBH_DATA_DIR"
scriobh restore /path/to/archive.zip
```

`restore` needs no identity provider and no secret key (it reads only `SCRIOBH_DATA_DIR`, and `SCRIOBH_SECRET_KEY` if you set it); it prints how many projects and documents came back and the made-at time of the archive. Without the original key it also prints notes that stored credentials "could not be checked": that is expected for a drill and means nothing is wrong with the archive. (This was run: an archive of a running server was restored into a directory that did not exist yet, and a second restore without `--force` was refused with the data untouched.) Check those against what you know the book holds. Then look at what was restored without starting anything: `/tmp/drill/mirror/` is the Markdown copy as it stood when the archive was made (open a recent chapter and read its last paragraph), and, if you have the `sqlite3` shell, `sqlite3 /tmp/drill/scriobh.db 'select title from projects'` lists the projects. If the counts and the last paragraph are right, the archive is good. A drill never sends anything: `restore` only writes files, and if you do start a server on the restored directory, everything that sends is already paused (the output of `restore` has a line for each: Git remotes and triggers, webhooks, mail, the semantic index, Pandoc), so that copy contacts none of your receivers, relays, repositories, embedding endpoints or programs until someone switches each on again. To rehearse a real second machine, leave them paused.

Starting a server on the restored data is a second, optional step, and which form you can use depends on the build. With a **development build** (`SCRIOBH_OIDC_STUB=1 SCRIOBH_LISTEN=127.0.0.1:0 scriobh`) it prints `LISTEN http://127.0.0.1:<port>` and you can sign in through the built-in issuer and open the project. A **production build** has no development issuer: it needs your real `SCRIOBH_OIDC_*` settings and `SCRIOBH_BASE_URL`, and the identity provider only accepts the callback of the registered address, so a scratch server is not reachable for sign-in. In a real recovery you restore into the real data directory and start the normal service. The repository rehearses the whole path with a development build: `scripts/restore-rehearsal.sh` (no network; a minute or less) fills an instance through the API with projects, documents, revisions, an image, a cover layout (front and back pictures), an alternative, comments, share links, API keys, a Git remote with a token, an AI provider with a key, a webhook, site options and a goal; backs it up while the server runs and again with it stopped; restores it into a directory that does not exist yet with the same `SCRIOBH_SECRET_KEY`; and checks every table row for row, the stored files, the documents and revisions through the API, search, the attachment's bytes, the cover (its layout and pictures come back; the pictures made from them are not in the archive, are absent from the restored directory, and are made again with the same bytes), a mirror written again from nothing, byte for byte, and each credential used for real against stand-ins on loopback. It then restores with a different key and checks that the writing is all there and that each credential refuses with the messages above. `go test ./cmd/scriobh -run 'BackupAndRestore|RestoreRehearsal'` runs the same tests. A second rehearsal (`TestRestoreRehearsalEveryFeature`, run by the same script) restores an instance with every feature that sends anything switched on and something waiting at each outside party, on a different path, and checks that every table but the paused ones is identical, that a server started on the copy reaches none of the loopback stand-ins and switches nothing on, that a restore without the key followed by the original key at the start switches no remote on, and the failure paths above (a pause that cannot be made, a restore ended after the install, a second restore over it).

If the server will not start after a restore because the archive came from a newer build, restore the pre-restore archive (`scriobh restore data/backups/pre-restore-<timestamp>.zip --force`) or upgrade the image and restore again.

## Git sync

A project can push its Markdown to a repository you own, on Forgejo, GitHub or any server that speaks Git over SSH or HTTPS. **Push-only is the contract**: Scríobh writes to the repository and never reads from it. It does not pull, merge or rebase, and the repository is not a second way to edit the book. Two Scríobh instances do not share a database through it. The repository is a readable, versioned copy: for backup, for reading elsewhere, for CI.

Only the project's **owner** (and administrators) can add, change or remove a remote, generate its key, accept its host key or acknowledge a finding of the pre-push check, because those decide where the manuscript and the credential go. Editors can see the status, test the connection, push now and read the history.

It needs the instance's secret key (`SCRIOBH_SECRET_KEY`, or the key file Scríobh creates), because the deploy key or token is stored encrypted with it. Without a key no credential is stored and the settings say so. Back up the key separately from the data, as for [backups](#backups).

Git sync is built on the Markdown mirror: with `SCRIOBH_MIRROR=off` there is nothing to push, and the push says so.

### Set up a remote (SSH deploy key, the usual way)

1. Create an **empty** repository on the host. A repository that already has commits on the branch makes the first push be refused (see below); use another branch, or force.
2. In the project's Git settings add a remote with the repository's address (`ssh://git@host/owner/repo.git` or `git@host:owner/repo.git`). Scríobh generates an ed25519 key for it. Its private half is stored encrypted and never leaves the server; the public half is shown for you to copy.
3. Add the public key to the repository as a **deploy key with write access**.
   - **Forgejo**: repository, Settings, Deploy keys, Add deploy key; tick *Enable write access*. If SSH runs on a port other than 22, put it in the address: `ssh://git@host:2222/owner/repo.git`.
   - **GitHub**: repository, Settings, Deploy keys, Add deploy key; tick *Allow write access*. A deploy key belongs to one repository.
4. **Accept the host key.** Scríobh connects, authenticates nothing, and shows the host's key fingerprints. Compare one with what the host publishes (GitHub lists theirs in its documentation; for your own Forgejo, ask whoever runs it, or run `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub` on the server) and accept it. Scríobh asks again for that exact key and pins it. There is no "trust anything" option.
5. **Test connection** reads the repository's refs with the stored credentials and the pinned key. It reports a typed result naming the host and the key, and whether the repository is empty.

The presets only fill in the address shape and say where the key goes. They carry no host key, because one that cannot be checked here must not be trusted.

An **HTTPS token** is the fallback for hosts that do not allow deploy keys: enter the address (`https://host/owner/repo.git`, with no credentials in it), the user name and the token. The token is stored encrypted, is never shown again and never appears in a log, an audit entry, an export, the mirror or a push.

### The bible and Git sync

The Markdown mirror holds a `compendium/` projection of the bible for every reader of the folder, whatever each person's access to the Compendium (the mirror is a server-side copy, protected by the volume). **The push does not publish it**: it sends the manuscript, the notes, the attachments the text links to and `scriobh.json`, and nothing under `compendium/` (a manifest that listed such a file would be refused, without naming it). What the bible does reach the repository through is the manifest: each scene's POV and location entity (id and name), exactly as the mirror's `project.json` has them, to whoever can read the repository, whatever the pusher's access to the Compendium. Pushing is the owner's decision. Findings of the pre-push check, push errors and the push history only ever name manuscript and note files, titles and counts, never an entity, and the Git ops hold no hash of a bible file (the one hash that covers bible names is that of `scriobh.json`, which carries the POV and location entities, and it is a hash of the whole manifest, which changes with every save).

### Which addresses Scríobh will connect to

Git sync connects only where the administrator has not forbidden: loopback, private, link-local (which holds the cloud metadata address), carrier-grade NAT and other internal addresses are refused, whatever the name or redirect that leads there, and the check is made on the address actually connected to (a name that resolves to an internal address is caught; so is a DNS answer that changes). This applies to pushes, tests, host key scans and imports. A remote on your own network (a LAN Forgejo) needs `SCRIOBH_GIT_ALLOW_PRIVATE=1`. What a person is told when an address is refused or unreachable is deliberately generic (it does not say what was tried or why it failed); the detail is in the server log at debug level. Connections never use `HTTP_PROXY`, `HTTPS_PROXY` or `ALL_PROXY` (the last is cleared at start, with a warning): a proxy would be the address checked, not the target. Responses are bounded (a refused request's body is cut off at 64 KiB, a successful one at the clone size cap), redirects are limited to three, to https and to the same host and port (a move within the host is followed, a redirect to another origin is refused), and idle connections end after a minute.

A credential is bound to the address it was entered for. Changing a remote's address (scheme, host, port or path; a rewrite of the same address with another case or a trailing `.git` does not count) drops the stored token or deploy key, its public half and the accepted host key; they are entered again for the new address. A stored credential also does not open if its row is altered in the database.

### What is pushed, and when

The repository gets `scriobh.json` (the mirror's `project.json`), `manuscript/` in reading order and, unless switched off, `notes/`; with a **folder** set, all of it goes under that folder. Nothing else: no database, sessions, keys, comments, revisions or AI material, and not the mirror's README or any stray file in the mirror folder. A push is built from a consistent snapshot of the mirror, taken in one step, so a save during a push cannot produce a half-written commit.

One commit per push, never one per save:

- **On save** (the default): 30 seconds after the writing stops, and never later than five minutes after the first unpushed change, however steadily you write.
- **Interval**: every 5 to 10080 minutes, if anything changed.
- **Manual**: only when you press *Push now*.

Commits are the same for the same text: the author is the neutral `Scríobh <noreply@scriobh>` unless the owner sets a name and email on the remote (the default names no one: the repository may be public, and its commits are forever), with no host name; the message is `The Salt Road · ch 12: +214 words` with a body listing what changed, in a fixed order. A push with nothing new makes no commit and, from an automatic trigger, no record.

Before anything leaves the server the text, the document titles (they are in commit messages) and the file names are checked for private keys (a header with key material after it), API keys (`sk-…`), bearer tokens, cloud and forge tokens and passwords in addresses. Documented examples and obvious placeholders (`AKIAIOSFODNN7EXAMPLE`, `sk-proj-xxxx…`, `user:password@host`) and a key header quoted with no key after it (a thriller) pass. If something real-looking matches, nothing is pushed and the file and the SHA-256 of what matched are shown (never the text). The next save that changes the text looks again by itself; saves that leave the offending file alone do not write another refusal. If it is not a secret, the project's owner can **acknowledge that exact file**: `git.remote.secret_ack` names the file and the hash, is audited, and covers only that content, so a changed file is checked again.

### When the push does not go through

- **The remote is unreachable** (no network, the host is down): the commit is kept locally, the remote shows *behind*, and Scríobh tries again with a growing wait (1, 2, 4 … up to 30 minutes), only on its own schedule. Nothing is lost.
- **The host refuses the credentials, or the repository is not found** (the deploy key was removed or has no write access): a typed error names the host and the key, the commit is kept, and the remote shows *behind*. Scríobh tries again on the same growing wait, so fixing the key at the host is enough; *Test connection* and *Push now* do not have to wait for it.
- **The host key changed**, credentials cannot be read, or a secret was found in the text: the remote is parked (*blocked*) and Scríobh does not try again by itself. For a changed host key nothing is sent. If you know why (a reinstalled server), compare the new fingerprint with the host's owner, scan, and accept it again, which must be confirmed as a replacement. Until then the remote stays blocked. A *Test connection* that works lifts a block (not one for a secret in the text).
- **The remote has commits Scríobh did not make** (someone pushed to it, or it was not empty): the push is **refused** and recorded as *rejected* with the remote's head and a list of its commits that are not ours. Nothing on the remote is changed and nothing is merged. The remote is parked until you act. The two ways out:
  - **Force push** replaces the remote branch with this project's history. Only the project's owner can do it; you type the remote's name to confirm; Scríobh first fetches the remote branch into a scratch folder and writes **a bundle of it to `data/backups/git/`**, reads the bundle back and checks it, and pushes only then. If the bundle cannot be written (the disk, the size limit) nothing is pushed. The push is lease-protected: it fails if the branch moved after the bundle was made. The bundle is a standard Git bundle: `git clone data/backups/git/<file>.bundle recovered` brings the overwritten history back. Bundles are in `data/backups/git/<project id>/`, not in backup archives; the newest `SCRIOBH_GIT_BUNDLES_KEEP` (default 5) of each remote are kept and older ones removed, and a purge of the project removes the folder. The bundle is the fetched pack streamed behind a header, checked back by reading it (object count and declared sizes are capped, so a branch that is tiny on disk and huge inflated is refused rather than loaded).
  - **Import** the repository into a new project (below), and decide what to keep by hand. There is no merge and no "keep both".

If the data directory is restored (without `data/git/`, which backups leave out) and the remote still holds exactly the last commit Scríobh recorded, that one commit is fetched back so the history continues. If the remote holds anything else, the push is refused as above.

A restore **pauses every remote and every trigger**, before any other step can fail (every step runs, and their errors are reported together): `scriobh restore` switches them off with the reason `paused_by_restore` and a `restore_paused` mark that nothing but the owner switching the remote on clears (not the start-up credential check, not a key coming back, not a connection test), removes the trigger deliveries that were waiting, and says how many it paused, as it does for webhooks. A copy that was restored on a second machine (or after a loss) must not push the old copy's text to a repository, a public mirror included, or call a CI service, until the owner has decided that this is the server in charge: each remote is switched on again from the project's Git settings (`git.remote.update` with `enabled`, owner or administrator), each trigger with `git.trigger.set`. A remote that was already off for another reason (credentials that the key cannot read) keeps that reason.

After restoring with a **different secret key**, `scriobh restore` clears the Git credentials the key cannot read and switches those remotes off, and says so; enter the credentials again. If it finds no key, the server checks at its next start: remotes whose credentials cannot be read are switched off but kept, so putting the original `secret.key` back and restarting brings them back.

### Import from a repository

*Import* in the library (or `import.git_preview` and `import.git_apply`) creates a new project from a repository: an https address (a public repository needs nothing else, a private one a token that is used once and not stored), or the remote of one of your projects. It makes a shallow clone into `data/imports/` on a background worker, refuses repositories over `SCRIOBH_GIT_MAX_REPO_MB` (default 512), and recognises exactly two layouts: a Scríobh repository (`scriobh.json`) and a generic folder of Markdown files. You see the proposed tree, the word counts and what will be skipped, and edit the mapping, before anything is written. The project is created in one transaction with the address, the commit and the hash of every file recorded as its source, and the clone is kept next to it. An import never touches an existing project and a clone is never made into the mirror. An import you do not apply is removed after a day, and the clone kept beside an imported project is removed after `SCRIOBH_GIT_KEEP_IMPORTS_DAYS` (default 30). Each person's imports are held to `SCRIOBH_GIT_USER_QUOTA_MB` and all Git copies to `SCRIOBH_GIT_TOTAL_QUOTA_MB`; at most five imports are open per person. Importing through a project's remote (which uses its stored credentials) is for the project's owner.

### What a remote may carry: public and private

Each remote has an **audience**. A **private** remote gets what a remote always got: the manuscript, the notes (unless switched off), each document's alternatives and ghosted text, the outline's details in `scriobh.json`. It is for a backup in a repository only you can read. A **public** remote gets what a reader of the book sees, by the rules the static site and the compile use, and nothing else:

- no notes, whatever `include_notes` says; a public remote cannot have notes on;
- no document marked *do not compile*, and nothing inside one; optionally (`only_done`) only documents whose status is *done*, with everything inside a document that is not;
- each document's text is the chosen wording: the alternatives comment is gone, ghosted text is gone, `%%private notes%%`, HTML comments, front matter and footnotes only a private note refers to are cut. A text the parser cannot place is **withheld**, never pushed as it is;
- `scriobh.json` is rebuilt from an allow-list: project title, subtitle, author, kind and language, and for each pushed document its path, kind, title, word count (of the text that is pushed) and hash. The mirror's synopses, point of view and location (and the entity names the bible gave them), story dates, targets, ids, statuses and an attachment's alt text are not in it;
- an attachment is pushed only if a pushed document links to it;
- the secret check runs over what is pushed, not over what is left out.

The database is asked as well as the mirror: a document that the database says is out is out even when the mirror has not caught up with the change. **The default for a remote made now is public**, the strictest; set `audience` to `private` for a backup. A remote that existed before Phase 7 stays private: it was already pushing the whole mirror, and the upgrade changes nothing about what it pushes.

**A public remote is built only from a current mirror.** Before a public push (any trigger: save, interval, by hand, force, the first push of a publish) and before the plan or the Markdown edition, Scríobh asks the mirror whether it was written from the state the database is in now (nothing pending, and the fingerprint of every document's metadata and text the same). If not, the push is refused with `mirror_behind` and tried again on the usual wait: text just made private, a document just marked do not compile, or just trashed is never published from a mirror that has not caught up. On top of that, a document the mirror and the database disagree about is left out of the view (`stale_mirror`), and a trashed document and everything under it is out (`trashed`). A private remote is not held back.

Changing a remote's `audience` or `only_done` **starts its local history over**: the earlier commits may hold more than the new setting allows, and they must not be carried forward into a repository people can read. The next push is then refused as "the remote has commits this project did not make" (the branch already has the old history), and the ways out are the usual ones: force push (typed confirmation, a bundle of the remote's branch first) or a new repository. Making a public repository private at the host does not undo what was already read there.

### Who may widen what reaches a public repository

A person signed in with a browser only. An API key of any scope (and so the CLI and MCP) is refused when it tries to change a remote's `audience`, `include_notes`, `only_done` or address, or to force-push it, if the remote is **exposed**: its audience is public, `repo.publish` made it, or its address is one the project has ever given a public remote (`git_public_addresses`, which outlives the remote, so deleting a public remote and making a private one at the same address, or re-pointing a private remote at that address, is the same refusal). A key may still change the push mode, the branch, the folder and the like. A person may widen it, and the push then checks the host.

**The push asks the host about visibility.** For a private remote at GitHub or a Forgejo or Gitea host that has a token and was made with the GitHub or Forgejo preset (every remote `repo.publish` makes), each push first asks the host's API (one `GET` of the repository, to the host's API address) whether the repository is public. If it is, nothing is pushed (`repository_is_public`, the remote is parked, the address is marked), because the full copy does not go to a public repository; the filter is not switched on behind the owner's back, since the remote's history already holds the rest. If the host cannot be asked, the push fails and is tried again. **This is a new request, to the host the remote already points at, for a remote that existed before Phase 7.** A remote at SSH or at a custom host has no API to ask: for those only the session rule above stands.

### Publish to a GitHub or Forgejo repository

`repo.account.connect` stores a personal access token for a host (GitHub, or your Forgejo or Gitea), checked with one request to the host's API (whatever goes wrong you are told one thing, that the host could not be reached or did not accept the token, so the form is no way to map the server's network; the detail is in the log at debug level, and only how long it took is visible); it is yours (no one else, an administrator included, can use it), sealed, and bound to the host. **GitHub**: github.com, or a GitHub Enterprise Server host (API under `/api/v3`); a fine-grained token with *Administration: write* (to create) and *Contents: write* (to push) on the repositories, or a classic token with `repo`. **Forgejo and Gitea**: an access token with repository write; the host is the one you type, and it is reached only if the Git network rules above allow its address (`SCRIOBH_GIT_ALLOW_PRIVATE=1` for one on your own network). The same guard, no proxy, no redirects, a 1 MiB limit on every answer and a deadline on every request apply to the host's API. Nothing the host says in an error is shown or kept, and no address in an answer is followed.

The flow is two steps, in a browser session (an API key of any scope is refused, and none of it is an MCP tool). `repo.plan` is a dry run that only reads from the host: it says what will be created (a repository is created with no README, no licence, **private** unless you say otherwise), whether the name exists (a name that exists is refused), which files the first push holds (count, size, paths), which documents the audience withholds and why, anything that looks like a secret (each finding blocks), and whether the repository is public. `repo.publish` takes the plan's `plan_token` for the same fields (it refuses with 409 `plan_changed` when the settings, the set of files, what is withheld or the repository at the host differ now; the files' text may have moved on, as for any push), and for a **public** repository the name typed exactly. It creates the repository, registers it as a remote (with its own sealed copy of the token) and queues the first push through the same engine as any push; after that push it points the repository's default branch at the branch that was pushed. A public repository gets the public audience only; a private one may take the private audience. `plan_token` is sealed with the instance key under the project and the person and is good for 30 minutes, and the plan is recomputed and compared at publish besides. `mode: existing` takes a repository that is **empty**; one with content is refused, because Scríobh pushes and never merges. If the repository was created and a later step failed, the error says so, and the call can be repeated with `mode: existing`.

### Release assets

`repo.release` publishes a compiled edition (`epub`, `docx`, `pdf`, `md`, or `md_bundle`, the reader's Markdown tree as a zip) as an asset of a release for a tag at the commit the remote has, after a successful push, at a GitHub or Forgejo remote made with a token. It runs as a job (`repo.release_get`, `repo.release_list`), in a browser session, as the owner. Only presets without the author's contact block are offered, and the options are held to no notes and no ghosted text. The file is capped at 64 MB before anything is created at the host. The release notes carry the compile's manifest hash and the file's SHA-256. A retry with the same tag and manifest hash adds no second asset (an asset of that name and size is left alone, a cut-short upload is replaced); a tag that already has another edition, or a release made by hand, is refused with `tag_conflict` and nothing there is changed. A server that stops mid-release closes the row at its next start; run it again.

### Signing commits

`git.signing.generate` makes the project's ed25519 signing key (sealed, never returned); `git.signing.get` shows the public key, its fingerprint and an allowed-signers line (`* namespaces="git" ssh-ed25519 …`). A remote with `sign_commits` adds an SSH signature (the format git writes for `gpg.format=ssh`, namespace `git`) to each commit it makes; `git verify-commit` with `gpg.ssh.allowedSignersFile` pointing at that line accepts them, and so does a host once the public key is added to your account there as a *signing key*. Commits made before signing was switched on stay as they are, and an unchanged tree makes no new commit, so the first signed commit comes with the next change. A remote that signs and has lost its key stops with `signing_failed` rather than pushing an unsigned commit.

### A trigger after a push

`git.trigger.set` gives a remote one signed POST to an address you choose after each push that reached it (not after a refused, failed or empty one): the body is `{event, delivery, project, repository, branch, commit}`, and nothing of the manuscript. It is signed like a webhook (`Scriobh-Webhook-Signature`, the scheme of [Webhooks](#webhooks), with a secret shown once), goes through the same address checks and network guard (plain http only to a local address when `SCRIOBH_GIT_ALLOW_PRIVATE` allows it), is cut off at ten seconds, and is retried with the webhook backoff (10 s, 1 min, 5 min, 30 min) up to five attempts, then given up. Only a short status line is kept, never what the receiver answered. A trashed project's deliveries wait, and are given up after a day.

### Limits worth knowing

- Up to five remotes per project, each with its own branch, folder, audience, state and backoff.
- Only `ssh://`, `git@host:path` and `https://` addresses are accepted. `http://`, `git://`, `file://` and local paths are refused.
- The server's own `~/.ssh/config`, `known_hosts` and SSH agent are never used.
- Pushing needs the server to reach the host. Scríobh makes no connection you did not configure: the remote's host, and nothing else.
- The local repository grows by the changed text of each push and is not repacked. For a manuscript this is small; deleting `data/git/<project>/<remote>/` is safe (see above for what happens next).

## AI providers: quick path

Three things must be true before any text can go to a model, and each belongs to a different person: the **server** allows AI and can store credentials, a **provider** exists, and the **project's owner** has turned AI on for that project. Nothing is sent until all three are, and a fresh install sends nothing. What follows is the shortest working path for each case; the rules behind it are in [the next section](#ai-providers-policy-and-budgets) and in [AI.md](AI.md). What was run: the API calls below, as the roles named, against a development server with a stand-in Ollama on a loopback port (it answered `/api/tags` and `/api/chat`): adding the providers, testing, setting the policy, preview, run, the local-only skip and the refusals. What was only read, from the page's templates and script and not driven in a browser: the labels and layout of the screens named in the steps. A real Ollama, LM Studio, OpenRouter or OpenAI account was **not** used.

**0. Check the server.** `SCRIOBH_AI` is on unless you set it to `off`, and credentials need a secret key (the one Scríobh makes in the data directory on first start, or `SCRIOBH_SECRET_KEY`). Anyone signed in can see the verdict at `GET /api/v1/ai/status`: `{"enabled": true, "credentials_ok": true, ...}`, and `warnings` says what is missing.

**1a. A local model (Ollama, llama.cpp, LM Studio, vLLM).** Only an **administrator** can mark a provider *local*, and without that mark an address on this machine or a private network is refused. As an administrator open **AI providers** in the account menu (`/settings/ai`), open **Add a provider**, and fill in:

| Field | Value |
| --- | --- |
| Kind | `ollama` for Ollama (the native `/api/chat` and `/api/tags`); `openai_compatible` for anything that speaks OpenAI chat completions (llama.cpp's server, LM Studio, vLLM) |
| Address | Ollama: `http://127.0.0.1:11434` (its default port; the form fills it in for Ollama). OpenAI-compatible: the service's base including `/v1`, for example `http://127.0.0.1:1234/v1` |
| Who may use it | Instance (projects may allow it; nobody else holds the key) |
| Stays on this machine or network | ticked (an administrator's setting; it allows a loopback or private address and plain `http`). Choose the kind first: changing the kind afterwards clears the box for every kind but Ollama and OpenCode |
| Key | none for Ollama; usually none for a local OpenAI-compatible server |
| Default model | a model that server has pulled, for example `llama3.2:3b` |

Press **Add the provider**, then **Test** on its card: it reads the model list and says how many models it found (the list can be refreshed over the API with `ai.provider.models.refresh`). From inside the Scríobh container `127.0.0.1` is the container itself: use an address the container can reach (the host's address on your network, or `host.docker.internal` where Docker provides it), and make sure the model server listens on that address (Ollama needs `OLLAMA_HOST=0.0.0.0` for that; not verified here). The same thing over the API, as an administrator's session:

```
curl -s -b admin.txt -H 'Content-Type: application/json' -d '{"name":"Local Ollama","kind":"ollama",
  "base_url":"http://127.0.0.1:11434","scope":"instance","local":true,"default_model":"llama3.2:3b"}' \
  $BASE/api/v1/ai/providers
curl -s -b admin.txt -X POST $BASE/api/v1/ai/providers/$PROVIDER/test
```

**1b. A hosted model (OpenRouter, OpenAI, anything with a public https address).** Any writer can add one for themselves, and an administrator can add one for everybody (scope `instance`). **AI providers** in the account menu, **Add a provider**: kind `openrouter` with address `https://openrouter.ai/api/v1`, or `openai_compatible` with `https://api.openai.com/v1`; paste the API key (it is sealed with the secret key, shown afterwards only as `…1234`, never returned, and never in an export, the mirror, a backup in the clear or a Git push); name a default model. Only public `https://` addresses are accepted for a provider that is not local. A personal provider is private to its owner (not even an administrator sees it), and a project uses it only when its owner has allowed personal providers (below).

**2. Turn AI on for one project.** The project's **owner** opens the project's AI page (`/p/<slug>/ai`, also the AI chip in the writing view) and chooses a mode:

| Mode | Meaning |
| --- | --- |
| `off` (the default) | Nothing leaves for this project. |
| `ask` | AI is available, with a preview of the exact request before every send. |
| `local_only` | As `ask`, but only providers marked local are used. |

Also on that page: which actions are allowed, whether personal providers may be used (`allow_personal_providers`, off by default), whether agents (API keys) may use AI on this project (`agents_may_use_ai`, off by default), a provider order, redaction of entity names, and a budget. All of it is browser-only: an API key cannot change it.

**3. Use it.** In the writing view, once the project's mode is `ask` or `local_only`, select some text and press the writing-partner button in the toolbar (Alt+Shift+I). It shows a **preview**: the exact bytes that would be sent and to which provider, the token estimate and the cost. Nothing is sent until you press run, and nothing comes back into the chapter until you accept the diff. The API path is in [API.md](API.md#ai).

**Forcing local.**

- *One project:* the owner sets the mode to `local_only`. A provider that is not local is then skipped, and the preview lists it with the reason `not_local` ("This project is local-only and the provider is not local."); the address is checked again when the connection is made, so a name that starts to resolve to a public address is refused.
- *The whole instance:* there is no single switch that forces every project local, and no `SCRIOBH_AI_ALLOW_PRIVATE`. The way to make it true is to add only local instance providers and set `SCRIOBH_AI_USER_PROVIDERS=off`, so no one can add a hosted one; then no project can send anywhere else. `SCRIOBH_AI=off` turns AI off for everyone.
- Local providers cost no money and count against token budgets only. The defaults cap each person at 1,000,000 tokens and five units of money a month (`SCRIOBH_AI_DEFAULT_USER_TOKENS`, `SCRIOBH_AI_DEFAULT_USER_COST_MICROS`; `0` is no cap).

## AI providers, policy and budgets

AI is **off** until three things are true: AI is not switched off for the server, an administrator (or a person, for their own use) has added a provider, and a project's owner has turned AI on for that project. Nothing is ever sent to an address that is not in the provider list, and a fresh install makes no AI request of any kind. With every provider removed or `SCRIOBH_AI=off`, the editing, outline, Compendium, timeline, research, revision, export and Git features are unchanged.

### The kill switch

`SCRIOBH_AI=off` makes every AI operation that could send something, or change what may be sent, answer `409` with "AI is switched off on this server" (the operations stay listed, so the schema and the CLI do not change; a screen reads `ai.status` to hide its AI controls): no provider is contacted and no run is recorded. An administrator can also switch AI off without restarting (the AI settings, `ai.settings.set`); the environment switch wins and the setting cannot turn it back on. `ai.status` and the settings always answer, so a screen can say why. What sends nothing keeps working, so a person is never locked out of their own records: listing, reading and deleting kept chats (`ai.chat.list`, `ai.chat.get`, `ai.chat.delete`), setting and lifting never-send flags (`ai.exclude.set`, `ai.exclude.list`), and the use and cap views (`ai.usage.get`, `ai.usage.project`, `ai.budget.get`, `ai.run.list`). The project's AI page shows exactly these while AI is off.

### Providers

| Kind | Address you give | Notes |
| --- | --- | --- |
| `openrouter` | `https://openrouter.ai/api/v1` | Requests deny data collection unless the provider's routing allows it; the cost OpenRouter reports replaces the price table. |
| `openai_compatible` | the service's base, like `https://api.openai.com/v1` | Anything that speaks chat completions: llama.cpp, LM Studio, vLLM, hosted services. The model list is optional; models can be typed in. |
| `ollama` | `http://192.168.1.20:11434` | The native `/api/chat` and `/api/tags`. Needs no key. |
| `opencode` | `http://127.0.0.1:4096` | An OpenCode instance's text routes only (one-shot generation and the model list); never its shell, files or tools. The request and model-list shapes are assumed from the route names; see OPEN-QUESTIONS. Basic auth, the key being the password. |

An **instance provider** is added by an administrator and used by anyone a project's policy allows, without holding its key; an editor sees its name, kind and models, not its address, headers or user name. A **personal provider** belongs to one person; nobody else sees it, an administrator included. `SCRIOBH_AI_USER_PROVIDERS=off` allows instance providers only. A project's owner decides whether collaborators may send the project to personal providers (`allow_personal_providers` in the policy, off by default; with no provider list a project uses instance providers only). Keys are sealed with the server's secret key (they need `SCRIOBH_SECRET_KEY` or the key file; without one no key can be stored), shown as "…1234" and never returned, never logged, never in an export, the mirror, a Git push or an event (a backup holds them sealed). Changing a provider's address drops its key. A restore onto a server with a different secret key leaves keys unreadable; enter them again.

**Local providers.** A model server on this machine or the local network is declared local by an administrator, per provider. Without that flag an address on this machine, a private network or carrier-grade NAT is refused when the provider is created and again on every connection, and a provider must be https. With it those addresses (and `http://`) are allowed. Link-local and the cloud metadata addresses are refused always. Only an administrator can set the flag; a writer's personal provider can only reach public https addresses. There is no `SCRIOBH_AI_ALLOW_PRIVATE`: the decision is per provider.

**Local-only projects.** A project in `local_only` mode may use only providers declared local, and the address is checked again at connect time, so a name that has started to resolve to a public address is refused.

**What a caller sees when it fails.** One plain sentence per kind of failure (blocked address, unreachable, timeout, bad key, out of credit, rate limited, provider error, unknown model, refused request, unreadable answer, answer too large). It never contains an address, a key or the provider's own wording; the server log has the detail (`ai provider call failed`, with the class and status) and never the key.

### Who can change what

Everything that decides where text may go is for people in a browser: the AI policy (`ai.policy.set`), never-send flags (`ai.exclude.set`), providers, prices, budgets and the AI settings refuse an API key at every scope, pinned or not. An agent can run AI only on a project whose owner turned on `agents_may_use_ai` in a session.

### What is sent, and what is stored

Sent: the recipe's fixed instructions, then the context the person chose, each piece between lines carrying a random per-request token, then the task. The preview (`ai.context.preview`) returns the exact request body of every provider that may receive it, the size and token estimate of every piece, the redaction mapping, what it will cost and whether it is allowed; running it sends those bytes, or is refused with a 409 if the text has changed since. Documents, notes and entities flagged **never-send** are left out or refused; a private scratchpad is never read; ghosted text, alternatives metadata and `%%private notes%%` are removed; with **redaction** on, entity names and aliases are replaced by placeholders and put back in the answer. No request carries tools, functions or any way for the model to act. The answer is shown as text and is never inserted without a person accepting it.

Output is filtered before anything sees it: invalid UTF-8, control characters other than newline and tab, zero-width characters and bidirectional overrides are removed. The server log has the class and status of a provider failure, never the provider's own words.

Stored: for each run, who, which project and recipe, the provider and model, token counts, cost, status, the time it took, a hash of what was sent and of what came back, and the chips by kind, label and token count. **No prompt and no answer text is kept** (keeping transcripts is not built). Each run also writes an audit entry and an event, with ids and counts only. The run log is `ai.run.list`; nothing deletes it.

### Limits

| Variable | Default | Meaning |
| --- | --- | --- |
| `SCRIOBH_AI` | `on` | `off`: the kill switch. |
| `SCRIOBH_AI_USER_PROVIDERS` | `on` | `off`: only administrators add providers. |
| `SCRIOBH_AI_MAX_REQUEST_KB` | `256` | The most one request sends. A larger one is refused before anything is sent. |
| `SCRIOBH_AI_MAX_CHIP_KB` | `200` | The most one piece of context may be. Nothing is cut to fit: an over-large piece is an error that names it. |
| `SCRIOBH_AI_MAX_OUTPUT_TOKENS` | `4096` | The longest answer any request may ask for (a recipe and a model may lower it). |
| `SCRIOBH_AI_MAX_RESPONSE_KB` | `4096` | The most of a provider's response (or stream) the server reads. |
| `SCRIOBH_AI_TIMEOUT_S` | `120` | A provider call when the provider sets no timeout of its own. |
| `SCRIOBH_AI_SYNC_TIMEOUT_S` | `50` | The longest a non-streamed run may take; it must finish under the server's 60 second write timeout. Use the stream route for longer answers. |
| `SCRIOBH_AI_STREAM_IDLE_S`, `SCRIOBH_AI_STREAM_MAX_S`, `SCRIOBH_AI_DIAL_TIMEOUT_S` | `60`, `600`, `10` | The longest silence in a stream, the longest stream, connecting. |
| `SCRIOBH_AI_PROVIDER_CONCURRENCY`, `SCRIOBH_AI_USER_CONCURRENCY` | `4`, `3` | Calls in flight to one provider (a provider can set its own), runs in flight for one person. |
| `SCRIOBH_AI_RATE_PER_MIN` | `30` | Runs per person per minute (a `429` with Retry-After). |
| `SCRIOBH_AI_RETRIES` | `1` | Repeats against the same provider after a timeout, 5xx or rate limit, before the next in the chain. `0` for none. |
| `SCRIOBH_AI_DEFAULT_USER_TOKENS`, `SCRIOBH_AI_DEFAULT_USER_COST_MICROS` | `1000000`, `5000000` | The monthly caps for a person with no budget of their own set by an administrator: a million tokens and five units of the currency. `0` is no cap, on purpose; with no cap anywhere `ai.status` warns administrators. |
| `SCRIOBH_AI_CURRENCY` | `USD` | The label for money. Prices and caps are entered in it as micro-units: 1,000,000 is one unit. |

### Budgets

Monthly (calendar month, UTC), in tokens and in money, for the whole instance, a person and a project (all set by an administrator with `ai.budget.set`) and, for a project, its owner's own cap in the project's AI policy. Every cap that applies must have room: before each call the server adds this request's worst case (what it sends once for every send the chain allows, each provider and each retry, plus the longest answer, at each provider's price; the preview shows it as `reserve_tokens`) to what has been spent and to what is held for requests in flight, in one transaction, and refuses the call if any cap would be passed. Afterwards the real usage replaces the hold. Every send that reached a provider counts, answered or not: a stream cancelled before its first token, a timeout and a retry that was thrown away are charged at least the input estimate at that provider's price (a bad key, a rate limit and a refused address, which were stopped before any work, are free). A run whose record cannot be written is marked `unsettled` and its reservation counts as spent for that month. Money caps count what the instance pays for (an administrator's provider that is not local); a person's own provider and a local one count in tokens only. A model with no known price under a money cap is refused: set its price with `ai.price.set`, or the provider's own list may give it (OpenRouter does). Everyone can read the caps that apply to them and how much is used (`ai.budget.get`, `ai.usage.get`); a project's owner sees its usage by person (`ai.usage.project`). After a crash, requests that were running are closed at the next start and stop holding budget. A month can end over a cap by at most the estimate's error on its last request, because a cap is enforced on the estimate and corrected by the provider's count. With the default money cap, a paid provider whose model has no known price is refused until you set its price (`ai.price.set`) or refresh its model list; `ai.status` names those providers.

### Streaming

`POST /api/v1/projects/{id}/ai/run/stream` is `ai.run` as server-sent events (`start`, `delta`, `done`, `error`). If a reverse proxy buffers responses, turn that off for this path (the response sets `X-Accel-Buffering: no`). Closing the connection cancels the request at the provider.

## AI recipes: what they send and what is kept

Every recipe is previewed (`ai.context.preview`) and sent only as previewed. What each sends is its chips (the preview lists them with sizes) plus a short fixed instruction; the style guide goes only where its chip is listed. Defaults, with the class that picks the model chain and the policy action that must be allowed:

| Recipe | Action | Class | Default chips | Result |
| --- | --- | --- | --- | --- |
| `rewrite`, `tighten`, `expand`, `simplify`, `tone`, `describe`, `dialogue` | selection | strong | the selection, style guide, brief, entities named in it | one suggestion with a diff |
| `grammar` | selection | cheap | the same | one suggestion |
| `continue` | selection | strong | the same (the selection is the text before the cursor) | text to insert after it |
| `alternatives` | alternatives | strong | the same | up to 5 candidate wordings |
| `critique.chapter`, `critique.selection` | critique | strong | the text, brief, style guide, entities | a review of notes |
| `continuity.chapter`, `continuity.selection` | continuity | strong | the text, entities named in it, earlier chapters' summaries, the timeline's events and findings, brief | a review of findings that cite their sources |
| `lab.convoluted`, `lab.tone` | lab | strong | the chapter, style guide (and brief for tone) | marks |
| `lab.trim` (modes filler, adverbs, repetition, dialogue_tags) | lab | cheap | the chapter, style guide | marks, each a candidate deletion |
| `summary.chapter` | summaries | cheap | the chapter, the previous chapter's summary, brief, style guide | a short and a long synopsis |
| `extract.chapter`, `extract.selection` | extraction | cheap | the text, entities named in it, brief | proposed entity sheets |
| `chat` | chat | strong | the selection, brief, style guide, and the conversation | text, and a proposed wording when asked for |

A chapter-sized request costs about the chapter's tokens plus 600 for the framing; answers are capped (`ai.recipe.list` shows each recipe's limit: 700 tokens for a summary, 2,000 to 2,400 for notes, marks and sheets, 900 to 1,600 for prose). A rolling summary of a manuscript is that, once per chapter; its plan shows the total and refuses when a cap cannot cover it.

**The Lab's local analysers** (`ai.lab.analyse`) send nothing and cost nothing and work with AI switched off: sentences over 40 words (`long_words`, 15 to 200), English hedge words (perhaps, maybe, somewhat, rather, quite, fairly, sort of, kind of, a bit, a little, slightly, almost, nearly, probably, possibly, apparently, seemed to, seems to, appeared to, appears to, tended to, I think, I guess, I suppose, in a way, more or less, somehow, supposedly, arguably; other languages have none), double spaces, repeated `,` `;` `:` and `..`, a space before `, . ; : ! ?` (not before `; : ! ?` in French), and the odd form out between straight and curly double quotes.

**What is kept, and for how long**

| What | Where | Kept | Removed by |
| --- | --- | --- | --- |
| A suggestion waiting to be accepted (the text; sealed with the secret key when the server has one) | `ai_pending` | 30 minutes | an accept or reject of the last of it; every read ignores an expired row; the hourly sweep (and at start) and every AI run or accept delete expired rows; the person leaving the project or no longer being an editor; the project, document or person |
| That a suggestion was accepted or rejected (no text) | `ai_accepts` | with the project | the project |
| A review (notes, findings, proposed sheets; quotes and sentences of the model) | `ai_reviews`, `ai_notes` | until deleted; the newest 100 per person and project | `ai.review.delete`, the project, the document, the person |
| Lab marks (quotes, reasons) | `ai_marks` | open ones until replaced by a new run of the same kind; settled ones 90 days, at most 1,000 a document, and a dismissed mark is not made again | `ai.lab.dismiss` settles; the document, the project, the person |
| A kept chat | `ai_chats`, `ai_chat_turns` | only if the person created it; 50 chats of 200 messages, 2 MB each and 20 MB a person on a project | `ai.chat.delete`, the project, the person |
| A rolling summary plan | `ai_jobs` | finished or cancelled 30 days, abandoned 60 | cancel marks it; the sweep; the project, the person |

The sweep also removes the accept ledger after 30 days and reviews with no open note after 180. A person's rows on a project go when their membership ends or they are no longer an owner or editor, and their continuity and extraction reviews when their access to the Compendium ends. Reviews, marks, chats and jobs are private to the person who made them; an owner or an administrator reading the database sees them, as with any manuscript text. None of it is in the mirror, an export or a Git push. The run log still holds no prompt and no answer. Events and audit entries for an accept name the recipe, the run and counts, never text.

## Semantic search (optional)

Search by meaning (for owners and editors: the question goes through the index's provider), and "scenes like this one" (for any reader), over an index of embeddings kept per project. **Off for every project.** Nothing is sent until the project's owner has seen a preview and confirmed, from a browser (an API key cannot switch it on, off or purge it). Details and limits: [p7-notes/semantic.md](p7-notes/semantic.md).

**What you need.** An AI provider of kind `ollama` or `openai_compatible` (OpenRouter's `/embeddings` is called the same way, as a remote endpoint; it has not been tried against OpenRouter), added the usual way (see "AI providers"), with an embedding model, for example `nomic-embed-text` on an Ollama on this machine or the local network. An administrator declares it local (`local: true`) to use it as a local endpoint. The project's AI policy must not be `off`; a `local_only` project can use only a local endpoint; the policy's provider and model lists and `agents_may_use_ai` apply as they do to any AI request. `SCRIOBH_AI=off` stops it like everything else.

**Turning it on** (the owner, in a browser): `search.semantic.preview` says how many documents, chunks, words and bytes would be sent, to which provider and model, whether that endpoint is local or remote, the estimated tokens and cost, and gives a consent token; `search.semantic.enable` takes the token. A remote endpoint (one not declared local) also needs the host typed exactly as the preview shows it: the whole manuscript goes there. The consent is to the endpoint (provider, address, local flag, model): if an administrator changes the provider's address, or the owner changes the model, indexing stops until the owner confirms again, and the old embeddings are deleted when the new endpoint is confirmed.

**Redaction.** If the project's AI policy asks for redaction (`entities` or `names`), the names are replaced by the same placeholders an AI request uses, in every chunk and every question, and a change of that setting, or a new entity name, embeds the documents again.

**What is embedded.** The chosen wording of documents, cut by the one rule every AI request uses (`CanonText`, listed in AI.md section 3.1.1): front matter, private notes (`%%...%%`, and one that is never closed takes the rest of its paragraph), ghosted text, HTML comments, footnotes referenced only from a private note or ghosted text, and the alternatives comment are removed, and the result is made plain (raw HTML and code blocks gone) before it is cut into chunks. Comments, stashes, research and the Compendium are never read. Documents flagged never-send, marked do not compile (or inside one that is), notes, research and the trash are not embedded, and if a document becomes one of those its embeddings are deleted. Chunks are about 120 words, at most 220 words or 1,400 characters, overlapping by up to 25 words; a project holds at most 20,000 chunks and a document at most 600.

**Keeping it current.** A background worker compares each document's hash with what was embedded, after the document has been left alone for 20 seconds; it sends at most two documents at a time, in requests of up to 32 chunks, and writes the result in a short transaction. Saving is never held up. A failure is recorded as a class (never the endpoint's words) and retried after 30 seconds, doubling to 15 minutes; a document the endpoint refuses is marked failed and tried again when it changes or after an hour; if the model starts answering with another vector size the index is paused (`model_changed`) until the owner rebuilds it. `search.semantic.status` shows the state (`off`, `paused`, `waiting`, `backoff`, `indexing`, `ready`), the counts, the queue and the last error class (the class of the last failure is shown to the owner and administrators; a local endpoint's address to administrators only, a remote endpoint's host to the owner).

**Cost and records.** Every embedding request is a row in the AI run log (`embed.index` or `embed.query`, tokens, no text) and in the day's usage, charged to the person who switched the index on (an index) or who asked (a search), and the instance, person, project and owner caps apply before each request. A search sends only the question.

**Restore.** `scriobh restore` switches every index off with the reason `restore` (a marker file, `semantic.restore-pause`, is written first and removed once they are off; if the pause cannot be done the restore fails with "NOT paused" and nothing is sent until the next start pauses them): a restore on another machine must not start sending text to an endpoint by itself. Embeddings whose document text differs from the backup's, or that were made under older rules, are deleted; the rest are kept and used when the owner switches the index on again.

**The size.** 768 bytes a chunk at 768 dimensions (int8): 10,000 chunks are 7.7 MB in the database and in a backup, and the same in memory while a project is being searched (at most 96 MB of such copies are held). Removing the index (`search.semantic.purge`) deletes every embedding.

## The agent CLI, keys, dry runs and idempotency

`scriobh-cli` is a small separate binary (`make cli`; the container image carries one at `/scriobh-cli`) for scripts and coding agents. It speaks HTTP and JSON to the server and links neither the server nor SQLite. Every command comes from the op schema the server publishes at `GET /api/v1/_ops`, so there is nothing to upgrade on the client when an op is added. It keeps a copy of the schema in the user's cache directory (revalidated by the schema hash as an ETag at most every ten minutes, and refetched when it meets a command it does not know) and falls back to the snapshot built into the binary when the server cannot be reached. The server binary keeps its own subcommands (`serve`, `backup`, `import`, and the rest); they need the data directory, the CLI does not.

### Keys

Create a key in **Settings, API keys** (name, what it may do, an optional restriction to one project, days until it expires; the default is 90 and 0 means never). The key is shown once. The server stores only its SHA-256. A key acts as the person who made it, narrowed by its scope:

| Scope | May |
| --- | --- |
| `read` | Look: every GET. |
| `write` | Also write what its owner may write. The default. |
| `admin` | Also manage keys and call administrator operations, if its owner is an administrator. |

A key can never be stronger than the person who creates it (a writer cannot make an `admin` key) and a key cannot make a key stronger than itself. Key management (`key create`, `list`, `revoke`) needs the `admin` scope when done with a key, so an agent that can write a manuscript cannot mint itself more power; a person in a browser session is not limited by scope. A key restricted to one project can reach that project's operations and the project's primary Compendium, and nothing instance-wide; a key restricted to one Compendium (you must be able to read it) reaches that Compendium and no project. To let an agent build a book's bible and draft its chapters, create a project-restricted key; the agent makes the Compendium itself with `project compendium create` (a Compendium that belongs to no project needs an unrestricted key, so that step is yours). `whoami` still answers for such a key and names the Compendium it works in. A key spends its own rate-limit allowance (`SCRIOBH_RATE_LIMIT`) and its owner's as well, so holding many keys does not multiply the rate; a person may hold `SCRIOBH_MAX_KEYS_PER_USER` live keys (25 by default). A key that is not recognised (mistyped, revoked, expired, guessed) is answered 401 until its client address has offered 30 of them in a minute, and 429 after: key guessing is bounded per address. A key made by another key (an admin-scope key can make keys) never outlives its parent, is listed with `parent_key_id`, and is revoked when its parent is, at any depth (`key revoke` reports how many followed). Revoking takes effect on the next request. Key creation and revocation are in the audit log (`apikey.create`, `apikey.revoke`) with the key's id, never its secret. Keys are honoured on `/api/` and `/mcp` only; a key sent to a page is ignored, and a browser session cookie is never read on `/mcp`.

### The CLI

```
scriobh-cli login --url https://write.example.com --key -    # the key from standard input
scriobh-cli whoami
scriobh-cli ops                                              # every command this server offers
scriobh-cli project create --title "The Salt Road" --kind novel
scriobh-cli doc create "The Salt Road" --title "Chapter 2" --content @chapter-2.md
scriobh-cli doc update "The Salt Road/Chapter 2" --base-revision 1 --content @chapter-2.md --dry-run
```

The server and key come from `--server` and `--key`, else `SCRIOBH_URL` and `SCRIOBH_API_KEY` (`SCRIOBH_TOKEN` is accepted for the key), else the saved login in the user's config directory (`config.json`, mode 0600, written by `login`, removed by `logout`). `SCRIOBH_CONFIG_DIR` and `SCRIOBH_CACHE_DIR` move those directories. The key is never printed, logged or echoed in an error; prefer the environment or the saved login to `--key`, which shows in the process list. A server URL that is not `https://` and not loopback gets a warning, and redirects are never followed, so a key goes only where the URL says.

Output is one JSON object on stdout when it is not a terminal (or with `--json`): `{"ok": true, "op", "status", "data", "meta"}`, where `data` is the server's response unchanged. An error is one JSON object on stderr, `{"ok": false, "error": {"code", "message", …}, "exit": N}`, with nothing on stdout. On a terminal the data is printed indented and errors are plain text.

Wherever an op takes the id of a project, document or any kind that has a `<kind>.list` op, the CLI accepts a name or a path (`"The Salt Road/Part One/Chapter 3"`) and looks it up on the client: exact match first, then ignoring case and spacing; more than one match is an error that lists the candidates with their ids (exit 2, code `ambiguous`), never a guess; ids pass straight through. Long text comes from `@file` or `-`; `--data` sends a whole JSON body; `--files @DIR` uploads a folder; `--all` follows `next_cursor`; `--output FILE` saves a file response. `scriobh-cli <group> <verb> --help` lists a command's flags; `scriobh-cli completion bash|zsh|fish` prints a completion script from the schema.

**Exit codes**

| Code | Name | Meaning |
| --- | --- | --- |
| 0 | ok | Success. A dry run is also 0. |
| 1 | error | A server error or an unreadable response. |
| 2 | usage | Mistyped command, missing flag, or an ambiguous name. |
| 3 | not_found | Nothing by that id or name, or no access to it. |
| 4 | conflict | A stale `base_revision`, or a retry that cannot be replayed. |
| 5 | auth | Missing, expired, revoked or insufficient key. |
| 6 | validation | `invalid`, `too_large`, `dry_run_unsupported`, `idempotency_key_reuse`. |
| 7 | rate_limited | Wait `retry_after` seconds. |
| 8 | unreachable | The server could not be reached. |
| 9 | would_change | `--dry-run --fail-on-change` and the command would change something. |
| 10 | drift | `skill check` found an installed skill or the CLI snapshot out of date. |

### Dry runs

`--dry-run` sends `Prefer: dry-run`. On REST, `Prefer` is read as RFC 7240 (comma separated `name[=value]` tokens, any case): `dry-run` with any value except `false`, `0`, `no` or `off` asks for a preview. `X-Scriobh-Dry-Run` takes `1`, `true`, `yes` or `on` (and `0`, `false`, `no`, `off` or nothing for none); any other value is a `400 invalid` and the request does not run, so a preview that was asked for is never silently a real write. Either header asking is enough, and a preview always answers `Preference-Applied: dry-run`. The server authorizes and validates the request exactly as it would, runs the op against the real database inside the writer transaction (so it sees its own writes, and reads back what it wrote), and rolls the transaction back: nothing is committed, no event or audit row is written, nothing reaches the mirror or a remote, and the op's data directory is a scratch directory that is deleted. The answer is what the op would have returned plus `dry_run: true`, `would_change` and `changes` (rows it would have inserted, updated or deleted). A dry run holds the single writer for the length of the op, like any write. An op that has effects a rollback cannot undo (a backup, a mirror rebuild, the in-browser writing lease, key creation) refuses with `422 dry_run_unsupported`; an op declares this with `NoDryRun` in its registration, and a test drives every op with a dry-run header and compares a digest of every table and of the data directory before and after.

### Idempotency

A mutating REST call may carry `Idempotency-Key` (1 to 200 printable ASCII characters). The first request runs and, if it succeeded, its status and body are stored for 24 hours under the caller (one API key, or one person's browser session). The same key with the same request (method, path, query and exact body) returns the stored answer with `Idempotent-Replay: true`; the same key with a different request is `422 idempotency_key_reuse` and nothing runs; two requests with one key at once run once, the second waiting for the first. A failed request stores nothing, so it can be corrected and sent again with the same key. A response that holds a secret (a new key) or is over 1 MiB is not stored, and one caller's stored responses are capped in count and in bytes (see the configuration table), oldest dropped first, so a long-lived key cannot fill the database; a repeat of that request is `409` rather than a second execution. The record is written just after the operation commits, so a crash in that instant lets one retry run again. Expired records are swept hourly.

The CLI sends a fresh random key with every real write (not a dry run) unless `--no-idempotency`, and retries a network failure or a 502, 503 or 504 with the same key, so a write is applied once. Pass your own `--idempotency-key` to make a retry loop safe across processes.

### The agent skill

`scriobh-cli --skill` prints the whole skill, rendered from the schema the CLI is using (so it describes this server); `--skill drafting` prints one recipe (`quickstart`, `drafting`, `compendium`, `import`, `export`). `scriobh-cli skill install` writes it for the coding agents it finds in the project directory (or `--agent claude|codex|opencode|gemini|cursor|windsurf|copilot|agents`, `--file PATH`, `--global` for the home directory): a skill file for Claude, Cursor and Windsurf, a marked block in a shared instruction file (`AGENTS.md`, `GEMINI.md`, the Copilot file) for the rest. Each carries a hash of what was written; a reinstall replaces only that, an uninstall removes only that and leaves your own text byte for byte, and text edited by hand is never overwritten without `--force`. Nothing is written outside the directory you name, and nothing through a symbolic link. `skill install --dry-run` shows the plan. `scriobh-cli skill check` compares each installed copy and the CLI's built-in snapshot with the live server's schema hash and exits 10 on drift.

## MCP, the Model Context Protocol endpoint

Scríobh answers the Model Context Protocol at `POST /mcp` and, through the slim CLI, over stdio. It is for an agent (Claude Desktop, Cursor, OpenCode, your own) that you want to read, and perhaps write, a project. It is not a second way in: a tool is an operation, called as the key that made the call, so a key's scope, its pin to a project or Compendium, a person's role and the project's trash all apply exactly as on `/api/v1`.

**Make a key first** (Settings, API keys): `read` for an agent that looks, `write` for one that builds, pinned to the one project it needs, with an expiry. Then either:

- **stdio (most desktop clients).** The client starts `scriobh-cli mcp` and talks to it on standard input and output. The key and address come from the environment the client sets: `SCRIOBH_URL` and `SCRIOBH_API_KEY`. `--groups doc,search,compendium` limits the tools to those groups (also the presets `core` and `bible`), `--read-only` offers only tools that cannot change anything. For Claude Desktop and Cursor the entry in the client's MCP configuration is `{"mcpServers": {"scriobh": {"command": "scriobh-cli", "args": ["mcp", "--groups", "core,bible", "--read-only"], "env": {"SCRIOBH_URL": "https://write.example.com", "SCRIOBH_API_KEY": "scr_..."}}}}`; OpenCode's `opencode.json` has the same under `"mcp"` as `{"type": "local", "command": ["scriobh-cli", "mcp", "--read-only"], "environment": {...}}`. (These shapes are from the clients' documentation as remembered and were not run here: check each client's own page.)
- **Remote.** A client that speaks Streamable HTTP with a header connects to `https://write.example.com/mcp` (add `?groups=core&readonly=1` to narrow it) with `Authorization: Bearer scr_...`. The endpoint is stateless: one JSON answer per request, no session id, `GET` is `405`. It does not implement the OAuth flow; a client that can only sign in with OAuth cannot use it.

**How many tools.** The whole set is about 200 tools and 58,000 tokens of descriptions; a `read` key sees about a quarter of that. Name the groups the agent needs (`--groups`, or `?groups=` on the address). The generated reference, with each tool's access, role, and the operations that are never offered and why, is `skills/scriobh/mcp.md`; `make skill` rewrites it from the operation registry and CI fails when it is stale.

**What is never offered:** API keys, sessions, browser-only operations, share links, Git remotes and pushes, backups, the in-app writing assistant, imports, webhooks, the operations that change or perform what the static site makes public (`site.options.set`, `site.doc.set`, `site.publish`), anything that takes a credential, the author's contact block (and the compile preview and the DOCX and submission exports, whose title page carries it), and the purge of a whole project or Compendium. **What is offered only on request:** 17 of the tools destroy data beyond the trash (a stash item deleted for good, a pruned or deleted history entry, a merged entity, a deleted comment, tag or attachment, a removed field, alias or relationship). Each is marked `Permanent` in the operation registry (a test fails when a deleting operation has no decision), and over MCP each is hidden unless the server runs with `SCRIOBH_MCP_PERMANENT_TOOLS=1`, the connection asks for it (`?permanent=1`, or `scriobh-cli mcp --permanent`) and the key can write; its description says PERMANENT and its `destructiveHint` is true. Everything else that deletes (documents, projects, entities, stash items, notes, events) moves to the trash and can be restored. Membership management is offered only to an `admin`-scope key on a server run with `SCRIOBH_MCP_ADMIN_TOOLS=1`. **A new group of operations is never offered by default:** every operation group is listed in `internal/mcp/catalog.go` as exposed or excluded, an operation in a group that is in neither list is excluded with a reason that names the group, and a test fails until someone decides.

**Safety rules to know.** Everything an agent reads from a project is the writer's data, and the tools say so in their descriptions and in every result; the server cannot make an agent obey that, so keep write keys narrow and let the agent preview writes (`dry_run`) and pass an `idempotency_key`. A browser origin may call the endpoint only if it is the instance's own or listed in `SCRIOBH_MCP_ALLOWED_ORIGINS`; for those the endpoint answers the CORS preflight (`OPTIONS`: 204, the origin echoed back, `Authorization`, `Content-Type` and the `MCP-*` headers allowed, five minutes) and names the origin on its answers, with no wildcard and no credentials flag (the key is a header the page sets itself; a cookie is never read). Calls are rate limited per key and per owner (`SCRIOBH_RATE_LIMIT`); a batch (legacy clients) pays for each message. The log has one line per request (method, route, status, principal) and, at `debug` level, the tool and operation names; never arguments, manuscript text or keys. `SCRIOBH_MCP=off` removes the endpoint.

## Webhooks

A project's owner can have the project tell an address when something happens: a document is saved (`document.updated`: document id, revision, word count), a comment or suggestion is made (`comment.created`, `suggestion.created`: ids), an export finishes (`export.completed`: export id, format, documents, bytes). Nothing a person wrote is in a payload; the one exception is a comment's words, only if the owner turned `include_text` on for that webhook, and only up to 2,000 characters, read when the delivery is sent and never stored.

**Make one** with `scriobh-cli webhook create` (the project's Webhooks page at `/p/<slug>/webhooks`, or the REST route `POST /api/v1/projects/{id}/webhooks`): a name, an `https://` address and the events. The signing secret is in that answer and nowhere else; keep it. `webhook test` sends a signed ping now and says what came back; `webhook deliveries` lists what was sent and what failed; `webhook rotate-secret` makes a new secret and lets the old one keep signing for 24 hours so the receiver can change over; `webhook redeliver` queues a dead notification again. All of it needs the project's owner and, for a key, the `admin` scope. The delivery format, the signature check (with a short Python verifier) and the event payloads are in [API.md](API.md#webhooks); a signed delivery was received and verified by a loopback receiver for that page.

**What a receiver gets.** A `POST` of JSON (`id`, `type`, `created_at`, `project_id`, `data`) with `Scriobh-Webhook-Id` (the same on every attempt of that notification), `-Event`, `-Attempt`, `-Timestamp` (unix seconds) and `-Signature: v1=<hex>`, the HMAC-SHA256, under the secret, of the timestamp, a full stop and the body exactly as received. Check the signature, reject a timestamp more than five minutes from now, and store the ids you have seen: delivery is **at least once** (a crash between the answer and the record, or a retry after a timeout, delivers again). Answer `2xx` quickly. `5xx`, `408`, `429` (and a timeout or refused connection) are retried: eight attempts, so seven waits of 10 s, 1 m, 5 m, 30 m, 2 h, 6 h and 12 h (each plus or minus 20 %; a 24 h wait follows if `SCRIOBH_WEBHOOK_MAX_ATTEMPTS` is raised above 8, so a receiver that is down is tried for about 20 hours); `410`, other `4xx` and redirects end it (a redirect is never followed). Order is not promised; the revision orders `document.updated`. Saves of one document within five seconds are one notification (the latest).

**What the server will call.** An address must be `https://` with no user name or password. Loopback, private, link-local (the cloud metadata address), carrier-grade NAT and other internal addresses are refused, whatever name or number spelling leads there; the check is made on the address actually connected to, on every delivery. A receiver on your own network needs `SCRIOBH_WEBHOOK_ALLOW_PRIVATE=1`, which also allows `http://` to it; **with it set, any project owner (any person who can make a project) can aim the server at your internal network and learn from the status or the kind of failure in the delivery list which addresses and ports answer, a reachability oracle**, so set it only on an instance whose project owners you trust as you trust its network. No proxy from the environment is used, no connection is reused, and no more than a few hundred bytes of a failing answer is read; with `SCRIOBH_WEBHOOK_ALLOW_PRIVATE=1` none of it is kept (a failing answer from an internal service could be a page of it), only the status and the kind of failure. The path and query of the address are stored sealed with `SCRIOBH_SECRET_KEY` and never shown again (the listing shows scheme, host and port and whether a path is set); to change the address send the whole new one.

**Limits and behaviour.** 10 webhooks a project and 200 an instance; 300 deliveries a minute for the instance and 120 for one project, four in flight, one at a time to one webhook; 500 waiting for one webhook and 5,000 in all, the excess counted and dropped (a warning once a minute). Ten dead deliveries in a row switch a webhook off with a reason; switching it on again clears that. A webhook that is switched off (by you or by the server) keeps no queue: what was waiting is dropped, and what happens while it is off is not replayed. The same holds for the whole feature: with `SCRIOBH_WEBHOOKS=off` the server parks the feeder at start (it records that no feeder is running and empties the outbox), the event log is pruned as usual after its 90 days although a webhook row is still enabled, and when the feature is switched on again the feeder starts at the newest event, so nothing that happened or waited while it was off is ever sent (the rows stay enabled and need no attention). A delivery that has waited 14 days (a project left in the trash, a secret never rotated) is dropped. One project may hold 1,000 deliveries and use 120 of the 300 a minute, and half the send slots. A project in the trash sends nothing and keeps its queue; restoring it resumes. A restore on a machine with a different `SCRIOBH_SECRET_KEY` cannot read the secrets: the webhooks switch themselves off (after the pause every restore brings) until their secrets are rotated and they are switched on. **A restore pauses every webhook.** A backup holds the outbox and the feeder's place in the event log, and a copy restored on a second machine with the same secret key could sign and send both to your real receivers. So `scriobh restore` switches every webhook off with the reason "Paused when this copy was restored from a backup", empties the outbox and parks the feeder, and says so in its output; the project's owner or an administrator switches each webhook on again from the Webhooks page (or `scriobh-cli webhook update`), and it then hears only what happens from that moment. (This is the same on the machine the backup came from: a webhook is not silently live after a restore.) A backup contains the webhook configuration, the delivery list and the secrets sealed with the key (which a backup never holds); the Markdown mirror, Git pushes and exports hold none of it. Deliveries are listed for 14 days (dead ones 28).

## Mail

Scríobh sends no mail until an administrator sets up a relay and switches mail on, and then only what a person asked for: an EPUB to their own Kindle (Send to Kindle) the email digest they turned on, or the progress email they turned on. There is no environment variable for it: the relay is set in the mail settings by an administrator in a browser session (the administrator's Email page at `/admin/mail`, which calls the REST route `PUT /api/v1/admin/mail`). An API key of any scope is refused, so neither `scriobh-cli` nor an agent can set it or send mail, and none of it is an MCP tool. With mail off the server makes no connection to any relay and the outbound-request budget stays at zero.

**The relay.** Host, port, user name, password, sender address and name, and how the connection is protected. `starttls` (the default, port 587) upgrades the connection before anything is sent and refuses a relay that does not offer it; `tls` (port 465) is encrypted from the first byte. The certificate is checked against the system roots, or, for a relay with a certificate of your own, against the authority's certificate pasted into the settings (never a private key). TLS 1.2 or later. The user name and password are sent only over an encrypted connection (AUTH PLAIN or LOGIN). `none` sends in the clear and is allowed only to a relay on this machine, only if you choose it, and never with a password. The password is stored encrypted with `SCRIOBH_SECRET_KEY`, never shown, never in a log, an audit entry or an export; changing the host, port or user name removes it, so a stored password is never sent somewhere you did not enter it for. A backup holds it sealed, as it holds the other sealed secrets, and never holds the key.

**Where the relay may be.** The address is checked when the connection is made. A public address is always allowed. A relay on this machine or your own network (the normal case for a home-lab relay) needs `allow_private`; without it the setting is refused and so is the connection. Link-local, cloud-metadata and reserved addresses are never allowed, with or without it, and no proxy from the environment is used. The same guard as for Git sync and webhooks, applied to the address that answers.

**Test it.** `POST /api/v1/admin/mail/test` sends one message through the stored relay to your own account address and says what happened as a class (`dns`, `refused`, `timeout`, `tls_cert`, `starttls`, `auth`, `rejected` and so on) and a sentence. It goes nowhere else, at most once every five seconds. What the relay said is never kept: a hostile relay can repeat what it was sent.

**What is sent, and to whom.**

- *Send to Kindle.* A person stores their own address (name@kindle.com) and previews what would go: the EPUB's name and size, that address as the only recipient, what is held back (no notes, no ghosted text, no alternatives, no author contact block, whatever the project's presets say) and a plain statement that the manuscript leaves this server. `kindle.send` needs the preview's token (once, within 15 minutes) and goes to that stored address only: there is no way to name another. Project editors and owners, in a browser (an instance administrator counts as the owner of any project, as everywhere); a project in the trash refuses. Amazon accepts a document only from an address on the person's Approved Personal Document E-mail List, so the sender address (shown to them) must be added there first; that is done at Amazon, not here. The largest attachment is 12 MB by default (`max_attachment_mb`, up to 40): a relay counts the encoded size, a third more, and many refuse over 20 or 25 MB.
- *The digest.* A person who has an account address turns it on, daily or weekly. It goes to that address only. It says how many new mentions, comments and suggestions are waiting for them, by project title, with a link to this server's base address (`SCRIOBH_BASE_URL`, when it is a real address rather than localhost; otherwise no link), and nothing else: never manuscript text, comment text, a document's title or another person's name. It is sent at about 07:00 in the person's zone, only when there is something new, and once per period.

- *The progress email.* Off until a person turns it on, weekly (Monday morning, for the week before) or monthly (the first, for the month before), at about 07:00 in their zone, to their account address only, for the address it was turned on for. It says numbers and plain words: the words added in the period and the period before, writing days, minutes, the days in a row and the goals counted, and a link to this server's base address and to the settings page to turn it off. It never carries manuscript text, a document's title or a project's name, and a project with a passphrase, one in the trash or one the person left is not counted, so a period in which that leaves nothing to report sends nothing. It is made again from its dates when it is sent. A restore pauses it with the rest of mail, and nothing written while mail was off is sent when it comes back.

**Where a Kindle edition may go.** Only to an address on `kindle_domains`: `kindle.com` and `free.kindle.com` unless an administrator widens the list (`mail.config.set`, `kindle_domains`; plain domains only, never a wildcard or "any"), so the relay cannot be used to send an attachment from your sender address to any mailbox. The list is checked when a person stores the address, at the preview and the send, and again when the message is sent. The project's send log keeps the full address sealed and shows it to the project's owner and to administrators.

**A project with a passphrase.** A digest never names one (a digest has no session to unlock it), and a Kindle edition queued before the passphrase was set or changed is cancelled when it comes due.

**The outbox.** Every message goes through a database queue so a restart loses none and sends none twice. A person may have 10 messages waiting and the server 500 (and 256 MB of attachments); a person may make 6 Kindle sends and 3 digests an hour and the server 600 messages an hour; sending is paced at 30 a minute. A message that the relay turns away for now (a 4xx, a timeout, no connection) is tried again after 1 minute, 5 minutes, 30 minutes, 2 hours and 6 hours, six attempts in all; a refusal for good (a 5xx) ends it at once. A problem with the relay's own settings (the password, the certificate, STARTTLS) pauses sending for ten minutes without using up any message's attempts, until the settings are changed or a test passes. A digest older than 6 hours and a Kindle edition older than 24 are not sent. A digest is made again from its window of notifications at the moment it is sent, so a project trashed, locked or read while the relay was down is not named, and one with nothing left to say is dropped. If the relay may have accepted a message and the answer was lost (or the server stopped just then) the message is **not** sent again: it ends as `delivery_unknown`, and the person can send again. `GET /api/v1/admin/mail/outbox` shows counts and the newest 30 messages by kind, state, attempts, failure class and the recipient's domain; it never shows an address, a subject or text.

**Switching mail off, and restoring.** Switching it off drops what is waiting (nothing is sent late). **A restore switches mail off**, with a reason, empties the queue and marks the pause as its own (a changed setting, a new password or a test does not lift it), as it does for webhooks and Git remotes, so a copy restored on a second machine never mails anyone. An administrator switches mail on again in the settings; it then sends only what is made from that moment, and no digest covers the time it was off. The pause is fail-safe: the restore writes a marker (`mail.restore-pause`) in the data directory as soon as the files are in place, and the server pauses mail at its next start if the marker is still there (a restore that stopped half way, or a step that failed). If the restore reports `mail is NOT paused`, start the server only after it is, or pause it by hand: `sqlite3 scriobh.db "UPDATE mail_config SET enabled = 0, restore_paused = 1; DELETE FROM mail_outbox WHERE state IN ('pending','sending');"`. A password that the installation's secret key cannot open (a restore without the original key) also switches mail off, with that reason.

**What mail does not do.** No HTML, no images, no tracking; no sign-up or invitation mail; no reading of bounces (a relay's later bounce goes to the sender address's own mailbox); no DKIM or SPF (they belong to the relay); no one-click unsubscribe (a digest's `List-Unsubscribe` points at the person's settings page, where they sign in and turn it off).

## A project passphrase: what it is not, and a forgotten one

A project's owner can protect it with a passphrase (the project's settings, `project.passphrase.set`). It is a lock for the screen, **not encryption**: it keeps a shared screen, a borrowed laptop or someone using a signed-in session out of the project. You, the person who runs the server, still hold every project's text: it is in the database file, in backups, in the Markdown folder under the data directory and in whatever the Git remotes got. Nothing in this section changes that, and there is no setting that does.

What the server does with one: nothing of the project is served to a session that has not unlocked it (an unlock lasts the owner's idle limit, 30 minutes by default, and never more than 12 hours, and ends when the session does); the mirror, Git pushes, webhooks, search and mention indexes, backups and a running AI run carry on, because they are not a person at a screen; API keys are refused at a protected project unless its owner accepted keys and approved the key (or it was made by a session that had the project unlocked). Wrong passphrases are counted per session and per person, and a counter that has earned a wait refuses even the right passphrase until it has passed (30 seconds, doubling, at most an hour); the client address only slows someone who has already got it wrong, so a person sharing an address with someone guessing is not locked out. An owner who lets API keys in (off by default) approves them one by one. There is no setting to configure: the limits are fixed and the cost of a check is a few microseconds.

**A forgotten passphrase.** Nothing is lost: the text is not encrypted. There are two ways back. An administrator who is not the project's owner can remove it without the passphrase (`DELETE /api/v1/projects/{id}/passphrase` from a signed-in browser session; recorded in the audit log as `project.passphrase.admin_clear`). An administrator who is the project's owner (the usual one-person instance) needs the passphrase like any owner, so that a borrowed laptop cannot clear it; for that person, and for anyone, the operator's command, run on the server with the server stopped, is `scriobh passphrase clear --project ID` (recorded as `project.passphrase.operator_clear`); the owner then sets a new one. An administrator who is not a member of a project cannot set a passphrase on it.

## HTML export and covers

**HTML export** (`export.html`, `POST /api/v1/projects/{id}/export/html`, and the Export dialog's "HTML, to read or share") writes the manuscript as a document to read or share. `mode` is `single` (one self-contained `.html` file: title page with the cover, contents, every document under a heading with an anchor) or `bundle` (a `.zip` with `index.html`, one page per document in the same folder layout as the Markdown bundle, previous and next links, and `scriobh.json`). `notes` adds documents of kind note after the manuscript, as in the Markdown export.

It differs from the Markdown export in what it leaves out, because it is a document to read rather than a copy to restore:

| In the stored text | Markdown export | HTML export |
| --- | --- | --- |
| Front matter (`---` block) | verbatim | left out |
| HTML comments, including the alternatives annotation | verbatim | left out |
| `%%private notes%%` | verbatim | left out; `private_notes: true` keeps them, marked |
| Raw HTML | verbatim | shown as text, never interpreted |
| Links | verbatim | `http`, `https`, `mailto` and in-page only; anything else keeps the link text |
| Images | verbatim | inline raster `data:` images only; anything else becomes "[image: alt (not included)]" |

The page makes no request and runs nothing: the styles are one inline `<style>` using the system serif fonts, a `Content-Security-Policy` meta allows that style by hash and inline images and nothing else (no script, no form, no frame, no remote load), and a print stylesheet and `prefers-color-scheme` are built in. The output is deterministic: the same project state gives the same bytes (anchors come from reading order and the title, never from ids; the bundle is a sorted, stored zip with a fixed time). Each export is recorded in the exports table with the ids and source hashes of the documents it held; in a bundle's manifest `sha256` is the hash of the Markdown a page was built from. The caps are the Markdown export's: 2,000,000 words, 5,000 documents, 64 MB of text. Neither export honours a document's "include in compile" flag yet; the compile presets will.

**Covers.** `project.cover` (`GET /api/v1/projects/{id}/cover`, any member) returns the project's cover as an SVG, drawn from its title, subtitle, author, kind and cover colour: a flat fill from the cover palette, the title wrapped and shrunk to fit, a hairline rule, the subtitle, the author and the kind in the system serif stack. No gradient, no script, no style element (presentation attributes only), hostile text escaped and invalid XML characters removed. It is sent inline with an `ETag` that is the hash of everything the cover shows, `Cache-Control: private, no-cache` (the browser revalidates, a match is a `304`), and its own policy (`default-src 'none'; sandbox`). The library tiles still use the CSS covers; an `<img src="/api/v1/projects/ID/cover" alt="">` can replace them. There is no PNG cover: rasterising text needs a font, and nothing here bundles one.

## Compile: DOCX, EPUB, Markdown and the submission bundle

**Compile** (`export.compile`, `POST /api/v1/projects/{id}/export/compile`, CLI `scriobh-cli export compile <project> --preset shunn-us --out manuscript.docx`; `--options '{"font":"Courier New"}'` overrides for one compile) writes the manuscript with a preset. Run `compile.preview` first: it writes nothing and returns the title page, the running header, the chapters with their words and estimated starting pages, the first pages as blocks, and warnings.

| Preset | Output |
| --- | --- |
| `shunn-us`, `shunn-us-courier` | US Standard Manuscript (Shunn) DOCX: Times New Roman or Courier New 12 pt, double-spaced, letter, 1-inch margins, a title page with your contact block top left and "about 84,300 words" top right, the title a third of the way down, a running header of `Surname / TITLE / page` from page 2, each chapter on a new page a third of the way down, `#` for scene breaks, a half-inch indent that skips the first paragraph after a heading or a break |
| `uk-manuscript`, `uk-manuscript-courier` | The same on A4 with a "Word count: 84,300" line, a mixed-case header and the language `en-GB` |
| `styled-docx` | A reading copy: Georgia 11 pt, 1.15 spacing, justified, bold headings, a centred title page, page numbers in the footer |
| `epub3` | An EPUB 3 e-book: cover, title page, contents, chapters, footnotes at the end of each chapter, system fonts and no colours |
| `plain-markdown` | One Markdown file of the chosen text |

What is in: documents in reading order, front matter first and back matter last, parts as heading pages, chapters numbered by the preset ("Chapter 3: Title", the title, the number, or none), scenes joined by a scene break (`#`, `* * *` or your own ornament) in the preset's style. What is never in: documents marked **do not compile** (and everything inside one), notes (unless `notes: appendix`), private notes, ghosted text (unless `ghosts: keep`), each document's alternatives comment, front matter blocks, HTML comments. Raw HTML is shown as text. A link is its text (the address in parentheses only with `link_urls`): a DOCX never holds a live link. Images are the project's own attachments and small inline pictures; a web address is never fetched. The options are in `compile.preset.list` with their values; `compile.preset.create` saves your own.

**Your contact block** (`compile.contact.set`: name, surname for the header, address, email, phone) is what a manuscript title page shows. It is stored with the project, written into manuscript files, and kept out of export manifests, the audit log, events, the mirror, Git pushes and stored idempotent responses (the two ops whose responses hold it are never replayed).

**Submission bundle** (`export.submission`): one zip with the manuscript DOCX, `synopsis.md` (the project's description, or an outline of the chapters' synopses), `cover-letter.md` (a template with your facts filled in and brackets for the rest) and `manifest.json` with the hashes.

Every compile is recorded like the other exports (`exports`: the preset, the options, the id and hash of each document, an `input_hash`), is deterministic (the same project and options give the same bytes: times in the file are the project's `updated_at`), and is limited as the others are: 2,000,000 words, 5,000 documents and 64 MB of Markdown. A 120,000-word, 60-chapter book compiles in about 25 ms and 40 MB of allocation (measured, Apple silicon, see the decisions). The single-file Markdown and the HTML exports now also leave out documents marked do not compile unless `include_excluded` is set; the Markdown bundle (the zip, the full copy that can be restored) keeps everything unless `include_excluded` is false. Set `contact: false` to leave your contact block off a manuscript title page, and `clear` (a list of option names) to empty a text option such as `byline`.

### Print: PDF, paged HTML and covers

`export.compile` with a print preset (`print-6x9`, `print-5.5x8.5`, `print-5x8`, `print-a5`, `proof-letter`, `proof-a4`) writes a print-ready PDF; `compile.preview` with the same preset runs the same layout and returns the **real** page count, the page each part starts on, the geometry and the first pages as placed lines, and the warnings. Fonts are embedded; nothing is fetched; no browser or other program is run. A 150,000-word book is laid out and written in about 0.2 s (Apple silicon) into about 450 pages and 1 MB; a book of 2,000,000 words takes about 2 s, 14 MB and about 1.7 GB of heap, which is why a PDF compile is refused above `SCRIOBH_COMPILE_PDF_MAX_WORDS` (600,000 by default; compile a part at a time) and why at most `SCRIOBH_COMPILE_CONCURRENCY` (2) compiles run at once: a third waits two seconds and is answered 429 "busy, try again". A book the layout would have to cut (more than 20,000 pages, or footnote text over 4 MB) is refused, never written without its text.

| Preset | Output |
| --- | --- |
| `print-6x9`, `print-5.5x8.5`, `print-5x8`, `print-a5` | Paperback interiors at 11 or 10.5 pt, justified, mirrored margins with a 0.15 inch gutter, chapters on right-hand pages, running heads, roman front matter, contents, footnotes at the foot |
| `proof-letter`, `proof-a4` | A proof to print at home and mark: 12 pt, one-inch margins, chapters on a new page |
| `paged-html` | One HTML file with CSS paged media for the 6 x 9 page; print it to PDF from a browser (margins none, background graphics on) |

Options worth knowing: `drop_cap`, `chapter_ornament`, `crop_marks` (a proof with 0.125 inch bleed and marks), `footnotes` (`page`, `chapter`, `book`), `imprint` (the copyright page text: the program never reads the clock, so the year is yours to write), `gutter_inches`, `justify`, `toc`. The warnings say what needs a look before the file goes to a printer: `gutter_small`, `few_pages`, `image_low_resolution`, `missing_glyph`, `widow_orphan`. Printing services publish their own margin and page-count rules and their own paper thickness: the numbers the program warns about are typical, not any printer's.

**Cover.** `compile.cover_spec` computes the spine and wrap from the page count and **your printer's thickness per page** (`page_thickness_in` is required: a guessed spine is a cover that does not wrap); `export.cover` writes a two-page PDF, the full wrap and the front, in the project's flat cover colour. It is a typographic cover for a proof or a plain edition, not a designed one: check it against your printer's template.

**Importing** a Word document or an Obsidian vault is the import dialog's, with the preview first: `import.preview` with `docx` (the file) or `layout: obsidian` (the vault as a zip or a folder). From the command line: `scriobh import book.docx`, `scriobh import ./Vault --layout obsidian`. The preview lists what was left out: images, text boxes, macros (never run), dataview blocks, canvases. A Word file's **tracked changes** become pending suggestions and its **comments** become comments, by the Word authors' names as labels (never accounts); `docx_changes` chooses `suggest` (the default: the author's text, each change proposed), `accept` or `reject`; the caps are 300 suggestions for a document and 1,000 for an import, and an import that would take a project past its 2,000 open suggestions is refused whole. From the command line: `scriobh import book.docx --docx-changes accept`.

**Importing a PDF** (`import.preview` with `pdf`, up to 32 MiB; `scriobh import book.pdf`): the text layer is read with the standard library only. Chapters come from the outline or from headings set larger than the text; the preview carries a fidelity report (what could not be recovered, a confidence for each page) and says the rule by which running heads and page numbers were removed. A scanned PDF (most pages are pictures) and an encrypted one are refused, whatever the password; nothing in a PDF is run (JavaScript and launch actions are counted and ignored, attachments are never extracted). **The file is read by a child process, not in the server.** The importer runs the server's own binary again with the argument `pdf-extract` (the server's `main` and each test binary's `TestMain` call `imports.MaybeRunPDFChild` first). The file goes in on its standard input and one JSON document comes out on its standard output (at most 64 MiB; its standard error is discarded); its environment holds three variables (the marker `PDFCHILD`, `GOMEMLIMIT`, a wall clock) and no secrets or paths, and nothing of the request is in its arguments. It runs in a process group of its own. The server kills the whole group with SIGKILL when 20 seconds pass (`SCRIOBH_PDF_TIMEOUT_SECONDS`, lower only), when its resident memory passes 512 MiB (`SCRIOBH_PDF_MEMORY_MB`, lower only; read every 75 ms from `/proc` on Linux and from `ps` elsewhere), when it writes more than it may, when the request is cancelled or the client goes, and when the server shuts down. The child also sets `RLIMIT_CPU` (every Unix) and `RLIMIT_DATA` (Linux only; macOS and the BSDs do not enforce a data or address-space limit on a Go program, so there the polling and `GOMEMLIMIT` are the limit), and on Linux asks to be killed with its parent. Where there is no process table to read (Windows), memory is `GOMEMLIMIT` alone and the child is killed singly. A crash, a fatal error such as a stack overflow, a kill by the kernel, a hang or a flood of output is a refusal of that file ("could not be read"), never a fault of the server. The read counts as one of `SCRIOBH_IMPORT_CONCURRENCY`'s places, and one person has one PDF being read at a time (a second is answered `429 rate_limited`). Large request bodies (8 MiB and over, as a PDF in a JSON body is) are counted together: 192 MiB across the process, each charged twice its Content-Length for the decoded copy; one that would pass it is answered `429 rate_limited` ("busy") before it is read. Limits: 3,000 pages, 20,000 page-tree nodes, 32 MiB for a stream inflated and 384 MiB for the file, 32 MiB of estimated parsed memory (charged while parsing), 50,000 name-tree nodes, 150,000 pieces of text and 256 MiB of content interpreted for the file, an inflation ratio of 30 beyond 512 KiB, 600,000 drawing operators for a page and 30 million for the file, 48 MiB of text, 100,000 entries in a font's character map and 24 MiB of map text for the file, 20 seconds for the whole read (the reading child's wall clock). A PDF of 304 pages previews in about 0.14 s and applies in about 0.03 s on a development laptop.

## The Pandoc bridge (optional)

[Pandoc](https://pandoc.org) is a program that converts between document formats. When it is installed and an administrator switches the bridge on, **import** accepts odt, rtf, latex, mediawiki, org, textile, rst, fb2, typst and docbook files (`pandoc` and `pandoc_format` on `import.preview` and `import.apply`, and on the append pair), and **compile** can write odt, rtf, latex, rst and mediawiki (the `format` option, in a custom preset or an override). Off by default and when the binary is not found. Details, limits and what was not verified: [p7-notes/pandoc.md](p7-notes/pandoc.md).

**Switching it on** (an administrator, in a browser: `pandoc.config.get`, `pandoc.config.set`, `pandoc.config.test`). Give the absolute path of the binary, or leave it empty to find `pandoc` on the server's `PATH`. It must be a regular file (after links) owned by root or by the user the server runs as, not writable by its group or anyone, in a folder that is not either, not inside the data directory, version 3.1.6 or later (a build older than that is refused with a message naming CVE-2023-38745: it can be made to write a file anywhere the server can, and `--sandbox` does not stop it); it is run once with `--version`, and checked again before every conversion. A restore switches the bridge off; the path stays, so switching it on again is one click. `pandoc.config.test` converts a small text each way with exactly the arguments, environment and limits of a real conversion and reports each result. The bridge is not available off Unix.

**How it is run.** Never through a shell. The argument list is fixed: `--sandbox`, an empty `--data-dir` made for the call, `-f` and `-t` with names from the lists above (no extension suffixes), `--wrap=none`, and, for an import that asked for pictures (`pictures: import`) and only then, `--extract-media=media`; an export reads the compile Markdown as `gfm-raw_html-yaml_metadata_block`; no filter, template, include, resource path, metadata or defaults file, and nothing a person typed is an argument. The input is a file with a name chosen by the server in a folder made for the call and removed after. The environment is built by the server (no `HOME` of the server's, no proxy variable, nothing starting with `PANDOC_`; `SOURCE_DATE_EPOCH` is fixed so that a written file's date does not change). The process has its own process group, which is killed on the deadline (60 seconds), when the request ends, when it writes more than the cap (64 MB) to standard output, or when its folder holds more than the cap while it runs (256 MiB at most, checked every 50 ms); standard error is read and thrown away, however much there is. A conversion does not start when the volume the folder is on has less than 512 MiB free, and a file Pandoc makes beside its folder is deleted and fails the conversion. At most two conversions run at once, and one per person; a third waits a few seconds and is told to try again. The memory setting (default 1024 MB) is passed as Pandoc's own heap limit (`+RTS -M`); set it to 0 for a build that does not accept RTS options. On Unix the server starts Pandoc through a re-execution of itself (`scriobh --scriobh-pandoc-helper`, built in; it sets CPU time, open-file, file-size and core-dump limits and then becomes Pandoc; it does nothing unless the server started it, because the call carries a token on a pipe that a typed command does not have); when that is not available `pandoc.config.get` says `resource_limits: false`.

**What comes back.** An imported file is converted to GitHub-flavoured Markdown (at most 2 MiB of it is read) and then read like any other foreign Markdown, a piece of 64 KB at a time with a deadline: raw HTML is dropped, every character that could be a private note, a ghost, an alternatives comment or a wiki link is escaped, control and bidirectional-override characters are removed, and the result is cut into chapters at the first heading level, as for a `.docx`. Pictures Pandoc extracts are admitted only through the same picture checks as any import's. The source is recorded in `project_sources` (kind `markdown`, layout `pandoc`, the file's SHA-256, the format and Pandoc's version). An exported file is the compile Markdown converted by Pandoc, recorded like every export with the Pandoc version in its manifest and in its input hash; the same text gives the same file only with the same Pandoc version. The server never opens or interprets a file Pandoc wrote.

## Publishing a static site

**What it is.** A project of kind *blog series* (or any project) can be turned into a plain website: HTML pages, an index with pagination, tag and category pages, RSS and Atom feeds, a sitemap, a stylesheet and the images the posts use. No script, no inline style, no font or request to anywhere else: it serves under `default-src 'self'`. It is generated from the manuscript and never writes back: the folder is a projection that the next publish replaces.

**What goes in.** Chapters are posts (a scene inside a chapter is folded into it, and so is a chapter nested in a chapter); `options.posts` = `top_level` makes every top-level document a post instead; a part is a container whose title is the posts' category; front and back matter documents are pages (an About page). Only documents whose status is `done` are published (`include_drafts` adds `draft` and `revise`; an idea or an outline is never). A document marked **do not compile**, and everything inside one, is never published; neither are notes, research, the Compendium, comments, suggestions, revisions, your contact block, or anyone's email. In a post: private notes, ghosted text, the alternatives annotation, HTML comments and front matter are gone; raw HTML is shown as text; links keep only http, https and mailto; an image must be a PNG, JPEG, GIF or WebP attachment of the project (never an SVG, a web address or a `data:` picture) and is copied under a hash of its bytes, with its width and height, **cleaned by an allow-list** (only what draws the picture is kept: EXIF with its GPS position, XMP, IPTC, comments, thumbnails and C2PA records are gone, and so is anything after the end of the file). A JPEG that says it is rotated (EXIF orientation 2 to 8) is rotated for real and saved again at quality 92 (one `image_rotated` warning: the colour profile of such a photo may be lost), because without the orientation note a portrait photo would lie on its side; one too large to turn (over 25 megapixels) is left out and the preview says it is too large. The same cleaning applies to the pictures of DOCX and EPUB compiles. **An address that has been out does not move.** The first build or publish writes down each post's address (`pinned_slug`, shown by `site.doc.list`); later, a newer post with the same title, an older post finished late or an address someone chooses by hand cannot take it, and a title change leaves it alone (`site.doc.set` refuses an address that is pinned to another post, and `clear: ["pinned_slug"]` lets one be made again). An address of a post that went back to draft stays reserved for it. Previews show the same addresses a build will, but write nothing.

The excerpt comes from `site.doc.set` or, if none is set, from the post's first paragraph; the synopsis fields are planning notes and are never used. **What is unpublished hides what is inside it**, at any depth: a done scene under a draft, an idea, a skipped or a do-not-compile scene under a done chapter is not published, nor is anything under a skipped part, chapter or page (the preview lists each as "inside a document that is not published"). A **part** is a container: it is not a post and its own text is not published (the preview says so); its title is the category of its chapters only if the part itself could be published (status `done`, or `include_drafts`; not marked do not compile, not skipped), so mark the part done.

**Steps.** On the project's Site page: set the options (a base address is what makes feeds, the sitemap and canonical links possible: without one they are not written and the preview says so), set each post's date, address, excerpt and tags (no date means the date it was created, with a warning), read the preview (the posts, what was left out and why, the warnings), then publish to the folder, or download the zip. Publishing makes text public, which is the same act as a share link, so the three operations that decide or perform it (`site.options.set`, `site.doc.set`, `site.publish`) are for a person signed in with a browser: an API key of any scope, pinned or not, is refused with 403, `scriobh-cli` (which signs in with a key) cannot call them, and they are not MCP tools, so an agent can draft the text but never makes it public. What stays open to a key that may read or write the project is the reading side: `scriobh-cli site status`, `site options get`, `site doc list`, `site preview <project>` and `site build <project> --output site.zip` (a download for whoever asked for it; it makes nothing public). Editors and owners can use the page; a reader gets 403. Nothing is ever published by itself. `site status` shows the last build and publish (time, counts, hashes); a fresh `site preview` with a different `input_hash` than the last publish's means something changed since.

**Publishing to a folder.** Set `SCRIOBH_SITE_DIR=/srv/sites` (or `SCRIOBH_SITE_PUBLISH=1`). A project's site is then at `/srv/sites/<project slug>--<last six of its id>/public/`; the name is fixed at the first publish, so renaming the project does not move the folder your web server points at. The write goes to a sibling folder that is renamed into place (a reader sees the old site or the new one, apart from the instant between two renames), and beside `public/` a record `managed.json` lists what was written. A later publish replaces `public/` only if it is empty or holds nothing but files in that record, **unchanged**: a file you (or something else) added, or a file Scríobh wrote that was edited by hand since (its SHA-256 no longer matches the record), makes it answer 409 naming the file and change nothing; `site.publish` with `overwrite_edited: true` replaces the edited ones (an added file still stops it). A write that fails (a full disk, a folder Scríobh may not write, two names the file system treats as one) is also a 409 that says why, never a bare server error. It refuses a symlink anywhere in the target and a folder that belongs to another project, writes files 0644 and folders 0755, and never touches anything outside `<slug>--<id6>/`. Purging a project for good removes its published site by the same rule. The sites folder is not in backups: it is generated, publish again after a restore.

In a container, give Scríobh a writable volume for the sites folder and mount the same volume **read-only** into the web server:

```yaml
services:
  scriobh:
    environment: { SCRIOBH_SITE_DIR: /sites }
    volumes: [data:/data, sites:/sites]
  caddy:
    volumes: [sites:/srv/sites:ro]
volumes: { data: {}, sites: {} }
```

**Serving it: Caddy.** Use the folder of the project (`ls /srv/sites`), a CSP that matches what the site is, and the 404 page.

```
blog.example.org {
	root * /srv/sites/the-salt-road--abc123/public
	encode zstd gzip
	header {
		Content-Security-Policy "default-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"
		X-Content-Type-Options nosniff
		Referrer-Policy no-referrer
		-Server
	}
	@pictures path /assets/img/*
	header @pictures Cache-Control "public, max-age=31536000, immutable"
	file_server
	handle_errors {
		rewrite * /404.html
		file_server
	}
}
```

Caddy lists no directories unless you ask (`browse`), and `/` serves `index.html`.

**Serving it: nginx.**

```nginx
server {
    listen 443 ssl;
    http2 on;
    server_name blog.example.org;
    ssl_certificate     /etc/ssl/blog.example.org/fullchain.pem;
    ssl_certificate_key /etc/ssl/blog.example.org/privkey.pem;

    root /srv/sites/the-salt-road--abc123/public;
    index index.html;
    autoindex off;
    server_tokens off;
    error_page 404 /404.html;

    add_header Content-Security-Policy "default-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'" always;
    add_header X-Content-Type-Options nosniff always;
    add_header Referrer-Policy no-referrer always;

    location / { try_files $uri $uri/ =404; }
    location = /404.html { internal; }
    location /assets/img/ {
        # add_header in a location replaces the ones above, so they are repeated
        add_header Content-Security-Policy "default-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'" always;
        add_header X-Content-Type-Options nosniff always;
        add_header Referrer-Policy no-referrer always;
        add_header Cache-Control "public, max-age=31536000, immutable" always;
    }
}
```

**Good to know.** Every link is relative and names its file (`posts/my-post.html`), so the site works from any folder, a sub-path (`https://example.org/blog/`, set as `base_url`) and `file://` (opened and clicked through in Chromium from disk), and the proxy needs no rewrite rules. Crawlers read `robots.txt` only at the root of a host: for a site under a sub-path it has no effect, and the `noindex` meta tag on each page is what holds. The two server snippets above were written from the servers' documentation and were **not run**; the site's own pages were served under the same policy by a small stand-in server and loaded with no violation. The 404 page is served at addresses of every depth, so its links cannot be relative: with a base address they are absolute, without one they start at `/` (right at the root of a host, wrong in a sub-path). `base_url` must be the real public address: feeds carry absolute links, and a staging site should use `noindex` (robots.txt disallows everything, every page says noindex, no sitemap). The pages list at most six pages in the navigation (the About page first) and then a link to `pages/index.html`, which lists them all. Dates are shown as `YYYY-MM-DD`; the site's own words ("Older posts", "min read", "Skip to the content") are English whatever the language option says (it sets `lang` and the text direction; paragraphs and headings use `dir="auto"`, so an Arabic post in an English site reads right to left). Feeds are `feed.xml` (RSS 2.0) and `atom.xml`, each with the newest `feed_items` posts, full text or an excerpt; entry identifiers are hashes of the document ids, so renaming a post or changing its address does not make readers see it again. The accent is one of five (`moss`, `slate`, `sepia`, `meadow`, `contrast`), each checked at 4.5:1 for text on the page in both the light and the dark theme; the site follows the reader's `prefers-color-scheme` and prints cleanly. The limits: 5,000 posts, 256 MB of site, 10 tags a post, 40 characters a tag. A build of 2,000 posts (218,000 words) takes about 0.1 s and 160 MB of allocation in the generator (the database read and the images come on top).

**Importing from Scrivener, WordPress or Ghost** is the same dialog and the same preview, with `files` or `zip` and, when the source is not recognised by its own markers, `layout: scrivener`, `wordpress` or `ghost`:

- *Scrivener* (a `.scriv` folder, zipped or as a listing; Scrivener 3's `Files/Data/<UUID>/` and Scrivener 2's `Files/Docs/<id>` layouts). The Draft becomes the manuscript (a folder that holds folders is a part, a folder of documents a chapter, a document in a chapter a scene, a document in the Draft or a part a chapter; folders named like front or back matter are that kind), synopses and Include in Compile come across, Status becomes a document status (`To Do` idea, `In Progress` and `First Draft` draft, `Revised Draft` revise, `Final Draft` and `Done` done; any other is draft and listed), a document's notes and the annotations and comments inside the text become private notes, footnotes become footnotes, links to other documents become wiki links when the title is unique. The Research folder becomes research notes (`scrivener_research: notes`, the default, or `skip`), keywords and labels as tags; images, PDFs and web pages are **not imported** (a note names each). Characters and Places folders are marked as candidates for entities and imported as research notes; nothing creates an entity. Trash is left out and counted. Labels and keywords of manuscript documents, snapshots, collections, custom metadata and targets are counted, not kept. `scrivener_notes: skip` leaves documents' notes out.
- *WordPress* (the WXR `.xml` of Tools, Export). Posts (and pages with `wp_types: pages` or `both`, in a part called Pages) become chapters of a blog project, oldest first, with the date as the story date, the excerpt as the synopsis, the slug in the source path. Drafts, pending, scheduled and private posts are imported only with `wp_status: all`, and are marked do not compile. Categories, tags, custom fields, comments (never read: neither commenters nor addresses) and attachments (listed by address, never fetched) are reported. Gutenberg blocks, classic editor text, `[footnote]` shortcodes, the footnotes block and simple tables are read; scripts, iframes, forms and embeds are dropped.
- *Ghost* (the JSON of Settings, Labs, Export). `ghost_status` and `ghost_types` as above. Lexical (Ghost 5), Markdown cards and mobiledoc Markdown (before that), and the `html` field are read; cards that are not text (bookmarks, embeds, galleries) become a note with their address; unknown cards are listed. Tags and authors are reported; e-mail addresses are never read.

From the command line: `scriobh import ./Novel.scriv`, `scriobh import export.xml --wp-status all`, `scriobh import ghost.json`; `--layout` names the source when it is not recognised. Limits: 40,000 files, 32 MB for one RTF file and 96 MB for all of them, 2,000 documents of at most 2 MiB, 5,000 research notes; an export file of 64 MB; 60,000 posts. Over HTTP the request body is at most 48 MiB, so send a large Scrivener project as a zip (the text compresses well). Nothing in any of them is ever fetched, and no outbound request is made. A Scrivener, WordPress or Ghost source is read once for a preview and the apply that follows it (what it was read into is kept for three minutes, at most two sources, under a hash of its bytes, the layout and the options; the apply still checks its token).

**Importing an EPUB book** (`epub`, an EPUB 2 or 3 file of at most 32 MB; `scriobh import book.epub`, with `--epub-sections auto|toc|files|headings`, `--epub-pages skip|keep` and `--edition-label`) is the same dialog and the same preview, with a map from the book's files to the proposed parts, chapters and front and back matter, which the writer edits (rename, kind, skip, merge, move, split, reorder) before anything is written. The reading order, the title, author, language, identifier, publisher and date, the cover, the pictures (stored as attachments, cleaned of their metadata, only those the text shows, and the cover) and footnotes (EPUB 3 `epub:type`, `role="doc-footnote"`, and the in-document anchors older books and Pandoc, Calibre and Gutenberg make) are read; scene breaks (a rule, an ornament, `* * *`, a class that says so) become scene breaks; a book whose text is encrypted (DRM) or laid out as pictures of pages (fixed layout) is refused with the reason. Scripts, forms, frames, embedded media and remote pictures are never kept or fetched. **The book's file is kept**, byte for byte, as an *edition* of the project: `data/files/projects/<project id>/editions/<sha256>.epub`, with its SHA-256, size, label and date recorded and the chapters it was read as. It counts against the project, person and server storage quotas like a picture (the same bytes in two editions of one project are one file), at most 50 editions a project; it is removed with the project's purge, kept in the trash, and carried by a backup (a backup made with `--no-files` has the records and not the book, and the edition then says its file is not there). Only the project's owners and editors can download it (`edition.download`; an API key needs the write scope, as for an export), and it is never part of the mirror, a Git push or an export: those carry the edition's label, hash, size and date and nothing of the file. A file in that folder that no edition names (a crash between placing it and committing its record) is removed by a sweep at start and every hour once it is more than an hour old (the log says how many). A book read for a preview and the apply that follows it is read once (the three-minute memory above). **Editions pages and administrators:** the Editions pages (`/p/{slug}/editions`, the diff and compare screens) find a project by its address through the person's own membership, as every project page does, so an instance administrator who is not a member of a project gets 404 there, while the REST ops (`edition.list`, `edition.get`, `edition.download`, `edition.diff`) treat an administrator as the owner of every project, as every op does. This is the existing rule (an administrator who is not a member is a stranger to the pages, whether or not the project has a passphrase; the gate tests assert it), not something these pages changed; to read or download an edition as an administrator, use the API or join the project.

## The installable app and working offline

Scríobh serves a web app manifest (`/manifest.webmanifest`: name, standalone display, start URL and scope `/`, Paper colours, icons drawn by the server at `/pwa/icon-*.png`) and a service worker at `/sw.js`, so a browser can install it and open it in its own window. On the desktop build the origin is the loopback address the app chose, which browsers treat as secure, so it installs there too. A service worker needs a secure context: `https://` behind a proxy, or `localhost` and `127.0.0.1`.

**What is kept on the device.** The worker keeps the application shell (everything under `/static`: styles, scripts, fonts, icons, about one megabyte, all public) and one fixed `/offline` page that is the same for everybody. A reload with the network cut shows that page ("Scríobh cannot reach the server") in the app's own styles, not the browser's error page. Chapter text a person had unsaved is held by the writing view in the browser's own storage and saved when the connection is back; that does not involve the worker.

**What is never kept: any page.** A page or an API answer holds the signed-in person's name, token and manuscript, and a cache belongs to the browser, not the person: whoever signs in next on the same profile would be served it. So the worker answers navigations from the network only and falls back to the offline page when the network cannot be reached; it never stores a page, a fragment or an API response, and it sends no cookies when it installs. Signing out therefore has nothing to clear, and signing in as someone else cannot show them the last person's pages. The cost: a chapter cannot be opened offline until offline reading (F-262) is built with per-person, per-session storage.

**Updates.** The cache is named for a hash of the shell, so a new build is a new cache and a new worker. It installs beside the old one and waits. The page shows a notice, "A new version of Scríobh is ready", with Reload and Later; Reload tells the worker to take over and reloads the page onto it, so old markup never meets new files. Nothing reloads without that click. Old caches are deleted when the new worker activates. The script is `static/js/pwa.js`; a browser that refuses workers (a private window, a blocked context) goes without, with no error.

`make e2e` (and `e2e-fast`) runs `scripts/e2e-pwa.mjs` in Chromium: the worker registers at the root and takes control, keeps only `/static` and `/offline`, shows the offline notice with the context switched offline and again with the server stopped (restarted on the same port), and one person's pages never reach the next person on the same profile.

### Developer notes: the Go build cache

The Go build and test cache (`go env GOCACHE`) keeps every test binary and every race build and is never trimmed by Go itself; on a machine that runs the full suite, the rehearsals and the browser scripts it has filled the disk. **`make clean-cache`** deletes the cache files that have not been touched for two hours (`find "$(go env GOCACHE)" -type f -mmin +120 -delete`): what a build in progress or one started a moment ago uses is newer and stays, and the next build only recompiles what was removed. Do not run it while someone else is building or testing on the same machine (a file a running build is about to read can disappear under it). The rehearsals and the bench write only to a temporary directory they remove; if one was killed, look for `scriobh-upgrade-*`, `scriobh-restore-*` and `scriobh-bench-*` folders in `$TMPDIR`.

## The writing studio

The dashboard, goals, sprints, nudges, the analytics export, the private progress card and the progress email (`docs/p7-notes/studio.md`) need no setting and make no request of their own: every number is computed on this server from the per-project daily totals the save loop keeps, and nothing leaves it unless a person makes a progress card (a link to this server's own page, which shows a streak and goal summary and nothing of any project) or turns on the progress email (above).

- *Days are the person's own.* A day is cut at midnight in the time zone of the person's profile (a validated IANA name, UTC until they choose one); days already written keep the date they were written under when a person changes zone.
- *Repair.* `scriobh stats recompute [--user EMAIL] [--project ID]` (server stopped) rebuilds the daily totals from the recorded sessions; the dashboard reads those totals and the sessions, so it gives the same numbers afterwards.
- *Cards.* `GET /card/<token>` (the page), `/card/<token>/card.svg` and `/card/<token>/card.json` are public: no cookie is read or set, the token is never logged (the request log records the route), `noindex`, and one rate limit per client address (`SCRIOBH_SHARE_RATE_LIMIT`, shared with share links). A person has at most five live cards; each expires (up to a year) and can be revoked. A page request never writes: views are counted in memory and written at most once a minute and when the server stops; a card's numbers are worked out at most once a minute for each card and four cards at a time, and the token (revoked, expired) is checked on every request. `SCRIOBH_SHARE_RATE_LIMIT` defaults to 120 a minute for each address. Cards revoked or expired more than 90 days ago are deleted by the hourly sweep.
- *Restore.* A sprint running when a backup was taken is closed by a restore; its words stay.
- *Cost.* The dashboard reads one row per project per day for the person (budget p95 under 150 ms for five years over 20 projects: `studio.dashboard` in `scriobh bench`), and the save path adds one indexed probe for a running sprint, in a savepoint of its own so a fault in it cannot cost the day's statistics.

## Performance: `scriobh bench`

`scriobh bench` is the performance gate of `docs/ARCHITECTURE.md`. It builds a synthetic project of each size through the real ops (parts, chapters and scenes of realistic size, wiki links, scene metadata, and revisions), times each budgeted operation, and checks the result against the budgets and against a baseline. It never reads the environment, so it cannot touch a real data directory, and it refuses a directory that holds anything but an earlier bench run.

```
make bench                              # 150k and 500k words, against bench/baseline.json (about a minute)
make bench BENCH_SIZES=150k,500k,1m     # add the million-word project (a few minutes to build)
scriobh bench --sizes 500k --json       # the report as JSON on stdout, the table on stderr
scriobh bench --sizes 500k --write-baseline --best-of 3   # record this machine's numbers (best of three)
scriobh bench --data-dir /var/tmp/bench --sizes 1m        # keep the project and reuse it next time
scriobh bench --cpuprofile cpu.out      # profile a run
```

It measures, as p50 and p95 over `--runs` (default 30) timings: `doc.get` (open a 5,000-word chapter), `page.chapter` (the whole chapter page through the middleware and templates), `page.project`, `doc.list`, `outline.get`, `project.list`, `search.query` (a common two-word query), `search.rare`, `search.prefix`, `page.search`, `studio.dashboard` and `studio.calendar.all` (the writing dashboard over five years of daily data on 20 projects, from a fixture of 36,520 daily rows added after the size is taken), `doc.update` (save a 5,000-word chapter, changing the text each time), `http.save` (the same over REST), `revision.list`, `revision.diff`, the four exports (`export.md.single`, `export.md.bundle`, `export.html.single`, `export.html.bundle`), and once each `mirror.full_write`, `mirror.rebuild_noop`, `search.rebuild`, `backup.create` and `db.size`. Pages are driven through the handler without a socket, so they exclude network time.

**What fails the run (exit 1).** An absolute budget from the architecture document (open a chapter and the chapter page under 150 ms, save under 120 ms, search under 80 ms, project open under 400 ms, autosave round trip under 400 ms, FTS rebuild under 60 s, backup under 30 s, database under 1 GB) when the p95 exceeds it at a size up to 500,000 words; larger sizes are reported with their budget but not gated. And a regression: the p50 more than 20% (`--tolerance`) over the baseline *and* more than a noise floor (5 ms for a metric timed 10 or more times, 15 ms for 3 to 9, 75 ms for one timing, 256 KB for sizes; a timing taken once needs 50%), so a number that went from 0.4 ms to 0.6 ms does not flap. A metric that looks slow is timed once more before the run fails, and the better of the two counts. The regression check wants a machine that is not busy with something else: on a laptop running a test suite alongside, the wide absolute budgets (here 15 to 200 times the measured numbers) are the gate that holds and a 20% check can still flag a busy moment. The two that depend on how fast the disk syncs (`mirror.full_write`, `backup.create`) are never judged against the baseline; the backup keeps its absolute budget.

**Baselines are per machine.** Numbers measured on one machine say nothing about another, so `bench/baseline.json` holds an entry per `os/arch/cpu-count`. A run on a machine with no entry is held to the absolute budgets only and says so; `--write-baseline` adds the entry for this machine (use `--best-of 3`), leaving the others. The development repository's nightly workflow (`nightly-bench.yml`) runs 150k, 500k and 1M words, uploads the JSON report and a candidate baseline, and fails on a budget or a regression. To gate regressions on a CI runner, commit its candidate as `bench/baseline.json`. `make ci` runs a 5,000-word smoke run only, to prove the command works.

**The soak.** `scriobh bench --soak 20m --sizes 150k` (or `make soak`, `SOAK_DURATION=`, `SOAK_SIZES=`) runs a realistic mix against the generated project for the given time with every background worker on, samples memory, goroutines, open files, the WAL and the database, and exits 1 when one of them keeps growing or an operation misses its budget. `--sample` and `--warmup` change the sampling interval (5 s) and the warm-up left out of the judgement; `--out` writes every sample and timing as JSON. What it does, the growth rule and the numbers measured are in `docs/ARCHITECTURE.md` ("Soak"). It writes only to its own temporary or `--data-dir` directory, like the rest of `bench`.

## Upgrades

1. Back up: `scriobh backup` (in the container: `docker compose -f compose.prod.yaml exec scriobh /scriobh backup`), then copy the archive off the machine. Read the release notes for anything that needs a setting.
2. Get the new code and rebuild: with Compose, `git pull` (or check out the release tag), then `docker compose -f compose.prod.yaml up -d --build`, which rebuilds the image and recreates the container on the same volume. For a binary install, build the new `./scriobh` (`make build`), stop the service, replace the binary, and start it.
3. Migrations run automatically at startup, each file in one transaction, after the pre-migration copy described above. `scriobh migrate` does the same without starting the server (`docker compose -f compose.prod.yaml run --rm scriobh migrate`, with the server stopped).
4. Check: `/readyz` (`curl -s http://127.0.0.1:8080/readyz`) says `ready` once every migration is applied, and the log (`docker compose -f compose.prod.yaml logs scriobh`) has a `migrations applied` line naming them. `/healthz` and `scriobh version` print the version label the image was built with, which is `dev` unless you set `SCRIOBH_VERSION` for the build (`SCRIOBH_VERSION=v1.0.0 docker compose -f compose.prod.yaml up -d --build`), so they only confirm the upgrade if you label it.

The server refuses to start if a migration it has already applied has been edited, and refuses to run an older binary against a database that has newer migrations. Roll back by restoring your backup and the previous image, not by editing the database.

The pre-migration copy is `data/backups/migrate/pre-migrate-<timestamp>.db`, a plain SQLite file of the database as the old release left it (the server logs its path). It is never deleted by Scríobh. To go back without an archive: stop the server, put the previous binary back, and copy that file over `data/scriobh.db` (removing `scriobh.db-wal` and `scriobh.db-shm`); anything written since the upgrade is lost, which is why the archive in step 1 is the better way back. The upgrade rehearsal below does exactly this after each upgrade and checks that the old release starts and answers as before. Sessions survive an upgrade: nobody is signed out.

### What changes at the upgrade from v1.0 to v1.1

Twenty migrations run at the first start (1800 to 1910: the editions of imported books, review author labels, Git publishing, signing keys, triggers and the restore pause, the project passphrase gate, Compendium changes, mail and the Kindle, the semantic index, the exports rebuild, typography). They take a fraction of a second on a database of any size that matters: fifteen only add tables, four add columns with defaults (`suggestions.author_label`, five columns on `git_remotes`, and `kindle_domains`, `address` and `recipient_sealed` on the mail and Kindle tables, which are themselves new), and one rebuilds a table. Twenty-five tables are new in all.

- **New tables, all empty.** Twenty-five. None holds a row after the upgrade (the upgrade tests count them), so nothing new is in use: no editions, no passphrases, no mail settings, no semantic index, no signing keys, no triggers, no Git accounts, no typography.
- **`exports` is rebuilt (migration 1895).** Its list of formats had to grow (odt, rtf, latex, rst and mediawiki are written by the Pandoc bridge), and SQLite cannot change a CHECK list in place, so the table is copied, replaced and renamed in the one transaction. Every row, its index (`exports_project`) and its foreign keys (to the project, with cascade, and to the person) come through unchanged; the upgrade tests compare the rows, the index and trigger definitions and the foreign keys before and after, and refuse a bad or missing format afterwards. Nothing else refers to `exports`.
- **The pre-migration copy** is taken first, as for every upgrade: `data/backups/migrate/pre-migrate-<timestamp>.db`, a plain SQLite file of the v1.0 database (59 migrations). Nothing deletes it.
- **A Git remote you already had keeps doing what it did.** It reads as a *private* remote (`audience`), which is the full mirror it was already pushing, unsigned, with no trigger and not paused. A remote you add now is *public* unless you say private. The rehearsal pushes a book from v1.0 to a development Git server, upgrades, pushes again and compares the pushed tree file by file and blob by blob: the same 19 files and no new commit; after one edit it changes that chapter's file and the book's index file and adds or drops nothing.
- **Keys, links, hooks, the site.** API keys (a pinned one included), share links (a revoked one stays revoked), the webhook (signed with its original secret, sent to its sealed address), the sealed AI provider key, idempotency keys (a repeat still replays, another body is still refused) and the static-site options are read back after the upgrade and used; none needs to be entered again. The same secret key must be in place (`SCRIOBH_SECRET_KEY` or `data/secret.key`).
- **Everything new is off.** Mail is not configured, no digest or Kindle address exists, no project has a semantic index, the Pandoc bridge is off with no path, presence sharing is off for every account (`presence.share` is `false` until a person turns it on), no project has a passphrase and no project or person has typography set. Each is switched on by an administrator or an owner, in the screens that were added for it, never by the upgrade.
- **New environment variables.** None is required. One setting of the server is new since v1.0 in the table under [Configuration](#configuration) (`SCRIOBH_PDF_TIMEOUT_SECONDS` and `SCRIOBH_PDF_MEMORY_MB`, which can only lower a built-in limit of the PDF import's reading child process). Three more names are recognised and are not settings: `SCRIOBH_DEV_TRUST_PEM` (a development build only: trust a code host's self-signed certificate in the browser tests), `SCRIOBH_MEASURE_PANDOC` and `SCRIOBH_PANDOC_HELPER_TOKEN` (tests). Mail, Pandoc, Git accounts and the semantic index are configured in the application and stored in the database, not in the environment, so the environment of a v1.0 deployment runs v1.1 unchanged.
- **The pages and the API** only gain: the generated tables in `docs/API.md` and the agent skill list the new operations; the browser-session-only ones (mail, Kindle, Pandoc, passphrases, the semantic index, presence) are not available to a key. Regenerate a local copy of the skill with `scriobh-cli skill` if you keep one.

### What changes at the upgrade from v1.1 to v1.2

Six migrations run at the first start (1930 to 1935: goals and their versions, sprints, nudges seen, progress cards, the progress email, and an index for the dashboard). Seven tables are new and empty (`studio_goals`, `studio_goal_versions`, `studio_chapters_done`, `studio_sprints`, `studio_nudges_seen`, `studio_cards`, `mail_progress`), one is rebuilt, and one index is added to `daily_stats`. The rehearsal from v1.1 applies these six to a database the v1.1 release wrote, and checks that the schema equals a fresh install's, that every answer the old release gave carries every field unchanged, that a second start applies nothing, and that the rollback works.

- **`mail_outbox` is rebuilt (migration 1934)**, because its list of message kinds had to grow by `progress` and SQLite cannot change a CHECK list in place. The rows and the three indexes come through unchanged. On a server with no mail it is an empty table.
- **Everything new is off or empty.** No person has a goal, a sprint, a card or a progress email; the progress email is off until a person turns it on in Settings (and, like every mail, only if an administrator has configured a relay). The one setting that is on by default is the in-app nudges (`studio.nudges`, a preference each person can turn off).
- **Words written before the upgrade count.** The dashboard reads the daily totals the save loop has kept since Phase 2, so a streak and the calendar reach back over the history of the instance. Chapters finished before the upgrade are the one exception: nothing recorded the day a chapter was finished, so a goal measured in chapters counts from the upgrade on.
- **After a rollback to v1.1**, goals, sprints, cards and progress-email settings made with v1.2 are gone with the copy of the database, and the Studio's pages do not exist in v1.1.

### What changes at the upgrade from v1.2 to v2.0

No migration runs: the schema is the one v1.2 has (85 migrations), the range 1970 to 1989 reserved for the desktop mode is unused, and so no pre-migration copy is taken (the server takes one only when a migration is pending). Desktop mode keeps its profile and its identity-provider state in two rows of the existing `settings` table, and only when `SCRIOBH_DESKTOP` is set.

- **A server install changes nothing it can see.** With `SCRIOBH_DESKTOP` unset there is no `/desktop/*` or `/desktop-idp/*` route, no Host check, no stop on standard input, no remembered port, and the seven `desktop.*` operations answer "not found" to everyone. No new variable is required; `SCRIOBH_DESKTOP`, `SCRIOBH_LAUNCH_TOKEN` and `SCRIOBH_PORT_FILE` are in the table under [Configuration](#configuration).
- **Five changes reach every deployment** (F-1148, F-1149, F-1150, F-1154), found while running the app in a desktop webview and each safe in a browser: the four questions that used `confirm()` (signing out with text the browser holds, resolving an alternative, deleting a compile preset, deleting a stash item for good) ask in the page, and every sign-out form asks a real question; every link that saves a file carries `download`; the Editions pages say a missing source file in place of its download link (the download answers 404 with the sentence); and the service worker leaves `/auth/`, `/desktop/` and `/desktop-idp/` alone (its file changed, so each browser fetches it once). The authorization endpoint's judgement by site (`Sec-Fetch-Site`, `Origin`) rather than by kind (`Sec-Fetch-Dest`) belongs to the embedded issuer, which exists in desktop mode only.
- **Rolling back to v1.2** needs no copy: the v1.2 binary starts on the database v2.0 used, with its own sessions. Anything written meanwhile is kept, and nothing of the desktop mode exists in v1.2.

### Rolling back

Roll back to v1.0 with the copy taken before the upgrade, **while the pre-migration file still exists** (Scríobh never deletes it) and before anything you cannot lose has been written. Stop the server, put the v1.0 binary or image back, remove `data/scriobh.db-wal` and `data/scriobh.db-shm`, copy `data/backups/migrate/pre-migrate-<timestamp>.db` over `data/scriobh.db`, start. The old binary refuses a database that has migrations it does not know, which is why the file is replaced and not edited. The rehearsal does exactly this after upgrading and checks that v1.0 starts and answers as before, down to the 69 answers it recorded before the upgrade.

What a rollback loses and what it leaves:

- **Everything written to the database since the upgrade** is gone, including any text. The archive from step 1 is the better way back if the new release has been in use for a while.
- **Files on disk are not rolled back.** The stored source files of EPUBs imported with v1.1 stay under `data/files/projects/<id>/editions/`, with no row that names them (v1.0 does not know editions); they are inert and can be deleted by hand. The Markdown mirror is rewritten from the database by the old release at its start. A local Git repository and a remote that v1.1 pushed to keep those commits.
- **Git after a rollback.** The old release reads its remote as `synced` and not in sync: the next push, which with push mode `manual` is yours to start, sends the rolled-back text as a new commit on top of what v1.1 pushed, so the text pushed after the upgrade is reverted on the remote (it is not erased from its history). With push on save or on an interval that push happens by itself. Switch the remote to `manual` first if you would rather look first.
- **Mail, Kindle, Pandoc, passphrases, signing keys and the semantic index** do not exist in v1.0: whatever was set up in v1.1 is not in the old database and is not running, and a project that had a passphrase is open to its members again, which is the one thing worth checking before a rollback if you used one.

**What is rehearsed.** `scripts/upgrade-rehearsal.sh` (by default v0.1, v0.5, v1.0, v1.1, v1.2 and v2.0; `--all` for every tag; or a list of tags) builds each release's own development server from `git archive <tag>`, writes data through that release's own API, stops it, and starts the current build on the same data directory. The data is the small dataset of earlier phases (projects, documents, saves, snapshots, goals) and a rich one: three people with the three roles, a book with parts, chapters, scenes, notes and front matter and one trashed scene, revisions and named snapshots, comments with a reply and a resolved question, suggestions (one rejected), two share links (one revoked), three API keys (one revoked, one pinned to the book), a webhook, a static-site configuration, a Compendium with entities, aliases, relationships, mentions and a timeline event, daily and project goals, an import, a compile preset, a compile and a Markdown export, an AI provider with a sealed key, an idempotency key, a picture named in a chapter and in a research note (real `attachment_refs` rows of both kinds), a Git remote pushed to a development Git server (`internal/devgit`, with the system `git` as its server side) and, from v1.1 (which has an audience), a second remote with audience private. The rich dataset is written through the old release's own operation table (`_ops`) and runs from v0.5, where an operation the release lacks is skipped (v0.5 lacks two); v0.1 to v0.4 have no such table and are rehearsed with the small dataset only, and the rehearsal says so. It checks that the pre-migration copy was taken and holds the old database, that every migration ran, that the schema then equals a fresh install's (columns, foreign keys, unique constraints, indexes and triggers), that integrity and foreign-key checks are clean, that the old session still works, that every field each old API answer had (69 answers for v1.0) is still there with the same value, that the keys, links, webhook, provider key, idempotent replay and static site still work, that the old Git remote reads as private and pushes the same tree, that the cover migrations (0810 and 1837) kept every `attachment_refs` row and attachment, made none cover-clean and gave no project a layout, and gave each remote `include_cover` 1 if private and 0 if public, that everything new is off, that a second start applies nothing and takes no second copy, and that the rollback above works. It needs no network (the Go module proxy is off and every address is loopback). Every release from v0.1 to v2.0 passes (v2.0 to this build applies 0810 and 1837). A release that adds no migration (v1.2 to v2.0) takes no pre-migration copy, and the rehearsal then requires exactly that: no migration applied, no copy taken, a second start that applies nothing, and the old binary starting on the upgraded database in place of a copy-back, which would have dropped the edit the rehearsal makes after the upgrade. `internal/db/testdata/<tag>.db` holds a small database written by each release (`UPGRADE_FIXTURE_DIR=internal/db/testdata` writes the one of a tag that has none; remove a file first to replace it), and `go test ./internal/db` upgrades each one on every run: `TestUpgradedDatabasesMatchAFreshInstall`, `TestEveryReleaseFixtureKeepsItsData` and, for v1.0, `TestV10DatabaseUpgrades` (row counts and a sample of values, the exports rebuild, the old remote, every new table empty, a second start a no-op). The v1.0 fixture is about 600 KB; it holds no session and no loopback address, and its ids and times differ every time it is made, so the tests count and compare, never name an id. When a release is tagged, add its fixture, and add it to `releaseFixtures` in `internal/db/release_fixtures_test.go`.

## Health checks and logs

`GET /healthz` returns `200` with `{"status":"ok","version":"..."}` after a `SELECT 1` against the database, and `503` otherwise. It needs no sign-in.

The image has no shell or curl, so its `HEALTHCHECK` runs the binary: `/scriobh healthcheck` requests `/healthz` on `127.0.0.1` at the port in `SCRIOBH_LISTEN` and exits 0 only on a `200`. It needs a fixed port, so it does not work with `SCRIOBH_LISTEN=...:0`. Inspect it with:

```
docker inspect --format '{{.State.Health.Status}}' <container>
docker compose ps
```

Logs are JSON on stderr (`docker compose logs scriobh`). They contain method, route, status, duration, and principal id, not manuscript text or request bodies.

## Verifying a build

**`make ci`** runs, locally and in order, every gate of the `ci.yml` workflow (see [Continuous integration](#continuous-integration) for how the workflow splits them): the workflow-file check, gofmt, `go vet`, `go test -race` (when a C compiler is available; otherwise without `-race`), the contrast check, the production build, the skill and CLI snapshot check, the editor bundle freshness check, the authorization sweep, the editor round-trip test, the desktop-ready smoke, and the CSP smoke when a Chromium is available. It needs Go, Node, Python 3, and `lsof` (or `ss`). A step that depends on a file that is not in the tree yet fails with a message saying which file.

`make help` lists every target. Targets you may want on their own: `make test`, `make build`, `make image`, `make csp-smoke`, `make desktop-smoke`, `make bench`.

**`make desktop-smoke`** (`scripts/desktop-smoke.sh`) is the proof that the app is wrappable as a desktop program, and since Phase 8 that the production build runs in desktop mode: it starts it with `SCRIOBH_DESKTOP=1` and a launch token and, with `curl`, walks the start address, the first-run profile and the sign-in through the embedded issuer, then checks the session, a second instance (status 75, `ALREADY_RUNNING`) and `SIGTERM` (status 0). With every proxy variable unset it runs the end-to-end tests (which start the binary on `127.0.0.1:0`, read the printed port, and sign in through the in-process development issuer), then starts the binary itself, reads its `LISTEN` line, calls `/healthz`, checks that the only listening sockets are loopback, sends `SIGTERM` and requires exit status 0, and confirms the production build refuses the development issuer.

**`make csp-smoke`** (`scripts/csp-smoke.mjs`) builds the development server, starts it on a temporary data directory, and walks the sign-in, library, new-project, project, and not-found pages in headless Chromium, failing on any console error or warning, any `securitypolicyviolation`, any request to another origin, any `style=` or `on*=` attribute, inline script or inline style in the served HTML or the live DOM, and any unexpected status. It needs the Playwright package pinned in `scripts/package.json` and a Chromium: set `CSP_SMOKE_CHROMIUM` to an executable, or run `npx playwright install chromium` in `scripts/`. A browser the script finds in Playwright's cache is used automatically.

The container image is built by `make image` and by the `image` job in CI. After the build and before the push, that job runs `scripts/ci-image-check.sh` on the image it just built: the image's configured user must be exactly `65532:65532` (and `/data` in it owned by that user), and the production binary in it must refuse `SCRIOBH_OIDC_STUB=1`. There is no Docker daemon in the job, so the script uses buildah: `buildah inspect` for the user, and `buildah run --isolation=chroot` in a working container for the binary (a control run of `/scriobh version` first; if the container cannot run it, the script says so and runs the binary from `buildah mount`'s root file system instead). The run sets `SCRIOBH_LISTEN=127.0.0.1:0`, because the image's `:8080` would make any build, production or not, exit on "not loopback" before the production check; the job fails unless the binary exits non-zero with `production build without the development issuer` (a binary that keeps running is the failure "accepted the stub"). Separately, `cmd/scriobh`'s `TestProductionBuildRefusesTheStubIssuer` checks the same of a production build made from source, and always runs uncached. `make ci` runs every step of the push gate and the race pass on the concurrent packages, in one sequence (not the nightly bench); the next section says which workflow runs which step.

## Releases

A release is a `v*` tag. What it produces:

| File | What it is |
| --- | --- |
| `scriobh-<tag>-<os>-<arch>.tar.gz` (`.zip` on Windows) | The production server `scriobh` and the agent CLI `scriobh-cli`, with LICENSE, NOTICE and README.md, for linux (amd64, arm64, arm (ARMv7), 386, riscv64, ppc64le), darwin (amd64, arm64), windows (amd64, arm64, 386), freebsd (amd64, arm64), openbsd (amd64) and netbsd (amd64) |
| `SHA256SUMS.txt` | The SHA-256 of each archive, in `sha256sum`'s format |
| the desktop packages | For the platforms whose build worked: a Linux `Scriobh_<version>_amd64.deb` (and `Scriobh_<version>_amd64.AppImage`; `<version>` is the tag without its `v`, as for the `.dmg` files), the Windows installer `scriobh-desktop-<tag>-windows-amd64-setup.exe`, the macOS `.dmg` files (`Scriobh_<version>_{aarch64,x86_64,universal}.dmg`; on the development forge only when a Mac runner is switched on) and, on the development forge, the Arch Linux package `scriobh-desktop-<version>-1-x86_64.pkg.tar.zst` (made from the `.deb`; its version is the release's, the one in the `.deb`'s file name), each platform with its own `SHA256SUMS-desktop*.txt` |

**Nothing is signed.** No signature, no notarisation, no signing certificate, no update server. `SHA256SUMS.txt` comes from the same place as the files, so it catches a damaged download and says nothing about who made the file. macOS and Windows warn about the programs and the packages. **Windows:** SmartScreen shows "Windows protected your PC" for the installer (More info, Run anyway), and the installer does not install WebView2 (`webviewInstallMode` is `skip`: installing makes no request), so a machine without that runtime has no working window. **macOS:** the `.dmg` images are signed ad hoc and not notarised, so Gatekeeper asks for Open Anyway (System Settings, Privacy and Security, on macOS 15 and later; right-click, Open before that), and `xattr -d com.apple.quarantine <file>` clears the quarantine of the programs once you have checked the checksum. The release notes say all of this to a downloader (`.forgejo/release-body.md`; DESKTOP.md has the detail). The signing steps, none of them run, are in `docs/p8-notes/release-runbook.md`.

**Check a download:** `sha256sum -c SHA256SUMS.txt` (`shasum -a 256 -c SHA256SUMS.txt` on macOS) in the folder with the archives; `scriobh --version` and `scriobh-cli --version` print the tag. The programs are static (no CGO) and have no development sign-in: the server is built with `-tags prod`, the same flags the `Dockerfile` uses.

**Make the same files yourself:** `make release-dist VERSION=v1.2.3` (Go and Python 3; about a minute and a half for all fifteen platforms on a busy laptop) writes them to `./dist`. It is `scripts/release-dist.sh`, the script the release workflows run: `--version` is stamped into both programs, `--src DIR` builds another tree (a tag's checkout, say), `--targets` takes another list of `GOOS/GOARCH`. The builds use `-trimpath -buildvcs=false`, and the archives are written by `scripts/release-archive.py` with one modification time (the commit's), owner 0:0, fixed modes and no gzip time, so the same source, version and Go toolchain give the same `SHA256SUMS.txt` on a second run (shown on one machine, twice; the compressed bytes also depend on the zlib behind Python's gzip, so on a different machine compare the programs inside). Only the host's own program (darwin/arm64) has been run; the other platforms are cross-compiled and unexecuted. The 32-bit (linux/arm built as ARMv7, linux/386, windows/386) and BSD (freebsd, openbsd, netbsd) archives are cross-compiled and have never been run on their platforms, like every platform but darwin/arm64; the 32-bit ones read PDFs with the same limits as the rest (sizes and offsets are kept in `int64`, and a number from a file that does not fit a 32-bit `int` becomes the largest or smallest `int`, never a wrapped one), but a 32-bit process has at most 2 GiB of address space, so a large library is not for them. On the BSDs and everywhere else the Pandoc bridge sets the same process limits and checks free disk space; Windows has no Pandoc bridge and refuses to run Pandoc. linux/s390x and linux/loong64 build and are left out as rare; add them with `--targets`. A tree older than the 32-bit and BSD fixes (an old tag built with `--src`) does not build for those seven; give it `--targets` with the first eight. `scripts/release-dist.sh` lists each pair and says why.

**On GitHub.** The published copy has one release workflow, `.github/workflows/release.yml`, for a `v*` tag (or a dispatch with a `tag`, or without one to build the files as workflow artifacts only): a `plan` job fixes the tag and version, `dist` runs `scripts/release-dist.sh`, `desktop` builds the Linux `.deb` (and an AppImage that may fail and is opened before it is staged), the macOS `.dmg` files (through `desktop/scripts/build-macos.sh`, as on the forge's Mac) and the Windows installer (a native `tauri build` on `windows-latest`, renamed to the forge's `scriobh-desktop-<tag>-windows-amd64-setup.exe`; the installer still does not install WebView2) on GitHub's runners, each leg copying its packages into one flat folder before uploading it (an artifact keeps each file's path below the common parent of what was uploaded, so packages uploaded from their bundle folders arrived nested and the checksum step failed), and one `release` job, the only thing that creates the GitHub Release (`gh release create`, and only when there is none: a re-run leaves an edited title and notes alone), attaches all of it with `--clobber` and writes `SHA256SUMS-desktop.txt` from the desktop assets the Release holds. As on the forge, the tag's tree is checked out beside the scripts of the commit the run is on, so a dispatch from a newer branch can release an older tag. It runs when `dist` succeeded even if a desktop leg failed, and a re-run replaces files of the same name. `docker-image.yml` publishes the image to GHCR for the same tag and makes no release. Nothing is signed. None of these workflows has run on GitHub (`actionlint`, a YAML parser and `make publish-check`'s assertions about the legs are all that has looked at them), and the macOS and native Windows legs have not run on a GitHub runner (the macOS script has run on a Mac, including on the v2.0 tag's tree through `SRC_DIR`).

## The landing page

`landing/` is a static page, plain HTML and CSS with one small script and no build step, for GitHub Pages (`https://<owner>.github.io/<repository>/`). It is drawn from the application's own tokens (warm paper, one moss accent, both themes through `prefers-color-scheme`, flat fills and hairline rules) and makes no request to any other site: its Content-Security-Policy is `default-src 'self'` with `media-src 'self'` and `script-src 'self'`, its two fonts are subsets of the application's own Newsreader and Instrument Sans files (`landing/fonts/`, with their licences), and its links to anything else are plain links. The one script, `landing/os.js`, reads the browser's own `navigator.userAgentData` (or the user agent string) and shows the visitor's system as the main download button; it makes no request, and without it the page lists every download. Its pictures are `landing/media/<slot>-light.png` and `<slot>-dark.png`, made by `scripts/landing-media.sh` from a table of sources (the files in `docs/media/`) at the top of that script; changing a picture is a change to a row (or replacing the file), not to the HTML. The demo video is not stored a second time: `pages.yml` copies `docs/media/demo.mp4` and `demo.webm` into the artifact at deploy time (and runs when they change), and the script copies them beside the pictures for a local preview, where `landing/.gitignore` keeps them out of git. The page plays it from a `<video controls preload="none">` with a poster, no autoplay and no loop: nothing moves until someone presses play. The page says what has not been verified, as the README does. It writes for authors first (what it is like to write in it, that the words stay on their machine, how to get the app) and keeps the technical description, the container run line and the server downloads in the last section.

**The download links** are filled in at deploy time. The HTML ships with links to the releases page, so an unfilled page still works. `pages.yml` runs `gh release view --json tagName,isPrerelease,assets` (the latest release is the newest one that is neither a draft nor a pre-release) with the workflow's own token, and `scripts/landing-downloads.py` writes each asset's URL, the version, the sizes and the list of the 15 server archives into `landing/index.html`. No browser asks GitHub's API anything, and a visitor without JavaScript gets the real files. The script FAILS, and nothing deploys, when the release is a pre-release or a file the page links to is missing (the three macOS images including `Scriobh_<version>_universal.dmg`, the Windows installer, the AppImage and the `.deb`, both checksum lists, all 15 server archives). The macOS default is the universal `.dmg`, because a browser cannot tell Apple silicon from Intel; the release workflow has a `macos-universal` leg that builds it (`build-macos.sh universal`). A release made before that leg existed has no universal image, so a deploy that reads it fails: cut a release with the leg first, or the page keeps its previous version. A new release redeploys the page through the `release` event, and through the end of the release workflow's own run, because a Release made with the workflow's `GITHUB_TOKEN` does not start the `release` event; on those events the step waits up to ten minutes for the files. `make publish-check` runs the script on the published page with made-up releases (every link filled, a pre-release, a missing universal image, AppImage or server archive, and a wrong version all refused).

It is deployed by `pages.yml` (a template in `scripts/publish/`, installed with the others), which uploads `landing/` with `actions/upload-pages-artifact` and deploys it with `actions/deploy-pages` under the `github-pages` environment, on a push to `main` that changes `landing/**`, when a release is published or the release workflow ends, and by hand. `make publish-check` asserts that the workflow is installed with its permissions (`pages: write`, `id-token: write`), its environment and exact action versions, that the page's files are published, that it has a policy, no script but `os.js` and no link beyond the project and Tauri.

**Once, by the repository's owner, after the first publish:** Pages must be told to deploy from GitHub Actions, which the workflow's own token cannot do:

```sh
gh api -X POST repos/crueber/scriobh/pages -f build_type=workflow
# if the site already exists:
gh api -X PUT repos/crueber/scriobh/pages -f build_type=workflow
```

(or Settings, Pages, Build and deployment, Source: GitHub Actions). Until then the first run of `pages.yml` fails in `configure-pages` with "Get Pages site failed"; run it again afterwards. `404.html` carries a `<base href="/scriobh/">` because GitHub serves it for a missing path at any depth; if the site moves to a custom domain at its root, change that one line to `/`.

**Not verified.** The page has been seen in Chromium only (1440 and 390 px, both themes, with the keyboard), the workflow has not run on GitHub, and the video was played in Chromium only (the MP4 source, which Safari would pick, and the WebM fallback were not played in other browsers).

## Continuous integration

This section describes the CI of the development repository. The copy published to GitHub carries four workflows instead, made from templates when it is published: `ci.yml` (the `quick` job below, on a GitHub runner), `docker-image.yml` (the image, for a `v*` tag only), `release.yml` (the binary archives and the unsigned desktop packages, as one GitHub Release, for a `v*` tag; see [Releases](#releases)) and `pages.yml` (the landing page, the files under `landing/`, deployed to GitHub Pages when a push to `main` changes them; it needs a one-time setting, see [The landing page](#the-landing-page)). The nightly, deep and desktop workflows, the `image` and `race-core` jobs and everything about the runner below are the development repository's only.


| File | Runs | Jobs |
| --- | --- | --- |
| `ci.yml` | every push, every pull request, on demand | `quick`, then `image` (the push gate) |
| `nightly-bench.yml` (the workflow is named `nightly`) | nightly at 03:17, on demand | `detect`, then `race`, `plain` and `bench` for every branch with a commit in the last 24 hours; nothing at all when there is none |
| `deep.yml` | on demand only (`workflow_dispatch`) | `browser`, `e2e-fast`, `e2e-extra`, one after another |
| `desktop.yml` | on demand only (`workflow_dispatch`), never on a push | `linux` by default; `windows-cross` and `arch` when asked for (they run on a Linux runner like `linux`; `arch` takes an optional `tag` input and runs the `arch` job with one, or `linux` then `arch-branch` without); `macos` and `windows` only when asked for, and the development CI has no runner for either |
| `release.yml` | every push of a `v*` tag, and on demand with an existing tag | `dist`, then `desktop`, `desktop-windows` and (only with the variable `MACOS_RUNNER` set to `true`) `desktop-macos`, and after `desktop` the Arch jobs `desktop-arch` and `arch-check`; creates the Release and uploads the files (see [Releases](#releases)) |

Each workflow has its own concurrency group (`ci-<ref>` for the push gate, one per branch or tag; `deep`; `nightly`; `desktop-<branch>-<target>`, so a dispatch of one target never cancels another's; `release-<tag>`). A newer push to a branch replaces that branch's older gate run; pushes to different branches queue. Each runner takes one job at a time and the forge has several, so two jobs of different runs, or of one run, may be running at once on different machines (a job's cores are not shared with another job on its own runner); the timing tests (nightly only) are written for a runner that is not shared and skip themselves in the push gate. A nightly run takes several hours of runner time on the runner it lands on.

**The image is published by CI.** In the development repository, on a push or a manual run, the `image` job of `ci.yml` pushes the production image to the project's package registry as `ghcr.io/crueber/scriobh:<short sha>` and `ghcr.io/crueber/scriobh:latest`, and as `ghcr.io/crueber/scriobh:<tag>` with the tag's own name for a `v*` tag. A pull request builds the image and does not push it. Because `latest` moves, pull a short-sha or a `v*` tag for anything that must not change under you. In the published copy the `docker-image.yml` workflow does this instead, and only for a `v*` tag: it pushes `ghcr.io/crueber/scriobh:<tag>`, the version without the `v`, the short SHA, and `latest` unless the tag has a prerelease suffix; a push to a branch publishes no image. Nothing else in the workflows pushes or logs in anywhere.

### Jobs

| Job | Workflow | What it runs | About |
| --- | --- | --- | --- |
| `quick` | `ci.yml` | `workflows-check`, `release-check` (the release publisher against a mock of the forge's API), `fmt-check`, `vet`, `contrast`, `build`, `skill-check`, `editor-deps`, `editor-check`, the editor unit tests, `bench-smoke`, `make test-plain` (the whole plain `go test ./...`, four packages at a time, with `SCRIOBH_TIMING=off`: it includes the authorization sweep, the editor round trip and the repository checks), `desktop-smoke` | 5.5 minutes of `go test` on a four-core laptop; not yet measured on the runner, where `internal/server` alone took 7 minutes in one early run (that run took 22.8 minutes in all, with the timing tests on and one package at a time) |
| `image` | `ci.yml`, after `quick` | builds the production image (`TAGS=prod`) and pushes it as above | a few minutes |
| `detect` | `nightly-bench.yml` | `scripts/nightly-branches.sh`: the branches of this repository whose tip was committed in the last 24 hours and is not already part of the default branch (a merged branch was tested there), at most four, or the one named by a manual run; the later jobs skip when the list is empty | seconds |
| `race` | `nightly-bench.yml` | for each branch, `make test-race-core`: the race detector on the packages where goroutines share state (the mirror, the Git engine, events and streams, webhooks, mail, the semantic and presence workers, backup and restore, the passphrase gate, the op layer, idempotency, the Pandoc supervisor, AI work, the import limiter) | 4 minutes a branch on a laptop at two cores, 5.6 minutes on the runner (335 s, web run 190; it was 32 to 35 minutes before; see "What the race pass costs") |
| `plain` | `nightly-bench.yml` | for each branch, the whole plain `go test ./...` with the timing tests on (`SCRIOBH_TIMING` unset, `TEST_PARALLEL=1`, `GOMAXPROCS=2`, `-count=1`), `internal/text` first | 11 minutes a branch on a laptop at two threads (14.5 minutes on the runner at two, 869 s in web run 190; about 25 there at four, before) |
| `bench` | `nightly-bench.yml` | for each branch, the performance gate at 150k, 500k and 1M words, with the reports uploaded | about 17 minutes a branch |
| `browser` | `deep.yml` | Chromium: the CSP smoke and the editor's Chromium tests | |
| `e2e-fast` | `deep.yml` | `make e2e-fast`, the Chromium suites | |
| `e2e-extra` | `deep.yml` | `make e2e-extra`, the rest of `make e2e` | |

`make ci` runs more than the push gate: it also runs the race detector on the concurrent packages (`make test` runs `test-plain` then `test-race-core`) and the timing tests, which the push gate leaves to the nightly run, and it still runs the authorization sweep (`go test ./internal/server -run Authz`) and the editor round trip (`go test ./internal/editor -run RoundTrip`) as their own steps, so a local run names them. The push gate does not run them on their own because `make test-plain` already runs both. `csp-smoke` and the editor's Chromium half run in `browser`, `e2e-fast` in `e2e-fast`. `make ci` skips the Chromium steps with a message when it finds no Chromium; the `deep.yml` jobs install one, so there a missing browser fails the job.

**What the push gate checks.** `quick` and `image`: formatting, vet, contrast, the production build, the skill and CLI snapshot, the editor bundle and its unit tests, the bench smoke, every Go test that does not assert time (behaviour, the authorization sweep, the editor round trip, the repository checks), and the desktop-ready smoke; `image` builds and pushes the image. It has no browser, no race detector and no wall-clock budgets. It also reuses cached test results: `quick` sets `TEST_COUNT` to the empty string, so `make test-plain` runs `go test` without `-count=1`, and a package whose inputs did not change is not run again. The exceptions always run (`-count=1`): `./cmd/...` and `internal/editor`, whose tests build the production binary or run node in a child process, which the Go test cache cannot see; `desktop-smoke` also always runs (the runner keeps a persistent Go cache; `GOCACHE` and `GOMODCACHE` are the runner's defaults, and nothing in the workflow overrides or cleans them). Locally, `make test-plain` keeps `-count=1` unless you set `TEST_COUNT=`.

**What the nightly run checks.** It forces a full run: `-count=1` everywhere, so no cached result stands in for a test. For each branch with a commit in the last 24 hours: the race detector on the concurrent packages, every test with the timing tests on, and the performance budgets. A run with nothing to test does nothing and is green. At most four branches are tested a night (`main` first, then `phase-*`, then the rest, by name); a further branch is named in the log as not tested tonight and the run stays green, so check the log or dispatch the workflow with `branch` for it. A branch whose tip is already an ancestor of the default branch's tip is not tested (it was merged, and the default branch is tested with it in; the log names it), and branches with the same tree count once (a squash merge takes no slot). "The last 24 hours" is by commit date, not push date: a commit pushed more than a day after it was made is not picked up; dispatch for it. A scheduled run uses the workflow file and scripts on the default branch (`main`); each branch is fetched into its own directory and tested with its own Makefile and tests. The nightly workflow only takes effect once it is on `main`: until then a `phase-*` branch gets no race pass and no timing tests from CI, and a manual run of `nightly` on that branch (with `branch` set) is how to get them. At most four branches a night are tested (`main`, then `phase-*`, then the rest, by name; the log names the ones left out, and the run still succeeds without them), a stage carries on after a branch fails and fails the job at the end, a branch that exceeds its own limit (35 minutes for `race` and `plain`, 25 for `bench`) counts as failed, and a branch whose Makefile has no `test-race-core` target skips the race stage with a notice. A manual run: Actions, `nightly`, Run workflow, pick any branch for the workflow file and put the branch to test in `branch` (empty: the ones that changed). With `tea`: `tea actions workflows dispatch nightly-bench.yml --ref main --input branch=<branch>`.

**The nightly order and the warm machine.** `plain` follows `race` in the queue, on a machine that has just run the race pass, and `internal/text` holds the tightest budget (a 2 MiB file of reference definitions must count in under 1.5 s; 0.85 s cool, 1.7 s after twenty minutes of all-core work). The script runs `internal/text` first in its pass. If the budget still misses on a warm runner, the fix is to move that test's budget or to run `plain` first, not to weaken the budget silently.

**What the push gate does not check.** No browser and no wall-clock budget: before a release, run `deep.yml` on the release commit, or `make ci` and `make e2e-extra` on a quiet machine, and look at the last `nightly` run of the branch. The `image` job does not start the server in the image or exercise it beyond the two checks above (user, and refusal of the development issuer by the production binary); the container healthcheck, the data volume and a real sign-in are not run in CI. To look at the user by hand: `docker inspect --format '{{.Config.User}}' ghcr.io/crueber/scriobh:<tag>` prints `65532:65532`, and starting the image with `SCRIOBH_OIDC_STUB=1 SCRIOBH_LISTEN=127.0.0.1:0` must exit with an error.

**Why not the whole tree under the race detector.** It took over two hours on the runner, held the runner so that push gates queued behind it, and in Phase 7 it found one real race (in a background job of repository publishing, a package `race-core` covers) against many failures of timing tests the detector slows down. `race-core` keeps the detector where goroutines share state, nightly; `make test-race` runs the whole tree by hand.

**What the race pass costs, and the rule for keeping it cheap.** `make test-race-core` is meant to finish in a few minutes at two cores (`GOMAXPROCS=2 make test-race-core TEST_PARALLEL=2`: 235 s wall and 351 s of CPU on an M4 Max). These tests do not need more than a couple of threads, so a test that needs a bigger machine to finish is a wrong test, and the answer is never a longer cap or a bigger runner. What made it take 35 minutes and more was five things, all in the tests or in waste the tests exposed: (1) `testutil.DB` applied the 85 migrations for every test, 1.7 s each under `-race` (the pure-Go SQLite driver re-parses the schema after every DDL statement, and checkptr slows that code thirty-fold); it now builds one migrated file per set of migrations, in `$TMPDIR/scriobh-testdb-<uid>`, and copies it (`testutil.DBAt` for a test that keeps its database in a data directory it builds). The directory is created 0700 and used only when it is a real directory owned by the current user (else the process builds into a private one); a cached file is trusted only after `PRAGMA quick_check` and a `schema_migrations` row count pass, once per process, and a broken one is rebuilt; build directories older than an hour and other migration sets' files older than a day are removed. (2) The Git engine's fake clock ran the 30-second check once for every 30 seconds an `Advance(48h)` crossed, 5,760 database reads; it runs the check (the one timer the service arms through the optional `gitmirror.PeriodicClock`) at most twice per call now (see the comment on `fakeClock.Advance`), so a test that must see each backoff expiry advances in steps. (3) The server parsed the layout and every partial again for each page and each fragment on every start; it parses them once and clones them. (4) Four tests measured something other than their subject: the Pandoc stderr-flood fake ran into the 60 s timeout, a restarted service in a Git test left the ops on the dead one (a 3 s spin), the `-run` pattern for `internal/imports` said `Child` and matched a nine-million-element XML test, and a slow receiver's handler slept out its delay after the sender had gone. (5) Two fixtures are smaller under `-short` only (the race pass): the 6 MiB large-repository push is 2 MiB, and the semantic search budget takes 30 warm searches instead of 200 for its percentiles; the plain pass keeps both at full size.

The checks that stay slow under `-race` do real work with the instrumentation on: 1,000-document snapshots, 10,000-chunk vector scans, real `git` pushes, backups of an 8 MB database. If a package's race time grows past a minute or two at two cores, profile it (`go test -race -cpuprofile`) before touching a timeout.

### Timing tests

A timing test asserts a wall-clock budget or measures throughput. The runner is shared and slow, and on it a budget measures the machine, so the push gate sets `SCRIOBH_TIMING=off` and these tests skip themselves: `testutil.Timing(t)` calls `t.Skip` when that variable is `off`, and does nothing otherwise, so a plain `go test` on a laptop and the nightly run keep running them.

The rule for a test author:

- A test that asserts a budget and also checks behaviour, memory or results (nearly all of them) keeps those checks in the parent and runs on every push. It measures the time in the parent and asserts it in `t.Run("time", func(t *testing.T) { testutil.Timing(t); ... })`. The fixture is built in the parent, because the behaviour checks need it.
- A test whose only assertion is time, or that only logs a measurement, calls `testutil.Timing(t)` on its first line, so the fixture is not built either. Do not use this form when the test has any other assertion: it would drop that assertion from the push gate.
- A budget helper (`bound(t, name, got, limit)`) does its comparison inside its own `t.Run(name, ...)` closure, with `Timing` first in that closure, so its callers need no change. Never call `Timing` bare inside a helper: it would skip the calling test half way and drop the assertions after it.
- A test that only has a timeout, waits for something with a deadline, polls, checks a lower bound ("it did wait"), a memory budget or logs a measurement is not a timing test and must not call it.

`internal/repocheck` (`TestTimingTestsCallTimingSkip`) fails when a function compares a duration measured with `time.Since` (or `time.Now().Sub`, directly or through a local variable) against a budget, or hands one to a same-package helper that compares a `time.Duration` parameter outside a closure that calls `Timing`, and neither it nor an enclosing function calls `Timing`; a `Timing` call in a `t.Run` closure covers only that closure. It does not report lower bounds, a comparison of two measurements (sorting samples, a best or worst tracker), `for` conditions or log-only measurements, and a comparison that is not a budget carries `// timing: not a budget: <reason>` on its line or the line above. It is a static check, so it does not see a relative budget (`slow > 3*fast`, the linearity tests: two measurements compared are ignored), a duration that reaches the comparison through a helper that is not shaped like `bound` (a median or p95 of a slice), through a struct field, or as a product-reported field, it does not run `testing.Benchmark`, and it does not check that `Timing` comes first. A reviewer has to.

**The marked tests.** 4 tests are skipped whole under `SCRIOBH_TIMING=off`, and 146 more keep their behaviour checks and skip only `time` subtests (taken from a `go test -json` run with `SCRIOBH_TIMING=off`: every test whose skip message is the `Timing` one; packages are under `internal/`). The tests that are skipped whole assert nothing but time or log a measurement. Three large fixtures are built smaller under `SCRIOBH_TIMING=off` (`testutil.TimingOff`) so their behaviour checks stay cheap: `mirror` `TestFiveThousandEntitiesMirror` and `TestChapterSaveWithALargeResearchLibrary` (60 notes instead of 600: the first write of 600 durable files can outlast the helpers' idle wait on a disk that really syncs), and `server` `TestSitePreviewOf2000Chapters`. Race-only variants of a budget live in the `race_test.go` and `norace_test.go` files and are not tests of their own.

| Package | Skipped whole | Behaviour kept, `time` subtests skipped |
| --- | --- | --- |
| `ai` |  | `TestEmbedTimeoutAndLimits`, `TestResponseBoundsAndHostileBodies` |
| `alts` | `TestOrphansDoNotMakeASaveSlow` | `TestRebaseOnARepetitiveText` |
| `domain/ai` | `TestNumbers` | `TestATimeoutFallsBack`, `TestAnEndlessStreamIsCutOff`, `TestTheSyncBudgetIsSharedAndATimeoutIsATimeout` |
| `domain/aiwork` |  | `TestAnalysisDoesNotBlockSaves` |
| `domain/compile` |  | `TestCompileOfALongBook`, `TestCompilesAreGated`, `TestPaginationOfHugeParagraphsIsLinear` |
| `domain/docs` |  | `TestAlternativePerformance`, `TestOutlineOfAThousandDocumentsIsFastAndStable`, `TestSixtyThousandWordProjectWithTwentyRevisionsEach` |
| `domain/editiondiff` |  | `TestAMillionTinyParagraphsStayWithinTheBudget`, `TestBudgetTheBookAgainstItsOwnEdition`, `TestBudgetTwoEditionsOfThreeHundredPages`, `TestBudgetsOfThreeHundredPages`, `TestCompareRefusesAHostileSourceFile`, `TestHostileSizesStayBounded`, `TestPathologicalRepeatedLinesDoNotHang`, `TestRepeatedTextDoesNotMakeTheMatchingQuadratic`, `TestTwoHugeUnrelatedBooksAreTooDifferent` |
| `domain/entities` |  | `TestFiveThousandEntities`, `TestMovePerformance` |
| `domain/goals` |  | `TestAddedWordsBigDocumentIsCheap` |
| `domain/links` |  | `TestSyncBatchCutsTheWorkIntoShortChunks` |
| `domain/mentions` |  | `TestSixtyThousandWordsFiveHundredEntities` |
| `domain/presence` |  | `TestGatedProjectBeatCost`, `TestLoadThousandProjectsTenViewersEach` |
| `domain/projectgate` |  | `TestGateCheckBudget`, `TestSetPassphraseCost` |
| `domain/review` |  | `TestReviewPerformance` |
| `domain/revisions` |  | `TestDiffIsBoundedForLargeDocuments` |
| `domain/search` |  | `TestSearchIsFastAtManuscriptScale` |
| `domain/semantic` |  | `TestSemanticSearchBudget`, `TestSlowEndpointTimesOut` |
| `domain/timeline` |  | `TestFindingsAreCappedAndTheScanIsBounded`, `TestFourHundredEventsFiveHundredEntities` |
| `gitmirror` |  | `TestAHostThatMisbehavesIsBoundedAndNothingItSaysIsKept`, `TestATriggerNeedsAnAddressTheGuardAllowsAndTimesOut`, `TestPublicViewPerformance` |
| `imports` | `TestScrivenerManyItemsIsLinear` | `TestAFileOfHeadingsIsNotSplitIntoAMillionScenes`, `TestAPdfChildThatMisbehavesIsRefusedAndNothingIsLeft`, `TestASecondImportByTheSamePersonIsBusyThroughTheRegistry`, `TestConvertedImportsAt150000Words`, `TestConvertersAreLinear`, `TestDocxManyNotesIsLinear`, `TestDocxTextBombsAreRefusedCheaply`, `TestEpubDeepAndHugeMarkup`, `TestEpubImportOfAThreeHundredPageBookIsUnderAMinute`, `TestEpubManyChaptersAndPictures`, `TestEpubNotesThatLoopOrMultiply`, `TestEpubSizeLimits`, `TestEpubWithAHundredThousandLandmarks`, `TestEpubXMLTricks`, `TestFindNotesWithManyContainersIsLinear`, `TestFootnotesAreWrittenOnceHoweverOftenTheyAreCited`, `TestGhostExportIsRefusedWithoutBeingBuilt`, `TestGhostHostile`, `TestGhostMarkdownBombs`, `TestGhostMarkdownCostIsOneBudget`, `TestHTMLBigTokensDoNotEndThePost`, `TestImportConversionsAreLimitedForTheWholeProcess`, `TestLinkHeavyImportDoesNotStarveOtherWriters`, `TestOnePersonHoldsOnePlace`, `TestPandocCapsHoldAtTheConverterForTheReviewedLeads`, `TestPandocMarkdownOverTheCapIsRefusedWithoutParsing`, `TestPandocMarkdownWorstShapesAreBoundedInTimeAndMemory`, `TestPandocOutputOverTheImportLimitsIsRefused`, `TestPandocOutputThatWouldSlowAParserIsBrokenUp`, `TestPdfHostileFilesAreBoundedThroughTheOp`, `TestPdfThreeHundredPagesWithinTheBudget`, `TestPictureCleaningIsBounded`, `TestRTFBinaryAndPictures`, `TestRTFControlWordBomb`, `TestRTFNestingBomb`, `TestRTFRunLimitBoundsRunsAndNeverMergesAPrivateNote`, `TestRTFTextAndRunLimits`, `TestScrivenerBinderHostile`, `TestScrivenerTextFilesNotReadAreSaid`, `TestScrivenerZipHostile`, `TestThreeHundredChangesInOneLargeChapterApplyInTime`, `TestThreeHundredFilesAreFast`, `TestWordPressHostile`, `TestXMLHostile`, `TestXMLManyChildrenAndAttributesAreNotBuilt`, `TestZipEntriesAreNotAllocatedByTheirDeclaredSize`, `TestZipTotalIsBoundedWhateverTheEntriesSay` |
| `mail` |  | `TestALargeAttachmentIsStreamedNotBuffered`, `TestAnIdleDispatchIsCheap`, `TestEndlessBannerIsBounded`, `TestReviewARelayThatStopsReadingMidDataReleasesTheWorker`, `TestSlowRelayTimesOut` |
| `mcp` |  | `TestBatchHasOneDeadlineAndRefusalsCarryIDs`, `TestCallTimeout`, `TestNotificationFloodIsCheapAndSilent` |
| `mirror` | | `TestASaveNeverWaitsForTheMirror`, `TestChapterSaveWithALargeResearchLibrary`, `TestFiveThousandEntitiesMirror`, `TestSixtyThousandWords`, `TestStartupCheckOfAHundredProjectsIsCheap` |
| `pandoc` |  | `TestAHungPandocIsKilledWithEverythingItStarted`, `TestCancellingTheRequestStopsPandoc`, `TestOutputOverTheCapStopsPandoc` |
| `pdfread` |  | `TestACorruptStreamSearchHasABudget`, `TestAFlateBombIsRefusedByRatioWithABoundedPeak`, `TestAFontWithAGiantCMapIsCut`, `TestAPageOfTwoHundredThousandLinesIsCapped`, `TestAbsurdPageCountsAreCapped`, `TestAnOperatorFloodStopsAtTheLimit`, `TestDeepNestingDoesNotOverflowTheStack`, `TestFormsThatDrawThemselvesEnd`, `TestLayoutCostWithManyRegionsAndLines`, `TestMalformedCrossReferenceIsRebuiltWithinALimit`, `TestOneHugeStringIsReadInBoundedTimeAndMemory`, `TestProductBigEncodingCMapAndALongString`, `TestProductDrawsTimesFormSize`, `TestProductManyFontsAndABigToUnicode`, `TestProductManyObjectStreamsOfDistinctArrays`, `TestProductNameTreeFanOut`, `TestProductObjectStreamsOfManyPairsAndBigObjects`, `TestProductPagesTimesPieces`, `TestReviewDeadlineIsCheckedInsideAPageThatDrawsAFormManyTimes`, `TestTheTimeLimitCoversOpenAndRebuild` |
| `publish` | `TestBreakingIsFastAtBookScale` | `TestFootnoteAmplificationIsBounded`, `TestGlyphBombs`, `TestHostilePDFInputs`, `TestLayoutStopsWhenCancelled`, `TestOnePaginatedHugeParagraphIsBrokenFirstFit`, `TestPDFOfALongBook`, `TestPDFOfTheLargestBook` |
| `server` |  | `TestAStalledStreamDoesNotSlowPresence`, `TestMCPAndWebhookBudgetsAt150kWords`, `TestReadingOneChapterIsFast`, `TestSitePreviewOf2000Chapters`, `TestStreamsStopOnShutdown`, `TestTimelineAtFiveHundredEvents`, `TestWrongSecretAndUnknownIdTakeAboutTheSameTime` |
| `site` |  | `TestGiantPostsStayLinear`, `TestPerformance2000Posts150kWords` |
| `text` |  | `TestAHugeAliasDoesNotMakeMatchingQuadratic`, `TestHostileInputsStayBounded`, `TestRenderHTMLStaysBoundedOnHostileText`, `TestWordCountIsFast` |
| `webhook` |  | `TestEnqueueThroughput`, `TestSavePathCostWithWebhooksAndAStalledReceiver` |

### The desktop build

The development repository's `desktop.yml` builds the desktop app ([DESKTOP.md](DESKTOP.md)). It is not part of the push gate and must not become part of it: a runner would be taken from `quick` for the length of a Rust build. Run it from Actions, Run workflow, with `target` left at `linux`; or `tea actions workflows dispatch desktop.yml --ref <branch> --input target=linux`.

- **`linux`** runs on the development CI's runner. It installs the Tauri build dependencies with apt (`libwebkit2gtk-4.1-dev libgtk-3-dev libayatana-appindicator3-dev librsvg2-dev build-essential curl file libssl-dev libxdo-dev libdbus-1-dev pkg-config`), Rust 1.100.0 with rustup, builds the server binary with `desktop/scripts/sidecar.sh`, then `cargo fmt --check`, `cargo clippy --locked --all-targets -- -D warnings`, `cargo test --locked` (no display is needed: the tests start a stand-in server, not a window), compiles the app with `tauri build --no-bundle` and packages a `.deb`. It then **installs that package and starts it**: `desktop/scripts/deb-install.sh` runs `apt-get install` on the `.deb` (so its dependencies resolve) and checks that `/usr/bin/scriobh-desktop`, the bundled `/usr/bin/scriobh-desktop-server` beside it and the desktop entry are on disk and that the shell's libraries resolve; `desktop/scripts/launch-check.sh` starts the installed app under `xvfb-run` and `dbus-run-session` with a throwaway `HOME` (no Secret Service, so the first-run key-file fallback), waits for the server to write `desktop/port` and answer `GET /healthz`, checks that the server is the shell's child and holds `scriobh.lock`, sends the shell `SIGTERM`, and requires that the server is gone and the lock released. Every wait is bounded and a failure prints the data folder, the process table and a short run of the server alone. A release build prints nothing on standard error, so the port file, the lock and `/proc` are the evidence, not a log. What these steps do not prove: that the `Depends` line suffices on a clean machine (the job still has the `-dev` packages), anything about how the window looks, and the keychain path. The package is uploaded last, whatever the steps before it did. **The build, package, install and launch steps have all run in CI** (on `2003268`): the installed app started under Xvfb, its bundled server answered `/healthz` with the lock held, `secret.key` was made with mode 600 and `SIGTERM` left no server and no lock.
- **`windows-cross`** makes the Windows installer on the same runner, without Windows: `desktop/scripts/build-windows-cross.sh` installs `nsis clang lld llvm p7zip-full` with apt (and, because bookworm's NSIS 3.08 cannot compile Tauri's installer script, builds NSIS 3.13 from its source release, SHA-256 pinned, with `scons g++ zlib1g-dev bzip2`; the result is cached with the rest), a pinned Rust toolchain with the `x86_64-pc-windows-msvc` target, and a pinned cargo-xwin, builds the server with `GOOS=windows`, runs clippy for the Windows target, runs `tauri build --runner cargo-xwin --target x86_64-pc-windows-msvc --bundles nsis`, and then `desktop/scripts/verify-windows-installer.sh` reads the installer (NSIS, both programs inside, x86-64, GUI subsystem, version resource) without running it. It uploads `dist/*.exe` and its `.sha256` as an artefact. It needs outbound access to Rust's, Microsoft's (the C runtime and Windows SDK, under [Microsoft's licence](DESKTOP.md#the-windows-build-cross-compiled-never-run)), GitHub's (one NSIS plugin), SourceForge's (the NSIS source, only when it must be built), npm's and apt's servers, at build time only. The SDK download (about 600 MB, x86-64 only) and the Rust installation are kept in `SCRIOBH_WIN_CACHE`, which defaults to `scriobh-win-cross` beside Go's build cache, so they are reused where the runner keeps Go's cache; the script's first log line says `warm` or `cold`. A second run that says `cold` again on the same runner means that runner does not persist that directory (the forge has several runners and each keeps its own). Run it with `tea actions workflows dispatch desktop.yml --ref <branch> --input target=windows-cross`. `release.yml`'s `desktop-windows` job runs the same script on a tag's tree and publishes the installer ([Releases](#on-the-development-forge)). **The job has run on the forge: web run 172 built and verified `scriobh-desktop-2.0.0-windows-amd64-setup.exe`** (after a first run failed on Debian's NSIS 3.08, which the script now replaces with a pinned 3.13 built from source); the script has also been run end to end on a Mac. Nothing of the installer has run on Windows.
- **`arch`** (with the `tag` input) and **`arch-branch`** (without it) make the Arch Linux package, `desktop/scripts/arch-pkg.sh`, in a pinned `archlinux` container; `target=arch` is the only value that selects them, and `all` does not include them. With a tag, `arch` takes the `.deb` (and the AppImage) from that tag's Release; without one, the `linux` job builds this commit's `.deb`, uploads it with `upload-artifact@v3`, and `arch-branch` takes it with `download-artifact@v3`. Neither publishes anything: the package is an artefact of the run. The steps, the two dispatch forms and what is unproven are under `desktop-arch` and `arch-check` in [Releases](#on-the-development-forge); `release.yml` runs the same script. What has and has not run is in [DESKTOP.md](DESKTOP.md#arch-linux-and-omarchy).
- **`macos`** and **`windows`** need runners labelled `macos` and `windows`. The development CI has neither; asking for them (or for `all`) queues jobs nothing will take. (`release.yml`'s `desktop-macos` job is different: it is gated on the repository variable `MACOS_RUNNER`, so a release run never waits for a Mac.) They are written from the tools' documentation and have never run.
- It signs only when the secrets named in [p8-notes/release-runbook.md](p8-notes/release-runbook.md) exist, and publishes nothing: packages are artefacts of the run.

Locally the same steps are `make desktop-test` and `make desktop-build`.

### Secrets

In the development repository `image` needs two repository secrets: `REGISTRY_USER` and `REGISTRY_TOKEN`, a token that may write packages. The automatic workflow token can log in to the registry but cannot write to it. Without the secrets the push step fails with a message that names them.

### Run the deep checks

In the repository's Actions page choose `deep`, Run workflow, pick the branch, and set `only` to `browser`, `e2e-fast`, `e2e-extra` or `all`. With `tea`: `tea actions workflows dispatch deep.yml --ref <branch> --input only=browser` (it may print a JSON error and still start the run; check the run list). The jobs run one after another; the two e2e jobs take over an hour each.

### Tests that behave differently on the runner

Each of these is a test or a script, not product code, and each says why in a comment where it is:

- `internal/text`: the hostile-input time budget is 25% wider when `CI` is set (the laptop budget is unchanged).
- `internal/domain/entities`: the race build's budget for the 2,000-entity move is twenty times the plain one.
- `internal/imports`: the 31 MiB PDF test is skipped under the race detector (the race-built reader needs more memory than the runner has); the plain build runs it.
- `internal/pandoc`: a killed grandchild that is a zombie counts as ended (the container's first process does not reap).
- `internal/gitmirror`, `internal/mirror`: the real-mirror tests wait for the complete manifest and for the start-up reconciliation before they look.

### Run the same steps locally

| Job | Locally |
| --- | --- |
| `quick` | `make workflows-check fmt-check vet contrast build skill-check editor-deps editor-check bench-smoke desktop-smoke`, `(cd web/editor && npm test)`, `SCRIOBH_TIMING=off GOMAXPROCS=2 TEST_PARALLEL=2 TEST_COUNT= make test-plain` |
| `browser` | `make editor-deps scripts-deps csp-smoke`, `(cd web/editor && npm run test:browser)` (set `CSP_SMOKE_CHROMIUM` and `SCRIOBH_CHROMIUM`, or `cd scripts && npx playwright install chromium`) |
| `image` | `make image` |
| `race` (nightly) | `GOMAXPROCS=2 TEST_PARALLEL=2 make test-race-core` (`TEST_PARALLEL` now defaults to 2; the full-tree pass, by hand only: `make editor-deps test-race`; `RACE_TIMEOUT` and `TEST_PARALLEL` override the limits) |
| `plain` (nightly) | `GOMAXPROCS=2 TEST_PARALLEL=1 make test-plain` (with `SCRIOBH_TIMING` unset) |
| `bench` (nightly) | `make bench BENCH_SIZES=150k,500k,1m` |
| `detect` (nightly) | `scripts/nightly-branches.sh` (add `--since <epoch>` or `--branch <name>`); `NIGHTLY_DRY_RUN=1 scripts/nightly-run.sh race "<sha>:<branch>"` fetches and checks out without running |
| `e2e-fast` | `make e2e-fast` |
| `e2e-extra` | `make e2e-extra` |
| everything | `make ci` (and `make e2e-extra`) |

`make workflows-check` parses the workflow files with a real YAML parser (ruby, else python3 with PyYAML) and checks each job has `runs-on`, `timeout-minutes` and `steps`. Run it before pushing a change to a workflow; with neither parser it says `SKIPPED` rather than pass (CI sets `WORKFLOWS_CHECK_STRICT=1`, which makes that a failure).

### Linux and macOS

The scripts the jobs call run on both. The Chromium search in `scripts/harness.mjs`, `scripts/csp-smoke.mjs` and the editor's `browser-test/harness.mjs` checks `PLAYWRIGHT_BROWSERS_PATH`, then `~/Library/Caches/ms-playwright`, then `~/.cache/ms-playwright`, newest revision first, for both the macOS and the `chrome-linux64` layouts. `scripts/desktop-smoke.sh` uses `lsof`, or `ss` when there is no `lsof`. The `fmt-check`, `skill-check` and `editor-check` targets fail when `git` itself fails, instead of reading its empty output as a clean tree.

