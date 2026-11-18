#!/usr/bin/env python3
"""The files the desktop app's updater reads, made from what a release holds. Standard library only.

  updater-manifest.py config   --tag v2.3.0 --out FILE [--updater [--no-packages] --repo OWNER/REPO --pubkey FILE]
  updater-manifest.py pubkey   --file desktop/updater.pub --repo OWNER/REPO --production OWNER/REPO [--var KEY] --out FILE
  updater-manifest.py manifest --tag v2.3.0 --repo OWNER/REPO --assets LIST --sigs DIR --out latest.json
                               [--verify-dir DIR --pubkey FILE] [--now TIME]
  updater-manifest.py check    --tag v2.3.0 --repo OWNER/REPO --assets LIST latest.json

`config` writes the JSON that `tauri build --config` merges over desktop/src-tauri/tauri.conf.json for a release: the
version of the tag (so the version inside the package, the one the update signature records and the one in the
manifest are the same) and, with --updater, the update packages and the one place the app may look for them: the
`latest.json` of the repository's own latest release, with the key in desktop/updater.pub. Nothing that names a
repository or a key is committed; the workflow gives both here. docs/UPDATES.md, "Release pipeline".

With `--updater --no-packages` the configuration bakes the update source (the address and the public key) into the app and
asks for no update package: the universal macOS app is built that way, so it asks the question and updates to the package of
its own architecture. Baking the source in and making a package are two different things.

`pubkey` chooses the public key a build is made with and the release job verifies against: `desktop/updater.pub`, or the key in
the repository variable (a staging repository's own test key), which is REFUSED in the production repository unless it is
the same key, so a variable left set there cannot make a release that no installed app trusts.

`manifest` writes latest.json, the file every app that has been told it may check reads. It is written from a
closed list of names, never from text found on the Release: the version is the tag, the notes are one fixed sentence,
every address is `https://github.com/<repository>/releases/download/<tag>/<asset>` (pinned to the tag, so a later
release or a re-run cannot change where an older manifest points), and a platform appears only when its package
AND its signature are on the Release. It REFUSES (exit 1, nothing written) for a tag that is not a full release, a
signature that is empty, is not base64, is not a minisign signature or was made for another version, for a package that is on the
Release with no signature, and for a Release with no package at all. With `--verify-dir` and `--pubkey` it also checks every
signature CRYPTOGRAPHICALLY (Ed25519 over the BLAKE2b-512 of the package, and over the trusted comment) against the public key,
on the bytes the Release holds: a secret that does not belong to the public key, a stale `.sig` left by a re-run and a package
changed after signing are all refused, before any app could be shown them. `check` runs the same rules over a finished file, so a manifest that has been
tampered with, or made by anything else, does not pass.

The platform names are the ones tauri-plugin-updater 2.14 looks for: `<os>-<arch>-<installer>`. There is
deliberately NO bare `<os>-<arch>` key: for a binary whose bundle type is unknown the plugin falls through to the
AppImage installer and would overwrite the running program with an AppImage.
"""
import argparse
import base64
import binascii
import hashlib
import json
import os
import re
import sys
from datetime import datetime, timezone

PRODUCT = "Scriobh"
# A full release: v<major>.<minor>.<patch>, digits only. A prerelease (a hyphen) never gets a manifest, and a build
# metadata suffix (a plus) is refused everywhere: the plugin compares versions as semver, which ignores it.
FULL = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)")
ANY = re.compile(r"v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(-[0-9A-Za-z]+(\.[0-9A-Za-z]+)*)?")
REPO = re.compile(r"[A-Za-z0-9][A-Za-z0-9-]*/(?!\.\.?$)[A-Za-z0-9._-]+")
ASSET = re.compile(r"[A-Za-z0-9._-]+")
TOP_KEYS = {"version", "notes", "pub_date", "platforms"}
PLATFORM_KEYS = {"url", "signature"}


class Refused(Exception):
    """The input is not something to publish. The message says why and is for a person reading a workflow log."""


