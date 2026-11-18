# Releasing the desktop app

For the maintainer. How to make the updater key, build signed packages for macOS, Windows and Linux, what the build pipeline needs, and how to publish an update. **None of the signing, notarising or publishing steps below has been run**: they were written from the tools' documentation on a machine with no signing identity. What was run is said where it applies. Check each step the first time it is used and correct this file.

The app is two programs in one package: the shell (`desktop/src-tauri`, Rust) and the server (`./cmd/scriobh`, Go, the same binary a server install runs, shipped inside the app as `scriobh-desktop-server` so that it cannot collide with a server package's `/usr/bin/scriobh`). Both must be built for the target, and both must be signed.

## Once: the updater key

The updater installs a download only if its minisign signature matches the public key inside the app. Lose the private key and no installed copy can ever be updated; leak it and anyone who can serve a file to your users can replace the app.

```
cd desktop && npm ci
node node_modules/@tauri-apps/cli/tauri.js signer generate -w ~/secure/scriobh-updater.key
```

It asks for a password and writes `scriobh-updater.key` (private) and `scriobh-updater.key.pub` (public). *(Run here with a throwaway key: the two files are written as described.)*

- Keep the private key and its password out of the repository and off the build machine's disk: in a password manager, and in the pipeline's secret store. `desktop/.gitignore` refuses `*.key`, `*.key.pub`, `*.pem`, `*.p12`, `*.pfx`, `*.p8`.
- **The key exists** (made on 17 November 2026): the public half is committed as `desktop/updater.pub`; the private half and its password are repository Actions secrets on the published repository, `TAURI_SIGNING_PRIVATE_KEY` and `TAURI_SIGNING_PRIVATE_KEY_PASSWORD` (not in an Environment: one restricted to the `v*` tags and `main`, with the owner as required reviewer, can be added later, `docs/UPDATES.md`, decision 5). Keep two copies the owner trusts (a password manager and one offline). **No file is committed that names an update address or holds a key**: the release workflow writes the build configuration (`desktop/scripts/updater-manifest.py config`: the tag's version, the update packages, the address `https://github.com/<repository>/releases/latest/download/latest.json`, the public key) into the runner's temporary folder, and the build scripts take its path in `TAURI_BUILD_CONFIG`. `tauri.release.example.json` is gone.
- `requireSignedVersion` stays `true`: each signature then names the version it was made for, and an old release cannot be served as a new one.

## Secrets the pipeline uses

the project's **GitHub release workflow** (`scripts/publish/release.yml`) makes signed update packages only when the two update-key secrets exist; without them (a fork, a run that builds the files only) it builds packages with no update artefacts and the app has no update source. The development forge's workflows are given **no** update key. The Apple and Windows rows are for the signing steps below, none of which is set up.

| Secret | For | What it is |
| --- | --- | --- |
| `TAURI_SIGNING_PRIVATE_KEY` | all | The contents of the updater private key file |
| `TAURI_SIGNING_PRIVATE_KEY_PASSWORD` | all | Its password |
| `APPLE_CERTIFICATE` | macOS | The Developer ID Application certificate and key as a base64 `.p12` |
| `APPLE_CERTIFICATE_PASSWORD` | macOS | The `.p12` password |
| `APPLE_SIGNING_IDENTITY` | macOS | `Developer ID Application: <name> (<team id>)` |
| `APPLE_API_ISSUER`, `APPLE_API_KEY`, `APPLE_API_KEY_P8` | macOS | An App Store Connect API key for notarising: the issuer id, the key id and the contents of `AuthKey_<id>.p8` |
| `WINDOWS_CERTIFICATE_THUMBPRINT` | Windows | The thumbprint of the code-signing certificate in the runner's store (or use a signing service; below) |

Set them in your CI host under the repository's Settings, Actions, Secrets. They are available to dispatched runs only; the development repository's `desktop.yml` never runs on a push or a pull request.

## Version

The app's version is in three places that must agree: `desktop/src-tauri/tauri.conf.json` (`version`), `desktop/src-tauri/Cargo.toml` and `desktop/package.json`. The server inside reports its own version from `git describe`. Set all three, commit, tag.

## macOS

Needs a Mac with full Xcode (not only the command-line tools), a Developer ID Application certificate in the keychain, and an App Store Connect API key.

```
rustup target add aarch64-apple-darwin x86_64-apple-darwin
desktop/scripts/sidecar.sh aarch64-apple-darwin
desktop/scripts/sidecar.sh x86_64-apple-darwin
cd desktop && npm ci
export APPLE_SIGNING_IDENTITY="Developer ID Application: <name> (<team id>)"
export APPLE_API_ISSUER=<issuer id> APPLE_API_KEY=<key id> APPLE_API_KEY_PATH=~/secure/AuthKey_<key id>.p8
export TAURI_SIGNING_PRIVATE_KEY="$(cat ~/secure/scriobh-updater.key)" TAURI_SIGNING_PRIVATE_KEY_PASSWORD=<password>
# the workflow's file; by hand: python3 desktop/scripts/updater-manifest.py config --tag vX.Y.Z --updater --repo <owner>/<repo> --pubkey desktop/updater.pub --out /tmp/build.json
export TAURI_BUILD_CONFIG=/tmp/build.json   # or use desktop/scripts/build-macos.sh, which takes it
node node_modules/@tauri-apps/cli/tauri.js build --target aarch64-apple-darwin --bundles app,dmg --config "$TAURI_BUILD_CONFIG"
node node_modules/@tauri-apps/cli/tauri.js build --target x86_64-apple-darwin  --bundles app,dmg --config "$TAURI_BUILD_CONFIG"
```

With those variables set the bundler signs the shell and the server inside it with the hardened runtime, has the result notarised with `notarytool` and staples the ticket. It writes `Scriobh.app`, a `.dmg`, and for the updater `Scriobh.app.tar.gz` with `Scriobh.app.tar.gz.sig`. *(Run here unsigned, debug: the `.app`, the `.tar.gz` and the `.sig` are made; the URL scheme and both document types are in `Info.plist`.)*

Check before publishing:

```
codesign --verify --deep --strict --verbose=2 Scriobh.app
codesign -dv --verbose=4 Scriobh.app/Contents/MacOS/scriobh-desktop-server        # the server must be signed too
spctl --assess --type execute -vv Scriobh.app                      # "accepted, source=Notarized Developer ID"
xcrun stapler validate Scriobh.dmg
```

Then, on a Mac that has never seen the app: open the `.dmg`, drag the app out, open it; write something; quit; open it again. The first time the app reads its keychain entry after an update macOS may ask for the login password if the signing identity changed; it should not ask between two releases signed by the same identity.

The server needs no entitlement beyond the hardened runtime: it is plain Go with no JIT and no library loading. If notarisation objects to the server binary, sign it yourself before the build (`codesign --force --options runtime --timestamp -s "$APPLE_SIGNING_IDENTITY" desktop/src-tauri/binaries/scriobh-desktop-server-<triple>`).

## Windows

Needs Windows 10 or 11 with the MSVC build tools, and a code-signing certificate. An OV certificate works but SmartScreen warns until the file has reputation; an EV certificate does not have that wait and lives on a hardware token or a cloud signing service, which is why signing is a command you configure, not a file the pipeline holds.

```
desktop/scripts/sidecar.sh x86_64-pc-windows-msvc
cd desktop && npm ci
node node_modules/@tauri-apps/cli/tauri.js build --bundles nsis --config "$TAURI_BUILD_CONFIG"
```

To sign, add to a configuration file merged with `--config` (the workflow's, or your own) under `bundle.windows` either the certificate in the machine's store:

```
"certificateThumbprint": "<thumbprint>", "digestAlgorithm": "sha256", "timestampUrl": "http://timestamp.digicert.com"
```

or, for a token or a signing service, the command to run on each file (`%1` is the file):

```
"signCommand": "signtool sign /fd sha256 /tr http://timestamp.digicert.com /td sha256 /sha1 <thumbprint> %1"
```

The bundler signs the shell, the server and the installer. Check with `signtool verify /pa /v <file>` on the installer and on `scriobh-desktop-server.exe` inside the install folder.

**Without a Windows machine.** `desktop/scripts/build-windows-cross.sh` makes the same installer on Linux or macOS (the `windows-cross` job of `desktop.yml`; how it works, what it downloads under Microsoft's licence and what is unproven are in [DESKTOP.md](../DESKTOP.md#the-windows-build-cross-compiled-never-run)). It is unsigned. Tauri signs on Windows hosts only, unless `bundle.windows.signCommand` names a tool that runs on the build host (for example `osslsigncode` with a key the host holds, or a cloud signing service's command); a `certificateThumbprint` cannot be used there. No signing command has been tried.

Two things to know. The installer does not download WebView2 (`webviewInstallMode` is `skip`, so installing makes no network request): Windows 11 has it; on a Windows 10 machine without it the app will not start, and the release notes say so (`.forgejo/release-body.md`, with what SmartScreen shows). And until the server exits on end of input (see the proposal in [desktop-contract.md](desktop-contract.md)), quitting the app on Windows takes ten seconds and ends the server by force.

## Linux

Needs the packages the Linux job installs (`libwebkit2gtk-4.1-dev libgtk-3-dev libayatana-appindicator3-dev librsvg2-dev build-essential curl file libssl-dev libxdo-dev libdbus-1-dev pkg-config`).

```
desktop/scripts/sidecar.sh x86_64-unknown-linux-gnu
cd desktop && npm ci
node node_modules/@tauri-apps/cli/tauri.js build --bundles deb,appimage --config "$TAURI_BUILD_CONFIG"
```

The `.deb` is for Debian and Ubuntu; the AppImage is the format the updater can replace in place, and gets a `.sig` from the updater key. The AppImage step downloads its packaging tools the first time (three of them; the bundler checks none of them and one is a moving `continuous` build), so it needs the network. It runs linuxdeploy with `APPIMAGE_EXTRACT_AND_RUN=1`, so FUSE is not needed. The release workflows build an AppImage after the `.deb` with those tools fetched first and checked against pinned SHA-256 values (`desktop/scripts/linux-deb.sh`, step `appimage`); that step has never been run, and a failure of it does not stop the release. There is no operating-system signature on Linux: publish the SHA-256 of each file, and if you keep a package repository, sign that with its own key. The app needs a Secret Service (GNOME Keyring, KWallet) for the keychain; without one it says so once and uses the key file.

**Arch Linux.** On the development forge the release also carries a pacman package, made from the `.deb` (`desktop/arch/PKGBUILD`, `desktop/scripts/arch-pkg.sh`; the jobs are described in OPERATIONS, "On the development forge"). A staged AUR recipe (`desktop/arch/aur/`), over the public release's `.deb`, is not published. Each release: the AUR files' `pkgver` and `_tag` follow `tauri.conf.json` and the tag (`internal/repocheck` checks `pkgver`, and the `.SRCINFO` against the `PKGBUILD`); after the public release exists, `updpkgsums`, `makepkg --printsrcinfo > .SRCINFO`, and push to the AUR with the owner's own account. What the Arch user sees, and the Hyprland and NVIDIA variables, are in [DESKTOP.md, "Arch Linux and Omarchy"](../DESKTOP.md#arch-linux-and-omarchy).

## Publishing an update

Pushing a `v*` tag to the published repository is the whole of it. The release workflow builds every package with the tag's version stamped in, signs the update packages with the key in the repository secrets, makes the Release as a **draft**, uploads everything, writes `latest.json` from what the Release holds (`desktop/scripts/updater-manifest.py manifest`, which refuses anything it cannot vouch for), uploads it **last**, and publishes. `latest.json` names only legs that built and signed, every address pinned to the tag; a prerelease (a hyphen in the tag) gets none and is never offered. Installed apps that have said yes read `https://github.com/<repository>/releases/latest/download/latest.json` about once a day.

- **The bridge release.** The first tag that has the key. Set `BRIDGE_TAG` in `release.yml` to it before tagging: its notes tell people on 2.2.x or earlier to install it by hand, once.
- **To withdraw a release:** mark it a draft or delete it on GitHub, or upload the previous `latest.json` to the latest release with `gh release upload <tag> latest.json --clobber`: installed copies do not downgrade.
- **No staging round trip.** The owner decided against a staging repository (UPDATES.md decision 12), so v2.3.0, the bridge release, is the first real run, and the update to the release after it is the first real update. Check it on each system then.
- **A release with a migration** says so in its notes, with where the pre-migration copy of the database is (`data/backups/migrate/`): there is no automatic way back (`docs/OPERATIONS.md`, upgrade and rollback).

## If the key is lost or leaked

- **Lost:** installed copies can no longer update. Make a new key, build a release with the new public key, and tell people to download it by hand once.
- **Leaked:** the same, at once, and take the release's `latest.json` down until the new release is up: the old key can sign anything. (A leaked key alone cannot push a bad update to everyone: the manifest also has to come from the project's GitHub releases over TLS.)
- **Rotation, planned:** an installed app trusts exactly one public key, so rotation is a *bridge release*: release N is signed with the old key and built with the new public key (`desktop/updater.pub` changed in that commit, the secrets changed after N is built and before N+1); installs that update to N then trust the new key, and N+1 is signed with the new one. An install that has not reached N cannot verify N+1, so N stays the release `latest.json` names for weeks, and the rest reinstall by hand. A leaked key cannot be rotated this way, because the bridge would be signed by the leaked key.

