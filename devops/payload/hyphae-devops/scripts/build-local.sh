#!/bin/bash
# build-local.sh — Build the hyphae Docker image from Mac source via /mnt/mac.
#
# Called by 'make build-local' (and transitively by 'make run-local' and
# 'make rebuild' when HYPHAE_IMAGE_MODE=local).
#
# Writes the built image tag to .local-image-tag so sibling Makefile targets
# can reference it without re-running the build.
#
# Prerequisites:
#   - OrbStack VM with automatic /mnt/mac filesystem mount
#   - HYPHAE_SOURCE_PATH set in .env (path to hyphae/ as seen inside the VM)
#   - Docker installed and running on the VM

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PAYLOAD_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

# --- Load .env ---
ENV_FILE="$PAYLOAD_DIR/.env"
if [ ! -f "$ENV_FILE" ]; then
    echo "Error: $ENV_FILE not found." >&2
    echo "Copy .env.example → .env and set HYPHAE_SOURCE_PATH." >&2
    exit 1
fi
# shellcheck source=/dev/null
set -a; . "$ENV_FILE"; set +a

# --- Validate source path ---
if [ -z "$HYPHAE_SOURCE_PATH" ]; then
    echo "Error: HYPHAE_SOURCE_PATH is not set in .env." >&2
    echo "Set it to the hyphae source directory visible via /mnt/mac, e.g.:" >&2
    echo "  HYPHAE_SOURCE_PATH=/mnt/mac/Users/you/ambient_labs/underleaf/hyphae" >&2
    exit 1
fi

if [ ! -d "$HYPHAE_SOURCE_PATH" ]; then
    echo "Error: HYPHAE_SOURCE_PATH does not exist: $HYPHAE_SOURCE_PATH" >&2
    echo "" >&2
    echo "If /mnt/mac is not mounted, ensure OrbStack is running and the VM was" >&2
    echo "created via OrbStack (not a standalone Docker VM). Check with:" >&2
    echo "  ls /mnt/mac" >&2
    exit 1
fi

if [ ! -f "$HYPHAE_SOURCE_PATH/Dockerfile" ]; then
    echo "Error: No Dockerfile found at $HYPHAE_SOURCE_PATH/Dockerfile." >&2
    echo "Verify HYPHAE_SOURCE_PATH points to the hyphae/ service directory." >&2
    exit 1
fi

# --- Determine git SHA for the image tag ---
if command -v git > /dev/null 2>&1 && [ -d "$HYPHAE_SOURCE_PATH/.git" ]; then
    GIT_SHA="$(git -C "$HYPHAE_SOURCE_PATH" rev-parse --short=7 HEAD 2>/dev/null || echo "nogit")"
else
    # Fallback: timestamp-based tag if git is unavailable in the VM
    GIT_SHA="$(date +%Y%m%d%H%M%S)"
    echo "Warning: git not available in VM, using timestamp tag: $GIT_SHA" >&2
fi

IMAGE_TAG="local-${GIT_SHA}"
IMAGE_REF="ambientlabsjose/hyphae:${IMAGE_TAG}"

echo "Building hyphae Docker image from source..."
echo "  Source : $HYPHAE_SOURCE_PATH"
echo "  Tag    : $IMAGE_REF"
echo ""

docker build -t "$IMAGE_REF" "$HYPHAE_SOURCE_PATH"

echo ""
echo "Build complete: $IMAGE_REF"

# Write the tag to a file so 'make run-local' / 'make rebuild' can pick it up
# without re-running the build.
echo "$IMAGE_TAG" > "$PAYLOAD_DIR/.local-image-tag"
echo "Tag written to $PAYLOAD_DIR/.local-image-tag"
