// Package pandoc is the optional bridge to the Pandoc program, for the formats
// Scríobh does not implement itself: import (odt, rtf, latex, mediawiki, org,
// textile, rst, fb2, typst, docbook, converted to GitHub-flavoured Markdown) and
// export (odt, rtf, latex, rst, mediawiki, converted from the compile Markdown).
//
// Pandoc is a large program that parses hostile formats, and some of its
// formats can name files (\input in LaTeX, include directives in Org and reStructuredText,
// images by path). So the bridge is built around not trusting it or the input:
//
//   - It is off until an administrator switches it on, and then only when the
//     binary is found: at an absolute path the administrator set, or on PATH.
//     The binary must be a regular executable file (after symbolic links) owned
//     by root or by the user the server runs as, not writable by its group or by
//     anyone, in a directory that is not either. It is checked again before every run.
//   - It is never run through a shell. The argument list is built here from a
//     fixed shape and two allow-lists of format names; nothing a person typed
//     (a file name, a title, an option) becomes an argument. --sandbox is always
//     given, and a version that does not have it is refused. There is no filter,
//     no template, no include, no resource path, no metadata and no extension on
//     a format name; --data-dir is an empty directory made for the call, and
//     media is extracted only into a directory made for the call.
//   - The input is a file with a name chosen here, in a directory made for the
//     call and removed after it, never a path from the request. It has a size cap.
//   - The environment is built here: no variable of the server's reaches it, no
//     proxy, nothing starting PANDOC_, and HOME is the call's own empty directory.
//   - The process runs in its own process group and the whole group is killed on a
//     deadline, when the caller goes away, or when it writes more than its cap to
//     standard output. Standard error is read and thrown away: it can echo the
//     document, so it is never logged or returned. Output has a size cap.
//   - Where the platform allows (Unix) it runs under resource limits (CPU time, open
//     files, the size of a file it writes, no core dump) set by a small re-exec of
//     this program just before Pandoc starts; see helper_unix.go. The memory limit is
//     Pandoc's own (+RTS -M), because an address-space limit breaks a Haskell program.
//   - At most two conversions run at once.
//   - Media it extracts is read only as regular files directly counted and capped
//     (links, directories and special files are skipped, names are discarded) and is
//     admitted only through the importer's picture checks (internal/imagesafe).
//
// What it returns is data: the Markdown goes through the same cleaning every
// importer applies (internal/imports); an exported file is never read by the server.
package pandoc
