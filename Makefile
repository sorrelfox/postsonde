# PostSonde local builds: build, check and package on a developer machine,
# without CI. A thin wrapper: every target runs a script in scripts/, so
# everything also works without make. docs/building.md.
#
# Portable make: works with GNU make and BSD make (no GNU-only syntax).
#
#   make linux ARCH=arm64       narrow the Linux targets to one architecture
#   make build VERSION=0.1.0    override the version stamp (scripts/version.sh)

ARCH =
FEDORA =
VERSION = $(PKG_VERSION)

.PHONY: help setup dev build package check check-go check-frontend bindings \
	macos linux linux-gtk4 linux-gtk3 linux-packages sources rpm flatpak windows \
	verify-reproducible clean distclean clean-docker

help:
	@echo 'PostSonde local builds (docs/building.md). Targets:'
	@echo ''
	@echo '  setup           check Go, wails3 and Node against the pinned versions; npm ci'
	@echo '  dev             wails3 dev: Vite dev server + live-rebuilt app'
	@echo '  build           production binary for this OS: bin/postsonde'
	@echo '  package         macOS: bin/postsonde.app (Linux: a .deb in dist/)'
	@echo '  check           everything CI checks: check-go, then check-frontend'
	@echo '  check-go        gofmt, core-deps guard, go vet, race tests, bindings check'
	@echo '  check-frontend  CSS-token check, npm ci, tsc, Vitest, frontend build,'
	@echo '                  no-remote-URL/CSP check of the build'
	@echo '  bindings        regenerate the committed frontend/bindings'
	@echo '  macos           universal PostSonde.app, .dmg and SHA256SUMS in dist/macos/'
	@echo '                  (macOS only; ad-hoc signed)'
	@echo '  linux           Linux binaries (gtk4, gtk3) + .deb, AppImage, SHA256SUMS'
	@echo '                  in dist/linux/, through Docker'
	@echo '  linux-gtk4      Linux gtk4 binaries only (Docker)'
	@echo '  linux-gtk3      Linux gtk3 binaries only (Docker)'
	@echo '  linux-packages  .deb, AppImage and SHA256SUMS from the binaries in dist/linux/'
	@echo '  sources         source tarball (tree, frontend build, vendored Go modules)'
	@echo '                  in dist/linux/, through Docker'
	@echo '  rpm             Fedora RPM from the source tarball, installed and started in'
	@echo '                  a clean Fedora container; in dist/linux/, through Docker'
	@echo '  flatpak         Flatpak bundle from the source tarball, built and started in'
	@echo '                  a container (x86_64); in dist/linux/, through Docker'
	@echo '  windows         Windows executables, NSIS installers and SHA256SUMS'
	@echo '                  in dist/windows/, through Docker'
	@echo '  verify-reproducible  build HEAD twice and compare every output file'
	@echo '                  (frontend, macOS on a Mac, Linux with Docker)'
	@echo '  clean           remove bin/, dist/, frontend/dist/* and the build caches'
	@echo '  distclean       clean, plus frontend/node_modules'
	@echo '  clean-docker    remove the postsonde-* Docker cache volumes'
	@echo ''
	@echo 'Variables: ARCH=amd64|arm64 (Linux and Windows targets; default both,'
	@echo '           rpm: Docker'"'"'s own),'
	@echo '           FEDORA=44|45|all (rpm; default: this machine'"'"'s Fedora release'
	@echo '           when it is 44 or 45, otherwise 45),'
	@echo '           VERSION=<x.y.z> (default: scripts/version.sh, from git)'

setup:
	@scripts/setup.sh

dev:
	@scripts/wails.sh dev

build:
	@PKG_VERSION='$(VERSION)' scripts/wails.sh build

package:
	@PKG_VERSION='$(VERSION)' scripts/wails.sh package

# Sequential rather than prerequisites, so `make -j check` doesn't run
# npm ci while the Go side regenerates the bindings.
check:
	@scripts/check-go.sh
	@scripts/check-frontend.sh

check-go:
	@scripts/check-go.sh

check-frontend:
	@scripts/check-frontend.sh

bindings:
	@scripts/wails.sh bindings

macos:
	@PKG_VERSION='$(VERSION)' scripts/package-macos.sh

linux:
	@PKG_VERSION='$(VERSION)' scripts/linux.sh all $(ARCH)

linux-gtk4:
	@PKG_VERSION='$(VERSION)' scripts/linux.sh build gtk4 $(ARCH)

linux-gtk3:
	@PKG_VERSION='$(VERSION)' scripts/linux.sh build gtk3 $(ARCH)

linux-packages:
	@PKG_VERSION='$(VERSION)' scripts/linux.sh packages $(ARCH)

sources:
	@PKG_VERSION='$(VERSION)' scripts/linux.sh sources

rpm:
	@PKG_VERSION='$(VERSION)' FEDORA='$(FEDORA)' scripts/linux.sh rpm $(ARCH)

flatpak:
	@PKG_VERSION='$(VERSION)' scripts/linux.sh flatpak

windows:
	@PKG_VERSION='$(VERSION)' scripts/windows.sh $(ARCH)

verify-reproducible:
	@PKG_VERSION='$(VERSION)' ARCH='$(ARCH)' scripts/verify-reproducible.sh

clean:
	@scripts/clean.sh

distclean:
	@scripts/clean.sh distclean

clean-docker:
	@scripts/clean.sh docker
