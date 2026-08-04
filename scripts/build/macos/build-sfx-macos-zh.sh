#!/usr/bin/env bash
# GeneralsX @build moloch 31/07/2026 Package the native macOS game and owned retail assets as a Go SFX and .app.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
SFX_MODULE="${PROJECT_ROOT}/scripts/tooling/sfx"
ASSET_DIR="${GX_SFX_ASSET_DIR:-${HOME}/GeneralsX/GeneralsZH}"
# GeneralsX @feature Codex 04/08/2026 Optionally authenticate and expose a target-native Online server sidecar.
SERVER_BINARY="${GX_SFX_SERVER_BINARY:-}"
OUTPUT="${GX_SFX_OUTPUT:-${PROJECT_ROOT}/build/sfx/GeneralsXZH-macos-arm64-sfx}"
SFX_APP_OUTPUT="${GX_SFX_APP_OUTPUT:-${PROJECT_ROOT}/build/sfx/GeneralsXZH.app}"
OUTPUT_DIR=""
OUTPUT_TEMP=""
SIGNING_IDENTIFIER="com.generalsx.generalsxzh.sfx"
BUNDLE_ZIP="${PROJECT_ROOT}/GeneralsXZH-macos-arm64.zip"
SKIP_GAME_BUILD=0
WORK_DIR=""
MANIFEST_TEMP=""
STAGE_MANIFEST=""
RUNTIME_STAGE=""
ONLINE_SERVER_ENTRY=""

cleanup() {
    if [[ -n "${OUTPUT_TEMP}" &&
          "${OUTPUT_TEMP}" == "${OUTPUT_DIR}/.$(basename -- "${OUTPUT}").packing."* ]]; then
        rm -f -- "${OUTPUT_TEMP}"
    fi
    if [[ -n "${MANIFEST_TEMP}" && "${MANIFEST_TEMP}" == "${OUTPUT_DIR}/.sfx-stage-contents."* ]]; then
        rm -f -- "${MANIFEST_TEMP}"
    fi
    if [[ -n "${WORK_DIR}" &&
          "${WORK_DIR}" == "${OUTPUT_DIR}/.macos-sfx-stage."* &&
          -d "${WORK_DIR}" ]]; then
        rm -rf -- "${WORK_DIR}"
    fi
}

validate_safe_output_target() {
    local target="$1"
    if [[ -L "${target}" ]]; then
        echo "ERROR: Refusing symlink output target: ${target}" >&2
        return 1
    fi
    if [[ -e "${target}" && ! -f "${target}" ]]; then
        echo "ERROR: Refusing non-regular output target: ${target}" >&2
        return 1
    fi
}

validate_safe_app_output_target() {
    local target="$1"
    if [[ "$(basename -- "${target}")" != *.app ]]; then
        echo "ERROR: macOS app output must end in .app: ${target}" >&2
        return 1
    fi
    if [[ -L "${target}" ]]; then
        echo "ERROR: Refusing symlink app output target: ${target}" >&2
        return 1
    fi
    if [[ -e "${target}" && ! -d "${target}" ]]; then
        echo "ERROR: Refusing non-directory app output target: ${target}" >&2
        return 1
    fi
}

directories_overlap() {
    local first="${1%/}"
    local second="${2%/}"
    [[ "${first}" == "${second}" ||
       "${first}" == "${second}/"* ||
       "${second}" == "${first}/"* ]]
}

validate_online_server_binary() {
    local candidate="$1"

    if [[ -L "${candidate}" || ! -f "${candidate}" || ! -x "${candidate}" ]]; then
        echo "ERROR: GX_SFX_SERVER_BINARY must name a regular executable: ${candidate}" >&2
        return 1
    fi
    if ! lipo "${candidate}" -verify_arch arm64 >/dev/null 2>&1 ||
       ! otool -hv "${candidate}" >/dev/null 2>&1; then
        echo "ERROR: Online server is not a macOS Mach-O executable with an arm64 slice: ${candidate}" >&2
        return 1
    fi
}

