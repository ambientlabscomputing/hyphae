#!/bin/sh

sudo apt update
sudo apt install build-essential
make -version
echo "Make installed successfully"
