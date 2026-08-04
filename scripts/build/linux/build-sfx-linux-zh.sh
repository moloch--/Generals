#!/usr/bin/env bash
# GeneralsX @build moloch 30/07/2026 Package the native Linux game and owned retail assets as one Go SFX binary.

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
SFX_MODULE="${PROJECT_ROOT}/scripts/tooling/sfx"
ASSET_DIR="${GX_SFX_ASSET_DIR:-${HOME}/GeneralsX/GeneralsZH}"
# GeneralsX @feature Codex 04/08/2026 Optionally authenticate and expose a target-native Online server sidecar.
SERVER_BINARY="${GX_SFX_SERVER_BINARY:-}"
OUTPUT="${GX_SFX_OUTPUT:-${PROJECT_ROOT}/build/sfx/GeneralsXZH-linux-amd64-sfx}"
OUTPUT_DIR=""
BUNDLE_TARBALL="${PROJECT_ROOT}/GeneralsXZH-linux-x86_64.tar.gz"
DOCKER_IMAGE="${GX_SFX_LINUX_BUILDER_IMAGE:-generalsx/linux-builder:latest}"
PRESET="linux64-deploy"
SKIP_GAME_BUILD=0
REUSE_BUNDLE=0
WORK_DIR=""
MANIFEST_TEMP=""
STAGE_MANIFEST=""
RUNTIME_STAGE=""
ONLINE_SERVER_ENTRY=""

