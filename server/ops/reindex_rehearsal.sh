#!/usr/bin/env bash
# Chain reindex rehearsal runbook — proves a running deployment can
# fully reconstruct its event-derived read models from the chain alone.
# Wraps `go run ./cmd/reindex` (the actual drop) with a before/after
# collection-count comparison, polling until the ALREADY-RUNNING platform
# server's own background reconcile loops (5-15s tickers — see
# cmd/platform/main.go's startBackgroundLoops) have caught back up.
#
# This assumes a platform server is already running against the same
# MONGO_URI/MONGO_DB/CHAIN_ID/*_ADDRESS env vars this script and cmd/reindex
# read — reindex only drops data; it does not itself re-scan the chain, the
# running server's tickers do that, exactly as they would after any
# incident that left the read model behind the chain.
#
# For the same rehearsal proven without any live infrastructure at all, see
# internal/indexer's TestReindexRehearsalReconstructsIdenticalReadModel.
#
# Usage: MONGO_URI=... MONGO_DB=... CHAIN_ID=... bash ops/reindex_rehearsal.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
SERVER_DIR="$(cd "${SCRIPT_DIR}/.." && pwd)"

MONGO_URI="${MONGO_URI:-mongodb://127.0.0.1:27017}"
MONGO_DB="${MONGO_DB:-rwa_platform}"
POLL_TIMEOUT_SECONDS="${POLL_TIMEOUT_SECONDS:-120}"

for tool in mongosh go; do
    if ! command -v "${tool}" >/dev/null 2>&1; then
        echo "reindex_rehearsal.sh: required tool '${tool}' not found on PATH" >&2
        exit 1
    fi
done

# Extracts host[:port] from a mongodb:// URI for `mongosh --host`, since
# --uri and a bare positional db name don't combine the way this script
# wants; simplest robust option is a small inline JS snippet run through
# `mongosh "<uri>"` directly instead of parsing the URI ourselves.
mongo_eval() {
    mongosh "${MONGO_URI}/${MONGO_DB}" --quiet --eval "$1"
}

collection_counts() {
    mongo_eval '
      const names = ["chain_events","purchases","redemption_requests"];
      const out = {};
      for (const n of names) { out[n] = db.getCollection(n).countDocuments({}); }
      print(JSON.stringify(out));
    ' | tail -1
}

echo "=== [1/3] snapshotting read-model collection counts (before) ==="
BEFORE="$(collection_counts)"
echo "before: ${BEFORE}"

echo "=== [2/3] running cmd/reindex (drops chain_events/checkpoints/purchases/redemption_requests) ==="
# cmd/reindex now reads a single YAML config via --config (internal/config.
# LoadFile). Materialize a minimal one from this script's env, matching what
# it previously passed as MONGO_URI/MONGO_DB env vars. environment stays
# "development" (the default), so no production fail-closed checks apply.
REINDEX_CONFIG="$(mktemp)"
trap 'rm -f "${REINDEX_CONFIG}"' EXIT
cat >"${REINDEX_CONFIG}" <<EOF
chain:
  id: ${CHAIN_ID:-31337}
mongo:
  uri: "${MONGO_URI}"
  db: "${MONGO_DB}"
EOF
(cd "${SERVER_DIR}" && go run ./cmd/reindex --config "${REINDEX_CONFIG}" --yes)

echo "=== [3/3] waiting up to ${POLL_TIMEOUT_SECONDS}s for the running platform server to rebuild the read model ==="
deadline=$(($(date +%s) + POLL_TIMEOUT_SECONDS))
rebuilt=false
while [[ "$(date +%s)" -lt "${deadline}" ]]; do
    AFTER="$(collection_counts)"
    if [[ "${AFTER}" == "${BEFORE}" ]]; then
        rebuilt=true
        break
    fi
    sleep 3
done

echo "after:  ${AFTER:-<no snapshot taken>}"
if [[ "${rebuilt}" != "true" ]]; then
    echo "" >&2
    echo "REINDEX REHEARSAL FAILED: read-model counts did not return to their pre-drop values within ${POLL_TIMEOUT_SECONDS}s." >&2
    echo "Check that a platform server is actually running against MONGO_URI=${MONGO_URI} MONGO_DB=${MONGO_DB} with its chain RPC reachable." >&2
    exit 1
fi

echo ""
echo "REINDEX REHEARSAL OK: read-model counts match pre-drop values after full reconstruction from the chain."
