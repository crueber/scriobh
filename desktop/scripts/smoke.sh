#!/usr/bin/env bash
# Drives a development build of the desktop app against the stand-in server, with nobody at the keyboard, and
# checks what the shell did from two records: the stand-in's list of the pages it was asked for (and what its
# page script could reach), and the notes a development build writes to standard error.
#
# It needs a display (it opens a real window) but no screen recording or accessibility permission. Dialogs are
# not shown (--auto-dialogs): questions are answered "no" and saves go to a scratch folder. What a dialog looks
# like, the menus and the keyboard are therefore not covered here; see docs/DESKTOP.md.
#
#   desktop/scripts/smoke.sh <path to target/debug/scriobh-desktop> [scratch dir]
set -euo pipefail
# Under pipefail, `cmd | grep -q` fails when grep exits at its first match and cmd then dies of SIGPIPE;
# has() reads all of its input, so a match is a match whatever the size of cmd's output.
has() { grep "$@" >/dev/null; }
here="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
app="${1:?usage: smoke.sh <debug app binary> [scratch dir]}"
work="${2:-$(mktemp -d "${TMPDIR:-/tmp}/scriobh-shell-smoke.XXXXXX")}"
mkdir -p "$work"
standin="$work/standin"
(cd "$here/testdata/standin" && go build -o "$standin" .)

pass=0; failn=0
ok()   { pass=$((pass+1)); echo "  ok    $1"; }
bad()  { failn=$((failn+1)); echo "  FAIL  $1"; }
check() { if eval "$2"; then ok "$1"; else bad "$1"; fi; }

app_pid=""; port=""; err=""
start() { # name, [STANDIN_MODE], [keep: do not clear the data folder first]
  local name="$1" mode="${2:-}" keep="${3:-}"
  [ -n "$keep" ] || rm -rf "$work/data-$name"
  rm -rf "$work/saved"; mkdir -p "$work/saved"
  err="$work/$name.err"
  STANDIN_MODE="$mode" "$app" --sidecar "$standin" --data-dir "$work/data-$name" --auto-dialogs "$work/saved" >"$work/$name.out" 2>"$err" &
  app_pid=$!
  port=""
  for _ in $(seq 1 80); do
    port="$(sed -n 's/^shell: server ready on port \([0-9]*\)$/\1/p' "$err" | tail -1)"
    [ -n "$port" ] && break
    grep -q "the server did not start" "$err" 2>/dev/null && break
    kill -0 "$app_pid" 2>/dev/null || break
    sleep 0.25
  done
}
second() { "$app" --sidecar "$standin" --data-dir "$work/data-unused" "$@" >/dev/null 2>&1; }
seen()   { curl -fsS "http://127.0.0.1:$port/standin/seen"; }
wait_for() { # description, command
  for _ in $(seq 1 40); do if eval "$2"; then ok "$1"; return 0; fi; sleep 0.25; done
  bad "$1"; return 0
}
stop() {
  kill -TERM "$app_pid" 2>/dev/null || true
  for _ in $(seq 1 60); do kill -0 "$app_pid" 2>/dev/null || break; sleep 0.25; done
}
servers() { pgrep -f "^$standin\$" | wc -l | tr -d ' '; }
note() { echo "  note  $1"; }
trap 'kill "$app_pid" 2>/dev/null || true; pkill -f "^$standin\$" 2>/dev/null || true; chmod -R u+rwx "$work/data-locked" 2>/dev/null || true' EXIT

echo "1. launch, handshake, lockdown"
start one
check "the server came up and the shell read its port" '[ -n "$port" ]'
wait_for "the webview loaded /desktop/start and was sent to the library" 'seen | has "GET /desktop/start\",\"GET /\""'
wait_for "the page script ran and reported" 'seen | has "\"calls\""'
check "no call from the page to the shell was allowed" '! seen | has ALLOWED'
check "every attempted call was refused" '[ "$(seen | grep -o "refused:" | wc -l)" -ge 14 ]'
check "window.confirm is the one the webview came with, not replaced" 'seen | has "\"confirmSource\":\"native\""'
check "the launch token is in no note" '! grep -qE "[0-9a-f]{64}" "$err"'

echo "2. a second launch hands over and exits"
second; check "a second launch exits with status 0" '[ $? -eq 0 ]'
check "there is still one server" '[ "$(servers)" = 1 ]'
second "scriobh://open/settings"
wait_for "a deep link moved the webview to /settings" 'seen | has "GET /settings"'
second "scriobh://open/p/demo/d/doc_1"
wait_for "a deep link to a document moved the webview there" 'seen | has "GET /p/demo/d/doc_1"'
second "scriobh://open/../../etc/passwd"
wait_for "a hostile deep link is answered with a calm message and no request" 'grep -q "dialog (not shown): That link does not open anything" "$err"'
check "nothing outside the routes was requested" '! seen | has passwd'
second "/Users/someone/An Leabhar.epub"
wait_for "a file handed over by the system opens the import page" 'seen | has "GET /import"'
wait_for "and the person is told to choose it there" 'grep -q "dialog (not shown): Choose An Leabhar.epub on the import page" "$err"'

