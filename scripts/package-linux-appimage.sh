#!/usr/bin/env bash
# package-linux-appimage.sh: package the gtk3 Linux binary as an AppImage.
# The linux:appimage:<arch> CI jobs run it; docs/packaging.md explains the
# choices.
#
#   scripts/package-linux-appimage.sh <amd64|arm64>
#
# Runs as root on ubuntu:22.04 of the target architecture: linuxdeploy finds
# the libraries to bundle with ldd, so this can't cross-package. It wraps
# bin/postsonde-linux-gtk3-<arch> (scripts/ci-build-linux.sh) together with
# GTK 3, WebKit2GTK 4.1 and their libraries from Ubuntu 22.04, so the
# AppImage runs on distributions with glibc 2.35 or newer.
#
# The AppImage tooling is downloaded from GitHub at pinned releases and each
# file is checked against the SHA-256 below before it runs. The GTK plugin
# for linuxdeploy is the copy Wails v3.0.0 ships
# (build/linux/appimage/linuxdeploy-plugin-gtk.sh), so nothing else is
# fetched. The AppImage carries no update information: it never looks for
# updates.
#
# Version: scripts/version.sh (PKG_VERSION, a vX.Y.Z tag on HEAD without
# the "v", or 0.0.0~git<short sha>).
#
# Writes dist/PostSonde-<version>-<x86_64|aarch64>.AppImage.
set -euo pipefail

usage="usage: $0 <amd64|arm64>"
arch="${1:?$usage}"
case "$arch" in
amd64)
	appimage_arch=x86_64
	triple=x86_64-linux-gnu
	;;
arm64)
	appimage_arch=aarch64
	triple=aarch64-linux-gnu
	;;
*)
	echo "$usage" >&2
	exit 2
	;;
esac

# Pinned AppImage tooling. The checksums are the SHA-256 digests GitHub lists
# for each release asset. To bump: pick a tagged release (never
# "continuous"), then update the version and both checksums.
linuxdeploy_version=1-alpha-20251107-1
appimagetool_version=1.9.1
runtime_version=20251108
declare -A sha256=(
	[linuxdeploy-x86_64.AppImage]=c20cd71e3a4e3b80c3483cef793cda3f4e990aca14014d23c544ca3ce1270b4d
	[linuxdeploy-aarch64.AppImage]=620095110d693282b8ebeb244a95b5e911cf8f65f76c88b4b47d16ae6346fcff
	[appimagetool-x86_64.AppImage]=ed4ce84f0d9caff66f50bcca6ff6f35aae54ce8135408b3fa33abfc3cb384eb0
	[appimagetool-aarch64.AppImage]=f0837e7448a0c1e4e650a93bb3e85802546e60654ef287576f46c71c126a9158
	[runtime-x86_64]=2fca8b443c92510f1483a883f60061ad09b46b978b2631c807cd873a47ec260d
	[runtime-aarch64]=00cbdfcf917cc6c0ff6d3347d59e0ca1f7f45a6df1a428a0d6d8a78664d87444
)
linuxdeploy="linuxdeploy-$appimage_arch.AppImage"
appimagetool="appimagetool-$appimage_arch.AppImage"
runtime="runtime-$appimage_arch"
declare -A url=(
	[$linuxdeploy]="https://github.com/linuxdeploy/linuxdeploy/releases/download/$linuxdeploy_version/$linuxdeploy"
	[$appimagetool]="https://github.com/AppImage/appimagetool/releases/download/$appimagetool_version/$appimagetool"
	[$runtime]="https://github.com/AppImage/type2-runtime/releases/download/$runtime_version/$runtime"
)

cd "$(dirname "$0")/.."
root="$PWD"

host="$(dpkg --print-architecture)"
if [ "$host" != "$arch" ]; then
	echo "an $arch AppImage must be built on $arch (this is $host); e.g. docker run --platform linux/$arch" >&2
	exit 1
