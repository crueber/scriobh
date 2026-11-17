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
- Copy `desktop/src-tauri/tauri.release.example.json` to `desktop/src-tauri/tauri.release.conf.json`, put the contents of the `.pub` file in `pubkey` and your release address in `endpoints`. That file holds nothing secret (a public key and an address), **and the intent is that you commit it**: the pipeline looks for exactly `src-tauri/tauri.release.conf.json` and makes and signs update artefacts only when it is in the checkout and the signing secrets exist, so until someone commits it CI builds without update artefacts. `desktop/.gitignore` ignores `tauri.release.local.json` instead: that name is for a copy you keep on your own machine and do not commit (for instance one that also carries a Windows signing command naming your certificate). Until a release configuration is used the app is built with a placeholder key and Help, Check for Updates… says updates are not set up.
- `requireSignedVersion` stays `true`: each signature then names the version it was made for, and an old release cannot be served as a new one.

## Secrets the pipeline uses

the development repository's `desktop.yml` workflow signs only when these repository secrets exist. Without them it builds unsigned packages.

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
node node_modules/@tauri-apps/cli/tauri.js build --target aarch64-apple-darwin --bundles app,dmg --config src-tauri/tauri.release.conf.json
node node_modules/@tauri-apps/cli/tauri.js build --target x86_64-apple-darwin  --bundles app,dmg --config src-tauri/tauri.release.conf.json
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
node node_modules/@tauri-apps/cli/tauri.js build --bundles nsis --config src-tauri/tauri.release.conf.json
```

To sign, add to `tauri.release.conf.json` under `bundle.windows` either the certificate in the machine's store:

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
node node_modules/@tauri-apps/cli/tauri.js build --bundles deb,appimage --config src-tauri/tauri.release.conf.json
```

The `.deb` is for Debian and Ubuntu; the AppImage is the format the updater can replace in place, and gets a `.sig` from the updater key. The AppImage step downloads its packaging tools the first time (three of them; the bundler checks none of them and one is a moving `continuous` build), so it needs the network. It runs linuxdeploy with `APPIMAGE_EXTRACT_AND_RUN=1`, so FUSE is not needed. The release workflows build an AppImage after the `.deb` with those tools fetched first and checked against pinned SHA-256 values (`desktop/scripts/linux-deb.sh`, step `appimage`); that step has never been run, and a failure of it does not stop the release. There is no operating-system signature on Linux: publish the SHA-256 of each file, and if you keep a package repository, sign that with its own key. The app needs a Secret Service (GNOME Keyring, KWallet) for the keychain; without one it says so once and uses the key file.

**Arch Linux.** On the development forge the release also carries a pacman package, made from the `.deb` (`desktop/arch/PKGBUILD`, `desktop/scripts/arch-pkg.sh`; the jobs are described in OPERATIONS, "On the development forge"). A staged AUR recipe (`desktop/arch/aur/`), over the public release's `.deb`, is not published. Each release: the AUR files' `pkgver` and `_tag` follow `tauri.conf.json` and the tag (`internal/repocheck` checks `pkgver`, and the `.SRCINFO` against the `PKGBUILD`); after the public release exists, `updpkgsums`, `makepkg --printsrcinfo > .SRCINFO`, and push to the AUR with the owner's own account. What the Arch user sees, and the Hyprland and NVIDIA variables, are in [DESKTOP.md, "Arch Linux and Omarchy"](../DESKTOP.md#arch-linux-and-omarchy).

## Publishing an update

The app asks one address for one file. For each target put a `latest.json` where the `endpoints` pattern points (`{{target}}` is `darwin`, `linux` or `windows`; `{{arch}}` is `aarch64` or `x86_64`):

```
{
  "version": "2.0.1",
  "notes": "What changed, in a sentence or two.",
  "pub_date": "2027-01-15T12:00:00Z",
  "url": "https://<release host>/desktop/darwin-aarch64/Scriobh_2.0.1_aarch64.app.tar.gz",
  "signature": "<the contents of the .sig file>"
}
```

Serve it over HTTPS from a host you control, as static files; no service is needed and nothing is logged beyond what any web server logs. Upload the package first, then `latest.json`. To withdraw a release, put the previous `latest.json` back: installed copies do not downgrade.

Test an update before announcing it: install the previous release on a clean machine, point nothing else at the release host, choose Help, Check for Updates…, and watch that it asks before contacting the host, names the version, installs, restarts, and that the writing is there. **This round trip has not been done.**

## If the key is lost or leaked

- **Lost:** installed copies can no longer update. Make a new key, build a release with the new public key, and tell people to download it by hand once.
- **Leaked:** the same, at once, and take the release host's files down until the new release is up: the old key can sign anything.
