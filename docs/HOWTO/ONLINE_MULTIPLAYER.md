# Host and Join Online Multiplayer

GeneralsX can route **MULTIPLAYER > ONLINE** through a self-hosted Internet
service while leaving Network/LAN play unchanged. The service coordinates
rooms and staged games, then relays the game's existing opaque UDP datagrams
between authenticated match slots. Relay tokens authenticate routing, but they
do not encrypt gameplay traffic.

This path is intended for the self-extracting or standalone GeneralsXZH and
GeneralsX packages. Every player still needs legally owned retail game data and
the same compatible game/mod version. The server keeps Generals separate from
Zero Hour and requires an exact Online protocol version and gameplay INI CRC
for joins and Quick Match. It intentionally does not compare native executable
CRCs because compatible Windows PE and macOS Mach-O binaries differ.
The replacement client is compiled by modern CMake presets; the VC6 reference
preset continues to use the original legacy Online implementation.

## What you need

- A public DNS name for the server, such as `online.example.net`.
- TCP 29900 and UDP 27901 forwarded to the server host.
- A TLS certificate whose name matches that DNS name for persistent accounts.
- The standalone
  [`moloch--/generals-server`](https://github.com/moloch--/generals-server)
  Go repository.

HTTP 8080 is for private health checks and metrics. Do not expose it publicly.

## Run a development server

Clone the server repository, then run it from `generals-server/`:

```bash
git clone https://github.com/moloch--/generals-server.git
cd generals-server
go run ./cmd/generals-server \
  --control-listen 127.0.0.1:29900 \
  --relay-listen 127.0.0.1:27901 \
  --health-listen 127.0.0.1:8080 \
  --public-host 127.0.0.1
```

A bare endpoint uses plaintext guest mode for local development:

```text
-onlineServer 127.0.0.1:29900
```

The retail login window still requires its normal fields, but the custom
plaintext path uses only the nickname as a temporary display name and never
sends the entered password. Guest identities, buddy lists, and stats do not
survive a disconnect.

Do not use plaintext guest mode as a substitute for TLS account service on an
untrusted network. Chat and control metadata are visible in transit even
when persistent credentials are not used, and gameplay relay packets are not
encrypted by this service.

## Build a default endpoint into local binaries

Modern builds accept the CMake cache variable
`SAGE_ONLINE_SERVER_DEFAULT=<[tls://]host[:port]>`. A non-empty value is parsed
and embedded in the native game executable, so **MULTIPLAYER > ONLINE** uses it
without a launcher argument. The committed default is empty, which preserves
the legacy Online path for upstream and CI builds.

For a persistent machine-local default, create the ignored file
`.generalsx-local.cmake` at the repository root:

```cmake
set(SAGE_ONLINE_SERVER_DEFAULT "online.example.net:29900" CACHE STRING
    "Default Online service endpoint compiled into modern clients")
```

Every normal preset and packaging script loads this file automatically. Do not
commit it: deployment addresses belong only in local build configuration and
ignored build caches. `-onlineServer` remains available and overrides the
embedded endpoint for that process. A `tls://` default requires
`SAGE_ONLINE_TLS=ON`; a bare default remains plaintext guest mode.

The local file seeds new CMake build trees. To change an existing build tree,
reconfigure it with `-DSAGE_ONLINE_SERVER_DEFAULT=<new-endpoint>` or clear that
tree's cached value before configuring again.

## Run an Internet server

Install a certificate chain and private key, then start the service with the
public relay name:

```bash
./generals-server \
  --control-listen :29900 \
  --relay-listen :27901 \
  --health-listen 127.0.0.1:8080 \
  --public-host online.example.net \
  --tls-cert /etc/generals-server/tls/fullchain.pem \
  --tls-key /etc/generals-server/tls/privkey.pem \
  --data-file /var/lib/generals-server/profiles.db
```

Docker Compose and systemd examples are included in the server repository.
File-backed SQLite databases use WAL, so do not copy only `profiles.db` while
the service is running: use a SQLite-consistent backup, or stop the service and
copy the complete database directory or named container volume. The service
remains single-node; do not point multiple processes at the same SQLite
database.

Allow these public firewall rules:

| Port | Transport | Purpose |
|---|---|---|
| 29900 | TCP | TLS Online control connection |
| 27901 | UDP | Authenticated gameplay relay |

Players select TLS by including `tls://` in the endpoint. Certificate and
hostname verification are mandatory, and the client does not fall back to
plaintext:

```text
-onlineServer tls://online.example.net:29900
```

## Launch a standalone game

Launch a binary with an embedded endpoint normally. The commands below are
needed only for a build with no endpoint or when temporarily overriding the
compiled value.

### macOS application

```bash
open -n "/Applications/GeneralsXZH.app" --args \
  -onlineServer tls://online.example.net:29900
```

### Windows executable

Run from PowerShell or add the argument after the closing quote in a shortcut's
**Target** field:

```powershell
& "C:\Games\GeneralsXZH-standalone.exe" `
  -onlineServer tls://online.example.net:29900
```

The standalone launcher forwards the endpoint to the extracted game process.
Both `-onlineServer` and `--onlineServer` are accepted.

The Windows game must be built with the `win32-vcpkg` preset for TLS Online
support. A self-contained Windows payload must include the resulting x86
`libcurl.dll`, `zlib1.dll`, `MSVCP140.dll`, `MSVCP140_ATOMIC_WAIT.dll`, and
`VCRUNTIME140.dll` beside `generalszh.exe`. libcurl uses the Windows Schannel
trust store, so OpenSSL DLLs are not required.

Native Windows build/link, Winsock framing, Schannel TLS, and
launcher-forwarding validation do not establish end-to-end gameplay readiness;
Windows remains exploratory because of the runtime dependencies and stubs
documented in
[Build and Run a Self-Extracting GeneralsXZH Executable](BUILD_SELF_EXTRACTING_GAME.md#manual-cross-target-packaging).

## Join or host a match

1. Start the game. Pass the server argument only when the binary has no embedded
   endpoint or needs a temporary override.
2. Select **MULTIPLAYER > ONLINE**. Do not select **NETWORK**; that remains the
   original LAN path.
3. On a TLS endpoint, use **Create Account** once or log in with an existing
   account. On a plaintext development endpoint, the nickname becomes a guest
   identity.
4. Enter a room, create or join a staged game, choose compatible map/slot
   options, and accept the setup.
5. The host starts after every non-host player accepts. The server supplies a
   per-player relay token and virtual slot address, waits for every client to
   accept those credentials, and then authorizes launch. The game transport
   separately requires an authenticated UDP BindAck before map loading.

Quick Match pairs two waiting players only when their mode, product, Online
protocol version, and gameplay INI CRC all match, then starts the relay
automatically. Quick Match is currently unranked and does not update persistent
statistics.

Once a custom match has launched, one participant losing the control
connection revokes only that player's relay token. Remaining players continue
through the same relay and the native game timeout handles the departed slot.
There is no mid-match host migration; if the host leaves, a survivor performs
the final service cleanup when the match ends.

## Verify the service

From the server host:

```bash
curl http://127.0.0.1:8080/healthz
curl http://127.0.0.1:8080/metrics
```

`/healthz` should report the advertised control/relay listeners and current
player/game counts. During a match, relay packet counters should increase.

## Troubleshooting

- **Cannot connect:** confirm TCP 29900 reaches the process and the command-line
  endpoint contains no path or whitespace.
- **TLS/certificate error:** use the certificate's DNS name, include the full
  certificate chain, and correct the system clock. IP certificates work only
  when the IP address is explicitly present in the certificate.
- **Lobby works but the game stalls:** publish/forward UDP 27901 as UDP, set
  `--public-host` to a name every player can resolve, and inspect the relay drop
  counters.
- **Bad password when joining a game:** the staged-game password is independent
  of the account password.
- **CRC mismatch:** use the same game product, standalone package/mod data, and
  compatibility generation. Incompatible public games can remain visible in
  the browser, but the client and server both reject the join.
- **Duplicate nickname:** Online display names are case-insensitively unique.
- **LAN regression concern:** use **MULTIPLAYER > NETWORK**. LAN selects its
  legacy transport explicitly and is unaffected by either an embedded Online
  endpoint or `-onlineServer`.
