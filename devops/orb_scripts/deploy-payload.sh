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

# --- Bootstrap make + envsubst on the VM ---
# Runs install-make.sh directly via bash (make may not yet be present).
# This is the only apt-get call made by the host — all subsequent installs
# are delegated to the VM-side payload scripts via 'make setup'.
echo "Bootstrapping make and envsubst on VM..."
$SSH_CMD "bash $VM_DEST_PATH/scripts/install-make.sh"

# --- Determine image mode from payload .env ---
ENV_FILE="$DEVOPS_DIR/payload/hyphae-devops/.env"
IMAGE_MODE="develop"
if [ -f "$ENV_FILE" ]; then
    # Extract HYPHAE_IMAGE_MODE without sourcing the whole file (avoids side-effects)
    IMAGE_MODE="$(grep -E '^HYPHAE_IMAGE_MODE=' "$ENV_FILE" | cut -d= -f2- | tr -d "\"' \\\\" | head -1)"
    IMAGE_MODE="${IMAGE_MODE:-develop}"
fi
echo "Image mode: $IMAGE_MODE"

# --- Run payload setup and start services ---
# setup and run are intentionally split into two SSH sessions.
# install-docker.sh adds the user to the docker group; that group membership
# only takes effect in a new login session, so we open a fresh connection
# for the service-start step.
echo "Running payload setup on VM..."
$SSH_CMD "cd $VM_DEST_PATH && make setup"

echo "Starting hyphae services (new SSH session so docker group is active)..."
if [ "$IMAGE_MODE" = "local" ]; then
    echo "  → local build mode: building image from /mnt/mac source"
    ssh -o StrictHostKeyChecking=no "$VM_NAME@orb" "cd $VM_DEST_PATH && make run-local"
else
    echo "  → remote image mode: pulling $IMAGE_MODE from Docker Hub"
    ssh -o StrictHostKeyChecking=no "$VM_NAME@orb" "cd $VM_DEST_PATH && make run"
fi

echo ""
echo "Deployment complete. To verify:"
echo "  make -C $(dirname $DEVOPS_DIR)/devops ssh         # open shell in VM"
echo "  make -C $(dirname $DEVOPS_DIR)/devops status      # docker ps"
echo "  make -C $(dirname $DEVOPS_DIR)/devops health      # hyphctl health"
