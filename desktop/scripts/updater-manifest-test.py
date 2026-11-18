#!/usr/bin/env python3
"""Tests of updater-manifest.py: the golden manifest, and each refusal. Standard library only; `make updater-check`."""
import base64
import importlib.util
import json
import os
import tempfile
import unittest

HERE = os.path.dirname(os.path.abspath(__file__))
spec = importlib.util.spec_from_file_location("updater_manifest", os.path.join(HERE, "updater-manifest.py"))
um = importlib.util.module_from_spec(spec)
spec.loader.exec_module(um)
um.print = lambda *args, **kwargs: None  # the script reports on stdout and stderr; the tests read its exit codes

REPO = "someone/scriobh"
TAG = "v2.3.0"
NOW = "2026-11-17T12:00:00Z"


def b64(raw):
    return base64.b64encode(raw).decode()


def sig(version="2.3.0", file="package", fill=1):
    """A signature in the shape the Tauri CLI writes: base64 of the four lines of a minisign signature file."""
    text = "\n".join([
        "untrusted comment: signature from tauri secret key",
        b64(bytes([fill]) * 74),
        f"trusted comment: timestamp:1763380800\tfile:{file}" + (f"\tversion:{version}" if version else ""),
        b64(bytes([fill + 1]) * 64),
    ]) + "\n"
    return b64(text.encode())


def release(tag=TAG, only=None, skip_sig=(), extra=()):
    """The asset list and the .sig texts of a Release with every leg, less what the test removes."""
    names = {}
    for key, package in um.legs(tag).items():
        if only is None or key in only:
            names[package] = sig(tag[1:], package, fill=len(names) + 1)
    assets = set(extra)
    sigs = {}
    for package, text in names.items():
        assets.add(package)
        if package not in skip_sig:
            assets.add(package + ".sig")
            sigs[package] = text
    return assets, sigs


