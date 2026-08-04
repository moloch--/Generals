# GeneralsX Self-Extracting Launcher

This Go module builds a native GeneralsX game, its runtime libraries, and a
locally staged retail-data tree into one self-extracting executable.

> **Current status (2026-08-02):** the packer, launcher, deterministic archive,
> extraction cache, safety checks, and platform staging paths are implemented.
> The previously measured retail-sized macOS/ARM64 artifact passed
> code-signature and full extraction verification, then reached the Zero Hour
> main menu on both cold and warm-cache launches. The current macOS path
> packages the launcher as a signed Finder-recognized `.app` with a complete
> Retina icon and native first-launch progress window; the progress-enabled app
> bundle has also passed signed fixture-based cold/warm launch validation. A
> standalone Windows launcher also presents cache-miss progress through native
> in-process controls without requiring a helper executable. The Linux/AMD64
> artifact passed full extraction and file-hash verification under an AMD64
> Linux container; its graphical runtime still needs a native Linux/Vulkan
> host. Windows wrapper cross-builds and the native MSVC x86 Online build/tests
> pass; a full Windows/macOS retail match remains the outstanding gameplay gate.

The SFX is native, not an emulator and not a universal binary. A
`darwin/arm64` wrapper contains a macOS/ARM64 game, a `linux/amd64` wrapper
contains a Linux/AMD64 game, and a `windows/amd64` wrapper can launch the
Windows/x86 game through normal 64-bit Windows compatibility. The wrapper
itself is host-native; it does not require the embedded child executable to
have the same bitness. A `windows/386` wrapper is also supported for small
payloads, but a retail-sized embed should use AMD64 address space. The launcher
rejects a payload whose declared wrapper OS or architecture does not match its
host.

The current macOS child declares macOS 15.0, but the effective minimum is the
highest deployment target among every staged Homebrew/Vulkan dylib and can be
as new as the build host. The current Linux child requires x86_64 glibc 2.38 or
newer (an Ubuntu 24.04-class userspace) plus a native display and Vulkan-capable
GPU/driver stack; bundling application libraries does not include the host
loader, libc, kernel, or GPU driver.

## Asset ownership and redistribution

GeneralsX source code does not grant a right to redistribute Electronic Arts
retail game data. The SFX packer does not download assets or prove ownership.

- Build only from a legitimate game installation that you own.
- Keep retail-backed SFX artifacts for personal use unless you have explicit
  permission to redistribute every included file.
- Do not commit generated payloads, retail `.big` files, or retail-backed SFX
  executables to this repository.
- Review the licenses of every native runtime library placed in the stage.

The exclusion profiles remove known installer and obsolete runtime files, but
an exclusion profile is not a license audit.

See [Getting the Game Files](../../../docs/HOWTO/GETTING_THE_GAME_FILES.md) for
supported ways to obtain a local retail installation.

## Architecture

```text
owned retail tree + target-native GeneralsX runtime
                       |
                       v
                target staging root
                       |
        sorted deterministic PAX tar + manifest
                       |
                       v
                bounded XZ stream
                       |
                       v
       private copy of this Go module + go:embed
                       |
                       v
             one target-native SFX executable
                       |
             first launch / cache miss
                       |
                       v
       verified private staging directory
                       |
              atomic cache publication
                       |
                       v
        direct native process launch (no shell)
```

The module is divided into a few narrow components:

- `cmd/generalsx-sfx-pack` creates the deterministic archive and manifest,
  copies this module into a private temporary directory, embeds the generated
  files there, and builds the final launcher.
- `cmd/generalsx-sfx` validates, extracts, caches, and launches the embedded
  target-native game.
- `internal/bundle` owns manifest validation, deterministic tar creation, and
  strict extraction.
- `internal/cache` owns content-addressed publication, concurrent first-launch
  coordination, crash-safe kernel locks, and runtime/purge leases.
- `internal/launch` prepares target-specific library paths, asset paths,
  writable runtime state, environment, arguments, and standard streams.
- `internal/progress` sends throttled, best-effort extraction updates to the
  separately packaged macOS AppKit helper or the in-process Windows presenter
  without adding CGO to the launcher.
- `internal/payload` selects an empty development filesystem by default and
  the generated `go:embed` filesystem only under the `gxpacked` build tag.

Generated `internal/payload/generated` files exist only in the private packer
workspace. The packer does not write a multi-gigabyte payload into the source
checkout.

## Staging contract

