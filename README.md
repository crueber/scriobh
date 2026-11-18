# Scríobh

**A writing app for long projects.** Scríobh is for the things that outgrow one file: a novel, a report, a run of essays. Your words are kept as Markdown you can open anywhere, and nothing leaves your computer unless you send it.

Scríobh (Irish, *write*) runs on your own machine or home lab, or as a desktop app. It's one Go binary and one SQLite file, and your words stay on your disk. You sign in with the identity provider you already have. You can keep a plain-text copy in a Git repository you control. There's no account on someone else's server and no telemetry.

Website: <https://crueber.github.io/scriobh/> · If it's useful to you, you can [sponsor the work](https://github.com/sponsors/crueber).

[Install](#install) · [Releases](https://github.com/crueber/scriobh/releases) · [Configuration](#configuration) · [Desktop app](#the-desktop-app) · [How it works](#how-it-works) · [Docs](#documentation) · [Status](#status)

### AI is off until you turn it on

- A fresh install sends nothing anywhere. There's no built-in AI service, so nothing works until you add your own key or point Scríobh at a model running on your own computer.
- Even then, AI stays off for every project until you switch it on for that one.
- Each feature is its own switch, per project: suggestions on a selection, alternative wordings, chat, critique, summaries, continuity checks, pulling characters and places out of a chapter, and the Lab. None of them starts on.
- When it's on, you see exactly what will be sent before it goes. You can force it to a local model.
- Nothing it writes goes into your text unless you accept it.
- The book doesn't need it. Outline, characters, timeline, research, revisions and compile work without AI.

<p align="center">
  <a href="docs/media/demo.webm"><img src="docs/media/demo.webp" alt="Scríobh demo (46 seconds): the library, opening a project, typing in the writing view, cycling an alternative wording in place, the outline, the Writing Studio, then compiling an EPUB" width="860"></a><br>
  <a href="docs/media/demo.webm"><b>&#9654; Watch the 46-second demo</b></a> &middot; the library, writing, alternatives, outline, studio and an EPUB compile, with captions, no sound (<a href="docs/media/demo.mp4">MP4</a>)
</p>

<p align="center">
  <a href="docs/media/library-light.png"><img src="docs/media/library-light.png" alt="The library: a shelf of typographic covers, one per project, with a card to continue where you left off (light theme)" width="49%"></a>
  <a href="docs/media/write-dark.png"><img src="docs/media/write-dark.png" alt="The writing view: the manuscript tree beside a calm page, with word counts against each chapter's target (dark theme)" width="49%"></a>
</p>

<p align="center">
  <a href="docs/media/alternatives-light.png"><img src="docs/media/alternatives-light.png" alt="A word with three wordings being cycled in place; the text always holds the chosen one (light theme)" width="49%"></a>
  <a href="docs/media/outline-dark.png"><img src="docs/media/outline-dark.png" alt="The outline: a synopsis, a status and a word target for every part and chapter (dark theme)" width="49%"></a>
</p>

<p align="center">
  <a href="docs/media/studio-light.png"><img src="docs/media/studio-light.png" alt="The Writing Studio: words by period, a habit streak and a calendar of writing days; words you add count, cuts never do (light theme)" width="49%"></a>
  <a href="docs/media/compile-dark.png"><img src="docs/media/compile-dark.png" alt="The Compile page with the EPUB 3 preset and a preview of the title page (dark theme)" width="49%"></a>
</p>

<details>
<summary>More screenshots</summary>

<p align="center">
  <a href="docs/media/review-light.png"><img src="docs/media/review-light.png" alt="The reading view with a comment anchored to a phrase and the margin beside it (light theme)" width="49%"></a>
  <a href="docs/media/compendium-dark.png"><img src="docs/media/compendium-dark.png" alt="The Compendium: the characters, places and things of a book, each an entity (dark theme)" width="49%"></a>
  <a href="docs/media/library-dark.png"><img src="docs/media/library-dark.png" alt="The library in the dark theme" width="49%"></a>
  <a href="docs/media/write-light.png"><img src="docs/media/write-light.png" alt="The writing view in the light theme" width="49%"></a>
  <a href="docs/media/outline-light.png"><img src="docs/media/outline-light.png" alt="The outline in the light theme" width="49%"></a>
  <a href="docs/media/studio-dark.png"><img src="docs/media/studio-dark.png" alt="The Writing Studio in the dark theme" width="49%"></a>
  <a href="docs/media/compile-light.png"><img src="docs/media/compile-light.png" alt="The Compile page in the light theme" width="49%"></a>
  <a href="docs/media/review-dark.png"><img src="docs/media/review-dark.png" alt="The reading view in the dark theme" width="49%"></a>
</p>

<p align="center">
  <a href="docs/media/phone-write-light.png"><img src="docs/media/phone-write-light.png" alt="The writing view at phone width, with the bottom tab bar (light theme)" width="24%"></a>
  <a href="docs/media/phone-write-dark.png"><img src="docs/media/phone-write-dark.png" alt="The writing view at phone width (dark theme)" width="24%"></a>
</p>

</details>

<p align="center"><sub>The book in these pictures, its author and its people are invented. The screenshots and the video are made by <code>scripts/readme-media.mjs</code>; see <a href="docs/media/README.md">docs/media</a>.</sub></p>

## What it is

Most writing tools are note apps with a publish button, or word processors with a sync problem. Neither holds a manuscript well. Scríobh is built around the project: a manuscript tree, a plan beside it, a cast of characters under it, and a revision of every word.

The rules it keeps (the full list is in [AGENTS.md](AGENTS.md) and [docs/VISION.md](docs/VISION.md)):

- **Nothing gets in your way while you type.** Nothing animates, reflows or interrupts you. Generated text is never inserted without an explicit accept, and live updates never overwrite what you haven't saved.
- **OIDC only.** No local passwords, reset flow or second factor of its own. Your identity provider decides who can sign in. The desktop app doesn't bypass this. It runs a real embedded OpenID Connect issuer on the same code path.
- **No data goes out that you didn't send.** No analytics, telemetry, external CDNs or fonts, error-reporting service, or outbound request you didn't configure. AI is opt-in per project, previewed before anything is sent, and can be forced to a local model.
- **The editorial work doesn't need AI.** Outline, entities, timeline, research, revisions and compile work without it. Switch AI off and the book is all there.
- **Markdown, in a database you can read.** The database is canonical. A read-only Markdown copy of every project is written continuously, so `grep`, `git` and a text editor always see your work. Alternatives to a wording are annotations on ordinary Markdown, so exports read as plain text.
- **Git is push-only.** Scríobh never pulls. Import is the only way a remote's text comes in.
- **One binary, one data directory.** No queue, cache or search service. No gradients, both themes are designed, and a script checks contrast.

## What's in it

- **Writing and structure.** A server-rendered writing surface over a ProseMirror editor with a Source view of the same Markdown, an outline with synopses and targets, wiki links and backlinks, versions you can compare and restore, search, light and dark themes, and an installable app shell with an offline page. Alternatives, ghosting, the stash and the Lab keep other wordings beside the chosen one.
- **The Compendium.** Entities, aliases and relations, a timeline with continuity checks, research notes and boards, shared across projects.
- **The Writing Studio.** Goals counted in words added (never net words, and nothing is called missed or failed), streaks, sprints, a pace estimate and an activity calendar, all computed on your server and nowhere else.
- **Review and sharing.** Comments anchored to the text, suggestions an editor accepts or rejects, read-only or comment links for guests, and an administrator console.
- **Output.** Compile presets (US and UK manuscript DOCX, styled DOCX, EPUB 3, Markdown), print-ready PDF with covers, a submission bundle, HTML export, and static site publishing.
- **Going in and out.** Import from a folder, a Git repository, Obsidian, Word (tracked changes become suggestions), Scrivener, WordPress, Ghost, an EPUB or a PDF with a text layer. A continuous Markdown mirror, push-only Git sync with deploy keys and several remotes, and backup and restore.
- **AI, optional.** A local Ollama or any OpenAI-compatible server, OpenRouter or OpenCode; a per-project policy; a preview of the exact request; budgets. Nothing is written into a chapter without an explicit accept.
- **Agents and integrations.** One op registry behind the pages, a REST API, a scoped CLI (`scriobh-cli`) with a generated skill, an MCP endpoint and signed webhooks.
- **A desktop app.** The same server binary in a [Tauri](https://tauri.app) window, with no server to run and no identity provider to set up.

Every feature, with its priority and phase, is in [docs/FEATURES.md](docs/FEATURES.md). The things deliberately not built are in [§ V](docs/FEATURES.md#v-deliberate-non-features). Scríobh isn't a notes app, a real-time collaborative document, a publishing platform, an AI book generator or multi-tenant SaaS. It isn't encrypted at rest either, so protecting the volume is your job. See [docs/OVERVIEW.md](docs/OVERVIEW.md#what-scríobh-is-not).

## How it works

```
Browser (HTMX shell + ProseMirror)        Desktop window (Tauri shell)
        │                                          │  127.0.0.1, one origin
        └────────────────────┬─────────────────────┘
                             ▼
      Go binary ── op registry ──┬── HTML fragments
         │                       ├── REST /api/v1
         │                       ├── scriobh-cli + API keys
         │                       └── MCP server (agents)
         │
         ├── SQLite (WAL)   data/scriobh.db       canonical
         ├── Files          data/files/…          images, covers, imports
         ├── Mirror         data/mirror/…         every project as plain Markdown
         ├── Git            push-only: mirror → a repo you own; import from a repo
         └── AI (optional)  openrouter │ opencode │ openai-compatible │ ollama
```

Every write is one registered operation, rendered as an HTML page, a REST route, a CLI command and an MCP tool. An agent holding a scoped key does what a person does and nothing more. The stack is Go (standard library `net/http`, pure-Go `modernc.org/sqlite`, no CGO), server-rendered HTML with HTMX, a small amount of vanilla JavaScript, and one pinned ProseMirror bundle. There's no npm at runtime, no client-side router and no framework. The reasons are in [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md).

## Install

There are three ways in: the container image, a binary built from source, or the desktop app. The first two need an OpenID Connect provider you already run (Passage, Authentik, Keycloak, Pocket ID, Authelia and so on). The desktop app needs none. Tagged versions, with their notes, are on the [releases page](https://github.com/crueber/scriobh/releases). It also has the prebuilt server and CLI binaries (Linux, macOS and Windows; unsigned, with a `SHA256SUMS.txt`) and the Linux `.deb`.

> The binary and its commands, the development sign-in and the REST API have been run. CI builds and publishes the container image, and its job checks the image's user and that the production binary refuses the development issuer. The server in the image has never been run to serve, and the compose files only parse. No real identity provider has been registered against this build. Details: [Status](#status).

### Just look at it

You don't need an identity provider to look around. The development build has a built-in sign-in for three fixed test users (`admin@dev.scriobh`, `writer@dev.scriobh`, `reader@dev.scriobh`). Never expose it to a network.

```sh
make run                       # Go 1.27.4 or newer; prints LISTEN http://127.0.0.1:<port>; data in ./data
# or
docker compose up --build      # then open http://localhost:8080 and pick a test user
```

### Container image

CI publishes the production image to the project's container registry, for example as `ghcr.io/crueber/scriobh:latest`. The tags are the short commit SHA, the release tag's own name for a `v*` tag, and `latest`, which moves. If something must not change under you, pin a release tag from the [releases page](https://github.com/crueber/scriobh/releases) or a SHA. If the registry refuses an anonymous pull, run `docker login ghcr.io` first.

To follow new builds, pin `tag@sha256:…` in your own manifest and rewrite that one line when a new digest appears. Compare digests, not tags. The tag can move, but the bytes a running task names can't.

1. **Register Scríobh with your identity provider** (see [Sign-in](#sign-in-register-an-oidc-client) below) and copy the issuer URL, client id and client secret.
2. **Write `.env`** (one `NAME=value` per line, comments on lines of their own, no `$` in values):

   ```
   SCRIOBH_BASE_URL=https://write.example.com
   SCRIOBH_OIDC_ISSUER=https://auth.example.com/application/o/scriobh/
   SCRIOBH_OIDC_CLIENT_ID=paste-the-client-id
   SCRIOBH_OIDC_CLIENT_SECRET=paste-the-client-secret
   SCRIOBH_SECRET_KEY=paste-the-output-of-openssl-rand-hex-32
   ```

   Make the key with `openssl rand -hex 32`, and keep a copy off this machine. It seals the credentials Scríobh stores, and it isn't in backups.
3. **Run it.** The image runs as uid 65532 with a read-only root filesystem and writes only under `/data`. A new named volume inherits that ownership.

   ```sh
   docker run -d --name scriobh --restart unless-stopped \
     --read-only --tmpfs /tmp --cap-drop ALL --security-opt no-new-privileges:true \
     -p 127.0.0.1:8080:8080 -v scriobh-data:/data --env-file .env \
     ghcr.io/crueber/scriobh:latest
   ```

4. **Check it.** `docker ps` shows `healthy` after about 30 seconds and `curl -s http://127.0.0.1:8080/healthz` prints `{"status":"ok",...}`. If it exited, `docker logs scriobh` names the problem in plain words, and `docker run --rm --env-file .env ghcr.io/crueber/scriobh:latest config check` lists everything that's missing.
5. **Put TLS in front.** The container publishes `127.0.0.1:8080` only. Point a reverse proxy at it (Caddy, Traefik and nginx examples are in [Reverse proxy](docs/OPERATIONS.md#reverse-proxy)); it must keep the `Host` header and allow request bodies of at least 50 MB. Run exactly one container per data volume.
6. **Sign in** at `https://write.example.com` (see [First sign-in](#first-sign-in)).

To build the image yourself with Compose instead, run `cp compose.prod.example.yaml compose.prod.yaml && docker compose -f compose.prod.yaml up -d --build`, and add `-f compose.prod.yaml` to every later `docker compose` command for this stack. To pull the published image under Compose, replace the `build:` block and `image: scriobh:prod` in `compose.prod.yaml` with `image: ghcr.io/crueber/scriobh:<tag>` (not run). To upgrade, read [Upgrades](docs/OPERATIONS.md#upgrades), take a backup, then start the new tag on the same volume.

### Binary from source

```sh
make build        # ./scriobh (production server) and ./scriobh-cli; needs Go 1.27.4 or newer
export SCRIOBH_BASE_URL=https://write.example.com SCRIOBH_DATA_DIR=/var/lib/scriobh \
       SCRIOBH_OIDC_ISSUER=https://auth.example.com/application/o/scriobh/ \
       SCRIOBH_OIDC_CLIENT_ID=... SCRIOBH_OIDC_CLIENT_SECRET=... \
       SCRIOBH_SECRET_KEY=$(openssl rand -hex 32)
./scriobh config check     # prints "configuration is valid", or every problem it found
./scriobh                  # listens on 127.0.0.1:8080; put a TLS proxy in front
```

The data directory must be one the service user owns. No service unit ships, so use whatever you use for services (for example a systemd unit that runs it with that environment and `Restart=on-failure`). Keep `SCRIOBH_SECRET_KEY` stable and copied off the machine. Without the variable, Scríobh writes `secret.key` into the data directory instead.

### The desktop app

You open it like any application. There's no server to run and no identity provider to set up. A Tauri 2 shell (`desktop/`) starts the same server binary on a loopback port and shows it in a window. On first run you make a local profile (a name, no password). It signs in through an embedded OpenID Connect issuer; there's no bypass. Your writing is in the system's place for application data, and the instance key is in the system keychain.

The desktop app isn't signed or notarised yet. It can check GitHub for a newer version, but only if you say yes the first time it opens (there's a switch in the Help menu): about once a day (the check is the same request for everyone; a download names the file for your system, as any download does), and it installs a new version only when you choose Restart to Update. Only builds the release workflow makes for a tag can do that; a build from source never looks. No release with the update key exists yet, so none has ever updated anything: the first one has to be installed by hand. Unsigned packages, when a release has them, are on the [releases page](https://github.com/crueber/scriobh/releases). None has been installed on a clean machine. The app has been run only as an unsigned development build on one Mac (Apple silicon). On Linux the CI runner builds a `.deb`, installs it and sees its server answer under a virtual display, but nobody has seen a Linux window. On Windows it hasn't been built or run. Read [docs/DESKTOP.md](docs/DESKTOP.md) (what is verified, and twelve known problems) before you rely on it.

**Arch Linux and Omarchy.** The app has a `-bin` pacman recipe over the release's `.deb` (`desktop/arch/`). The AppImage needs `fuse2` or `--appimage-extract-and-run`. [docs/DESKTOP.md](docs/DESKTOP.md#arch-linux-and-omarchy) covers how to install it, what Omarchy already has, what to set if the window is blank on Hyprland, and what's unproven. Nothing there has run on Arch.

To build it for this machine you need Go (the version in `go.mod`), Rust 1.100 with `clippy` and `rustfmt`, and Node 20 or later for packaging. npm is only a build tool here. On Linux the extra packages are in [the release runbook](docs/p8-notes/release-runbook.md).

```sh
make desktop-dev      # run a development build; its data is in the temporary folder, not your real data
make desktop-build    # the unsigned app and installers for this machine
make desktop-test     # format check, lints and tests; opens no window
```

A debug build of the shell is about 2.5 GB of build output. Set `CARGO_TARGET_DIR` to put it somewhere with room. Your writing lives in `~/Library/Application Support/Scriobh` (macOS), `${XDG_DATA_HOME:-~/.local/share}/scriobh` (Linux) or `%AppData%\Scriobh` (Windows), and Help, Show the Data Folder opens it. The app never checks for updates by itself, and with AI off nothing leaves the computer.

## Configuration

You configure everything with environment variables, read once at startup. Secrets (the OIDC client secret and `SCRIOBH_SECRET_KEY`) come only from the environment or the key file, never from the database or the UI.

| Variable | Default | What it does |
| --- | --- | --- |
| `SCRIOBH_OIDC_ISSUER` | none, required | The provider's issuer URL. It must equal the `issuer` in the provider's `/.well-known/openid-configuration` exactly, trailing slash included. |
| `SCRIOBH_OIDC_CLIENT_ID` | none, required | The confidential client's id. |
| `SCRIOBH_OIDC_CLIENT_SECRET` | none, required | The client's secret. |
| `SCRIOBH_BASE_URL` | derived | The URL people type, with no trailing slash. Set it in any real install: it builds the OIDC callback (`<base>/auth/oidc/callback`), absolute links, and whether cookies are `Secure`. |
| `SCRIOBH_SECRET_KEY` | generated | At least 32 characters (`openssl rand -hex 32`). Signs the sign-in flow and seals stored credentials (Git deploy keys, AI provider keys, webhook secrets). If unset, `secret.key` is created in the data directory. Not in backups: keep a copy elsewhere. |
| `SCRIOBH_DATA_DIR` | `data` (image: `/data`) | The data directory: the database, files, the Markdown mirror, backups. |
| `SCRIOBH_LISTEN` | `127.0.0.1:8080` (image: `:8080`) | Address to bind. |
| `SCRIOBH_TRUST_PROXY` | off | Read the client address from `X-Forwarded-For`. Read [Reverse proxy](docs/OPERATIONS.md#reverse-proxy) before turning it on. |
| `SCRIOBH_ADMIN_GROUP`, `SCRIOBH_WRITER_GROUP`, `SCRIOBH_READER_GROUP` | `scriobh-admins`, `scriobh-writers`, `scriobh-readers` | The identity provider groups that map to roles. Rename them here. |

Run `scriobh config check` after any change. It lists everything missing and warns about any `SCRIOBH_` name this version doesn't read (with a hint for typos). It doesn't contact the provider. The full table (limits, quotas, backups, MCP, webhooks, Git, site publishing, sessions and more, each with its default) is [Configuration in docs/OPERATIONS.md](docs/OPERATIONS.md#configuration). An administrator adds AI providers in the application, and a project's owner turns AI on for that project. A fresh install sends nothing ([AI providers: quick path](docs/OPERATIONS.md#ai-providers-quick-path)).

### Sign-in: register an OIDC client

Scríobh signs people in only through OpenID Connect. Tell your provider this:

| Setting | Value |
| --- | --- |
| Client type | Confidential (client id and secret) |
| Grant | Authorization code, with PKCE (S256, always sent) |
| Redirect URI | `${SCRIOBH_BASE_URL}/auth/oidc/callback`, for example `https://write.example.com/auth/oidc/callback` |
| Scopes requested | `openid profile email` (fixed) |
| ID token | `sub`, `email`, `email_verified` true, and a `groups` list **in the ID token itself** (Scríobh does not read the userinfo endpoint, and does not request a `groups` scope) |

Create three groups, `scriobh-admins`, `scriobh-writers` and `scriobh-readers`, and put yourself in `scriobh-admins`. Roles are recomputed from groups at every sign-in. Administrator wins over writer, and writer over reader. Someone in none of the three groups is refused, with a message that says so. Per-provider notes for Passage (the maintainer's provider), Authentik, Keycloak, Pocket ID and Authelia, and what to do when the provider is down, are in [docs/OPERATIONS.md](docs/OPERATIONS.md#registering-scríobh-with-an-identity-provider).

### First sign-in

Open your `SCRIOBH_BASE_URL`, choose Sign in, and sign in at your provider. You land in an empty library as an administrator. If you're refused, the message says why (not in a group, email not verified, groups missing from the token). If the issuer doesn't match, the page names `SCRIOBH_OIDC_ISSUER` and the value you set, and the log has both. If you have no admin group at your provider, set `SCRIOBH_ADMIN_GROUP=` (present but empty) and the first person to sign in is offered a one-time claim of administration.

Then make a project, write a sentence and reload. It's still there. Take a first backup, and copy the archive and your `SCRIOBH_SECRET_KEY` off the machine:

```sh
docker exec scriobh /scriobh backup              # writes /data/backups/<timestamp>.zip in the container
docker cp scriobh:/data/backups/<file>.zip .     # copy it out
scriobh backup --out /mnt/offsite/scriobh.zip    # a binary install, same environment as the server
```

Backups aren't encrypted, and nothing else Scríobh stores is either. Protect the volume with what you already use. See [Backups](docs/OPERATIONS.md#backups) and its restore drill.

## Working with an agent

An agent is a guest with a key. In Settings, create a scoped API key (`read`, `write` or `admin`, ideally restricted to one project, with an expiry), then:

```sh
scriobh-cli login --url https://write.example.com --key -    # paste the key on standard input
scriobh-cli skill install                                    # for the coding agents it finds in this project directory
scriobh-cli project create "The Salt Road" --kind novel --dry-run
```

The CLI addresses things by name, prints JSON, shows what would change with `--dry-run`, and takes `--idempotency-key` so a retried write is safe. The skill and the CLI's command set are generated from the op registry, so they can't drift from the server. An MCP endpoint (`/mcp`) offers the same operations. Manuscript content is treated as data, never as instructions. See [docs/AGENT-GUIDE.md](docs/AGENT-GUIDE.md) and [docs/API.md](docs/API.md).

## Documentation

| Doc | What it answers |
| --- | --- |
| [docs/OPERATIONS.md](docs/OPERATIONS.md) | Install, sign-in, proxy, backup and restore, upgrade, AI providers, Git sync, webhooks, MCP, CI |
| [docs/DESKTOP.md](docs/DESKTOP.md) | The desktop app: what it's made of, where it keeps things, what it will and won't do, how to build it |
| [docs/OVERVIEW.md](docs/OVERVIEW.md) | The long description: what exists, what isn't built, what hasn't been verified, the bet, the hard constraints |
| [docs/VISION.md](docs/VISION.md) | Who this is for, what it isn't, and the principles that decide arguments |
| [docs/FEATURES.md](docs/FEATURES.md) | Every feature, with a priority and a phase, and the things deliberately not built |
| [docs/ROADMAP.md](docs/ROADMAP.md) | Releases, exit criteria, and what "done" means at each stage |
| [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md) | Go, HTMX, SQLite, the op registry, auth, storage, security, performance budgets |
| [docs/API.md](docs/API.md) | The REST API, with every operation (generated from the registry) |
| [docs/AGENT-GUIDE.md](docs/AGENT-GUIDE.md) | Giving an agent a scoped key, the CLI, and the generated skill |
| [docs/COVERS.md](docs/COVERS.md) | Cover layouts: your own front and back, or one wrap, in place of the generated cover, on screens, in ebooks and in the paperback cover PDF; what other tools and printers do |
| [docs/UPDATES.md](docs/UPDATES.md) | Updates of the desktop app: consent, signing, what each system can do, the release pipeline, and what is not verified |
| [docs/AI.md](docs/AI.md) | The AI writing partner, provider adapters, context, privacy, cost |
| [docs/DATA-MODEL.md](docs/DATA-MODEL.md) | Tables, ownership, migration ranges, indexes |
| [docs/DESIGN.md](docs/DESIGN.md), [docs/ACCESSIBILITY.md](docs/ACCESSIBILITY.md) | Typography, colour, motion, the editor, mobile; the accessibility standard and known gaps |
| [docs/OPEN-QUESTIONS.md](docs/OPEN-QUESTIONS.md) | The decision log: what is settled and what is still open |
| [docs/RESEARCH.md](docs/RESEARCH.md), [docs/WRITEON.md](docs/WRITEON.md) | What we took from quire, weave and loom; the source feature list from Write_On |
| [wireframes/README.md](wireframes/README.md) | The Paper wireframes; earlier directions are archived at tag `archive/wireframes` |

## Status

Releases are tagged `v0.1` through `v2.3.0` (v1.0 "Publish", v1.1 "Second Wave", v1.2 "Writing Studio", v2.0 "Desktop", v2.2.0 "Covers"). In v2.0 the desktop app exists and runs unsigned on one Mac. v2.3.0 is released with print covers and desktop auto-update. The tag records what was built. It doesn't claim the roadmap's v2.0 exit criteria are met. v2.2.0 put your own cover on screens and in ebooks. v2.3.0 makes the paperback cover PDF from your layout, as one page for the printer, with the front and a proof as separate downloads. No printer's previewer has seen one yet and none has been printed. v2.3.0 also lets the desktop app update itself from GitHub, but only after you say yes. Nothing has updated from a real release yet, and the desktop builds are unsigned. If you have 2.2.x or earlier, install 2.3.0 by hand, once. Later versions can then update themselves. The plan in `docs/` is still the contract.

Not built: a signed or notarised desktop app, anything for Windows that has been run, a native open dialog, and moving an existing key file into the keychain. The v2.0 exit criteria (a non-technical person installs a signed `.dmg`, `.msi` or AppImage on a clean machine and updates in place; Windows) are not met.

What hasn't been checked, since the docs lean on it:

- The desktop app has been run unsigned on one Mac and nowhere else by a person. No installer was used on a clean machine, no update has been fetched from a real release (v2.3.0 is the first that can be), nothing was seen on Windows, and nobody has looked at the window, menus and dialogs.
- The container image has never been run to serve, and the compose files only parse.
- No real identity provider (Passage, Authentik, Keycloak and the rest) has been registered against this build. Only the built-in development issuer has been used. No real AI provider, MCP client, printer, copy of Word, SMTP relay, Pandoc, embedding model or Git host (GitHub, Forgejo) has been used. Stand-ins on loopback were.
- No screen reader, physical phone or browser other than Chromium has been used on the Studio's screens.
- The v1.0 exit criteria that need people have not been met: two independent users completing the install guide unaided, a printed proof, and a clean-machine restore by someone other than the author.

The complete statement, phase by phase, is in [docs/OVERVIEW.md](docs/OVERVIEW.md#status). What was and wasn't checked per feature is in [docs/OPERATIONS.md](docs/OPERATIONS.md) and [docs/OPEN-QUESTIONS.md](docs/OPEN-QUESTIONS.md).

## Development

Read [AGENTS.md](AGENTS.md) first. It holds the invariants an implementation has to keep, and the plan in `docs/` is the contract, so change the plan before changing the code. A new feature needs an op, a UI route or fragment, a test, a line in `docs/FEATURES.md` and, when one exists, a changelog note. A new or changed op needs `make skill`; CI fails when the committed skill and CLI snapshot drift from the registry.

```sh
make help               # every target
make build              # production binary and the agent CLI
make run                # development build, stub issuer, ephemeral loopback port, data in ./data
go vet ./... && go test ./...
go test ./internal/server -run Authz       # the authorization sweep
go test ./internal/editor -run RoundTrip   # Markdown round trip through the editor
./scripts/check-contrast.py                # both themes, every accent
./scripts/csp-smoke.mjs                    # when a browser is available
make bench              # the performance gate: scriobh bench against the published budgets (about a minute)
make ci                 # every step the push gate runs, and more
```

A change is not done until it works at 390 px, with the keyboard alone, after a reload with the network cut, and with the desktop smoke test (`make desktop-smoke`: an ephemeral loopback port, no proxy, no external identity provider). The CI workflows and what each runs are described under [Continuous integration](docs/OPERATIONS.md#continuous-integration).

## Licence

MIT. See [LICENSE](LICENSE). An earlier WriteFreely-style scaffold of this repository is archived at branch `archive/writefreely-scaffold` and tag `pre-plan-scaffold`; nothing in the plan depends on it.