echo "3. navigation"
act() { second "scriobh://open/p/do-$1"; }
act external
wait_for "a script that leaves for another site is refused and the question is put" 'grep -q "navigation refused, offered outside: https example.com" "$err"'
check "the refusal returned before the question was put (the dialog is not inside the webview callback)" '[ "$(grep -n "navigation refused, offered outside" "$err" | head -1 | cut -d: -f1)" -lt "$(grep -n "question (not shown" "$err" | head -1 | cut -d: -f1)" ]'
act redirect
wait_for "a server redirect to another site is refused" '[ "$(grep -c "navigation refused, offered outside: https example.com" "$err")" -ge 2 ]'
act open
wait_for "window.open to another site opens no window" 'grep -q "new window refused: scheme https" "$err"'
act openlocal
wait_for "window.open to an app page opens no window" 'grep -q "new window refused: scheme http$" "$err"'
act localhost
wait_for "a link to localhost is not the app: it is refused in the window and offered outside" 'grep -q "navigation refused, offered outside: http localhost" "$err"'
act file
sleep 1
check "a file: address was not loaded" '! grep -q "passwd" "$work/one.out" && seen | has "GET /p/do-file"'
if grep -q "navigation refused: scheme file" "$err"; then note "the shell refused the file: address"; else note "the webview refused the file: address itself, before asking the shell (WebKit does)"; fi
act scheme
wait_for "a system-settings address is refused" 'grep -q "navigation refused: scheme x-apple.systempreferences" "$err"'
check "after all of that the app is still on its own origin" 'seen | has "GET /p/do-scheme"'

echo "4. downloads"
act download
wait_for "an attachment is saved where the save dialog said" '[ -f "$work/saved/An Leabhar.epub" ]'
check "with the bytes the server sent" 'grep -q "a stand-in book" "$work/saved/An Leabhar.epub"'
act evilname
wait_for "a hostile suggested name is made safe" 'grep -q "download started: suggested" "$err" && ls "$work/saved" | has "evil"'
check "nothing was written outside the chosen folder" '[ ! -e /tmp/.evil ] && ! ls -a "$work/saved" | has "^\.\.[^.]"'
act blob
wait_for "a download the page made itself (a blob) is saved" '[ -f "$work/saved/from-a-blob.md" ]'
act pdfattr
wait_for "a PDF behind a link with the download attribute is saved" '[ -f "$work/saved/An Leabhar.pdf" ]'
rm -f "$work/saved/An Leabhar.pdf"
act pdf
sleep 2
if [ -f "$work/saved/An Leabhar.pdf" ]; then note "a PDF attachment behind a plain link is saved"; else note "KNOWN GAP: a PDF attachment behind a plain link (no download attribute) is shown in the window, not saved: the webview decides by type, not by Content-Disposition"; fi
second "scriobh://open/"
act confirm
sleep 2
note "window.confirm() in this webview returned: $(seen | sed -n 's/.*"confirmResult":"\([a-z]*\)".*/\1/p') (false with no dialog shown means the webview has no confirm panel)"

echo "5. quit"
stop
check "the shell exited" '! kill -0 "$app_pid" 2>/dev/null'
check "it asked the server to stop and the server exited cleanly" 'grep -q "server stopped: Clean(Some(0))" "$err"'
check "no server is left" '[ "$(servers)" = 0 ]'

echo "5b. the other ways out"
start quit
second --press-quit
for _ in $(seq 1 60); do kill -0 "$app_pid" 2>/dev/null || break; sleep 0.25; done
check "Quit from the menu's handler stops the server cleanly and leaves nothing" '! kill -0 "$app_pid" 2>/dev/null && grep -q "server stopped: Clean(Some(0))" "$err" && [ "$(servers)" = 0 ]'
start close
second --close-window
for _ in $(seq 1 60); do kill -0 "$app_pid" 2>/dev/null || break; sleep 0.25; done
check "closing the window quits, stops the server cleanly and leaves nothing" '! kill -0 "$app_pid" 2>/dev/null && grep -q "server stopped: Clean(Some(0))" "$err" && [ "$(servers)" = 0 ]'

