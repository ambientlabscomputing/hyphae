#!/bin/sh
VERSION="v1.4.4"
FORCE=false
while getopts "f" opt; do
  case $opt in
    f) FORCE=true ;;
    *) echo "Usage: $0 [-f] [version]"
       echo "  -f    Force reinstallation even if the specified version is already installed."
       exit 1 ;;
  esac
done
shift $((OPTIND -1))
VERSION=${1:-$VERSION}

# check if ufctl is already installed and at the correct version
# example output of `ufctl version`:
# ufctl
# ✓ Underleaf CLI - Use --help to see available commands


# Version: dev-b56f7de, Go Version: go1.25.5, OS/Arch: linux/arm64
if command -v ufctl > /dev/null 2>&1
then
    INSTALLED_VERSION=$(ufctl | grep "Version:" | awk '{print $2}' | tr -d ',')
    if echo "$INSTALLED_VERSION" | grep -qE "^${VERSION}$" && [ "$FORCE" = false ]; then
        echo "Underleaf client version $INSTALLED_VERSION is already installed. Skipping installation."
        exit 0
    else
        echo "Underleaf client version $INSTALLED_VERSION is installed, but version $VERSION is required. Reinstalling..."
    fi
else
    echo "Underleaf client is not installed. Installing version $VERSION..."
fi

# Download ufctl
echo "Installing Underleaf client version ${VERSION}..."
echo "from https://github.com/ambientlabscomputing/underleaf_client/releases/download/${VERSION}/ufctl-linux-arm64"
curl -L -o ufctl https://github.com/ambientlabscomputing/underleaf_client/releases/download/${VERSION}/ufctl-linux-arm64
chmod +x ufctl
sudo mv ufctl /usr/local/bin/

# Download underleaf_agent
echo "from https://github.com/ambientlabscomputing/underleaf_client/releases/download/${VERSION}/underleaf_agent-linux-arm64"
curl -L -o underleaf_agent https://github.com/ambientlabscomputing/underleaf_client/releases/download/${VERSION}/underleaf_agent-linux-arm64
chmod +x underleaf_agent
sudo mv underleaf_agent /usr/local/bin/

echo "Underleaf client installed successfully."
echo "Useful commands:"
echo "  ufctl auth login"
echo "  ufctl start"
echo "  ufctl agent stop"
echo "  ufctl agent status"
