#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
#
# Builds girlpages' binaries for using without a package manager:
#
#   girlc-VERSION-OS-ARCH[.exe]          girlc on its own, for each system
#                                        it runs on (one static binary)
#   girlpages-VERSION-linux-ARCH.tar.gz  girl, girlrepo, girlc and girlserve,
#                                        with their man pages, the example
#                                        config, README and LICENSE, for each
#                                        Linux architecture
#
#   dist/binaries.sh [-v VERSION] [-o DIR]
#
# VERSION defaults to dist/version.sh's, DIR to dist/binaries. As
# package.sh does, it builds in a copy of the source, with the Makefile
# (so with its flags, and GIRLC_REPO's default repository in girlc), and
# with cgo off, so each binary is static and runs on any system of its kind.
set -eu

version=
out=dist/binaries
while getopts v:o: opt; do
	case $opt in
	v) version=$OPTARG ;;
	o) out=$OPTARG ;;
	*) exit 1 ;;
	esac
done
cd "$(dirname "$0")/.."
root=$PWD
case $out in /*) ;; *) out=$root/$out ;; esac
[ -n "$version" ] || version=$(dist/version.sh)

# girlc's systems, as GOOS/GOARCH (arm is ARMv6, for every Raspberry Pi),
# and the Linux architectures the whole set is tarred for.
girlcTargets="linux/amd64 linux/arm64 linux/arm linux/386 linux/riscv64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64 freebsd/amd64"
tarArches="amd64 arm64 arm 386 riscv64"

umask 022
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
git ls-files -z --cached --others --exclude-standard | (cd "$root" && xargs -0 cp --parents -t "$tmp" 2>/dev/null) || true
mkdir -p "$out"
export CGO_ENABLED=0 GOARM=6 GOFLAGS="${GOFLAGS:-} -buildvcs=false"

# build makes binaries for a system (GOOS, GOARCH), in the copy: always,
# as the last ones there were another system's.
build() {
	os=$1 arch=$2
	shift 2
	GOOS=$os GOARCH=$arch make -C "$tmp" -s -B VERSION="$version" "$@"
}

for t in $girlcTargets; do
	os=${t%/*} arch=${t#*/} ext=
	[ "$os" = windows ] && ext=.exe
	build "$os" "$arch" girlc
	cp "$tmp/girlc" "$out/girlc-$version-$os-$arch$ext"
	echo "girlc-$version-$os-$arch$ext"
done

for arch in $tarArches; do
	name=girlpages-$version-linux-$arch
	build linux "$arch" girl girlrepo girlc girlserve
	d=$tmp/$name
	rm -rf "$d"
	mkdir -p "$d/bin" "$d/man/man1"
	cp "$tmp/girl" "$tmp/girlrepo" "$tmp/girlc" "$tmp/girlserve" "$d/bin/"
	cp "$tmp"/man/*.1 "$d/man/man1/"
	cp "$tmp/dist/config.toml" "$d/config.toml.example"
	cp "$tmp/README.md" "$tmp/LICENSE" "$d/"
	# The same tarball from the same source, whenever it's made: files in
	# order, owned by nobody in particular, dated by the commit.
	mtime=$(git -C "$root" log -1 --format=%cI 2>/dev/null || date -u +%Y-%m-%dT%H:%M:%SZ)
	tar -C "$tmp" --sort=name --owner=0 --group=0 --numeric-owner --mtime="$mtime" -cf - "$name" | gzip -9n >"$out/$name.tar.gz"
	echo "$name.tar.gz"
done
