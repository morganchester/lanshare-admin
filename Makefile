.PHONY: build test clean

VERSION ?= dev
DIST := dist
LDFLAGS := -s -w -X main.version=$(VERSION)

build:
	npm ci
	npm run css
	mkdir -p $(DIST)
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/lanshare-admin-linux-amd64 ./cmd/server
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/lanshare-helper-linux-amd64 ./cmd/helper
	CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "$(LDFLAGS)" -o $(DIST)/lanshare-auth-linux-amd64 ./cmd/authctl

test:
	go test ./...

clean:
	find $(DIST) -mindepth 1 -delete 2>/dev/null || true