class Manifest(unittest.TestCase):
    def test_a_full_release_makes_the_golden_manifest(self):
        assets, sigs = release()
        doc, skipped = um.build(TAG, REPO, assets, sigs, NOW)
        self.assertEqual(skipped, [])
        self.assertEqual(set(doc), {"version", "notes", "pub_date", "platforms"})
        self.assertEqual(doc["version"], "2.3.0")
        self.assertEqual(doc["pub_date"], NOW)
        self.assertEqual(
            sorted(doc["platforms"]),
            ["darwin-aarch64-app", "darwin-x86_64-app", "linux-x86_64-appimage", "linux-x86_64-deb", "windows-x86_64-nsis"],
        )
        base = f"https://github.com/{REPO}/releases/download/{TAG}/"
        self.assertEqual(doc["platforms"]["darwin-aarch64-app"]["url"], base + "Scriobh_2.3.0_aarch64.app.tar.gz")
        self.assertEqual(doc["platforms"]["darwin-x86_64-app"]["url"], base + "Scriobh_2.3.0_x86_64.app.tar.gz")
        self.assertEqual(doc["platforms"]["windows-x86_64-nsis"]["url"], base + "scriobh-desktop-v2.3.0-windows-amd64-setup.exe")
        self.assertEqual(doc["platforms"]["linux-x86_64-appimage"]["url"], base + "Scriobh_2.3.0_amd64.AppImage")
        self.assertEqual(doc["platforms"]["linux-x86_64-deb"]["url"], base + "Scriobh_2.3.0_amd64.deb")
        for entry in doc["platforms"].values():
            self.assertEqual(set(entry), {"url", "signature"})
            self.assertTrue(entry["url"].startswith(base))
        self.assertEqual(doc["notes"], f"Scriobh 2.3.0 is available. What changed: https://github.com/{REPO}/releases/tag/{TAG}")

    def test_the_golden_file_is_byte_for_byte_what_the_script_writes(self):
        assets, sigs = release()
        with tempfile.TemporaryDirectory() as d:
            lst, sigdir, out = f"{d}/assets.txt", f"{d}/sigs", f"{d}/latest.json"
            os.mkdir(sigdir)
            with open(lst, "w") as f:
                f.write("\n".join(sorted(assets)) + "\n")
            for package, text in sigs.items():
                with open(f"{sigdir}/{package}.sig", "w") as f:
                    f.write(text + "\n")
            rc = um.main(["manifest", "--tag", TAG, "--repo", REPO, "--assets", lst, "--sigs", sigdir, "--out", out, "--now", NOW])
            self.assertEqual(rc, 0)
            with open(out) as f:
                text = f.read()
            # The file is deterministic (sorted keys), ends with a newline, and every platform key is listed once.
            self.assertTrue(text.endswith("}\n"))
            self.assertEqual(json.loads(text)["version"], "2.3.0")
            self.assertEqual(text, json.dumps(json.loads(text), indent=2, sort_keys=True) + "\n")
            self.assertEqual(um.main(["check", "--tag", TAG, "--repo", REPO, "--assets", lst, out]), 0)

    def test_there_is_never_a_bare_os_arch_key(self):
        doc, _ = um.build(TAG, REPO, *release(), NOW)
        for key in doc["platforms"]:
            self.assertEqual(key.count("-"), 2, key)
        self.assertNotIn("linux-x86_64", doc["platforms"])
        self.assertNotIn("darwin-x86_64", doc["platforms"])
        self.assertNotIn("windows-x86_64", doc["platforms"])

    def test_a_leg_that_built_no_package_is_left_out_and_the_release_still_goes(self):
        # no Windows package at all, and no AppImage (that leg is allowed to fail)
        only = set(um.legs(TAG)) - {"windows-x86_64-nsis", "linux-x86_64-appimage"}
        doc, skipped = um.build(TAG, REPO, *release(only=only), NOW)
        self.assertEqual(sorted(skipped), ["linux-x86_64-appimage", "windows-x86_64-nsis"])
        self.assertNotIn("windows-x86_64-nsis", doc["platforms"])
        self.assertIn("darwin-aarch64-app", doc["platforms"])

    def test_a_package_on_the_release_with_no_signature_is_refused(self):
        # a leg that half failed must not become a release that leaves its platform without updates
        for package in um.legs(TAG).values():
            assets, sigs = release(skip_sig={package})
            with self.assertRaisesRegex(um.Refused, "not publishing"):
                um.build(TAG, REPO, assets, sigs, NOW)
        # a .sig that is listed but cannot be read counts as missing
        assets, sigs = release()
        del sigs[um.legs(TAG)["linux-x86_64-deb"]]
        with self.assertRaises(um.Refused):
            um.build(TAG, REPO, assets, sigs, NOW)

    def test_a_signature_with_no_package_names_nothing_and_nothing_is_signed_for_it(self):
        assets, sigs = release()
        assets.discard(um.legs(TAG)["linux-x86_64-deb"])
        doc, skipped = um.build(TAG, REPO, assets, sigs, NOW)
        self.assertIn("linux-x86_64-deb", skipped)
        self.assertNotIn("linux-x86_64-deb", doc["platforms"])

    def test_a_release_with_no_package_is_refused(self):
        with self.assertRaisesRegex(um.Refused, "no update package"):
            um.build(TAG, REPO, {"SHA256SUMS.txt"}, {}, NOW)

    def test_a_prerelease_and_other_odd_tags_are_refused(self):
        for tag in ["v2.3.0-rc.1", "v2.3.0+build5", "2.3.0", "v2.3", "v2.3.0.1", "v02.3.0", "main", "", "v2.3.0\n"]:
            assets, sigs = release()
            with self.assertRaises(um.Refused, msg=tag):
                um.build(tag, REPO, assets, sigs, NOW)

    def test_an_odd_repository_is_refused(self):
        for repo in ["", "someone", "a/b/c", "a b/c", "a/b\n", "https://evil.example/x", "../x"]:
            with self.assertRaises(um.Refused, msg=repo):
                um.build(TAG, repo, *release(), NOW)

    def test_an_empty_or_garbled_signature_is_refused(self):
        package = um.legs(TAG)["darwin-aarch64-app"]
        for bad in ["", "   \n", "not base64 !!", b64(b"just one line"), b64(b"a\nb\nc\nd"), b64(("untrusted comment: x\n" + b64(b"short") + "\ntrusted comment: version:2.3.0\n" + b64(b"x" * 64) + "\n").encode())]:
            assets, sigs = release()
            sigs[package] = bad
            with self.assertRaises(um.Refused, msg=bad):
                um.build(TAG, REPO, assets, sigs, NOW)

    def test_a_signature_made_for_another_version_or_none_is_refused(self):
        package = um.legs(TAG)["windows-x86_64-nsis"]
        for text in [sig("2.2.0"), sig(None)]:
            assets, sigs = release()
            sigs[package] = text
            with self.assertRaisesRegex(um.Refused, "version"):
                um.build(TAG, REPO, assets, sigs, NOW)

    def test_check_refuses_a_manifest_that_points_somewhere_else(self):
        assets, sigs = release()
        doc, _ = um.build(TAG, REPO, assets, sigs, NOW)
        um.verify(doc, TAG, REPO, assets)

        def mutated(change):
            d = json.loads(json.dumps(doc))
            change(d)
            return d

        smuggled = [
            # a foreign host, a lookalike host, another repository, the moving `latest`, a path trick, plain http
            lambda d: d["platforms"]["windows-x86_64-nsis"].update(url="https://downloads.example/x/" + um.legs(TAG)["windows-x86_64-nsis"]),
            lambda d: d["platforms"]["windows-x86_64-nsis"].update(url=f"https://github.com.example/{REPO}/releases/download/{TAG}/" + um.legs(TAG)["windows-x86_64-nsis"]),
            lambda d: d["platforms"]["windows-x86_64-nsis"].update(url=f"https://github.com/other/repo/releases/download/{TAG}/" + um.legs(TAG)["windows-x86_64-nsis"]),
            lambda d: d["platforms"]["windows-x86_64-nsis"].update(url=f"https://github.com/{REPO}/releases/latest/download/" + um.legs(TAG)["windows-x86_64-nsis"]),
            lambda d: d["platforms"]["windows-x86_64-nsis"].update(url=f"https://github.com/{REPO}/releases/download/{TAG}/../v1.0.0/" + um.legs(TAG)["windows-x86_64-nsis"]),
            lambda d: d["platforms"]["windows-x86_64-nsis"].update(url=f"http://github.com/{REPO}/releases/download/{TAG}/" + um.legs(TAG)["windows-x86_64-nsis"]),
            # another asset of the same Release under a platform's name
            lambda d: d["platforms"]["windows-x86_64-nsis"].update(url=f"https://github.com/{REPO}/releases/download/{TAG}/SHA256SUMS.txt"),
            # a key the plugin would honour that the closed list does not have
            lambda d: d["platforms"].update({"linux-x86_64": dict(d["platforms"]["linux-x86_64-appimage"])}),
            lambda d: d["platforms"].update({"linux-aarch64-appimage": dict(d["platforms"]["linux-x86_64-appimage"])}),
            # extra keys anywhere
            lambda d: d.update(url="https://evil.example/x"),
            lambda d: d.update(signature=sig()),
            lambda d: d["platforms"]["darwin-aarch64-app"].update(headers={"x": "y"}),
            # the wrong version, notes with a line break, a bad date, no platforms
            lambda d: d.update(version="2.4.0"),
            lambda d: d.update(notes="one\ntwo"),
            lambda d: d.update(pub_date="yesterday"),
            lambda d: d.update(platforms={}),
        ]
        for i, change in enumerate(smuggled):
            with self.assertRaises(um.Refused, msg=f"case {i}"):
                um.verify(mutated(change), TAG, REPO, assets)
        # a package that is not on the Release, and an entry whose signature was swapped for another version's
        with self.assertRaises(um.Refused):
            um.verify(doc, TAG, REPO, assets - {um.legs(TAG)["linux-x86_64-deb"]})
        with self.assertRaises(um.Refused):
            um.verify(mutated(lambda d: d["platforms"]["linux-x86_64-deb"].update(signature=sig("2.2.0"))), TAG, REPO, assets)

    def test_check_refuses_a_file_that_is_not_json(self):
        with tempfile.TemporaryDirectory() as d:
            lst, bad = f"{d}/assets.txt", f"{d}/latest.json"
            with open(lst, "w") as f:
                f.write("x\n")
            with open(bad, "w") as f:
                f.write("<html>not json</html>")
            self.assertEqual(um.main(["check", "--tag", TAG, "--repo", REPO, "--assets", lst, bad]), 1)

    def test_a_refusal_writes_nothing(self):
        with tempfile.TemporaryDirectory() as d:
            lst, sigdir, out = f"{d}/assets.txt", f"{d}/sigs", f"{d}/latest.json"
            os.mkdir(sigdir)
            with open(lst, "w") as f:
                f.write("Scriobh_2.3.0_aarch64.app.tar.gz\nScriobh_2.3.0_aarch64.app.tar.gz.sig\n")
            with open(f"{sigdir}/Scriobh_2.3.0_aarch64.app.tar.gz.sig", "w") as f:
                f.write("")
            self.assertEqual(um.main(["manifest", "--tag", TAG, "--repo", REPO, "--assets", lst, "--sigs", sigdir, "--out", out]), 1)
            self.assertFalse(os.path.exists(out))

    def test_asset_names_may_not_escape_the_sigs_folder(self):
        with tempfile.TemporaryDirectory() as d:
            self.assertEqual(um.read_sigs(d, {"../etc/passwd.sig", "a b.sig"}), {})


