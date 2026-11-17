#!/usr/bin/env python3
"""Check WCAG 2.x contrast for every theme in the stylesheet.

Reads the token blocks out of web/static/css/*.css (or the files given on the
command line), resolves each theme, and checks the rules from docs/DESIGN.md
("Contrast rule"). Prints a table and exits non-zero when any pair fails.

A theme is found by its selector: the data-theme attribute names it (or, for the
named themes beyond Paper and Ink, data-lt / data-dk does), a bare
:root that declares --bg is "light", and the prefers-color-scheme: dark block
is "dark". A named theme that omits a token
inherits it from the default of its colour-scheme, so adding a theme that only
changes the accent works without touching this script. Two blocks that resolve
to the same theme name must agree exactly.

Python 3 standard library only.
"""
import glob
import os
import re
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_GLOB = os.path.join(ROOT, "web", "static", "css", "*.css")

ALIASES = {"light": "Paper", "dark": "Ink"}

# (label, foreground token, background token, minimum ratio, required)
RULES = [
    ("body text", "fg", "bg", 7.0, True),
    ("body text on page", "fg", "surface", 7.0, True),
    ("body text on rails", "fg", "surface-2", 7.0, True),
    ("secondary text", "fg-2", "bg", 4.5, True),
    ("secondary text on page", "fg-2", "surface", 4.5, True),
    ("secondary text on rails", "fg-2", "surface-2", 4.5, True),
    ("muted text", "muted", "bg", 4.5, True),
    ("muted text on page", "muted", "surface", 4.5, True),
    ("muted text on rails", "muted", "surface-2", 4.5, True),
    ("on-accent on accent", "on-accent", "accent", 4.5, True),
    ("accent link on bg", "accent", "bg", 4.5, True),
    ("accent link on page", "accent", "surface", 4.5, True),
    ("accent link on rails", "accent", "surface-2", 4.5, True),
    ("muted text on accent wash", "muted", "accent-soft", 4.5, True),  # the current row of a list and the chosen version: counts and numbers stay muted there
    ("accent on accent wash", "accent", "accent-soft", 4.5, False),
    ("fg-2 on accent wash", "fg-2", "accent-soft", 4.5, False),
    ("danger text on bg", "danger", "bg", 4.5, True),
    ("danger text on page", "danger", "surface", 4.5, True),
    ("danger on danger wash", "danger", "danger-soft", 4.5, False),
    ("confirming button text on danger", "surface", "danger", 4.5, True),
    ("text on the confirmation panel", "fg", "danger-soft", 4.5, True),
    ("warn text on bg", "warn", "bg", 4.5, True),
    ("warn text on page", "warn", "surface", 4.5, True),
    ("ok text on bg", "ok", "bg", 4.5, True),
    ("ok text on page", "ok", "surface", 4.5, True),
    ("prose on selection", "fg", "sel", 7.0, True),
    ("prose on search match", "fg", "match", 7.0, True),
    ("prose on comment wash", "fg", "comment", 7.0, True),
    ("prose on suggestion insert", "fg", "sugg-ins", 7.0, True),
    ("suggestion delete text", "sugg-del", "bg", 4.5, True),
    # The Lab's marks are lines and letters in the text: a graphic needs 3:1 against the page.
    ("Lab mark line on page", "lab", "bg", 3.0, True),
]

# The Lab's colour must be told apart from the other lines drawn in prose: the alternatives' underline (the accent) and the
# comment line. CIE76 distance in Lab space; 25 is a plainly different colour, 20 a clearly different one for a thin line.
# (token a, token b, minimum distance)
SEPARATION = [
    ("lab", "accent", 25.0),
    ("lab", "comment-line", 20.0),
]


def cielab(c):
    def f(x):
        x /= 255
        return x / 12.92 if x <= 0.04045 else ((x + 0.055) / 1.055) ** 2.4

    r, g, b = (f(x) for x in c)
    x = (0.4124 * r + 0.3576 * g + 0.1805 * b) / 0.95047
    y = 0.2126 * r + 0.7152 * g + 0.0722 * b
    z = (0.0193 * r + 0.1192 * g + 0.9505 * b) / 1.08883

    def t(v):
        return v ** (1 / 3) if v > 0.008856 else 7.787 * v + 16 / 116

    fx, fy, fz = t(x), t(y), t(z)
    return (116 * fy - 16, 500 * (fx - fy), 200 * (fy - fz))


def distance(a, b):
    return sum((p - q) ** 2 for p, q in zip(cielab(a), cielab(b))) ** 0.5
COVER_MIN = 4.5

def _ghost_opacity():
    try:
        m = re.search(r"\.scriobh-ghost\s*\{[^}]*?opacity:\s*([\d.]+)", open(os.path.join(ROOT, "web", "static", "css", "editor.css")).read())
        return float(m.group(1)) if m else None
    except OSError:
        return None


GHOST_OPACITY = _ghost_opacity()


def strip_comments(s):
    return re.sub(r"/\*.*?\*/", "", s, flags=re.S)


