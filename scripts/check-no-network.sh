#!/usr/bin/env bash
# check-no-network.sh: run a PostSonde build under strace and report every
# network connection it, or a process it starts (WebKitNetworkProcess,
# WebKitWebProcess, bwrap, xdg-dbus-proxy), attempts (docs/privacy.md,
# "Manual checks"). Linux only, no root needed.
#
#   scripts/check-no-network.sh [options] [BINARY] [-- APP_ARGS...]
#
#   BINARY          the build to run (default bin/postsonde: `make build`)
#   --idle SECONDS  how long the app runs before it is closed (default 300).
#                   Use the window meanwhile: whatever you do is traced.
#   --offline       run it in a new network namespace with no interfaces
#                   up (`unshare --user --map-current-user --net`), so the
#                   network is unavailable; Wayland, D-Bus and the other
#                   Unix sockets still work
#   --allow A:P     a connection to IP address A, port P is expected (a
#                   request you send yourself; IPv6 as [::1]:P); repeatable
#   --dir DIR       where the logs and the scratch data go (default: a new
#                   directory under $TMPDIR)
#   APP_ARGS        passed to the app, e.g. a project folder to open
#
# The app runs with POSTSONDE_HOME and XDG_{CONFIG,DATA,CACHE,STATE}_HOME in
# that directory, so your own settings, recent projects and WebKit data are
# not used or changed. After --idle seconds it gets SIGTERM, on which Wails
# quits cleanly.
#
# strace -f follows every process the app starts, including WebKit's
# sandboxed ones, and logs connect, sendto, sendmsg, sendmmsg, socket and
# execve with the sockets decoded (-yy). The report fails (exit 1) on:
#   - a connect or send to an AF_INET/AF_INET6 address (loopback included)
#     or on a TCP/UDP socket, unless --allow lists it;
#   - a connect to a name resolver socket (systemd-resolved, nscd), or a
#     D-Bus message to org.freedesktop.resolve1 (a DNS lookup);
#   - a D-Bus message to the proxy resolver or network-reachability calls
#     of the desktop portal or to PacRunner (they look up hosts).
# It lists, without failing, the Unix sockets connected to (Wayland, the
# session and system buses, AT-SPI) and the programs started.
#
# Exit status: 0 nothing found, 1 network activity found, 2 usage or setup
# error.
set -euo pipefail

root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

die() {
	echo "error: $*" >&2
	exit 2
}

idle=300
offline=false
dir=
bin=
allow=()
while [ $# -gt 0 ]; do
	case "$1" in
	--idle)
		idle="${2:?--idle needs a number of seconds}"
		shift 2
		;;
	--offline)
		offline=true
		shift
		;;
	--allow)
		allow+=("${2:?--allow needs ADDRESS:PORT}")
		shift 2
		;;
	--dir)
		dir="${2:?--dir needs a directory}"
		shift 2
		;;
	--)
		shift
		break
		;;
	-h | --help)
		sed -n '2,/^set -euo/p' "$0" | sed '$d; s/^# \{0,1\}//'
		exit 0
		;;
	-*) die "unknown option $1 (see --help)" ;;
	*)
		[ -z "$bin" ] || die "one binary only (app arguments go after --)"
		bin="$1"
		shift
		;;
	esac
done
[ "$(uname -s)" = Linux ] || die "Linux only; docs/privacy.md has the checks for macOS and Windows"
command -v strace >/dev/null 2>&1 || die "strace not found (dnf install strace / apt install strace)"
case "$idle" in '' | *[!0-9]*) die "--idle takes whole seconds" ;; esac
bin="${bin:-$root/bin/postsonde}"
[ -x "$bin" ] || die "$bin is not an executable (build it with: make build)"
bin="$(cd "$(dirname "$bin")" && pwd)/$(basename "$bin")"
if $offline; then
	command -v unshare >/dev/null 2>&1 || die "unshare not found (util-linux)"
	unshare --user --map-current-user --net true 2>/dev/null ||
		die "unshare --user --net failed: unprivileged user namespaces are off"
fi

if [ -z "$dir" ]; then
	dir="$(mktemp -d "${TMPDIR:-/tmp}/postsonde-netcheck.XXXXXX")"
else
	mkdir -p "$dir"
fi
dir="$(cd "$dir" && pwd)"
log="$dir/strace.log"
mkdir -p "$dir/home" "$dir/xdg/config" "$dir/xdg/data" "$dir/xdg/cache" "$dir/xdg/state"
rm -f "$log"

echo "==> $bin ($("$bin" --version 2>/dev/null || echo '?'))"
echo "==> logs and scratch data: $dir"
$offline && echo "==> offline: own network namespace, no interfaces up"
echo "==> running for ${idle}s under strace -f; use the window meanwhile"