DATA = os.path.join(HERE, "testdata")


def data(name):
    with open(os.path.join(DATA, name), "rb") as f:
        return f.read()


def text(name):
    return data(name).decode("ascii")


class Verify(unittest.TestCase):
    """Real signatures, made by the Tauri CLI with two throwaway keys (testdata/): a good one passes, and every way of being
    wrong fails. The fixtures were made for version 2.3.0 over minisign-payload.txt."""

    def check(self, sig, payload=None, pub="minisign-test-a.pub"):
        um.verify_signature(sig, [payload if payload is not None else data("minisign-payload.txt")], text(pub), "x.sig")

    def test_a_good_signature_passes(self):
        self.check(text("minisign-payload.txt.sig"))
        self.check(text("minisign-payload.other-key.sig"), pub="minisign-test-b.pub")

    def test_a_signature_by_another_key_fails(self):
        with self.assertRaisesRegex(um.Refused, "another key"):
            self.check(text("minisign-payload.other-key.sig"))          # B's signature, A's public key
        with self.assertRaisesRegex(um.Refused, "another key"):
            self.check(text("minisign-payload.txt.sig"), pub="minisign-test-b.pub")

    def test_a_signature_for_other_bytes_fails(self):
        # a package changed after it was signed, and a stale .sig of an earlier build left beside a new package
        good = data("minisign-payload.txt")
        for changed in [good[:-1] + b"!", b"x" + good, good + b"\n", b"", b"another build"]:
            with self.assertRaisesRegex(um.Refused, "does not match"):
                self.check(text("minisign-payload.txt.sig"), changed)

    def test_a_changed_trusted_comment_fails(self):
        # the version is covered by its own signature: 2.3.0 cannot be rewritten as another version
        raw = base64.b64decode(text("minisign-payload.txt.sig")).decode()
        self.assertIn("version:2.3.0", raw)
        forged = b64(raw.replace("version:2.3.0", "version:9.9.9").encode())
        with self.assertRaisesRegex(um.Refused, "trusted comment"):
            self.check(forged)

    # ---- forged signatures: every one starts from the real signature and changes one thing

    def parts(self, name="minisign-payload.txt.sig"):
        lines = base64.b64decode(text(name)).decode().strip("\n").split("\n")
        return lines, bytearray(base64.b64decode(lines[1])), base64.b64decode(lines[3])

    def encode(self, lines, raw=None, glob=None, sep="\n", tail="\n"):
        lines = list(lines)
        if raw is not None:
            lines[1] = b64(bytes(raw))
        if glob is not None:
            lines[3] = b64(bytes(glob))
        return b64((sep.join(lines) + tail).encode())

    def test_every_single_change_to_a_real_signature_is_refused(self):
        lines, raw, glob = self.parts()
        _, _, other_glob = self.parts("minisign-payload.other-key.sig")
        flipped = bytearray(raw)
        flipped[20] ^= 1
        s_value = int.from_bytes(bytes(raw[42:]), "little")
        non_canonical = bytearray(raw[:42]) + (s_value + um._Q).to_bytes(32, "little")  # the same S plus the group order
        relabelled = bytearray(raw)
        relabelled[:2] = b"Ed"
        other_id = bytearray(raw)
        other_id[5] ^= 1
        bad_glob = bytearray(glob)
        bad_glob[3] ^= 1
        cases = {
            "a flipped signature byte": (self.encode(lines, raw=flipped), "does not match"),
            "a flipped global signature byte": (self.encode(lines, glob=bad_glob), "trusted comment"),
            "a global signature of another key": (self.encode(lines, glob=other_glob), "trusted comment"),
            "a non-canonical S (S + L)": (self.encode(lines, raw=non_canonical), "does not match"),
            "the algorithm relabelled Ed": (self.encode(lines, raw=relabelled), "prehashed"),
            "a flipped key id": (self.encode(lines, raw=other_id), "another key"),
            "CRLF line ends": (self.encode(lines, sep="\r\n", tail="\r\n"), ""),
            "a trailing line": (self.encode(lines + ["one more line"]), ""),
            "a missing line": (self.encode(lines[:3]), ""),
            "a comment line moved": (self.encode([lines[0], lines[2], lines[1], lines[3]]), ""),
        }
        for what, (sig, why) in cases.items():
            with self.assertRaisesRegex(um.Refused, why, msg=what):
                self.check(sig)
        # the unchanged one still passes: the cases above fail because of the change, not because of the harness
        self.check(self.encode(lines))

    def test_a_version_recorded_twice_is_refused_whichever_is_read(self):
        lines, raw, glob = self.parts()
        twice = list(lines)
        twice[2] = lines[2] + "\tversion:9.9.9"
        sig = self.encode(twice)
        with self.assertRaisesRegex(um.Refused, "more than once"):
            um.decode_signature(sig, "2.3.0", "x.sig")
        with self.assertRaisesRegex(um.Refused, "more than once"):
            um.decode_signature(sig, "9.9.9", "x.sig")
        first = list(lines)
        first[2] = lines[2].replace("version:2.3.0", "version:9.9.9\tversion:2.3.0")
        with self.assertRaises(um.Refused):
            um.decode_signature(self.encode(first), "2.3.0", "x.sig")

    def test_ed25519_matches_the_rfc_8032_vectors(self):
        # RFC 8032, section 7.1, tests 1 to 3: public key, message, signature
        vectors = [
            ("d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a", "",
             "e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e065224901555fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b"),
            ("3d4017c3e843895a92b70aa74d1b7ebc9c982ccf2ec4968cc0cd55f12af4660c", "72",
             "92a009a9f0d4cab8720e820b5f642540a2b27b5416503f8fb3762223ebdb69da085ac1e43e15996e458f3613d0f11d8c387b2eaeb4302aeeb00d291612bb0c00"),
            ("fc51cd8e6218a1a38da47ed00230f0580816ed13ba3303ac5deb911548908025", "af82",
             "6291d657deec24024827e69c3abe01a30ce548a284743a445e3680d7db5ac3ac18ff9b538d16f290ae67f760984dc6594a7c15e9716ed28dc027beceea1ec40a"),
        ]
        for pk, msg, sig in vectors:
            pk, msg, sig = bytes.fromhex(pk), bytes.fromhex(msg), bytes.fromhex(sig)
            self.assertTrue(um.ed25519_verify(sig, msg, pk), msg.hex())
            self.assertFalse(um.ed25519_verify(sig, msg + b"x", pk), "another message")
            self.assertFalse(um.ed25519_verify(sig[:63] + bytes([sig[63] ^ 1]), msg, pk), "a flipped S bit")
            self.assertFalse(um.ed25519_verify(bytes([sig[0] ^ 1]) + sig[1:], msg, pk), "a flipped R bit")
            self.assertFalse(um.ed25519_verify(sig, msg, bytes([pk[0] ^ 1]) + pk[1:]), "another key")

    def test_garbled_signatures_and_keys_fail(self):
        for bad in ["", "not base64!", b64(b"one line"), text("minisign-payload.txt.sig")[:-8]]:
            with self.assertRaises(um.Refused, msg=bad):
                self.check(bad)
        with self.assertRaises(um.Refused):
            um.parse_public_key("not a key")
        with self.assertRaises(um.Refused):
            um.parse_public_key(b64(b"untrusted comment: rsign encrypted secret key\nRWRT\n"))

    def test_the_committed_public_key_is_a_minisign_public_key(self):
        with open(os.path.join(HERE, "..", "updater.pub"), encoding="ascii") as f:
            key_id, key = um.parse_public_key(f.read())
        self.assertEqual((len(key_id), len(key)), (8, 32))

    def test_ed25519_rejects_what_is_not_a_signature(self):
        key = um.parse_public_key(text("minisign-test-a.pub"))[1]
        self.assertFalse(um.ed25519_verify(b"\x00" * 64, b"m", key))
        self.assertFalse(um.ed25519_verify(b"\xff" * 64, b"m", key))
        self.assertFalse(um.ed25519_verify(b"short", b"m", key))
        self.assertFalse(um.ed25519_verify(b"\x00" * 64, b"m", b"short"))

    def release_dir(self, d, sig_name="minisign-payload.txt.sig"):
        """A Release folder: the fixture bytes as every package, the given signature as every .sig."""
        assets, sigdir, pkgdir = set(), f"{d}/sigs", f"{d}/pkgs"
        os.mkdir(sigdir)
        os.mkdir(pkgdir)
        for package in um.legs(TAG).values():
            with open(f"{pkgdir}/{package}", "wb") as f:
                f.write(data("minisign-payload.txt"))
            with open(f"{sigdir}/{package}.sig", "w") as f:
                f.write(text(sig_name))
            assets |= {package, package + ".sig"}
        lst = f"{d}/assets.txt"
        with open(lst, "w") as f:
            f.write("\n".join(sorted(assets)) + "\n")
        return lst, sigdir, pkgdir

    def manifest(self, d, lst, sigdir, pkgdir, pub):
        pubfile = f"{d}/pub"
        with open(pubfile, "w") as f:
            f.write(text(pub))
        return um.main(["manifest", "--tag", TAG, "--repo", REPO, "--assets", lst, "--sigs", sigdir, "--out", f"{d}/latest.json",
                        "--verify-dir", pkgdir, "--pubkey", pubfile, "--now", NOW])

    def test_the_manifest_is_written_only_for_signatures_that_verify(self):
        with tempfile.TemporaryDirectory() as d:
            lst, sigdir, pkgdir = self.release_dir(d)
            self.assertEqual(self.manifest(d, lst, sigdir, pkgdir, "minisign-test-a.pub"), 0)
            self.assertTrue(os.path.exists(f"{d}/latest.json"))
        # the secret does not belong to the public key (or the variable names another key)
        with tempfile.TemporaryDirectory() as d:
            lst, sigdir, pkgdir = self.release_dir(d, "minisign-payload.other-key.sig")
            self.assertEqual(self.manifest(d, lst, sigdir, pkgdir, "minisign-test-a.pub"), 1)
            self.assertFalse(os.path.exists(f"{d}/latest.json"))
        # one package changed after signing
        with tempfile.TemporaryDirectory() as d:
            lst, sigdir, pkgdir = self.release_dir(d)
            with open(f"{pkgdir}/{um.legs(TAG)['linux-x86_64-deb']}", "ab") as f:
                f.write(b"x")
            self.assertEqual(self.manifest(d, lst, sigdir, pkgdir, "minisign-test-a.pub"), 1)
            self.assertFalse(os.path.exists(f"{d}/latest.json"))
        # a package that is not in the folder cannot be verified
        with tempfile.TemporaryDirectory() as d:
            lst, sigdir, pkgdir = self.release_dir(d)
            os.remove(f"{pkgdir}/{um.legs(TAG)['darwin-aarch64-app']}")
            self.assertEqual(self.manifest(d, lst, sigdir, pkgdir, "minisign-test-a.pub"), 1)

    def test_verify_dir_and_pubkey_go_together(self):
        with tempfile.TemporaryDirectory() as d:
            lst, sigdir, pkgdir = self.release_dir(d)
            self.assertEqual(um.main(["manifest", "--tag", TAG, "--repo", REPO, "--assets", lst, "--sigs", sigdir, "--out", f"{d}/l.json", "--verify-dir", pkgdir]), 1)


