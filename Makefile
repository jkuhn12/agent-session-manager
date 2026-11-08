BINARY  := asm
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build test vet fmt tidy install clean

all: build

build:
	go build -ldflags "$(LDFLAGS)" -o $(BINARY) ./cmd/asm

test:
	go test ./...

vet:
	go vet ./...

fmt:
	gofmt -w .

tidy:
	go mod tidy

install:
	go install -ldflags "$(LDFLAGS)" ./cmd/asm

clean:
	rm -f $(BINARY)
