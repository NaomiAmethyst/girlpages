#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
#
# Builds girlpages' packages for one architecture: girlpages (girl,
# girlrepo, girlc and girlserve, with each package manager's hook and the daily timer)
# and girlc alone, as deb, rpm, apk and Arch packages, with nfpm.
#
#   dist/package.sh [-a ARCH] [-v VERSION] [-o DIR]
#
# ARCH is Go's (amd64, arm64; default this machine's); VERSION defaults to
# dist/version.sh's; DIR to dist/packages. The source is
# built in a copy, so the binaries here aren't swapped for another
# architecture's. GIRLC_REPO builds a default repository into girlc.
set -eu

arch=$(go env GOARCH)
version=
out=dist/packages
while getopts a:v:o: opt; do
	case $opt in
	a) arch=$OPTARG ;;
	v) version=$OPTARG ;;
	o) out=$OPTARG ;;
	*) exit 1 ;;
	esac
done
cd "$(dirname "$0")/.."
root=$PWD
case $out in /*) ;; *) out=$root/$out ;; esac
nfpm=${NFPM:-nfpm}
command -v "$nfpm" >/dev/null || { echo "package.sh needs nfpm (https://nfpm.goreleaser.com)" >&2; exit 1; }

[ -n "$version" ] || version=$(dist/version.sh)
# A version's release and what comes before it (1.2.0-rc1: 1.2.0 and rc1),
# written as each kind of package wants it, so a pre-release sorts first:
# 1.2.0~rc1 for deb and rpm, 1.2.0_rc1 for apk and Arch.
base=${version%%-*}
pre=$(printf %s "${version#"$base"}" | tr -cd '[:alnum:]')
versionFor() {
	if [ -z "$pre" ]; then
		echo "$base"
	else
		case $1 in deb | rpm) echo "$base~$pre" ;; *) echo "${base}_$pre" ;; esac
	fi
}

# Packages' files are root's, and only theirs to write.
umask 022
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
# The source, as it is here (committed or not), without what's built.
git ls-files -z --cached --others --exclude-standard | (cd "$root" && xargs -0 cp --parents -t "$tmp" 2>/dev/null) || true
mkdir -p "$out"

build() { # make, in the copy, for arch
	make -C "$tmp" -s GOOS=linux GOARCH="$arch" CGO_ENABLED=0 VERSION="$version" "$@"
}
# (The copy has no .git to stamp the build with; VERSION says it.)
export GOOS=linux GOARCH="$arch" CGO_ENABLED=0 GOFLAGS="${GOFLAGS:-} -buildvcs=false"
build girl girlrepo girlc girlserve

for format in deb rpm apk archlinux; do
	case $format in
	deb) hooks=apt ;;
	rpm) hooks=dnf ;;
	archlinux) hooks=pacman ;;
	apk) hooks=none ;;
	esac
	stage=$tmp/stage-$format
	build install DESTDIR="$stage" PREFIX=/usr SYSCONFDIR=/etc CACHEDIR=/var/cache/girlpages \
		SYSTEMDUNITDIR=/usr/lib/systemd/system PACMANHOOKDIR=/usr/share/libalpm/hooks \
		HOOKS="$hooks" SYSTEMD=yes >/dev/null
	# The config is the package's to keep across upgrades, and the cache
	# a directory it makes (nfpm.yaml); neither's a plain file.
	rm -rf "$stage/etc/girlpages" "$stage/var/cache/girlpages"
	rmdir -p "$stage/var/cache" 2>/dev/null || :
	# Each staged file, listed (a tree would have the package own /usr
	# and /etc themselves); nfpm takes ARCH and VERSION from the
	# environment, but not paths.
	{
		cat dist/nfpm/girlpages.yaml
		(cd "$stage" && find . -type f | sort) | while read -r f; do
			f=${f#.}
			printf '  - src: %s\n    dst: %s\n' "$stage$f" "$f"
			case $f in /etc/*) printf '    type: config\n' ;; esac
			printf '    file_info:\n      mode: 0%s\n' "$(stat -c %a "$stage$f")"
		done
	} >"$tmp/girlpages.yaml"
	sed "s|\${GIRLC}|$tmp/girlc|" dist/nfpm/girlc.yaml >"$tmp/girlc.yaml"
	v=$(versionFor "$format")
	ARCH=$arch VERSION=$v "$nfpm" package -f "$tmp/girlpages.yaml" -p "$format" -t "$out/" >/dev/null
	ARCH=$arch VERSION=$v "$nfpm" package -f "$tmp/girlc.yaml" -p "$format" -t "$out/" >/dev/null
done
ls -1 "$out"
