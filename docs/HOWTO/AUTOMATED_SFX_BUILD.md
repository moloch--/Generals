# Automated GeneralsXZH Source, Asset, and SFX Build

## What the command does

`generalsx-build` turns a fresh macOS, Linux, or Windows development machine
into a target-native personal GeneralsXZH self-extracting executable. Go is the
bootstrap entrypoint, so Go 1.25 or newer is the only separately installed
development tool it needs. On Windows, the supported OS must also provide its
standard Microsoft App Installer; when WinGet has not completed first-login
registration, the builder requests that registration automatically.

The command performs this sequence:

1. installs Git if it is missing;
2. clones `https://github.com/moloch--/Generals.git` when the selected checkout
   does not exist, then checks out the requested ref without updating an
   existing checkout;
3. synchronizes and initializes all Git submodules;
4. installs or verifies the target's compiler, build system, SDK, and packaging
   tools;
5. installs SteamCMD in a private user cache and asks it for the Windows Zero
   Hour depot (Steam app `2732960`);
6. validates required retail data before it enters staging;
7. optionally clones and builds `generals-server` as a target-native, CGO-free
   sidecar;
8. builds the native game and packages the complete runtime as one SFX file.

An existing source superproject is not pulled, reset, or cleaned. Its recursive
submodules are still synchronized, initialized, and checked out at the commits
recorded by that superproject; Git refuses a checkout that would overwrite
conflicting local submodule edits. A clone destination that already exists but
is not a complete GeneralsX checkout is rejected.

## Legal and credential boundary

The repository contains engine source but no retail game data. SteamCMD can
download Zero Hour only through a Steam account that owns it. The resulting SFX
contains copyrighted retail data: keep it for personal use unless you have
permission to redistribute every embedded file.

Pass only the Steam account name with `--steam-user`. SteamCMD reads the account
password and Steam Guard challenge directly from the terminal. The builder
does not accept either secret through a flag or environment variable, and it
does not echo or store them. Do not use an account password as the account-name
argument.

Package managers and SDK/tool installers may request the host administrator's
approval. Automatic macOS LunarG SDK or Rosetta installation additionally
requires the explicit `--accept-sdk-licenses` flag; read the applicable terms
before supplying it.

## Fresh-machine command

Run outside an existing checkout:

```sh
go run github.com/moloch--/Generals/cmd/generalsx-build@main \
  --steam-user YOUR_STEAM_ACCOUNT
```

The default target follows the host. The source goes to
`~/GeneralsX/source`, retail data to `~/GeneralsX/GeneralsZH`, and managed
downloads/checkouts to the operating system's user cache. Override the source
destination without changing an existing tree:

```sh
go run github.com/moloch--/Generals/cmd/generalsx-build@main \
  --repo /path/to/new/GeneralsX \
  --source-ref main \
  --steam-user YOUR_STEAM_ACCOUNT
```

Use a release tag or commit with `--source-ref` for a reproducible source
selection. `@main` above selects the version of the bootstrap command itself;
`--source-ref` selects the game checkout it creates.

## Existing-checkout command

From the repository root:

```sh
go run ./cmd/generalsx-build --steam-user YOUR_STEAM_ACCOUNT
```

To reuse a previously prepared asset tree and avoid Steam authentication:

```sh
go run ./cmd/generalsx-build \
  --skip-assets \
  --assets-dir /path/to/GeneralsZH
```

`--skip-assets` still validates the tree. Windows additionally requires the
owned retail `binkw32.dll` and `mss32.dll` because they are native game imports,
not generated substitutes.

## Platform behavior

| Target | Supported build host | Native game | Default SFX output | Status |
|---|---|---|---|---|
| macOS | Apple Silicon macOS | ARM64, `macos-vulkan` | `build/sfx/GeneralsXZH-macos-arm64-sfx` plus `GeneralsXZH.app` | Primary |
| Linux | Linux/AMD64, or macOS with Docker | AMD64, `linux64-deploy` | `build/sfx/GeneralsXZH-linux-amd64-sfx` | Active |
| Windows | Windows/AMD64 | x86, `win32-vcpkg`, inside an AMD64 SFX | `build\sfx\GeneralsXZH-windows-amd64-sfx.exe` | Exploratory |

Select a non-default supported target with `--target macos`, `--target linux`,
or `--target windows`. macOS and Windows game builds require their native host;
a Mac can also orchestrate the Linux Docker build. The command does not pretend
that one host can cross-build every native platform stack.

### macOS

The bootstrap verifies Xcode Command Line Tools; installs Homebrew when needed;
installs the repository's current package set; creates a pinned vcpkg checkout;
and downloads, SHA-256 verifies, and installs LunarG Vulkan SDK `1.4.341.1`.
Valve's initial macOS SteamCMD bootstrap is x86-64, so the command also verifies
Rosetta 2 and can install it before SteamCMD updates itself. When either licensed
component is absent, use:

```sh
go run ./cmd/generalsx-build \
  --steam-user YOUR_STEAM_ACCOUNT \
  --accept-sdk-licenses
```

The raw SFX and Finder-launchable `.app` are both produced.
The game binary declares macOS 15, but Homebrew/Vulkan dylibs can raise the
finished artifact's effective minimum to the builder's OS; the packager does
not claim older-host compatibility without a complete dylib deployment-target
audit.

### Linux

