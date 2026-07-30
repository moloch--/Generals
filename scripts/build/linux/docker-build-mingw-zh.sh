#!/usr/bin/env bash
# Build GeneralsXZH for Windows using MinGW cross-compiler in Docker
# Usage: ./scripts/build/linux/docker-build-mingw-zh.sh [preset]
# Environment: GX_MINGW_JOBS controls build parallelism (default: 8).

set -euo pipefail

PRESET="${1:-mingw-w64-i686}"
LOG_FILE="logs/build_zh_${PRESET}_docker.log"
DOCKER_IMAGE="generalsx/mingw-builder:latest"
CONTAINER_NAME="generalsx-build-mingw-zh-${PRESET}"
BUILD_JOBS="${GX_MINGW_JOBS:-8}"

if [[ ! "$BUILD_JOBS" =~ ^[1-9][0-9]*$ ]]; then
    echo "ERROR: GX_MINGW_JOBS must be a positive integer" >&2
    exit 2
fi

# GeneralsX @build BenderAI 24/03/2026 Preserve host file ownership for bind mounts created by cross-builds.
HOST_UID="$(id -u)"
HOST_GID="$(id -g)"

echo "🐳 Building GeneralsXZH (Windows/MinGW, preset: ${PRESET})..."
mkdir -p logs

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
    ./scripts/env/docker/docker-build-images.sh mingw
fi

# GeneralsX @bugfix OpenAI 29/07/2026 Keep the amd64 MinGW image runnable on Apple Silicon hosts.
docker run --rm \
    --platform linux/amd64 \
    --name "$CONTAINER_NAME" \
    --user "${HOST_UID}:${HOST_GID}" \
    -e HOME=/tmp/generalsx-home \
    -e XDG_CACHE_HOME=/tmp/generalsx-cache \
    -v "$PWD:/work" \
    -w /work \
    "$DOCKER_IMAGE" \
    bash -c "
        set -e
        mkdir -p \"\$HOME\" \"\$XDG_CACHE_HOME\"
        
        echo '⚙️  Configuring CMake (MinGW cross-compile)...'
        cmake --preset ${PRESET}
        
        echo '🔨 Building GeneralsXZH (Windows .exe)...'
        # GeneralsX @performance OpenAI 29/07/2026 Use bounded parallelism for emulated cross-builds.
        cmake --build build/${PRESET} --target z_generals --parallel ${BUILD_JOBS}
        
        echo '✅ Build complete!'
        # GeneralsX @bugfix OpenAI 29/07/2026 Fail when the actual Windows output is missing.
        test -f build/${PRESET}/GeneralsMD/generalszh.exe
        ls -lh build/${PRESET}/GeneralsMD/generalszh.exe
    " 2>&1 | tee "$LOG_FILE"

echo "✅ Build complete. Log: $LOG_FILE"
echo "ℹ️  Cross-compile/link validation only; retail runtime dependencies are not packaged yet."
