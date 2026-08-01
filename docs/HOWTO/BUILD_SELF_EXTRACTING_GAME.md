# Build and Run a Self-Extracting GeneralsXZH Executable

## Overview

The GeneralsX self-extracting (SFX) tooling packages one target-native
GeneralsXZH runtime and a locally staged retail-data tree into one executable.
On first launch, that executable verifies and extracts its payload into a
private content-addressed cache, then starts the native game directly. Later
launches reuse the verified cache.

On macOS, the integrated build also places that same executable directly inside
a signed `GeneralsXZH.app`. The app adds normal Finder/Dock metadata and a
Retina `.icns` icon. A small, separately signed AppKit helper presents
first-launch extraction progress while the Go SFX remains the only game
launcher and source of cache state.

An SFX is native to one operating system and CPU architecture. It is not an
emulator or a universal binary:

- `darwin/arm64` packages the macOS/Apple Silicon game.
- `linux/amd64` packages the Linux/x86-64 game.
- `windows/amd64` is the recommended wrapper for the exploratory Windows/x86
  game on 64-bit Windows.
- `windows/386` is supported for smaller 32-bit wrapper payloads, but is not
  recommended for a retail-sized embed.

The launcher OS/architecture is host-native; Windows can launch the embedded
PE32 child normally. The launcher rejects a payload on a different declared
host OS or architecture.

> **Validation status (2026-07-31):** the retail-sized macOS/ARM64 artifact
> passed strict ad-hoc signature and full extraction verification, then reached
> the Zero Hour main menu on cold and warm-cache launches. Its Finder-recognized
> `.app`, Retina icon, sealed resources, ARM64 entrypoint, and LaunchServices
> invocation also passed local validation. Its embedded payload
> is 1,780,979,836 compressed bytes and 2,957,172,396 extracted bytes. The
> Linux/AMD64 artifact passed full extraction/file-hash verification at
> 1,883,386,056 compressed bytes and 3,333,325,193 extracted bytes; a graphical
> run still requires a native Linux/Vulkan host. Windows gameplay remains
> blocked by the native runtime dependencies described below. These measured
> artifacts recorded embedded version `5b2bdadb0ed4-dirty`; their wrapper
> checksums will change with a commit, version override, rebuild, or asset
> change. The progress-enabled macOS app integration was subsequently validated
> with a signed fixture bundle, cold/warm cache launches, and macOS
> Accessibility inspection; the measured retail artifact predates that UI.

## Before You Begin

The packer does not download retail assets and does not establish a right to
redistribute them.

- Use a legitimate Command & Conquer: Generals Zero Hour installation that
  you own.
- Do not commit retail `.big` files, generated payloads, or retail-backed SFX
  executables to the repository.
- Keep the resulting executable for personal use unless you have permission
  to redistribute every included file.
- Review the licenses of the native libraries in the staged runtime.

See [Getting the Game Files](GETTING_THE_GAME_FILES.md) for supported ways to
obtain a local installation. Exclusion profiles remove known installers and
obsolete runtime files, but they are not a license audit.

For macOS/ARM64, install:

- Xcode Command Line Tools and the normal GeneralsX build dependencies;
- Go 1.22 or newer;
- XZ (`brew install xz`);
- `unzip`.

The current macOS native binary declares macOS 15.0 as its minimum OS version.
The current Linux artifact requires an x86_64 host with glibc 2.38 or newer
(an Ubuntu 24.04-class userspace) plus a native display and Vulkan-capable
GPU/driver stack. Bundled application libraries do not make the SFX hermetic
with respect to libc, the kernel, or GPU drivers.

Allow enough free space for the source installation, a private staging copy,
the compressed artifact, and a complete temporary extraction during
verification.

## Build on macOS/ARM64

Run these commands from the repository root on an Apple Silicon Mac:

```bash
export GX_SFX_ASSET_DIR="${HOME}/GeneralsX/GeneralsZH"
export GX_SFX_OUTPUT="${PWD}/build/sfx/GeneralsXZH-macos-arm64-sfx"
export GX_SFX_APP_OUTPUT="${PWD}/build/sfx/GeneralsXZH.app"

./scripts/build/macos/build-sfx-macos-zh.sh
```

