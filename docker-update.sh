#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
cd "${ROOT_DIR}"

SERVICE_NAME="${SERVICE_NAME:-gover}"
IMAGE_NAME="${IMAGE_NAME:-gover:latest}"

echo "==> Stopping and removing old containers"
docker compose down --remove-orphans || true
docker rm -f "${SERVICE_NAME}" >/dev/null 2>&1 || true

echo "==> Removing old image: ${IMAGE_NAME}"
docker image rm -f "${IMAGE_NAME}" >/dev/null 2>&1 || true

echo "==> Building new image"
docker compose build --no-cache "${SERVICE_NAME}"

echo "==> Starting new container"
docker compose up -d "${SERVICE_NAME}"

echo "==> Done"
docker compose ps
