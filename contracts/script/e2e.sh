#!/usr/bin/env bash
# Start Anvil (chain ID 31337), broadcast E2EFlow's deploy-through-burn flow, distribute
# and claim income, then request again and advance time with cast rpc before cancelling.
# Attempt Anvil cleanup on exit.
#
# Usage: bash script/e2e.sh
set -euo pipefail

# Choose a random high port to reduce conflicts with leftover Anvil processes;
# ANVIL_PORT overrides it. The preflight probe checks for an existing listener.
ANVIL_PORT="${ANVIL_PORT:-$((20000 + RANDOM % 20000))}"
ANVIL_HOST="127.0.0.1"
RPC_URL="http://${ANVIL_HOST}:${ANVIL_PORT}"

is_port_free() {
    # A successful Bash TCP connection means a listener is using the port
    if (exec 3<>"/dev/tcp/${ANVIL_HOST}/${1}") 2>/dev/null; then
        exec 3>&- 3<&- 2>/dev/null || true
        return 1
    fi
    return 0
}

if ! is_port_free "${ANVIL_PORT}"; then
    echo "Port ${ANVIL_PORT} is already in use; set ANVIL_PORT to a free port and retry." >&2
    exit 1
fi

# Default to the factory's minimum one-day redemption timeout; cast rpc advances chain time
export REDEMPTION_TIMEOUT="${REDEMPTION_TIMEOUT:-86400}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
CONTRACTS_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"
MAIN_LOG="$(mktemp)"
INCOME_LOG="$(mktemp)"
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
    rm -f "${MAIN_LOG}" "${INCOME_LOG}" "${REQUEST_LOG}" "${CANCEL_LOG}" "${ANVIL_LOG}"
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

# Step 1: deploy -> allow -> mint -> buy -> request -> fund -> claim -> burn
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

# Reuse deployment addresses from the step 1 log for steps 2 and 3
extract_addr() {
    # Parse address lines from DeployBase._logDeployment
    grep -E "^\s*${1}\s+0x" "${MAIN_LOG}" | tail -1 | grep -oE "0x[0-9a-fA-F]{40}"
}

export TOKEN
export COMPLIANCE
export SUPPLY_CONTROLLER
export VAULT
export ESCROW
export STRATEGY
export INCOME_DISTRIBUTOR
export QUOTE_TOKEN
TOKEN="$(extract_addr token)"
COMPLIANCE="$(extract_addr compliance)"
SUPPLY_CONTROLLER="$(extract_addr supplyController)"
VAULT="$(extract_addr vault)"
ESCROW="$(extract_addr redemptionEscrow)"
STRATEGY="$(extract_addr strategy)"
INCOME_DISTRIBUTOR="$(extract_addr incomeDistributor)"
QUOTE_TOKEN="$(extract_addr quoteToken)"

# Missing addresses must fail the required income step before vm.envAddress runs
for name in TOKEN COMPLIANCE SUPPLY_CONTROLLER VAULT ESCROW STRATEGY INCOME_DISTRIBUTOR QUOTE_TOKEN; do
    if [[ -z "${!name}" ]]; then
        echo "Could not parse ${name} from step 1 log; the income step needs it." >&2
        exit 1
    fi
done

# Independent step 1 balances: 1000 minted, 100 sold, 40 redeemed, 40 burned;
# supply 960, Vault 900, escrow 0. Vault retains 100 * 2.000000 = 200000000 quote units;
# escrow paid out its funding.
onchain_uint() {
    # cast prints "<decimal> [<scientific>]" for large values; keep the decimal
    cast call "$1" "$2" ${3:+"$3"} --rpc-url "${RPC_URL}" | awk '{print $1}'
}
expect_eq() {
    if [[ "$2" != "$3" ]]; then
        echo "On-chain check failed: $1 is $2, expected $3" >&2
        exit 1
    fi
}
expect_eq "token totalSupply" "$(onchain_uint "${TOKEN}" 'totalSupply()(uint256)')" "960000000000000000000"
expect_eq "vault RWA balance" "$(onchain_uint "${TOKEN}" 'balanceOf(address)(uint256)' "${VAULT}")" "900000000000000000000"
expect_eq "escrow RWA balance" "$(onchain_uint "${TOKEN}" 'balanceOf(address)(uint256)' "${ESCROW}")" "0"
expect_eq "vault quote balance" "$(onchain_uint "${QUOTE_TOKEN}" 'balanceOf(address)(uint256)' "${VAULT}")" "200000000"
expect_eq "escrow quote balance" "$(onchain_uint "${QUOTE_TOKEN}" 'balanceOf(address)(uint256)' "${ESCROW}")" "0"
echo "Step 1 on-chain state PASSED."

# Step 2: income distribution and claim (required)
echo "Distributing and claiming income ..."
if ! forge script script/E2EFlow.s.sol:E2EFlow --sig "distributeIncome()" --broadcast --non-interactive --rpc-url "${RPC_URL}" -vv 2>&1 | tee "${INCOME_LOG}"; then
    echo "forge script (distributeIncome) exited non-zero." >&2
    exit 1
fi
if ! grep -q "Distributed and claimed income" "${INCOME_LOG}"; then
    echo "Income step FAILED." >&2
    exit 1
fi
echo "Income step PASSED."

# Step 3: timeout -> cancel on the same deployment

echo "Requesting a second redemption for the timeout -> cancel demo ..."
if ! forge script script/E2EFlow.s.sol:E2EFlow --sig "requestForCancelDemo()" --broadcast --non-interactive --rpc-url "${RPC_URL}" -vv 2>&1 | tee "${REQUEST_LOG}"; then
    echo "forge script (requestForCancelDemo) exited non-zero." >&2
    exit 1
fi

request_id="$(grep -oE "Requested \(for cancel demo\) redemption id [0-9]+" "${REQUEST_LOG}" | grep -oE "[0-9]+$" | tail -1)"
if [[ -z "${request_id}" ]]; then
    echo "Could not parse cancel-demo request id; cannot run the cancel-demo branch." >&2
    exit 1
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

expect_eq "escrow RWA balance after cancel" "$(onchain_uint "${TOKEN}" 'balanceOf(address)(uint256)' "${ESCROW}")" "0"
expect_eq "token totalSupply after cancel" "$(onchain_uint "${TOKEN}" 'totalSupply()(uint256)')" "960000000000000000000"
echo "timeout -> cancel branch PASSED."
echo "E2E flow PASSED (required flow + income + timeout->cancel)."
exit 0
