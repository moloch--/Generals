#!/usr/bin/env bash
# GeneralsX @build Codex 10/10/2026 Export the validated Linux runtime without retail data.
# Usage: bash scripts/build/linux/export-runtime-linux-zh.sh NEW_OUTPUT_DIRECTORY
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
OUTPUT="${1:?A new runtime output directory is required}"
[[ ! -e "${OUTPUT}" && ! -L "${OUTPUT}" ]] || { echo 'Runtime output already exists' >&2; exit 1; }
bash "${SCRIPT_DIR}/bundle-linux-zh.sh"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf -- "${WORK_DIR}"' EXIT
tar -xzf "${PROJECT_ROOT}/GeneralsXZH-linux-x86_64.tar.gz" -C "${WORK_DIR}"
BUNDLE="${WORK_DIR}/GeneralsXZH-linux"
# This validates the canonical flat layout before normalizing library placement.
bash "${SCRIPT_DIR}/bundle-linux-zh.sh" --validate-directory "${BUNDLE}"
mkdir -p "${OUTPUT}/lib"
cp "${BUNDLE}/GeneralsXZH" "${OUTPUT}/GeneralsXZH"
chmod 0755 "${OUTPUT}/GeneralsXZH"
shopt -s nullglob
libraries=("${BUNDLE}"/*.so "${BUNDLE}"/*.so.*)
(( ${#libraries[@]} > 0 )) || { echo 'Runtime has no shared libraries' >&2; exit 1; }
cp -a "${libraries[@]}" "${OUTPUT}/lib/"
cp "${BUNDLE}/dxvk.conf" "${OUTPUT}/dxvk.conf"
# The runtime producer adds the tracked ExtrasMenu.wnd from this exact source
# revision to Window/Menus, matching build-sfx-linux-zh.sh without duplicate overlays.
echo "Exported Linux/AMD64 runtime: ${OUTPUT}"
