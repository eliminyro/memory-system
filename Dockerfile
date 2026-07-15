FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# BUILD_COMMIT is passed at build time (the release workflow sets it via
# --build-arg) and stamped into the binary; builds without it report "unknown".
ARG BUILD_COMMIT=unknown
RUN CGO_ENABLED=0 go build \
    -ldflags="-X github.com/eliminyro/memory-system/internal/version.Commit=${BUILD_COMMIT}" \
    -o /memory-server ./cmd/server
RUN CGO_ENABLED=0 go build -o /memory-import ./cmd/import
RUN CGO_ENABLED=0 go build -o /memory-admin ./cmd/admin

FROM alpine:3.21
RUN apk add --no-cache ca-certificates \
    && adduser -D -H -u 10001 -s /sbin/nologin appuser
COPY --from=builder /memory-server /memory-server
COPY --from=builder /memory-import /memory-import
COPY --from=builder /memory-admin /memory-admin
# Run as an unprivileged user — the server needs no root capabilities.
USER appuser
ENTRYPOINT ["/memory-server"]
