BIN_DIR := bin
APP := $(BIN_DIR)/lark

.PHONY: build test clean

build:
	mkdir -p $(BIN_DIR)
	go build -o $(APP) ./cmd/lark

test:
	go test ./...

clean:
	rm -rf $(BIN_DIR)
