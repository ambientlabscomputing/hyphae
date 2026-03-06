#!/bin/sh
set -e

# skip if make is already available
if command -v make > /dev/null 2>&1; then
    echo "make is already installed. Skipping."
else
    echo "Installing build-essential (make + gcc + other tools)..."
    sudo apt-get update -qq
    sudo apt-get install -y -qq build-essential
    make --version
    echo "build-essential installed successfully."
fi

# gettext-base provides envsubst, used by 'make render-config'.
if command -v envsubst > /dev/null 2>&1; then
    echo "envsubst is already installed. Skipping."
else
    echo "Installing gettext-base (provides envsubst)..."
    sudo apt-get update -qq
    sudo apt-get install -y -qq gettext-base
    echo "gettext-base installed successfully."
fi
