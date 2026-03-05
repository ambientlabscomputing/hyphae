#!/bin/sh

# skip if make is already available
if command -v make > /dev/null 2>&1; then
    echo "make is already installed. Skipping."
    exit 0
fi

echo "Installing build-essential (make + gcc + other tools)..."
sudo apt-get update -qq
sudo apt-get install -y -qq build-essential
make --version
echo "build-essential installed successfully"