fi
# shellcheck source=/dev/null
os_version="$(. /etc/os-release && echo "$ID $VERSION_ID")"
if [ "$os_version" != "ubuntu 22.04" ]; then
	echo "build the AppImage on Ubuntu 22.04 (this is $os_version): its glibc sets the oldest system the AppImage runs on" >&2
	exit 1
fi

binary="bin/postsonde-linux-gtk3-$arch"
if [ ! -f "$binary" ]; then
	echo "$binary is missing: build it first (scripts/ci-build-linux.sh gtk3 $arch)" >&2
	exit 1
fi

version="$(scripts/version.sh)"
# The mksquashfs appimagetool runs (squashfs-tools 4.4 and later) sets the
# filesystem time and every inode time to SOURCE_DATE_EPOCH when it is set:
# the commit time, so the squashfs doesn't record the build time.
# Two amd64 builds came out identical; docs/reproducible-builds.md.
if sde="$(scripts/source-date-epoch.sh)"; then
	export SOURCE_DATE_EPOCH="$sde"
fi

export DEBIAN_FRONTEND=noninteractive
echo "==> installing GTK 3, WebKit2GTK 4.1 and the linuxdeploy GTK plugin's tools"
apt-get -o Acquire::Retries=5 update -qq
# libgtk-3-0 and libwebkit2gtk-4.1-0 are what gets bundled. The GTK plugin
# needs dpkg-architecture (dpkg-dev), pkg-config, glib-compile-schemas
# (libglib2.0-bin) and gtk-query-immodules-3.0 (libgtk-3-bin);
# librsvg2-common adds the SVG loader for GTK's symbolic icons.
apt-get -o Acquire::Retries=5 install -y -qq --no-install-recommends \
	ca-certificates curl file dpkg-dev pkg-config libglib2.0-bin \
	libgtk-3-0 libgtk-3-bin librsvg2-common libwebkit2gtk-4.1-0

# The GTK plugin copies the GObject introspection typelib directory
# unconditionally. PostSonde doesn't use introspection, so give it an empty
# one rather than bundling typelibs nothing loads.
mkdir -p "/usr/lib/$triple/girepository-1.0"

work="$root/build/linux/appimage/build/$arch"
tools="$work/tools"
mkdir -p "$tools"
for file in "$linuxdeploy" "$appimagetool" "$runtime"; do
	if [ -f "$tools/$file" ] && echo "${sha256[$file]}  $tools/$file" | sha256sum -c --quiet - 2>/dev/null; then
		continue
	fi
	echo "==> downloading $file"
	curl -fsSL -o "$tools/$file" "${url[$file]}"
	echo "${sha256[$file]}  $tools/$file" | sha256sum -c -
done
# AppImages mark themselves with "AI\x02" at ELF offset 8. Under emulation
# (qemu via binfmt_misc, as `make linux` packages the other architecture)
# the header then no longer matches the emulator's ELF pattern and the
# kernel refuses to start the tool: "Exec format error". Run copies with
# those three bytes zeroed. The verified downloads stay as they are, and
# the runtime that goes into the PostSonde AppImage is never touched.
for file in "$linuxdeploy" "$appimagetool"; do
	cp "$tools/$file" "$tools/run-$file"
	printf '\0\0\0' | dd of="$tools/run-$file" bs=1 seek=8 count=3 conv=notrunc status=none
	chmod +x "$tools/run-$file"
done
linuxdeploy="run-$linuxdeploy"
appimagetool="run-$appimagetool"
# linuxdeploy finds plugins on PATH as linuxdeploy-plugin-<name>.sh.
install -m 0755 build/linux/appimage/linuxdeploy-plugin-gtk.sh "$tools/"
export PATH="$tools:$PATH"
# The tools are AppImages themselves; containers have no FUSE.
export APPIMAGE_EXTRACT_AND_RUN=1

