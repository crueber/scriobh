// Package editor holds the Go side of the editor's Markdown contract.
//
// The parser and serializer are JavaScript (ProseMirror, in web/editor), because
// the browser has to run the same code the server-side checks do. This package
// does not reimplement them. Its tests drive the JavaScript with Node over a
// shared golden corpus (testdata/corpus) so that the round trip is a gate on
// the Go build, and so that the committed bundle in web/static/js/editor.js
// cannot drift from its source.
//
// Node is a build-time and test-time dependency only. Nothing at runtime
// shells out to it.
package editor
