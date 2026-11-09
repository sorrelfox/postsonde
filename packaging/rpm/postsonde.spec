# PostSonde as a Fedora RPM: the GTK 4 + WebKitGTK 6.0 build, from the source
# tarball (scripts/package-sources.sh: the tree, the frontend's production
# build and the vendored Go modules) with Fedora's own Go, offline. A
# release whose golang is older than go.mod's Go (Fedora 44) builds with the
# go.dev toolchain scripts/pins.env pins instead: package-linux-rpm.sh
# installs it and names it in the macro postsonde_goroot.
# scripts/package-linux-rpm.sh makes the source RPM and rebuilds it as a
# normal user in a clean Fedora container, the way a build service does;
# `make rpm` runs that through Docker (docs/packaging.md, RPM).
#
# A template: package-linux-rpm.sh puts the version in for @VERSION@ and the
# day of the commit for @DATE@.

Name:           postsonde
Version:        @VERSION@
Release:        1%{?dist}
Summary:        Local-only desktop API testing tool

# PostSonde is MIT. The binary bundles Go modules (MIT, BSD-3-Clause,
# BSD-2-Clause), Protocol Buffers and googleapis files (BSD-3-Clause,
# Apache-2.0), the frontend's npm packages (MIT) and two fonts (OFL-1.1):
# THIRD_PARTY_NOTICES.md lists them.
License:        MIT AND BSD-3-Clause AND BSD-2-Clause AND Apache-2.0 AND OFL-1.1
URL:            https://github.com/sorrelfox/postsonde
Source0:        %{name}-%{version}.tar.xz
ExclusiveArch:  x86_64 aarch64

%if %{undefined postsonde_goroot}
BuildRequires:  golang >= 1.27
%endif
BuildRequires:  gcc
BuildRequires:  pkgconfig(gtk4)
BuildRequires:  pkgconfig(webkitgtk-6.0)
BuildRequires:  pkgconfig(x11)
BuildRequires:  desktop-file-utils
BuildRequires:  appstream

# The icons go into hicolor's folders.
Requires:       hicolor-icon-theme

# The binary is built stripped (-ldflags "-w -s"), as every other package's
# is: there is no debug information to split off.
%global debug_package %{nil}

%description
PostSonde sends HTTP and GraphQL requests and shows the responses. It runs
entirely on your machine, with no account, cloud sync or telemetry.

%prep
%setup -q

%build
# The production build of the other packages (scripts/ci-build-linux.sh),
# with the vendored modules and the frontend build from the tarball: no
# download, no other Go than Fedora's.
%if %{defined postsonde_goroot}
export PATH=%{postsonde_goroot}/bin:$PATH
%endif
export GOTOOLCHAIN=local GOFLAGS=-mod=vendor GOPROXY=off CGO_ENABLED=1
export CGO_CFLAGS="-O2 -g -Wno-deprecated-declarations"
go build -tags production -trimpath -buildvcs=false \
    -ldflags "-w -s -X main.version=%{version}" \
    -o postsonde ./cmd/postsonde

%install
id=io.github.sorrelfox.PostSonde
install -Dm0755 postsonde %{buildroot}%{_bindir}/postsonde
install -Dm0644 build/linux/$id.desktop %{buildroot}%{_datadir}/applications/$id.desktop
install -Dm0644 build/linux/$id.metainfo.xml %{buildroot}%{_metainfodir}/$id.metainfo.xml
mkdir -p %{buildroot}%{_datadir}/icons
cp -R assets/icons/linux/hicolor %{buildroot}%{_datadir}/icons/
install -Dm0644 frontend/src/assets/fonts/instrument-sans/OFL.txt fonts/instrument-sans/OFL.txt
install -Dm0644 frontend/src/assets/fonts/jetbrains-mono/OFL.txt fonts/jetbrains-mono/OFL.txt

%check
desktop-file-validate %{buildroot}%{_datadir}/applications/io.github.sorrelfox.PostSonde.desktop
appstreamcli validate --no-net %{buildroot}%{_metainfodir}/io.github.sorrelfox.PostSonde.metainfo.xml
# The packaged binary runs (--version needs no display).
%{buildroot}%{_bindir}/postsonde --version

%files
%license LICENSE fonts
%doc THIRD_PARTY_NOTICES.md
%{_bindir}/postsonde
%{_datadir}/applications/io.github.sorrelfox.PostSonde.desktop
%{_metainfodir}/io.github.sorrelfox.PostSonde.metainfo.xml
%{_datadir}/icons/hicolor/*/apps/postsonde.*

%changelog
* @DATE@ Sorrelfox <348316453+sorrelfox@users.noreply.github.com> - @VERSION@-1
- Built from the PostSonde @VERSION@ source tarball.
