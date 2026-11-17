# Write_On: source feature list

Preserved from the feature summary supplied by the maintainer. Write_On is Jason Fried's personal writing tool, built over a weekend with Claude, unreleased and described as free or open-source. It is a plain Markdown editor focused on **alternative control** rather than version control.

This file is the record. The implementation decisions are in [FEATURES.md § U](FEATURES.md#u-alternatives-ghosting-stash-and-the-lab), [AI.md § 12](AI.md#12-write_on-reconciled), [ROADMAP Phase 3.5](ROADMAP.md#phase-35-v035-alternatives) for manual alternatives, and [Phase 4](ROADMAP.md#phase-4-v04-writing-partner) for AI alternatives and the Lab.

## Core: alternatives (word / sentence / paragraph)

- Squiggly/wavy underline and end dots mark items with alternatives, intentionally imperfect, editor-pen style.
- Hover and arrow keys cycle alternatives **in-line, in context**.
- A side panel with Word / Sentence / Paragraph tabs to view, add, or manage alternatives for the selection.
- A visual cue (highlighted middle dot) when you are on a non-original alternative.
- Add your own alternatives manually.
- AI alternatives: right-click → "AI alternatives for selection", or type `??` in the alternatives sidebar.
- AI suggestions are context-aware (sentence plus paragraph), not just synonyms.
- User-written alternatives are marked with a bullet; AI-written ones with a "bot point".
- Automatic a/an adjustment for the chosen word.
- Audio feedback: a higher tone on an alternative switch, a lower tone when returning to the original.
- Delete an alternative with a small explosion effect; undo restores it.
- Everything is stored as Markdown.

## Ghosting / dimming

- Select text → right-click "ghost it" dims it to about 10% opacity.
- Still readable, but easy to ignore, so you can feel the piece without it.
- Right-click "revive" to restore.
- Lab trim features reuse this dimming to suggest cuts.

## Overflow / stash panel

- A button opens a nearby overflow sidebar.
- Stash extra paragraphs, notes, outlines, or unused writing (select and stash, or paste).
- Pull it up any time to copy, paste, or drag back — better than dumping at the bottom of the document.

## Lab (AI editing helpers)

- Mark convoluted sentences.
- Flag words that do not match the tone.
- Fix punctuation and typos.
- Mask long or convoluted sentences.
- Mask hedges and filler.
- Trim modes: slight trim, tighten more, even sharper, cut in half.
- AI scans the document and dims suggested removable bits; it does not rewrite.
- Walk through the suggestions and accept or reject.

## Other

- Toggleable word count.
- Save and open.
- One-click post to X.
- A joke "LinkedIn" button that deliberately prevents posting there.
- Keyboard shortcuts for the side panels.
- Dark UI.

He described it as the four features he had always wanted in a writing environment.
