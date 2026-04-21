.PHONY: build build-bridge test lint clean cross

build:
	go build -o claude-hybrid ./cmd/claude-hybrid

build-bridge:
	go build -o opencode-bridge ./providers/opencode

test:
	go test ./... -v

lint:
	go vet ./...

clean:
	rm -f claude-hybrid opencode-bridge claude-hybrid-*

cross:
	GOOS=linux   GOARCH=amd64 go build -o claude-hybrid-linux-amd64       ./cmd/claude-hybrid
	GOOS=linux   GOARCH=arm64 go build -o claude-hybrid-linux-arm64       ./cmd/claude-hybrid
	GOOS=darwin  GOARCH=amd64 go build -o claude-hybrid-darwin-amd64      ./cmd/claude-hybrid
	GOOS=darwin  GOARCH=arm64 go build -o claude-hybrid-darwin-arm64      ./cmd/claude-hybrid
	GOOS=windows GOARCH=amd64 go build -o claude-hybrid-windows-amd64.exe ./cmd/claude-hybrid
	GOOS=windows GOARCH=arm64 go build -o claude-hybrid-windows-arm64.exe ./cmd/claude-hybrid
