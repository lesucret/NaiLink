#!/usr/bin/env python3
"""Package the optional, static upstream Mihomo core without installing services."""
import argparse
import gzip
import hashlib
import io
import json
from pathlib import Path
import tarfile
import urllib.request

import build
from singbox import archive_bytes

VERSION = '1.19.33'
COMMIT = '3f27ef39f8c96bbfbc8b0b9bb10f5c9ef60149d8'
SOURCE_URL = 'https://codeload.github.com/MetaCubeX/mihomo/tar.gz/' + COMMIT
SOURCE_SHA = 'ecda5f8c8a8a1d60bc088c2640c85d92c8f002c0686234a26bdf671735afe216'
CPUS = {
    'x86_64': ('amd64-compatible', '386508bfb1c72c43b6a0eda73708ecdce82546581b0ebcb16366c53757c622b6'),
    'arm64': ('arm64', 'ba408b317080ee265c46cb923a50018c1b82cbd708551cdd46a1e3e307396187'),
}


def download(url, path, digest):
    if not path.exists():
        with urllib.request.urlopen(url, timeout=90) as response:
            path.write_bytes(response.read(80 << 20))
    if build.sha256(path) != digest:
        raise ValueError('Mihomo input checksum mismatch')
    return path.read_bytes()


def package(cpu, work, output):
    work.mkdir(parents=True, exist_ok=True)
    output.mkdir(parents=True, exist_ok=True)
    architecture, digest = CPUS[cpu]
    name = f'mihomo-linux-{architecture}-v{VERSION}.gz'
    url = f'https://github.com/MetaCubeX/mihomo/releases/download/v{VERSION}/' + name
    binary = gzip.decompress(download(url, work / name, digest))
    executable = work / 'mihomo'
    executable.write_bytes(binary)
    build.verify_elf(executable, cpu)
    source = download(SOURCE_URL, work / 'source.tar.gz', SOURCE_SHA)
    entries = []
    with tarfile.open(fileobj=io.BytesIO(source)) as archive:
        for item in archive.getmembers():
            if not item.isfile():
                continue
            path = item.name.split('/', 1)[1]
            if path.startswith('/') or '..' in Path(path).parts:
                raise ValueError('Unsafe source member')
            entries.append((path, archive.extractfile(item).read(), item.mode & 0o777))
    license_data = next(data for name, data, _ in entries if name == 'LICENSE')
    metadata = (json.dumps({'version': VERSION, 'commit': COMMIT, 'cpu': cpu,
                           'binary_url': url, 'binary_gzip_sha256': digest,
                           'source_url': SOURCE_URL, 'source_sha256': SOURCE_SHA}, indent=2) + '\n').encode()
    data = archive_bytes([('./usr/bin/mihomo', binary, 0o755)])
    name = f'nailink-mihomo-{VERSION}-{cpu}.tar.gz'
    (output / name).write_bytes(archive_bytes([('LICENSE', license_data, 0o644), ('SOURCE.json', metadata, 0o644), ('data.tar.gz', data, 0o644)]))
    common = (json.dumps({'version': VERSION, 'commit': COMMIT, 'source_url': SOURCE_URL,
                         'source_sha256': SOURCE_SHA, 'upstream': 'https://github.com/MetaCubeX/mihomo'}, indent=2) + '\n').encode()
    entries += [('NAILINK_SOURCE.json', common, 0o644), ('NAILINK_BUILD.py', Path(__file__).read_bytes(), 0o644),
                ('build.py', Path(__file__).with_name('build.py').read_bytes(), 0o644),
                ('singbox.py', Path(__file__).with_name('singbox.py').read_bytes(), 0o644)]
    (output / f'nailink-mihomo-{VERSION}-source.tar.gz').write_bytes(archive_bytes(entries))
    print(json.dumps({'file': name, 'sha256': build.sha256(output / name)}))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--cpu', choices=CPUS, required=True)
    parser.add_argument('--work', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--verify-pins', action='store_true')
    args = parser.parse_args()
    package(args.cpu, args.work.resolve(), args.output.resolve())
    if args.verify_pins:
        digest = build.sha256(args.output / f'nailink-mihomo-{VERSION}-{args.cpu}.tar.gz')
        if '"' + digest + '"' not in Path(__file__).with_name('corelib').joinpath('mihomo.go').read_text():
            raise ValueError('Mihomo package differs from reviewed checksum')
