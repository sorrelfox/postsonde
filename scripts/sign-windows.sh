#!/usr/bin/env bash
# sign-windows.sh: sign a Windows executable in place with a code-signing
# certificate, using osslsigncode. scripts/package-windows-installer.sh calls
# it for the app, and makensis for the uninstaller and the installer
# (build/windows/nsis/project.nsi), only when a certificate is set; without
# one, everything stays unsigned (docs/releasing.md, Signing).
#
#   scripts/sign-windows.sh FILE
#
#   POSTSONDE_WINDOWS_CERTIFICATE           the certificate, a .pfx file
#   POSTSONDE_WINDOWS_CERTIFICATE_PASSWORD  its password (may be empty)
#   POSTSONDE_WINDOWS_TIMESTAMP_URL         an RFC 3161 timestamp server
#                                           (default DigiCert's)
set -euo pipefail

file="${1:?usage: $0 FILE}"
certificate="${POSTSONDE_WINDOWS_CERTIFICATE:?sign-windows.sh: POSTSONDE_WINDOWS_CERTIFICATE names no certificate}"
[ -f "$certificate" ] || {
	echo "sign-windows.sh: $certificate is missing" >&2
	exit 1
}
command -v osslsigncode >/dev/null 2>&1 || {
	echo "sign-windows.sh: osslsigncode not found (Debian/Ubuntu: apt-get install osslsigncode)" >&2
	exit 1
}

# The password goes through a pipe, not the command line.
signed="$file.signed"
rm -f "$signed"
osslsigncode sign -pkcs12 "$certificate" \
	-readpass <(printf '%s' "${POSTSONDE_WINDOWS_CERTIFICATE_PASSWORD:-}") \
	-n PostSonde -i https://github.com/sorrelfox/postsonde -h sha256 \
	-ts "${POSTSONDE_WINDOWS_TIMESTAMP_URL:-http://timestamp.digicert.com}" \
	-in "$file" -out "$signed" >/dev/null
mv -f "$signed" "$file"
echo "signed: $file"