The script:

1. builds the `macos-vulkan` GeneralsXZH target;
2. creates the portable native bundle;
3. copies the locally owned retail tree into a private stage;
4. overlays `GeneralsXZH`, its ARM64 libraries, and runtime configuration;
5. records a reviewable pre-exclusion candidate inventory beside the output;
6. validates the complete staged Mach-O closure and every ARM64 slice;
7. excludes files unused by the native macOS build;
8. packs the stage with XZ and embeds it in a Go launcher;
9. applies a local ad-hoc signature to the raw executable;
10. packages that executable as a proper ARM64 `.app`, compiles its native
    progress helper, generates all ten icon representations from 16x16 through
    1024x1024, and signs and validates the completed bundle.

The default asset directory is `~/GeneralsX/GeneralsZH`, and the default output
files are `build/sfx/GeneralsXZH-macos-arm64-sfx` and
`build/sfx/GeneralsXZH.app`. Override the recorded SFX version when needed:

```bash
export GX_SFX_VERSION="test-2026-07-31"
```

The script appends `-dirty` when the Git worktree is not clean. To reuse an
already built `macos-vulkan` game binary while iterating on packaging:

```bash
./scripts/build/macos/build-sfx-macos-zh.sh --skip-game-build
```

This still recreates the bundle, stage, compressed payload, and SFX launcher.
The sibling `${GX_SFX_OUTPUT}.stage-contents.txt` file lists staged candidates
before exclusion-profile filtering. Review it together with the selected
profile; it is not the final embedded manifest.

To rebuild only the `.app` around an existing SFX executable:

```bash
./scripts/build/macos/package-sfx-macos-zh-app.sh
```

The app packager uses an APFS copy-on-write clone when available, stages and
signs the nested AppKit progress helper and complete bundle before publication,
retains the previous matching bundle for rollback during replacement, and
refuses to replace a bundle with a different identifier. The integrated build
also rejects an app output inside the retail tree or either output nested
inside the other. Its optional metadata/signing overrides are
`GX_SFX_APP_VERSION`, `GX_SFX_APP_BUILD_VERSION`,
`GX_SFX_APP_ICON_SOURCE`, and `GX_SFX_APP_CODESIGN_IDENTITY`.

The tracked icon source is 650x650. The generated 1024x1024 Retina
representation is therefore upscaled; replacing the source with true authored
1024x1024 artwork would improve the largest Finder preview in a future release.

## Inspect, Verify, and Run

Set a convenient shell variable:

```bash
artifact="${PWD}/build/sfx/GeneralsXZH-macos-arm64-sfx"
app="${PWD}/build/sfx/GeneralsXZH.app"
```

Inspect its embedded metadata without extracting the full game:

```bash
"${artifact}" --sfx-info
```

Perform a full integrity check before the first launch:

```bash
"${artifact}" --sfx-verify
```

This verifies the compressed-payload digest, extracts into a temporary
directory, validates the archive structure, and checks every regular file
against the manifest. It needs temporary disk space for the complete extracted
tree.

Launch Zero Hour in a window:

```bash
"${artifact}" -win

# Or launch the app through Finder/LaunchServices:
open -n "${app}" --args -win
```

You can also double-click `GeneralsXZH.app` in Finder. With no arguments it
extracts or verifies its private cache and launches the game normally. On a
cache miss, the app displays an animated package-checking phase, byte-accurate
file-extraction progress, and an animated validation phase. The window is not
opened on warm launches or when using the raw SFX executable.

The local build is ad-hoc signed, so its plist and icon are sealed and signature
corruption is detectable. It is not Developer ID signed or notarized. Normal
Gatekeeper-trusted public distribution still requires a Developer ID identity,
hardened-runtime review, notarization, and stapling; do not represent an ad-hoc
build as notarized.

Game arguments are passed directly, without a shell. If a game argument has
the same name as an SFX option, put it after `--`:

```bash
"${artifact}" -- --sfx-info
```

