FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# BUILD_COMMIT is injected automatically by `ci build-push` (from CI_COMMIT_SHA)
# and stamped into the binary; local/dev builds without it report "unknown".
ARG BUILD_COMMIT=unknown
RUN CGO_ENABLED=0 go build \
    -ldflags="-X github.com/eliminyro/memory-system/internal/version.Commit=${BUILD_COMMIT}" \
    -o /memory-server ./cmd/server
RUN CGO_ENABLED=0 go build -o /memory-import ./cmd/import

FROM alpine:3.21
RUN apk add --no-cache ca-certificates \
    && adduser -D -H -u 10001 -s /sbin/nologin appuser
COPY --from=builder /memory-server /memory-server
COPY --from=builder /memory-import /memory-import
# Run as an unprivileged user — the server needs no root capabilities.
USER appuser
ENTRYPOINT ["/memory-server"]
