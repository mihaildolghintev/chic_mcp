# MoySklad MCP server — local build & run helpers.
# Pure Go (no CGO), so `make build` produces a native binary on Apple Silicon.

BINARY := moysklad-mcp
BIN_DIR := bin
INSTALL_DIR := $(HOME)/.local/bin
CACHE_DIR := $(HOME)/.moysklad-mcp

# Version stamped into the binary (see internal/buildinfo). Falls back to "dev"
# outside a git checkout. The git revision/time are embedded by the toolchain.
VERSION := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X mcp.chic.md/internal/buildinfo.Version=$(VERSION)

.PHONY: build install test fmt vet ci clean run-stdio run-bot config

## build: compile a native static binary into ./bin
build:
	CGO_ENABLED=0 go build -trimpath -ldflags="$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) ./cmd/server
	@echo "built $(BIN_DIR)/$(BINARY) $(VERSION) ($$(go env GOOS)/$$(go env GOARCH))"

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

## ci: run the full CI suite locally (lint, build, race tests, security) before a PR
ci:
	./scripts/ci.sh

clean:
	rm -rf $(BIN_DIR)

## run-stdio: run locally over stdio (needs MOYSKLAD_TOKEN in the environment)
run-stdio: build
	MOYSKLAD_TOKEN=$${MOYSKLAD_TOKEN:?set MOYSKLAD_TOKEN} \
	CACHE_DB=$(CACHE_DIR)/cache.db \
	$(BIN_DIR)/$(BINARY) -transport stdio

## run-bot: run the Telegram bot locally on :8080 (pair with a cloudflared
## tunnel; PUBLIC_BASE_URL must be the tunnel's https URL). OPENAI_API_KEY is
## optional — it enables photo understanding.
run-bot: build
	@mkdir -p $(CACHE_DIR)
	TELEGRAM_BOT_TOKEN=$${TELEGRAM_BOT_TOKEN:?set TELEGRAM_BOT_TOKEN} \
	TELEGRAM_WEBHOOK_SECRET=$${TELEGRAM_WEBHOOK_SECRET:?set TELEGRAM_WEBHOOK_SECRET} \
	ALLOWED_USER_IDS=$${ALLOWED_USER_IDS:?set ALLOWED_USER_IDS} \
	PUBLIC_BASE_URL=$${PUBLIC_BASE_URL:?set PUBLIC_BASE_URL to the tunnel url} \
	MOYSKLAD_TOKEN=$${MOYSKLAD_TOKEN:?set MOYSKLAD_TOKEN} \
	DEEPSEEK_API_KEY=$${DEEPSEEK_API_KEY:?set DEEPSEEK_API_KEY} \
	CACHE_DB=$(CACHE_DIR)/cache.db \
	APP_DB=$(CACHE_DIR)/app.db \
	$(BIN_DIR)/$(BINARY)

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
