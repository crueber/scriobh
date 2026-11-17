#!/usr/bin/env bash
# Starts an INSTALLED desktop app (Linux: headless; macOS: a window opens on the logged-in desktop, nobody needs
# to touch it) and proves the shell started its bundled server and that the server
# came up, then quits the app and proves nothing is left behind. Linux (the Linux job runs it after
# desktop/scripts/deb-install.sh); the logic is portable so that it can be tested against a stand-in shell.
#
#   desktop/scripts/launch-check.sh <launcher> [server]
#   desktop/scripts/launch-check.sh /usr/bin/scriobh-desktop
#
# What a release build exposes: nothing on standard error (the notes and the server's echo are development-only;
# see dialogs::note and SpawnConfig::echo_stderr), so the evidence is what the server itself leaves in the data
# directory and what the operating system shows:
#   - <data>/desktop/port      written by the server when it binds (cmd/scriobh/desktop.go, desktopListen),
#   - GET /healthz on that port,
#   - <data>/scriobh.lock      the advisory lock the server holds for as long as it runs (internal/db/lock_unix.go),
#   - <data>/secret.key        the first-run fallback when no keychain is usable (docs/p8-notes/desktop-shell.md),
#   - the process table: the server is a child of the shell and its executable is scriobh-desktop-server.
#
# HOME, XDG_DATA_HOME, XDG_CONFIG_HOME, XDG_CACHE_HOME and XDG_RUNTIME_DIR are a throwaway folder, so the data
# directory is <work>/data/scriobh (the Linux rule: ${XDG_DATA_HOME}/scriobh), and the real keychain is not asked:
# in a container there is no Secret Service, so `keychain::resolve` takes its first-run fallback and the server
# makes secret.key. The "keychain could not be used" notice is a dialog shown once; the marker that says it was
# shown is created first (unless LAUNCH_CHECK_NO_NOTICE_MARKER=1), so that no modal dialog is open while the shell is
# asked to quit.
#
# Environment:
#   LAUNCH_CHECK_WRAP        the command that gives the app a display and a session bus. Default
#                            "xvfb-run -a dbus-run-session --"; set it empty to run the launcher directly.
#   LAUNCH_CHECK_START_WAIT  seconds for the server to bind and answer /healthz (default 90; the shell itself
#                            allows the server 30)
#   LAUNCH_CHECK_QUIT_WAIT   seconds for the shell to exit after SIGTERM (default 40; it allows the server 10)
#   LAUNCH_CHECK_NO_NOTICE_MARKER  1 to NOT pre-empt the first-run "keychain could not be used" dialog: a dialog is then
#                            open when the app is asked to quit, and the quit must still succeed (default: it is
#                            suppressed, so no modal dialog can be in the way)
#   LAUNCH_CHECK_GDB         1 when the launcher is a gdb that runs the shell (desktop/scripts/arch-pkg.sh makes one when
#                            ARCH_LAUNCH_GDB=1; it needs LAUNCH_CHECK_SHELL_NAME too): a shell that will not quit is then
#                            stopped with SIGINT to gdb, which prints every thread's backtrace into the app's output,
#                            and that output is shown. This exists because Yama's default (ptrace_scope 1) lets only an
#                            ancestor attach, so a debugger started afterwards cannot; the debugger has to be the parent.
#                            It changes nothing else: the limits, and the failure, are the same.
#   LAUNCH_CHECK_GDB_WAIT    seconds to wait for those backtraces (default 240: symbols may be fetched)
#   LAUNCH_CHECK_SHELL_NAME  the executable name of the shell (scriobh-desktop), for a launcher that is not the shell
#                            but starts it below itself: an AppImage run with --appimage-extract-and-run, whose
#                            runtime stays as the parent. The shell is then looked for among the launcher's
#                            descendants, and SIGTERM goes to it, not to the runtime. Unset (the .deb): the process
#                            the launcher command became is the shell, as before.
#
# Every wait is bounded, and a failure prints the data directory, the process table, the app's own output, and
# a short run of the server on its own (to tell "the server is broken" from "the shell did not start it").
set -euo pipefail

