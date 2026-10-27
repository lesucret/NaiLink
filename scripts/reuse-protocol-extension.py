#!/usr/bin/env python3
"""Reuse reviewed, unchanged optional components; never trust rolling feeds."""
import argparse
import hashlib
from pathlib import Path
import tarfile
import urllib.request

ROOT = Path(__file__).resolve().parents[1]
PREFIX = "nailink-sing-box-extra-1.14.3-nailink.2-"
BASE = "https://github.com/slobys/NaiLink/releases/download/openwrt-v0.6.39/"
PINS = {
    "x86_64": "315233fbd665ec15c7fb5af0454d79a368d9dd3b9045da73c23d8d06cdf7809f",
    "arm64": "79c80a537dccb4bf9009a3851ab9af2bd34ef24619bd04a1cf2fd7228085cf76",
    "source": "50a6fe3c682cc486aa5a480bc2d088c311fdb3558a5b896a0d17b862395a7051",
}
SOURCES = {
    "NAILINK_EXTRA_BUILD.py": "extra.py", "extra-sources.json": "extra-sources.json",
    "singbox.py": "singbox.py", "build.py": "build.py",
    "sdk-matrix.json": "sdk-matrix.json", "tor-launch.c": "tor-launch.c",
}


def fetch(suffix, output):
    name = PREFIX + suffix + ".tar.gz"
    path = output / name
    if path.exists():
        data = path.read_bytes()
    else:
        with urllib.request.urlopen(BASE + name, timeout=90) as response:
            data = response.read((96 << 20) + 1)
    if len(data) > 96 << 20 or hashlib.sha256(data).hexdigest() != PINS[suffix]:
        raise ValueError("Reviewed extension checksum mismatch: " + suffix)
    if not path.exists():
        temporary = path.with_suffix(".download")
        temporary.write_bytes(data)
        temporary.replace(path)
    return path


def check_source(path, root=ROOT):
    # A changed source/build input requires a new reviewed build and version.
    # No silently stale binary may accompany modified extension sources.
    with tarfile.open(path) as archive:
        for member, source in SOURCES.items():
            if archive.extractfile(member).read() != (root / "openwrt" / source).read_bytes():
                raise ValueError("Extension source changed; rebuild and review: " + source)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cpu", choices=("x86_64", "arm64"), required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    check_source(fetch("source", args.output))
    digest = PINS[args.cpu]
    if '"' + digest + '"' not in (ROOT / "openwrt/corelib/extra.go").read_text():
        raise ValueError("Reviewed extension differs from installer pin")
    fetch(args.cpu, args.output)
    print("Unchanged extension and corresponding source verified: " + args.cpu)


if __name__ == "__main__":
    main()
