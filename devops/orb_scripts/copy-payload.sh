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

# --- Verify certs exist (they are mounted via /mnt/mac, not copied) ---
# Certs live at hyphae/certs/ on the Mac and are accessed inside the VM
# through OrbStack's automatic /mnt/mac FUSE mount. No staging needed.
SRC_CERTS="$HYPHAE_DIR/certs"
if [ ! -d "$SRC_CERTS" ] || [ -z "$(ls -A "$SRC_CERTS")" ]; then
    echo "Warning: $SRC_CERTS is empty or missing." >&2
    echo "  The hyphae tunnel listener will fail without certs." >&2
    echo "  Create them with: make -C $DEVOPS_DIR cert" >&2
fi

# --- Verify .env is present before copying ---
ENV_FILE="$PAYLOAD_DIR/.env"
if [ ! -f "$ENV_FILE" ]; then
    echo "Error: $ENV_FILE not found." >&2
    echo "Copy $PAYLOAD_DIR/.env.example → $PAYLOAD_DIR/.env and fill in secrets." >&2
    exit 1
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
