#!/bin/bash
# GeneralsX @build BenderAI 03/03/2026 Bundle Linux GeneralsXZH binary + .so libs into a tarball archive
# Packages the same files as deploy-linux-zh.sh into GeneralsXZH-linux-x86_64.tar.gz

set -euo pipefail

# GeneralsX @bugfix BenderAI 09/03/2026 Resolve repository root correctly from scripts/build/linux.
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(cd "${SCRIPT_DIR}/../../.." && pwd)"
BUILD_DIR="${PROJECT_ROOT}/build/linux64-deploy"
DXVK_LIB_DIR="${BUILD_DIR}/_deps/dxvk-src/lib"
SDL3_LIB_DIR="${BUILD_DIR}/_deps/sdl3-build"
SDL3_IMAGE_LIB_DIR="${BUILD_DIR}/_deps/sdl3_image-build"
OPENAL_LIB_DIR="${BUILD_DIR}/_deps/openal_soft-build"
GAMESPY_LIB="${BUILD_DIR}/libgamespy.so"
FFMPEG_LIB_DIR="/usr/lib/x86_64-linux-gnu"
FFMPEG_DEP_LIB_DIR="/lib/x86_64-linux-gnu"
BINARY_SRC="${BUILD_DIR}/GeneralsMD/GeneralsXZH"
DXVK_CONF_SRC="${PROJECT_ROOT}/resources/dxvk/dxvk.conf"
OUTPUT_TARBALL="${PROJECT_ROOT}/GeneralsXZH-linux-x86_64.tar.gz"
MAX_REQUIRED_GLIBC_VERSION="2.38"
SYSTEM_LIBRARY_CACHE_OUTPUT=""
SYSTEM_LIBRARY_CACHE_READY=0
STAGE_DIR=""
OUTPUT_TEMP=""

cleanup() {
    if [[ -n "${OUTPUT_TEMP}" && "${OUTPUT_TEMP}" == "${PROJECT_ROOT}/.GeneralsXZH-linux-x86_64.tar.gz."* ]]; then
        rm -f -- "${OUTPUT_TEMP}"
    fi
    if [[ -n "${STAGE_DIR}" && "${STAGE_DIR}" == /tmp/* && -d "${STAGE_DIR}" ]]; then
        rm -rf -- "${STAGE_DIR}"
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

path_is_within_bundle() {
    local candidate="$1"
    local canonical
    if ! canonical="$(readlink -f -- "${candidate}")"; then
        return 1
    fi
    [[ "${canonical}" == "${BUNDLE_DIR}" || "${canonical}" == "${BUNDLE_DIR}/"* ]]
}

# Keep only the target loader and the glibc ABI libraries that must match the
# destination host. Everything else must be copied into and resolved from the
# portable bundle.
is_allowed_host_elf_dependency() {
    local soname="$1"
    local resolved="$2"
    local canonical

    if [[ "${soname}" == "linux-vdso.so.1" && "${resolved}" == "-" ]]; then
        return 0
    fi
    case "${soname}" in
        ld-linux-x86-64.so.2 | libc.so.6 | libm.so.6 | \
        libpthread.so.0 | librt.so.1 | libdl.so.2)
            ;;
        *)
            return 1
            ;;
    esac
    if ! canonical="$(readlink -f -- "${resolved}")"; then
        return 1
    fi
    case "${canonical}" in
        "/lib/x86_64-linux-gnu/${soname}" | \
        "/usr/lib/x86_64-linux-gnu/${soname}" | \
        "/lib64/${soname}" | \
        "/usr/lib64/${soname}")
            return 0
            ;;
    esac
    return 1
}

is_host_elf_soname() {
    case "$1" in
        ld-linux-x86-64.so.2 | libc.so.6 | libm.so.6 | \
        libpthread.so.0 | librt.so.1 | libdl.so.2)
            return 0
            ;;
    esac
    return 1
}

validate_elf64_amd64_object() {
    local root="$1"
    local header_output
    local program_output
    local line
    local interpreter=""
    local canonical
    local interpreter_count=0

    if [[ ! -f "${root}" ]]; then
        echo "ERROR: Cannot inspect missing ELF object: ${root}" >&2
        return 1
    fi
    if ! header_output="$(LC_ALL=C readelf -hW "${root}" 2>&1)"; then
        echo "ERROR: readelf failed while inspecting ${root}:" >&2
        printf '%s\n' "${header_output}" >&2
        return 1
    fi
    if grep -Eq '^readelf: (Error|Warning):' <<< "${header_output}" ||
       ! grep -Eq '^[[:space:]]*Class:[[:space:]]*ELF64[[:space:]]*$' <<< "${header_output}" ||
       ! grep -Eq '^[[:space:]]*Data:[[:space:]].*little endian[[:space:]]*$' <<< "${header_output}" ||
       ! grep -Eq '^[[:space:]]*Type:[[:space:]]*(EXEC|DYN)[[:space:]]' <<< "${header_output}" ||
       ! grep -Eq '^[[:space:]]*Machine:[[:space:]]*(Advanced Micro Devices X86-64|AMD x86-64)[[:space:]]*$' <<< "${header_output}"; then
        echo "ERROR: ELF object is not Linux/AMD64: ${root}" >&2
        return 1
    fi

    if ! program_output="$(LC_ALL=C readelf -lW "${root}" 2>&1)"; then
        echo "ERROR: readelf failed while inspecting program headers for ${root}:" >&2
        printf '%s\n' "${program_output}" >&2
        return 1
    fi
    if grep -Eq '^readelf: (Error|Warning):' <<< "${program_output}"; then
        echo "ERROR: readelf reported malformed program headers for ${root}:" >&2
        printf '%s\n' "${program_output}" >&2
        return 1
    fi
    while IFS= read -r line; do
        [[ "${line}" == *"Requesting program interpreter"* ]] || continue
        if [[ "${line}" =~ \[Requesting[[:space:]]program[[:space:]]interpreter:[[:space:]]([^]]+)\] ]]; then
            interpreter="${BASH_REMATCH[1]}"
            ((interpreter_count += 1))
        else
            echo "ERROR: Malformed PT_INTERP record in ${root}: ${line}" >&2
            return 1
        fi
    done <<< "${program_output}"
    if (( interpreter_count > 1 )); then
        echo "ERROR: ELF object contains multiple PT_INTERP records: ${root}" >&2
        return 1
    fi
    if (( interpreter_count == 1 )); then
        case "${interpreter}" in
            /lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 | \
            /usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2 | \
            /lib64/ld-linux-x86-64.so.2 | \
            /usr/lib64/ld-linux-x86-64.so.2)
                ;;
            *)
                echo "ERROR: Unsupported Linux/AMD64 ELF interpreter in ${root}: ${interpreter}" >&2
                return 1
                ;;
        esac
        if ! canonical="$(readlink -f -- "${interpreter}")" ||
           ! is_allowed_host_elf_dependency "ld-linux-x86-64.so.2" "${canonical}"; then
            echo "ERROR: ELF interpreter is unavailable or unsafe for ${root}: ${interpreter}" >&2
            return 1
        fi
    fi
}

read_elf_dynamic_section() {
    local root="$1"
    local dynamic_output

    if ! dynamic_output="$(LC_ALL=C readelf -dW "${root}" 2>&1)"; then
        echo "ERROR: readelf failed while inspecting dynamic metadata for ${root}:" >&2
        printf '%s\n' "${dynamic_output}" >&2
        return 1
    fi
    if grep -Eq '^readelf: (Error|Warning):' <<< "${dynamic_output}"; then
        echo "ERROR: readelf reported malformed dynamic metadata for ${root}:" >&2
        printf '%s\n' "${dynamic_output}" >&2
        return 1
    fi
    printf '%s\n' "${dynamic_output}"
}

read_elf_needed_names() {
    local root="$1"
    local dynamic_output
    local line
    local soname

    if ! dynamic_output="$(read_elf_dynamic_section "${root}")"; then
        return 1
    fi
    while IFS= read -r line; do
        [[ "${line}" == *"(NEEDED)"* ]] || continue
        if [[ "${line}" =~ \(NEEDED\).*Shared[[:space:]]library:[[:space:]]\[([^]]+)\][[:space:]]*$ ]]; then
            soname="${BASH_REMATCH[1]}"
        else
            echo "ERROR: Malformed DT_NEEDED record in ${root}: ${line}" >&2
            return 1
        fi
        if [[ ! "${soname}" =~ ^[A-Za-z0-9][A-Za-z0-9._+~-]*$ ]] ||
           [[ "${soname}" == */* || "${soname}" == *\\* ]]; then
            echo "ERROR: Refusing unsafe DT_NEEDED name in ${root}: ${soname}" >&2
            return 1
        fi
        printf '%s\n' "${soname}"
    done <<< "${dynamic_output}"
}