launcher="${1:?usage: launch-check.sh <launcher> [server]}"
server_name="scriobh-desktop-server"
server="${2:-$(dirname "$launcher")/$server_name}"
START_WAIT="${LAUNCH_CHECK_START_WAIT:-90}"
QUIT_WAIT="${LAUNCH_CHECK_QUIT_WAIT:-40}"
os="$(uname -s)"
if [ "$os" = Darwin ]; then WRAP="${LAUNCH_CHECK_WRAP-}"; else WRAP="${LAUNCH_CHECK_WRAP-xvfb-run -a dbus-run-session --}"; fi

[ -x "$launcher" ] || { echo "launch-check: FAIL: $launcher is not an executable file" >&2; exit 1; }
[ -x "$server" ] || { echo "launch-check: FAIL: no executable bundled server at $server" >&2; exit 1; }

work="$(mktemp -d "${TMPDIR:-/tmp}/scriobh-lc.XXXXXX")"
home="$work/home"
log="$work/app.log"
pidfile="$work/shell.pid"
mkdir -p "$work/run"
chmod 700 "$work/run"
if [ "$os" = Darwin ]; then
  # macOS: the data folder is $HOME/Library/Application Support/Scriobh and the shell's configuration folder is
  # $HOME/Library/Application Support/app.scriobh, so a throwaway HOME moves both. HOME is also where the
  # Security framework finds the keychain search list: with a throwaway HOME the list holds only the system
  # keychain and there is no default keychain, so the person's login keychain (and the real "Scriobh" entry in
  # it) is out of reach, the keychain lookup fails and the first-run fallback makes secret.key, as in a container.
  # WebKit's own folders (~/Library/WebKit/<identifier>, Caches, HTTPStorages) follow CFFIXED_USER_HOME, set below,
  # not HOME: with HOME alone a launch leaves them in the real ~/Library (found and removed while testing this).
  data="$home/Library/Application Support/Scriobh"
  confdir="$home/Library/Application Support/app.scriobh"
  mkdir -p "$home/Library/Application Support" "$home/Library/Caches" "$home/Library/Preferences"
else
  data="$home/.local/share/scriobh"
  confdir="$home/.config/app.scriobh"
  mkdir -p "$home/.local/share" "$home/.config" "$home/.cache"
fi
# The said-once marker (Linux: <XDG_CONFIG_HOME>/<bundle identifier>; macOS: see above). Made first, so that the
# first-run notice is not shown. LAUNCH_CHECK_NO_NOTICE_MARKER=1 leaves it out: the notice is then a real dialog, open
# while the app is asked to quit, which is the path the Arch jobs exercise (docs/DESKTOP.md, known problem 10).
mkdir -p "$confdir"
if [ "${LAUNCH_CHECK_NO_NOTICE_MARKER:-}" != 1 ]; then
  : > "$confdir/notice-keychain-unavailable"
fi

shell_pid=""
server_pid=""
wrapper_pid=""
launcher_pid=""
term_cpu=""
finished=""

say() { echo "launch-check: $*"; }
now() { date +%s; }

# Runs a command for at most $1 seconds where timeout exists (GNU coreutils; macOS has none), and without a limit where
# it does not.
limited() {
  local secs="$1"
  shift
  if command -v timeout >/dev/null 2>&1; then timeout "$secs" "$@"; else "$@"; fi
}

exe_of() { # the executable of a process
  if [ -e "/proc/$1/exe" ]; then readlink "/proc/$1/exe" 2>/dev/null | sed 's/ (deleted)$//'
  else ps -o comm= -p "$1" 2>/dev/null | sed 's/^ *//'; fi
}

children_of() { pgrep -P "$1" 2>/dev/null || true; }

