.PHONY: fmt vet lint test test-cover build all clean

BIN := bin/claude-gate

all: fmt vet lint test build

fmt:
	go fmt ./...

vet:
	go vet ./...

lint:
	golangci-lint run

test:
	go test -race ./...

test-cover:
	go test -race -coverprofile=coverage.out ./...
	go tool cover -html=coverage.out -o coverage.html

build:
	CGO_ENABLED=0 go build -o $(BIN) ./cmd/claude-gate

clean:
	rm -rf bin/ coverage.out coverage.html
