#!/usr/bin/env bash
# Desktop-ready smoke test (docs/ARCHITECTURE.md, "Desktop-ready constraints").
#
# Proves the app runs the way a desktop wrapper would run it: on an ephemeral
# loopback port, with no reverse proxy, no proxy environment, and no external
# identity provider.
#
#   1. Run the end-to-end tests with every proxy variable unset. They build the
#      binary, start it on 127.0.0.1:0, read the printed port, and sign in
#      through the in-process development issuer.
#   2. Start the binary once more by hand, read LISTEN, hit /healthz, check the
#      process listens on loopback only, then send SIGTERM and require a clean
#      exit (status 0).
#   3. Start the production binary (no development issuer in it) in desktop
#      mode, as the shell does: SCRIOBH_DESKTOP=1, a launch token, no data
#      directory named. With curl, as a window would: the start address, the
#      first-run profile, the sign-in through the embedded issuer, the session;
#      then a second instance (exit 75, ALREADY_RUNNING) and SIGTERM (exit 0);
#      then the next launch, which must keep its port, stopped by closing the
#      pipe that is its standard input, as a shell that quits or dies does.
set -euo pipefail
# Under pipefail, `cmd | grep -q` fails when grep exits at its first match and cmd then dies of SIGPIPE;
# has() reads all of its input, so a match is a match whatever the size of cmd's output.
has() { grep "$@" >/dev/null; }

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"

for v in HTTP_PROXY HTTPS_PROXY ALL_PROXY http_proxy https_proxy all_proxy; do
  unset "$v"
done
# NO_PROXY is irrelevant once no proxy is set, so it is left alone.

say() { printf '\n== %s\n' "$*"; }
fail() { printf 'desktop-smoke: FAIL: %s\n' "$*" >&2; exit 1; }

work="$(mktemp -d "${TMPDIR:-/tmp}/scriobh-desktop-XXXXXX")"
pid=""
cleanup() {
  if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
    kill -TERM "$pid" 2>/dev/null || true
    sleep 1
    kill -KILL "$pid" 2>/dev/null || true
  fi
  rm -rf "$work"
}
trap cleanup EXIT

say "end-to-end tests, no proxy variables set"
if env | grep -Ei '^(http|https|all)_proxy='; then fail "a proxy variable is still set"; fi
# Always -count=1: these tests build the production binary in a child process, which the
# Go test cache cannot see, so a cached pass could hide a change.
# shellcheck disable=SC2086
go test ./cmd/scriobh -run 'TestEndToEnd|TestProduction|TestStub|TestDesktopBinary|TestWithoutDesktopMode' -count=1 -v

say "build the production binary (prod tag)"
go build -trimpath -tags prod -o "$work/scriobh-prod" ./cmd/scriobh
say "build the development binary"
go build -trimpath -o "$work/scriobh" ./cmd/scriobh

say "start on 127.0.0.1:0"
out="$work/stdout.log"
err="$work/stderr.log"
env -i PATH="$PATH" HOME="$work" \
  SCRIOBH_LISTEN=127.0.0.1:0 \
  SCRIOBH_DATA_DIR="$work/data" \
  SCRIOBH_OIDC_STUB=1 \
  "$work/scriobh" >"$out" 2>"$err" &
pid=$!

base=""
for _ in $(seq 1 100); do
  if ! kill -0 "$pid" 2>/dev/null; then
    cat "$err" >&2
    fail "the server exited before it listened"
  fi
  base="$(sed -n 's/^LISTEN //p' "$out" | head -n1)"
  [ -n "$base" ] && break
  sleep 0.2
done
[ -n "$base" ] || { cat "$err" >&2; fail "no LISTEN line within 20s"; }
echo "LISTEN $base"

case "$base" in
  http://127.0.0.1:*) ;;
  *) fail "base URL $base is not http://127.0.0.1:<port>" ;;
esac
port="${base##*:}"
[ "$port" != "0" ] || fail "the printed port is 0"

say "/healthz"
body="$(curl --noproxy '*' -fsS --max-time 5 "$base/healthz")" || fail "/healthz did not answer"
echo "$body"
case "$body" in *'"status":"ok"'*) ;; *) fail "/healthz body is not ok" ;; esac