echo "5c. the shell is killed outright"
start killed
killed_port="$port"
check "the server came up" '[ -n "$killed_port" ]'
kill -KILL "$app_pid" 2>/dev/null || true
for _ in $(seq 1 40); do [ "$(servers)" = 0 ] && break; sleep 0.25; done
check "the server saw its standard input close and stopped by itself" '[ "$(servers)" = 0 ]'
start killed "" keep
check "the next launch is not told the data folder is in use, and is on the same port" '[ -n "$port" ] && [ "$port" = "$killed_port" ]'
stop
check "and quits, leaving no server" '! kill -0 "$app_pid" 2>/dev/null && [ "$(servers)" = 0 ]'

echo "6. a server that will not start, then does"
start two die-once
wait_for "the failure is noticed" 'grep -q "the server did not start: start" "$err"'
check "the shell is still running" 'kill -0 "$app_pid" 2>/dev/null'
wait_for "the error window loaded the shell page and its script ran" 'grep -q "error page says: Scríobh could not start" "$err"'
second --press-restart
wait_for "Restart starts the server again" 'grep -q "server ready on port" "$err"'
port="$(sed -n 's/^shell: server ready on port \([0-9]*\)$/\1/p' "$err" | tail -1)"
wait_for "and the app window loads with a fresh launch token" 'seen | has "GET /desktop/start\",\"GET /\""'
stop
check "and quits, leaving no server" '! kill -0 "$app_pid" 2>/dev/null && [ "$(servers)" = 0 ]'

echo "6b. a program that answers /healthz without the proof is not sent the token"
for mode in no-proof wrong-proof proof-once; do
  start "proof-$mode" "$mode"
  wait_for "$mode: the launch is refused" 'grep -q "the server did not start: notserver" "$err"'
  wait_for "$mode: the error window says it could not tell its own server apart" 'grep -q "error page says: Scríobh could not tell its own server apart" "$err"'
  check "$mode: the shell is still running, and no server is left" 'kill -0 "$app_pid" 2>/dev/null && [ "$(servers)" = 0 ]'
  check "$mode: the window was never opened on the server" '! grep -q "server ready on port" "$err"'
  stop
done

echo "7. a server that dies while the app is open"
start three die-after
wait_for "the death is noticed" 'grep -q "the server stopped by itself" "$err"'
wait_for "the error window says the server stopped" 'grep -q "error page says: Scríobh has stopped" "$err"'
check "the shell is still running, showing its error window" 'kill -0 "$app_pid" 2>/dev/null'
stop
check "no server is left" '[ "$(servers)" = 0 ]'

echo "8. the data folder is already in use by a server this app did not start"
rm -rf "$work/data-held"; mkdir -p "$work/data-held"
SCRIOBH_DESKTOP=1 SCRIOBH_LISTEN=127.0.0.1:0 SCRIOBH_LAUNCH_TOKEN="$(printf 'a%.0s' $(seq 1 64))" SCRIOBH_DATA_DIR="$work/data-held" "$standin" >/dev/null 2>&1 &
held=$!
sleep 1
"$app" --sidecar "$standin" --data-dir "$work/data-held" --auto-dialogs "$work/saved" >/dev/null 2>"$work/held.err" &
app_pid=$!
for _ in $(seq 1 40); do kill -0 "$app_pid" 2>/dev/null || break; sleep 0.25; done
check "the app says so and exits by itself" '! kill -0 "$app_pid" 2>/dev/null && grep -q "dialog (not shown): Scríobh is already running" "$work/held.err"'
check "the server that was there is untouched" 'kill -0 "$held" 2>/dev/null'
kill -TERM "$held" 2>/dev/null || true

echo "9. a data folder that cannot be read is an error, not a first run"
if [ "$(id -u)" = 0 ]; then
  note "skipped: running as root, which reads everything"
else
  chmod -R u+rwx "$work/data-locked" 2>/dev/null || true
  rm -rf "$work/data-locked"; mkdir -p "$work/data-locked"; : > "$work/data-locked/scriobh.db"; chmod 000 "$work/data-locked"
  "$app" --sidecar "$standin" --data-dir "$work/data-locked" --auto-dialogs "$work/saved" >/dev/null 2>"$work/locked.err" &
  app_pid=$!
  wait_for_file() { for _ in $(seq 1 40); do grep -q "$1" "$work/locked.err" && return 0; sleep 0.25; done; return 1; }
  check "the window says why and no server was started" 'wait_for_file "the server did not start: datafolder" && [ "$(servers)" = 0 ]'
  wait_for "the error window loaded the shell page and its script ran" 'grep -q "error page says: Scríobh cannot look in its data folder" "$work/locked.err"'
  kill -TERM "$app_pid" 2>/dev/null || true
  for _ in $(seq 1 40); do kill -0 "$app_pid" 2>/dev/null || break; sleep 0.25; done
  check "and it quits" '! kill -0 "$app_pid" 2>/dev/null'
  chmod 700 "$work/data-locked"
fi

echo
echo "smoke: $pass passed, $failn failed (records in $work)"
[ "$failn" -eq 0 ]
