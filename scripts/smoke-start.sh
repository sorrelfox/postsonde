#!/usr/bin/env bash
# smoke-start.sh: start PostSonde, wait until its first window is ready, and
# close it again. The package tests run it on the installed app, offline
# (scripts/test-linux-packages.sh, the macOS and Windows jobs of
# .github/workflows/release.yml; docs/ci.md).
#
#   scripts/smoke-start.sh [-t SECONDS] COMMAND [ARGS...]
#
# Runs COMMAND with a new, empty POSTSONDE_HOME, so nothing of the user's
# settings, data or single-instance lock is used, and polls its log for the
# "first window ready" record, which the app writes once its window has
# loaded the UI (internal/app/startup.go). Then the app gets SIGTERM, as on a
# normal quit. Without a display (no DISPLAY or WAYLAND_DISPLAY) on Linux it
# runs under xvfb-run, on a session bus of its own (dbus-run-session) when
# that is installed.
#
# Fails when the app exits before its window is ready, or when the record
# doesn't come within SECONDS (default 90); the app's output and log are
# printed then. Windows: in Git Bash; the app is stopped with taskkill.
set -euo pipefail

timeout=90
if [ "${1:-}" = -t ]; then
	timeout="${2:?-t needs a number of seconds}"
	shift 2
fi
[ "$#" -gt 0 ] || {
	echo "usage: $0 [-t SECONDS] COMMAND [ARGS...]" >&2
	exit 2
}

home="$(mktemp -d "${TMPDIR:-/tmp}/postsonde-smoke.XXXXXX")"
log="$home/logs/postsonde.log"
out="$home.out"
pid=

case "$(uname -s)" in
MINGW* | MSYS* | CYGWIN*) os=windows ;;
Darwin) os=macos ;;
*) os=linux ;;
esac

stop() {
	if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
		if [ "$os" = windows ]; then
			MSYS2_ARG_CONV_EXCL='*' taskkill /F /T /IM "$(basename "$1")" >/dev/null 2>&1 || true
		else
			kill -TERM "$pid" 2>/dev/null || true
			for _ in $(seq 50); do
				kill -0 "$pid" 2>/dev/null || break
				sleep 0.2
			done
			kill -KILL "$pid" 2>/dev/null || true
		fi
		wait "$pid" 2>/dev/null || true
	fi
	pid=
}
cleanup() {
	stop "$exe"
	rm -rf "$home" "$out"
}
exe="$1"
trap cleanup EXIT

# The command, wrapped for a display and a session bus where Linux has none.
run=("$@")
if [ "$os" = linux ] && [ -z "${DISPLAY:-}${WAYLAND_DISPLAY:-}" ]; then
	command -v xvfb-run >/dev/null 2>&1 || {
		echo "smoke-start.sh: no display and no xvfb-run (install xvfb and xauth)" >&2
		exit 2
	}
	run=(xvfb-run -a -s "-screen 0 1280x800x24" "${run[@]}")
	if command -v dbus-run-session >/dev/null 2>&1; then
		run=(dbus-run-session -- "${run[@]}")
	fi
fi

# A Windows program takes a Windows path.
apphome="$home"
if [ "$os" = windows ]; then
	apphome="$(cygpath -w "$home")"
fi

echo "==> $* (POSTSONDE_HOME $apphome, up to ${timeout}s)"
start="$(date +%s)"
POSTSONDE_HOME="$apphome" "${run[@]}" >"$out" 2>&1 &
pid=$!

ready=
while :; do
	if [ -f "$log" ] && grep -q 'first window ready' "$log"; then
		ready=yes
		break
	fi
	kill -0 "$pid" 2>/dev/null || break
	[ $(($(date +%s) - start)) -lt "$timeout" ] || break
	sleep 0.5
done

if [ -z "$ready" ]; then
	if kill -0 "$pid" 2>/dev/null; then
		echo "smoke-start.sh: no \"first window ready\" in the log after ${timeout}s" >&2
	else
		echo "smoke-start.sh: the app exited before its window was ready" >&2
	fi
	echo "--- output:" >&2
	cat "$out" >&2 || true
	echo "--- log:" >&2
	cat "$log" >&2 2>/dev/null || echo "(no log)" >&2
	exit 1
fi

record="$(grep -m1 'first window ready' "$log")"
startup="$(printf '%s\n' "$record" | grep -o 'startupMs[":= ]*[0-9]*' | grep -o '[0-9]*$' || true)"
stop "$exe"
echo "ok: first window ready after ${startup:-?} ms (the app's own count)"
