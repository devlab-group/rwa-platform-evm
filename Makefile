.DEFAULT_GOAL := help
SHELL := /bin/bash

.PHONY: help bootstrap format lint contracts-test signer-test server-test web-test \
        investor-web-test vectors-check dialect-check bindings-check e2e ci up down \
        security-scan platform signer embed-check

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

bootstrap: ## Install toolchains/deps for all subrepos
	# contracts/lib is gitignored with no submodule/lockfile, so install the pinned deps
	# explicitly (idempotent — skips a dep that is already present). Keep these versions in
	# sync with .github/actions/forge-deps (the CI single source of truth).
	@echo "==> contracts"; cd contracts && \
	  { [ -d lib/openzeppelin-contracts ] || forge install --no-git OpenZeppelin/openzeppelin-contracts@v5.6.1; } && \
	  { [ -d lib/forge-std ] || forge install --no-git foundry-rs/forge-std@v1.16.2; }
	@echo "==> signer";    cd signer && go mod download
	@echo "==> server";    cd server && go mod download
	@echo "==> web";       cd web && npm install
	@echo "==> investor-web"; cd investor-web && npm install

format: ## Format all code
	-cd contracts && forge fmt
	-cd signer && gofmt -w .
	-cd server && gofmt -w .
	-cd web && npm run format --if-present
	-cd investor-web && npm run format --if-present

lint: ## Lint all code (fail-closed: a lint failure fails the build)
	cd contracts && forge fmt --check
	cd signer && go vet ./...
	cd server && go vet ./...
	cd web && npm run lint --if-present
	cd investor-web && npm run lint --if-present

contracts-test: ## Foundry build + tests
	cd contracts && forge build && forge test -vv

signer-test: ## Signer unit tests (incl. golden vectors), race detector on
	cd signer && go test -race ./...

server-test: ## Server unit/integration tests, race detector on
	cd server && go test -race ./...

web-test: ## Web (admin console) typecheck + tests + build
	cd web && npm run typecheck --if-present && npm test --if-present && npm run build

investor-web-test: ## Investor example SPA typecheck + tests + build
	cd investor-web && npm run typecheck --if-present && npm test --if-present && npm run build

vectors-check: dialect-check ## Verify shared golden vectors reproduce across languages
	cd signer && go test ./internal/attestation/... -run Vectors
	cd contracts && forge test --match-contract Vectors

dialect-check: ## Cross-binary parity for the assetSchema dialect (see ADR-002/003)
	# First run fails the build on any failing case; second asserts a Dialect test
	# actually ran (guards against a rename silently matching zero tests). Package
	# globs include every package holding a Dialect* test (server: assets;
	# signer: jsonschema + profile, where the envelope differential test lives).
	cd server && go test ./internal/assets/... -run Dialect -count=1 && \
	  go test ./internal/assets/... -run Dialect -count=1 -v | grep -q -- '--- PASS: .*Dialect'
	cd signer && go test ./internal/jsonschema/... ./internal/profile/... -run Dialect -count=1 && \
	  go test ./internal/jsonschema/... ./internal/profile/... -run Dialect -count=1 -v | grep -q -- '--- PASS: .*Dialect'

bindings-check: ## Build ABIs and fail on hand-written binding drift
	cd contracts && forge build
	cd server && go run ./cmd/verifyabi

platform: ## Build the release platform binary with a FRESH embedded SPA
	# Deterministic production web build (web/package.json pins NODE_ENV=production),
	# then re-embed it into the server module tree before compiling the binary, so
	# the shipped binary can never carry a stale SPA. The built SPA is NOT committed:
	# only dist/.gitkeep is tracked and assets/ are gitignored, so it is rebuilt here
	# every release. Clear everything EXCEPT the tracked
	# placeholder so a removed/renamed asset can't linger (Vite content-hashes names).
	cd web && npm install && npm run build
	find server/internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
	cp -r web/dist/. server/internal/webui/dist/
	cd server && go build -o bin/platform ./cmd/platform
	@echo "platform binary -> server/bin/platform (SPA embedded from a fresh web/dist)"

signer: ## Build the offline signer binary (signer/bin/signer)
	cd signer && go build -o bin/signer ./cmd/signer
	@echo "signer binary -> signer/bin/signer"

embed-check: platform ## Prove the release binary builds with a freshly embedded SPA (dist is built at release, not committed)
	# The built SPA is no longer committed (only dist/.gitkeep is tracked), so a
	# committed-drift diff no longer applies. `platform` rebuilds web/, embeds it
	# fresh, and compiles the binary — proving go:embed resolves the real assets.
	@echo "embed-check OK (fresh SPA embedded into server/bin/platform)"

e2e: ## Full on-chain + HTTP-API end-to-end flow
	cd contracts && bash script/e2e.sh
	cd server && bash e2e/run_e2e.sh

ci: lint contracts-test signer-test server-test web-test investor-web-test vectors-check bindings-check embed-check ## PR gate (mirrors the fail-closed CI: lint checks formatting, it does not auto-fix — run `make format` yourself first)
	@echo "CI OK"

security-scan: ## Dependency vulnerability scan (fail-closed on reachable advisories)
	cd server && go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	cd signer && go run golang.org/x/vuln/cmd/govulncheck@latest ./...
	cd web && npm audit --audit-level=high

up: ## Start local dev stack (anvil, mongo, ipfs)
	cd docker && docker compose up -d

down: ## Stop local dev stack
	cd docker && docker compose down -v
