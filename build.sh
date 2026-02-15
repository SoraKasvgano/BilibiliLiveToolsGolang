#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
DIST_DIR="${ROOT_DIR}/dist"
APP_NAME="${APP_NAME:-BilibiliLiveToolsGover}"

mkdir -p "${DIST_DIR}"
rm -rf "${DIST_DIR}/linux-amd64" "${DIST_DIR}/linux-arm64" "${DIST_DIR}/linux-armv7" "${DIST_DIR}/windows-amd64"
rm -f "${DIST_DIR}"/*_windows_amd64.exe "${DIST_DIR}"/*_linux_amd64 "${DIST_DIR}"/*_linux_arm64 "${DIST_DIR}"/*_linux_armv7

build_target() {
  local target_os="$1"
  local target_arch="$2"
  local target_arm="${3:-}"
  local suffix="${target_arch}"
  if [[ -n "${target_arm}" ]]; then
    suffix="armv${target_arm}"
  fi
  local ext=""
  if [[ "${target_os}" == "windows" ]]; then
    ext=".exe"
  fi
  local output_path="${DIST_DIR}/${APP_NAME}_${target_os}_${suffix}${ext}"

  echo "==> Building ${target_os}/${target_arch}${target_arm:+ (GOARM=${target_arm})}"
  if [[ -n "${target_arm}" ]]; then
    CGO_ENABLED=0 GOOS="${target_os}" GOARCH="${target_arch}" GOARM="${target_arm}" \
      go build -trimpath -ldflags="-s -w" -o "${output_path}" .
  else
    CGO_ENABLED=0 GOOS="${target_os}" GOARCH="${target_arch}" \
      go build -trimpath -ldflags="-s -w" -o "${output_path}" .
  fi
}

build_target "windows" "amd64" ""
build_target "linux" "amd64" ""
build_target "linux" "arm64" ""
build_target "linux" "arm" "7"

echo "Build finished. Output directory: ${DIST_DIR}"
