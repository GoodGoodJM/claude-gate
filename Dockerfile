FROM golang:1.26-alpine AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -o /claude-gate ./cmd/claude-gate

FROM alpine:3.21
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /claude-gate /usr/local/bin/claude-gate
EXPOSE 8080
ENTRYPOINT ["claude-gate"]
