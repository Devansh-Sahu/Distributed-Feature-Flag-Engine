# syntax=docker/dockerfile:1

# ── Stage 1: Build ──────────────────────────────────────────────
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

# Copy go.mod only first — go mod tidy will resolve and generate go.sum
COPY go.mod ./

# Copy source files before go mod tidy so it can see all imports
COPY . .

# go mod tidy resolves the full dependency graph from go.mod and
# generates go.sum inside the container — no local Go install needed.
RUN go mod tidy

# Compile static binary
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -trimpath \
    -o /app/worker \
    ./main.go

# ── Stage 2: Minimal runtime ─────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /app/worker /worker
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

EXPOSE 9095

ENTRYPOINT ["/worker"]
