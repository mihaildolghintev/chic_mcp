#!/usr/bin/env bash
# Local mirror of .github/workflows/ci.yml — run before opening a PR to catch
# what CI would catch. Runs every check even if an earlier one fails, then
# exits non-zero if any failed, so you see the whole picture in one pass.
#
# Usage: make ci   (or ./scripts/ci.sh)
#
# Missing tools are installed on first run via `go install` (cached after).
# Versions are pinned to match CI where CI pins them.
set -uo pipefail

cd "$(dirname "$0")/.."

# Keep in sync with .github/workflows/ci.yml.
GOLANGCI_VERSION=v2.12.2

export PATH="$(go env GOPATH)/bin:$PATH"

fail=0
step() { printf '\n\033[1;34m== %s ==\033[0m\n' "$1"; }
ok()   { printf '\033[1;32m✓ %s\033[0m\n' "$1"; }
bad()  { printf '\033[1;31m✗ %s FAILED\033[0m\n' "$1"; fail=1; }
run()  { if "${@:2}"; then ok "$1"; else bad "$1"; fi; }

# ensure <binary> <go-install-spec>: install the tool if it isn't on PATH.
ensure() {
	command -v "$1" >/dev/null 2>&1 && return
	printf 'installing %s ...\n' "$1"
	go install "$2" || { bad "install $1"; return 1; }
}

# ---------- lint ----------
step "gofmt"
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
	echo "these files need gofmt (run: make fmt):"; echo "$unformatted"; bad "gofmt"
else
	ok "gofmt"
fi

step "go vet"
run "go vet" go vet ./...

step "golangci-lint"
ensure golangci-lint "github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$GOLANGCI_VERSION" \
	&& run "golangci-lint" golangci-lint run ./...

# ---------- build & test ----------
step "go build"
run "go build" go build ./...

step "go test -race"
run "go test -race" go test -race -coverprofile=coverage.out ./...

# ---------- security ----------
step "govulncheck"
ensure govulncheck "golang.org/x/vuln/cmd/govulncheck@latest" \
	&& run "govulncheck" govulncheck ./...

step "gosec"
ensure gosec "github.com/securego/gosec/v2/cmd/gosec@latest" \
	&& run "gosec" gosec -severity=medium -quiet ./...

step "gitleaks"
ensure gitleaks "github.com/gitleaks/gitleaks/v8@latest" \
	&& run "gitleaks" gitleaks detect --no-banner --redact

echo
if [ "$fail" -eq 0 ]; then
	printf '\033[1;32mAll CI checks passed — safe to open a PR.\033[0m\n'
else
	printf '\033[1;31mSome CI checks failed — fix before opening a PR.\033[0m\n'
fi
exit "$fail"