## SFX Commands

| Command | Purpose |
|---|---|
| `--sfx-help` | Show launcher help; `-h` and `--help` are aliases |
| `--sfx-info` | Show product, version, target, payload sizes and hashes, entrypoint, and cache root |
| `--sfx-verify` | Verify the payload and every extracted file in a temporary directory |
| `--sfx-extract DIRECTORY` | Verify and extract to a new destination; the destination must not already exist |
| `--sfx-purge-cache` | Remove only this artifact's current content-addressed cache entry |
| `--sfx-notices` | Print embedded third-party license notices |
| `-- GAME_ARGUMENT...` | Treat everything after `--` as game arguments |

`--sfx-help` and `--sfx-notices` work without loading a payload.
Payload-inspection commands require the artifact's target OS and architecture.

## First-Launch Cache

The first normal launch extracts into:

```text
os.UserCacheDir()/GeneralsX/sfx
```

Typical locations are:

- macOS: `~/Library/Caches/GeneralsX/sfx`
- Linux: `${XDG_CACHE_HOME:-~/.cache}/GeneralsX/sfx`
- Windows: `%LocalAppData%\GeneralsX\sfx`

The content key combines the manifest and compressed-payload SHA-256 values.
Changing either publishes a separate cache entry rather than updating an
existing runtime in place.

The native payload stays immutable. On macOS and Linux, the game process uses
a separate product-stable writable directory for its current directory,
GameSpy identity/queue files, and DXVK cache/log state:

```text
cache-root/
└── GeneralsXZH/
    ├── .locks/
    │   ├── CONTENT_KEY.guard  # permanent acquisition-lock inode
    │   └── CONTENT_KEY.lease  # permanent runtime/purge-lock inode
    ├── CONTENT_KEY/           # extracted payload, rehashed before every launch
    └── .runtime-state/        # writable state shared across content versions
```

By default, asset, library, and configuration paths reference the verified
payload. Explicit inherited path/configuration environment overrides take
precedence and deliberately opt those resources out of that guarantee. With
the default environment, the Unix platform filesystem resolves relative reads
against the immutable asset root before normal BIG archives and routes
relative writes to `.runtime-state`; writable state therefore cannot override
packaged assets.
Windows retains the payload's manifest working directory as its process current
directory for compatibility, while DXVK state is directed to `.runtime-state`.
Use the normal unpacked development build, not the SFX, for editor workflows
that rely on writing and then reading relative temporary files.

On macOS or Linux, use another local executable filesystem with:

```bash
GX_SFX_CACHE="${HOME}/.generalsx-sfx-cache" "${artifact}" -win
```

Choose a dedicated directory. If it does not exist, the launcher creates it
privately. On macOS and Linux, an existing override must already be a real
owner-only directory:

```bash
mkdir -m 700 "${HOME}/.generalsx-sfx-cache"
```

The launcher rejects a pre-existing group/world-accessible cache root instead
of changing its permissions. A cache on a `noexec` filesystem cannot start the
native game. `GX_SFX_CACHE` overrides are disabled on Windows because the Go
standard library cannot verify an owner-only DACL for an arbitrary directory.
Windows uses and trusts the OS-provided `%LocalAppData%` per-user cache
location without independently verifying its DACL.

On a cache miss, the launcher takes exclusive kernel-backed acquisition and
runtime leases, extracts into a private same-filesystem staging directory,
checks every file, writes its completion marker last, and atomically publishes
the result. In the macOS app, only the process performing that extraction opens
the native progress window; concurrent launches waiting for its cache result do
not claim duplicate progress.
Concurrent first launches wait for the same content entry. Cache hits validate
the complete expected structure, metadata, symlinks, and every manifest-listed
payload regular-file SHA-256 while holding a shared lease, which remains held
until that game process exits. Warm launches skip decompression but still read
and hash the complete manifest-listed payload, so startup work remains
proportional to the roughly 3 GiB extracted tree. The permanent lock files use
`flock` on macOS/Linux and `LockFileEx` on Windows; a crashed wrapper releases
them automatically.
Purge deliberately retains `.locks` and its stable inodes. Do not unlink,
replace, or manually prune them, and do not manually remove a content
directory while a wrapper or native child may still use it. There is currently
no automatic garbage collection for older content keys; each retained retail
runtime consumes roughly 3 GiB or more.

