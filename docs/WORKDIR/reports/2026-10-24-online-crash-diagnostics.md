# Online crash diagnostics validation — 2026-10-24

The October 22 gameplay crashes were **not reproduced, diagnosed, or proved fixed**.
This work recovered the available evidence, fixed a reproducible symbol-initialization
defect, improved future crash reports, and validated the rebuilt Windows package.
The final SFX host and two baseline guests completed more than 20 minutes of
shared Online simulation with matching CRCs in the observed AI/light-input workload.

All local times are PDT (UTC−07:00). Evidence paths below are relative to
`logs/crash-recovery-2026-10-24/` unless stated otherwise. Raw dumps, replays,
private QA credentials, and the personal retail-content SFX remain local.

## Recovered evidence and preservation limits

- Two reports identify crashes on October 22 at 17:45:08 and 18:40:10. Both say
  `Uncaught Exception in GameEngine::update`; both have empty error and stack
  sections. All four associated minimal/full dump files contain zero bytes.
- The recovered latest replay contains 54,096 bytes and 2,339 complete commands.
  Its 263 three-player CRC rounds agree through frame 26,320; the last command is
  frame 26,369, about 14:39 of simulation. Its unfinished header and lack of an
  end-game command are consistent with interrupted recording. Agreement through
  the last checkpoint does not exclude a later failure. The first match's replay
  was not found.
- `evidence-manifest.json` verifies preserved copies of the reports, empty dumps,
  and replay in `game-user-data/`. `binary-identities.json` and
  `binary-copy-verification.json` identify the preserved October 10 executable
  and PDB in `binary-and-symbols/`. The short source tag `82ddee37c` alone does not
  identify an August build: the October 10 executable retained dirty-build metadata.
- Windows event, WER, reliability, and standard dump-location searches did not
  supply a matching October crash or a contemporaneous GPU, hardware, power, or
  resource-exhaustion explanation. Query results are under `system/`.

An early attempt launched the game with CDB as its parent and unexpectedly used
the personal game profile. The controller observed that startup's existing
two-full-dump retention pruned the older August 6 full dump; it had not been
copied, and was not recovered. This was a preservation mistake. The October
evidence had already been copied and remains intact. The August 6 minimal dump,
`CrashMZ-20260806-180557-5ea64fdc4-pid28512.dmp` (362,076 bytes), remains in the
personal profile's `CrashDumps/` directory. Its nested C++ exception data contains
`0xDEAD0002`, mapped by that revision to `ERROR_OUT_OF_MEMORY`; that older finding
does not establish the cause of either October crash. Later completed checks used
native game launch followed by debugger attachment and isolated profiles, with
verified restoration of the previous registry setting.

The surviving August minimal dump was also copied into the evidence archive;
`august-minimal-preservation.json` records matching source/copy SHA-256
`24AA7934D919C3E08D2109BA1EA461D8F69010A158B2CAF731F32D724A423B83`.

## Source changes and final review

- Both games now initialize a process symbol session even when MiniDumper has
  already loaded DbgHelp. The shared loader keeps its own reference until exit.
  The original initializer returned success after a preload while a focused probe
  could not resolve its own module (`diagnostics/baseline-symbol-test.log`).
- The update-loop handler records engine error names and hexadecimal codes or
  `std::exception::what()` in a fixed buffer. A nested replay-cleanup failure is
  recorded without replacing the original exception. Both Windows entrypoints
  now return failure and write stderr for exceptions escaping startup or shutdown.
- Failed dump creation/writes report the operation and preserved Windows error in
  a `.dmp.txt` sidecar and stderr. Retention treats a dump and report as one capture,
  includes orphan reports, and retries reports whose deletion was blocked.
- A Windows platform helper records process memory, system memory, free virtual
  address space, and the largest free region before dump work begins. These are
  snapshots, not an automatic diagnosis. No gameplay, protocol, replay format,
  allocator behavior, or executable address-space flag was changed.

The final read-only diff review found no new blocking correctness issue in these
changes. It checked platform guards, loader-reference lifetime and locking,
Windows error preservation, nested exception handling, bounded formatting,
retention ownership, and test integration. `git diff --check` passed, with only
the existing StackDump line-ending normalization warnings. Validation covers
modern Windows MSVC x86; Linux, macOS, VC6, and base-game runtime behavior were
not exercised in this session.

