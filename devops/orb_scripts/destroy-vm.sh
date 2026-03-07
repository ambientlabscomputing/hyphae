#!/bin/bash

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEVOPS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
VM_NAME_FILE="$DEVOPS_DIR/.vm-name"

# Check if VM name file exists
if [ ! -f "$VM_NAME_FILE" ]; then
    echo "Error: VM name file not found at $VM_NAME_FILE"
    echo "No VM to destroy."
    exit 0
fi

# Read the VM name
VM_NAME=$(cat "$VM_NAME_FILE")

if [ -z "$VM_NAME" ]; then
    echo "Error: VM name is empty in $VM_NAME_FILE"
    exit 1
fi

echo "Destroying OrbStack VM: $VM_NAME"
echo "  FQDN: ${VM_NAME}.orb.local"

# Stop and delete the VM
orb stop "$VM_NAME" || echo "Warning: Failed to stop VM (may already be stopped)"
orb delete "$VM_NAME"

# Remove the VM name file
rm "$VM_NAME_FILE"
echo "VM destroyed and $VM_NAME_FILE removed"
