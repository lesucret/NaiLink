#!/usr/bin/env python3
"""Build the optional musl Naive/WireGuard/Tor bundle (x86_64 / ARM64).

No packages are installed on the router. Tor, its loader, libraries and GeoIP
files live in a private immutable directory. Every input is checksum pinned.
"""
import argparse
import hashlib
import io
import json
import os
from pathlib import Path
import shutil
import subprocess
import tarfile
import urllib.request

import build
import singbox

HERE = Path(__file__).resolve().parent
VERSION = '1.14.3-nailink.2'
TOR_VERSION = '0.4.9.13'
TAGS = 'with_quic,with_utls,with_clash_api,with_gvisor,with_naive_outbound,with_musl,with_wireguard'


def fetch(record, cache):
    path = cache / record['sha256']
    if not path.exists():
        with urllib.request.urlopen(record['url'], timeout=90) as response:
            data = response.read(64 << 20)
        if hashlib.sha256(data).hexdigest() != record['sha256']:
            raise ValueError('Input checksum mismatch: ' + record['name'])
        path.write_bytes(data)
    if build.sha256(path) != record['sha256']:
        raise ValueError('Cached input checksum mismatch')
    return path


def package_files(path):
    # OpenWrt 24.10 IPKs use a gzip tar outer container, never execute scripts.
    with tarfile.open(path) as outer:
        name = next(n for n in outer.getnames() if n.endswith('data.tar.gz'))
        with tarfile.open(fileobj=io.BytesIO(outer.extractfile(name).read())) as data:
            result, links = {}, {}
            for item in data.getmembers():
                name = item.name.removeprefix('./')
                if name.startswith('/') or '..' in Path(name).parts:
                    raise ValueError('Unsafe package member')
                if item.isfile():
                    result[name] = data.extractfile(item).read()
                elif item.issym():
                    links[name] = item.linkname
            for name, target in links.items():
                resolved = (Path(name).parent / target).as_posix() if not target.startswith('/') else target[1:]
                if resolved in result:
                    result[name] = result[resolved]
            return result


def patch_tor(root):
    path = root / 'protocol/tor/outbound.go'
    text = path.read_text()
    lines = text.splitlines(True)
    removed = [line for line in lines if '.Trace(' in line and ('proxyUsername' in line or 'proxyPassword' in line)]
    if len(removed) != 2:
        raise ValueError('Unexpected Tor diagnostic implementation')
    path.write_text(''.join(line for line in lines if line not in removed), newline='\n')


