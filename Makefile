BIN_DIR := bin
APP := $(BIN_DIR)/lark
INSTALL_DIR ?= $(HOME)/.local/bin
VERSION ?= $(shell git describe --tags --abbrev=0 2>/dev/null || echo dev)
COMMIT := $(shell git rev-parse --short HEAD 2>/dev/null || echo unknown)
DATE := $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
LDFLAGS := -X github.com/richardsondx/IronLark/internal/buildinfo.Version=$(VERSION) -X github.com/richardsondx/IronLark/internal/buildinfo.Commit=$(COMMIT) -X github.com/richardsondx/IronLark/internal/buildinfo.Date=$(DATE) -X github.com/richardsondx/IronLark/internal/buildinfo.RepoSlug=richardsondx/IronLark

.PHONY: build install test clean

build:
	mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(APP) ./cmd/lark

install: build
	mkdir -p $(INSTALL_DIR)
	install $(APP) $(INSTALL_DIR)/lark
	ln -sf $(INSTALL_DIR)/lark $(INSTALL_DIR)/lk

test:
	go test ./...

clean:
	rm -rf $(BIN_DIR)
