# ── Build stage ───────────────────────────────────────────────────────────────
FROM golang:1.23-alpine AS builder

WORKDIR /build

# Download dependencies first so this layer is cached separately from source.
COPY go.mod go.sum ./
RUN go mod download

# Build a fully static binary (no libc dependency in the runtime image).
COPY *.go ./
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o compexchange .

# ── Runtime stage ─────────────────────────────────────────────────────────────
FROM alpine:3.20

# ca-certificates: required for HTTPS calls to the Discord API.
# tzdata: correct timestamps in log output.
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /app

COPY --from=builder /build/compexchange .
COPY frontend/ frontend/

# comps/ is intentionally NOT copied here — mount it as a volume at runtime
# so competitions can be added or removed without rebuilding the image.
RUN mkdir comps

EXPOSE 8080

CMD ["./compexchange"]