read_elf_search_directory_records() {
    local root="$1"
    local dynamic_output
    local line
    local raw_paths=""
    local run_paths=""
    local rpath_seen=0
    local runpath_seen=0
    local path_kind
    local path_list
    local entry
    local expanded
    local origin
    local canonical
    local -a entries=()

    if ! dynamic_output="$(read_elf_dynamic_section "${root}")"; then
        return 1
    fi
    while IFS= read -r line; do
        if [[ "${line}" == *"(RUNPATH)"* ]]; then
            if [[ "${line}" =~ \(RUNPATH\).*Library[[:space:]]runpath:[[:space:]]\[([^]]*)\][[:space:]]*$ ]]; then
                run_paths="${run_paths:+${run_paths}:}${BASH_REMATCH[1]}"
                ((runpath_seen += 1))
            else
                echo "ERROR: Malformed DT_RUNPATH record in ${root}: ${line}" >&2
                return 1
            fi
        elif [[ "${line}" == *"(RPATH)"* ]]; then
            if [[ "${line}" =~ \(RPATH\).*Library[[:space:]]rpath:[[:space:]]\[([^]]*)\][[:space:]]*$ ]]; then
                raw_paths="${raw_paths:+${raw_paths}:}${BASH_REMATCH[1]}"
                ((rpath_seen += 1))
            else
                echo "ERROR: Malformed DT_RPATH record in ${root}: ${line}" >&2
                return 1
            fi
        fi
    done <<< "${dynamic_output}"

    # The dynamic loader ignores DT_RPATH when DT_RUNPATH is present.
    if (( runpath_seen > 0 )); then
        path_kind="runpath"
        path_list="${run_paths}"
    elif (( rpath_seen > 0 )); then
        path_kind="rpath"
        path_list="${raw_paths}"
    else
        return 0
    fi
    if [[ -z "${path_list}" ]]; then
        echo "ERROR: Refusing an empty ELF ${path_kind^^} in ${root}" >&2
        return 1
    fi
    origin="$(dirname -- "$(readlink -f -- "${root}")")"
    IFS=':' read -r -a entries <<< "${path_list}"
    for entry in "${entries[@]}"; do
        if [[ -z "${entry}" ]]; then
            echo "ERROR: Refusing an empty ELF search-path component in ${root}" >&2
            return 1
        fi
        expanded="${entry//\$\{ORIGIN\}/${origin}}"
        expanded="${expanded//\$ORIGIN/${origin}}"
        if [[ "${expanded}" == *'$'* || "${expanded}" != /* ||
              "${expanded}" == *$'\t'* || "${expanded}" == *$'\r'* ||
              "${expanded}" == *$'\n'* ]]; then
            echo "ERROR: Unsupported ELF search path in ${root}: ${entry}" >&2
            return 1
        fi
        if [[ -e "${expanded}" && ! -d "${expanded}" ]]; then
            echo "ERROR: ELF search path is not a directory in ${root}: ${entry}" >&2
            return 1
        fi
        if [[ -d "${expanded}" ]]; then
            canonical="$(readlink -f -- "${expanded}")"
        else
            canonical="$(readlink -m -- "${expanded}")"
        fi
        if [[ -z "${canonical}" || "${canonical}" != /* ]]; then
            echo "ERROR: Cannot canonicalize ELF search path in ${root}: ${entry}" >&2
            return 1
        fi
        printf '%s\t%s\n' "${path_kind}" "${canonical}"
    done
}

is_allowed_elf_search_directory() {
    local directory="$1"

    if [[ "${directory}" == "${BUNDLE_DIR}" ||
          "${directory}" == "${BUNDLE_DIR}/"* ]]; then
        return 0
    fi
    case "${directory}" in
        /lib/x86_64-linux-gnu | /usr/lib/x86_64-linux-gnu | \
        /lib64 | /usr/lib64 | /lib | /usr/lib)
            return 0
            ;;
    esac
    return 1
}

validate_elf_rpath_policy() {
    local root="$1"
    local search_output
    local kind
    local directory

    if ! search_output="$(read_elf_search_directory_records "${root}")"; then
        return 1
    fi
    while IFS=$'\t' read -r kind directory; do
        # RUNPATH follows the launcher's private LD_LIBRARY_PATH, so the
        # resolver can model it dependency-by-dependency. RPATH is inherited
        # and precedes LD_LIBRARY_PATH; reject external RPATH roots outright
        # rather than trying to approximate ancestry-dependent loader state.
        [[ "${kind}" == "rpath" ]] || continue
        if ! is_allowed_elf_search_directory "${directory}"; then
            echo "ERROR: ${root} has an external DT_RPATH directory: ${directory}" >&2
            return 1
        fi
    done <<< "${search_output}"
}

read_system_library_candidates() {
    local soname="$1"
    local candidate
    local -a defaults=(
        /lib/x86_64-linux-gnu
        /usr/lib/x86_64-linux-gnu
        /lib64
        /usr/lib64
        /lib
        /usr/lib
    )

    if (( SYSTEM_LIBRARY_CACHE_READY != 1 )); then
        echo "ERROR: Internal ELF resolver error: system library cache was not initialized." >&2
        return 1
    fi
    if [[ -n "${SYSTEM_LIBRARY_CACHE_OUTPUT}" ]]; then
        awk -v soname="${soname}" '
            $1 == soname &&
            $0 ~ /(x86-64|x86_64)/ &&
            $NF ~ /^\// &&
            !seen[$NF]++ { print $NF }
        ' <<< "${SYSTEM_LIBRARY_CACHE_OUTPUT}"
    fi
    for candidate in "${defaults[@]}"; do
        if [[ -f "${candidate}/${soname}" ]]; then
            printf '%s\n' "${candidate}/${soname}"
        fi
    done
}

initialize_system_library_cache() {
    local ldconfig_command=""

    SYSTEM_LIBRARY_CACHE_OUTPUT=""
    if command -v ldconfig >/dev/null 2>&1; then
        ldconfig_command="$(command -v ldconfig)"
    elif [[ -x /sbin/ldconfig ]]; then
        ldconfig_command="/sbin/ldconfig"
    elif [[ -x /usr/sbin/ldconfig ]]; then
        ldconfig_command="/usr/sbin/ldconfig"
    fi
    if [[ -n "${ldconfig_command}" ]] &&
       ! SYSTEM_LIBRARY_CACHE_OUTPUT="$("${ldconfig_command}" -p 2>&1)"; then
        echo "ERROR: Unable to inspect the system ELF library cache:" >&2
        printf '%s\n' "${SYSTEM_LIBRARY_CACHE_OUTPUT}" >&2
        return 1
    fi
    SYSTEM_LIBRARY_CACHE_READY=1
}

resolve_elf_dependency() {
    local root="$1"
    local soname="$2"
    local candidate
    local canonical
    local search_output
    local system_output
    local kind
    local directory

    if ! search_output="$(read_elf_search_directory_records "${root}")"; then
        return 2
    fi

    # DT_RPATH (when no DT_RUNPATH exists) precedes LD_LIBRARY_PATH. Check it
    # before the private bundle so same-SONAME host collisions cannot hide.
    while IFS=$'\t' read -r kind directory; do
        [[ "${kind}" == "rpath" ]] || continue
        candidate="${directory}/${soname}"
        [[ -f "${candidate}" ]] || continue
        if ! canonical="$(readlink -f -- "${candidate}")"; then
            echo "ERROR: Cannot canonicalize dependency candidate ${candidate}" >&2
            return 2
        fi
        printf '%s\n' "${canonical}"
        return 0
    done <<< "${search_output}"

    candidate="${BUNDLE_DIR}/${soname}"
    if [[ -e "${candidate}" || -L "${candidate}" ]]; then
        if [[ ! -f "${candidate}" ]] ||
           ! canonical="$(readlink -f -- "${candidate}")" ||
           ! path_is_within_bundle "${canonical}"; then
            echo "ERROR: Bundled dependency is not a safe in-bundle file: ${candidate}" >&2
            return 2
        fi
        printf '%s\n' "${canonical}"
        return 0
    fi

    # DT_RUNPATH follows LD_LIBRARY_PATH, represented here by BUNDLE_DIR.
    while IFS=$'\t' read -r kind directory; do
        [[ "${kind}" == "runpath" ]] || continue
        candidate="${directory}/${soname}"
        [[ -f "${candidate}" ]] || continue
        if ! canonical="$(readlink -f -- "${candidate}")"; then
            echo "ERROR: Cannot canonicalize dependency candidate ${candidate}" >&2
            return 2
        fi
        printf '%s\n' "${canonical}"
        return 0
    done <<< "${search_output}"

    if ! system_output="$(read_system_library_candidates "${soname}")"; then
        return 2
    fi
    while IFS= read -r candidate; do
        [[ -n "${candidate}" && -f "${candidate}" ]] || continue
        if ! canonical="$(readlink -f -- "${candidate}")"; then
            echo "ERROR: Cannot canonicalize dependency candidate ${candidate}" >&2
            return 2
        fi
        printf '%s\n' "${canonical}"
        return 0
    done <<< "${system_output}"
    return 1
}

copy_regular_elf_dependency() {
    local source="$1"
    local soname="$2"
    local destination
    local copy_temp

    if [[ -z "${soname}" || "${soname}" == */* ||
          "$(basename -- "${soname}")" != "${soname}" ]]; then
        echo "ERROR: Refusing unsafe ELF dependency name: ${soname}" >&2
        return 1
    fi
    destination="${BUNDLE_DIR}/${soname}"
    if [[ ! -f "${source}" ]]; then
        echo "ERROR: Resolved dependency is not a regular file: ${source}" >&2
        return 1
    fi
    if [[ -e "${destination}" || -L "${destination}" ]]; then
        if [[ -f "${destination}" ]] && cmp -s -- "${source}" "${destination}"; then
            if [[ ! -L "${destination}" ]]; then
                return 0
            fi
        else
            echo "ERROR: Conflicting bundled dependency: ${destination}" >&2
            return 1
        fi
    fi

    copy_temp="$(mktemp "${BUNDLE_DIR}/.elf-dependency.XXXXXX")"
    if ! cp --dereference --preserve=mode,timestamps -- "${source}" "${copy_temp}"; then
        rm -f -- "${copy_temp}"
        echo "ERROR: Failed to copy ELF dependency ${source}" >&2
        return 1
    fi
    mv -fT -- "${copy_temp}" "${destination}"
}