`-source` is both the archive root and the retail/runtime root seen by the
launched game. A Zero Hour macOS or Linux stage normally resembles:

```text
stage/
├── GeneralsXZH
├── *.big
├── ZH_Generals/                 # Base Generals data, when supplied
├── lib/                         # Target-native runtime libraries
├── MoltenVK_icd.json            # macOS
├── dxvk.conf
├── fontconfig/
│   └── fonts.conf
└── Window/Menus/ExtrasMenu.wnd  # Loose GeneralsX override, when available
```

At launch, the stage root is the default for `CNC_GENERALS_ZH_PATH`,
`GENERALSX_ASSET_PATH`, and `CNC_ZH_INSTALLPATH`. If `ZH_Generals/` exists, it
is the default for `CNC_GENERALS_PATH`,
`GENERALSX_GENERALS_ASSET_PATH`, and `CNC_GENERALS_INSTALLPATH`. Explicit
non-empty caller environment values take precedence.

Library search order is `stage/lib`, then the manifest working directory, then
the inherited host value:

- macOS: `DYLD_LIBRARY_PATH`, optional SagePatch through
  `DYLD_INSERT_LIBRARIES`, MoltenVK, DXVK, and Fontconfig variables.
- Linux: `LD_LIBRARY_PATH`, optional SagePatch through `LD_PRELOAD`, DXVK, and
  OpenAL defaults.
- Windows: `PATH`; the stage must contain ordinary files because Windows
  payloads reject symlinks.

SagePatch and configuration files are resolved inside the stage root, with a
manifest-working-directory fallback for compatibility with older layouts. The
native entrypoint and manifest working directory are resolved inside the stage
root before launch. On every target, the child process itself runs with
`PWD` and its current directory set to a private, product-stable
`.runtime-state` directory outside the immutable payload. This contains legacy
relative writes such as GameSpy identity/queue files and DXVK state while
asset, library, and configuration paths continue to reference the verified
stage. SFX mode also makes the Unix platform filesystem resolve relative reads
only against that asset root (then normal BIG archives) and relative writes
only against `.runtime-state`, so writable files cannot shadow verified loose
or archived assets. Windows keeps executable and library discovery rooted in
the verified stage while containing process-relative writes in runtime state.

The packaged executable is a game runtime, not a mod editor. Unix developer
flows that write a relative temporary file and then expect to read it through
the game filesystem are intentionally outside the SFX contract; use the normal
unpacked development build for those workflows.

### Platform expectations

| Target | Required stage | Current status |
|---|---|---|
| `darwin/arm64` | Apple Silicon macOS, ARM64 `GeneralsXZH`, ARM64 dylibs under `lib/`, MoltenVK/DXVK/Fontconfig configuration, and owned retail assets; effective minimum follows the newest staged dylib | Retail-sized artifact verified; cold and warm-cache launches reached the main menu locally |
| `linux/amd64` | x86_64 glibc 2.38+, native display/Vulkan GPU-driver stack, AMD64 `GeneralsXZH`, bundled non-glibc `.so` dependencies, DXVK configuration, safe relative library symlinks, and owned retail assets | Retail-sized artifact and complete ELF closure verified under Linux/AMD64; graphical launch still requires a native Linux/Vulkan host |
| `windows/amd64` | PE32 `generalszh.exe`, complete runtime DLL set as ordinary files, owned retail Bink/Miles DLLs, and retail assets | Native MSVC retail-sized SFX build, full payload verification, and headless startup through Direct3D pass on Windows; rendered gameplay remains exploratory |
| `windows/386` | Same Windows/x86 stage, with a 32-bit Go wrapper | Small fixture cross-builds pass; a retail-sized `go:embed` payload is not recommended in a 32-bit address space |

The integrated native MSVC packager links the fetched static D3DX archive,
requires and preserves the user's retail `binkw32.dll` and `mss32.dll`, and
validates the complete non-system PE dependency closure before packing. It
never stages the generated Bink/Miles null implementations. These checks make
the artifact self-contained at the dependency level; they do not establish a
supported Windows gameplay release. The older MinGW target retains the
separate limitations documented below.

## Packer

Run the packer from the repository root with absolute source, output, profile,
and module paths:

```bash
repo="$(pwd)"
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
`-version`. `-workdir` defaults to the payload root. The module root is inferred
when possible, but release/reproducibility commands should pass `-module`
explicitly.

Only XZ output is currently supported by the packer. If an `xz` executable is
available, the packer streams tar data through `xz -T1 -6 -c`; otherwise it
uses the pure-Go XZ implementation. The macOS retail staging script requires
the external `xz` command because a multi-gigabyte source tree is expensive to
compress with the fallback. The fixed single worker avoids host-CPU-dependent
XZ block layouts.

### Deterministic inputs

For equivalent output, keep all of these inputs identical:

- source path names, file contents, file types, permission modes, and safe
  symlink targets;
- exclusion-profile contents;
- product, version, target, entrypoint, and working-directory metadata;
- `SOURCE_DATE_EPOCH`;
- Go toolchain version and XZ implementation/version;
- target OS and architecture.

When `SOURCE_DATE_EPOCH` is unset, the packer uses Unix epoch zero. For a
source-derived release timestamp:

```bash
export SOURCE_DATE_EPOCH="$(git show -s --format=%ct HEAD)"
```

The packer:

- sorts archive paths;
- emits PAX tar headers with UID/GID zero and a fixed timestamp;
- normalizes Windows modes;
- records the SHA-256 and size of every regular file;
- records the SHA-256 and size of the compressed payload;
- streams and re-reads every source file, rejecting identity, metadata, size,
  or content changes so the stage must remain quiescent while packing;
- builds with `CGO_ENABLED=0`, `-trimpath`, VCS stamping disabled, and an empty
  Go build ID;
- removes host XZ option variables and pins the Go workspace/toolchain
  environment used by the inner launcher build;
- syncs and atomically renames the final launcher in its output filesystem.

An identical stage is not sufficient by itself if Go or external XZ versions
differ. Pin those tools when byte-for-byte reproducibility is required.

## Embedded-size limit

Go's linker has a roughly 2 GiB individual embedded-symbol constraint.
`go:embed` represents the compressed archive as one symbol, so the practical
SFX limit is much smaller than the general archive schema's safety limits.

The packer therefore caps the **compressed** payload at
`1,900,000,000` bytes by default. This leaves headroom below the linker limit
for the manifest, Go runtime, and executable-format metadata. The packer stops
compression before invoking the linker when that cap would be exceeded.

Do not raise the cap to or beyond the 2 GiB boundary. Reduce the stage with the
target exclusion profile or use a non-embedded installer/sidecar format
instead. The uncompressed source can be larger; only the final XZ stream is
compared with this cap.

## Runtime commands

Assume `artifact` names a packed launcher for the current host:

| Command | Behavior |
|---|---|
| `"${artifact}" -win` | Prepare the cache and pass `-win` directly to the game |
| `"${artifact}" -- GAME_ARGUMENT...` | Force all following values to be treated as game arguments |
| `"${artifact}" --sfx-help` | Show launcher help; `-h` and `--help` are aliases |
| `"${artifact}" --sfx-info` | Print product, version, target, sizes, digests, entrypoint, and cache root |
| `"${artifact}" --sfx-verify` | Verify the compressed payload and extract every file into a temporary directory with manifest/hash checks |
| `"${artifact}" --sfx-extract DIRECTORY` | Verify and atomically extract to a destination that does not already exist |
| `"${artifact}" --sfx-purge-cache` | Remove only this launcher's current content-addressed cache entry |
| `"${artifact}" --sfx-notices` | Print embedded third-party notices |

The launcher does not invoke a shell. Spaces, metacharacters, and other game
arguments are passed directly through `exec`.
On Unix, captured `SIGINT` and `SIGTERM` are forwarded unchanged to the native
game and produce conventional wrapper statuses 130 and 143. A child terminated
independently by another Unix signal also reports `128 + signal`.

## First-launch cache

The default cache root is:

```text
os.UserCacheDir()/GeneralsX/sfx
```

Typical locations are:

- macOS: `~/Library/Caches/GeneralsX/sfx`
- Linux: `${XDG_CACHE_HOME:-~/.cache}/GeneralsX/sfx`
- Windows: `%LocalAppData%\GeneralsX\sfx`

On macOS and Linux, override the exact cache root when necessary:

```bash
GX_SFX_CACHE="/path/on/a/local-executable-filesystem" "${artifact}" -win
```

The cache key combines the compressed-payload and manifest SHA-256 values. A
new payload or manifest therefore publishes to a new content directory instead
of updating a running version in place.

Each product also has a private, digest-independent writable state directory:

```text
cache-root/
└── GeneralsXZH/
    ├── .locks/
    │   ├── CONTENT_KEY.guard  # permanent acquisition-lock inode
    │   └── CONTENT_KEY.lease  # permanent runtime/purge-lock inode
    ├── CONTENT_KEY/           # immutable, fully revalidated extracted payload
    └── .runtime-state/        # writable state retained across upgrades