def legs(tag):
    """The closed list: platform key -> the name of its package on the Release, for a tag."""
    v = tag[1:]
    return {
        "darwin-aarch64-app": f"{PRODUCT}_{v}_aarch64.app.tar.gz",
        "darwin-x86_64-app": f"{PRODUCT}_{v}_x86_64.app.tar.gz",
        "windows-x86_64-nsis": f"scriobh-desktop-{tag}-windows-amd64-setup.exe",
        "linux-x86_64-appimage": f"{PRODUCT}_{v}_amd64.AppImage",
        # The .deb is listed so the app can say that a newer version exists; the app never installs it.
        "linux-x86_64-deb": f"{PRODUCT}_{v}_amd64.deb",
    }


def prefix(repo, tag):
    return f"https://github.com/{repo}/releases/download/{tag}/"


def need_tag(tag, full):
    if not (FULL if full else ANY).fullmatch(tag or ""):
        raise Refused(f"'{tag}' is not a {'full release' if full else 'release'} tag (v<major>.<minor>.<patch>"
                      f"{'' if full else ', optionally -prerelease'}; no +build)")


def need_repo(repo):
    if not REPO.fullmatch(repo or ""):
        raise Refused(f"'{repo}' is not OWNER/REPO")


# ---- a minisign verifier (standard library only: the release job does not hold the private key and installs nothing) -------

