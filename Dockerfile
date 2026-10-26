# Multi-stage build for minimal container image
FROM golang:alpine AS builder

WORKDIR /app

# Copy source code and build static binary
COPY go.mod ./
COPY . .

RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o talus .

# Final minimal deployment image
FROM alpine:latest

# Install CA certificates for HTTPS requests to APIs, and tzdata for correct timezones
RUN apk --no-cache add ca-certificates tzdata

WORKDIR /data

COPY --from=builder /app/talus /usr/local/bin/talus

# Environment defaults
ENV PORT=8080 \
    SYNC_INTERVAL=1h \
    CHECK_UPDATES_INTERVAL=24h \
    DATA_DIR=/data

EXPOSE 8080

VOLUME ["/data"]

ENTRYPOINT ["talus"]
CMD ["serve", "/data"]
