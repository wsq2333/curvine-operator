#!/usr/bin/env python3
"""Claim only empty prepared directories; never format, erase, or adopt data."""
import json
import os
import pathlib
import re
import sys

MARKER = '.curvine-operator-owner.json'


def claim(paths, identity):
    # Validate the entire set before creating any markers. Completed markers
    # are idempotent; an interrupted/corrupt marker requires manual inspection.
    pending = []
    for value in paths:
        root = pathlib.Path(value)
        if not root.is_absolute() or root.resolve() != root or not root.is_dir():
            raise ValueError('unsafe directory')
        marker = root / MARKER
        if marker.is_symlink():
            raise ValueError('unsafe marker')
        if marker.exists():
            if not marker.is_file() or marker.stat().st_size > 4096:
                raise ValueError('invalid marker')
            if json.loads(marker.read_text()) != identity:
                raise ValueError('foreign disk identity')
        else:
            if any(child.name != 'lost+found' for child in root.iterdir()):
                raise ValueError('unclaimed nonempty disk')
            pending.append(marker)
    for marker in pending:
        data = (json.dumps(identity, sort_keys=True) + '\n').encode()
        descriptor = os.open(marker, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        with os.fdopen(descriptor, 'wb') as file:
            file.write(data)
            file.flush()
            os.fsync(file.fileno())
        descriptor = os.open(marker.parent, os.O_RDONLY | os.O_DIRECTORY)
        try:
            os.fsync(descriptor)
        finally:
            os.close(descriptor)


def main():
    identity = {'expansionUID': sys.argv[1], 'clusterID': sys.argv[2]}
    paths = json.loads(sys.argv[3])
    if not 1 <= len(paths) <= 16 or len(set(paths)) != len(paths):
        raise ValueError('invalid disk set')
    mounts = {}
    for line in pathlib.Path('/proc/self/mountinfo').read_text().splitlines():
        left, separator, right = line.partition(' - ')
        fields = left.split()
        if separator and len(fields) >= 6:
            mounts[fields[4]] = fields[5].split(',')
    devices = set()
    for value in paths:
        root = pathlib.Path(value)
        device = root.stat().st_dev
        if value not in mounts or 'rw' not in mounts[value] or device == pathlib.Path('/').stat().st_dev:
            raise ValueError('not a dedicated writable mount')
        real = pathlib.Path(f'/sys/dev/block/{os.major(device)}:{os.minor(device)}').resolve()
        match = re.fullmatch(r'(nvme[0-9]+n[0-9]+)(?:p[0-9]+)?', real.name)
        if not match or match[1] in devices:
            raise ValueError('NVMe device collision or non-NVMe disk')
        devices.add(match[1])
    # The pinned source NVMe/mount guard runs before this identity guard.
    claim(paths, identity)


if __name__ == '__main__':
    try:
        main()
    except Exception:
        print('Local disk identity validation failed; manual inspection required', file=sys.stderr)
        raise SystemExit(1)
