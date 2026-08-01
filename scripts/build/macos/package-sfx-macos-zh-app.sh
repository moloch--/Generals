#!/usr/bin/env bash
# GeneralsX @build moloch 31/07/2026 Package the macOS/ARM64 SFX as a signed Finder-launchable app bundle.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
SFX_INPUT="${GX_SFX_APP_INPUT:-${PROJECT_ROOT}/build/sfx/GeneralsXZH-macos-arm64-sfx}"
APP_OUTPUT="${GX_SFX_APP_OUTPUT:-${PROJECT_ROOT}/build/sfx/GeneralsXZH.app}"
ICON_SOURCE="${GX_SFX_APP_ICON_SOURCE:-${PROJECT_ROOT}/assets/generalsx-zh_icon.png}"
APP_VERSION="${GX_SFX_APP_VERSION:-0.1.0}"
APP_BUILD_VERSION="${GX_SFX_APP_BUILD_VERSION:-}"
CODESIGN_IDENTITY="${GX_SFX_APP_CODESIGN_IDENTITY:--}"
BUNDLE_IDENTIFIER="com.generalsx.generalsxzh.sfx"
PROGRESS_HELPER_SOURCE="${PROJECT_ROOT}/scripts/tooling/sfx/macos/progress.m"
PROGRESS_HELPER_NAME="GeneralsX-SFX-Progress"
PROGRESS_HELPER_IDENTIFIER="${BUNDLE_IDENTIFIER}.progress"
APP_NAME="GeneralsXZH.app"
APP_STAGE_ROOT=""
APP_BACKUP=""
APP_PUBLISHED=0

cleanup() {
    if [[ -n "${APP_STAGE_ROOT}" &&
          "${APP_STAGE_ROOT}" == "$(dirname -- "${APP_OUTPUT}")/.${APP_NAME}.packing."* &&
          -d "${APP_STAGE_ROOT}" ]]; then
        rm -rf -- "${APP_STAGE_ROOT}"
    fi

    if [[ -n "${APP_BACKUP}" && -d "${APP_BACKUP}" ]]; then
        if [[ ! -e "${APP_OUTPUT}" ]]; then
            echo "Restoring the previous app bundle after a packaging failure..." >&2
            mv -- "${APP_BACKUP}" "${APP_OUTPUT}"
        elif [[ "${APP_PUBLISHED}" -eq 1 ]]; then
            rm -rf -- "${APP_BACKUP}"
        else
            echo "WARNING: Preserving the previous app bundle at ${APP_BACKUP}" >&2
        fi
    fi
}

fail() {
    echo "ERROR: $*" >&2
    exit 1
}

plist_value() {
    local plist="$1"
    local key="$2"
    plutil -extract "${key}" raw -o - "${plist}"
}

assert_plist_value() {
    local plist="$1"
    local key="$2"
    local expected="$3"
    local actual

    actual="$(plist_value "${plist}" "${key}")"
    if [[ "${actual}" != "${expected}" ]]; then
        fail "Info.plist ${key} is '${actual}', expected '${expected}'."
    fi
}

image_dimension() {
    local image="$1"
    local property="$2"
    sips -g "${property}" "${image}" 2>/dev/null |
        awk -v property="${property}" '$1 == property ":" { print $2 }'
}

render_icon() {
    local source="$1"
    local iconset="$2"
    local pixels="$3"
    local name="$4"

    sips -z "${pixels}" "${pixels}" "${source}" \
        --out "${iconset}/${name}" >/dev/null
}