# The first process at or below $1 whose executable is named $2, breadth first (LAUNCH_CHECK_SHELL_NAME only).
find_descendant() {
  local level="$1" next c
  while [ -n "$level" ]; do
    next=""
    for c in $level; do
      if [ "$(basename "$(exe_of "$c")")" = "$2" ]; then echo "$c"; return 0; fi
      next="$next $(children_of "$c")"
    done
    level="${next# }"
    level="${level//  / }"
    [ -n "${level// /}" ] || level=""
  done
  return 1
}

find_server() { # the child of the shell whose executable is the bundled server
  local c
  for c in $(children_of "$shell_pid"); do
    if [ "$(basename "$(exe_of "$c")")" = "$server_name" ]; then echo "$c"; return 0; fi
  done
  return 1
}

read_port() {
  local p
  [ -r "$data/desktop/port" ] || return 1
  p="$(tr -d ' \r\n' < "$data/desktop/port" 2>/dev/null)" || return 1
  case "$p" in '' | *[!0-9]*) return 1 ;; esac
  [ "$p" -ge 1024 ] && [ "$p" -le 65535 ] || return 1
  echo "$p"
}

healthz_code() { # the HTTP status of GET /healthz, or 000
  curl -sS --noproxy '*' -o "$work/healthz.body" -w '%{http_code}' --max-time 3 "http://127.0.0.1:$1/healthz" 2>/dev/null || echo 000
}

# 0 when the lock is free (this process took it and let it go), 10 when someone holds it.
lock_state() {
  python3 -I -c '
import fcntl, sys
f = open(sys.argv[1], "rb")
try:
    fcntl.flock(f, fcntl.LOCK_EX | fcntl.LOCK_NB)
except OSError:
    sys.exit(10)
sys.exit(0)
' "$data/scriobh.lock"
}

# CPU time (utime + stime, clock ticks) of a process, from /proc.
cpu_ticks() { sed 's/^.*) //' "/proc/$1/stat" 2>/dev/null | awk '{ print $12 + $13 }' || true; }

# The windows the X display holds, as the shell's own environment names it: did the webview's window ever appear?
# xwininfo is optional (x11-utils, xorg-xwininfo); without it this says nothing.
x_windows() {
  local disp
  command -v xwininfo >/dev/null 2>&1 || { echo "(xwininfo is not installed: not looked at)"; return 0; }
  disp="$(tr '\0' '\n' < "/proc/$1/environ" 2>/dev/null | sed -n 's/^DISPLAY=//p' | head -1 || true)"
  [ -n "$disp" ] || { echo "(the shell has no DISPLAY in its environment)"; return 0; }
  DISPLAY="$disp" limited 10 xwininfo -root -tree 2>&1 | grep -E 'Scriobh|scriobh|has [0-9]+ child|[0-9]+x[0-9]+\+' | head -20 || true
}

