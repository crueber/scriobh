#!/usr/bin/env bash
# Make sure the tools a CI job calls exist, installing the missing ones with
# apt when the runner image is a Debian or Ubuntu one and we are root (or have
# sudo). Fails, naming what is missing, when it cannot. Build-time only.
#
#   scripts/ci-prereqs.sh make git curl cc ssh-keygen unzip lsof-or-ss yaml
#
# Plain names are commands. The special names are:
#   cc          a C compiler (cgo, the race detector); installs gcc and libc6-dev
#   lsof-or-ss  either socket inspector (scripts/desktop-smoke.sh needs one)
#   yaml        ruby, or python3 with PyYAML (scripts/check-workflows.sh)
set -euo pipefail

pkgs=()
need() { pkgs+=("$@"); }

check() {
  for t in "$@"; do
    case "$t" in
    cc) command -v cc >/dev/null 2>&1 || command -v gcc >/dev/null 2>&1 || need gcc libc6-dev ;;
    ssh-keygen) command -v ssh-keygen >/dev/null 2>&1 || need openssh-client ;;
    lsof-or-ss) command -v lsof >/dev/null 2>&1 || command -v ss >/dev/null 2>&1 || need lsof ;;
    yaml) command -v ruby >/dev/null 2>&1 || python3 -c 'import yaml' >/dev/null 2>&1 || need python3-yaml ;;
    *) command -v "$t" >/dev/null 2>&1 || need "$t" ;;
    esac
  done
}

check "$@"

if [ "${#pkgs[@]}" -eq 0 ]; then
  echo "ci-prereqs: everything present: $*"
  exit 0
fi

echo "ci-prereqs: missing, installing: ${pkgs[*]}"
if ! command -v apt-get >/dev/null 2>&1; then
  echo "ci-prereqs: apt-get is not available on this runner image; install: ${pkgs[*]}" >&2
  exit 1
fi
sudo=""
if [ "$(id -u)" != 0 ]; then
  if command -v sudo >/dev/null 2>&1; then
    sudo=sudo
  else
    echo "ci-prereqs: not root and no sudo; install: ${pkgs[*]}" >&2
    exit 1
  fi
fi
export DEBIAN_FRONTEND=noninteractive
$sudo apt-get update -qq
$sudo apt-get install -y -qq --no-install-recommends "${pkgs[@]}"

# Look again: a package installed for another interpreter than the one on PATH
# (python3-yaml and a python3 that is not the system one) does not help.
pkgs=()
check "$@"
if [ "${#pkgs[@]}" -ne 0 ]; then
  echo "ci-prereqs: still missing after the install: ${pkgs[*]}" >&2
  exit 1
fi
echo "ci-prereqs: installed: $*"
