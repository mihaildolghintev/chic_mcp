# MoySklad MCP server — local build & run helpers.
# Pure Go (no CGO), so `make build` produces a native binary on Apple Silicon.

BINARY := moysklad-mcp
BIN_DIR := bin
INSTALL_DIR := $(HOME)/.local/bin
CACHE_DIR := $(HOME)/.moysklad-mcp

.PHONY: build install test fmt vet clean run-stdio run-http config

## build: compile a native static binary into ./bin
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN_DIR)/$(BINARY) ./cmd/server
	@echo "built $(BIN_DIR)/$(BINARY) ($$(go env GOOS)/$$(go env GOARCH))"

## install: build and copy the binary to ~/.local/bin
install: build
	@mkdir -p $(INSTALL_DIR)
	cp $(BIN_DIR)/$(BINARY) $(INSTALL_DIR)/$(BINARY)
	@echo "installed to $(INSTALL_DIR)/$(BINARY)"

## test: run the full test suite with the race detector
test:
	go test -race ./...

fmt:
	gofmt -w .

vet:
	go vet ./...

clean:
	rm -rf $(BIN_DIR)

## run-stdio: run locally over stdio (needs MOYSKLAD_TOKEN in the environment)
run-stdio: build
	MOYSKLAD_TOKEN=$${MOYSKLAD_TOKEN:?set MOYSKLAD_TOKEN} \
	CACHE_DB=$(CACHE_DIR)/cache.db \
	$(BIN_DIR)/$(BINARY) -transport stdio

## run-http: run a local HTTP server on :8080 (needs MOYSKLAD_TOKEN, MCP_BEARER_TOKEN)
run-http: build
	MOYSKLAD_TOKEN=$${MOYSKLAD_TOKEN:?set MOYSKLAD_TOKEN} \
	MCP_BEARER_TOKEN=$${MCP_BEARER_TOKEN:?set MCP_BEARER_TOKEN} \
	CACHE_DB=$(CACHE_DIR)/cache.db \
	$(BIN_DIR)/$(BINARY) -transport http

## config: print a ready-to-paste Claude Desktop connector config
config: install
	@mkdir -p $(CACHE_DIR)
	@echo 'Add this to ~/Library/Application Support/Claude/claude_desktop_config.json:'
	@echo ''
	@echo '{'
	@echo '  "mcpServers": {'
	@echo '    "moysklad": {'
	@echo '      "command": "$(INSTALL_DIR)/$(BINARY)",'
	@echo '      "args": ["-transport", "stdio"],'
	@echo '      "env": {'
	@echo '        "MOYSKLAD_TOKEN": "PUT-YOUR-MOYSKLAD-TOKEN-HERE",'
	@echo '        "CACHE_DB": "$(CACHE_DIR)/cache.db"'
	@echo '      }'
	@echo '    }'
	@echo '  }'
	@echo '}'
