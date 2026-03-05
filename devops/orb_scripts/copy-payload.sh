#!/bin/bash

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEVOPS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
HYPHAE_DIR="$(cd "$DEVOPS_DIR/.." && pwd)"
VM_NAME_FILE="$DEVOPS_DIR/.vm-name"
PAYLOAD_DIR="$DEVOPS_DIR/payload/hyphae-devops"

# --- Read persisted VM name ---
if [ ! -f "$VM_NAME_FILE" ]; then
    echo "Error: $VM_NAME_FILE not found. Run 'make run-orb' first to create the VM." >&2
    exit 1
fi
VM_NAME="$(cat "$VM_NAME_FILE")"
echo "Targeting OrbStack VM: $VM_NAME"

# --- Stage tunnel certs into the payload directory ---
# certs are in hyphae/certs/ and are gitignored inside the payload (see .gitignore)
SRC_CERTS="$HYPHAE_DIR/certs"
DEST_CERTS="$PAYLOAD_DIR/hyphae/certs"

if [ ! -d "$SRC_CERTS" ] || [ -z "$(ls -A "$SRC_CERTS")" ]; then
    echo "Warning: $SRC_CERTS is empty or missing. The hyphae tunnel listener will fail to start without certs." >&2
else
    echo "Staging tunnel certs from $SRC_CERTS → $DEST_CERTS"
    cp "$SRC_CERTS"/. "$DEST_CERTS"/ 2>/dev/null || cp -r "$SRC_CERTS"/. "$DEST_CERTS"/
    # Ensure certs are readable by the container process.
    # Private keys are typically 600 on macOS; make them 644 so the Docker
    # process (which may run as a non-root user) can read them.
    chmod 644 "$DEST_CERTS"/*
fi

# --- Wait for VM SSH to be ready ---
echo "Waiting for VM SSH to be ready..."
RETRIES=20
for i in $(seq 1 $RETRIES); do
    if ssh -o StrictHostKeyChecking=no -o ConnectTimeout=3 -o BatchMode=yes "$VM_NAME@orb" true 2>/dev/null; then
        echo "VM is ready."
        break
    fi
    if [ "$i" -eq "$RETRIES" ]; then
        echo "Error: VM SSH not ready after $RETRIES attempts." >&2
        exit 1
    fi
    echo "  attempt $i/$RETRIES — retrying in 3s..."
    sleep 3
done

# --- Copy payload into the VM ---
VM_DEST_PATH="/home/$USER/hyphae-devops"
echo "Copying payload → $VM_NAME@orb:$VM_DEST_PATH"
# Use tar-over-ssh: no rsync dependency needed on the remote side.
# Exclude macOS metadata files (._*, .DS_Store) that cause noise on Linux.
tar -czf - -C "$PAYLOAD_DIR" \
    --exclude='._*' \
    --exclude='.DS_Store' \
    . \
    | ssh -o StrictHostKeyChecking=no "$VM_NAME@orb" \
        "mkdir -p $VM_DEST_PATH && tar -xzf - -C $VM_DEST_PATH"

echo "Payload copied successfully."