# Where a process that does not quit is: one line per thread (state, CPU time, the kernel function it sleeps in, the
# system call, signals pending and blocked), the signal dispositions, and the stacks. Everything is best effort: a
# container may refuse /proc/<pid>/syscall and ptrace (Yama), and the output says so.
stuck_report() {
  local pid="$1" t tid comm st cpu wchan sc pnd blk
  echo "--- the stuck shell (pid $pid): process state and signals ---" >&2
  grep -E '^(State|Threads|SigPnd|ShdPnd|SigBlk|SigIgn|SigCgt|VmRSS|voluntary_ctxt_switches|nonvoluntary_ctxt_switches):' "/proc/$pid/status" >&2 2>/dev/null || echo "(no /proc/$pid/status)" >&2
  echo "SIGTERM is bit 15 (0x4000) of SigCgt/SigIgn/SigBlk/SigPnd/ShdPnd; a handler caught it when SigCgt has it, and ShdPnd has it while no thread has taken it" >&2
  if [ -n "${term_cpu:-}" ]; then echo "CPU ticks used since SIGTERM: $(( $(cpu_ticks "$pid") - term_cpu )) (about 0 means asleep, not spinning)" >&2; fi
  echo "--- threads (tid, name, state, utime+stime, wchan, syscall [number, args], SigPnd, SigBlk) ---" >&2
  for t in /proc/"$pid"/task/*; do
    tid="${t##*/}"
    comm="$(cat "$t/comm" 2>/dev/null || echo '?')"
    st="$(sed 's/^.*) //' "$t/stat" 2>/dev/null | awk '{ print $1 }' || true)"
    cpu="$(cpu_ticks_task "$t")"
    wchan="$(cat "$t/wchan" 2>/dev/null || echo '-')"
    sc="$(cat "$t/syscall" 2>/dev/null | cut -c1-60 || true)"
    [ -n "$sc" ] || sc="(unreadable)"
    pnd="$(sed -n 's/^SigPnd:[[:space:]]*//p' "$t/status" 2>/dev/null || true)"
    blk="$(sed -n 's/^SigBlk:[[:space:]]*//p' "$t/status" 2>/dev/null || true)"
    echo "  $tid $comm $st cpu=$cpu wchan=$wchan syscall=$sc pnd=$pnd blk=$blk" >&2
  done
  echo "--- the shell's open files (count, and the sockets and pipes) ---" >&2
  echo "  $(ls "/proc/$pid/fd" 2>/dev/null | wc -l | tr -d ' ' || true) descriptors" >&2
  ls -l "/proc/$pid/fd" 2>/dev/null | sed -n 's/.* -> \(socket\|pipe\|anon_inode\)/  \1/p' | sort | uniq -c | sort -rn | head -8 >&2 || true
  echo "--- stack traces ---" >&2
  if [ "$os" != Linux ]; then
    echo "(stacks are taken on Linux only: /proc and ptrace_scope are Linux's, and a debugger is not attached to a live process on $os)" >&2
  else
    echo "ptrace_scope: $(cat /proc/sys/kernel/yama/ptrace_scope 2>/dev/null || echo 'no Yama'); CapEff: $(sed -n 's/^CapEff:[[:space:]]*//p' /proc/self/status 2>/dev/null) (CAP_SYS_PTRACE is bit 19, 0x80000)" >&2
    if [ "${LAUNCH_CHECK_GDB:-}" = 1 ]; then
      echo "(gdb is the parent: its backtraces follow below)" >&2
    elif command -v eu-stack >/dev/null 2>&1; then
      echo "(eu-stack -p $pid; the shell is stripped, so its own frames are addresses, but libc, GLib, GTK, WebKit and futex frames show where each thread waits)" >&2
      limited 60 eu-stack -p "$pid" 2>&1 | head -250 >&2 || true
    elif command -v gdb >/dev/null 2>&1; then
      limited 90 gdb -p "$pid" -batch -ex 'set pagination off' -ex 'set debuginfod enabled off' -ex 'thread apply all bt 30' 2>&1 | grep -vE '^(\[New LWP|warning:|Reading symbols|Using host)' | head -250 >&2 || true
    else
      echo "(neither eu-stack nor gdb is installed: no stacks; the thread table above is what there is)" >&2
    fi
  fi
  echo "--- the windows on the display ---" >&2
  x_windows "$pid" >&2
}

cpu_ticks_task() { sed 's/^.*) //' "$1/stat" 2>/dev/null | awk '{ print $12 + $13 }' || true; }

