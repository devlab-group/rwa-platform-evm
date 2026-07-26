#!/usr/bin/env bash
# Boots a fresh anvil (chainId 31337), runs the full exit scenario against it via
# script/E2EFlow.s.sol with real broadcast transactions (deploy -> allow -> mint -> buy ->
# request -> fund -> claim -> burn), then a timeout -> cancel branch on a second request,
# advancing the live chain's clock between two more script invocations via `cast rpc`. Tears
# down anvil on exit regardless of outcome.
#
# Usage: bash script/e2e.sh
set -euo pipefail

# Default to a random high port (not the well-known 8545) so repeated runs in one session don't
# collide with an earlier run's anvil that couldn't be torn down (e.g. `kill` unavailable in a
# sandboxed environment) — still overridable via env. Combined with the preflight check below,
# this removes the port-reuse flake class entirely rather than just making it rarer.
ANVIL_PORT="${ANVIL_PORT:-$((20000 + RANDOM % 20000))}"
ANVIL_HOST="127.0.0.1"
RPC_URL="http://${ANVIL_HOST}:${ANVIL_PORT}"

is_port_free() {
    # Portable bash-only TCP probe (no nc/lsof dependency): a successful connect means
    # something is already listening on that port.
    if (exec 3<>"/dev/tcp/${ANVIL_HOST}/${1}") 2>/dev/null; then
        exec 3>&- 3<&- 2>/dev/null || true
        return 1
    fi
    return 0
}

if ! is_port_free "${ANVIL_PORT}"; then
    echo "Port ${ANVIL_PORT} is already in use — set ANVIL_PORT to a free port and retry." >&2
    exit 1
fi

# RWAFactory enforces redemptionTimeout >= 1 days, so this is the shortest usable value; using
# cast rpc evm_increaseTime to jump it is near-instant regardless of magnitude.
export REDEMPTION_TIMEOUT="${REDEMPTION_TIMEOUT:-86400}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONTRACTS_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
MAIN_LOG="$(mktemp)"
REQUEST_LOG="$(mktemp)"
CANCEL_LOG="$(mktemp)"
ANVIL_LOG="$(mktemp)"

ANVIL_PID=""

cleanup() {
    local status=$?
    if [[ -n "${ANVIL_PID}" ]] && kill -0 "${ANVIL_PID}" 2>/dev/null; then
        kill "${ANVIL_PID}" 2>/dev/null || true
        wait "${ANVIL_PID}" 2>/dev/null || true
    fi
    rm -f "${MAIN_LOG}" "${REQUEST_LOG}" "${CANCEL_LOG}" "${ANVIL_LOG}"
    exit "${status}"
}
trap cleanup EXIT

echo "Starting anvil (chainId 31337) on ${RPC_URL} ..."
anvil --chain-id 31337 --port "${ANVIL_PORT}" --silent >"${ANVIL_LOG}" 2>&1 &
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
    echo "anvil did not become ready on ${RPC_URL}" >&2
    cat "${ANVIL_LOG}" >&2
    exit 1
fi
echo "anvil is up."

cd "${CONTRACTS_DIR}"

# --- Step 1: required flow (deploy -> allow -> mint -> buy -> request -> fund -> claim -> burn) ---
echo "Running required E2E flow ..."
if ! forge script script/E2EFlow.s.sol:E2EFlow --sig "run()" --broadcast --non-interactive --rpc-url "${RPC_URL}" -vv 2>&1 | tee "${MAIN_LOG}"; then
    echo "forge script (run) exited non-zero." >&2
    exit 1
fi

fail=false
for marker in "Minted" "Bought" "Requested redemption" "Funded redemption" "Claimed redemption" "Burned" "E2E OK"; do
    if ! grep -q "${marker}" "${MAIN_LOG}"; then
        echo "Missing expected log marker: '${marker}'" >&2
        fail=true
    fi
done
if [[ "${fail}" == "true" ]]; then
    echo "Required E2E flow FAILED." >&2
    exit 1
fi
echo "Required E2E flow PASSED."

# --- Step 2 (nice-to-have): timeout -> cancel, against the SAME deployed contracts ---
extract_addr() {
    # matches lines like "token              0x...." printed by Deploy._logDeployment
    grep -E "^\s*${1}\s+0x" "${MAIN_LOG}" | tail -1 | grep -oE "0x[0-9a-fA-F]{40}"
}

export TOKEN
export COMPLIANCE
export SUPPLY_CONTROLLER
export VAULT
export ESCROW
export STRATEGY
export QUOTE_TOKEN
TOKEN="$(extract_addr token)"
COMPLIANCE="$(extract_addr compliance)"
SUPPLY_CONTROLLER="$(extract_addr supplyController)"
VAULT="$(extract_addr vault)"
ESCROW="$(extract_addr redemptionEscrow)"
STRATEGY="$(extract_addr strategy)"
QUOTE_TOKEN="$(extract_addr quoteToken)"

if [[ -z "${ESCROW}" || -z "${TOKEN}" || -z "${STRATEGY}" ]]; then
    echo "Could not parse deployed addresses from step 1 log; skipping cancel-demo branch." >&2
    exit 0
fi

echo "Requesting a second redemption for the timeout -> cancel demo ..."
if ! forge script script/E2EFlow.s.sol:E2EFlow --sig "requestForCancelDemo()" --broadcast --non-interactive --rpc-url "${RPC_URL}" -vv 2>&1 | tee "${REQUEST_LOG}"; then
    echo "forge script (requestForCancelDemo) exited non-zero." >&2
    exit 1
fi

request_id="$(grep -oE "Requested \(for cancel demo\) redemption id [0-9]+" "${REQUEST_LOG}" | grep -oE "[0-9]+$" | tail -1)"
if [[ -z "${request_id}" ]]; then
    echo "Could not parse cancel-demo request id; skipping cancel-demo branch." >&2
    exit 0
fi

echo "Advancing chain time past redemptionTimeout (${REDEMPTION_TIMEOUT}s) ..."
cast rpc evm_increaseTime $((REDEMPTION_TIMEOUT + 1)) --rpc-url "${RPC_URL}" >/dev/null
cast rpc evm_mine --rpc-url "${RPC_URL}" >/dev/null

echo "Cancelling timed-out redemption id ${request_id} ..."
if ! forge script script/E2EFlow.s.sol:E2EFlow --sig "cancelIt(uint256)" "${request_id}" --broadcast --non-interactive --rpc-url "${RPC_URL}" -vv 2>&1 | tee "${CANCEL_LOG}"; then
    echo "forge script (cancelIt) exited non-zero." >&2
    exit 1
fi

if ! grep -q "Cancelled timed-out redemption" "${CANCEL_LOG}"; then
    echo "timeout -> cancel branch FAILED." >&2
    exit 1
fi

echo "timeout -> cancel branch PASSED."
echo "E2E flow PASSED (required flow + timeout->cancel)."
exit 0