```

On a cache miss, the launcher:

1. takes exclusive per-content acquisition and runtime kernel leases;
2. extracts into a unique same-filesystem staging directory;
3. verifies archive structure, modes, sizes, symlinks, and file hashes;
4. writes the completion marker last;
5. atomically renames the complete stage into its final cache path.

The process that actually owns a cache-miss extraction opens a native progress
window when launched from `GeneralsXZH.app` on macOS or from the standalone
`.exe` on Windows. Package authentication and final cache validation use an
animated indeterminate bar; regular-file extraction uses exact bytes written
against the manifest's total. Presentation is best-effort: a missing macOS
helper, unavailable Windows desktop, or failed native control does not change
extraction, cache publication, or launch behavior. Raw macOS SFX executables
and Linux targets retain their console-only behavior, and warm launches do not
open the extraction window.

Concurrent first launches wait for the same content entry instead of exposing
a partially extracted tree. Later launches take a shared lease, validate the
complete expected tree, metadata, symlink targets, and every manifest-listed
payload regular-file SHA-256 before reuse, then retain that lease until the
native game exits.
Warm launches skip decompression but still read and hash the complete
manifest-listed payload, so startup work remains proportional to the roughly
3 GiB extracted tree.
The permanent lock files use `flock` on macOS/Linux and `LockFileEx` on
Windows. The operating system releases their locks after a wrapper crash; the
files themselves deliberately remain at stable paths to prevent lock-inode
replacement races.
Purge leaves `.locks` and those stable inodes intact. Do not unlink, replace,
or manually prune them, and do not manually delete a content directory while a
wrapper or native child may still use it. Older content keys are not
garbage-collected automatically; each retained retail runtime consumes roughly
3 GiB or more.

The cache is retained after the game exits. `--sfx-purge-cache` removes only
the current immutable content key; it intentionally does not erase the
product’s `.runtime-state` directory or older product versions. This preserves
DXVK caches and legacy GameSpy identity/queue state across SFX rebuilds. Remove
that exact state directory manually only when a full runtime-state reset is
intended. When `HOME` was absent at launch, `.runtime-state/home` is also the
fallback user-data root and can contain settings, saves, replays, screenshots,
and other user files; inspect or back it up before deleting the directory. A
cache on a `noexec` filesystem cannot launch the native game;
choose a private local executable filesystem with `GX_SFX_CACHE`.
If an overridden cache root does not exist, the launcher creates it privately.
On Unix, an existing overridden root must already be a real, owner-only
directory (mode `0700`); the launcher rejects a shared root and never changes
its permissions. `GX_SFX_CACHE` overrides are disabled on Windows because the
Go standard library cannot verify that an arbitrary directory has an
owner-only DACL. Windows uses and trusts the OS-provided `%LocalAppData%`
per-user cache location without independently verifying its DACL.

## Threat and safety model

The implementation is intended to fail closed on malformed or corrupted
payloads:

- strict JSON rejects unknown fields, duplicate keys, trailing values, invalid
  metadata, and oversized manifests;
- tar headers must exactly match the sorted manifest;
- absolute paths, traversal, backslashes, NULs, Windows device names,
  alternate streams, case-folding collisions, duplicate paths, special
  filesystem nodes, and unsupported entry types are rejected;
- safe relative Unix symlinks are created only after regular files and
  directories and may not escape, cycle, or traverse a non-directory;
- Windows bundles reject symlinks;
- per-file, total-size, entry-count, path-length, and compressed-size limits
  bound extraction work;
- compressed size and SHA-256 are checked in a complete first pass before any
  decoder is created or payload content is written;
- the XZ path accepts one stream, and a manifest-derived decompressed-byte cap
  bounds tar data and trailing output;
- every manifest-listed payload regular file is hashed while first extracted;
- private staging, exact-scope removal, a synced completion marker, permanent
  per-content kernel advisory locks, and atomic rename prevent cooperating
  launchers from observing partial extraction; complete tree/hash validation
  rejects corruption on reuse;
- the manifest entrypoint, working directory, and launcher-discovered optional
  runtime/configuration paths are resolved inside the extracted root.

These protections do **not** provide all of the following:

- The embedded manifest is not signed independently. An attacker who can
  replace both the executable and its payload can replace its self-reported
  hashes too. Authenticity still depends on a trusted download hash and
  platform code signing/notarization.
- The launcher does not sandbox the native game or its libraries.
- Explicit inherited path/configuration environment values are preserved by
  design. Setting them to external locations deliberately opts those resources
  out of the verified-payload guarantee.
- The cache is private to the current user, not a privilege boundary against
  that same user. Every manifest-listed payload regular file is rehashed before
  launch, but the launcher does not prevent a same-user process from racing a
  file change after validation.
- Runtime leases are held by the wrapper process. If the wrapper is forcibly
  killed while an independently surviving native child keeps running, the
  operating system releases the wrapper's lease and a later purge cannot know
  that orphaned child still needs the extracted files.
- The cache must be on a local filesystem whose kernel advisory locks work
  correctly. Unsupported locking fails closed; do not place `GX_SFX_CACHE` on
  NFS, SMB, or another filesystem with uncertain lock semantics.
- Raw legacy GameSpy files are not transactionally locked across simultaneous
  game instances. Avoid running multiple SFX content versions concurrently
  when using online services.
- A replay argument containing a relative directory and a relative local
  `file://` URL still follow the native process current directory. Use an
  absolute path for those uncommon inputs; bare replay names and normal
  user-data discovery are unaffected.
