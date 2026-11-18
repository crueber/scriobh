#!/usr/bin/env bash
# release-steps-check.sh WORKFLOW.yml REPO_ROOT: (the source template or the exported file) runs the shell of the release job's steps, cut out of the workflow text,
# against a fake `gh` that keeps one Release in a folder, and checks what the update pipeline promises (docs/UPDATES.md):
#   a new Release is a draft until the end, latest.json is uploaded after every other file and before it is published,
#   a Release that is already public is never made a draft and is not edited, a prerelease gets no latest.json and is
#   published as one, and everything that must not be published leaves the Release a draft with no latest.json: no signed
#   package at all, a package with no signature, an empty signature, a signature by another key, a signature for other bytes,
#   and a repository variable for another key in the production repository. The signatures are real ones made by the Tauri
#   CLI with throwaway keys (desktop/scripts/testdata). The steps use no GNU extension, so this runs on a Mac as it does
#   in the workflow; it never skips. `make publish-check` calls it on the exported tree's workflow.
set -euo pipefail
wf="${1:?usage: release-steps-check.sh WORKFLOW.yml REPO_ROOT}"; root="${2:?usage: release-steps-check.sh WORKFLOW.yml REPO_ROOT}"
T="$(mktemp -d "${TMPDIR:-/tmp}/scriobh-release-steps.XXXXXX")"
trap 'rm -rf "$T"' EXIT
fail() { echo "release-steps-check: FAILED: $*" >&2; exit 1; }

mkdir -p "$T/bin"

# The workflow may be the source template (it names the production repository `{{OWNER}}/{{REPO}}`, which the publish fills in)
# or the exported file (a real name). Either way the steps below run with a production repository name; for the template it is
# made up here, so this works on both.
sed 's|{{OWNER}}|prodowner|g; s|{{REPO}}|prodrepo|g' "$wf" >"$T/workflow.yml"

# the steps' scripts
python3 -I - "$T/workflow.yml" "$T" <<'PY'
import re, sys, textwrap
t = open(sys.argv[1], encoding="utf-8").read()
rel = t.split("\n  release:\n")[1]
def step(name):
    i = rel.index("- name: " + name)
    j = rel.index("        run: |\n", i) + len("        run: |\n")
    m = re.search(r"\n\n      (#|- )", rel[j:])
    return textwrap.dedent(rel[j:j + m.start() if m else len(rel)]) + "\n"
for name, f in [("Create the Release if there is none, and attach the files", "create"), ("Checksums of the desktop packages on the Release", "sums"),
                ("Write latest.json, check it, and upload it last", "manifest"), ("Publish the Release", "publish")]:
    open("%s/%s.sh" % (sys.argv[2], f), "w").write(step(name))
PY

cat >"$T/bin/gh" <<'GH'
#!/bin/bash
# a fake gh: one Release in $STATE (exists, draft, assets/), and a log of what was asked
S="$STATE"; echo "gh $*" >>"$S/log"
a="$1 $2"; shift 2; tag="${1:-}"; shift || true
case "$a" in
  "release view")
    [ -e "$S/exists" ] || { echo "release not found" >&2; exit 1; }
    case "$*" in
      *"--json isDraft --jq"*) if [ -e "$S/draft" ]; then echo true; else echo false; fi ;;
      *"--json assets"*) ls "$S/assets" ;;
      *"--json isDraft,isPrerelease"*) echo "published" ;;
    esac ;;
  "release create") touch "$S/exists"; mkdir -p "$S/assets"; case "$*" in *--draft*) touch "$S/draft";; esac; cp body.md "$S/body.md" ;;
  "release upload") mkdir -p "$S/assets"; files=(); for x in "$@"; do case "$x" in --clobber) ;; *) files+=("$x");; esac; done; cp "${files[@]}" "$S/assets/" ;;
  "release download") dir=""; pat="*"; while [ $# -gt 0 ]; do case "$1" in --dir) dir="$2"; shift;; --pattern) pat="$2"; shift;; esac; shift; done
                       mkdir -p "$dir"; for f in "$S/assets/"$pat; do [ -f "$f" ] && cp "$f" "$dir/"; done; true ;;
  "release edit") case "$*" in *--draft=false*) rm -f "$S/draft";; esac; echo "$*" >>"$S/edits" ;;
  "release delete-asset") rm -f "$S/assets/$1" ;;
  "release list") [ -f "$S/others" ] && cat "$S/others"; true ;;
  *) echo "fake gh: unexpected: $a $tag $*" >&2; exit 9 ;;
esac
GH
chmod +x "$T/bin/gh"

