#!/usr/bin/env bash
# GeneralsX @build Codex 10/10/2026 Share native runtime closure validation with CI exports.
# Source this file after setting RUNTIME_STAGE to its canonical absolute path.

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
