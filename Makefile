# All tooling runs inside Docker — nothing is installed on the host except Docker.
# The tooling image (target: dev) carries ruff, mypy, pytest, bandit, pip-audit
# and uv; gitleaks runs from its own image.

COMPOSE := docker compose
TOOL    := $(COMPOSE) run --rm tooling
GITLEAKS_IMG := zricethezav/gitleaks:latest

.PHONY: help
help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'

.PHONY: lock
lock: ## Regenerate uv.lock from pyproject.toml
	$(TOOL) uv lock

.PHONY: sync
sync: ## (Re)build the tooling image after dependency changes
	$(COMPOSE) build tooling

.PHONY: fmt
fmt: ## Auto-format with ruff
	$(TOOL) ruff format .

.PHONY: lint
lint: ## Check formatting + lint (ruff)
	$(TOOL) sh -c "ruff format --check . && ruff check ."

.PHONY: typecheck
typecheck: ## Static types (mypy --strict)
	$(TOOL) mypy

.PHONY: test
test: ## Run tests (pytest)
	$(TOOL) pytest -q

.PHONY: security
security: ## Static security scan + dependency audit + secret scan
	$(TOOL) sh -c "bandit -q -r chic && pip-audit"
	docker run --rm -v $(PWD):/repo $(GITLEAKS_IMG) detect --source=/repo --no-banner --redact

.PHONY: ci
ci: lint typecheck test security ## Everything CI runs

.PHONY: migrate
migrate: ## Apply DB migrations to ./app.db (the app also does this on startup)
	$(TOOL) alembic upgrade head

.PHONY: migration
migration: ## Autogenerate a migration: make migration m="add x" (run migrate first)
	$(TOOL) alembic revision --autogenerate -m "$(m)"

.PHONY: build
build: ## Build the production runtime image
	$(COMPOSE) build app

.PHONY: run
run: ## Run the runtime image locally (needs .env)
	$(COMPOSE) up --build app

.PHONY: shell
shell: ## Open a shell in the tooling container
	$(TOOL) bash

.PHONY: clean
clean: ## Remove caches and local volumes
	$(COMPOSE) down -v --remove-orphans
	rm -rf .pytest_cache .mypy_cache .ruff_cache
