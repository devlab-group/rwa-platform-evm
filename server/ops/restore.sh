#!/usr/bin/env bash
# Restore from a backup made by ops/backup.sh: mongorestore, then import and pin
# every exported IPFS CAR archive.
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
    echo "restore.sh: ${BACKUP_DIR}/mongodump/${MONGO_DB} not found. Is this a backup.sh output directory, and does MONGO_DB match the one it was taken from?" >&2
    exit 1
fi

echo "=== [1/2] mongorestore ${MONGO_DB} from ${BACKUP_DIR}/mongodump ==="
# --drop replaces backed-up collections instead of merging their data.
# Collections absent from the backup remain untouched.
mongorestore --uri="${MONGO_URI}" --nsInclude="${MONGO_DB}.*" --drop "${BACKUP_DIR}/mongodump"

echo "=== [2/2] re-importing IPFS CAR archives from ${BACKUP_DIR}/ipfs ==="
if [[ ! -d "${BACKUP_DIR}/ipfs" ]] || [[ -z "$(ls -A "${BACKUP_DIR}/ipfs"/*.car 2>/dev/null)" ]]; then
    echo "no .car archives found in ${BACKUP_DIR}/ipfs, so there is nothing to re-import (fine if the backup predates any pinned content)"
else
    for car in "${BACKUP_DIR}/ipfs"/*.car; do
        # `ipfs dag import` pins the roots listed in the CAR header by default
        # (--pin-roots), which restores the recursive pins backup.sh exported
        ipfs --api="${IPFS_API_URL}" dag import "${car}"
    done
fi

echo ""
echo "RESTORE OK"
