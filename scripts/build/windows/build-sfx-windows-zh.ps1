#requires -Version 5.1
# GeneralsX @build Codex 04/08/2026 Add guarded exploratory native Windows SFX packaging.
<#
.SYNOPSIS
Build an exploratory native Windows GeneralsXZH SFX executable.

.DESCRIPTION
Configures and builds the 32-bit win32-vcpkg Release target, copies a legally
owned raw Zero Hour asset tree into a private temporary stage, overlays the
generated game and its complete non-system PE dependency closure, and invokes
the Go SFX packer for a 64-bit Windows launcher.

The retail binkw32.dll and mss32.dll are required and are never replaced by
the generated null-stub DLLs. Windows packaging and gameplay remain
exploratory; producing this artifact does not make it release-ready.

.PARAMETER AssetDirectory
Raw legally owned Zero Hour files. Defaults to GX_SFX_ASSET_DIR or
%USERPROFILE%\GeneralsX\GeneralsZH.

.PARAMETER Output
Destination .exe. Defaults to GX_SFX_OUTPUT or
build\sfx\GeneralsXZH-windows-amd64-sfx.exe.

.PARAMETER OnlineServerBinary
Optional Windows/AMD64 generals-server binary. Defaults to
GX_SFX_SERVER_BINARY and is staged as online-server/generals-server.exe.

.PARAMETER RuntimeSearchDirectory
Additional directories used to resolve non-system imported DLLs. The
GX_WINDOWS_RUNTIME_DIRS environment variable accepts the same directories,
separated by semicolons.

.PARAMETER SkipGameBuild
Reuse the existing win32-vcpkg Release game output.

.PARAMETER KeepStage
Retain the private Windows runtime stage after packaging for diagnosis.

.NOTES
Run from an x64-hosted Visual Studio x86 developer environment. Required tools
are detected but never installed by this script.
#>

[CmdletBinding()]
param(
    [string]$AssetDirectory,
    [string]$Output,
    [string]$OnlineServerBinary,
    [string[]]$RuntimeSearchDirectory = @(),
    [switch]$SkipGameBuild,
    [switch]$KeepStage
)

Set-StrictMode -Version Latest
$ErrorActionPreference = "Stop"

function Get-RequiredCommandPath {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][string]$Hint
    )

    $command = Get-Command $Name -ErrorAction SilentlyContinue
    if ($null -eq $command) {
        throw "Required tool '$Name' was not found. $Hint"
    }
    return $command.Source
}

function Add-ProcessPathDirectory {
    param([Parameter(Mandatory = $true)][string]$Directory)

    if (-not (Test-Path -LiteralPath $Directory -PathType Container)) {
        return
    }
    $pathEntries = @($env:PATH -split [Regex]::Escape([string][IO.Path]::PathSeparator))
    if ($pathEntries -notcontains $Directory) {
        $env:PATH = $Directory + [IO.Path]::PathSeparator + $env:PATH
    }
}

function Invoke-CheckedNativeCommand {
    param(
        [Parameter(Mandatory = $true)][string]$FilePath,
        [Parameter(Mandatory = $true)][string[]]$Arguments
    )

    & $FilePath @Arguments
    if ($LASTEXITCODE -ne 0) {
        throw "Command failed with exit code $LASTEXITCODE`: $FilePath"
    }
}

