#!/usr/bin/env bash
# GeneralsX @build Codex 05/08/2026 Generate architecture-specific Go COFF resources from the canonical Wails ICO.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
PACKAGE_DIR="${SCRIPT_DIR}/cmd/generalsx-sfx"
ICON_SOURCE="${PROJECT_ROOT}/tools/generalsx-build-desktop/build/windows/icon.ico"
WORK_DIR=""

cleanup() {
    if [[ -n "${WORK_DIR}" && "${WORK_DIR}" == "${PACKAGE_DIR}/.windows-icons."* ]]; then
        rm -rf -- "${WORK_DIR}"
    fi
}
trap cleanup EXIT

for tool in x86_64-w64-mingw32-windres i686-w64-mingw32-windres; do
    if ! command -v "${tool}" >/dev/null 2>&1; then
        echo "ERROR: ${tool} is required to regenerate Windows SFX icon resources." >&2
        exit 1
    fi
done
if [[ ! -f "${ICON_SOURCE}" || -L "${ICON_SOURCE}" || ! -s "${ICON_SOURCE}" ]]; then
    echo "ERROR: Canonical Windows icon is missing: ${ICON_SOURCE}" >&2
    exit 1
fi

WORK_DIR="$(mktemp -d "${PACKAGE_DIR}/.windows-icons.XXXXXX")"
cp -- "${ICON_SOURCE}" "${WORK_DIR}/generalsxzh.ico"
cp -- "${PACKAGE_DIR}/windows_resources.rc" "${WORK_DIR}/windows_resources.rc"

(
    cd "${WORK_DIR}"
    x86_64-w64-mingw32-windres \
        --input windows_resources.rc \
        --output rsrc_windows_amd64.syso \
        --output-format coff
    i686-w64-mingw32-windres \
        --input windows_resources.rc \
        --output rsrc_windows_386.syso \
        --output-format coff
)

for generated in generalsxzh.ico rsrc_windows_amd64.syso rsrc_windows_386.syso; do
    chmod 0644 "${WORK_DIR}/${generated}"
    mv -f -- "${WORK_DIR}/${generated}" "${PACKAGE_DIR}/${generated}"
done

echo "Regenerated Windows SFX icon resources in ${PACKAGE_DIR}."
