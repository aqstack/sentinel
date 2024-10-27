.PHONY: build clean test

BINARY_DIR=bin
GO=go

build:
	@echo "Building binaries..."
	@mkdir -p $(BINARY_DIR)

clean:
	rm -rf $(BINARY_DIR)

test:
	$(GO) test -v ./...
