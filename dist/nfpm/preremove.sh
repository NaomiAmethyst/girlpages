#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
#
# Before girlpages is removed (not upgraded: dpkg says "upgrade", rpm 1):
# the timer goes off.

case "$1" in
upgrade | 1) exit 0 ;;
esac
if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
	systemctl disable --now girlpages-update.timer >/dev/null 2>&1 || :
fi
exit 0