function Import-VisualStudioX86Environment {
    $vswherePath = $env:GX_VSWHERE
    if ([string]::IsNullOrWhiteSpace($vswherePath)) {
        $vswhereCommand = Get-Command 'vswhere.exe' -ErrorAction SilentlyContinue
        if ($null -ne $vswhereCommand) {
            $vswherePath = $vswhereCommand.Source
        }
        else {
            $vswherePath = Join-Path ${env:ProgramFiles(x86)} `
                'Microsoft Visual Studio\Installer\vswhere.exe'
        }
    }
    $vswherePath = Resolve-RegularFile -Path $vswherePath -Label 'Visual Studio locator'

    $installationOutput = & $vswherePath -latest -products '*' `
        -requires Microsoft.VisualStudio.Component.VC.Tools.x86.x64 `
        -property installationPath
    if ($LASTEXITCODE -ne 0 -or $null -eq $installationOutput) {
        throw "Visual Studio 2022 C++ tools were not found by vswhere."
    }
    $installationPath = ([string](@($installationOutput)[0])).Trim()
    if ([string]::IsNullOrWhiteSpace($installationPath)) {
        throw "Visual Studio 2022 C++ tools were not found by vswhere."
    }
    $developerCommand = Resolve-RegularFile `
        -Path (Join-Path $installationPath 'Common7\Tools\VsDevCmd.bat') `
        -Label 'Visual Studio developer command script'

    # GeneralsX @build Codex 04/08/2026 Import an x86 target environment into
    # this PowerShell process without requiring the caller to use a VS prompt.
    $commandLine = "call `"$developerCommand`" -no_logo -arch=x86 -host_arch=x64 >nul && set"
    $environmentOutput = & $env:COMSPEC /d /s /c $commandLine
    if ($LASTEXITCODE -ne 0) {
        throw "Visual Studio x86 developer environment initialization failed."
    }
    foreach ($line in $environmentOutput) {
        $text = [string]$line
        if ($text -match '^([^=]+)=(.*)$') {
            [Environment]::SetEnvironmentVariable($Matches[1], $Matches[2], 'Process')
        }
    }
}

function Resolve-RegularFile {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Label
    )

    if (-not (Test-Path -LiteralPath $Path -PathType Leaf)) {
        throw "$Label was not found: $Path"
    }
    $item = Get-Item -LiteralPath $Path -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "$Label may not be a symlink or reparse point: $Path"
    }
    return $item.FullName
}

function Resolve-ExistingDirectory {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][string]$Label
    )

    if (-not (Test-Path -LiteralPath $Path -PathType Container)) {
        throw "$Label was not found: $Path"
    }
    $item = Get-Item -LiteralPath $Path -Force
    if (($item.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "$Label may not be a symlink or reparse point: $Path"
    }
    return $item.FullName
}

function Test-DirectoriesOverlap {
    param(
        [Parameter(Mandatory = $true)][string]$First,
        [Parameter(Mandatory = $true)][string]$Second
    )

    $trimChars = [char[]]"\/"
    $firstPath = [IO.Path]::GetFullPath($First).TrimEnd($trimChars)
    $secondPath = [IO.Path]::GetFullPath($Second).TrimEnd($trimChars)
    if ([string]::Equals($firstPath, $secondPath, [StringComparison]::OrdinalIgnoreCase)) {
        return $true
    }
    $firstPrefix = $firstPath + [IO.Path]::DirectorySeparatorChar
    $secondPrefix = $secondPath + [IO.Path]::DirectorySeparatorChar
    return $firstPath.StartsWith($secondPrefix, [StringComparison]::OrdinalIgnoreCase) -or
        $secondPath.StartsWith($firstPrefix, [StringComparison]::OrdinalIgnoreCase)
}

function Get-TopLevelFile {
    param(
        [Parameter(Mandatory = $true)][string]$Directory,
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][string]$Label
    )

    $matches = @(
        Get-ChildItem -LiteralPath $Directory -Force -File |
            Where-Object { $_.Name -ieq $Name }
    )
    if ($matches.Count -ne 1) {
        throw "$Label must contain exactly one top-level $Name file: $Directory"
    }
    if (($matches[0].Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0) {
        throw "$Label $Name may not be a symlink or reparse point."
    }
    return $matches[0].FullName
}