Reconstruct the current entry from the embedded payload with:

```bash
"${artifact}" --sfx-purge-cache
"${artifact}" -win
```

Purge takes an exclusive lease, so it waits for game instances using this
artifact to exit and fails with the bounded lock timeout instead of deleting
assets beneath a running process. Older content keys and `.runtime-state` are
retained and are not removed by `--sfx-purge-cache`. This intentionally
preserves GameSpy identity/queue data and DXVK state across wrapper upgrades.
To fully reset that state, first use `--sfx-info` to confirm the cache root,
stop the game, and remove only the `GeneralsXZH/.runtime-state` directory for
this product. If the launcher ever ran without `HOME`, its fallback
`.runtime-state/home` may also contain settings, saves, replays, screenshots,
and other user data; inspect or back up the directory before removing it.

## Build on Linux/AMD64

The integrated Linux script uses the existing Docker native build and bundle
workflow, then constructs the same SFX stage layout:

```bash
export GX_SFX_ASSET_DIR="${HOME}/GeneralsX/GeneralsZH"
export GX_SFX_OUTPUT="${PWD}/build/sfx/GeneralsXZH-linux-amd64-sfx"

./scripts/build/linux/build-sfx-linux-zh.sh
```

To reuse the existing `build/linux64-deploy` output:

```bash
./scripts/build/linux/build-sfx-linux-zh.sh --skip-game-build
```

To also reuse an existing `GeneralsXZH-linux-x86_64.tar.gz` dependency bundle:

```bash
./scripts/build/linux/build-sfx-linux-zh.sh --reuse-bundle
```

Reuse skips bundle regeneration, but the extracted runtime is still
revalidated. On a non-Linux host, the configured Linux builder image must
therefore remain available; native Linux/AMD64 can validate locally.

The script can orchestrate the Linux/AMD64 Docker builder from another host,
but the resulting game must be run and gameplay-tested on Linux/AMD64.
It validates the complete transitive ELF64/AMD64 dependency closure before
packing and writes the same pre-exclusion
`${GX_SFX_OUTPUT}.stage-contents.txt` candidate inventory.
The bundle intentionally relies on the host's x86_64 glibc/dynamic loader and
on its display/Vulkan GPU-driver stack.

The `5b2bdadb0ed4-dirty` validation artifact contained 465 manifest entries,
with 1,883,386,056 compressed bytes and 3,333,325,193 extracted bytes, and passed
`--sfx-verify` under Linux/AMD64. That compressed payload leaves only
16,613,944 bytes below the default cap. If a later bundle grows past the cap,
remove unnecessary staged content instead of raising the limit toward Go's
approximately 2 GiB embedded-symbol boundary.

## Manual Cross-Target Packaging

Use the integrated script for macOS. The generic packer is useful for
platform-specific staging and development. Start with a complete target-native
stage:

```text
stage/
├── GeneralsXZH                  # generalszh.exe for Windows
├── *.big
├── ZH_Generals/                 # Optional base Generals data
├── lib/                         # Target-native dylibs or shared objects
├── MoltenVK_icd.json            # macOS
├── dxvk.conf
├── fontconfig/
│   └── fonts.conf
└── Window/Menus/ExtrasMenu.wnd  # Optional loose GeneralsX override
```

From the repository root, use absolute paths because `go -C` changes the Go
command's working directory:

```bash
repo="${PWD}"
stage="/absolute/path/to/stage"
artifact="${repo}/build/sfx/GeneralsXZH-macos-arm64-sfx"

GOENV=off GOFLAGS= GOEXPERIMENT= GOTOOLCHAIN=local GOWORK=off \
go -C "${repo}/scripts/tooling/sfx" run ./cmd/generalsx-sfx-pack \
  -source "${stage}" \
  -output "${artifact}" \
  -target darwin/arm64 \
  -entry GeneralsXZH \
  -workdir . \
  -product GeneralsXZH \
  -version "$(git -C "${repo}" rev-parse --short=12 HEAD)" \
  -exclude "${repo}/scripts/tooling/sfx/profiles/macos-zh.exclude" \
  -module "${repo}/scripts/tooling/sfx" \
  -compression xz \
  -max-embed-bytes 1900000000
```

