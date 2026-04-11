FROM golang:1.25-alpine AS builder
WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /memory-server ./cmd/server
RUN CGO_ENABLED=0 go build -o /memory-import ./cmd/import

FROM alpine:3.21
RUN apk add --no-cache ca-certificates
COPY --from=builder /memory-server /memory-server
COPY --from=builder /memory-import /memory-import
ENTRYPOINT ["/memory-server"]