- The packer is not a DRM, provenance, or license-compliance tool.
- The current macOS script applies ad-hoc signatures to the raw executable and
  final `.app` for local execution; it does not provide Developer ID signing or
  notarization.

## Platform build status

### macOS/ARM64

`scripts/build/macos/build-sfx-macos-zh.sh` is the current integrated path. It:

1. builds and bundles the native ARM64 game unless `--skip-game-build` is used;
2. copies locally owned retail assets into a private stage;
3. overlays `GeneralsXZH`, `lib/`, MoltenVK, DXVK, Fontconfig, and the loose
   Extras menu;
4. runs the XZ packer with the 1.9 GB compressed cap;
5. applies an ad-hoc signature to the raw SFX;
6. packages the SFX directly as `GeneralsXZH.app`, generates a ten-size Retina
   `.icns`, validates its plist and ARM64/system-library contract, and signs the
   completed app bundle.

A local validation snapshot with embedded version `5b2bdadb0ed4-dirty` linked
and passed `--sfx-verify` at
1,780,979,836 compressed bytes and 2,957,172,396 extracted bytes across 363
manifest entries. That snapshot is an ARM64 Mach-O, is 1,787,457,152
bytes, has SHA-256
`fc418e56f7d3b3f7581938eb30846805265ab69ecd1ac00f07769d60f400a66e`,
and passes strict ad-hoc `codesign` verification with identifier
`com.generalsx.generalsxzh.sfx`. A fresh private-cache launch extracted and
published the payload, loaded both retail asset roots, and reached
`Menus/MainMenu.wnd`. A second launch reused the same content entry,
rehashed it, reached the main menu without an extraction message, and kept
DXVK cache/log writes under `.runtime-state`. Artifact-level `SIGINT` and
`SIGTERM` tests shut down the native game cleanly and returned wrapper statuses
130 and 143.

The app wrapper was validated as an ARM64 application bundle with the same
identifier, a sealed 3.58 MB `.icns` resource, only system Mach-O dependencies,
and a strict-valid ad-hoc signature. Its embedded SFX passed a fresh full
payload/file verification, reached `Menus/MainMenu.wnd`, and launched through
macOS LaunchServices. The app is not notarized.

### Linux/AMD64

`scripts/build/linux/build-sfx-linux-zh.sh` drives the existing Docker native
build and Linux dependency-bundle workflow, stages its executable and `.so`
closure under `lib/`, then invokes the generic packer with
`-target linux/amd64`. Use `--skip-game-build` to reuse
`build/linux64-deploy`, or `--reuse-bundle` to also reuse
`GeneralsXZH-linux-x86_64.tar.gz`.

The matching `5b2bdadb0ed4-dirty` Linux validation snapshot contains 465
manifest entries, with 1,883,386,056 compressed bytes and 3,333,325,193
extracted bytes. It is a static Linux/AMD64 wrapper, is 1,886,593,150 bytes,
has SHA-256
`1881ce1aa5ce67bd7b23c63d9a846b72c5247414e56d2c4072c70ad0000f6545`,
and passed `--sfx-verify` inside the AMD64 Linux builder container. The native
game and its complete transitive ELF dependency closure were rebuilt and
validated before packing. The payload is only 16,613,944 bytes below the
default embed cap, so adding assets or libraries may require reducing the
stage. A graphical gameplay launch remains unverified because the build host
is macOS and its Docker VM does not expose the required Linux display/Vulkan
stack.

