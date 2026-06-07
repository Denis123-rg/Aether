#!/bin/sh
cd /home/denis/Aether || exit 1
for pkg in internal/strategy internal/risk internal/config internal/events internal/grpc internal/db internal/signer; do
  echo \