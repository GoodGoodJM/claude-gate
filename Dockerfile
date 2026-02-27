FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /claude-gate ./cmd/claude-gate

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata

# Litestream (optional: activated when LITESTREAM_S3_BUCKET is set)
ADD https://github.com/benbjohnson/litestream/releases/download/v0.3.13/litestream-v0.3.13-linux-amd64.tar.gz /tmp/litestream.tar.gz
RUN tar -xzf /tmp/litestream.tar.gz -C /usr/local/bin && rm /tmp/litestream.tar.gz

COPY --from=builder /claude-gate /usr/local/bin/claude-gate
COPY litestream.yml /etc/litestream.yml
COPY entrypoint.sh /entrypoint.sh
RUN chmod +x /entrypoint.sh && mkdir -p /data

EXPOSE 8080
ENTRYPOINT ["/entrypoint.sh"]