generate_icns() {
    local source="$1"
    local output="$2"
    local work_root="$3"
    local iconset="${work_root}/GeneralsXZH.iconset"
    local verify_iconset="${work_root}/GeneralsXZH-verify.iconset"
    local source_width
    local source_height
    local file
    local expected
    local actual_width
    local actual_height

    source_width="$(image_dimension "${source}" pixelWidth)"
    source_height="$(image_dimension "${source}" pixelHeight)"
    if [[ ! "${source_width}" =~ ^[0-9]+$ ||
          ! "${source_height}" =~ ^[0-9]+$ ]]; then
        fail "Unable to read icon dimensions from ${source}."
    fi
    if [[ "${source_width}" -ne "${source_height}" ]]; then
        fail "The app icon source must be square: ${source_width}x${source_height}."
    fi
    if [[ "${source_width}" -lt 512 ]]; then
        fail "The app icon source must be at least 512x512: ${source}."
    fi

    mkdir -p -- "${iconset}"
    render_icon "${source}" "${iconset}" 16 icon_16x16.png
    render_icon "${source}" "${iconset}" 32 icon_16x16@2x.png
    render_icon "${source}" "${iconset}" 32 icon_32x32.png
    render_icon "${source}" "${iconset}" 64 icon_32x32@2x.png
    render_icon "${source}" "${iconset}" 128 icon_128x128.png
    render_icon "${source}" "${iconset}" 256 icon_128x128@2x.png
    render_icon "${source}" "${iconset}" 256 icon_256x256.png
    render_icon "${source}" "${iconset}" 512 icon_256x256@2x.png
    render_icon "${source}" "${iconset}" 512 icon_512x512.png
    render_icon "${source}" "${iconset}" 1024 icon_512x512@2x.png
    iconutil -c icns "${iconset}" -o "${output}"

    iconutil -c iconset "${output}" -o "${verify_iconset}"
    while read -r file expected; do
        actual_width="$(image_dimension "${verify_iconset}/${file}" pixelWidth)"
        actual_height="$(image_dimension "${verify_iconset}/${file}" pixelHeight)"
        if [[ "${actual_width}" != "${expected}" ||
              "${actual_height}" != "${expected}" ]]; then
            fail "Generated icon ${file} is ${actual_width}x${actual_height}, expected ${expected}x${expected}."
        fi
    done <<'ICON_SIZES'
icon_16x16.png 16
icon_16x16@2x.png 32
icon_32x32.png 32
icon_32x32@2x.png 64
icon_128x128.png 128
icon_128x128@2x.png 256
icon_256x256.png 256
icon_256x256@2x.png 512
icon_512x512.png 512
icon_512x512@2x.png 1024
ICON_SIZES
}

