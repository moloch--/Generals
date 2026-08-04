# GeneralsX

![GeneralsX](assets/generalsx_splash.png)

GeneralsX is a modern, cross-platform build of the original *Command & Conquer:
Generals* and *Zero Hour* engine. Zero Hour is the primary target. The active
macOS client uses SDL3, DXVK, MoltenVK, OpenAL Soft, and FFmpeg; the native
Windows client uses the original DirectX 8 renderer with a modern MSVC toolchain.

The repository contains GPL-licensed engine source only. **No game data is
included.** To run a build, provide the data files from a copy of Generals or
Zero Hour that you legally own, such as the
[Steam release](https://store.steampowered.com/app/2732960/).

## Platform status

| Platform | Preset | Architecture | Status |
|---|---|---:|---|
| macOS | `macos-vulkan` | Apple Silicon (`arm64`) | Primary native desktop build |
| Windows | `win32-vcpkg` | 32-bit x86 | Native build, retail-sized SFX, and headless startup through Direct3D verified on Windows; rendered gameplay remains exploratory |
| Linux | `linux64-deploy` | 64-bit x86 | Active; see the [Linux build guide](docs/BUILD/LINUX.md) |

The macOS preset requests a macOS 15 deployment target. The actual minimum OS of
a packaged build can be raised by Homebrew or Vulkan runtime libraries, so audit
all bundled dylibs before distributing it as a macOS 15-compatible release.
The Linux SFX launcher itself is static, but its bundled native game currently
requires a Vulkan-capable x86-64 system with glibc 2.38 or newer.

## Get the source

Clone recursively so that the port's reference and platform dependencies are
available:

```sh
git clone --recursive https://github.com/moloch--/Generals.git GeneralsX
cd GeneralsX
git submodule sync --recursive
git submodule update --init --recursive --jobs 6
```

Build products are written below `build/<preset>/`; source files are not changed
by configuration.

## Build a personal single-file game automatically

With Go 1.25 or newer installed, the bootstrap command can clone this source
repository and all submodules, install the target's missing build tools, install
a private SteamCMD, download the Windows Zero Hour depot owned by your Steam
account, build the native game, and package it as one self-extracting executable:

```sh
go run github.com/moloch--/Generals/cmd/generalsx-build@main \
  --steam-user YOUR_STEAM_ACCOUNT
```

Run it from an empty directory or any directory outside a GeneralsX checkout;
the default checkout is `~/GeneralsX/source`. SteamCMD prompts directly for the
password and Steam Guard challenge—the builder has no password flag and does
not store those secrets. On a Mac without the pinned Vulkan SDK or Rosetta 2,
explicitly acknowledge the applicable installer licenses on the first run:

```sh
go run github.com/moloch--/Generals/cmd/generalsx-build@main \
  --steam-user YOUR_STEAM_ACCOUNT \
  --accept-sdk-licenses
```

From an existing checkout, use `go run ./cmd/generalsx-build`. Add
`--with-online-server` to clone/build and embed the optional
`generals-server` sidecar. It is not started automatically; the finished SFX
runs it on loopback with `--sfx-server`. Use `--dry-run` to inspect the planned
external commands first.

Without `--with-online-server`, the builder does not clone, compile, package,
or start the backend; the normal output contains only the game client and its
runtime dependencies.

The output is a CGO-free, single-file Go SFX launcher with no non-system shared
library dependency. It contains the native game, its required runtime libraries,
and the retail data; the launcher still uses normal operating-system libraries,
and the C++ child uses target-native dynamic platform libraries. “Single file”
describes the distributed SFX, not a fully static game process. Windows
headless startup reaches Direct3D, but rendered gameplay remains exploratory.
See the [automated SFX build guide](docs/HOWTO/AUTOMATED_SFX_BUILD.md) for
platform requirements, flags, outputs, server operation, and legal/safety
boundaries.

## Compile a default Online endpoint into the client

The standalone [`generals-server`](https://github.com/moloch--/generals-server)
is a separate Go service. A directory such as `./generals-server` is its source
checkout; the game does **not** link that directory into the client. Instead,
the client compiles the server's reachable network endpoint into the executable
through the CMake cache variable `SAGE_ONLINE_SERVER_DEFAULT`.

An empty value embeds no replacement default and preserves legacy Online unless
`-onlineServer` supplies a runtime endpoint. Valid non-empty values are a DNS
name or IPv4 address, an optional port, and an optional lowercase `tls://`
prefix:

```text
127.0.0.1:29900
online.example.net
tls://online.example.net:29900
```

If the port is omitted, the client uses TCP port `29900`. Paths, credentials,
whitespace, other URL schemes, and IPv6 literals are not accepted. A `tls://`
endpoint requires `SAGE_ONLINE_TLS=ON`; both desktop presets in this README
enable it. Bare endpoints use plaintext guest sessions for local development,
while persistent account registration and login require verified TLS.

### Recommended: a machine-local build setting

Create `.generalsx-local.cmake` in the repository root:

```cmake
set(SAGE_ONLINE_SERVER_DEFAULT "127.0.0.1:29900" CACHE STRING
    "Default Online service endpoint compiled into modern clients")
```

For a deployed service, use its public DNS name and TLS endpoint instead:

```cmake
set(SAGE_ONLINE_SERVER_DEFAULT "tls://online.example.net:29900" CACHE STRING
    "Default Online service endpoint compiled into modern clients")
```

The file is intentionally ignored by Git and seeds newly configured build
directories. If a preset has already been configured, update its cache
explicitly:

```sh
cmake --preset macos-vulkan \
  -DSAGE_ONLINE_SERVER_DEFAULT="127.0.0.1:29900"
```

Because the local file is read by every newly configured preset, prefer an
explicit `-D` override when the same checkout is also used for iOS. A desktop
TLS endpoint is not valid for the current iOS dependency profile, and a phone
cannot reach the Mac's server through its own `127.0.0.1` address.

The Windows equivalent is shown in the Windows build below. CMake generates
`build/<preset>/generated/GameNetwork/Online/OnlineBuildConfig.h`, and the value
is compiled into the native executable.

The endpoint is plain text in the CMake cache, generated header, and executable.
A runtime override is also printed to standard error and may enter logs. It is
configuration, **not a secret**: never put a password, admin token, certificate
private key, or other credential in it.

### Run a local server checkout

For same-machine development, clone the server next to or inside this checkout
and start only loopback listeners:

```sh
git clone https://github.com/moloch--/generals-server.git generals-server
cd generals-server
go run ./cmd/generals-server \
  --control-listen 127.0.0.1:29900 \
  --relay-listen 127.0.0.1:27901 \
  --health-listen 127.0.0.1:8080 \
  --public-host 127.0.0.1
```

This requires Go 1.26 or newer. `127.0.0.1` works only when the game and server
run on the same computer. For remote players, configure public DNS, verified
TLS, and the firewall/NAT boundaries described in the
[server deployment guide](https://github.com/moloch--/generals-server#readme).

For a one-off connection, the runtime `-onlineServer` (or `--onlineServer`)
argument overrides the compiled endpoint for that process:

```sh
./scripts/build/macos/run-macos-zh.sh -win \
  -onlineServer 127.0.0.1:29900
```

The normal **MULTIPLAYER > NETWORK** LAN path is not changed by this setting.
See [Online multiplayer](docs/HOWTO/ONLINE_MULTIPLAYER.md) for protocol modes,
TLS behavior, deployment ports, and troubleshooting.

## Build on macOS

The primary macOS build is native Apple Silicon. It translates DirectX 8 to
Vulkan with DXVK and then to Metal with MoltenVK.

### 1. Install prerequisites

Install the Xcode command-line tools and build dependencies:

```sh
xcode-select --install
brew install cmake ninja meson python3 pkgconf \
  autoconf autoconf-archive automake libtool \
  ffmpeg libpng
brew install --cask steamcmd
```

Do not install Homebrew GLM for this build; GeneralsX supplies its pinned GLM,
and a second CMake package can create conflicting targets.

Install a full vcpkg checkout. A shallow clone cannot resolve the manifest's
historical baseline:

```sh
git clone https://github.com/microsoft/vcpkg.git "$HOME/vcpkg"
git -C "$HOME/vcpkg" checkout ffc071e0c08432c60c9b64f00334c0227667931b
"$HOME/vcpkg/bootstrap-vcpkg.sh" -disableMetrics
export VCPKG_ROOT="$HOME/vcpkg"
export VCPKG_DEFAULT_TRIPLET=arm64-osx
```

Install the macOS Vulkan SDK from
[LunarG](https://vulkan.lunarg.com/sdk/home#mac), then point the build to the
SDK's `macOS` directory. The version currently exercised by CI is `1.4.341.1`;
other SDK versions have not been verified by this repository:

```sh
export VULKAN_SDK="$HOME/VulkanSDK/1.4.341.1/macOS"
```

Add `VCPKG_ROOT`, `VCPKG_DEFAULT_TRIPLET`, and `VULKAN_SDK` to your shell profile
if you want them available in future terminals.

### 2. Fetch the game data you own

The asset helper signs in through SteamCMD, downloads app `2732960`, and copies
the required game data into `~/GeneralsX/GeneralsZH`:

```sh
./scripts/get-assets.sh <steam-username>
```

Steam Guard may prompt during the first sign-in. Existing retail data may be
copied to the same directory instead.

### 3. Configure, compile, deploy, and run

If `.generalsx-local.cmake` contains the desired Online endpoint:

```sh
./scripts/build/macos/build-macos-zh.sh
./scripts/build/macos/deploy-macos-zh.sh
./scripts/build/macos/run-macos-zh.sh -win
```

To set the endpoint directly while configuring instead:

```sh
cmake --preset macos-vulkan \
  -DSAGE_ONLINE_SERVER_DEFAULT="tls://online.example.net:29900"
cmake --build build/macos-vulkan --target z_generals --parallel
./scripts/build/macos/deploy-macos-zh.sh
./scripts/build/macos/run-macos-zh.sh -win
```

The first configure and build download and build dependencies, including the
GeneralsX DXVK fork, so they take longer than an incremental build.

Outputs:

- Zero Hour: `build/macos-vulkan/GeneralsMD/GeneralsXZH`
- Generals: `build/macos-vulkan/Generals/GeneralsX`

Build the base game with:

```sh
cmake --build build/macos-vulkan --target g_generals --parallel
```

The deploy script is intended for a local developer runtime. For a relocatable
build, follow the
[self-extracting executable and macOS app guide](docs/HOWTO/BUILD_SELF_EXTRACTING_GAME.md),
and do not redistribute a package containing retail data.

## Build on Windows

The current native Windows path is a 32-bit x86 Release build made with Visual
Studio 2022 and vcpkg. It is not the future 64-bit SDL3/DXVK Windows target.

### 1. Install prerequisites

Install Visual Studio 2022 with:

- Desktop development with C++
- MSVC x86/x64 build tools
- A current Windows SDK
- CMake and Ninja

Open an **x86 Native Tools Command Prompt for VS 2022** or its Developer
PowerShell equivalent. The preset expects the shell to provide the x86 compiler
environment. Use a source path without spaces while the DX8 dependency is being
modernized.

Install and bootstrap vcpkg from Developer PowerShell:

```powershell
git clone https://github.com/microsoft/vcpkg C:\src\vcpkg
& C:\src\vcpkg\bootstrap-vcpkg.bat -disableMetrics

$env:VCPKG_ROOT = 'C:\src\vcpkg'
$env:VCPKG_DEFAULT_TRIPLET = 'x86-windows'
```

### 2. Configure and compile

From the GeneralsX repository root, configure Zero Hour with the Online service
compiled in:

```powershell
cmake --preset win32-vcpkg `
  -DVCPKG_TARGET_TRIPLET=x86-windows `
  -DRTS_BUILD_GENERALS=OFF `
  -DRTS_BUILD_ZEROHOUR=ON `
  -DSAGE_ONLINE_SERVER_DEFAULT='tls://online.example.net:29900'

cmake --build --preset win32-vcpkg `
  --target z_generals `
  --parallel
```

For a server on the same Windows computer, replace the endpoint with
`127.0.0.1:29900`. If you created `.generalsx-local.cmake` before the first
configure, omit `-DSAGE_ONLINE_SERVER_DEFAULT`; the preset will read that file.

To build both games after using the Zero Hour-only configuration above,
re-enable the Generals target and then build both targets:

```powershell
cmake --preset win32-vcpkg `
  -DVCPKG_TARGET_TRIPLET=x86-windows `
  -DRTS_BUILD_GENERALS=ON `
  -DRTS_BUILD_ZEROHOUR=ON

cmake --build --preset win32-vcpkg `
  --target z_generals g_generals `
  --parallel
```

Outputs:

- Zero Hour: `build\win32-vcpkg\GeneralsMD\Release\generalszh.exe`
- Generals: `build\win32-vcpkg\Generals\Release\generalsv.exe`

The CMake dependency target imports the fetched DX8 compatibility archive and
links its D3DX implementation into the Release executable. No separate
`d3dx8d.dll` or manual linker-flag workaround is required for this native MSVC
path.

### 3. Prepare a runtime directory

Run the executable beside the data files from your legally owned Windows copy.
Keep this as a separate GeneralsX runtime directory instead of overwriting the
retail installation. Copy the retail `binkw32.dll` and `mss32.dll` into that
directory; they are required imports, and the generated dependency stubs are
not gameplay replacements.

TLS-enabled builds also need the x86 `libcurl.dll` and `zlib1.dll` from:

```text
build\win32-vcpkg\vcpkg_installed\x86-windows\bin
```

Install the Microsoft Visual C++ 2015-2022 x86 Redistributable, or stage the x86
`MSVCP140.dll`, `MSVCP140_ATOMIC_WAIT.dll`, and `VCRUNTIME140.dll` beside the
executable. Curl uses Windows Schannel, so OpenSSL DLLs are not required.

The native build, focused Winsock/Schannel paths, and retail-sized SFX packaging
have been exercised on Windows. Startup, end-to-end gameplay validation, and CI
are still in progress.

## Validate Online endpoint behavior

The focused regression target uses the documentation-only TEST-NET endpoint
`192.0.2.10:30000`; it validates endpoint parsing, initialization, runtime
override precedence, and restoration without connecting to a deployment. On
macOS:

```sh
cmake --preset macos-vulkan -DRTS_BUILD_ONLINE_TESTS=ON
cmake --build build/macos-vulkan \
  --target generalsx_online_endpoint_tests --parallel
ctest --test-dir build/macos-vulkan \
  -R '^generalsx_online_endpoint_tests$' --output-on-failure
```

On Windows, use `win32-vcpkg` for the same configure and build steps and add
`-C Release` to `ctest`. To verify the endpoint actually configured for the game,
inspect the generated `OnlineBuildConfig.h` before packaging. A runtime
`-onlineServer` argument must win over the compiled value; launching without it
must use the compiled value.

## More documentation

- [Automated source, asset, and SFX builder](docs/HOWTO/AUTOMATED_SFX_BUILD.md)
- [Online multiplayer and server deployment](docs/HOWTO/ONLINE_MULTIPLAYER.md)
- [Self-extracting executable and macOS app packaging](docs/HOWTO/BUILD_SELF_EXTRACTING_GAME.md)
- [Linux build guide](docs/BUILD/LINUX.md)
- [Porting playbook](docs/port/PORTING_PLAYBOOK.md)
- [Porting patterns](docs/port/PORTING_PATTERNS.md)
- [License](LICENSE.md)

## Lineage & credits

GeneralsX is the latest link in a long community lineage. Each earlier project
provided foundational work that this repository continues to use:

- **Westwood Studios / EA Pacific** created *Command & Conquer: Generals* and
  *Zero Hour*; **Electronic Arts** released the engine source under GPL v3.
- **[TheSuperHackers/GeneralsGameCode](https://github.com/TheSuperHackers/GeneralsGameCode)**
  maintains the community mainline and its modern toolchain and portability
  work. **[feliwir](https://github.com/feliwir)** of
  **[OpenSAGE](https://github.com/OpenSAGE/OpenSAGE)** authored foundational
  FFmpeg video and OpenAL audio work used by the porting lineage.
- **[Fighter19/CnC_Generals_Zero_Hour](https://github.com/Fighter19/CnC_Generals_Zero_Hour)**
  established the original Unix and 64-bit port, including SDL platform work,
  modern filesystem and threading support, FreeType/Fontconfig rendering, and
  the DXVK direction inherited here.
- **[fbraz3/GeneralsX](https://github.com/fbraz3/GeneralsX)** integrated and
  extended that foundation into the macOS and Linux port from which this
  repository descends.
- **[ammaarreshi/Generals-Mac-iOS-iPad](https://github.com/ammaarreshi/Generals-Mac-iOS-iPad)**
  contributed the Apple-platform fork, including the iPhone/iPad cross-build,
  DXVK-on-iOS work, touch input, app lifecycle handling, packaging, and engine
  fixes inherited by this tree.
- **[jmarshall2323/CnC_Generals_Zero_Hour](https://github.com/jmarshall2323/CnC_Generals_Zero_Hour)**
  provided an important Windows modernization and OpenAL reference.
- **GeneralsX contributors** continue the shared desktop clients, deterministic
  compatibility work, Apple packaging, native Windows bring-up, and the
  self-hostable replacement Online client and
  **[generals-server](https://github.com/moloch--/generals-server)** service.
- **DXVK, MoltenVK, SDL3, OpenAL Soft, FFmpeg, libcurl, vcpkg, FreeType,
  Fontconfig, and Liberation Fonts** are load-bearing open-source components of
  the modern platform stack.

Engine code is licensed under **GPL v3** through EA's source release and the
community lineage above; see [LICENSE.md](LICENSE.md). Original game data is not
included and is not licensed by this repository.
