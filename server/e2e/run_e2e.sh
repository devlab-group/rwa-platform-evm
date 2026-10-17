#!/usr/bin/env bash
# API E2E against Anvil, single-node MongoDB replica set and IPFS. Bootstrap only
# RWAFactory (contracts/script/DeployFactory.s.sol) and a quote token
# (DeployTestToken.s.sol).
#
# The Go e2e/harness/main.go acts as the admin wallet: deploy and verify the stack,
# prove ownership, allow compliance, create/sign/mint an asset, buy, redeem/fund/claim,
# time out/cancel, check ERC-7943 enforcement, and run the rwa-v3 income flow (snapshot,
# createDistribution, investor claim). Assertions poll server read models,
# not just receipts; HTTP calls and readiness need no curl or wget.
#
# Ports default to Anvil 8545, API 8090, Mongo 27017 and IPFS 5001 and can be moved with
# ANVIL_PORT, E2E_HTTP_PORT, E2E_MONGO_PORT and E2E_IPFS_API_PORT when a dev stack holds them.
#
# Usage: bash e2e/run_e2e.sh
# Stops Anvil/platform and removes Mongo/IPFS containers on exit, except successful
# E2E_KEEP_UP runs (see below).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
ROOT_DIR="$(cd "${SERVER_DIR}/.." && pwd)"
CONTRACTS_DIR="${ROOT_DIR}/contracts"
SIGNER_DIR="${ROOT_DIR}/signer"
WEB_DIR="${ROOT_DIR}/web"
WEBUI_EMBED_DIR="${SERVER_DIR}/internal/webui/dist"

for tool in anvil forge cast docker go npm; do
    if ! command -v "${tool}" >/dev/null 2>&1; then
        echo "run_e2e.sh: required tool '${tool}' not found on PATH" >&2
        exit 1
    fi
done

WORKDIR="$(mktemp -d)"

ANVIL_PORT="${ANVIL_PORT:-8545}"
RPC_URL="http://127.0.0.1:${ANVIL_PORT}"
HTTP_PORT="${E2E_HTTP_PORT:-8090}"
BASE_URL="http://127.0.0.1:${HTTP_PORT}"

MONGO_IMAGE="${E2E_MONGO_IMAGE:-mongo:7}"
MONGO_PORT="${E2E_MONGO_PORT:-27017}"
MONGO_DB="rwa_e2e"
MONGO_CONTAINER="rwa-e2e-mongo-$$"

# The default digest pins the image even if its tag moves
IPFS_IMAGE="${E2E_IPFS_IMAGE:-ipfs/kubo:v0.32.1@sha256:7cc0e0de8f845d6c9fa1dce414c069974c34ed3cd3742e0d4f5bccda4adc376d}"
IPFS_API_PORT="${E2E_IPFS_API_PORT:-5001}"
IPFS_CONTAINER="rwa-e2e-ipfs-$$"

# RWAFactory requires at least a 1-day redemptionTimeout; evm_increaseTime lets the
# harness test timeout/cancel without waiting
REDEMPTION_TIMEOUT="${REDEMPTION_TIMEOUT:-86400}"

# Anvil's default accounts 0 and 1. Account 0 holds every role in the ProjectConfig
# that the harness deploys (admin, auditor, compliance operator, pricer, treasurer,
# redemption manager and treasury) and is also the server's compliance key. Account
# 1 is the investor.
DEPLOYER_PK="${DEPLOYER_PK:-ac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80}"
INVESTOR_PK="${INVESTOR_PK:-59c6995e998f97a5a0044966f0945389dc9e86dae88c7a8412f4603b6b78690d}"
DEPLOYER_PK="${DEPLOYER_PK#0x}"
INVESTOR_PK="${INVESTOR_PK#0x}"

# The profile and harness share this UUID; its keccak256 is on-chain
# ProjectConfig.projectId. The server uses it to bind ProjectDeployed to the profile.
PROJECT_ID_UUID="${E2E_PROJECT_ID:-aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee}"

