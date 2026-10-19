# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
#
#   make && sudo make install     system-wide, with package-manager hooks
#   make install-user             just for you, under ~/.local
#   make install-girlc            just girlc, which reads pages from a repo
#
# Both installs put in girl, girlrepo and girlc, with their man pages.
#
# Packagers: set DESTDIR, PREFIX=/usr, SYSTEMDUNITDIR=/usr/lib/systemd/system,
# PACMANHOOKDIR=/usr/share/libalpm/hooks and HOOKS as appropriate.

PREFIX         ?= /usr/local
BINDIR         ?= $(PREFIX)/bin
LIBEXECDIR     ?= $(PREFIX)/libexec/girlpages
MANDIR         ?= $(PREFIX)/share/man
SYSCONFDIR     ?= /etc
CACHEDIR       ?= /var/cache/girlpages
SYSTEMDUNITDIR ?= /etc/systemd/system
APTCONFDIR     ?= $(SYSCONFDIR)/apt/apt.conf.d
PACMANHOOKDIR  ?= $(SYSCONFDIR)/pacman.d/hooks
DNFACTIONDIR   ?= $(SYSCONFDIR)/dnf/plugins/post-transaction-actions.d
DESTDIR        ?=

# Which package-manager hooks to install: auto (whatever's on this
# machine), none, or a list from: apt pacman dnf
HOOKS   ?= auto
# Install systemd units: auto (if systemd is running), yes or no.
SYSTEMD ?= auto

# The repository girlc reads if none is configured.
GIRLC_REPO ?=

USER_PREFIX     ?= $(HOME)/.local
USER_CONFIG_DIR ?= $(or $(XDG_CONFIG_HOME),$(HOME)/.config)

GO      ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
PKG     := github.com/NaomiAmethyst/girlpages
LDFLAGS := -s -w \
	-X main.version=$(VERSION) \
	-X $(PKG)/internal/config.SysconfDir=$(SYSCONFDIR) \
	-X $(PKG)/internal/config.SystemCacheDir=$(CACHEDIR)

SUBST = sed -e 's|@BINDIR@|$(1)|g' -e 's|@LIBEXECDIR@|$(LIBEXECDIR)|g'

# (override, so these work given on the command line too.)
ifeq ($(HOOKS),auto)
  override HOOKS := $(strip \
    $(if $(shell command -v dpkg 2>/dev/null),apt) \
    $(if $(shell command -v pacman 2>/dev/null),pacman) \
    $(if $(shell command -v dnf 2>/dev/null),dnf))
endif
ifeq ($(HOOKS),none)
  override HOOKS :=
endif
ifeq ($(SYSTEMD),auto)
  override SYSTEMD := $(if $(wildcard /run/systemd/system),yes,no)
endif

.PHONY: all build test check e2e lint install install-user install-girlrepo install-girlc uninstall uninstall-user clean

all: build

build: girl girlrepo girlc

SOURCES := $(shell find cmd internal -name '*.go' -o -name '*.html' -o -name '*.css' -o -name '*.js') go.mod go.sum

girl: $(SOURCES)
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $@ ./cmd/girl

# girlrepo builds girlpages repositories and websites.
girlrepo: $(SOURCES)
	$(GO) build -trimpath -ldflags '$(LDFLAGS)' -o $@ ./cmd/girlrepo

# girlc reads pages from a repository, as man does from the system: for
# machines (containers, say) that don't need girl itself, or man pages. It's
# static, with no cgo, so it runs anywhere (musl, distroless, scratch).
girlc: $(SOURCES)
	CGO_ENABLED=0 $(GO) build -trimpath -ldflags '$(LDFLAGS) -X main.defaultRepo=$(GIRLC_REPO)' -o $@ ./cmd/girlc

test:
	$(GO) test ./...

check: test
	$(GO) vet ./...
	@test -z "$$(gofmt -l .)" || { gofmt -l .; echo 'run gofmt -w .'; exit 1; }

