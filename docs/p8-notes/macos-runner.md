# macOS builds: a Mac as a Forgejo runner

For the owner. How to make a Mac take the `macos` jobs of `.forgejo/workflows/desktop.yml`, what those jobs do to it, and why the other two routes to a macOS package were not taken. The script the job runs is `desktop/scripts/build-macos.sh`, which also runs by hand on any Mac.

**What was run and what was not.** On the Mac this was written on (Apple silicon), `build-macos.sh` built, signed ad hoc, checked and packaged the app for `aarch64-apple-darwin`, `x86_64-apple-darwin` and `universal-apple-darwin` (see [DESKTOP.md](../DESKTOP.md#macos-building-and-gatekeeper)). The runner itself was built from the `v13.2.1` tag with `make build` on that Mac (it compiles, reports `forgejo-runner version v13.2.1`), and a configuration with the label `macos:host` and a connection block was accepted by `forgejo-runner daemon` (it got as far as connecting to an address that was not listening). **No runner was registered, no launchd service was loaded and no workflow job has run on a Mac runner.** Everything under "Steps" is from the documentation and those two checks; correct this file the first time it is used.

## What forgejo-runner needs on macOS

| Question | Answer | Source |
| --- | --- | --- |
| Is there a darwin binary? | No. The releases at `code.forgejo.org/forgejo/runner/releases` carry `linux-amd64` and `linux-arm64` binaries (plain, `.xz`, `.asc`, `.sha256`) and source archives only. The runner's README says it is "supported and tested on `amd64` and `arm64` Linux-based systems" and does not mention macOS. | [releases](https://code.forgejo.org/forgejo/runner/releases), [README](https://code.forgejo.org/forgejo/runner/src/branch/main/README.md) |
| How do I get one? | Build the release tag: Go and `make`, `git clone --branch v13.2.1 https://code.forgejo.org/forgejo/runner`, `make build`. It needs the Go version in the tag's `go.mod` (`go 1.26.0`, `toolchain go1.26.8` at `v13.2.1`; a newer Go is fine). Built here in under ten seconds with a warm module cache, a 21 MB arm64 executable. Or Homebrew: `brew install forgejo-runner` (formula version 13.2.1, bottles for Apple silicon macOS and for Linux; it builds from source otherwise and has `brew services`). | [a build write-up](https://angu-software.codeberg.page/devops/2026/03/01/forging-a-mac-os-compatible-forgejo-actions-runner.html), [Homebrew formula](https://formulae.brew.sh/formula/forgejo-runner) |
| Which version matches the forge? | The brief said "the forge's v13.2.1"; **I could not tell whether that is the server's version or the existing runner's.** What is known: runner `v13.2.1` (2026-10-29) is the newest tag on the runner's releases page, and its generated config and `--help` are what this note is written against. The documentation has no runner-to-server compatibility table (neither the v13.0 nor the v15.0 pages say which runner goes with which server), and the current server documentation is already at v17.0, so the runner's numbers do not obviously follow the server's. **Before installing, read the server version from the forge (Administration, Site information) and the version of the runner the forge already has (`forgejo-runner --version` on that machine), and use the same runner release as that existing runner**; v13.2.1 is the default here only because it is the tag that was built and tried. | [releases](https://code.forgejo.org/forgejo/runner/releases) |
| Does `host` mode exist? | Yes. A label of the form `<name>:host` runs the job's steps "in a shell forked from the runner, directly on the host". The runner's own example config lists `macos-arm64:host`. The documentation warns: "There is no isolation at all and a single job can permanently destroy the host." | [configuration, choosing labels](https://forgejo.org/docs/latest/admin/actions/configuration/#choosing-labels), generated `config.yml` of v13.2.1 |
| How is it registered? | Two flows, by the forge's version, and both work with runner `v13.2.1`. **Older servers (the v13.0 documentation):** the Runners page has a registration token (**Create new runner**); the runner runs `forgejo-runner register` and is told the instance URL, token, name and labels, and writes a `.runner` file. **Newer servers (the v15.0 documentation and later):** **Create new runner** asks for a name and description and then shows a UUID and a token, which go in the `server.connections` block of `config.yml`; no `register` is run. `forgejo-runner register` (flags `--instance --token --name --labels --no-interactive --ephemeral`) and `create-runner-file` are marked *deprecated* in v13.2.1's `--help`. An administrator can also pre-register from the server's shell (`forgejo forgejo-cli actions register --name N --scope S --secret <40 hex>`). | [v13.0 runner installation](https://forgejo.codeberg.page/docs/v13.0/admin/actions/runner-installation/), [v15.0 registration](https://forgejo.org/docs/v15.0/admin/actions/registration/), `forgejo-runner register --help` at v13.2.1 |
| As a launchd service? | Nothing official: the binary installation page documents only a systemd unit. The plist below is written for it and not run. | [binary installation](https://forgejo.org/docs/latest/admin/actions/installation/binary/) |

## Steps

Choose the Mac and the account first. **Host mode runs the workflow's code as the user the runner runs as, with no sandbox.** A workflow can read that user's files, keychain items it is allowed to see, SSH keys and `.runner` (the credential of the runner itself), and use the machine's network. Therefore:

- **Make a dedicated standard (non-admin) macOS account for the runner** and put nothing of yours in it: no personal keychain items, no SSH keys, no cloud credentials, no browser profile. Never run the runner as your own account.
- **Register it for this repository only** (the repository's own runners page, not the organisation's or the instance's), so that nobody else's workflow can be handed to it.
- Keep `capacity: 1`. Keep the forge's rule that only people you trust can push branches or edit workflows in this repository (anyone who can push a workflow file can run any command as that account), and do not let pull requests from forks run on this label.
- Do not put signing secrets in the repository's secrets until the runner is on a Mac you are content to have those secrets visible on: a job's secrets are in its process environment.

### 1. The account and the tools

```sh
# as an administrator; asks for the new account's password
sudo sysadminctl -addUser forgejo-runner -fullName "Forgejo runner" -password -
# the tools: Apple's command line tools (linker, codesign, lipo, hdiutil), Homebrew, then
xcode-select --install
brew install go node git make
```

Then as the runner account (`sudo -u forgejo-runner -i`): `curl --proto '=https' --tlsv1.2 -sSf https://sh.rustup.rs | sh -s -- -y --profile minimal` (the install the Linux job uses; this puts the Rust toolchains in that account's `~/.rustup` and `~/.cargo`; the job adds the two Apple targets itself). The jobs call `go`, `node`, `npm`, `git` and `rustup` from the `PATH` set in the plist below, and do not use `actions/setup-go` or `actions/setup-node`: those install into a tool-cache directory whose location in host mode was not checked. Node 22 and the Go version of `go.mod` or newer are what the jobs were written for.

### 2. The runner binary

```sh
# v13.2.1, or the tag chosen in the version row above
git clone --branch v13.2.1 --depth 1 https://code.forgejo.org/forgejo/runner /tmp/forgejo-runner-src
make -C /tmp/forgejo-runner-src build
sudo install -m 755 /tmp/forgejo-runner-src/forgejo-runner /usr/local/bin/forgejo-runner
forgejo-runner --version        # forgejo-runner version v13.2.1
rm -rf /tmp/forgejo-runner-src
```

(or `brew install forgejo-runner` for the Homebrew formula's version, 13.2.1 when this was written, and use `/opt/homebrew/bin/forgejo-runner` in the plist). When the forge is upgraded, build the matching tag again.

### 3. The registration token (you do this, in the browser)

Open the repository's **Settings, Actions, Runners** (`/<owner>/<repository>/settings/actions/runners`) and choose **Create new runner**. What the forge then shows decides the flow in step 4:

- **a registration token only** (older servers): copy it; you will give it to `forgejo-runner register` in step 4.
- **a UUID and a token** (newer servers, after you name the runner, for example `mac-m4`): copy both; they go into `config.yml` in step 4.

The token is shown once and is a secret. Nothing in this repository or in these notes holds one.

### 4. The configuration and registration

As the runner account: `mkdir -p ~/runner && cd ~/runner && forgejo-runner generate-config > config.yml`, then set these keys (everything else stays at the generated defaults) and `chmod 600 config.yml`:

```yaml
log:
  level: info
runner:
  file: .runner
  capacity: 1                # one job at a time; a build uses the whole machine
  timeout: 3h
  shutdown_timeout: 10m      # launchd's ExitTimeOut below is a little longer
  labels:
    - "macos:host"           # runs-on: macos in .forgejo/workflows/desktop.yml
host:
  workdir_parent: /Users/forgejo-runner/work   # job checkouts; the default is ~/.cache/act
```

Then, by flow:

**Flow with a UUID and a token (newer servers).** Add the connection to the same file (the token is now in it, hence the `chmod 600`):

```yaml
server:
  connections:
    forge:
      url: https://<your forge address>/
      uuid: <the UUID the forge showed>
      token: <the token the forge showed>
```

**Flow with a registration token (older servers).** Register once, from `~/runner`, with the config so the label is the one above:

```sh
cd ~/runner && forgejo-runner register --no-interactive -c config.yml \
  --instance https://<your forge address>/ --token <the registration token> \
  --name mac-m4 --labels "macos:host"
```

This writes `~/runner/.runner` (the runner's credential; keep it `600` and out of any job's reach; deleting it makes the runner register again as a new one). The daemon then needs no `server:` block.

One `macos` label serves both architectures: the Intel app is cross-built on an Apple-silicon Mac (Go and Rust both cross-compile; the script installs the Rust target). If you later add an Intel Mac, give its runner a second label such as `macos-x86_64:host` and split the job.

Try it in the foreground first: `cd ~/runner && forgejo-runner daemon -c config.yml`. The repository's runners page should show it as idle. Stop it with Ctrl-C.

### 5. launchd

A system daemon with `UserName` starts at boot as the runner account, with no one logged in, which is what a build machine wants. Save as `/Library/LaunchDaemons/local.forgejo-runner.plist`, owned by `root:wheel`, mode 644:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>              <string>local.forgejo-runner</string>
  <key>UserName</key>           <string>forgejo-runner</string>
  <key>GroupName</key>          <string>staff</string>
  <key>WorkingDirectory</key>   <string>/Users/forgejo-runner/runner</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/forgejo-runner</string>
    <string>daemon</string>
    <string>--config</string>
    <string>/Users/forgejo-runner/runner/config.yml</string>
  </array>
  <key>EnvironmentVariables</key>
  <dict>
    <key>HOME</key>             <string>/Users/forgejo-runner</string>
    <!-- launchd's own PATH is /usr/bin:/bin:/usr/sbin:/sbin; the jobs need go, node, git, rustup and cargo -->
    <key>PATH</key>             <string>/Users/forgejo-runner/.cargo/bin:/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
  </dict>
  <key>RunAtLoad</key>          <true/>
  <key>KeepAlive</key>          <true/>
  <key>ExitTimeOut</key>        <integer>660</integer>
  <key>StandardOutPath</key>    <string>/Users/forgejo-runner/runner/runner.log</string>
  <key>StandardErrorPath</key>  <string>/Users/forgejo-runner/runner/runner.log</string>
</dict>
</plist>
```

```sh
sudo plutil -lint /Library/LaunchDaemons/local.forgejo-runner.plist
sudo launchctl bootstrap system /Library/LaunchDaemons/local.forgejo-runner.plist
sudo launchctl print system/local.forgejo-runner | head      # state = running
tail -f /Users/forgejo-runner/runner/runner.log
# to stop and unload it:
sudo launchctl bootout system/local.forgejo-runner
```

A daemon has no window-server session, so the workflow's step that opens the built app (`launch-check.sh`) finds no logged-in desktop for that account and says so and skips; the build, signing, architecture, `.dmg` mount and signature checks in `build-macos.sh` do not need one. To have the launch check run in CI as well, run the runner as a LaunchAgent in `~/Library/LaunchAgents` instead (same keys without `UserName`, loaded with `launchctl bootstrap gui/$(id -u forgejo-runner) ...`), keep that account logged in at the console, and accept that a window opens on its screen during the check.

Keep the Mac awake while it works: `sudo pmset -a sleep 0` (or `caffeinate -s` in the plist's command), and keep the disk free: a release build of one architecture is about 1 GB of Cargo output beyond the Go and npm caches.

### 6. Check it

Dispatch the **desktop** workflow from the repository's Actions page with `target` set to `macos`. Until a runner with the label `macos` is connected the job sits queued; it does not fail, and **it will wait for a label nobody serves**. `concurrency.group: desktop` in that workflow also makes a second dispatch cancel the first.

## What a job may touch on the Mac

Everything below is inside the runner account; nothing needs `sudo`.

- Its working copy under `host.workdir_parent`, and a `dist/` folder inside it, which the job uploads as the run's artifact.
- `~/.rustup` and `~/.cargo` (the pinned toolchain and the two Apple targets; the job runs `rustup toolchain install` and `rustup target add`, which only add files), `~/go` and `~/Library/Caches/go-build` (Go modules and build cache), `~/.npm` (the npm cache for `npm ci`).
- Temporary files under `$TMPDIR`; the `.dmg` check mounts the image on a temporary mount point (not under `/Volumes`) with `-nobrowse` and detaches it.
- The network: the forge, `static.rust-lang.org`, `crates.io`, the Go module proxy and the npm registry (the Tauri command line is an npm package). (The app has no telemetry; the build machine does fetch its dependencies.)
- The login keychain only when signing with a Developer ID certificate: the Tauri bundler then imports the certificate into a temporary keychain. The unsigned and ad-hoc paths never touch a keychain.
- It does not need Finder or Accessibility permission: `build-macos.sh` sets `CI=true` so the `.dmg` tool skips the AppleScript that lays out the Finder window (without it the build waited for a permission prompt and timed out after minutes).

## Route B, not built: cross-compiling on Linux with the Apple SDK (osxcross)

A Linux runner would be a better fit for this forge (it has one), so it was considered and left out, for facts rather than taste:

1. **The licence.** The Xcode and Apple SDKs Agreement lets the Apple SDKs be installed and used on Apple-branded hardware. osxcross needs the macOS SDK taken out of Xcode and put on a Linux machine, which is a use the agreement does not give. (Read from the agreement's text as quoted in forum and mailing-list discussions of osxcross; this is not legal advice, and no Apple SDK was downloaded or installed here.)
2. **The packager.** The Tauri bundler's macOS path is not documented as something that runs off a Mac: its signing guide says signing "requires an Apple device", its CI example builds on macOS runners, and what `tauri build` shells out to for macOS bundles (`hdiutil` for the `.dmg`, `codesign`, `lipo`, `plutil`, and AppleScript for the window) exists only on macOS. [Tauri: macOS code signing](https://v2.tauri.app/distribute/sign/macos/), [Tauri: GitHub pipelines](https://v2.tauri.app/distribute/pipelines/github/).
3. **Replacements would be ours to build and prove.** A Linux build would need a `.dmg` writer, a signer and a notariser that are not Apple's (for example `rcodesign` from the `apple-codesign` project for signing and notarising, and a hand-made disk image), a way to produce the `.app` layout and `Info.plist` the bundler makes, and a macOS machine in any case to launch the result once. That is a second packaging pipeline for one platform whose output nobody on this forge could test.

So the macOS build stays on macOS: this Mac as a runner (route A), or GitHub's macOS runners for the published copy (route C: the macOS legs of the template `scripts/publish/release.yml`, which call the same `build-macos.sh`).

**Switching the release on.** The forge's `release.yml` has a `desktop-macos` job that builds the `.dmg` files for a tag on this runner and publishes them, but it is off: it runs only when the repository variable `MACOS_RUNNER` is `true` (Settings, Actions, Variables; the comparison ignores case, so `True` works too, and anything else is off), because a job that asks for the label `macos` while no runner has it waits for ever and leaves the release run unfinished. Register the runner (above), check that it is online with the label `macos` (Settings, Actions, Runners: Idle), then add the variable. Delete it to switch the job off again; a run with the variable unset skips the job at once (its `runs-on` falls back to `docker` so even a forge that hands the `if` to a runner does not wait for `macos`). The job runs `build-macos.sh all` and `universal` on the tag's tree (`SRC_DIR`) and ends with `.forgejo/release-publish.sh`; see OPERATIONS, "On the development forge".

**Do not leave the variable `true` while this Mac is offline.** The gate only keeps a missing runner out of the run; it cannot tell that a registered one is switched off. `runs-on` is worked out when the run is created, so with the variable `true` and no online runner labelled `macos` the job waits for ever: the run never finishes, and since the `release-<tag>` concurrency group does not cancel in progress, every later run for the same tag queues behind it. Deleting the variable afterwards does not help that run. Cancel it: Actions, open the run, Cancel; or `tea api -X POST /repos/{owner}/{repo}/actions/runs/<run id>/cancel` (in Forgejo 16.0.7's swagger, answers 204; `tea api /repos/{owner}/{repo}/actions/runs` lists the runs). Then fix the variable (delete it, or bring the Mac back online) and start the release again with a dispatch.

## Still unproven

- A runner registered on this forge (by either flow), the launchd unit, and a workflow job running to the end on it (including `release.yml`'s `desktop-macos`, and the `MACOS_RUNNER` gate: with the variable unset the job must show skipped, not waiting).
- That `actions/checkout` and `actions/upload-artifact@v3` work in `host` mode with the runner's `PATH` (they need `node` on it); the existing Linux job uses the same actions inside a container.
- Signing with a Developer ID certificate and notarising, from this runner or any other (no certificate exists; see [release-runbook.md](release-runbook.md)).
- A launch check on a runner account that has a desktop session.