The repaired initialization does not restore the original throw-site stack after
C++ unwinding. ReleaseCrash still captures its reporting-site DebugBreak. Failure
sidecars are best effort when filesystem or process resources are exhausted.

## Completed validation

| Check | Observed result | Evidence |
| --- | --- | --- |
| Four focused diagnostics CTests, Release/x86 | 4/4 passed: typed exceptions and cleanup, real virtual-memory reservation, real-file retention, DbgHelp lifetime and a parsed real exception dump | `diagnostics/ctest-diagnostics.log` |
| Existing endpoint and WWMath tests | 2/2 passed; TLS test enabled against `tls://multiplayer.generals.network:29900` | `diagnostics/online-determinism-live-tls.log` |
| Automated native build and final SFX | Completed; embedded payload and every extracted file verified | `automated-build-final.log`, `final-sfx-info.txt`, `final-sfx-verify.txt` |
| Rendered recovered replay, baseline and final engine | Both reached EOF at frame 27,269 with `m_sawCRCMismatch=false`; game/debugger exit 0 and profile restoration confirmed | `baseline-rendered-tail-02/`, `final-rendered-tail-01/` |
| Two-client baseline Online AI soak | 375 matching two-peer CRC rounds through 20:50 shared simulation; both clients exited 0 through the UI; no new crash artifacts | `baseline-online-01/validation.txt` |
| Controlled crash in final native executable | Readable minimal/full dumps, matching private symbols, controlled reason and memory counters; expected game exit 1 and restored profile | `controlled-final-01/validation.txt`, `dump-validation-manifest.json`, `result.json` |

The rendered diagnostic replay preserves every original byte and all 789 original
CRC records, appending only a 13-byte discarded marker 30 seconds later. That tail
tests continued execution without additional recorded input or comparison CRCs.
Earlier headless baseline playback diverged near frame 8,718 and is not a passing
equivalence test or a reproduction of the original crash. The baseline Online
soak used light human input and four Hard AI on Hostile Dawn; it was a different
workload from the original match.

The controlled test deliberately called `ReleaseCrash` through CDB after native
startup. It produced a 262,994-byte minimal dump and a 444,898,255-byte full dump.
Both contain the expected `0x80000003` synthetic exception, thread/context/module
streams, and engine frames resolved with the matching PDB. They remain in
`%USERPROFILE%/Documents/GeneralsX-Crash-QA-20261024/CrashDumps/`. The copied report
contains the controlled reason and memory snapshot, but its textual `Current stack`
section is still empty in this artificial call. This proves the tested dump path
works; it does not prove every text stack will be populated or explain why the
October dumps were empty. The baseline DbgHelp probe also wrote a valid dump.

## Final package and symbols

The final personal SFX is `build/sfx/GeneralsXZH-online-diagnostics-20261024.exe`
and its hash-verified delivery copy is
`C:/Users/moloch/Desktop/GeneralsXZH-online-diagnostics-20261024.exe`.
The existing older Desktop SFX was retained. See `delivered-artifact.json`.

| Artifact | Size (bytes) | SHA-256 |
| --- | ---: | --- |
| Final SFX | 1,769,178,112 | `6499FDB4306230F2AE7DCA64CC5BDDA2C4116B3AD95E600BBB316521C9D44077` |
| Final `generalszh.exe` | 7,105,536 | `DF6E180B8C67DABAF41C9680376F65B5EE465CEDFC37BF793B161707F50568CB` |
| Matching `generalszh.pdb` | 60,616,704 | `C46E59E6558B00A610C1D3A9881A8A297014B31D158BC3353768CF8A4AE5C24F` |
| Baseline `generalszh.exe` | 7,099,392 | `D4F4E8127EB342C0EEE9128E4D13C99B43E4DF9FEE93CA624F5C81CD82C18CA0` |

SFX metadata is `c8efb55508ca-dirty`, 328 manifest entries, and payload SHA-256
`573c2f2a6280e27b5ca2ef3102abd2088fc1981e34f37dd81da41cbc7e892416`.
The launcher's `windows/amd64` label describes the launcher; the game remains x86.
Final executable and symbols are preserved together in `final-binary-and-symbols/`
with `manifest.json`; build outputs are under
`build/win32-vcpkg/GeneralsMD/Release/`. Baseline symbols remain in
`binary-and-symbols/`.