class Pubkey(unittest.TestCase):
    PRODUCTION = "someone/scriobh"

    def run_pubkey(self, repo, var):
        with tempfile.TemporaryDirectory() as d:
            out = f"{d}/chosen.pub"
            rc = um.main(["pubkey", "--file", os.path.join(DATA, "minisign-test-a.pub"), "--repo", repo, "--production", self.PRODUCTION,
                          "--var", var, "--out", out])
            if rc != 0:
                return rc, None
            with open(out) as f:
                return rc, f.read().strip()

    def test_the_committed_key_is_used_unless_a_staging_repository_says_otherwise(self):
        a, b = text("minisign-test-a.pub").strip(), text("minisign-test-b.pub").strip()
        self.assertEqual(self.run_pubkey(self.PRODUCTION, ""), (0, a))
        self.assertEqual(self.run_pubkey("someone/staging", ""), (0, a))
        self.assertEqual(self.run_pubkey("someone/staging", b), (0, b))
        self.assertEqual(self.run_pubkey(self.PRODUCTION, a), (0, a), "the same key is harmless")

    def test_a_variable_left_set_in_the_production_repository_is_refused(self):
        b = text("minisign-test-b.pub").strip()
        self.assertEqual(self.run_pubkey(self.PRODUCTION, b), (1, None))
        # GitHub names are case-insensitive, and github.repository may be spelt either way
        self.assertEqual(self.run_pubkey(self.PRODUCTION.upper(), b), (1, None))
        self.assertEqual(self.run_pubkey("Someone/Scriobh", b), (1, None))
        self.assertEqual(self.run_pubkey("someone/staging", "not a key")[0], 1)


