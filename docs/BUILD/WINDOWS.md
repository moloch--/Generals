# GeneralsX - Windows Cross-Build Instructions

The Windows target is currently an exploratory 32-bit MinGW cross-build. It
can be compiled and linked from Linux or macOS, including Apple Silicon hosts,
but it is not yet a runtime-ready Windows distribution.

## Prerequisites

- Docker Desktop on macOS, or Docker Engine on Linux
- Git and CMake available on the host
- A recursive checkout of this repository
- Approximately 10 GB of free disk space for the Docker image and build tree

Docker supplies the i686 MinGW compiler and Wine's `widl` interface generator.
No host MinGW installation is required.

## Build Zero Hour

From the repository root:

```bash
GX_MINGW_JOBS=8 \
  ./scripts/build/linux/docker-build-mingw-zh.sh mingw-w64-i686
```

`GX_MINGW_JOBS` is optional and defaults to `8`. The script validates that the
expected output exists and writes its full build log to:

```text
logs/build_zh_mingw-w64-i686_docker.log
```

The release artifacts are:

```text
build/mingw-w64-i686/GeneralsMD/generalszh.exe
build/mingw-w64-i686/GeneralsMD/generalszh.exe.debug
```

The `.debug` file contains unstripped symbols and is not the executable to
launch or distribute.

## Rebuild an Existing Tree

After the wrapper has configured the build tree, individual targets can be
rebuilt inside the MinGW image. The primary target is:

```bash
cmake --build build/mingw-w64-i686 --target z_generals --parallel 8
```

This command must run in the `generalsx/mingw-builder:latest` container, not
directly against the host compiler. The wrapper is the supported entry point
because it mounts the checkout, selects the correct container platform, and
preserves host file ownership.

## Current Validation Boundary

A successful build currently proves:

- The shared source configures with the i686 MinGW toolchain.
- Zero Hour compiles and links into a PE32 GUI executable.
- GCC, libstdc++, and winpthreads runtime libraries are linked statically.
- Debug symbols are split into a companion artifact.

It does not yet prove that the executable starts or runs gameplay on Windows.
The current binary has three known runtime blockers:

- It imports `d3dx8d.dll`, which is not supplied by the retail game.
- The fetched Bink and Miles import libraries use symbol decoration that does
  not match the retail `binkw32.dll` and `mss32.dll` exports.
- The Bink and Miles DLLs generated in the dependency build are no-op link
  stubs; they must not be packaged as gameplay replacements.

Do not copy the generated stub DLLs into a retail installation. Windows and
Wine launch testing becomes meaningful only after the import ABI and D3DX
compatibility path are repaired and a deployment step stages the correct
runtime files.

## Troubleshooting

### `widl` is missing

Rebuild the MinGW Docker image so it includes `wine64-tools`:

```bash
./scripts/env/docker/docker-build-images.sh mingw
```

### Apple Silicon reports an image platform mismatch

Use the repository wrapper. It explicitly selects the image's `linux/amd64`
platform while Docker performs the emulation.

### Build files are owned by root

Use the repository wrapper rather than invoking `docker run` manually. It
passes the host user and group IDs into the container.

### Reduce CPU or memory pressure

Set a smaller positive job count:

```bash
GX_MINGW_JOBS=4 \
  ./scripts/build/linux/docker-build-mingw-zh.sh mingw-w64-i686
```
