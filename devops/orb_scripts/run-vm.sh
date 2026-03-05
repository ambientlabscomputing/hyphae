#!/bin/bash

# create random name to avoid collisions with existing VMs
VM_NAME="hyphae-host-$RANDOM"

orb create $VM_NAME ubuntu
orb start $VM_NAME