The containerized game build uses the existing Docker workflow. The bootstrap
can install Docker through `apt`, `dnf`, or `pacman`; a newly added Docker group
membership requires logging out and back in before rerunning. On macOS it can
install and open Docker Desktop, after which its first-run setup must finish
before the build is rerun. On native AMD64 Linux, downloading assets also
installs the distribution's 32-bit runtime required by Valve's SteamCMD; this is
not required when `--skip-assets` uses a complete prepared tree. Arch Linux
must have its standard `[multilib]` repository enabled; the builder checks this
before package installation and reports the exact `/etc/pacman.conf` remedy.
Docker can orchestrate the build from an older distribution, but it does not
lower the resulting native game's runtime floor: the current portable bundle
requires a Vulkan-capable x86-64 system with glibc 2.38 or newer. The outer Go
SFX launcher remains statically linked.

### Windows

Run from a 64-bit Windows terminal. The bootstrap uses `winget` to install Git,
Visual Studio 2022 Build Tools with C++ and a Windows SDK, CMake, and
Ninja, then bootstraps pinned vcpkg. It also repairs the standard App Installer
registration when possible and refuses partial Visual Studio installations
that lack `Windows.h` or the x86 resource compiler. The packager imports the
x86 Visual Studio environment itself, builds the PE32 game, resolves and
validates its complete non-system DLL closure, and emits a PE32+ AMD64 SFX
launcher.

The native Windows compile and full retail-sized SFX verification have passed
on a Windows/AMD64 host. A headless SSH launch also loaded the retail
audio/video libraries and language data, created Direct3D8, and initialized
WW3D before reaching the expected no-display failure. Rendered gameplay remains
exploratory. Use `--keep-windows-stage` only for diagnosis; its retained stage
contains retail data and must not be committed or shared.

## Optional bundled Online backend

Add one flag to build a target-native backend and include it in the SFX:

```sh
go run ./cmd/generalsx-build \
  --steam-user YOUR_STEAM_ACCOUNT \
  --with-online-server
```

This path is entirely opt-in. Without the flag, the builder never clones or
compiles `generals-server`, and the SFX contains only the game client and its
runtime dependencies.

The source selection order is:

1. `--online-server-source /path/to/generals-server`;
2. `<game-checkout>/generals-server` when it is a valid checkout;
3. a managed clone of `https://github.com/moloch--/generals-server.git` at
   `--online-server-ref main`.

The option also compiles `127.0.0.1:29900` as the game's default Online
endpoint unless `--online-endpoint` supplies another valid DNS/IPv4 endpoint.
The server is embedded but never started automatically. Start its safe
same-machine configuration in one terminal:

```sh
./build/sfx/GeneralsXZH-macos-arm64-sfx --sfx-server
```

Use the platform's SFX filename on Linux or Windows. With no server arguments,
the launcher binds control `29900`, relay `27901`, and health `8080` to
loopback, advertises loopback, and persists SQLite data beneath the SFX's
private `.runtime-state/online-server` directory. The launcher uses the
relative data-file name `profiles.db` from that dedicated working directory so
the same contained default works on Windows, macOS, and Linux. Stop it with the
terminal's interrupt; the launcher gives the backend a bounded graceful
shutdown window.

Advanced server arguments can be forwarded after `--`. Each listener,
advertised-host, and data-file default remains in effect unless that specific
flag is supplied explicitly, so unrelated tuning does not accidentally expose
the service or move its persistent data:

```sh
./build/sfx/GeneralsXZH-linux-amd64-sfx --sfx-server -- \
  --max-online-players 16
```

Public deployment is a separate operator task. Do not expose listeners merely
because the executable contains a server; follow
[Online multiplayer](ONLINE_MULTIPLAYER.md) for TLS, firewall, NAT, persistent
data, and private management boundaries.

## Inspect before changing the host

Show the complete flag list:

```sh
go run ./cmd/generalsx-build --help
```

Print external actions without cloning, downloading, installing, or building:

```sh
go run ./cmd/generalsx-build \
  --dry-run \
  --target auto \
  --steam-user YOUR_STEAM_ACCOUNT
```

For unattended validation, install all host dependencies and prepare the asset
tree first, then combine
`--non-interactive --install-deps=false --skip-assets`. Interactive Steam
authentication and dependency installers that can prompt or open system UI are
rejected in non-interactive mode.

Automatic host setup is a convenience bootstrap, not a hermetic toolchain:
Homebrew's installer and SteamCMD's self-update stream are mutable official
HTTPS inputs, and an already valid vcpkg checkout is reused. For a controlled
build environment, provision reviewed tool versions yourself and use
`--install-deps=false`.

Useful advanced controls include:

- `--install-deps=false` to verify rather than install prerequisites;
- `--skip-game-build` to reuse the target's current native game build (it cannot
  be combined with a newly compiled Online endpoint or `--with-online-server`);
- `--output` and macOS `--app-output` to select publication paths;
- `--cache-dir` and `--steamcmd-dir` to select managed cache locations;
- `--source-repo` and `--online-server-repo` for deliberate repository mirrors;
- `--online-endpoint [tls://]HOST[:PORT]` for a non-loopback compiled endpoint.

The builder rejects an output workspace that overlaps the retail asset tree,
unsafe archive paths, incomplete managed checkouts, wrong-architecture server
binaries, and missing target runtime files.

## Verify the result

The outer executable is a CGO-free, single-file Go launcher with no non-system
shared-library dependency. It can still use normal operating-system libraries.
It embeds the target-native C++ game and its required dynamic runtime libraries;
it does not turn those native components into one statically linked process.

Inspect and fully verify an artifact before launch:

```sh
./build/sfx/GeneralsXZH-macos-arm64-sfx --sfx-info
./build/sfx/GeneralsXZH-macos-arm64-sfx --sfx-verify
```

Replace the filename for Linux or Windows. Full verification decompresses and
hashes every embedded file, so it needs temporary space for the complete game.
See [Build and Run a Self-Extracting GeneralsXZH Executable](BUILD_SELF_EXTRACTING_GAME.md)
for cache behavior, launch options, signing, dependency boundaries, and manual
packer workflows.
