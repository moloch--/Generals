#!/usr/bin/env bash
# GeneralsX @build Codex 10/10/2026 Export and relocate the macOS runtime without retail data.
# Usage: bash scripts/build/macos/export-runtime-macos-zh.sh NEW_OUTPUT_DIRECTORY
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
OUTPUT="${1:?A new runtime output directory is required}"
[[ "$(uname -s)" == Darwin && "$(uname -m)" == arm64 ]] || { echo 'Requires macOS/arm64' >&2; exit 1; }
[[ ! -e "${OUTPUT}" && ! -L "${OUTPUT}" ]] || { echo 'Runtime output already exists' >&2; exit 1; }
bash "${SCRIPT_DIR}/bundle-macos-zh.sh"
WORK_DIR="$(mktemp -d)"
trap 'rm -rf -- "${WORK_DIR}"' EXIT
unzip -q "${PROJECT_ROOT}/GeneralsXZH-macos-arm64.zip" -d "${WORK_DIR}"
RESOURCES="${WORK_DIR}/GeneralsXZH.app/Contents/Resources"
mkdir -p "${OUTPUT}"
RUNTIME_STAGE="$(cd "${OUTPUT}" && pwd -P)"
cp "${RESOURCES}/bin/GeneralsXZH" "${RUNTIME_STAGE}/GeneralsXZH"
chmod 0755 "${RUNTIME_STAGE}/GeneralsXZH"
cp -R "${RESOURCES}/lib" "${RUNTIME_STAGE}/lib"
for file in MoltenVK_icd.json dxvk.conf; do
    cp "${RESOURCES}/${file}" "${RUNTIME_STAGE}/${file}"
done
# Homebrew conf.d entries can point outside the copied tree into conf.avail.
# Export the configuration bytes, never links back to the build host.
cp -RL "${RESOURCES}/fontconfig" "${RUNTIME_STAGE}/fontconfig"
. "${SCRIPT_DIR}/runtime-macos-zh.sh"
verify_macho_dependency_closure "${RUNTIME_STAGE}/GeneralsXZH" "${RUNTIME_STAGE}/lib"
# Replace runner-specific install names, then re-sign the modified Mach-O files.
# The published files retain only @loader_path references into this runtime.
while IFS= read -r -d '' root; do
    lipo "${root}" -verify_arch arm64
    prefix='@loader_path/'
    [[ "${root}" == "${RUNTIME_STAGE}/GeneralsXZH" ]] && prefix='@loader_path/lib/'
    while IFS= read -r dependency; do
        case "${dependency}" in
            /System/Library/*|/usr/lib/*) continue ;;
        esac
        leaf="$(basename "${dependency}")"
        [[ -f "${RUNTIME_STAGE}/lib/${leaf}" ]] || { echo "Missing dylib ${dependency}" >&2; exit 1; }
        install_name_tool -change "${dependency}" "${prefix}${leaf}" "${root}"
    done < <(otool -L "${root}" | sed -n 's/^[[:space:]]*\(.*\) (compatibility version.*$/\1/p')
    if [[ "${root}" == *.dylib ]]; then
        install_name_tool -id "@loader_path/$(basename "${root}")" "${root}"
    fi
    codesign --force --sign - --timestamp=none "${root}"
    codesign --verify --strict "${root}"
done < <(find "${RUNTIME_STAGE}" -type f \( -name GeneralsXZH -o -name '*.dylib' \) -print0)
verify_macho_dependency_closure "${RUNTIME_STAGE}/GeneralsXZH" "${RUNTIME_STAGE}/lib"
# The runtime producer adds the tracked ExtrasMenu.wnd from this exact source
# revision to Window/Menus, matching build-sfx-macos-zh.sh without duplicate overlays.
echo "Exported macOS/ARM64 runtime: ${RUNTIME_STAGE}"
