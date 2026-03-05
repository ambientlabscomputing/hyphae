#!/bin/bash

# skip if docker is already installed
if command -v docker &> /dev/null
then
    echo "Docker is already installed. Skipping installation."
    exit 0
fi

# Install Docker
echo "Installing Docker..."

echo "  deleting old versions..."
sudo apt-get remove -y $(dpkg --get-selections docker.io docker-compose docker-compose-v2 docker-doc podman-docker containerd runc 2>/dev/null | cut -f1) 2>/dev/null || true

echo "  installing dependencies..."
# Add Docker's official GPG key:
sudo apt-get update -qq
sudo apt-get install -y ca-certificates curl
sudo install -m 0755 -d /etc/apt/keyrings
sudo curl -fsSL https://download.docker.com/linux/ubuntu/gpg -o /etc/apt/keyrings/docker.asc
sudo chmod a+r /etc/apt/keyrings/docker.asc
# Add the repository to Apt sources:
sudo tee /etc/apt/sources.list.d/docker.sources <<EOF
Types: deb
URIs: https://download.docker.com/linux/ubuntu
Suites: $(. /etc/os-release && echo "${UBUNTU_CODENAME:-$VERSION_CODENAME}")
Components: stable
Signed-By: /etc/apt/keyrings/docker.asc
EOF

sudo apt-get update -qq

echo "  installing Docker Engine..."
sudo apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin
# Add current user to docker group so docker can be used without sudo.
# newgrp would open an interactive shell, so instead we rely on the group taking
# effect on the next login / SSH session (which deploy-payload.sh handles by
# re-SSH-ing for the docker compose step).
sudo usermod -aG docker $USER

echo "Docker installed successfully"
