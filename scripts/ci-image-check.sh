#!/usr/bin/env bash
# Checks the container image the `image` job just built, without a Docker daemon (the job runs in the buildah
# image, with `--storage-driver vfs` and chroot isolation; see the ci.yml workflow). Two things, which the
# image job made before it moved to buildah and which nothing else in CI covers:
#
#   1. the image runs as a non-root user: its configured User is exactly 65532:65532 (distroless "nonroot",
#      docs/OPERATIONS.md), and /data in it belongs to that user;
#   2. the production binary in the image refuses the development issuer: with SCRIOBH_OIDC_STUB=1 it exits
#      non-zero and says "production build without the development issuer".
#
#   scripts/ci-image-check.sh <image reference or id>
#
# The binary is run with `buildah run --isolation=chroot` in a working container made from the image, so the
# image's own User, environment and working directory apply. A control run (`/scriobh version`) comes first: if
# that cannot run at all in this container, the script says so and runs the same two commands from the mounted
# root file system instead (the binary is static; the job container is linux/amd64 like the image).
#
# Why SCRIOBH_LISTEN is overridden: the image sets it to :8080 and the server validates its configuration before
# it looks at the build. With the stub on and a listener that is not loopback, ANY build, production or not, exits
# 1 on "not loopback" and never reaches the production check. The run therefore listens on loopback and the
# message it must print is the production build's own.
#
# Environment (for a local test with a stand-in): BUILDAH is the command (default buildah); IMAGE_CHECK_WAIT is how
# many seconds the stub run may take before it is called "accepted the stub" (default 20).
set -euo pipefail

img="${1:?usage: ci-image-check.sh <image reference or id>}"
BUILDAH="${BUILDAH:-buildah}"
WAIT="${IMAGE_CHECK_WAIT:-20}"
WANT_USER="65532:65532"
REFUSAL="production build without the development issuer"

b() { "$BUILDAH" --storage-driver vfs "$@"; }
fail() { echo "image-check: FAIL: $*" >&2; exit 1; }
say() { echo "image-check: $*"; }

ctr=""
mnt=""
cleanup() {
  if [ -n "$ctr" ]; then
    b umount "$ctr" >/dev/null 2>&1 || true
    b rm "$ctr" >/dev/null 2>&1 || true
  fi
}
trap cleanup EXIT

# ---- 1. the user ---------------------------------------------------------------------------------------------

user=""
if user="$(b inspect --type image --format '{{.OCIv1.Config.User}}' "$img" 2>/dev/null)"; then
  say "configured user (OCIv1.Config.User): '$user'"