ANVIL_PID=""
PLATFORM_PID=""

# Nonzero, nonempty E2E_KEEP_UP preserves a successful stack and prints its addresses
# for other suites' BASE_URL, such as web's live-smoke.spec.ts. Failed runs always tear
# down.
keep_up_requested() {
    [[ -n "${E2E_KEEP_UP:-}" && "${E2E_KEEP_UP}" != "0" ]]
}

print_keep_up_summary() {
    cat <<SUMMARY

=== E2E_KEEP_UP=1: stack left running (tear it down yourself when done) ===
BASE_URL=${BASE_URL}   (serves /api/v1/** and the embedded web SPA; point Playwright's BASE_URL here)
RPC_URL=${RPC_URL}
MONGO_URI=mongodb://127.0.0.1:${MONGO_PORT}
MONGO_DB=${MONGO_DB}
MONGO_CONTAINER=${MONGO_CONTAINER}
IPFS_API_URL=http://127.0.0.1:${IPFS_API_PORT}
IPFS_CONTAINER=${IPFS_CONTAINER}
ANVIL_PID=${ANVIL_PID}
PLATFORM_PID=${PLATFORM_PID}
WORKDIR=${WORKDIR}

bootstrap:
  factory=${FACTORY}
  start_block=${START_BLOCK}
  quoteToken=${QUOTE_TOKEN}
  projectId(UUID)=${PROJECT_ID_UUID}

The project stack was deployed by the harness via RWAFactory.deploy and its
addresses are in the Project record in the database (GET /api/v1/project).

Admin auth: wallet-signature JWT for admin_address=${DEPLOYER_ADDR}

Teardown when done:
  kill ${ANVIL_PID} ${PLATFORM_PID}
  docker rm -f ${MONGO_CONTAINER} ${IPFS_CONTAINER}
  rm -rf ${WORKDIR}
SUMMARY
}

cleanup() {
    local status=$?
    if [[ "${status}" -eq 0 ]] && keep_up_requested; then
        print_keep_up_summary
        exit 0
    fi
    if [[ -n "${PLATFORM_PID}" ]] && kill -0 "${PLATFORM_PID}" 2>/dev/null; then
        kill "${PLATFORM_PID}" 2>/dev/null || true
        wait "${PLATFORM_PID}" 2>/dev/null || true
    fi
    if [[ -n "${ANVIL_PID}" ]] && kill -0 "${ANVIL_PID}" 2>/dev/null; then
        kill "${ANVIL_PID}" 2>/dev/null || true
        wait "${ANVIL_PID}" 2>/dev/null || true
    fi
    docker rm -f "${MONGO_CONTAINER}" >/dev/null 2>&1 || true
    docker rm -f "${IPFS_CONTAINER}" >/dev/null 2>&1 || true
    rm -rf "${WORKDIR}"
    exit "${status}"
}
trap cleanup EXIT

echo "=== [1/9] starting anvil (chainId 31337) on ${RPC_URL} ==="
anvil --chain-id 31337 --port "${ANVIL_PORT}" --silent >"${WORKDIR}/anvil.log" 2>&1 &
ANVIL_PID=$!
ready=false
for _ in $(seq 1 60); do
    if cast chain-id --rpc-url "${RPC_URL}" >/dev/null 2>&1; then
        ready=true
        break
    fi
    sleep 0.5
done
if [[ "${ready}" != "true" ]]; then
    echo "run_e2e.sh: anvil did not become ready on ${RPC_URL}" >&2
    cat "${WORKDIR}/anvil.log" >&2
    exit 1
fi
echo "anvil is up."

echo "=== [2/9] starting MongoDB (${MONGO_IMAGE}) + IPFS (${IPFS_IMAGE}) ==="
docker run -d --name "${MONGO_CONTAINER}" -p "${MONGO_PORT}:27017" "${MONGO_IMAGE}" --replSet rs0 --bind_ip_all >/dev/null
docker run -d --name "${IPFS_CONTAINER}" -p "${IPFS_API_PORT}:5001" "${IPFS_IMAGE}" >/dev/null

mongo_ready=false
for _ in $(seq 1 60); do
    if docker exec "${MONGO_CONTAINER}" mongosh --quiet --eval "db.runCommand({ping:1})" >/dev/null 2>&1; then
        mongo_ready=true
        break
    fi
    sleep 1
done
if [[ "${mongo_ready}" != "true" ]]; then
    echo "run_e2e.sh: MongoDB did not become ready" >&2
    docker logs "${MONGO_CONTAINER}" >&2 || true
    exit 1
fi

# Atomic CommitChunk needs MongoDB transactions. Start single-node rs0 as
# docker/docker-compose.yml does, initiate it and wait for a writable primary before
# connecting the server.
# Only "already initialized" is tolerated; any other failure aborts the run.
if ! rs_init_out="$(docker exec "${MONGO_CONTAINER}" mongosh --quiet --eval \
    "rs.initiate({_id:'rs0',members:[{_id:0,host:'127.0.0.1:27017'}]})" 2>&1)"; then
    if ! grep -qi "already initialized" <<<"${rs_init_out}"; then
        echo "run_e2e.sh: rs.initiate failed: ${rs_init_out}" >&2
        exit 1
    fi
fi
mongo_primary=false
for _ in $(seq 1 60); do
    if docker exec "${MONGO_CONTAINER}" mongosh --quiet --eval "db.hello().isWritablePrimary" 2>/dev/null | grep -q "true"; then
        mongo_primary=true
        break
    fi
    sleep 1
done
if [[ "${mongo_primary}" != "true" ]]; then
    echo "run_e2e.sh: MongoDB replica set did not elect a primary" >&2
    docker logs "${MONGO_CONTAINER}" >&2 || true
    exit 1
fi
echo "mongo is up (replica set rs0, primary)."

# CreateRecord returns an error if pinning fails (internal/assets/service.go), so
# the create-record step needs IPFS to be ready
ipfs_ready=false
for _ in $(seq 1 60); do
    if docker logs "${IPFS_CONTAINER}" 2>&1 | grep -q "Daemon is ready"; then
        ipfs_ready=true
        break
    fi
    sleep 1
done
if [[ "${ipfs_ready}" != "true" ]]; then
    echo "run_e2e.sh: IPFS did not become ready" >&2
    docker logs "${IPFS_CONTAINER}" >&2 || true
    exit 1
fi
echo "ipfs is up."

echo "=== [3/9] building the web SPA and embedding it into the platform binary ==="
# go:embed reads only files under the server module, so the built SPA is copied into
# internal/webui/dist before the platform binary is built (see
# internal/webui/webui.go). npm install and not npm ci, so an existing node_modules
# is reused
(cd "${WEB_DIR}" && npm install && npm run build)
mkdir -p "${WEBUI_EMBED_DIR}"
# keep the tracked .gitkeep
find "${WEBUI_EMBED_DIR}" -mindepth 1 ! -name .gitkeep -delete
cp -r "${WEB_DIR}/dist/." "${WEBUI_EMBED_DIR}/"
echo "web SPA embedded from ${WEB_DIR}/dist into ${WEBUI_EMBED_DIR}."

echo "=== [4/9] building signer + platform binaries ==="
(cd "${SIGNER_DIR}" && go build -o "${WORKDIR}/signer" ./cmd/signer)
(cd "${SERVER_DIR}" && go build -o "${WORKDIR}/platform" ./cmd/platform)

echo "=== [5/9] preparing auditor keystore + asset profile ==="
cat >"${WORKDIR}/profile.json" <<EOF
{
  "profileVersion": "1.0",
  "projectId": "${PROJECT_ID_UUID}",
  "assetType": "Gold Bar",
  "tokenUnit": "GBT",
  "tokenDecimals": 18,
  "recordIdLabel": "Bar Serial Number",
  "displayFields": [
    { "label": "Bar ID", "pointer": "/barId" },
    { "label": "Weight (g)", "pointer": "/weightGrams" }
  ],
  "assetSchema": {
    "type": "object",
    "required": ["barId", "weightGrams"],
    "properties": {
      "barId": { "type": "string" },
      "weightGrams": { "type": "number" }
    },
    "additionalProperties": false
  }
}
EOF

KEYSTORE_PASSWORD="e2e-$$-$(date +%s)"
echo -n "${KEYSTORE_PASSWORD}" >"${WORKDIR}/keystore.password"
# the signer refuses a password file that group or others can read
chmod 600 "${WORKDIR}/keystore.password"
# Create the auditor keystore with the signer's own keystore tool and not
# `cast wallet import`. The signer refuses a keystore whose scrypt N is below
# 131072, and cast wallet import writes N=8192 (the eth-keystore default it uses). `signer keystore import` also
# writes the file owner-only (0600) and reads the private key from an owner-only
# file, never from argv.
mkdir -p "${WORKDIR}/keystore"
printf '%s' "${DEPLOYER_PK}" >"${WORKDIR}/auditor.privkey"
chmod 600 "${WORKDIR}/auditor.privkey"
"${WORKDIR}/signer" keystore import \
    --privkey-file "${WORKDIR}/auditor.privkey" \
    --out "${WORKDIR}/keystore/auditor" \
    --password-file "${WORKDIR}/keystore.password" >/dev/null
rm -f "${WORKDIR}/auditor.privkey"

DEPLOYER_ADDR="$(cast wallet address --private-key "0x${DEPLOYER_PK}")"
echo "deployer/admin address=${DEPLOYER_ADDR}"

echo "=== [6/9] deploying ONLY the RWAFactory + a quote-token MockERC20 ==="
# The harness broadcasts RWAFactory.deploy(config) itself, as the admin's web wallet
# would. This step deploys only the reusable factory, whose address and start block
# become the server's bootstrap config, and a quote token for the ProjectConfig.
FACTORY_LOG="${WORKDIR}/factory.log"
(cd "${CONTRACTS_DIR}" && \
    DEPLOYER_PK="0x${DEPLOYER_PK}" \
    forge script script/DeployFactory.s.sol --broadcast --rpc-url "${RPC_URL}" -vv) 2>&1 | tee "${FACTORY_LOG}"

FACTORY="$(grep -E "^\s*factory\s+0x" "${FACTORY_LOG}" | tail -1 | grep -oE "0x[0-9a-fA-F]{40}")"
START_BLOCK="$(grep -E "^\s*start_block\s+[0-9]+" "${FACTORY_LOG}" | tail -1 | grep -oE "[0-9]+" | tail -1)"
if [[ -z "${FACTORY}" || -z "${START_BLOCK}" ]]; then
    echo "run_e2e.sh: could not parse factory address / start block from ${FACTORY_LOG}" >&2
    exit 1
fi
echo "factory=${FACTORY} start_block=${START_BLOCK}"

# Quote token: a new 6-decimal TestToken with a permissionless mint, which goes into
# the ProjectConfig that the harness deploys. The investor and treasurer call its
# mint() directly later. It is deployed with DeployTestToken.s.sol.
QUOTE_LOG="${WORKDIR}/quote.log"
(cd "${CONTRACTS_DIR}" && \
    DEPLOYER_PK="0x${DEPLOYER_PK}" \
    forge script script/DeployTestToken.s.sol --broadcast --rpc-url "${RPC_URL}" -vv) 2>&1 | tee "${QUOTE_LOG}"
QUOTE_TOKEN="$(grep -E "^\s*token\s+0x" "${QUOTE_LOG}" | tail -1 | grep -oE "0x[0-9a-fA-F]{40}")"
if [[ -z "${QUOTE_TOKEN}" ]]; then
    echo "run_e2e.sh: could not parse quote token address from ${QUOTE_LOG}" >&2
    exit 1
fi
echo "quoteToken=${QUOTE_TOKEN}"

echo "=== [7/9] (no project seeding: the harness deploys the project through the factory) ==="
# The harness deploys the full stack through RWAFactory.deploy(config);
# ReconcileDeployment observes ProjectDeployed, verifies it and stores the Active
# project, matching production

echo "=== [8/9] starting the platform server (API + embedded web SPA) on ${BASE_URL} ==="
# The single admin/deployer is Anvil account 0. The harness signs its login challenge
# with DEPLOYER_PK; the server issues the JWT using security.jwt_secret.
JWT_SECRET="e2e-jwt-secret-000000000000000000-$$"

# Write the --config YAML, including secrets, under WORKDIR for automatic cleanup
# (internal/config.LoadFile). The default development environment skips production
# checks.
PLATFORM_CONFIG="${WORKDIR}/platform-config.yaml"
cat >"${PLATFORM_CONFIG}" <<EOF
http:
  addr: ":${HTTP_PORT}"
chain:
  rpc_url: "${RPC_URL}"
  id: 31337
  confirmations: 1
  fee_mode: "eip1559"
mongo:
  # directConnection=true: the single-node replica set behind a docker port mapping
  # advertises its in-container host (127.0.0.1:27017), which is not reachable at
  # the mapped port from the host. Connecting directly to the mapped endpoint skips
  # that topology discovery, and the primary there still serves transactions.
  uri: "mongodb://127.0.0.1:${MONGO_PORT}/?directConnection=true"
  db: "${MONGO_DB}"
ipfs:
  api_url: "http://127.0.0.1:${IPFS_API_PORT}"
# Bootstrap contract settings hold the factory address and scan start. The deployment
# projector stores stack addresses and auditor in the DB Project after ProjectDeployed.
# compliance_key is the only server hot key; the admin wallet broadcasts deployment and
# auditor-signed mint.
contract:
  factory_address: "${FACTORY}"
  start_block: ${START_BLOCK}
keys:
  compliance_key: "${DEPLOYER_PK}"
security:
  admin_address: "${DEPLOYER_ADDR}"
  jwt_secret: "${JWT_SECRET}"
EOF

"${WORKDIR}/platform" --config "${PLATFORM_CONFIG}" >"${WORKDIR}/platform.log" 2>&1 &
PLATFORM_PID=$!

echo "=== [9/9] running the HTTP-driven lifecycle harness ==="
set +e
# The harness deploys through RWAFactory.deploy and reads addresses from GET
# /api/v1/project. Supply the quote token and profile UUID (keccak256 becomes
# projectId); the harness sets prices and timeout in ProjectConfig.
(cd "${SERVER_DIR}" && \
    E2E_BASE_URL="${BASE_URL}" CHAIN_RPC_URL="${RPC_URL}" CHAIN_ID=31337 \
    DEPLOYER_PK="${DEPLOYER_PK}" INVESTOR_PK="${INVESTOR_PK}" \
    SIGNER_BIN="${WORKDIR}/signer" KEYSTORE_PATH="${WORKDIR}/keystore/auditor" KEYSTORE_PASSWORD_FILE="${WORKDIR}/keystore.password" \
    E2E_WORKDIR="${WORKDIR}" \
    QUOTE_TOKEN_ADDRESS="${QUOTE_TOKEN}" E2E_PROJECT_ID="${PROJECT_ID_UUID}" \
    E2E_PURCHASE_PRICE="2000000" E2E_REDEMPTION_PRICE="1950000" \
    REDEMPTION_TIMEOUT="${REDEMPTION_TIMEOUT}" \
    go run ./e2e/harness)
harness_status=$?
set -e

if [[ "${harness_status}" -ne 0 ]]; then
    echo "run_e2e.sh: harness FAILED (exit ${harness_status}); platform log follows:" >&2
    cat "${WORKDIR}/platform.log" >&2 || true
    exit "${harness_status}"
fi

echo "API E2E OK"
exit 0
