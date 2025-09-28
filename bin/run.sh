#!/usr/bin/env bash
set -e

IMAGE_NAME="aco"

podman run --rm -p 8080:8080 "$IMAGE_NAME"
