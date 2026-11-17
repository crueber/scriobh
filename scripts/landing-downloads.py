#!/usr/bin/env python3
"""Writes the real download links of the latest release into the landing page.

  scripts/landing-downloads.py RELEASE.json landing/index.html

RELEASE.json is the output of `gh release view --json tagName,isPrerelease,assets` (the latest release, which GitHub
defines as the newest one that is neither a draft nor a pre-release). The page's own hrefs point at the releases
page, so a page that was never filled still has working links; this replaces them with the asset URLs, so the visitor's
browser never asks GitHub's API anything and a visitor without JavaScript gets the real files too.

What it fills, in landing/index.html:
  <a data-dl="KEY" ...>             its href becomes the asset's URL (KEYS below)
  <span data-fill="version">        the release tag (v2.2.0)
  <span data-fill="size:KEY">       the asset's size, "18 MB"
  <ul data-fill="servers">          one <li><a> per server archive, replacing whatever is inside

It FAILS (exit 1, nothing written) when the release is a pre-release, when any expected asset is missing, when an
asset's name carries another version than the tag, or when the page has no link for an expected key. A deploy that
stops is better than a page with dead links. Python 3 standard library only.
"""
import html
import json
import re
import sys

# KEY -> a regular expression for the asset's file name. {v} is the tag without its leading v.
DESKTOP = {
    "mac-universal": r"Scriobh_{v}_universal\.dmg",
    "mac-arm64": r"Scriobh_{v}_aarch64\.dmg",
    "mac-intel": r"Scriobh_{v}_x86_64\.dmg",
    "windows": r"scriobh-desktop-v{v}-windows-amd64-setup\.exe",
    "linux-appimage": r"Scriobh_{v}_amd64\.AppImage",
    "linux-deb": r"Scriobh_{v}_amd64\.deb",
    "sums-desktop": r"SHA256SUMS-desktop\.txt",
    "sums": r"SHA256SUMS\.txt",
}

# The server archives, in the order the page lists them: (os-arch in the file name, label).
SERVERS = [
    ("linux-amd64", "Linux, 64-bit Intel or AMD"),
    ("linux-arm64", "Linux, 64-bit ARM"),
    ("linux-arm", "Linux, 32-bit ARM"),
    ("linux-386", "Linux, 32-bit Intel or AMD"),
    ("linux-riscv64", "Linux, RISC-V"),
    ("linux-ppc64le", "Linux, POWER (little-endian)"),
    ("darwin-arm64", "macOS, Apple silicon"),
    ("darwin-amd64", "macOS, Intel"),
    ("windows-amd64", "Windows, 64-bit Intel or AMD"),
    ("windows-arm64", "Windows, ARM"),
    ("windows-386", "Windows, 32-bit"),
    ("freebsd-amd64", "FreeBSD, 64-bit Intel or AMD"),
    ("freebsd-arm64", "FreeBSD, 64-bit ARM"),
    ("openbsd-amd64", "OpenBSD, 64-bit Intel or AMD"),
    ("netbsd-amd64", "NetBSD, 64-bit Intel or AMD"),
]


def size_text(n):
    mb = n / (1024 * 1024)
    return "%d MB" % round(mb) if mb >= 1 else "%d KB" % max(1, round(n / 1024))


def main(argv):
    if len(argv) != 3:
        print(__doc__, file=sys.stderr)
        return 2
    release = json.load(open(argv[1], encoding="utf-8"))
    page = open(argv[2], encoding="utf-8").read()
    tag = release.get("tagName", "")
    problems = []
    if not re.fullmatch(r"v\d+\.\d+\.\d+", tag):
        problems.append("the latest release's tag is %r, not vX.Y.Z" % tag)
    if release.get("isPrerelease"):
        problems.append("the latest release %s is a pre-release" % tag)
    v = re.escape(tag[1:])
    assets = {a["name"]: a for a in release.get("assets", [])}

    found = {}
    for key, pat in DESKTOP.items():
        hits = [n for n in assets if re.fullmatch(pat.replace("{v}", v), n)]
        if len(hits) == 1:
            found[key] = assets[hits[0]]
        else:
            problems.append("%s: expected one asset named like %s, found %d" % (key, pat.replace("{v}", tag[1:]), len(hits)))
    servers = []
    for osarch, label in SERVERS:
        ext = "zip" if osarch.startswith("windows") else "tar.gz"
        name = "scriobh-%s-%s.%s" % (tag, osarch, ext)
        if name in assets:
            servers.append((label, assets[name]))
        else:
            problems.append("server archive %s is missing" % name)

    # every expected link must exist on the page, and every link on the page must have an asset
    page_keys = set(re.findall(r'data-dl="([a-z0-9-]+)"', page))
    for key in DESKTOP:
        if key not in page_keys:
            problems.append("the page has no link with data-dl=%r" % key)
    for key in page_keys - set(DESKTOP):
        problems.append("the page has a link with an unknown data-dl=%r" % key)
    if 'data-fill="servers"' not in page:
        problems.append('the page has no data-fill="servers" list')
    if problems:
        print("landing-downloads: refusing to fill the page:", file=sys.stderr)
        for p in problems:
            print("  - " + p, file=sys.stderr)
        return 1

    def set_href(m):
        tagtxt = m.group(0)
        key = re.search(r'data-dl="([a-z0-9-]+)"', tagtxt).group(1)
        url = html.escape(found[key]["url"], quote=True)
        if re.search(r'\shref="[^"]*"', tagtxt):
            return re.sub(r'(\shref=")[^"]*(")', lambda h: h.group(1) + url + h.group(2), tagtxt, count=1)
        return tagtxt[:-1] + ' href="%s">' % url

    page, n_links = re.subn(r'<a\b[^>]*\sdata-dl="[a-z0-9-]+"[^>]*>', set_href, page)
    page = re.sub(r'(<span data-fill="version">)[^<]*(</span>)', lambda m: m.group(1) + tag + m.group(2), page)
    page = re.sub(r'(<span data-fill="size:([a-z0-9-]+)">)[^<]*(</span>)',
                  lambda m: m.group(1) + size_text(found[m.group(2)]["size"]) + m.group(3) if m.group(2) in found else m.group(0), page)
    items = "".join('<li><a href="%s">%s</a> <span class="size">%s</span></li>' % (
        html.escape(a["url"], quote=True), html.escape(label), size_text(a["size"])) for label, a in servers)
    page = re.sub(r'(<ul[^>]*data-fill="servers"[^>]*>).*?(</ul>)', lambda m: m.group(1) + items + m.group(2), page, count=1, flags=re.S)
    open(argv[2], "w", encoding="utf-8").write(page)
    print("landing-downloads: %s: %d links, %d server archives" % (tag, n_links, len(servers)))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