def blocks(css, media=None):
    """Yield (media, selector, body) for every rule, one level of @media deep."""
    i, n = 0, len(css)
    while i < n:
        j = css.find("{", i)
        if j < 0:
            return
        head = css[i:j].strip()
        depth, k = 1, j + 1
        while k < n and depth:
            depth += {"{": 1, "}": -1}.get(css[k], 0)
            k += 1
        body = css[j + 1 : k - 1]
        if head.startswith("@media"):
            yield from blocks(body, head)
        elif head.startswith("@"):
            pass  # @font-face, @keyframes, ...
        else:
            yield media, head, body
        i = k


def decls(body):
    out = {}
    for m in re.finditer(r"(-{0,2}[\w-]+)\s*:\s*([^;]+?)\s*(?:;|$)", body):
        out[m.group(1)] = m.group(2).strip()
    return out


def parse_color(v, over=None):
    v = v.strip()
    m = re.fullmatch(r"#([0-9a-fA-F]{3,8})", v)
    if m:
        h = m.group(1)
        if len(h) in (3, 4):
            h = "".join(c * 2 for c in h)
        r, g, b = (int(h[x : x + 2], 16) for x in (0, 2, 4))
        a = int(h[6:8], 16) / 255 if len(h) == 8 else 1.0
    else:
        m = re.fullmatch(r"rgba?\(\s*(\d+)[ ,]+(\d+)[ ,]+(\d+)(?:[ ,/]+([\d.]+%?))?\s*\)", v)
        if not m:
            return None
        r, g, b = int(m.group(1)), int(m.group(2)), int(m.group(3))
        a = 1.0
        if m.group(4):
            a = float(m.group(4).rstrip("%")) / (100 if m.group(4).endswith("%") else 1)
    if a < 1.0:
        if over is None:
            return None
        r, g, b = (round(a * c + (1 - a) * o) for c, o in zip((r, g, b), over))
    return (r, g, b)


def lum(c):
    def ch(x):
        x /= 255
        return x / 12.92 if x <= 0.03928 else ((x + 0.055) / 1.055) ** 2.4

    r, g, b = (ch(x) for x in c)
    return 0.2126 * r + 0.7152 * g + 0.0722 * b


def ratio(a, b):
    la, lb = lum(a), lum(b)
    if la < lb:
        la, lb = lb, la
    return (la + 0.05) / (lb + 0.05)


def theme_names(media, selector):
    """Names of the themes a selector list declares."""
    names = []
    for sel in (s.strip() for s in selector.split(",")):
        # The named themes beyond Paper and Ink: data-lt (a light theme) or data-dk (a dark one),
        # alone or with the data-theme that forces the scheme.
        m = re.search(r'\.tsw\[data-sw\s*=\s*"?([\w-]+)"?\]', sel)
        if m:  # the Settings swatch takes a theme's tokens; Paper and Ink go by their scheme names
            names.append({"paper": "light", "ink": "dark"}.get(m.group(1), m.group(1)))
            continue
        m = re.search(r'\[data-(?:lt|dk)\s*=\s*"?([\w-]+)"?\]', sel)
        if m:
            names.append(m.group(1))
            continue
        m = re.search(r'\[data-theme\s*=\s*"?([\w-]+)"?\]', sel)
        if m and ":not(" not in sel:
            names.append(m.group(1))
        elif sel.startswith(":root") and ":not(" in sel and media and "dark" in media:
            names.append("dark")
        elif sel == ":root":
            names.append("dark" if media and "prefers-color-scheme: dark" in media else "light")
    return names


def load(files):
    raw = {}  # name -> list of (where, decls)
    covers = {}
    for f in files:
        css = strip_comments(open(f, encoding="utf-8").read())
        for media, sel, body in blocks(css):
            d = decls(body)
            # A theme is found by its selector. The two defaults must declare their
            # palette; a named theme may override only a few tokens and inherit the rest.
            for name in theme_names(media, sel):
                if "--bg" in d or name not in ("light", "dark"):
                    raw.setdefault(name, []).append((os.path.basename(f) + ": " + sel, d))
            if "--cover" in d:
                for m in re.finditer(r"\.cover-(\d+)\b", sel):
                    covers[int(m.group(1))] = d["--cover"]
    return raw, covers


def resolve(raw):
    errors = []
    themes = {}
    for name, defs in raw.items():
        first = defs[0][1]
        for where, d in defs[1:]:
            toks = {k: v for k, v in d.items() if k.startswith("--")}
            ftoks = {k: v for k, v in first.items() if k.startswith("--")}
            if toks != ftoks:
                diff = sorted(k for k in set(toks) | set(ftoks) if toks.get(k) != ftoks.get(k))
                errors.append(f"theme '{name}' is declared twice with different tokens ({defs[0][0]} vs {where}): {', '.join(diff)}")
        themes[name] = dict(first)
    for name, d in themes.items():
        if name in ("light", "dark"):
            continue
        base = themes.get(d.get("color-scheme", "light").split()[0])
        if base:
            for k, v in base.items():
                d.setdefault(k, v)
    return themes, errors