diagnose() {
  echo "----- launch-check: what was found -----" >&2
  echo "work folder: $work   data folder: $data" >&2
  echo "shell pid: ${shell_pid:-none}   server pid: ${server_pid:-none}   wrapper pid: ${wrapper_pid:-none}" >&2
  echo "--- data folder (two levels) ---" >&2
  (cd "$data" 2>/dev/null && find . -maxdepth 2 -exec ls -ld {} + 2>&1 | head -40) >&2 || echo "(no data folder)" >&2
  [ -f "$data/desktop/port" ] && echo "desktop/port: $(cat "$data/desktop/port")" >&2
  if [ -n "${shell_pid:-}" ] && kill -0 "$shell_pid" 2>/dev/null; then stuck_report "$shell_pid"; fi
  if [ "${LAUNCH_CHECK_GDB:-}" = 1 ] && [ -n "${launcher_pid:-}" ] && kill -0 "$launcher_pid" 2>/dev/null; then
    echo "--- gdb is the shell's parent: interrupting it to get every thread's backtrace (up to ${LAUNCH_CHECK_GDB_WAIT:-240} s) ---" >&2
    kill -INT "$launcher_pid" 2>/dev/null || true
    local waited=0
    while [ "$waited" -lt "${LAUNCH_CHECK_GDB_WAIT:-240}" ] && ! grep -q 'GDB: end' "$log" 2>/dev/null && kill -0 "$launcher_pid" 2>/dev/null; do sleep 1; waited=$(( waited + 1 )); done
    echo "--- gdb's output (after ${waited} s) ---" >&2
    grep -A14 'GDB DIALOG' "$log" 2>/dev/null | cut -c1-200 >&2 || true
    sed -n '/GDB: the shell was interrupted/,$p' "$log" 2>/dev/null | cut -c1-300 | head -700 >&2 || true
  fi
  echo "--- processes ---" >&2
  # shellcheck disable=SC2009
  ps -eo pid,ppid,etime,stat,args 2>/dev/null | grep -E 'scriobh|Xvfb|dbus-(run|daemon)|WebKit' | grep -v grep >&2 || echo "(none)" >&2
  echo "--- the app's own output (last 60 lines of $log) ---" >&2
  tail -60 "$log" >&2 2>/dev/null || true
  # The server alone, with the environment the shell gives it, for a few seconds: server broken, or shell not starting it?
  if [ "${SERVER_ALONE:-1}" = 1 ] && [ -x "$server" ]; then
    echo "--- the bundled server on its own (6 s, its own data folder) ---" >&2
    local alone="$work/alone"
    mkdir -p "$alone"
    (
      env -i PATH="$PATH" HOME="$home" SCRIOBH_DESKTOP=1 SCRIOBH_LISTEN=127.0.0.1:0 \
        SCRIOBH_LAUNCH_TOKEN="$(printf 'b%.0s' $(seq 1 64))" SCRIOBH_DATA_DIR="$alone" "$server" < /dev/null > "$work/alone.out" 2>&1 &
      p=$!
      sleep 6
      kill -TERM "$p" 2>/dev/null || true
      sleep 1
      kill -KILL "$p" 2>/dev/null || true
    )
    head -30 "$work/alone.out" | cut -c1-300 >&2
  fi
  echo "----- end -----" >&2
}

die() {
  echo "launch-check: FAIL: $*" >&2
  diagnose
  exit 1
}

cleanup() {
  local status=$?
  if [ -z "$finished" ]; then
    # Something went wrong or the script was interrupted: nothing may be left running.
    local kids=""
    [ -n "$shell_pid" ] && kids="$(children_of "$shell_pid")"
    [ -n "$server_pid" ] && kids="$kids $server_pid"
    [ -n "$shell_pid" ] && kill -TERM "$shell_pid" 2>/dev/null || true
    sleep 1
    # shellcheck disable=SC2086
    [ -n "${kids// /}" ] && kill -KILL $kids 2>/dev/null || true
    [ -n "$shell_pid" ] && kill -KILL "$shell_pid" 2>/dev/null || true
    [ -n "$wrapper_pid" ] && kill -KILL "$wrapper_pid" 2>/dev/null || true
  fi
  rm -rf "$work"
  exit "$status"
}
trap cleanup EXIT
trap 'exit 130' INT TERM

# ---- start ---------------------------------------------------------------------------------------------------

if [ "$os" = Darwin ]; then
  # CFFIXED_USER_HOME is what Cocoa (NSHomeDirectory, which WebKit uses for the web view's storage under
  # ~/Library/WebKit/<bundle identifier>) reads instead of the account's real home; HOME alone moves the Rust side
  # only, and a launch then leaves a WebKit folder in the real ~/Library.
  app_env=("HOME=$home" "CFFIXED_USER_HOME=$home" "TMPDIR=$work/")