copy_elf_dependencies() {
    local root="$1"
    local needed_output
    local soname
    local resolved
    local canonical
    local resolution_status

    validate_elf64_amd64_object "${root}"
    if ! needed_output="$(read_elf_needed_names "${root}")"; then
        return 1
    fi

    while IFS= read -r soname; do
        [[ -n "${soname}" ]] || continue
        resolution_status=0
        resolved="$(resolve_elf_dependency "${root}" "${soname}")" ||
            resolution_status=$?
        if (( resolution_status == 1 )); then
            echo "ERROR: Missing ELF dependency ${soname} required by ${root}" >&2
            return 1
        elif (( resolution_status != 0 )); then
            return 1
        fi
        if ! canonical="$(readlink -f -- "${resolved}")"; then
            echo "ERROR: Cannot canonicalize dependency ${resolved} required by ${root}" >&2
            return 1
        fi
        if path_is_within_bundle "${canonical}" ||
           is_allowed_host_elf_dependency "${soname}" "${canonical}"; then
            continue
        fi
        copy_regular_elf_dependency "${canonical}" "${soname}"
    done <<< "${needed_output}"
}

verify_elf_dependency_closure() {
    local root="$1"
    local needed_output
    local soname
    local resolved
    local canonical
    local resolution_status

    validate_elf64_amd64_object "${root}"
    validate_elf_rpath_policy "${root}"
    if ! needed_output="$(read_elf_needed_names "${root}")"; then
        return 1
    fi
    while IFS= read -r soname; do
        [[ -n "${soname}" ]] || continue
        resolution_status=0
        resolved="$(resolve_elf_dependency "${root}" "${soname}")" ||
            resolution_status=$?
        if (( resolution_status == 1 )); then
            echo "ERROR: ${root} has unresolved dependency ${soname}" >&2
            return 1
        elif (( resolution_status != 0 )); then
            return 1
        fi
        if ! canonical="$(readlink -f -- "${resolved}")"; then
            echo "ERROR: Cannot canonicalize ${resolved} required by ${root}" >&2
            return 1
        fi
        if ! path_is_within_bundle "${canonical}" &&
           ! is_allowed_host_elf_dependency "${soname}" "${canonical}"; then
            echo "ERROR: ${root} resolves ${soname} outside the bundle: ${canonical}" >&2
            return 1
        fi
    done <<< "${needed_output}"
}

