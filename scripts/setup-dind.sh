#!/bin/bash
set -e

apt-get update
apt-get install -y docker.io docker-compose-plugin

usermod -aG docker nexus

dockerd &

sleep 2

docker ps