def lab_used_for_text(files):
    """The Lab's colour reaches 3:1 for a line, not 4.5:1 for text: any rule that sets a text colour from it is a failure."""
    bad = []
    for f in files:
        css = strip_comments(open(f, encoding="utf-8").read())
        for media, sel, body in blocks(css):
            if re.search(r"(?<![-\w])color\s*:\s*var\(--lab\)", body):
                bad.append(f"{os.path.basename(f)}: {sel}")
    return bad


def main(argv):
    files = argv or sorted(glob.glob(DEFAULT_GLOB))
    if not files:
        print("no stylesheet found", file=sys.stderr)
        return 2
    raw, covers = load(files)
    themes, errors = resolve(raw)
    for need in ("light", "dark"):
        if need not in themes:
            errors.append(f"no '{need}' theme found in the token blocks")
    # The light and dark mappings in Settings need a choice on each side.
    for scheme in ("light", "dark"):
        n = sum(1 for t in themes.values() if t.get("color-scheme", "light").split()[0] == scheme)
        if n < 2:
            errors.append(f"only {n} {scheme} theme(s); Settings offers a choice of at least two per scheme")
    for where in lab_used_for_text(files):
        errors.append(f"--lab is a line colour (3:1) and is used for text in {where}: text needs 4.5:1")
    fails = 0
    print(f"{'theme':<14}{'rule':<28}{'pair':<26}{'ratio':>7}{'min':>6}  result")
    print("-" * 90)
    for name in sorted(themes, key=lambda n: (n not in ALIASES, n)):
        t = themes[name]
        label = ALIASES.get(name, name)
        bg = parse_color(t.get("--bg", ""))
        for rule, fg, bgt, minimum, required in RULES:
            fv, bv = t.get("--" + fg), t.get("--" + bgt)
            if fv is None or bv is None:
                if required:
                    errors.append(f"{label}: token --{fg if fv is None else bgt} is missing")
                continue
            bc = parse_color(bv, bg)
            fc = parse_color(fv, bc)
            if fc is None or bc is None:
                errors.append(f"{label}: cannot read --{fg}: {fv} or --{bgt}: {bv}")
                continue
            r = ratio(fc, bc)
            ok = r >= minimum
            fails += not ok
            print(f"{label:<14}{rule:<28}{fg + ' on ' + bgt:<26}{r:>7.2f}{minimum:>6.1f}  {'ok' if ok else 'FAIL'}")
        for ta, tb, minimum in SEPARATION:
            av, bv = t.get("--" + ta), t.get("--" + tb)
            if av is None or bv is None:
                errors.append(f"{label}: token --{ta if av is None else tb} is missing")
                continue
            ac, bc2 = parse_color(av, bg), parse_color(bv, bg)
            if ac is None or bc2 is None:
                errors.append(f"{label}: cannot read --{ta}: {av} or --{tb}: {bv}")
                continue
            d = distance(ac, bc2)
            ok = d >= minimum
            fails += not ok
            print(f"{label:<14}{'separation':<28}{ta + ' from ' + tb:<26}{d:>7.1f}{minimum:>6.1f}  {'ok' if ok else 'FAIL'}")
        # Ghosted text in the editor: the text colour at the stylesheet's opacity over the page, as readable as any text.
        if GHOST_OPACITY is not None:
            fv = parse_color(t.get("--fg", ""), bg)
            if fv is not None and bg is not None:
                eff = tuple(round(GHOST_OPACITY * c + (1 - GHOST_OPACITY) * o) for c, o in zip(fv, bg))
                r = ratio(eff, bg)
                ok = r >= COVER_MIN
                fails += not ok
                print(f"{label:<14}{'ghosted text':<28}{'fg at ' + format(GHOST_OPACITY, '.2f'):<26}{r:>7.2f}{COVER_MIN:>6.1f}  {'ok' if ok else 'FAIL'}")
        # Cover and avatar text: white (or whatever --cover-fg says) on every swatch.
        cf = parse_color(t.get("--cover-fg", ""))
        if cf is None:
            errors.append(f"{label}: --cover-fg is missing or not a solid colour")
        else:
            for i in sorted(covers):
                cc = parse_color(covers[i])
                if cc is None:
                    errors.append(f"cover-{i}: cannot read {covers[i]}")
                    continue
                r = ratio(cf, cc)
                ok = r >= COVER_MIN
                fails += not ok
                print(f"{label:<14}{'cover text':<28}{'cover-' + str(i) + ' ' + covers[i]:<26}{r:>7.2f}{COVER_MIN:>6.1f}  {'ok' if ok else 'FAIL'}")
    if not covers:
        errors.append("no .cover-N colours found")
    print("-" * 90)
    for e in errors:
        print("error:", e)
    print(f"{len(themes)} themes ({', '.join(ALIASES.get(n, n) for n in themes)}), {fails} failing pairs, {len(errors)} errors")
    return 1 if fails or errors else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