Launch the separately named Desktop SFX normally to play with the rebuilt engine.
If it crashes, preserve `ReleaseCrashInfo.txt`, `ReleaseCrashInfoPrev.txt`, the
latest replay, and the contents of `CrashDumps/` from the usual Zero Hour data
folder before launching again. The next report can distinguish an engine error
code from another exception and show memory pressure; failed dump writes should
also have an operation/error sidecar when the filesystem permits it. The saved
PDB identifies this exact executable for later debugger analysis.

## Final mixed-build Online outcome

The match started at 10:08:44 PDT on October 24. The exact final SFX launched host
PID 20568 (launcher PID 20276); preserved baseline guests were PIDs 22868 and
52932. The replay headers confirm Hostile Dawn, Internet mode, three humans plus
three Hard AI, and three teams with one human and one AI each. Starting cash was
$10,000 with unlimited superweapons and random faction/start selections.
`final-online-01/launch.json` records
actual executable paths, hashes, and parentage; the final host hash matches the
controlled-crash and delivered package engine. The run uses the separate
`GeneralsX-Online-Final-QA-20261024` profile and private QA login files.

The host replay identifies the October 24 build and executable CRC `89997d43`;
both guest replays identify the October 10 build and executable CRC `5a277989`.
All three have INI CRC `feaae3f3`. A sanitized read-only TCP snapshot also confirms
an established connection to port 29900 for each game process.

Before any planned departure, `final-online-01/pre-exit-20min/` preserved all
three streams byte for byte at frame 36,114 (**20:03.8** simulated). They share
1,118 identical command records and 361 complete three-peer CRC rounds; all
1,083 CRC records agree within each stream and between every pair. The 35
non-CRC inputs include construction, unit production, selection, and movement.
No human special-power command appears in that prefix. The controller observed
sustained AI vehicle combat and explosions on the final host from about 14:38
simulated, plus Particle Cannon, Scud, and Nuclear countdowns. The first 41 samples
reached only 19:50.5 simulated, so sampling continued before claiming the 20-minute milestone.

The controller then closed guest 3, guest 2, and the host through the game UI.
The completed files in `final-online-01/captures/20261024-103238-662/` share
1,151 identical records and **372 matching three-peer CRC rounds through frame
37,213 (20:40.43)**. Later two-peer and solo tails are not counted as three-peer
agreement. All three files parse completely with finalized headers and no desync
flag. No unexpected exit, CRC mismatch, or new QA crash artifact was observed.

| Participant | Final header frame / simulation duration | Commands / CRC records | Shutdown evidence |
| --- | --- | --- | --- |
| Final SFX host | 40,542 / 22:31.4 | 1,205 / 1,168 | Normal UI closure; SFX launcher exit 0; native child status unavailable directly |
| Baseline guest 2 | 39,186 / 21:46.2 | 1,190 / 1,154 | Directly recorded exit 0 |
| Baseline guest 3 | 37,278 / 20:42.6 | 1,151 / 1,116 | Directly recorded exit 0 |

`final-online-01/result.json` records launcher exit 0, both guest exit codes 0,
no helper error, and exact restoration of the previous registry value. Its raw
host `exit_code` remains `null`: the helper discovered that child through process
lookup. Host success is inferred from the launcher's observed exit 0 because the
SFX waits with `command.Run()` and propagates `exec.ExitError` via `launch.ExitCode`
(`scripts/tooling/sfx/cmd/generalsx-sfx/main.go:223`), not from a direct host query.
The helper and host debugger controllers also reported exit 0. The host debugger
logged ten first-chance C++ EH notifications during shutdown, with no observed
engine throw breakpoint or access violation; this is not a claim that no handled
exceptions occurred.

Sampled peak private memory was 483.7 MiB for the final host and 472.2 / 543.7 MiB
for the baseline guests. These are sampled maxima, not a performance comparison
against the different two-human/four-AI baseline scenario. Detailed hashes,
per-process memory, final frame totals, and capture paths are in
`final-online-01/validation.txt`, `memory-summary.json`, and `monitor.jsonl`.

This is a successful compatibility observation for an Online AI soak with light
human input. It uses a different scenario and command sequence from the original
three-human match ending near 14:39; it neither reproduces nor proves a fix for
the October crashes.