read_required_glibc_versions() {
    local root="$1"
    local version_output
    local line
    local in_needs=0

    if ! version_output="$(LC_ALL=C readelf --version-info -W "${root}" 2>&1)"; then
        echo "ERROR: readelf failed while inspecting symbol versions for ${root}:" >&2
        printf '%s\n' "${version_output}" >&2
        return 1
    fi
    if grep -Eq '^readelf: (Error|Warning):' <<< "${version_output}"; then
        echo "ERROR: readelf reported malformed symbol-version metadata for ${root}:" >&2
        printf '%s\n' "${version_output}" >&2
        return 1
    fi
    while IFS= read -r line; do
        if [[ "${line}" == "Version needs section "* ]]; then
            in_needs=1
            continue
        fi
        if [[ "${line}" == "Version "* && "${line}" != "Version needs section "* ]]; then
            in_needs=0
        fi
        (( in_needs == 1 )) || continue
        if [[ "${line}" =~ Name:[[:space:]]GLIBC_([0-9]+(\.[0-9]+)+)([[:space:]]|$) ]]; then
            printf '%s\n' "${BASH_REMATCH[1]}"
        fi
    done <<< "${version_output}"
}

version_is_greater() {
    local left="$1"
    local right="$2"
    local left_part
    local right_part
    local index
    local -a left_parts=()
    local -a right_parts=()

    IFS='.' read -r -a left_parts <<< "${left}"
    IFS='.' read -r -a right_parts <<< "${right}"
    for ((index = 0;
         index < ${#left_parts[@]} || index < ${#right_parts[@]};
         index++)); do
        left_part="${left_parts[index]:-0}"
        right_part="${right_parts[index]:-0}"
        while [[ "${#left_part}" -gt 1 && "${left_part}" == 0* ]]; do
            left_part="${left_part#0}"
        done
        while [[ "${#right_part}" -gt 1 && "${right_part}" == 0* ]]; do
            right_part="${right_part#0}"
        done
        if (( ${#left_part} > ${#right_part} )); then
            return 0
        elif (( ${#left_part} < ${#right_part} )); then
            return 1
        elif [[ "${left_part}" > "${right_part}" ]]; then
            return 0
        elif [[ "${left_part}" < "${right_part}" ]]; then
            return 1
        fi
    done
    return 1
}

validate_required_glibc_ceiling() {
    local root
    local versions
    local version
    local maximum="0"

    for root in "$@"; do
        if ! versions="$(read_required_glibc_versions "${root}")"; then
            return 1
        fi
        while IFS= read -r version; do
            [[ -n "${version}" ]] || continue
            if version_is_greater "${version}" "${maximum}"; then
                maximum="${version}"
            fi
        done <<< "${versions}"
    done
    if version_is_greater "${maximum}" "${MAX_REQUIRED_GLIBC_VERSION}"; then
        echo "ERROR: Bundle requires GLIBC_${maximum}, exceeding policy ceiling GLIBC_${MAX_REQUIRED_GLIBC_VERSION}." >&2
        return 1
    fi
    if [[ "${maximum}" == "0" ]]; then
        echo "Validated required glibc symbol ceiling: none (policy <= GLIBC_${MAX_REQUIRED_GLIBC_VERSION})"
    else
        echo "Validated required glibc symbol ceiling: GLIBC_${maximum} (policy <= GLIBC_${MAX_REQUIRED_GLIBC_VERSION})"
    fi
}

validate_existing_bundle() {
    local requested_bundle_dir="$1"
    local symlink_target
    local -a elf_roots=()

    if [[ -L "${requested_bundle_dir}" || ! -d "${requested_bundle_dir}" ]]; then
        echo "ERROR: Bundle validation target must be a real directory: ${requested_bundle_dir}" >&2
        return 1
    fi
    BUNDLE_DIR="$(cd "${requested_bundle_dir}" && pwd -P)"
    if [[ ! -f "${BUNDLE_DIR}/GeneralsXZH" ||
          -L "${BUNDLE_DIR}/GeneralsXZH" ||
          ! -x "${BUNDLE_DIR}/GeneralsXZH" ]]; then
        echo "ERROR: Bundle is missing a regular executable GeneralsXZH: ${BUNDLE_DIR}" >&2
        return 1
    fi
    if ! command -v readelf >/dev/null 2>&1; then
        echo "ERROR: readelf is required to validate the Linux/AMD64 dependency closure." >&2
        return 1
    fi
    initialize_system_library_cache

    for _host_soname in \
        ld-linux-x86-64.so.2 libc.so.6 libm.so.6 \
        libpthread.so.0 librt.so.1 libdl.so.2; do
        if [[ -e "${BUNDLE_DIR}/${_host_soname}" ||
              -L "${BUNDLE_DIR}/${_host_soname}" ]]; then
            echo "ERROR: Bundle must use the target host ${_host_soname}, not ship its own copy." >&2
            return 1
        fi
    done

    mapfile -t elf_roots < <(
        find "${BUNDLE_DIR}" -maxdepth 1 -type f \
            \( -name 'GeneralsXZH' -o -name '*.so' -o -name '*.so.*' \) |
            LC_ALL=C sort
    )
    if (( ${#elf_roots[@]} == 0 )); then
        echo "ERROR: Bundle contains no ELF objects: ${BUNDLE_DIR}" >&2
        return 1
    fi
    for _elf_root in "${elf_roots[@]}"; do
        verify_elf_dependency_closure "${_elf_root}"
    done
    validate_required_glibc_ceiling "${elf_roots[@]}"

    while IFS= read -r -d '' _bundle_symlink; do
        symlink_target="$(readlink -- "${_bundle_symlink}")"
        if [[ "${symlink_target}" == /* ]] ||
           ! path_is_within_bundle "${_bundle_symlink}"; then
            echo "ERROR: Bundle contains escaping symlink ${_bundle_symlink} -> ${symlink_target}" >&2
            return 1
        fi
    done < <(find "${BUNDLE_DIR}" -maxdepth 1 -type l -print0)

    echo "Validated Linux/AMD64 ELF dependency closure (${#elf_roots[@]} objects): ${BUNDLE_DIR}"
}

if (( $# != 0 )); then
    if [[ "$1" == "--validate-directory" && $# == 2 ]]; then
        validate_existing_bundle "$2"
        exit 0
    fi
    echo "Usage: $0 [--validate-directory BUNDLE_DIRECTORY]" >&2
    exit 2
fi

echo "Bundling GeneralsXZH (Linux x86_64)"

validate_safe_output_target "${OUTPUT_TARBALL}"
if ! command -v readelf >/dev/null 2>&1; then
    echo "ERROR: readelf is required to validate the Linux/AMD64 dependency closure." >&2
    exit 1
fi
initialize_system_library_cache

# Validate binary
if [[ ! -f "${BINARY_SRC}" ]]; then
    echo "ERROR: Binary not found at ${BINARY_SRC}"
    echo "Build first: ./scripts/build/linux/docker-build-linux-zh.sh linux64-deploy"
    exit 1
fi
if [[ ! -s "${BINARY_SRC}" ]]; then
    echo "ERROR: Binary at ${BINARY_SRC} is empty - build may have failed"
    exit 1
fi

# Check if DXVK libraries exist
if [[ ! -d "${DXVK_LIB_DIR}" ]]; then
    echo "ERROR: DXVK libraries not found at ${DXVK_LIB_DIR}"
    echo "Configure first: ./scripts/build/linux/docker-configure-linux.sh linux64-deploy"
    exit 1
fi

# Check if SDL3 libraries exist
if [[ ! -d "${SDL3_LIB_DIR}" ]]; then
    echo "ERROR: SDL3 libraries not found at ${SDL3_LIB_DIR}"
    echo "Build first: ./scripts/build/linux/docker-build-linux-zh.sh linux64-deploy"
    exit 1
fi

if [[ ! -d "${SDL3_IMAGE_LIB_DIR}" ]]; then
    echo "ERROR: SDL3_image libraries not found at ${SDL3_IMAGE_LIB_DIR}"
    echo "Build first: ./scripts/build/linux/docker-build-linux-zh.sh linux64-deploy"
    exit 1
fi

# Check if GameSpy library exists
if [[ ! -f "${GAMESPY_LIB}" ]]; then
    echo "ERROR: GameSpy library not found at ${GAMESPY_LIB}"
    echo "Build first: ./scripts/build/linux/docker-build-linux-zh.sh linux64-deploy"
    exit 1
fi

# Prepare temp staging directory
STAGE_DIR="$(mktemp -d /tmp/generalsx-linux-bundle.XXXXXX)"
trap cleanup EXIT
BUNDLE_DIR="${STAGE_DIR}/GeneralsXZH-linux"
mkdir -p "${BUNDLE_DIR}"
BUNDLE_DIR="$(cd "${BUNDLE_DIR}" && pwd -P)"

echo "  Staging files to ${BUNDLE_DIR}..."

# Binary
echo "  + GeneralsXZH"
cp "${BINARY_SRC}" "${BUNDLE_DIR}/GeneralsXZH"
chmod +x "${BUNDLE_DIR}/GeneralsXZH"

# DXVK libraries
echo "  + DXVK libraries"
shopt -s nullglob
dxvk_libraries=(
    "${DXVK_LIB_DIR}"/libdxvk_d3d8.so*
    "${DXVK_LIB_DIR}"/libdxvk_d3d9.so*
)
shopt -u nullglob
if (( ${#dxvk_libraries[@]} == 0 )) ||
   ! compgen -G "${DXVK_LIB_DIR}/libdxvk_d3d8.so*" > /dev/null; then
    echo "ERROR: Missing required DXVK D3D8 runtime under ${DXVK_LIB_DIR}" >&2
    exit 1
fi
cp -a "${dxvk_libraries[@]}" "${BUNDLE_DIR}/"

# SDL3 and SDL3_image libraries
echo "  + SDL3 libraries"
cp "${SDL3_LIB_DIR}"/libSDL3.so* "${BUNDLE_DIR}/"
cp "${SDL3_IMAGE_LIB_DIR}"/libSDL3_image.so* "${BUNDLE_DIR}/"

# GeneralsX @bugfix moloch 30/07/2026 Bundle fetched OpenAL Soft instead of relying on a host SONAME.
echo "  + OpenAL Soft libraries"
if compgen -G "${OPENAL_LIB_DIR}/libopenal.so*" > /dev/null; then
    cp -a "${OPENAL_LIB_DIR}"/libopenal.so* "${BUNDLE_DIR}/"
else
    echo "ERROR: Missing required OpenAL Soft runtime under ${OPENAL_LIB_DIR}" >&2
    exit 1
fi

# GameSpy library
echo "  + GameSpy library"
cp "${GAMESPY_LIB}" "${BUNDLE_DIR}/"

# SagePatch (optional, gated by RTS_BUILD_OPTION_SAGE_PATCH at configure time).
SAGE_PATCH_LIB="${BUILD_DIR}/Patches/SagePatch/libsage_patch.so"
if [[ -f "${SAGE_PATCH_LIB}" ]]; then
    echo "  + libsage_patch (SagePatch QoL)"
    cp "${SAGE_PATCH_LIB}" "${BUNDLE_DIR}/"
fi

# GeneralsX @build GitHubCopilot 17/05/2026 Copy FFmpeg runtime libs transitively so the bundle is independent of host SONAME layout.
echo "  + FFmpeg runtime libraries"
shopt -s nullglob
ffmpeg_roots=(
    "${FFMPEG_LIB_DIR}"/libavcodec.so*
    "${FFMPEG_LIB_DIR}"/libavformat.so*
    "${FFMPEG_LIB_DIR}"/libavutil.so*
    "${FFMPEG_LIB_DIR}"/libswresample.so*
    "${FFMPEG_LIB_DIR}"/libswscale.so*
    "${FFMPEG_DEP_LIB_DIR}"/libavcodec.so*
    "${FFMPEG_DEP_LIB_DIR}"/libavformat.so*
    "${FFMPEG_DEP_LIB_DIR}"/libavutil.so*
    "${FFMPEG_DEP_LIB_DIR}"/libswresample.so*
    "${FFMPEG_DEP_LIB_DIR}"/libswscale.so*
)
shopt -u nullglob
for ffmpeg_root in "${ffmpeg_roots[@]}"; do
    cp -a "${ffmpeg_root}" "${BUNDLE_DIR}/"
    copy_elf_dependencies "${ffmpeg_root}"
done

if ! compgen -G "${BUNDLE_DIR}/libavcodec.so*" > /dev/null; then
    echo "ERROR: Missing required bundle library libavcodec.so*"
    exit 1
fi

# GeneralsX @bugfix moloch 30/07/2026 Close and verify the complete ELF dependency graph.
echo "  + resolving transitive ELF dependencies"
for _dependency_pass in $(seq 1 20); do
    _before_count="$(find "${BUNDLE_DIR}" -maxdepth 1 \( -type f -o -type l \) | wc -l)"
    while IFS= read -r _elf_root; do
        copy_elf_dependencies "${_elf_root}"
    done < <(find "${BUNDLE_DIR}" -maxdepth 1 -type f \( -name 'GeneralsXZH' -o -name '*.so' -o -name '*.so.*' \) | sort)
    _after_count="$(find "${BUNDLE_DIR}" -maxdepth 1 \( -type f -o -type l \) | wc -l)"
    if [[ "${_before_count}" == "${_after_count}" ]]; then
        break
    fi
    if [[ "${_dependency_pass}" == "20" ]]; then
        echo "ERROR: ELF dependency closure did not converge after 20 passes." >&2
        exit 1
    fi
done

while IFS= read -r _elf_root; do
    verify_elf_dependency_closure "${_elf_root}"
done < <(find "${BUNDLE_DIR}" -maxdepth 1 -type f \( -name 'GeneralsXZH' -o -name '*.so' -o -name '*.so.*' \) | sort)

mapfile -t _bundled_elf_roots < <(
    find "${BUNDLE_DIR}" -maxdepth 1 -type f \
        \( -name 'GeneralsXZH' -o -name '*.so' -o -name '*.so.*' \) |
        LC_ALL=C sort
)
validate_required_glibc_ceiling "${_bundled_elf_roots[@]}"

while IFS= read -r -d '' _bundle_symlink; do
    _symlink_target="$(readlink -- "${_bundle_symlink}")"
    if [[ "${_symlink_target}" == /* ]] ||
       ! path_is_within_bundle "${_bundle_symlink}"; then
        echo "ERROR: Bundle contains escaping symlink ${_bundle_symlink} -> ${_symlink_target}" >&2
        exit 1
    fi
done < <(find "${BUNDLE_DIR}" -maxdepth 1 -type l -print0)

# DXVK config
if [[ -f "${DXVK_CONF_SRC}" ]]; then
    echo "  + dxvk.conf"
    cp "${DXVK_CONF_SRC}" "${BUNDLE_DIR}/dxvk.conf"
else
    echo "WARNING: ${DXVK_CONF_SRC} not found - terrain shaders may fail"
fi

# Run wrapper
echo "  + run.sh"
cat > "${BUNDLE_DIR}/run.sh" << 'WRAPPER'
#!/bin/bash
# GeneralsX @build BenderAI 03/03/2026 - Linux wrapper for bundled runtime
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

# Set LD_LIBRARY_PATH to find DXVK, SDL3, and other libs in same directory
export LD_LIBRARY_PATH="${SCRIPT_DIR}:${LD_LIBRARY_PATH:-}"

# Set DXVK environment
export DXVK_WSI_DRIVER="SDL3"
export DXVK_LOG_LEVEL="${DXVK_LOG_LEVEL:-info}"

# SagePatch (optional QoL: F11 screenshot, Scroll Lock cursor lock,
# Ctrl+PgUp/Dn brightness, Ctrl+1..5 window snap). Loaded via LD_PRELOAD only
# if libsage_patch.so is bundled. DXVK_HUD defaults to "fps" when active.
if [[ -f "${SCRIPT_DIR}/libsage_patch.so" && "${SAGE_PATCH_DISABLED:-0}" != "1" ]]; then
    if [[ -n "${LD_PRELOAD:-}" ]]; then
        export LD_PRELOAD="${SCRIPT_DIR}/libsage_patch.so:${LD_PRELOAD}"
    else
        export LD_PRELOAD="${SCRIPT_DIR}/libsage_patch.so"
    fi
    export DXVK_HUD="${DXVK_HUD:-fps}"
else
    export DXVK_HUD="${DXVK_HUD:-0}"
fi

# Auto-detect base Generals install path
if [[ -z "${CNC_GENERALS_INSTALLPATH:-}" && -d "${SCRIPT_DIR}/../Generals" ]]; then
    export CNC_GENERALS_INSTALLPATH="${SCRIPT_DIR}/../Generals/"
fi

# GeneralsX @bugfix BenderAI 06/03/2026 - Exclude LLVMpipe Vulkan ICD (LLVM 20.x crash workaround)
# libvulkan_lvp.so (LLVMpipe) crashes during static initialization with LLVM 20.x.
# Filter hardware-only ICDs via VK_DRIVER_FILES to prevent loading the crashing library.
# User can override by setting VK_DRIVER_FILES or VK_ICD_FILENAMES before running.
if [[ -z "${VK_DRIVER_FILES:-}" && -z "${VK_ICD_FILENAMES:-}" ]]; then
    _hw_icds=""
    for _dir in /usr/share/vulkan/icd.d /etc/vulkan/icd.d; do
        [[ -d "$_dir" ]] || continue
        for _f in "$_dir"/*.json; do
            [[ -f "$_f" ]] || continue
            _base="$(basename "$_f")"
            case "${_base,,}" in
                *lvp* | *lavapipe* | *softpipe* | *llvmpipe*)
                    echo "INFO: Vulkan ICD filter: skipping software ICD '$_base'" ;;
                *)
                    _hw_icds="${_hw_icds:+${_hw_icds}:}$_f" ;;
            esac
        done
    done
    if [[ -n "$_hw_icds" ]]; then
        export VK_DRIVER_FILES="$_hw_icds"
        echo "INFO: Vulkan ICD filter: VK_DRIVER_FILES=$VK_DRIVER_FILES"
    else
        echo "WARNING: Vulkan ICD filter: no hardware ICDs found, LLVMpipe exclusion skipped"
        echo "WARNING: If startup crashes, set VK_DRIVER_FILES to your hardware Vulkan ICD JSON"
    fi
fi

# GeneralsX @bugfix 09/03/2026 - Work around openal-soft 1.25.1 movaps alignment crash
# alcOpenDevice() crashes with SIGSEGV in a 'movaps %xmm1, 0x26260(%rbx)' instruction
# inside openal-soft's device initializer. movaps requires 16-byte alignment; if the
# ALCdevice struct is not aligned correctly, it faults regardless of which backend is
# selected. Disabling CPU extensions forces openal-soft to use scalar code paths that
# do not have alignment requirements. The pipewire backend is also excluded because it
# has its own crash at device-open time on PipeWire 1.4.x.
# These env vars are read by openal-soft's static constructor at library load time,
# so they must be set here in the launcher before the binary starts.
# User can override by setting ALSOFT_DISABLE_CPU_EXTS or ALSOFT_DRIVERS explicitly.
if [[ -z "${ALSOFT_DISABLE_CPU_EXTS:-}" ]]; then
    export ALSOFT_DISABLE_CPU_EXTS="all"
    echo "INFO: OpenAL: ALSOFT_DISABLE_CPU_EXTS=all (movaps alignment crash workaround)"
fi
if [[ -z "${ALSOFT_DRIVERS:-}" ]]; then
    export ALSOFT_DRIVERS="pulse,alsa,oss,jack,null,wave"
    echo "INFO: OpenAL: ALSOFT_DRIVERS=$ALSOFT_DRIVERS (pipewire excluded)"
fi

exec "${SCRIPT_DIR}/GeneralsXZH" "$@"
WRAPPER
chmod +x "${BUNDLE_DIR}/run.sh"

# Create tarball
echo ""
echo "Creating ${OUTPUT_TARBALL}..."
OUTPUT_TEMP="$(mktemp "${PROJECT_ROOT}/.GeneralsXZH-linux-x86_64.tar.gz.XXXXXX")"
(cd "${STAGE_DIR}" && tar -czf "${OUTPUT_TEMP}" GeneralsXZH-linux/)
mv -f -- "${OUTPUT_TEMP}" "${OUTPUT_TARBALL}"
OUTPUT_TEMP=""

echo ""
echo "Bundle complete: ${OUTPUT_TARBALL}"
echo "Contents:"
tar -tzf "${OUTPUT_TARBALL}" | sed -n '1,30p'
echo ""
echo "To use: extract alongside your game data directory (GeneralsZH/)"
echo "  (legacy fallback also supported: GeneralsMD/)"
echo "  tar -xzf GeneralsXZH-linux-x86_64.tar.gz"
echo "  ./GeneralsXZH-linux/run.sh -win"
