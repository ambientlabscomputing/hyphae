#!/bin/bash

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEVOPS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
VM_NAME_FILE="$DEVOPS_DIR/.vm-name"

# --- Preflight checks ---
if ! command -v orb > /dev/null 2>&1; then
    echo "Error: 'orb' CLI not found. Install OrbStack from https://orbstack.dev and try again." >&2
    exit 1
fi

if [ -f "$VM_NAME_FILE" ]; then
    EXISTING_VM="$(cat "$VM_NAME_FILE")"
    echo "Error: A VM is already registered: $EXISTING_VM" >&2
    echo "Run 'make destroy' to remove it before creating a new one," >&2
    echo "or 'make redeploy' to update the existing VM without recreating it." >&2
    exit 1
fi

# Use a fixed name so the local Hyphae endpoint is stable across restarts.
VM_NAME="hyphae-host"

echo "Creating OrbStack VM: $VM_NAME"
orb create ubuntu $VM_NAME
orb start $VM_NAME

# Persist the VM name so subsequent scripts (copy-payload, deploy-payload) can find it.
# All services reference the VM by its OrbStack FQDN (<name>.orb.local) rather
# than its IP, so the wildcard *.orb.local tunnel cert works across VM recreations.
echo "$VM_NAME" > "$VM_NAME_FILE"
echo "VM name persisted to $VM_NAME_FILE"
echo "  FQDN: ${VM_NAME}.orb.local"
