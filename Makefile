BINARY_NAME=ok-gobot
BUILD_DIR=bin
GO=go

# Build flags
LDFLAGS=-ldflags "-s -w"

.PHONY: all build build-small clean test deps run install

all: build

deps:
	$(GO) mod download
	$(GO) mod tidy

build: deps
	mkdir -p $(BUILD_DIR)
	$(GO) build -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/ok-gobot

build-small: deps
	mkdir -p $(BUILD_DIR)
	CGO_ENABLED=1 $(GO) build $(LDFLAGS) -o $(BUILD_DIR)/$(BINARY_NAME) ./cmd/ok-gobot

clean:
	rm -rf $(BUILD_DIR)
	$(GO) clean

test:
	$(GO) test -v ./...

run: build
	./$(BUILD_DIR)/$(BINARY_NAME)

install: build
	cp $(BUILD_DIR)/$(BINARY_NAME) $(GOPATH)/bin/

# Development commands
dev:
	$(GO) run ./cmd/ok-gobot

config-init:
	$(GO) run ./cmd/ok-gobot config init

start:
	$(GO) run ./cmd/ok-gobot start

# Cross compilation
build-linux:
	mkdir -p $(BUILD_DIR)
	GOOS=linux GOARCH=amd64 $(GO) build -o $(BUILD_DIR)/$(BINARY_NAME)-linux-amd64 ./cmd/ok-gobot

build-darwin:
	mkdir -p $(BUILD_DIR)
	GOOS=darwin GOARCH=amd64 $(GO) build -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-amd64 ./cmd/ok-gobot
	GOOS=darwin GOARCH=arm64 $(GO) build -o $(BUILD_DIR)/$(BINARY_NAME)-darwin-arm64 ./cmd/ok-gobot

build-all: build-linux build-darwin