wrap=()
$offline && wrap=(unshare --user --map-current-user --net)
(
	export POSTSONDE_HOME="$dir/home"
	export XDG_CONFIG_HOME="$dir/xdg/config" XDG_DATA_HOME="$dir/xdg/data"
	export XDG_CACHE_HOME="$dir/xdg/cache" XDG_STATE_HOME="$dir/xdg/state"
	exec "${wrap[@]}" strace -f -qq -yy -tt -s 256 -o "$log" \
		-e trace=connect,sendto,sendmsg,sendmmsg,socket,execve -e signal=none \
		"$bin" "$@"
) >"$dir/app.log" 2>&1 &
tracer=$!

# The app's PID is the one in the first execve line strace writes.
app=
for _ in $(seq 1 100); do
	if [ -s "$log" ]; then
		app="$(awk '/execve\(/ { print $1; exit }' "$log")"
		[ -n "$app" ] && break
	fi
	kill -0 "$tracer" 2>/dev/null || break
	sleep 0.1
done
[ -n "$app" ] || {
	cat "$dir/app.log" >&2
	die "the app did not start"
}

end=$((SECONDS + idle))
while [ "$SECONDS" -lt "$end" ] && kill -0 "$tracer" 2>/dev/null; do
	sleep 1
done
if kill -0 "$tracer" 2>/dev/null; then
	echo "==> closing the app (SIGTERM to $app)"
	kill -TERM "$app" 2>/dev/null || true
	for _ in $(seq 1 150); do
		kill -0 "$tracer" 2>/dev/null || break
		sleep 0.1
	done
	if kill -0 "$tracer" 2>/dev/null; then
		echo "==> still running after 15s; killing it"
		kill -KILL "$app" 2>/dev/null || true
	fi
else
	echo "==> the app exited by itself before ${idle}s (closed by hand?)"
fi
wait "$tracer" || true

report="$dir/report.txt"
awk -v allow="${allow[*]-}" '
function addr(line,    a, p) {
	a = ""; p = ""
	if (match(line, /inet_addr\("[^"]*"\)/)) a = substr(line, RSTART + 11, RLENGTH - 13)
	else if (match(line, /inet_pton\(AF_INET6, "[^"]*"/)) a = "[" substr(line, RSTART + 21, RLENGTH - 22) "]"
	if (match(line, /sin6?_port=htons\([0-9]+\)/)) { p = substr(line, RSTART, RLENGTH); sub(/.*htons\(/, "", p); sub(/\)/, "", p) }
	return a ":" p
}
function allowed(ap,    i, n, list) {
	n = split(allow, list, " ")
	for (i = 1; i <= n; i++) if (list[i] == ap) return 1
	return 0
}
BEGIN { bad = 0 }
/execve\("/ {
	if (match($0, /execve\("[^"]*"/)) progs[substr($0, RSTART + 8, RLENGTH - 9)]++
}
/ socket\(AF_INET6?,/ { inetsock++ }
/ (connect|sendto|sendmsg|sendmmsg)\(/ {
	call = $3; sub(/\(.*/, "", call)
	if ($0 ~ /sa_family=AF_INET6?,/ || $0 ~ /<(TCP|TCPv6|UDP|UDPv6|UDPLITE|UDPLITEv6):/) {
		ap = addr($0)
		if (ap != ":" && allowed(ap)) { allowedhits[call " " ap]++; next }
		bad++; inet[call " " (ap == ":" ? "(connected socket)" : ap)]++
		if (length(examples) < 4000) examples = examples "  " $0 "\n"
		next
	}
	if (call == "connect" && match($0, /sun_path=@?"[^"]*"/)) {
		path = substr($0, RSTART + 9, RLENGTH - 9); gsub(/"/, "", path)
		if (path ~ /\/run\/systemd\/resolve\/|nscd\/socket/) { bad++; resolver[path]++ }
		else unix[path]++
	}
	if (call ~ /^send/) {
		if ($0 ~ /org\.freedesktop\.resolve1/) { bad++; dbus["org.freedesktop.resolve1 (DNS)"]++ }
		if ($0 ~ /ProxyResolver|PacRunner|CanReach/) { bad++; dbus["proxy resolver / reachability"]++ }
	}
}
END {
	print "Programs started:"
	for (p in progs) printf "  %4d x %s\n", progs[p], p
	print "Unix sockets connected to (local, expected):"
	for (u in unix) printf "  %4d x %s\n", unix[u], u
	printf "AF_INET/AF_INET6 sockets created: %d\n", inetsock + 0
	for (a in allowedhits) printf "Allowed (--allow): %d x %s\n", allowedhits[a], a
	if (bad == 0) { print "Network activity: none"; exit 0 }
	print "Network activity FOUND:"
	for (i in inet) printf "  %4d x %s\n", inet[i], i
	for (r in resolver) printf "  %4d x connect to resolver %s\n", resolver[r], r
	for (d in dbus) printf "  %4d x D-Bus %s\n", dbus[d], d
	if (examples != "") { print "First lines:"; printf "%s", examples }
	exit 1
}' "$log" >"$report" && status=0 || status=$?
cat "$report"
echo "==> strace log: $log"
[ "$status" -le 1 ] || die "could not read $log"
exit "$status"
