#!/usr/bin/env bash
# package-linux-rpm.sh: the Fedora RPM, built from the source tarball
# (scripts/package-sources.sh) the way a build service builds it: the source
# RPM from the tarball and the spec in it, exactly its BuildRequires installed
# (dnf builddep: Fedora's own Go and GTK 4 / WebKitGTK 6.0 headers), and the
# binary RPM rebuilt from nothing but that source RPM, offline, by a normal
# user. The release workflow's rpm jobs and `make rpm` run it as root in a
# clean Fedora container of the release and the target architecture
# (POSTSONDE_PIN_FEDORA_44 and _45 in scripts/pins.env); docs/packaging.md,
# RPM.
#
# Where the release's own repositories have no golang of go.mod's Go minor
# (Fedora 44 has 1.26), the build uses Go from go.dev instead, at the
# version and checksum scripts/pins.env pins, as the Linux binaries and the
# Flatpak do: installed in /usr/local/go and named in the spec's
# postsonde_goroot macro. Go links statically, so the RPM's dependencies
# don't change.
#
#   scripts/package-linux-rpm.sh TARBALL OUT_DIR
#
# Writes OUT_DIR/postsonde-<version>-1.<dist>.src.rpm, the binary
# postsonde-<version>-1.<dist>.<arch>.rpm and rpmbuild.log. The changelog
# entry is dated SOURCE_DATE_EPOCH's day (the commit's), which rpm takes for
# the package's build time and the files' times.
set -euo pipefail

usage="usage: $0 TARBALL OUT_DIR"
tarball="${1:?$usage}"
out="${2:?$usage}"
tarball="$(cd "$(dirname "$tarball")" && pwd)/$(basename "$tarball")"
mkdir -p "$out"
out="$(cd "$out" && pwd)"

version="$(basename "$tarball" | sed -n 's/^postsonde-\(.*\)\.tar\.xz$/\1/p')"
[ -n "$version" ] || {
	echo "package-linux-rpm.sh: not a source tarball: $tarball" >&2
	exit 1
}
if [ -z "${SOURCE_DATE_EPOCH:-}" ]; then
	echo "package-linux-rpm.sh: no SOURCE_DATE_EPOCH: the changelog takes today's date (not reproducible)" >&2
fi
day="$(LC_ALL=C date -u -d "@${SOURCE_DATE_EPOCH:-$(date +%s)}" '+%a %b %d %Y')"
[ "$(id -u)" = 0 ] || {
	echo "package-linux-rpm.sh: run it as root, in a clean Fedora container" >&2
	exit 1
}

echo "==> tools"
dnf -y -q install rpm-build 'dnf-command(builddep)' xz tar >/dev/null

# Fedora's golang when it is new enough for go.mod, else the pinned go.dev
# toolchain.
# shellcheck source=scripts/pins.env
. "$(dirname "$0")/pins.env"
go_minor="$(tar -xOJf "$tarball" "postsonde-$version/go.mod" | awk '$1 == "go" { split($2, v, "."); print v[1] "." v[2]; exit }')"
# rpmbuild's extra option, as su -c takes it: empty with Fedora's golang.
goroot_define=
if [ -z "$(dnf -q repoquery --whatprovides "golang >= $go_minor" 2>/dev/null)" ]; then
	case "$(uname -m)" in
	x86_64) go_arch=amd64 go_sum="$POSTSONDE_PIN_GO_SHA256_AMD64" ;;
	aarch64) go_arch=arm64 go_sum="$POSTSONDE_PIN_GO_SHA256_ARM64" ;;
	*) echo "package-linux-rpm.sh: no pinned Go for $(uname -m)" >&2; exit 1 ;;
	esac
	echo "==> Fedora $(rpm -E %fedora) has no golang >= $go_minor: Go $POSTSONDE_PIN_GO_VERSION from go.dev (scripts/pins.env)"
	curl -fsSL -o /tmp/go.tar.gz "https://go.dev/dl/go$POSTSONDE_PIN_GO_VERSION.linux-$go_arch.tar.gz"
	echo "$go_sum  /tmp/go.tar.gz" | sha256sum -c -
	rm -rf /usr/local/go
	tar -C /usr/local -xzf /tmp/go.tar.gz
	rm /tmp/go.tar.gz
	goroot_define="--define 'postsonde_goroot /usr/local/go'"
fi

id -u builder >/dev/null 2>&1 || useradd -m builder
top=/home/builder/rpmbuild
install -d -o builder "$top" "$top/SOURCES" "$top/SPECS"
install -o builder -m0644 "$tarball" "$top/SOURCES/"
# The spec as the tarball has it, with the version and the day put in.
tar -xOJf "$tarball" "postsonde-$version/packaging/rpm/postsonde.spec" |
	sed -e "s/@VERSION@/$version/g" -e "s/@DATE@/$day/g" >"$top/SPECS/postsonde.spec"
chown builder "$top/SPECS/postsonde.spec"

echo "==> source RPM"
su builder -c "rpmbuild -bs $goroot_define $top/SPECS/postsonde.spec" >/dev/null
srpm="$(ls "$top"/SRPMS/postsonde-*.src.rpm)"
echo "==> BuildRequires of $(basename "$srpm")"
dnf -y -q builddep "$srpm" >/dev/null
if [ -n "$goroot_define" ]; then
	echo "go: $(/usr/local/go/bin/go version) (go.dev)"
else
	echo "go: $(go version) (from $(rpm -qf "$(command -v go)"))"
fi
echo "webkitgtk6.0: $(rpm -q --qf '%{version}-%{release}' webkitgtk6.0-devel), gtk4: $(rpm -q --qf '%{version}-%{release}' gtk4-devel)"

echo "==> rpmbuild --rebuild (log: $out/rpmbuild.log)"
su builder -c "rpmbuild --rebuild $goroot_define $srpm" >"$out/rpmbuild.log" 2>&1 || {
	tail -40 "$out/rpmbuild.log" >&2
	exit 1
}
arch="$(uname -m)"
rm -f "$out"/postsonde-*.rpm
cp "$srpm" "$top"/RPMS/"$arch"/postsonde-*.rpm "$out/"
for rpm in "$out"/postsonde-*.rpm; do
	echo "==> $rpm ($(wc -c <"$rpm" | tr -d ' ') bytes)"
done
rpm -qp --requires "$out"/postsonde-*."$arch".rpm | grep -v '^rpmlib(' | sed 's/^/  requires: /'