say "the sign-in page is served with a policy and no outbound references"
headers="$(curl --noproxy '*' -sS --max-time 5 -D - -o "$work/home.html" "$base/")"
printf '%s\n' "$headers" | has -i '^content-security-policy:' || fail "no Content-Security-Policy header"
if grep -Eoi '(src|href|action)="https?://[^"]+"' "$work/home.html" | grep -v "$base"; then
  fail "the page references an external URL"
fi

say "listening sockets of pid $pid"
if command -v lsof >/dev/null 2>&1; then
  sockets="$(lsof -nP -a -iTCP -sTCP:LISTEN -p "$pid" | tail -n +2 || true)"
elif command -v ss >/dev/null 2>&1; then
  sockets="$(ss -ltnpH 2>/dev/null | grep "pid=$pid," || true)"
else
  fail "neither lsof nor ss is available to inspect listening sockets"
fi
[ -n "$sockets" ] || fail "no listening socket found for pid $pid"
echo "$sockets"
# Every listening address must be 127.0.0.1 or ::1.
if printf '%s\n' "$sockets" | grep -Eq '(\*|0\.0\.0\.0|\[::\]):[0-9]+'; then
  fail "the process listens on a non-loopback address"
fi
printf '%s\n' "$sockets" | grep -Eq '(127\.0\.0\.1|\[::1\]):[0-9]+' || fail "no loopback listener found"

say "SIGTERM, expect a clean exit"
kill -TERM "$pid"
rc=0
for _ in $(seq 1 100); do
  kill -0 "$pid" 2>/dev/null || break
  sleep 0.1
done
if kill -0 "$pid" 2>/dev/null; then
  fail "the process did not exit within 10s of SIGTERM"
fi
wait "$pid" || rc=$?
pid=""
[ "$rc" = 0 ] || { cat "$err" >&2; fail "exit status $rc after SIGTERM, want 0"; }
grep -q '"msg":"stopped"' "$err" || fail "no 'stopped' log line; the shutdown was not graceful"
echo "exit status 0"

say "desktop mode: the production binary, a launch token, the embedded issuer"
token="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')"
[ "${#token}" = 64 ] || fail "could not make a launch token"
dhome="$work/desktop-home"
mkdir -p "$dhome"
dout="$work/desktop.out"
derr="$work/desktop.err"
jar="$work/desktop.jar"
if env -i PATH="$PATH" HOME="$dhome" SCRIOBH_DESKTOP=1 SCRIOBH_LISTEN=127.0.0.1:0 "$work/scriobh-prod" >"$work/notoken.out" 2>&1; then
  fail "desktop mode started without a launch token"
fi
grep -q 'SCRIOBH_LAUNCH_TOKEN' "$work/notoken.out" || { cat "$work/notoken.out" >&2; fail "the refusal does not name SCRIOBH_LAUNCH_TOKEN"; }
if env -i PATH="$PATH" HOME="$dhome" SCRIOBH_DESKTOP=1 SCRIOBH_LISTEN=0.0.0.0:0 SCRIOBH_LAUNCH_TOKEN="$token" "$work/scriobh-prod" >"$work/nolo.out" 2>&1; then
  fail "desktop mode started on a non-loopback address"
fi
[ ! -e "$dhome/Library" ] && [ ! -e "$dhome/.local" ] || fail "a refused start made a data directory"

env -i PATH="$PATH" HOME="$dhome" \
  SCRIOBH_DESKTOP=1 \
  SCRIOBH_LISTEN=127.0.0.1:0 \
  SCRIOBH_LAUNCH_TOKEN="$token" \
  "$work/scriobh-prod" >"$dout" 2>"$derr" &
pid=$!
dbase=""
for _ in $(seq 1 150); do
  if ! kill -0 "$pid" 2>/dev/null; then
    cat "$derr" >&2
    fail "the desktop server exited before it listened"
  fi
  dbase="$(sed -n 's/^LISTEN //p' "$dout" | head -n1)"
  [ -n "$dbase" ] && break
  sleep 0.2
