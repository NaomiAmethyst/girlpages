#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
#
# Builds girlpages' FreeBSD packages: girlpages (girl, girlrepo, girlc and
# girlserve, with a pkg trigger and a daily periodic script) and girlc
# alone, for FreeBSD 14 and 15 on amd64 and aarch64. In two steps, as
# staging needs Go and GNU make (and GNU install), and making packages
# needs pkg:
#
#   dist/freebsd/package.sh stage [-v VERSION] [-o DIR]   anywhere with Go
#   dist/freebsd/package.sh create [-o DIR]               on FreeBSD
#
# stage puts each package's files, manifest and plist in DIR/stage (DIR is
# dist/freebsd-packages by default); create makes them into
# DIR/NAME-VERSION-freebsdN-ARCH.pkg. The binaries are static, so one
# FreeBSD (of any version or architecture) can make them all.
set -eu

step=${1:-}
[ $# -gt 0 ] && shift
version=
out=dist/freebsd-packages
while getopts v:o: opt; do
	case $opt in
	v) version=$OPTARG ;;
	o) out=$OPTARG ;;
	*) exit 1 ;;
	esac
done
cd "$(dirname "$0")/../.."
root=$PWD
case $out in /*) ;; *) out=$root/$out ;; esac

osVersions="14 15"
arches="amd64 arm64"

# pkgVersion is a version as pkg has them, without dashes, which separate
# the name from the version in its file names: 0.0.0-git20261102 is
# 0.0.0.git20261102, 1.2.0-rc1 1.2.0.rc1.
pkgVersion() { echo "$1" | tr - .; }

# abiArch is FreeBSD's name for an architecture.
abiArch() { case $1 in arm64) echo aarch64 ;; *) echo "$1" ;; esac; }

# manifest writes a package's manifest (JSON, which UCL reads).
manifest() { # name comment desc abi [message]
	msg=
	[ -n "${5:-}" ] && msg="
  \"messages\": [{\"message\": \"$5\"}],"
	cat <<EOF
{
  "name": "$1",
  "origin": "sysutils/$1",
  "version": "$(pkgVersion "$version")",
  "comment": "$2",
  "desc": "$3",
  "maintainer": "naomi@amethyst.name",
  "www": "https://github.com/NaomiAmethyst/girlpages",
  "abi": "$4",
  "prefix": "/usr/local",
  "categories": ["sysutils"],
  "licenselogic": "single",$msg
  "licenses": ["GPLv3+"]
}
EOF
}

stage() {
	[ -n "$version" ] || version=$(dist/version.sh)
	umask 022
	tmp=$(mktemp -d)
	trap 'rm -rf "$tmp"' EXIT
	git ls-files -z --cached --others --exclude-standard | xargs -0 cp --parents -t "$tmp" 2>/dev/null || true
	rm -rf "$out/stage"
	mkdir -p "$out/stage"
	export CGO_ENABLED=0 GOOS=freebsd GOFLAGS="${GOFLAGS:-} -buildvcs=false"
	for arch in $arches; do
		export GOARCH=$arch
		vars="PREFIX=/usr/local SYSCONFDIR=/usr/local/etc MANDIR=/usr/local/share/man CACHEDIR=/var/cache/girlpages"
		# shellcheck disable=SC2086 # (a list)
		make -C "$tmp" -s -B VERSION="$version" $vars girl girlrepo girlc girlserve
		all=$tmp/root-girlpages-$arch girlc=$tmp/root-girlc-$arch
		rm -rf "$all" "$girlc"
		# shellcheck disable=SC2086
		make -C "$tmp" -s VERSION="$version" $vars HOOKS=freebsd SYSTEMD=no DESTDIR="$all" \
			-o girl -o girlrepo -o girlc -o girlserve install >/dev/null
		# shellcheck disable=SC2086
		make -C "$tmp" -s VERSION="$version" $vars DESTDIR="$girlc" -o girlc install-girlc >/dev/null
		# The config is a sample, which pkg installs as the config unless
		# there is one; the cache is a directory pkg makes.
		mv "$all/usr/local/etc/girlpages/config.toml" "$all/usr/local/etc/girlpages/config.toml.sample"
		rm -rf "$all/var"
		for osv in $osVersions; do
			abi=FreeBSD:$osv:$(abiArch "$arch")
			for pkg in girlpages girlc; do
				d=$out/stage/$pkg-freebsd$osv-$arch
				mkdir -p "$d"
				src=$all
				[ $pkg = girlc ] && src=$girlc
				cp -R "$src" "$d/root"
				if [ $pkg = girlpages ]; then
					manifest girlpages "man pages, but make them girly" \
						"girl is man, but every page is rewritten by an LLM in the voice of a Gen Z teenage girl, keeping every option, default and caveat, and cached. With it come girlrepo, which builds and publishes repositories of girlified pages and their website, girlserve, which serves them, and girlc, which reads pages from one." \
						"$abi" "💖 girlpages installed. Try: girl girl\\nPick a model in /usr/local/etc/girlpages/config.toml, then girlify everything with: girl --bulk\\nAfter that, a pkg trigger and periodic(8) keep it fresh (daily_girlpages_enable=\\\"NO\\\" in /etc/periodic.conf turns the daily run off)." >"$d/+MANIFEST"
				else
					manifest girlc "read girly man pages from a girlpages repository" \
						"girlc reads man pages, girlified by girl, straight from a girlpages repository (by default https://girlpages.dev), the way man reads them from the system: one static binary, with nothing else to install." \
						"$abi" >"$d/+MANIFEST"
				fi
				(cd "$d/root" && find . -type f | sort | sed 's|^\.||') | while read -r f; do
					case $f in
					*.sample) echo "@sample $f" ;;
					*) echo "$f" ;;
					esac
				done >"$d/plist"
				[ $pkg = girlpages ] && echo "@dir /var/cache/girlpages" >>"$d/plist"
				echo "$pkg-freebsd$osv-$arch (staged)"
			done
		done
	done
}

create() {
	command -v pkg >/dev/null || { echo "create needs FreeBSD's pkg" >&2; exit 1; }
	for d in "$out"/stage/*/; do
		d=${d%/}
		name=$(basename "$d") # girlpages-freebsd15-amd64
		pkg=${name%%-freebsd*}
		tag=${name#"$pkg"-}
		tmp=$(mktemp -d)
		pkg create -M "$d/+MANIFEST" -p "$d/plist" -r "$d/root" -o "$tmp"
		for f in "$tmp"/*.pkg; do
			base=$(basename "$f" .pkg) # girlpages-0.1.3
			mv "$f" "$out/$base-$tag.pkg"
			echo "$base-$tag.pkg"
		done
		rm -rf "$tmp"
	done
}

case $step in
stage) stage ;;
create) create ;;
*) echo "usage: dist/freebsd/package.sh stage|create [-v VERSION] [-o DIR]" >&2; exit 1 ;;
esac
