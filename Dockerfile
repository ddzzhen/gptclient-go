# ─── Stage 1: Build ─────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOMAXPROCS=1 \
    go build -p=1 -ldflags="-s -w" -o /build/sentinel-server ./cmd/server/

# ─── Stage 2: Runtime (slim, no Chromium) ──────────────────────────────────────
FROM alpine:3.20

RUN apk add --no-cache ca-certificates tzdata

ENV TZ=Asia/Shanghai

WORKDIR /app

COPY --from=builder /build/sentinel-server .

RUN mkdir -p /app/images /app/data

# Default environment variables
ENV HOST=0.0.0.0
ENV PORT=5005
ENV DEFAULT_MODEL=gpt-5-5-thinking
ENV TEMP_MODE=false
ENV IMAGE_DIR=/app/images
ENV TOKENS_FILE=/app/tokens.json
ENV SESSION_TTL_MINUTES=120
ENV DATA_DIR=/app/data

# Browser mode disabled by default for low-resource VPS.
# Credentials are injected via tokens.json + browser_session.json
# (extracted from a real browser on a separate machine).
ENV BROWSER_ENABLED=false
ENV BROWSER_SESSION_FILE=/app/data/browser_session.json

EXPOSE 5005

ENTRYPOINT ["/app/sentinel-server"]