canonicalize_future_directory() {
    local candidate="$1"
    local absolute
    local existing
    local parent
    local leaf
    local suffix=""

    if [[ "${candidate}" == /* ]]; then
        absolute="${candidate}"
    else
        absolute="${PWD}/${candidate}"
    fi

    # A not-yet-existing path containing ".." is ambiguous in the presence of
    # symlinks. Reject it rather than creating anything before overlap checks.
    case "/${absolute#/}/" in
        */../*)
            echo "ERROR: Output directory may not contain '..': ${candidate}" >&2
            return 1
            ;;
    esac

    existing="${absolute}"
    while [[ ! -e "${existing}" ]]; do
        leaf="$(basename -- "${existing}")"
        if [[ -z "${leaf}" || "${leaf}" == "." || "${leaf}" == ".." ]]; then
            echo "ERROR: Cannot resolve output directory safely: ${candidate}" >&2
            return 1
        fi
        if [[ -n "${suffix}" ]]; then
            suffix="${leaf}/${suffix}"
        else
            suffix="${leaf}"
        fi
        parent="$(dirname -- "${existing}")"
        if [[ "${parent}" == "${existing}" ]]; then
            echo "ERROR: Cannot find an existing ancestor for output directory: ${candidate}" >&2
            return 1
        fi
        existing="${parent}"
    done
    if [[ ! -d "${existing}" ]]; then
        echo "ERROR: Output directory ancestor is not a directory: ${existing}" >&2
        return 1
    fi

    existing="$(cd "${existing}" && pwd -P)"
    if [[ -n "${suffix}" ]]; then
        printf '%s/%s\n' "${existing%/}" "${suffix}"
    else
        printf '%s\n' "${existing}"
    fi
}

prepare_paths() {
    local requested_output_dir
    local prospective_output_dir
    local prospective_output
    local output_name
    local requested_app_parent
    local prospective_app_parent
    local prospective_app_output
    local app_name

    ASSET_DIR="$(cd "${ASSET_DIR}" && pwd -P)"
    if [[ "${ASSET_DIR}" == "/" ]]; then
        echo "ERROR: Refusing to use the filesystem root as the retail asset tree." >&2
        return 1
    fi

    validate_safe_output_target "${OUTPUT}"
    requested_output_dir="$(dirname -- "${OUTPUT}")"
    output_name="$(basename -- "${OUTPUT}")"
    if [[ -z "${output_name}" || "${output_name}" == "." ||
          "${output_name}" == ".." || "${output_name}" == "/" ]]; then
        echo "ERROR: Invalid SFX output filename: ${OUTPUT}" >&2
        return 1
    fi
    prospective_output_dir="$(canonicalize_future_directory "${requested_output_dir}")"
    prospective_output="${prospective_output_dir%/}/${output_name}"
    if directories_overlap "${ASSET_DIR}" "${prospective_output_dir}"; then
        echo "ERROR: SFX output/workspace directory overlaps the retail asset tree." >&2
        echo "  Assets: ${ASSET_DIR}" >&2
        echo "  Output: ${prospective_output_dir}" >&2
        return 1
    fi

    # GeneralsX @build moloch 31/07/2026 Validate both publish targets before
    # staging so a custom app path cannot enter the retail tree or contain the SFX.
    validate_safe_app_output_target "${SFX_APP_OUTPUT}"
    requested_app_parent="$(dirname -- "${SFX_APP_OUTPUT}")"
    app_name="$(basename -- "${SFX_APP_OUTPUT}")"
    prospective_app_parent="$(canonicalize_future_directory "${requested_app_parent}")"
    if [[ "${prospective_app_parent}" == "/" ]]; then
        echo "ERROR: Refusing to publish an app bundle directly under the filesystem root." >&2
        return 1
    fi
    prospective_app_output="${prospective_app_parent%/}/${app_name}"
    validate_safe_app_output_target "${prospective_app_output}"
    if directories_overlap "${ASSET_DIR}" "${prospective_app_output}"; then
        echo "ERROR: SFX app output overlaps the retail asset tree." >&2
        echo "  Assets: ${ASSET_DIR}" >&2
        echo "  App:    ${prospective_app_output}" >&2
        return 1
    fi
    if directories_overlap "${prospective_output}" "${prospective_app_output}"; then
        echo "ERROR: Raw SFX and app output paths may not contain one another." >&2
        echo "  SFX: ${prospective_output}" >&2
        echo "  App: ${prospective_app_output}" >&2
        return 1
    fi

    mkdir -p -- "${requested_output_dir}"
    OUTPUT_DIR="$(cd "${requested_output_dir}" && pwd -P)"
    OUTPUT="${OUTPUT_DIR}/${output_name}"
    SFX_APP_OUTPUT="${prospective_app_output}"
    STAGE_MANIFEST="${OUTPUT}.stage-contents.txt"
    validate_safe_output_target "${OUTPUT}"
    validate_safe_output_target "${STAGE_MANIFEST}"

    if [[ "${OUTPUT}" == "${BUNDLE_ZIP}" ||
          ( -e "${OUTPUT}" && -e "${BUNDLE_ZIP}" &&
            "${OUTPUT}" -ef "${BUNDLE_ZIP}" ) ]]; then
        echo "ERROR: SFX output must not replace its macOS runtime bundle." >&2
        return 1
    fi
}

reset_runtime_stage() {
    local runtime_stage="$1"
    if [[ "${runtime_stage}" != "${WORK_DIR}/runtime" ]]; then
        echo "ERROR: Refusing to reset unexpected staging path: ${runtime_stage}" >&2
        return 1
    fi
    rm -rf -- "${runtime_stage}"
    mkdir -p -- "${runtime_stage}"
}

path_is_within_runtime_stage() {
    local candidate="$1"
    local canonical
    if [[ "${candidate}" != /* ]] ||
       ! canonical="$(realpath "${candidate}")"; then
        return 1
    fi
    [[ "${canonical}" == "${RUNTIME_STAGE}" ||
       "${canonical}" == "${RUNTIME_STAGE}/"* ]]
}

validate_staged_macho_dependency() {
    local root="$1"
    local dependency="$2"
    local suffix
    local candidate
    local canonical

    case "${dependency}" in
        /System/Library/* | /usr/lib/*)
            return 0
            ;;
        @rpath/*)
            # The launcher prepends stage/lib to DYLD_LIBRARY_PATH.
            suffix="${dependency#@rpath/}"
            candidate="${RUNTIME_STAGE}/lib/${suffix}"
            ;;
        @loader_path/*)
            suffix="${dependency#@loader_path/}"
            candidate="$(dirname -- "${root}")/${suffix}"
            ;;
        @executable_path/*)
            suffix="${dependency#@executable_path/}"
            candidate="${RUNTIME_STAGE}/${suffix}"
            ;;
        /*)
            # Homebrew-style install names are overridden by the launcher's
            # private stage/lib search path and therefore resolve by leaf name.
            candidate="${RUNTIME_STAGE}/lib/$(basename -- "${dependency}")"
            ;;
        *)
            echo "ERROR: Unsupported Mach-O install name in ${root}: ${dependency}" >&2
            return 1
            ;;
    esac

    if [[ ! -f "${candidate}" ]]; then
        echo "ERROR: ${root} requires unstaged Mach-O dependency ${dependency}" >&2
        echo "  Expected staged file: ${candidate}" >&2
        return 1
    fi
    if ! canonical="$(realpath "${candidate}")" ||
       ! path_is_within_runtime_stage "${canonical}"; then
        echo "ERROR: Mach-O dependency escapes the staged runtime: ${candidate}" >&2
        return 1
    fi
    if ! otool -L "${canonical}" >/dev/null 2>&1; then
        echo "ERROR: Staged Mach-O dependency is not inspectable: ${canonical}" >&2
        return 1
    fi
    if ! lipo "${canonical}" -verify_arch arm64 >/dev/null 2>&1; then
        echo "ERROR: Staged Mach-O dependency has no arm64 slice: ${canonical}" >&2
        return 1
    fi
}

verify_macho_dependency_closure() {
    local executable="$1"
    local library_dir="$2"
    local macho_roots=("${executable}")
    local dylib
    local root
    local otool_output
    local line
    local dependency

    while IFS= read -r -d '' dylib; do
        macho_roots+=("${dylib}")
    done < <(find "${library_dir}" -type f -name '*.dylib' -print0)
    if (( ${#macho_roots[@]} == 1 )); then
        echo "ERROR: Staged macOS runtime contains no dylibs under ${library_dir}" >&2
        return 1
    fi

    for root in "${macho_roots[@]}"; do
        if ! path_is_within_runtime_stage "${root}"; then
            echo "ERROR: Mach-O closure root escapes the staged runtime: ${root}" >&2
            return 1
        fi
        if ! lipo "${root}" -verify_arch arm64 >/dev/null 2>&1; then
            echo "ERROR: Staged Mach-O closure root has no arm64 slice: ${root}" >&2
            return 1
        fi
        if ! otool_output="$(otool -L "${root}" 2>&1)"; then
            echo "ERROR: otool failed while inspecting ${root}:" >&2
            printf '%s\n' "${otool_output}" >&2
            return 1
        fi
        while IFS= read -r line; do
            # Thin files have one heading and universal files repeat it for
            # every architecture slice.
            if [[ "${line}" =~ ^[^[:space:]].*:$ ]]; then
                continue
            fi
            line="${line#"${line%%[![:space:]]*}"}"
            [[ -n "${line}" ]] || continue
            dependency="${line%% (compatibility version*}"
            if [[ -z "${dependency}" || "${dependency}" == "${line}" ]]; then
                echo "ERROR: Unrecognized otool output for ${root}: ${line}" >&2
                return 1
            fi
            validate_staged_macho_dependency "${root}" "${dependency}"
        done <<< "${otool_output}"
    done
}

write_stage_manifest() {
    local stage="$1"
    local profile_name="$2"
    local unsorted_paths="${WORK_DIR}/.stage-paths.unsorted"
    local sorted_paths="${WORK_DIR}/.stage-paths.sorted"
    local staged_path
    local relative
    local target
    local bytes
    local entry_count=0

    find "${stage}" -mindepth 1 -print0 > "${unsorted_paths}"
    LC_ALL=C sort -z "${unsorted_paths}" -o "${sorted_paths}"
    MANIFEST_TEMP="$(mktemp "${OUTPUT_DIR}/.sfx-stage-contents.XXXXXX")"
    {
        echo "# GeneralsXZH SFX staged candidates"
        echo "# The packer applies ${profile_name} after this inventory."
        printf '# type\tbytes/target\tpath\n'
    } > "${MANIFEST_TEMP}"

    while IFS= read -r -d '' staged_path; do
        relative="${staged_path#"${stage}/"}"
        if [[ "${relative}" == "${staged_path}" ]]; then
            echo "ERROR: Staged path escaped inventory root: ${staged_path}" >&2
            return 1
        fi
        if [[ -L "${staged_path}" ]]; then
            target="$(readlink -- "${staged_path}")"
            printf 'symlink\t%q\t%q\n' "${target}" "${relative}" >> "${MANIFEST_TEMP}"
        elif [[ -d "${staged_path}" ]]; then
            printf 'directory\t-\t%q\n' "${relative}" >> "${MANIFEST_TEMP}"
        elif [[ -f "${staged_path}" ]]; then
            bytes="$(wc -c < "${staged_path}")"
            printf 'file\t%s\t%q\n' "${bytes//[[:space:]]/}" "${relative}" >> "${MANIFEST_TEMP}"
        else
            echo "ERROR: Unsupported staged filesystem object: ${staged_path}" >&2
            return 1
        fi
        ((entry_count += 1))
    done < "${sorted_paths}"

    if [[ "$(sed -n '3p' "${MANIFEST_TEMP}")" != $'# type\tbytes/target\tpath' ]]; then
        echo "ERROR: Staged candidate inventory header is malformed." >&2
        return 1
    fi
    chmod 0600 "${MANIFEST_TEMP}"
    mv -f -- "${MANIFEST_TEMP}" "${STAGE_MANIFEST}"
    MANIFEST_TEMP=""
    echo "Staged candidate inventory: ${STAGE_MANIFEST} (${entry_count} entries)"
}

usage() {
    cat <<'USAGE'
Usage: ./scripts/build/macos/build-sfx-macos-zh.sh [--skip-game-build]

Builds one native macOS/arm64 Go executable containing the GeneralsXZH runtime
and locally owned retail assets, then packages it as a Finder-launchable .app.
Assets are read from:
  $GX_SFX_ASSET_DIR (default: $HOME/GeneralsX/GeneralsZH)

Set $GX_SFX_SERVER_BINARY to an optional macOS/arm64 generals-server binary.
It is staged at online-server/generals-server and declared to the SFX launcher.

The raw executable and app outputs can be changed with $GX_SFX_OUTPUT and
$GX_SFX_APP_OUTPUT, respectively.

Options:
  --skip-game-build  Reuse the current macos-vulkan game binary.
  -h, --help         Show this help.
USAGE
}

for argument in "$@"; do
    case "${argument}" in
        --skip-game-build)
            SKIP_GAME_BUILD=1
            ;;
        -h|--help)
            usage
            exit 0
            ;;
        *)
            echo "ERROR: Unknown argument: ${argument}" >&2
            usage >&2
            exit 2
            ;;
    esac
done

if [[ "$(uname -s)" != "Darwin" || "$(uname -m)" != "arm64" ]]; then
    echo "ERROR: The macOS payload contains an arm64 native game and must be packaged on macOS/arm64." >&2
    exit 1
fi
if ! command -v go >/dev/null 2>&1; then
    echo "ERROR: Go is required. Install Go 1.25 or newer." >&2
    exit 1
fi
if ! command -v xz >/dev/null 2>&1; then
    echo "ERROR: xz is required for the multi-gigabyte retail payload (brew install xz)." >&2
    exit 1
fi
if ! command -v unzip >/dev/null 2>&1; then
    echo "ERROR: unzip is required to stage the portable native bundle." >&2
    exit 1
fi
if ! command -v otool >/dev/null 2>&1 ||
   ! command -v lipo >/dev/null 2>&1 ||
   ! command -v realpath >/dev/null 2>&1; then
    echo "ERROR: otool, lipo, and realpath are required to validate the staged Mach-O closure." >&2
    exit 1
fi
if [[ ! -d "${ASSET_DIR}" ]]; then
    echo "ERROR: Retail asset directory not found: ${ASSET_DIR}" >&2
    exit 1
fi
if [[ -n "${SERVER_BINARY}" ]]; then
    validate_online_server_binary "${SERVER_BINARY}"
    SERVER_BINARY="$(realpath "${SERVER_BINARY}")"
fi
prepare_paths
if ! find "${ASSET_DIR}" -maxdepth 1 -type f -name '*.big' -print -quit | grep -q .; then
    echo "ERROR: No Zero Hour .big assets found in ${ASSET_DIR}" >&2
    exit 1
fi

echo "NOTICE: This creates a local executable containing copyrighted retail game assets."
echo "        Build and use it only with assets you own; do not redistribute the result."

cd "${PROJECT_ROOT}"
validate_safe_output_target "${BUNDLE_ZIP}"
if [[ "${SKIP_GAME_BUILD}" -eq 0 ]]; then
    # GeneralsX @build Codex 04/08/2026 Configure fresh SFX builds so the optional Online endpoint reaches CMake.
    "${SCRIPT_DIR}/build-macos-zh.sh"
fi
"${SCRIPT_DIR}/bundle-macos-zh.sh"
if [[ ! -f "${BUNDLE_ZIP}" || -L "${BUNDLE_ZIP}" ||
      ! -s "${BUNDLE_ZIP}" ]]; then
    echo "ERROR: Portable macOS bundle is not a regular archive: ${BUNDLE_ZIP}" >&2
    exit 1
fi

WORK_DIR="$(mktemp -d "${OUTPUT_DIR}/.macos-sfx-stage.XXXXXX")"
trap cleanup EXIT
WORK_DIR="$(cd "${WORK_DIR}" && pwd -P)"
if directories_overlap "${ASSET_DIR}" "${WORK_DIR}"; then
    echo "ERROR: Temporary SFX workspace overlaps the retail asset tree." >&2
    exit 1
fi

RUNTIME_STAGE="${WORK_DIR}/runtime"
APP_STAGE="${WORK_DIR}/bundle"
mkdir -p "${RUNTIME_STAGE}" "${APP_STAGE}"

echo "Staging retail assets with copy-on-write when the filesystem supports it..."
if ! cp -cR "${ASSET_DIR}/." "${RUNTIME_STAGE}/" 2>/dev/null; then
    reset_runtime_stage "${RUNTIME_STAGE}"
    if command -v rsync >/dev/null 2>&1; then
        rsync -a "${ASSET_DIR}/" "${RUNTIME_STAGE}/"
    else
        cp -R "${ASSET_DIR}/." "${RUNTIME_STAGE}/"
    fi
fi
if [[ -e "${RUNTIME_STAGE}/online-server" || -L "${RUNTIME_STAGE}/online-server" ]]; then
    echo "ERROR: Retail assets contain reserved SFX path: online-server" >&2
    exit 1
fi

unzip -q "${BUNDLE_ZIP}" -d "${APP_STAGE}"
APP_RESOURCES="${APP_STAGE}/GeneralsXZH.app/Contents/Resources"
if [[ ! -x "${APP_RESOURCES}/bin/GeneralsXZH" || ! -d "${APP_RESOURCES}/lib" ]]; then
    echo "ERROR: Portable native runtime is incomplete inside ${BUNDLE_ZIP}" >&2
    exit 1
fi

echo "Overlaying the portable native runtime..."
cp "${APP_RESOURCES}/bin/GeneralsXZH" "${RUNTIME_STAGE}/GeneralsXZH"
chmod 0755 "${RUNTIME_STAGE}/GeneralsXZH"
rm -rf "${RUNTIME_STAGE}/lib"
cp -R "${APP_RESOURCES}/lib" "${RUNTIME_STAGE}/lib"

for runtime_file in MoltenVK_icd.json dxvk.conf; do
    if [[ -f "${APP_RESOURCES}/${runtime_file}" ]]; then
        cp "${APP_RESOURCES}/${runtime_file}" "${RUNTIME_STAGE}/${runtime_file}"
    fi
done
if [[ -d "${APP_RESOURCES}/fontconfig" ]]; then
    rm -rf "${RUNTIME_STAGE}/fontconfig"
    cp -R "${APP_RESOURCES}/fontconfig" "${RUNTIME_STAGE}/fontconfig"
fi

EXTRAS_MENU="${PROJECT_ROOT}/GeneralsZH/Data/Window/Menus/ExtrasMenu.wnd"
if [[ -f "${EXTRAS_MENU}" ]]; then
    mkdir -p "${RUNTIME_STAGE}/Window/Menus"
    cp "${EXTRAS_MENU}" "${RUNTIME_STAGE}/Window/Menus/ExtrasMenu.wnd"
fi

if [[ -n "${SERVER_BINARY}" ]]; then
    echo "Staging optional macOS/arm64 Online server..."
    mkdir -p "${RUNTIME_STAGE}/online-server"
    cp "${SERVER_BINARY}" "${RUNTIME_STAGE}/online-server/generals-server"
    chmod 0755 "${RUNTIME_STAGE}/online-server/generals-server"
    validate_online_server_binary "${RUNTIME_STAGE}/online-server/generals-server"
    ONLINE_SERVER_ENTRY="online-server/generals-server"
fi

echo "Validating staged macOS Mach-O dependency closure..."
verify_macho_dependency_closure \
    "${RUNTIME_STAGE}/GeneralsXZH" \
    "${RUNTIME_STAGE}/lib"
write_stage_manifest "${RUNTIME_STAGE}" "profiles/macos-zh.exclude"

VERSION="${GX_SFX_VERSION:-$(git -C "${PROJECT_ROOT}" rev-parse --short=12 HEAD)}"
if [[ -n "$(git -C "${PROJECT_ROOT}" status --porcelain --untracked-files=normal)" ]]; then
    VERSION="${VERSION}-dirty"
fi

echo "Building self-extracting executable (the xz and Go link stages take several minutes)..."
OUTPUT_TEMP="$(mktemp "${OUTPUT_DIR}/.$(basename -- "${OUTPUT}").packing.XXXXXX")"
PACKER_ARGS=(
    -source "${RUNTIME_STAGE}"
    -output "${OUTPUT_TEMP}"
    -target darwin/arm64
    -entry GeneralsXZH
    -workdir .
    -product GeneralsXZH
    -version "${VERSION}"
)
if [[ -n "${ONLINE_SERVER_ENTRY}" ]]; then
    PACKER_ARGS+=(-online-server-entry "${ONLINE_SERVER_ENTRY}")
fi
PACKER_ARGS+=(
    -exclude "${SFX_MODULE}/profiles/macos-zh.exclude"
    -module "${SFX_MODULE}"
    -compression xz
    -max-embed-bytes 1900000000
)
GOENV=off GOFLAGS= GOEXPERIMENT= GOTOOLCHAIN=local GOWORK=off \
go -C "${SFX_MODULE}" run ./cmd/generalsx-sfx-pack \
    "${PACKER_ARGS[@]}"

if command -v codesign >/dev/null 2>&1; then
    echo "Applying an ad-hoc macOS signature..."
    codesign \
        --force \
        --sign - \
        --identifier "${SIGNING_IDENTIFIER}" \
        --timestamp=none \
        "${OUTPUT_TEMP}"
    codesign --verify --strict --verbose=1 "${OUTPUT_TEMP}"
    SIGNATURE_DETAILS="$(codesign -d --verbose=4 "${OUTPUT_TEMP}" 2>&1)"
    if ! grep -Fqx "Identifier=${SIGNING_IDENTIFIER}" <<< "${SIGNATURE_DETAILS}"; then
        echo "ERROR: Signed SFX has an unexpected identifier." >&2
        printf '%s\n' "${SIGNATURE_DETAILS}" >&2
        exit 1
    fi
fi
validate_safe_output_target "${OUTPUT}"
mv -f -- "${OUTPUT_TEMP}" "${OUTPUT}"
OUTPUT_TEMP=""

echo "Packaging the self-extracting executable as a macOS app..."
# GeneralsX @build moloch 31/07/2026 Publish the SFX as the direct executable of a signed Finder app.
GX_SFX_APP_INPUT="${OUTPUT}" \
GX_SFX_APP_OUTPUT="${SFX_APP_OUTPUT}" \
    "${SCRIPT_DIR}/package-sfx-macos-zh-app.sh"

echo
echo "Self-extracting GeneralsXZH build complete:"
ls -lh "${OUTPUT}"
echo
echo "Inspect: ${OUTPUT} --sfx-info"
echo "Verify:  ${OUTPUT} --sfx-verify"
echo "Run:     ${OUTPUT} -win"
echo "App:     ${SFX_APP_OUTPUT}"
