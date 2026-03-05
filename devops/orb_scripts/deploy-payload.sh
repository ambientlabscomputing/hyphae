#!/bin/bash

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DEVOPS_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"
VM_NAME_FILE="$DEVOPS_DIR/.vm-name"

# --- Read persisted VM name ---
if [ ! -f "$VM_NAME_FILE" ]; then
    echo "Error: $VM_NAME_FILE not found. Run 'make run-orb' first to create the VM." >&2
    exit 1
fi
VM_NAME="$(cat "$VM_NAME_FILE")"
echo "Deploying on OrbStack VM: $VM_NAME"

VM_DEST_PATH="/home/$USER/hyphae-devops"
SSH_CMD="ssh -o StrictHostKeyChecking=no $VM_NAME@orb"

# --- Bootstrap build-essential (provides make + gcc + other tools) ---
# This must run before `make setup` since make itself may not be present.
echo "Installing build-essential on VM..."
$SSH_CMD "sudo apt-get update -qq && sudo apt-get install -y -qq build-essential"

# --- Run payload setup and start services ---
# setup and run are intentionally split into two SSH calls.
# install-docker.sh adds the user to the docker group; that group membership
# only takes effect in a new login session, so we open a fresh SSH connection
# for the `make run` (docker compose up) step.
echo "Running payload setup on VM..."
$SSH_CMD "cd $VM_DEST_PATH && make setup"

echo "Starting hyphae services (new SSH session so docker group is active)..."
ssh -o StrictHostKeyChecking=no "$VM_NAME@orb" "cd $VM_DEST_PATH && make run"

echo ""
echo "Deployment complete. To verify:"
echo "  ssh $VM_NAME@orb 'docker ps'"
echo "  ssh $VM_NAME@orb 'curl -s http://localhost:8084/health'"
echo "  ssh $VM_NAME@orb \"cd $VM_DEST_PATH && make health\""