else
  app_env=(
    "HOME=$home" "XDG_DATA_HOME=$home/.local/share" "XDG_CONFIG_HOME=$home/.config" "XDG_CACHE_HOME=$home/.cache"
    "XDG_RUNTIME_DIR=$work/run" "LANG=C.UTF-8" "NO_AT_BRIDGE=1" "GDK_BACKEND=x11"
    # The webview is not under test and a runner has no GPU, no user namespaces to spare and no compositor.
    "LIBGL_ALWAYS_SOFTWARE=1" "WEBKIT_DISABLE_COMPOSITING_MODE=1" "WEBKIT_DISABLE_DMABUF_RENDERER=1"
    "WEBKIT_DISABLE_SANDBOX_THIS_IS_DANGEROUS=1"
  )
fi
wrap=()
if [ -n "$WRAP" ]; then read -r -a wrap <<< "$WRAP"; fi

say "launching $launcher ${WRAP:+under: $WRAP}"
say "data folder: $data (a throwaway HOME; the real data folder and keychain are out of reach)"
# shellcheck disable=SC2016  # $$ and "$@" are for the inner sh
env "${app_env[@]}" ${wrap[@]+"${wrap[@]}"} sh -c 'echo $$ > "$1"; shift; exec "$@"' sh "$pidfile" "$launcher" > "$log" 2>&1 &
wrapper_pid=$!

deadline=$(( $(now) + 20 ))
while [ ! -s "$pidfile" ]; do
  kill -0 "$wrapper_pid" 2>/dev/null || die "the launch command ended before the app started (a display or session bus could not be made?)"
  [ "$(now)" -lt "$deadline" ] || die "no app process after 20 s (the wrapper never ran the launcher)"
  sleep 0.2
done
shell_pid="$(cat "$pidfile")"
if [ -n "${LAUNCH_CHECK_SHELL_NAME:-}" ]; then
  launcher_pid="$shell_pid"
  say "launcher pid $launcher_pid (executable: $(exe_of "$launcher_pid")); looking below it for $LAUNCH_CHECK_SHELL_NAME"
  shell_pid=""
  deadline=$(( $(now) + 60 ))
  while [ -z "$shell_pid" ]; do
    shell_pid="$(find_descendant "$launcher_pid" "$LAUNCH_CHECK_SHELL_NAME")" || shell_pid=""
    [ -n "$shell_pid" ] && break
    kill -0 "$launcher_pid" 2>/dev/null || die "the launcher ended before a $LAUNCH_CHECK_SHELL_NAME process appeared below it"
    [ "$(now)" -lt "$deadline" ] || die "no $LAUNCH_CHECK_SHELL_NAME process below the launcher (pid $launcher_pid) after 60 s"
    sleep 0.2
  done
fi
say "shell pid $shell_pid (executable: $(exe_of "$shell_pid"))"

# ---- the server came up ---------------------------------------------------------------------------------------

port=""
code=000
deadline=$(( $(now) + START_WAIT ))
while :; do
  kill -0 "$shell_pid" 2>/dev/null || die "the shell exited before its server answered (port file: $([ -f "$data/desktop/port" ] && cat "$data/desktop/port" || echo none))"
  if port="$(read_port)"; then
    code="$(healthz_code "$port")"
    [ "$code" = 200 ] && break
  fi
  if [ "$(now)" -ge "$deadline" ]; then
    if [ -z "$port" ]; then die "after ${START_WAIT} s the server had not bound a port: no usable $data/desktop/port"
    else die "after ${START_WAIT} s GET /healthz on 127.0.0.1:$port answered '$code', not 200"; fi
  fi
  sleep 0.2
done
say "ok: the server bound 127.0.0.1:$port (desktop/port) and GET /healthz answered 200: $(head -c 120 "$work/healthz.body" | tr '\n' ' ')"

for _ in $(seq 1 50); do
  server_pid="$(find_server)" && break
  sleep 0.1
done
[ -n "$server_pid" ] || die "/healthz answered, but no child of the shell (pid $shell_pid) is running $server_name: the shell did not start the server that answered, or it already stopped it (its window could not be opened?)"
say "ok: the shell's child $server_pid runs $(exe_of "$server_pid")"