# The AppDir lives in a temporary directory inside the container: on a
# Docker Desktop bind mount of a macOS checkout, linuxdeploy's file copies
# fail at random with "Permission denied".
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT
appdir="$tmp/PostSonde.AppDir"
echo "==> AppDir $appdir"
install -D -m 0755 "$binary" "$appdir/usr/bin/postsonde"
# The GTK plugin's AppRun hook sets GDK_BACKEND=x11, so the window always
# carries an X11 WM_CLASS. The app sets its program name to the app ID, so
# that is io.github.sorrelfox.PostSonde, as the Wayland app_id is, and the
# desktop file is used as it is (docs/packaging.md).
install -D -m 0644 build/linux/io.github.sorrelfox.PostSonde.desktop "$appdir/usr/share/applications/io.github.sorrelfox.PostSonde.desktop"
install -D -m 0644 build/linux/io.github.sorrelfox.PostSonde.metainfo.xml "$appdir/usr/share/metainfo/io.github.sorrelfox.PostSonde.metainfo.xml"
mkdir -p "$appdir/usr/share/icons"
cp -R assets/icons/linux/hicolor "$appdir/usr/share/icons/"
doc="$appdir/usr/share/doc/postsonde"
install -D -m 0644 LICENSE "$doc/LICENSE"
install -D -m 0644 THIRD_PARTY_NOTICES.md "$doc/THIRD_PARTY_NOTICES.md"
install -D -m 0644 frontend/src/assets/fonts/instrument-sans/OFL.txt "$doc/fonts/instrument-sans/OFL.txt"
install -D -m 0644 frontend/src/assets/fonts/jetbrains-mono/OFL.txt "$doc/fonts/jetbrains-mono/OFL.txt"

# WebKit2GTK starts its web and network processes from, and loads its
# injected bundle out of, a directory compiled into the library:
# /usr/lib/<triple>/webkit2gtk-4.1. Bundle that directory (minus the
# MiniBrowser demo) and, further down, point the library at the bundled copy.
webkit_dir="/usr/lib/$triple/webkit2gtk-4.1"
mkdir -p "$appdir$webkit_dir"
for helper in WebKitWebProcess WebKitNetworkProcess WebKitGPUProcess injected-bundle; do
	if [ -e "$webkit_dir/$helper" ]; then
		cp -a "$webkit_dir/$helper" "$appdir$webkit_dir/"
	fi
done
# The bundled path is relative ("././/lib/..." instead of "/usr/lib/...",
# the same length, see below), so the app must start in $APPDIR/usr. This
# AppRun hook does that; the directory the user started from is in $OWD,
# which the AppImage runtime sets.
mkdir -p "$appdir/apprun-hooks"
cat >"$appdir/apprun-hooks/postsonde-webkit.sh" <<'EOF'
# WebKit2GTK finds its bundled helper processes relative to the working
# directory (see scripts/package-linux-appimage.sh in the PostSonde repo).
cd "${APPDIR:-$this_dir}/usr"
EOF

webkit_elves=()
while IFS= read -r -d '' file; do
	webkit_elves+=("--deploy-deps-only=$file")
done < <(find "$appdir$webkit_dir" -type f \( -perm -u+x -o -name '*.so' \) -print0)

echo "==> linuxdeploy $linuxdeploy_version (GTK plugin, GTK 3)"
cd "$tmp"
# Ubuntu's libraries are already stripped, and linuxdeploy's bundled strip
# can't handle newer ELF sections; skip stripping.
DEPLOY_GTK_VERSION=3 NO_STRIP=1 "$tools/$linuxdeploy" \
	--appdir "$appdir" \
	--executable "$appdir/usr/bin/postsonde" \
	--desktop-file "$appdir/usr/share/applications/io.github.sorrelfox.PostSonde.desktop" \
	--icon-file "$root/assets/icons/linux/hicolor/256x256/apps/postsonde.png" \
	"${webkit_elves[@]}" \
	--plugin gtk