# Type-checks the website's JavaScript (JSDoc types; see
# internal/site/static/tsconfig.json) with TypeScript, fetched by npx and
# cached: nothing's built, and the site needs none of it.
TSC ?= npx --yes -p typescript@7.0.2 tsc
lint:
	@command -v npx >/dev/null || { echo "lint needs npx (Node.js)"; exit 1; }
	$(TSC) -p internal/site/static/tsconfig.json

# The website's navigation, in a real browser (needs Python's playwright and
# man; skipped without them).
e2e: girlrepo
	python3 test/e2e/navigation.py ./girlrepo

install-girlrepo: girlrepo
	install -Dm755 girlrepo '$(DESTDIR)$(BINDIR)/girlrepo'
	install -Dm644 man/girlrepo.1 '$(DESTDIR)$(MANDIR)/man1/girlrepo.1'

install-girlc: girlc
	install -Dm755 girlc '$(DESTDIR)$(BINDIR)/girlc'
	install -Dm644 man/girlc.1 '$(DESTDIR)$(MANDIR)/man1/girlc.1'

install: girl install-girlrepo install-girlc
	install -Dm755 girl '$(DESTDIR)$(BINDIR)/girl'
	install -Dm644 man/girl.1 '$(DESTDIR)$(MANDIR)/man1/girl.1'
	install -dm755 '$(DESTDIR)$(LIBEXECDIR)'
	$(call SUBST,$(BINDIR)) dist/girlpages-hook.in > '$(DESTDIR)$(LIBEXECDIR)/girlpages-hook'
	chmod 755 '$(DESTDIR)$(LIBEXECDIR)/girlpages-hook'
	@if [ ! -e '$(DESTDIR)$(SYSCONFDIR)/girlpages/config.toml' ]; then \
		install -Dm644 dist/config.toml '$(DESTDIR)$(SYSCONFDIR)/girlpages/config.toml'; \
	else echo 'keeping existing $(SYSCONFDIR)/girlpages/config.toml'; fi
	install -dm755 '$(DESTDIR)$(CACHEDIR)'
ifeq ($(SYSTEMD),yes)
	install -dm755 '$(DESTDIR)$(SYSTEMDUNITDIR)'
	$(call SUBST,$(BINDIR)) dist/systemd/girlpages-update.service.in > '$(DESTDIR)$(SYSTEMDUNITDIR)/girlpages-update.service'
	install -m644 dist/systemd/girlpages-update.timer '$(DESTDIR)$(SYSTEMDUNITDIR)/girlpages-update.timer'
endif
ifneq ($(filter apt,$(HOOKS)),)
	install -dm755 '$(DESTDIR)$(APTCONFDIR)'
	$(call SUBST,$(BINDIR)) dist/apt/80girlpages.in > '$(DESTDIR)$(APTCONFDIR)/80girlpages'
endif
ifneq ($(filter pacman,$(HOOKS)),)
	install -dm755 '$(DESTDIR)$(PACMANHOOKDIR)'
	$(call SUBST,$(BINDIR)) dist/pacman/girlpages.hook.in > '$(DESTDIR)$(PACMANHOOKDIR)/girlpages.hook'
endif
ifneq ($(filter dnf,$(HOOKS)),)
	install -dm755 '$(DESTDIR)$(DNFACTIONDIR)'
	$(call SUBST,$(BINDIR)) dist/dnf/girlpages.action.in > '$(DESTDIR)$(DNFACTIONDIR)/girlpages.action'
endif
ifeq ($(SYSTEMD)$(DESTDIR),yes)
	systemctl daemon-reload
	systemctl enable --now girlpages-update.timer
endif
	@echo
	@echo '💖 girlpages installed. Try: girl girl'
	@echo '   Pick a model in $(SYSCONFDIR)/girlpages/config.toml, then'
	@echo '   girlify everything with: sudo girl --bulk'
ifneq ($(HOOKS)$(filter yes,$(SYSTEMD)),)
	@echo '   After that, the $(strip $(if $(HOOKS),$(HOOKS) hooks)$(if $(HOOKS),$(if $(filter yes,$(SYSTEMD)), and)) $(if $(filter yes,$(SYSTEMD)),daily timer)) will keep it fresh ✨'
