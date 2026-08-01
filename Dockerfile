# Builder runs on the NATIVE build platform ($BUILDPLATFORM) and cross-compiles
# for the requested target arch — so a multi-arch buildx never emulates the Go
# compile under QEMU (which is minutes-slow for this dep tree).
FROM --platform=$BUILDPLATFORM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# BUILD_COMMIT is passed at build time (the release workflow sets it via
# --build-arg) and stamped into the binary; builds without it report "unknown".
# TARGETOS/TARGETARCH are supplied automatically by buildx per target platform.
ARG BUILD_COMMIT=unknown
ARG TARGETOS TARGETARCH
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -ldflags="-X github.com/eliminyro/memory-system/internal/version.Commit=${BUILD_COMMIT}" \
    -o /memory-server ./cmd/server
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /memory-import ./cmd/import
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /memory-admin ./cmd/admin

# Distroless static ships ca-certificates and a nonroot user (uid 65532) and has
# no shell/package manager, so the final stage is pure COPY — no RUN, hence zero
# emulation on a multi-arch buildx, plus a smaller, shell-free image.
FROM gcr.io/distroless/static:nonroot
COPY --from=builder /memory-server /memory-server
COPY --from=builder /memory-import /memory-import
COPY --from=builder /memory-admin /memory-admin
USER nonroot:nonroot
ENTRYPOINT ["/memory-server"]
