# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

# Copy sdk and loadtest directories so go.mod's relative path replacement works
COPY sdk ./sdk
COPY loadtest ./loadtest

WORKDIR /app/loadtest

# go mod tidy resolves local sdk dependency and generates go.sum
RUN go mod tidy

# Compile static binary
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -trimpath \
    -o /app/loadtest-app \
    ./main.go

# ── Runtime stage ──────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /app/loadtest-app /loadtest-app
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

USER nonroot:nonroot

EXPOSE 8085

ENTRYPOINT ["/loadtest-app"]
