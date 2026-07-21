# ─── Stage 1: Build ─────────────────────────────────────────────────────────
FROM golang:1.25-alpine AS builder

WORKDIR /build

RUN apk add --no-cache git ca-certificates

COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 GOMAXPROCS=1 \
    go build -p=1 -ldflags="-s -w" -o /build/sentinel-server ./cmd/server/

# ─── Stage 2: Runtime ────────────────────────────────────────────────────────
FROM alpine:3.20

# Install Chromium and required dependencies
# chromium package version depends on Alpine repo; pin if needed
RUN apk add --no-cache ca-certificates tzdata chromium nss freetype harfbuzz

# Print installed Chromium version for verification
RUN echo "Installed Chromium version:" && chromium-browser --version 2>/dev/null || chromium --version 2>/dev/null || echo "chromium not found in PATH"

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

# Browser mode defaults
ENV BROWSER_ENABLED=true
ENV BROWSER_HEADLESS=true
# Auto-detect chromium path; override if needed
# ENV BROWSER_CHROME_PATH=/usr/bin/chromium-browser

EXPOSE 5005

ENTRYPOINT ["/app/sentinel-server"]