# The GTK plugin copies GTK's modules (input methods, print backends) and
# the pixbuf loaders, which GTK loads with dlopen, but linuxdeploy never
# sees them: their own libraries (libtiff, librsvg, ...) aren't bundled and
# they have no RPATH. Drop the print backends (PostSonde doesn't print, and
# the CUPS backend would pull in CUPS), then let a second linuxdeploy pass
# bundle the rest's dependencies and point them at usr/lib.
find "$appdir/usr/lib/gtk-3.0" -type d -name printbackends -prune -exec rm -rf {} +
find "$appdir/usr/lib" -maxdepth 1 -xtype l -delete
modules=()
while IFS= read -r -d '' file; do
	modules+=("--deploy-deps-only=$file")
done < <(find "$appdir/usr/lib/gtk-3.0" "$appdir/usr/lib/gdk-pixbuf-2.0" -type f -name '*.so' -print0)
echo "==> linuxdeploy: dependencies of ${#modules[@]} GTK modules and pixbuf loaders"
NO_STRIP=1 "$tools/$linuxdeploy" --appdir "$appdir" "${modules[@]}"
cd "$root"

# Point the bundled libwebkit2gtk at the bundled helpers: rewrite the
# compiled-in /usr/lib/<triple>/webkit2gtk-4.1 to ././/lib/<triple>/..., the
# same length, which resolves against $APPDIR/usr (the AppRun hook's working
# directory). Tauri and AppImageKit's AppRun do the same.
libwebkit="$(readlink -f "$appdir/usr/lib/libwebkit2gtk-4.1.so.0")"
relative="././${webkit_dir#/usr}"
before="$(grep -a -o -F "$webkit_dir" "$libwebkit" | wc -l)"
if [ "$before" -lt 2 ]; then
	echo "expected $webkit_dir at least twice in $libwebkit, found $before" >&2
	exit 1
fi
sed -i "s|$webkit_dir|$relative|g" "$libwebkit"
after="$(grep -a -o -F "$relative" "$libwebkit" | wc -l)"
if grep -q -a -F "$webkit_dir" "$libwebkit" || [ "$after" -ne "$before" ]; then
	echo "failed to rewrite $webkit_dir in $libwebkit" >&2
	exit 1
fi
echo "rewrote $before paths in $libwebkit to $relative"

# linuxdeploy points .DirIcon at the scalable SVG; file managers' AppImage
# thumbnailers expect a PNG there.
rm -f "$appdir/.DirIcon"
install -m 0644 assets/icons/linux/hicolor/256x256/apps/postsonde.png "$appdir/.DirIcon"

if ! grep -q postsonde-webkit.sh "$appdir/AppRun"; then
	echo "AppRun doesn't source the postsonde-webkit.sh hook" >&2
	exit 1
fi
# linuxdeploy's AppRun execs the binary through the AppRun.wrapped symlink,
# so the process would be named "AppRun.wrapped" (in ps, and GLib's default
# program name). Exec usr/bin/postsonde instead, as in the .deb. The window
# class doesn't depend on it any more: the app sets the program name to its
# app ID itself.
# shellcheck disable=SC2016 # $this_dir and $@ are literal AppRun text
sed -i 's|^exec "$this_dir"/AppRun.wrapped |exec "$this_dir"/usr/bin/postsonde |' "$appdir/AppRun"
# shellcheck disable=SC2016 # same
if ! grep -q '^exec "$this_dir"/usr/bin/postsonde "$@"$' "$appdir/AppRun"; then
	echo "failed to point AppRun at usr/bin/postsonde:" >&2
	cat "$appdir/AppRun" >&2
	exit 1
fi

mkdir -p dist
out="dist/PostSonde-$version-$appimage_arch.AppImage"
echo "==> appimagetool $appimagetool_version, runtime $runtime_version -> $out"
ARCH="$appimage_arch" VERSION="$version" "$tools/$appimagetool" \
	--no-appstream --runtime-file "$tools/$runtime" "$appdir" "$out"
size="$(stat -c %s "$out")"
echo "==> $out ($size bytes, $(numfmt --to=iec "$size"))"
