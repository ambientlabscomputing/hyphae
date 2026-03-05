#!/bin/bash

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEVOPS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
VM_NAME_FILE="$DEVOPS_DIR/.vm-name"

# create random name to avoid collisions with existing VMs
VM_NAME="hyphae-host-$RANDOM"

echo "Creating OrbStack VM: $VM_NAME"
orb create ubuntu $VM_NAME
orb start $VM_NAME

# Persist the VM name so subsequent scripts (copy-payload, deploy-payload) can find it.
# All services reference the VM by its OrbStack FQDN (<name>.orb.local) rather
# than its IP, so the wildcard *.orb.local tunnel cert works across VM recreations.
echo "$VM_NAME" > "$VM_NAME_FILE"
echo "VM name persisted to $VM_NAME_FILE"
echo "  FQDN: ${VM_NAME}.orb.local"