endif

install-user: girl girlrepo girlc
	for b in girl girlrepo girlc; do \
		install -Dm755 $$b '$(USER_PREFIX)/bin/'$$b && \
		install -Dm644 man/$$b.1 '$(USER_PREFIX)/share/man/man1/'$$b.1 || exit 1; \
	done
	@if [ ! -e '$(USER_CONFIG_DIR)/girlpages/config.toml' ]; then \
		install -Dm644 dist/config.toml '$(USER_CONFIG_DIR)/girlpages/config.toml'; \
	else echo 'keeping existing $(USER_CONFIG_DIR)/girlpages/config.toml'; fi
ifeq ($(SYSTEMD),yes)
	install -dm755 '$(USER_CONFIG_DIR)/systemd/user'
	$(call SUBST,$(USER_PREFIX)/bin) dist/systemd/user/girlpages-update.service.in > '$(USER_CONFIG_DIR)/systemd/user/girlpages-update.service'
	install -m644 dist/systemd/user/girlpages-update.timer '$(USER_CONFIG_DIR)/systemd/user/girlpages-update.timer'
	-systemctl --user daemon-reload && systemctl --user enable --now girlpages-update.timer
endif
	@echo
	@echo '💖 girlpages installed in $(USER_PREFIX). Try: girl girl'
	@case ":$$PATH:" in *':$(USER_PREFIX)/bin:'*) ;; *) echo '   (add $(USER_PREFIX)/bin to your PATH first)';; esac
	@if [ -n "$$MANPATH" ]; then echo '   $$MANPATH is set, so add $(USER_PREFIX)/share/man to it for girl(1), girlrepo(1) and girlc(1)'; fi
	@echo '   Girlify all your man pages with: girl --bulk'

uninstall:
	-[ -z '$(DESTDIR)' ] && command -v systemctl >/dev/null && systemctl disable --now girlpages-update.timer
	rm -f '$(DESTDIR)$(BINDIR)/girl' '$(DESTDIR)$(MANDIR)/man1/girl.1' \
		'$(DESTDIR)$(BINDIR)/girlrepo' '$(DESTDIR)$(MANDIR)/man1/girlrepo.1' \
		'$(DESTDIR)$(BINDIR)/girlc' '$(DESTDIR)$(MANDIR)/man1/girlc.1' \
		'$(DESTDIR)$(LIBEXECDIR)/girlpages-hook' \
		'$(DESTDIR)$(SYSTEMDUNITDIR)/girlpages-update.service' \
		'$(DESTDIR)$(SYSTEMDUNITDIR)/girlpages-update.timer' \
		'$(DESTDIR)$(APTCONFDIR)/80girlpages' \
		'$(DESTDIR)$(PACMANHOOKDIR)/girlpages.hook' \
		'$(DESTDIR)$(DNFACTIONDIR)/girlpages.action'
	-rmdir '$(DESTDIR)$(LIBEXECDIR)'
	@echo 'Left $(SYSCONFDIR)/girlpages and $(CACHEDIR) in place; remove them yourself if you like.'

uninstall-user:
	-systemctl --user disable --now girlpages-update.timer
	rm -f '$(USER_PREFIX)/bin/girl' '$(USER_PREFIX)/share/man/man1/girl.1' \
		'$(USER_PREFIX)/bin/girlrepo' '$(USER_PREFIX)/share/man/man1/girlrepo.1' \
		'$(USER_PREFIX)/bin/girlc' '$(USER_PREFIX)/share/man/man1/girlc.1' \
		'$(USER_CONFIG_DIR)/systemd/user/girlpages-update.service' \
		'$(USER_CONFIG_DIR)/systemd/user/girlpages-update.timer'
	@echo 'Left $(USER_CONFIG_DIR)/girlpages and your cache in place.'

clean:
	rm -f girl girlrepo girlc
