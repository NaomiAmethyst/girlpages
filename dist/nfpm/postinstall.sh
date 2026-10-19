#!/bin/sh
# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
#
# After girlpages is installed or upgraded: the daily refresh timer goes on,
# as make install does, if systemd is running (not in a container, say).

if command -v systemctl >/dev/null 2>&1 && [ -d /run/systemd/system ]; then
	systemctl daemon-reload >/dev/null 2>&1 || :
	systemctl enable --now girlpages-update.timer >/dev/null 2>&1 || :
fi
exit 0
