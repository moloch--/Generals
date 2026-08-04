#!/usr/bin/env bash
# Build GeneralsXZH (Zero Hour) for Linux using Docker
# Usage: ./scripts/build/linux/docker-build-linux-zh.sh [preset]

set -euo pipefail

PRESET="${1:-linux64-deploy}"
LOG_FILE="logs/build_zh_${PRESET}_docker.log"
DOCKER_IMAGE="generalsx/linux-builder:latest"
CONTAINER_NAME="generalsx-build-zh-${PRESET}"

# GeneralsX @build BenderAI 24/03/2026 Preserve host file ownership for bind mounts and vcpkg cache.
HOST_UID="$(id -u)"
HOST_GID="$(id -g)"
VCPKG_DIR="${VCPKG_DIR:-${HOME}/.generalsx/vcpkg}"

if [[ ! "${PRESET}" =~ ^[A-Za-z0-9][A-Za-z0-9._-]*$ ]]; then
    echo "ERROR: Invalid CMake preset name: ${PRESET}" >&2
    exit 2
fi

# GeneralsX @bugfix Codex 04/08/2026 Keep the array non-empty for macOS Bash 3.2.
# Docker's name-only -e form copies a set host value (including empty) and
# leaves the container variable absent when the host variable is unset.
DOCKER_ONLINE_ENV=(-e GX_ONLINE_SERVER_DEFAULT)

echo "🐳 Building GeneralsXZH (Linux, preset: ${PRESET})..."
mkdir -p logs
if [[ -L "$VCPKG_DIR" || ( -e "$VCPKG_DIR" && ! -d "$VCPKG_DIR" ) ]]; then
    echo "ERROR: vcpkg cache must be a real directory, not a symlink or file: $VCPKG_DIR" >&2
    exit 1
fi
mkdir -p -- "$VCPKG_DIR"
if [[ -L "$VCPKG_DIR" || ! -d "$VCPKG_DIR" ]]; then
    echo "ERROR: vcpkg cache changed while it was being prepared: $VCPKG_DIR" >&2
    exit 1
fi

if [[ ! -w "$VCPKG_DIR" ]]; then
    echo "ERROR: vcpkg directory is not writable: $VCPKG_DIR" >&2
    echo "Fix ownership or set VCPKG_DIR to a writable path." >&2
    exit 1
fi

# Check if container is already running
if docker ps --format '{{.Names}}' | grep -q "^${CONTAINER_NAME}$"; then
    echo "⚠️  Container '${CONTAINER_NAME}' is already running!"
    echo "Wait for the current build to finish or stop it with:"
    echo "    docker stop ${CONTAINER_NAME}"
    exit 1
fi

# Check if Docker image exists, build if not
if ! docker image inspect "$DOCKER_IMAGE" &> /dev/null; then
    echo "⚠️  Docker image not found: $DOCKER_IMAGE"
    echo "📦 Building image (this will take a few minutes)..."
    # GeneralsX @bugfix BenderAI 14/03/2026 Follow scripts/env/docker relocation for builder image bootstrap.
    ./scripts/env/docker/docker-build-images.sh linux
fi

docker run --rm \
    --name "$CONTAINER_NAME" \
    --platform linux/amd64 \
    --user "${HOST_UID}:${HOST_GID}" \
    -e HOME=/tmp/generalsx-home \
    -e XDG_CACHE_HOME=/tmp/generalsx-cache \
    "${DOCKER_ONLINE_ENV[@]}" \
    -v "$PWD:/work" \
    -v "$VCPKG_DIR:/opt/vcpkg" \
    -w /work \
    "$DOCKER_IMAGE" \
    bash -c "
        set -e
        mkdir -p \"\$HOME\" \"\$XDG_CACHE_HOME\"

        PROC=1
        if command -v nproc &> /dev/null; then
            PROC=\$(( (\$(nproc) + 1) / 2 ))
        fi
        echo \"🛠  Using \$PROC parallel jobs for building...\"
        
        # Bootstrap vcpkg in Docker volume if not exists
        if [ ! -f /opt/vcpkg/vcpkg ]; then
            echo '📦 Bootstrapping vcpkg (first time, will be cached in Docker volume)...'
            if find /opt/vcpkg -mindepth 1 -maxdepth 1 -print -quit | grep -q .; then
                echo 'ERROR: vcpkg cache is non-empty but incomplete; move it aside after reviewing its contents, then rerun.' >&2
                exit 1
            fi
            git clone https://github.com/microsoft/vcpkg.git /opt/vcpkg
            /opt/vcpkg/bootstrap-vcpkg.sh -disableMetrics
        fi
        
        export VCPKG_ROOT=/opt/vcpkg

        # GeneralsX @bugfix Copilot 23/04/2026 Drop stale host-generated CMake cache when running inside /work container mount.
        CACHE_FILE='build/${PRESET}/CMakeCache.txt'
        if [ -f \$CACHE_FILE ]; then
            CACHE_HOME_DIR=\$(sed -n 's#^CMAKE_HOME_DIRECTORY:INTERNAL=##p' \$CACHE_FILE | head -n1)
            CACHE_BUILD_DIR=\$(sed -n 's#^CMAKE_CACHEFILE_DIR:INTERNAL=##p' \$CACHE_FILE | head -n1)
            if [ \$CACHE_HOME_DIR != '/work' ] || [ \$CACHE_BUILD_DIR != '/work/build/${PRESET}' ]; then
                echo '🧹 Removing incompatible CMake cache generated outside Docker...'
                rm -rf 'build/${PRESET}'
            fi
        fi
        
        CMAKE_CONFIGURE_ARGS=(--preset '${PRESET}')
        # GeneralsX @bugfix Codex 04/08/2026 Preserve unset for manual reuse; empty explicitly clears a cached Online endpoint.
        if [[ \${GX_ONLINE_SERVER_DEFAULT+x} == x ]]; then
            CMAKE_CONFIGURE_ARGS+=("-DSAGE_ONLINE_SERVER_DEFAULT=\${GX_ONLINE_SERVER_DEFAULT}")
            echo '🌐 Applying GX_ONLINE_SERVER_DEFAULT (empty clears the cache value).'
        fi
        echo '⚙️  Configuring CMake with vcpkg...'
        cmake "\${CMAKE_CONFIGURE_ARGS[@]}"
        
        echo '🔨 Building GeneralsXZH...'
        cmake --build build/${PRESET} --target z_generals -j\$PROC
        
        echo '✅ Build complete!'
        ls -lh build/${PRESET}/GeneralsMD/GeneralsXZH || echo '⚠️  Binary not found'
    " 2>&1 | tee "$LOG_FILE"

echo "✅ Build complete. Log: $LOG_FILE"
