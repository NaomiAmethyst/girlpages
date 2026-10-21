# SPDX-License-Identifier: GPL-3.0-or-later
# Copyright (C) 2026 Naomi Persephone Amethyst <naomi@amethyst.name>
#
# girlserve alone, in an image of nothing else, running as nobody: it
# serves a published site from an archive (or a directory) mounted in.
# Built from the top of the source:
#
#   docker build -f dist/docker/girlserve.Dockerfile --build-arg VERSION=$(dist/version.sh) .
#   docker run --rm -p 8080:8080 -p 9090:9090 -v /data/archive:/archive:ro girlserve
#
# The site is on :8080; /healthz, /readyz and /metrics on :9090.

FROM --platform=$BUILDPLATFORM golang:1.25 AS build
ARG TARGETOS TARGETARCH VERSION=dev
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH CGO_ENABLED=0 GOFLAGS=-buildvcs=false \
	make girlserve VERSION="$VERSION"

FROM scratch
COPY --from=build /src/girlserve /girlserve
USER 65534:65534
ENV GIRLSERVE_ARCHIVE=/archive
EXPOSE 8080 9090
ENTRYPOINT ["/girlserve"]