done
[ -n "$dbase" ] || { cat "$derr" >&2; fail "no LISTEN line within 30s"; }
echo "LISTEN $dbase"
case "$dbase" in
  http://127.0.0.1:*) ;;
  *) fail "base URL $dbase is not http://127.0.0.1:<port>" ;;
esac
dcurl() { curl --noproxy '*' -sS --max-time 10 "$@"; }

say "desktop: /healthz, and the data directory in the system's place"
dcurl -f "$dbase/healthz" | has '"status":"ok"' || fail "/healthz is not ok in desktop mode"
case "$(uname -s)" in
  Darwin) ddata="$dhome/Library/Application Support/Scriobh"; dmode="$(stat -f %Lp "$ddata" 2>/dev/null || true)" ;;
  *) ddata="$dhome/.local/share/scriobh"; dmode="$(stat -c %a "$ddata" 2>/dev/null || true)" ;;
esac
[ -f "$ddata/scriobh.db" ] || fail "the database is not in $ddata"
[ "$dmode" = 700 ] || fail "the data directory's mode is $dmode, want 700"
echo "$ddata ($dmode)"

say "desktop: a client without the launch cookie cannot sign in; a wrong token is an unknown address"
code="$(dcurl -o /dev/null -w '%{http_code}' "$dbase/auth/login")"
[ "$code" = 403 ] || fail "sign-in without the launch cookie answered $code, want 403"
code="$(dcurl -o /dev/null -w '%{http_code}' "$dbase/desktop/start?t=0000000000000000000000000000000000000000000000000000000000000000")"
[ "$code" = 404 ] || fail "a wrong launch token answered $code, want 404"
code="$(dcurl -o /dev/null -w '%{http_code}' -H 'Host: evil.example' "$dbase/healthz")"
[ "$code" = 421 ] || fail "a foreign Host header answered $code, want 421"
code="$(dcurl -o /dev/null -w '%{http_code}' "$dbase/dev-idp/.well-known/openid-configuration")"
[ "$code" = 404 ] || fail "the development issuer answers in the production build ($code)"

say "desktop: the start address, the first-run profile, the sign-in through the embedded issuer"
where="$(dcurl -L -c "$jar" -b "$jar" -o "$work/profile.html" -w '%{url_effective} %{http_code}' "$dbase/desktop/start?t=$token")"
[ "$where" = "$dbase/desktop/profile 200" ] || { cat "$derr" >&2; fail "the first run ended at '$where', want the profile page"; }
grep -q 'no password' "$work/profile.html" || fail "the profile page does not say it is a local profile with no password"
csrf="$(sed -n 's/.*name="_csrf" value="\([^"]*\)".*/\1/p' "$work/profile.html" | head -n1)"
[ -n "$csrf" ] || fail "the profile form has no token"
where="$(dcurl -L -c "$jar" -b "$jar" -o "$work/library.html" -w '%{url_effective} %{http_code}' \
  --data-urlencode "_csrf=$csrf" --data-urlencode "name=Smoke Writer" --data-urlencode "email=" "$dbase/desktop/profile")"
[ "$where" = "$dbase/ 200" ] || { cat "$derr" >&2; fail "after the profile the window is at '$where', want the library"; }
grep -q 'class="signed-in' "$work/library.html" || fail "the library page is not signed in"
grep -q 'Smoke Writer' "$work/library.html" || fail "the library page does not name the profile"
grep -q 'scriobh_session' "$jar" || fail "no session cookie was set"
grep -q 'scriobh_desktop_launch' "$jar" || fail "no launch cookie was set"
echo "signed in as the local profile"

say "desktop: the session works, the token does not work twice, nothing left the machine"
info="$(dcurl -f -b "$jar" "$dbase/api/v1/desktop/info")" || fail "desktop.info did not answer the session"
echo "$info"
case "$info" in *'"mode":"embedded"'*) ;; *) fail "desktop.info does not say embedded" ;; esac
case "$info" in *'"outbound_elsewhere":0'*) ;; *) fail "desktop.info counts outbound connections to somewhere else" ;; esac
code="$(dcurl -o /dev/null -w '%{http_code}' "$dbase/desktop/start?t=$token")"
[ "$code" = 404 ] || fail "the launch token worked a second time ($code)"

