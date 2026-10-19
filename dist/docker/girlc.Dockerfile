# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
#
# girlc alone, in an image of nothing else: it needs no libraries, and
# brings its own root certificates. Built from the top of the source:
#
#   docker build -f dist/docker/girlc.Dockerfile --build-arg VERSION=$(dist/version.sh) .
#   docker run --rm -e GIRLC_REPO=https://example.org/repo girlc ls
#
# GIRLC_REPO as a build argument builds a default repository in.

FROM --platform=$BUILDPLATFORM golang:1.24 AS build
ARG TARGETOS TARGETARCH VERSION=dev GIRLC_REPO=
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 GOFLAGS=-buildvcs=false \
	make girlc VERSION="$VERSION" GIRLC_REPO="$GIRLC_REPO"

FROM scratch
COPY --from=build /src/girlc /girlc
ENTRYPOINT ["/girlc"]