D="$root/desktop/scripts/testdata"
prod="$(python3 -I -c 'import re,sys; m = re.search(r"--production \"([^\"]+)\"", open(sys.argv[1]).read()); print(m.group(1))' "$T/workflow.yml")"
[ -n "$prod" ] || fail "the workflow does not name the production repository"
# A staging repository's name is made from the production one, and cannot be it in any spelling.
staging="staging-${prod%%/*}/${prod##*/}-staging"
[ "$(printf %s "$staging" | tr A-Z a-z)" != "$(printf %s "$prod" | tr A-Z a-z)" ] || fail "the staging name collides with the production name"

run() { # run NAME TAG BRIDGE_TAG [already-published] ; leaves the folder $T/NAME
  local name="$1" tag="$2" bridge="$3" published="${4:-}" w="$T/$1" v="${2#v}" step repo="$staging" var=""
  mkdir -p "$w/tools/scripts" "$w/tools/desktop/scripts" "$w/dist" "$w/desktop" "$w/state"
  cp "$root/scripts/release-sums.sh" "$w/tools/scripts/"; cp "$root/desktop/scripts/updater-manifest.py" "$w/tools/desktop/scripts/"
  cp "$D/minisign-test-a.pub" "$w/tools/desktop/updater.pub"   # the committed key of this run is test key A
  echo archive >"$w/dist/scriobh-$tag-linux-amd64.tar.gz"; echo dmg >"$w/desktop/Scriobh_${v}_aarch64.dmg"
  local pkg="$w/desktop/Scriobh_${v}_aarch64.app.tar.gz" sig="$D/minisign-payload.txt.sig"
  case "$name" in
    nosig|pre) ;;                                                           # no signed package at all
    badsig)     cp "$D/minisign-payload.txt" "$pkg"; : >"$pkg.sig" ;;       # an empty signature
    wrongkey)   cp "$D/minisign-payload.txt" "$pkg"; cp "$D/minisign-payload.other-key.sig" "$pkg.sig" ;;
    stale)      { cat "$D/minisign-payload.txt"; echo "another build"; } >"$pkg"; cp "$sig" "$pkg.sig" ;;
    missingsig) cp "$D/minisign-payload.txt" "$pkg" ;;                      # a package with no .sig
    prodvar)    cp "$D/minisign-payload.txt" "$pkg"; cp "$D/minisign-payload.other-key.sig" "$pkg.sig"; repo="$prod"; var="$(cat "$D/minisign-test-b.pub")" ;;
    prodcase)   cp "$D/minisign-payload.txt" "$pkg"; cp "$D/minisign-payload.other-key.sig" "$pkg.sig"; repo="$(printf %s "$prod" | tr a-z A-Z)"; var="$(cat "$D/minisign-test-b.pub")" ;;
    stagingvar) cp "$D/minisign-payload.txt" "$pkg"; cp "$D/minisign-payload.other-key.sig" "$pkg.sig"; var="$(cat "$D/minisign-test-b.pub")" ;;
    *)          cp "$D/minisign-payload.txt" "$pkg"; cp "$sig" "$pkg.sig" ;;
  esac
  if [ "$name" = older ]; then printf '%s\n' v10.0.0 v9.0.0 >"$w/state/others"; fi   # the highest full release already published is not this tag
  if [ -n "$published" ]; then touch "$w/state/exists"; mkdir -p "$w/state/assets"; echo old >"$w/state/assets/latest.json"; fi
  : >"$w/state/log"; : >"$w/env"
  ( cd "$w"
    export PATH="$T/bin:$PATH" STATE="$w/state" GITHUB_ENV="$w/env" TAG="$tag" BRIDGE_TAG="$bridge" GITHUB_REPOSITORY="$repo" GITHUB_SERVER_URL=https://github.com GH_REPO="$repo" STAGING_PUBKEY="$var"
    for step in create sums manifest publish; do
      if [ "$step" = publish ]; then set -a; . "$w/env"; set +a; [ "${PUBLISH_AFTER:-}" = true ] || continue; fi
      bash -e "$T/$step.sh" >"$w/$step.out" 2>&1 || { echo "step $step failed" >"$w/failed"; break; }
      if [ "$step" = create ]; then set -a; . "$w/env"; set +a; fi
    done ) || true
}
calls() { grep -v '^gh release \(view\|download\)' "$T/$1/state/log" | sed 's/ --clobber//' | cut -c1-100; }
draft() { [ -e "$T/$1/state/draft" ] && echo draft || echo public; }

