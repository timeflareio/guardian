# guardiand + guardianctl — guardian image
#
# The build context is THIS repository's root. It no longer needs the monorepo
# root: guardian consumes the wire contract and the crypto primitives as pinned
# public modules, so there are no sibling module directories to copy and no
# go.work to honour.
#
#   docker build -t timeflare/guardiand:dev \
#     --build-arg VERSION=$(git describe --tags --always) \
#     --build-arg COMMIT=$(git rev-parse HEAD) .
#
# Pure Go, no cgo, so the runtime image is distroless — tens of MB, no libc, no
# shell. The builder pins the exact go.mod toolchain.

FROM golang:1.26.5 AS build
WORKDIR /src

# Module graph first so source edits do not re-download modules
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

# No -ldflags and no version arg: the toolchain stamps the module version and
# VCS state itself, which is why `COPY . .` above must include .git. Keep it out
# of any .dockerignore, or the image silently reports "(devel)".
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 go build -trimpath \
    -o /out/guardiand ./cmd/guardiand && \
    CGO_ENABLED=0 go build -trimpath \
    -o /out/guardianctl ./cmd/guardianctl

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
ARG COMMIT=unknown
LABEL org.opencontainers.image.title="guardiand" \
      org.opencontainers.image.source="https://github.com/timeflareio/guardian" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"

# Both binaries ship. guardiand is the entrypoint; guardianctl is reached with
# `docker run --entrypoint guardianctl …` and holds every verb that can write or
# export key material — registration, backup, restore and rotation all have to be
# runnable on the host that holds the keys.
#
# Shipping it here does not weaken the property the split exists for. That
# property is about what is linked into the long-running process: guardiand has
# no code path that can seal or generate a key, so a compromise of its
# network-facing surface — the dashboard listener, the event subscription —
# cannot reach one. Anything able to start a second process in this container
# already holds Docker-socket access and could read the mounted key files
# directly, so guardianctl's presence on the filesystem adds nothing to it.
COPY --from=build /out/guardiand /usr/local/bin/guardiand
COPY --from=build /out/guardianctl /usr/local/bin/guardianctl

# health, metrics
EXPOSE 21000 21100
# Data home: mount a named volume at /home/nonroot/.timeflare/guardian.
# Deliberately no VOLUME directive — it would create a root-owned anonymous
# volume on every bare `docker run` and leak volumes; explicit mounts don't
# need it.

ENTRYPOINT ["guardiand"]