Required flags are `-source`, `-output`, `-target`, `-entry`, `-product`, and
`-version`. Only XZ compression is supported.

For manual Linux/AMD64 packaging, stage a native AMD64 `GeneralsXZH` and all
required `.so` files, use `-target linux/amd64`, and select
`profiles/linux-zh.exclude`. Safe relative SONAME symlinks are preserved. The
integrated Linux script automates that layout and its retail-sized payload has
passed full extraction and hash verification. Graphical gameplay still needs
validation on a native Linux/Vulkan host.

For the Windows/x86 game, use `-target windows/amd64`,
`-entry generalszh.exe`, `profiles/windows-zh.exclude`, and an output path
ending in `.exe`. Packaging can cross-build on any supported Go host; executing
the recommended wrapper requires 64-bit Windows. The 64-bit wrapper can start
its embedded PE32 child and provides the address space needed for a
retail-sized `go:embed` payload. `windows/386` also cross-builds for small
payloads. The stage may not contain symlinks. Neither choice makes the current
game runtime ready: it still imports `d3dx8d.dll`, and its generated Bink and
Miles DLLs are deliberate null implementations.
An Online/TLS-capable game built with `win32-vcpkg` additionally requires the
x86 Release `libcurl.dll`, `zlib1.dll`, `MSVCP140.dll`,
`MSVCP140_ATOMIC_WAIT.dll`, and `VCRUNTIME140.dll` in a self-contained stage
beside `generalszh.exe`; curl uses Schannel and does not require OpenSSL DLLs.
They export all names the current game imports, but audio/video behavior is
unimplemented and compatibility with retail DLL implementations is
unvalidated. Do not distribute or describe the Windows artifact as a working
game build. Windows also retains the payload as its process working directory,
so remaining legacy relative writes can invalidate the immutable cache; that
routing must be completed and tested with the native runtime before Windows
gameplay support is claimed.

## Deterministic and Reproducible Inputs

The packer sorts archive paths; uses fixed PAX tar metadata, UID/GID zero, and
normalized modes; hashes files while packing; builds the launcher with
`CGO_ENABLED=0`, `-trimpath`, no VCS stamping, and an empty Go build ID; and
atomically publishes the final output. It also re-reads each source file and
requires both passes and metadata to match, so keep the stage quiescent while
packing. External XZ compression uses the fixed `xz -T1 -6 -c` settings with
host XZ option variables removed, so worker count does not vary with the build
machine.

The archive timestamp comes from `SOURCE_DATE_EPOCH` and defaults to Unix epoch
zero. To derive it from the current commit:

```bash
export SOURCE_DATE_EPOCH="$(git show -s --format=%ct HEAD)"
```

Byte-for-byte comparison also requires identical:

- staged paths, contents, file types, modes, and symlink targets;
- exclusion profile and all packer metadata flags;
- Go toolchain version;
- XZ implementation and version;
- target OS and architecture.

The macOS script applies an ad-hoc signature after packing and signs the final
app bundle after installing its plist and icon. Platform Developer ID signing
and notarization are separate release inputs. Do not claim reproducibility
across different toolchains or signing identities without comparing results.

Verify an artifact and record its digest:

```bash
file "${artifact}"
"${artifact}" --sfx-info
"${artifact}" --sfx-verify
shasum -a 256 "${artifact}"      # macOS
# sha256sum "${artifact}"        # Linux
codesign --verify --verbose=2 "${artifact}"  # macOS
codesign --verify --deep --strict --verbose=2 "${app}"  # macOS app
```

For a controlled same-toolchain comparison, build twice from unchanged inputs
and compare both SHA-256 values and bytes with `cmp`.

