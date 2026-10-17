.DEFAULT_GOAL := help
SHELL := /bin/bash

.PHONY: help bootstrap format lint contracts-test signer-test server-test web-test \
        investor-web-test web-e2e investor-web-e2e vectors-check dialect-check bindings-check \
        e2e ci up down \
        security-scan platform signer embed-check

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | \
	  awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'

bootstrap: ## Install dependencies for each module
	# contracts/lib is gitignored with no submodule or lockfile, so install the pinned deps
	# explicitly (idempotent: a dep that is already present is skipped). Keep these versions in
	# sync with .github/actions/forge-deps, the source of truth for CI.
	@echo "==> contracts"; cd contracts && \
	  { [ -d lib/openzeppelin-contracts ] || forge install --no-git OpenZeppelin/openzeppelin-contracts@v5.6.1; } && \
	  { [ -d lib/forge-std ] || forge install --no-git foundry-rs/forge-std@v1.16.2; }
	@echo "==> signer";    cd signer && go mod download
	@echo "==> server";    cd server && go mod download
	@echo "==> web";       cd web && npm install && npx playwright install chromium
	@echo "==> investor-web"; cd investor-web && npm install && npx playwright install chromium

format: ## Format all code
	-cd contracts && forge fmt
	-cd signer && gofmt -w .
	-cd server && gofmt -w .
	-cd web && npm run format --if-present
	-cd investor-web && npm run format --if-present

lint: ## Check Solidity formatting, Go code and web lint
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

# Keep Playwright's mocked API on the preview origin by overriding any .env API URL
web-e2e: ## Web (admin console) Playwright suite against the built SPA
	cd web && npm run typecheck:e2e && VITE_API_BASE_URL= npm run test:e2e

investor-web-e2e: ## Investor example SPA Playwright suite against the built SPA
	cd investor-web && npm run typecheck:e2e && VITE_API_BASE_URL= npm run test:e2e

vectors-check: dialect-check ## Verify shared golden vectors reproduce across languages
	cd signer && go test ./internal/attestation/... -run Vectors
	cd contracts && forge test --match-contract Vectors
	# ERC-7943 ABI surface: every language asserts its own fragments against
	# shared/vectors/erc7943-abi.json, so one drifting selector fails here.
	# The web line needs web/node_modules, so run `make bootstrap` first on a fresh checkout.
	cd server && go test ./internal/bindings/... -run Vectors -count=1
	# Check the server tree against the same OpenZeppelin vectors as the contracts
	cd server && go test ./internal/income -run TestIncomeMerkleVectors -count=1
	# Mint and burn EIP-712 digests
	cd server && go test ./internal/eip712 -run 'Typehashes|GoldenVector' -count=1
	cd web && npm test -- --run src/lib/abis.test.ts src/lib/vectors.test.ts

dialect-check: ## Cross-binary parity for the assetSchema dialect
	# The first run fails on any failing case; the second asserts that a Dialect test
	# ran, so a rename cannot silently match zero tests. The package globs cover every
	# package holding a Dialect test (server: assets; signer: jsonschema and profile,
	# where the envelope differential test lives).
	cd server && go test ./internal/assets/... -run Dialect -count=1 && \
	  go test ./internal/assets/... -run Dialect -count=1 -v | grep -q -- '--- PASS: .*Dialect'
	cd signer && go test ./internal/jsonschema/... ./internal/profile/... -run Dialect -count=1 && \
	  go test ./internal/jsonschema/... ./internal/profile/... -run Dialect -count=1 -v | grep -q -- '--- PASS: .*Dialect'

bindings-check: ## Build ABIs and fail on hand-written binding drift
	cd contracts && forge build
	cd server && go run ./cmd/verifyabi

platform: ## Build the release binary with a fresh embedded admin app
	# Build the web app for production (the build script sets NODE_ENV=production), then
	# embed it into the server module tree before compiling the binary, so the binary
	# never carries a stale SPA. The built SPA is not committed: only dist/.gitkeep is
	# tracked and assets/ is gitignored. Everything except that placeholder is cleared
	# first so a removed or renamed asset cannot linger (Vite content-hashes file names).
	cd web && npm install && npm run build
	find server/internal/webui/dist -mindepth 1 ! -name .gitkeep -delete
	cp -r web/dist/. server/internal/webui/dist/
	cd server && go build -o bin/platform ./cmd/platform
	@echo "platform binary -> server/bin/platform (SPA embedded from a fresh web/dist)"

signer: ## Build the offline signer binary (signer/bin/signer)
	cd signer && go build -o bin/signer ./cmd/signer
	@echo "signer binary -> signer/bin/signer"

embed-check: platform ## Prove the release binary builds with a freshly embedded SPA (dist is built at release, not committed)
	# The built SPA is not committed, so there is no drift to diff. `platform` rebuilds
	# web/, embeds it and compiles the binary, which shows that go:embed resolves the
	# real assets.
	@echo "embed-check OK (fresh SPA embedded into server/bin/platform)"

e2e: ## Full on-chain + HTTP-API end-to-end flow
	cd contracts && bash script/e2e.sh
	cd server && bash e2e/run_e2e.sh

ci: lint contracts-test signer-test server-test web-test investor-web-test web-e2e investor-web-e2e vectors-check bindings-check embed-check ## Run PR checks; run make format first
	@echo "CI OK"

security-scan: ## Dependency vulnerability scan (fail-closed on reachable advisories)
	cd server && go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
	cd signer && go run golang.org/x/vuln/cmd/govulncheck@v1.9.0 ./...
	# Production deps only: dev tooling (eslint, openapi-typescript, playwright) never
	# ships in the SPA or binary. Mirrors the ci.yml security-scan job.
	cd web && npm audit --omit=dev --audit-level=high

up: ## Start local dev stack (anvil, mongo, ipfs)
	cd docker && docker compose up -d

down: ## Stop local dev stack
	cd docker && docker compose down -v
