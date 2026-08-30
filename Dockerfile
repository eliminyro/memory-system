# Builder runs on the NATIVE build platform ($BUILDPLATFORM) and cross-compiles
# for the requested target arch — so a multi-arch buildx never emulates the Go
# compile under QEMU (which is minutes-slow for this dep tree).
FROM --platform=$BUILDPLATFORM golang:1.26.6-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# BUILD_* are passed at build time by the release workflow (--build-arg) and
# stamped into the server binary so /~/version reports the real release — matching
# how GoReleaser stamps the archive binaries. Dev builds fall back to the defaults.
# TARGETOS/TARGETARCH are supplied automatically by buildx per target platform.
ARG BUILD_COMMIT=unknown
ARG BUILD_VERSION=dev
ARG BUILD_DATE=
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -ldflags="-s -w -X github.com/eliminyro/memory-system/internal/version.Version=${BUILD_VERSION} -X github.com/eliminyro/memory-system/internal/version.Commit=${BUILD_COMMIT} -X github.com/eliminyro/memory-system/internal/version.Date=${BUILD_DATE}" \
    -o /memory-server ./cmd/server

# Distroless static ships ca-certificates and a nonroot user (uid 65532) and has
# no shell/package manager, so the final stage is pure COPY — no RUN, hence zero
# emulation on a multi-arch buildx, plus a smaller, shell-free image.
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /memory-server /memory-server
USER nonroot:nonroot
ENTRYPOINT ["/memory-server"]
