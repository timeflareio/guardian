# guardiand — guardian daemon image
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
    -o /out/guardiand ./cmd/guardiand

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
ARG COMMIT=unknown
LABEL org.opencontainers.image.title="guardiand" \
      org.opencontainers.image.source="https://github.com/timeflareio/guardian" \
      org.opencontainers.image.version="${VERSION}" \
      org.opencontainers.image.revision="${COMMIT}"

COPY --from=build /out/guardiand /usr/local/bin/guardiand

# health, metrics
EXPOSE 21000 21100
# Data home: mount a named volume at /home/nonroot/.timeflare/guardian.
# Deliberately no VOLUME directive — it would create a root-owned anonymous
# volume on every bare `docker run` and leak volumes; explicit mounts don't
# need it.

ENTRYPOINT ["guardiand"]