validate_arm64_system_macho() {
    local executable="$1"
    local description="$2"
    local dependency

    if ! lipo "${executable}" -verify_arch arm64 >/dev/null 2>&1; then
        fail "${description} has no arm64 slice: ${executable}."
    fi
    if [[ "$(lipo -archs "${executable}")" != "arm64" ]]; then
        fail "${description} must be ARM64-only: $(lipo -archs "${executable}")."
    fi
    if ! file "${executable}" | grep -Fq "Mach-O 64-bit executable arm64"; then
        fail "${description} is not an ARM64 Mach-O file: $(file "${executable}")."
    fi
    if otool -l "${executable}" |
        awk '$1 == "cmd" && $2 == "LC_RPATH" { found = 1 } END { exit !found }'; then
        fail "${description} unexpectedly contains LC_RPATH entries."
    fi
    while IFS= read -r dependency; do
        [[ -n "${dependency}" ]] || continue
        case "${dependency}" in
            /System/Library/* | /usr/lib/*)
                ;;
            *)
                fail "${description} has a non-system dependency: ${dependency}."
                ;;
        esac
    done < <(otool -L "${executable}" | awk 'NR > 1 { print $1 }')
}

validate_macho_launcher() {
    local executable="$1"
    local info

    validate_arm64_system_macho "${executable}" "The SFX app launcher"

    info="$("${executable}" --sfx-info)"
    grep -Eq '^Product:[[:space:]]+GeneralsXZH$' <<< "${info}" ||
        fail "The app executable does not identify as GeneralsXZH."
    grep -Eq '^Target:[[:space:]]+darwin/arm64$' <<< "${info}" ||
        fail "The app executable does not contain a darwin/arm64 payload."
    grep -Eq '^Entrypoint:[[:space:]]+GeneralsXZH$' <<< "${info}" ||
        fail "The app executable has an unexpected payload entrypoint."
}

validate_macho_progress_helper() {
    local executable="$1"
    local minimum_version

    validate_arm64_system_macho "${executable}" "The SFX progress helper"
    minimum_version="$(
        otool -l "${executable}" |
            awk '$1 == "cmd" && $2 == "LC_BUILD_VERSION" { found = 1; next }
                 found && $1 == "minos" { print $2; exit }'
    )"
    if [[ "${minimum_version}" != "15.0" ]]; then
        fail "The SFX progress helper targets macOS ${minimum_version:-unknown}, expected 15.0."
    fi
    "${executable}" --self-test >/dev/null
}

build_progress_helper() {
    local source="$1"
    local output="$2"

    echo "Compiling the native extraction-progress helper..."
    xcrun --sdk macosx clang \
        -arch arm64 \
        -mmacosx-version-min=15.0 \
        -fobjc-arc \
        -O2 \
        -Wall \
        -Wextra \
        -Wpedantic \
        -Werror \
        -framework AppKit \
        -framework Foundation \
        "${source}" \
        -o "${output}"
}

usage() {
    cat <<'USAGE'
Usage: ./scripts/build/macos/package-sfx-macos-zh-app.sh

Packages the existing macOS/ARM64 self-extracting executable as a signed,
Finder-launchable GeneralsXZH.app with a Retina icon and native extraction
progress window.

Environment:
  GX_SFX_APP_INPUT             Input SFX executable
  GX_SFX_APP_OUTPUT            Output .app path
  GX_SFX_APP_ICON_SOURCE       Square PNG icon source (at least 512x512)
  GX_SFX_APP_VERSION           User-facing numeric version (default: 0.1.0)
  GX_SFX_APP_BUILD_VERSION     Numeric bundle build version (default: Git count)
  GX_SFX_APP_CODESIGN_IDENTITY Signing identity (default: - for ad-hoc)
USAGE
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    usage
    exit 0
fi
if [[ "$#" -ne 0 ]]; then
    usage >&2
    exit 2
fi

if [[ "$(uname -s)" != "Darwin" || "$(uname -m)" != "arm64" ]]; then
    fail "The SFX app must be packaged on macOS/ARM64."
fi
for required_command in codesign file iconutil lipo otool plutil sips xattr xcrun; do
    command -v "${required_command}" >/dev/null 2>&1 ||
        fail "Required command is unavailable: ${required_command}."
done

if [[ ! -f "${SFX_INPUT}" || -L "${SFX_INPUT}" || ! -x "${SFX_INPUT}" ]]; then
    fail "SFX input must be a regular executable: ${SFX_INPUT}."
fi
if [[ ! -f "${ICON_SOURCE}" || -L "${ICON_SOURCE}" ]]; then
    fail "Icon source must be a regular file: ${ICON_SOURCE}."
fi
if [[ ! -f "${PROGRESS_HELPER_SOURCE}" || -L "${PROGRESS_HELPER_SOURCE}" ]]; then
    fail "Progress helper source must be a regular file: ${PROGRESS_HELPER_SOURCE}."
fi
SFX_INPUT="$(cd "$(dirname -- "${SFX_INPUT}")" && pwd -P)/$(basename -- "${SFX_INPUT}")"
ICON_SOURCE="$(cd "$(dirname -- "${ICON_SOURCE}")" && pwd -P)/$(basename -- "${ICON_SOURCE}")"

if [[ ! "${APP_VERSION}" =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]]; then
    fail "GX_SFX_APP_VERSION must contain exactly three numeric components."
fi
if [[ -z "${APP_BUILD_VERSION}" ]]; then
    APP_BUILD_VERSION="$(git -C "${PROJECT_ROOT}" rev-list --count HEAD 2>/dev/null || echo 1)"
fi
if [[ ! "${APP_BUILD_VERSION}" =~ ^[0-9]+(\.[0-9]+){0,2}$ ]]; then
    fail "GX_SFX_APP_BUILD_VERSION must contain one to three numeric components."
fi

if [[ "${APP_OUTPUT}" != /* ]]; then
    APP_OUTPUT="${PWD}/${APP_OUTPUT}"
fi
case "/${APP_OUTPUT#/}/" in
    */../*)
        fail "GX_SFX_APP_OUTPUT may not contain '..': ${APP_OUTPUT}."
        ;;
esac
if [[ "$(basename -- "${APP_OUTPUT}")" != *.app ]]; then
    fail "GX_SFX_APP_OUTPUT must end in .app: ${APP_OUTPUT}."
fi
APP_PARENT="$(dirname -- "${APP_OUTPUT}")"
if [[ "${APP_PARENT}" == "/" ]]; then
    fail "Refusing to publish an app bundle directly under the filesystem root."
fi
mkdir -p -- "${APP_PARENT}"
APP_PARENT="$(cd "${APP_PARENT}" && pwd -P)"
if [[ "${APP_PARENT}" == "/" ]]; then
    fail "Refusing an app output whose resolved parent is the filesystem root."
fi
APP_OUTPUT="${APP_PARENT}/$(basename -- "${APP_OUTPUT}")"
if [[ "${SFX_INPUT}" == "${APP_OUTPUT}"/* ]]; then
    fail "The SFX input may not be located inside its output app bundle."
fi
if [[ -L "${APP_OUTPUT}" ]]; then
    fail "Refusing symlink app output: ${APP_OUTPUT}."
fi
if [[ -e "${APP_OUTPUT}" && ! -d "${APP_OUTPUT}" ]]; then
    fail "Refusing non-directory app output: ${APP_OUTPUT}."
fi
if [[ -d "${APP_OUTPUT}" ]]; then
    EXISTING_PLIST="${APP_OUTPUT}/Contents/Info.plist"
    if [[ ! -f "${EXISTING_PLIST}" ||
          "$(plist_value "${EXISTING_PLIST}" CFBundleIdentifier 2>/dev/null || true)" != "${BUNDLE_IDENTIFIER}" ]]; then
        fail "Refusing to replace an app bundle that is not ${BUNDLE_IDENTIFIER}: ${APP_OUTPUT}."
    fi
fi

trap cleanup EXIT
APP_STAGE_ROOT="$(mktemp -d "${APP_PARENT}/.${APP_NAME}.packing.XXXXXX")"
STAGED_APP="${APP_STAGE_ROOT}/${APP_NAME}"
CONTENTS_DIR="${STAGED_APP}/Contents"
MACOS_DIR="${CONTENTS_DIR}/MacOS"
RESOURCES_DIR="${CONTENTS_DIR}/Resources"
HELPERS_DIR="${CONTENTS_DIR}/Helpers"
EXECUTABLE="${MACOS_DIR}/GeneralsXZH"
PROGRESS_HELPER="${HELPERS_DIR}/${PROGRESS_HELPER_NAME}"
PLIST="${CONTENTS_DIR}/Info.plist"
ICON="${RESOURCES_DIR}/GeneralsXZH.icns"
mkdir -p -- "${MACOS_DIR}" "${RESOURCES_DIR}" "${HELPERS_DIR}"

echo "Packaging ${SFX_INPUT} as ${APP_OUTPUT}..."
if ! cp -c -- "${SFX_INPUT}" "${EXECUTABLE}" 2>/dev/null; then
    cp -- "${SFX_INPUT}" "${EXECUTABLE}"
fi
chmod 0755 "${EXECUTABLE}"
build_progress_helper "${PROGRESS_HELPER_SOURCE}" "${PROGRESS_HELPER}"
chmod 0755 "${PROGRESS_HELPER}"

cat > "${PLIST}" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleDevelopmentRegion</key>
    <string>en</string>
    <key>CFBundleDisplayName</key>
    <string>GeneralsX Zero Hour</string>
    <key>CFBundleExecutable</key>
    <string>GeneralsXZH</string>
    <key>CFBundleIconFile</key>
    <string>GeneralsXZH.icns</string>
    <key>CFBundleIdentifier</key>
    <string>${BUNDLE_IDENTIFIER}</string>
    <key>CFBundleInfoDictionaryVersion</key>
    <string>6.0</string>
    <key>CFBundleName</key>
    <string>GeneralsXZH</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>${APP_VERSION}</string>
    <key>CFBundleSupportedPlatforms</key>
    <array>
        <string>MacOSX</string>
    </array>
    <key>CFBundleVersion</key>
    <string>${APP_BUILD_VERSION}</string>
    <key>LSApplicationCategoryType</key>
    <string>public.app-category.strategy-games</string>
    <key>LSArchitecturePriority</key>
    <array>
        <string>arm64</string>
    </array>
    <key>LSMinimumSystemVersion</key>
    <string>15.0</string>
    <key>NSHighResolutionCapable</key>
    <true/>
</dict>
</plist>
PLIST
printf 'APPL????' > "${CONTENTS_DIR}/PkgInfo"

echo "Generating the Retina app icon from ${ICON_SOURCE}..."
generate_icns "${ICON_SOURCE}" "${ICON}" "${APP_STAGE_ROOT}"

# GeneralsX @build moloch 31/07/2026 Normalize bundle permissions independently of the caller's umask.
chmod 0755 "${STAGED_APP}" "${CONTENTS_DIR}" "${MACOS_DIR}" "${RESOURCES_DIR}" "${HELPERS_DIR}"
chmod 0644 "${PLIST}" "${CONTENTS_DIR}/PkgInfo" "${ICON}"

plutil -lint "${PLIST}" >/dev/null
assert_plist_value "${PLIST}" CFBundleExecutable GeneralsXZH
assert_plist_value "${PLIST}" CFBundleIconFile GeneralsXZH.icns
assert_plist_value "${PLIST}" CFBundleIdentifier "${BUNDLE_IDENTIFIER}"
assert_plist_value "${PLIST}" CFBundlePackageType APPL
assert_plist_value "${PLIST}" LSMinimumSystemVersion 15.0
validate_macho_launcher "${EXECUTABLE}"
validate_macho_progress_helper "${PROGRESS_HELPER}"

# GeneralsX @build moloch 31/07/2026 Strip copied extended attributes before sealing the completed bundle.
xattr -cr "${STAGED_APP}"
# GeneralsX @build Codex 01/08/2026 Sign nested native code before sealing it in the outer app signature.
echo "Signing the extraction-progress helper with identity '${CODESIGN_IDENTITY}'..."
if [[ "${CODESIGN_IDENTITY}" == "-" ]]; then
    codesign --force --sign - --identifier "${PROGRESS_HELPER_IDENTIFIER}" --timestamp=none "${PROGRESS_HELPER}"
else
    codesign --force --sign "${CODESIGN_IDENTITY}" --identifier "${PROGRESS_HELPER_IDENTIFIER}" "${PROGRESS_HELPER}"
fi
codesign --verify --strict --verbose=2 "${PROGRESS_HELPER}"
HELPER_SIGNATURE_DETAILS="$(codesign -d --verbose=4 "${PROGRESS_HELPER}" 2>&1)"
if ! grep -Fqx "Identifier=${PROGRESS_HELPER_IDENTIFIER}" <<< "${HELPER_SIGNATURE_DETAILS}"; then
    printf '%s\n' "${HELPER_SIGNATURE_DETAILS}" >&2
    fail "Signed progress helper has an unexpected code-signing identifier."
fi

echo "Signing the completed app bundle with identity '${CODESIGN_IDENTITY}'..."
if [[ "${CODESIGN_IDENTITY}" == "-" ]]; then
    codesign --force --sign - --timestamp=none "${STAGED_APP}"
else
    codesign --force --sign "${CODESIGN_IDENTITY}" "${STAGED_APP}"
fi
codesign --verify --deep --strict --verbose=2 "${STAGED_APP}"
codesign --verify --strict --verbose=2 "${PROGRESS_HELPER}"
SIGNATURE_DETAILS="$(codesign -d --verbose=4 "${STAGED_APP}" 2>&1)"
if ! grep -Fqx "Identifier=${BUNDLE_IDENTIFIER}" <<< "${SIGNATURE_DETAILS}"; then
    printf '%s\n' "${SIGNATURE_DETAILS}" >&2
    fail "Signed app has an unexpected code-signing identifier."
fi
if ! grep -Eq '^Sealed Resources version=[0-9]+' <<< "${SIGNATURE_DETAILS}"; then
    printf '%s\n' "${SIGNATURE_DETAILS}" >&2
    fail "Signed app does not report a sealed resource envelope."
fi

if [[ -d "${APP_OUTPUT}" ]]; then
    APP_BACKUP="${APP_PARENT}/.$(basename -- "${APP_OUTPUT}").previous.$$"
    if [[ -e "${APP_BACKUP}" || -L "${APP_BACKUP}" ]]; then
        fail "Refusing to replace an existing app backup: ${APP_BACKUP}."
    fi
    mv -- "${APP_OUTPUT}" "${APP_BACKUP}"
fi
mv -- "${STAGED_APP}" "${APP_OUTPUT}"
APP_PUBLISHED=1
if [[ -n "${APP_BACKUP}" ]]; then
    rm -rf -- "${APP_BACKUP}"
    APP_BACKUP=""
fi

echo
echo "macOS app bundle complete: ${APP_OUTPUT}"
du -sh "${APP_OUTPUT}"
printf 'Inspect: "%s/Contents/MacOS/GeneralsXZH" --sfx-info\n' "${APP_OUTPUT}"
printf 'Verify:  "%s/Contents/MacOS/GeneralsXZH" --sfx-verify\n' "${APP_OUTPUT}"
printf 'Run:     open -n "%s" --args -win\n' "${APP_OUTPUT}"
