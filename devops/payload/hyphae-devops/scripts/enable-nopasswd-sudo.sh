#!/bin/sh
# Enable passwordless sudo for the current user.
# Usage: sudo sh enable-nopasswd-sudo.sh

set -e

USER_NAME="${SUDO_USER:-$(whoami)}"

if [ "$(id -u)" -ne 0 ]; then
  echo "Error: This script must be run with sudo." >&2
  exit 1
fi

SUDOERS_FILE="/etc/sudoers.d/${USER_NAME}-nopasswd"

echo "${USER_NAME} ALL=(ALL) NOPASSWD: ALL" > "${SUDOERS_FILE}"
chmod 0440 "${SUDOERS_FILE}"

# Validate the sudoers file
if visudo -cf "${SUDOERS_FILE}"; then
  echo "Passwordless sudo enabled for user '${USER_NAME}'."
else
  echo "Error: Invalid sudoers file. Removing it." >&2
  rm -f "${SUDOERS_FILE}"
  exit 1
fi