`--reuse-bundle` skips regenerating the tarball but always revalidates its
extracted ELF closure. That validation requires either the configured
Linux/AMD64 Docker builder image or a native Linux/AMD64 host.

The wrapper hashes above identify only that local validation snapshot. A
commit, `GX_SFX_VERSION` override, native rebuild, toolchain change, or asset
change can legitimately produce different executable bytes.

### Windows/x86 game

The CGO-free Go wrapper cross-builds for `windows/amd64` and `windows/386`.
Use an AMD64 wrapper around the PE32 game for a retail-sized payload; Windows
stages must contain no symlinks. The older MinGW game remains blocked by the
missing `d3dx8d.dll` and unimplemented Bink/Miles audio/video behavior. Its
generated Bink/Miles DLLs resolve import names but are intentional null stubs,
not retail-compatible substitutes. The integrated native MSVC packager does
not use those stubs: it requires the owned retail DLLs and verifies their PE
closure. Historic small fixture wrappers established the PE32+/PE32 wrapper
formats but did not contain the complete gameplay dependency set. Windows runs
from the stable writable runtime-state directory while executable and DLL
discovery stays rooted in the verified stage, so relative writes do not
invalidate the immutable payload.

A game built with the `win32-vcpkg` preset for verified Online TLS imports the
x86 Release `libcurl.dll` and `zlib1.dll`. A self-contained stage also needs
`MSVCP140.dll`, `MSVCP140_ATOMIC_WAIT.dll`, and `VCRUNTIME140.dll` beside
`generalszh.exe`. This curl build uses Windows Schannel, so it does not add
OpenSSL DLLs to the closure.

The integrated `scripts/build/windows/build-sfx-windows-zh.ps1` flow uses that
native MSVC build, statically links the fetched D3DX implementation, requires
the owned retail Bink/Miles binaries, resolves the DLL closure above, and wraps
the PE32 game in a PE32+ AMD64 launcher. Client-only and optional-server
retail-sized artifacts have passed native Windows `--sfx-info` and full
`--sfx-verify`. A headless client launch loaded retail data and DLLs, created
Direct3D8, and initialized WW3D before the SSH session's expected no-display
failure; rendered gameplay remains exploratory.

## Development verification

From the repository root:

```bash
go -C scripts/tooling/sfx test ./...
go -C scripts/tooling/sfx vet ./...
go -C scripts/tooling/sfx test -race ./...
```

The packer tests include a small real `go:embed` launcher build, `--sfx-info`,
and full `--sfx-verify`; they do not substitute for a retail-sized platform
build.

After producing a target-native artifact:

```bash
artifact="build/sfx/GeneralsXZH-macos-arm64-sfx"

file "${artifact}"
"${artifact}" --sfx-info
"${artifact}" --sfx-verify
shasum -a 256 "${artifact}"      # macOS
# sha256sum "${artifact}"        # Linux
```

`--sfx-verify` needs enough temporary disk space for the full extracted tree.
Run inspection and verification on the payload's target OS/architecture; the
launcher intentionally rejects a mismatched host.

For a same-toolchain reproducibility comparison, build twice from unchanged
stages and metadata, then compare:

```bash
shasum -a 256 build/sfx/GeneralsXZH-sfx.first \
              build/sfx/GeneralsXZH-sfx.second
cmp build/sfx/GeneralsXZH-sfx.first \
    build/sfx/GeneralsXZH-sfx.second
```

On macOS, also inspect the local signature:

```bash
codesign --verify --verbose=2 "${artifact}"
codesign -dv --verbose=4 "${artifact}"
app="build/sfx/GeneralsXZH.app"
codesign --verify --deep --strict --verbose=2 "${app}"
codesign -dv --verbose=4 "${app}"
"${app}/Contents/Helpers/GeneralsX-SFX-Progress" --self-test
```

## Launcher third-party notices

The launcher embeds `github.com/ulikunitz/xz` for XZ decoding and as the
pure-Go compression fallback. Windows launchers also use
`golang.org/x/sys/windows` to load native UI dependencies exclusively from the
system directory. Both modules use BSD-style licenses. Their required notices
are embedded in every packed launcher and are available through:

```bash
"${artifact}" --sfx-notices
```

The source notice is
[`internal/notices/THIRD_PARTY_NOTICES.md`](internal/notices/THIRD_PARTY_NOTICES.md).
