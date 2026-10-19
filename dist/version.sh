#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
#
# Prints the version packages and images are made as: the tag (v1.2.0 is
# 1.2.0), or before any release, 0.0.0 and when the commit was made
# (0.0.0-git20261019051323).
set -eu
cd "$(dirname "$0")/.."
if tag=$(git describe --tags --exact-match --match 'v[0-9]*' 2>/dev/null); then
	echo "${tag#v}"
else
	echo "0.0.0-git$(git log -1 --format=%cd --date=format:%Y%m%d%H%M%S)"
fi