class Config(unittest.TestCase):
    PUB = b64(b"untrusted comment: minisign public key: 1D64395039D2808E\nRWSOgNI5UDlkHTtqlG2waqDrrftFNfakE0uVXsmbxXBWuwYztsTAxgIV\n")

    def write(self, *extra, pub=None):
        with tempfile.TemporaryDirectory() as d:
            key = f"{d}/k.pub"
            with open(key, "w") as f:
                f.write(pub or self.PUB)
            out = f"{d}/c.json"
            rc = um.main(["config", "--out", out, "--pubkey", key, *extra])
            if rc == 0 and os.path.exists(out):
                with open(out) as f:
                    return rc, json.load(f)
            return rc, None

    def test_a_version_only_configuration(self):
        rc, conf = self.write("--tag", "v2.3.0-rc.1")
        self.assertEqual((rc, conf), (0, {"version": "2.3.0-rc.1"}))

    def test_an_updater_configuration_names_one_https_source_and_requires_a_signed_version(self):
        rc, conf = self.write("--tag", TAG, "--updater", "--repo", REPO)
        self.assertEqual(rc, 0)
        self.assertEqual(conf["version"], "2.3.0")
        self.assertEqual(conf["bundle"], {"createUpdaterArtifacts": True})
        up = conf["plugins"]["updater"]
        self.assertEqual(up["endpoints"], [f"https://github.com/{REPO}/releases/latest/download/latest.json"])
        self.assertEqual(up["pubkey"], self.PUB)
        self.assertIs(up["requireSignedVersion"], True)
        self.assertEqual(set(up), {"endpoints", "pubkey", "requireSignedVersion"})

    def test_the_universal_leg_bakes_the_source_in_and_asks_for_no_package(self):
        rc, conf = self.write("--tag", TAG, "--updater", "--no-packages", "--repo", REPO)
        self.assertEqual(rc, 0)
        self.assertEqual(set(conf), {"version", "plugins"}, "no bundle.createUpdaterArtifacts")
        up = conf["plugins"]["updater"]
        self.assertEqual(up["endpoints"], [f"https://github.com/{REPO}/releases/latest/download/latest.json"])
        self.assertEqual(up["pubkey"], self.PUB)
        self.assertIs(up["requireSignedVersion"], True)
        self.assertEqual(self.write("--tag", TAG, "--no-packages")[0], 1, "--no-packages goes with --updater")

    def test_a_private_key_or_a_bad_tag_or_repository_is_refused(self):
        private = b64(b"untrusted comment: rsign encrypted secret key\nRWRTY0Iy...\n")
        self.assertEqual(self.write("--tag", TAG, "--updater", "--repo", REPO, pub=private)[0], 1)
        self.assertEqual(self.write("--tag", TAG, "--updater", "--repo", REPO, pub="not base64!")[0], 1)
        self.assertEqual(self.write("--tag", "v2.3.0+x", "--updater", "--repo", REPO)[0], 1)
        self.assertEqual(self.write("--tag", "v2", "--repo", REPO)[0], 1)
        self.assertEqual(self.write("--tag", TAG, "--updater", "--repo", "nonsense")[0], 1)
        self.assertEqual(self.write("--tag", TAG, "--updater")[0], 1)


if __name__ == "__main__":
    unittest.main(verbosity=1)
