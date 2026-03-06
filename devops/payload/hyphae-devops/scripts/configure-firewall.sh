#!/bin/bash
# DEPRECATED — this script is not called by any Makefile target and should not
# be run manually.
#
# Reasons:
#   1. OrbStack local VMs are on a private host-only network; ufw firewall rules
#      are unnecessary and can interfere with container port publishing.
#   2. The port list (80, 443, 22) does not match the ports published by
#      docker-compose.yaml (8084, 9090, 443, 8080) and would block the
#      management API and tunnel listener if ever enabled.
#
# If you need firewall hardening for a cloud (non-OrbStack) deployment,
# create a cloud-specific firewall script in a future orb_scripts/do-scripts/
# directory and align its port list with docker-compose.yaml.
echo "configure-firewall.sh is deprecated and should not be run." >&2
exit 1
