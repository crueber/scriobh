# README media

The screenshots and the demo video used by the README (and free to reuse on a landing page). Everything shown is invented: a small novel called *The Long Slack* by an invented author, with an invented Compendium, comment and writing history. No real person, account or host appears in any of it.

Made by `node scripts/readme-media.mjs` from a seeded throwaway instance (`scripts/readme-seed.mjs`); see "Remaking them" below. File names are stable: link to them by name.

## The video

| File | What it is |
| --- | --- |
| `demo.webm` | The 46-second demo, 1280x720, VP8, no sound. The file to play. |
| `demo.mp4` | The same video as H.264 (`yuv420p`, `faststart`), for a player or a browser that wants MP4. |
| `demo.webp` | An 800-pixel animated WebP of the same video. It plays inside a README, where GitHub will not play a `<video>`; link it to `demo.webm`. |
| `demo-poster.png` | One still of the writing view, 1280x720, for a `<video poster>` or a social card. |

The demo: the library, opening a project, typing a sentence, cycling an alternative wording in place, the outline, the Writing Studio, and compiling an EPUB, with a caption for each step.

## The screenshots

Desktop shots are 1440x900. Each comes in a light and a dark theme: `NAME-light.png` and `NAME-dark.png`.

| Name | What it shows |
| --- | --- |
| `library` | The library: a shelf of five projects and a card to continue where you left off. |
| `write` | The writing view, calm, with the manuscript tree and the toolbar. |
| `alternatives` | A word with three wordings being cycled in place; the text holds the chosen one. |
| `review` | The reading view with a comment anchored to a phrase, in the margin. |
| `outline` | The outline: synopses, status and word targets per part and chapter. |
| `compendium` | The Compendium: the entities of the book's bible. |
| `studio` | The Writing Studio dashboard: words by period, streak, and the year's calendar. |
| `compile` | The Compile page with the EPUB 3 preset and a preview of the title page. |
| `phone-write` | The writing view at phone width (390 px wide, drawn at twice the resolution, 780x1688). |

## Remaking them

```sh
cd scripts && npm ci                 # once: Playwright
cd ..
node scripts/readme-media.mjs        # the stills and the video into docs/media
node scripts/readme-media.mjs --shots   # stills only
node scripts/readme-media.mjs --video   # video only
```

It builds the development server, starts it on an ephemeral loopback port with the stub issuer, seeds it through the REST ops (the writing history through the Go helper `TestSeedStudioHistory`, which is why a Go toolchain is needed), and drives a real Chromium. Needs `ffmpeg` (with libx264 and libvpx) and `img2webp` (`brew install ffmpeg webp`); `oxipng` (`brew install oxipng`) is used for the PNGs when it is there. It makes no request outside the machine. `--out DIR` writes somewhere else, `--only NAME` takes one still.
