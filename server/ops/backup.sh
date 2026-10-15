#!/usr/bin/env bash
# Back up MongoDB and recursively pinned IPFS CIDs as CARv1 archives in one
# timestamped directory. Restore with ops/restore.sh.
#
# Usage: MONGO_URI=... MONGO_DB=... IPFS_API_URL=... bash ops/backup.sh [output-dir]
# The defaults match internal/config, so a local development stack needs no
# environment variables.
#
# Stop the server for a consistent copy: mongodump runs without --oplog and
# cannot capture a single point in time while writes continue.
set -euo pipefail

MONGO_URI="${MONGO_URI:-mongodb://127.0.0.1:27017}"
MONGO_DB="${MONGO_DB:-rwa_platform}"
IPFS_API_URL="${IPFS_API_URL:-http://127.0.0.1:5001}"
OUT_DIR="${1:-backups/$(date -u +%Y%m%dT%H%M%SZ)}"

for tool in mongodump ipfs; do
    if ! command -v "${tool}" >/dev/null 2>&1; then
        echo "backup.sh: required tool '${tool}' not found on PATH" >&2
        exit 1
    fi
done

mkdir -p "${OUT_DIR}/mongodump" "${OUT_DIR}/ipfs"

echo "=== [1/2] mongodump ${MONGO_DB} -> ${OUT_DIR}/mongodump ==="
mongodump --uri="${MONGO_URI}" --db="${MONGO_DB}" --out="${OUT_DIR}/mongodump"

echo "=== [2/2] exporting IPFS pins -> ${OUT_DIR}/ipfs ==="
# --type=recursive lists the pins made by `ipfs add` (the Kubo client in
# internal/ipfs adds with pin=true), and leaves out the indirect pins of a recursive
# pin's own blocks, which need no separate export
ipfs --api="${IPFS_API_URL}" pin ls --type=recursive -q > "${OUT_DIR}/ipfs/pinned-cids.txt"
while read -r cid; do
    [ -z "${cid}" ] && continue
    ipfs --api="${IPFS_API_URL}" dag export "${cid}" > "${OUT_DIR}/ipfs/${cid}.car"
done < "${OUT_DIR}/ipfs/pinned-cids.txt"
echo "exported $(wc -l < "${OUT_DIR}/ipfs/pinned-cids.txt" | tr -d ' ') pinned CIDs as .car archives"

echo ""
echo "BACKUP OK: ${OUT_DIR}"