else
  # The template path is what buildah's own documentation shows ({{.OCIv1.Config.Env}}), but if this buildah
  # does not take it, read the raw JSON: every non-empty "User" in it must be the one wanted.
  say "the inspect template was refused; reading the raw JSON instead"
  user="$(b inspect --type image "$img" | grep -o '"User": *"[^"]*"' | sed 's/^"User": *"//; s/"$//' | grep -v '^$' | sort -u | tr '\n' ' ' | sed 's/ $//')" || true
  say "configured user (raw JSON): '$user'"
fi
if [ "$user" != "$WANT_USER" ]; then
  echo "--- buildah inspect (first 120 lines) ---" >&2
  b inspect --type image "$img" 2>&1 | head -120 >&2 || true
  fail "the image's user is '$user', not $WANT_USER (a missing or root user would run the server as root)"
fi
say "ok: the image runs as $WANT_USER"

# ---- 2. the production binary refuses the stub ---------------------------------------------------------------

# --pull=never: the image under test is the one just built, never a copy of the same tag from the registry.
ctr="$(b from --pull=never "$img")"
[ -n "$ctr" ] || fail "buildah from printed no container name"
say "working container: $ctr"

# What runs a command: through buildah run (chroot), or, when the control cannot run there, from the mount.
mode="run"
out=""
rc=0

run_in_image() { # env settings are the container's (buildah config --env); arguments are the command
  timeout -k 5 "$WAIT" "$BUILDAH" --storage-driver vfs run --isolation=chroot "$ctr" -- "$@"
}

# The control: the binary starts and answers. Any failure here is not about the stub.
if out="$(run_in_image /scriobh version 2>&1)"; then
  say "control through buildah run: /scriobh version -> $(printf '%s' "$out" | head -1)"
else
  rc=$?
  say "the control did not run through 'buildah run --isolation=chroot' (status $rc): $(printf '%s' "$out" | head -5 | tr '\n' ' ')"
  mnt="$(b mount "$ctr")" || fail "buildah run could not start the image's binary and buildah mount failed too; no way to run it in this container"
  say "falling back to the mounted root file system at $mnt"
  mode="mount"
  [ -x "$mnt/scriobh" ] || fail "no executable /scriobh in the image's root file system"
  if out="$(timeout -k 5 "$WAIT" env -i PATH=/usr/bin:/bin "$mnt/scriobh" version 2>&1)"; then
    say "control from the mount: $(printf '%s' "$out" | head -1)"
  else
    rc=$?
    fail "the image's /scriobh does not run in this container either (status $rc): $out"
  fi
fi

# The stub run. `serve` would run until stopped if it accepted the stub, so it is bounded: the three outcomes are
# the production refusal (pass), still running when the wait ends (the binary accepted the stub), and anything
# else (reported with its output, so a wrong reason is never taken for a right one).
data="$(mktemp -d)"
trap 'rm -rf "$data"; cleanup' EXIT
rc=0
if [ "$mode" = run ]; then
  b config --env SCRIOBH_OIDC_STUB=1 --env SCRIOBH_LISTEN=127.0.0.1:0 "$ctr"
  out="$(run_in_image /scriobh serve 2>&1)" || rc=$?
else
  out="$(timeout -k 5 "$WAIT" env -i PATH=/usr/bin:/bin SCRIOBH_OIDC_STUB=1 SCRIOBH_LISTEN=127.0.0.1:0 SCRIOBH_DATA_DIR="$data" \
    "$mnt/scriobh" serve 2>&1)" || rc=$?
fi
say "stub run ($mode): exit status $rc"
printf '%s\n' "$out" | head -20 | sed 's/^/image-check:   | /'

if [ "$rc" -eq 124 ] || [ "$rc" -eq 137 ]; then
  fail "the production binary was still running ${WAIT}s after SCRIOBH_OIDC_STUB=1: it accepted the development issuer"
fi
if [ "$rc" -eq 0 ]; then
  fail "the production binary exited 0 with SCRIOBH_OIDC_STUB=1; it must refuse it"
fi
case "$out" in
  *"$REFUSAL"*) say "ok: the production binary refuses SCRIOBH_OIDC_STUB=1 ('$REFUSAL', status $rc)" ;;
  *) fail "the binary exited $rc but not with the production refusal ('$REFUSAL'); some other failure is being read as the right one. Output is above" ;;
esac

# ---- 3. what the layers hold ---------------------------------------------------------------------------------

# /data is made in the build stage and copied with --chown=65532:65532; a non-root server can write only there.
if [ -z "$mnt" ]; then mnt="$(b mount "$ctr" 2>/dev/null)" || mnt=""; fi
if [ -n "$mnt" ] && [ -d "$mnt/data" ]; then
  owner="$(stat -c '%u:%g' "$mnt/data" 2>/dev/null || stat -f '%u:%g' "$mnt/data" 2>/dev/null || true)"
  say "/data in the image is owned by '$owner'"
  [ "$owner" = "$WANT_USER" ] || fail "/data in the image is owned by '$owner', not $WANT_USER; the non-root server could not write its data"
else
  say "note: /data could not be looked at (mount unavailable); the owner was not checked"
fi
say "all image checks passed"