cleanup() {
    if [[ -n "${MANIFEST_TEMP}" && "${MANIFEST_TEMP}" == "${OUTPUT_DIR}/.sfx-stage-contents."* ]]; then
        rm -f -- "${MANIFEST_TEMP}"
    fi
    if [[ -n "${WORK_DIR}" &&
          "${WORK_DIR}" == "${OUTPUT_DIR}/.linux-sfx-stage."* &&
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

directories_overlap() {
    local first="${1%/}"
    local second="${2%/}"
    [[ "${first}" == "${second}" ||
       "${first}" == "${second}/"* ||
       "${second}" == "${first}/"* ]]
}

validate_online_server_binary() {
    local candidate="$1"
    local description

    if [[ -L "${candidate}" || ! -f "${candidate}" || ! -x "${candidate}" ]]; then
        echo "ERROR: GX_SFX_SERVER_BINARY must name a regular executable: ${candidate}" >&2
        return 1
    fi
    if ! description="$(file -b "${candidate}")" ||
       [[ "${description}" != ELF\ 64-bit\ LSB*x86-64* ]]; then
        echo "ERROR: Online server is not a Linux ELF x86-64 executable: ${candidate}" >&2
        echo "  Detected: ${description:-unknown}" >&2
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
    local output_name

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
    if directories_overlap "${ASSET_DIR}" "${prospective_output_dir}"; then
        echo "ERROR: SFX output/workspace directory overlaps the retail asset tree." >&2
        echo "  Assets: ${ASSET_DIR}" >&2
        echo "  Output: ${prospective_output_dir}" >&2
        return 1
    fi

    mkdir -p -- "${requested_output_dir}"
    OUTPUT_DIR="$(cd "${requested_output_dir}" && pwd -P)"
    OUTPUT="${OUTPUT_DIR}/${output_name}"
    STAGE_MANIFEST="${OUTPUT}.stage-contents.txt"
    validate_safe_output_target "${OUTPUT}"
    validate_safe_output_target "${STAGE_MANIFEST}"

    if [[ "${OUTPUT}" == "${BUNDLE_TARBALL}" ||
          ( -e "${OUTPUT}" && -e "${BUNDLE_TARBALL}" &&
            "${OUTPUT}" -ef "${BUNDLE_TARBALL}" ) ]]; then
        echo "ERROR: SFX output must not replace its Linux runtime bundle." >&2
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

validate_portable_runtime() {
    local portable_runtime="$1"

    if command -v docker >/dev/null 2>&1 &&
       docker image inspect "${DOCKER_IMAGE}" >/dev/null 2>&1; then
        echo "Validating the staged Linux/AMD64 dependency closure inside ${DOCKER_IMAGE}..."
        docker run --rm \
            --platform linux/amd64 \
            --user "$(id -u):$(id -g)" \
            -e HOME=/tmp/generalsx-home \
            -v "${PROJECT_ROOT}:/work:ro" \
            -v "${portable_runtime}:/bundle:ro" \
            -w /work \
            "${DOCKER_IMAGE}" \
            bash ./scripts/build/linux/bundle-linux-zh.sh \
                --validate-directory /bundle
    elif [[ "$(uname -s)" == "Linux" && "$(uname -m)" == "x86_64" ]]; then
        echo "Validating the staged Linux/AMD64 dependency closure on the native host..."
        "${SCRIPT_DIR}/bundle-linux-zh.sh" \
            --validate-directory "${portable_runtime}"
    else
        echo "ERROR: Revalidating the portable Linux runtime requires either" >&2
        echo "       ${DOCKER_IMAGE} or a native Linux/AMD64 host." >&2
        return 1
    fi
}

usage() {
    cat <<'USAGE'
Usage: ./scripts/build/linux/build-sfx-linux-zh.sh [OPTIONS]

Builds one Linux/amd64 Go executable containing the GeneralsXZH runtime and
the locally owned retail assets from:
  $GX_SFX_ASSET_DIR (default: $HOME/GeneralsX/GeneralsZH)

Set $GX_SFX_SERVER_BINARY to an optional Linux/amd64 generals-server binary.
It is staged at online-server/generals-server and declared to the SFX launcher.

The native Linux build and portable dependency bundle use the project's
existing Docker workflow. Output can be changed with $GX_SFX_OUTPUT.

Options:
  --skip-game-build  Reuse build/linux64-deploy.
  --reuse-bundle     Reuse the tarball, then revalidate it in Docker/Linux.
  -h, --help         Show this help.
USAGE
}

for argument in "$@"; do
    case "${argument}" in
        --skip-game-build)
            SKIP_GAME_BUILD=1
            ;;
        --reuse-bundle)
            SKIP_GAME_BUILD=1
            REUSE_BUNDLE=1
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

if ! command -v go >/dev/null 2>&1; then
    echo "ERROR: Go is required. Install Go 1.25 or newer." >&2
    exit 1
fi
if ! command -v xz >/dev/null 2>&1; then
    echo "ERROR: xz is required for the multi-gigabyte retail payload." >&2
    exit 1
fi
if [[ ! -d "${ASSET_DIR}" ]]; then
    echo "ERROR: Retail asset directory not found: ${ASSET_DIR}" >&2
    exit 1
fi
if [[ -n "${SERVER_BINARY}" ]]; then
    if ! command -v file >/dev/null 2>&1; then
        echo "ERROR: file is required to validate GX_SFX_SERVER_BINARY." >&2
        exit 1
    fi
    validate_online_server_binary "${SERVER_BINARY}"
    SERVER_BINARY="$(cd "$(dirname "${SERVER_BINARY}")" && pwd -P)/$(basename "${SERVER_BINARY}")"
fi
prepare_paths
if ! find "${ASSET_DIR}" -maxdepth 1 -type f -name '*.big' -print -quit | grep -q .; then
    echo "ERROR: No Zero Hour .big assets found in ${ASSET_DIR}" >&2
    exit 1
fi

echo "NOTICE: This creates a local executable containing copyrighted retail game assets."
echo "        Build and use it only with assets you own; do not redistribute the result."

cd "${PROJECT_ROOT}"
if [[ "${SKIP_GAME_BUILD}" -eq 0 ]]; then
    "${SCRIPT_DIR}/docker-build-linux-zh.sh" "${PRESET}"
fi

if [[ "${REUSE_BUNDLE}" -eq 0 ]]; then
    if command -v docker >/dev/null 2>&1 &&
       docker image inspect "${DOCKER_IMAGE}" >/dev/null 2>&1; then
        echo "Bundling the Linux dependency closure inside ${DOCKER_IMAGE}..."
        docker run --rm \
            --platform linux/amd64 \
            --user "$(id -u):$(id -g)" \
            -e HOME=/tmp/generalsx-home \
            -v "${PROJECT_ROOT}:/work" \
            -w /work \
            "${DOCKER_IMAGE}" \
            bash ./scripts/build/linux/bundle-linux-zh.sh
    elif [[ "$(uname -s)" == "Linux" && "$(uname -m)" == "x86_64" ]]; then
        echo "Docker builder unavailable; bundling with the native Linux host."
        "${SCRIPT_DIR}/bundle-linux-zh.sh"
    else
        echo "ERROR: The Linux builder image is unavailable." >&2
        echo "Run ${SCRIPT_DIR}/docker-build-linux-zh.sh ${PRESET} first." >&2
        echo "--reuse-bundle skips regeneration, but a Docker/Linux validator" >&2
        echo "is still required before the runtime can be packed." >&2
        exit 1
    fi
fi

if [[ ! -f "${BUNDLE_TARBALL}" || -L "${BUNDLE_TARBALL}" ||
      ! -s "${BUNDLE_TARBALL}" ]]; then
    echo "ERROR: Portable Linux bundle not found: ${BUNDLE_TARBALL}" >&2
    exit 1
fi

WORK_DIR="$(mktemp -d "${OUTPUT_DIR}/.linux-sfx-stage.XXXXXX")"
trap cleanup EXIT
WORK_DIR="$(cd "${WORK_DIR}" && pwd -P)"
if directories_overlap "${ASSET_DIR}" "${WORK_DIR}"; then
    echo "ERROR: Temporary SFX workspace overlaps the retail asset tree." >&2
    exit 1
fi

RUNTIME_STAGE="${WORK_DIR}/runtime"
BUNDLE_STAGE="${WORK_DIR}/bundle"
mkdir -p "${RUNTIME_STAGE}" "${BUNDLE_STAGE}"

echo "Staging retail assets with copy-on-write when the filesystem supports it..."
if [[ "$(uname -s)" == "Darwin" ]] &&
   cp -cR "${ASSET_DIR}/." "${RUNTIME_STAGE}/" 2>/dev/null; then
    :
elif cp -a --reflink=auto "${ASSET_DIR}/." "${RUNTIME_STAGE}/" 2>/dev/null; then
    :
else
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

tar -xzf "${BUNDLE_TARBALL}" -C "${BUNDLE_STAGE}"
PORTABLE_RUNTIME="${BUNDLE_STAGE}/GeneralsXZH-linux"
if [[ ! -x "${PORTABLE_RUNTIME}/GeneralsXZH" ]]; then
    echo "ERROR: Portable Linux runtime is incomplete inside ${BUNDLE_TARBALL}" >&2
    exit 1
fi
validate_portable_runtime "${PORTABLE_RUNTIME}"

echo "Overlaying the portable native runtime..."
cp "${PORTABLE_RUNTIME}/GeneralsXZH" "${RUNTIME_STAGE}/GeneralsXZH"
chmod 0755 "${RUNTIME_STAGE}/GeneralsXZH"
rm -rf "${RUNTIME_STAGE}/lib"
mkdir -p "${RUNTIME_STAGE}/lib"
shopt -s nullglob
LINUX_LIBRARIES=("${PORTABLE_RUNTIME}"/*.so "${PORTABLE_RUNTIME}"/*.so.*)
shopt -u nullglob
if (( ${#LINUX_LIBRARIES[@]} == 0 )); then
    echo "ERROR: Portable Linux bundle contains no shared libraries." >&2
    exit 1
fi
cp -a "${LINUX_LIBRARIES[@]}" "${RUNTIME_STAGE}/lib/"
if ! compgen -G "${RUNTIME_STAGE}/lib/libopenal.so*" > /dev/null; then
    echo "ERROR: Portable Linux bundle is missing its fetched OpenAL Soft runtime." >&2
    exit 1
fi
if [[ -f "${PORTABLE_RUNTIME}/dxvk.conf" ]]; then
    cp "${PORTABLE_RUNTIME}/dxvk.conf" "${RUNTIME_STAGE}/dxvk.conf"
fi

EXTRAS_MENU="${PROJECT_ROOT}/GeneralsZH/Data/Window/Menus/ExtrasMenu.wnd"
if [[ -f "${EXTRAS_MENU}" ]]; then
    mkdir -p "${RUNTIME_STAGE}/Window/Menus"
    cp "${EXTRAS_MENU}" "${RUNTIME_STAGE}/Window/Menus/ExtrasMenu.wnd"
fi

if [[ -n "${SERVER_BINARY}" ]]; then
    echo "Staging optional Linux/amd64 Online server..."
    mkdir -p "${RUNTIME_STAGE}/online-server"
    cp "${SERVER_BINARY}" "${RUNTIME_STAGE}/online-server/generals-server"
    chmod 0755 "${RUNTIME_STAGE}/online-server/generals-server"
    validate_online_server_binary "${RUNTIME_STAGE}/online-server/generals-server"
    ONLINE_SERVER_ENTRY="online-server/generals-server"
fi

write_stage_manifest "${RUNTIME_STAGE}" "profiles/linux-zh.exclude"

VERSION="${GX_SFX_VERSION:-$(git -C "${PROJECT_ROOT}" rev-parse --short=12 HEAD)}"
if [[ -n "$(git -C "${PROJECT_ROOT}" status --porcelain --untracked-files=normal)" ]]; then
    VERSION="${VERSION}-dirty"
fi

echo "Building Linux self-extracting executable..."
PACKER_ARGS=(
    -source "${RUNTIME_STAGE}"
    -output "${OUTPUT}"
    -target linux/amd64
    -entry GeneralsXZH
    -workdir .
    -product GeneralsXZH
    -version "${VERSION}"
)
if [[ -n "${ONLINE_SERVER_ENTRY}" ]]; then
    PACKER_ARGS+=(-online-server-entry "${ONLINE_SERVER_ENTRY}")
fi
PACKER_ARGS+=(
    -exclude "${SFX_MODULE}/profiles/linux-zh.exclude"
    -module "${SFX_MODULE}"
    -compression xz
    -max-embed-bytes 1900000000
)
GOENV=off GOFLAGS= GOEXPERIMENT= GOTOOLCHAIN=local GOWORK=off \
go -C "${SFX_MODULE}" run ./cmd/generalsx-sfx-pack \
    "${PACKER_ARGS[@]}"

echo
echo "Self-extracting GeneralsXZH Linux build complete:"
ls -lh "${OUTPUT}"
echo
echo "Run on Linux/amd64: ${OUTPUT} -win"