## Embedded Payload Size Limit

Go represents the `go:embed` payload as one linker symbol. The Go linker has an
approximately 2 GiB per-symbol limit, so a nominally valid archive can still be
too large to embed.

The packer defaults to a **compressed** payload cap of `1,900,000,000` bytes.
This deliberately leaves headroom for the manifest, Go runtime, and executable
metadata. Do not raise the cap toward or beyond 2 GiB. Reduce the staged
content with the target exclusion profile or choose a sidecar/installer format
instead. The uncompressed stage may be larger; the cap applies to the XZ
stream.

## Safety Model and Limitations

The launcher rejects malformed manifests, path traversal, absolute or
backslash paths, duplicate and case-colliding paths, Windows device names and
alternate streams, unsupported filesystem nodes, escaping/cyclic symlinks,
tar/manifest disagreement, and size/count/path-limit violations. Windows
payloads reject all symlinks. Files are hashed during full extraction, and the
game is launched directly from a path resolved inside the extracted root.
Before extraction, the launcher authenticates the complete compressed size and
SHA-256 without constructing a decoder. It then accepts a single XZ stream and
enforces a manifest-derived cap on decompressed tar and trailing data.

These controls have important limits:

- The manifest has no independent cryptographic signature. Someone who can
  replace both the executable and its payload can replace the embedded hashes.
  Obtain artifacts through a trusted channel and verify a separately published
  digest and platform signature.
- The launcher does not sandbox the game or its libraries.
- The cache is private to the current user, not a privilege boundary against
  that same user. Every manifest-listed payload regular file is rehashed before
  launch, but a same-user process could still race a file change after
  validation.
- Use a local cache filesystem with working kernel advisory locks. Locking
  failures are fatal; NFS, SMB, and filesystems with uncertain lock semantics
  are unsupported.
- The wrapper owns the runtime lease. If it is forcibly killed while an
  independently surviving game child continues to run, the operating system
  releases that lease and a later purge cannot identify the orphaned child.
- Raw GameSpy state is not transactionally locked between simultaneous game
  instances. Avoid running different SFX versions concurrently when using
  online services.
- Replay arguments containing relative directories and relative local
  `file://` URLs resolve from `.runtime-state`; use absolute paths for those
  inputs. Bare replay names and normal user-data discovery are unaffected.
- The packer is not DRM, ownership validation, or a license-compliance audit.
- The current macOS script uses an ad-hoc local signature, not Developer ID
  signing or Apple notarization.

The XZ decoder and fallback compressor use `github.com/ulikunitz/xz`, which is
distributed under a BSD-style license. Its notice is embedded in every packed
launcher and is available with `--sfx-notices`.

## Troubleshooting

### The compressed payload exceeds the maximum embedded size

Remove unnecessary files through the correct exclusion profile and rebuild.
Do not bypass the 1.9 GB default by moving the cap close to Go's roughly 2 GiB
embedded-symbol limit.

### The cache root has unsafe permissions

Point `GX_SFX_CACHE` at a new dedicated directory, or deliberately make a
directory you own private with `chmod 700`. The launcher will not silently
change permissions on a pre-existing shared directory.

### The game cannot execute from the cache

The cache filesystem may be mounted `noexec`. Set `GX_SFX_CACHE` to a private
directory on a local filesystem that permits native executables.

### The launcher reports an OS or architecture mismatch

Run the artifact on its declared target. A cross-built wrapper cannot be
inspected or verified by executing it on the build host when the target
differs.

### The development launcher says it has no payload

Running `go run ./cmd/generalsx-sfx` builds the deliberately empty development
variant. Use `generalsx-sfx-pack`, which creates generated payload files only
inside its private temporary module and builds with the `gxpacked` tag.

### macOS says the executable cannot be verified

The integrated script applies only an ad-hoc signature and does not notarize
the artifact. Prefer an artifact you built locally from reviewed source. A
public release needs Developer ID signing and notarization as a separate
release step.

For implementation details and developer tests, see the
[SFX tooling README](../../scripts/tooling/sfx/README.md).
