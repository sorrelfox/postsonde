#!/usr/bin/env bash
# check-size.sh: fail when a binary or package is bigger than its size
# budget (APP-05). The size:budgets CI job runs it over the build and
# package artifacts; docs/footprint.md has the budgets' reasoning.
#
#   scripts/check-size.sh <file>...
#
# The budgets are in scripts/size-budgets.txt, one per line:
#
#   <pattern> <budget>    # comment
#
# <pattern> is a shell glob matched against the file's name (not its
# directory), first match wins; <budget> is bytes, or a whole number with
# K or M (KiB, MiB). Every file given must exist and match a budget, so a
# missing artifact or a new one without a budget fails too.
set -euo pipefail

budgets="$(dirname "$0")/size-budgets.txt"
[ "$#" -gt 0 ] || {
	echo "usage: $0 <file>..." >&2
	exit 2
}
[ -f "$budgets" ] || {
	echo "error: $budgets is missing" >&2
	exit 1
}

# <budget> in bytes.
to_bytes() {
	case "$1" in
	*[!0-9KM]* | "" | K | M) return 1 ;;
	*K) echo $((${1%K} * 1024)) ;;
	*M) echo $((${1%M} * 1024 * 1024)) ;;
	*) echo "$1" ;;
	esac
}

mib() { awk -v b="$1" 'BEGIN { printf "%.1f MiB", b / 1048576 }'; }

failed=0
printf '%-7s %-60s %12s %12s %6s\n' "" file size budget used
for f in "$@"; do
	name="$(basename "$f")"
	if [ ! -f "$f" ]; then
		printf '%-7s %-60s %s\n' MISSING "$f" "(no such file)"
		failed=1
		continue
	fi
	limit=
	while read -r pattern budget _; do
		case "$pattern" in "" | \#*) continue ;; esac
		# shellcheck disable=SC2254 # the pattern is a glob on purpose
		case "$name" in
		$pattern)
			limit="$(to_bytes "$budget")" || {
				echo "error: $budgets: bad budget '$budget' for $pattern" >&2
				exit 1
			}
			break
			;;
		esac
	done <"$budgets"
	size="$(wc -c <"$f" | tr -d ' ')"
	if [ -z "$limit" ]; then
		printf '%-7s %-60s %12s %12s\n' NONE "$f" "$(mib "$size")" "(no budget in $budgets)"
		failed=1
		continue
	fi
	used="$((size * 100 / limit))%"
	status=ok
	if [ "$size" -gt "$limit" ]; then
		status=OVER
		failed=1
	fi
	printf '%-7s %-60s %12s %12s %6s\n' "$status" "$f" "$(mib "$size")" "$(mib "$limit")" "$used"
done

if [ "$failed" = 1 ]; then
	echo "check-size: failed. Make the file smaller, or raise its budget in $budgets on purpose, with the reason in the commit (docs/footprint.md)." >&2
	exit 1
fi
echo "check-size: every file is within its budget"
