#!/bin/sh
set -e

DEFAULT_VERSION="v1.4.4"
FORCE=false

while getopts "f" opt; do
  case $opt in
    f) FORCE=true ;;
    *) echo "Usage: $0 [-f] [version]"
       echo "  -f    Force reinstallation even if the specified version is already installed."
       exit 1 ;;
  esac
done
shift $((OPTIND - 1))

VERSION="${1:-$DEFAULT_VERSION}"

# Detect CPU architecture and map to release suffix.
ARCH="$(uname -m)"
case "$ARCH" in
  aarch64|arm64) ARCH_SUFFIX="linux-arm64" ;;
  x86_64)        ARCH_SUFFIX="linux-amd64" ;;
  *)
    echo "Error: unsupported architecture '$ARCH'. Only arm64 and amd64 are supported." >&2
    exit 1 ;;
esac

echo "Architecture detected: $ARCH → $ARCH_SUFFIX"

# Check if ufctl is already installed at the correct version.
# Example output of 'ufctl version':
#   ufctl
#   ✓ Underleaf CLI - Use --help to see available commands
#   Version: v1.4.4, Go Version: go1.25.5, OS/Arch: linux/arm64
if command -v ufctl > /dev/null 2>&1; then
    INSTALLED_VERSION="$(ufctl | grep "Version:" | awk '{print $2}' | tr -d ',')"
    if echo "$INSTALLED_VERSION" | grep -qE "^${VERSION}$" && [ "$FORCE" = false ]; then
        echo "Underleaf client $INSTALLED_VERSION already installed. Skipping."
        exit 0
    else
        echo "Installed: $INSTALLED_VERSION — required: $VERSION. Reinstalling..."
    fi
else
    echo "Underleaf client not installed. Installing $VERSION..."
fi

BASE_URL="https://github.com/ambientlabscomputing/underleaf_client/releases/download/${VERSION}"

# Download ufctl
echo "Downloading ufctl ${VERSION} for ${ARCH_SUFFIX}..."
curl -L --fail -o ufctl "${BASE_URL}/ufctl-${ARCH_SUFFIX}"
chmod +x ufctl
sudo mv ufctl /usr/local/bin/

# Download underleaf_agent
echo "Downloading underleaf_agent ${VERSION} for ${ARCH_SUFFIX}..."
curl -L --fail -o underleaf_agent "${BASE_URL}/underleaf_agent-${ARCH_SUFFIX}"
chmod +x underleaf_agent
sudo mv underleaf_agent /usr/local/bin/

echo "Underleaf client ${VERSION} installed successfully."
echo "Useful commands:"
echo "  ufctl auth login"
echo "  ufctl start"
echo "  ufctl agent stop"
echo "  ufctl agent status"
