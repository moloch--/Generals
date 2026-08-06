# Automated GeneralsXZH Source, Asset, and SFX Build

## Desktop app (preferred)

Download the Automated Build Tool desktop asset for your host from a tagged
[`vX.Y.Z` release](https://github.com/moloch--/Generals/releases). Use the
macOS/ARM64 DMG, the direct Linux/AMD64 desktop executable, or the direct
Windows/AMD64 desktop `.exe`. After downloading a direct macOS or Linux
executable, run `chmod +x PATH` before starting it. The three desktop builds
present the same guided flow:

1. choose a target supported by this build host;
2. select an existing source checkout or automatic clone destination;
3. select owned retail data or ask SteamCMD to download it;
4. choose dependency, output, and optional Online server settings;
5. review the plan and personal-use data boundary, then start the build.

The app validates each step before it starts and shows the shared builder's
real phase, terminal output, cancellation state, and final artifact path. It
does not contain game data. Tagged releases distribute only the Automated Build
Tool, its headless companion, checksums, and licensing files; they never attach
a user-generated game SFX or macOS game app.

After a real build completes, select **Copy to Desktop** to publish the primary
personal game artifact through a temporary sibling without overwriting an
existing Desktop item. For a macOS target, that artifact is the complete
Finder-ready `GeneralsXZH.app` bundle with its Generals icon. Linux and Windows
copy their native SFX executables. The private sibling is fully hashed and runs
the platform verifier before its atomic no-replace publication; a failed check
therefore leaves no partial or unrecorded Desktop artifact. Once that copy
succeeds, **Cleanup** becomes available. Cleanup first presents every path
authorized for deletion in a
destructive confirmation dialog. The confirmation is bound to that exact
one-time plan; reviewing a new plan, starting another build, or changing the
copied artifact invalidates stale authorization.

Cleanup claims only destinations that did not exist when that specific build
started and that still carry the same build's private ownership receipt. This
can include an automatically cloned GeneralsX checkout, a newly created builder
cache or managed Git checkout, a newly created target build directory, the raw
output and macOS app bundle, stage manifests, portable bundle intermediates,
and build logs. Descendants collapse under an owned parent so the dialog stays
readable. Existing repositories, existing cache roots and their pre-existing
contents, explicit or adjacent server sources, retail assets, installed SDKs
and package-manager state, Docker images, and the Desktop artifact are never
inferred as disposable.

Immediately before deletion, the app revalidates the complete Desktop artifact
and runs the SFX's full `--sfx-verify`. For macOS, it verifies the app bundle and
invokes its nested `Contents/MacOS/GeneralsXZH` launcher; the separately emitted
raw SFX remains available for direct terminal and headless workflows. A
Linux/AMD64 SFX produced on macOS is verified in the already-required Linux
builder container with no network and a read-only container filesystem. Every
cleanup candidate is content-fingerprinted again, moved to a same-parent
quarantine name through a root-scoped filesystem handle, and removed only if
its identity, ownership marker, contents, and protected-path boundaries remain
unchanged. Cancellation stops between owned roots, and any untouched roots
remain eligible for a newly reviewed retry.

When SteamCMD needs to download files, the desktop app opens it in a real native
terminal and waits for it to finish. Enter the Steam password and Steam Guard
challenge only in that terminal. The GUI receives only the non-secret account
name and SteamCMD's final status; it has no password, token, or challenge field.
After SteamCMD exits successfully, the build continues in the app.

Prompt-capable host setup also stays visible: Homebrew bootstrap, Rosetta
installation, and Linux package-manager commands that need administrator
approval open in a native terminal and return control to the same GUI build.
Noninteractive dependency commands continue to stream their output in the app.

Each release attaches a dependency-light `generalsx-build` headless companion
as a separate platform download. The desktop executable itself also accepts
`--headless` and the same flags when started from a terminal. Check any download
against the separately attached `SHA256SUMS`; `LICENSE.md` and
`THIRD_PARTY_NOTICES.md` are independent release assets too.

### Build the desktop tool from source

Desktop frontend development requires a valid HeroUI Pro license. Authenticate
the local HeroUI Pro CLI, or provide `HEROUI_AUTH_TOKEN` to the dependency
installation step, then build the tracked frontend:

```sh
cd tools/generalsx-build-desktop/frontend
npm ci --strict-allow-scripts --no-audit --no-fund
npm run build
cd ..
```

Use `npm run preview:desktop` for an explicitly labelled browser-only preview.
Normal `npm run dev` is reserved for Wails and fails closed when its native
bridge is absent, so preview events cannot be mistaken for a real build.

Build the native shell with the pinned Wails CLI. Add
`-tags webkit2_41` on Linux. On Windows, add
`-webview2 embed -windowsconsole`; keeping the console subsystem lets the same
executable provide exact terminal behavior for `--headless` and private prompt
handoffs while the no-argument launch detaches it for the GUI.

```sh
go run github.com/wailsapp/wails/v2/cmd/wails@v2.13.0 build \
  -clean -trimpath -nosyncgomod -skipbindings -skipembedcreate -s
```

The tag-release workflow performs these builds on native runners and requires a
repository Actions secret named `HEROUI_AUTH_TOKEN`. Fork pull requests never
receive that secret and therefore run the Go verification lane without the
licensed frontend/native packaging jobs.

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
approval. In desktop mode, prompt-capable installers open in a native terminal
instead of trying to read from a detached GUI process. Automatic macOS LunarG
SDK or Rosetta installation additionally requires the explicit
`--accept-sdk-licenses` flag; read the applicable terms before supplying it.

## Fresh-machine command

Run outside an existing checkout:

```sh
go run github.com/moloch--/Generals/cmd/generalsx-build@main \
  --headless \
  --steam-user YOUR_STEAM_ACCOUNT
```

The default target follows the host. The source goes to
`~/GeneralsX/source`, retail data to `~/GeneralsX/GeneralsZH`, and managed
downloads/checkouts to the operating system's user cache. Override the source
destination without changing an existing tree:

```sh
go run github.com/moloch--/Generals/cmd/generalsx-build@main \
  --headless \
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
go run ./cmd/generalsx-build --headless --steam-user YOUR_STEAM_ACCOUNT
```

To reuse a previously prepared asset tree and avoid Steam authentication:

```sh
go run ./cmd/generalsx-build \
  --headless \
  --skip-assets \
  --assets-dir /path/to/GeneralsZH
```

`--skip-assets` still validates the tree. Windows additionally requires the
owned retail `binkw32.dll` and `mss32.dll` because they are native game imports,
not generated substitutes.

## Platform behavior

| Target | Supported build host | Native game | Default personal artifact | Status |
|---|---|---|---|---|
| macOS | Apple Silicon macOS | ARM64, `macos-vulkan` | `build/sfx/GeneralsXZH.app` (primary) plus `GeneralsXZH-macos-arm64-sfx` | Primary |
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
  --headless \
  --steam-user YOUR_STEAM_ACCOUNT \
  --accept-sdk-licenses
```

The Finder-launchable `GeneralsXZH.app` is the Automated Build Tool's primary
macOS personal game artifact, and **Copy to Desktop** publishes that complete
bundle with its Generals icon intact. The raw SFX is still produced separately
and nested at `GeneralsXZH.app/Contents/MacOS/GeneralsXZH` for direct terminal
and headless compatibility.
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
  --headless \
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
go run ./cmd/generalsx-build --headless --help
```

Print external actions without cloning, downloading, installing, or building:

```sh
go run ./cmd/generalsx-build \
  --headless \
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
