# AGENTS.md: building Scríobh

Read this first, then [README.md](README.md), [docs/VISION.md](docs/VISION.md), [docs/ARCHITECTURE.md](docs/ARCHITECTURE.md), and [docs/FEATURES.md](docs/FEATURES.md). The plan is the contract; change it before changing the code.

## What this is

A self-hosted, privacy-first long-form writing application. Go, SQLite, server-rendered HTML with HTMX, OIDC-only auth, one binary, one data directory. Markdown is the storage and editing format. A project has a manuscript tree, revisions, comments, an entity bible, AI assistance, export, and a push-only Git remote.

## Invariants (do not re-litigate)

1. **OIDC only.** No local passwords, reset flow, or TOTP. The desktop build uses a real embedded OIDC issuer, never an auth bypass or a "desktop mode" header.
2. **SQLite, one writer, one replica.** No Postgres, no Redis, no external queue or search service. WAL, short transactions, `busy_timeout`.
3. **One op registry.** Every write is an op registration rendered into HTML, REST, CLI, and MCP. UI handlers never write SQL directly.
4. **Markdown, database canonical.** The on-disk mirror is a generated projection. Never make the filesystem a second writer.
5. **Git is push-only.** Never pull, ever. Force-push is an explicit UI action with a typed confirmation and a local bundle; import is the only path from a remote into Scríobh.
6. **Desktop-ready from Phase 0.** Loopback binding with an ephemeral port, no hardcoded origin, no reverse-proxy assumption, no wrapper-only feature, no filesystem bridge into the web layer. Test it every phase.
7. **Privacy by construction.** No telemetry, no external CDNs or fonts, no outbound request the user did not configure, except the desktop app's update check, which exists only after the person says yes, and only in GitHub release builds. AI is opt-in per project, previewed before sending, and can be forced local.
8. **The writing surface is sacred.** No generated text is inserted without an explicit accept. Nothing animates or reflows while typing. Live updates never clobber unsaved input.
9. **No gradients.** Flat fills, hairline rules, one accent, both themes designed. Judge looks from a real browser screenshot, not from CSS.
10. **Small, with one exception.** No npm at runtime, no client-side router, no framework. One pinned ProseMirror editor bundle is built in CI and embedded; the rest of the JS stays inside the shell budget in `docs/ARCHITECTURE.md`. Assets are self-hosted.
11. **Alternatives are annotations on canonical text.** The document body always holds the chosen wording, and export reads ordinary Markdown. Ghosting never deletes; the Lab never rewrites.
12. **Editorial before generative.** Outline, entities, timeline, research, and compile do not depend on AI. AI features must be removable, optional, and unable to block the editorial path.
13. **Performance is a gate.** `scriobh bench` and the published budgets in `docs/ARCHITECTURE.md` decide whether a release is done. A single document may not be allowed to grow into an uneditorable blob.

## Working rules

- Migrations are numbered ranges per domain, run once, in filename order, never edited after they are applied. Take a pre-flight backup.
- IDs from Go (`db.NewID`), times from Go (`db.Now`), positions as REAL gaps. No SQLite clocks.
- Authz returns `404` for missing access so project existence does not leak. Every op declares its auth and is covered by the sweep test.
- Secrets are write-only in the UI and encrypted with `SCRIOBH_SECRET_KEY`; they never enter logs, exports, the mirror, or a Git push.
- Manuscript content is untrusted data. AI prompts delimit it and say so; completion requests have no tools.
- Exports are deterministic and recorded with a manifest of included documents and hashes.
- New features need: an op, a UI route or fragment, a test, a line in `docs/FEATURES.md`, and a note in the changelog when one exists.
- Any new or changed op requires `make skill`; the committed skill and CLI snapshot must match the registry, and CI fails when they drift.
- Goals count **words added**; a streak day is a day the daily goal was met. Never use net words to fail a day or shame a cut.

## Verify

```
go vet ./...
go test ./...
go test ./internal/server -run Authz
go test ./internal/editor -run RoundTrip
./scripts/check-contrast.py
./scripts/csp-smoke.mjs        # when a browser is available
```

A change is not done until it works at 390 px, with the keyboard alone, after a reload with the network cut, and with the desktop smoke test (ephemeral loopback port, no proxy, no external IdP).
