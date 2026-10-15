#!/usr/bin/env bash
# Run go run ./cmd/reindex, then poll until collection counts match their pre-drop
# values. A platform server must already be running against the same MongoDB and chain;
# its 5-to-15-second reconcile loops rebuild the data (cmd/platform/main.go).
#
# reindex reads addresses from the DB Project and only drops data; it never scans. See
# internal/indexer's TestReindexRehearsalReconstructsIdenticalReadModel for the
# fake-chain equivalent.
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

# runs a JavaScript snippet in mongosh against the database. MONGO_URI and
# MONGO_DB are joined as "<uri>/<db>", so MONGO_URI must not contain a database name
# or query options.
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
# cmd/reindex reads a YAML config file (--config, internal/config.LoadFile), so
# write a minimal one from this script's environment. The environment stays
# "development" (the default), so the production checks do not apply.
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
