# Vendored scripts

## htmx

| | |
| --- | --- |
| File | `htmx.min.js` |
| Version | 2.0.11 |
| Source | `npm pack htmx.org@2.0.11`, file `package/dist/htmx.min.js` (unmodified) |
| SHA-256 | `d6fdc75f204e6bdefa99b69bf1e6d4ac69b8a364f77929f45c13476b4000f717` |
| Licence | Zero-Clause BSD (BSD-0), text in the package's `LICENSE` |
| Size | 52,182 bytes, 16.9 KB gzip |

htmx is configured by the `htmx-config` meta tag in `templates/layout.html`
(`allowEval` off, no inline indicator styles, same-origin requests only).
To upgrade: pack the new version into a scratch directory, replace the file,
update this table, and re-run `go test ./internal/server/...` for the budget.

`shell.js` is first-party. `editor.js`, when it lands, is a separate bundle
with its own budget and its own notes.
