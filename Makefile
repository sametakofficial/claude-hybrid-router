VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build build-bridge test lint install

build:
	go build -trimpath -ldflags "$(LDFLAGS)" -o claude-hybrid ./cmd/claude-hybrid

build-bridge:
	go build -o opencode-bridge ./providers/opencode

test:
	go test -race ./...

lint:
	go vet ./...

# Installs to ~/.local/bin and restarts the service if it is installed.
install: build
	install -m 755 claude-hybrid $(HOME)/.local/bin/claude-hybrid
	-systemctl --user try-restart claude-hybrid 2>/dev/null
