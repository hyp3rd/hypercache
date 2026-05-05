# syntax=docker/dockerfile:1.7

# Multi-stage build: produce a small, distroless final image.
# Build stage uses the matching Go toolchain pinned in go.mod.
ARG GO_VERSION=1.26

FROM golang:${GO_VERSION}-alpine AS build

WORKDIR /src

# Copy go.mod / go.sum first so the dependency layer caches across
# code-only rebuilds.
COPY go.mod go.sum ./
RUN go mod download

# Copy the rest and build a static binary. CGO_ENABLED=0 so the binary
# runs unchanged on distroless/scratch; -trimpath strips local paths
# from the binary; -ldflags shrinks symbols.
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build \
    -trimpath \
    -ldflags="-s -w" \
    -o /out/hypercache-server \
    ./cmd/hypercache-server

# Final stage: distroless static, no shell, no package manager.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/hypercache-server /hypercache-server

# Default ports — override via HYPERCACHE_API_ADDR / _MGMT_ADDR /
# _DIST_ADDR if you need to bind elsewhere.
EXPOSE 8080 8081 7946

USER nonroot:nonroot

ENTRYPOINT ["/hypercache-server"]
