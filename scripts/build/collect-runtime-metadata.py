#!/usr/bin/env python3
"""Collect build identity and dependency notices for an asset-free runtime.

Usage: python scripts/build/collect-runtime-metadata.py --repo .
       --build-dir build/PRESET --runtime-dir STAGE --target TARGET
"""
# GeneralsX @build Codex 10/10/2026 Bind runtime dependency provenance and notices to the package.
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess


def command(*args):
    return subprocess.check_output(args, text=True, stderr=subprocess.STDOUT).strip()


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ('repo', 'build-dir', 'runtime-dir', 'target'):
        parser.add_argument('--' + name, required=True)
    args = parser.parse_args()
    repo = Path(args.repo).resolve(strict=True)
    build = Path(args.build_dir).resolve(strict=True)
    runtime = Path(args.runtime_dir).resolve(strict=True)
    notice_dir = runtime / 'licenses'
    notice_dir.mkdir()
    notices = {}

    def notice(source, identity):
        if not source.is_file():
            return
        data = source.read_bytes()
        digest = hashlib.sha256(data).hexdigest()
        filename = re.sub(r'[^A-Za-z0-9._-]', '_', identity)[:100] + '-' + digest[:12] + '.txt'
        (notice_dir / filename).write_bytes(data)
        notices[filename] = identity

    notice(repo / 'LICENSE.md', 'GeneralsX-LICENSE')
    deps = {}
    for source in sorted((build / '_deps').glob('*-src*')):
        if not source.is_dir():
            continue
        identity = source.name
        if (source / '.git').exists():
            deps[identity] = {'commit': command('git', '-C', str(source), 'rev-parse', 'HEAD')}
        else:
            deps[identity] = {'source': 'CMake pinned download; see source revision'}
        for pattern in ('LICENSE*', 'COPYING*', 'NOTICE*', 'Copyright*', 'copyright*'):
            for path in source.glob(pattern):
                notice(path, identity + '-' + path.name)
    for path in (build / 'vcpkg_installed').glob('*/share/*/copyright'):
        notice(path, 'vcpkg-' + path.parent.name)
    status = build / 'vcpkg_installed/vcpkg/status'
    if status.is_file():
        deps['vcpkg_packages'] = status.read_text(encoding='utf-8')
    vcpkg = os.environ.get('VCPKG_ROOT')
    if vcpkg:
        if (Path(vcpkg) / '.git').exists():
            deps['vcpkg_commit'] = command('git', '-C', vcpkg, 'rev-parse', 'HEAD')
        else:
            deps['vcpkg_toolchain_sha256'] = hashlib.sha256(
                (Path(vcpkg) / 'scripts/buildsystems/vcpkg.cmake').read_bytes()).hexdigest()
    cache = {}
    for line in (build / 'CMakeCache.txt').read_text(encoding='utf-8').splitlines():
        match = re.match(r'((?:SAGE_|RTS_|CMAKE_BUILD_TYPE|CMAKE_OSX_DEPLOYMENT_TARGET|CMAKE_OSX_ARCHITECTURES)[^:]*):[^=]*=(.*)', line)
        if match:
            cache[match[1]] = match[2]
    metadata = {'schema': 1, 'target': args.target,
                'source_commit': command('git', '-C', str(repo), 'rev-parse', 'HEAD'),
                'build_configuration': cache, 'dependencies': deps}
    compilers = {}
    for path in (build / 'CMakeFiles').glob('*/CMake*Compiler.cmake'):
        values = dict(re.findall(r'set\((CMAKE_(?:C|CXX)_COMPILER_(?:ID|VERSION|ARCHITECTURE_ID)) "([^"]*)"\)',
                                 path.read_text(encoding='utf-8')))
        compilers.update(values)
    if not compilers:
        raise RuntimeError('CMake compiler identity is missing from the build tree')
    metadata['compiler'] = compilers
    if args.target == 'linux-amd64':
        # Preserve the installed package inventory and distribution copyright
        # files, including transitively bundled FFmpeg/SDL dependencies.
        deps['distribution_packages'] = command('dpkg-query', '-W', '-f=${Package}\t${Version}\n')
        for path in Path('/usr/share/doc').glob('*/copyright'):
            notice(path, 'debian-' + path.parent.name)
        versions = []
        for path in runtime.rglob('*'):
            if not path.is_file() or path.is_symlink():
                continue
            with path.open('rb') as stream:
                is_elf = stream.read(4) == b'\x7fELF'
            if is_elf:
                output = command('readelf', '--version-info', '--wide', str(path))
                versions += re.findall(r'Name: GLIBC_(\d+(?:\.\d+)+)', output)
        if not versions:
            raise RuntimeError('No required GLIBC version was measured')
        metadata['minimum_glibc'] = max(versions, key=lambda v: tuple(map(int, v.split('.'))))
    elif args.target == 'macos-arm64':
        deps['homebrew_packages'] = command('brew', 'list', '--versions')
        cellar = Path(command('brew', '--cellar'))
        for formula in cellar.iterdir():
            for version in formula.iterdir():
                for pattern in ('LICENSE*', 'COPYING*', 'NOTICE*', 'share/doc/*/LICENSE*', 'share/doc/*/COPYING*'):
                    for path in version.glob(pattern):
                        notice(path, 'homebrew-' + formula.name + '-' + path.name)
        sdk = Path(os.environ['VULKAN_SDK'])
        deps['vulkan_sdk'] = sdk.parent.name
        for pattern in ('**/LICENSE*', '**/COPYING*', '**/NOTICE*'):
            for path in sdk.glob(pattern):
                notice(path, 'vulkan-' + path.name)
        metadata['minimum_os'] = cache['CMAKE_OSX_DEPLOYMENT_TARGET']
        required_versions = []
        for path in runtime.rglob('*'):
            if path.is_file() and not path.is_symlink() and (path.name == 'GeneralsXZH' or path.suffix == '.dylib'):
                output = command('otool', '-l', str(path))
                required_versions += re.findall(r'\bminos (\d+(?:\.\d+)+)', output)
                required_versions += re.findall(r'cmd LC_VERSION_MIN_MACOSX\s+cmdsize \d+\s+version (\d+(?:\.\d+)+)', output)
        if not required_versions:
            raise RuntimeError('No macOS deployment version was measured')
        def version_tuple(value):
            components = list(map(int, value.split('.')))
            return tuple((components + [0, 0, 0])[:3])
        measured = max(required_versions, key=version_tuple)
        if version_tuple(measured) > version_tuple(metadata['minimum_os']):
            raise RuntimeError(f'Dependency requires macOS {measured}, above deployment target {metadata["minimum_os"]}')
        metadata['measured_minimum_os'] = measured
    elif args.target == 'windows-x86':
        metadata['minimum_os'] = '10.0'
        deps['msvc_tools_version'] = os.environ.get('VCToolsVersion', '')
        deps['windows_sdk_version'] = os.environ.get('WindowsSDKVersion', '')
        # The CRT license lives with the installed toolchain; retain its terms
        # alongside vcpkg and fetched dependency notices.
        vc_install = os.environ.get('VCINSTALLDIR')
        if vc_install:
            for path in Path(vc_install).glob('**/license*.txt'):
                notice(path, 'msvc-' + path.name)
    else:
        raise RuntimeError('Unsupported target: ' + args.target)
    (notice_dir / 'index.json').write_text(json.dumps(notices, indent=2, sort_keys=True) + '\n', encoding='utf-8')
    (runtime / 'build-metadata.json').write_text(json.dumps(metadata, indent=2, sort_keys=True) + '\n', encoding='utf-8')
    print(json.dumps({'target': args.target, 'notices': len(notices),
                      'minimum_glibc': metadata.get('minimum_glibc', '')}))


if __name__ == '__main__':
    main()
