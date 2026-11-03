# syntax=docker/dockerfile:1

# Must match the go directive in go.mod; CI fails the build if they drift.
ARG GO_VERSION=1.27.3

FROM --platform=$BUILDPLATFORM golang:${GO_VERSION}-alpine AS build
WORKDIR /src

COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod \
    go mod download

COPY . .
ARG TARGETOS TARGETARCH
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH \
    go build -trimpath -ldflags="-s -w" -o /out/rateguard ./cmd/rateguard

# Static binary on distroless: no shell, no package manager, non-root user.
FROM gcr.io/distroless/static-debian13:nonroot
COPY --from=build /out/rateguard /rateguard
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/rateguard"]
