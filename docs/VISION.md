# Vision

## The sentence

Scríobh is the place where a long piece of writing lives from the first fragment to the finished file, on hardware you own, without a subscription deciding what you may do with your own manuscript.

## Who it is for

**The home-lab writer.** One person, or a household, with a server in the closet and a Synology, Proxmox box, or NUC in the corner. They already run Immich, Forgejo, maybe Weave. They are comfortable with Docker Compose and an OIDC client secret. They write fiction, essays, newsletters, documentation, or a thesis, and they are tired of subscription editors and cloud sync that silently mangles their work.

**The small writing group.** Two to five people: a writer and an editor, a pair of co-authors, a writing group with beta readers. They need comments, suggestions, versions, and a read-only link — not a real-time multiplayer engine.

**The privacy-conscious professional.** Someone writing a memoir, a legal brief, or client work that must not touch a third-party AI service by default. They may still want AI help, routed to a local model or a key they control, with a clear view of exactly what was sent.

### Not the target

- A 50-person editorial team needing workflow automation, SSO group provisioning at scale, and an audit trail for compliance.
- A public blogging platform with readers, feeds, and comments.
- Someone who wants an AI to produce a novel from a prompt.

## Priority

The **editorial core is first-class**: the editor, outline, entities, timeline, research, revisions, and compile. AI is **second-class**: it can ship later, be disabled, or be removed without weakening the tool. No AI feature may become a dependency of writing, outlining, or continuity.

That ordering is not a slogan. It decides what gets built first, what gets cut first, and what the editor is allowed to assume. The book must stand on its own with every provider switched off.

## The four jobs

Writing tools fail when they optimise for one job and get used for another. Scríobh is designed around four jobs that a long project repeats constantly:

### 1. Get the words down

> "I have 40 minutes before dinner. Put me in the chapter, hide everything else, and do not lose a keystroke."

The editor must open fast, survive a phone locking, work on a train, and never fight the writer. This is the job that decides whether the product is used at all.

### 2. Keep the whole thing in your head

> "Who is Máire's brother? Did they already know about the letter in chapter 4? What did I say her eyes looked like?"

Structure, outline, character sheets, timeline, search, and backlinks exist so the writer does not have to reread 300 pages to stay consistent.

### 3. Revise without fear

> "I want to rewrite this chapter, but I want yesterday's version back if it is worse."

Snapshots, named versions, diffs, comments, and suggestions turn revision from a destructive act into a reversible one. And sometimes the revision is smaller than a chapter: three ways to say one sentence, kept side by side until one sounds right.

### 4. Finish and hand it over

> "Give me a manuscript-format DOCX, an EPUB, and a print-ready PDF, with the front matter I set up once."

Export is not an afterthought bolted onto the end. Compile presets are part of the project from day one.

## Principles

These are the tie-breakers. When two reasonable people disagree, the principle with the lower number wins.

1. **The writer's attention is the budget.** Every control, badge, animation, and notification spends it. Default to less. If a feature cannot be ignored while typing, it is in the wrong place.
2. **Nothing is destroyed silently.** Autosave, versions, undo, trash, conflict copies. A mistaken action should cost one click, not an evening. Alternatives and originals are kept, not overwritten.
3. **Plain text is the truth.** Markdown is the storage format. The editor is a rich view over it, with a Source view of the same text. No opaque binary document model that only this app can open.
4. **The local machine is the default.** Assets ship with the binary. Fonts are self-hosted. AI is opt-in, explicit, and previewed. Nothing leaves the box that the user did not choose to send.
5. **Structure is opt-in, never imposed.** A blank project is a cursor on an empty page. Parts, chapters, scenes, beats, and character sheets appear when the writer asks for them, not before.
6. **Fast is a feature.** Server-rendered pages, no framework, no client router, no spinner where a page could have been drawn. Budgets are in the architecture doc and are enforced in CI.
7. **The interface should look like a book, not a dashboard.** Warm paper, one accent, hairline rules, generous margins, real typography. No gradients, no glass, no floating cards over a mesh background.
8. **Agents are guests.** The API, CLI, and MCP surface exist so a writer can point their own tools at their own work — with scoped keys, read-only defaults, and every AI action visible in a log.
9. **Own your exit.** Markdown mirror, full export, documented schema, MIT licence. Leaving should be boring.
10. **Your work is a repository.** Every project has a plain-text mirror and can push to a Git remote you own. The app is never the only copy.
11. **Editorial before generative.** Outline, entities, timeline, and research are first-class. AI is a guest that can be switched off; the book must stand without it.

## What "pleasant" means, concretely

Pleasant is not a mood board. It is a list of behaviours we can test:

- The editor is interactive in under 250 ms on a phone, and the first keystroke never waits on the network.
- Typing never moves the text under the cursor. Saves are debounced and silent; the word count updates without a layout shift.
- The cursor stays where you left it, in the chapter you left it in, on the device you left it on.
- Opening the app on a phone resumes the last chapter at the last scroll position, with the keyboard one tap away.
- A rewrite from the AI arrives as a diff you can reject in one key, never as text that has already replaced yours.
- A sentence can hold three ways of saying itself, and you can hear each one in place before choosing.
- Every list is keyboard-navigable; every destructive action is undoable; every empty state teaches the next step.
- Both themes are designed, not inverted. Contrast is checked by a script, not by eye.
- Nothing on screen is there to sell you something.
- A copy of every project lives in a Git repository you control, and the app tells you when it last got there.

## Success criteria

**For v1.0:**

- A writer can create a project, draft a 100,000-word manuscript in chapters, track continuity, revise with versions and comments, and export EPUB + DOCX + print PDF without leaving the app.
- A second user can be added through OIDC and given editor or beta-reader access in under two minutes.
- The full stack runs from one container and one volume, with a documented restore that has been rehearsed.
- The AI features work with OpenRouter, a local Ollama model, and the OpenCode API, with a visible context preview and a hard cost cap.
- The app is usable one-handed on a 390 px viewport for a 30-minute writing session.

**For the project:**

- No feature requires an account anywhere.
- Every capability has the same name in HTML, REST, and CLI.
- A new contributor can read three docs and make a correct change.

## Non-goals

Restated here because they will be proposed again:

- Real-time co-editing with remote cursors (v1).
- A plugin ecosystem or marketplace.
- Hosted multi-tenant service.
- Public social features: follows, likes, reader comments on published work.
- Mobile native apps. The web app is the product; a PWA shell is the compromise.
- Training or fine-tuning models on the user's work.
- Becoming a general note-taking app with a graph view.