say "desktop: a second instance exits 75 and says ALREADY_RUNNING"
rc=0
second="$(env -i PATH="$PATH" HOME="$dhome" SCRIOBH_DESKTOP=1 SCRIOBH_LISTEN=127.0.0.1:0 \
  SCRIOBH_LAUNCH_TOKEN="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')" "$work/scriobh-prod" 2>/dev/null)" || rc=$?
[ "$rc" = 75 ] || fail "the second instance exited $rc, want 75"
[ "$second" = "ALREADY_RUNNING" ] || fail "the second instance printed '$second', want ALREADY_RUNNING"
echo "exit status 75, $second"

say "desktop: SIGTERM, expect a clean exit"
kill -TERM "$pid"
for _ in $(seq 1 100); do
  kill -0 "$pid" 2>/dev/null || break
  sleep 0.1
done
if kill -0 "$pid" 2>/dev/null; then
  fail "the desktop process did not exit within 10s of SIGTERM"
fi
rc=0
wait "$pid" || rc=$?
pid=""
[ "$rc" = 0 ] || { cat "$derr" >&2; fail "exit status $rc after SIGTERM, want 0"; }
grep -q '"msg":"stopped"' "$derr" || fail "no 'stopped' log line in desktop mode"
if grep -q "$token" "$derr" "$dout"; then fail "the launch token is in the output"; fi
if grep -rq "$token" "$ddata"; then fail "the launch token is in the data directory"; fi
echo "exit status 0"

say "desktop: the next launch keeps its port, and the server stops when the shell's end of its standard input closes"
fifo="$work/stdin.fifo"
mkfifo "$fifo"
env -i PATH="$PATH" HOME="$dhome" SCRIOBH_DESKTOP=1 SCRIOBH_LISTEN=127.0.0.1:0 \
  SCRIOBH_LAUNCH_TOKEN="$(od -An -N32 -tx1 /dev/urandom | tr -d ' \n')" \
  "$work/scriobh-prod" <"$fifo" >"$work/desktop2.out" 2>"$work/desktop2.err" &
pid=$!
exec 9>"$fifo" # the shell's end of the pipe
dbase2=""
for _ in $(seq 1 150); do
  kill -0 "$pid" 2>/dev/null || { cat "$work/desktop2.err" >&2; fail "the second desktop launch exited before it listened"; }
  dbase2="$(sed -n 's/^LISTEN //p' "$work/desktop2.out" | head -n1)"
  [ -n "$dbase2" ] && break
  sleep 0.2
done
[ "$dbase2" = "$dbase" ] || fail "the second launch listens on '$dbase2', want the port of the first ($dbase): what the browser kept for that address would be lost"
dcurl -f "$dbase2/healthz" >/dev/null || fail "/healthz on the second launch"
exec 9>&- # the shell is gone
for _ in $(seq 1 100); do
  kill -0 "$pid" 2>/dev/null || break
  sleep 0.1
done
if kill -0 "$pid" 2>/dev/null; then
  fail "the server outlived the shell: it did not stop within 10s of its standard input closing"
fi
rc=0
wait "$pid" || rc=$?
pid=""
[ "$rc" = 0 ] || { cat "$work/desktop2.err" >&2; fail "exit status $rc after standard input closed, want 0"; }
grep -q 'standard input closed' "$work/desktop2.err" || fail "the log does not say why the server stopped"
echo "LISTEN $dbase2 again; exit status 0 when the pipe closed"

say "the production binary refuses the development issuer"
if env -i PATH="$PATH" HOME="$work" SCRIOBH_LISTEN=127.0.0.1:0 SCRIOBH_DATA_DIR="$work/prod-data" SCRIOBH_OIDC_STUB=1 \
  "$work/scriobh-prod" >"$work/prod.out" 2>&1; then
  fail "the prod binary started with SCRIOBH_OIDC_STUB=1"
fi
grep -qi 'development issuer' "$work/prod.out" || { cat "$work/prod.out" >&2; fail "unexpected refusal message"; }
head -n 3 "$work/prod.out"

printf '\ndesktop-smoke: ok\n'
