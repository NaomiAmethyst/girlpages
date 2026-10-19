# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
#
# All of girlpages (girl, girlrepo and girlc), on Debian with man-db: for
# building and serving repositories (girlrepo), and reading or searching
# them (girl -r, girlc). Built from the top of the source:
#
#   docker build -f dist/docker/girlpages.Dockerfile --build-arg VERSION=$(dist/version.sh) .
#   docker run --rm -v "$PWD/site:/site" -p 1990:1990 girlpages \
#       girlrepo -d /site serve --listen :1990
#
# (Debian's slim image leaves out packages' man pages, so there's little
# installed for girl to girlify by itself.)

FROM --platform=$BUILDPLATFORM golang:1.24 AS build
ARG TARGETOS TARGETARCH VERSION=dev GIRLC_REPO=
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 GOFLAGS=-buildvcs=false \
	make girl girlrepo girlc VERSION="$VERSION" GIRLC_REPO="$GIRLC_REPO"

FROM debian:trixie-slim
RUN apt-get update \
	&& apt-get install -y --no-install-recommends man-db less ca-certificates \
	&& rm -rf /var/lib/apt/lists/*
COPY --from=build /src/girl /src/girlrepo /src/girlc /usr/local/bin/
COPY --from=build /src/man/girl.1 /src/man/girlrepo.1 /src/man/girlc.1 /usr/local/share/man/man1/
COPY dist/config.toml /etc/girlpages/config.toml
RUN mkdir -p /var/cache/girlpages
CMD ["girl", "--help"]
