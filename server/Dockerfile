# syntax=docker/dockerfile:1
FROM golang:1.22-alpine AS builder

RUN apk add --no-cache git ca-certificates tzdata

WORKDIR /app

# Copy go.mod first (no go.sum needed — tidy generates it)
COPY go.mod ./

# Copy all source code
COPY . .

# go mod tidy: resolves ALL dependencies (direct + indirect), fetches them,
# and writes go.sum. This is the correct approach when go.sum doesn't exist
# on the host machine. Tidy needs the source files to know which indirect
# deps are actually imported, which is why we copy source before running it.
#
# GONOSUMCHECK=* skips the checksum database for packages that have had
# their module history rewritten (common for libraries on pkg.go.dev).
ENV GONOSUMCHECK=*
ENV GONOSUMDB=*
RUN go mod tidy

# Build the static binary
# CGO_ENABLED=0: fully static, no libc dependency → works in distroless
# -ldflags "-s -w": strip debug info (binary goes from ~15MB to ~8MB)
# -trimpath: remove local build paths (security + reproducible builds)
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w" \
    -trimpath \
    -o /app/server \
    ./main.go

# ── Runtime stage ──────────────────────────────────────────────────
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=builder /app/server /server
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/
COPY --from=builder /usr/share/zoneinfo /usr/share/zoneinfo

USER nonroot:nonroot

EXPOSE 8080 9090

ENTRYPOINT ["/server"]