_P = 2**255 - 19
_Q = 2**252 + 27742317777372353535851937790883648493
_D = -121665 * pow(121666, _P - 2, _P) % _P
_I = pow(2, (_P - 1) // 4, _P)


def _xrecover(y):
    xx = (y * y - 1) * pow(_D * y * y + 1, _P - 2, _P)
    x = pow(xx, (_P + 3) // 8, _P)
    if (x * x - xx) % _P != 0:
        x = (x * _I) % _P
    return _P - x if x % 2 else x


_BY = 4 * pow(5, _P - 2, _P) % _P
_B = (_xrecover(_BY) % _P, _BY)


def _add(a, b):
    x1, y1 = a
    x2, y2 = b
    t = _D * x1 * x2 * y1 * y2
    return ((x1 * y2 + x2 * y1) * pow(1 + t, _P - 2, _P) % _P, (y1 * y2 + x1 * x2) * pow(1 - t, _P - 2, _P) % _P)


def _mul(point, e):
    result = (0, 1)
    while e:
        if e & 1:
            result = _add(result, point)
        point = _add(point, point)
        e >>= 1
    return result


def _decode_point(raw):
    y = int.from_bytes(raw, "little") & ((1 << 255) - 1)
    if y >= _P:
        raise ValueError("not a point")
    x = _xrecover(y)
    if (x & 1) != (raw[31] >> 7):
        x = _P - x
    if (-x * x + y * y - 1 - _D * x * x * y * y) % _P != 0:
        raise ValueError("not on the curve")
    return (x, y)


def _encode_point(point):
    x, y = point
    return (y | ((x & 1) << 255)).to_bytes(32, "little")


def ed25519_verify(signature, message, public_key):
    """RFC 8032 verification. False for anything that is not exactly a good signature."""
    if len(signature) != 64 or len(public_key) != 32:
        return False
    try:
        r = _decode_point(signature[:32])
        a = _decode_point(public_key)
    except ValueError:
        return False
    s = int.from_bytes(signature[32:], "little")
    if s >= _Q:
        return False
    h = int.from_bytes(hashlib.sha512(_encode_point(r) + public_key + message).digest(), "little")
    return _mul(_B, s) == _add(r, _mul(a, h))


def parse_public_key(text):
    """The contents of a Tauri .pub file (base64 of two lines) -> (key id, 32-byte key)."""
    try:
        lines = base64.b64decode(text.strip(), validate=True).decode("utf-8").strip("\n").split("\n")
        raw = base64.b64decode(lines[1], validate=True)
    except (binascii.Error, UnicodeDecodeError, ValueError, IndexError):
        raise Refused("the public key is not a minisign public key")
    if not lines[0].startswith("untrusted comment: minisign public key") or len(raw) != 42 or raw[:2] != b"Ed":
        raise Refused("the public key is not a minisign public key")
    return raw[2:10], raw[10:]


def verify_signature(sig_text, data_chunks, public_key_text, name):
    """Checks a .sig file (as the Tauri CLI writes it) over the bytes of a package, against a public key: the signature of the
    package and the global signature that covers the trusted comment (the version). `data_chunks` yields the package's bytes."""
    key_id, key = parse_public_key(public_key_text)
    try:
        lines = base64.b64decode(sig_text.strip(), validate=True).decode("utf-8").strip("\n").split("\n")
        raw = base64.b64decode(lines[1], validate=True)
        global_sig = base64.b64decode(lines[3], validate=True)
    except (binascii.Error, UnicodeDecodeError, ValueError, IndexError):
        raise Refused(f"the signature of {name} is not a minisign signature")
    if len(lines) != 4 or len(raw) != 74 or not lines[2].startswith("trusted comment: "):
        raise Refused(f"the signature of {name} is not a minisign signature")
    # The Tauri CLI always writes the prehashed form, `ED`. The legacy `Ed` (the signature over the whole file) is refused: nothing
    # of ours makes it, and a verifier that accepts two forms is one more thing to get wrong.
    if raw[:2] != b"ED":
        raise Refused(f"the signature of {name} is not in the prehashed form the Tauri CLI writes (algorithm {raw[:2]!r})")
    if raw[2:10] != key_id:
        raise Refused(f"the signature of {name} was made by another key than the public key it is checked against")
    h = hashlib.blake2b()
    for chunk in data_chunks:
        h.update(chunk)
    if not ed25519_verify(raw[10:], h.digest(), key):
        raise Refused(f"the signature of {name} does not match the package's bytes under the public key")
    if not ed25519_verify(global_sig, raw[10:] + lines[2][len("trusted comment: "):].encode("utf-8"), key):
        raise Refused(f"the trusted comment of {name} (its version) is not covered by a good signature")


def file_chunks(path):
    with open(path, "rb") as f:
        while True:
            chunk = f.read(1 << 20)
            if not chunk:
                return
            yield chunk


def decode_signature(text, version, name):
    """A minisign signature file as the Tauri CLI writes it, base64 of its four lines. Returns nothing; refuses.

    The signature's trusted comment is covered by the signature, and the plugin refuses a package whose signed
    version is not the one announced. So a signature that names another version, or none, is refused here, before
    any app would have to."""
    text = (text or "").strip()
    if not text:
        raise Refused(f"the signature of {name} is empty")
    try:
        raw = base64.b64decode(text, validate=True).decode("utf-8")
    except (binascii.Error, UnicodeDecodeError, ValueError):
        raise Refused(f"the signature of {name} is not base64 text")
    lines = raw.strip("\n").split("\n")
    if len(lines) != 4 or not lines[0].startswith("untrusted comment:") or not lines[2].startswith("trusted comment:"):
        raise Refused(f"the signature of {name} is not a minisign signature")
    for i in (1, 3):
        try:
            if len(base64.b64decode(lines[i], validate=True)) < 32:
                raise ValueError
        except (binascii.Error, ValueError):
            raise Refused(f"the signature of {name} is not a minisign signature (line {i + 1})")
    # The plugin reads the FIRST `version:` field of the trusted comment; a second one is an attempt to say two things and is refused.
    versions = [f[len("version:"):] for f in lines[2][len("trusted comment:"):].strip().split("\t") if f.startswith("version:")]
    if len(versions) > 1:
        raise Refused(f"the signature of {name} records its version more than once")
    signed = versions[0] if versions else None
    if signed is None:
        raise Refused(f"the signature of {name} does not record the version it was made for")
    if signed.lstrip("v") != version:
        raise Refused(f"the signature of {name} was made for version {signed}, not {version}")


def build(tag, repo, assets, sigs, now, verifier=None):
    """The manifest as a dict. `assets` is the set of names on the Release; `sigs` maps an asset name to the text of
    its .sig file (a name with no file is missing). `verifier(package, sig_text)`, when given, raises Refused for a signature that is
    not good for the bytes of that package."""
    need_tag(tag, True)
    need_repo(repo)
    version = tag[1:]
    base = prefix(repo, tag)
    platforms = {}
    skipped = []
    for key, package in legs(tag).items():
        sig_name = package + ".sig"
        if package not in assets:
            skipped.append(key)
            continue
        # A package that is on the Release must have its signature: a missing one is a leg that half failed, and a release
        # that silently leaves a platform without updates is not published.
        if sig_name not in assets or package not in sigs:
            raise Refused(f"{package} is on the Release and {sig_name} is not (or cannot be read): not publishing a release that leaves a platform without its update")
        if not ASSET.fullmatch(package):
            raise Refused(f"the asset name {package!r} has characters a download address must not")
        decode_signature(sigs[package], version, sig_name)
        if verifier is not None:
            verifier(package, sigs[package])
        platforms[key] = {"url": base + package, "signature": sigs[package].strip()}
    if not platforms:
        raise Refused("the Release holds no update package with its signature; there is nothing to announce")
    doc = {
        "version": version,
        "notes": f"Scriobh {version} is available. What changed: https://github.com/{repo}/releases/tag/{tag}",
        "pub_date": now,
        "platforms": platforms,
    }
    verify(doc, tag, repo, assets)
    return doc, skipped


def verify(doc, tag, repo, assets):
    """The rules every manifest must meet, applied to the one just made and to any file handed to `check`."""
    need_tag(tag, True)
    need_repo(repo)
    if not isinstance(doc, dict) or set(doc) != TOP_KEYS:
        raise Refused(f"the manifest's keys are {sorted(doc) if isinstance(doc, dict) else doc!r}, not {sorted(TOP_KEYS)}")
    if doc["version"] != tag[1:]:
        raise Refused(f"the manifest's version is {doc['version']!r}, not {tag[1:]!r}")
    if not isinstance(doc["notes"], str) or "\n" in doc["notes"] or len(doc["notes"]) > 300:
        raise Refused("the notes are not one short line")
    try:
        datetime.strptime(doc["pub_date"], "%Y-%m-%dT%H:%M:%SZ")
    except (TypeError, ValueError):
        raise Refused(f"pub_date {doc['pub_date']!r} is not a UTC time")
    platforms = doc["platforms"]
    if not isinstance(platforms, dict) or not platforms:
        raise Refused("the manifest names no platform")
    allowed = legs(tag)
    base = prefix(repo, tag)
    for key, entry in platforms.items():
        if key not in allowed:
            raise Refused(f"the platform {key!r} is not one of {sorted(allowed)}")
        if not isinstance(entry, dict) or set(entry) != PLATFORM_KEYS:
            raise Refused(f"the entry of {key} has keys other than {sorted(PLATFORM_KEYS)}")
        url = entry["url"]
        if not isinstance(url, str) or not url.startswith(base):
            raise Refused(f"the address of {key} does not start with {base}")
        name = url[len(base):]
        if name != allowed[key] or not ASSET.fullmatch(name):
            raise Refused(f"the address of {key} names {name!r}, not {allowed[key]!r}")
        if name not in assets or name + ".sig" not in assets:
            raise Refused(f"{name} or its signature is not on the Release")
        decode_signature(entry["signature"], tag[1:], name + ".sig")


def write_config(args):
    need_tag(args.tag, False)
    conf = {"version": args.tag[1:]}
    if args.no_packages and not args.updater:
        raise Refused("--no-packages goes with --updater")
    if args.updater:
        need_repo(args.repo)
        if not args.pubkey:
            raise Refused("--updater needs --pubkey")
        with open(args.pubkey, encoding="ascii") as f:
            key = f.read().strip()
        try:
            text = base64.b64decode(key, validate=True).decode("ascii")
        except (binascii.Error, UnicodeDecodeError, ValueError):
            raise Refused("the public key file is not base64 text")
        if not text.startswith("untrusted comment: minisign public key"):
            raise Refused("the public key file is not a minisign PUBLIC key (is it the private one?)")
        if not args.no_packages:
            conf["bundle"] = {"createUpdaterArtifacts": True}
        conf["plugins"] = {"updater": {
            "endpoints": [f"https://github.com/{args.repo}/releases/latest/download/latest.json"],
            "pubkey": key,
            "requireSignedVersion": True,
        }}
    with open(args.out, "w", encoding="utf-8") as f:
        json.dump(conf, f, indent=2)
        f.write("\n")
    what = " (version only)"
    if args.updater:
        what = " with the update source" + (" and no update package" if args.no_packages else "")
    print(f"updater-manifest: wrote {args.out} for {args.tag}{what}")


def choose_pubkey(args):
    with open(args.file, encoding="ascii") as f:
        committed = f.read().strip()
    parse_public_key(committed)
    chosen = (args.var or "").strip()
    if chosen:
        parse_public_key(chosen)
        need_repo(args.repo)
        if args.repo.lower() == args.production.lower() and chosen != committed:
            raise Refused("the repository variable UPDATER_PUBKEY is set in the production repository and is not desktop/updater.pub: "
                          "a release made with it would be trusted by no installed app. Delete the variable.")
    else:
        chosen = committed
    with open(args.out, "w", encoding="ascii") as f:
        f.write(chosen + "\n")
    print(f"updater-manifest: the public key is {'the repository variable' if chosen != committed else 'desktop/updater.pub'}")


def read_assets(path):
    with open(path, encoding="utf-8") as f:
        return {line.strip() for line in f if line.strip()}


def read_sigs(directory, assets):
    out = {}
    for name in assets:
        if name.endswith(".sig") and ASSET.fullmatch(name):
            try:
                with open(f"{directory}/{name}", encoding="utf-8") as f:
                    out[name[:-4]] = f.read()
            except OSError:
                pass
    return out


def main(argv):
    p = argparse.ArgumentParser(description=__doc__, formatter_class=argparse.RawDescriptionHelpFormatter)
    sub = p.add_subparsers(dest="cmd", required=True)
    c = sub.add_parser("config")
    c.add_argument("--tag", required=True)
    c.add_argument("--out", required=True)
    c.add_argument("--updater", action="store_true")
    c.add_argument("--no-packages", action="store_true", help="with --updater: bake the update source in, ask for no update package")
    c.add_argument("--repo")
    c.add_argument("--pubkey")
    pk = sub.add_parser("pubkey")
    pk.add_argument("--file", required=True)
    pk.add_argument("--repo", required=True)
    pk.add_argument("--production", required=True, help="the production repository, OWNER/REPO")
    pk.add_argument("--var", default="", help="the repository variable UPDATER_PUBKEY (may be empty)")
    pk.add_argument("--out", required=True)
    m = sub.add_parser("manifest")
    m.add_argument("--tag", required=True)
    m.add_argument("--repo", required=True)
    m.add_argument("--assets", required=True, help="a file with the Release's asset names, one per line")
    m.add_argument("--sigs", required=True, help="a folder holding the .sig files of the Release")
    m.add_argument("--out", required=True)
    m.add_argument("--verify-dir", help="a folder holding the packages of the Release: every signature is verified against --pubkey")
    m.add_argument("--pubkey", help="the file with the public key (see the pubkey subcommand)")
    m.add_argument("--now", help="the pub_date, as 2026-11-17T12:00:00Z (default: now, UTC)")
    k = sub.add_parser("check")
    k.add_argument("--tag", required=True)
    k.add_argument("--repo", required=True)
    k.add_argument("--assets", required=True)
    k.add_argument("file")
    a = p.parse_args(argv)
    try:
        if a.cmd == "config":
            write_config(a)
        elif a.cmd == "pubkey":
            choose_pubkey(a)
        elif a.cmd == "manifest":
            assets = read_assets(a.assets)
            now = a.now or datetime.now(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ")
            verifier = None
            if bool(a.verify_dir) != bool(a.pubkey):
                raise Refused("--verify-dir and --pubkey go together")
            if a.pubkey:
                with open(a.pubkey, encoding="ascii") as f:
                    pub = f.read()

                def verifier(package, sig_text):
                    path = f"{a.verify_dir}/{package}"
                    if not ASSET.fullmatch(package) or not os.path.isfile(path):
                        raise Refused(f"{package} is not in {a.verify_dir}, so its signature cannot be verified")
                    verify_signature(sig_text, file_chunks(path), pub, package + ".sig")
            doc, skipped = build(a.tag, a.repo, assets, read_sigs(a.sigs, assets), now, verifier)
            with open(a.out, "w", encoding="utf-8") as f:
                json.dump(doc, f, indent=2, sort_keys=True)
                f.write("\n")
            print(f"updater-manifest: {a.out} announces {doc['version']} for {', '.join(sorted(doc['platforms']))}")
            for key in skipped:
                print(f"updater-manifest: no package with a signature for {key}; it is left out")
        else:
            try:
                with open(a.file, encoding="utf-8") as f:
                    doc = json.load(f)
            except (OSError, ValueError) as e:
                raise Refused(f"{a.file} is not JSON: {e}")
            verify(doc, a.tag, a.repo, read_assets(a.assets))
            print(f"updater-manifest: {a.file} is a manifest for {a.tag}")
    except Refused as e:
        print(f"updater-manifest: REFUSED: {e}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