# 1. a new full release: a draft, every file, the sums, latest.json, and only then public (and the latest release)
run new v2.3.0 v0.0.0
[ ! -e "$T/new/failed" ] || { cat "$T/new"/*.out >&2; fail "a new release failed a step"; }
n="$(calls new | grep -n . )"
[ "$(draft new)" = public ] || fail "a new release was left a draft"
l_json="$(calls new | grep -n 'upload v2.3.0 latest.json' | cut -d: -f1)"; l_edit="$(calls new | grep -n 'release edit' | cut -d: -f1)"
l_files="$(calls new | grep -n 'upload v2.3.0 desktop/' | cut -d: -f1)"; l_create="$(calls new | grep -n 'release create' | cut -d: -f1)"
[ -n "$l_json" ] && [ "$l_create" -lt "$l_files" ] && [ "$l_files" -lt "$l_json" ] && [ "$l_json" -lt "$l_edit" ] || fail "the order is not create, files, latest.json, publish: $n"
calls new | grep -q 'release create v2.3.0 --title v2.3.0 --notes-file body.md --verify-tag --draft$' || fail "a new release is not created as a draft"
[ "$(cat "$T/new/state/edits")" = "--draft=false --latest" ] || fail "the publish call is: $(cat "$T/new/state/edits")"
grep -q '"darwin-aarch64-app"' "$T/new/latest.json" && grep -q 'releases/download/v2.3.0/Scriobh_2.3.0_aarch64.app.tar.gz' "$T/new/latest.json" || fail "latest.json does not name the package at its tag"
if grep -q 'latest.json' "$T/new/state/assets/SHA256SUMS-desktop.txt"; then fail "latest.json is in the checksum list"; fi
if grep -q '@BRIDGE@\|@TAG@\|@NOTES@' "$T/new/state/body.md" || grep -q 'install this release by hand' "$T/new/state/body.md"; then fail "the notes of an ordinary release have a placeholder or the bridge paragraph"; fi
# 2. the bridge release's notes tell people on older versions to install it by hand, once
run bridge v2.3.0 v2.3.0
grep -q 'install this release by hand, once' "$T/bridge/state/body.md" && ! grep -q '@BRIDGE@' "$T/bridge/state/body.md" || fail "the bridge release's notes do not carry the by-hand paragraph"
# 3. a Release that is already public stays public, is not edited, and has its latest.json rewritten
run published v2.3.0 v0.0.0 yes
[ "$(draft published)" = public ] && [ ! -e "$T/published/state/edits" ] || fail "a published release was made a draft or edited again"
[ "$(cat "$T/published/state/assets/latest.json")" != old ] || fail "a re-run did not rewrite latest.json"
# 3b. a run for an old tag (a dispatch) does not make it the latest release
run older v2.3.0 v0.0.0
[ "$(cat "$T/older/state/edits")" = "--draft=false --latest=false" ] && [ "$(draft older)" = public ] || fail "an old tag was made the latest release: $(cat "$T/older/state/edits")"
# 4. a prerelease: no latest.json, published as a prerelease
run pre v2.3.0-rc.1 v0.0.0
[ ! -e "$T/pre/state/assets/latest.json" ] && [ "$(cat "$T/pre/state/edits")" = "--draft=false --prerelease" ] || fail "a prerelease got a latest.json or was not published as one"
# 5. everything that must not be published leaves the Release a draft with no latest.json, and says why
refused() { # refused NAME WHY [text the log must carry]
  [ -e "$T/$1/failed" ] && [ "$(draft "$1")" = draft ] && [ ! -e "$T/$1/state/assets/latest.json" ] && [ ! -e "$T/$1/state/edits" ] \
    || fail "$2: the release was not left a draft without a latest.json"
  [ -z "${3:-}" ] || grep -qi -- "$3" "$T/$1/manifest.out" || { cat "$T/$1/manifest.out" >&2; fail "$2: the refusal does not say '$3'"; }
}
run nosig v2.3.0 v0.0.0;     refused nosig "a full release with no signed package (it would strand every installed app)" "holds no signed update package"
run badsig v2.3.0 v0.0.0;    refused badsig "an empty signature" "is empty"
run missingsig v2.3.0 v0.0.0; refused missingsig "a package with no signature" "not publishing"
run wrongkey v2.3.0 v0.0.0;  refused wrongkey "a signature by another key than the public key (a secret that does not match)" "another key"
run stale v2.3.0 v0.0.0;     refused stale "a stale signature for other bytes" "does not match"
run prodvar v2.3.0 v0.0.0;   refused prodvar "UPDATER_PUBKEY set in the production repository" "UPDATER_PUBKEY"
run prodcase v2.3.0 v0.0.0;  refused prodcase "UPDATER_PUBKEY set in the production repository, spelt in capitals" "UPDATER_PUBKEY"
# 6. a staging repository's own key, by variable, is used end to end
run stagingvar v2.3.0 v0.0.0
[ ! -e "$T/stagingvar/failed" ] && [ -f "$T/stagingvar/state/assets/latest.json" ] && [ "$(draft stagingvar)" = public ] || { cat "$T/stagingvar"/*.out >&2; fail "a staging repository with its own key could not release"; }
echo "release-steps-check: ok (new, bridge, already published, an old tag, prerelease, seven refusals, a staging key)"
