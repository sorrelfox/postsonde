#!/usr/bin/env bash
# measure-footprint.sh: measure how fast PostSonde starts and how much memory
# it uses when idle, on Linux (APP-05). docs/footprint.md has the method
# and the results.
#
#   scripts/measure-footprint.sh [-n runs] [-i idle seconds] <binary>
#
# Starts <binary> <runs> times (default 5) in the current graphical session
# (Wayland or X11), each time with a fresh, empty POSTSONDE_HOME, so the
# user's own PostSonde, settings and single-instance lock are not touched.
# Before each start the binary is dropped from the page cache. Per run:
#
#   start  ms from the exec until the log has the app's "first window
#          ready" record (internal/app/startup.go), polled every 10 ms
#   app    the startupMs of that record: the app's own count, from its Go
#          package init
#   rss    the resident memory of the process and all its children (the
#          WebKit web and network processes) after <idle> seconds (default
#          10) with the welcome screen open, and of the main process alone
#   pss    the same tree's proportional set size: shared libraries counted
#          once across the processes, so closer to what the app costs
#
# Then the app is asked to quit (SIGTERM) and the medians are printed. Only
# the binary is evicted from the cache, not the GTK and WebKit libraries:
# without root this measures a start right after boot only approximately.
set -euo pipefail

usage="usage: $0 [-n runs] [-i idle seconds] <binary>"
runs=5
idle=10
while getopts n:i: opt; do
	case "$opt" in
	n) runs="$OPTARG" ;;
	i) idle="$OPTARG" ;;
	*)
		echo "$usage" >&2
		exit 2
		;;
	esac
done
shift $((OPTIND - 1))
bin="${1:?$usage}"

die() {
	echo "error: $*" >&2
	exit 1
}

[ "$(uname -s)" = Linux ] || die "Linux only (it reads /proc); docs/footprint.md has the other OSes"
[ -n "${WAYLAND_DISPLAY:-}${DISPLAY:-}" ] || die "needs a graphical session (WAYLAND_DISPLAY or DISPLAY)"
[ -x "$bin" ] || die "$bin is not an executable"
bin="$(cd "$(dirname "$bin")" && pwd)/$(basename "$bin")"

scratch="$(mktemp -d "${TMPDIR:-/tmp}/postsonde-footprint.XXXXXX")"
pid=
cleanup() {
	if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
		kill -KILL "$pid" 2>/dev/null || true
	fi
	case "$scratch" in
	*/postsonde-footprint.*) rm -rf "$scratch" ;;
	esac
}
trap cleanup EXIT

now_ms() { echo $(($(date +%s%N) / 1000000)); }

# pid and every process below it, from the PPid field of /proc/*/stat
# (the field after the parenthesised command name).
tree() {
	local root="$1" line rest f p
	local -A parent=()
	for f in /proc/[0-9]*/stat; do
		line="$(cat "$f" 2>/dev/null)" || continue
		rest="${line##*) }"
		p="${f#/proc/}"
		p="${p%/stat}"
		read -r _ ppid _ <<<"$rest"
		parent[$p]="$ppid"
	done
	local found=("$root") changed=1 q
	while [ "$changed" = 1 ]; do
		changed=0
		for p in "${!parent[@]}"; do
			for q in "${found[@]}"; do
				if [ "${parent[$p]}" = "$q" ] && [[ " ${found[*]} " != *" $p "* ]]; then
					found+=("$p")
					changed=1
				fi
			done
		done
	done
	echo "${found[@]}"
}

# The kB value of field $2 (e.g. VmRSS:, Pss:) in /proc/<pid>/$1, summed
# over the pids that follow.
sum_kb() {
	local file="$1" field="$2" total=0 v p
	shift 2
	for p in "$@"; do
		v="$(awk -v k="$field" '$1 == k { print $2; exit }' "/proc/$p/$file" 2>/dev/null || true)"
		total=$((total + ${v:-0}))
	done
	echo "$total"
}

median() { sort -n | awk '{ v[NR] = $1 } END { if (NR % 2) print v[(NR + 1) / 2]; else print int((v[NR / 2] + v[NR / 2 + 1]) / 2) }'; }

echo "binary:  $bin ($(wc -c <"$bin" | tr -d ' ') bytes)"
echo "version: $("$bin" --version 2>&1 || true)"
echo "session: ${XDG_SESSION_TYPE:-?} ${XDG_CURRENT_DESKTOP:-}; $(uname -srm)"
echo "runs: $runs, idle: ${idle}s"
printf '%-4s %8s %8s %10s %10s %10s %6s\n' run start_ms app_ms rss_tree_kB pss_tree_kB rss_main_kB procs

: >"$scratch/results"
for i in $(seq 1 "$runs"); do
	home="$scratch/home-$i"
	mkdir -p "$home"
	log="$home/logs/postsonde.log"
	# Drop the binary from the page cache (GNU dd: count=0 with nocache
	# drops the whole file), so each start reads it from disk.
	dd if="$bin" iflag=nocache count=0 status=none 2>/dev/null || true

	t0="$(now_ms)"
	POSTSONDE_HOME="$home" "$bin" >"$scratch/out-$i" 2>&1 &
	pid=$!
	ready=
	while :; do
		if [ -f "$log" ] && grep -q 'first window ready' "$log"; then
			ready="$(now_ms)"
			break
		fi
		kill -0 "$pid" 2>/dev/null || {
			cat "$scratch/out-$i" >&2
			die "run $i: the app exited before its window was ready"
		}
		[ $(($(now_ms) - t0)) -lt 30000 ] || die "run $i: no 'first window ready' in $log after 30 s"
		sleep 0.01
	done
	app_ms="$(grep -o 'startupMs[":= ]*[0-9]*' "$log" | head -n1 | grep -o '[0-9]*$' || true)"

	sleep "$idle"
	read -r -a procs <<<"$(tree "$pid")"
	rss_tree="$(sum_kb status VmRSS: "${procs[@]}")"
	pss_tree="$(sum_kb smaps_rollup Pss: "${procs[@]}")"
	rss_main="$(sum_kb status VmRSS: "$pid")"
	if [ "$i" = 1 ]; then
		echo "processes: $(for p in "${procs[@]}"; do cat "/proc/$p/comm" 2>/dev/null; done | tr '\n' ' ')"
	fi

	kill -TERM "$pid" 2>/dev/null || true
	for _ in $(seq 50); do
		kill -0 "$pid" 2>/dev/null || break
		sleep 0.1
	done
	kill -KILL "$pid" 2>/dev/null || true
	wait "$pid" 2>/dev/null || true
	pid=

	start=$((ready - t0))
	printf '%-4s %8s %8s %10s %10s %10s %6s\n' "$i" "$start" "${app_ms:--}" "$rss_tree" "$pss_tree" "$rss_main" "${#procs[@]}"
	echo "$start ${app_ms:-0} $rss_tree $pss_tree $rss_main" >>"$scratch/results"
	sleep 1
done

col() { awk -v c="$1" '{ print $c }' "$scratch/results" | median; }
printf '%-4s %8s %8s %10s %10s %10s\n' median "$(col 1)" "$(col 2)" "$(col 3)" "$(col 4)" "$(col 5)"
