#!/usr/bin/env bash
# Restore from a backup made by ops/backup.sh — mongorestore +
# re-importing/re-pinning every exported IPFS CAR archive.
#
# Usage: MONGO_URI=... MONGO_DB=... IPFS_API_URL=... bash ops/restore.sh <backup-dir>
set -euo pipefail

if [[ $# -lt 1 ]]; then
    echo "usage: restore.sh <backup-dir> (as produced by ops/backup.sh)" >&2
    exit 1
fi
BACKUP_DIR="$1"

MONGO_URI="${MONGO_URI:-mongodb://127.0.0.1:27017}"
MONGO_DB="${MONGO_DB:-rwa_platform}"
IPFS_API_URL="${IPFS_API_URL:-http://127.0.0.1:5001}"

for tool in mongorestore ipfs; do
    if ! command -v "${tool}" >/dev/null 2>&1; then
        echo "restore.sh: required tool '${tool}' not found on PATH" >&2
        exit 1
    fi
done
if [[ ! -d "${BACKUP_DIR}/mongodump/${MONGO_DB}" ]]; then
    echo "restore.sh: ${BACKUP_DIR}/mongodump/${MONGO_DB} not found — is this really a backup.sh output dir, and does MONGO_DB match the one it was taken from?" >&2
    exit 1
fi

echo "=== [1/2] mongorestore ${MONGO_DB} from ${BACKUP_DIR}/mongodump ==="
# --drop: replace whatever is currently in each restored collection rather
# than merging with it — a restore is meant to return to exactly the
# backed-up state, not blend two states together.
mongorestore --uri="${MONGO_URI}" --db="${MONGO_DB}" --drop "${BACKUP_DIR}/mongodump/${MONGO_DB}"

echo "=== [2/2] re-importing IPFS CAR archives from ${BACKUP_DIR}/ipfs ==="
if [[ ! -d "${BACKUP_DIR}/ipfs" ]] || [[ -z "$(ls -A "${BACKUP_DIR}/ipfs"/*.car 2>/dev/null)" ]]; then
    echo "no .car archives found in ${BACKUP_DIR}/ipfs — nothing to re-import (fine if the backup predates any pinned content)"
else
    for car in "${BACKUP_DIR}/ipfs"/*.car; do
        # --pin-roots (kubo's default) re-pins the archive's root block(s)
        # on import, restoring exactly the recursive pin backup.sh exported.
        ipfs --api="${IPFS_API_URL}" dag import "${car}"
    done
fi

echo ""
echo "RESTORE OK"