# The lock is the server's: held while it runs. A probe that could never report "held" would prove nothing at quit.
[ -f "$data/scriobh.lock" ] || die "no $data/scriobh.lock while the server runs"
rc=0; lock_state || rc=$?
[ "$rc" -eq 10 ] || die "the data directory's lock is not held while the server runs (probe status $rc)"
say "ok: scriobh.lock is held while the server runs"

if [ -s "$data/secret.key" ]; then
  say "first run: the server made $data/secret.key (the keychain fallback), mode $(stat -c '%a' "$data/secret.key" 2>/dev/null || stat -f '%Lp' "$data/secret.key")"
else
  say "note: no secret.key in the data folder, so a keychain supplied the key"
fi

# Still the same server a moment later: it did not exit as the window opened.
sleep 2
kill -0 "$server_pid" 2>/dev/null || die "the server (pid $server_pid) stopped by itself within 2 s of answering"
[ "$(healthz_code "$port")" = 200 ] || die "GET /healthz stopped answering 200 two seconds later"

# ---- quit ------------------------------------------------------------------------------------------------------

# Is the shell's main thread already asleep on a lock before it is asked to quit? (A GTK main loop at rest waits in poll.)
# /proc is Linux's: on macOS there is nothing to read, and no line is printed.
if [ "$os" = Linux ]; then
  say "main thread before the quit: wchan=$(cat "/proc/$shell_pid/task/$shell_pid/wchan" 2>/dev/null || echo '?'), threads waiting: $(for t in /proc/"$shell_pid"/task/*; do cat "$t/wchan" 2>/dev/null; echo; done | sort | uniq -c | sort -rn | awk '{ printf "%s x%s  ", $2, $1 }')"
fi
term_cpu="$(cpu_ticks "$shell_pid")"
say "the display's windows before the quit (informational):"
x_windows "$shell_pid" | sed 's/^/launch-check:   /'
say "sending SIGTERM to the shell (pid $shell_pid)"
kill -TERM "$shell_pid"
deadline=$(( $(now) + QUIT_WAIT ))
while kill -0 "$shell_pid" 2>/dev/null; do
  [ "$(now)" -lt "$deadline" ] || die "the shell was still running ${QUIT_WAIT} s after SIGTERM (a dialog holding the main loop, or a hang in its exit path)"
  sleep 0.2
done
say "ok: the shell exited"

deadline=$(( $(now) + 15 ))
while kill -0 "$server_pid" 2>/dev/null; do
  [ "$(now)" -lt "$deadline" ] || die "the shell exited but its server (pid $server_pid) is still running: it was orphaned"
  sleep 0.2
done
say "ok: the server process $server_pid is gone"
code="$(healthz_code "$port")"
[ "$code" != 200 ] || die "something still answers /healthz on port $port after the server exited"

rc=0; lock_state || rc=$?
[ "$rc" -eq 0 ] || die "the data directory's lock is still held after quit (probe status $rc)"
say "ok: scriobh.lock is released"

deadline=$(( $(now) + 30 ))
while kill -0 "$wrapper_pid" 2>/dev/null; do
  [ "$(now)" -lt "$deadline" ] || die "the launch command (xvfb-run, dbus-run-session) did not finish 30 s after the app quit"
  sleep 0.2
done
wrapper_status=0; wait "$wrapper_pid" 2>/dev/null || wrapper_status=$?
say "the launch command finished with status $wrapper_status (not asserted)"
if [ "${LAUNCH_CHECK_GDB:-}" = 1 ]; then
  # Under gdb, breakpoints named every dialog the shell raised (arch-pkg.sh); say which, even on a pass.
  if grep -q 'GDB DIALOG' "$log" 2>/dev/null; then
    say "NOTE: the shell raised a dialog while it ran:"
    grep -A14 'GDB DIALOG' "$log" | cut -c1-200 | sed 's/^/launch-check:   /'
  else
    say "no dialog was raised while it ran"
  fi
fi
finished=1
say "all launch checks passed"
