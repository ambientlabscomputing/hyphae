#!/bin/bash

ALLOWED_PORTS=(80 443 22)
for PORT in "${ALLOWED_PORTS[@]}"; do
    sudo ufw allow $PORT
done

sudo ufw enable