#!/bin/bash

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PAYLOAD_DIR="$(cd "$SCRIPT_DIR/.." && pwd)"

if [ -f "$PAYLOAD_DIR/.env" ]; then
	set -a
	# shellcheck source=/dev/null
	. "$PAYLOAD_DIR/.env"
	set +a
fi

CERT_DIR="${HYPHAE_CERTS_PATH:-$PAYLOAD_DIR/certs}"
CONFIG_PATH="${1:-$PAYLOAD_DIR/hyphae/config.yaml}"
CERT_PATH="${2:-$CERT_DIR/tunnel.crt}"
KEY_PATH="${3:-$CERT_DIR/tunnel.key}"

if [ ! -f "$CONFIG_PATH" ] || [ ! -f "$CERT_PATH" ]; then
	exit 0
fi

CURRENT_SANS="$(openssl x509 -in "$CERT_PATH" -noout -ext subjectAltName 2>/dev/null || true)"
if [ -z "$CURRENT_SANS" ]; then
	exit 0
fi

EXPECTED_NAMES="$({
	awk '
		/^[[:space:]]*cert_dns_names:[[:space:]]*$/ { in_dns=1; next }
		in_dns && /^[[:space:]]*-[[:space:]]*/ {
			line = $0
			sub(/^[[:space:]]*-[[:space:]]*/, "", line)
			gsub(/"/, "", line)
			print line
			next
		}
		in_dns { exit }
	' "$CONFIG_PATH"
} | tr '\n' ' ')"

if [ -z "$EXPECTED_NAMES" ]; then
	exit 0
fi

for expected_name in $EXPECTED_NAMES; do
	if ! printf '%s' "$CURRENT_SANS" | grep -F "DNS:$expected_name" >/dev/null; then
		echo "Removing stale Hyphae tunnel cert; missing SAN $expected_name"
		rm -f "$CERT_PATH" "$KEY_PATH"
		exit 0
	fi
done