function Get-PeMachine {
    param([Parameter(Mandatory = $true)][string]$Path)

    $stream = [IO.File]::Open($Path, [IO.FileMode]::Open, [IO.FileAccess]::Read, [IO.FileShare]::Read)
    try {
        $reader = [IO.BinaryReader]::new($stream)
        if ($reader.ReadUInt16() -ne 0x5A4D) {
            throw "File is not a DOS/PE image: $Path"
        }
        $stream.Position = 0x3C
        $peOffset = $reader.ReadInt32()
        if ($peOffset -lt 0 -or $peOffset -gt ($stream.Length - 6)) {
            throw "File has an invalid PE header offset: $Path"
        }
        $stream.Position = $peOffset
        if ($reader.ReadUInt32() -ne 0x00004550) {
            throw "File has no PE signature: $Path"
        }
        return $reader.ReadUInt16()
    }
    finally {
        $stream.Dispose()
    }
}

function Assert-PeMachine {
    param(
        [Parameter(Mandatory = $true)][string]$Path,
        [Parameter(Mandatory = $true)][UInt16]$Expected,
        [Parameter(Mandatory = $true)][string]$Description
    )

    $machine = Get-PeMachine -Path $Path
    if ($machine -ne $Expected) {
        throw ("{0} has PE machine 0x{1:X4}; expected {2} (0x{3:X4}): {4}" -f `
            $Description, $machine, $Description, $Expected, $Path)
    }
}

function Get-PeDependencies {
    param([Parameter(Mandatory = $true)][string]$Path)

    $dumpOutput = & $script:DumpbinPath /nologo /dependents $Path 2>&1
    if ($LASTEXITCODE -ne 0) {
        throw "dumpbin could not inspect PE dependencies: $Path"
    }
    $dependencies = [Collections.Generic.HashSet[string]]::new(
        [StringComparer]::OrdinalIgnoreCase
    )
    foreach ($line in $dumpOutput) {
        $text = [string]$line
        if ($text -match '^\s*([A-Za-z0-9_.+-]+\.dll)\s*$') {
            [void]$dependencies.Add($Matches[1])
        }
    }
    if ($dependencies.Count -eq 0) {
        throw "No PE dependencies were reported for $Path; refusing an unverified closure."
    }
    return @($dependencies)
}

function Test-WindowsSystemDependency {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][ValidateSet('x86', 'amd64')][string]$Architecture
    )

    if ($Name -match '^(?i)(api-ms-win-|ext-ms-win-)') {
        return $true
    }
    if ($Name -match '^(?i)(msvcp|vcruntime|concrt)') {
        return $false
    }
    $systemDirectory = if ($Architecture -eq 'x86') {
        Join-Path $env:WINDIR 'SysWOW64'
    }
    else {
        Join-Path $env:WINDIR 'System32'
    }
    return (Test-Path -LiteralPath (Join-Path $systemDirectory $Name) -PathType Leaf)
}

function Resolve-RuntimeDependency {
    param([Parameter(Mandatory = $true)][string]$Name)

    foreach ($directory in $script:RuntimeSearchDirectories) {
        $candidate = Join-Path $directory $Name
        if (Test-Path -LiteralPath $candidate -PathType Leaf) {
            return Resolve-RegularFile -Path $candidate -Label "Runtime dependency $Name"
        }
    }

    $matches = @(
        Get-ChildItem -LiteralPath $script:BuildDirectory -Recurse -Force -File -Filter $Name |
            Where-Object {
                $_.FullName -notmatch '(?i)[\\/]debug[\\/]' -and
                $_.FullName -notmatch '(?i)[\\/]_deps[\\/](bink|miles)-build[\\/]'
            }
    )
    $releaseMatches = @($matches | Where-Object { $_.FullName -match '(?i)[\\/]release[\\/]' })
    if ($releaseMatches.Count -eq 1) {
        return Resolve-RegularFile -Path $releaseMatches[0].FullName -Label "Runtime dependency $Name"
    }
    if ($matches.Count -eq 1) {
        return Resolve-RegularFile -Path $matches[0].FullName -Label "Runtime dependency $Name"
    }
    if ($matches.Count -gt 1) {
        $locations = ($matches.FullName -join [Environment]::NewLine)
        throw "Runtime dependency $Name is ambiguous. Set GX_WINDOWS_RUNTIME_DIRS or -RuntimeSearchDirectory.`n$locations"
    }
    throw "Could not resolve non-system runtime dependency $Name. Set GX_WINDOWS_RUNTIME_DIRS or -RuntimeSearchDirectory."
}

function Copy-ResolvedRuntimeDependency {
    param(
        [Parameter(Mandatory = $true)][string]$Name,
        [Parameter(Mandatory = $true)][string]$Stage
    )

    $source = Resolve-RuntimeDependency -Name $Name
    Assert-PeMachine -Path $source -Expected 0x014C -Description "Windows/x86 runtime dependency"
    $destination = Join-Path $Stage $Name
    Copy-Item -LiteralPath $source -Destination $destination -Force
    return (Resolve-RegularFile -Path $destination -Label "Staged runtime dependency $Name")
}

function Copy-WindowsDependencyClosure {
    param(
        [Parameter(Mandatory = $true)][string]$Stage,
        [Parameter(Mandatory = $true)][string]$GameExecutable,
        [Parameter(Mandatory = $true)][string]$RetailBink,
        [Parameter(Mandatory = $true)][string]$RetailMiles
    )

    $retailDependencies = @{
        'binkw32.dll' = $RetailBink
        'mss32.dll' = $RetailMiles
    }
    $resolvedNames = [Collections.Generic.HashSet[string]]::new(
        [StringComparer]::OrdinalIgnoreCase
    )
    $processedPaths = [Collections.Generic.HashSet[string]]::new(
        [StringComparer]::OrdinalIgnoreCase
    )
    $queue = [Collections.Generic.Queue[string]]::new()
    $queue.Enqueue($GameExecutable)
    $queue.Enqueue($RetailBink)
    $queue.Enqueue($RetailMiles)

    # These are the documented Release runtime requirements for win32-vcpkg.
    foreach ($requiredName in @(
        'libcurl.dll',
        'zlib1.dll',
        'MSVCP140.dll',
        'MSVCP140_ATOMIC_WAIT.dll',
        'VCRUNTIME140.dll'
    )) {
        $staged = Copy-ResolvedRuntimeDependency -Name $requiredName -Stage $Stage
        [void]$resolvedNames.Add($requiredName)
        $queue.Enqueue($staged)
    }

    while ($queue.Count -gt 0) {
        $current = $queue.Dequeue()
        if (-not $processedPaths.Add($current)) {
            continue
        }
        foreach ($dependency in (Get-PeDependencies -Path $current)) {
            if ($retailDependencies.ContainsKey($dependency)) {
                $queue.Enqueue([string]$retailDependencies[$dependency])
                continue
            }
            if (Test-WindowsSystemDependency -Name $dependency -Architecture x86) {
                continue
            }

            $stagedDependency = Join-Path $Stage $dependency
            if (-not $resolvedNames.Contains($dependency)) {
                $stagedDependency = Copy-ResolvedRuntimeDependency -Name $dependency -Stage $Stage
                [void]$resolvedNames.Add($dependency)
            }
            elseif (-not (Test-Path -LiteralPath $stagedDependency -PathType Leaf)) {
                throw "Resolved dependency disappeared from the private stage: $dependency"
            }
            $queue.Enqueue($stagedDependency)
        }
    }
}

if ([Environment]::OSVersion.Platform -ne [PlatformID]::Win32NT) {
    throw "This script builds the native Windows runtime and must run on Windows."
}
if ([IntPtr]::Size -ne 8) {
    throw "Use 64-bit PowerShell so the retail-sized SFX packer has sufficient address space."
}

$projectRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..\..'))
$script:SfxModule = Join-Path $projectRoot 'scripts\tooling\sfx'
$script:BuildDirectory = Join-Path $projectRoot 'build\win32-vcpkg'
$gameBuildDirectory = Join-Path $script:BuildDirectory 'GeneralsMD\Release'
$gameBuildExecutable = Join-Path $gameBuildDirectory 'generalszh.exe'

# Winget may install these tools after the parent process captured PATH.
Add-ProcessPathDirectory -Directory (Join-Path $env:ProgramFiles 'CMake\bin')
Add-ProcessPathDirectory -Directory (Join-Path $env:ProgramFiles 'Git\cmd')
Add-ProcessPathDirectory -Directory (Join-Path $env:ProgramFiles 'Git\bin')
Add-ProcessPathDirectory -Directory (Join-Path $env:LOCALAPPDATA 'Microsoft\WinGet\Links')

if ([string]::IsNullOrWhiteSpace($AssetDirectory)) {
    $AssetDirectory = if (-not [string]::IsNullOrWhiteSpace($env:GX_SFX_ASSET_DIR)) {
        $env:GX_SFX_ASSET_DIR
    }
    else {
        Join-Path ([Environment]::GetFolderPath('UserProfile')) 'GeneralsX\GeneralsZH'
    }
}
if ([string]::IsNullOrWhiteSpace($Output)) {
    $Output = if (-not [string]::IsNullOrWhiteSpace($env:GX_SFX_OUTPUT)) {
        $env:GX_SFX_OUTPUT
    }
    else {
        Join-Path $projectRoot 'build\sfx\GeneralsXZH-windows-amd64-sfx.exe'
    }
}
if ([string]::IsNullOrWhiteSpace($OnlineServerBinary) -and
    -not [string]::IsNullOrWhiteSpace($env:GX_SFX_SERVER_BINARY)) {
    $OnlineServerBinary = $env:GX_SFX_SERVER_BINARY
}
if (-not [string]::IsNullOrWhiteSpace($env:GX_WINDOWS_RUNTIME_DIRS)) {
    $RuntimeSearchDirectory += @(
        $env:GX_WINDOWS_RUNTIME_DIRS -split [Regex]::Escape([string][IO.Path]::PathSeparator) |
            Where-Object { -not [string]::IsNullOrWhiteSpace($_) }
    )
}

$assetRoot = Resolve-ExistingDirectory -Path $AssetDirectory -Label 'Retail asset directory'
if (-not [IO.Path]::IsPathRooted($Output)) {
    $Output = Join-Path $projectRoot $Output
}
$outputPath = [IO.Path]::GetFullPath($Output)
if ([IO.Path]::GetExtension($outputPath) -ine '.exe') {
    throw "Windows SFX output must end in .exe: $outputPath"
}
$outputDirectory = Split-Path -Parent $outputPath
if (Test-DirectoriesOverlap -First $assetRoot -Second $outputDirectory) {
    throw "SFX output directory may not overlap the retail asset tree."
}
if ($null -ne (Get-Item -LiteralPath $outputPath -Force -ErrorAction SilentlyContinue)) {
    [void](Resolve-RegularFile -Path $outputPath -Label 'SFX output')
}
New-Item -ItemType Directory -Path $outputDirectory -Force | Out-Null
$outputDirectory = Resolve-ExistingDirectory -Path $outputDirectory -Label 'SFX output directory'

$assetReparsePoint = Get-ChildItem -LiteralPath $assetRoot -Recurse -Force |
    Where-Object { ($_.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 } |
    Select-Object -First 1
if ($null -ne $assetReparsePoint) {
    throw "Windows SFX assets may not contain symlinks or reparse points: $($assetReparsePoint.FullName)"
}
$bigAsset = Get-ChildItem -LiteralPath $assetRoot -Force -File |
    Where-Object { $_.Extension -ieq '.big' } |
    Select-Object -First 1
if ($null -eq $bigAsset) {
    throw "No top-level Zero Hour .big assets were found in $assetRoot"
}
$retailBinkSource = Get-TopLevelFile -Directory $assetRoot -Name 'binkw32.dll' -Label 'Retail asset directory'
$retailMilesSource = Get-TopLevelFile -Directory $assetRoot -Name 'mss32.dll' -Label 'Retail asset directory'
Assert-PeMachine -Path $retailBinkSource -Expected 0x014C -Description 'Retail Windows/x86 Bink runtime'
Assert-PeMachine -Path $retailMilesSource -Expected 0x014C -Description 'Retail Windows/x86 Miles runtime'
$retailBinkHash = (Get-FileHash -LiteralPath $retailBinkSource -Algorithm SHA256).Hash
$retailMilesHash = (Get-FileHash -LiteralPath $retailMilesSource -Algorithm SHA256).Hash

$dumpbinCommand = Get-Command 'dumpbin.exe' -ErrorAction SilentlyContinue
$clCommand = Get-Command 'cl.exe' -ErrorAction SilentlyContinue
if (-not [string]::IsNullOrWhiteSpace($env:GX_VSWHERE) -or
    $null -eq $dumpbinCommand -or
    (-not $SkipGameBuild -and
        ($null -eq $clCommand -or $env:VSCMD_ARG_TGT_ARCH -ine 'x86'))) {
    Import-VisualStudioX86Environment
}
$script:DumpbinPath = Get-RequiredCommandPath -Name 'dumpbin.exe' `
    -Hint 'Run from an x64-hosted Visual Studio x86 developer environment.'
$goPath = Get-RequiredCommandPath -Name 'go.exe' -Hint 'Install Go 1.25 or newer.'
if ($null -eq (Get-Command 'xz.exe' -ErrorAction SilentlyContinue)) {
    Write-Warning 'xz.exe was not found; the packer will use its slower pure-Go XZ implementation.'
}
$goVersion = & $goPath version
if ($LASTEXITCODE -ne 0 -or $goVersion -notmatch 'go([0-9]+)\.([0-9]+)') {
    throw "Unable to determine the installed Go version."
}
if ([int]$Matches[1] -lt 1 -or ([int]$Matches[1] -eq 1 -and [int]$Matches[2] -lt 25)) {
    throw "Go 1.25 or newer is required; detected: $goVersion"
}

if (-not $SkipGameBuild) {
    $cmakePath = Get-RequiredCommandPath -Name 'cmake.exe' -Hint 'Install CMake and expose it on PATH.'
    [void](Get-RequiredCommandPath -Name 'ninja.exe' -Hint 'Install Ninja and expose it on PATH.')
    $clPath = Get-RequiredCommandPath -Name 'cl.exe' `
        -Hint 'Run from an x64-hosted Visual Studio x86 developer environment.'
    if ($env:VSCMD_ARG_TGT_ARCH -ine 'x86' -and
        $clPath -notmatch '(?i)[\\/]Host(x64|x86)[\\/]x86[\\/]cl\.exe$') {
        throw "The win32-vcpkg preset requires an x86 MSVC environment; detected compiler: $clPath"
    }
    if ([string]::IsNullOrWhiteSpace($env:VCPKG_ROOT) -or
        -not (Test-Path -LiteralPath (Join-Path $env:VCPKG_ROOT 'scripts\buildsystems\vcpkg.cmake') -PathType Leaf)) {
        throw "VCPKG_ROOT must identify a bootstrapped vcpkg checkout."
    }

    $configureArguments = @(
        '--preset', 'win32-vcpkg',
        '-DVCPKG_TARGET_TRIPLET=x86-windows',
        '-DRTS_BUILD_ZEROHOUR=ON'
    )
    $onlineEndpointConfigured = $null -ne [Environment]::GetEnvironmentVariable(
        'GX_ONLINE_SERVER_DEFAULT',
        'Process'
    )
    if ($onlineEndpointConfigured) {
        $configureArguments += "-DSAGE_ONLINE_SERVER_DEFAULT=$($env:GX_ONLINE_SERVER_DEFAULT)"
        Write-Host 'Applying GX_ONLINE_SERVER_DEFAULT (empty clears the cache value).'
    }
    Invoke-CheckedNativeCommand -FilePath $cmakePath -Arguments $configureArguments
    Invoke-CheckedNativeCommand -FilePath $cmakePath -Arguments @(
        '--build', '--preset', 'win32-vcpkg',
        '--config', 'Release',
        '--target', 'z_generals',
        '--parallel'
    )
}

$gameBuildExecutable = Resolve-RegularFile -Path $gameBuildExecutable -Label 'win32-vcpkg Release game executable'
Assert-PeMachine -Path $gameBuildExecutable -Expected 0x014C -Description 'Windows/x86 game executable'

$script:RuntimeSearchDirectories = @(
    $gameBuildDirectory,
    (Join-Path $script:BuildDirectory 'Core\Release'),
    (Join-Path $script:BuildDirectory 'vcpkg_installed\x86-windows\bin')
)
if (-not [string]::IsNullOrWhiteSpace($env:VCToolsRedistDir)) {
    $redistX86 = Join-Path $env:VCToolsRedistDir 'x86'
    if (Test-Path -LiteralPath $redistX86 -PathType Container) {
        $script:RuntimeSearchDirectories += @(
            Get-ChildItem -LiteralPath $redistX86 -Directory -Force |
                Where-Object { $_.Name -like 'Microsoft.VC*.CRT' } |
                ForEach-Object { $_.FullName }
        )
    }
}
foreach ($directory in $RuntimeSearchDirectory) {
    $script:RuntimeSearchDirectories += Resolve-ExistingDirectory -Path $directory `
        -Label 'Additional Windows runtime search directory'
}
$script:RuntimeSearchDirectories = @(
    $script:RuntimeSearchDirectories |
        Where-Object { Test-Path -LiteralPath $_ -PathType Container } |
        Select-Object -Unique
)

$resolvedServer = $null
if (-not [string]::IsNullOrWhiteSpace($OnlineServerBinary)) {
    $resolvedServer = Resolve-RegularFile -Path $OnlineServerBinary -Label 'Online server binary'
    Assert-PeMachine -Path $resolvedServer -Expected 0x8664 -Description 'Windows/AMD64 Online server executable'
}

$temporaryRoot = Join-Path ([IO.Path]::GetTempPath()) `
    ("GeneralsXZH-sfx-{0}-{1}" -f $PID, [Guid]::NewGuid().ToString('N'))
$stage = Join-Path $temporaryRoot 'runtime'
New-Item -ItemType Directory -Path $stage -Force | Out-Null

Write-Warning 'Windows SFX packaging and gameplay support are exploratory and not release-ready.'
Write-Host 'NOTICE: The private stage contains copyrighted retail assets you own. Do not redistribute the output.'

try {
    foreach ($item in (Get-ChildItem -LiteralPath $assetRoot -Force)) {
        Copy-Item -LiteralPath $item.FullName -Destination $stage -Recurse -Force
    }
    if (Test-Path -LiteralPath (Join-Path $stage 'online-server')) {
        throw "Retail assets contain reserved SFX path: online-server"
    }

    $retailBinkStage = Get-TopLevelFile -Directory $stage -Name 'binkw32.dll' -Label 'Private runtime stage'
    $retailMilesStage = Get-TopLevelFile -Directory $stage -Name 'mss32.dll' -Label 'Private runtime stage'
    Copy-Item -LiteralPath $gameBuildExecutable -Destination (Join-Path $stage 'generalszh.exe') -Force
    $stagedGame = Resolve-RegularFile -Path (Join-Path $stage 'generalszh.exe') -Label 'Staged game executable'

    Copy-WindowsDependencyClosure -Stage $stage -GameExecutable $stagedGame `
        -RetailBink $retailBinkStage -RetailMiles $retailMilesStage

    if ((Get-FileHash -LiteralPath $retailBinkStage -Algorithm SHA256).Hash -ne $retailBinkHash -or
        (Get-FileHash -LiteralPath $retailMilesStage -Algorithm SHA256).Hash -ne $retailMilesHash) {
        throw "Retail Bink/Miles DLLs changed while staging; generated stubs must never replace them."
    }

    $packerServerArguments = @()
    if ($null -ne $resolvedServer) {
        foreach ($dependency in (Get-PeDependencies -Path $resolvedServer)) {
            if (-not (Test-WindowsSystemDependency -Name $dependency -Architecture amd64)) {
                throw "Online server is not self-contained; unresolved Windows/AMD64 dependency: $dependency"
            }
        }
        $serverDirectory = Join-Path $stage 'online-server'
        New-Item -ItemType Directory -Path $serverDirectory -Force | Out-Null
        $stagedServer = Join-Path $serverDirectory 'generals-server.exe'
        Copy-Item -LiteralPath $resolvedServer -Destination $stagedServer
        Assert-PeMachine -Path $stagedServer -Expected 0x8664 -Description 'Staged Windows/AMD64 Online server executable'
        $packerServerArguments = @('-online-server-entry', 'online-server/generals-server.exe')
    }

    $stageReparsePoint = Get-ChildItem -LiteralPath $stage -Recurse -Force |
        Where-Object { ($_.Attributes -band [IO.FileAttributes]::ReparsePoint) -ne 0 } |
        Select-Object -First 1
    if ($null -ne $stageReparsePoint) {
        throw "Private Windows stage contains a forbidden symlink or reparse point: $($stageReparsePoint.FullName)"
    }

    $version = $env:GX_SFX_VERSION
    if ([string]::IsNullOrWhiteSpace($version)) {
        $gitPath = Get-RequiredCommandPath -Name 'git.exe' -Hint 'Install Git or set GX_SFX_VERSION.'
        $versionOutput = & $gitPath -C $projectRoot rev-parse --short=12 HEAD
        if ($LASTEXITCODE -ne 0 -or $null -eq $versionOutput) {
            throw "Could not derive the SFX version from Git."
        }
        $version = ([string]$versionOutput).Trim()
        if ([string]::IsNullOrWhiteSpace($version)) {
            throw "Could not derive the SFX version from Git."
        }
        $dirty = & $gitPath -C $projectRoot status --porcelain --untracked-files=normal
        if ($LASTEXITCODE -ne 0) {
            throw "Could not inspect the Git worktree while deriving the SFX version."
        }
        if ($null -ne $dirty -and @($dirty).Count -gt 0) {
            $version += '-dirty'
        }
    }

    $packerArguments = @(
        '-C', $script:SfxModule,
        'run', './cmd/generalsx-sfx-pack',
        '-source', $stage,
        '-output', $outputPath,
        '-target', 'windows/amd64',
        '-entry', 'generalszh.exe',
        '-workdir', '.',
        '-product', 'GeneralsXZH',
        '-version', $version
    )
    $packerArguments += $packerServerArguments
    $packerArguments += @(
        '-exclude', (Join-Path $script:SfxModule 'profiles\windows-zh.exclude'),
        '-module', $script:SfxModule,
        '-compression', 'xz',
        '-max-embed-bytes', '1900000000'
    )

    $savedGoEnvironment = @{}
    foreach ($name in @('GOENV', 'GOFLAGS', 'GOEXPERIMENT', 'GOTOOLCHAIN', 'GOWORK')) {
        $savedGoEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, 'Process')
    }
    try {
        $env:GOENV = 'off'
        $env:GOFLAGS = ''
        $env:GOEXPERIMENT = ''
        $env:GOTOOLCHAIN = 'local'
        $env:GOWORK = 'off'
        Invoke-CheckedNativeCommand -FilePath $goPath -Arguments $packerArguments
    }
    finally {
        foreach ($name in $savedGoEnvironment.Keys) {
            [Environment]::SetEnvironmentVariable($name, $savedGoEnvironment[$name], 'Process')
        }
    }

    Write-Host ''
    Write-Host 'Exploratory Windows SFX build complete:'
    Get-Item -LiteralPath $outputPath | Format-List FullName, Length, LastWriteTime
    Write-Host "Inspect: $outputPath --sfx-info"
    Write-Host "Verify:  $outputPath --sfx-verify"
}
finally {
    if ($KeepStage) {
        Write-Warning "Retained private Windows runtime stage: $stage"
    }
    elseif (Test-Path -LiteralPath $temporaryRoot -PathType Container) {
        Remove-Item -LiteralPath $temporaryRoot -Recurse -Force
    }
}