def build_bundle(cpu, work, output, sdk_work, reuse=None):
    work.mkdir(parents=True, exist_ok=True)
    output.mkdir(parents=True, exist_ok=True)
    target = next(t for t in json.loads((HERE / 'sdk-matrix.json').read_text()) if t['name'] == 'ipk-' + cpu)
    sdk = build.prepare_sdk(target, sdk_work)
    toolchain = next((sdk / 'staging_dir').glob('toolchain-*'))
    compiler = next((toolchain / 'bin').glob('*-linux-musl-gcc'))
    lld = shutil.which('ld.lld-20') or shutil.which('ld.lld')
    if not lld:
        raise ValueError('LLVM LLD 20.1.2 linker is required for the pinned Cronet archive')
    lld = Path(os.path.abspath(lld))
    if 'LLD 20.1.2' not in subprocess.check_output([str(lld), '--version'], text=True):
        raise ValueError('Pinned Cronet uses CREL relocations; LLD 20.1.2 is required')
    linker_dir = work / 'linker'
    linker_dir.mkdir(exist_ok=True)
    (linker_dir / 'ld.lld').symlink_to(lld) if not (linker_dir / 'ld.lld').exists() else None
    root = singbox.source(work)
    patch_tor(root)
    binary = work / 'sing-box'
    if reuse:
        shutil.copy2(reuse, binary)
    else:
        env = {**os.environ, 'GOOS': 'linux', 'GOARCH': singbox.CPUS[cpu], 'CGO_ENABLED': '1', 'GOTOOLCHAIN': 'local',
               'STAGING_DIR': str(sdk / 'staging_dir'), 'CC': f'{compiler} -B{linker_dir}/ -fuse-ld=lld'}
        subprocess.run(['go', 'build', '-trimpath', '-buildvcs=false', '-tags', TAGS,
                        '-ldflags', '-s -w -buildid= -X github.com/sagernet/sing-box/constant.Version=' + VERSION,
                        '-o', str(binary), './cmd/sing-box'], cwd=root, env=env, check=True)
    build.verify_elf(binary, cpu)
    pins = json.loads((HERE / 'extra-sources.json').read_text())
    cache = work / 'downloads'
    cache.mkdir(exist_ok=True)
    entries = [('sing-box', binary.read_bytes(), 0o755), ('licenses/sing-box.txt', (root / 'LICENSE').read_bytes(), 0o644)]
    for record in pins['licenses']:
        raw = fetch(record, cache).read_bytes()
        if 'member' in record:
            with tarfile.open(fileobj=io.BytesIO(raw)) as archive:
                raw = archive.extractfile(record['member']).read()
        entries.append(('licenses/' + record['name'] + '.txt', raw, 0o644))
    # Keep copyright notices for every downloaded Go module in this fixed
    # dependency graph, including the statically linked Cronet bindings.
    modules = subprocess.check_output(['go', 'list', '-m', '-json', 'all'], cwd=root, text=True,
                                     env={**os.environ, 'GOTOOLCHAIN':'local'})
    linked = {line.split()[1] for line in subprocess.check_output(['go', 'version', '-m', str(binary)], text=True).splitlines() if line.strip().startswith('dep\t')}
    decoder, notices = json.JSONDecoder(), []
    while modules.strip():
        module, end = decoder.raw_decode(modules.lstrip()); modules = modules.lstrip()[end:]
        directory = module.get('Dir')
        if directory and module["Path"] in linked:
            for file in sorted(Path(directory).iterdir()):
                if file.is_file() and file.name.upper().startswith(('LICENSE', 'COPYING', 'NOTICE')):
                    notices.append((module['Path'] + ' ' + module.get('Version','') + ' / ' + file.name).encode() + b'\n' + file.read_bytes() + b'\n')
    entries.append(('licenses/GO-MODULES.txt', b'\n'.join(notices), 0o644))
    private = {}
    for record in pins['architectures'][cpu]:
        for name, data in package_files(fetch(record, cache)).items():
            if name in ('usr/bin/tor', 'usr/sbin/tor'):
                private['tor/bin/tor'] = data
            elif name.startswith(('usr/lib/', 'lib/')) and '.so' in Path(name).name and data.startswith(b'\x7fELF'):
                private['tor/lib/' + Path(name).name] = data
            elif name.startswith('usr/share/tor/') and Path(name).name in ('geoip', 'geoip6'):
                private['tor/share/' + Path(name).name] = data
    loader_arch = 'x86_64' if cpu == 'x86_64' else 'aarch64'
    libc = next(p for p in (toolchain / 'lib').glob('libc.so') if p.is_file())
    private['tor/lib/ld-musl-' + loader_arch + '.so.1'] = libc.read_bytes()
    private['tor/lib/libc.so'] = libc.read_bytes()
    for name in ('libgcc_s.so.1',):
        matches = list(toolchain.rglob(name))
        if matches:
            private['tor/lib/' + name] = matches[0].read_bytes()
    if not {'tor/bin/tor', 'tor/share/geoip', 'tor/share/geoip6'} <= private.keys():
        raise ValueError('Tor package payload is incomplete')
    for name, data in private.items():
        entries.append((name, data, 0o755 if name.startswith('tor/bin/') or name.startswith('tor/lib/') else 0o644))
    launcher = work / 'tor-launch'
    subprocess.run([str(compiler), '-Os', '-static', '-s', '-Wl,--build-id=none',
                    '-DLOADER_NAME="ld-musl-' + loader_arch + '.so.1"',
                    str(HERE / 'tor-launch.c'), '-o', str(launcher)], check=True,
                   env={**os.environ, 'STAGING_DIR':str(sdk / 'staging_dir')})
    build.verify_elf(launcher, cpu)
    entries.append(('tor-launch', launcher.read_bytes(), 0o755))
    metadata = {'version': VERSION, 'tor_version': TOR_VERSION, 'cpu': cpu, 'go': '1.26.0',
                'tags': TAGS, 'source': {'url': singbox.SOURCE_URL, 'sha256': singbox.SOURCE_SHA256},
                'sdk': target, 'tor_packages': pins['architectures'][cpu]}
    entries.append(('SOURCE.json', (json.dumps(metadata, indent=2) + '\n').encode(), 0o644))
    # License notices and exact corresponding-source pointers travel with the
    # binaries, including Cronet/Chromium and the OpenWrt source build system.
    notice = '''Optional NaiLink protocol components
sing-box: GPL-3.0-or-later; source and patches accompany this release.
Cronet: Chromium third-party licenses and GPL-3.0-or-later wrapper/static bundle:
https://github.com/SagerNet/cronet-go/tree/0d28acc44093
https://github.com/SagerNet/cronet-go/tree/c10c03c318db
https://github.com/SagerNet/naiveproxy/tree/72a06c9fca0e2d228588c7f3074bf7efff3ff686
Tor: BSD-3-Clause; https://dist.torproject.org/tor-0.4.9.13.tar.gz
OpenSSL: Apache-2.0; https://www.openssl.org/source/openssl-3.0.22.tar.gz
libevent: BSD-3-Clause; https://github.com/libevent/libevent/releases/tag/release-2.1.12-stable
zlib: Zlib; https://zlib.net/fossils/zlib-1.3.1.tar.gz
libcap: BSD-3-Clause or GPL-2.0-only; https://www.kernel.org/pub/linux/libs/security/linux-privs/libcap2/libcap-2.69.tar.xz
musl: MIT; source and license in the checksum-pinned OpenWrt SDK.
libgcc: GPL-3.0 with GCC Runtime Library Exception; source in that SDK.
OpenWrt package build recipes and corresponding source retrieval:
https://github.com/openwrt/packages (Tor/libevent); https://github.com/openwrt/openwrt (base libraries).
Exact package versions, download URLs and hashes are in SOURCE.json.
Build: install Go 1.26.0, LLVM LLD 20.1.2 and zstd, then run NAILINK_EXTRA_BUILD.py.
'''
    entries.append(('licenses/NOTICE.txt', notice.encode(), 0o644))
    manifest = {name: {'sha256': hashlib.sha256(data).hexdigest(), 'size': len(data), 'mode': mode} for name, data, mode in entries}
    entries.append(('manifest.json', (json.dumps({'version': VERSION, 'cpu': cpu, 'files': manifest}, sort_keys=True) + '\n').encode(), 0o644))
    name = f'nailink-sing-box-extra-{VERSION}-{cpu}.tar.gz'
    (output / name).write_bytes(singbox.archive_bytes(entries))
    source_entries = [(str(p.relative_to(root)), p.read_bytes(), 0o644) for p in sorted(root.rglob('*')) if p.is_file()]
    source_entries += [('NAILINK_EXTRA_BUILD.py', Path(__file__).read_bytes(), 0o644), ('extra-sources.json', (HERE / 'extra-sources.json').read_bytes(), 0o644),
                       ('singbox.py', (HERE / 'singbox.py').read_bytes(), 0o644), ('build.py', (HERE / 'build.py').read_bytes(), 0o644),
                       ('sdk-matrix.json', (HERE / 'sdk-matrix.json').read_bytes(), 0o644),
                       ('tor-launch.c', (HERE / 'tor-launch.c').read_bytes(), 0o644)]
    (output / f'nailink-sing-box-extra-{VERSION}-source.tar.gz').write_bytes(singbox.archive_bytes(source_entries))
    print(json.dumps({'file': name, 'sha256': build.sha256(output / name), 'bytes': (output / name).stat().st_size}))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--cpu', choices=singbox.CPUS, required=True)
    parser.add_argument('--work', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--sdk-work', type=Path, required=True)
    parser.add_argument("--verify-pins", action="store_true")
    args = parser.parse_args()
    if ' go1.26.0 ' not in subprocess.check_output(['go', 'version'], text=True):
        raise ValueError('Go 1.26.0 is required')
    build_bundle(args.cpu, args.work.resolve(), args.output.resolve(), args.sdk_work.resolve())
    if args.verify_pins:
        name = f'nailink-sing-box-extra-{VERSION}-{args.cpu}.tar.gz'
        digest = build.sha256(args.output / name)
        if '"' + digest + '"' not in (HERE / 'corelib/extra.go').read_text():
            raise ValueError('Rebuilt extension differs from the reviewed installer checksum')


if __name__ == '__main__':
    main()
