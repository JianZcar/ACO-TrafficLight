#!/usr/bin/env bash
set -e

IMAGE_NAME="aco"
SRC_DIR="$(realpath ./)"

podman build --cache-from "$IMAGE_NAME" -t "$IMAGE_NAME" "$SRC_DIR"
